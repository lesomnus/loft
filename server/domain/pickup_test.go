package domain_test

import (
	"testing"
	"time"

	"google.golang.org/grpc/codes"

	app "github.com/lesomnus/loft"
)

// A pool's reservation is picked up as some of its units: what its pickup is
// offered is the pool's units free until it ends, as many as it holds, and
// what it hands out is its own from then on, each unit's time the unit's own
// (design 4, D46).

// offer is what the pickup of `r` is offered of `pool`: the units it is to be
// handed, and the others, by tag.
func (e *env) offer(r *app.Reservation, pool *app.Asset) (uint32, []string, []string) {
	e.t.Helper()
	v, err := e.app().Reservation().Pickup(e.owner, app.ReservationPickupRequest_builder{Ref: ref[app.ReservationRef](r.GetId())}.Build())
	e.x.NoError(err)
	for _, p := range v.GetPools() {
		if sameId(p.GetPoolId(), pool.GetId()) {
			return p.GetUnits(), e.tags(p.GetOffered()), e.tags(p.GetOthers())
		}
	}
	e.x.FailNow("nothing offered of the pool", pool.GetName())
	return 0, nil, nil
}

func (e *env) tags(ids [][]byte) []string {
	e.t.Helper()
	out := []string{}
	for _, id := range ids {
		a, err := e.app().Asset().Get(e.owner, app.AssetGetRequest_builder{Ref: ref[app.AssetRef](id)}.Build())
		e.x.NoError(err)
		out = append(out, a.GetTag())
	}
	return out
}

// pickup hands `things` out to `who` for `r`, a loan until it ends.
func (e *env) pickup(r *app.Reservation, who *app.Party, things ...*app.Asset) (*app.Custody, error) {
	e.t.Helper()
	lines := []*app.CustodyLineSpec{}
	for _, a := range things {
		lines = append(lines, app.CustodyLineSpec_builder{Asset: assetRef(a)}.Build())
	}
	return e.app().Custody().Add(e.owner, app.CustodyAddRequest_builder{
		Party:         ref[app.PartyRef](who.GetId()),
		DueAt:         r.GetEndsAt(),
		ReservationId: r.GetId(),
		Lines:         lines,
	}.Build())
}

// TestAPoolsPickupIsOfferedItsFreeUnits: what a pool's pickup is offered is
// as many of its units as it holds, free until it ends -- in use, with
// nobody, and taken then by nothing else -- by tag, one being repaired last;
// and the rest of them free then, any of which may be handed out instead.
func TestAPoolsPickupIsOfferedItsFreeUnits(t *testing.T) {
	e := newEnv(t)
	x := e.x
	repaired := e.item("카메라 0", "CM-0", nil, day)
	one := e.item("카메라 1", "CM-1", nil, day)
	booked := e.item("카메라 2", "CM-2", nil, day)
	three := e.item("카메라 3", "CM-3", nil, day)
	lent := e.item("카메라 4", "CM-4", nil, day)
	five := e.item("카메라 5", "CM-5", nil, day)
	lost := e.item("카메라 6", "CM-6", nil, day)
	e.bookable(booked, nil)
	e.status(repaired, "in_repair")
	e.status(lost, "lost")
	cameras := e.pool("카메라", repaired, one, booked, three, lent, five, lost)
	kim, me := e.person("member")
	lee, _ := e.person("member")
	at, until := e.tomorrow(9, 0), e.tomorrow(12, 0)

	_, err := e.reserve(lee, booked, e.tomorrow(11, 0), e.tomorrow(13, 0), nil)
	x.NoError(err)
	_, err = e.app().Custody().Add(e.owner, app.CustodyAddRequest_builder{
		Party: ref[app.PartyRef](me.GetId()),
		Lines: []*app.CustodyLineSpec{app.CustodyLineSpec_builder{Asset: assetRef(lent)}.Build()},
	}.Build())
	x.NoError(err)
	r, err := e.take(kim, cameras, 2, at, until)
	x.NoError(err)

	units, offered, others := e.offer(r, cameras)
	x.EqualValues(2, units)
	x.Equal([]string{"CM-1", "CM-3"}, offered)
	x.Equal([]string{"CM-5", "CM-0"}, others, "one being repaired last; not one booked then, lent or lost")
}

