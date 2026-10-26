package domain_test

import (
	"slices"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/protobuf/types/known/timestamppb"

	app "github.com/lesomnus/rove"
)

// What is voided -- erased as registered by mistake -- is out of use to every
// reservation that booked it, for good (2026-10-26, D47): each says so and
// is told, is neither approved, confirmed nor fulfilled through it, and is
// not let go of -- as of what goes out of use (D43). A pool voided is out of
// use itself, so nothing of it is counted short, and what was in it is the
// other pools' to give.

// erase voids `a` as the owner.
func (e *env) erase(a *app.Asset) {
	e.t.Helper()
	_, err := e.app().Asset().Erase(e.owner, assetRef(a))
	e.x.NoError(err)
}

// TestAVoidedPoolsBookingsAreMarkedAndTold: a pool voided leaves its bookings
// still to be fulfilled marked and their people told -- not short of it, and
// waiting for nothing -- and they are not confirmed or picked up, but are
// cancelled.
func TestAVoidedPoolsBookingsAreMarkedAndTold(t *testing.T) {
	e := newEnv(t)
	x := e.x
	one := e.item("카메라 1", "CM-1", nil, day)
	two := e.item("카메라 2", "CM-2", nil, day)
	e.bookable(one, nil)
	cameras := e.pool("카메라", one, two)
	kim, me := e.person("member")
	boss, _ := e.person("manager")
	at, until := e.tomorrow(9, 0), e.tomorrow(12, 0)

	booked, err := e.take(kim, cameras, 1, at, until)
	x.NoError(err)
	held, err := e.reserve(kim, cameras, at, until, func(r *app.ReservationAddRequest) { r.SetHold(true) })
	x.NoError(err)
	later, err := e.take(kim, cameras, 1, e.tomorrow(13, 0), e.tomorrow(14, 0))
	x.NoError(err)
	e.status(two, "disposed")
	x.True(e.holds(booked, cameras))
	x.False(e.holds(held, cameras), "made last, waits")

	e.erase(cameras)
	for _, r := range []*app.Reservation{booked, held, later} {
		x.Equal([]string{cameras.GetTag() + " group voided"}, e.marks(r), r.GetName())
		x.Empty(e.entry(r).GetConflicts(), "not short of what is no more")
	}
	x.Contains(e.told(kim), "reservation.out_of_use")
	x.Contains(e.told(boss), "reservation.out_of_use")
	x.NotContains(e.told(e.owner), "reservation.out_of_use", "not whoever voided it")

	_, err = e.app().Reservation().Confirm(kim, decision(held))
	x.Equal(codes.FailedPrecondition, codeOf(err), "%v", err)
	x.ErrorContains(err, cameras.GetTag())
	x.ErrorContains(err, "삭제")
	_, err = e.pickup(booked, me, one)
	x.Equal(codes.FailedPrecondition, codeOf(err), "%v", err)
	x.Equal("confirmed", e.statusOf(booked))

	// What made it reservable went with it: it is not listed to book, and a
	// calendar of all there is to book asks nothing of it.
	bs, err := e.app().Bookable().List(e.owner, app.BookableListRequest_builder{Size: 500}.Build())
	x.NoError(err)
	all := []*app.AssetRef{}
	for _, b := range bs.GetItems() {
		x.False(sameId(b.GetAsset().GetId(), cameras.GetId()))
		all = append(all, ref[app.AssetRef](b.GetAsset().GetId()))
	}
	_, err = e.app().Reservation().Calendar(e.owner, app.ReservationCalendarRequest_builder{Resources: all}.Build())
	x.NoError(err)
	// And one that asks for it all the same, having read what there was to
	// book before, is shown its bookings with the rest.
	cal, err := e.app().Reservation().Calendar(e.owner, app.ReservationCalendarRequest_builder{
		Resources: append(all, assetRef(cameras)),
		From:      timestamppb.New(at.Add(-time.Minute)),
		To:        timestamppb.New(until),
	}.Build())
	x.NoError(err)
	x.True(slices.ContainsFunc(cal.GetEntries(), func(v *app.CalendarEntry) bool { return sameId(v.GetReservation().GetId(), booked.GetId()) }))

	// What was in it is its own again, and the sweep takes nothing for it.
	_, err = e.reserve(kim, one, at, until, nil)
	x.NoError(err)
	e.sweep()
	x.False(e.holds(held, cameras))
	e.cancel(kim, booked)
	x.Equal("cancelled", e.statusOf(booked))
}

