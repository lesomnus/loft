package domain_test

import (
	"context"
	"slices"
	"testing"
	"time"

	"google.golang.org/protobuf/types/known/timestamppb"

	app "github.com/lesomnus/loft"
	"github.com/lesomnus/loft/server/domain"
)

// What a booking takes that goes out of use before it is picked up -- lost,
// retired, disposed -- is not handed over (design 4, D42): the booking says
// so wherever it is shown, and whoever has to act on it is told in the change
// that put it out of use, and told again once it is in use.

// notices is what `ctx` has been told of `kind`, newest first.
func (e *env) notices(ctx context.Context, kind string) []*app.Notification {
	e.t.Helper()
	in, err := e.app().Notification().Inbox(ctx, app.NotificationInboxRequest_builder{Size: 200}.Build())
	e.x.NoError(err)
	out := []*app.Notification{}
	for _, n := range in.GetItems() {
		if n.GetKind() == kind {
			out = append(out, n)
		}
	}
	return out
}

// about is the one of these notices whose subject is `a`.
func about(ns []*app.Notification, a *app.Asset) *app.Notification {
	for _, n := range ns {
		if sameId(n.GetSubjectId(), a.GetId()) {
			return n
		}
	}
	return nil
}

// gone is what the calendar says `r` takes that is out of use, by tag and
// status.
func (e *env) gone(r *app.Reservation) []string {
	e.t.Helper()
	out := []string{}
	for _, v := range e.entry(r).GetOutOfUse() {
		out = append(out, v.GetResourceTag()+" "+v.GetStatus())
	}
	return out
}

// TestWhatABookingTakesThatGoesOutOfUseIsSaid: a member kept in its kit as it
// goes out of use, and an item booked itself, are said of each booking that
// takes them, and whoever runs the bookings and whoever each is for are told
// -- by mail as well -- but not whoever put it out of use. Out of use another
// way, nobody is told again; in use again, it is said no more, and whoever
// each booking is for is told.
func TestWhatABookingTakesThatGoesOutOfUseIsSaid(t *testing.T) {
	for _, status := range domain.OutOfUse {
		t.Run(status, func(t *testing.T) {
			e := newEnv(t)
			x := e.x
			box := e.mailing()
			camera := e.item("카메라", "CM-1", nil, day)
			battery := e.item("배터리", "BT-1", nil, day)
			e.bookable(camera, nil)
			kit := e.kit("촬영 키트", camera, battery)
			kim, kimP := e.person("member")
			lee, leeP := e.person("member")
			boss, bossP := e.person("manager")

			shoot, err := e.reserve(kim, kit, e.tomorrow(9, 0), e.tomorrow(12, 0), func(r *app.ReservationAddRequest) { r.SetName("제품 촬영") })
			x.NoError(err)
			talk, err := e.reserve(lee, camera, e.tomorrow(14, 0), e.tomorrow(15, 0), func(r *app.ReservationAddRequest) { r.SetName("인터뷰") })
			x.NoError(err)
			x.Empty(e.entry(shoot).GetOutOfUse())

			// Kept in its kit, which its booking still takes it with.
			x.NoError(e.outOfUse(battery, status, nil))
			x.NotNil(e.allocation(shoot, battery), "held as it was")
			x.Equal([]string{"BT-1 " + status}, e.gone(shoot))
			got := e.entry(shoot).GetOutOfUse()[0]
			x.True(sameId(battery.GetId(), got.GetResourceId()))
			x.Equal("배터리", got.GetResourceName())
			x.Empty(e.gone(talk), "what takes something else")
			x.Empty(e.entry(shoot).GetConflicts(), "nothing it waits for")

			mine := e.notices(kim, "reservation.out_of_use")
			x.Len(mine, 1, "whose booking it is")
			x.Contains(mine[0].GetName(), "제품 촬영")
			x.Contains(mine[0].GetDesc(), "BT-1 배터리")
			x.Equal("/reservations?tab=mine", mine[0].GetLink())
			x.True(sameId(shoot.GetId(), mine[0].GetSubjectId()))
			runs := e.notices(boss, "reservation.out_of_use")
			x.Len(runs, 1, "who runs the bookings")
			x.Contains(runs[0].GetName(), "BT-1 배터리")
			x.Contains(runs[0].GetDesc(), "예약 1건")
			x.True(sameId(battery.GetId(), runs[0].GetSubjectId()))
			x.Empty(e.notices(lee, "reservation.out_of_use"), "whose booking does not take it")
			x.Empty(e.notices(e.owner, "reservation.out_of_use"), "not whoever did it")
			e.mail()
			x.Len(box.to(kimP.GetEmail()), 1, "by mail as well")
			x.Len(box.to(bossP.GetEmail()), 1)

			// Booked itself, and taken with the kit as well.
			x.NoError(e.outOfUse(camera, status, nil))
			x.Equal([]string{"BT-1 " + status, "CM-1 " + status}, e.gone(shoot), "by their tags")
			x.Equal([]string{"CM-1 " + status}, e.gone(talk))
			x.Len(e.notices(kim, "reservation.out_of_use"), 2)
			x.Len(e.notices(lee, "reservation.out_of_use"), 1)
			runs = e.notices(boss, "reservation.out_of_use")
			x.Len(runs, 2)
			x.Contains(about(runs, camera).GetDesc(), "예약 2건")

			// Out of use another way: nothing a booking is handed changes.
			other := "disposed"
			if status == other {
				other = "lost"
			}
			x.NoError(e.outOfUse(camera, other, nil))
			x.Equal([]string{"BT-1 " + status, "CM-1 " + other}, e.gone(shoot))
			x.Len(e.notices(kim, "reservation.out_of_use"), 2, "told once")
			x.Len(e.notices(boss, "reservation.out_of_use"), 2)

			// In use again: it is handed over after all.
			e.status(camera, "active")
			x.Equal([]string{"BT-1 " + status}, e.gone(shoot))
			x.Empty(e.gone(talk))
			back := e.notices(lee, "reservation.resolved")
			x.Len(back, 1)
			x.Contains(back[0].GetName(), "인터뷰")
			x.Contains(back[0].GetDesc(), "CM-1 카메라")
			x.Len(e.notices(kim, "reservation.resolved"), 1)
			x.Empty(e.notices(boss, "reservation.resolved"), "nothing left to settle")
			e.mail()
			x.Len(box.to(leeP.GetEmail()), 1, "and in the app only")
		})
	}
}

