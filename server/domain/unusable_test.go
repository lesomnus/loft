package domain_test

import (
	"context"
	"fmt"
	"slices"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/protobuf/types/known/timestamppb"

	app "github.com/lesomnus/loft"
	"github.com/lesomnus/loft/server/domain"
)

// What is out of use -- lost, retired, disposed -- is not booked anew,
// whatever it is (2026-10-26, design 4, D43): an item, a space, a kit, a
// group. A reservation made of it before is not approved or confirmed, nor
// fulfilled through it -- a space is not checked into, a kit or a group not
// picked up -- and it says what it booked that is out of use, as it says an
// item it takes is (D42), and whoever has to act on it is told.

// marks is what the calendar says `r` takes that is out of use, by tag, kind
// and status.
func (e *env) marks(r *app.Reservation) []string {
	e.t.Helper()
	out := []string{}
	for _, v := range e.entry(r).GetOutOfUse() {
		out = append(out, v.GetResourceTag()+" "+v.GetResourceKind()+" "+v.GetStatus())
	}
	return out
}

// written is how many reservations, allocations, events and notices the
// deployment has.
func (e *env) written() [4]int {
	e.t.Helper()
	ctx := context.Background()
	rs, err := e.s.Ent.Reservation.Query().Count(ctx)
	e.x.NoError(err)
	as, err := e.s.Ent.Allocation.Query().Count(ctx)
	e.x.NoError(err)
	es, err := e.s.Ent.Event.Query().Count(ctx)
	e.x.NoError(err)
	ns, err := e.s.Ent.Notification.Query().Count(ctx)
	e.x.NoError(err)
	return [4]int{rs, as, es, ns}
}

func decision(r *app.Reservation) *app.ReservationDecideRequest {
	return app.ReservationDecideRequest_builder{Ref: ref[app.ReservationRef](r.GetId())}.Build()
}

// statusOf is what `r` is now.
func (e *env) statusOf(r *app.Reservation) string {
	e.t.Helper()
	v, err := e.app().Reservation().Get(e.owner, app.ReservationGetRequest_builder{Ref: ref[app.ReservationRef](r.GetId())}.Build())
	e.x.NoError(err)
	return v.GetStatus()
}

var statusSaid = map[string]string{"lost": "분실", "retired": "불용", "disposed": "폐기"}

