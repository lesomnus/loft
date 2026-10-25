package domain_test

import (
	"context"
	"fmt"
	"slices"
	"testing"
	"time"
	"uuid"

	"google.golang.org/grpc/codes"
	"google.golang.org/protobuf/types/known/timestamppb"

	app "github.com/lesomnus/rove"
	"github.com/lesomnus/rove/internal/ent"
)

// What a pool can give is its members in use, less the ones whose own time is
// taken then (design 4): its reservations follow it when that changes, the
// ones made last waiting until there is room, and a thing that is one of its
// units is booked on its own only while the pool can spare it.

// group is a group that may be reserved, with these members.
func (e *env) group(name string, members ...*app.Asset) *app.Asset {
	e.t.Helper()
	v, err := e.app().Asset().Add(e.owner, app.AssetAddRequest_builder{Name: name, Kind: "group"}.Build())
	e.x.NoError(err)
	for _, m := range members {
		e.relate(m, v, false)
	}
	return v
}

// pool is a group that may be reserved, with these members.
func (e *env) pool(name string, members ...*app.Asset) *app.Asset {
	e.t.Helper()
	v := e.group(name, members...)
	e.bookable(v, nil)
	return v
}

// status says what `a` is from now on: in use, lost, disposed.
func (e *env) status(a *app.Asset, v string) {
	e.t.Helper()
	_, err := e.app().Asset().SetAttributes(e.owner, app.AssetSetAttributesRequest_builder{Ref: assetRef(a), Set: map[string]string{"status": v}}.Build())
	e.x.NoError(err)
}

// units is how many of `a` its reservable row says there are.
func (e *env) units(a *app.Asset) uint32 {
	e.t.Helper()
	return e.bookableOf(a).GetUnits()
}

func (e *env) bookableOf(a *app.Asset) *app.Bookable {
	e.t.Helper()
	bs, err := e.app().Bookable().List(e.owner, app.BookableListRequest_builder{
		Filters: []*app.BookableFilter{app.BookableFilter_builder{Asset: assetRef(a)}.Build()},
	}.Build())
	e.x.NoError(err)
	e.x.Len(bs.GetItems(), 1)
	return bs.GetItems()[0]
}

// take books `n` of `what` for [from, to).
func (e *env) take(ctx context.Context, what *app.Asset, n uint32, from, to time.Time) (*app.Reservation, error) {
	return e.reserve(ctx, what, from, to, func(r *app.ReservationAddRequest) { r.GetItems()[0].SetUnits(n) })
}

// holds says whether `r` holds what it booked of `a`, rather than waits.
func (e *env) holds(r *app.Reservation, a *app.Asset) bool {
	e.t.Helper()
	v := e.allocation(r, a)
	e.x.NotNil(v, "%s books %s", r.GetName(), a.GetName())
	if v.Blocking {
		e.x.Equal("reservation", v.Kind)
		return true
	}
	e.x.Equal("waiting", v.Kind)
	return false
}

// TestAPoolHasAsManyAsItsMembersInUse: a group's units are its members that
// are there to be handed out -- one being repaired among them -- and not one
// ordered and not received, lost, retired or disposed; nobody sets them.
func TestAPoolHasAsManyAsItsMembersInUse(t *testing.T) {
	e := newEnv(t)
	x := e.x
	in := e.item("카메라 1", "CM-1", nil, day)
	repaired := e.item("카메라 2", "CM-2", nil, day)
	ordered := e.item("카메라 3", "CM-3", nil, day)
	lost := e.item("카메라 4", "CM-4", nil, day)
	retired := e.item("카메라 5", "CM-5", nil, day)
	disposed := e.item("카메라 6", "CM-6", nil, day)
	e.status(repaired, "in_repair")
	e.status(ordered, "ordered")
	e.status(lost, "lost")
	e.status(retired, "retired")
	e.status(disposed, "disposed")
	cameras := e.group("카메라", in, repaired, ordered, lost, retired, disposed)

	_, err := e.app().Bookable().Add(e.owner, app.BookableAddRequest_builder{Asset: assetRef(cameras), Units: 6}.Build())
	x.Equal(codes.InvalidArgument, codeOf(err), "what it has is what its members say")
	e.bookable(cameras, nil)
	x.EqualValues(2, e.units(cameras))

	kim, _ := e.person("member")
	_, err = e.take(kim, cameras, 3, e.tomorrow(9, 0), e.tomorrow(12, 0))
	x.Equal(codes.InvalidArgument, codeOf(err), "there are two")
	_, err = e.take(kim, cameras, 2, e.tomorrow(9, 0), e.tomorrow(12, 0))
	x.NoError(err)
	_, err = e.take(kim, cameras, 1, e.tomorrow(10, 0), e.tomorrow(11, 0))
	x.Equal(codes.Aborted, codeOf(err), "both are booked")

	_, err = e.app().Bookable().Update(e.owner, app.BookableUpdateRequest_builder{Ref: ref[app.BookableRef](e.bookableOf(cameras).GetId()), Units: 5, Enabled: true}.Build())
	x.Equal(codes.InvalidArgument, codeOf(err))
	_, err = e.app().Bookable().Update(e.owner, app.BookableUpdateRequest_builder{Ref: ref[app.BookableRef](e.bookableOf(cameras).GetId()), Units: 2, Enabled: true, Approval: true}.Build())
	x.NoError(err, "said as it is, which a form sends back")
	x.True(e.bookableOf(cameras).GetApproval())

	// Received, the one ordered is one more of them.
	e.status(ordered, "active")
	x.EqualValues(3, e.units(cameras))
	_, err = e.take(kim, cameras, 1, e.tomorrow(10, 0), e.tomorrow(11, 0))
	x.NoError(err)
}

