package domain_test

import (
	"context"
	"testing"
	"time"

	"github.com/lesomnus/z"
	"google.golang.org/grpc/codes"
	"google.golang.org/protobuf/types/known/timestamppb"

	app "github.com/lesomnus/rove"
)

func (e *env) bookable(a *app.Asset, edit func(*app.BookableAddRequest)) *app.Bookable {
	req := app.BookableAddRequest_builder{Asset: assetRef(a)}.Build()
	if edit != nil {
		edit(req)
	}
	v, err := e.app().Bookable().Add(e.owner, req)
	e.x.NoError(err)
	return v
}

// tomorrow is the given hour tomorrow, on the env's clock.
func (e *env) tomorrow(h, m int) time.Time {
	t := e.now.AddDate(0, 0, 1)
	return time.Date(t.Year(), t.Month(), t.Day(), h, m, 0, 0, time.Local)
}

func (e *env) reserve(ctx context.Context, what *app.Asset, from, to time.Time, edit func(*app.ReservationAddRequest)) (*app.Reservation, error) {
	req := app.ReservationAddRequest_builder{
		Name:     "회의",
		BeginsAt: timestamppb.New(from),
		EndsAt:   timestamppb.New(to),
		Items:    []*app.ReservationItemSpec{app.ReservationItemSpec_builder{Resource: assetRef(what)}.Build()},
	}.Build()
	if edit != nil {
		edit(req)
	}
	return e.app().Reservation().Add(ctx, req)
}

func TestReservationsDoNotOverlap(t *testing.T) {
	e := newEnv(t)
	x := e.x
	room := e.space("회의실 A", nil)
	e.bookable(room, nil)
	kim, _ := e.person("member")
	lee, _ := e.person("member")

	r1, err := e.reserve(kim, room, e.tomorrow(10, 0), e.tomorrow(11, 0), nil)
	x.NoError(err)
	x.Equal("confirmed", r1.GetStatus())

	_, err = e.reserve(lee, room, e.tomorrow(10, 30), e.tomorrow(11, 30), nil)
	x.Equal(codes.Aborted, codeOf(err), "overlapping a confirmed reservation")

	_, err = e.reserve(lee, room, e.tomorrow(11, 0), e.tomorrow(12, 0), nil)
	x.NoError(err, "back to back is fine")

	// A member cannot book over somebody; a manager can, saying why.
	_, err = e.reserve(lee, room, e.tomorrow(10, 0), e.tomorrow(11, 0), func(r *app.ReservationAddRequest) { r.SetOverride(true); r.SetReason("임원 회의") })
	x.Equal(codes.PermissionDenied, codeOf(err))
	_, err = e.reserve(e.owner, room, e.tomorrow(10, 0), e.tomorrow(11, 0), func(r *app.ReservationAddRequest) { r.SetOverride(true); r.SetReason("임원 회의") })
	x.NoError(err)

	// Cancelling gives the time back.
	_, err = e.app().Reservation().Cancel(lee, app.ReservationDecideRequest_builder{Ref: ref[app.ReservationRef](r1.GetId())}.Build())
	x.Equal(codes.PermissionDenied, codeOf(err), "not lee's to cancel")
	_, err = e.app().Reservation().Cancel(kim, app.ReservationDecideRequest_builder{Ref: ref[app.ReservationRef](r1.GetId())}.Build())
	x.NoError(err)
	_, err = e.reserve(lee, room, e.tomorrow(10, 0), e.tomorrow(10, 30), nil)
	x.NoError(err)
}

func TestBufferAndHours(t *testing.T) {
	e := newEnv(t)
	x := e.x
	room := e.space("대회의실", nil)
	e.bookable(room, func(r *app.BookableAddRequest) {
		r.SetBufferAfter(15)
		hours := &app.OpeningHours{}
		for wd := int32(0); wd < 7; wd++ {
			hours.SetRanges(append(hours.GetRanges(), app.OpeningRange_builder{Weekday: wd, FromMinute: 9 * 60, ToMinute: 18 * 60}.Build()))
		}
		r.SetHours(hours)
		r.SetTimezone("Local")
	})
	_, err := e.reserve(e.owner, room, e.tomorrow(14, 0), e.tomorrow(15, 0), nil)
	x.NoError(err)
	_, err = e.reserve(e.owner, room, e.tomorrow(15, 0), e.tomorrow(16, 0), nil)
	x.Equal(codes.Aborted, codeOf(err), "the fifteen minutes after belong to cleaning up")
	_, err = e.reserve(e.owner, room, e.tomorrow(15, 15), e.tomorrow(16, 0), nil)
	x.NoError(err)
	_, err = e.reserve(e.owner, room, e.tomorrow(19, 0), e.tomorrow(20, 0), nil)
	x.Equal(codes.FailedPrecondition, codeOf(err), "closed in the evening")
}