// TestWhatIsOutOfUseIsNotBookedAnew: an item, a space, a kit or a group that
// is lost, retired or disposed is refused a booking -- a series of it as well,
// and asked for with what is in use, the whole of it -- each of it named with
// its status, and the refusal writes nothing. What is ordered or in repair is
// in use; and a kit whose member is out of use is booked, and says so.
func TestWhatIsOutOfUseIsNotBookedAnew(t *testing.T) {
	for _, status := range domain.OutOfUse {
		t.Run(status, func(t *testing.T) {
			e := newEnv(t)
			x := e.x
			camera := e.item("카메라", "CM-1", nil, day)
			e.bookable(camera, nil)
			room := e.space("회의실", nil)
			e.bookable(room, nil)
			lens := e.item("렌즈", "LN-1", nil, day)
			kit := e.kit("촬영 키트", lens)
			projectors := e.pool("빔프로젝터", e.item("빔프로젝터 1", "PJ-1", nil, day))
			tripod := e.item("삼각대", "TR-1", nil, day)
			e.bookable(tripod, nil)
			kim, _ := e.person("member")

			things := []*app.Asset{camera, room, kit, projectors}
			for _, a := range things {
				e.status(a, status)
			}
			named := func(a *app.Asset) string {
				return fmt.Sprintf("%s %s(%s)", a.GetTag(), a.GetName(), statusSaid[status])
			}
			before := e.written()
			for _, a := range things {
				_, err := e.reserve(kim, a, e.tomorrow(9, 0), e.tomorrow(10, 0), nil)
				x.Equal(codes.FailedPrecondition, codeOf(err), "%s: %v", a.GetName(), err)
				x.ErrorContains(err, "쓸 수 없는 것은 새로 예약할 수 없습니다: "+named(a))
			}
			_, err := e.reserve(kim, camera, e.tomorrow(9, 0), e.tomorrow(10, 0), func(r *app.ReservationAddRequest) { r.SetRepeat("FREQ=WEEKLY;COUNT=4") })
			x.Equal(codes.FailedPrecondition, codeOf(err), "a series of it: %v", err)
			_, err = e.reserve(kim, tripod, e.tomorrow(9, 0), e.tomorrow(10, 0), func(r *app.ReservationAddRequest) {
				r.SetItems(append(r.GetItems(),
					app.ReservationItemSpec_builder{Resource: assetRef(camera)}.Build(),
					app.ReservationItemSpec_builder{Resource: assetRef(room)}.Build(),
				))
			})
			x.Equal(codes.FailedPrecondition, codeOf(err), "%v", err)
			x.ErrorContains(err, "쓸 수 없는 것은 새로 예약할 수 없습니다: "+named(camera)+", "+named(room), "each of what is out of use, as it was asked for")
			x.NotContains(err.Error(), "TR-1", "and nothing in use")
			x.Equal(before, e.written(), "a refusal writes nothing")

			// Ordered, or in repair -- whose time its work order blocks -- it
			// is in use.
			for _, v := range []string{"ordered", "in_repair"} {
				e.status(tripod, v)
				r, err := e.reserve(kim, tripod, e.tomorrow(11, 0), e.tomorrow(12, 0), nil)
				x.NoError(err, v)
				e.cancel(kim, r)
			}

			// A kit in use whose member is not is booked, and says so.
			e.status(kit, "active")
			e.status(lens, status)
			r, err := e.reserve(kim, kit, e.tomorrow(13, 0), e.tomorrow(14, 0), nil)
			x.NoError(err)
			x.Equal([]string{"LN-1 item " + status}, e.marks(r))
		})
	}
}

// TestWhatWentOutOfUseIsNotSaidYesToNorUsed: a reservation of what went out
// of use since it was made is not approved -- one at a time or its series --
// nor confirmed from a hold, which is booking it; nor checked into, until it
// is in use again. It is rejected and cancelled, and what is refused writes
// nothing.
func TestWhatWentOutOfUseIsNotSaidYesToNorUsed(t *testing.T) {
	e := newEnv(t)
	x := e.x
	hall := e.space("대회의실", nil)
	e.bookable(hall, func(b *app.BookableAddRequest) { b.SetApproval(true) })
	room := e.space("회의실", nil)
	e.bookable(room, nil)
	camera := e.item("카메라", "CM-1", nil, day)
	e.bookable(camera, nil)
	kim, _ := e.person("member")
	boss, _ := e.person("manager")

	// Asked for, alone and as a series, and the hall goes out of use.
	asked, err := e.reserve(kim, hall, e.tomorrow(9, 0), e.tomorrow(10, 0), nil)
	x.NoError(err)
	x.Equal("requested", asked.GetStatus())
	weekly, err := e.reserve(kim, hall, e.tomorrow(11, 0), e.tomorrow(12, 0), func(r *app.ReservationAddRequest) { r.SetRepeat("FREQ=WEEKLY;COUNT=3") })
	x.NoError(err)
	e.status(hall, "retired")
	before := e.written()
	_, err = e.app().Reservation().Approve(boss, decision(asked))
	x.Equal(codes.FailedPrecondition, codeOf(err), "%v", err)
	x.ErrorContains(err, fmt.Sprintf("승인할 수 없습니다: %s 대회의실(불용)", hall.GetTag()))
	series := decision(weekly)
	series.SetSeries(true)
	_, err = e.app().Reservation().Approve(boss, series)
	x.Equal(codes.FailedPrecondition, codeOf(err), "%v", err)
	x.Equal(before, e.written(), "a refusal writes nothing")
	x.Equal("requested", e.statusOf(asked))
	_, err = e.app().Reservation().Reject(boss, decision(asked))
	x.NoError(err)
	_, err = e.app().Reservation().Cancel(kim, series)
	x.NoError(err)

	// Held, and the camera is lost: the hold is not confirmed.
	held, err := e.reserve(kim, camera, e.tomorrow(9, 0), e.tomorrow(10, 0), func(r *app.ReservationAddRequest) { r.SetHold(true) })
	x.NoError(err)
	e.status(camera, "lost")
	_, err = e.app().Reservation().Confirm(kim, decision(held))
	x.Equal(codes.FailedPrecondition, codeOf(err), "%v", err)
	x.ErrorContains(err, "확정할 수 없습니다: CM-1 카메라(분실)")
	x.Equal("held", e.statusOf(held))
	e.cancel(kim, held)

	// Booked for now, and the room is disposed of: it is not checked into,
	// until it is in use again.
	soon, err := e.reserve(kim, room, e.now.Add(10*time.Minute), e.now.Add(70*time.Minute), nil)
	x.NoError(err)
	e.status(room, "disposed")
	_, err = e.app().Reservation().CheckIn(kim, decision(soon))
	x.Equal(codes.FailedPrecondition, codeOf(err), "%v", err)
	x.ErrorContains(err, fmt.Sprintf("체크인할 수 없습니다: %s 회의실(폐기)", room.GetTag()))
	x.Equal("confirmed", e.statusOf(soon))
	e.status(room, "active")
	_, err = e.app().Reservation().CheckIn(kim, decision(soon))
	x.NoError(err)
	x.Equal("in_use", e.statusOf(soon))
	x.Empty(e.eventsOf("reservation.approve"))
	x.Empty(e.eventsOf("reservation.confirm"))
}

