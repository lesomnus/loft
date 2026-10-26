package domain

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/lesomnus/otx/log"
	"github.com/lesomnus/payday/pdid"
	"github.com/lesomnus/payday/spin"
	"github.com/protobuf-orm/ent/dialect"

	"github.com/lesomnus/rove/internal/ent"
	"github.com/lesomnus/rove/internal/ent/holder"
	"github.com/lesomnus/rove/internal/ent/notification"
	"github.com/lesomnus/rove/internal/ent/party"
	"github.com/lesomnus/rove/internal/identity"
	"github.com/lesomnus/rove/server/apphost"
	"github.com/lesomnus/rove/server/mail"
	"github.com/lesomnus/rove/server/pd"
	"github.com/lesomnus/rove/server/tenancy"
)

// Mailer sends by mail what the notifications of the mailed kinds say
// ([Mailed]), after the operation that made them and never inside it.
//
// A notification is queued as it is written (`Notification.mail`), in the
// operation's own transaction, so one that was undone was never queued. A pass
// takes what is queued and not yet sent, oldest first; claims each by counting
// the try, so that two servers do not send one twice; and sends it to the
// address of the person it is for. What fails stays queued and is tried again
// next pass, up to [mailTries] times. What has nobody to go to -- a membership
// that ended, a person who turned mail off, no address -- leaves the queue
// without a mail.
//
// The address is the one roster holds as checked for the account, and on the
// roster in this process the person record's beside it, which is where that
// deployment's addresses are written. A person record's address on a roster
// of its own is a workspace's note and nobody's proof, and is not mailed.
type Mailer struct {
	Drv  dialect.Driver
	Deps *Deps

	// Every is the wait between passes; a minute when unset.
	Every time.Duration
}

var _ spin.Spinner = Mailer{}

// mailTries is how often a notification's mail is tried before it is given
// up on, and said so in the log.
const mailTries = 8

func (m Mailer) SpinName() string { return "rove.mail" }

func (m Mailer) Spin(ctx context.Context) error {
	every := m.Every
	if every <= 0 {
		every = time.Minute
	}
	return spin.Every(every, m.Pass).Spin(ctx)
}

// Pass sends what is queued, as far as one pass goes.
func (m Mailer) Pass(ctx context.Context) error {
	if !m.Deps.mailing() {
		return nil
	}
	db := ent.NewClient(ent.Driver(m.Drv))
	rows, err := db.Notification.Query().
		Where(notification.Mail(true), notification.DateMailedIsNil()).
		Order(ent.Asc(notification.FieldDateCreated), ent.Asc(notification.FieldId)).
		Limit(200).
		All(ctx)
	if err != nil {
		return err
	}
	for _, n := range rows {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := m.one(ctx, db, n); err != nil {
			log.From(ctx).WarnContext(ctx, "mail: a notification was not sent; it is tried again",
				"notification", pdid.Id(n.Id).String(), slog.Any("err", err))
		}
	}
	return nil
}

func (m Mailer) one(ctx context.Context, db *ent.Client, n *ent.Notification) error {
	h, err := db.Holder.Query().Where(holder.Id(n.HolderId)).WithTenant().Only(ctx)
	if ent.IsNotFound(err) {
		return m.drop(ctx, db, n)
	}
	if err != nil {
		return err
	}
	if h.DateErased != nil || h.MailOff {
		return m.drop(ctx, db, n)
	}
	to, err := m.addressOf(ctx, db, h)
	if err != nil {
		return err
	}
	if to == "" {
		return m.drop(ctx, db, n)
	}

	// Claimed by counting the try: of two servers asking at once, one
	// counts it and sends, and the other finds it counted and moves on.
	claimed := false
	err = m.recorded(ctx, db, MailTried, n, func(db *ent.Client) (bool, error) {
		k, err := db.Notification.Update().
			Where(
				notification.Id(n.Id),
				notification.Mail(true),
				notification.DateMailedIsNil(),
				notification.MailTries(n.MailTries),
			).
			AddMailTries(1).
			Save(ctx)
		claimed = k > 0
		return claimed, err
	})
	if err != nil || !claimed {
		return err
	}
	// And sent as it says now, which may be less than the pass read: words
	// taken out of it since -- a reason a person asked to have erased
	// (`EventService.Redact`, design D51) -- are not mailed.
	says, err := db.Notification.Get(ctx, n.Id)
	if err != nil {
		return err
	}

	at, err := m.Deps.Links.Of(ctx, db, pdid.Id(h.TenantId), apphost.Page)
	if err != nil {
		return err
	}
	// What the workspace is called, as its pages call it (D57): after its
	// owner, for one with no name of its own.
	workspace := ""
	if t := h.Edges.Tenant; t != nil {
		called, err := tenancy.NameOf(ctx, db, t)
		if err != nil {
			return err
		}
		workspace = called.Said
	}
	if err := m.Deps.Mail.Send(ctx, m.message(h, says, at, to, workspace)); err != nil {
		if n.MailTries+1 >= mailTries {
			log.From(ctx).ErrorContext(ctx, "mail: a notification was given up on after every try",
				"notification", pdid.Id(n.Id).String(), "tries", n.MailTries+1, slog.Any("err", err))
			return m.drop(ctx, db, n)
		}
		return err
	}
	return m.recorded(ctx, db, MailSent, n, func(db *ent.Client) (bool, error) {
		return true, db.Notification.UpdateOneId(n.Id).SetDateMailed(m.Deps.now()).Exec(ctx)
	})
}

