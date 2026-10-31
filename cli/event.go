package cli

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"
	"uuid"

	"github.com/lesomnus/payday/access"
	"github.com/lesomnus/payday/frame"
	"github.com/lesomnus/payday/pdid"
	"github.com/lesomnus/xli"
	"github.com/lesomnus/xli/flg"
	"google.golang.org/grpc"

	app "github.com/lesomnus/loft"
	"github.com/lesomnus/loft/cmd"
	"github.com/lesomnus/loft/internal/ent"
	entevent "github.com/lesomnus/loft/internal/ent/event"
	entnotification "github.com/lesomnus/loft/internal/ent/notification"
	"github.com/lesomnus/loft/server/domain"
	"github.com/lesomnus/loft/server/pd"
)

// redactAction is what `loft event redact` is called in the access record:
// the command, since it is no RPC.
const redactAction = "loft event redact"

// NewCmdEvent is what an operator does to a workspace's history: taking an
// event's reason out, for a person whose request to erase what it says of
// them points at it (payday#151).
func NewCmdEvent(c *cmd.Config) *xli.Command {
	return &xli.Command{
		Name:  "event",
		Brief: "a workspace's history, as an operator acts on it: an event's reason taken out",

		Commands: xli.Commands{
			newCmdEventRedact(c),
		},
	}
}

// newCmdEventRedact is `loft event redact`: what `EventService.Redact` does,
// done by the deployment's operator on the instruction of the workspace the
// event is its -- which decides, as it keeps the history -- since no role of a
// workspace is given it yet (`server/policy`).
//
// It is the deployment's own work in the workspace, as every operator's act
// on one is ([domain.Deps.RedactEvent]): the act's event and the trail's line
// are nobody's, filed under the workspace. Its access record is the one the
// interceptor writes of a call -- what it read, and what it wrote at its
// commit -- with the operator where a call's address would be, and neither
// reason in it: the one taken out, nor the request's reference.
//
// The words are taken out of every event of the operation the event is part
// of that says them -- its parts copy its reason (design D49) -- of every
// notice that says them as its own (D51), and of every row that keeps them as
// its own (D56), and before `--yes` it says how many those are, and of which
// kinds, having read none of what they say ([domain.Copies], [domain.Told],
// [domain.Kept]).
func newCmdEventRedact(c *cmd.Config) *xli.Command {
	return &xli.Command{
		Name:  "redact",
		Brief: "take the reason out of an event, for a person whose request to erase what it says of them points at it",
		Flags: flg.Flags{
			&flg.String{Name: "tenant", Brief: "the workspace, by alias"},
			&flg.String{Name: "event", Brief: "the event, by its identifier"},
			&flg.String{Name: "why", Brief: "the request this answers, by its reference; kept as the act's own reason, which the trail never holds"},
			&flg.String{Name: "by", Brief: "who does it, for the access record; the operator's user and host by default"},
			&flg.Switch{Name: "yes", Brief: "do it; without this it only says what it would do"},
		},
		Handler: xli.OnRun(func(ctx context.Context, self *xli.Command, next xli.Next) error {
			yes, _ := flg.Find[bool](self, "yes")
			why := flag(self, "why")
			if why == "" {
				return errors.New("--why: the request this answers, by its reference -- it is kept as the act's reason")
			}
			by := flag(self, "by")
			if by == "" {
				by = operator()
			}
			if by == "" {
				return errors.New("--by: who does it, since this shell does not say")
			}
			id, err := pdid.Parse(flag(self, "event"))
			if err != nil {
				return fmt.Errorf("--event: %w", err)
			}
			if id.Domain() != pd.EventDomain {
				return fmt.Errorf("--event: %s is no event's identifier", id)
			}

			s, err := cmd.Build(ctx, *c)
			if err != nil {
				return err
			}
			defer s.Close()
			t, err := tenantNamed(ctx, s.Ent, self)
			if err != nil {
				return err
			}
			tenant := pdid.Id(t.Id)

			// What it is, without what it says: whether there is a reason,
			// and not the reason, which is somebody's words.
			ev, err := s.Ent.Event.Query().
				Where(entevent.Id(id.Uuid()), entevent.TenantId(t.Id)).
				Select(entevent.FieldId, entevent.FieldKind, entevent.FieldOccurredAt, entevent.FieldDateCreated, entevent.FieldSubjectId).
				Only(ctx)
			if ent.IsNotFound(err) {
				return fmt.Errorf("--event: @%s has no event %s", t.Alias, id)
			}
			if err != nil {
				return err
			}
			said, err := s.Ent.Event.Query().Where(entevent.Id(id.Uuid()), entevent.TenantId(t.Id), entevent.ReasonNEQ("")).Exist(ctx)
			if err != nil {
				return err
			}
			about := ""
			if ev.SubjectId != nil {
				about = ", about " + pdid.Id(*ev.SubjectId).String()
			}
			self.Printf("event %s of @%s: %s, happened %s, recorded %s%s\n", id, t.Alias, ev.Kind, instantOf(ev.OccurredAt), instantOf(ev.DateCreated), about)
			if !said {
				self.Printf("it has no reason: there is nothing to take out\n")
				return next(ctx)
			}
			// And where else its operation says the same, found without
			// reading what it says (design D49).
			copies, err := domain.Copies(ctx, s.Ent, tenant, id)
			if err != nil {
				return err
			}
			if len(copies) > 0 {
				kinds, err := kindsOf(ctx, s.Ent, copies)
				if err != nil {
					return err
				}
				self.Printf("the operation it is part of says the same in %d other events, which it is taken out of too: %s\n", len(copies), kinds)
			}
			told, err := domain.Told(ctx, s.Ent, tenant, id, copies)
			if err != nil {
				return err
			}
			if len(told) > 0 {
				kinds, err := noticeKindsOf(ctx, s.Ent, told)
				if err != nil {
					return err
				}
				self.Printf("%d notice(s) say the same as their words, which it is taken out of too: %s\n", len(told), kinds)
				sent, err := mailed(ctx, s.Ent, told)
				if err != nil {
					return err
				}
				if sent > 0 {
					self.Printf("%d of them went by mail before this, which is not taken back\n", sent)
				}
			}
			kept, err := domain.Kept(ctx, s.Ent, tenant, id, copies)
			if err != nil {
				return err
			}
			if len(kept) > 0 {
				self.Printf("%d row(s) keep the same as their own words, which it is taken out of too: %s\n", len(kept), rowKindsOf(kept))
			}
			if !yes {
				self.Printf("would take its reason out, and record that as an event of @%s's own\n", t.Alias)
				self.Printf("(nothing was done: `--yes` does it. It cannot be undone)\n")
				return next(ctx)
			}

			// Recorded as the interceptor records a call, the operator where
			// its address would be; what it read waits to be written, and is
			// written before this ends, since nothing else here will.
			as := frame.Into(ctx, frame.New(pdid.Nil, tenant, frame.Grant{}).WithScope(frame.Only(tenant)))
			rec := access.Config{
				Subjects: pd.Subjects,
				Refs:     pd.SubjectRefs,
				Address:  func(context.Context) string { return by },
				Writer:   s.Access,
			}
			req := app.EventRedactRequest_builder{Ref: app.EventRef_builder{Id: id.Bytes()}.Build(), Reason: why}.Build()
			var done domain.Redacted
			_, err = rec.Unary()(as, req, &grpc.UnaryServerInfo{FullMethod: redactAction}, func(ctx context.Context, _ any) (any, error) {
				v, err := s.Deps.RedactEvent(ctx, s.Base, s.Drv, tenant, id, why)
				done = v
				return v.Event, err
			})
			if ferr := s.Access.Flush(ctx); ferr != nil {
				return errors.Join(err, fmt.Errorf("the access record of what it read: %w", ferr))
			}
			if err != nil {
				return err
			}

			self.Printf("took the reason out of event %s of @%s; the act is event %s\n", id, t.Alias, done.Act)
			if n := len(done.Copies); n > 0 {
				self.Printf("and out of the %d other events of its operation that said the same, each with an event of the act's\n", n)
			}
			if n := len(done.Notices); n > 0 {
				self.Printf("and out of the %d notice(s) that said the same as their words; one that went by mail before is not taken back\n", n)
			}
			if n := len(done.Rows); n > 0 {
				self.Printf("and out of the %d row(s) that kept the same as their own words\n", n)
			}
			return next(ctx)
		}),
	}
}

