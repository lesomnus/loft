// Package identity is who somebody is, which roster answers (design 9.10,
// D26).
//
// A deployment serves one roster tenant, which is its workspace (D85): Loft's
// tenant has the roster tenant's identifier, and each member the identifier
// of their holder there, made the first time they sign in. A password is
// checked where it is held, and never here. roster is either a deployment of
// its own that this one reaches over the wire, which is what a deployment
// runs, or one run inside this process on a database of its own and served on
// a listener nothing outside the process can dial, which is for tests and for
// trying Loft (#327's decision 5).
//
// Whichever it is, Loft talks to it the same way: as the holder `loft` in the
// tenant, holding the role that lets it check a password, read a person and
// their tenant, ask what somebody may call, and write the roles Loft ships.
// On an external roster that is the tenant's own key, which hangs on that
// holder (`rt_`, roster's `docs/apps.md` shape C: the tenant runs it). On the
// embedded one Loft writes that holder and its role itself.
//
// shale was here first (its `internal/identity`), and this is the same shape,
// cut to what Loft asks.
package identity

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"os"
	"slices"
	"strings"
	"sync"
	"time"
	"uuid"

	"github.com/lesomnus/z"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"

	"github.com/lesomnus/payday/auth"
	"github.com/lesomnus/payday/config"
	"github.com/lesomnus/payday/pdid"
	"github.com/lesomnus/roster/reach"
	"github.com/lesomnus/roster/rstr"
	rostercore "github.com/lesomnus/roster/server/core"
	"github.com/lesomnus/roster/server/front"
	"github.com/lesomnus/roster/standing"

	"github.com/lesomnus/loft/server/policy"
)

// Config is `auth.roster`.
type Config struct {
	// Addr is an external roster's data plane. Empty runs one in this
	// process, on Db.
	Addr string `yaml:"addr"`
	// Insecure dials an external roster in plaintext, for a lab.
	Insecure bool `yaml:"insecure"`
	// CaFile pins the CA an external roster's certificate chains to; empty is
	// the system pool.
	CaFile string `yaml:"ca_file"`
	// Key is the key Loft acts with on an external roster: `env:NAME`,
	// `file:PATH`, or the key itself. It is the tenant's own key (`rt_`,
	// roster's `docs/apps.md` shape C), on the holder `loft` installed there
	// (`roster app install --tenant <alias> ... loft`, then `roster key add
	// --tenant <alias> --holder loft`): one tenant, the key's own, which no
	// call can name another of.
	Key string `yaml:"key"`
	// Db is the embedded roster's database. Empty is `roster.db` in the
	// directory Loft's own SQLite file is in.
	Db config.DbConfig `yaml:"db"`

	// Tenant is the roster tenant this deployment serves, by its alias: the
	// workspace, whose identifier and members Loft's are (design 9.10, D85).
	// Empty is [DefaultTenant] on the roster in this process, and the key's
	// own tenant on a roster of its own -- where, named, it is held to the
	// key's.
	Tenant string `yaml:"tenant"`

	// AccountUrl is roster's account page, at the front door the tenant signs
	// in through: where somebody changes how they sign in and, where the
	// tenant offers it (roster's `TenantConfig.leave`), deletes their holder
	// there (`#leave`). It is an external roster's; empty draws no link.
	AccountUrl string `yaml:"account_url"`

	// ConsoleUrl is roster's user console for the tenant: where its
	// administrators add and remove the members and give them a way in,
	// which is roster's and not Loft's (D87). The people screen links to it.
	// It is an external roster's; empty draws no link.
	ConsoleUrl string `yaml:"console_url"`
}

// DefaultTenant is the tenant of the roster in this process when the
// configuration names none.
const DefaultTenant = "loft"

// Tenant is the roster tenant this deployment serves: its workspace.
type Tenant struct {
	Id    pdid.Id
	Alias string
	Name  string
}

// Embedded says whether roster runs in this process.
func (c Config) Embedded() bool { return c.Addr == "" }

// Agent is the alias of the holder Loft acts as in the tenant.
const Agent = "loft"

// RosterAdministrator is the holder roster makes every tenant with, so that
// somebody can administer it, with no way to sign in until it is given one.
const RosterAdministrator = rostercore.Administers