// TestAPoolsPickupMakesWhatItHandsOutItsOwn: what a pool's pickup hands out
// is the reservation's from then until it ends, in place of what it held of
// the pool: nothing books it over the reservation, and the pool has as many
// as before to give beside it -- until it is back.
func TestAPoolsPickupMakesWhatItHandsOutItsOwn(t *testing.T) {
	e := newEnv(t)
	x := e.x
	one := e.item("카메라 1", "CM-1", nil, day)
	two := e.item("카메라 2", "CM-2", nil, day)
	three := e.item("카메라 3", "CM-3", nil, day)
	e.bookable(two, nil)
	cameras := e.pool("카메라", one, two, three)
	kim, me := e.person("member")
	at, until := e.tomorrow(9, 0), e.tomorrow(12, 0)
	r, err := e.take(kim, cameras, 1, at, until)
	x.NoError(err)

	// Offered the first, and handed the second instead.
	_, offered, _ := e.offer(r, cameras)
	x.Equal([]string{"CM-1"}, offered)
	c, err := e.pickup(r, me, two)
	x.NoError(err)
	x.Nil(e.allocation(r, cameras), "it held none of it yet, and holds it no more")
	got := e.allocation(r, two)
	x.NotNil(got)
	x.True(got.Blocking)
	x.True(got.Exclusive)
	x.Equal("reservation", got.Kind)
	x.Equal(e.now, got.BeginsAt.Local().Truncate(time.Second), "from the pickup on")
	x.True(got.EndsAt.Equal(until))

	_, err = e.reserve(kim, two, e.tomorrow(10, 0), e.tomorrow(11, 0), nil)
	x.Equal(codes.Aborted, codeOf(err), "it is out until the reservation ends")
	_, err = e.take(kim, cameras, 3, e.tomorrow(10, 0), e.tomorrow(11, 0))
	x.Equal(codes.Aborted, codeOf(err), "one of three is out")
	_, err = e.take(kim, cameras, 2, e.tomorrow(10, 0), e.tomorrow(11, 0))
	x.NoError(err, "and the other two are there")

	// Back, it is free again.
	_, err = e.app().Custody().Return(e.owner, app.CustodyReturnRequest_builder{Ref: ref[app.CustodyRef](c.GetId())}.Build())
	x.NoError(err)
	_, err = e.reserve(kim, two, e.tomorrow(13, 0), e.tomorrow(14, 0), nil)
	x.NoError(err)
	x.False(e.allocation(r, two).Blocking, "let go of with the rest")
}

// TestAPoolPickedUpOnceBegunKeepsWhatItHeldUntilThen: what a reservation held
// of the pool before it was picked up stays what it was, until the pickup.
func TestAPoolPickedUpOnceBegunKeepsWhatItHeldUntilThen(t *testing.T) {
	e := newEnv(t)
	x := e.x
	one := e.item("카메라 1", "CM-1", nil, day)
	cameras := e.pool("카메라", one)
	kim, me := e.person("member")
	r, err := e.take(kim, cameras, 1, e.now.Add(10*time.Minute), e.now.Add(3*time.Hour))
	x.NoError(err)

	e.now = e.now.Add(time.Hour)
	_, err = e.pickup(r, me, one)
	x.NoError(err)
	held := e.allocation(r, cameras)
	x.NotNil(held)
	x.True(held.EndsAt.Equal(e.now), "until the pickup")
	x.True(e.allocation(r, one).BeginsAt.Equal(e.now), "and the camera from then")
}

