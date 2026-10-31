package domain_test

import (
	"context"
	"slices"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/protobuf/types/known/timestamppb"

	app "github.com/lesomnus/loft"
	"github.com/lesomnus/loft/internal/ent"
	"github.com/lesomnus/loft/internal/ent/allocation"
)

// What a change to what a kit is made of does to the kit's bookings (design
// 4): each one still to be fulfilled takes what the kit was given and lets go
// of what it no longer needs, and waits for a member something else holds
// then -- the change is not refused for it -- until the member is free.

// kit is a kit that may be reserved, with these members it needs.
func (e *env) kit(name string, members ...*app.Asset) *app.Asset {
	e.t.Helper()
	v, err := e.app().Asset().Add(e.owner, app.AssetAddRequest_builder{Name: name, Kind: "kit"}.Build())
	e.x.NoError(err)
	for _, m := range members {
		e.relate(m, v, true)
	}
	e.bookable(v, nil)
	return v
}

// relate makes `member` one of `kit`'s from now on, needed or not.
func (e *env) relate(member, kit *app.Asset, required bool) {
	e.t.Helper()
	_, err := e.app().Asset().Relate(e.owner, app.AssetRelateRequest_builder{Ref: assetRef(member), Target: assetRef(kit), Kind: "member_of", Required: required}.Build())
	e.x.NoError(err)
}

// unrelate takes `member` off `kit` from now on.
func (e *env) unrelate(member, kit *app.Asset) {
	e.t.Helper()
	_, err := e.app().Asset().Relate(e.owner, app.AssetRelateRequest_builder{Ref: assetRef(member), Target: assetRef(kit), Kind: "member_of", End: true}.Build())
	e.x.NoError(err)
}

// allocation is what a reservation holds of a thing, if anything.
func (e *env) allocation(r *app.Reservation, a *app.Asset) *ent.Allocation {
	e.t.Helper()
	vs, err := e.s.Ent.Allocation.Query().Where(allocation.RefId(idUUID(r.GetId())), allocation.ResourceId(idUUID(a.GetId()))).All(context.Background())
	e.x.NoError(err)
	e.x.LessOrEqual(len(vs), 1, "one allocation of a thing for a reservation")
	if len(vs) == 0 {
		return nil
	}
	return vs[0]
}

// entry is a reservation as the calendar answers it.
func (e *env) entry(r *app.Reservation) *app.CalendarEntry {
	e.t.Helper()
	v, err := e.app().Reservation().Calendar(e.owner, app.ReservationCalendarRequest_builder{
		From: timestamppb.New(r.GetBeginsAt().AsTime().Add(-time.Minute)),
		To:   timestamppb.New(r.GetEndsAt().AsTime().Add(time.Minute)),
	}.Build())
	e.x.NoError(err)
	for _, en := range v.GetEntries() {
		if sameId(en.GetReservation().GetId(), r.GetId()) {
			return en
		}
	}
	e.x.FailNow("not on the calendar", r.GetName())
	return nil
}

// told is the kinds of what `ctx` has been told, newest first.
func (e *env) told(ctx context.Context) []string {
	e.t.Helper()
	in, err := e.app().Notification().Inbox(ctx, &app.NotificationInboxRequest{})
	e.x.NoError(err)
	out := []string{}
	for _, n := range in.GetItems() {
		out = append(out, n.GetKind())
	}
	return out
}

func (e *env) cancel(ctx context.Context, r *app.Reservation) {
	e.t.Helper()
	_, err := e.app().Reservation().Cancel(ctx, app.ReservationDecideRequest_builder{Ref: ref[app.ReservationRef](r.GetId())}.Build())
	e.x.NoError(err)
}