// kindsOf is what kinds of event `ids` are, and how many of each, as an
// operator reads it: "custody.issue ×2, reservation.add ×1".
func kindsOf(ctx context.Context, db *ent.Client, ids []pdid.Id) (string, error) {
	return tally(ids, func(us []uuid.UUID) ([]string, error) {
		return db.Event.Query().Where(entevent.IdIn(us...)).Select(entevent.FieldKind).Strings(ctx)
	})
}

// noticeKindsOf is [kindsOf] of notices: "reservation.decided ×3".
func noticeKindsOf(ctx context.Context, db *ent.Client, ids []pdid.Id) (string, error) {
	return tally(ids, func(us []uuid.UUID) ([]string, error) {
		return db.Notification.Query().Where(entnotification.IdIn(us...)).Select(entnotification.FieldKind).Strings(ctx)
	})
}

// rowKindsOf is [kindsOf] of rows of any kind, by the name the schema gives
// each: "Custody ×1".
func rowKindsOf(ids []pdid.Id) string {
	said, _ := tally(ids, func(us []uuid.UUID) ([]string, error) {
		out := make([]string, len(us))
		for i, u := range us {
			out[i] = kindOf(pdid.Id(u).Domain())
		}
		return out, nil
	})
	return said
}

// mailed is how many of the notices `ids` went by mail.
func mailed(ctx context.Context, db *ent.Client, ids []pdid.Id) (int, error) {
	n := 0
	for page := range slices.Chunk(ids, 500) {
		us := make([]uuid.UUID, len(page))
		for i, v := range page {
			us[i] = v.Uuid()
		}
		k, err := db.Notification.Query().Where(entnotification.IdIn(us...), entnotification.DateMailedNotNil()).Count(ctx)
		if err != nil {
			return 0, err
		}
		n += k
	}
	return n, nil
}

// tally is how many of `ids` are of each kind, which `kinds` answers of each
// page of them.
func tally(ids []pdid.Id, kinds func([]uuid.UUID) ([]string, error)) (string, error) {
	n := map[string]int{}
	for page := range slices.Chunk(ids, 500) {
		us := make([]uuid.UUID, len(page))
		for i, v := range page {
			us[i] = v.Uuid()
		}
		vs, err := kinds(us)
		if err != nil {
			return "", err
		}
		for _, v := range vs {
			n[v]++
		}
	}
	said := make([]string, 0, len(n))
	for _, k := range slices.Sorted(maps.Keys(n)) {
		said = append(said, fmt.Sprintf("%s ×%d", k, n[k]))
	}
	return strings.Join(said, ", "), nil
}

// instantOf is a moment as an operator's command says one: in UTC, whatever
// zone the shell it runs in is in ([day]).
func instantOf(t time.Time) string {
	return t.UTC().Format(time.RFC3339)
}
