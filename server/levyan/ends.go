package levyan

import (
	"context"
	"time"
	"uuid"

	"github.com/lesomnus/payday/pdid"

	"github.com/lesomnus/loft/internal/ent"
	"github.com/lesomnus/loft/internal/ent/event"
	"github.com/lesomnus/loft/internal/ent/levyannotice"
	enttenant "github.com/lesomnus/loft/internal/ent/tenant"
)

// ended is how many notices of what became of the workspaces the first pass
// wrote ([Teller.ends]).
type ended struct {
	closed, restored int
}

func (e ended) total() int { return e.closed + e.restored }

// ends writes what became of the workspaces before this deployment told
// levyan of it -- closed, restored -- and answers how much of it
// ([Teller.Backfill]), so that levyan does not go on billing a workspace that
// was closed before it was told it could be. Nothing told already, or waiting
// to be, is written again: a notice of the same workspace, kind and moment is
// that one.
//
//   - **The closes and restores the workspaces' events keep**, oldest first,
//     each of its own moment: once, since they are read off every event there
//     is, the mark [Past] written after them. What a workspace's window took
//     of its history is gone, and what it said with it.
//   - **Every workspace closed now**, from when it says it was
//     (`Tenant.date_closed`): what its events no longer say, and one closed
//     while this deployment told levyan nothing. Every pass that starts.
//
// A workspace destroyed is told as it is destroyed (`closing.Destroy`, design
// 7.8), and one destroyed while this deployment told levyan nothing is not
// told after: nothing of it is left here to say it was (D86) -- a deployment
// serves its one workspace, and one whose workspace is gone bills nobody.
func (t *Teller) ends(ctx context.Context) (ended, error) {
	var out ended

	// What is told already, or waits to be.
	type said struct {
		workspace uuid.UUID
		kind      string
		at        time.Time
	}
	vs, err := t.Db.LevyanNotice.Query().
		Where(levyannotice.KindIn(Closed, Restored)).
		Select(levyannotice.FieldWorkspace, levyannotice.FieldKind, levyannotice.FieldDateOccurred).
		All(ctx)
	if err != nil {
		return out, err
	}
	have := map[said]bool{}
	for _, v := range vs {
		have[said{v.Workspace, v.Kind, moment(v.DateOccurred)}] = true
	}

	notices := &Notices{Now: t.Now}
	creates := []*ent.LevyanNoticeCreate{}
	add := func(kind string, workspace uuid.UUID, at time.Time) bool {
		k := said{workspace, kind, moment(at)}
		if have[k] {
			return false
		}
		have[k] = true
		creates = append(creates, notices.create(t.Db, kind, pdid.Id(workspace), at, ByBackfill))
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
			Order(ent.Asc(event.FieldDateOccurred), ent.Asc(event.FieldId)).
			Select(event.FieldId, event.FieldKind, event.FieldDateOccurred, event.FieldTenantId).
			All(ctx)
		if err != nil {
			return out, err
		}
		for _, e := range evs {
			switch e.Kind {
			case closeKind:
				if add(Closed, e.TenantId, e.DateOccurred) {
					out.closed++
				}
			case restoreKind:
				if add(Restored, e.TenantId, e.DateOccurred) {
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
		if err := notices.create(t.Db, Past, pdid.Nil, now, ByBackfill).SetDateTold(now).Exec(ctx); err != nil {
			return out, err
		}
	}
	return out, nil
}