// AgentMethods is what that holder may call:
//
//   - what roster's `standing` asks for ([standing.Methods]: somebody's
//     standing, the stream of what stopped being good about somebody, and the
//     addresses somebody checked for them -- which is how their person record
//     here is found, and where their mail goes);
//   - what roster's `reach` asks for ([reach.Methods]: what somebody may call,
//     and the stream that says when that may have changed), which every call
//     here is decided by (D89);
//   - what Loft asks itself: checking a password, reading the tenant, and
//     whose the key is;
//   - and the roles Loft ships (`policy.Roles`, #327's decision 31): written
//     into the tenant and read back (`RoleService/Add`, `Get`), widened by
//     what a release brings (`Widen`), and the first owner bound to one
//     (`BindingService/Add`) -- with every method of Loft's, `/loft.*/*`,
//     since nobody writes a role or a binding that grants what they do not
//     hold themselves (roster's escalation rule). It gives nobody anything
//     here: Loft asks roster about whoever calls it, and is never called as
//     itself.
//
// It is the `--role` an external roster installs Loft with, and what the
// tenant's key allows; `--administer` gives the tenant's administrators Loft's
// methods too, without which nobody there could bind the roles it writes:
//
//	roster app install --tenant acme --role <AgentMethods, by commas> --administer '/loft.*/*' loft
//	roster key add --tenant acme --holder loft --name loft --allow <the same>
//
// Installing again leaves a role the tenant has as the tenant has it
// (roster's `docs/apps.md`): a role is changed by patching it, with the whole
// list --
//
//	roster --as <operator> role patch @acme/loft-app '{"methods": [...], "date_updated_force": true}'
//
// -- at roster's own box, naming the operator who does it where there is a
// control plane (roster#221) --
//
// While the role lacks one of them, nobody's person record is found by the
// address they checked at roster, a session learns what roster says of its
// member only when it next asks, or -- lacking `HolderService/Reaches` --
// nothing at all is served.
//
// People are made, given passwords and suspended at roster, by whoever
// administers the tenant there; on the embedded roster Loft does those through
// the deployment's own door, which is not a role.
var AgentMethods = func() []string {
	out := []string{}
	for _, m := range slices.Concat(standing.Methods, reach.Methods, []string{
		rstr.VouchService_Verify_FullMethodName,
		rstr.TenantService_Get_FullMethodName,
		rstr.MeService_Get_FullMethodName,
		rstr.RoleService_Add_FullMethodName,
		rstr.RoleService_Get_FullMethodName,
		rstr.RoleService_Widen_FullMethodName,
		rstr.BindingService_Add_FullMethodName,
		LoftMethods,
	}) {
		if !slices.Contains(out, m) {
			out = append(out, m)
		}
	}
	return out
}()

// LoftMethods is every method of Loft's, as a pattern: what Loft's own role
// holds so that the roles it writes are grants it may make, and what the
// tenant's administrators are given with it (`--administer`).
const LoftMethods = "/loft.*/*"

// prefixTenantKey is what a tenant's key begins with, the way roster reads
// it.
const prefixTenantKey = "rt_"

// Person is who roster said somebody is: the identifiers Loft's rows are
// anchored on, and the names to make them with.
type Person struct {
	Id     pdid.Id
	Tenant pdid.Id
	Alias  string
	Name   string

	TenantAlias string
	TenantName  string
}

var (
	// ErrRefused is a wrong password, or nobody by that name: one answer for
	// both, as roster gives it -- and a session roster's word does not hold
	// any more ([Store.Held]). It is `standing`'s, so that either reads as
	// itself.
	ErrRefused = standing.ErrRefused
	// ErrSecondFactor is a right password for somebody who has more to prove:
	// a second factor, which they prove at roster's issuer. A password alone
	// is never a sign-in for them.
	ErrSecondFactor = errors.New("a second factor is asked for: sign in through the issuer")
	// ErrNoTenant is the tenant this deployment serves not being there: on
	// the roster in this process before `loft init` made it, and on a roster
	// of its own before its operator did, or a key of another one's.
	ErrNoTenant = errors.New("the tenant this deployment serves is not there")
	// ErrNoPerson is nobody by that name in the tenant this deployment
	// serves: `standing`'s, which [Store.StandingOf] answers.
	ErrNoPerson = standing.ErrNoPerson
	// ErrExternal is an operation only the embedded roster takes: on an
	// external one it is the tenant administrator's, at roster.
	ErrExternal = errors.New("people and the tenant are made at roster, which is not this process")
	// ErrUnreachable is a roster that could not be asked. A session is not
	// served while it lasts: what roster would have said is not known. It is
	// `standing`'s, which [Store.Held] answers.
	ErrUnreachable = standing.ErrUnreachable
)

