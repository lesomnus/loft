package domain

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"uuid"

	"github.com/lesomnus/payday/access"
	"github.com/lesomnus/payday/past"
	"github.com/lesomnus/payday/pdid"

	"github.com/lesomnus/rove/internal/ent"
	"github.com/lesomnus/rove/internal/ent/asset"
	"github.com/lesomnus/rove/internal/ent/assettype"
	"github.com/lesomnus/rove/internal/ent/holder"
	"github.com/lesomnus/rove/internal/ent/itemmodel"
	"github.com/lesomnus/rove/internal/ent/party"
	"github.com/lesomnus/rove/server/pd"
)

// namer is what an answer names people and things by, as they are called
// when it is read: one for each answer, which reads each name once however
// often the answer says it, off a row of the caller's tenant.
//
// A name that is somebody's personal data -- a person record's, a
// membership's -- is shown with whose it is. What an answer says of it puts
// them on the access record of its call (rove#136), since no `personal`
// declaration can see a name in a sentence; [namer.Name] is the same reading
// as payday's past asks it (`past.Names`, payday#151), which says whose it is
// and leaves the record to whoever says it.
type namer struct {
	t *Tx

	// What it read of each kind of row, by the row: an asset by its tag and
	// name, a person record by its name, a membership as whoever did
	// something is named and by the name it signs in with, and a type and a
	// model by theirs.
	assets, parties, holders, members, kinds, models map[uuid.UUID]called

	// listed is each person record as a list of people names it, told apart
	// from whoever else goes by its name ([namer.readApart]).
	listed map[uuid.UUID]called

	// labels is what each asset's type calls its attributes, by the asset:
	// what a sentence that names one of them says ([namer.attribute]).
	labels map[uuid.UUID]map[string]string

	// legacies is what each row from before Rove's events kept a message
	// said, by the row: its sentence and what it named ([Sentences]).
	legacies map[uuid.UUID]legacyRow
}

var _ past.Names = (*namer)(nil)

// called is what one row is called, as a [namer] read it: whether there was a
// row to read it off, the name, whose personal data saying it shows, and what
// kept it from being read.
type called struct {
	found bool
	name  string
	saw   []access.Subject
	err   error
}

// Name is what the row `id` is called now, and whose data saying it shows, by
// the kind of row it is: a person record by its name, a membership as the
// history names whoever did something ([namer.actor]), an asset, a type and a
// model by theirs, which are nobody's. Empty, and no error, for a row of any
// other kind and for one the tenant does not have; an error for one that
// could not be read.
func (n *namer) Name(ctx context.Context, id pdid.Id) (string, []access.Subject, error) {
	var v called
	u := id.Uuid()
	switch id.Domain() {
	case pd.PartyDomain:
		v = n.look(ctx, &n.parties, u, n.ofParty)
	case pd.HolderDomain:
		v = n.look(ctx, &n.holders, u, n.ofHolder)
	case pd.AssetDomain:
		v = n.look(ctx, &n.assets, u, n.ofAsset)
	case pd.AssetTypeDomain:
		v = n.look(ctx, &n.kinds, u, n.ofKind)
	case pd.ItemModelDomain:
		v = n.look(ctx, &n.models, u, n.ofModel)
	}
	if v.err != nil {
		return "", nil, v.err
	}
	return v.name, v.saw, nil
}

// look is what `read` says the row `id` is called, read once.
func (n *namer) look(ctx context.Context, m *map[uuid.UUID]called, id uuid.UUID, read func(context.Context, uuid.UUID) called) called {
	if *m == nil {
		*m = map[uuid.UUID]called{}
	}
	if v, ok := (*m)[id]; ok {
		return v
	}
	v := read(ctx, id)
	(*m)[id] = v
	return v
}

// show is a name as an answer of this layer says it: "?" for a row nothing was
// read off -- none, or one that could not be read -- and, for a name that is
// somebody's, whose it is on the access record of the call that shows it.
func (n *namer) show(v called) string {
	if !v.found || v.err != nil {
		return "?"
	}
	access.Saw(n.t.ctx, v.saw...)
	return v.name
}