// TestWhatAVoidedPoolHeldIsThePoolsAroundItsToGive: a member a pool voided
// shared with another pool is the other's to give at once, and the booking
// of the other that waited for it takes it in the void (D44).
func TestWhatAVoidedPoolHeldIsThePoolsAroundItsToGive(t *testing.T) {
	e := newEnv(t)
	x := e.x
	m := e.item("카메라 M", "CM-M", nil, day)
	b := e.item("카메라 B", "CM-B", nil, day)
	cameras := e.pool("카메라", m)
	gear := e.pool("촬영 장비", m, b)
	kim, _ := e.person("member")
	at, until := e.tomorrow(9, 0), e.tomorrow(12, 0)

	first, err := e.take(kim, cameras, 1, at, until)
	x.NoError(err)
	shoot, err := e.take(kim, gear, 1, at, until)
	x.NoError(err)
	e.status(b, "lost")
	x.True(e.holds(first, cameras))
	x.False(e.holds(shoot, gear), "M is the first's")

	e.erase(cameras)
	x.True(e.holds(shoot, gear), "M is the other pool's to give now")
	x.Contains(e.marks(first), cameras.GetTag()+" group voided")
}

// TestWhatIsVoidedIsOutOfUseToWhatBookedIt: of whatever kind -- a space, an
// item, a kit -- what is voided says so on what booked it, and is told, and a
// space is not checked into. What does not book it any more, or is over, or
// out, is not.
func TestWhatIsVoidedIsOutOfUseToWhatBookedIt(t *testing.T) {
	e := newEnv(t)
	x := e.x
	room := e.space("회의실 V", nil)
	e.bookable(room, nil)
	camera := e.item("카메라", "CM-V", nil, day)
	e.bookable(camera, nil)
	tripod := e.item("삼각대", "TR-V", nil, day)
	kit := e.kit("촬영 키트", tripod)
	kim, me := e.person("member")

	talk, err := e.reserve(kim, room, e.now.Add(10*time.Minute), e.now.Add(70*time.Minute), nil)
	x.NoError(err)
	shoot, err := e.reserve(kim, camera, e.tomorrow(9, 0), e.tomorrow(12, 0), nil)
	x.NoError(err)
	out, err := e.reserve(kim, camera, e.now.Add(10*time.Minute), e.now.Add(70*time.Minute), nil)
	x.NoError(err)
	c, err := e.pickup(out, me, camera)
	x.NoError(err)
	trip, err := e.reserve(kim, kit, e.tomorrow(13, 0), e.tomorrow(14, 0), nil)
	x.NoError(err)

	e.erase(room)
	x.Equal([]string{room.GetTag() + " space voided"}, e.marks(talk))
	_, err = e.app().Reservation().CheckIn(kim, decision(talk))
	x.Equal(codes.FailedPrecondition, codeOf(err), "%v", err)

	e.erase(tripod)
	x.Empty(e.marks(trip), "the kit let it go as it left it")
	e.erase(kit)
	x.Equal([]string{kit.GetTag() + " kit voided"}, e.marks(trip))

	_, err = e.app().Custody().Return(e.owner, app.CustodyReturnRequest_builder{Ref: ref[app.CustodyRef](c.GetId())}.Build())
	x.NoError(err)
	e.erase(camera)
	x.Equal([]string{camera.GetTag() + " item voided"}, e.marks(shoot))
	x.Equal("completed", e.statusOf(out), "returned before, and nothing to it")
}