// TestAKitOrAGroupOutOfUseIsNotPickedUp: what a pickup hands over for a kit
// or a pool is what it is made of, and a reservation of one out of use is not
// picked up -- whatever its members are -- until it is in use again.
func TestAKitOrAGroupOutOfUseIsNotPickedUp(t *testing.T) {
	e := newEnv(t)
	x := e.x
	camera := e.item("카메라", "CM-1", nil, day)
	kit := e.kit("촬영 키트", camera)
	one := e.item("빔프로젝터 1", "PJ-1", nil, day)
	projectors := e.pool("빔프로젝터", one)
	kim, party := e.person("member")

	shoot, err := e.reserve(kim, kit, e.now.Add(10*time.Minute), e.now.Add(3*time.Hour), nil)
	x.NoError(err)
	talk, err := e.reserve(kim, projectors, e.now.Add(10*time.Minute), e.now.Add(3*time.Hour), nil)
	x.NoError(err)
	pickup := func(r *app.Reservation, a *app.Asset) error {
		_, err := e.app().Custody().Add(e.owner, app.CustodyAddRequest_builder{
			Party:         ref[app.PartyRef](party.GetId()),
			DueAt:         timestamppb.New(e.now.Add(3 * time.Hour)),
			ReservationId: r.GetId(),
			Lines:         []*app.CustodyLineSpec{app.CustodyLineSpec_builder{Asset: assetRef(a)}.Build()},
		}.Build())
		return err
	}

	e.status(kit, "retired")
	e.status(projectors, "lost")
	err = pickup(shoot, camera)
	x.Equal(codes.FailedPrecondition, codeOf(err), "%v", err)
	x.ErrorContains(err, fmt.Sprintf("수령할 수 없습니다: %s 촬영 키트(불용)", kit.GetTag()))
	err = pickup(talk, one)
	x.Equal(codes.FailedPrecondition, codeOf(err), "%v", err)
	x.ErrorContains(err, fmt.Sprintf("수령할 수 없습니다: %s 빔프로젝터(분실)", projectors.GetTag()))
	x.Equal("confirmed", e.statusOf(shoot))
	x.Nil(e.get(camera).GetCustodian(), "nothing handed over")

	e.status(kit, "active")
	x.NoError(pickup(shoot, camera))
	x.Equal("in_use", e.statusOf(shoot))
}