// TestWhatLeavesItsKitAsItGoesIsNotSaid: what goes out of use and leaves its
// kit is let go of by the kit's booking, which neither says nor is told of
// it; a booking of it itself still is.
func TestWhatLeavesItsKitAsItGoesIsNotSaid(t *testing.T) {
	e := newEnv(t)
	x := e.x
	camera := e.item("카메라", "CM-1", nil, day)
	tripod := e.item("삼각대", "TR-1", nil, day)
	e.bookable(tripod, nil)
	kit := e.kit("촬영 키트", camera, tripod)
	kim, _ := e.person("member")
	lee, _ := e.person("member")

	shoot, err := e.reserve(kim, kit, e.tomorrow(9, 0), e.tomorrow(12, 0), nil)
	x.NoError(err)
	talk, err := e.reserve(lee, tripod, e.tomorrow(14, 0), e.tomorrow(15, 0), nil)
	x.NoError(err)

	x.NoError(e.outOfUse(tripod, "disposed", nil, kit))
	x.Nil(e.allocation(shoot, tripod), "let go of")
	x.Empty(e.gone(shoot))
	x.Empty(e.notices(kim, "reservation.out_of_use"))
	x.Equal([]string{"TR-1 disposed"}, e.gone(talk))
	x.Len(e.notices(lee, "reservation.out_of_use"), 1)
}

// TestEveryWayOutOfUseIsSaid: a count's finding settled as lost puts what it
// found missing out of use as setting its status does, and so does a
// correction of the status a booking's item was put back in use with -- and
// one of the status it went out of use with puts it back.
func TestEveryWayOutOfUseIsSaid(t *testing.T) {
	e := newEnv(t)
	x := e.x
	room := e.space("장비실", nil)
	lens := e.item("렌즈", "LN-1", room, day)
	kit := e.kit("촬영 키트", lens)
	kim, _ := e.person("member")

	shoot, err := e.reserve(kim, kit, e.tomorrow(9, 0), e.tomorrow(12, 0), nil)
	x.NoError(err)

	// A count that never saw it, settled as lost and kept in the kit.
	c, err := e.app().InventoryCount().Add(e.owner, app.InventoryCountAddRequest_builder{Name: "10월 실사", Scope: assetRef(room)}.Build())
	x.NoError(err)
	_, err = e.app().InventoryCount().Reconcile(e.owner, app.InventoryCountReconcileRequest_builder{Ref: ref[app.InventoryCountRef](c.GetId())}.Build())
	x.NoError(err)
	fs, err := e.app().CountFinding().List(e.owner, app.CountFindingListRequest_builder{}.Build())
	x.NoError(err)
	x.Len(fs.GetItems(), 1)
	_, err = e.app().InventoryCount().Resolve(e.owner, app.InventoryCountResolveRequest_builder{Ref: ref[app.CountFindingRef](fs.GetItems()[0].GetId()), Resolution: "lost"}.Build())
	x.NoError(err)
	x.Equal([]string{"LN-1 lost"}, e.gone(shoot))
	x.Len(e.notices(kim, "reservation.out_of_use"), 1)

	// Found, and then said never to have been found: lost again.
	e.now = e.now.Add(time.Minute)
	e.status(lens, "active")
	x.Empty(e.gone(shoot))
	x.Len(e.notices(kim, "reservation.resolved"), 1)
	e.now = e.now.Add(time.Minute)
	x.NoError(e.retractStatus(lens))
	x.Equal("lost", e.get(lens).GetStatus())
	x.Equal([]string{"LN-1 lost"}, e.gone(shoot))
	x.Len(e.notices(kim, "reservation.out_of_use"), 2)

	// And said never to have been lost: in use, as it was before.
	x.NoError(e.retractStatus(lens))
	x.Equal("active", e.get(lens).GetStatus())
	x.Empty(e.gone(shoot))
	x.Len(e.notices(kim, "reservation.resolved"), 2)
}

