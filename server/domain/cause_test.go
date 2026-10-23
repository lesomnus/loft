package domain_test

import (
	"context"
	"testing"

	"uuid"

	app "github.com/lesomnus/rove"
	"github.com/lesomnus/rove/internal/ent"
	"github.com/lesomnus/rove/internal/ent/event"
	"github.com/lesomnus/rove/internal/ent/fact"
	"github.com/lesomnus/rove/internal/ent/stewardship"
)

// An operation over many things records an event of its own and, for each
// thing it changed, a child: what it did to that one, with that one as the
// subject and the operation's event as the cause, and the rows it wrote for
// that one pointing at the child (design D34). So a thing's history holds what
// happened to it, and the operation says once what the operation was.

// eventsOf is every event of `kind` in the env's workspace, an operation's own
// and its children both.
func (e *env) eventsOf(kind string) []*ent.Event {
	vs, err := e.s.Ent.Event.Query().Where(event.TenantId(e.tenant.Uuid()), event.Kind(kind)).All(context.Background())
	e.x.NoError(err)
	return vs
}

// split is an operation's own event, and its children by their subject.
func (e *env) split(vs []*ent.Event) (*ent.Event, map[uuid.UUID]*ent.Event) {
	var parent *ent.Event
	kids := map[uuid.UUID]*ent.Event{}
	for _, v := range vs {
		if v.CauseId == nil {
			e.x.Nil(parent, "two events of the operation's own")
			parent = v
			continue
		}
		kids[*v.SubjectId] = v
	}
	e.x.NotNil(parent, "no event of the operation's own")
	for _, v := range kids {
		e.x.Equal(parent.Id, *v.CauseId, "a child of another operation")
	}
	return parent, kids
}

// TestWhatAnOperationCallsIsItsChild: an operation made of others -- a stock's
// units turned into assets, each registered -- is the cause of the events they
// record, and the rows each wrote point at its own.
func TestWhatAnOperationCallsIsItsChild(t *testing.T) {
	e := newEnv(t)
	x := e.x
	room := e.space("창고", nil)
	st := e.stock(e.model("모니터"), room, 5, 0)

	v, err := e.app().Stock().Convert(e.owner, app.StockConvertRequest_builder{Ref: ref[app.StockRef](st.GetId()), Quantity: 2}.Build())
	x.NoError(err)
	x.Len(v.GetAssets(), 2)

	converts := e.eventsOf("stock.convert")
	x.Len(converts, 1)
	x.Nil(converts[0].CauseId)

	adds, err := e.s.Ent.Event.Query().Where(event.CauseId(converts[0].Id)).All(context.Background())
	x.NoError(err)
	x.Len(adds, 2, "an event for each asset it made, under the conversion")
	for _, a := range v.GetAssets() {
		id := idOf(a.GetId()).Uuid()
		var mine *ent.Event
		for _, ev := range adds {
			if *ev.SubjectId == id {
				mine = ev
			}
		}
		x.NotNil(mine, "%s has no event of its own", a.GetTag())
		x.Equal("asset.add", mine.Kind, "the kind a registration of one is")

		fs, err := e.s.Ent.Fact.Query().Where(fact.SubjectId(id)).All(context.Background())
		x.NoError(err)
		x.NotEmpty(fs)
		for _, f := range fs {
			x.Equal(mine.Id, f.EventId, "a fact of %s points at another event", a.GetTag())
		}

		// And its history says what it was part of.
		found := false
		for _, en := range e.timeline(a, false) {
			if en.GetKind() == "event" {
				x.Equal("재고 → 자산: 모니터 2개", en.GetCause())
				found = true
			}
		}
		x.True(found, "%s's history has no event", a.GetTag())
	}
}

