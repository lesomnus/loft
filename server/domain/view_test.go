package domain_test

import (
	"strings"
	"testing"
	"time"
	"uuid"

	"github.com/lesomnus/payday/pdid"
	"google.golang.org/grpc/codes"
	"google.golang.org/protobuf/types/known/timestamppb"

	app "github.com/lesomnus/rove"
	"github.com/lesomnus/rove/server/pd"
)

// contract writes the tenant's contract the way `rove contract set` does.
func (e *env) contract(view, keep uint32) {
	_, err := e.s.Base.TenantContract().Add(e.owner, app.TenantContractAddRequest_builder{
		Tenant:        app.TenantRef_builder{Id: e.tenant.Bytes()}.Build(),
		Name:          "test",
		ViewDays:      view,
		KeepDays:      keep,
		DateEffective: timestamppb.New(e.now.Add(-1000 * day)),
	}.Build())
	e.x.NoError(err)
}

// aYearOfALaptop lives a laptop through most of a year and a half, the clock
// moving with it: in A, then B, broken and mended, and back in A.
func (e *env) aYearOfALaptop() (hq, a, b, laptop *app.Asset) {
	start := e.now
	e.now = start.Add(-400 * day)
	hq = e.space("본사", nil)
	a = e.space("A", hq)
	b = e.space("B", hq)
	laptop = e.item("노트북", "NB-1", a, 0)

	e.now = start.Add(-300 * day)
	_, err := e.app().Asset().Move(e.owner, app.AssetMoveRequest_builder{Ref: assetRef(laptop), To: assetRef(b)}.Build())
	e.x.NoError(err)

	e.now = start.Add(-250 * day)
	_, err = e.app().Asset().SetAttributes(e.owner, app.AssetSetAttributesRequest_builder{Ref: assetRef(laptop), Set: map[string]string{"status": "in_repair"}}.Build())
	e.x.NoError(err)

	e.now = start.Add(-200 * day)
	_, err = e.app().Asset().SetAttributes(e.owner, app.AssetSetAttributesRequest_builder{Ref: assetRef(laptop), Set: map[string]string{"status": "active"}}.Build())
	e.x.NoError(err)

	e.now = start.Add(-100 * day)
	_, err = e.app().Asset().Move(e.owner, app.AssetMoveRequest_builder{Ref: assetRef(laptop), To: assetRef(a)}.Build())
	e.x.NoError(err)

	e.now = start
	return hq, a, b, laptop
}

func (e *env) timeline(a *app.Asset, superseded bool) []*app.TimelineEntry {
	v, err := e.app().Asset().Timeline(e.owner, app.AssetTimelineRequest_builder{Ref: assetRef(a), Superseded: superseded}.Build())
	e.x.NoError(err)
	return v.GetEntries()
}

func repaired(en *app.TimelineEntry) bool {
	for _, v := range en.GetDetail() {
		if strings.Contains(v, "in_repair") {
			return true
		}
	}
	return false
}

