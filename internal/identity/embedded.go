package identity

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/lesomnus/z"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/lesomnus/payday/config"
	"github.com/lesomnus/payday/pdid"
	"github.com/lesomnus/payday/spin"
	rostercli "github.com/lesomnus/roster/cli"
	rostercmd "github.com/lesomnus/roster/cmd"
	"github.com/lesomnus/roster/rstr"
	rostercore "github.com/lesomnus/roster/server/core"
	"github.com/lesomnus/roster/server/forget"
)

// embedded is roster in this process: its server on an in-process listener
// nothing outside can dial, which is why it may believe what a caller says it
// is (payday's Plain), and the deployment's own door to it -- the server the
// wall was never installed on -- for the rows a deployment writes about itself
// and its people.
type embedded struct {
	rs  *rostercmd.Server
	cfg rostercmd.Config
	g   *grpc.Server
	lis *bufconn.Listener
	log *slog.Logger
}

// DbFile is the embedded roster's database, beside Rove's own.
const DbFile = "roster.db"

// unframed is a context for the deployment's own door at the embedded roster.
// Both apps read payday's one frame type, so a request's frame here would be a
// frame there, and roster would take Rove's actor for one of its own: an
// administrator issuing somebody a password would be refused as asking for
// their own. What the deployment does through that door is nobody's request,
// so the frame stays behind; the deadline comes along.
func unframed(ctx context.Context) (context.Context, context.CancelFunc) {
	if d, ok := ctx.Deadline(); ok {
		return context.WithDeadline(context.Background(), d)
	}

	return context.WithCancel(context.Background())
}

func openEmbedded(ctx context.Context, cfg Config, stateDir string, log *slog.Logger) (*embedded, error) {
	rc := rostercmd.Config{Db: cfg.Db}
	if rc.Db.Driver == "" {
		if stateDir == "" {
			return nil, errors.New("auth.roster.db: the roster in this process needs a database, and there is no directory to put one in")
		}
		if err := os.MkdirAll(stateDir, 0o700); err != nil {
			return nil, err
		}
		rc.Db.Driver = "sqlite3"
		rc.Db.Dsn = "file:" + filepath.Join(stateDir, DbFile) + "?_pragma=foreign_keys(1)&_pragma=journal_mode(wal)&_pragma=busy_timeout(10000)"
	}
	// Nothing watches roster's rows from here, and a broker would be the wrong
	// one for a second app on the same database anyway.
	rc.Watch.Broker = config.BrokerNone
	rs, err := rostercmd.Build(ctx, rc)
	if err != nil {
		return nil, fmt.Errorf("embedded roster: %w", err)
	}
	if err := rostercli.Migrate(ctx, rs); err != nil {
		rs.Close()
		return nil, fmt.Errorf("embedded roster: %w", err)
	}
	g, err := rs.Grpc(ctx, rc)
	if err != nil {
		rs.Close()
		return nil, fmt.Errorf("embedded roster: %w", err)
	}
	log.Info("identity: roster in this process, on its own database; reachable from this process only", "db", rc.Db.Driver)
	e := &embedded{rs: rs, cfg: rc, g: g, lis: bufconn.Listen(1 << 20), log: log}
	// Served from here on, so that whoever opened the store can call it --
	// `rove init` does, before anything runs. What Run adds is roster's own
	// housekeeping.
	go func() {
		if err := g.Serve(e.lis); err != nil && !errors.Is(err, grpc.ErrServerStopped) {
			log.Error("identity: the embedded roster stopped", "err", err.Error())
		}
	}()

	return e, nil
}

func (e *embedded) run(ctx context.Context) error {
	err := spin.Run(ctx, slices.Values(e.rs.Spin))
	e.g.Stop()

	return err
}

func (e *embedded) Close() error {
	e.g.Stop()

	return e.rs.Close()
}

