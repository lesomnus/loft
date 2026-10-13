package domain_test

import (
	"context"
	"testing"

	app "github.com/lesomnus/rove"
	"github.com/lesomnus/rove/internal/ent/event"
	"github.com/lesomnus/rove/internal/ent/fact"
	"github.com/lesomnus/rove/server/domain"
)

// rows is how many of each kind the tenant has.
type rows struct {
	placements, links, stewardships, facts, events            int
	reservations, items, allocations, custodies, lines, works int
	movements                                                 int
}

func (e *env) rows() rows {
	ctx := context.Background()
	n := func(v int, err error) int {
		e.x.NoError(err)
		return v
	}
	db := e.s.Ent
	return rows{
		placements:   n(db.Placement.Query().Count(ctx)),
		links:        n(db.Link.Query().Count(ctx)),
		stewardships: n(db.Stewardship.Query().Count(ctx)),
		facts:        n(db.Fact.Query().Count(ctx)),
		events:       n(db.Event.Query().Where(event.KindNEQ("retention.expired")).Count(ctx)),
		reservations: n(db.Reservation.Query().Count(ctx)),
		items:        n(db.ReservationItem.Query().Count(ctx)),
		allocations:  n(db.Allocation.Query().Count(ctx)),
		custodies:    n(db.Custody.Query().Count(ctx)),
		lines:        n(db.CustodyLine.Query().Count(ctx)),
		works:        n(db.WorkOrder.Query().Count(ctx)),
		movements:    n(db.StockMovement.Query().Count(ctx)),
	}
}

func (e *env) expire(dry bool) domain.Expired {
	x, err := e.s.Deps.Expire(context.Background(), e.s.Base, e.s.Drv, e.tenant, dry)
	e.x.NoError(err)
	return x
}

// TestAKeepWindowTakesWhatItNoLongerReaches.
//
// A dry run does all of it and undoes it, so it says what a run takes, to the
// row. The run takes what was over before the window and leaves the state the
// window begins in, so the history inside the window still answers.
func TestAKeepWindowTakesWhatItNoLongerReaches(t *testing.T) {
	e := newEnv(t)
	x := e.x
	hq, _, b, laptop := e.aYearOfALaptop()
	docs := e.aYearOfDocuments()

	x.Zero(e.expire(false).Total(), "a tenant with no contract is kept forever")

	e.contract(180, 180)
	before := e.rows()

	plan := e.expire(true)
	x.Positive(plan.Total())
	x.Equal(before, e.rows(), "a dry run took something")

	got := e.expire(false)
	x.Equal(plan, got, "the run took other than the dry run said")
	x.Positive(got.Placements)
	x.Positive(got.Facts)
	x.Positive(got.Events)
	after := e.rows()
	x.Equal(before.placements-got.Placements, after.placements)
	x.Equal(before.facts-got.Facts, after.facts)
	x.Equal(before.events-got.Events, after.events)

	x.Equal(1, got.Reservations, "the meeting called off")
	x.Equal(1, got.Custodies, "the loan given back")
	x.Equal(1, got.WorkOrders, "the work done")
	x.Positive(got.StockMovements)
	x.Zero(after.reservations)
	x.Zero(after.items, "the meeting's parts went with it")
	x.Equal(1, after.custodies, "the loan never given back is the present")
	x.Equal(1, after.lines)
	x.Equal(1, after.works, "the work still open")
	x.Equal(1, got.Attachments, "the file kept for the loan given back")
	x.Empty(got.Lost)
	_, err := e.s.Deps.Files.Open(context.Background(), docs.receipt.GetObjectKey())
	x.Error(err, "the file of a loan that went is still stored")
	var n int

	// The value it had stopped having, and the event that set it, are gone.
	n, err = e.s.Ent.Fact.Query().Where(fact.Key("status"), fact.Value("in_repair")).Count(context.Background())
	x.NoError(err)
	x.Zero(n)

	// What the window holds still answers, from the state it begins in.
	x.Equal(b.GetId(), e.where(hq, laptop, e.now.Add(-125*day), nil), "a hundred and twenty-five days ago it was in B")
	tl := e.timeline(laptop, true)
	x.NotEmpty(tl)

	// The receipt says how many, and nothing they said.
	r, err := e.s.Ent.Event.Query().Where(event.Kind("retention.expired")).Only(context.Background())
	x.NoError(err)
	x.Contains(r.Payload, "placements")
	x.NotContains(r.Payload, "in_repair")

	x.Zero(e.expire(false).Total(), "what a pass has taken is not there to take again")
	n, err = e.s.Ent.Event.Query().Where(event.Kind("retention.expired")).Count(context.Background())
	x.NoError(err)
	x.Equal(1, n, "a pass that took nothing wrote a receipt")
}

// TestAHoldKeepsAllOfIt.
func TestAHoldKeepsAllOfIt(t *testing.T) {
	e := newEnv(t)
	x := e.x
	e.aYearOfALaptop()
	e.contract(30, 180)

	_, err := e.s.Base.LegalHold().Add(e.owner, app.LegalHoldAddRequest_builder{
		Tenant: app.TenantRef_builder{Id: e.tenant.Bytes()}.Build(),
		Name:   "사건 1",
	}.Build())
	x.NoError(err)

	before := e.rows()
	got := e.expire(false)
	x.Equal([]string{"사건 1"}, got.Held)
	x.Zero(got.Total())
	x.Equal(before, e.rows())
}

// TestADisposalOutlivesTheWindow.
//
// The status that took an asset out of use is the record of its disposal, and
// stays when a later status replaced it long ago.
func TestADisposalOutlivesTheWindow(t *testing.T) {
	e := newEnv(t)
	x := e.x
	start := e.now

	e.now = start.Add(-400 * day)
	room := e.space("창고", nil)
	a := e.item("모니터", "MN-1", room, 0)
	set := func(v string) {
		_, err := e.app().Asset().SetAttributes(e.owner, app.AssetSetAttributesRequest_builder{Ref: assetRef(a), Set: map[string]string{"status": v}}.Build())
		x.NoError(err)
	}
	e.now = start.Add(-350 * day)
	set("disposed")
	e.now = start.Add(-300 * day)
	set("active")
	e.now = start.Add(-250 * day)
	set("in_repair")
	e.now = start.Add(-200 * day)
	set("active")
	e.now = start

	e.contract(30, 180)
	e.expire(false)

	n, err := e.s.Ent.Fact.Query().Where(fact.Key("status"), fact.Value("disposed")).Count(context.Background())
	x.NoError(err)
	x.Equal(1, n, "the disposal went")
	n, err = e.s.Ent.Fact.Query().Where(fact.Key("status"), fact.Value("in_repair")).Count(context.Background())
	x.NoError(err)
	x.Zero(n)
	n, err = e.s.Ent.Event.Query().Where(event.Kind("asset.add")).Count(context.Background())
	x.NoError(err)
	x.Positive(n, "the registration went")
}