// Locked is a refusal that says when somebody's login opens again.
type Locked struct{ Until time.Time }

func (e Locked) Error() string      { return "locked until " + e.Until.UTC().Format(time.RFC3339) }
func (Locked) Is(target error) bool { return target == ErrRefused }

// Store is the connection to roster, whichever kind.
type Store struct {
	cfg  Config
	log  *slog.Logger
	conn *grpc.ClientConn
	// key is the one key, on an external roster.
	key string

	// The embedded roster, nil for an external one.
	em *embedded

	mu     sync.Mutex
	agents map[string]bool

	// accounts is roster's `standing` on this connection: what a session is
	// held to, the stream, and the asking again ([Store.Accounts]).
	accounts *standing.Accounts
	// reaches is roster's `reach` on it: what somebody may call, which every
	// call here is decided by ([Store.Reach], D89).
	reaches *reach.Holders

	// The tenant, once it was found.
	dmu    sync.Mutex
	tenant Tenant

	// now is the clock, for the tests.
	now func() time.Time

	// The tenant a key is of, asked once.
	tmu sync.Mutex
	own string
}

// Open connects to roster, or builds and migrates the embedded one; Run serves
// its housekeeping. `stateDir` is where the embedded database goes when Db
// names none.
func Open(ctx context.Context, cfg Config, stateDir string, log *slog.Logger) (*Store, error) {
	if log == nil {
		log = slog.Default()
	}
	s := &Store{cfg: cfg, log: log, agents: map[string]bool{}, now: time.Now}
	if cfg.Embedded() {
		if cfg.Key != "" || cfg.CaFile != "" || cfg.Insecure || cfg.AccountUrl != "" || cfg.ConsoleUrl != "" {
			return nil, errors.New("auth.roster: key, ca_file, insecure, account_url and console_url are an external roster's, and addr names none")
		}
		em, err := openEmbedded(ctx, cfg, stateDir, log)
		if err != nil {
			return nil, err
		}
		s.em = em
		s.conn, err = grpc.NewClient("passthrough:///roster",
			grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) { return em.lis.DialContext(ctx) }),
			grpc.WithTransportCredentials(insecure.NewCredentials()))
		if err != nil {
			em.Close()
			return nil, err
		}
		if err := s.keepAsking(); err != nil {
			s.Close()
			return nil, err
		}

		return s, nil
	}

	if cfg.Key == "" {
		return nil, errors.New("auth.roster.key: an external roster is reached with the tenant's own key -- " + prefixTenantKey)
	}
	v, err := ResolveRef(cfg.Key)
	if err != nil {
		return nil, fmt.Errorf("auth.roster.key: %w", err)
	}
	if !strings.HasPrefix(v, prefixTenantKey) {
		return nil, fmt.Errorf("auth.roster.key: not %s, the tenant's own key: a deployment serves one tenant, as the holder its key hangs on (roster's docs/apps.md, shape C)", prefixTenantKey)
	}
	s.key = v

	var creds credentials.TransportCredentials
	switch {
	case cfg.Insecure:
		creds = insecure.NewCredentials()
	case cfg.CaFile != "":
		pem, err := os.ReadFile(cfg.CaFile)
		if err != nil {
			return nil, fmt.Errorf("auth.roster.ca_file: %w", err)
		}
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(pem) {
			return nil, fmt.Errorf("auth.roster.ca_file: %s holds no certificate", cfg.CaFile)
		}
		creds = credentials.NewTLS(&tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12})
	default:
		creds = credentials.NewTLS(&tls.Config{MinVersion: tls.VersionTLS12})
	}
	conn, err := grpc.NewClient(cfg.Addr, grpc.WithTransportCredentials(creds))
	if err != nil {
		return nil, fmt.Errorf("auth.roster.addr: %w", err)
	}
	s.conn = conn
	if err := s.keepAsking(); err != nil {
		s.Close()
		return nil, err
	}
	log.Info("identity: roster", "addr", cfg.Addr, "key", "the tenant's own key")

	return s, nil
}

