package levyan

import (
	"context"
	"time"
	"uuid"

	"github.com/lesomnus/payday/pdid"

	"github.com/lesomnus/loft/internal/ent"
	"github.com/lesomnus/loft/internal/ent/aliashold"
	"github.com/lesomnus/loft/internal/ent/event"
	"github.com/lesomnus/loft/internal/ent/levyannotice"
	enttenant "github.com/lesomnus/loft/internal/ent/tenant"
)

// ended is how many notices of what became of the workspaces the first pass
// wrote ([Teller.ends]).
type ended struct {
	closed, restored, destroyed int
}

func (e ended) total() int { return e.closed + e.restored + e.destroyed }

// ends writes what became of the workspaces before this deployment told
// levyan of it -- closed, restored, destroyed -- and answers how much of it
// ([Teller.Backfill]), so that levyan does not go on billing a workspace that
// was closed before it was told it could be. Nothing told already, or waiting
// to be, is written again: a notice of the same workspace, kind and moment is
// that one, and a workspace's destruction is told once.
//
//   - **The closes and restores the workspaces' events keep**, oldest first,
//     each of its own moment: once, since they are read off every event there
//     is, the mark [Past] written after them. What a workspace's window took
//     of its history is gone, and what it said with it.
//   - **Every workspace closed now**, from when it says it was
//     (`Tenant.date_closed`): what its events no longer say, and one closed
//     while this deployment told levyan nothing. Every pass that starts, as
//     the arrivals are.
//   - **Every workspace destroyed here that levyan was told arrived**, from
//     when its address was held, which was after its final reading: told
//     nothing, levyan goes on taking somebody to pay for it, who would have
//     one free workspace fewer for good. One levyan was never told arrived
//     has no payer there. One that is here again -- put back from its export
//     under its identifiers -- is not destroyed now, and is not told it was:
//     what levyan is to make of that is its open question (its design 17,
//     104). Every pass that starts.
func (t *Teller) ends(ctx context.Context) (ended, error) {
	var out ended

	// What is told already, or waits to be.
	type said struct {
		workspace uuid.UUID
		kind      string
		at        time.Time
	}
	vs, err := t.Db.LevyanNotice.Query().
		Where(levyannotice.KindIn(Closed, Restored, Destroyed)).
		Select(levyannotice.FieldWorkspace, levyannotice.FieldKind, levyannotice.FieldAt).
		All(ctx)
	if err != nil {
		return out, err
	}
	have := map[said]bool{}
	destroyed := map[uuid.UUID]bool{}
	for _, v := range vs {
		have[said{v.Workspace, v.Kind, moment(v.At)}] = true
		if v.Kind == Destroyed {
			destroyed[v.Workspace] = true
		}
	}

	notices := &Notices{Now: t.Now}
	creates := []*ent.LevyanNoticeCreate{}
	add := func(kind string, workspace uuid.UUID, at time.Time) bool {
		k := said{workspace, kind, moment(at)}
		if have[k] {
			return false
		}
		have[k] = true
		creates = append(creates, notices.create(t.Db, kind, pdid.Id(workspace), pdid.Nil, at, ByBackfill))
		return true
	}

	// The closes and restores, as the events say them: read once.
	past, err := t.Db.LevyanNotice.Query().Where(levyannotice.KindEQ(Past)).Exist(ctx)
	if err != nil {
		return out, err
	}
	if !past {
		evs, err := t.Db.Event.Query().
			Where(event.KindIn(closeKind, restoreKind)).
			Order(ent.Asc(event.FieldOccurredAt), ent.Asc(event.FieldId)).
			Select(event.FieldId, event.FieldKind, event.FieldOccurredAt, event.FieldTenantId).
			All(ctx)
		if err != nil {
			return out, err
		}
		for _, e := range evs {
			switch e.Kind {
			case closeKind:
				if add(Closed, e.TenantId, e.OccurredAt) {
					out.closed++
				}
			case restoreKind:
				if add(Restored, e.TenantId, e.OccurredAt) {
					out.restored++
				}
			}
		}
	}

	// Closed now, from when it says it was.
	closed, err := t.Db.Tenant.Query().
		Where(enttenant.DateClosedNotNil()).
		Order(ent.Asc(enttenant.FieldDateClosed), ent.Asc(enttenant.FieldId)).
		Select(enttenant.FieldId, enttenant.FieldDateClosed).
		All(ctx)
	if err != nil {
		return out, err
	}
	for _, v := range closed {
		if add(Closed, v.Id, *v.DateClosed) {
			out.closed++
		}
	}

	// Destroyed, from when its address was held: the first hold of it that
	// says it went, whether or not an operator let the address go since.
	holds, err := t.Db.AliasHold.Query().
		Where(aliashold.ReasonIn(gone...)).
		Order(ent.Asc(aliashold.FieldDateCreated), ent.Asc(aliashold.FieldId)).
		All(ctx)
	if err != nil {
		return out, err
	}
	when := map[uuid.UUID]time.Time{}
	ids := []uuid.UUID{}
	for _, h := range holds {
		if _, ok := when[h.Tenant]; ok || destroyed[h.Tenant] {
			continue
		}
		when[h.Tenant] = h.DateCreated
		ids = append(ids, h.Tenant)
	}
	for len(ids) > 0 {
		page := ids[:min(len(ids), tenantsPage)]
		ids = ids[len(page):]

		here, err := t.Db.Tenant.Query().Where(enttenant.IdIn(page...)).Ids(ctx)
		if err != nil {
			return out, err
		}
		arrived, err := t.Db.LevyanNotice.Query().
			Where(levyannotice.KindEQ(Arrived), levyannotice.RefusedEQ(""), levyannotice.WorkspaceIn(page...)).
			Select(levyannotice.FieldWorkspace).
			All(ctx)
		if err != nil {
			return out, err
		}
		known := map[uuid.UUID]bool{}
		for _, v := range arrived {
			known[v.Workspace] = true
		}
		for _, id := range here {
			known[id] = false
		}
		for _, id := range page {
			if known[id] && add(Destroyed, id, when[id]) {
				out.destroyed++
			}
		}
	}

	for len(creates) > 0 {
		page := creates[:min(len(creates), writesPage)]
		creates = creates[len(page):]
		if err := t.Db.LevyanNotice.CreateBulk(page...).Exec(ctx); err != nil {
			return out, err
		}
	}

	// And the mark that the events were read, after what they said.
	if !past {
		now := t.now()
		if err := notices.create(t.Db, Past, pdid.Nil, pdid.Nil, now, ByBackfill).SetDateTold(now).Exec(ctx); err != nil {
			return out, err
		}
	}
	return out, nil
}