// TestWhatABookingBookedThatGoesOutOfUseIsSaid: a space, a kit or a group
// that goes out of use is said of each booking of it -- each of a series --
// with what it is; whoever runs the bookings is told once, and whoever they
// are for once for theirs, of what it keeps them from, by mail as well; and
// neither is told it is short as well. In use again, it is said no more, and
// whoever the bookings are for is told.
func TestWhatABookingBookedThatGoesOutOfUseIsSaid(t *testing.T) {
	for _, kind := range []string{"space", "kit", "group"} {
		t.Run(kind, func(t *testing.T) {
			e := newEnv(t)
			x := e.x
			box := e.mailing()
			var thing *app.Asset
			cannot, can := "수령할 수 없습니다", "수령할 수 있습니다"
			switch kind {
			case "space":
				thing = e.space("회의실", nil)
				e.bookable(thing, nil)
				cannot, can = "체크인할 수 없습니다", "체크인할 수 있습니다"
			case "kit":
				thing = e.kit("촬영 키트", e.item("카메라", "CM-1", nil, day))
			case "group":
				thing = e.pool("빔프로젝터", e.item("빔프로젝터 1", "PJ-1", nil, day))
			}
			kim, kimP := e.person("member")
			lee, _ := e.person("member")
			boss, bossP := e.person("manager")

			talk, err := e.reserve(kim, thing, e.tomorrow(9, 0), e.tomorrow(10, 0), func(r *app.ReservationAddRequest) { r.SetName("주간 회의") })
			x.NoError(err)
			weekly, err := e.reserve(kim, thing, e.tomorrow(13, 0), e.tomorrow(14, 0), func(r *app.ReservationAddRequest) { r.SetRepeat("FREQ=WEEKLY;COUNT=3") })
			x.NoError(err)
			x.Empty(e.marks(talk))

			e.status(thing, "retired")
			said := []string{thing.GetTag() + " " + kind + " retired"}
			x.Equal(said, e.marks(talk))
			x.Equal(said, e.marks(weekly), "and each of a series")
			x.Empty(e.entry(talk).GetConflicts())

			mine := e.notices(kim, "reservation.out_of_use")
			x.Len(mine, 1, "once for all of theirs")
			x.Contains(mine[0].GetName(), "주간 회의 외 3건")
			x.Contains(mine[0].GetDesc(), thing.GetTag()+" "+thing.GetName()+"은(는) 불용 상태라 "+cannot)
			runs := e.notices(boss, "reservation.out_of_use")
			x.Len(runs, 1)
			x.Contains(runs[0].GetName(), thing.GetTag())
			x.Contains(runs[0].GetDesc(), "예약 4건은 "+cannot)
			x.Contains(runs[0].GetDesc(), "새로 예약할 수도 없고")
			if kind == "space" {
				x.Contains(mine[0].GetName(), "예약 공간 불용")
				x.Contains(runs[0].GetName(), "예약된 공간 불용")
			}
			x.Empty(e.notices(lee, "reservation.out_of_use"), "whose booking it is not")
			x.Empty(e.notices(e.owner, "reservation.out_of_use"), "not whoever did it")
			x.False(slices.Contains(e.told(kim), "reservation.conflict"), "and not short as well")
			e.mail()
			x.Len(box.to(kimP.GetEmail()), 1, "by mail as well")
			x.Len(box.to(bossP.GetEmail()), 1)

			e.status(thing, "active")
			x.Empty(e.marks(talk))
			back := e.notices(kim, "reservation.resolved")
			x.Len(back, 1)
			x.Contains(back[0].GetDesc(), "이제 사용중 상태라 "+can)
			x.Empty(e.notices(boss, "reservation.resolved"), "nothing left to settle")
		})
	}
}