// keepAsking is roster's `standing` and `reach` on the connection, either
// roster's: what Loft keeps asking roster after a sign-in, and what somebody
// may call, each written once in roster for every app that asks it (roster's
// `docs/relying-app.md`, `docs/apps.md`). Both ask as Loft's holder in the
// tenant, as everything here does.
func (s *Store) keepAsking() error {
	as := func(ctx context.Context) (context.Context, error) {
		as, _, err := s.inTenant(ctx)
		return as, err
	}
	v, err := standing.New(standing.Config{
		Conn: s.conn,
		Log:  s.log,
		As:   as,
		Now:  func() time.Time { return s.now() },
	})
	if err != nil {
		return err
	}
	s.accounts = v
	r, err := reach.New(reach.Config{
		Conn: s.conn,
		Log:  s.log,
		As:   as,
		Now:  func() time.Time { return s.now() },
	})
	if err != nil {
		return err
	}
	s.reaches = r

	return nil
}

// Reach is what the tenant's people may call, as roster says it -- roster's
// `reach`: asked at every call by the gate and by the domain layer
// ([reach.Holders.May]), kept for [HeldTtl], and dropped by roster's stream
// ([reach.Holders.Follower], which an external roster's deployment runs in
// every process) or by [Store.Bind], for a change made from here.
func (s *Store) Reach() *reach.Holders { return s.reaches }

// Accounts is the tenant's people as roster has them -- roster's `standing`:
// what a session is held to ([Store.Held]), roster's stream as a loop
// ([standing.Accounts.Follower]), and the asking again before a mark is acted
// on, and for what the stream missed ([standing.Accounts.Settle],
// [standing.Accounts.Reconcile]).
func (s *Store) Accounts() *standing.Accounts { return s.accounts }

// ResolveRef reads a secret's reference: `env:NAME`, `file:PATH`, or the value
// itself.
func ResolveRef(ref string) (string, error) {
	switch {
	case strings.HasPrefix(ref, "env:"):
		v := os.Getenv(ref[4:])
		if v == "" {
			return "", fmt.Errorf("%s is not set", ref[4:])
		}

		return strings.TrimSpace(v), nil
	case strings.HasPrefix(ref, "file:"):
		b, err := os.ReadFile(ref[5:])
		if err != nil {
			return "", err
		}

		return strings.TrimSpace(string(b)), nil
	}

	return strings.TrimSpace(ref), nil
}

// Embedded says whether roster runs in this process.
func (s *Store) Embedded() bool { return s.em != nil }

// Spinners is the embedded roster's housekeeping, its loops as roster named
// them, for the loops this process runs: each one under a lease of its own,
// and the ones roster runs in every process -- its access record's writer --
// in every process of this one. None, on an external roster, which keeps its
// own house.
//
// They are handed over one by one rather than as one loop that runs them:
// under a lease, one loop is one process, and a loop that must run in every
// process -- what writes the reads this process queued -- ran in only the one
// that held it.
func (s *Store) Spinners() []any {
	if s.em == nil {
		return nil
	}

	return s.em.spinners()
}

// Close ends the connection and the embedded roster.
func (s *Store) Close() error {
	var err error
	if s.conn != nil {
		err = s.conn.Close()
	}
	if s.em != nil {
		err = errors.Join(err, s.em.Close())
	}

	return err
}

