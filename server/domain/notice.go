package domain

import (
	"context"
	"uuid"

	"github.com/lesomnus/otx/log"
	"github.com/lesomnus/payday/pdid"
	"github.com/lesomnus/z"
	"github.com/protobuf-orm/ent/dialect/sql"

	app "github.com/lesomnus/loft"
	"github.com/lesomnus/loft/internal/ent/holder"
	"github.com/lesomnus/loft/internal/ent/notification"
	"github.com/lesomnus/loft/internal/ent/party"
)

// Mailed is the kinds of notification that go by mail as well as in the app:
// the ones somebody has to act on soon, or would want to hear about away from
// the screen (2026-10-12). Everything else is told in the app only.
var Mailed = map[string]bool{
	"reservation.requested":  true, // to whoever approves
	"reservation.decided":    true, // to whoever asked
	"custody.overdue":        true, // to who has it, and who manages it
	"work.due":               true, // maintenance coming due, to who manages it
	"domain.lapsed":          true, // a workspace's domain taken out of use, to who runs it
	"reservation.conflict":   true, // a kit's member, or a pool's units, its reservation waits for, to who runs it and whose it is
	"reservation.out_of_use": true, // an item a reservation takes is out of use, to who runs it and whose it is
	"reservation.out":        true, // a thing a reservation takes is out with somebody over its time, to who runs it and whose it is
}

// Telling is when a workspace levyan makes read-only ([ReadOnly], D64) is
// told a kind of notification the deployment's own work sends it (D67).
type Telling int

const (
	// TellsStill is told while it is read-only, as at any other time: what
	// the deployment did or found by itself, which asks nobody to do anything
	// by any time.
	TellsStill Telling = iota + 1

	// TellsOnceWritable is held back while it is read-only, in the app and
	// by mail: it asks somebody to do, by a time, what a read-only workspace
	// does not -- return a loan, settle a booking, do the work. Nothing of it
	// is written, and the sweep that sends it tells it once the workspace
	// writes again, of what is so then: what it is not told while it cannot
	// do it, it is told once it can, and once.
	TellsOnceWritable
)

// ReadOnlyTells is when a read-only workspace is told each kind of
// notification a sweep sends (D67): the deployment's own work in it, which a
// read-only workspace does not stop -- its clock moves, a hold runs out, a
// loan goes late -- and which says so as ever, and asks nothing of it. A
// caller's write that would tell anybody anything is refused before it does
// (D64); what a read-only workspace still does tells nobody.
//
// Every kind a sweep sends is here, and the domain's tests hold them to it:
// a kind is decided as it is added. A kind no sweep sends is not: an
// operation's, which a read-only workspace refuses before it tells anything.
// The table is the sweeps' alone (D68): what an operator's hand writes there
// -- the deployment's own, which is not refused -- tells what the operator
// did, and is told as it is done, whatever its kind, since nothing would
// tell it later.
var ReadOnlyTells = map[string]Telling{
	// What became of a booking by itself: let go of, as nobody approved it
	// in time, or as nobody came; or given what it waited for. It asks for
	// nothing -- a booking made again is a write, refused -- and is so still
	// once the workspace writes again, when it would be told late.
	"reservation.expired":  TellsStill, // its time began before anybody approved it (sweepRequests), or nobody could check in or pick up while it was read-only (sweepNoShows, D70)
	"reservation.no_show":  TellsStill, // nobody checked in, or picked up, in time (sweepNoShows)
	"reservation.resolved": TellsStill, // what it waited for is its now (sweepKits, and what a booking let go of)

	// What the deployment found of the workspace's own domains: how people
	// reach it, which is how they read it, take its data out and pay for it.
	// What a lapsed one asks is done at its DNS, not here, and the sweep
	// asks again while the workspace is read-only; switching to a ready one
	// is a choice with no time to it, there to be made once it writes.
	"domain.lapsed": TellsStill, // links and sign-in went back to the service's address (sweepDomains)
	"domain.ready":  TellsStill, // one verified, beside the one in use (sweepDomains)

	// What asks somebody to act on it soon.
	"custody.due":          TellsOnceWritable, // return it by its due, within a day: asked again every pass while it is so, and told once (sweepLoans)
	"custody.overdue":      TellsOnceWritable, // return it, late: told once of each due, found late or not ([Tx.lateUntold])
	"reservation.out":      TellsOnceWritable, // what a late loan holds is out over a booking's time: told with the loan ([Tx.retellLate])
	"reservation.conflict": TellsOnceWritable, // a pool's booking a late loan made wait: told with the loan ([Tx.retellLate])
	"work.due":             TellsOnceWritable, // the work coming due: asked again every pass while it is scheduled, and told once (sweepWork)
}

