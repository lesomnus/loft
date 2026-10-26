package domain_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/protobuf/types/known/timestamppb"

	app "github.com/lesomnus/rove"
	"github.com/lesomnus/rove/internal/ent"
	"github.com/lesomnus/rove/internal/ent/allocation"
	"github.com/lesomnus/rove/internal/ent/event"
	"github.com/lesomnus/rove/server/domain"
)

// What somebody has out takes its time (design 4, D53): from the hand-over
// until a loan is due back, or, of an issue and of a loan past its due, until
// it is back. Nothing is booked over it, a pool has one fewer while it is out,
// and what was booked before it keeps its time, says so, and is told.

// lend hands `things` to `who` as a loan due back at `due`.
func (e *env) lend(who *app.Party, due time.Time, things ...*app.Asset) *app.Custody {
	e.t.Helper()
	return e.handOver(who, timestamppb.New(due), things...)
}

// issue hands `things` to `who` for keeps.
func (e *env) issue(who *app.Party, things ...*app.Asset) *app.Custody {
	e.t.Helper()
	return e.handOver(who, nil, things...)
}

func (e *env) handOver(who *app.Party, due *timestamppb.Timestamp, things ...*app.Asset) *app.Custody {
	e.t.Helper()
	lines := []*app.CustodyLineSpec{}
	for _, a := range things {
		lines = append(lines, app.CustodyLineSpec_builder{Asset: assetRef(a)}.Build())
	}
	req := app.CustodyAddRequest_builder{Party: ref[app.PartyRef](who.GetId()), Lines: lines}.Build()
	if due != nil {
		req.SetDueAt(due)
	}
	c, err := e.app().Custody().Add(e.owner, req)
	e.x.NoError(err)
	return c
}

// giveBack takes back all that `c` has out.
func (e *env) giveBack(c *app.Custody) {
	e.t.Helper()
	_, err := e.app().Custody().Return(e.owner, app.CustodyReturnRequest_builder{Ref: ref[app.CustodyRef](c.GetId())}.Build())
	e.x.NoError(err)
}

// out is what the calendar says `r` takes that somebody has out, by tag and
// how: "CM-1 loan", "CM-1 loan late", "CM-1 issue".
func (e *env) out(r *app.Reservation) []string {
	e.t.Helper()
	vs := []string{}
	for _, v := range e.entry(r).GetOut() {
		s := v.GetResourceTag() + " " + v.GetKind()
		if v.GetOverdue() {
			s += " late"
		}
		vs = append(vs, s)
	}
	return vs
}

// hold is what `c` holds of `a`, if anything.
func (e *env) hold(c *app.Custody, a *app.Asset) *ent.Allocation {
	e.t.Helper()
	vs, err := e.s.Ent.Allocation.Query().Where(allocation.RefId(idUUID(c.GetId())), allocation.ResourceId(idUUID(a.GetId()))).All(context.Background())
	e.x.NoError(err)
	e.x.LessOrEqual(len(vs), 1, "one hold of a thing for a hand-over")
	if len(vs) == 0 {
		return nil
	}
	return vs[0]
}