// as is a context that calls roster as Loft's holder in the tenant: the
// `loft` holder the embedded roster has, or the holder the tenant's key hangs
// on.
func (s *Store) as(ctx context.Context, tenant string) (context.Context, error) {
	if s.em != nil {
		if err := s.ensureAgent(ctx, tenant); err != nil {
			return nil, err
		}

		return auth.PlainProvider(front.TenantMark + tenant + "/" + Agent).Provide(ctx), nil
	}
	own, err := s.ownTenant(ctx)
	if err != nil {
		return nil, err
	}
	if own != tenant {
		return nil, fmt.Errorf("%w: @%s, and this deployment's key is @%s's", ErrNoTenant, tenant, own)
	}

	return auth.BearerProvider(s.key).Provide(ctx), nil
}

// ownTenant is the tenant the key is of, asked once: roster's answer to who
// the key is (`MeService.Get`), and that tenant's alias.
func (s *Store) ownTenant(ctx context.Context) (string, error) {
	s.tmu.Lock()
	defer s.tmu.Unlock()
	if s.own != "" {
		return s.own, nil
	}
	as := auth.BearerProvider(s.key).Provide(ctx)
	me, err := rstr.NewMeServiceClient(s.conn).Get(as, rstr.MeGetRequest_builder{}.Build())
	if err != nil {
		return "", fmt.Errorf("roster: whose this key is: %w", err)
	}
	id, err := pdid.From(me.GetTenant())
	if err != nil {
		return "", fmt.Errorf("roster: whose this key is: %w", err)
	}
	t, err := rstr.NewTenantServiceClient(s.conn).Get(as, rstr.TenantGetRequest_builder{Ref: rstr.TenantRef_builder{Id: id.Bytes()}.Build()}.Build())
	if err != nil {
		return "", fmt.Errorf("roster: tenant %s: %w", id, err)
	}
	s.own = t.GetAlias()

	return s.own, nil
}

// ensureAgent writes the `loft` holder, its role and the binding into a tenant
// of the embedded roster, once.
func (s *Store) ensureAgent(ctx context.Context, tenant string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.agents[tenant] {
		return nil
	}
	if err := s.em.agent(ctx, tenant); err != nil {
		return err
	}
	s.agents[tenant] = true

	return nil
}

// Verify checks somebody's password with roster and answers who they are.
// `login` is their alias in the tenant, or -- with an `@` in it -- the
// address they are reached at.
//
// The tenant is not named: Loft calls as its holder in it, which says which
// one (roster's `VouchWho.tenant`), so a tenant renamed at roster goes on
// signing people in before this process takes its new address (D86).
func (s *Store) Verify(ctx context.Context, login, password string) (Person, error) {
	as, _, err := s.inTenant(ctx)
	if err != nil {
		return Person{}, err
	}
	who := rstr.VouchWho_builder{}
	if strings.Contains(login, "@") {
		who.Address = login
	} else {
		who.Alias = login
	}
	res, err := rstr.NewVouchServiceClient(s.conn).Verify(as, rstr.VouchVerifyRequest_builder{
		Who:    who.Build(),
		Kind:   "password",
		Secret: []byte(password),
	}.Build())
	if err != nil {
		return Person{}, fmt.Errorf("roster: %w", err)
	}
	if !res.GetOk() {
		switch {
		case res.GetContinuation() != "":
			// roster never answers ok beside a continuation: the password was
			// right, and it is not a sign-in on its own.
			return Person{}, ErrSecondFactor
		case res.GetDateLocked() != nil:
			return Person{}, Locked{Until: res.GetDateLocked().AsTime()}
		}

		return Person{}, ErrRefused
	}
	id, err := pdid.From(res.GetHolder())
	if err != nil {
		return Person{}, err
	}

	return s.person(as, rstr.HolderRef_builder{Id: id.Bytes()}.Build())
}

// Lookup is somebody in the tenant by their alias, with nothing checked: for a
// caller some other credential already vouched for.
func (s *Store) Lookup(ctx context.Context, alias string) (Person, error) {
	as, d, err := s.inTenant(ctx)
	if err != nil {
		return Person{}, err
	}

	return s.person(as, rstr.HolderRef_builder{
		Slug: rstr.HolderRefBySlug_builder{Alias: z.Ptr(alias), Tenant: rstr.TenantRef_builder{Id: d.Id.Bytes()}.Build()}.Build(),
	}.Build())
}