// TestAHandOverIsOnTheHistoryOfEachThingHandedOver: what an operation writes
// itself is under a child of each thing's own. A hand-over's event is about
// the hand-over; the history of each asset in it says it was handed over, and
// its custodian's row says by what.
func TestAHandOverIsOnTheHistoryOfEachThingHandedOver(t *testing.T) {
	e := newEnv(t)
	x := e.x
	_, kim := e.person("member")
	room := e.space("창고", nil)
	laptop := e.item("노트북", "NB-1", room, day)
	screen := e.item("모니터", "MN-1", room, day)

	c, err := e.app().Custody().Add(e.owner, app.CustodyAddRequest_builder{
		Party: ref[app.PartyRef](kim.GetId()),
		Lines: []*app.CustodyLineSpec{
			app.CustodyLineSpec_builder{Asset: assetRef(laptop)}.Build(),
			app.CustodyLineSpec_builder{Asset: assetRef(screen)}.Build(),
		},
	}.Build())
	x.NoError(err)

	issue, kids := e.split(e.eventsOf("custody.issue"))
	x.Equal(idOf(c.GetId()).Uuid(), *issue.SubjectId, "the hand-over's own is about the hand-over")
	x.Len(kids, 2)
	for _, a := range []*app.Asset{laptop, screen} {
		id := idOf(a.GetId()).Uuid()
		mine, ok := kids[id]
		x.True(ok, "%s has no event of its own", a.GetTag())

		rows, err := e.s.Ent.Stewardship.Query().Where(stewardship.AssetId(id), stewardship.Role("custodian")).All(context.Background())
		x.NoError(err)
		x.Len(rows, 1)
		x.Equal(mine.Id, rows[0].EventId, "the custodian's row points at the asset's own event")

		said := false
		for _, en := range e.timeline(a, false) {
			if en.GetKind() == "event" && en.GetEventKind() == "custody.issue" {
				x.Contains(en.GetSummary(), kim.GetName())
				said = true
			}
		}
		x.True(said, "%s's history does not say it was handed over", a.GetTag())
	}

	// And a return the same way.
	_, err = e.app().Custody().Return(e.owner, app.CustodyReturnRequest_builder{Ref: ref[app.CustodyRef](c.GetId())}.Build())
	x.NoError(err)
	back, kids := e.split(e.eventsOf("custody.return"))
	x.Equal(idOf(c.GetId()).Uuid(), *back.SubjectId)
	x.Len(kids, 2, "a return is on the history of each thing returned")
}

// TestAReservationIsOnTheHistoryOfEverythingItBooks: the reservation's own
// event is about the reservation, where it was about the first thing booked
// and nothing said it of the rest.
func TestAReservationIsOnTheHistoryOfEverythingItBooks(t *testing.T) {
	e := newEnv(t)
	x := e.x
	room := e.space("회의실", nil)
	projector := e.space("프로젝터", nil)
	e.bookable(room, nil)
	e.bookable(projector, nil)

	r, err := e.reserve(e.owner, room, e.tomorrow(10, 0), e.tomorrow(11, 0), func(r *app.ReservationAddRequest) {
		r.SetItems(append(r.GetItems(), app.ReservationItemSpec_builder{Resource: assetRef(projector)}.Build()))
	})
	x.NoError(err)

	own, kids := e.split(e.eventsOf("reservation.add"))
	x.Equal(idOf(r.GetId()).Uuid(), *own.SubjectId)
	x.Len(kids, 2)
	for _, a := range []*app.Asset{room, projector} {
		_, ok := kids[idOf(a.GetId()).Uuid()]
		x.True(ok, "%s's history does not say it was booked", a.GetName())
	}
}

// TestTheFeedSaysAnOperationOnce: what an operation did to each thing is that
// thing's history, and the workspace's feed says the operation -- unless it is
// asked for the children too.
func TestTheFeedSaysAnOperationOnce(t *testing.T) {
	e := newEnv(t)
	x := e.x
	room := e.space("창고", nil)
	st := e.stock(e.model("모니터"), room, 5, 0)
	_, err := e.app().Stock().Convert(e.owner, app.StockConvertRequest_builder{Ref: ref[app.StockRef](st.GetId()), Quantity: 3}.Build())
	x.NoError(err)

	kinds := func(children bool) map[string]int {
		v, err := e.app().Event().Recent(e.owner, app.EventRecentRequest_builder{Size: 100, Children: children}.Build())
		x.NoError(err)
		out := map[string]int{}
		for _, ev := range v.GetItems() {
			out[ev.GetKind()]++
		}
		return out
	}
	feed := kinds(false)
	x.Equal(1, feed["stock.convert"])
	x.Equal(1, feed["asset.add"], "the room registered on its own, and none of the three converted")

	all := kinds(true)
	x.Equal(4, all["asset.add"], "and with its children, each of the three")
}