// TestAMemberGivenToABookedKitIsTakenWithIt: a booking of a kit takes what
// the kit was given after it was made, as a booking made then would have, and
// lets go of a member the kit no longer needs.
func TestAMemberGivenToABookedKitIsTakenWithIt(t *testing.T) {
	e := newEnv(t)
	x := e.x
	camera := e.item("카메라", "CM-1", nil, day)
	tripod := e.item("삼각대", "TR-1", nil, day)
	e.bookable(camera, nil)
	e.bookable(tripod, nil)
	kit := e.kit("촬영 키트", camera)
	kim, _ := e.person("member")

	r, err := e.reserve(kim, kit, e.tomorrow(9, 0), e.tomorrow(12, 0), nil)
	x.NoError(err)
	x.Nil(e.allocation(r, tripod))

	e.relate(tripod, kit, true)
	got := e.allocation(r, tripod)
	x.NotNil(got, "taken with the kit")
	x.True(got.Blocking)
	x.Equal("reservation", got.Kind)
	x.Empty(e.entry(r).GetConflicts())
	_, err = e.reserve(kim, tripod, e.tomorrow(10, 0), e.tomorrow(11, 0), nil)
	x.Equal(codes.Aborted, codeOf(err), "it goes with the kit then")

	// Made a member the kit can go without: the booking lets it go.
	e.relate(tripod, kit, false)
	x.Nil(e.allocation(r, tripod))
	_, err = e.reserve(kim, tripod, e.tomorrow(10, 0), e.tomorrow(11, 0), nil)
	x.NoError(err, "free once the kit no longer needs it")
	x.NotNil(e.allocation(r, camera), "what the kit still needs is still taken")
}

// TestAMemberTakenOffABookedKitIsLetGo: every way a member stops being one a
// kit needs lets a booking of the kit go of it -- the relation ended, said
// never to have held, or the member voided.
func TestAMemberTakenOffABookedKitIsLetGo(t *testing.T) {
	for _, how := range []string{"end", "retract", "void"} {
		t.Run(how, func(t *testing.T) {
			e := newEnv(t)
			x := e.x
			camera := e.item("카메라", "CM-1", nil, day)
			lens := e.item("렌즈", "LN-1", nil, day)
			e.bookable(lens, nil)
			kit := e.kit("촬영 키트", camera, lens)

			r, err := e.reserve(e.owner, kit, e.tomorrow(9, 0), e.tomorrow(12, 0), nil)
			x.NoError(err)
			x.NotNil(e.allocation(r, lens))

			switch how {
			case "end":
				e.unrelate(lens, kit)
			case "retract":
				tl, err := e.app().Asset().Timeline(e.owner, app.AssetTimelineRequest_builder{Ref: assetRef(lens)}.Build())
				x.NoError(err)
				i := slices.IndexFunc(tl.GetEntries(), func(en *app.TimelineEntry) bool { return en.GetKind() == "link" })
				x.GreaterOrEqual(i, 0)
				_, err = e.app().Asset().Correct(e.owner, app.AssetCorrectRequest_builder{
					Ref: assetRef(lens), RowId: tl.GetEntries()[i].GetRowId(), Retract: true, Reason: "다른 키트의 렌즈",
				}.Build())
				x.NoError(err)
			case "void":
				_, err := e.app().Asset().Erase(e.owner, assetRef(lens))
				x.NoError(err)
			}
			x.Nil(e.allocation(r, lens), "let go of")
			x.NotNil(e.allocation(r, camera), "and nothing else")
			if how != "void" {
				_, err = e.reserve(e.owner, lens, e.tomorrow(10, 0), e.tomorrow(11, 0), nil)
				x.NoError(err, "free then")
			}
		})
	}
}