// TestAThingsOwnUnitsAreWhatWasSet: anything but a group with members has the
// units it was given, as it had: thirty chairs registered as one, and a group
// nothing is a member of.
func TestAThingsOwnUnitsAreWhatWasSet(t *testing.T) {
	e := newEnv(t)
	x := e.x
	chairs := e.item("접이식 의자", "CH-1", nil, day)
	e.bookable(chairs, func(r *app.BookableAddRequest) { r.SetUnits(30) })
	empty := e.group("의자 묶음")
	e.bookable(empty, func(r *app.BookableAddRequest) { r.SetUnits(20) })
	kim, _ := e.person("member")

	for _, v := range []struct {
		what *app.Asset
		n    uint32
	}{{chairs, 30}, {empty, 20}} {
		x.EqualValues(v.n, e.units(v.what))
		_, err := e.take(kim, v.what, v.n-1, e.tomorrow(9, 0), e.tomorrow(12, 0))
		x.NoError(err)
		_, err = e.take(kim, v.what, 2, e.tomorrow(10, 0), e.tomorrow(11, 0))
		x.Equal(codes.Aborted, codeOf(err))
		_, err = e.app().Bookable().Update(e.owner, app.BookableUpdateRequest_builder{Ref: ref[app.BookableRef](e.bookableOf(v.what).GetId()), Units: v.n + 1, Enabled: true}.Build())
		x.NoError(err)
		x.EqualValues(v.n+1, e.units(v.what))
		_, err = e.take(kim, v.what, 2, e.tomorrow(10, 0), e.tomorrow(11, 0))
		x.NoError(err)
	}

	// Its status is nothing to it.
	e.status(chairs, "disposed")
	x.EqualValues(31, e.units(chairs))
}

// TestAMemberOutOfUseMakesThePoolsLatestBookingWait: a member lost, retired or
// disposed is one fewer for the pool to give, and where its bookings no longer
// fit, the one made last waits for what it booked -- nobody is told it lost
// what it was promised first -- whoever has to settle it is told, and it takes
// it once there is room again.
func TestAMemberOutOfUseMakesThePoolsLatestBookingWait(t *testing.T) {
	for _, gone := range []string{"lost", "retired", "disposed"} {
		t.Run(gone, func(t *testing.T) {
			e := newEnv(t)
			x := e.x
			one := e.item("빔프로젝터 1", "PJ-1", nil, day)
			two := e.item("빔프로젝터 2", "PJ-2", nil, day)
			projectors := e.pool("빔프로젝터", one, two)
			kim, _ := e.person("member")
			lee, _ := e.person("member")
			boss, _ := e.person("manager")

			first, err := e.take(kim, projectors, 1, e.tomorrow(9, 0), e.tomorrow(12, 0))
			x.NoError(err)
			later, err := e.take(lee, projectors, 1, e.tomorrow(10, 0), e.tomorrow(11, 0))
			x.NoError(err)
			apart, err := e.take(lee, projectors, 1, e.tomorrow(13, 0), e.tomorrow(14, 0))
			x.NoError(err)

			e.status(two, gone)
			x.EqualValues(1, e.units(projectors))
			x.True(e.holds(first, projectors), "made first, kept")
			x.False(e.holds(later, projectors), "made last, waits")
			x.True(e.holds(apart, projectors), "nothing else then")
			x.Contains(e.told(lee), "reservation.conflict")
			x.NotContains(e.told(kim), "reservation.conflict")
			x.Contains(e.told(boss), "reservation.conflict")

			// Wherever it is acted on, it says what it waits for and what
			// holds the rest.
			cs := e.entry(later).GetConflicts()
			x.Len(cs, 1)
			x.True(sameId(projectors.GetId(), cs[0].GetResourceId()))
			x.EqualValues(1, cs[0].GetUnits())
			x.EqualValues(0, cs[0].GetAvailable())
			x.Len(cs[0].GetHeldBy(), 1)
			x.True(sameId(first.GetId(), cs[0].GetHeldBy()[0].GetRefId()))
			x.Empty(e.entry(first).GetConflicts())
			_, err = e.take(kim, projectors, 1, e.tomorrow(10, 0), e.tomorrow(11, 0))
			x.Equal(codes.Aborted, codeOf(err), "what waits is not booked over")

			// Back in use, it is the pool's again, and what waited has it.
			e.status(two, "active")
			x.EqualValues(2, e.units(projectors))
			x.True(e.holds(later, projectors))
			x.Empty(e.entry(later).GetConflicts())
			x.Contains(e.told(lee), "reservation.resolved")
		})
	}
}

