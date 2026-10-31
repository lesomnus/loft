package cli

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/lesomnus/xli"
	"github.com/lesomnus/xli/flg"
	"github.com/lesomnus/z"

	"github.com/lesomnus/payday/frame"
	"github.com/lesomnus/payday/pdid"
	"github.com/lesomnus/payday/trail"

	app "github.com/lesomnus/loft"
	"github.com/lesomnus/loft/cmd"
	enttenant "github.com/lesomnus/loft/internal/ent/tenant"
	"github.com/lesomnus/loft/internal/identity"
	"github.com/lesomnus/loft/server/tenancy"
)

// NewCmdInit is `loft init`: a workspace, the account that owns it, and the
// types an asset register starts with.
//
// The account is in the directory at the roster in this process, which is
// where it signs in, and `init` makes the directory too when there is none
// (design 9.10, D27). An account there already keeps its password and is
// made the owner of another workspace. The workspace is Loft's own, and so is
// its identifier. A deployment whose roster is one of its own makes no
// accounts: they are roster's.
//
// It exists because there is nowhere else it could happen. A tenant is not put
// up from inside one, so the first row of a deployment cannot arrive over the
// API. What puts it there is [cmd.Server.Base], which is not a privilege
// anybody holds: it is a server instance this process was handed, reachable
// from this command and from nowhere a request can get to.
//
// Running it twice is an error rather than a no-op, because an alias is unique
// and the database says so. That is the right answer -- an `init` that quietly
// did nothing is one somebody runs against the wrong deployment and believes.
//
// A configuration that names the trail an archive and no key to sign its
// account with -- which `serve` and the trail's commands refuse (payday#115)
// -- is given a key of its own beside the archive, and `init` says where. That
// is for a deployment being tried: there is no key everybody knows to fall
// back on, a key that signs is never committed (`.gitignore`), and a
// deployment's own is made apart and mounted as a secret, as `deploy/compose`
// and `deploy/k8s` mount theirs.
func NewCmdInit(c *cmd.Config) *xli.Command {
	return &xli.Command{
		Name:  "init",
		Brief: "put up the first tenant and its owner",

		Flags: flg.Flags{
			&flg.String{Name: "tenant", Brief: "the workspace's alias, in its address: /@<alias>"},
			&flg.String{Name: "name", Brief: "the workspace's name"},
			&flg.String{Name: "login", Brief: "the owner's login: an account in the directory, made when it is not there"},
			&flg.String{Name: "person", Brief: "the owner's name"},
			&flg.String{Name: "email", Brief: "the owner's e-mail, kept on their person record"},
			&flg.String{Name: "password", Brief: "the owner's password; empty makes one up"},
			&flg.Switch{Name: "demo", Brief: "also fill it with teams, people, spaces and assets to try things on"},
		},

		Handler: xli.OnRun(func(ctx context.Context, self *xli.Command, next xli.Next) error {
			get := func(name, otherwise string) string {
				v, ok := flg.Find[string](self, name)
				if !ok || strings.TrimSpace(v) == "" {
					return otherwise
				}
				return strings.TrimSpace(v)
			}
			alias := get("tenant", "loft")
			name := get("name", alias)
			login := get("login", "admin")
			person := get("person", "관리자")
			email := get("email", "")
			pw := get("password", "")
			demo, _ := flg.Find[bool](self, "demo")

			// The archive's key, before anything is built that reads it: this
			// run signs with the key it writes, and the next one with whatever
			// the configuration then names -- so it is said, and not kept.
			if c.Audit.Archive != "" && c.Audit.Checkpoints.Key == "" {
				path, err := keyFor(c.Audit.Archive)
				if err != nil {
					return err
				}

				c.Audit.Checkpoints.Key = path
				self.Printf("the trail's archive is signed with a key of this deployment's own, written to %s\n", path)
				self.Printf("name it as audit.checkpoints.key, or LOFT_AUDIT_CHECKPOINTS_KEY, before serving\n\n")
			}

			s, err := cmd.Build(ctx, *c)
			if err != nil {
				return err
			}
			defer s.Close()
			if !s.Identity.Embedded() {
				return errors.New("auth.roster names a roster of its own, and accounts are made there; a workspace is made by somebody signed in")
			}

			// The schema, so that a fresh database is one this can run against.
			if err := Migrate(ctx, s); err != nil {
				return err
			}

			// Here first, since a workspace already here is the error, and
			// roster would otherwise keep an account made for it.
			if here, err := s.Ent.Tenant.Query().Where(enttenant.Alias(alias)).Exist(ctx); err != nil {
				return err
			} else if here {
				return fmt.Errorf("workspace %q is here already", alias)
			}

			// The account, in the directory at roster, which is where it
			// signs in.
			p, made, err := ownerAccount(ctx, s, login, person)
			if err != nil {
				return fmt.Errorf("roster: %w", err)
			}
			if pw != "" {
				if made == "" {
					return fmt.Errorf("--password: %s is an account already, with a password of its own; `loft holder password` changes it", login)
				}
				if err := s.Identity.SetPassword(ctx, p.Id, pw); err != nil {
					return fmt.Errorf("roster: %w", err)
				}
				made = ""
			}

			// And the workspace, which is Loft's, with the account's
			// membership of it.
			t, err := s.Base.Tenant().Add(ctx, app.TenantAddRequest_builder{Alias: alias, Name: name}.Build())
			if err != nil {
				return fmt.Errorf("workspace %q: %w", alias, err)
			}
			tenant := app.TenantRef_builder{Id: t.GetId()}.Build()
			h, err := s.Base.Holder().Add(ctx, app.HolderAddRequest_builder{
				Tenant:  tenant,
				Alias:   p.Alias,
				Name:    person,
				Role:    z.Ptr("owner"),
				Account: p.Id.Bytes(),
			}.Build())
			if err != nil {
				return fmt.Errorf("holder %q: %w", login, err)
			}
			if _, err := s.Base.Party().Add(ctx, app.PartyAddRequest_builder{
				Tenant: tenant,
				Name:   person,
				Kind:   "person",
				Email:  email,
				Holder: app.HolderRef_builder{Id: h.GetId()}.Build(),
			}.Build()); err != nil {
				return err
			}

			// From here on it is the owner doing it, through the domain layer,
			// so that what it writes reads as theirs.
			tid, _ := pdid.From(t.GetId())
			hid, _ := pdid.From(h.GetId())
			ctx = frame.Into(ctx, frame.New(hid, tid, frame.Whole()).WithScope(frame.Only(tid)).WithRow(h))

			if err := tenancy.SetUp(ctx, s.Ungated); err != nil {
				return err
			}
			if demo {
				if err := seedDemo(ctx, s.Ungated, self); err != nil {
					return fmt.Errorf("demo: %w", err)
				}
			}

			self.Printf("workspace @%s (%s)\n", alias, tid)
			self.Printf("owner     %s (account %s)\n", p.Alias, p.Id)
			if made != "" {
				self.Printf("password  %s\n", made)
			}
			self.Printf("\nsign in at %s with %s\n", c.App.App(), p.Alias)
			return nil
		}),
	}
}