// agent writes the `rove` holder, the role it holds and the binding into a
// tenant, through the deployment's own door. It refuses a tenant that is not
// there rather than making one, since a tenant is a customer.
func (e *embedded) agent(ctx context.Context, tenant string) error {
	ctx, cancel := unframed(ctx)
	defer cancel()
	own := e.rs.Ungated
	t, err := own.Tenant().Get(ctx, rstr.TenantGetRequest_builder{Ref: rstr.TenantRef_builder{Alias: z.Ptr(tenant)}.Build()}.Build())
	if err != nil {
		if status.Code(err) == codes.NotFound {
			return fmt.Errorf("%w: %s", ErrNoTenant, tenant)
		}

		return err
	}
	tref := rstr.TenantRef_builder{Id: t.GetId()}.Build()
	h, err := own.Holder().Get(ctx, rstr.HolderGetRequest_builder{
		Ref: rstr.HolderRef_builder{Slug: rstr.HolderRefBySlug_builder{Alias: z.Ptr(Agent), Tenant: tref}.Build()}.Build(),
	}.Build())
	if err != nil {
		if status.Code(err) != codes.NotFound {
			return err
		}
		h, err = own.Holder().Add(ctx, rstr.HolderAddRequest_builder{Tenant: tref, Alias: Agent, Name: "Rove"}.Build())
		if err != nil {
			return fmt.Errorf("holder %s: %w", Agent, err)
		}
	}
	r, err := own.Role().Get(ctx, rstr.RoleGetRequest_builder{
		Ref: rstr.RoleRef_builder{Slug: rstr.RoleRefBySlug_builder{Alias: z.Ptr(Agent), Tenant: tref}.Build()}.Build(),
	}.Build())
	if err != nil {
		if status.Code(err) != codes.NotFound {
			return err
		}
		r, err = own.Role().Add(ctx, rstr.RoleAddRequest_builder{
			Tenant: tref, Alias: Agent, Name: "what Rove asks roster", Methods: AgentMethods,
		}.Build())
		if err != nil {
			return fmt.Errorf("role %s: %w", Agent, err)
		}
	} else if missing := slices.DeleteFunc(slices.Clone(AgentMethods), func(m string) bool { return slices.Contains(r.GetMethods(), m) }); len(missing) > 0 {
		// A role written by an older Rove: what this one asks is added.
		if _, err := own.Role().Patch(ctx, rstr.RolePatchRequest_builder{
			Ref:              rstr.RoleRef_builder{Id: r.GetId()}.Build(),
			Methods:          append(slices.Clone(r.GetMethods()), missing...),
			DateUpdatedForce: z.Ptr(true),
		}.Build()); err != nil {
			return fmt.Errorf("role %s: %w", Agent, err)
		}
	}
	vs, err := own.Binding().List(ctx, rstr.BindingListRequest_builder{
		Filters: []*rstr.BindingFilter{rstr.BindingFilter_builder{
			Role:   rstr.RoleRef_builder{Id: r.GetId()}.Build(),
			Holder: rstr.HolderRef_builder{Id: h.GetId()}.Build(),
		}.Build()},
	}.Build())
	if err != nil {
		return err
	}
	if len(vs.GetItems()) == 0 {
		if _, err := own.Binding().Add(ctx, rstr.BindingAddRequest_builder{
			Role: rstr.RoleRef_builder{Id: r.GetId()}.Build(), Holder: rstr.HolderRef_builder{Id: h.GetId()}.Build(),
		}.Build()); err != nil && status.Code(err) != codes.AlreadyExists {
			return fmt.Errorf("binding: %w", err)
		}
	}

	return nil
}

// MakeDirectory makes the directory at the embedded roster, with its first
// account bound to the role that administers it there and a password shown
// once: what `rove init` does on a deployment that has none. An external
// roster refuses: its operator makes the directory.
func (s *Store) MakeDirectory(ctx context.Context, login string) (Person, string, error) {
	if s.em == nil {
		return Person{}, "", ErrExternal
	}
	alias, err := s.directoryAlias(ctx)
	if err != nil {
		return Person{}, "", err
	}
	ctx, cancel := unframed(ctx)
	defer cancel()
	seeded, err := rostercmd.Seed(ctx, s.em.rs, rostercmd.Seeding{Tenant: alias, Holder: login})
	if err != nil {
		return Person{}, "", err
	}
	secret, err := s.issue(ctx, seeded.Holder)
	if err != nil {
		return Person{}, "", err
	}
	p, err := s.lookupOwn(ctx, seeded.Holder)
	if err != nil {
		return Person{}, "", err
	}

	return p, secret, nil
}