// TestAMemberBookedElsewhereIsWaitedFor: a member the kit is given that is
// booked for the time already is not taken from that booking, and the change
// is not refused for it. The kit's booking waits for it, which whoever runs
// the kits and whoever it is for are told and the calendar shows, and takes it
// once the other lets it go.
func TestAMemberBookedElsewhereIsWaitedFor(t *testing.T) {
	e := newEnv(t)
	x := e.x
	camera := e.item("카메라", "CM-1", nil, day)
	tripod := e.item("삼각대", "TR-1", nil, day)
	e.bookable(tripod, nil)
	kit := e.kit("촬영 키트", camera)
	kim, _ := e.person("member")
	lee, _ := e.person("member")
	boss, _ := e.person("manager")

	r, err := e.reserve(kim, kit, e.tomorrow(9, 0), e.tomorrow(12, 0), func(req *app.ReservationAddRequest) { req.SetName("제품 촬영") })
	x.NoError(err)
	other, err := e.reserve(lee, tripod, e.tomorrow(10, 0), e.tomorrow(11, 0), func(req *app.ReservationAddRequest) { req.SetName("인터뷰") })
	x.NoError(err)

	e.relate(tripod, kit, true)
	got := e.allocation(r, tripod)
	x.NotNil(got)
	x.Equal("waiting", got.Kind)
	x.False(got.Blocking, "the time is the other booking's")

	cs := e.entry(r).GetConflicts()
	x.Len(cs, 1)
	x.Equal(tripod.GetId(), cs[0].GetResourceId())
	x.Equal("TR-1", cs[0].GetResourceTag())
	x.Len(cs[0].GetHeldBy(), 1)
	x.Equal("인터뷰", cs[0].GetHeldBy()[0].GetLabel())
	x.Equal(other.GetId(), cs[0].GetHeldBy()[0].GetRefId())
	x.Empty(e.entry(other).GetConflicts(), "the booking that holds it waits for nothing")

	x.Contains(e.told(kim), "reservation.conflict", "whose booking it is")
	x.Contains(e.told(boss), "reservation.conflict", "who runs the kits")
	x.NotContains(e.told(lee), "reservation.conflict", "whose booking holds it")

	busy, err := e.app().Bookable().Availability(e.owner, app.BookableAvailabilityRequest_builder{
		Resources: []*app.AssetRef{assetRef(tripod)},
		From:      timestamppb.New(e.tomorrow(0, 0)),
		To:        timestamppb.New(e.tomorrow(23, 0)),
	}.Build())
	x.NoError(err)
	kinds := []string{}
	for _, b := range busy.GetBusy() {
		kinds = append(kinds, b.GetKind()+" "+b.GetLabel())
	}
	x.ElementsMatch([]string{"reservation 인터뷰", "waiting 제품 촬영"}, kinds)

	// It goes to the kit's booking as soon as it is let go.
	e.cancel(lee, other)
	got = e.allocation(r, tripod)
	x.Equal("reservation", got.Kind)
	x.True(got.Blocking)
	x.True(got.BeginsAt.Equal(e.tomorrow(9, 0)), "for all of it: %s", got.BeginsAt)
	x.Empty(e.entry(r).GetConflicts())
	x.Contains(e.told(kim), "reservation.resolved")
	_, err = e.reserve(lee, tripod, e.tomorrow(10, 0), e.tomorrow(11, 0), nil)
	x.Equal(codes.Aborted, codeOf(err), "the kit's booking has it now")
}