// ToldWhy is the kinds of notification whose words are a person's -- the
// reason of the event they tell of -- by the kind of that event: whoever
// asked for a reservation is told it was decided, and why. Each is written
// beside its event, in its transaction, about the event's subject; and that
// is how taking the reason out finds it ([Told], design D51), since nothing
// else links the two. A notice that says anybody's words is of a kind here,
// and the domain's tests hold every notice they write to it.
var ToldWhy = map[string]string{
	"reservation.approve": "reservation.decided",
	"reservation.reject":  "reservation.decided",
}

// notify leaves a message for one holder, queued for mail as well when it is
// of a kind that goes by mail and this deployment sends any. Queued and not
// sent: the row is written in the operation's transaction, so an operation
// that is undone mails nothing, and `Mailer` sends after.
//
// Nothing, while the workspace is read-only, that a sweep sends of a kind it
// is not told then ([ReadOnlyTells], D67): no row, so no mail, and what
// dedupes a reminder ([Tx.noticed]) has not seen it -- the sweeps tell it
// once the workspace writes again. Nothing else tells one later, so nothing
// else holds one back (D68): what an operator's command tells is of what the
// operator did, told as it is done, read-only or not.
//
// And nothing at all in a workspace that is closed (D68), whoever writes it:
// nobody there may read it, and a mail would ask them into what they cannot
// open. The sweeps do not sweep one; what else would tell it -- an
// operator's command -- is said in the log, since nothing tells it later.
func (t *Tx) notify(h uuid.UUID, kind, title, body string, subject pdid.Id, link string) error {
	closed, err := t.closed()
	if err != nil {
		return err
	}
	if closed {
		log.From(t.ctx).InfoContext(t.ctx, "domain: a notice was not written, as its workspace is closed",
			"workspace", t.tenant.String(), "kind", kind, "subject", subject.String())
		return nil
	}
	if t.shared != nil && t.shared.sweep && ReadOnlyTells[kind] != TellsStill {
		quiet, err := t.readonly()
		if err != nil || quiet {
			return err
		}
	}
	req := app.NotificationAddRequest_builder{
		Tenant: t.tenantRef(),
		Holder: app.HolderRef_builder{Id: pdid.Id(h).Bytes()}.Build(),
		Kind:   kind,
		Name:   title,
		Desc:   body,
		Link:   link,
		Mail:   Mailed[kind] && t.deps.mailing(),
	}.Build()
	if !subject.IsZero() {
		req.SetSubjectId(subject.Bytes())
	}
	_, err = t.next.Notification().Add(t.ctx, req)
	return err
}

// notifyManagers tells everybody who may act on it.
func (t *Tx) notifyManagers(kind, title, body string, subject pdid.Id, link string) error {
	hs, err := t.db.Holder.Query().Where(
		holder.TenantId(t.tenant.Uuid()),
		holder.RoleIn("owner", "admin", "manager"),
		holder.DateErasedIsNil(),
	).Ids(t.ctx)
	if err != nil {
		return err
	}
	for _, h := range hs {
		if pdid.Id(h) == t.actor {
			continue
		}
		if err := t.notify(h, kind, title, body, subject, link); err != nil {
			return err
		}
	}
	return nil
}

// notifyAdmins tells whoever runs the workspace: its owners and
// administrators, who may act on its domains and its people.
func (t *Tx) notifyAdmins(kind, title, body string, subject pdid.Id, link string) error {
	hs, err := t.db.Holder.Query().Where(
		holder.TenantId(t.tenant.Uuid()),
		holder.RoleIn("owner", "admin"),
		holder.DateErasedIsNil(),
	).Ids(t.ctx)
	if err != nil {
		return err
	}
	for _, h := range hs {
		if pdid.Id(h) == t.actor {
			continue
		}
		if err := t.notify(h, kind, title, body, subject, link); err != nil {
			return err
		}
	}
	return nil
}