// TestAMemberDisposedOfAndTakenOffAPoolInOneChange: what goes out of use and
// leaves its pool and its kit in the same change (design 4, 처분) is one fewer
// for the pool to give, as either alone is, and the kit's booking lets it go.
func TestAMemberDisposedOfAndTakenOffAPoolInOneChange(t *testing.T) {
	e := newEnv(t)
	x := e.x
	one := e.item("빔프로젝터 1", "PJ-1", nil, day)
	two := e.item("빔프로젝터 2", "PJ-2", nil, day)
	projectors := e.pool("빔프로젝터", one, two)
	cable := e.item("케이블", "CB-1", nil, day)
	kit := e.kit("발표 키트", cable)
	e.relate(two, kit, true)
	kim, _ := e.person("member")

	first, err := e.take(kim, projectors, 1, e.tomorrow(9, 0), e.tomorrow(12, 0))
	x.NoError(err)
	later, err := e.take(kim, projectors, 1, e.tomorrow(9, 0), e.tomorrow(12, 0))
	x.NoError(err)
	talk, err := e.reserve(kim, kit, e.tomorrow(13, 0), e.tomorrow(14, 0), nil)
	x.NoError(err)
	x.NotNil(e.allocation(talk, two))

	_, err = e.app().Asset().SetAttributes(e.owner, app.AssetSetAttributesRequest_builder{
		Ref:   assetRef(two),
		Set:   map[string]string{"status": "disposed"},
		Leave: []*app.AssetRef{assetRef(projectors), assetRef(kit)},
	}.Build())
	x.NoError(err)
	x.EqualValues(1, e.units(projectors))
	x.True(e.holds(first, projectors))
	x.False(e.holds(later, projectors))
	x.Nil(e.allocation(talk, two), "let go of by the kit's booking")
}

// TestAMemberTakenOffAPoolMakesItsLatestBookingWait: every way a member stops
// being one of a pool's -- the relation ended, said never to have held, or the
// member voided -- is one fewer to give.
func TestAMemberTakenOffAPoolMakesItsLatestBookingWait(t *testing.T) {
	for _, how := range []string{"end", "retract", "void"} {
		t.Run(how, func(t *testing.T) {
			e := newEnv(t)
			x := e.x
			one := e.item("빔프로젝터 1", "PJ-1", nil, day)
			two := e.item("빔프로젝터 2", "PJ-2", nil, day)
			projectors := e.pool("빔프로젝터", one, two)
			kim, _ := e.person("member")

			first, err := e.take(kim, projectors, 1, e.tomorrow(9, 0), e.tomorrow(12, 0))
			x.NoError(err)
			later, err := e.take(kim, projectors, 1, e.tomorrow(9, 0), e.tomorrow(12, 0))
			x.NoError(err)

			switch how {
			case "end":
				e.unrelate(two, projectors)
			case "retract":
				tl, err := e.app().Asset().Timeline(e.owner, app.AssetTimelineRequest_builder{Ref: assetRef(two)}.Build())
				x.NoError(err)
				var row []byte
				for _, en := range tl.GetEntries() {
					if en.GetKind() == "link" {
						row = en.GetRowId()
					}
				}
				x.NotEmpty(row)
				_, err = e.app().Asset().Correct(e.owner, app.AssetCorrectRequest_builder{Ref: assetRef(two), RowId: row, Retract: true, Reason: "잘못 연결함"}.Build())
				x.NoError(err)
			case "void":
				_, err := e.app().Asset().Erase(e.owner, assetRef(two))
				x.NoError(err)
			}
			x.EqualValues(1, e.units(projectors))
			x.True(e.holds(first, projectors))
			x.False(e.holds(later, projectors))
		})
	}
}

// TestAPoolGivenAMemberGivesItOut: a member given to a pool is one more of it
// to book, and what waited takes it, the one that has waited longest first.
func TestAPoolGivenAMemberGivesItOut(t *testing.T) {
	e := newEnv(t)
	x := e.x
	one := e.item("카메라 1", "CM-1", nil, day)
	two := e.item("카메라 2", "CM-2", nil, day)
	three := e.item("카메라 3", "CM-3", nil, day)
	cameras := e.pool("카메라", one, two, three)
	kim, _ := e.person("member")

	at, until := e.tomorrow(9, 0), e.tomorrow(12, 0)
	a, err := e.take(kim, cameras, 1, at, until)
	x.NoError(err)
	b, err := e.take(kim, cameras, 1, at, until)
	x.NoError(err)
	c, err := e.take(kim, cameras, 1, at, until)
	x.NoError(err)

	// Two of them out of use, one after the other: the one made last waits
	// first, and waits longest.
	e.status(three, "disposed")
	x.False(e.holds(c, cameras))
	e.now = e.now.Add(time.Minute)
	e.status(two, "disposed")
	x.False(e.holds(b, cameras))
	x.True(e.holds(a, cameras))

	e.now = e.now.Add(time.Minute)
	four := e.item("카메라 4", "CM-4", nil, day)
	e.relate(four, cameras, false)
	x.EqualValues(2, e.units(cameras))
	x.True(e.holds(c, cameras), "it waited longest")
	x.False(e.holds(b, cameras))

	// And one more is there to book again.
	five := e.item("카메라 5", "CM-5", nil, day)
	e.relate(five, cameras, false)
	x.True(e.holds(b, cameras))
	_, err = e.take(kim, cameras, 1, at, until)
	x.Equal(codes.Aborted, codeOf(err))
	six := e.item("카메라 6", "CM-6", nil, day)
	e.relate(six, cameras, false)
	_, err = e.take(kim, cameras, 1, at, until)
	x.NoError(err, "given to it, it is booked through it")
}

