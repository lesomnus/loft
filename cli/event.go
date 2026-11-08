package cli

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/lesomnus/payday/pdid"
	"github.com/lesomnus/xli"
	"github.com/lesomnus/xli/flg"
	"google.golang.org/protobuf/types/known/timestamppb"

	app "github.com/lesomnus/loft"
	"github.com/lesomnus/loft/server/pd"
)

// tallied is how many of each kind a map says, as an operator reads it --
// "custody.issue ×2, reservation.add ×1" -- and how many in all.
func tallied(n map[string]uint32) (string, uint32) {
	said := make([]string, 0, len(n))
	all := uint32(0)
	for _, k := range slices.Sorted(maps.Keys(n)) {
		said = append(said, fmt.Sprintf("%s ×%d", k, n[k]))
		all += n[k]
	}
	return strings.Join(said, ", "), all
}

// eventWritten is `loft event redact`, a workspace's history as the operator
// acts on it on the workspace's word, `loft access list`, the access record
// of the whole deployment, and `loft access-review list`, the reviews of it
// -- the last either kind's, as `AccessReviewService.List` is: an operator
// reads every workspace's, and a workspace's administrators and auditors its
// own. The generated `access list` calls what no role and no entry point
// serves, and the operator's own is its place.
func eventWritten() []written {
	out := []written{}
	for _, c := range newCmdEvent().Commands {
		if c.Name == "redact" {
			out = append(out, written{path: "event/redact", replace: true, cmd: c, calls: []string{app.EventService_Redact_FullMethodName}})
		}
	}
	for _, c := range newCmdAccess().Commands {
		switch c.Name {
		case "ls":
			out = append(out, written{path: "access/list", replace: true, cmd: c, calls: []string{app.OperatorAccessService_List_FullMethodName}})
		case "reviews":
			out = append(out, written{path: "access-review/list", replace: true, cmd: c, calls: []string{app.AccessReviewService_List_FullMethodName}})
		}
	}
	return out
}

func newCmdEvent() *xli.Command {
	return &xli.Command{
		Name:  "event",
		Brief: "a workspace's history, as an operator acts on it: an event's reason taken out",
		Commands: xli.Commands{
			{
				Name:  "redact",
				Brief: "take the reason out of an event, for a person whose request to erase what it says of them points at it",
				Flags: flg.Flags{
					&flg.String{Name: "tenant", Brief: "the workspace, by address"},
					&flg.String{Name: "event", Brief: "the event, by its identifier"},
					&flg.String{Name: "why", Brief: "the request this answers, by its reference; kept as the act's own reason, which the trail never holds"},
					&flg.Switch{Name: "yes", Brief: "do it; without this it only says what it would do"},
				},
				Handler: xli.OnRun(func(ctx context.Context, self *xli.Command, next xli.Next) error {
					ws := strings.TrimPrefix(str(self, "tenant"), "@")
					if ws == "" {
						return errors.New("--tenant: which workspace, by address")
					}
					why := str(self, "why")
					if why == "" {
						return errors.New("--why: the request this answers, by its reference -- it is kept as the act's reason")
					}
					id, err := pdid.Parse(str(self, "event"))
					if err != nil {
						return fmt.Errorf("--event: %w", err)
					}
					if id.Domain() != pd.EventDomain {
						return fmt.Errorf("--event: %s is no event's identifier", id)
					}
					conn, err := operatorFor(ctx, self)
					if err != nil {
						return err
					}
					events := app.NewOperatorEventServiceClient(conn)
					ask := app.OperatorEventRedactionRequest_builder{Event: id.Bytes(), Tenant: byAlias(ws)}.Build()

					// What it is, without what it says, and where else its
					// operation says the same.
					v, err := events.Redaction(ctx, ask)
					if err != nil {
						return err
					}
					about := ""
					if len(v.GetSubject()) > 0 {
						about = ", about " + idSaid(v.GetSubject())
					}
					self.Printf("event %s of @%s: %s, happened %s, recorded %s%s\n", id, v.GetTenant(), v.GetKind(), momentOf(v.GetDateOccurred()), momentOf(v.GetDateRecorded()), about)
					if !v.GetSaid() {
						self.Printf("it has no reason: there is nothing to take out\n")
						for _, a := range v.GetActs() {
							self.Printf("event %s took it out, %s%s\n", idSaid(a.GetEvent()), momentOf(a.GetDateRecorded()), byWhom(a))
						}
						return next(ctx)
					}
					copies, nCopies := tallied(v.GetCopies())
					if nCopies > 0 {
						self.Printf("the operation it is part of says the same in %d other events, which it is taken out of too: %s\n", nCopies, copies)
					}
					told, nTold := tallied(v.GetTold())
					if nTold > 0 {
						self.Printf("%d notice(s) say the same as their words, which it is taken out of too: %s\n", nTold, told)
						if v.GetMailed() > 0 {
							self.Printf("%d of them went by mail before this, which is not taken back\n", v.GetMailed())
						}
					}
					kept, nKept := tallied(v.GetKept())
					if nKept > 0 {
						self.Printf("%d row(s) keep the same as their own words, which it is taken out of too: %s\n", nKept, kept)
					}
					if !yes(self) {
						self.Printf("would take its reason out, and record that as an event of @%s's own\n", v.GetTenant())
						self.Printf("(nothing was done: `--yes` does it. It cannot be undone)\n")
						return next(ctx)
					}

					if _, err := app.NewEventServiceClient(conn).Redact(ctx, app.EventRedactRequest_builder{
						Ref:    app.EventRef_builder{Id: id.Bytes()}.Build(),
						Reason: why,
					}.Build()); err != nil {
						return err
					}
					line := fmt.Sprintf("took the reason out of event %s of @%s", id, v.GetTenant())
					if after, err := events.Redaction(ctx, ask); err == nil && len(after.GetActs()) > 0 {
						line += "; the act is event " + idSaid(after.GetActs()[0].GetEvent())
					}
					self.Printf("%s\n", line)
					if nCopies > 0 {
						self.Printf("and out of the %d other events of its operation that said the same, each with an event of the act's\n", nCopies)
					}
					if nTold > 0 {
						self.Printf("and out of the %d notice(s) that said the same as their words; one that went by mail before is not taken back\n", nTold)
					}
					if nKept > 0 {
						self.Printf("and out of the %d row(s) that kept the same as their own words\n", nKept)
					}
					return next(ctx)
				}),
			},
		},
		Handler: xli.RequireSubcommand(),
	}
}