// TestWhatNothingLetGoOfIsTakenByTheSweep: a member whose schedule the
// operation that freed it did not hold -- one nobody may reserve on its own,
// taken by another kit's booking; one that was being repaired -- is taken by
// the sweep.
func TestWhatNothingLetGoOfIsTakenByTheSweep(t *testing.T) {
	e := newEnv(t)
	x := e.x
	battery := e.item("배터리", "BT-1", nil, day)
	light := e.item("조명", "LT-1", nil, day)
	e.bookable(light, nil)
	kit := e.kit("촬영 키트")
	spare := e.kit("예비 키트", battery)
	kim, _ := e.person("member")

	r, err := e.reserve(kim, kit, e.tomorrow(9, 0), e.tomorrow(12, 0), nil)
	x.NoError(err)
	other, err := e.reserve(e.owner, spare, e.tomorrow(8, 0), e.tomorrow(10, 0), nil)
	x.NoError(err)
	w, err := e.app().WorkOrder().Add(e.owner, app.WorkOrderAddRequest_builder{
		Asset:    assetRef(light),
		Kind:     "repair",
		Name:     "전원 불량",
		BeginsAt: timestamppb.New(e.tomorrow(7, 0)),
		EndsAt:   timestamppb.New(e.tomorrow(11, 0)),
		Blocking: true,
	}.Build())
	x.NoError(err)

	e.relate(battery, kit, true)
	e.relate(light, kit, true)
	x.Equal("waiting", e.allocation(r, battery).Kind)
	x.Equal("waiting", e.allocation(r, light).Kind)
	x.Len(e.entry(r).GetConflicts(), 2)

	e.cancel(e.owner, other)
	_, err = e.app().WorkOrder().Complete(e.owner, app.WorkOrderCompleteRequest_builder{Ref: ref[app.WorkOrderRef](w.GetId()), Condition: "good"}.Build())
	x.NoError(err)
	x.Equal("waiting", e.allocation(r, battery).Kind, "nobody held its schedule")
	x.Len(e.entry(r).GetConflicts(), 2)
	for _, c := range e.entry(r).GetConflicts() {
		x.Empty(c.GetHeldBy(), "nothing holds it any more")
	}

	e.sweep()
	x.Equal("reservation", e.allocation(r, battery).Kind)
	x.Equal("reservation", e.allocation(r, light).Kind)
	x.Empty(e.entry(r).GetConflicts())
}

// TestABookingFulfilledIsLeftAsItWasHandedOut: once a kit is picked up or
// checked into, a change to it is no change to what went out -- a member
// given to it then did not go with it, and one taken off it is out until it
// comes back.
func TestABookingFulfilledIsLeftAsItWasHandedOut(t *testing.T) {
	e := newEnv(t)
	x := e.x
	camera := e.item("카메라", "CM-1", nil, day)
	tripod := e.item("삼각대", "TR-1", nil, day)
	e.bookable(camera, nil)
	e.bookable(tripod, nil)
	kit := e.kit("촬영 키트", camera)
	kim, party := e.person("member")

	r, err := e.reserve(kim, kit, e.now.Add(10*time.Minute), e.now.Add(3*time.Hour), nil)
	x.NoError(err)
	_, err = e.app().Custody().Add(e.owner, app.CustodyAddRequest_builder{
		Party:         ref[app.PartyRef](party.GetId()),
		DueAt:         timestamppb.New(e.now.Add(3 * time.Hour)),
		ReservationId: r.GetId(),
		Lines:         []*app.CustodyLineSpec{app.CustodyLineSpec_builder{Asset: assetRef(camera)}.Build()},
	}.Build())
	x.NoError(err)

	e.relate(tripod, kit, true)
	x.Nil(e.allocation(r, tripod), "not handed out with it")
	_, err = e.reserve(kim, tripod, e.now.Add(time.Hour), e.now.Add(2*time.Hour), nil)
	x.NoError(err)

	e.unrelate(camera, kit)
	x.NotNil(e.allocation(r, camera), "out with the kit")
	_, err = e.reserve(kim, camera, e.now.Add(time.Hour), e.now.Add(2*time.Hour), nil)
	x.Equal(codes.Aborted, codeOf(err))
}

// TestABookingBegunTakesAMemberFromNowOn: equipment booked from an hour ago
// and not picked up yet takes what its kit was given for what is left of it.
func TestABookingBegunTakesAMemberFromNowOn(t *testing.T) {
	e := newEnv(t)
	x := e.x
	tripod := e.item("삼각대", "TR-1", nil, day)
	e.bookable(tripod, nil)
	kit := e.kit("촬영 키트")

	r, err := e.reserve(e.owner, kit, e.now.Add(-time.Hour), e.now.Add(2*time.Hour), nil)
	x.NoError(err)
	e.relate(tripod, kit, true)
	got := e.allocation(r, tripod)
	x.NotNil(got)
	x.True(got.BeginsAt.Equal(e.now), "from now: %s", got.BeginsAt)
	x.True(got.EndsAt.Equal(e.now.Add(2 * time.Hour)))
}