// TestAPoolsPickupInPartOrBeyond: a pickup that hands out fewer than it holds
// holds the rest of the pool still; one that hands out more holds each of
// what it hands out, which the pools could spare -- from the pickup on, so
// what is picked up early is out over what is booked before it begins.
func TestAPoolsPickupInPartOrBeyond(t *testing.T) {
	e := newEnv(t)
	x := e.x
	kim, me := e.person("member")
	at, until := e.tomorrow(9, 0), e.tomorrow(12, 0)

	one := e.item("카메라 1", "CM-1", nil, day)
	two := e.item("카메라 2", "CM-2", nil, day)
	cameras := e.pool("카메라", one, two)
	part, err := e.take(kim, cameras, 2, at, until)
	x.NoError(err)
	_, err = e.pickup(part, me, one)
	x.NoError(err)
	x.EqualValues(1, e.allocation(part, cameras).Units, "the other still held")
	x.NotNil(e.allocation(part, one))

	three := e.item("빔프로젝터 1", "PJ-1", nil, day)
	four := e.item("빔프로젝터 2", "PJ-2", nil, day)
	five := e.item("빔프로젝터 3", "PJ-3", nil, day)
	projectors := e.pool("빔프로젝터", three, four, five)
	before, err := e.take(kim, projectors, 1, at, until)
	x.NoError(err)
	more, err := e.take(kim, projectors, 1, at.Add(4*time.Hour), until.Add(4*time.Hour))
	x.NoError(err)
	_, err = e.pickup(more, me, three, four, five)
	x.Equal(codes.FailedPrecondition, codeOf(err), "out from now, the three of them leave none for the morning's")
	_, err = e.pickup(more, me, three, four)
	x.NoError(err)
	x.NotNil(e.allocation(more, three))
	x.NotNil(e.allocation(more, four), "handed out, and held for it")
	x.Nil(e.allocation(more, projectors))
	x.True(e.holds(before, projectors), "the third is there for it")
	_, err = e.take(kim, projectors, 1, at, until)
	x.Equal(codes.Aborted, codeOf(err), "two of three are out then")
}

// TestAPoolsPickupIsRefusedWhatIsNotItsToHandOut: a unit something else takes
// before the reservation ends, or one the pools around it need to keep what
// they promised (D44), is not handed out for it -- nothing is -- and is not
// offered.
func TestAPoolsPickupIsRefusedWhatIsNotItsToHandOut(t *testing.T) {
	e := newEnv(t)
	x := e.x
	a := e.item("카메라 A", "CM-A", nil, day)
	m := e.item("카메라 M", "CM-M", nil, day)
	b := e.item("카메라 B", "CM-B", nil, day)
	e.bookable(b, nil)
	cameras := e.pool("카메라", a, m, b)
	gear := e.pool("촬영 장비", m)
	kim, me := e.person("member")
	at, until := e.tomorrow(9, 0), e.tomorrow(12, 0)

	_, err := e.take(kim, gear, 1, at, until)
	x.NoError(err)
	_, err = e.reserve(kim, b, e.tomorrow(11, 0), e.tomorrow(13, 0), nil)
	x.NoError(err)
	r, err := e.take(kim, cameras, 1, at, until)
	x.NoError(err)

	_, offered, others := e.offer(r, cameras)
	x.Equal([]string{"CM-A"}, offered)
	x.Empty(others, "M is the other pool's, and B is booked at eleven")
	for _, v := range []*app.Asset{m, b} {
		_, err = e.pickup(r, me, v)
		x.Equal(codes.FailedPrecondition, codeOf(err), "%v", err)
		x.Equal("confirmed", e.reservation(r).GetStatus(), "nothing of it written")
		x.Empty(e.get(v).GetCustodian())
	}
	_, err = e.pickup(r, me, a)
	x.NoError(err)
}

// TestAWaitingPoolBookingIsOfferedNothing: a reservation that waits for what
// it booked of a pool holds none of it, and is offered none to hand out.
func TestAWaitingPoolBookingIsOfferedNothing(t *testing.T) {
	e := newEnv(t)
	x := e.x
	one := e.item("카메라 1", "CM-1", nil, day)
	two := e.item("카메라 2", "CM-2", nil, day)
	cameras := e.pool("카메라", one, two)
	kim, _ := e.person("member")
	at, until := e.tomorrow(9, 0), e.tomorrow(12, 0)
	first, err := e.take(kim, cameras, 1, at, until)
	x.NoError(err)
	later, err := e.take(kim, cameras, 1, at, until)
	x.NoError(err)
	e.status(two, "disposed")
	x.False(e.holds(later, cameras))

	units, offered, others := e.offer(later, cameras)
	x.Zero(units)
	x.Empty(offered)
	x.Empty(others, "the first has the one there is")
	_, offered, _ = e.offer(first, cameras)
	x.Equal([]string{"CM-1"}, offered)
}

// reservation is `r` as it is now.
func (e *env) reservation(r *app.Reservation) *app.Reservation {
	e.t.Helper()
	v, err := e.app().Reservation().Get(e.owner, app.ReservationGetRequest_builder{Ref: ref[app.ReservationRef](r.GetId())}.Build())
	e.x.NoError(err)
	return v
}