// TestAPoolOutOfUseIsNotCountedShort: the bookings of a pool that goes out of
// use are said to take what is out of use, and not counted short of it as
// well: one that waited is said not to, takes nothing that frees up, and
// nothing that goes out of the pool makes any wait -- until it is in use
// again, which counts it as it is then.
func TestAPoolOutOfUseIsNotCountedShort(t *testing.T) {
	e := newEnv(t)
	x := e.x
	one := e.item("빔프로젝터 1", "PJ-1", nil, day)
	two := e.item("빔프로젝터 2", "PJ-2", nil, day)
	projectors := e.pool("빔프로젝터", one, two)
	kim, _ := e.person("member")
	lee, _ := e.person("member")

	first, err := e.take(kim, projectors, 1, e.tomorrow(9, 0), e.tomorrow(12, 0))
	x.NoError(err)
	second, err := e.take(lee, projectors, 1, e.tomorrow(9, 0), e.tomorrow(12, 0))
	x.NoError(err)

	// Out of use itself: both said to be, and no member that goes out of it
	// then makes one wait.
	e.status(projectors, "retired")
	said := []string{projectors.GetTag() + " group retired"}
	x.Equal(said, e.marks(first))
	x.Equal(said, e.marks(second))
	e.status(two, "lost")
	x.True(e.holds(second, projectors), "nothing is counted of it")
	x.Empty(e.entry(second).GetConflicts())
	x.False(slices.Contains(e.told(lee), "reservation.conflict"))

	// In use again, it is counted as it is: one has to give, and the booking
	// made last waits for it -- told so -- and is said to be short.
	e.status(projectors, "active")
	x.Empty(e.marks(second))
	x.False(e.holds(second, projectors))
	x.Len(e.entry(second).GetConflicts(), 1)
	x.True(slices.Contains(e.told(lee), "reservation.conflict"))

	// Out of use again while it waits: said to be out of use, and not short;
	// what frees up meanwhile it does not take, nor does the sweep.
	e.status(projectors, "disposed")
	x.Empty(e.entry(second).GetConflicts(), "said to be out of use, and not short as well")
	x.Equal([]string{projectors.GetTag() + " group disposed"}, e.marks(second))
	e.cancel(kim, first)
	x.False(e.holds(second, projectors))
	e.sweep()
	x.False(e.holds(second, projectors), "nor the sweep")

	// And in use again, it takes what there is.
	e.status(projectors, "active")
	x.True(e.holds(second, projectors))
	x.Empty(e.entry(second).GetConflicts())
}

// TestABookingAndGoingOutOfUseAtOnceAreOneOrTheOther: a booking of a thing and
// its going out of use, at once, are one after the other whichever goes
// first -- each holds the thing's schedule before it reads the other's -- so
// the booking is refused, or it is made and whoever it is for is told.
func TestABookingAndGoingOutOfUseAtOnceAreOneOrTheOther(t *testing.T) {
	e := newEnv(t)
	e.postgresOnly()
	x := e.x
	camera := e.item("카메라", "CM-1", nil, day)
	e.bookable(camera, nil)
	kim, _ := e.person("member")
	book := func(h int) func() error {
		return func() error {
			_, err := e.reserve(kim, camera, e.tomorrow(h, 0), e.tomorrow(h+1, 0), nil)
			return err
		}
	}
	retire := func() error { return e.outOfUse(camera, "retired", nil) }

	booked, retired := e.inTurn(camera, book(9), retire)
	x.NoError(booked, "booked first")
	x.NoError(retired)
	x.Len(e.notices(kim, "reservation.out_of_use"), 1, "and told")

	e.status(camera, "active")
	retired, booked = e.inTurn(camera, retire, book(11))
	x.NoError(retired)
	x.Equal(codes.FailedPrecondition, codeOf(booked), "retired first: %v", booked)
}
