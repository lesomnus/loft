package domain

import (
	"context"
	"uuid"

	"github.com/lesomnus/payday/pdid"
	"github.com/lesomnus/z"
	"github.com/protobuf-orm/ent/dialect/sql"

	app "github.com/lesomnus/rove"
	"github.com/lesomnus/rove/internal/ent/holder"
	"github.com/lesomnus/rove/internal/ent/notification"
	"github.com/lesomnus/rove/internal/ent/party"
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
}

// notify leaves a message for one holder, queued for mail as well when it is
// of a kind that goes by mail and this deployment sends any. Queued and not
// sent: the row is written in the operation's transaction, so an operation
// that is undone mails nothing, and `Mailer` sends after.
func (t *Tx) notify(h uuid.UUID, kind, title, body string, subject pdid.Id, link string) error {
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
	_, err := t.next.Notification().Add(t.ctx, req)
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
// names somebody else.
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

// MarkRead marks the caller's messages read: the ones named, or all of them.
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
