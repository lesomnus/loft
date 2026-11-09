package cli

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/lesomnus/payday/pdid"
	"github.com/lesomnus/xli"
	"github.com/lesomnus/xli/flg"
	"github.com/lesomnus/z"

	app "github.com/lesomnus/loft"
	"github.com/lesomnus/loft/server/domain"
)

// identityWritten is `loft identity`, who people are, which is roster's
// (design 9.10), and `loft holder add|password`, an account and its
// memberships on the roster in this process (D27): the operator's. The
// generated `holder add` calls nothing any role or the entry point serves,
// and the operator's own is its place.
func identityWritten() []written {
	out := group(newCmdIdentity(), map[string][]string{
		"migrate":   {app.OperatorIdentityService_Migrate_FullMethodName},
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
		Brief: "who people are: roster, in this process or of its own",
		Commands: xli.Commands{
			{
				Name:  "migrate",
				Brief: "give an account to each member who names none, with the member's identifier: those of a workspace restored from another deployment",
				Flags: flg.Flags{
					&flg.String{Name: "password", Brief: "give everybody this one, for a deployment to try things on; empty makes one up for each"},
				},
				Handler: xli.OnRun(func(ctx context.Context, self *xli.Command, next xli.Next) error {
					conn, err := operatorFor(ctx, self)
					if err != nil {
						return err
					}
					stream, err := app.NewOperatorIdentityServiceClient(conn).Migrate(ctx, app.OperatorIdentityMigrateRequest_builder{Password: str(self, "password")}.Build())
					if err != nil {
						return err
					}
					var done *app.OperatorIdentityMigrated
					told := false
					if err := each(stream, self, func(m *app.OperatorIdentityMigrateResponse) string {
						if v := m.GetAdopted(); m.HasAdopted() {
							switch {
							case v.GetMake() != "":
								if !told {
									self.Printf("# roster is not in this process: its operator makes these accounts, with the identifiers they have here,\n")
									self.Printf("# and gives each a way in there; then run this again to link them\n")
									told = true
								}
								self.Printf("%s\n", v.GetMake())
							case v.GetPassword() != "":
								self.Printf("@%s/%s  login %s  password %s\n", v.GetWorkspace(), v.GetMember(), v.GetLogin(), v.GetPassword())
							case v.GetLogin() != "":
								self.Printf("@%s/%s  login %s  (an account already)\n", v.GetWorkspace(), v.GetMember(), v.GetLogin())
							default:
								self.Printf("@%s/%s  linked\n", v.GetWorkspace(), v.GetMember())
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
					case done.GetMade() == 0 && done.GetLinked() == 0:
						self.Printf("nobody to bring in: every holder here is an account's\n")
					case done.GetMade() == 0:
						self.Printf("\n%d holder(s) linked to the accounts roster had for them\n", done.GetLinked())
					case done.GetGiven():
						self.Printf("\n%d account(s), every one with the password given\n", done.GetMade())
					default:
						self.Printf("\n%d password(s), shown once\n", done.GetMade())
					}
					return next(ctx)
				}),
			},
			{
				Name:  "depart",
				Brief: "an account leaves every workspace it is in, now: its person records pseudonymized, its memberships ended",
				Flags: flg.Flags{
					&flg.String{Name: "member", Brief: "the account, by one of its memberships: @workspace/alias"},
					&flg.String{Name: "account", Brief: "the account, by its identifier at roster"},
					&flg.Switch{Name: "yes", Brief: "do it; without this it only says what it would do"},
				},
				Handler: xli.OnRun(func(ctx context.Context, self *xli.Command, next xli.Next) error {
					member, account := str(self, "member"), str(self, "account")
					req := app.OperatorIdentityDepartRequest_builder{DryRun: !yes(self)}.Build()
					switch {
					case member != "" && account != "":
						return errors.New("--member or --account, not both")
					case account != "":
						id, err := pdid.Parse(account)
						if err != nil {
							return errors.New("--account: " + err.Error())
						}
						req.SetAccount(id.Bytes())
					case member != "":
						req.SetMember(member)
					default:
						return errors.New("which account: --member @workspace/alias, or --account")
					}
					conn, err := operatorFor(ctx, self)
					if err != nil {
						return err
					}
					v, err := app.NewOperatorIdentityServiceClient(conn).Depart(ctx, req)
					if err != nil {
						return err
					}
					who := idSaid(v.GetAccount())
					if len(v.GetMembers()) == 0 {
						self.Printf("%s is a member of no workspace here\n", who)
					}
					for _, m := range v.GetMembers() {
						line := fmt.Sprintf("%s: @%s, %s", m.GetWorkspace(), m.GetMember(), m.GetRole())
						if v.GetDryRun() {
							switch {
							case m.GetOwner() != "":
								line += "; its last owner, so it would be @" + m.GetOwner() + "'s"
							case m.GetOwnerless():
								line += "; its last owner, and nobody is there to hand it to"
							}
							if m.GetCloses() {
								line += "; nobody would be left in it, so it would close"
							}
						}
						self.Printf("%s\n", line)
					}
					if v.GetDryRun() {
						self.Printf("(nothing was done: `--yes` does it. It cannot be undone)\n")
						return next(ctx)
					}
					for _, l := range v.GetLeft() {
						line := "left " + l.GetWorkspace()
						switch {
						case l.GetOwner() != "":
							line += "; its last owner, so it is @" + l.GetOwner() + "'s now"
						case l.GetOwnerless():
							line += "; its last owner, and nobody was there to hand it to"
						}
						if l.GetClosed() {
							line += "; nobody was left in it, so it is closed"
						}
						if l.GetHeld() > 0 {
							line += fmt.Sprintf("; %d trail row(s) kept by a legal hold", l.GetHeld())
						}
						self.Printf("%s\n", line)
					}
					if v.GetForgot() {
						self.Printf("forgot %s at the roster in this process\n", who)
					} else if !v.GetEmbedded() {
						self.Printf("the account itself is roster's: `roster holder erase`, then `roster forget`, there\n")
					}
					return next(ctx)
				}),
			},
			{
				Name:  "reconcile",
				Brief: "ask roster about every account with a membership here, and mark those it has nobody by as leaving",
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
					self.Printf("%d account(s) with a membership here; roster has %d of them\n", v.GetAccounts(), v.GetFound())
					for _, a := range v.GetGone() {
						self.Printf("gone at roster, not yet leaving here: %s (%s)\n", idSaid(a.GetAccount()), strings.Join(a.GetMembers(), ", "))
					}
					for _, a := range v.GetBack() {
						self.Printf("leaving here, and roster has it again: %s (%s)\n", idSaid(a.GetAccount()), strings.Join(a.GetMembers(), ", "))
					}
					if len(v.GetGone()) == 0 && len(v.GetBack()) == 0 {
						self.Printf("nothing to do\n")
						return next(ctx)
					}
					if v.GetDryRun() {
						self.Printf("(nothing was done: `--yes` does it. The ones gone start leaving now, and leave after `app.accounts.forget_after`)\n")
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

// newCmdHolder is an account and its memberships, on the roster in this
// process (design 9.10, D27): `holder add` and `holder password`.
func newCmdHolder() *xli.Command {
	return &xli.Command{
		Name:  "holder",
		Brief: "an account and its memberships, on the roster in this process",
		Commands: xli.Commands{
			{
				Name:  "add",
				Brief: "put an account in a workspace, making the account with a password shown once when it is new",
				Flags: flg.Flags{
					&flg.String{Name: "tenant", Brief: "the workspace, by address"},
					&flg.String{Name: "login", Brief: "the account's login"},
					&flg.String{Name: "name", Brief: "their name; the login by default"},
					&flg.String{Name: "email", Brief: "their e-mail, kept on the person record"},
					&flg.String{Name: "role", Brief: "what they may do there: " + strings.Join(domain.Roles, ", ") + "; member by default"},
					&flg.String{Name: "password", Brief: "a new account's password; empty makes one up"},
				},
				Handler: xli.OnRun(func(ctx context.Context, self *xli.Command, next xli.Next) error {
					ws := strings.TrimPrefix(str(self, "tenant"), "@")
					if ws == "" {
						return errors.New("--tenant: which workspace, by address")
					}
					if str(self, "login") == "" {
						return errors.New("--login: what they sign in with")
					}
					conn, err := operatorFor(ctx, self)
					if err != nil {
						return err
					}
					v, err := app.NewOperatorHolderServiceClient(conn).Add(ctx, app.OperatorHolderAddRequest_builder{
						Tenant:   app.OperatorTenantRef_builder{Alias: z.Ptr(ws)}.Build(),
						Login:    str(self, "login"),
						Name:     str(self, "name"),
						Email:    str(self, "email"),
						Role:     str(self, "role"),
						Password: str(self, "password"),
					}.Build())
					if err != nil {
						return err
					}
					self.Printf("holder   @%s/%s (account %s), %s\n", v.GetWorkspace(), v.GetMember(), idSaid(v.GetAccount()), v.GetRole())
					if v.GetPassword() != "" {
						self.Printf("password %s\n", v.GetPassword())
					}
					return next(ctx)
				}),
			},
			{
				Name:  "password",
				Brief: "give an account a new password, shown once, and end everywhere it is signed in",
				Flags: flg.Flags{
					&flg.String{Name: "login", Brief: "the account's login"},
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
					self.Printf("account  %s (%s)\n", v.GetLogin(), idSaid(v.GetAccount()))
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
