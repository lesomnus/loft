package domain_test

import (
	"context"
	"testing"
	"time"

	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/lesomnus/payday/frame"
	"github.com/lesomnus/payday/pdid"

	app "github.com/lesomnus/loft"
	"github.com/lesomnus/loft/cmd"
	"github.com/lesomnus/loft/internal/ent/notification"
	"github.com/lesomnus/loft/internal/levyantest"
	"github.com/lesomnus/loft/server/domain"
)

// What the deployment's own work tells a workspace levyan makes read-only
// (D67, design 7.13): what became of its bookings, as ever, and nothing that
// asks anybody to do what a read-only workspace does not -- which it is told
// once it writes again, of what is so then, and once. And what a person is
// told stays theirs to quiet.

// paying is an env whose deployment bills to a levyan of its own -- the
// fake, which says what each workspace may do -- and so reads the copy of
// what levyan grants: one whose workspace levyan can make read-only, and
// writable again once it is paid for.
type paying struct {
	*env
	fake  *levyantest.Levyan
	grant []byte
}

func newPaying(t *testing.T) *paying {
	t.Helper()
	fake := levyantest.Start(t)
	return &paying{env: newEnvWith(t, func(c *cmd.Config) { c.App.Billing.Levyan = fake.Config() }), fake: fake}
}

// readOnly has levyan make the workspace read-only (D64), and the copy hear
// it.
func (e *paying) readOnly() {
	e.t.Helper()
	e.grant = e.fake.ReadOnly(e.tenant)
	e.heard()
	e.x.True(e.isReadOnly())
}

// paid has levyan end that, as it does once the invoice is paid.
func (e *paying) paid() {
	e.t.Helper()
	e.fake.End(e.grant)
	e.heard()
	e.x.False(e.isReadOnly())
}

func (e *paying) heard() {
	e.t.Helper()
	_, err := e.s.Follower.Sync(context.Background(), e.tenant)
	e.x.NoError(err)
}

// isReadOnly is what every member's page is told.
func (e *paying) isReadOnly() bool {
	e.t.Helper()
	me, err := e.app().Party().Me(e.owner, &app.PartyMeRequest{})
	e.x.NoError(err)
	return me.GetReadonly()
}

// rpc is `ctx` as a call of `method` over the wire is served: what a
// read-only workspace holds a caller's write to ([domain.ReadOnlyAllows]),
// and which a call made in this process, as these tests make them, says
// nothing of.
func rpc(ctx context.Context, method string) context.Context {
	return grpc.NewContextWithServerTransportStream(ctx, called(method))
}

// called is the stream a call of a method is served on, as far as the
// domain layer asks it: which method.
type called string

func (c called) Method() string             { return string(c) }
func (called) SetHeader(metadata.MD) error  { return nil }
func (called) SendHeader(metadata.MD) error { return nil }
func (called) SetTrailer(metadata.MD) error { return nil }

// queued is how many notices wait to be mailed.
func (e *paying) queued() int {
	e.t.Helper()
	n, err := e.s.Ent.Notification.Query().Where(notification.Mail(true), notification.DateMailedIsNil()).Count(context.Background())
	e.x.NoError(err)
	return n
}

// asks is the kinds of notice that ask somebody to act soon: what a
// read-only workspace is told once it writes again.
func asks() []string {
	out := []string{}
	for k, v := range domain.ReadOnlyTells {
		if v == domain.TellsOnceWritable {
			out = append(out, k)
		}
	}
	return out
}

