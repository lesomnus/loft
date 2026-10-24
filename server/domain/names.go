package domain

import (
	"context"
	"fmt"
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