// EnsureDirectory is the directory at the embedded roster, made with nobody in
// it but the administrator roster makes every tenant with when it is not
// there: what bringing in a deployment from before roster starts from.
func (s *Store) EnsureDirectory(ctx context.Context) (Directory, error) {
	if s.em == nil {
		return Directory{}, ErrExternal
	}
	d, err := s.Directory(ctx)
	if !errors.Is(err, ErrNoDirectory) {
		return d, err
	}
	alias, err := s.directoryAlias(ctx)
	if err != nil {
		return Directory{}, err
	}
	uctx, cancel := unframed(ctx)
	defer cancel()
	if _, err := s.em.rs.Ungated.Tenant().Add(uctx, rstr.TenantAddRequest_builder{Alias: alias, Name: alias}.Build()); err != nil {
		return Directory{}, fmt.Errorf("directory @%s: %w", alias, err)
	}

	return s.Directory(ctx)
}

// AddPerson makes an account in the directory of the embedded roster, with no
// way to sign in yet: IssuePassword or SetPassword gives it one. `id` is the
// identifier it is given, and the nil one mints a fresh one.
func (s *Store) AddPerson(ctx context.Context, alias, name string, id pdid.Id) (Person, error) {
	if s.em == nil {
		return Person{}, ErrExternal
	}
	d, err := s.Directory(ctx)
	if err != nil {
		return Person{}, err
	}
	ctx, cancel := unframed(ctx)
	defer cancel()
	req := rstr.HolderAddRequest_builder{
		Tenant: rstr.TenantRef_builder{Id: d.Id.Bytes()}.Build(),
		Alias:  alias,
		Name:   name,
	}
	if id != pdid.Nil {
		req.Id = id.Bytes()
	}
	v, err := s.em.rs.Ungated.Holder().Add(ctx, req.Build())
	if err != nil {
		return Person{}, err
	}
	made, err := pdid.From(v.GetId())
	if err != nil {
		return Person{}, err
	}

	return s.lookupOwn(ctx, made)
}

// AddAddress puts an address on an account of the embedded roster, checked or
// not, as the deployment says: what a deployment that runs roster here knows
// of its people, which no link was sent to prove.
func (s *Store) AddAddress(ctx context.Context, account pdid.Id, address string, verified bool) error {
	if s.em == nil {
		return ErrExternal
	}
	ctx, cancel := unframed(ctx)
	defer cancel()
	v, err := s.em.rs.Ungated.Email().Add(ctx, rstr.EmailAddRequest_builder{
		Holder:  rstr.HolderRef_builder{Id: account.Bytes()}.Build(),
		Address: strings.ToLower(strings.TrimSpace(address)),
	}.Build())
	if err != nil || !verified {
		return err
	}
	_, err = s.em.rs.Ungated.Email().Patch(ctx, rstr.EmailPatchRequest_builder{
		Ref:          rstr.EmailRef_builder{Id: v.GetId()}.Build(),
		DateVerified: timestamppb.Now(),
		DateUpdated:  v.GetDateUpdated(),
	}.Build())
	return err
}

// IssuePassword gives a person of the embedded roster a fresh password, shown
// once, replacing whatever they had -- and so ending whatever they were signed
// in to. At an external one the tenant's administrator does it there.
func (s *Store) IssuePassword(ctx context.Context, holder pdid.Id) (string, error) {
	if s.em == nil {
		return "", ErrExternal
	}
	ctx, cancel := unframed(ctx)
	defer cancel()

	return s.issue(ctx, holder)
}

func (s *Store) issue(ctx context.Context, holder pdid.Id) (string, error) {
	res, err := s.em.rs.Ungated.Credential().Issue(ctx, rstr.CredentialIssueRequest_builder{
		Ref: rstr.HolderRef_builder{Id: holder.Bytes()}.Build(), Kind: "password",
	}.Build())
	if err != nil {
		return "", err
	}

	return res.GetSecret(), nil
}

// SetPassword writes a person's password to the one given, at the embedded
// roster, as the deployment: for a person changing their own, once Rove has
// checked the one they have, and for the tests.
func (s *Store) SetPassword(ctx context.Context, holder pdid.Id, password string) error {
	if s.em == nil {
		return ErrExternal
	}
	ctx, cancel := unframed(ctx)
	defer cancel()
	_, err := s.em.rs.Ungated.Credential().Set(ctx, rstr.CredentialSetRequest_builder{
		Ref: rstr.HolderRef_builder{Id: holder.Bytes()}.Build(), Kind: "password", Secret: []byte(password),
	}.Build())

	return err
}