// TestAReadOnlyWorkspaceIsAskedNothingItCannotDo (D67): while levyan makes a
// workspace read-only, the deployment's sweeps go on -- a loan goes late and
// holds what it lent, a request nobody approved expires, a room nobody came
// to is let go of -- and tell what became of a booking, as ever; and nobody
// is asked, in the app or by mail, to do what it cannot: return a loan late
// or due tomorrow, settle a booking what is out is out over, do the work
// coming due. Each pass asks nothing again. Meanwhile a person marks what
// they were told read, and turns mail off. Once it writes again, the next
// pass tells what is so then, once: the loan late, to who has it and who
// manages it, the booking it is out over, the loan due tomorrow, the work
// coming due -- by mail as well to whoever still takes mail, and not to
// whoever turned it off.
func TestAReadOnlyWorkspaceIsAskedNothingItCannotDo(t *testing.T) {
	e := newPaying(t)
	x := e.x
	box := e.mailing()
	camera := e.item("카메라", "CM-1", nil, day)
	e.bookable(camera, nil)
	phone := e.item("업무폰", "PH-1", nil, day)
	drill := e.item("드릴", "DR-1", nil, day)
	room := e.space("회의실", nil)
	e.bookable(room, nil)
	hall := e.space("강당", nil)
	e.bookable(hall, func(r *app.BookableAddRequest) { r.SetApproval(true) })
	kimCtx, kim := e.person("member")
	leeCtx, lee := e.person("member")
	boss, chief := e.person("manager")

	// What it has before it is read-only: a loan due within the hour and a
	// booking of what it lent after then; a loan due tomorrow; work coming
	// due; a request nobody approves before it begins, and a room nobody
	// comes to.
	loan := e.lend(lee, e.now.Add(time.Hour), camera)
	r, err := e.reserve(kimCtx, camera, e.tomorrow(10, 0), e.tomorrow(11, 0), nil)
	x.NoError(err)
	e.lend(lee, e.now.Add(20*time.Hour), phone)
	_, err = e.app().WorkOrder().Add(e.owner, app.WorkOrderAddRequest_builder{
		Asset: assetRef(drill), Kind: "inspection", Name: "드릴 점검",
		BeginsAt: e.in(5 * time.Hour), EndsAt: e.in(6 * time.Hour),
	}.Build())
	x.NoError(err)
	asked, err := e.reserve(kimCtx, hall, e.now.Add(time.Hour), e.now.Add(90*time.Minute), nil)
	x.NoError(err)
	x.Equal("requested", asked.GetStatus())
	met, err := e.reserve(kimCtx, room, e.now.Add(time.Hour), e.now.Add(3*time.Hour), nil)
	x.NoError(err)
	e.mail()
	mailed := map[string]int{}
	for _, addr := range []string{"owner@acme.example", kim.GetEmail(), lee.GetEmail(), chief.GetEmail()} {
		mailed[addr] = len(box.to(addr))
	}

	e.readOnly()
	e.now = e.now.Add(2 * time.Hour)
	e.sweep()
	e.mail()

	// The clock moved: the loan was found late, once, and holds what it lent
	// until it is back, which the booking after its due says; the request
	// expired, and the room was let go of.
	x.Len(loansOf(e.eventsOf("custody.overdue")), 1, "found late")
	x.Equal(2200, e.hold(loan, camera).EndsAt.Year(), "held until it is back")
	x.Equal([]string{"CM-1 loan late"}, e.out(r))
	x.Equal("expired", e.reservation(asked).GetStatus())
	x.Equal("no_show", e.reservation(met).GetStatus())

	// What became of a booking is told, as ever.
	x.Len(e.notices(kimCtx, "reservation.expired"), 1)
	x.Len(e.notices(kimCtx, "reservation.no_show"), 1)

	// And nothing that asks anybody to act, in the app or by mail.
	nothingAsked := func(why string) {
		t.Helper()
		for _, who := range []context.Context{e.owner, kimCtx, leeCtx, boss} {
			for _, k := range asks() {
				x.Empty(e.notices(who, k), "%s: %s", why, k)
			}
		}
		x.Zero(e.queued(), "%s: queued for mail", why)
		for addr, n := range mailed {
			x.Len(box.to(addr), n, "%s: mailed to %s", why, addr)
		}
	}
	nothingAsked("read-only")
	e.now = e.now.Add(time.Minute)
	e.sweep()
	e.mail()
	nothingAsked("swept again")

	// What a person was told is theirs to quiet, read-only or not: kim marks
	// it read, and the manager turns mail off.
	v, err := e.app().Notification().MarkRead(rpc(kimCtx, app.NotificationService_MarkRead_FullMethodName), &app.NotificationMarkReadRequest{})
	x.NoError(err)
	x.NotZero(v.GetMarked())
	in, err := e.app().Notification().Inbox(kimCtx, &app.NotificationInboxRequest{})
	x.NoError(err)
	x.Zero(in.GetUnread())
	off, err := e.app().Notification().SetMail(rpc(boss, app.NotificationService_SetMail_FullMethodName), app.NotificationSetMailRequest_builder{Off: true}.Build())
	x.NoError(err)
	x.True(off.GetOff())

	// Paid for, it writes again, and the next pass tells what is so then.
	e.paid()
	e.sweep()
	e.mail()
	told := func(why string) {
		t.Helper()
		late := e.notices(leeCtx, "custody.overdue")
		x.Len(late, 1, "%s: the loan late, to who has it", why)
		if len(late) == 1 {
			x.Contains(late[0].GetDesc(), "기다리고 있습니다", "%s: and that a booking waits for it", why)
		}
		x.Len(e.notices(boss, "custody.overdue"), 1, "%s: and to who manages it", why)
		x.Len(e.notices(e.owner, "custody.overdue"), 1, why)
		x.Len(e.notices(kimCtx, "reservation.out"), 1, "%s: the booking what is out is out over, to whose it is", why)
		x.NotNil(about(e.notices(boss, "reservation.out"), camera), "%s: and to who runs the reservations", why)
		due := e.notices(leeCtx, "custody.due")
		x.Len(due, 1, "%s: the loan due tomorrow, due still", why)
		if len(due) == 1 {
			x.True(sameId(due[0].GetSubjectId(), e.loanOf(phone)), "%s: of the phone's loan", why)
		}
		x.Len(e.notices(boss, "work.due"), 1, "%s: the work coming due, scheduled still", why)
		x.Len(e.notices(e.owner, "work.due"), 1, why)

		// By mail as well, as these go by mail: but the reminder, which is
		// told in the app only.
		x.Len(box.to(lee.GetEmail()), mailed[lee.GetEmail()]+1, "%s: late, to who has it", why)
		x.Len(box.to(kim.GetEmail()), mailed[kim.GetEmail()]+1, "%s: out over the booking, to whose it is", why)
		x.Len(box.to("owner@acme.example"), mailed["owner@acme.example"]+3, "%s: late, out, the work", why)
		x.Len(box.to(chief.GetEmail()), mailed[chief.GetEmail()], "%s: not to whoever turned mail off", why)
	}
	told("written again")

	// And once.
	e.now = e.now.Add(time.Minute)
	e.sweep()
	e.mail()
	told("swept again")
	x.Len(loansOf(e.eventsOf("custody.overdue")), 1, "and found late once")
}

