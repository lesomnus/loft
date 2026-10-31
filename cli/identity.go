package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"uuid"

	"github.com/lesomnus/payday/pdid"
	"github.com/lesomnus/xli"
	"github.com/lesomnus/xli/flg"
	"github.com/lesomnus/z"

	app "github.com/lesomnus/loft"
	"github.com/lesomnus/loft/cmd"
	"github.com/lesomnus/loft/internal/ent"
	"github.com/lesomnus/loft/internal/ent/holder"
	enttenant "github.com/lesomnus/loft/internal/ent/tenant"
	"github.com/lesomnus/loft/internal/identity"
)

// NewCmdIdentity is `loft identity`: who people are, which is roster's
// (design 9.10).
func NewCmdIdentity(c *cmd.Config) *xli.Command {
	return &xli.Command{
		Name:  "identity",
		Brief: "who people are: roster, in this process or of its own",

		Commands: xli.Commands{
			{
				Name:  "migrate",
				Brief: "bring the tenants and people of a deployment from before roster into it, with their identifiers",
				Flags: flg.Flags{
					&flg.String{Name: "password", Brief: "give everybody this one, for a deployment to try things on; empty makes one up for each"},
				},
				Handler: xli.OnRun(func(ctx context.Context, self *xli.Command, next xli.Next) error {
					return migrateIdentity(ctx, self, c)
				}),
			},
			newCmdIdentityDepart(c),
			newCmdIdentityReconcile(c),
		},
	}
}

// migrateIdentity is `loft identity migrate`, the upgrade of a deployment
// whose logins are holders that name no account (D27): made before roster held
// people, or before holders named accounts. Nobody of them can sign in.
//
// On the roster in this process it makes the directory when there is none and
// writes every such holder into it as an account with the identifier the
// holder has, so that what the trail says each did is still theirs; it gives
// each a new password and prints it once, and links the holder to it. A login
// another workspace has too gets that workspace's alias beside it. Somebody
// roster has by that identifier already is linked as they are. Run again it
// makes nothing. A roster of its own is its operator's to write to, so for one
// this prints what to make there, with the same identifiers, and links what
// was made.
//
// Either way it then drops the table the passwords from before were kept in,
// which nothing reads any more.
func migrateIdentity(ctx context.Context, self *xli.Command, c *cmd.Config) error {
	s, err := cmd.Build(ctx, *c)
	if err != nil {
		return err
	}
	defer s.Close()
	if err := Migrate(ctx, s); err != nil {
		return err
	}

	tenants, err := s.Ent.Tenant.Query().Order(ent.Asc(enttenant.FieldAlias)).All(ctx)
	if err != nil {
		return err
	}
	if s.Identity.Embedded() {
		err = adoptAll(ctx, self, s, tenants, flag(self, "password"))
	} else {
		err = describeAll(ctx, self, s, tenants)
	}
	if err != nil {
		return err
	}

	dropped, err := dropCredentials(ctx, s)
	if err != nil {
		return err
	}
	if dropped {
		self.Printf("dropped `credential`, the passwords from before roster\n")
	}
	return nil
}

// unlinked is everybody who signs in to a workspace here and whose holder
// names no account yet, in the order they were made.
func unlinked(ctx context.Context, s *cmd.Server, t *ent.Tenant) ([]*ent.Holder, error) {
	return s.Ent.Holder.Query().
		Where(holder.TenantId(t.Id), holder.DateErasedIsNil(), holder.AccountIsNil()).
		Order(ent.Asc(holder.FieldDateCreated), ent.Asc(holder.FieldId)).
		All(ctx)
}

// link makes a holder the membership of the account with its own identifier.
// Through the stack, so the trail says it happened.
func link(ctx context.Context, s *cmd.Server, h *ent.Holder) error {
	id := pdid.Id(h.Id)
	_, err := s.Base.Holder().Patch(ctx, app.HolderPatchRequest_builder{
		Ref:              app.HolderRef_builder{Id: id.Bytes()}.Build(),
		Account:          id.Bytes(),
		DateUpdatedForce: z.Ptr(true),
	}.Build())
	return err
}

// freeAlias is a login for a holder in the directory: its own alias when
// nobody there has it, and otherwise that alias with its workspace's beside it.
// A login was unique inside one workspace and the directory is every
// workspace's.
func freeAlias(ctx context.Context, s *cmd.Server, alias, workspace string, chosen map[string]bool) (string, error) {
	for i := 0; ; i++ {
		v := alias
		switch {
		case i == 1:
			v = alias + "-" + workspace
		case i > 1:
			v = fmt.Sprintf("%s-%s-%d", alias, workspace, i)
		}
		if chosen[v] || v == identity.Agent {
			continue
		}
		free, err := aliasFree(ctx, s, v)
		if err != nil {
			return "", err
		}
		if free {
			return v, nil
		}
	}
}

// aliasFree says whether nobody in the directory has a login. The
// administrator roster makes a directory with is nobody: no holder here is it,
// and it has no way to sign in, so [identity.Store.Adopt] gives its name to
// the person here who has it.
func aliasFree(ctx context.Context, s *cmd.Server, alias string) (bool, error) {
	p, err := s.Identity.Lookup(ctx, alias)
	if errors.Is(err, identity.ErrNoPerson) {
		return true, nil
	}
	if err != nil || alias != identity.RosterAdministrator {
		return false, err
	}
	used, err := s.Ent.Holder.Query().Where(holder.Or(holder.Account(p.Id.Uuid()), holder.Id(p.Id.Uuid()))).Exist(ctx)
	return !used, err
}