// retractStatus says the status `a` was last put in never held.
func (e *env) retractStatus(a *app.Asset) error {
	e.t.Helper()
	tl, err := e.app().Asset().Timeline(e.owner, app.AssetTimelineRequest_builder{Ref: assetRef(a)}.Build())
	e.x.NoError(err)
	var row *app.TimelineEntry
	for _, en := range tl.GetEntries() {
		if en.GetKind() != "fact" || en.GetDetail()["key"] != "status" || en.HasSupersededAt() {
			continue
		}
		if row == nil || en.GetValidFrom().AsTime().After(row.GetValidFrom().AsTime()) {
			row = en
		}
	}
	e.x.NotNil(row)
	_, err = e.app().Asset().Correct(e.owner, app.AssetCorrectRequest_builder{Ref: assetRef(a), RowId: row.GetRowId(), Retract: true, Reason: "잘못 적음"}.Build())
	return err
}

// TestWhatABookingWaitsForThatGoesOutOfUseIsSaid: a member a kit's booking
// waits for, which another booking holds, is no more to be handed over once
// it is free: both bookings say it, and both are told -- and once the kit's
// booking takes it, it still says it.
func TestWhatABookingWaitsForThatGoesOutOfUseIsSaid(t *testing.T) {
	e := newEnv(t)
	x := e.x
	camera := e.item("카메라", "CM-1", nil, day)
	tripod := e.item("삼각대", "TR-1", nil, day)
	e.bookable(tripod, nil)
	kit := e.kit("촬영 키트", camera)
	kim, _ := e.person("member")
	lee, _ := e.person("member")

	shoot, err := e.reserve(kim, kit, e.tomorrow(9, 0), e.tomorrow(12, 0), nil)
	x.NoError(err)
	talk, err := e.reserve(lee, tripod, e.tomorrow(10, 0), e.tomorrow(11, 0), nil)
	x.NoError(err)
	e.relate(tripod, kit, true)
	x.Equal("waiting", e.allocation(shoot, tripod).Kind)

	e.status(tripod, "retired")
	x.Equal([]string{"TR-1 retired"}, e.gone(shoot))
	x.Len(e.entry(shoot).GetConflicts(), 1, "and still waits for it")
	x.Equal([]string{"TR-1 retired"}, e.gone(talk))
	x.Len(e.notices(kim, "reservation.out_of_use"), 1)
	x.Len(e.notices(lee, "reservation.out_of_use"), 1)

	// Let go of, it is taken, and is still not to be handed over: which
	// what tells whoever it is for that it was taken says too.
	e.cancel(lee, talk)
	x.Equal("reservation", e.allocation(shoot, tripod).Kind)
	x.Empty(e.entry(shoot).GetConflicts())
	x.Equal([]string{"TR-1 retired"}, e.gone(shoot))
	took := e.notices(kim, "reservation.resolved")
	x.Len(took, 1)
	x.Contains(took[0].GetDesc(), "불용 상태라 수령할 때 건네지 않습니다")
}

// TestWhatNoBookingIsToBeHandedIsNotSaid: a pool's members are none of what
// its bookings take -- one out of use is one fewer of the pool, which its
// conflict says -- and nor is anything of a booking that went out or is over.
// (What a booking booked itself is said whatever it is, a space as well:
// unusable_test.go.)
func TestWhatNoBookingIsToBeHandedIsNotSaid(t *testing.T) {
	e := newEnv(t)
	x := e.x
	one := e.item("빔프로젝터 1", "PJ-1", nil, day)
	two := e.item("빔프로젝터 2", "PJ-2", nil, day)
	projectors := e.pool("빔프로젝터", one, two)
	camera := e.item("카메라", "CM-1", nil, day)
	kit := e.kit("촬영 키트", camera)
	kim, party := e.person("member")

	both, err := e.take(kim, projectors, 2, e.tomorrow(9, 0), e.tomorrow(12, 0))
	x.NoError(err)
	out, err := e.reserve(kim, kit, e.now.Add(10*time.Minute), e.now.Add(3*time.Hour), nil)
	x.NoError(err)
	_, err = e.app().Custody().Add(e.owner, app.CustodyAddRequest_builder{
		Party:         ref[app.PartyRef](party.GetId()),
		DueAt:         timestamppb.New(e.now.Add(3 * time.Hour)),
		ReservationId: out.GetId(),
		Lines:         []*app.CustodyLineSpec{app.CustodyLineSpec_builder{Asset: assetRef(camera)}.Build()},
	}.Build())
	x.NoError(err)

	e.status(two, "lost")
	e.status(camera, "lost")
	x.Empty(e.gone(both))
	x.Len(e.entry(both).GetConflicts(), 1, "the pool has one fewer")
	x.Empty(e.gone(out), "out as it was handed")
	x.Empty(e.notices(kim, "reservation.out_of_use"))
	x.True(slices.Contains(e.told(kim), "reservation.conflict"))
}