// missing is a row a [namer] read nothing off: none for one that is not
// there, and what kept it from being read for any other.
func missing(err error) called {
	if ent.IsNotFound(err) {
		return called{}
	}
	return called{err: err}
}

// whose is the row `id` of the tenant, as the access record names whose data
// a name read off it is.
func (n *namer) whose(id uuid.UUID) []access.Subject {
	return []access.Subject{{Id: pdid.Id(id), Tenant: n.t.tenant}}
}

// readApart reads at once the person records an answer lists -- who has what
// is out, who a booking is for, who has each asset of the register -- so that
// naming each in it ([namer.apart]) reads none of them again, a few hundred
// to a statement. A list names each by its name, and, where another record
// of the workspace goes by the same name, with what tells them apart and is
// never how either is reached (D50, D54). That is what the page tells them
// apart by where it lists them (`PartySelect`, the people screen): the team
// the record is in; then, where another of the name is in a team of that
// name too, its 사번 (`code`); and, where neither tells them apart, its
// identifier. A team's name is as personal as a person's, so saying it shows
// the team too.
//
// What it reads is the records it is asked for, every other that goes by one
// of their names -- whoever a reader might take one of them for -- and the
// teams of the records that share a name.
func (n *namer) readApart(ids []uuid.UUID) error {
	if n.listed == nil {
		n.listed = map[uuid.UUID]called{}
	}
	tid := n.t.tenant.Uuid()
	need := []uuid.UUID{}
	for _, id := range slices.Compact(sortedIds(ids)) {
		if _, ok := n.listed[id]; ok {
			continue
		}
		// None, unless it is read below.
		n.listed[id] = called{}
		need = append(need, id)
	}
	names := []string{}
	for some := range slices.Chunk(need, 500) {
		ps, err := n.t.db.Party.Query().
			Where(party.TenantId(tid), party.IdIn(some...)).
			Select(party.FieldId, party.FieldName).
			All(n.t.ctx)
		if err != nil {
			return err
		}
		for _, p := range ps {
			names = append(names, p.Name)
		}
	}
	slices.Sort(names)
	byName := map[string][]*ent.Party{}
	for some := range slices.Chunk(slices.Compact(names), 500) {
		ps, err := n.t.db.Party.Query().
			Where(party.TenantId(tid), party.NameIn(some...)).
			Select(party.FieldId, party.FieldName, party.FieldParentId, party.FieldCode).
			All(n.t.ctx)
		if err != nil {
			return err
		}
		for _, p := range ps {
			byName[p.Name] = append(byName[p.Name], p)
		}
	}

	// The team a record is in, by a name: none for one in no team, or in one
	// that is not there or has none. Only a record that shares its name is
	// told apart by its team, so only theirs are read.
	ups := []uuid.UUID{}
	for _, same := range byName {
		for _, p := range same {
			if len(same) > 1 && p.ParentId != nil {
				ups = append(ups, *p.ParentId)
			}
		}
	}
	teams := map[uuid.UUID]*ent.Party{}
	for some := range slices.Chunk(slices.Compact(sortedIds(ups)), 500) {
		ps, err := n.t.db.Party.Query().
			Where(party.TenantId(tid), party.IdIn(some...)).
			Select(party.FieldId, party.FieldName).
			All(n.t.ctx)
		if err != nil {
			return err
		}
		for _, p := range ps {
			if p.Name != "" {
				teams[p.Id] = p
			}
		}
	}
	team := func(p *ent.Party) *ent.Party {
		if p.ParentId == nil {
			return nil
		}
		return teams[*p.ParentId]
	}
	teamName := func(p *ent.Party) string {
		if v := team(p); v != nil {
			return v.Name
		}
		return ""
	}

	// Every record read is called as the list calls it: a namesake read to
	// tell the others apart is told apart from them as well, by the same
	// records.
	for _, same := range byName {
		for _, p := range same {
			v := called{found: true, name: p.Name, saw: n.whose(p.Id)}
			if len(same) > 1 {
				words := []string{p.Name}
				if tm := team(p); tm != nil {
					words = append(words, tm.Name)
					v.saw = append(v.saw, n.whose(tm.Id)...)
				}
				alike, coded := 0, 0
				for _, q := range same {
					if teamName(q) == teamName(p) {
						alike++
						if q.Code == p.Code {
							coded++
						}
					}
				}
				switch {
				case alike == 1:
					// The team tells them apart.
				case p.Code != "" && coded == 1:
					words = append(words, p.Code)
				default:
					words = append(words, pdid.Id(p.Id).String())
				}
				v.name = strings.Join(words, " · ")
			}
			n.listed[p.Id] = v
		}
	}
	return nil
}