// TestWhatIsOutIsNotBookedThen: a loan holds what it lent from its hand-over
// until it is due back, and the buffer after, and an issue until it is
// returned; a booking over either is refused, saying until when; and once it
// is back its time is free.
func TestWhatIsOutIsNotBookedThen(t *testing.T) {
	e := newEnv(t)
	x := e.x
	camera := e.item("카메라", "CM-1", nil, day)
	e.bookable(camera, func(r *app.BookableAddRequest) { r.SetBufferAfter(30) })
	laptop := e.item("노트북", "NB-1", nil, day)
	e.bookable(laptop, nil)
	kim, _ := e.person("member")
	_, lee := e.person("member")

	loan := e.lend(lee, e.tomorrow(12, 0), camera)
	h := e.hold(loan, camera)
	x.NotNil(h)
	x.Equal("custody", h.Kind)
	x.True(h.Blocking)
	x.False(h.Exclusive, "held to by the domain: it lies over what was booked before it")
	x.True(h.BeginsAt.Equal(e.now))
	x.True(h.EndsAt.Equal(e.tomorrow(12, 30)), "until it is due back, and its buffer after: %s", h.EndsAt)

	_, err := e.reserve(kim, camera, e.tomorrow(10, 0), e.tomorrow(11, 0), nil)
	x.Equal(codes.Aborted, codeOf(err))
	x.Contains(err.Error(), "대여 중")
	_, err = e.reserve(kim, camera, e.tomorrow(12, 0), e.tomorrow(13, 0), nil)
	x.Equal(codes.Aborted, codeOf(err), "its buffer after it is due is its time too")
	_, err = e.reserve(kim, camera, e.tomorrow(12, 30), e.tomorrow(13, 30), nil)
	x.NoError(err, "after it is due back and ready")

	given := e.issue(lee, laptop)
	x.True(e.hold(given, laptop).EndsAt.Year() == 2200, "for keeps: until it is returned")
	_, err = e.reserve(kim, laptop, e.tomorrow(10, 0).AddDate(1, 0, 0), e.tomorrow(11, 0).AddDate(1, 0, 0), nil)
	x.Equal(codes.Aborted, codeOf(err))
	x.Contains(err.Error(), "지급되어")

	// Back, early: what it held of the rest is free.
	e.giveBack(loan)
	e.giveBack(given)
	h = e.hold(loan, camera)
	x.False(h.Blocking)
	x.True(h.EndsAt.Equal(e.now), "out until it came back")
	_, err = e.reserve(kim, camera, e.tomorrow(10, 0), e.tomorrow(11, 0), nil)
	x.NoError(err)
	_, err = e.reserve(kim, laptop, e.tomorrow(10, 0), e.tomorrow(11, 0), nil)
	x.NoError(err)
}

// TestWhatIsHandedOverIsNotRefusedForWhatWasBooked: the desk hands out what was
// booked for later. The booking keeps its time and says what is out then, and
// whoever runs the reservations and whoever it is for are told, by mail as
// well, and not whoever handed it out; once it is back it says so no more, and
// whoever it is for is told that too.
func TestWhatIsHandedOverIsNotRefusedForWhatWasBooked(t *testing.T) {
	e := newEnv(t)
	x := e.x
	camera := e.item("카메라", "CM-1", nil, day)
	e.bookable(camera, nil)
	kim, _ := e.person("member")
	_, lee := e.person("member")
	boss, _ := e.person("manager")

	r, err := e.reserve(kim, camera, e.tomorrow(10, 0), e.tomorrow(12, 0), nil)
	x.NoError(err)
	loan := e.lend(lee, e.tomorrow(11, 0), camera)

	got := e.allocation(r, camera)
	x.True(got.Blocking, "it keeps its time")
	x.Equal("reservation", got.Kind)
	x.Equal([]string{"CM-1 loan"}, e.out(r))
	v := e.entry(r).GetOut()[0]
	x.True(sameId(loan.GetId(), v.GetCustodyId()))
	x.True(v.GetDueAt().AsTime().Equal(e.tomorrow(11, 0)))

	mine := e.notices(kim, "reservation.out")
	x.Len(mine, 1)
	x.True(sameId(r.GetId(), mine[0].GetSubjectId()))
	x.Contains(mine[0].GetDesc(), "CM-1 카메라")
	x.True(domain.Mailed["reservation.out"], "by mail as well, where the deployment sends any")
	run := e.notices(boss, "reservation.out")
	x.Len(run, 1)
	x.NotNil(about(run, camera))
	x.Empty(e.notices(e.owner, "reservation.out"), "not whoever handed it out")
	for _, n := range append(mine, run...) {
		x.NotContains(n.GetDesc(), lee.GetName(), "nobody is told who has it")
	}

	// Nor is anything else booked over either.
	_, err = e.reserve(kim, camera, e.tomorrow(9, 0), e.tomorrow(10, 0), nil)
	x.Equal(codes.Aborted, codeOf(err))

	e.giveBack(loan)
	x.Empty(e.out(r))
	back := e.notices(kim, "reservation.resolved")
	x.Len(back, 1)
	x.Contains(back[0].GetName(), "돌아옴")
}

