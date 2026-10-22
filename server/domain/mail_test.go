package domain_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/lesomnus/payday/pdid"
	"google.golang.org/protobuf/types/known/timestamppb"

	app "github.com/lesomnus/rove"
	"github.com/lesomnus/rove/internal/ent/holder"
	"github.com/lesomnus/rove/internal/ent/notification"
	"github.com/lesomnus/rove/server/domain"
	"github.com/lesomnus/rove/server/mail"
)

// mailbox is a server that keeps what it is sent, and refuses the first
// `fail` of them.
type mailbox struct {
	mu   sync.Mutex
	sent []mail.Message
	fail int
}

func (m *mailbox) Send(_ context.Context, v mail.Message) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.fail > 0 {
		m.fail--
		return errors.New("the server said no")
	}
	m.sent = append(m.sent, v)
	return nil
}

// to is what went to one address.
func (m *mailbox) to(addr string) []mail.Message {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := []mail.Message{}
	for _, v := range m.sent {
		if v.To == addr {
			out = append(out, v)
		}
	}
	return out
}

func (m *mailbox) all() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.sent)
}

// mailing is this env sending mail, and the owner with an address roster
// checked for them.
func (e *env) mailing() *mailbox {
	e.t.Helper()
	box := &mailbox{}
	e.s.Deps.Mail = box
	e.s.Deps.Links.App = "https://rove.example"
	h, err := e.s.Ent.Holder.Query().Where(holder.Alias("owner"), holder.TenantId(e.tenant.Uuid())).Only(context.Background())
	e.x.NoError(err)
	e.x.NoError(e.s.Identity.AddAddress(context.Background(), pdid.Id(*h.Account), "owner@acme.example", true))
	return box
}

// mail is one pass of the mailer, at the env's clock.
func (e *env) mail() {
	e.t.Helper()
	e.x.NoError(domain.Mailer{Drv: e.s.Drv, Deps: e.s.Deps}.Pass(context.Background()))
}

// TestAnApprovalIsAskedAndAnsweredByMail.
//
// A reservation that waits for approval mails whoever approves; the answer
// mails whoever asked -- once each, after the operation and not inside it,
// with a link into the workspace.
func TestAnApprovalIsAskedAndAnsweredByMail(t *testing.T) {
	e := newEnv(t)
	x := e.x
	box := e.mailing()
	hall := e.space("강당", nil)
	e.bookable(hall, func(r *app.BookableAddRequest) { r.SetApproval(true) })
	kim, kimP := e.person("member")
	_, lee := e.person("manager")

	r, err := e.reserve(kim, hall, e.tomorrow(16, 0), e.tomorrow(17, 0), nil)
	x.NoError(err)
	x.Equal("requested", r.GetStatus())
	x.Zero(box.all(), "mailed from inside the operation")

	e.mail()
	for _, addr := range []string{"owner@acme.example", lee.GetEmail()} {
		got := box.to(addr)
		x.Len(got, 1, "%s: once", addr)
		x.Contains(got[0].Subject, "승인 요청")
		x.Contains(got[0].Body, "https://rove.example/@acme/reservations")
	}
	x.Empty(box.to(kimP.GetEmail()), "not to whoever asked")

	e.mail()
	x.Len(box.to("owner@acme.example"), 1, "and not twice")

	_, err = e.app().Reservation().Approve(e.owner, app.ReservationDecideRequest_builder{Ref: ref[app.ReservationRef](r.GetId())}.Build())
	x.NoError(err)
	e.mail()
	got := box.to(kimP.GetEmail())
	x.Len(got, 1, "the answer, to whoever asked")
}

