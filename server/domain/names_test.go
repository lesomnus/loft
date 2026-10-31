package domain_test

import (
	"context"
	"testing"

	"github.com/lesomnus/payday/access"
	"github.com/lesomnus/payday/pdid"
	"github.com/lesomnus/z"

	app "github.com/lesomnus/loft"
	"github.com/lesomnus/loft/server/domain"
	"github.com/lesomnus/loft/server/pd"
)

// TestARowIsNamedAsThePastAsks: what an answer names a row by is what payday's
// past asks of an app's names (`past.Names`, payday#151) -- what it is called
// now, and whose data saying it shows -- by the kind of row it is. A person
// record by its name, and as theirs; a membership by its person record's, or
// by its own name when it is no person record's; a thing by its own, and as
// nobody's. A row that is not here, or is another workspace's, or of no kind
// a sentence names, is named by nothing.
func TestARowIsNamedAsThePastAsks(t *testing.T) {
	e := newEnv(t)
	x := e.x
	ctx := context.Background()

	names, err := domain.NamesFor(e.owner, e.app(), e.s.Drv, e.s.Deps)
	x.NoError(err)
	name := func(id []byte) (string, []access.Subject) {
		v, err := pdid.From(id)
		x.NoError(err)
		n, saw, err := names.Name(e.owner, v)
		x.NoError(err)
		return n, saw
	}
	whose := func(id []byte) []access.Subject {
		v, err := pdid.From(id)
		x.NoError(err)
		return []access.Subject{{Id: v, Tenant: e.tenant}}
	}

	_, kim := e.person("member")
	n, saw := name(kim.GetId())
	x.Equal(kim.GetName(), n)
	x.Equal(whose(kim.GetId()), saw)
	n, saw = name(kim.GetHolder().GetId())
	x.Equal(kim.GetName(), n, "a membership is named by its person record")
	x.Equal(whose(kim.GetId()), saw)

	// A membership that is no person record's goes by the name it signs in
	// with, and saying it shows the membership's.
	p, err := e.s.Identity.AddPerson(ctx, "bot", "Bot", pdid.Nil)
	x.NoError(err)
	bot, err := e.s.Base.Holder().Add(ctx, app.HolderAddRequest_builder{
		Tenant:  app.TenantRef_builder{Id: e.tenant.Bytes()}.Build(),
		Alias:   "bot",
		Name:    "Bot",
		Role:    z.Ptr("member"),
		Account: p.Id.Bytes(),
	}.Build())
	x.NoError(err)
	n, saw = name(bot.GetId())
	x.Equal("@bot", n)
	x.Equal(whose(bot.GetId()), saw)

	// Things, by their own, which are nobody's.
	room := e.space("회의실", nil)
	n, saw = name(room.GetId())
	x.Equal(room.GetTag()+" 회의실", n)
	x.Empty(saw)
	ty, err := e.app().AssetType().Add(e.owner, app.AssetTypeAddRequest_builder{Name: "노트북"}.Build())
	x.NoError(err)
	n, saw = name(ty.GetId())
	x.Equal("노트북", n)
	x.Empty(saw)
	m, err := e.app().ItemModel().Add(e.owner, app.ItemModelAddRequest_builder{Name: "XPS 13", Maker: "Dell"}.Build())
	x.NoError(err)
	n, saw = name(m.GetId())
	x.Equal("Dell XPS 13", n)
	x.Empty(saw)

	// Nothing: a row that is not here, another workspace's, and a kind no
	// sentence names -- the workspace itself.
	_, other := e.newTenant("other")
	theirs, err := e.app().Party().Add(other, app.PartyAddRequest_builder{Name: "남의 사람", Kind: "person"}.Build())
	x.NoError(err)
	for what, id := range map[string][]byte{
		"a person record that is not here":   pdid.New(pd.PartyDomain).Bytes(),
		"a membership that is not here":      pdid.New(pd.HolderDomain).Bytes(),
		"another workspace's person record":  theirs.GetId(),
		"another workspace's thing":          e.spaceIn(other, "남의 방").GetId(),
		"a kind of row no sentence names":    e.tenant.Bytes(),
		"an operation, which is no row here": pdid.New(pd.EventDomain).Bytes(),
	} {
		n, saw := name(id)
		x.Empty(n, what)
		x.Empty(saw, what)
	}
}

// spaceIn is a space of the workspace `ctx` is signed in to.
func (e *env) spaceIn(ctx context.Context, name string) *app.Asset {
	v, err := e.app().Asset().Add(ctx, app.AssetAddRequest_builder{Name: name, Kind: "space"}.Build())
	e.x.NoError(err)
	return v
}
