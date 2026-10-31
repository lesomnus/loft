package domain_test

import (
	"context"
	"testing"
	"time"

	"google.golang.org/grpc/codes"

	app "github.com/lesomnus/loft"
	"github.com/lesomnus/loft/internal/ent/allocation"
	"github.com/lesomnus/loft/internal/ent/event"
)

// atOnce runs two transitions of one reservation so that neither has written
// it before both have read it: its row is held from outside until both wait at
// a lock, and then let go. Every way a reservation changes writes its row, so
// what decided on what it read before writing decides twice on the same
// thing, and what holds the row before it reads it decides on what the other
// left.
func (e *env) atOnce(r *app.Reservation, a, b func() error) (error, error) {
	e.t.Helper()
	o := e.holdFromOutside("UPDATE reservation SET date_updated = $1 WHERE id = $2", time.Now(), idUUID(r.GetId()))
	held := true
	release := func() {
		if held {
			held = false
			e.x.NoError(o.tx.Rollback())
		}
	}
	defer release()

	ea, eb := make(chan error, 1), make(chan error, 1)
	go func() { ea <- a() }()
	go func() { eb <- b() }()

	deadline := time.Now().Add(10 * time.Second)
	for {
		n := e.behind(o)
		if n >= 2 {
			break
		}
		if time.Now().After(deadline) {
			release()
			<-ea
			<-eb
			e.x.FailNow("the two never both waited at a lock", "%d did", n)
		}
		time.Sleep(10 * time.Millisecond)
	}
	release()

	return <-ea, <-eb
}

// blocking says whether a reservation's time is held: what another booking of
// its room is refused for.
func (e *env) blocking(r *app.Reservation) bool {
	e.t.Helper()
	n, err := e.s.Ent.Allocation.Query().Where(allocation.RefId(idUUID(r.GetId())), allocation.Blocking(true)).Count(context.Background())
	e.x.NoError(err)
	return n > 0
}

func (e *env) kindsOf(r *app.Reservation) []string {
	e.t.Helper()
	vs, err := e.s.Ent.Event.Query().Where(event.SubjectId(idUUID(r.GetId()))).Select(event.FieldKind).Strings(context.Background())
	e.x.NoError(err)
	return vs
}

// TestACheckInAndTheNoShowSweepAtOnceDoNotBothLand: a check-in in the last
// moment of the grace and the sweep that releases the room as a no-show, at
// once. Each read the reservation as confirmed before either wrote it, and the
// one that wrote second wrote over the first: the person was told they had
// checked in, and the reservation was a no-show with its time free to book. A
// transition decides on what a reservation is once it holds it (loft#35).
func TestACheckInAndTheNoShowSweepAtOnceDoNotBothLand(t *testing.T) {
	e := newEnv(t)
	e.postgresOnly()
	x := e.x
	room := e.space("회의실", nil)
	e.bookable(room, nil)
	kim, _ := e.person("member")

	start := e.now.Add(time.Hour).Truncate(time.Minute)
	r, err := e.reserve(kim, room, start, start.Add(time.Hour), nil)
	x.NoError(err)
	x.Equal("confirmed", r.GetStatus())

	// Past the grace: the sweep would release it, and the check-in is not
	// too late to make.
	e.now = start.Add(20 * time.Minute)
	checkIn, _ := e.atOnce(r,
		func() error {
			_, err := e.app().Reservation().CheckIn(kim, app.ReservationDecideRequest_builder{Ref: ref[app.ReservationRef](r.GetId())}.Build())
			return err
		},
		func() error { e.sweep(); return nil },
	)

	v, err := e.app().Reservation().Get(kim, app.ReservationGetRequest_builder{Ref: ref[app.ReservationRef](r.GetId())}.Build())
	x.NoError(err)
	switch v.GetStatus() {
	case "in_use":
		x.NoError(checkIn)
		x.True(e.blocking(v), "checked in, and its time is free to book")
		x.NotContains(e.kindsOf(v), "reservation.no_show", "checked in, and released as a no-show too")
	case "no_show":
		x.Equal(codes.FailedPrecondition, codeOf(checkIn), "told it checked in, and it is a no-show: %v", checkIn)
		x.False(e.blocking(v))
		x.False(v.HasCheckedInAt(), "a no-show somebody checked into")
	default:
		x.Failf("neither", "%s", v.GetStatus())
	}
}

