package cli

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"uuid"

	"github.com/lesomnus/payday/pdid"
	"github.com/lesomnus/xli"
	"github.com/lesomnus/xli/flg"

	"github.com/lesomnus/rove/cmd"
	"github.com/lesomnus/rove/internal/ent"
	"github.com/lesomnus/rove/internal/ent/holder"
	enttenant "github.com/lesomnus/rove/internal/ent/tenant"
)

// newCmdIdentityDepart is `rove identity depart`: an account leaving every
// workspace it is in, now, rather than when roster's grace is over -- for
// somebody who asked to be forgotten at once (design 9.10).
func newCmdIdentityDepart(c *cmd.Config) *xli.Command {
	return &xli.Command{
		Name:  "depart",
		Brief: "an account leaves every workspace it is in, now: its person records pseudonymized, its memberships ended",
		Flags: flg.Flags{
			&flg.String{Name: "member", Brief: "the account, by one of its memberships: @workspace/alias"},
			&flg.String{Name: "account", Brief: "the account, by its identifier at roster"},
			&flg.Switch{Name: "yes", Brief: "do it; without this it only says what it would do"},
		},
		Handler: xli.OnRun(func(ctx context.Context, self *xli.Command, next xli.Next) error {
			yes, _ := flg.Find[bool](self, "yes")
			return depart(ctx, self, c, flag(self, "member"), flag(self, "account"), yes)
		}),
	}
}

func depart(ctx context.Context, self *xli.Command, c *cmd.Config, member, account string, yes bool) error {
	s, err := cmd.Build(ctx, *c)
	if err != nil {
		return err
	}
	defer s.Close()
	if err := Migrate(ctx, s); err != nil {
		return err
	}

	who, err := accountNamed(ctx, s, member, account)
	if err != nil {
		return err
	}
	hs, err := membershipsOf(ctx, s, who)
	if err != nil {
		return err
	}
	if len(hs) == 0 {
		self.Printf("%s is a member of no workspace here\n", who)
	}
	for _, h := range hs {
		self.Printf("%s: @%s, %s\n", workspaceAlias(ctx, s, h.TenantId), h.Alias, h.Role)
	}
	if !yes {
		self.Printf("(nothing was done: `--yes` does it. It cannot be undone)\n")
		return nil
	}

	gone, err := s.Lifecycle.Depart(ctx, who)
	for _, v := range gone {
		line := fmt.Sprintf("left %s", workspaceAlias(ctx, s, v.Workspace.Uuid()))
		switch {
		case !v.Owner.IsZero():
			line += fmt.Sprintf("; its last owner, so it is %s's now", aliasOf(ctx, s, v.Owner))
		case v.Ownerless:
			line += "; its last owner, and nobody was there to hand it to"
		}
		if v.Held > 0 {
			line += fmt.Sprintf("; %d trail row(s) kept by a legal hold", v.Held)
		}
		self.Printf("%s\n", line)
	}
	if err != nil {
		return err
	}

	// The account is roster's. The roster in this process is this
	// deployment's alone, so an account that left every workspace leaves it
	// too; one of its own is its operator's to erase.
	if s.Identity.Embedded() {
		if err := s.Identity.ForgetPerson(ctx, who); err != nil {
			return err
		}
		self.Printf("forgot %s at the roster in this process\n", who)
	} else {
		self.Printf("the account itself is roster's: `roster holder erase`, then `roster forget`, there\n")
	}
	return nil
}

// newCmdIdentityReconcile is `rove identity reconcile`: what roster says of
// every account with a membership here, against what this deployment holds.
//
// The stream from roster replays nothing (`identity.Store.Follow`), so an
// account erased while this was not listening was never marked as leaving and
// would never leave. This asks about every one, and marks those roster has
// nobody by -- from now, so that they wait the whole grace like anybody else
// -- and unmarks those it has again. A served deployment does the same by
// itself every `app.accounts.reconcile` (`lifecycle.Lifecycle.Reconcile`);
// this is it asked for now, and told first.
func newCmdIdentityReconcile(c *cmd.Config) *xli.Command {
	return &xli.Command{
		Name:  "reconcile",
		Brief: "ask roster about every account with a membership here, and mark those it has nobody by as leaving",
		Flags: flg.Flags{
			&flg.Switch{Name: "yes", Brief: "do it; without this it only says what it would do"},
		},
		Handler: xli.OnRun(func(ctx context.Context, self *xli.Command, next xli.Next) error {
			yes, _ := flg.Find[bool](self, "yes")
			return reconcile(ctx, self, c, yes)
		}),
	}
}