// Disable stops a person of the embedded roster signing in, and leaves them
// there; Enable is the other way.
func (s *Store) Disable(ctx context.Context, holder pdid.Id) error {
	if s.em == nil {
		return ErrExternal
	}
	ctx, cancel := unframed(ctx)
	defer cancel()
	_, err := s.em.rs.Ungated.Holder().Disable(ctx, rstr.HolderDisableRequest_builder{Ref: rstr.HolderRef_builder{Id: holder.Bytes()}.Build()}.Build())
	s.Recheck(holder)

	return err
}

func (s *Store) Enable(ctx context.Context, holder pdid.Id) error {
	if s.em == nil {
		return ErrExternal
	}
	ctx, cancel := unframed(ctx)
	defer cancel()
	_, err := s.em.rs.Ungated.Holder().Enable(ctx, rstr.HolderEnableRequest_builder{Ref: rstr.HolderRef_builder{Id: holder.Bytes()}.Build()}.Build())

	return err
}

// Invalidate voids everything a person of the embedded roster was issued
// before now: signing them out everywhere.
func (s *Store) Invalidate(ctx context.Context, holder pdid.Id) error {
	if s.em == nil {
		return ErrExternal
	}
	ctx, cancel := unframed(ctx)
	defer cancel()
	_, err := s.em.rs.Ungated.Holder().Invalidate(ctx, rstr.HolderInvalidateRequest_builder{Ref: rstr.HolderRef_builder{Id: holder.Bytes()}.Build()}.Build())
	s.Recheck(holder)

	return err
}

// ForgetPerson destroys what the embedded roster holds about a person -- their
// addresses, their password, their sessions there and the trail's copies of
// them -- and leaves their row an identifier naming nobody, which frees their
// alias (roster's `forget`). It is what a login ending in Rove means when the
// login is Rove's alone, and it is done at once: the grace roster gives an
// erase is for undoing it, which Rove does not offer, and a grace with nothing
// to undo is only a delay. At an external roster the person is the tenant's,
// and Rove leaves them.
func (s *Store) ForgetPerson(ctx context.Context, holder pdid.Id) error {
	if s.em == nil {
		return ErrExternal
	}
	ctx, cancel := unframed(ctx)
	defer cancel()
	_, err := s.em.rs.Ungated.Holder().Get(ctx, rstr.HolderGetRequest_builder{Ref: rstr.HolderRef_builder{Id: holder.Bytes()}.Build()}.Build())
	switch {
	case err == nil:
	case status.Code(err) == codes.NotFound:
		// A read leaves out somebody erased, who still has everything a
		// forgetting takes.
		erased, err := s.em.erased(ctx)
		if err != nil {
			return err
		}
		if _, ok := erased[holder]; !ok {
			// Nobody, or forgotten already.
			return nil
		}
	default:
		return err
	}
	p, err := s.em.cfg.Audit.Policy()
	if err != nil {
		return err
	}
	_, err = forget.Forget(ctx, s.em.rs.Ent, holder, p)
	s.Recheck(holder)

	return err
}

// erased is everybody erased here and not forgotten, by the tenant they are
// in: what the servers' reads leave out.
func (e *embedded) erased(ctx context.Context) (map[pdid.Id]pdid.Id, error) {
	ids, err := forget.Due(ctx, e.rs.Ent, time.Now().Add(time.Hour))
	if err != nil {
		return nil, err
	}
	out := make(map[pdid.Id]pdid.Id, len(ids))
	for _, id := range ids {
		v, err := e.rs.Ent.Holder.Get(ctx, id.Uuid())
		if err != nil {
			return nil, err
		}
		out[id] = pdid.Id(v.TenantId)
	}

	return out, nil
}

// Adoptee is a person [Store.Adopt] writes into the embedded roster, as Rove
// has them.
type Adoptee struct {
	Id    pdid.Id
	Alias string
	Name  string
}

