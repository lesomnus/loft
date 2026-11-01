package domain_test

import (
	"context"
	"testing"
	"time"

	"google.golang.org/protobuf/types/known/timestamppb"

	app "github.com/lesomnus/loft"
	"github.com/lesomnus/loft/internal/ent/custody"
	"github.com/lesomnus/loft/internal/ent/event"
	"github.com/lesomnus/loft/internal/ent/notification"
)

// What the deployment's own work does to a closed workspace (D68, design
// 7.14): nothing. Nobody there may read what a sweep would tell them, nor act
// on it, so nothing of it moves while it is closed and nobody is told or
// mailed anything; once it is restored, what passed its time meanwhile the
// restore lets go of (D70, design 7.15), and the next pass does the rest of
// what the time passed meanwhile did, of what is so then, and tells it once.

// closeIt is the workspace closed by its owner, typing its name.
func (e *env) closeIt() {
	e.t.Helper()
	ws := app.TenantRef_builder{Id: e.tenant.Bytes()}.Build()
	cur, err := e.app().Tenant().Get(e.owner, app.TenantGetRequest_builder{Ref: ws}.Build())
	e.x.NoError(err)
	_, err = e.app().Tenant().Close(e.owner, app.TenantCloseRequest_builder{Ref: ws, Confirm: cur.GetName()}.Build())
	e.x.NoError(err)
}

// restoreIt is the closed workspace restored by its owner.
func (e *env) restoreIt() {
	e.t.Helper()
	ws := app.TenantRef_builder{Id: e.tenant.Bytes()}.Build()
	_, err := e.app().Tenant().Restore(e.owner, app.TenantRestoreRequest_builder{Ref: ws}.Build())
	e.x.NoError(err)
}

// toldAndDone is how many notices, and how many events, the workspace has.
func (e *env) toldAndDone() (notices, events int) {
	e.t.Helper()
	ctx := context.Background()
	n, err := e.s.Ent.Notification.Query().Where(notification.TenantId(e.tenant.Uuid())).Count(ctx)
	e.x.NoError(err)
	v, err := e.s.Ent.Event.Query().Where(event.TenantId(e.tenant.Uuid())).Count(ctx)
	e.x.NoError(err)
	return n, v
}