// TestAPoolHasOneFewerWhileAMemberIsOut: a member lent at the desk is not one of
// its pool's to give while it is out: where the pool's bookings no longer fit,
// the one made last waits, and whoever has to settle it is told; once it is
// back, what waited takes it.
func TestAPoolHasOneFewerWhileAMemberIsOut(t *testing.T) {
	e := newEnv(t)
	x := e.x
	one := e.item("카메라 1", "CM-1", nil, day)
	two := e.item("카메라 2", "CM-2", nil, day)
	cameras := e.pool("카메라", one, two)
	kim, _ := e.person("member")
	lee, _ := e.person("member")
	_, park := e.person("member")

	first, err := e.take(kim, cameras, 1, e.tomorrow(9, 0), e.tomorrow(12, 0))
	x.NoError(err)
	later, err := e.take(lee, cameras, 1, e.tomorrow(10, 0), e.tomorrow(11, 0))
	x.NoError(err)

	loan := e.lend(park, e.tomorrow(10, 30), one)
	x.True(e.holds(first, cameras), "made first, kept")
	x.False(e.holds(later, cameras), "made last, waits")
	x.Contains(e.told(lee), "reservation.conflict")
	cs := e.entry(later).GetConflicts()
	x.Len(cs, 1)
	x.EqualValues(0, cs[0].GetAvailable())
	_, err = e.take(kim, cameras, 1, e.tomorrow(10, 0), e.tomorrow(10, 15))
	x.Equal(codes.Aborted, codeOf(err), "one fewer to give while it is out")

	// What it did is on the reservation's history, caused by the hand-over.
	ctx := context.Background()
	w, err := e.s.Ent.Event.Query().Where(event.Kind("reservation.wait"), event.SubjectId(idUUID(later.GetId()))).Only(ctx)
	x.NoError(err)
	x.NotNil(w.CauseId)
	issued, err := e.s.Ent.Event.Query().Where(event.Kind("custody.issue"), event.SubjectId(idUUID(loan.GetId()))).Only(ctx)
	x.NoError(err)
	x.Equal(issued.Id, *w.CauseId, "the hand-over its cause")

	e.giveBack(loan)
	x.True(e.holds(later, cameras), "back, and what waited has it")
	x.Contains(e.told(lee), "reservation.resolved")
}

// TestAKitsBookingSaysItsMemberIsOut: a member of a kit lent at the desk over
// a booking of the kit is said of the booking, which keeps it; a booking of the
// kit over it is refused; and a member given to the kit while it is out is
// waited for until it is back.
func TestAKitsBookingSaysItsMemberIsOut(t *testing.T) {
	e := newEnv(t)
	x := e.x
	camera := e.item("카메라", "CM-1", nil, day)
	tripod := e.item("삼각대", "TR-1", nil, day)
	light := e.item("조명", "LT-1", nil, day)
	kit := e.kit("촬영 키트", camera, tripod)
	kim, _ := e.person("member")
	_, lee := e.person("member")

	r, err := e.reserve(kim, kit, e.tomorrow(9, 0), e.tomorrow(12, 0), nil)
	x.NoError(err)
	loan := e.lend(lee, e.tomorrow(10, 0), tripod, light)
	x.Equal([]string{"TR-1 loan"}, e.out(r))
	x.True(e.allocation(r, tripod).Blocking)
	_, err = e.reserve(kim, kit, e.tomorrow(8, 0), e.tomorrow(9, 0), nil)
	x.Equal(codes.Aborted, codeOf(err), "its member is out then")

	e.relate(light, kit, true)
	got := e.allocation(r, light)
	x.NotNil(got)
	x.Equal("waiting", got.Kind, "out when the kit was given it")
	held := e.entry(r).GetConflicts()
	x.Len(held, 1)
	x.Len(held[0].GetHeldBy(), 1)
	x.Equal("custody", held[0].GetHeldBy()[0].GetKind())
	x.Equal("대여", held[0].GetHeldBy()[0].GetLabel())

	// Back, and taken -- by the sweep, of a member nobody may book on its
	// own, whose schedule the return does not hold (D38).
	e.giveBack(loan)
	x.Empty(e.out(r))
	e.sweep()
	got = e.allocation(r, light)
	x.Equal("reservation", got.Kind, "back, and taken")
	x.True(got.Blocking)
}