func reconcile(ctx context.Context, self *xli.Command, c *cmd.Config, yes bool) error {
	s, err := cmd.Build(ctx, *c)
	if err != nil {
		return err
	}
	defer s.Close()
	if err := Migrate(ctx, s); err != nil {
		return err
	}

	v, err := s.Lifecycle.Reconcile(ctx, yes)
	if v == nil {
		return err
	}
	self.Printf("%d account(s) with a membership here; roster has %d of them\n", len(v.Accounts), v.Found)
	for _, a := range v.Gone {
		self.Printf("gone at roster, not yet leaving here: %s (%s)\n", a, membersLine(ctx, s, v.Members[a]))
	}
	for _, st := range v.Back {
		self.Printf("leaving here, and roster has it again: %s (%s)\n", st.Holder, membersLine(ctx, s, v.Members[st.Holder]))
	}
	if len(v.Gone) == 0 && len(v.Back) == 0 {
		self.Printf("nothing to do\n")
		return nil
	}
	if !yes {
		self.Printf("(nothing was done: `--yes` does it. The ones gone start leaving now, and leave after `app.accounts.forget_after`)\n")
		return nil
	}
	if err != nil {
		return err
	}
	self.Printf("marked %d as leaving, and %d as not\n", len(v.Gone), len(v.Back))
	return nil
}

// accountNamed is the account a command names: by its identifier, or by one
// of its memberships as @workspace/alias.
func accountNamed(ctx context.Context, s *cmd.Server, member, account string) (pdid.Id, error) {
	switch {
	case member != "" && account != "":
		return pdid.Nil, errors.New("--member or --account, not both")
	case account != "":
		id, err := pdid.Parse(account)
		if err != nil {
			return pdid.Nil, fmt.Errorf("--account: %w", err)
		}
		return id, nil
	case member != "":
		workspace, alias, ok := strings.Cut(strings.TrimPrefix(member, "@"), "/")
		if !ok || !strings.HasPrefix(member, "@") || workspace == "" || alias == "" {
			return pdid.Nil, fmt.Errorf("--member: %q is not @workspace/alias", member)
		}
		h, err := s.Ent.Holder.Query().Where(
			holder.Alias(alias),
			holder.DateErasedIsNil(),
			holder.HasTenantWith(enttenant.Alias(workspace)),
		).Only(ctx)
		if err != nil {
			if ent.IsNotFound(err) {
				return pdid.Nil, fmt.Errorf("--member: nobody is %s", member)
			}
			return pdid.Nil, err
		}
		if h.Account == nil {
			return pdid.Nil, fmt.Errorf("--member: %s names no account; `rove identity migrate` first", member)
		}
		return pdid.Id(*h.Account), nil
	default:
		return pdid.Nil, errors.New("which account: --member @workspace/alias, or --account")
	}
}

func membershipsOf(ctx context.Context, s *cmd.Server, account pdid.Id) ([]*ent.Holder, error) {
	return s.Ent.Holder.Query().
		Where(holder.Account(account.Uuid()), holder.DateErasedIsNil()).
		Order(ent.Asc(holder.FieldDateCreated)).
		All(ctx)
}

func workspaceAlias(ctx context.Context, s *cmd.Server, id uuid.UUID) string {
	if t, err := s.Ent.Tenant.Get(ctx, id); err == nil {
		return "@" + t.Alias
	}
	return pdid.Id(id).String()
}

func aliasOf(ctx context.Context, s *cmd.Server, member pdid.Id) string {
	if h, err := s.Ent.Holder.Get(ctx, member.Uuid()); err == nil {
		return "@" + h.Alias
	}
	return member.String()
}

func membersLine(ctx context.Context, s *cmd.Server, hs []*ent.Holder) string {
	vs := make([]string, 0, len(hs))
	for _, h := range hs {
		vs = append(vs, workspaceAlias(ctx, s, h.TenantId)+"/"+h.Alias)
	}
	return strings.Join(vs, ", ")
}