// apart is the person record `id` as a list of people names it
// ([namer.readApart]): read by itself, with whoever goes by its name, when
// the answer did not read it with the rest.
func (n *namer) apart(id uuid.UUID) string {
	if _, ok := n.listed[id]; !ok {
		if err := n.readApart([]uuid.UUID{id}); err != nil {
			n.listed[id] = called{err: err}
		}
	}
	return n.show(n.listed[id])
}

func (n *namer) asset(id uuid.UUID) string { return n.show(n.look(n.t.ctx, &n.assets, id, n.ofAsset)) }
func (n *namer) party(id uuid.UUID) string { return n.show(n.look(n.t.ctx, &n.parties, id, n.ofParty)) }
func (n *namer) kind(id uuid.UUID) string  { return n.show(n.look(n.t.ctx, &n.kinds, id, n.ofKind)) }
func (n *namer) model(id uuid.UUID) string { return n.show(n.look(n.t.ctx, &n.models, id, n.ofModel)) }

// actor is whoever did something, as the history names them: by the person
// record of the membership, or the membership's own name when it is no
// person record's. The deployment's own work is nobody's.
func (n *namer) actor(id *uuid.UUID) string {
	if id == nil {
		return "시스템"
	}
	return n.show(n.look(n.t.ctx, &n.holders, *id, n.ofHolder))
}

// actorOf is [namer.actor] of an identifier, nobody's for none.
func (n *namer) actorOf(id pdid.Id) string {
	if id.IsZero() {
		return n.actor(nil)
	}
	u := id.Uuid()
	return n.actor(&u)
}

// member is a membership by its own name: the one it signs in with.
func (n *namer) member(id uuid.UUID) string {
	return n.show(n.look(n.t.ctx, &n.members, id, n.ofMember))
}

func (n *namer) ofAsset(ctx context.Context, id uuid.UUID) called {
	a, err := n.t.db.Asset.Query().Where(asset.Id(id), asset.TenantId(n.t.tenant.Uuid())).Only(ctx)
	if err != nil {
		return missing(err)
	}
	return called{found: true, name: fmt.Sprintf("%s %s", a.Tag, a.Name)}
}

func (n *namer) ofParty(ctx context.Context, id uuid.UUID) called {
	p, err := n.t.db.Party.Query().Where(party.Id(id), party.TenantId(n.t.tenant.Uuid())).Only(ctx)
	if err != nil {
		return missing(err)
	}
	return called{found: true, name: p.Name, saw: n.whose(id)}
}

// ofHolder is a membership as [namer.actor] names it.
func (n *namer) ofHolder(ctx context.Context, id uuid.UUID) called {
	p, err := n.t.db.Party.Query().Where(party.TenantId(n.t.tenant.Uuid()), party.HolderId(id)).First(ctx)
	switch {
	case err == nil:
		return called{found: true, name: p.Name, saw: n.whose(p.Id)}
	case !ent.IsNotFound(err):
		return called{err: err}
	}
	return n.look(ctx, &n.members, id, n.ofMember)
}

// ofMember is a membership as [namer.member] names it.
func (n *namer) ofMember(ctx context.Context, id uuid.UUID) called {
	h, err := n.t.db.Holder.Query().Where(holder.Id(id), holder.TenantId(n.t.tenant.Uuid())).Only(ctx)
	if err != nil {
		return missing(err)
	}
	v := called{found: true, name: "@" + h.Alias, saw: n.whose(id)}
	if h.Alias == "" {
		// The membership of somebody forgotten names nobody, and goes by
		// the pseudonym it was left with (`endMembership`).
		v.name = h.Name
	}
	return v
}