// TestTheTimelineAnswersWithinTheViewWindow.
//
// What ended before the window began is not shown; what the window begins
// inside of is the state it begins in, and is.
func TestTheTimelineAnswersWithinTheViewWindow(t *testing.T) {
	e := newEnv(t)
	x := e.x
	_, a, b, laptop := e.aYearOfALaptop()

	all := e.timeline(laptop, true)
	x.True(slicesAny(all, repaired), "with no contract, the repair is in the history")

	e.contract(180, 0)
	since := e.now.Add(-180 * day)
	for _, superseded := range []bool{false, true} {
		got := e.timeline(laptop, superseded)
		x.NotEmpty(got)

		inB, inA := false, false
		for _, en := range got {
			x.False(repaired(en), "the repair was over before the window began: %s", en.GetSummary())
			if en.HasValidTo() {
				x.True(en.GetValidTo().AsTime().After(since), "a row that ended before the window: %s", en.GetSummary())
			}
			if en.HasSupersededAt() {
				x.True(en.GetSupersededAt().AsTime().After(since), "a row superseded before the window: %s", en.GetSummary())
			}
			if en.GetKind() == "placement" && sameId(en.GetOtherId(), b.GetId()) {
				inB = true
			}
			if en.GetKind() == "placement" && sameId(en.GetOtherId(), a.GetId()) && !en.HasValidTo() {
				inA = true
			}
		}
		x.True(inB, "the window begins with it in B")
		x.True(inA, "and it is in A now")

		x.True(slicesAny(got, func(en *app.TimelineEntry) bool {
			return en.GetKind() == "fact" && en.GetDetail()["key"] == "status" && en.GetDetail()["value"] == "active"
		}), "the status the window begins with")
		x.True(slicesAny(got, func(en *app.TimelineEntry) bool {
			return en.GetKind() == "event" && en.GetEventKind() == "asset.add"
		}), "the registration wrote the name it still has, so it is still what made the asset")
	}

	_, err := e.app().Asset().Timeline(e.owner, app.AssetTimelineRequest_builder{
		Ref:   assetRef(laptop),
		Known: timestamppb.New(since.Add(-day)),
	}.Build())
	x.Equal(codes.OutOfRange, codeOf(err), "what was known before the window")
}

// TestAMomentBeforeTheViewWindowIsNotOneToAskAbout.
func TestAMomentBeforeTheViewWindowIsNotOneToAskAbout(t *testing.T) {
	e := newEnv(t)
	x := e.x
	hq, _, b, laptop := e.aYearOfALaptop()

	me, err := e.app().Party().Me(e.owner, &app.PartyMeRequest{})
	x.NoError(err)
	x.False(me.HasHistorySince(), "with no contract, all of the history")

	e.contract(180, 0)
	me, err = e.app().Party().Me(e.owner, &app.PartyMeRequest{})
	x.NoError(err)
	x.EqualValues(180, me.GetHistoryDays())
	x.WithinDuration(e.now.Add(-180*day), me.GetHistorySince().AsTime(), time.Second, "where the window begins, for a page to keep to")

	x.Equal(b.GetId(), e.where(hq, laptop, e.now.Add(-170*day), nil), "inside the window, the state then")

	_, err = e.app().Asset().QueryAt(e.owner, app.AssetQueryAtRequest_builder{Root: assetRef(hq), At: e.ago(190 * day)}.Build())
	x.Equal(codes.OutOfRange, codeOf(err))
	_, err = e.app().Asset().QueryAt(e.owner, app.AssetQueryAtRequest_builder{Root: assetRef(hq), Known: e.ago(190 * day)}.Build())
	x.Equal(codes.OutOfRange, codeOf(err))

	_, err = e.app().Asset().Diff(e.owner, app.AssetDiffRequest_builder{Root: assetRef(hq), From: e.ago(190 * day), To: e.ago(0)}.Build())
	x.Equal(codes.OutOfRange, codeOf(err))
	_, err = e.app().Asset().Diff(e.owner, app.AssetDiffRequest_builder{Root: assetRef(hq), From: e.ago(170 * day), To: e.ago(0)}.Build())
	x.NoError(err)

	_, err = e.app().Asset().Report(e.owner, app.AssetReportRequest_builder{Kind: "utilization", From: e.ago(190 * day)}.Build())
	x.Equal(codes.OutOfRange, codeOf(err))
	_, err = e.app().Asset().Report(e.owner, app.AssetReportRequest_builder{Kind: "utilization"}.Build())
	x.NoError(err, "the default span is inside the window")
}

