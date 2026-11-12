package cli

import (
	"cmp"
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

	"github.com/protobuf-orm/ent/dialect"
	"github.com/protobuf-orm/protoc-gen-orm-ent/runtime/enttx"

	app "github.com/lesomnus/loft"
	"github.com/lesomnus/loft/cmd"
	"github.com/lesomnus/loft/internal/ent"
	"github.com/lesomnus/loft/internal/identity"
	"github.com/lesomnus/loft/server/tenancy"
)

// NewCmdInit is `loft init`: the workspace -- the roster tenant this
// deployment serves (`auth.roster.tenant`), by the same identifier (D85) --
// its first owner, and the types an asset register starts with.
//
// On a roster of its own the tenant and its people are there already: its
// operator made the tenant and installed Loft in it, and the key Loft holds is
// that tenant's (roster's `docs/apps.md` shape C). `init` reads the tenant,
// and makes its owner the holder `--login` names there -- `admin`, the one
// roster makes every tenant with, unless it names another -- whom the
// tenant's administrator gave a way to sign in. On the roster in this process,
// which is for tests and for trying Loft (#327's decision 5), `init` makes the
// tenant and its first person too, with a password shown once, and `--demo`
// people at both.
//
// It exists because there is nowhere else it could happen. A tenant is not put
// up from inside one, so the first row of a deployment cannot arrive over the
// API. What puts it there is [cmd.Server.Base], which is not a privilege
// anybody holds: it is a server instance this process was handed, reachable
// from this command and from nowhere a request can get to.
//
// Running it twice is an error rather than a no-op, because a deployment
// serves one workspace. That is the right answer -- an `init` that quietly did
// nothing is one somebody runs against the wrong deployment and believes.
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
		Brief: "put up the workspace and its first owner",

		Flags: flg.Flags{
			&flg.String{Name: "name", Brief: "the workspace's name; the tenant's at roster by default"},
			&flg.String{Name: "login", Brief: "the owner's login in the tenant at roster; made there, on the roster in this process, when it is not there"},
			&flg.String{Name: "person", Brief: "the owner's name; roster's by default, and \"관리자\" for one made on the roster in this process"},
			&flg.String{Name: "email", Brief: "the owner's e-mail, kept on their person record"},
			&flg.String{Name: "password", Brief: "on the roster in this process, the owner's password; empty makes one up"},
			&flg.Switch{Name: "demo", Brief: "on the roster in this process, also fill it with teams, people, spaces and assets to try things on"},
		},

		Handler: xli.OnRun(func(ctx context.Context, self *xli.Command, next xli.Next) error {
			get := func(name, otherwise string) string {
				v, ok := flg.Find[string](self, name)
				if !ok || strings.TrimSpace(v) == "" {
					return otherwise
				}
				return strings.TrimSpace(v)
			}
			name := get("name", "")
			login := get("login", identity.RosterAdministrator)
			person := get("person", "")
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
			if !s.Identity.Embedded() && (pw != "" || demo) {
				return errors.New("--password and --demo are the roster in this process's: on a roster of its own, people are made and given passwords there")
			}

			// The schema, so that a fresh database is one this can run against.
			if err := Migrate(ctx, s); err != nil {
				return err
			}

			// Here first, since a workspace already here is the error, and
			// roster would otherwise keep a tenant made for it.
			if here, err := s.Ent.Tenant.Query().First(ctx); err == nil {
				return fmt.Errorf("the workspace @%s is here already: a deployment serves one", here.Alias)
			} else if !ent.IsNotFound(err) {
				return err
			}

			// The tenant at roster and its owner there.
			t, p, made, err := owner(ctx, s, name, login, person)
			if err != nil {
				return fmt.Errorf("roster: %w", err)
			}
			if pw != "" {
				if made == "" {
					return fmt.Errorf("--password: %s is at roster already, with a password of its own; `loft holder password` changes it", login)
				}
				if err := s.Identity.SetPassword(ctx, p.Id, pw); err != nil {
					return fmt.Errorf("roster: %w", err)
				}
				made = ""
			}

			ws, err := putUp(ctx, s, t, cmp.Or(name, t.Name), p, cmp.Or(person, p.Name), email, demo, self)
			if err != nil {
				return err
			}

			tid, _ := pdid.From(ws.GetId())
			self.Printf("workspace @%s (%s)\n", ws.GetAlias(), tid)
			self.Printf("owner     %s (%s)\n", p.Alias, p.Id)
			if made != "" {
				self.Printf("password  %s\n", made)
			}
			self.Printf("\nsign in at %s with %s\n", c.App.App(), p.Alias)
			return nil
		}),
	}
}