// Adopt writes people made here before roster held them into the directory of
// the embedded roster as accounts, with the identifiers they already have, and
// issues each of them a password -- `password`, or one made up for each when
// it is empty: the upgrade of a deployment from before roster (design 9.10).
// Somebody roster has already is left alone, so it can be run again. It
// answers the passwords by alias, for the accounts it made. The directory has
// to be there ([Store.EnsureDirectory]).
//
// The verifiers they had are not brought along. Nothing says the two apps'
// formats agree, and a road that brings in a verifier brings in whatever is
// put on it.
func (s *Store) Adopt(ctx context.Context, people []Adoptee, password string) (map[string]string, error) {
	if s.em == nil {
		return nil, ErrExternal
	}
	d, err := s.Directory(ctx)
	if err != nil {
		return nil, err
	}
	alias := d.Alias
	ctx, cancel := unframed(ctx)
	defer cancel()
	own := s.em.rs.Ungated
	tref := rstr.TenantRef_builder{Id: d.Id.Bytes()}.Build()

	passwords := map[string]string{}
	for _, p := range people {
		if p.Alias == Agent {
			return nil, fmt.Errorf("@%s/%s: the name Rove acts by at roster; nobody else can have it there", alias, p.Alias)
		}
		if _, err := own.Holder().Get(ctx, rstr.HolderGetRequest_builder{Ref: rstr.HolderRef_builder{Id: p.Id.Bytes()}.Build()}.Build()); err == nil {
			continue
		} else if status.Code(err) != codes.NotFound {
			return nil, err
		}
		v, err := own.Holder().Get(ctx, rstr.HolderGetRequest_builder{
			Ref: rstr.HolderRef_builder{Slug: rstr.HolderRefBySlug_builder{Alias: z.Ptr(p.Alias), Tenant: tref}.Build()}.Build(),
		}.Build())
		switch {
		case err == nil && p.Alias == rostercore.Administers:
			// The one roster makes every tenant with, so that somebody can
			// administer it, and gives no way to sign in: here, the person
			// of that name is who it was for.
			id, err := pdid.From(v.GetId())
			if err != nil {
				return nil, err
			}
			if err := s.ForgetPerson(ctx, id); err != nil {
				return nil, fmt.Errorf("@%s/%s: %w", alias, p.Alias, err)
			}
		case err == nil:
			return nil, fmt.Errorf("@%s/%s: somebody else at roster already", alias, p.Alias)
		case status.Code(err) != codes.NotFound:
			return nil, err
		}
		if _, err := own.Holder().Add(ctx, rstr.HolderAddRequest_builder{Id: p.Id.Bytes(), Tenant: tref, Alias: p.Alias, Name: p.Name}.Build()); err != nil {
			return nil, fmt.Errorf("@%s/%s: %w", alias, p.Alias, err)
		}
		secret := password
		if secret == "" {
			secret, err = s.issue(ctx, p.Id)
		} else {
			_, err = own.Credential().Set(ctx, rstr.CredentialSetRequest_builder{
				Ref: rstr.HolderRef_builder{Id: p.Id.Bytes()}.Build(), Kind: "password", Secret: []byte(secret),
			}.Build())
		}
		if err != nil {
			// Not left made with no way in: a run after this one would take
			// them for brought in already, and pass them by.
			if e := s.em.rs.Ent.Holder.DeleteOneId(p.Id.Uuid()).Exec(ctx); e != nil {
				err = fmt.Errorf("%w (and they are at roster with no password: %v)", err, e)
			}
			return nil, fmt.Errorf("@%s/%s: %w", alias, p.Alias, err)
		}
		passwords[p.Alias] = secret
	}

	return passwords, nil
}

func (s *Store) lookupOwn(ctx context.Context, holder pdid.Id) (Person, error) {
	v, err := s.em.rs.Ungated.Holder().Get(ctx, rstr.HolderGetRequest_builder{
		Ref:    rstr.HolderRef_builder{Id: holder.Bytes()}.Build(),
		Select: rstr.HolderSelect_builder{All: z.Ptr(true), Tenant: rstr.TenantSelect_builder{All: z.Ptr(true)}.Build()}.Build(),
	}.Build())
	if err != nil {
		if status.Code(err) == codes.NotFound {
			return Person{}, ErrNoPerson
		}

		return Person{}, err
	}

	return personOf(v)
}