// TestAPoolThatLosesItsLastMemberHasNone: what a group with members had were
// its members; the last of them gone, it has none to give, until it is given
// a number by hand -- it is a group with no members then -- or members again.
func TestAPoolThatLosesItsLastMemberHasNone(t *testing.T) {
	e := newEnv(t)
	x := e.x
	one := e.item("카메라 1", "CM-1", nil, day)
	cameras := e.pool("카메라", one)
	kim, _ := e.person("member")
	r, err := e.take(kim, cameras, 1, e.tomorrow(9, 0), e.tomorrow(12, 0))
	x.NoError(err)

	e.unrelate(one, cameras)
	x.EqualValues(0, e.units(cameras))
	x.False(e.holds(r, cameras))
	_, err = e.take(kim, cameras, 1, e.tomorrow(13, 0), e.tomorrow(14, 0))
	x.Error(err)

	_, err = e.app().Bookable().Update(e.owner, app.BookableUpdateRequest_builder{Ref: ref[app.BookableRef](e.bookableOf(cameras).GetId()), Units: 3, Enabled: true}.Build())
	x.NoError(err, "a group with no members has what was set")
	x.EqualValues(3, e.units(cameras))
	e.sweep()
	x.True(e.holds(r, cameras), "taken by the sweep, as anything that waits")
}

// TestAPoolsMemberUnderMaintenanceIsNotOneOfItsUnitsThen: a member being
// repaired is one of its pool's units, and the time its work order blocks is
// what keeps it from a reservation, as for anything booked on its own. The
// pool has one fewer then, booked before the work order or after it.
func TestAPoolsMemberUnderMaintenanceIsNotOneOfItsUnitsThen(t *testing.T) {
	e := newEnv(t)
	x := e.x
	one := e.item("노트북 1", "NB-1", nil, day)
	two := e.item("노트북 2", "NB-2", nil, day)
	laptops := e.pool("노트북", one, two)
	kim, _ := e.person("member")
	lee, _ := e.person("member")

	first, err := e.take(kim, laptops, 1, e.tomorrow(9, 0), e.tomorrow(12, 0))
	x.NoError(err)
	later, err := e.take(lee, laptops, 1, e.tomorrow(10, 0), e.tomorrow(11, 0))
	x.NoError(err)

	// A repair that takes the afternoon before and the morning: the booking
	// made last waits, for as long as the work order takes the laptop.
	w, err := e.app().WorkOrder().Add(e.owner, app.WorkOrderAddRequest_builder{
		Asset:    assetRef(two),
		Kind:     "repair",
		Name:     "배터리 교체",
		BeginsAt: timestamppb.New(e.tomorrow(8, 0)),
		EndsAt:   timestamppb.New(e.tomorrow(10, 30)),
		Blocking: true,
	}.Build())
	x.NoError(err)
	x.True(e.holds(first, laptops))
	x.False(e.holds(later, laptops))
	x.EqualValues(2, e.units(laptops), "still one of them")
	_, err = e.take(kim, laptops, 1, e.tomorrow(9, 30), e.tomorrow(10, 0))
	x.Equal(codes.Aborted, codeOf(err), "one booked, the other under maintenance then")
	_, err = e.take(kim, laptops, 2, e.tomorrow(8, 0), e.tomorrow(9, 0))
	x.Equal(codes.Aborted, codeOf(err), "one under maintenance then")
	_, err = e.take(kim, laptops, 1, e.tomorrow(8, 0), e.tomorrow(9, 0))
	x.NoError(err, "and the other free")
	_, err = e.take(kim, laptops, 2, e.tomorrow(15, 0), e.tomorrow(16, 0))
	x.NoError(err, "neither then")

	// In repair, it is still one of them: what keeps it from a booking is
	// its work order's time.
	_, err = e.app().WorkOrder().Update(e.owner, app.WorkOrderUpdateRequest_builder{Ref: ref[app.WorkOrderRef](w.GetId()), Status: "in_progress", Blocking: true}.Build())
	x.NoError(err)
	x.Equal("in_repair", e.get(two).GetStatus())
	x.EqualValues(2, e.units(laptops))

	// Done early, the time is the pool's again, and what waited has it.
	_, err = e.app().WorkOrder().Complete(e.owner, app.WorkOrderCompleteRequest_builder{Ref: ref[app.WorkOrderRef](w.GetId())}.Build())
	x.NoError(err)
	x.Equal("active", e.get(two).GetStatus())
	x.True(e.holds(later, laptops))
}