func adoptAll(ctx context.Context, self *xli.Command, s *cmd.Server, tenants []*ent.Tenant, password string) error {
	if _, err := s.Identity.EnsureDirectory(ctx); err != nil {
		return fmt.Errorf("roster: %w", err)
	}

	made, linked := 0, 0
	chosen := map[string]bool{}
	for _, t := range tenants {
		hs, err := unlinked(ctx, s, t)
		if err != nil {
			return err
		}
		people := []identity.Adoptee{}
		logins := map[uuid.UUID]string{}
		for _, h := range hs {
			// Somebody roster has by this identifier already -- made for this
			// deployment before holders named accounts -- is that account.
			p, err := s.Identity.ById(ctx, pdid.Id(h.Id).String())
			if err == nil {
				logins[h.Id] = p.Alias
				continue
			}
			if !errors.Is(err, identity.ErrNoPerson) {
				return fmt.Errorf("roster: %w", err)
			}
			alias, err := freeAlias(ctx, s, h.Alias, t.Alias, chosen)
			if err != nil {
				return fmt.Errorf("roster: %w", err)
			}
			chosen[alias] = true
			logins[h.Id] = alias
			people = append(people, identity.Adoptee{Id: pdid.Id(h.Id), Alias: alias, Name: h.Name})
		}
		passwords, err := s.Identity.Adopt(ctx, people, password)
		if err != nil {
			return err
		}

		for _, h := range hs {
			if err := link(ctx, s, h); err != nil {
				return err
			}
			login := logins[h.Id]
			if pw, ok := passwords[login]; ok {
				self.Printf("@%s/%s  login %s  password %s\n", t.Alias, h.Alias, login, pw)
				made++
			} else {
				self.Printf("@%s/%s  login %s  (an account already)\n", t.Alias, h.Alias, login)
				linked++
			}
		}
	}

	switch {
	case made == 0 && linked == 0:
		self.Printf("nobody to bring in: every holder here is an account's\n")
	case made == 0:
		self.Printf("\n%d holder(s) linked to the accounts roster had for them\n", linked)
	case password != "":
		self.Printf("\n%d account(s), every one with the password given; the ones from before roster are not brought along\n", made)
	default:
		self.Printf("\n%d password(s), shown once; the ones from before roster are not brought along\n", made)
	}
	return nil
}

func describeAll(ctx context.Context, self *xli.Command, s *cmd.Server, tenants []*ent.Tenant) error {
	d, err := s.Identity.Directory(ctx)
	if err != nil {
		return fmt.Errorf("roster: %w", err)
	}
	todo := 0
	for _, t := range tenants {
		hs, err := unlinked(ctx, s, t)
		if err != nil {
			return err
		}
		for _, h := range hs {
			// The ones roster's operator made already, with these identifiers,
			// are linked; the rest are what to make.
			if _, err := s.Identity.ById(ctx, pdid.Id(h.Id).String()); err == nil {
				if err := link(ctx, s, h); err != nil {
					return err
				}
				self.Printf("@%s/%s  linked\n", t.Alias, h.Alias)
				continue
			} else if !errors.Is(err, identity.ErrNoPerson) {
				return fmt.Errorf("roster: %w", err)
			}
			if todo == 0 {
				self.Printf("# roster is not in this process: its operator makes these accounts in @%s, with the identifiers they have here,\n", d.Alias)
				self.Printf("# and gives each a way in there; then run this again to link them\n")
			}
			self.Printf("roster holder add @%s/%s %s\n", d.Alias, h.Alias, quoted(map[string]string{"id": pdid.Id(h.Id).String(), "name": h.Name}))
			todo++
		}
	}
	if todo == 0 {
		self.Printf("nobody to bring in: every holder here is an account's\n")
	}
	return nil
}

// quoted is a request body for a shell.
func quoted(v map[string]string) string {
	b, _ := json.Marshal(v)
	return "'" + strings.ReplaceAll(string(b), "'", `'\''`) + "'"
}

// dropCredentials takes out the table a deployment from before roster kept its
// password verifiers in. Nothing reads it, its rows point at holders a purge
// takes, and a verifier nobody reads is only something to lose.
//
// The trail has nothing of them to forget: payday kept a declared secret out
// of it before Loft had passwords.
func dropCredentials(ctx context.Context, s *cmd.Server) (bool, error) {
	if left, err := fromBeforeRoster(ctx, s); err != nil || !left {
		return false, err
	}
	if _, err := s.Db.ExecContext(ctx, "DROP TABLE credential"); err != nil {
		return false, err
	}
	return true, nil
}

// fromBeforeRoster says whether this deployment still has the table it kept
// passwords in before roster held them: one `loft identity migrate` has not
// brought in, whose people cannot sign in.
func fromBeforeRoster(ctx context.Context, s *cmd.Server) (bool, error) {
	q := "SELECT count(*) FROM sqlite_master WHERE type = 'table' AND name = 'credential'"
	if s.Dialect == "postgres" {
		q = "SELECT count(*) FROM information_schema.tables WHERE table_schema = current_schema() AND table_name = 'credential'"
	}
	n := 0
	if err := s.Db.QueryRowContext(ctx, q).Scan(&n); err != nil {
		return false, err
	}
	return n > 0, nil
}