// ownerAccount is the account `init` makes the owner: the one in the directory
// by that login, or a new one, with the password made for it when it is new.
// The directory is made with it when there is none.
func ownerAccount(ctx context.Context, s *cmd.Server, login, name string) (identity.Person, string, error) {
	_, err := s.Identity.Directory(ctx)
	if errors.Is(err, identity.ErrNoDirectory) {
		return s.Identity.MakeDirectory(ctx, login, name)
	}
	if err != nil {
		return identity.Person{}, "", err
	}
	p, err := s.Identity.Lookup(ctx, login)
	if err == nil {
		return p, "", nil
	}
	if !errors.Is(err, identity.ErrNoPerson) {
		return identity.Person{}, "", err
	}
	p, err = s.Identity.AddPerson(ctx, login, name, pdid.Nil)
	if err != nil {
		return identity.Person{}, "", err
	}
	made, err := s.Identity.IssuePassword(ctx, p.Id)
	if err != nil {
		s.Identity.ForgetPerson(ctx, p.Id)
		return identity.Person{}, "", err
	}

	return p, made, nil
}

// keyFor writes a key for the trail's archive to sign its account with, made
// fresh, beside the archive, and answers the file it is in. A file already
// there is somebody's key, and is not written over.
func keyFor(archive string) (string, error) {
	doc, _, err := trail.GenerateKey()
	if err != nil {
		return "", err
	}

	path := filepath.Clean(archive) + "-checkpoints.pem"
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return "", fmt.Errorf("a key for the trail: %w", err)
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if errors.Is(err, fs.ErrExist) {
		return "", fmt.Errorf("a key for the trail: %s is one already; name it as audit.checkpoints.key, or move it away for a new one", path)
	}
	if err != nil {
		return "", fmt.Errorf("a key for the trail: %w", err)
	}
	if _, err := f.Write(doc); err != nil {
		f.Close()

		return "", fmt.Errorf("a key for the trail: %w", err)
	}

	return path, f.Close()
}