// notifyParty tells a person, when they have a login.
func (t *Tx) notifyParty(p uuid.UUID, kind, title, body string, subject pdid.Id, link string) error {
	v, err := t.db.Party.Query().Where(party.Id(p), party.TenantId(t.tenant.Uuid())).Only(t.ctx)
	if err != nil || v.HolderId == (uuid.UUID{}) {
		return nil
	}
	if pdid.Id(v.HolderId) == t.actor {
		return nil
	}
	return t.notify(v.HolderId, kind, title, body, subject, link)
}

type domainNotification struct {
	Domain
	app.NotificationServiceServer
}

func (s Domain) Notification() app.NotificationServiceServer {
	return domainNotification{s, s.Next().Notification()}
}

// Inbox answers the caller's messages, newest first.
func (s domainNotification) Inbox(ctx context.Context, req *app.NotificationInboxRequest) (*app.NotificationInboxResponse, error) {
	t, err := s.read(ctx)
	if err != nil {
		return nil, err
	}
	q := t.db.Notification.Query().Where(notification.TenantId(t.tenant.Uuid()), notification.HolderId(t.actor.Uuid()))
	unread, err := q.Clone().Where(notification.ReadAtIsNil()).Count(ctx)
	if err != nil {
		return nil, err
	}
	if req.GetUnread() {
		q = q.Where(notification.ReadAtIsNil())
	}
	size := int(req.GetSize())
	if size <= 0 || size > 200 {
		size = 50
	}
	ids, err := q.Order(notification.ByDateCreated(sql.OrderDesc())).Limit(size).Ids(ctx)
	if err != nil {
		return nil, err
	}
	items := []*app.Notification{}
	for _, k := range ids {
		v, err := t.next.Notification().Get(ctx, app.NotificationGetRequest_builder{Ref: app.NotificationRef_builder{Id: pdid.Id(k).Bytes()}.Build()}.Build())
		if err == nil {
			items = append(items, v)
		}
	}
	return app.NotificationInboxResponse_builder{Items: items, Unread: uint32(unread)}.Build(), nil
}

// SetMail is whether the caller is mailed what they are told of here. It is a
// setting of their membership, and the person's own: there is no request that
// names somebody else -- and so a read-only workspace still takes it ([Own],
// D67), as it takes their notices marked read.
func (s domainNotification) SetMail(ctx context.Context, req *app.NotificationSetMailRequest) (*app.NotificationSetMailResponse, error) {
	err := s.tx(ctx, func(t *Tx) error {
		_, err := t.next.Holder().Patch(t.ctx, app.HolderPatchRequest_builder{
			Ref:              app.HolderRef_builder{Id: t.actor.Bytes()}.Build(),
			MailOff:          z.Ptr(req.GetOff()),
			DateUpdatedForce: z.Ptr(true),
		}.Build())
		return err
	})
	if err != nil {
		return nil, err
	}
	return app.NotificationSetMailResponse_builder{Off: req.GetOff()}.Build(), nil
}

// MarkRead marks the caller's messages read: the ones named, or all of them,
// and none of anybody else's. A read-only workspace still does ([Own], D67).
func (s domainNotification) MarkRead(ctx context.Context, req *app.NotificationMarkReadRequest) (*app.NotificationMarkReadResponse, error) {
	var n uint32
	err := s.tx(ctx, func(t *Tx) error {
		q := t.db.Notification.Query().Where(
			notification.TenantId(t.tenant.Uuid()),
			notification.HolderId(t.actor.Uuid()),
			notification.ReadAtIsNil(),
		)
		if len(req.GetIds()) > 0 {
			ids := []uuid.UUID{}
			for _, b := range req.GetIds() {
				ids = append(ids, uuidOf(b))
			}
			q = q.Where(notification.IdIn(ids...))
		}
		ids, err := q.Ids(t.ctx)
		if err != nil {
			return err
		}
		for _, k := range ids {
			if _, err := t.next.Notification().Patch(t.ctx, app.NotificationPatchRequest_builder{
				Ref:              app.NotificationRef_builder{Id: pdid.Id(k).Bytes()}.Build(),
				ReadAt:           ts(t.now),
				DateUpdatedForce: z.Ptr(true),
			}.Build()); err != nil {
				return err
			}
			n++
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return app.NotificationMarkReadResponse_builder{Marked: n}.Build(), nil
}