// loanOf is the open hand-over `a` is out with.
func (e *env) loanOf(a *app.Asset) []byte {
	e.t.Helper()
	vs, err := e.app().CustodyLine().List(e.owner, app.CustodyLineListRequest_builder{
		Filters: []*app.CustodyLineFilter{app.CustodyLineFilter_builder{Asset: assetRef(a)}.Build()},
	}.Build())
	e.x.NoError(err)
	e.x.NotEmpty(vs.GetItems())
	return vs.GetItems()[0].GetCustody().GetId()
}

// TestADueMissedWhileReadOnlyIsToldLateOnly (D67): a reminder of a loan due
// tomorrow is not told while the workspace is read-only, and asked again
// every pass; a due it misses meanwhile is not told as one still to come
// once it writes again -- the loan is told late, once, as what it is then.
func TestADueMissedWhileReadOnlyIsToldLateOnly(t *testing.T) {
	e := newPaying(t)
	x := e.x
	phone := e.item("업무폰", "PH-1", nil, day)
	leeCtx, lee := e.person("member")
	e.lend(lee, e.now.Add(20*time.Hour), phone)

	e.readOnly()
	e.sweep()
	x.Empty(e.notices(leeCtx, "custody.due"), "due tomorrow, and read-only")

	e.now = e.now.Add(21 * time.Hour)
	e.sweep()
	x.Len(loansOf(e.eventsOf("custody.overdue")), 1, "found late, read-only or not")
	x.Empty(e.notices(leeCtx, "custody.overdue"))

	e.paid()
	e.sweep()
	x.Empty(e.notices(leeCtx, "custody.due"), "a due missed is not to come")
	x.Len(e.notices(leeCtx, "custody.overdue"), 1, "it is late")
	e.sweep()
	x.Len(e.notices(leeCtx, "custody.overdue"), 1, "once")
}

// TestALateLoanGivenBackWhileReadOnlyIsToldNothing (D67): a loan found late
// while the workspace was read-only, and given back by the time it writes
// again -- by an operator's hand, the deployment's own, which a read-only
// workspace does not refuse -- is late no more, and nobody is told it was.
// And one given a later due meanwhile is told only once it misses that one.
func TestALateLoanGivenBackWhileReadOnlyIsToldNothing(t *testing.T) {
	e := newPaying(t)
	x := e.x
	camera := e.item("카메라", "CM-1", nil, day)
	phone := e.item("업무폰", "PH-1", nil, day)
	leeCtx, lee := e.person("member")
	back := e.lend(lee, e.now.Add(time.Hour), camera)
	later := e.lend(lee, e.now.Add(time.Hour), phone)

	e.readOnly()
	e.now = e.now.Add(2 * time.Hour)
	e.sweep()
	x.Len(loansOf(e.eventsOf("custody.overdue")), 2)

	_, err := e.s.Ungated.Custody().Return(e.operator(), app.CustodyReturnRequest_builder{Ref: ref[app.CustodyRef](back.GetId())}.Build())
	x.NoError(err)
	due := e.now.Add(3 * time.Hour).Truncate(time.Minute)
	_, err = e.s.Ungated.Custody().Extend(e.operator(), app.CustodyExtendRequest_builder{Ref: ref[app.CustodyRef](later.GetId()), DueAt: timestamppb.New(due)}.Build())
	x.NoError(err)

	e.paid()
	e.sweep()
	x.Empty(e.notices(leeCtx, "custody.overdue"), "back, and due later")

	e.now = due.Add(time.Hour)
	e.sweep()
	late := e.notices(leeCtx, "custody.overdue")
	x.Len(late, 1, "the due it missed since")
	if len(late) == 1 {
		x.True(sameId(late[0].GetSubjectId(), later.GetId()))
	}
}