// TestLateCustodyAndDueWorkGoByMail.
func TestLateCustodyAndDueWorkGoByMail(t *testing.T) {
	e := newEnv(t)
	x := e.x
	box := e.mailing()
	store := e.space("창고", nil)
	phone := e.item("업무폰", "PH-1", store, day)
	_, kim := e.person("member")

	_, err := e.app().Custody().Add(e.owner, app.CustodyAddRequest_builder{
		Party: ref[app.PartyRef](kim.GetId()),
		DueAt: e.in(2 * day),
		Lines: []*app.CustodyLineSpec{app.CustodyLineSpec_builder{Asset: assetRef(phone)}.Build()},
	}.Build())
	x.NoError(err)
	e.mail()
	x.Empty(box.to(kim.GetEmail()), "an issue is told in the app only")

	e.now = e.now.Add(3 * day)
	e.sweep()
	e.mail()
	x.Len(box.to(kim.GetEmail()), 1, "late, to who has it")
	x.Len(box.to("owner@acme.example"), 1, "and to who manages it")

	_, err = e.app().WorkOrder().Add(e.owner, app.WorkOrderAddRequest_builder{
		Asset:    assetRef(phone),
		Kind:     "inspection",
		Name:     "배터리 점검",
		BeginsAt: timestamppb.New(e.now.Add(2 * time.Hour)),
		EndsAt:   timestamppb.New(e.now.Add(3 * time.Hour)),
	}.Build())
	x.NoError(err)
	e.sweep()
	e.mail()
	owner := box.to("owner@acme.example")
	x.Len(owner, 2)
	x.Contains(owner[1].Subject, "배터리 점검", "maintenance coming due")
}

// TestMailIsThePersonsOwnToTurnOff: off is told in the app only; on again is
// mailed again.
func TestMailIsThePersonsOwnToTurnOff(t *testing.T) {
	e := newEnv(t)
	x := e.x
	box := e.mailing()
	hall := e.space("강당", nil)
	e.bookable(hall, func(r *app.BookableAddRequest) { r.SetApproval(true) })
	kim, _ := e.person("member")
	leeCtx, lee := e.person("manager")

	v, err := e.app().Notification().SetMail(leeCtx, app.NotificationSetMailRequest_builder{Off: true}.Build())
	x.NoError(err)
	x.True(v.GetOff())
	me, err := e.app().Party().Me(leeCtx, &app.PartyMeRequest{})
	x.NoError(err)
	x.True(me.GetHolder().GetMailOff(), "what the person's page reads")

	_, err = e.reserve(kim, hall, e.tomorrow(9, 0), e.tomorrow(10, 0), nil)
	x.NoError(err)
	e.mail()
	x.Empty(box.to(lee.GetEmail()), "turned off")
	x.Len(box.to("owner@acme.example"), 1, "and nobody else's")
	in, err := e.app().Notification().Inbox(leeCtx, app.NotificationInboxRequest_builder{}.Build())
	x.NoError(err)
	x.NotZero(in.GetUnread(), "still told in the app")

	_, err = e.app().Notification().SetMail(leeCtx, app.NotificationSetMailRequest_builder{Off: false}.Build())
	x.NoError(err)
	_, err = e.reserve(kim, hall, e.tomorrow(11, 0), e.tomorrow(12, 0), nil)
	x.NoError(err)
	e.mail()
	x.Len(box.to(lee.GetEmail()), 1, "on again")
}

// TestNobodyToMailIsNoMail: no address, no mail -- and off the queue rather
// than asked about forever.
func TestNobodyToMailIsNoMail(t *testing.T) {
	e := newEnv(t)
	x := e.x
	box := e.mailing()
	hall := e.space("강당", nil)
	e.bookable(hall, func(r *app.BookableAddRequest) { r.SetApproval(true) })
	kim, _ := e.person("member")
	// A manager whose person record has no address, on a roster that has
	// none checked for them either.
	p, err := e.app().Party().Add(e.owner, app.PartyAddRequest_builder{Name: "주소 없는 매니저", Kind: "person"}.Build())
	x.NoError(err)
	_, err = e.app().Party().Invite(e.owner, app.PartyInviteRequest_builder{
		Ref: ref[app.PartyRef](p.GetId()), Alias: "noaddr", Role: "manager", Password: "password1234",
	}.Build())
	x.NoError(err)

	_, err = e.reserve(kim, hall, e.tomorrow(9, 0), e.tomorrow(10, 0), nil)
	x.NoError(err)
	e.mail()
	x.Len(box.to("owner@acme.example"), 1)
	x.Equal(1, box.all(), "nothing to somebody with no address")

	queued, err := e.s.Ent.Notification.Query().Where(notification.Mail(true), notification.DateMailedIsNil()).Count(context.Background())
	x.NoError(err)
	x.Zero(queued, "off the queue")
}