// TestAnOverrideTakesWhatItsKitIsGivenAsItTookTheRest: a booking made over
// what was there, on purpose, blocks nothing, and what its kit is given it
// takes the same way.
func TestAnOverrideTakesWhatItsKitIsGivenAsItTookTheRest(t *testing.T) {
	e := newEnv(t)
	x := e.x
	tripod := e.item("삼각대", "TR-1", nil, day)
	e.bookable(tripod, nil)
	kit := e.kit("촬영 키트")

	r, err := e.reserve(e.owner, kit, e.tomorrow(9, 0), e.tomorrow(12, 0), func(req *app.ReservationAddRequest) {
		req.SetOverride(true)
		req.SetReason("행사")
	})
	x.NoError(err)
	_, err = e.reserve(e.owner, tripod, e.tomorrow(10, 0), e.tomorrow(11, 0), nil)
	x.NoError(err)

	e.relate(tripod, kit, true)
	got := e.allocation(r, tripod)
	x.Equal("override", got.Kind)
	x.False(got.Blocking)
	x.Empty(e.entry(r).GetConflicts())
}

// TestEveryOccurrenceOfASeriesIsRechecked: each time a series takes place is
// a booking of its own, and each takes what the kit was given, or waits for
// it.
func TestEveryOccurrenceOfASeriesIsRechecked(t *testing.T) {
	e := newEnv(t)
	x := e.x
	tripod := e.item("삼각대", "TR-1", nil, day)
	e.bookable(tripod, func(r *app.BookableAddRequest) { r.SetHorizonDays(120) })
	kit := e.kit("촬영 키트")

	first, err := e.reserve(e.owner, kit, e.tomorrow(9, 0), e.tomorrow(12, 0), func(r *app.ReservationAddRequest) { r.SetRepeat("FREQ=WEEKLY;COUNT=3") })
	x.NoError(err)
	_, err = e.reserve(e.owner, tripod, e.tomorrow(10, 0).AddDate(0, 0, 7), e.tomorrow(11, 0).AddDate(0, 0, 7), nil)
	x.NoError(err)

	e.relate(tripod, kit, true)
	all, err := e.app().Reservation().List(e.owner, app.ReservationListRequest_builder{
		Filters: []*app.ReservationFilter{app.ReservationFilter_builder{SeriesId: first.GetSeriesId()}.Build()},
	}.Build())
	x.NoError(err)
	x.Len(all.GetItems(), 3)
	kinds := []string{}
	for _, r := range all.GetItems() {
		kinds = append(kinds, e.allocation(r, tripod).Kind)
	}
	slices.Sort(kinds)
	x.Equal([]string{"reservation", "reservation", "waiting"}, kinds)
}

// TestWhatWaitsIsForgottenOnceTheBookingIsOver: a booking that waited for a
// member and was cancelled waits for nothing -- the calendar of the member
// does not show it, and the member's being let go does not give it to it.
func TestWhatWaitsIsForgottenOnceTheBookingIsOver(t *testing.T) {
	e := newEnv(t)
	x := e.x
	tripod := e.item("삼각대", "TR-1", nil, day)
	e.bookable(tripod, nil)
	kit := e.kit("촬영 키트")

	r, err := e.reserve(e.owner, kit, e.tomorrow(9, 0), e.tomorrow(12, 0), nil)
	x.NoError(err)
	other, err := e.reserve(e.owner, tripod, e.tomorrow(10, 0), e.tomorrow(11, 0), nil)
	x.NoError(err)
	e.relate(tripod, kit, true)
	x.Equal("waiting", e.allocation(r, tripod).Kind)

	e.cancel(e.owner, r)
	e.cancel(e.owner, other)
	x.Equal("waiting", e.allocation(r, tripod).Kind, "not taken by a booking that is over")
	busy, err := e.app().Bookable().Availability(e.owner, app.BookableAvailabilityRequest_builder{
		Resources: []*app.AssetRef{assetRef(tripod)},
		From:      timestamppb.New(e.tomorrow(0, 0)),
		To:        timestamppb.New(e.tomorrow(23, 0)),
	}.Build())
	x.NoError(err)
	for _, b := range busy.GetBusy() {
		x.NotEqual("waiting", b.GetKind())
	}
	e.sweep()
	x.Equal("waiting", e.allocation(r, tripod).Kind)
	_, err = e.reserve(e.owner, tripod, e.tomorrow(10, 0), e.tomorrow(11, 0), nil)
	x.NoError(err)
}

