package cli

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/lesomnus/payday/access"
	"github.com/lesomnus/payday/frame"
	"github.com/lesomnus/payday/pdid"
	"github.com/lesomnus/xli"
	"github.com/lesomnus/xli/flg"
	"google.golang.org/grpc"

	app "github.com/lesomnus/rove"
	"github.com/lesomnus/rove/cmd"
	"github.com/lesomnus/rove/internal/ent"
	entevent "github.com/lesomnus/rove/internal/ent/event"
	"github.com/lesomnus/rove/server/pd"
)

// redactAction is what `rove event redact` is called in the access record:
// the command, since it is no RPC.
const redactAction = "rove event redact"

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

// newCmdEventRedact is `rove event redact`: what `EventService.Redact` does,
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
			var act pdid.Id
			_, err = rec.Unary()(as, req, &grpc.UnaryServerInfo{FullMethod: redactAction}, func(ctx context.Context, _ any) (any, error) {
				v, a, err := s.Deps.RedactEvent(ctx, s.Base, s.Drv, tenant, id, why)
				act = a
				return v, err
			})
			if ferr := s.Access.Flush(ctx); ferr != nil {
				return errors.Join(err, fmt.Errorf("the access record of what it read: %w", ferr))
			}
			if err != nil {
				return err
			}

			self.Printf("took the reason out of event %s of @%s; the act is event %s\n", id, t.Alias, act)
			return next(ctx)
		}),
	}
}

// instantOf is a moment as an operator's command says one: in UTC, whatever
// zone the shell it runs in is in ([day]).
func instantOf(t time.Time) string {
	return t.UTC().Format(time.RFC3339)
}