// ById is the person an issuer's `sub` names: roster's `sub` is a
// `Holder.id`, looked up in the tenant. Somebody the tenant does not have is
// nobody here, whatever the issuer vouched for.
func (s *Store) ById(ctx context.Context, sub string) (Person, error) {
	id, err := uuid.Parse(sub)
	if err != nil {
		return Person{}, fmt.Errorf("%w: %q is not roster's identifier", ErrNoPerson, sub)
	}
	as, _, err := s.inTenant(ctx)
	if err != nil {
		return Person{}, err
	}

	return s.person(as, rstr.HolderRef_builder{Id: id[:]}.Build())
}

// Addresses is somebody's addresses that somebody checked -- roster holds
// them as verified, by a link it sent or by the provider they signed in
// through -- as roster stores them, folded.
//
// An address nobody checked is not one of them, and that is the whole of the
// method. Any tenant's administrator may put any address on a user of their
// own, and a provider passes it on (nOAuth); roster keeps it, with nothing
// claimed, and a person record found by it would be whoever typed it.
func (s *Store) Addresses(ctx context.Context, holder pdid.Id) ([]string, error) {
	return s.accounts.Addresses(ctx, holder)
}

func (s *Store) person(as context.Context, ref *rstr.HolderRef) (Person, error) {
	v, err := rstr.NewHolderServiceClient(s.conn).Get(as, rstr.HolderGetRequest_builder{
		Ref:    ref,
		Select: rstr.HolderSelect_builder{All: z.Ptr(true), Tenant: rstr.TenantSelect_builder{All: z.Ptr(true)}.Build()}.Build(),
	}.Build())
	if err != nil {
		if status.Code(err) == codes.NotFound {
			return Person{}, ErrNoPerson
		}

		return Person{}, fmt.Errorf("roster: %w", err)
	}
	if v.GetDateDisabled() != nil {
		return Person{}, fmt.Errorf("%w: suspended", ErrRefused)
	}

	return personOf(v)
}

func personOf(v *rstr.Holder) (Person, error) {
	id, err := pdid.From(v.GetId())
	if err != nil {
		return Person{}, err
	}
	tenant, err := pdid.From(v.GetTenant().GetId())
	if err != nil {
		return Person{}, err
	}

	return Person{
		Id: id, Tenant: tenant, Alias: v.GetAlias(), Name: v.GetName(),
		TenantAlias: v.GetTenant().GetAlias(), TenantName: v.GetTenant().GetName(),
	}, nil
}

// TenantAlias is the alias of the tenant this deployment serves, whether or
// not it is there yet: what the configuration names, [DefaultTenant] on the
// roster in this process when it names none, and the key's own tenant on a
// roster of its own.
func (s *Store) TenantAlias(ctx context.Context) (string, error) { return s.tenantAlias(ctx) }

// tenantAlias is the alias the tenant goes by: what the configuration says,
// and what the key is.
func (s *Store) tenantAlias(ctx context.Context) (string, error) {
	if s.em != nil {
		if s.cfg.Tenant != "" {
			return s.cfg.Tenant, nil
		}

		return DefaultTenant, nil
	}
	own, err := s.ownTenant(ctx)
	if err != nil {
		return "", err
	}
	if s.cfg.Tenant != "" && s.cfg.Tenant != own {
		return "", fmt.Errorf("auth.roster.tenant: @%s, and this deployment's key is @%s's", s.cfg.Tenant, own)
	}

	return own, nil
}

// Tenant is the roster tenant this deployment serves, found once and kept.
// [ErrNoTenant] on the roster in this process before anything made it.
func (s *Store) Tenant(ctx context.Context) (Tenant, error) {
	s.dmu.Lock()
	defer s.dmu.Unlock()
	if s.tenant.Alias != "" {
		return s.tenant, nil
	}
	alias, err := s.tenantAlias(ctx)
	if err != nil {
		return Tenant{}, err
	}
	as, err := s.as(ctx, alias)
	if err != nil {
		return Tenant{}, err
	}
	v, err := rstr.NewTenantServiceClient(s.conn).Get(as, rstr.TenantGetRequest_builder{
		Ref: rstr.TenantRef_builder{Alias: z.Ptr(alias)}.Build(),
	}.Build())
	if err != nil {
		if status.Code(err) == codes.NotFound {
			return Tenant{}, fmt.Errorf("%w (@%s)", ErrNoTenant, alias)
		}

		return Tenant{}, fmt.Errorf("roster: %w", err)
	}
	id, err := pdid.From(v.GetId())
	if err != nil {
		return Tenant{}, err
	}
	s.tenant = Tenant{Id: id, Alias: alias, Name: v.GetName()}

	return s.tenant, nil
}

