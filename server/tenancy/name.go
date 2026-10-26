package tenancy

import (
	"context"
	"uuid"

	"github.com/lesomnus/payday/access"
	"github.com/lesomnus/payday/pdid"

	"github.com/lesomnus/rove/internal/ent"
	"github.com/lesomnus/rove/internal/ent/holder"
	"github.com/lesomnus/rove/internal/ent/party"
)

// What a workspace is called (D57).
//
// A workspace is called by its own name, which whoever made it chose -- the
// account that made it, or an operator (`rove init`) -- and which is nobody's:
// the name of a thing, which the trail keeps as a value as it keeps any
// thing's (design 8.2). The workspace an account is given when it arrives
// ([Workspaces.Arrive]) has none. What it would be called is a person's name,
// and written there it would be a person in a field about no one -- on the
// trail as a value, where nothing that forgets them reaches it. So it is said
// where it is read: "<이름>의 워크스페이스", after its first owner, named as
// the history names whoever did something -- by the person record of their
// membership, or the membership's own name. It is the name they go by now: a
// person renamed renames it, a workspace handed on is called after whoever
// has it, and one with no name and no owner is called by its address.

// Name is what a workspace is called where it is read, and whose personal data
// saying it shows.
type Name struct {
	// Said is what it is called: its own name, or after its owner.
	Said string
	// Saw is the owner it is called after -- their person record, or their
	// membership where they are no person record's -- and none for one
	// called by its own name or its address. Whoever shows it puts it on the
	// access record of the call that does (rove#136).
	Saw []access.Subject
}

// Names is what each of `ts` is called, by its identifier.
func Names(ctx context.Context, db *ent.Client, ts ...*ent.Tenant) (map[uuid.UUID]Name, error) {
	out := make(map[uuid.UUID]Name, len(ts))
	unnamed := []uuid.UUID{}
	for _, t := range ts {
		if t == nil {
			continue
		}
		if t.Name != "" {
			out[t.Id] = Name{Said: t.Name}
			continue
		}
		out[t.Id] = Name{Said: t.Alias}
		unnamed = append(unnamed, t.Id)
	}
	if len(unnamed) == 0 {
		return out, nil
	}

	// Each one's first owner: the one who has been in it longest.
	hs, err := db.Holder.Query().
		Where(holder.TenantIdIn(unnamed...), holder.Role("owner"), holder.DateErasedIsNil()).
		Order(ent.Asc(holder.FieldDateCreated), ent.Asc(holder.FieldId)).
		All(ctx)
	if err != nil {
		return nil, err
	}
	first := map[uuid.UUID]*ent.Holder{}
	ids := []uuid.UUID{}
	for _, h := range hs {
		if _, ok := first[h.TenantId]; !ok {
			first[h.TenantId] = h
			ids = append(ids, h.Id)
		}
	}
	if len(ids) == 0 {
		return out, nil
	}
	ps, err := db.Party.Query().
		Where(party.HolderIdIn(ids...), party.DateErasedIsNil()).
		Order(ent.Asc(party.FieldDateCreated), ent.Asc(party.FieldId)).
		All(ctx)
	if err != nil {
		return nil, err
	}
	records := map[uuid.UUID]*ent.Party{}
	for _, p := range ps {
		if _, ok := records[p.HolderId]; !ok && p.Name != "" {
			records[p.HolderId] = p
		}
	}

	for tid, h := range first {
		who, saw := or(h.Name, h.Alias), access.Subject{Id: pdid.Id(h.Id), Tenant: pdid.Id(h.TenantId)}
		if p, ok := records[h.Id]; ok {
			who, saw = p.Name, access.Subject{Id: pdid.Id(p.Id), Tenant: pdid.Id(p.TenantId)}
		}
		if who == "" {
			continue
		}
		out[tid] = Name{Said: who + "의 워크스페이스", Saw: []access.Subject{saw}}
	}

	return out, nil
}

// NameOf is what one workspace is called ([Names]).
func NameOf(ctx context.Context, db *ent.Client, t *ent.Tenant) (Name, error) {
	vs, err := Names(ctx, db, t)
	if err != nil {
		return Name{}, err
	}

	return vs[t.Id], nil
}