// TestALateLoanHoldsWhatItLentUntilItIsBack: once a loan is past its due and
// what it lent is not back, it holds it until it is: a booking after its due
// says it is late, whoever it is for and whoever runs the reservations are
// told, whoever has it is told a booking waits, and nothing more is booked
// over it until it is back.
func TestALateLoanHoldsWhatItLentUntilItIsBack(t *testing.T) {
	e := newEnv(t)
	x := e.x
	camera := e.item("카메라", "CM-1", nil, day)
	e.bookable(camera, nil)
	kim, _ := e.person("member")
	leeCtx, lee := e.person("member")
	boss, _ := e.person("manager")

	loan := e.lend(lee, e.now.Add(time.Hour), camera)
	r, err := e.reserve(kim, camera, e.tomorrow(10, 0), e.tomorrow(11, 0), nil)
	x.NoError(err, "after it is due back")
	x.Empty(e.out(r))

	e.now = e.now.Add(2 * time.Hour)
	e.sweep()
	x.True(e.hold(loan, camera).EndsAt.Year() == 2200, "late, until it is back")
	x.Equal([]string{"CM-1 loan late"}, e.out(r))
	x.Len(e.notices(kim, "reservation.out"), 1)
	x.NotNil(about(e.notices(boss, "reservation.out"), camera))
	late := e.notices(leeCtx, "custody.overdue")
	x.Len(late, 1)
	x.Contains(late[0].GetDesc(), "기다리고 있습니다")
	_, err = e.reserve(kim, camera, e.tomorrow(13, 0), e.tomorrow(14, 0), nil)
	x.Equal(codes.Aborted, codeOf(err))
	x.Contains(err.Error(), "반납 기한")

	// Swept again, nobody is told again.
	e.sweep()
	x.Len(e.notices(kim, "reservation.out"), 1)

	e.giveBack(loan)
	x.Empty(e.out(r))
	x.Len(e.notices(kim, "reservation.resolved"), 1)
	_, err = e.reserve(kim, camera, e.tomorrow(13, 0), e.tomorrow(14, 0), nil)
	x.NoError(err)
}

// TestALoanDueLaterHoldsLonger: a loan's due moved later takes what was booked
// then, which is told; moved sooner, it lets go of it, and whoever it is for is
// told. A loan late given a due again holds until then.
func TestALoanDueLaterHoldsLonger(t *testing.T) {
	e := newEnv(t)
	x := e.x
	camera := e.item("카메라", "CM-1", nil, day)
	e.bookable(camera, nil)
	kim, _ := e.person("member")
	_, lee := e.person("member")

	loan := e.lend(lee, e.tomorrow(9, 0), camera)
	r, err := e.reserve(kim, camera, e.tomorrow(10, 0), e.tomorrow(11, 0), nil)
	x.NoError(err)

	extend := func(to time.Time) {
		_, err := e.app().Custody().Extend(e.owner, app.CustodyExtendRequest_builder{Ref: ref[app.CustodyRef](loan.GetId()), DueAt: timestamppb.New(to)}.Build())
		x.NoError(err)
	}
	extend(e.tomorrow(12, 0))
	x.Equal([]string{"CM-1 loan"}, e.out(r))
	x.Len(e.notices(kim, "reservation.out"), 1)
	x.True(e.hold(loan, camera).EndsAt.Equal(e.tomorrow(12, 0)))

	extend(e.tomorrow(9, 30))
	x.Empty(e.out(r))
	back := e.notices(kim, "reservation.resolved")
	x.Len(back, 1)
	x.Contains(back[0].GetName(), "앞당겨짐")
	_, err = e.reserve(kim, camera, e.tomorrow(9, 30), e.tomorrow(10, 0), nil)
	x.NoError(err, "free from when it is due back")

	// An issue given a due is a loan until then.
	laptop := e.item("노트북", "NB-1", nil, day)
	given := e.issue(lee, laptop)
	_, err = e.app().Custody().Extend(e.owner, app.CustodyExtendRequest_builder{Ref: ref[app.CustodyRef](given.GetId()), DueAt: timestamppb.New(e.tomorrow(8, 0))}.Build())
	x.NoError(err)
	x.True(e.hold(given, laptop).EndsAt.Equal(e.tomorrow(8, 0)))
}