// TestALoanLateAgainWhileReadOnlyIsToldOfItsSecondDue (D67): a loan told it
// was late, and given a later due, that misses that one too while the
// workspace is read-only, is told nothing then -- what told it of the first
// says nothing of this one -- and told of it once the workspace writes again.
func TestALoanLateAgainWhileReadOnlyIsToldOfItsSecondDue(t *testing.T) {
	e := newPaying(t)
	x := e.x
	camera := e.item("카메라", "CM-1", nil, day)
	leeCtx, lee := e.person("member")
	loan := e.lend(lee, e.now.Add(time.Hour), camera)

	e.now = e.now.Add(2 * time.Hour)
	e.sweep()
	x.Len(e.notices(leeCtx, "custody.overdue"), 1, "late, and written")
	due := e.now.Add(time.Hour).Truncate(time.Minute)
	_, err := e.app().Custody().Extend(e.owner, app.CustodyExtendRequest_builder{Ref: ref[app.CustodyRef](loan.GetId()), DueAt: timestamppb.New(due)}.Build())
	x.NoError(err)

	e.readOnly()
	e.now = due.Add(time.Hour)
	e.sweep()
	x.Len(loansOf(e.eventsOf("custody.overdue")), 2, "found late of each due")
	x.Len(e.notices(leeCtx, "custody.overdue"), 1, "and told of the first")

	e.paid()
	e.sweep()
	late := e.notices(leeCtx, "custody.overdue")
	x.Len(late, 2, "and of the second, once it writes")
	if len(late) == 2 {
		x.Contains(late[0].GetDesc(), due.In(domain.DefaultZone).Format("1월 2일 15:04"), "newest first: the due it missed")
	}
	e.sweep()
	x.Len(e.notices(leeCtx, "custody.overdue"), 2, "once")
}

// operator is the domain layer as an operator's command reaches it: framed
// as the deployment's own work in the workspace, asked over no wire.
func (e *env) operator() context.Context {
	return frame.Into(context.Background(), frame.New(pdid.Nil, e.tenant, frame.Whole()).WithScope(frame.Only(e.tenant)))
}