// inTenant is a context that calls roster as Loft's holder in the tenant,
// and the tenant.
func (s *Store) inTenant(ctx context.Context) (context.Context, Tenant, error) {
	d, err := s.Tenant(ctx)
	if err != nil {
		return nil, Tenant{}, err
	}
	as, err := s.as(ctx, d.Alias)
	if err != nil {
		return nil, Tenant{}, err
	}

	return as, d, nil
}

// AccountPage is roster's page where somebody manages how they sign in, and
// deletes themselves where the tenant offers that; empty for none.
func (s *Store) AccountPage() string { return s.cfg.AccountUrl }

// ConsolePage is roster's user console for the tenant, where its
// administrators add and remove members and give them a way in; empty for
// none.
func (s *Store) ConsolePage() string { return s.cfg.ConsoleUrl }

// Standing is what roster says of somebody now -- when everything issued to
// them before stopped being good, when they were suspended, when they were
// erased, and when the tenant they are in was closed (roster#105) -- as
// roster's `standing` says it.
type Standing = standing.Standing

// StandingOf is what roster says of somebody now, asked every time and kept
// nowhere. [ErrNoPerson] is somebody roster does not have any more; anything
// else is a roster that could not be asked. It reads the four facts and
// nothing of the person's: a read of those would be a row of roster's access
// record every time a session is held to it.
func (s *Store) StandingOf(ctx context.Context, holder pdid.Id) (Standing, error) {
	return s.accounts.StandingOf(ctx, holder)
}

// HeldTtl is how long what roster says of a person is believed before it is
// asked again: how late a suspension, an erasure or signing out everywhere
// reaches a session already open, when the stream did not carry it first.
const HeldTtl = standing.HeldTtl

// HeldRetry is how long a roster that could not be asked is not asked again,
// so that one that is down is not asked once per call.
const HeldRetry = standing.HeldRetry

// Held says whether a session that began at `began` is still good, by what
// roster says of the member it names: [ErrRefused] when they were suspended,
// erased or signed out everywhere since, the tenant closed -- or are not there
// at all -- and [ErrUnreachable] when roster could not be asked, which is a
// refusal too. Believed for [HeldTtl], and a roster that could not be asked
// for [HeldRetry]; a call that ends while roster is asked keeps nothing of it.
func (s *Store) Held(ctx context.Context, holder pdid.Id, began time.Time) error {
	return s.accounts.Held(ctx, holder, began)
}

// Recheck drops what is known of a person, for a change made here to be seen
// on the next call rather than after [HeldTtl].
func (s *Store) Recheck(holder pdid.Id) { s.accounts.Recheck(holder) }

// WriteRoles writes the roles Loft ships into the tenant, as the holder Loft is
// there (#327's decisions 9, 31): each under its alias, with what it holds at
// first. A role the tenant has under that alias already is the tenant's and
// left as it is, whatever it holds -- narrowed, widened, or another's -- since
// from the first write on it is theirs to change in roster's screens. It
// answers the aliases it wrote.
//
// It is `loft init`'s, on either roster: the embedded one is asked over the
// wire like any other, as the holder this process writes there.
func (s *Store) WriteRoles(ctx context.Context, roles []policy.Role) ([]string, error) {
	as, d, err := s.inTenant(ctx)
	if err != nil {
		return nil, err
	}
	c := rstr.NewRoleServiceClient(s.conn)
	tref := rstr.TenantRef_builder{Id: d.Id.Bytes()}.Build()
	wrote := []string{}
	for _, r := range roles {
		_, err := c.Get(as, rstr.RoleGetRequest_builder{
			Ref: rstr.RoleRef_builder{Slug: rstr.RoleRefBySlug_builder{Alias: z.Ptr(r.Alias), Tenant: tref}.Build()}.Build(),
		}.Build())
		if err == nil {
			continue
		}
		if status.Code(err) != codes.NotFound {
			return wrote, fmt.Errorf("roster: role %s: %w", r.Alias, err)
		}
		if _, err := c.Add(as, rstr.RoleAddRequest_builder{
			Tenant: tref, Alias: r.Alias, Name: r.Name, Desc: r.Desc, Methods: r.Methods,
		}.Build()); err != nil {
			if status.Code(err) == codes.AlreadyExists {
				// Written meanwhile, by another `init`: the tenant's.
				continue
			}
			return wrote, fmt.Errorf("roster: role %s: %w", r.Alias, err)
		}
		wrote = append(wrote, r.Alias)
	}

	return wrote, nil
}