// TestAPickupHoldsWhatItHandsOutUntilItIsDue: a pickup is a hand-over too: what
// it hands out is out until it is due back, which may be after the reservation
// ends, and what it hands out besides what was booked is out as anything is.
func TestAPickupHoldsWhatItHandsOutUntilItIsDue(t *testing.T) {
	e := newEnv(t)
	x := e.x
	camera := e.item("카메라", "CM-1", nil, day)
	lens := e.item("렌즈", "LN-1", nil, day)
	e.bookable(camera, nil)
	e.bookable(lens, nil)
	kim, who := e.person("member")

	r, err := e.reserve(kim, camera, e.now.Add(-time.Hour), e.now.Add(2*time.Hour), nil)
	x.NoError(err)
	c, err := e.app().Custody().Add(e.owner, app.CustodyAddRequest_builder{
		Party:         ref[app.PartyRef](who.GetId()),
		DueAt:         timestamppb.New(e.now.Add(5 * time.Hour)),
		ReservationId: r.GetId(),
		Lines: []*app.CustodyLineSpec{
			app.CustodyLineSpec_builder{Asset: assetRef(camera)}.Build(),
			app.CustodyLineSpec_builder{Asset: assetRef(lens)}.Build(),
		},
	}.Build())
	x.NoError(err)
	x.NotNil(e.hold(c, camera))
	x.NotNil(e.hold(c, lens))
	_, err = e.reserve(kim, camera, e.now.Add(3*time.Hour), e.now.Add(4*time.Hour), nil)
	x.Equal(codes.Aborted, codeOf(err), "out past the reservation, until it is due")
	_, err = e.reserve(kim, lens, e.now.Add(time.Hour), e.now.Add(2*time.Hour), nil)
	x.Equal(codes.Aborted, codeOf(err), "handed out with it")
	x.Empty(e.out(r), "a reservation in use is not out to itself")

	e.giveBack(c)
	_, err = e.reserve(kim, camera, e.now.Add(3*time.Hour), e.now.Add(4*time.Hour), nil)
	x.NoError(err)
}

// TestWhatIsOutShowsOnTheCalendarOfIt: what a hand-over holds is busy on the
// calendar of what it holds, as what it is, naming nobody.
func TestWhatIsOutShowsOnTheCalendarOfIt(t *testing.T) {
	e := newEnv(t)
	x := e.x
	camera := e.item("카메라", "CM-1", nil, day)
	e.bookable(camera, nil)
	_, lee := e.person("member")

	e.lend(lee, e.tomorrow(12, 0), camera)
	v, err := e.app().Bookable().Availability(e.owner, app.BookableAvailabilityRequest_builder{
		Resources: []*app.AssetRef{assetRef(camera)},
		From:      timestamppb.New(e.now),
		To:        timestamppb.New(e.now.AddDate(0, 0, 2)),
	}.Build())
	x.NoError(err)
	x.Len(v.GetBusy(), 1)
	b := v.GetBusy()[0]
	x.Equal("custody", b.GetKind())
	x.Equal("대여", b.GetLabel())
	x.True(b.GetBlocking())
	x.True(b.GetEndsAt().AsTime().Equal(e.tomorrow(12, 0)))
}

