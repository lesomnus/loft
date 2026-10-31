package domain_test

import (
	"testing"
	"time"

	"google.golang.org/grpc/codes"

	app "github.com/lesomnus/loft"
)

// A thing that is a member of two pools is one unit to one of them at a time
// (D44): what one of them promises of it, the other does not have then, and
// the pools it is in are counted together, whichever is booked, changed or
// waits.

// TestAMemberOfTwoPoolsIsPromisedByOneOfThemAtATime: two pools of the one
// camera do not both promise it for the same time, whichever is booked first.
func TestAMemberOfTwoPoolsIsPromisedByOneOfThemAtATime(t *testing.T) {
	e := newEnv(t)
	x := e.x
	camera := e.item("카메라 1", "CM-1", nil, day)
	cameras := e.pool("카메라", camera)
	gear := e.pool("촬영 장비", camera)
	kim, _ := e.person("member")

	_, err := e.take(kim, cameras, 1, e.tomorrow(9, 0), e.tomorrow(12, 0))
	x.NoError(err)
	_, err = e.take(kim, gear, 1, e.tomorrow(10, 0), e.tomorrow(11, 0))
	x.Equal(codes.Aborted, codeOf(err), "the other pool has promised it then")
	_, err = e.take(kim, gear, 1, e.tomorrow(12, 0), e.tomorrow(13, 0))
	x.NoError(err, "and not after")
	_, err = e.take(kim, cameras, 1, e.tomorrow(12, 30), e.tomorrow(14, 0))
	x.Equal(codes.Aborted, codeOf(err), "the other way round")
	x.EqualValues(1, e.units(cameras), "each has it as one of its units")
	x.EqualValues(1, e.units(gear))
}

// TestPoolsThatShareAMemberGiveWhatTheyCanTogether: what pools that share a
// member can give is what they can give at once, each unit to one of them --
// the one they share to whichever has no other then -- so a booking that fits
// that way is taken, and one that does not is refused, of a pool or of a unit
// of one.
func TestPoolsThatShareAMemberGiveWhatTheyCanTogether(t *testing.T) {
	e := newEnv(t)
	x := e.x
	a := e.item("카메라 A", "CM-A", nil, day)
	m := e.item("카메라 M", "CM-M", nil, day)
	b := e.item("카메라 B", "CM-B", nil, day)
	e.bookable(b, nil)
	e.bookable(m, nil)
	cameras := e.pool("카메라", a, m)
	gear := e.pool("촬영 장비", m, b)
	kim, _ := e.person("member")
	at, until := e.tomorrow(9, 0), e.tomorrow(12, 0)

	_, err := e.take(kim, gear, 1, at, until)
	x.NoError(err)
	_, err = e.take(kim, cameras, 2, at, until)
	x.NoError(err, "the other pool has B for its one")
	_, err = e.reserve(kim, b, at, until, nil)
	x.Equal(codes.Aborted, codeOf(err), "B is the one the other pool has left")
	_, err = e.reserve(kim, m, at, until, nil)
	x.Equal(codes.Aborted, codeOf(err), "and M is promised by one of them")
	_, err = e.take(kim, gear, 1, at, until)
	x.Equal(codes.Aborted, codeOf(err), "all three are promised")
	_, err = e.reserve(kim, b, e.tomorrow(13, 0), e.tomorrow(14, 0), nil)
	x.NoError(err, "after, B is its own")
}

// TestABookingOfPoolsThatShareAMemberIsOneBookingOfIt: what one booking takes
// is counted together too: two pools of the one camera, or a pool and its
// camera, booked at once take it once.
func TestABookingOfPoolsThatShareAMemberIsOneBookingOfIt(t *testing.T) {
	e := newEnv(t)
	x := e.x
	camera := e.item("카메라 1", "CM-1", nil, day)
	e.bookable(camera, nil)
	cameras := e.pool("카메라", camera)
	gear := e.pool("촬영 장비", camera)
	kim, _ := e.person("member")
	both := func(what ...*app.Asset) func(r *app.ReservationAddRequest) {
		return func(r *app.ReservationAddRequest) {
			items := []*app.ReservationItemSpec{}
			for _, v := range what {
				items = append(items, app.ReservationItemSpec_builder{Resource: assetRef(v)}.Build())
			}
			r.SetItems(items)
		}
	}

	_, err := e.reserve(kim, cameras, e.tomorrow(9, 0), e.tomorrow(12, 0), both(cameras, gear))
	x.Equal(codes.Aborted, codeOf(err), "%v", err)
	_, err = e.reserve(kim, cameras, e.tomorrow(9, 0), e.tomorrow(12, 0), both(cameras, camera))
	x.Equal(codes.Aborted, codeOf(err), "%v", err)
	_, err = e.reserve(kim, cameras, e.tomorrow(9, 0), e.tomorrow(12, 0), both(cameras, cameras))
	x.Equal(codes.Aborted, codeOf(err), "%v", err)
	_, err = e.reserve(kim, cameras, e.tomorrow(9, 0), e.tomorrow(12, 0), nil)
	x.NoError(err, "nothing of it was written")
}