func (n *namer) ofKind(ctx context.Context, id uuid.UUID) called {
	ty, err := n.t.db.AssetType.Query().Where(assettype.Id(id), assettype.TenantId(n.t.tenant.Uuid())).Only(ctx)
	if err != nil {
		return missing(err)
	}
	return called{found: true, name: ty.Name}
}

func (n *namer) ofModel(ctx context.Context, id uuid.UUID) called {
	m, err := n.t.db.ItemModel.Query().Where(itemmodel.Id(id), itemmodel.TenantId(n.t.tenant.Uuid())).Only(ctx)
	if err != nil {
		return missing(err)
	}
	return called{found: true, name: strings.TrimSpace(m.Maker + " " + m.Name)}
}

// attribute is what the type of the asset `id` calls its attribute `key` now:
// its label, or the key where the type gives it none, as a type gives an
// asset its attributes ([specOf]) -- its own first, then what it inherits.
// An asset with no type, and one the tenant does not have, names none. Each
// asset's are read once, off the rows of the caller's tenant: a type is
// nobody's.
func (n *namer) attribute(ctx context.Context, id uuid.UUID, key string) (string, error) {
	labels, ok := n.labels[id]
	if !ok {
		var err error
		if labels, err = n.ofLabels(ctx, id); err != nil {
			return key, err
		}
		if n.labels == nil {
			n.labels = map[uuid.UUID]map[string]string{}
		}
		n.labels[id] = labels
	}
	if v := labels[key]; v != "" {
		return v, nil
	}
	return key, nil
}

func (n *namer) ofLabels(ctx context.Context, id uuid.UUID) (map[string]string, error) {
	out := map[string]string{}
	a, err := n.t.db.Asset.Query().Where(asset.Id(id), asset.TenantId(n.t.tenant.Uuid())).Select(asset.FieldTypeId).Only(ctx)
	if ent.IsNotFound(err) {
		return out, nil
	}
	if err != nil {
		return nil, err
	}
	ty := a.TypeId
	for range 16 {
		if ty == (uuid.UUID{}) {
			break
		}
		v, err := n.t.db.AssetType.Query().Where(assettype.Id(ty), assettype.TenantId(n.t.tenant.Uuid())).Only(ctx)
		if ent.IsNotFound(err) {
			break
		}
		if err != nil {
			return nil, err
		}
		for _, d := range v.Spec.GetAttributes() {
			if _, ok := out[d.GetKey()]; !ok {
				out[d.GetKey()] = d.GetLabel()
			}
		}
		if v.ParentId == nil {
			break
		}
		ty = *v.ParentId
	}
	return out, nil
}

// An event from before Rove's events kept a message (payday#151) kept the
// sentence it was written with instead, `desc`, and named the people it was
// about by identifier in its `payload`, its sentence by a mark for each; the
// words are read with the event, so that one about somebody forgotten reads
// with the pseudonym they were left with, and one about somebody renamed with
// the name they have now (design 8.2). One written before marks named them in
// its words, and reads as written. Every event since is said from its message
// (`say.go`), and names people the same way.
//
// What it says of a thing -- a tag, a type, a reservation -- is what the thing
// was called then, and stays.
var marks = []struct {
	mark, key string
	say       func(*namer, uuid.UUID) string
}{
	// The person record.
	{"{party}", "party", (*namer).party},
	// The membership, as the history names whoever did something.
	{"{holder}", "holder", func(n *namer, id uuid.UUID) string { return n.actor(&id) }},
	// The membership by the name it signs in with.
	{"{alias}", "holder", (*namer).member},
}

// say is an event's sentence with the people it marks named as they are now,
// each on the access record of the call that shows it.
func (n *namer) say(desc, payload string) string {
	if !strings.Contains(desc, "{") {
		return desc
	}
	p := decode(payload)
	for _, m := range marks {
		if !strings.Contains(desc, m.mark) {
			continue
		}
		id, err := pdid.Parse(p[m.key])
		if err != nil {
			continue
		}
		desc = strings.ReplaceAll(desc, m.mark, m.say(n, id.Uuid()))
	}
	return desc
}