// TestABookingAndAChangeToItsKitAtOnceAgree: somebody booking a kit while a
// manager gives it a member, at once, each first in turn. A booking that
// holds the kit's schedule first is booked as the kit was, and the change,
// once it holds it, has the booking take the member. A change that holds it
// first is read by the booking once it does, which then needs a schedule it
// does not hold, and is refused, to be asked for again. Either way nothing is
// booked without the member. Before, the booking read the kit before it held
// anything, the change rechecked the kit's bookings before the booking was
// one of them, and the booking went without the member.
func TestABookingAndAChangeToItsKitAtOnceAgree(t *testing.T) {
	e := newEnv(t)
	e.postgresOnly()
	x := e.x
	tripod := e.item("삼각대", "TR-1", nil, day)
	e.bookable(tripod, nil)
	kit := e.kit("촬영 키트")
	kim, _ := e.person("member")

	for round := range 6 {
		at := e.tomorrow(9, 0).AddDate(0, 0, round)
		if round > 0 {
			e.unrelate(tripod, kit)
		}
		var r *app.Reservation
		book := func() error {
			var err error
			r, err = e.reserve(kim, kit, at, at.Add(3*time.Hour), nil)
			return err
		}
		relate := func() error {
			_, err := e.app().Asset().Relate(e.owner, app.AssetRelateRequest_builder{Ref: assetRef(tripod), Target: assetRef(kit), Kind: "member_of", Required: true}.Build())
			return err
		}

		bookingFirst := round%2 == 0
		var booked, related error
		if bookingFirst {
			booked, related = e.inTurn(kit, book, relate)
		} else {
			related, booked = e.inTurn(kit, relate, book)
		}
		x.NoError(related, "round %d", round)
		if bookingFirst {
			x.NoError(booked, "round %d", round)
		} else {
			x.Equal(codes.Aborted, codeOf(booked), "round %d: %v", round, booked)
			x.NoError(book(), "round %d, asked again", round)
		}
		got := e.allocation(r, tripod)
		x.NotNil(got, "round %d: booked without the member", round)
		x.True(got.Blocking, "round %d", round)
		x.Equal("reservation", got.Kind, "round %d", round)
	}
}

// inTurn runs `first` and `second` so that each waits for the schedule of
// `what` before either has it, `first` ahead: it is held from outside until
// `first` waits at a lock, and then `second` too, and then let go.
func (e *env) inTurn(what *app.Asset, first, second func() error) (error, error) {
	e.t.Helper()
	bs, err := e.app().Bookable().List(e.owner, app.BookableListRequest_builder{
		Filters: []*app.BookableFilter{app.BookableFilter_builder{Asset: assetRef(what)}.Build()},
	}.Build())
	e.x.NoError(err)
	e.x.Len(bs.GetItems(), 1)

	o := e.holdFromOutside("UPDATE bookable SET date_updated = $1 WHERE id = $2", time.Now(), idUUID(bs.GetItems()[0].GetId()))
	held := true
	release := func() {
		if held {
			held = false
			e.x.NoError(o.tx.Rollback())
		}
	}
	defer release()

	waiting := func(n int) {
		deadline := time.Now().Add(10 * time.Second)
		for {
			got := e.behind(o)
			if got >= n {
				return
			}
			if time.Now().After(deadline) {
				release()
				e.x.FailNow("never waited at a lock", "%d of %d did", got, n)
			}
			time.Sleep(10 * time.Millisecond)
		}
	}
	ea, eb := make(chan error, 1), make(chan error, 1)
	go func() { ea <- first() }()
	waiting(1)
	go func() { eb <- second() }()
	waiting(2)
	release()

	return <-ea, <-eb
}