// TestAChangeToOnePoolMakesTheLatestBookingOfThePoolsAroundItWait: where the
// pools around a change no longer fit what they are booked for, the booking
// made last waits, of whichever of them -- the one disposed of a unit of, or
// the other -- and takes what it booked once there is room again.
func TestAChangeToOnePoolMakesTheLatestBookingOfThePoolsAroundItWait(t *testing.T) {
	for _, first := range []string{"cameras", "gear"} {
		t.Run(first, func(t *testing.T) {
			e := newEnv(t)
			x := e.x
			a := e.item("카메라 A", "CM-A", nil, day)
			m := e.item("카메라 M", "CM-M", nil, day)
			c := e.item("카메라 C", "CM-C", nil, day)
			cameras := e.pool("카메라", a, m, c)
			gear := e.pool("촬영 장비", m)
			kim, _ := e.person("member")
			lee, _ := e.person("member")
			at, until := e.tomorrow(9, 0), e.tomorrow(12, 0)

			var two, one *app.Reservation
			var err error
			if first == "cameras" {
				two, err = e.take(kim, cameras, 2, at, until)
				x.NoError(err)
				one, err = e.take(lee, gear, 1, at, until)
				x.NoError(err, "the pool of three has A and C for its two")
			} else {
				one, err = e.take(lee, gear, 1, at, until)
				x.NoError(err)
				two, err = e.take(kim, cameras, 2, at, until)
				x.NoError(err)
			}

			// C disposed of: the pool of three has A and M, and M is the
			// other pool's only one.
			e.status(c, "disposed")
			if first == "cameras" {
				x.True(e.holds(two, cameras), "made first, kept")
				x.False(e.holds(one, gear), "made last, waits -- of the other pool")
				x.Contains(e.told(lee), "reservation.conflict")
				cs := e.entry(one).GetConflicts()
				x.Len(cs, 1)
				x.True(sameId(gear.GetId(), cs[0].GetResourceId()))
				x.EqualValues(0, cs[0].GetAvailable(), "the other pool has M")
				x.NotEmpty(cs[0].GetHeldBy())
			} else {
				x.True(e.holds(one, gear), "made first, kept")
				x.False(e.holds(two, cameras), "made last, waits")
				x.Contains(e.told(kim), "reservation.conflict")
				cs := e.entry(two).GetConflicts()
				x.Len(cs, 1)
				x.EqualValues(2, cs[0].GetUnits())
				x.EqualValues(1, cs[0].GetAvailable(), "A, since M is the other pool's")
			}

			// Back in use, there is room for both again.
			e.status(c, "active")
			x.True(e.holds(two, cameras))
			x.True(e.holds(one, gear))
		})
	}
}

// TestWhatOnePoolLetsGoOfIsThePoolsAroundItToGiveAtOnce: a booking of one pool
// cancelled is what another pool that shares its unit may give, in the
// cancellation: the booking that waited takes it then, not at the sweep.
func TestWhatOnePoolLetsGoOfIsThePoolsAroundItToGiveAtOnce(t *testing.T) {
	e := newEnv(t)
	x := e.x
	m := e.item("카메라 M", "CM-M", nil, day)
	b := e.item("카메라 B", "CM-B", nil, day)
	cameras := e.pool("카메라", m)
	gear := e.pool("촬영 장비", m, b)
	kim, _ := e.person("member")
	at, until := e.tomorrow(9, 0), e.tomorrow(12, 0)

	shoot, err := e.take(kim, gear, 1, at, until)
	x.NoError(err)
	later, err := e.take(kim, cameras, 1, at, until)
	x.NoError(err, "the other pool has B for its one")
	e.status(b, "lost")
	x.True(e.holds(shoot, gear))
	x.False(e.holds(later, cameras), "M is the other pool's now")

	e.cancel(kim, shoot)
	x.True(e.holds(later, cameras), "taken as it was let go of")
}