// TestABookingALateLoanMadeWaitIsToldOnceItWritesAgain (D67): a pool's
// booking waits once a unit lent before it goes late while the workspace is
// read-only, and nobody is told -- not whose booking it is, nor who runs the
// reservations; once it writes again, both are told, once. Not whoever's
// booking kept its time, nor whoever's waited before and was told then.
func TestABookingALateLoanMadeWaitIsToldOnceItWritesAgain(t *testing.T) {
	e := newPaying(t)
	x := e.x
	camera := e.item("카메라 1", "CM-1", nil, day)
	lost := e.item("카메라 3", "CM-3", nil, day)
	cameras := e.pool("카메라", camera, e.item("카메라 2", "CM-2", nil, day), lost)
	kim, _ := e.person("member")
	lee, _ := e.person("member")
	choi, _ := e.person("member")
	parkCtx, park := e.person("member")
	boss, _ := e.person("manager")
	first, err := e.take(kim, cameras, 1, e.tomorrow(9, 0), e.tomorrow(12, 0))
	x.NoError(err)
	second, err := e.take(lee, cameras, 1, e.tomorrow(10, 0), e.tomorrow(11, 0))
	x.NoError(err)
	third, err := e.take(choi, cameras, 1, e.tomorrow(10, 0), e.tomorrow(11, 0))
	x.NoError(err)

	// One lost before, while the workspace writes: the one made last waits,
	// and is told so.
	e.status(lost, "lost")
	x.False(e.holds(third, cameras))
	x.Len(e.notices(choi, "reservation.conflict"), 1)
	x.Len(e.notices(boss, "reservation.conflict"), 1)
	// And one lent, due back before any of them begins.
	e.lend(park, e.now.Add(time.Hour), camera)
	x.True(e.holds(second, cameras))

	e.readOnly()
	e.now = e.now.Add(2 * time.Hour)
	e.sweep()
	x.True(e.holds(first, cameras), "made first, kept")
	x.False(e.holds(second, cameras), "made next, waits now")
	x.NotEmpty(e.eventsOf("reservation.wait"), "under the loan's being found late")
	for _, who := range []context.Context{kim, lee, parkCtx, e.owner} {
		x.NotContains(e.told(who), "reservation.conflict")
		x.NotContains(e.told(who), "custody.overdue")
	}
	x.Len(e.notices(boss, "reservation.conflict"), 1, "nothing more")
	x.Len(e.notices(choi, "reservation.conflict"), 1)

	e.paid()
	told := func(why string) {
		t.Helper()
		x.Len(e.notices(lee, "reservation.conflict"), 1, "%s: the booking that waits, to whose it is", why)
		x.Len(e.notices(boss, "reservation.conflict"), 2, "%s: and to who runs the reservations, of the pool", why)
		x.NotNil(about(e.notices(boss, "reservation.conflict"), cameras), why)
		x.Empty(e.notices(kim, "reservation.conflict"), "%s: not whoever's booking kept its time", why)
		x.Len(e.notices(choi, "reservation.conflict"), 1, "%s: nor whoever's waited before, and was told then", why)
		x.Len(e.notices(parkCtx, "custody.overdue"), 1, "%s: and the loan late, to who has it", why)
	}
	e.sweep()
	told("written again")
	e.sweep()
	told("swept again")
	x.False(e.holds(second, cameras), "and it waits still")
	x.False(e.holds(third, cameras))
}

// TestAPersonsNoticesAreTheirOwnInAReadOnlyWorkspace (D67): a read-only
// workspace refuses a write of its own, and still marks a person's notices
// read and takes their word on mail -- which is theirs, a setting of their
// membership, and what they quiet what it still tells them by -- and none
// of anybody else's.
func TestAPersonsNoticesAreTheirOwnInAReadOnlyWorkspace(t *testing.T) {
	e := newPaying(t)
	x := e.x
	hall := e.space("강당", nil)
	e.bookable(hall, func(r *app.BookableAddRequest) { r.SetApproval(true) })
	kim, _ := e.person("member")
	boss, _ := e.person("manager")
	_, err := e.reserve(kim, hall, e.tomorrow(9, 0), e.tomorrow(10, 0), nil)
	x.NoError(err)
	unread := func(ctx context.Context) uint32 {
		in, err := e.app().Notification().Inbox(ctx, &app.NotificationInboxRequest{})
		x.NoError(err)
		return in.GetUnread()
	}
	x.NotZero(unread(e.owner))
	x.NotZero(unread(boss))

	e.readOnly()
	_, err = e.app().Asset().Add(rpc(e.owner, app.AssetService_Add_FullMethodName), app.AssetAddRequest_builder{Name: "프로젝터"}.Build())
	x.Equal(codes.FailedPrecondition, status.Code(err), "a write of the workspace's: %v", err)
	x.Equal(domain.ErrorReadOnly, reasonOf(err))

	v, err := e.app().Notification().MarkRead(rpc(e.owner, app.NotificationService_MarkRead_FullMethodName), &app.NotificationMarkReadRequest{})
	x.NoError(err, "marked read")
	x.EqualValues(1, v.GetMarked())
	x.Zero(unread(e.owner))
	x.NotZero(unread(boss), "and nobody else's")

	m, err := e.app().Notification().SetMail(rpc(boss, app.NotificationService_SetMail_FullMethodName), app.NotificationSetMailRequest_builder{Off: true}.Build())
	x.NoError(err, "mail turned off")
	x.True(m.GetOff())
	me, err := e.app().Party().Me(boss, &app.PartyMeRequest{})
	x.NoError(err)
	x.True(me.GetHolder().GetMailOff(), "what the person's page reads")
	me, err = e.app().Party().Me(e.owner, &app.PartyMeRequest{})
	x.NoError(err)
	x.False(me.GetHolder().GetMailOff(), "and nobody else's")
}

// reasonOf is the reason a refusal carries for a page to tell it by.
func reasonOf(err error) string {
	for _, d := range status.Convert(err).Details() {
		if v, ok := d.(*errdetails.ErrorInfo); ok {
			return v.GetReason()
		}
	}
	return ""
}