// drop takes a notification off the queue without a mail.
func (m Mailer) drop(ctx context.Context, db *ent.Client, n *ent.Notification) error {
	return m.recorded(ctx, db, MailDropped, n, func(db *ent.Client) (bool, error) {
		return true, db.Notification.UpdateOneId(n.Id).SetMail(false).Exec(ctx)
	})
}

// The acts the mailer writes a notification with, as the trail names them.
const (
	MailTried   = "rove.mail.try"
	MailSent    = "rove.mail.sent"
	MailDropped = "rove.mail.drop"
)

// recorded is one of the mailer's writes of a notification, and the act on
// the trail that says so, in one transaction (payday#108). The writes are the
// mailer's own -- a try counted, a mail sent, a notification taken off the
// queue -- and made through ent, since a claim is a condition no Patch can
// state; recorded, a check of the rows finds the notification as the mailer
// left it. `write` answers whether it wrote anything, and nothing is recorded
// when it did not.
func (m Mailer) recorded(ctx context.Context, db *ent.Client, act string, n *ent.Notification, write func(*ent.Client) (bool, error)) error {
	tx, err := db.Tx(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	wrote, err := write(tx.Client())
	if err != nil || !wrote {
		return err
	}
	if _, err := pd.Record(ctx, tx.Client(), m.Deps.Audit, act, []pdid.Id{pdid.Id(n.Id)}); err != nil {
		return err
	}

	return tx.Commit()
}

// addressOf is where a membership's mail goes, or nowhere.
func (m Mailer) addressOf(ctx context.Context, db *ent.Client, h *ent.Holder) (string, error) {
	people := m.Deps.People
	if h.Account != nil && people != nil {
		as, err := people.Addresses(ctx, pdid.Id(*h.Account))
		switch {
		case errors.Is(err, identity.ErrNoPerson):
		case err != nil:
			return "", err
		case len(as) > 0:
			return as[0], nil
		}
	}
	if people != nil && !people.Embedded() {
		return "", nil
	}
	p, err := db.Party.Query().Where(party.HolderId(h.Id), party.DateErasedIsNil()).First(ctx)
	if ent.IsNotFound(err) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(p.Email), nil
}

// message is what a notification says, as a mail: its title, its words, and
// the link that opens what it is about, on `at`: where its workspace is,
// which it is called `workspace`.
func (m Mailer) message(h *ent.Holder, n *ent.Notification, at, to, workspace string) mail.Message {
	alias := ""
	if t := h.Edges.Tenant; t != nil {
		alias = t.Alias
	}
	link := at
	if alias != "" {
		link += "/@" + alias
	}
	link += n.Link

	body := n.Desc
	if body != "" {
		body += "\n\n"
	}
	body += fmt.Sprintf(`%s

Rove의 %s에서 온 알림입니다. 메일로 받지 않으려면 Rove의 내 정보에서 끌 수 있습니다. 알림은 앱에서 계속 받습니다.
`, link, workspaceWords(workspace))

	return mail.Message{To: to, Subject: "[Rove] " + n.Name, Body: body}
}
