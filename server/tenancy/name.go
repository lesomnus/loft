package tenancy

import (
	"context"
	"strings"
	"unicode"
	"unicode/utf8"
	"uuid"

	"github.com/lesomnus/payday/access"
	"github.com/lesomnus/payday/pdid"

	"github.com/lesomnus/loft/internal/ent"
	"github.com/lesomnus/loft/internal/ent/holder"
	"github.com/lesomnus/loft/internal/ent/party"
)

// What a workspace is called (D57).
//
// A workspace is called by its own name, which whoever made it chose -- the
// account that made it, or an operator (`loft init`) -- and which is nobody's:
// the name of a thing, which payday's Tenant declares about no one
// (payday#180) and the trail keeps as a value, as it keeps any thing's
// (design 8.2). The workspace an account is given when it arrives
// ([Workspaces.Arrive]) has none. What it would be called is a person's name,
// and written there it would be a person in a field about no one -- on the
// trail as a value, where nothing that forgets them reaches it. So it is said
// where it is read: "<이름>의 워크스페이스", after its first owner, named as
// the history names whoever did something -- by the person record of their
// membership, or the membership's own name. It is the name they go by now: a
// person renamed renames it, a workspace handed on is called after whoever
// has it, and one with no name and no owner is called by its address.

// MaxName is how long a workspace's own name may be, in characters.
const MaxName = 80

// Fits says whether `v` may be what a workspace is called: one line -- it is
// the subject of the mails that name it -- of up to [MaxName] characters.
// Empty fits, and is none of its own.
func Fits(v string) bool {
	return utf8.RuneCountInString(v) <= MaxName && !strings.ContainsFunc(v, unicode.IsControl)
}

// Name is what a workspace is called where it is read, and whose personal data
// saying it shows.
type Name struct {
	// Said is what it is called: its own name, or after its owner.
	Said string
	// Saw is the owner it is called after -- their person record, or their
	// membership where they are no person record's -- and none for one
	// called by its own name or its address. Whoever shows it puts it on the
	// access record of the call that does (loft#136).
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
	first, err := FirstOwners(ctx, db, unnamed...)
	if err != nil {
		return nil, err
	}
	ids := []uuid.UUID{}
	for _, h := range first {
		ids = append(ids, h.Id)
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

// FirstOwners is each of `ts`'s first owner, by the workspace: of the
// memberships in it now whose role is owner, the one that has been in it
// longest. What a workspace with no name of its own is called after
// ([Names]), and who levyan is told pays for a workspace made before Loft
// told it of any, whose maker nothing recorded (`levyan.Teller`). A
// workspace with no owner now has none.
func FirstOwners(ctx context.Context, db *ent.Client, ts ...uuid.UUID) (map[uuid.UUID]*ent.Holder, error) {
	out := map[uuid.UUID]*ent.Holder{}
	if len(ts) == 0 {
		return out, nil
	}
	hs, err := db.Holder.Query().
		Where(holder.TenantIdIn(ts...), holder.Role("owner"), holder.DateErasedIsNil()).
		Order(ent.Asc(holder.FieldDateCreated), ent.Asc(holder.FieldId)).
		All(ctx)
	if err != nil {
		return nil, err
	}
	for _, h := range hs {
		if _, ok := out[h.TenantId]; !ok {
			out[h.TenantId] = h
		}
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
