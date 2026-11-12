package cli

import (
	"context"
	"errors"
	"fmt"

	"github.com/lesomnus/xli"
	"github.com/lesomnus/xli/flg"

	app "github.com/lesomnus/loft"
)

// identityWritten is `loft identity`, who the members are, which is roster's
// (design 9.10), and `loft holder add|password`, a member on the roster in
// this process (D85): the operator's. The generated `holder add` calls
// nothing any role or the entry point serves, and the operator's own is its
// place.
func identityWritten() []written {
	out := group(newCmdIdentity(), map[string][]string{
		"link":      {app.OperatorIdentityService_Link_FullMethodName},
		"depart":    {app.OperatorIdentityService_Depart_FullMethodName},
		"reconcile": {app.OperatorIdentityService_Reconcile_FullMethodName},
	})
	holder := newCmdHolder()
	for _, c := range holder.Commands {
		switch c.Name {
		case "add":
			out = append(out, written{path: "holder/add", replace: true, cmd: c, calls: []string{app.OperatorHolderService_Add_FullMethodName}})
		case "password":
			out = append(out, written{path: "holder/password", cmd: c, calls: []string{app.OperatorHolderService_Password_FullMethodName}})
		}
	}
	return out
}

func newCmdIdentity() *xli.Command {
	return &xli.Command{
		Name:  "identity",
		Brief: "who the members are: roster, in this process or of its own",
		Commands: xli.Commands{
			{
				Name:  "link",
				Brief: "make at roster each member it does not have, by their identifier here: those of a workspace restored from an export",
				Flags: flg.Flags{
					&flg.String{Name: "password", Brief: "give everybody this one, for a deployment to try things on; empty makes one up for each"},
				},
				Handler: xli.OnRun(func(ctx context.Context, self *xli.Command, next xli.Next) error {
					conn, err := operatorFor(ctx, self)
					if err != nil {
						return err
					}
					stream, err := app.NewOperatorIdentityServiceClient(conn).Link(ctx, app.OperatorIdentityLinkRequest_builder{Password: str(self, "password")}.Build())
					if err != nil {
						return err
					}
					var done *app.OperatorIdentityLinked
					told := false
					if err := each(stream, self, func(m *app.OperatorIdentityLinkResponse) string {
						if v := m.GetAdopted(); m.HasAdopted() {
							switch {
							case v.GetMake() != "":
								if !told {
									self.Printf("# roster is not in this process: its operator makes these people in the tenant, with the\n")
									self.Printf("# identifiers they have here, and gives each a way in there\n")
									told = true
								}
								self.Printf("%s\n", v.GetMake())
							case v.GetErased():
								self.Printf("%s  erased at roster, which makes nobody under their identifier again: they do not sign in here\n", v.GetMember())
							default:
								self.Printf("%s  login %s  password %s\n", v.GetMember(), v.GetLogin(), v.GetPassword())
							}
						}
						if m.HasDone() {
							done = m.GetDone()
						}
						return ""
					}); err != nil {
						return err
					}
					if done == nil {
						return errors.New("the entry point ended without saying it was done")
					}
					switch {
					case !done.GetEmbedded() && done.GetTodo() > 0:
					case done.GetMade() == 0 && done.GetErased() == 0:
						self.Printf("nobody to make: roster has every member here (%d) in @%s\n", done.GetHad(), done.GetTenant())
					case done.GetMade() == 0:
					case done.GetGiven():
						self.Printf("\n%d made at roster, every one with the password given\n", done.GetMade())
					default:
						self.Printf("\n%d password(s), shown once\n", done.GetMade())
					}
					if done.GetErased() > 0 {
						self.Printf("%d erased at roster: they leave here once reconciled (`loft identity reconcile`), unless roster restores them in time\n", done.GetErased())
					}
					return next(ctx)
				}),
			},
			{
				Name:  "depart",
				Brief: "a member leaves the workspace now: their person record pseudonymized, their membership ended",
				Flags: flg.Flags{
					&flg.String{Name: "member", Brief: "who: their alias here, or their identifier"},
					&flg.Switch{Name: "yes", Brief: "do it; without this it only says what it would do"},
				},
				Handler: xli.OnRun(func(ctx context.Context, self *xli.Command, next xli.Next) error {
					member := str(self, "member")
					if member == "" {
						return errors.New("--member: who, by their alias here or their identifier")
					}
					conn, err := operatorFor(ctx, self)
					if err != nil {
						return err
					}
					v, err := app.NewOperatorIdentityServiceClient(conn).Depart(ctx, app.OperatorIdentityDepartRequest_builder{
						Member: member, DryRun: !yes(self),
					}.Build())
					if err != nil {
						return err
					}
					line := fmt.Sprintf("%s (%s)", v.GetMember(), idSaid(v.GetId()))
					if v.GetDryRun() {
						if v.GetCloses() {
							line += "; nobody would be left, so the workspace would close"
						}
						self.Printf("%s\n", line)
						self.Printf("(nothing was done: `--yes` does it. It cannot be undone)\n")
						return next(ctx)
					}
					line = "left: " + line
					if v.GetCloses() {
						line += "; nobody was left, so the workspace is closed"
					}
					if v.GetHeld() > 0 {
						line += fmt.Sprintf("; %d trail row(s) kept by a legal hold", v.GetHeld())
					}
					self.Printf("%s\n", line)
					if v.GetForgot() {
						self.Printf("forgot %s at the roster in this process\n", idSaid(v.GetId()))
					} else if !v.GetEmbedded() {
						self.Printf("their holder is roster's: `roster holder erase`, then `roster forget`, there\n")
					}
					return next(ctx)
				}),
			},
			{
				Name:  "reconcile",
				Brief: "ask roster about every member, and mark those it has nobody by as leaving",
				Flags: flg.Flags{&flg.Switch{Name: "yes", Brief: "do it; without this it only says what it would do"}},
				Handler: xli.OnRun(func(ctx context.Context, self *xli.Command, next xli.Next) error {
					conn, err := operatorFor(ctx, self)
					if err != nil {
						return err
					}
					v, err := app.NewOperatorIdentityServiceClient(conn).Reconcile(ctx, app.OperatorIdentityReconcileRequest_builder{DryRun: !yes(self)}.Build())
					if err != nil {
						return err
					}
					said := func(m *app.OperatorIdentityMember) string {
						if m.GetMember() == "" {
							return idSaid(m.GetId())
						}
						return fmt.Sprintf("%s (%s)", m.GetMember(), idSaid(m.GetId()))
					}
					self.Printf("%d member(s) here; roster has %d of them\n", v.GetMembers(), v.GetFound())
					for _, m := range v.GetGone() {
						self.Printf("gone at roster, not yet leaving here: %s\n", said(m))
					}
					for _, m := range v.GetBack() {
						self.Printf("leaving here, and roster has them again: %s\n", said(m))
					}
					if len(v.GetGone()) == 0 && len(v.GetBack()) == 0 {
						self.Printf("nothing to do\n")
						return next(ctx)
					}
					if v.GetDryRun() {
						self.Printf("(nothing was done: `--yes` does it. The ones gone start leaving now, and leave after `app.members.forget_after`)\n")
						return next(ctx)
					}
					self.Printf("marked %d as leaving, and %d as not\n", len(v.GetGone()), len(v.GetBack()))
					return next(ctx)
				}),
			},
		},
		Handler: xli.RequireSubcommand(),
	}
}