// TestAClosedWorkspaceIsSweptOnceItIsRestored (D68): while a workspace is
// closed for days, the sweeps leave it as it was -- a hold past its time,
// a request whose time began, a room nobody came to, a loan past its due,
// work whose time came -- writing no event, no notice and no mail. Once its
// owner restores it, the restore lets go of the hold, the request and the
// room, which nobody could take up, as expiries it tells nobody of (D70);
// and the next pass does the rest of what the time did, of what is so then:
// the loan is found late, once, and holds what it lent until it is back,
// which the booking after it says; and it tells, once and by mail where it
// goes by mail, the loan late to who has it and who manages it, the booking
// it is out over, a loan due within the day, and the work -- whose time is
// past, and which says so.
func TestAClosedWorkspaceIsSweptOnceItIsRestored(t *testing.T) {
	e := newEnv(t)
	x := e.x
	ctx := context.Background()
	box := e.mailing()
	camera := e.item("카메라", "CM-1", nil, day)
	e.bookable(camera, nil)
	phone := e.item("업무폰", "PH-1", nil, day)
	drill := e.item("드릴", "DR-1", nil, day)
	room := e.space("회의실", nil)
	e.bookable(room, nil)
	hall := e.space("강당", nil)
	e.bookable(hall, func(r *app.BookableAddRequest) { r.SetApproval(true) })
	small := e.space("소회의실", nil)
	e.bookable(small, nil)
	kimCtx, kim := e.person("member")
	leeCtx, lee := e.person("member")
	boss, chief := e.person("manager")

	// What it has as it closes: a loan due within the hour, and a booking
	// of what it lent for after the closure; a loan due the day it is
	// restored; work coming due; a hold, a request nobody approves before it
	// begins, and a room nobody comes to.
	const closure = 3 * 24 * time.Hour
	loan := e.lend(lee, e.now.Add(time.Hour), camera)
	later := e.tomorrow(10, 0).AddDate(0, 0, 3)
	booked, err := e.reserve(kimCtx, camera, later, later.Add(time.Hour), nil)
	x.NoError(err)
	e.lend(lee, e.now.Add(closure+20*time.Hour), phone)
	_, err = e.app().WorkOrder().Add(e.owner, app.WorkOrderAddRequest_builder{
		Asset: assetRef(drill), Kind: "inspection", Name: "드릴 점검",
		BeginsAt: e.in(5 * time.Hour), EndsAt: e.in(6 * time.Hour),
	}.Build())
	x.NoError(err)
	held, err := e.reserve(kimCtx, small, e.now.Add(time.Hour), e.now.Add(2*time.Hour), func(r *app.ReservationAddRequest) { r.SetHold(true) })
	x.NoError(err)
	x.Equal("held", held.GetStatus())
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
	dueAt := e.hold(loan, camera).EndsAt

	e.closeIt()
	notices, events := e.toldAndDone()
	e.now = e.now.Add(closure)
	e.sweep()
	e.mail()

	// Nothing of it moved, and nobody was told anything.
	still := func(why string) {
		t.Helper()
		n, v := e.toldAndDone()
		x.Equal(notices, n, "%s: notices written", why)
		x.Equal(events, v, "%s: events written", why)
		for addr, n := range mailed {
			x.Len(box.to(addr), n, "%s: mailed to %s", why, addr)
		}
		x.Equal("held", e.reservation(held).GetStatus(), why)
		x.Equal("requested", e.reservation(asked).GetStatus(), why)
		x.Equal("confirmed", e.reservation(met).GetStatus(), why)
		late, err := e.s.Ent.Custody.Query().Where(custody.TenantId(e.tenant.Uuid()), custody.OverdueNoticed(true)).Count(ctx)
		x.NoError(err)
		x.Zero(late, "%s: a loan found late", why)
		x.WithinDuration(dueAt, e.hold(loan, camera).EndsAt, 0, "%s: what the loan holds", why)
	}
	still("closed")
	e.now = e.now.Add(time.Minute)
	e.sweep()
	e.mail()
	still("swept again")

	// Restored, what nobody could take up is let go of, and the next pass
	// does the rest of what the time did.
	e.restoreIt()
	x.Equal("expired", e.reservation(held).GetStatus(), "the hold ran out")
	x.Equal("expired", e.reservation(asked).GetStatus(), "the request began")
	x.Equal("expired", e.reservation(met).GetStatus(), "the room nobody could come to")
	e.sweep()
	e.mail()
	x.Len(loansOf(e.eventsOf("custody.overdue")), 1, "the loan found late")
	x.Equal(2200, e.hold(loan, camera).EndsAt.Year(), "and held until it is back")
	x.Equal([]string{"CM-1 loan late"}, e.out(booked))

	told := func(why string) {
		t.Helper()
		x.Empty(e.notices(kimCtx, "reservation.expired"), "%s: what passed while it was closed, to nobody", why)
		x.Empty(e.notices(kimCtx, "reservation.no_show"), "%s: nor as a no-show", why)
		late := e.notices(leeCtx, "custody.overdue")
		x.Len(late, 1, "%s: the loan late, to who has it", why)
		if len(late) == 1 {
			x.Contains(late[0].GetDesc(), "기다리고 있습니다", "%s: and that a booking waits for it", why)
		}
		x.Len(e.notices(boss, "custody.overdue"), 1, "%s: and to who manages it", why)
		x.Len(e.notices(kimCtx, "reservation.out"), 1, "%s: the booking what is out is out over, to whose it is", why)
		due := e.notices(leeCtx, "custody.due")
		x.Len(due, 1, "%s: the loan due within the day", why)
		if len(due) == 1 {
			x.True(sameId(due[0].GetSubjectId(), e.loanOf(phone)), "%s: of the phone's loan", why)
		}
		work := e.notices(boss, "work.due")
		x.Len(work, 1, "%s: the work, scheduled still", why)
		if len(work) == 1 {
			x.Contains(work[0].GetDesc(), "예정이었고 아직 시작하지 않았습니다", "%s: whose time is past", why)
		}

		// By mail as well, what goes by mail.
		x.Len(box.to(lee.GetEmail()), mailed[lee.GetEmail()]+1, "%s: late, to who has it", why)
		x.Len(box.to(kim.GetEmail()), mailed[kim.GetEmail()]+1, "%s: out over the booking, to whose it is", why)
		x.Len(box.to(chief.GetEmail()), mailed[chief.GetEmail()]+3, "%s: late, out, the work", why)
		x.Len(box.to("owner@acme.example"), mailed["owner@acme.example"]+3, why)
	}
	told("restored")

	// And once.
	e.now = e.now.Add(time.Minute)
	e.sweep()
	e.mail()
	told("swept again")
	x.Len(loansOf(e.eventsOf("custody.overdue")), 1, "and found late once")
}

// TestAClosedWorkspaceIsToldNothingByAnOperator (D68): an operator's command
// that changes a closed workspace -- the deployment's own work, which no
// closing refuses -- writes no notice there, and so mails nothing: nobody
// there may read it. What it changed is as it was changed.
func TestAClosedWorkspaceIsToldNothingByAnOperator(t *testing.T) {
	e := newEnv(t)
	x := e.x
	box := e.mailing()
	camera := e.item("카메라", "CM-1", nil, day)
	e.bookable(camera, nil)
	kimCtx, kim := e.person("member")
	_, lee := e.person("member")
	loan := e.lend(lee, e.now.Add(time.Hour), camera)
	booked, err := e.reserve(kimCtx, camera, e.tomorrow(10, 0), e.tomorrow(11, 0), nil)
	x.NoError(err)
	e.mail()
	before := len(box.to(kim.GetEmail()))

	e.closeIt()
	notices, _ := e.toldAndDone()
	due := e.tomorrow(12, 0)
	_, err = e.s.Ungated.Custody().Extend(e.operator(), app.CustodyExtendRequest_builder{Ref: ref[app.CustodyRef](loan.GetId()), DueAt: timestamppb.New(due)}.Build())
	x.NoError(err)
	e.mail()
	x.False(e.hold(loan, camera).EndsAt.Before(due), "the loan holds the time it is due")
	x.Equal([]string{"CM-1 loan"}, e.out(booked), "and the booking says so")
	n, _ := e.toldAndDone()
	x.Equal(notices, n, "and nobody is told")
	x.Len(box.to(kim.GetEmail()), before, "nor mailed")
}