// TestTheEventsAndTheTrailAnswerWithinTheViewWindow.
//
// The events, through the domain layer's Recent and the generated List alike,
// and the trail's rows of the history -- which hold what each write wrote.
// The trail of who signed in is not the history, and stays.
func TestTheEventsAndTheTrailAnswerWithinTheViewWindow(t *testing.T) {
	e := newEnv(t)
	x := e.x
	e.aYearOfALaptop()

	old := e.now.Add(-400 * day)
	history := e.trailRow(pd.AssetDomain, old)
	account := e.trailRow(pd.HolderDomain, old)

	recent := func() []*app.Event {
		v, err := e.app().Event().Recent(e.owner, app.EventRecentRequest_builder{Size: 500}.Build())
		x.NoError(err)
		return v.GetItems()
	}
	listed := func() []*app.Event {
		v, err := e.app().Event().List(e.owner, app.EventListRequest_builder{Size: 500}.Build())
		x.NoError(err)
		return v.GetItems()
	}
	trail := func() map[uuid.UUID]bool {
		v, err := e.app().Audit().Recent(e.owner, app.AuditRecentRequest_builder{Size: 500}.Build())
		x.NoError(err)
		out := map[uuid.UUID]bool{}
		for _, r := range v.GetItems() {
			out[uuid.UUID(r.GetId())] = true
		}
		return out
	}

	x.True(slicesAny(recent(), func(v *app.Event) bool { return strings.Contains(v.GetPayload(), "in_repair") }))
	x.True(trail()[history])

	e.contract(180, 0)
	since := e.now.Add(-180 * day)
	for _, vs := range [][]*app.Event{recent(), listed()} {
		x.NotEmpty(vs)
		for _, v := range vs {
			x.True(!v.GetOccurredAt().AsTime().Before(since) || !v.GetDateCreated().AsTime().Before(since),
				"an event from before the window: %s", v.GetDesc())
		}
	}
	got := trail()
	x.False(got[history], "the trail of the history from before the window")
	x.True(got[account], "the trail of an account is not the history")
}

func (e *env) trailRow(d pdid.Domain, at time.Time) uuid.UUID {
	e.t.Helper()

	v, err := e.s.Ent.Audit.Create().
		SetId(uuid.NewV7()).
		SetTenantId(e.tenant.Uuid()).
		SetActorTenantId(e.tenant.Uuid()).
		SetActorId(uuid.Nil()).
		SetTraceId([]byte{}).
		SetAction("/rove.Test/Write").
		SetObjectId(pdid.New(d).Uuid()).
		SetDomain(uint32(d)).
		SetPatch([]byte{}).
		SetValue([]byte("contents")).
		SetDateCreated(at).
		Save(e.owner)
	e.x.NoError(err)

	return v.Id
}

func slicesAny[T any](vs []T, f func(T) bool) bool {
	for _, v := range vs {
		if f(v) {
			return true
		}
	}
	return false
}

// documents is a year of things begun and finished, and things begun and not.
type documents struct {
	meeting     *app.Reservation
	back, still *app.Custody
	done, open  *app.WorkOrder
	paper       *app.Stock

	// receipt is a file kept for the loan given back.
	receipt *app.Attachment
}

// aYearOfDocuments makes, four hundred days ago, a meeting called off, two
// loans and two work orders; ten days later one of each is over.
func (e *env) aYearOfDocuments() documents {
	x := e.x
	start := e.now
	e.now = start.Add(-400 * day)

	room := e.space("회의실", nil)
	e.bookable(room, nil)
	store := e.space("창고", nil)
	laptop := e.item("대여용 노트북", "LN-1", store, 0)
	phone := e.item("대여용 휴대폰", "LP-1", store, 0)
	_, kim := e.person("member")

	d := documents{paper: e.stock(e.model("A4 용지"), store, 10, 0)}

	var err error
	d.meeting, err = e.reserve(e.owner, room, e.tomorrow(10, 0), e.tomorrow(11, 0), nil)
	x.NoError(err)
	_, err = e.app().Reservation().Cancel(e.owner, app.ReservationDecideRequest_builder{Ref: ref[app.ReservationRef](d.meeting.GetId())}.Build())
	x.NoError(err)

	lend := func(a *app.Asset) *app.Custody {
		v, err := e.app().Custody().Add(e.owner, app.CustodyAddRequest_builder{
			Party: ref[app.PartyRef](kim.GetId()),
			Lines: []*app.CustodyLineSpec{app.CustodyLineSpec_builder{Asset: assetRef(a)}.Build()},
		}.Build())
		x.NoError(err)
		return v
	}
	d.back, d.still = lend(laptop), lend(phone)
	d.receipt, err = e.app().Attachment().Upload(e.owner, app.AttachmentUploadRequest_builder{
		SubjectId: d.back.GetId(),
		Name:      "인수증.pdf",
		Data:      []byte("signed"),
	}.Build())
	x.NoError(err)

	work := func(a *app.Asset) *app.WorkOrder {
		v, err := e.app().WorkOrder().Add(e.owner, app.WorkOrderAddRequest_builder{Asset: assetRef(a), Name: "점검"}.Build())
		x.NoError(err)
		return v
	}
	d.done, d.open = work(laptop), work(phone)

	e.now = start.Add(-390 * day)
	_, err = e.app().Custody().Return(e.owner, app.CustodyReturnRequest_builder{Ref: ref[app.CustodyRef](d.back.GetId())}.Build())
	x.NoError(err)
	_, err = e.app().WorkOrder().Complete(e.owner, app.WorkOrderCompleteRequest_builder{Ref: ref[app.WorkOrderRef](d.done.GetId())}.Build())
	x.NoError(err)

	e.now = start
	return d
}

