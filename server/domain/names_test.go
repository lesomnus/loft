package domain_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/lesomnus/payday/access"
	"github.com/lesomnus/payday/pdid"
	"github.com/lesomnus/z"
	"google.golang.org/protobuf/types/known/timestamppb"

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

// TestTheHistoryTellsTwoOfANameApart (D80): history names each person as a
// list of people does (D54) -- by name, and where another of the workspace
// goes by it, with the team one is in -- wherever it is read: an asset's, the
// work log and a reservation's. Its sentences, what its events held, whoever
// did each and whom an asset's stewardship rows are of alike. One whose name
// is nobody else's is called by it alone. What tells two apart is shown, so
// it is on the access record of the call that shows it, beside the person.
func TestTheHistoryTellsTwoOfANameApart(t *testing.T) {
	e := newEnv(t)
	x := e.x
	n := 0
	party := func(name, kind string, in *app.Party) *app.Party {
		n++
		req := app.PartyAddRequest_builder{Name: name, Kind: kind, Email: fmt.Sprintf("p%d@acme.example", n)}.Build()
		if in != nil {
			req.SetParentId(in.GetId())
		}
		v, err := e.app().Party().Add(e.owner, req)
		x.NoError(err)
		return v
	}
	whose := func(ps ...*app.Party) []access.Subject {
		out := []access.Subject{}
		for _, p := range ps {
			out = append(out, access.Subject{Id: idOf(p.GetId()), Tenant: e.tenant})
		}
		return out
	}
	dev, sales, design := party("개발팀", "team", nil), party("영업팀", "team", nil), party("디자인팀", "team", nil)
	// Two of a name, one of whom lends things out, and one of a name of
	// her own.
	kim, other, lee := party("김민준", "person", dev), party("김민준", "person", sales), party("이서연", "person", design)
	signed, err := e.app().Party().Invite(e.owner, app.PartyInviteRequest_builder{
		Ref:      ref[app.PartyRef](other.GetId()),
		Alias:    "minjun",
		Role:     "manager",
		Password: "password1234",
	}.Build())
	x.NoError(err)
	lender := e.as(signed.GetHolder())

	store := e.space("창고", nil)
	laptop := e.item("노트북", "NB-1", store, day)
	_, err = e.app().Asset().Assign(e.owner, app.AssetAssignRequest_builder{Ref: assetRef(laptop), Role: "owner", Party: ref[app.PartyRef](lee.GetId())}.Build())
	x.NoError(err)
	_, err = e.app().Custody().Add(lender, app.CustodyAddRequest_builder{
		Party: ref[app.PartyRef](kim.GetId()),
		Lines: []*app.CustodyLineSpec{app.CustodyLineSpec_builder{Asset: assetRef(laptop)}.Build()},
	}.Build())
	x.NoError(err)

	// The asset's history, and the access record of the call that reads it.
	ctx, call := access.Into(e.owner, "test")
	v, err := e.app().Asset().Timeline(ctx, app.AssetTimelineRequest_builder{Ref: assetRef(laptop)}.Build())
	x.NoError(err)
	es := v.GetEntries()
	lent := e.happenedIn(es, "custody.issue")
	x.Equal("지급: 김민준 · 개발팀", lent.GetSummary())
	x.Equal("김민준 · 영업팀", lent.GetActor(), "whoever lent it")
	x.Equal("김민준 · 개발팀", lent.GetDetail()["party"], "and whom, as the event held it")
	owned := e.happenedIn(es, "asset.assign")
	x.Equal("NB-1 소유: 이서연", owned.GetSummary())
	x.Equal("Owner of acme", owned.GetActor())
	held := map[string]string{}
	for _, en := range es {
		if en.GetKind() == "stewardship" {
			held[en.GetSummary()] = en.GetOtherName()
		}
	}
	x.Equal(map[string]string{"보유: 김민준 · 개발팀": "김민준 · 개발팀", "소유: 이서연": "이서연"}, held)
	_, carried := call.Unsaid()
	for _, s := range whose(kim, dev, other, sales, lee) {
		x.Contains(carried, s)
	}
	x.NotContains(carried, whose(design)[0], "what tells nobody apart is not shown")

	// The work log, and its access record: the teams there are said too.
	ctx, call = access.Into(e.owner, "test")
	recent, err := e.app().Event().Recent(ctx, app.EventRecentRequest_builder{Size: 500}.Build())
	x.NoError(err)
	said := []string{}
	for _, ev := range recent.GetItems() {
		said = append(said, ev.GetDesc())
	}
	for _, s := range []string{
		"추가: 김민준 · 개발팀",
		"추가: 김민준 · 영업팀",
		"추가: 이서연",
		"추가: 개발팀",
		"로그인 발급: 김민준 · 영업팀 (@minjun)",
		"NB-1 소유: 이서연",
		"지급: 김민준 · 개발팀",
	} {
		x.Contains(said, s)
	}
	_, carried = call.Unsaid()
	for _, s := range whose(kim, dev, other, sales, lee, design) {
		x.Contains(carried, s)
	}

	// A reservation's: booked for one of them, and picked up by the other.
	camera := e.item("카메라", "CM-1", nil, day)
	e.bookable(camera, nil)
	r, err := e.reserve(e.owner, camera, e.tomorrow(9, 0), e.tomorrow(12, 0), func(r *app.ReservationAddRequest) {
		r.SetName("제품 촬영")
		r.SetParty(ref[app.PartyRef](kim.GetId()))
	})
	x.NoError(err)
	e.now = e.now.Add(time.Minute)
	_, err = e.app().Custody().Add(lender, app.CustodyAddRequest_builder{
		Party:         ref[app.PartyRef](kim.GetId()),
		DateDue:       timestamppb.New(e.tomorrow(12, 0)),
		ReservationId: r.GetId(),
		Lines:         []*app.CustodyLineSpec{app.CustodyLineSpec_builder{Asset: assetRef(camera)}.Build()},
	}.Build())
	x.NoError(err)
	rs := e.reservationTimeline(e.owner, r)
	x.Equal([]string{"대여: 김민준 · 개발팀", "예약: 카메라 " + begins(r)}, summaries(rs))
	x.Equal([]string{"김민준 · 영업팀", "Owner of acme"}, actorsOf(rs))
	x.Equal("김민준 · 개발팀", rs[1].GetDetail()["party"], "whom it was booked for")

	// What payday's past is told a row is called, and whose data saying it
	// shows.
	names, err := domain.NamesFor(e.owner, e.app(), e.s.Drv, e.s.Deps)
	x.NoError(err)
	for _, v := range []struct {
		id   []byte
		says string
		saw  []access.Subject
	}{
		{kim.GetId(), "김민준 · 개발팀", whose(kim, dev)},
		{signed.GetHolder().GetId(), "김민준 · 영업팀", whose(other, sales)},
		{lee.GetId(), "이서연", whose(lee)},
	} {
		got, saw, err := names.Name(e.owner, idOf(v.id))
		x.NoError(err)
		x.Equal(v.says, got)
		x.Equal(v.saw, saw)
	}

	// Renamed, the one left of the name is called by it alone.
	_, err = e.app().Party().Update(e.owner, app.PartyUpdateRequest_builder{Ref: ref[app.PartyRef](other.GetId()), Name: "김민준 과장"}.Build())
	x.NoError(err)
	lent = e.happenedIn(e.timeline(laptop, false), "custody.issue")
	x.Equal("지급: 김민준", lent.GetSummary())
	x.Equal("김민준 과장", lent.GetActor())
}

// happenedIn is the entry of these entries for the event of `kind`, the
// newest.
func (e *env) happenedIn(es []*app.TimelineEntry, kind string) *app.TimelineEntry {
	e.t.Helper()
	for _, en := range es {
		if en.GetKind() == "event" && en.GetEventKind() == kind {
			return en
		}
	}
	e.t.Fatalf("no %s in the history", kind)
	return nil
}