func TestApprovalAndHolds(t *testing.T) {
	e := newEnv(t)
	x := e.x
	hall := e.space("강당", nil)
	e.bookable(hall, func(r *app.BookableAddRequest) { r.SetApproval(true) })
	kim, _ := e.person("member")

	r, err := e.reserve(kim, hall, e.tomorrow(16, 0), e.tomorrow(17, 0), nil)
	x.NoError(err)
	x.Equal("requested", r.GetStatus())
	_, err = e.reserve(e.owner, hall, e.tomorrow(16, 0), e.tomorrow(17, 0), nil)
	x.Equal(codes.Aborted, codeOf(err), "a request holds its time while it waits")

	_, err = e.app().Reservation().Approve(kim, app.ReservationDecideRequest_builder{Ref: ref[app.ReservationRef](r.GetId())}.Build())
	x.Error(err, "a member does not approve their own")
	v, err := e.app().Reservation().Approve(e.owner, app.ReservationDecideRequest_builder{Ref: ref[app.ReservationRef](r.GetId())}.Build())
	x.NoError(err)
	x.Equal("confirmed", v.GetStatus())

	// A hold keeps the time for ten minutes and no longer.
	room := e.space("소회의실", nil)
	e.bookable(room, nil)
	h, err := e.reserve(kim, room, e.tomorrow(9, 0), e.tomorrow(10, 0), func(r *app.ReservationAddRequest) { r.SetHold(true) })
	x.NoError(err)
	x.Equal("held", h.GetStatus())
	_, err = e.reserve(e.owner, room, e.tomorrow(9, 0), e.tomorrow(10, 0), nil)
	x.Equal(codes.Aborted, codeOf(err))

	e.now = e.now.Add(11 * time.Minute)
	e.sweep()
	got, err := e.app().Reservation().Get(kim, app.ReservationGetRequest_builder{Ref: ref[app.ReservationRef](h.GetId())}.Build())
	x.NoError(err)
	x.Equal("expired", got.GetStatus())
	_, err = e.reserve(e.owner, room, e.tomorrow(9, 0), e.tomorrow(10, 0), nil)
	x.NoError(err, "the expired hold let go of its time")
}

func TestSeries(t *testing.T) {
	e := newEnv(t)
	x := e.x
	room := e.space("회의실", nil)
	e.bookable(room, func(r *app.BookableAddRequest) { r.SetHorizonDays(120) })

	r, err := e.reserve(e.owner, room, e.tomorrow(10, 0), e.tomorrow(11, 0), func(r *app.ReservationAddRequest) { r.SetRepeat("FREQ=WEEKLY;COUNT=4") })
	x.NoError(err)
	x.NotEmpty(r.GetSeriesId())

	// The third week is taken; the whole series is refused rather than three
	// quarters of it booked.
	room2 := e.space("회의실 2", nil)
	e.bookable(room2, func(r *app.BookableAddRequest) { r.SetHorizonDays(120) })
	_, err = e.reserve(e.owner, room2, e.tomorrow(10, 0).AddDate(0, 0, 14), e.tomorrow(11, 0).AddDate(0, 0, 14), nil)
	x.NoError(err)
	_, err = e.reserve(e.owner, room2, e.tomorrow(10, 0), e.tomorrow(11, 0), func(r *app.ReservationAddRequest) { r.SetRepeat("FREQ=WEEKLY;COUNT=4") })
	x.Equal(codes.Aborted, codeOf(err))
	cal, err := e.app().Reservation().Calendar(e.owner, app.ReservationCalendarRequest_builder{
		Resources: []*app.AssetRef{assetRef(room2)},
		From:      timestamppb.New(e.now),
		To:        timestamppb.New(e.now.AddDate(0, 0, 40)),
	}.Build())
	x.NoError(err)
	x.Len(cal.GetEntries(), 1, "nothing of the refused series was kept")

	// Cancelling the series from the second one on leaves the first.
	all, err := e.app().Reservation().List(e.owner, app.ReservationListRequest_builder{
		Filters: []*app.ReservationFilter{app.ReservationFilter_builder{SeriesId: r.GetSeriesId()}.Build()},
	}.Build())
	x.NoError(err)
	x.Len(all.GetItems(), 4)
	second := all.GetItems()[1]
	_, err = e.app().Reservation().Cancel(e.owner, app.ReservationDecideRequest_builder{Ref: ref[app.ReservationRef](second.GetId()), Series: true}.Build())
	x.NoError(err)
	all, err = e.app().Reservation().List(e.owner, app.ReservationListRequest_builder{
		Filters: []*app.ReservationFilter{app.ReservationFilter_builder{SeriesId: r.GetSeriesId()}.Build()},
	}.Build())
	x.NoError(err)
	n := 0
	for _, v := range all.GetItems() {
		if v.GetStatus() == "confirmed" {
			n++
		}
	}
	x.Equal(1, n)
}