// TestWhatWasOutBeforeIsHeldOnce: what a build from before handed over holds
// none of its time; once held -- as a migration does -- it holds it from its
// hand-over, a pool's latest booking waits, and what was booked over it says
// so. Asked again, nothing is left to hold.
func TestWhatWasOutBeforeIsHeldOnce(t *testing.T) {
	e := newEnv(t)
	x := e.x
	ctx := context.Background()
	one := e.item("카메라 1", "CM-1", nil, day)
	two := e.item("카메라 2", "CM-2", nil, day)
	cameras := e.pool("카메라", one, two)
	laptop := e.item("노트북", "NB-1", nil, day)
	e.bookable(laptop, nil)
	kim, _ := e.person("member")
	lee, _ := e.person("member")
	_, park := e.person("member")

	issued := e.issue(park, laptop)
	loan := e.lend(park, e.tomorrow(12, 0), one)
	// As a build from before left them: out, and holding nothing.
	_, err := e.s.Ent.Allocation.Delete().Where(allocation.Kind("custody")).Exec(ctx)
	x.NoError(err)
	booked, err := e.reserve(kim, laptop, e.tomorrow(10, 0), e.tomorrow(11, 0), nil)
	x.NoError(err, "nothing held it")
	first, err := e.take(kim, cameras, 1, e.tomorrow(9, 0), e.tomorrow(12, 0))
	x.NoError(err)
	later, err := e.take(lee, cameras, 1, e.tomorrow(10, 0), e.tomorrow(11, 0))
	x.NoError(err, "the pool counted what was out")

	ts, err := domain.Unheld(ctx, e.s.Ent)
	x.NoError(err)
	x.Len(ts, 1)
	x.Equal(e.tenant, ts[0])
	n, err := e.s.Deps.HoldOut(ctx, e.s.Base, e.s.Drv, e.tenant)
	x.NoError(err)
	x.Equal(2, n)

	h := e.hold(issued, laptop)
	x.NotNil(h)
	x.True(h.BeginsAt.Equal(issued.GetIssuedAt().AsTime()), "from its hand-over")
	x.True(e.hold(loan, one).EndsAt.Equal(e.tomorrow(12, 0)))
	x.Equal([]string{"NB-1 issue"}, e.out(booked))
	x.Contains(e.told(kim), "reservation.out")
	x.True(e.holds(first, cameras))
	x.False(e.holds(later, cameras), "made last, waits")

	ts, err = domain.Unheld(ctx, e.s.Ent)
	x.NoError(err)
	x.Empty(ts)
	n, err = e.s.Deps.HoldOut(ctx, e.s.Base, e.s.Drv, e.tenant)
	x.NoError(err)
	x.Zero(n, "nothing left to hold")
}

// TestWhatIsOutIsUsageBesideTheBookings: the report of how what may be booked
// was used says what was handed over of it with no reservation, and for how
// long, beside what was booked.
func TestWhatIsOutIsUsageBesideTheBookings(t *testing.T) {
	e := newEnv(t)
	x := e.x
	camera := e.item("카메라", "CM-1", nil, day)
	e.bookable(camera, nil)
	_, lee := e.person("member")

	loan := e.lend(lee, e.now.Add(3*time.Hour), camera)
	e.now = e.now.Add(2 * time.Hour)
	e.giveBack(loan)
	e.lend(lee, e.now.Add(time.Hour), camera)
	e.now = e.now.Add(30 * time.Minute)

	v, err := e.app().Bookable().Usage(e.owner, &app.BookableUsageRequest{})
	x.NoError(err)
	var row *app.BookableUsageRow
	for _, r := range v.GetRows() {
		if r.GetTag() == "CM-1" {
			row = r
		}
	}
	x.NotNil(row)
	x.EqualValues(2, row.GetHandedOut())
	x.EqualValues(150, row.GetHandedOutMinutes(), "two hours back, and half an hour still out")
	x.Zero(row.GetReservations())
	x.Zero(row.GetBookedMinutes())
}

// TestAHoldIsSaidOfNobodyElsesTimeline: a hand-over's hold is the thing's
// time, and none of a reservation's history.
func TestAHoldIsSaidOfNobodyElsesTimeline(t *testing.T) {
	e := newEnv(t)
	x := e.x
	camera := e.item("카메라", "CM-1", nil, day)
	e.bookable(camera, nil)
	kim, _ := e.person("member")
	_, lee := e.person("member")

	r, err := e.reserve(kim, camera, e.tomorrow(10, 0), e.tomorrow(12, 0), nil)
	x.NoError(err)
	e.lend(lee, e.tomorrow(11, 0), camera)
	tl, err := e.app().Reservation().Timeline(e.owner, app.ReservationTimelineRequest_builder{Ref: ref[app.ReservationRef](r.GetId())}.Build())
	x.NoError(err)
	for _, en := range tl.GetEntries() {
		x.False(strings.Contains(en.GetSummary(), lee.GetName()), "nobody is named who has it: %s", en.GetSummary())
	}
}