// TestAPoolsUnitIsThePoolsToSpare: a thing that is one of a pool's units is
// booked on its own, or with a kit, only while the pool can spare it -- the
// pool's reservations are promises of some of its members -- and a booking of
// the pool counts what its members' own bookings take.
func TestAPoolsUnitIsThePoolsToSpare(t *testing.T) {
	e := newEnv(t)
	x := e.x
	one := e.item("카메라 1", "CM-1", nil, day)
	two := e.item("카메라 2", "CM-2", nil, day)
	e.bookable(one, nil)
	cameras := e.pool("카메라", one, two)
	kit := e.kit("촬영 키트", two)
	kim, _ := e.person("member")

	_, err := e.take(kim, cameras, 2, e.tomorrow(9, 0), e.tomorrow(12, 0))
	x.NoError(err)
	_, err = e.reserve(kim, one, e.tomorrow(10, 0), e.tomorrow(11, 0), nil)
	x.Equal(codes.Aborted, codeOf(err), "the pool has both then")
	_, err = e.reserve(kim, kit, e.tomorrow(10, 0), e.tomorrow(11, 0), nil)
	x.Equal(codes.Aborted, codeOf(err), "and the kit's member is one of them")

	_, err = e.reserve(kim, one, e.tomorrow(13, 0), e.tomorrow(14, 0), nil)
	x.NoError(err)
	_, err = e.take(kim, cameras, 2, e.tomorrow(13, 0), e.tomorrow(14, 0))
	x.Equal(codes.Aborted, codeOf(err), "one of them is booked on its own then")
	_, err = e.take(kim, cameras, 1, e.tomorrow(13, 0), e.tomorrow(14, 0))
	x.NoError(err)
	_, err = e.reserve(kim, kit, e.tomorrow(13, 30), e.tomorrow(14, 30), nil)
	x.Equal(codes.Aborted, codeOf(err), "the pool has the other")

	// One out of use is nobody's unit: booked on its own, it takes nothing of
	// the pool.
	e.status(one, "lost")
	_, err = e.reserve(kim, one, e.tomorrow(10, 0), e.tomorrow(11, 0), nil)
	x.NoError(err)
}

// TestAKitWaitsForAPoolsUnitThePoolCannotSpare: a kit given a member the pool
// has promised then takes it as it takes one something else holds: its
// booking waits for it, and takes it once the pool can spare it.
func TestAKitWaitsForAPoolsUnitThePoolCannotSpare(t *testing.T) {
	e := newEnv(t)
	x := e.x
	one := e.item("카메라 1", "CM-1", nil, day)
	two := e.item("카메라 2", "CM-2", nil, day)
	cameras := e.pool("카메라", one, two)
	tripod := e.item("삼각대", "TR-1", nil, day)
	kit := e.kit("촬영 키트", tripod)
	kim, _ := e.person("member")

	shoot, err := e.reserve(kim, kit, e.tomorrow(9, 0), e.tomorrow(12, 0), nil)
	x.NoError(err)
	both, err := e.take(kim, cameras, 2, e.tomorrow(10, 0), e.tomorrow(11, 0))
	x.NoError(err)

	e.relate(one, kit, true)
	got := e.allocation(shoot, one)
	x.NotNil(got)
	x.Equal("waiting", got.Kind)
	cs := e.entry(shoot).GetConflicts()
	x.Len(cs, 1)
	x.True(sameId(one.GetId(), cs[0].GetResourceId()))
	x.Zero(cs[0].GetUnits(), "a kit's member is one")

	e.cancel(kim, both)
	e.sweep()
	got = e.allocation(shoot, one)
	x.True(got.Blocking)
	x.Equal("reservation", got.Kind)
}

// TestAReservationInUseIsNotMoved: what was handed out, or checked into, is
// out; a pool with fewer to give makes the ones still to be fulfilled wait,
// made before it or not.
func TestAReservationInUseIsNotMoved(t *testing.T) {
	e := newEnv(t)
	x := e.x
	one := e.item("빔프로젝터 1", "PJ-1", nil, day)
	two := e.item("빔프로젝터 2", "PJ-2", nil, day)
	projectors := e.pool("빔프로젝터", one, two)
	kim, party := e.person("member")

	booked, err := e.take(kim, projectors, 1, e.now.Add(10*time.Minute), e.now.Add(3*time.Hour))
	x.NoError(err)
	out, err := e.take(kim, projectors, 1, e.now.Add(10*time.Minute), e.now.Add(3*time.Hour))
	x.NoError(err)
	_, err = e.app().Custody().Add(e.owner, app.CustodyAddRequest_builder{
		Party:         ref[app.PartyRef](party.GetId()),
		DueAt:         timestamppb.New(e.now.Add(3 * time.Hour)),
		ReservationId: out.GetId(),
		Lines:         []*app.CustodyLineSpec{app.CustodyLineSpec_builder{Asset: assetRef(one)}.Build()},
	}.Build())
	x.NoError(err)

	e.status(two, "disposed")
	x.True(e.holds(out, projectors), "it is out")
	x.False(e.holds(booked, projectors), "made first, and still to be picked up")
}

// TestAnOverrideTakesNothingOfAPool: a booking over what was booked, on
// purpose, blocks nothing, and neither counts against the pool nor waits.
func TestAnOverrideTakesNothingOfAPool(t *testing.T) {
	e := newEnv(t)
	x := e.x
	one := e.item("빔프로젝터 1", "PJ-1", nil, day)
	two := e.item("빔프로젝터 2", "PJ-2", nil, day)
	projectors := e.pool("빔프로젝터", one, two)
	kim, _ := e.person("member")

	booked, err := e.take(kim, projectors, 2, e.tomorrow(9, 0), e.tomorrow(12, 0))
	x.NoError(err)
	over, err := e.reserve(e.owner, projectors, e.tomorrow(9, 0), e.tomorrow(12, 0), func(r *app.ReservationAddRequest) {
		r.SetOverride(true)
		r.SetReason("행사")
	})
	x.NoError(err)

	e.status(two, "disposed")
	x.False(e.holds(booked, projectors), "two of one")
	got := e.allocation(over, projectors)
	x.Equal("override", got.Kind)
	x.False(got.Blocking)
	x.Empty(e.entry(over).GetConflicts())
}