// TestWhatWasOverBeforeTheViewWindowIsOutOfIt.
//
// A document that was over before the window began is the history, and one
// still open is the present, however old it is: a loan never given back is
// still on the list.
func TestWhatWasOverBeforeTheViewWindowIsOutOfIt(t *testing.T) {
	e := newEnv(t)
	x := e.x
	start := e.now
	d := e.aYearOfDocuments()
	still, open, paper := d.still, d.open, d.paper

	reservations := func() []*app.Reservation {
		v, err := e.app().Reservation().List(e.owner, app.ReservationListRequest_builder{}.Build())
		x.NoError(err)
		return v.GetItems()
	}
	custodies := func() []*app.Custody {
		v, err := e.app().Custody().List(e.owner, app.CustodyListRequest_builder{}.Build())
		x.NoError(err)
		return v.GetItems()
	}
	works := func() []*app.WorkOrder {
		v, err := e.app().WorkOrder().List(e.owner, app.WorkOrderListRequest_builder{}.Build())
		x.NoError(err)
		return v.GetItems()
	}
	movements := func() []*app.StockMovement {
		v, err := e.app().StockMovement().List(e.owner, app.StockMovementListRequest_builder{
			Filters: []*app.StockMovementFilter{app.StockMovementFilter_builder{Stock: ref[app.StockRef](paper.GetId())}.Build()},
		}.Build())
		x.NoError(err)
		return v.GetItems()
	}
	workCount := func() float64 {
		v, err := e.app().Asset().Report(e.owner, app.AssetReportRequest_builder{Kind: "work"}.Build())
		x.NoError(err)
		n := 0.0
		for _, r := range v.GetRows() {
			if r.GetGroup() == "status" {
				n += r.GetValue()
			}
		}
		return n
	}
	x.Len(reservations(), 1)
	x.Len(custodies(), 2)
	x.Len(works(), 2)
	x.NotEmpty(movements())
	x.EqualValues(2, workCount())

	e.contract(180, 0)

	x.Empty(reservations(), "a meeting called off a year ago")
	cs := custodies()
	x.Len(cs, 1, "the loan given back a year ago")
	x.True(sameId(still.GetId(), cs[0].GetId()), "the loan never given back is the present")
	ws := works()
	x.Len(ws, 1)
	x.True(sameId(open.GetId(), ws[0].GetId()), "the work still open")
	x.Empty(movements(), "the paper came in before the window")
	x.EqualValues(1, workCount(), "the report counts what the window has")

	cal, err := e.app().Reservation().Calendar(e.owner, app.ReservationCalendarRequest_builder{
		From: timestamppb.New(start.Add(-401 * day)),
		To:   timestamppb.New(start.Add(-399 * day)),
	}.Build())
	x.NoError(err)
	x.Empty(cal.GetEntries())
}