// Bind binds somebody in the tenant to one of its roles, by the role's alias,
// as the holder Loft is there: what `loft init` does for the first owner, and
// the demo and `loft holder add` for the people they make on the roster in
// this process. A binding that is there already is no error. What is kept of
// what they may call is dropped at once ([reach.Holders.Recheck]): roster's
// stream says so too on a roster of its own, and the one in this process has
// none.
func (s *Store) Bind(ctx context.Context, role string, holder pdid.Id) error {
	as, d, err := s.inTenant(ctx)
	if err != nil {
		return err
	}
	_, err = rstr.NewBindingServiceClient(s.conn).Add(as, rstr.BindingAddRequest_builder{
		Role: rstr.RoleRef_builder{Slug: rstr.RoleRefBySlug_builder{
			Alias:  z.Ptr(role),
			Tenant: rstr.TenantRef_builder{Id: d.Id.Bytes()}.Build(),
		}.Build()}.Build(),
		Holder: rstr.HolderRef_builder{Id: holder.Bytes()}.Build(),
	}.Build())
	if err != nil && status.Code(err) != codes.AlreadyExists {
		return fmt.Errorf("roster: binding %s to %s: %w", holder, role, err)
	}
	s.reaches.Recheck(holder)

	return nil
}

// ErrRoleGone is a role the tenant at roster has none by the alias Loft wrote
// it under: taken away, or made another, which is the tenant's.
var ErrRoleGone = errors.New("the tenant at roster has no role by that alias")

// WidenRole gives the tenant's role `alias` at roster `methods` as well, as
// the holder Loft is there (`RoleService.Widen`, #327's decision 32): only
// adding, and nothing it holds already, so that asking twice is asking once
// -- what a later release gave the roles Loft ships (D90). [ErrRoleGone] is
// a role the tenant has none by. What is kept of what its holders may call is
// asked again within [standing.HeldTtl], and at once where roster's stream
// says so.
func (s *Store) WidenRole(ctx context.Context, alias string, methods []string) error {
	as, d, err := s.inTenant(ctx)
	if err != nil {
		return err
	}
	_, err = rstr.NewRoleServiceClient(s.conn).Widen(as, rstr.RoleWidenRequest_builder{
		Ref: rstr.RoleRef_builder{Slug: rstr.RoleRefBySlug_builder{
			Alias:  z.Ptr(alias),
			Tenant: rstr.TenantRef_builder{Id: d.Id.Bytes()}.Build(),
		}.Build()}.Build(),
		Methods: methods,
	}.Build())
	switch {
	case status.Code(err) == codes.NotFound:
		return fmt.Errorf("%w: %s", ErrRoleGone, alias)
	case err != nil:
		return fmt.Errorf("roster: widening %s: %w", alias, err)
	}
	return nil
}

// Enters says whether roster says somebody may come in at all: call
// `PartyService.Me`, which every page asks first and every role Loft ships
// holds (D89). Nil when it does; [reach.ErrDenied] when it does not -- no role
// of Loft's bound to them at roster, which a sign-in is refused for and told;
// and anything else for a roster that could not be asked.
func (s *Store) Enters(ctx context.Context, holder pdid.Id) error {
	return s.reaches.May(ctx, holder, Enters)
}

// Enters is the method somebody has to be able to call to come in: who they
// are, the first thing a page asks.
const Enters = "/loft.PartyService/Me"