// TestABookingSentAgainIsTheSameBooking: a booking sent again by its
// operation's identifier finds its time taken -- by itself -- and later finds
// it gone by. It answers with the reservation the first copy made, and the
// series is booked once.
func TestABookingSentAgainIsTheSameBooking(t *testing.T) {
	e := newEnv(t)
	x := e.x
	room := e.space("회의실", nil)
	e.bookable(room, func(r *app.BookableAddRequest) { r.SetHorizonDays(120) })

	op := newOp()
	book := func() (*app.Reservation, error) {
		return e.reserve(e.owner, room, e.tomorrow(10, 0), e.tomorrow(11, 0), func(r *app.ReservationAddRequest) {
			r.SetRepeat("FREQ=DAILY;COUNT=3")
			r.SetOp(op)
		})
	}
	r, err := book()
	x.NoError(err)
	again, err := book()
	x.NoError(err, "the same booking, sent again")
	x.Equal(r.GetId(), again.GetId())
	x.Equal(r.GetSeriesId(), again.GetSeriesId())

	_, err = e.reserve(e.owner, room, e.tomorrow(10, 0), e.tomorrow(11, 0), nil)
	x.Equal(codes.Aborted, codeOf(err), "a booking of its own finds the time taken")

	e.now = e.now.AddDate(0, 0, 5)
	again, err = book()
	x.NoError(err, "sent again once it is over")
	x.Equal(r.GetId(), again.GetId())

	all, err := e.app().Reservation().List(e.owner, app.ReservationListRequest_builder{
		Filters: []*app.ReservationFilter{app.ReservationFilter_builder{SeriesId: r.GetSeriesId()}.Build()},
	}.Build())
	x.NoError(err)
	x.Len(all.GetItems(), 3, "booked once")
	x.Equal(1, e.ops(op))
}

func TestKitTakesItsParts(t *testing.T) {
	e := newEnv(t)
	x := e.x
	store := e.space("창고", nil)
	kit, err := e.app().Asset().Add(e.owner, app.AssetAddRequest_builder{Name: "촬영 키트", Kind: "kit", To: assetRef(store)}.Build())
	x.NoError(err)
	cam := e.item("카메라", "CM-1", kit, day)
	_, err = e.app().Asset().Relate(e.owner, app.AssetRelateRequest_builder{Ref: assetRef(cam), Target: assetRef(kit), Kind: "member_of", Required: true}.Build())
	x.NoError(err)
	e.bookable(kit, nil)
	e.bookable(cam, nil)

	_, err = e.reserve(e.owner, kit, e.tomorrow(9, 0), e.tomorrow(12, 0), nil)
	x.NoError(err)
	_, err = e.reserve(e.owner, cam, e.tomorrow(10, 0), e.tomorrow(11, 0), nil)
	x.Equal(codes.Aborted, codeOf(err), "the camera goes with the kit")
}

func TestExclusiveGroup(t *testing.T) {
	e := newEnv(t)
	x := e.x
	hall := e.space("대회의실", nil)
	left := e.space("대회의실 A", hall)
	e.bookable(hall, func(r *app.BookableAddRequest) { r.SetExclusiveGroup("hall") })
	e.bookable(left, func(r *app.BookableAddRequest) { r.SetExclusiveGroup("hall") })

	_, err := e.reserve(e.owner, left, e.tomorrow(13, 0), e.tomorrow(14, 0), nil)
	x.NoError(err)
	_, err = e.reserve(e.owner, hall, e.tomorrow(13, 30), e.tomorrow(15, 0), nil)
	x.Equal(codes.Aborted, codeOf(err), "the whole hall is not free while half of it is taken")
}

func TestNoShowAndFinish(t *testing.T) {
	e := newEnv(t)
	x := e.x
	room := e.space("회의실", nil)
	e.bookable(room, nil)
	kim, _ := e.person("member")

	start := e.now.Add(time.Hour).Truncate(time.Minute)
	r, err := e.reserve(kim, room, start, start.Add(time.Hour), nil)
	x.NoError(err)
	r2, err := e.reserve(kim, room, start.Add(2*time.Hour), start.Add(3*time.Hour), nil)
	x.NoError(err)

	// Nobody came to the first.
	e.now = start.Add(20 * time.Minute)
	e.sweep()
	v, err := e.app().Reservation().Get(kim, app.ReservationGetRequest_builder{Ref: ref[app.ReservationRef](r.GetId())}.Build())
	x.NoError(err)
	x.Equal("no_show", v.GetStatus())

	// Somebody did come to the second, and it finished on its own.
	e.now = start.Add(2*time.Hour + 5*time.Minute)
	_, err = e.app().Reservation().CheckIn(kim, app.ReservationDecideRequest_builder{Ref: ref[app.ReservationRef](r2.GetId())}.Build())
	x.NoError(err)
	e.now = start.Add(3*time.Hour + time.Minute)
	e.sweep()
	v, err = e.app().Reservation().Get(kim, app.ReservationGetRequest_builder{Ref: ref[app.ReservationRef](r2.GetId())}.Build())
	x.NoError(err)
	x.Equal("completed", v.GetStatus())

	in, err := e.app().Notification().Inbox(kim, &app.NotificationInboxRequest{})
	x.NoError(err)
	kinds := []string{}
	for _, n := range in.GetItems() {
		kinds = append(kinds, n.GetKind())
	}
	x.Contains(kinds, "reservation.no_show")
}

