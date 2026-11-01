package domain_test

import (
	"fmt"
	"testing"
	"time"

	app "github.com/lesomnus/loft"
	"github.com/lesomnus/loft/server/domain"
)

// TestARoomThatEndedIsCompletedHoweverMuchEquipmentIsStillOut: what the
// finished sweep passes over -- equipment still out past its end, which its
// return completes -- fills no page it stops at (loft#279). A room that ended
// behind more of it than a page holds is completed in the same pass, and the
// equipment stays out.
func TestARoomThatEndedIsCompletedHoweverMuchEquipmentIsStillOut(t *testing.T) {
	e := newEnv(t)
	x := e.x
	domain.SetFinishedPage(t, 2)
	start := e.now
	kimCtx, kim := e.person("member")

	// Three loans, booked before the room: identifiers grow, so the room's
	// comes after every one of them, past the first page.
	type loan struct {
		r *app.Reservation
		a *app.Asset
	}
	loans := []loan{}
	for i, name := range []string{"카메라", "렌즈", "삼각대"} {
		a := e.item(name, fmt.Sprintf("FN-%d", i+1), nil, day)
		e.bookable(a, nil)
		r, err := e.reserve(kimCtx, a, start.Add(time.Hour), start.Add(2*time.Hour), named(name+" 대여", false))
		x.NoError(err)
		loans = append(loans, loan{r, a})
	}
	room := e.space("회의실", nil)
	e.bookable(room, nil)
	met, err := e.reserve(kimCtx, room, start.Add(time.Hour), start.Add(2*time.Hour), named("주간 회의", false))
	x.NoError(err)

	// Each picked up and none given back; the room checked in to.
	e.now = start.Add(70 * time.Minute)
	for _, l := range loans {
		_, err := e.pickup(l.r, kim, l.a)
		x.NoError(err)
		x.Equal("in_use", e.reservation(l.r).GetStatus())
	}
	_, err = e.app().Reservation().CheckIn(kimCtx, app.ReservationDecideRequest_builder{Ref: ref[app.ReservationRef](met.GetId())}.Build())
	x.NoError(err)
	x.Equal("in_use", e.reservation(met).GetStatus())

	e.now = start.Add(2*time.Hour + time.Minute)
	e.sweep()
	x.Equal("completed", e.reservation(met).GetStatus(), "behind a page of equipment still out")
	x.Contains(e.kindsOf(met), "reservation.complete")
	for _, l := range loans {
		x.Equal("in_use", e.reservation(l.r).GetStatus(), "out till it is back")
	}
}