// newCmdHolder is a member on the roster in this process (design 9.10, D85):
// `holder add` and `holder password`.
func newCmdHolder() *xli.Command {
	return &xli.Command{
		Name:  "holder",
		Brief: "a member, on the roster in this process",
		Commands: xli.Commands{
			{
				Name:  "add",
				Brief: "make somebody a member, making them at roster with a password shown once when they are new there",
				Flags: flg.Flags{
					&flg.String{Name: "login", Brief: "what they sign in with"},
					&flg.String{Name: "name", Brief: "their name; the login by default"},
					&flg.String{Name: "email", Brief: "their e-mail, kept on the person record"},
					&flg.String{Name: "role", Brief: "the role Loft ships they are bound at the roster in this process: owner, admin, manager, member or auditor; member by default"},
					&flg.String{Name: "password", Brief: "a password for somebody new at roster; empty makes one up"},
				},
				Handler: xli.OnRun(func(ctx context.Context, self *xli.Command, next xli.Next) error {
					if str(self, "login") == "" {
						return errors.New("--login: what they sign in with")
					}
					conn, err := operatorFor(ctx, self)
					if err != nil {
						return err
					}
					v, err := app.NewOperatorHolderServiceClient(conn).Add(ctx, app.OperatorHolderAddRequest_builder{
						Login:    str(self, "login"),
						Name:     str(self, "name"),
						Email:    str(self, "email"),
						Role:     str(self, "role"),
						Password: str(self, "password"),
					}.Build())
					if err != nil {
						return err
					}
					self.Printf("member   %s (%s), %s\n", v.GetMember(), idSaid(v.GetId()), v.GetRole())
					if v.GetPassword() != "" {
						self.Printf("password %s\n", v.GetPassword())
					}
					return next(ctx)
				}),
			},
			{
				Name:  "password",
				Brief: "give a member a new password, shown once, and end everywhere they are signed in",
				Flags: flg.Flags{
					&flg.String{Name: "login", Brief: "what they sign in with"},
					&flg.String{Name: "password", Brief: "the new password; empty makes one up"},
				},
				Handler: xli.OnRun(func(ctx context.Context, self *xli.Command, next xli.Next) error {
					if str(self, "login") == "" {
						return errors.New("--login: whose password")
					}
					conn, err := operatorFor(ctx, self)
					if err != nil {
						return err
					}
					v, err := app.NewOperatorHolderServiceClient(conn).Password(ctx, app.OperatorHolderPasswordRequest_builder{
						Login: str(self, "login"), Password: str(self, "password"),
					}.Build())
					if err != nil {
						return err
					}
					self.Printf("member   %s (%s)\n", v.GetLogin(), idSaid(v.GetId()))
					if v.GetPassword() != "" {
						self.Printf("password %s\n", v.GetPassword())
					}
					return next(ctx)
				}),
			},
		},
		Handler: xli.RequireSubcommand(),
	}
}