// byWhom is who did an act, as a line ends with it.
func byWhom(a *app.OperatorEventRedactionAct) string {
	switch {
	case a.GetOperator() != "":
		return " by " + a.GetOperator() + ", an operator"
	case len(a.GetActor()) > 0:
		return " by " + idSaid(a.GetActor())
	}
	return ", the deployment's own work"
}

// newCmdAccess is the access record of the whole deployment (loft#136) --
// who read, wrote or took out whose personal data, from where, in a
// workspace, by a member, of one person, of one kind -- and the reviews the
// workspaces' administrators recorded. A person asking what was done with
// their data is answered with `loft access list --subject`. It says
// identifiers and no names, but an operator's, and changes nothing.
func newCmdAccess() *xli.Command {
	return &xli.Command{
		Name:  "access",
		Brief: "the access record: who read, wrote or took out whose personal data, and its reviews",
		Commands: xli.Commands{
			{
				Name:  "ls",
				Brief: "the calls that carried somebody's personal data, newest first",
				Flags: flg.Flags{
					&flg.String{Name: "tenant", Brief: "only this workspace's record, by address"},
					&flg.String{Name: "from", Brief: "from this date or instant (RFC 3339); thirty days ago if left out"},
					&flg.String{Name: "to", Brief: "before this date or instant; now if left out"},
					&flg.String{Name: "actor", Brief: "only the calls of this actor -- a membership, an account, an operator -- by id"},
					&flg.String{Name: "subject", Brief: "only the calls that carried this person's data, by the id of their person record or membership"},
					&flg.String{Name: "kind", Brief: "only read, write or download"},
					&flg.Uint32{Name: "limit", Brief: "at most this many; 100 if left out, and 1000 at most"},
				},
				Handler: xli.OnRun(func(ctx context.Context, self *xli.Command, next xli.Next) error {
					req := app.OperatorAccessListRequest_builder{Kind: str(self, "kind")}.Build()
					if ws := strings.TrimPrefix(str(self, "tenant"), "@"); ws != "" {
						req.SetTenant(byAlias(ws))
					}
					for _, f := range []struct {
						name string
						set  func(*timestamppb.Timestamp)
					}{{"from", req.SetDateFrom}, {"to", req.SetDateTo}} {
						if v := str(self, f.name); v != "" {
							t, err := instant(v)
							if err != nil {
								return fmt.Errorf("--%s: %w", f.name, err)
							}
							f.set(timestamppb.New(t))
						}
					}
					for _, f := range []struct {
						name string
						set  func([]byte)
					}{{"actor", req.SetActor}, {"subject", req.SetSubject}} {
						if v := str(self, f.name); v != "" {
							id, err := pdid.Parse(v)
							if err != nil {
								return fmt.Errorf("--%s: %w", f.name, err)
							}
							f.set(id.Bytes())
						}
					}
					if v, _ := flg.Find[uint32](self, "limit"); v > 0 {
						req.SetSize(v)
					}
					conn, err := operatorFor(ctx, self)
					if err != nil {
						return err
					}
					v, err := app.NewOperatorAccessServiceClient(conn).List(ctx, req)
					if err != nil {
						return err
					}
					for _, r := range v.GetItems() {
						actor := idSaid(r.GetActor())
						if r.GetOperator() != "" {
							actor += " (" + r.GetOperator() + ", an operator)"
						}
						reason := ""
						if r.GetReason() != "" {
							reason = fmt.Sprintf("  reason=%q", r.GetReason())
						}
						address := r.GetAddress()
						if address == "" {
							address = "-"
						}
						self.Printf("%s  %s  %-8s %-18s %s  actor=%s  from=%s  subjects=%d%s\n",
							momentOf(r.GetDateCreated()), r.GetTenant(), r.GetKind(), r.GetResult(), r.GetAction(), actor, address, r.GetSubjects(), reason)
					}
					if len(v.GetItems()) == 0 {
						self.Printf("nothing in that stretch\n")
					}
					return next(ctx)
				}),
			},
			{
				Name:  "reviews",
				Brief: "the reviews of the record a workspace's administrators recorded, newest first",
				Flags: flg.Flags{
					&flg.String{Name: "tenant", Brief: "only this workspace's, by address"},
				},
				Handler: xli.OnRun(func(ctx context.Context, self *xli.Command, next xli.Next) error {
					conn, _, err := remote{}.Connect(ctx)
					if err != nil {
						return err
					}
					var only []byte
					if ws := strings.TrimPrefix(str(self, "tenant"), "@"); ws != "" {
						t, err := app.NewTenantServiceClient(conn).Get(ctx, app.TenantGetRequest_builder{Ref: app.TenantRef_builder{Alias: &ws}.Build()}.Build())
						if err != nil {
							return err
						}
						only = t.GetId()
					}

					// Every page of every workspace's, as the wall lets no
					// workspace's own read them: a review a month each.
					vs := []*app.AccessReview{}
					for after := ""; ; {
						page, err := app.NewAccessReviewServiceClient(conn).List(ctx, app.AccessReviewListRequest_builder{Size: 500, After: after}.Build())
						if err != nil {
							return err
						}
						for _, r := range page.GetItems() {
							if only == nil || slices.Equal(r.GetTenant().GetId(), only) {
								vs = append(vs, r)
							}
						}
						if after = page.GetNext(); after == "" {
							break
						}
					}
					slices.SortStableFunc(vs, func(a, b *app.AccessReview) int {
						return b.GetDateCreated().AsTime().Compare(a.GetDateCreated().AsTime())
					})

					aliases := map[string]string{}
					aliasOf := func(id []byte) string {
						if v, ok := aliases[string(id)]; ok {
							return v
						}
						v := idSaid(id)
						if t, err := app.NewTenantServiceClient(conn).Get(ctx, app.TenantGetRequest_builder{Ref: app.TenantRef_builder{Id: id}.Build()}.Build()); err == nil {
							v = t.GetAlias()
						}
						aliases[string(id)] = v
						return v
					}
					for _, r := range vs {
						desc := ""
						if r.GetDesc() != "" {
							desc = fmt.Sprintf("  %q", r.GetDesc())
						}
						self.Printf("%s  %s  %s – %s  calls=%d downloads=%d refused=%d  by=%s%s\n",
							momentOf(r.GetDateCreated()), aliasOf(r.GetTenant().GetId()),
							momentOf(r.GetDateStart()), momentOf(r.GetDateEnd()),
							r.GetCalls(), r.GetDownloads(), r.GetRefused(), idSaid(r.GetReviewer().GetId()), desc)
					}
					if len(vs) == 0 {
						self.Printf("no review recorded\n")
					}
					return next(ctx)
				}),
			},
		},
		Handler: xli.RequireSubcommand(),
	}
}
