package cli

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/lesomnus/payday/pdid"
	"github.com/lesomnus/xli"
	"github.com/lesomnus/xli/flg"
	"github.com/lesomnus/z"

	app "github.com/lesomnus/rove"
	"github.com/lesomnus/rove/cmd"
	"github.com/lesomnus/rove/internal/ent/holder"
	"github.com/lesomnus/rove/internal/ent/session"
	"github.com/lesomnus/rove/internal/identity"
	"github.com/lesomnus/rove/server/domain"
)

// NewCmdHolder is `rove holder`: an account and its memberships, from a shell,
// on the roster in this process (design 9.10, D27).
//
// What the people screen does, for the operator who has no screen to do it
// from -- the owner who lost their password above all. A deployment whose
// roster is one of its own refuses both: its accounts are made and given
// passwords there.
func NewCmdHolder(c *cmd.Config) *xli.Command {
	return &xli.Command{
		Name:  "holder",
		Brief: "an account and its memberships, on the roster in this process",

		Commands: xli.Commands{
			{
				Name:  "add",
				Brief: "put an account in a workspace, making the account with a password shown once when it is new",
				Flags: flg.Flags{
					&flg.String{Name: "tenant", Brief: "the workspace, by alias"},
					&flg.String{Name: "login", Brief: "the account's login"},
					&flg.String{Name: "name", Brief: "their name; the login by default"},
					&flg.String{Name: "email", Brief: "their e-mail, kept on the person record"},
					&flg.String{Name: "role", Brief: "what they may do there: " + strings.Join(domain.Roles, ", ") + "; member by default"},
					&flg.String{Name: "password", Brief: "a new account's password; empty makes one up"},
				},
				Handler: xli.OnRun(func(ctx context.Context, self *xli.Command, next xli.Next) error {
					return addHolder(ctx, self, c)
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
					return resetHolder(ctx, self, c)
				}),
			},
		},
	}
}

func flag(self *xli.Command, name string) string {
	v, _ := flg.Find[string](self, name)
	return strings.TrimSpace(v)
}

// embedded is the server, refused when its roster is not in this process.
func embedded(ctx context.Context, c *cmd.Config) (*cmd.Server, error) {
	s, err := cmd.Build(ctx, *c)
	if err != nil {
		return nil, err
	}
	if !s.Identity.Embedded() {
		s.Close()
		return nil, errors.New("auth.roster names a roster of its own: accounts are made and given passwords there")
	}
	return s, nil
}

func addHolder(ctx context.Context, self *xli.Command, c *cmd.Config) error {
	login := flag(self, "login")
	if login == "" {
		return errors.New("--login: what they sign in with")
	}
	name := flag(self, "name")
	if name == "" {
		name = login
	}
	role := flag(self, "role")
	if role == "" {
		role = "member"
	}
	if !slices.Contains(domain.Roles, role) {
		return fmt.Errorf("--role: one of %s", strings.Join(domain.Roles, ", "))
	}

	s, err := embedded(ctx, c)
	if err != nil {
		return err
	}
	defer s.Close()

	t, err := tenantNamed(ctx, s.Ent, self)
	if err != nil {
		return err
	}

	// The account: the one by that login, or a new one.
	made, fresh := "", false
	p, err := s.Identity.Lookup(ctx, login)
	switch {
	case err == nil:
		if flag(self, "password") != "" {
			return fmt.Errorf("--password: %s is an account already, with a password of its own; `rove holder password` changes it", login)
		}
	case errors.Is(err, identity.ErrNoPerson):
		p, err = s.Identity.AddPerson(ctx, login, name, pdid.Nil)
		if err != nil {
			return fmt.Errorf("roster: %w", err)
		}
		fresh = true
		if pw := flag(self, "password"); pw != "" {
			err = s.Identity.SetPassword(ctx, p.Id, pw)
		} else {
			made, err = s.Identity.IssuePassword(ctx, p.Id)
		}
		if err != nil {
			s.Identity.ForgetPerson(ctx, p.Id)
			return fmt.Errorf("roster: %w", err)
		}
	default:
		return fmt.Errorf("roster: %w", err)
	}
	undo := func() {
		if fresh {
			s.Identity.ForgetPerson(ctx, p.Id)
		}
	}

	if in, err := s.Ent.Holder.Query().Where(holder.TenantId(t.Id), holder.Account(p.Id.Uuid()), holder.DateErasedIsNil()).Exist(ctx); err != nil {
		undo()
		return err
	} else if in {
		return fmt.Errorf("--login: %s is in @%s already", login, t.Alias)
	}
	if taken, err := s.Ent.Holder.Query().Where(holder.TenantId(t.Id), holder.Alias(p.Alias), holder.DateErasedIsNil()).Exist(ctx); err != nil {
		undo()
		return err
	} else if taken {
		undo()
		return fmt.Errorf("--login: @%s/%s is somebody else already", t.Alias, p.Alias)
	}

	tref := app.TenantRef_builder{Id: pdid.Id(t.Id).Bytes()}.Build()
	h, err := s.Base.Holder().Add(ctx, app.HolderAddRequest_builder{
		Tenant:  tref,
		Alias:   p.Alias,
		Name:    name,
		Role:    z.Ptr(role),
		Account: p.Id.Bytes(),
	}.Build())
	if err == nil {
		_, err = s.Base.Party().Add(ctx, app.PartyAddRequest_builder{
			Tenant: tref,
			Name:   name,
			Kind:   "person",
			Email:  flag(self, "email"),
			Holder: app.HolderRef_builder{Id: h.GetId()}.Build(),
		}.Build())
	}
	if err != nil {
		undo()
		return err
	}

	self.Printf("holder   @%s/%s (account %s), %s\n", t.Alias, p.Alias, p.Id, role)
	if made != "" {
		self.Printf("password %s\n", made)
	}
	return nil
}

func resetHolder(ctx context.Context, self *xli.Command, c *cmd.Config) error {
	login := flag(self, "login")
	if login == "" {
		return errors.New("--login: whose password")
	}

	s, err := embedded(ctx, c)
	if err != nil {
		return err
	}
	defer s.Close()

	p, err := s.Identity.Lookup(ctx, login)
	if errors.Is(err, identity.ErrNoPerson) {
		return fmt.Errorf("--login: there is no account %s", login)
	}
	if err != nil {
		return fmt.Errorf("roster: %w", err)
	}

	made := ""
	if pw := flag(self, "password"); pw != "" {
		err = s.Identity.SetPassword(ctx, p.Id, pw)
	} else {
		made, err = s.Identity.IssuePassword(ctx, p.Id)
	}
	if err != nil {
		return fmt.Errorf("roster: %w", err)
	}
	// Wherever they were signed in -- here, in every workspace, and at
	// roster -- ends with it.
	if err := s.Identity.Invalidate(ctx, p.Id); err != nil {
		return fmt.Errorf("roster: %w", err)
	}
	if _, err := s.Ent.Session.Delete().Where(session.Account(p.Id.Uuid())).Exec(ctx); err != nil {
		return err
	}

	self.Printf("account  %s (%s)\n", p.Alias, p.Id)
	if made != "" {
		self.Printf("password %s\n", made)
	}
	return nil
}