func TestMaintenanceBlocks(t *testing.T) {
	e := newEnv(t)
	x := e.x
	room := e.space("회의실", nil)
	e.bookable(room, nil)
	projector := e.item("프로젝터", "PJ-1", room, day)
	e.bookable(projector, nil)

	w, err := e.app().WorkOrder().Add(e.owner, app.WorkOrderAddRequest_builder{
		Asset:     assetRef(projector),
		Kind:      "inspection",
		Name:      "램프 점검",
		BeginsAt:  timestamppb.New(e.tomorrow(9, 0)),
		EndsAt:    timestamppb.New(e.tomorrow(12, 0)),
		Blocking:  true,
		EveryDays: 90,
	}.Build())
	x.NoError(err)
	_, err = e.reserve(e.owner, projector, e.tomorrow(10, 0), e.tomorrow(11, 0), nil)
	x.Equal(codes.Aborted, codeOf(err), "it is being looked at then")

	_, err = e.app().WorkOrder().Complete(e.owner, app.WorkOrderCompleteRequest_builder{Ref: ref[app.WorkOrderRef](w.GetId()), Condition: "good"}.Build())
	x.NoError(err)
	_, err = e.reserve(e.owner, projector, e.tomorrow(10, 0), e.tomorrow(11, 0), nil)
	x.NoError(err, "done early, the time is free")

	ws, err := e.app().WorkOrder().List(e.owner, app.WorkOrderListRequest_builder{
		Filters: []*app.WorkOrderFilter{app.WorkOrderFilter_builder{Status: z.Ptr("scheduled")}.Build()},
	}.Build())
	x.NoError(err)
	x.Len(ws.GetItems(), 1, "the next inspection is on the calendar")
	x.WithinDuration(e.now.AddDate(0, 0, 90), ws.GetItems()[0].GetBeginsAt().AsTime(), time.Minute)

	// A member reports something broken, and only that.
	kim, _ := e.person("member")
	_, err = e.app().WorkOrder().Add(kim, app.WorkOrderAddRequest_builder{Asset: assetRef(projector), Kind: "inspection", Name: "x"}.Build())
	x.Equal(codes.PermissionDenied, codeOf(err))
	_, err = e.app().WorkOrder().Add(kim, app.WorkOrderAddRequest_builder{Asset: assetRef(projector), Kind: "repair", Name: "안 켜짐", Blocking: true}.Build())
	x.Equal(codes.PermissionDenied, codeOf(err))
	_, err = e.app().WorkOrder().Add(kim, app.WorkOrderAddRequest_builder{Asset: assetRef(projector), Kind: "repair", Name: "안 켜짐"}.Build())
	x.NoError(err)
	in, err := e.app().Notification().Inbox(e.owner, &app.NotificationInboxRequest{})
	x.NoError(err)
	x.NotZero(in.GetUnread(), "the managers were told")
}

// The second answer to a double booking, on PostgreSQL: written straight to
// the table, around the domain layer, it is still refused.
func TestPostgresBackstop(t *testing.T) {
	e := newEnv(t)
	if e.s.Dialect != "postgres" {
		t.Skip("PostgreSQL only; set PDTEST_POSTGRES")
	}
	room := e.space("회의실", nil)
	ctx := context.Background()
	tx, err := e.s.Ent.Tx(ctx)
	e.x.NoError(err)
	at := e.now.Add(day)
	for range 2 {
		e.x.NoError(tx.Allocation.Create().
			SetId(newOpUUID()).
			SetTenantId(e.tenant.Uuid()).
			SetResourceId(idUUID(room.GetId())).
			SetKind("reservation").
			SetBeginsAt(at).
			SetEndsAt(at.Add(time.Hour)).
			SetBlocking(true).
			SetExclusive(true).
			SetUnits(1).
			SetDateUpdated(e.now).
			Exec(ctx))
	}
	e.x.Error(tx.Commit(), "two blocking allocations of one room at one time")
}