// TestABookingAndAHandOverOfItAtOnceAgree: a booking of a thing and its
// hand-over over the same time, together, land one after the other: the
// booking first is kept and says the thing is out, and is told; the hand-over
// first refuses the booking.
func TestABookingAndAHandOverOfItAtOnceAgree(t *testing.T) {
	e := newEnv(t)
	e.postgresOnly()
	x := e.x
	kim, _ := e.person("member")
	_, lee := e.person("member")

	for round := range 4 {
		camera := e.item("카메라", "", nil, day)
		e.bookable(camera, nil)
		at := e.tomorrow(9, 0).AddDate(0, 0, round)
		var r *app.Reservation
		var c *app.Custody
		book := func() error {
			var err error
			r, err = e.reserve(kim, camera, at, at.Add(2*time.Hour), nil)
			return err
		}
		lend := func() error {
			var err error
			c, err = e.app().Custody().Add(e.owner, app.CustodyAddRequest_builder{
				Party: ref[app.PartyRef](lee.GetId()),
				DueAt: timestamppb.New(at.Add(time.Hour)),
				Lines: []*app.CustodyLineSpec{app.CustodyLineSpec_builder{Asset: assetRef(camera)}.Build()},
			}.Build())
			return err
		}
		told := len(e.notices(kim, "reservation.out"))
		if round%2 == 0 {
			booked, lent := e.inTurn(camera, book, lend)
			x.NoError(booked, "round %d", round)
			x.NoError(lent, "round %d: the desk hands it out", round)
			x.Equal([]string{camera.GetTag() + " loan"}, e.out(r), "round %d", round)
			x.Len(e.notices(kim, "reservation.out"), told+1, "round %d", round)
		} else {
			lent, booked := e.inTurn(camera, lend, book)
			x.NoError(lent, "round %d", round)
			x.Equal(codes.Aborted, codeOf(booked), "round %d: %v", round, booked)
		}
		x.NotNil(e.hold(c, camera), "round %d", round)
	}
}

// TestAPoolsBookingAndAHandOverOfItsMemberAtOnceAgree: a booking of all of a
// pool and a hand-over of one of its members, together, land one after the
// other: the booking first waits for what the hand-over took, and the
// hand-over first leaves the pool too few to book.
func TestAPoolsBookingAndAHandOverOfItsMemberAtOnceAgree(t *testing.T) {
	e := newEnv(t)
	e.postgresOnly()
	x := e.x
	kim, _ := e.person("member")
	_, lee := e.person("member")

	for round := range 4 {
		one := e.item("카메라 1", "", nil, day)
		two := e.item("카메라 2", "", nil, day)
		cameras := e.pool("카메라", one, two)
		at := e.tomorrow(9, 0).AddDate(0, 0, round)
		var r *app.Reservation
		book := func() error {
			var err error
			r, err = e.take(kim, cameras, 2, at, at.Add(2*time.Hour))
			return err
		}
		lend := func() error {
			_, err := e.app().Custody().Add(e.owner, app.CustodyAddRequest_builder{
				Party: ref[app.PartyRef](lee.GetId()),
				DueAt: timestamppb.New(at.Add(time.Hour)),
				Lines: []*app.CustodyLineSpec{app.CustodyLineSpec_builder{Asset: assetRef(one)}.Build()},
			}.Build())
			return err
		}
		if round%2 == 0 {
			booked, lent := e.inTurn(cameras, book, lend)
			x.NoError(booked, "round %d", round)
			x.NoError(lent, "round %d", round)
			x.False(e.holds(r, cameras), "round %d: it waits for what went out", round)
		} else {
			lent, booked := e.inTurn(cameras, lend, book)
			x.NoError(lent, "round %d", round)
			x.Equal(codes.Aborted, codeOf(booked), "round %d: %v", round, booked)
		}
	}
}