// TestAThingInTwoPoolsBookedOnItsOwnIsWhatNeitherNeeds: a unit of pools that
// share a member is booked on its own only while they can all do without it
// -- one booked on its own leaves the others fewer to share.
func TestAThingInTwoPoolsBookedOnItsOwnIsWhatNeitherNeeds(t *testing.T) {
	e := newEnv(t)
	x := e.x
	a := e.item("카메라 A", "CM-A", nil, day)
	m := e.item("카메라 M", "CM-M", nil, day)
	b := e.item("카메라 B", "CM-B", nil, day)
	for _, v := range []*app.Asset{a, m, b} {
		e.bookable(v, nil)
	}
	cameras := e.pool("카메라", a, m)
	gear := e.pool("촬영 장비", m, b)
	kim, _ := e.person("member")
	at, until := e.tomorrow(9, 0), e.tomorrow(12, 0)

	_, err := e.take(kim, cameras, 1, at, until)
	x.NoError(err)
	_, err = e.take(kim, gear, 1, at, until)
	x.NoError(err)
	_, err = e.reserve(kim, a, at, until, nil)
	x.NoError(err, "the one pool has M, and the other B")
	_, err = e.reserve(kim, m, at, until, nil)
	x.Equal(codes.Aborted, codeOf(err), "A booked, the one pool needs M")
	_, err = e.reserve(kim, b, at, until, nil)
	x.Equal(codes.Aborted, codeOf(err), "and so the other needs B")
	_, err = e.reserve(kim, b, e.tomorrow(13, 0), e.tomorrow(14, 0), nil)
	x.NoError(err)
}

// TestBookingsOfPoolsThatShareAMemberAtOnceAgree: bookings of two pools of the
// one camera that arrive together land one after the other, whichever goes
// first: the camera is never promised twice.
func TestBookingsOfPoolsThatShareAMemberAtOnceAgree(t *testing.T) {
	e := newEnv(t)
	e.postgresOnly()
	x := e.x
	kim, _ := e.person("member")

	for round := range 4 {
		camera := e.item("카메라", "", nil, day)
		cameras := e.pool("카메라", camera)
		gear := e.pool("촬영 장비", camera)
		at := e.tomorrow(9, 0).AddDate(0, 0, round)
		var r1, r2 *app.Reservation
		one := func() error {
			var err error
			r1, err = e.take(kim, cameras, 1, at, at.Add(3*time.Hour))
			return err
		}
		other := func() error {
			var err error
			r2, err = e.take(kim, gear, 1, at, at.Add(time.Hour))
			return err
		}

		var e1, e2 error
		if round%2 == 0 {
			e1, e2 = e.inTurn(cameras, one, other)
			x.NoError(e1, "round %d", round)
			x.Equal(codes.Aborted, codeOf(e2), "round %d: %v", round, e2)
			x.True(e.holds(r1, cameras), "round %d", round)
		} else {
			e2, e1 = e.inTurn(cameras, other, one)
			x.NoError(e2, "round %d", round)
			x.Equal(codes.Aborted, codeOf(e1), "round %d: %v", round, e1)
			x.True(e.holds(r2, gear), "round %d", round)
		}
	}
}

// TestABookingOfAPoolAndAChangeToAPoolAroundItAtOnceAgree: a booking of one
// pool and a unit of the other pool it shares a camera with going out of use,
// together, land one after the other: if the booking went first, the one made
// last waits; if the change did, the booking is refused.
func TestABookingOfAPoolAndAChangeToAPoolAroundItAtOnceAgree(t *testing.T) {
	e := newEnv(t)
	e.postgresOnly()
	x := e.x
	kim, _ := e.person("member")

	for round := range 4 {
		m := e.item("카메라 M", "", nil, day)
		b := e.item("카메라 B", "", nil, day)
		cameras := e.pool("카메라", m)
		gear := e.pool("촬영 장비", m, b)
		at := e.tomorrow(9, 0).AddDate(0, 0, round)
		shoot, err := e.take(kim, gear, 1, at, at.Add(3*time.Hour))
		x.NoError(err)

		var r *app.Reservation
		book := func() error {
			var err error
			r, err = e.take(kim, cameras, 1, at, at.Add(3*time.Hour))
			return err
		}
		lose := func() error {
			_, err := e.app().Asset().SetAttributes(e.owner, app.AssetSetAttributesRequest_builder{Ref: assetRef(b), Set: map[string]string{"status": "lost"}}.Build())
			return err
		}

		bookingFirst := round%2 == 0
		var booked, lost error
		if bookingFirst {
			booked, lost = e.inTurn(gear, book, lose)
		} else {
			lost, booked = e.inTurn(gear, lose, book)
		}
		x.NoError(lost, "round %d", round)
		x.True(e.holds(shoot, gear), "round %d: made first", round)
		if bookingFirst {
			x.NoError(booked, "round %d", round)
			x.False(e.holds(r, cameras), "round %d: M promised twice", round)
		} else {
			x.Equal(codes.Aborted, codeOf(booked), "round %d: %v", round, booked)
		}
	}
}