// TestEveryOccurrenceOfASeriesFollowsItsPool: each occurrence is a
// reservation of its own, and waits, or does not, by its own time.
func TestEveryOccurrenceOfASeriesFollowsItsPool(t *testing.T) {
	e := newEnv(t)
	x := e.x
	one := e.item("빔프로젝터 1", "PJ-1", nil, day)
	two := e.item("빔프로젝터 2", "PJ-2", nil, day)
	projectors := e.pool("빔프로젝터", one, two)
	kim, _ := e.person("member")

	// Three days running, and on the third something else as well.
	daily, err := e.reserve(kim, projectors, e.tomorrow(9, 0), e.tomorrow(10, 0), func(r *app.ReservationAddRequest) { r.SetRepeat("FREQ=DAILY;COUNT=3") })
	x.NoError(err)
	third, err := e.reserve(kim, projectors, e.tomorrow(9, 0).AddDate(0, 0, 2), e.tomorrow(10, 0).AddDate(0, 0, 2), nil)
	x.NoError(err)

	e.status(two, "disposed")
	rs, err := e.app().Reservation().List(e.owner, app.ReservationListRequest_builder{
		Filters: []*app.ReservationFilter{app.ReservationFilter_builder{SeriesId: daily.GetId()}.Build()},
	}.Build())
	x.NoError(err)
	x.Len(rs.GetItems(), 3)
	for _, r := range rs.GetItems() {
		x.True(e.holds(r, projectors), "the series was made first")
	}
	x.False(e.holds(third, projectors))
}

// TestTheUnitsOfAPoolAreKeptOnItsRow: the units a pool's row says are what
// its members say, whatever changed them.
func TestTheUnitsOfAPoolAreKeptOnItsRow(t *testing.T) {
	e := newEnv(t)
	x := e.x
	one := e.item("카메라 1", "CM-1", nil, day)
	two := e.item("카메라 2", "CM-2", nil, day)
	cameras := e.pool("카메라", one, two)
	x.EqualValues(2, e.units(cameras))

	f, err := e.app().Asset().SetAttributes(e.owner, app.AssetSetAttributesRequest_builder{Ref: assetRef(two), Set: map[string]string{"status": "lost"}, At: e.ago(time.Hour)}.Build())
	x.NoError(err)
	x.Equal("lost", f.GetStatus())
	x.EqualValues(1, e.units(cameras))

	// Said never to have been lost, it is one of them again.
	tl, err := e.app().Asset().Timeline(e.owner, app.AssetTimelineRequest_builder{Ref: assetRef(two)}.Build())
	x.NoError(err)
	var row *app.TimelineEntry
	for _, en := range tl.GetEntries() {
		if en.GetKind() != "fact" || en.GetDetail()["key"] != "status" || en.HasSupersededAt() {
			continue
		}
		if row == nil || en.GetValidFrom().AsTime().After(row.GetValidFrom().AsTime()) {
			row = en
		}
	}
	x.NotNil(row)
	_, err = e.app().Asset().Correct(e.owner, app.AssetCorrectRequest_builder{Ref: assetRef(two), RowId: row.GetRowId(), Retract: true, Reason: "찾음"}.Build())
	x.NoError(err)
	x.EqualValues(2, e.units(cameras))

	// And the number is not anybody's to set.
	b := e.bookableOf(cameras)
	_, err = e.app().Bookable().Update(e.owner, app.BookableUpdateRequest_builder{Ref: ref[app.BookableRef](b.GetId()), Units: 1, Enabled: true}.Build())
	x.Equal(codes.InvalidArgument, codeOf(err))
	x.EqualValues(2, e.units(cameras))
}