// TestAMailThatFailsIsTriedAgain, and given up on after every try.
func TestAMailThatFailsIsTriedAgain(t *testing.T) {
	e := newEnv(t)
	x := e.x
	box := e.mailing()
	hall := e.space("강당", nil)
	e.bookable(hall, func(r *app.BookableAddRequest) { r.SetApproval(true) })
	kim, _ := e.person("member")

	box.fail = 1
	_, err := e.reserve(kim, hall, e.tomorrow(9, 0), e.tomorrow(10, 0), nil)
	x.NoError(err)
	e.mail()
	x.Empty(box.to("owner@acme.example"), "the server said no")
	e.mail()
	x.Len(box.to("owner@acme.example"), 1, "and yes the next time")

	box.fail = 100
	_, err = e.reserve(kim, hall, e.tomorrow(11, 0), e.tomorrow(12, 0), nil)
	x.NoError(err)
	for range 12 {
		e.mail()
	}
	x.Len(box.to("owner@acme.example"), 1)
	queued, err := e.s.Ent.Notification.Query().Where(notification.Mail(true), notification.DateMailedIsNil()).Count(context.Background())
	x.NoError(err)
	x.Zero(queued, "given up on, rather than tried forever")
}

// TestWhatIsToldInTheAppOnlyIsNotQueued, and nothing is queued where this
// deployment sends no mail at all.
func TestWhatIsToldInTheAppOnlyIsNotQueued(t *testing.T) {
	e := newEnv(t)
	x := e.x
	ctx := context.Background()

	// No server: told in the app, queued for nothing -- not even the log.
	e.s.Deps.Mail = mail.Log{}
	hall := e.space("강당", nil)
	e.bookable(hall, func(r *app.BookableAddRequest) { r.SetApproval(true) })
	kim, _ := e.person("member")
	_, err := e.reserve(kim, hall, e.tomorrow(9, 0), e.tomorrow(10, 0), nil)
	x.NoError(err)
	n, err := e.s.Ent.Notification.Query().Where(notification.Kind("reservation.requested")).Count(ctx)
	x.NoError(err)
	x.Positive(n)
	queued, err := e.s.Ent.Notification.Query().Where(notification.Mail(true)).Count(ctx)
	x.NoError(err)
	x.Zero(queued)

	// A server, and a kind that is not mailed.
	box := e.mailing()
	store := e.space("창고", nil)
	phone := e.item("업무폰", "PH-1", store, day)
	_, err = e.app().Custody().Add(e.owner, app.CustodyAddRequest_builder{
		Party: ref[app.PartyRef](kimParty(e, kim)),
		DueAt: e.in(2 * day),
		Lines: []*app.CustodyLineSpec{app.CustodyLineSpec_builder{Asset: assetRef(phone)}.Build()},
	}.Build())
	x.NoError(err)
	issued, err := e.s.Ent.Notification.Query().Where(notification.Kind("custody.issued")).Only(ctx)
	x.NoError(err)
	x.False(issued.Mail)
	e.mail()
	x.Zero(box.all())
}

// kimParty is the person record a signed-in context is.
func kimParty(e *env, ctx context.Context) []byte {
	me, err := e.app().Party().Me(ctx, &app.PartyMeRequest{})
	e.x.NoError(err)
	return me.GetParty().GetId()
}