// TestAConfirmAndARejectAtOnceDoNotBothLand: somebody confirming their hold
// and a manager rejecting it, at once. The confirm took no lock at all, and
// both were told they had done it.
func TestAConfirmAndARejectAtOnceDoNotBothLand(t *testing.T) {
	e := newEnv(t)
	e.postgresOnly()
	x := e.x
	room := e.space("회의실", nil)
	e.bookable(room, nil)
	kim, _ := e.person("member")

	r, err := e.reserve(kim, room, e.tomorrow(10, 0), e.tomorrow(11, 0), func(req *app.ReservationAddRequest) { req.SetHold(true) })
	x.NoError(err)
	x.Equal("held", r.GetStatus())

	confirmed, rejected := e.atOnce(r,
		func() error {
			_, err := e.app().Reservation().Confirm(kim, app.ReservationDecideRequest_builder{Ref: ref[app.ReservationRef](r.GetId())}.Build())
			return err
		},
		func() error {
			_, err := e.app().Reservation().Reject(e.owner, app.ReservationDecideRequest_builder{Ref: ref[app.ReservationRef](r.GetId()), Reason: "회의실 공사"}.Build())
			return err
		},
	)

	v, err := e.app().Reservation().Get(kim, app.ReservationGetRequest_builder{Ref: ref[app.ReservationRef](r.GetId())}.Build())
	x.NoError(err)
	switch v.GetStatus() {
	case "confirmed":
		x.NoError(confirmed)
		x.Equal(codes.FailedPrecondition, codeOf(rejected), "told it rejected, and it is confirmed: %v", rejected)
		x.True(e.blocking(v), "confirmed, and its time is free to book")
	case "rejected":
		x.NoError(rejected)
		x.Equal(codes.FailedPrecondition, codeOf(confirmed), "told it confirmed, and it is rejected: %v", confirmed)
		x.False(e.blocking(v))
	default:
		x.Failf("neither", "%s", v.GetStatus())
	}
}

// TestAPickupAndTheNoShowSweepAtOnceDoNotBothLand: equipment reserved for a
// room's time picked up through a custody as the no-show sweep releases the
// reservation. The pickup took no lock of the reservation's, and the custody
// was handed out against a reservation that was a no-show.
func TestAPickupAndTheNoShowSweepAtOnceDoNotBothLand(t *testing.T) {
	e := newEnv(t)
	e.postgresOnly()
	x := e.x
	room := e.space("회의실", nil)
	e.bookable(room, nil)
	kim, member := e.person("member")
	laptop := e.item("노트북", "NB-1", room, 30*24*time.Hour)

	start := e.now.Add(time.Hour).Truncate(time.Minute)
	r, err := e.reserve(kim, room, start, start.Add(time.Hour), nil)
	x.NoError(err)

	e.now = start.Add(20 * time.Minute)
	picked, _ := e.atOnce(r,
		func() error {
			_, err := e.app().Custody().Add(e.owner, app.CustodyAddRequest_builder{
				Party:         ref[app.PartyRef](member.GetId()),
				Lines:         []*app.CustodyLineSpec{app.CustodyLineSpec_builder{Asset: assetRef(laptop)}.Build()},
				ReservationId: r.GetId(),
			}.Build())
			return err
		},
		func() error { e.sweep(); return nil },
	)

	v, err := e.app().Reservation().Get(kim, app.ReservationGetRequest_builder{Ref: ref[app.ReservationRef](r.GetId())}.Build())
	x.NoError(err)
	switch v.GetStatus() {
	case "in_use":
		x.NoError(picked)
		x.NotContains(e.kindsOf(v), "reservation.no_show", "picked up, and released as a no-show too")
	case "no_show":
		x.Equal(codes.FailedPrecondition, codeOf(picked), "handed out against a no-show: %v", picked)
	default:
		x.Failf("neither", "%s", v.GetStatus())
	}
}