// putUp is what `init` does once it has the tenant at roster and its owner
// there: the workspace and the owner's membership of it ([initWorkspace]),
// then -- as the owner, through the domain layer, so that what it writes reads
// as theirs -- what an asset register starts with, and with `demo` people and
// things to try it on.
func putUp(ctx context.Context, s *cmd.Server, t identity.Tenant, name string, p identity.Person, person, email string, demo bool, out *xli.Command) (*app.Tenant, error) {
	ws, h, err := initWorkspace(ctx, s, t, name, p, person, email)
	if err != nil {
		return nil, err
	}

	tid, _ := pdid.From(ws.GetId())
	hid, _ := pdid.From(h.GetId())
	ctx = frame.Into(ctx, frame.New(hid, tid, frame.Whole()).WithScope(frame.Only(tid)).WithRow(h))
	if err := tenancy.SetUp(ctx, s.Ungated); err != nil {
		return nil, err
	}
	if demo {
		if err := seedDemo(ctx, s.Ungated, out); err != nil {
			return nil, fmt.Errorf("demo: %w", err)
		}
	}

	return ws, nil
}

// initWorkspace puts up the workspace `init` makes -- the tenant at roster, by
// its identifier and alias -- and its owner's membership, by theirs at roster,
// and person record: one transaction.
func initWorkspace(ctx context.Context, s *cmd.Server, t identity.Tenant, name string, p identity.Person, person, email string) (*app.Tenant, *app.Holder, error) {
	tdrv, tx, err := dialect.BeginTx(ctx, s.Drv)
	if err != nil {
		return nil, nil, err
	}
	defer tx.Rollback()
	base, err := enttx.Rebind(s.Base, tdrv)
	if err != nil {
		return nil, nil, err
	}

	ws, err := base.Tenant().Add(ctx, app.TenantAddRequest_builder{Id: t.Id.Bytes(), Alias: t.Alias, Name: name}.Build())
	if err != nil {
		return nil, nil, fmt.Errorf("workspace @%s: %w", t.Alias, err)
	}
	tenant := app.TenantRef_builder{Id: ws.GetId()}.Build()
	h, err := base.Holder().Add(ctx, app.HolderAddRequest_builder{
		Id:      p.Id.Bytes(),
		Tenant:  tenant,
		Alias:   p.Alias,
		Name:    cmp.Or(person, p.Alias),
		Role:    z.Ptr("owner"),
		Account: p.Id.Bytes(),
	}.Build())
	if err != nil {
		return nil, nil, fmt.Errorf("holder %q: %w", p.Alias, err)
	}
	if _, err := base.Party().Add(ctx, app.PartyAddRequest_builder{
		Tenant: tenant,
		Name:   cmp.Or(person, p.Alias),
		Kind:   "person",
		Email:  email,
		Holder: app.HolderRef_builder{Id: h.GetId()}.Build(),
	}.Build()); err != nil {
		return nil, nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, nil, err
	}

	return ws, h, nil
}

// madeOwner is what the owner the roster in this process is given is called,
// when `--person` names nobody: who they are to everybody they work with.
const madeOwner = "관리자"

// owner is the tenant at roster and the person `init` makes its owner: on the
// roster in this process the tenant made with them when there is none, and
// them made in it when they are not there, each with a password made for
// them; on a roster of its own, the tenant the key is of and the person there
// by that login, made by its administrator.
func owner(ctx context.Context, s *cmd.Server, name, login, person string) (identity.Tenant, identity.Person, string, error) {
	t, err := s.Identity.Tenant(ctx)
	if errors.Is(err, identity.ErrNoTenant) && s.Identity.Embedded() {
		return s.Identity.MakeTenant(ctx, name, login, cmp.Or(person, madeOwner))
	}
	if err != nil {
		return identity.Tenant{}, identity.Person{}, "", err
	}
	p, err := s.Identity.Lookup(ctx, login)
	if err == nil {
		return t, p, "", nil
	}
	if !errors.Is(err, identity.ErrNoPerson) {
		return identity.Tenant{}, identity.Person{}, "", err
	}
	if !s.Identity.Embedded() {
		return identity.Tenant{}, identity.Person{}, "", fmt.Errorf("@%s/%s is nobody there: the tenant's administrator makes them first, or --login names somebody who is", t.Alias, login)
	}
	p, err = s.Identity.AddPerson(ctx, login, cmp.Or(person, madeOwner), pdid.Nil)
	if err != nil {
		return identity.Tenant{}, identity.Person{}, "", err
	}
	made, err := s.Identity.IssuePassword(ctx, p.Id)
	if err != nil {
		s.Identity.ForgetPerson(ctx, p.Id)
		return identity.Tenant{}, identity.Person{}, "", err
	}

	return t, p, made, nil
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