// TestAPoolBookedTwiceInOneReservationIsPickedUpAsBoth: what one reservation
// holds of a pool by two of its items is handed out as one, the first held
// given first.
func TestAPoolBookedTwiceInOneReservationIsPickedUpAsBoth(t *testing.T) {
	e := newEnv(t)
	x := e.x
	one := e.item("카메라 1", "CM-1", nil, day)
	two := e.item("카메라 2", "CM-2", nil, day)
	three := e.item("카메라 3", "CM-3", nil, day)
	cameras := e.pool("카메라", one, two, three)
	kim, me := e.person("member")
	r, err := e.reserve(kim, cameras, e.tomorrow(9, 0), e.tomorrow(12, 0), func(r *app.ReservationAddRequest) {
		item := app.ReservationItemSpec_builder{Resource: assetRef(cameras)}.Build()
		r.SetItems([]*app.ReservationItemSpec{item, item})
	})
	x.NoError(err)

	units, offered, _ := e.offer(r, cameras)
	x.EqualValues(2, units)
	x.Equal([]string{"CM-1", "CM-2"}, offered)
	_, err = e.pickup(r, me, one, two)
	x.NoError(err)
	_, err = e.take(kim, cameras, 1, e.tomorrow(10, 0), e.tomorrow(11, 0))
	x.NoError(err, "the third")
	_, err = e.take(kim, cameras, 1, e.tomorrow(10, 0), e.tomorrow(11, 0))
	x.Equal(codes.Aborted, codeOf(err), "and no more: two are out, by neither of the reservation's of the pool")
}

// TestAPoolBookedOverIsPickedUpAsAnythingElse: a reservation booked over what
// a pool had, on purpose, took nothing the pool counts (D41), and its pickup
// is offered none of it and makes none of what it hands out its own.
func TestAPoolBookedOverIsPickedUpAsAnythingElse(t *testing.T) {
	e := newEnv(t)
	x := e.x
	one := e.item("빔프로젝터 1", "PJ-1", nil, day)
	projectors := e.pool("빔프로젝터", one)
	kim, me := e.person("member")
	_, err := e.take(kim, projectors, 1, e.tomorrow(9, 0), e.tomorrow(12, 0))
	x.NoError(err)
	over, err := e.reserve(e.owner, projectors, e.tomorrow(9, 0), e.tomorrow(12, 0), func(r *app.ReservationAddRequest) {
		r.SetOverride(true)
		r.SetReason("행사")
	})
	x.NoError(err)

	v, err := e.app().Reservation().Pickup(e.owner, app.ReservationPickupRequest_builder{Ref: ref[app.ReservationRef](over.GetId())}.Build())
	x.NoError(err)
	x.Empty(v.GetPools())
	_, err = e.pickup(over, me, one)
	x.NoError(err)
	x.Nil(e.allocation(over, one), "booked over, it takes nothing counted")
}

// TestAPoolsPickupAndABookingOfWhatItHandsOutAtOnceAgree: a pickup that hands
// out a pool's unit and a booking of that unit for the reservation's time,
// together, land one after the other: the unit is never out to both.
func TestAPoolsPickupAndABookingOfWhatItHandsOutAtOnceAgree(t *testing.T) {
	e := newEnv(t)
	e.postgresOnly()
	x := e.x
	kim, me := e.person("member")

	for round := range 4 {
		one := e.item("카메라 1", "", nil, day)
		two := e.item("카메라 2", "", nil, day)
		e.bookable(one, nil)
		cameras := e.pool("카메라", one, two)
		at := e.tomorrow(9, 0).AddDate(0, 0, round)
		r, err := e.take(kim, cameras, 1, at, at.Add(3*time.Hour))
		x.NoError(err)

		pick := func() error {
			_, err := e.pickup(r, me, one)
			return err
		}
		book := func() error {
			_, err := e.reserve(kim, one, at.Add(time.Hour), at.Add(2*time.Hour), nil)
			return err
		}
		pickupFirst := round%2 == 0
		var picked, booked error
		if pickupFirst {
			picked, booked = e.inTurn(one, pick, book)
			x.NoError(picked, "round %d", round)
			x.Equal(codes.Aborted, codeOf(booked), "round %d: %v", round, booked)
		} else {
			booked, picked = e.inTurn(one, book, pick)
			x.NoError(booked, "round %d", round)
			x.Equal(codes.FailedPrecondition, codeOf(picked), "round %d: %v", round, picked)
		}
	}
}