// TestABookingOfAPoolAndAChangeToItAtOnceAgree: a booking of a pool and a
// change to what it can give -- a member taken off it, or out of use -- that
// arrive together land one after the other, whichever goes first: the pool
// is never booked for more than it has.
func TestABookingOfAPoolAndAChangeToItAtOnceAgree(t *testing.T) {
	for _, how := range []string{"end", "disposed", "leave"} {
		t.Run(how, func(t *testing.T) {
			e := newEnv(t)
			e.postgresOnly()
			x := e.x
			kim, _ := e.person("member")

			for round := range 4 {
				one := e.item("빔프로젝터 1", "", nil, day)
				two := e.item("빔프로젝터 2", "", nil, day)
				projectors := e.pool("빔프로젝터", one, two)
				at := e.tomorrow(9, 0).AddDate(0, 0, round)
				first, err := e.take(kim, projectors, 1, at, at.Add(3*time.Hour))
				x.NoError(err)

				var r *app.Reservation
				book := func() error {
					var err error
					r, err = e.take(kim, projectors, 1, at, at.Add(3*time.Hour))
					return err
				}
				change := func() error {
					switch how {
					case "end":
						_, err := e.app().Asset().Relate(e.owner, app.AssetRelateRequest_builder{Ref: assetRef(two), Target: assetRef(projectors), Kind: "member_of", End: true}.Build())
						return err
					case "leave":
						// Disposed of, and out of the pool in the same change.
						_, err := e.app().Asset().SetAttributes(e.owner, app.AssetSetAttributesRequest_builder{Ref: assetRef(two), Set: map[string]string{"status": "disposed"}, Leave: []*app.AssetRef{assetRef(projectors)}}.Build())
						return err
					}
					_, err := e.app().Asset().SetAttributes(e.owner, app.AssetSetAttributesRequest_builder{Ref: assetRef(two), Set: map[string]string{"status": how}}.Build())
					return err
				}

				bookingFirst := round%2 == 0
				var booked, changed error
				if bookingFirst {
					booked, changed = e.inTurn(projectors, book, change)
				} else {
					changed, booked = e.inTurn(projectors, change, book)
				}
				x.NoError(changed, "round %d", round)
				x.True(e.holds(first, projectors), "round %d", round)
				if bookingFirst {
					x.NoError(booked, "round %d", round)
					x.False(e.holds(r, projectors), "round %d: booked past what it has", round)
				} else {
					x.Equal(codes.Aborted, codeOf(booked), "round %d: %v", round, booked)
				}
				x.EqualValues(1, e.units(projectors), "round %d", round)
			}
		})
	}
}

// TestABookingOfAPoolsUnitAndAChangeToItsPoolAtOnceAgree: a booking of a thing
// on its own and the thing given to a pool, together, land one after the
// other: the pool's bookings are never more than it has with the thing's.
func TestABookingOfAPoolsUnitAndAChangeToItsPoolAtOnceAgree(t *testing.T) {
	e := newEnv(t)
	e.postgresOnly()
	x := e.x
	kim, _ := e.person("member")

	for round := range 4 {
		one := e.item("카메라 1", "", nil, day)
		two := e.item("카메라 2", "", nil, day)
		e.bookable(two, nil)
		cameras := e.pool("카메라", one)
		at := e.tomorrow(9, 0).AddDate(0, 0, round)
		pooled, err := e.take(kim, cameras, 1, at, at.Add(3*time.Hour))
		x.NoError(err)
		// Given two, it has two; and two of it is what it is booked for.
		var mine *app.Reservation
		book := func() error {
			var err error
			mine, err = e.reserve(kim, two, at, at.Add(3*time.Hour), nil)
			return err
		}
		relate := func() error {
			_, err := e.app().Asset().Relate(e.owner, app.AssetRelateRequest_builder{Ref: assetRef(two), Target: assetRef(cameras), Kind: "member_of"}.Build())
			return err
		}
		more := func() {
			_, err := e.take(kim, cameras, 1, at, at.Add(3*time.Hour))
			x.Equal(codes.Aborted, codeOf(err), "round %d: two is booked on its own", round)
		}

		bookingFirst := round%2 == 0
		var booked, related error
		if bookingFirst {
			booked, related = e.inTurn(two, book, relate)
		} else {
			related, booked = e.inTurn(two, relate, book)
		}
		x.NoError(related, "round %d", round)
		if bookingFirst {
			x.NoError(booked, "round %d", round)
		} else {
			// It looked before two was the pool's, and is asked again.
			x.Equal(codes.Aborted, codeOf(booked), "round %d: %v", round, booked)
			x.NoError(book(), "round %d, asked again", round)
		}
		x.True(e.holds(pooled, cameras), "round %d", round)
		x.NotNil(e.allocation(mine, two), "round %d", round)
		more()
	}
}

// TestWhatAPoolDidToItsBookingsIsOnTheHistoryOfEach: a booking a pool made
// wait, and the one it gave what it waited for, is an event of the booking's
// and one of the pool's, by how many of it, under the change that did it --
// and, for what the sweep settles, an operation of the sweep's own (design
// D39).
func TestWhatAPoolDidToItsBookingsIsOnTheHistoryOfEach(t *testing.T) {
	e := newEnv(t)
	x := e.x
	one := e.item("빔프로젝터 1", "PJ-1", nil, day)
	two := e.item("빔프로젝터 2", "PJ-2", nil, day)
	projectors := e.pool("빔프로젝터", one, two)
	kim, _ := e.person("member")
	lee, _ := e.person("member")

	_, err := e.take(kim, projectors, 1, e.tomorrow(9, 0), e.tomorrow(12, 0))
	x.NoError(err)
	class, err := e.reserve(lee, projectors, e.tomorrow(10, 0), e.tomorrow(11, 0), func(r *app.ReservationAddRequest) { r.SetName("교육") })
	x.NoError(err)

	e.status(two, "disposed")
	set := e.about("asset.set")[idUUID(two.GetId())]
	x.NotNil(set)
	waits := e.about("reservation.wait")
	x.ElementsMatch([]uuid.UUID{idUUID(class.GetId()), idUUID(projectors.GetId())}, subjects(waits), "the booking, and the pool")
	for _, v := range waits {
		x.NotNil(v.CauseId)
		x.Equal(set.Id, *v.CauseId, "part of what disposed of it")
	}
	w := value[*app.ReservationAllocationChanged](e, waits[idUUID(projectors.GetId())])
	x.Equal(class.GetId(), w.GetReservation())
	x.Equal(projectors.GetId(), w.GetAsset())
	x.EqualValues(1, w.GetUnits(), "a pool's, by how many")
	said := fmt.Sprintf("예약 대기: 빔프로젝터 1개 (교육 %s)", begins(class))
	x.Contains(e.history(class), said)
	x.Equal([]string{said}, summaries(e.happened(projectors, "reservation.wait")))

	e.status(two, "active")
	back := e.eventsOf("asset.set")
	resolves := e.about("reservation.resolve")
	x.ElementsMatch([]uuid.UUID{idUUID(class.GetId()), idUUID(projectors.GetId())}, subjects(resolves))
	for _, v := range resolves {
		x.NotNil(v.CauseId)
		x.True(slices.ContainsFunc(back, func(s *ent.Event) bool { return s.Id == *v.CauseId && s.Id != set.Id }), "part of what had it back")
	}
	x.Contains(e.history(class), fmt.Sprintf("예약 확보: 빔프로젝터 1개 (교육 %s)", begins(class)))
}

// TestWhatTheSweepGaveAPoolsBookingIsAnOperationOfItsOwn: a wait nothing that
// let go of the pool settled is settled by the sweep, as the deployment's own
// work: the booking's event is the operation, and the pool's its child.
func TestWhatTheSweepGaveAPoolsBookingIsAnOperationOfItsOwn(t *testing.T) {
	e := newEnv(t)
	x := e.x
	one := e.item("카메라 1", "CM-1", nil, day)
	cameras := e.pool("카메라", one)
	kim, _ := e.person("member")
	r, err := e.take(kim, cameras, 1, e.tomorrow(9, 0), e.tomorrow(12, 0))
	x.NoError(err)
	e.unrelate(one, cameras)
	x.False(e.holds(r, cameras))
	_, err = e.app().Bookable().Update(e.owner, app.BookableUpdateRequest_builder{Ref: ref[app.BookableRef](e.bookableOf(cameras).GetId()), Units: 1, Enabled: true}.Build())
	x.NoError(err)

	e.sweep()
	x.True(e.holds(r, cameras))
	resolves := e.about("reservation.resolve")
	op, kid := resolves[idUUID(r.GetId())], resolves[idUUID(cameras.GetId())]
	x.NotNil(op)
	x.NotNil(kid)
	x.Nil(op.CauseId, "the sweep's own")
	x.Equal(op.Id, *kid.CauseId)
	x.EqualValues(1, value[*app.ReservationAllocationChanged](e, kid).GetUnits())
}

// TestASweepThatFindsNothingToTakeHoldsNothing: what waits and has nothing
// to take is read, and nothing is held for it -- a pool's bookings may wait
// for days, and a pass a minute that held what each waits for wrote its
// schedule every minute.
func TestASweepThatFindsNothingToTakeHoldsNothing(t *testing.T) {
	e := newEnv(t)
	x := e.x
	one := e.item("카메라 1", "CM-1", nil, day)
	two := e.item("카메라 2", "CM-2", nil, day)
	cameras := e.pool("카메라", one, two)
	kim, _ := e.person("member")
	_, err := e.take(kim, cameras, 1, e.tomorrow(9, 0), e.tomorrow(12, 0))
	x.NoError(err)
	later, err := e.take(kim, cameras, 1, e.tomorrow(9, 0), e.tomorrow(12, 0))
	x.NoError(err)
	e.status(two, "disposed")
	x.False(e.holds(later, cameras))

	at := e.bookableOf(cameras).GetScheduleVersion()
	e.sweep()
	x.False(e.holds(later, cameras))
	x.Equal(at, e.bookableOf(cameras).GetScheduleVersion(), "nothing held")
}

// TestWhatAKitLetsGoOfIsItsPoolsToGiveAtOnce: a member a kit's booking lets
// go of is one more for its pool to give then, and the pool's booking that
// waited takes it in the change that let it go.
func TestWhatAKitLetsGoOfIsItsPoolsToGiveAtOnce(t *testing.T) {
	e := newEnv(t)
	x := e.x
	m := e.item("카메라 1", "CM-1", nil, day)
	n := e.item("카메라 2", "CM-2", nil, day)
	cameras := e.pool("카메라", m, n)
	kit := e.kit("촬영 키트", m)
	kim, _ := e.person("member")

	at, until := e.tomorrow(9, 0), e.tomorrow(12, 0)
	_, err := e.reserve(kim, kit, at, until, nil)
	x.NoError(err)
	first, err := e.take(kim, cameras, 1, at, until)
	x.NoError(err, "the other camera")
	o := e.item("카메라 3", "CM-3", nil, day)
	e.relate(o, cameras, false)
	later, err := e.take(kim, cameras, 1, at, until)
	x.NoError(err, "the one given it")
	e.status(o, "disposed")
	x.True(e.holds(first, cameras))
	x.False(e.holds(later, cameras), "the kit has one, and the first booking the other")

	e.unrelate(m, kit)
	x.True(e.holds(later, cameras), "let go of by the kit, and the pool's to give")
}
