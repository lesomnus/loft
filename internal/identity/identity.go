// Package identity is who somebody is, which roster answers (design 9.10,
// D26).
//
// The tenants and the people in them are roster's rows; Rove's are anchored on
// the same identifiers and made the first time somebody roster vouched for
// arrives. A password is checked where it is held, and never here. roster is
// either a deployment of its own that this one reaches over the wire, which is
// the default, or one run inside this process on a database of its own and
// served on a listener nothing outside the process can dial, for a deployment
// of one server.
//
// Whichever it is, Rove talks to it the same way: as the holder `rove` in each
// tenant it serves, holding the role that lets it check a password, read a
// person and their tenant. On an external roster the key the operator hands
// over says how (roster's `docs/apps.md`): a deployment key is answered as the
// holder each tenant nominated for it, a tenant key as the holder it hangs on.
// On the embedded one Rove writes that holder and its role itself.
//
// shale was here first (its `internal/identity`), and this is the same shape,
// cut to what Rove asks.
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
	"strings"
	"sync"
	"time"
	"uuid"

	"github.com/lesomnus/z"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	"github.com/lesomnus/payday/auth"
	"github.com/lesomnus/payday/config"
	"github.com/lesomnus/payday/pdid"
	"github.com/lesomnus/roster/rstr"
	rostercore "github.com/lesomnus/roster/server/core"
	"github.com/lesomnus/roster/server/front"
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
	// Key is the key Rove acts with on an external roster: `env:NAME`,
	// `file:PATH`, or the key itself. Its prefix says what it is, the way
	// roster reads it (roster's `docs/apps.md`):
	//
	//   - `rk_`, a deployment key a roster operator minted on roster's control
	//     plane (`roster control key add --allow /roster.NominationService/List
	//     rove`: the one method it holds as itself is the list of the tenants
	//     that nominated it). The tenants it serves are the ones
	//     that **nominated** it -- `roster app install --tenant <alias> rove` --
	//     and every call names its tenant beside the key with `roster-at`.
	//   - `rt_`, a tenant key: one tenant, the key's own.
	Key string `yaml:"key"`
	// Db is the embedded roster's database. Empty is `roster.db` in the
	// directory Rove's own SQLite file is in.
	Db config.DbConfig `yaml:"db"`

	// Directory is the roster tenant whose people are this deployment's
	// accounts (design 9.10, D27): one for the whole of a SaaS deployment, a
	// customer's own for a Dedicated one. Workspaces are Rove's tenants and
	// are not roster's at all. Empty is [DefaultDirectory] on the roster in
	// this process, and the key's own tenant for an `rt_`; an `rk_` serves
	// every tenant that nominated it, so which one holds the accounts is said
	// here.
	Directory string `yaml:"directory"`
}

// DefaultDirectory is the directory of the roster in this process when the
// configuration names none.
const DefaultDirectory = "rove"

// Directory is the roster tenant this deployment's accounts are in.
type Directory struct {
	Id    pdid.Id
	Alias string
	Name  string
}

// Embedded says whether roster runs in this process.
func (c Config) Embedded() bool { return c.Addr == "" }

// Agent is the alias of the holder Rove acts as in every tenant.
const Agent = "rove"

// RosterAdministrator is the holder roster makes every tenant with, so that
// somebody can administer it, with no way to sign in until it is given one.
const RosterAdministrator = rostercore.Administers

// AgentMethods is what that holder may call: checking a password, reading a
// person and their tenant, the addresses somebody checked for them -- which is
// whose an invitation is (`Store.Addresses`) -- and, for a tenant key, whose
// key it is. It is the `--role` an external roster installs Rove with:
//
//	roster app install --tenant acme --role /roster.VouchService/Verify,\
//	  /roster.HolderService/Get,/roster.TenantService/Get,/roster.MeService/Get,\
//	  /roster.EmailService/List,/roster.EmailService/Get rove
//
// An installation older than the addresses is installed again with this list;
// until it is, an account sees only the invitations it holds a link for.
//
// People are made, given passwords and suspended at roster, by whoever
// administers the tenant there; on the embedded roster Rove does those through
// the deployment's own door, which is not a role.
var AgentMethods = []string{
	"/roster.VouchService/Verify",
	"/roster.HolderService/Get",
	"/roster.TenantService/Get",
	"/roster.MeService/Get",
	"/roster.EmailService/List",
	"/roster.EmailService/Get",
}

// The prefixes a key is read by, the way roster reads them.
const (
	prefixDeploymentKey = "rk_"
	prefixTenantKey     = "rt_"
)

// ServedTtl is how long the tenants an `rk_` serves are believed before roster
// is asked again. A key cannot watch its nominations, so a tenant that
// nominates it is found at the next asking -- or at once when one of its people
// arrives, since a tenant that is not known is asked about again, at most once
// per [servedRetry].
const ServedTtl = time.Minute

// servedRetry is how soon roster is asked again about the tenants an `rk_`
// serves, after it was asked for any reason.
const servedRetry = 5 * time.Second

// Person is who roster said somebody is: the identifiers Rove's rows are
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
	// both, as roster gives it.
	ErrRefused = errors.New("refused")
	// ErrSecondFactor is a right password for somebody who has more to prove:
	// a second factor, which they prove at roster's issuer. A password alone
	// is never a sign-in for them.
	ErrSecondFactor = errors.New("a second factor is asked for: sign in through the issuer")
	// ErrNoTenant is a tenant this deployment does not serve: one that did not
	// nominate its key, or no such tenant on the embedded roster.
	ErrNoTenant = errors.New("no such tenant here")
	// ErrNoPerson is nobody by that name in a tenant this deployment serves.
	ErrNoPerson = errors.New("no such person")
	// ErrExternal is an operation only the embedded roster takes: on an
	// external one it is the tenant administrator's, at roster.
	ErrExternal = errors.New("people and tenants are made at roster, which is not this process")
	// ErrUnreachable is a roster that could not be asked. A session is not
	// served while it lasts: what roster would have said is not known.
	ErrUnreachable = errors.New("roster cannot be asked just now")
	// ErrNoDirectory is a directory that is not there yet: on the roster in
	// this process, before `rove init` or `rove identity migrate` made it.
	ErrNoDirectory = errors.New("there is no directory of accounts yet: `rove init` makes it")
)

// Locked is a refusal that says when the account opens again.
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

	// What roster said of each person a session names, and when.
	hmu  sync.Mutex
	held map[pdid.Id]held

	// The directory, once it was found.
	dmu sync.Mutex
	dir Directory

	// now is the clock, for the tests.
	now func() time.Time

	// What roster said about tenants, kept: for an `rk_` the tenants that
	// nominated it and when that was asked, for an `rt_` the key's own, and
	// for any tenant met the alias it goes by.
	tmu       sync.Mutex
	served    map[string]pdid.Id
	servedAt  time.Time
	asked     time.Time
	servedErr error
	own       string
	aliases   map[pdid.Id]string
}

// Open connects to roster, or builds and migrates the embedded one; Run serves
// its housekeeping. `stateDir` is where the embedded database goes when Db
// names none.
func Open(ctx context.Context, cfg Config, stateDir string, log *slog.Logger) (*Store, error) {
	if log == nil {
		log = slog.Default()
	}
	s := &Store{cfg: cfg, log: log, agents: map[string]bool{}, held: map[pdid.Id]held{}, aliases: map[pdid.Id]string{}, now: time.Now}
	if cfg.Embedded() {
		if cfg.Key != "" || cfg.CaFile != "" || cfg.Insecure {
			return nil, errors.New("auth.roster: key, ca_file and insecure are an external roster's, and addr names none")
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

		return s, nil
	}

	if cfg.Key == "" {
		return nil, errors.New("auth.roster.key: an external roster is reached with a key -- " + prefixDeploymentKey + " or " + prefixTenantKey)
	}
	v, err := ResolveRef(cfg.Key)
	if err != nil {
		return nil, fmt.Errorf("auth.roster.key: %w", err)
	}
	if !strings.HasPrefix(v, prefixDeploymentKey) && !strings.HasPrefix(v, prefixTenantKey) {
		return nil, fmt.Errorf("auth.roster.key: neither %s nor %s, which is how roster says what a key is", prefixDeploymentKey, prefixTenantKey)
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
	if strings.HasPrefix(s.key, prefixDeploymentKey) {
		log.Info("identity: roster", "addr", cfg.Addr, "key", "deployment key; the tenants that nominated it")
	} else {
		log.Info("identity: roster", "addr", cfg.Addr, "key", "tenant key; its own tenant")
	}

	return s, nil
}

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

// Run serves the embedded roster's housekeeping until ctx ends; on an external
// one it waits for ctx.
func (s *Store) Run(ctx context.Context) error {
	if s.em == nil {
		<-ctx.Done()
		return nil
	}

	return s.em.run(ctx)
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

// as is a context that calls roster as Rove's holder in a tenant: the `rove`
// holder the embedded roster has, the holder a tenant nominated for an `rk_`,
// or the holder a tenant key hangs on.
func (s *Store) as(ctx context.Context, tenant string) (context.Context, error) {
	if s.em != nil {
		if err := s.ensureAgent(ctx, tenant); err != nil {
			return nil, err
		}

		return auth.PlainProvider(front.TenantMark + tenant + "/" + Agent).Provide(ctx), nil
	}
	if strings.HasPrefix(s.key, prefixDeploymentKey) {
		ok, err := s.serves(ctx, tenant)
		if err != nil {
			return nil, err
		}
		if !ok {
			return nil, fmt.Errorf("%w: %s did not nominate this deployment's key", ErrNoTenant, tenant)
		}

		return s.at(ctx, front.TenantMark+tenant), nil
	}
	own, err := s.ownTenant(ctx)
	if err != nil {
		return nil, err
	}
	if own != tenant {
		return nil, fmt.Errorf("%w: %s, and this deployment's tenant key is @%s's", ErrNoTenant, tenant, own)
	}

	return auth.BearerProvider(s.key).Provide(ctx), nil
}

// at is a context whose calls carry the deployment key and say which tenant
// they are about: `roster-at`, which roster answers as the holder that tenant
// nominated for the key.
func (s *Store) at(ctx context.Context, at string) context.Context {
	return metadata.AppendToOutgoingContext(auth.BearerProvider(s.key).Provide(ctx), front.HeaderAt, at)
}

// serves says whether a tenant nominated this deployment's `rk_`, asking
// roster again when what is known is [ServedTtl] old, or when the tenant is not
// known and roster was not asked a moment ago.
//
// An answer that is out of date widens nothing: roster finds the nomination
// again on every call that names the tenant, and refuses one that has ended.
func (s *Store) serves(ctx context.Context, tenant string) (bool, error) {
	s.tmu.Lock()
	defer s.tmu.Unlock()
	now := s.now()
	_, ok := s.served[tenant]
	known := s.served != nil
	switch {
	case known && ok && now.Sub(s.servedAt) < ServedTtl:
		return true, nil
	case now.Sub(s.asked) < servedRetry:
		// Asked a moment ago, whatever it answered: that stands, so a roster
		// that is down is not asked once per call.
		if !known {
			return false, s.servedErr
		}

		return ok, nil
	}
	if err := s.nominations(ctx, now); err != nil {
		s.servedErr = err
		if !known {
			return false, err
		}
		s.log.WarnContext(ctx, "identity: roster did not say which tenants nominated this key; the last answer stands", "err", err.Error())

		return ok, nil
	}
	_, ok = s.served[tenant]

	return ok, nil
}

// nominations asks roster which tenants nominated this deployment's key, and
// what each is called. The tenant lock is held.
//
// `NominationService.List` asked as the key, naming no tenant, answers the
// key's own nominations and nobody else's; it is the one method the key holds
// as itself. A nomination names its tenant by identifier, and the alias is
// asked in that tenant, as the holder it nominated.
func (s *Store) nominations(ctx context.Context, now time.Time) error {
	s.asked = now
	as := auth.BearerProvider(s.key).Provide(ctx)
	var ids []pdid.Id
	after := ""
	for {
		res, err := rstr.NewNominationServiceClient(s.conn).List(as, rstr.NominationListRequest_builder{Size: 100, After: after}.Build())
		if err != nil {
			return fmt.Errorf("roster: the tenants that nominated this key: %w", err)
		}
		for _, n := range res.GetItems() {
			id, err := pdid.From(n.GetTenant().GetId())
			if err != nil {
				return fmt.Errorf("roster: a nomination's tenant: %w", err)
			}
			ids = append(ids, id)
		}
		if after = res.GetNext(); after == "" || len(res.GetItems()) == 0 {
			break
		}
	}

	served := make(map[string]pdid.Id, len(ids))
	for _, id := range ids {
		alias, ok := s.aliases[id]
		if !ok {
			v, err := rstr.NewTenantServiceClient(s.conn).Get(s.at(ctx, front.TenantMark+id.String()),
				rstr.TenantGetRequest_builder{Ref: rstr.TenantRef_builder{Id: id.Bytes()}.Build()}.Build())
			if err != nil {
				return fmt.Errorf("roster: tenant %s: %w", id, err)
			}
			alias = v.GetAlias()
			s.aliases[id] = alias
		}
		served[alias] = id
	}
	s.served, s.servedAt = served, now

	return nil
}

// ownTenant is the tenant an `rt_` is a key of, asked once: roster's answer to
// who the key is (`MeService.Get`), and that tenant's alias.
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
	s.aliases[id] = s.own

	return s.own, nil
}

// ensureAgent writes the `rove` holder, its role and the binding into a tenant
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

// Verify checks an account's password with roster and answers who they are.
// `login` is their alias in the directory, or -- with an `@` in it -- the
// address they are reached at.
func (s *Store) Verify(ctx context.Context, login, password string) (Person, error) {
	as, d, err := s.inDirectory(ctx)
	if err != nil {
		return Person{}, err
	}
	who := rstr.VouchWho_builder{Tenant: d.Alias}
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
		case res.GetLockedUntil() != nil:
			return Person{}, Locked{Until: res.GetLockedUntil().AsTime()}
		}

		return Person{}, ErrRefused
	}
	id, err := pdid.From(res.GetHolder())
	if err != nil {
		return Person{}, err
	}

	return s.person(as, rstr.HolderRef_builder{Id: id.Bytes()}.Build())
}

// Lookup is an account by its alias, with nothing checked: for a caller some
// other credential already vouched for.
func (s *Store) Lookup(ctx context.Context, alias string) (Person, error) {
	as, d, err := s.inDirectory(ctx)
	if err != nil {
		return Person{}, err
	}

	return s.person(as, rstr.HolderRef_builder{
		Slug: rstr.HolderRefBySlug_builder{Alias: z.Ptr(alias), Tenant: rstr.TenantRef_builder{Id: d.Id.Bytes()}.Build()}.Build(),
	}.Build())
}

// ById is the account an issuer's `sub` names: roster's `sub` is a
// `Holder.id`, looked up in the directory. Somebody the directory does not
// have is nobody here, whatever the issuer vouched for.
func (s *Store) ById(ctx context.Context, sub string) (Person, error) {
	id, err := uuid.Parse(sub)
	if err != nil {
		return Person{}, fmt.Errorf("%w: %q is not roster's identifier", ErrNoPerson, sub)
	}
	as, _, err := s.inDirectory(ctx)
	if err != nil {
		return Person{}, err
	}

	return s.person(as, rstr.HolderRef_builder{Id: id[:]}.Build())
}

// Addresses is an account's addresses that somebody checked -- roster holds
// them as verified, by a link it sent or by the provider the account signed in
// through -- as roster stores them, folded.
//
// An address nobody checked is not one of them, and that is the whole of the
// method. Any directory's administrator may put any address on a user of
// their own, and a provider passes it on (nOAuth); roster keeps it, with
// nothing claimed, and an invitation found by it would go to whoever typed it.
func (s *Store) Addresses(ctx context.Context, account pdid.Id) ([]string, error) {
	as, _, err := s.inDirectory(ctx)
	if err != nil {
		return nil, err
	}
	c := rstr.NewEmailServiceClient(s.conn)
	vs, err := c.List(as, rstr.EmailListRequest_builder{
		Filters: []*rstr.EmailFilter{rstr.EmailFilter_builder{
			Holder: rstr.HolderRef_builder{Id: account.Bytes()}.Build(),
		}.Build()},
	}.Build())
	if err != nil {
		return nil, fmt.Errorf("roster: %w", err)
	}
	var out []string
	for _, v := range vs.GetItems() {
		// Listed narrow and read whole: what a list answers is not the stamp.
		e, err := c.Get(as, rstr.EmailGetRequest_builder{
			Ref:    rstr.EmailRef_builder{Id: v.GetId()}.Build(),
			Select: rstr.EmailSelect_builder{Address: z.Ptr(true), DateVerified: z.Ptr(true)}.Build(),
		}.Build())
		if err != nil {
			return nil, fmt.Errorf("roster: %w", err)
		}
		if e.HasDateVerified() {
			out = append(out, strings.ToLower(strings.TrimSpace(e.GetAddress())))
		}
	}
	return out, nil
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

// tenant is a tenant at roster, by alias, asked as Rove's holder there.
func (s *Store) tenant(ctx context.Context, alias string) (id pdid.Id, name string, err error) {
	as, err := s.as(ctx, alias)
	if err != nil {
		return pdid.Nil, "", err
	}
	v, err := rstr.NewTenantServiceClient(s.conn).Get(as, rstr.TenantGetRequest_builder{
		Ref: rstr.TenantRef_builder{Alias: z.Ptr(alias)}.Build(),
	}.Build())
	if err != nil {
		if status.Code(err) == codes.NotFound {
			return pdid.Nil, "", fmt.Errorf("%w: %s", ErrNoTenant, alias)
		}

		return pdid.Nil, "", fmt.Errorf("roster: %w", err)
	}
	id, err = pdid.From(v.GetId())
	if err == nil {
		s.tmu.Lock()
		s.aliases[id] = alias
		s.tmu.Unlock()
	}

	return id, v.GetName(), err
}

// directoryAlias is the alias the directory goes by: what the configuration
// says, and what the key is.
func (s *Store) directoryAlias(ctx context.Context) (string, error) {
	switch {
	case s.em != nil:
		if s.cfg.Directory != "" {
			return s.cfg.Directory, nil
		}

		return DefaultDirectory, nil
	case strings.HasPrefix(s.key, prefixDeploymentKey):
		if s.cfg.Directory == "" {
			return "", errors.New("auth.roster.directory: a deployment key serves every tenant that nominated it, and which of them holds this deployment's accounts is said here")
		}

		return s.cfg.Directory, nil
	default:
		own, err := s.ownTenant(ctx)
		if err != nil {
			return "", err
		}
		if s.cfg.Directory != "" && s.cfg.Directory != own {
			return "", fmt.Errorf("auth.roster.directory: @%s, and this deployment's tenant key is @%s's", s.cfg.Directory, own)
		}

		return own, nil
	}
}

// Directory is the roster tenant this deployment's accounts are in, found once
// and kept. [ErrNoDirectory] on the roster in this process before anything
// made it.
func (s *Store) Directory(ctx context.Context) (Directory, error) {
	s.dmu.Lock()
	defer s.dmu.Unlock()
	if s.dir.Alias != "" {
		return s.dir, nil
	}
	alias, err := s.directoryAlias(ctx)
	if err != nil {
		return Directory{}, err
	}
	id, name, err := s.tenant(ctx, alias)
	if err != nil {
		if s.em != nil && errors.Is(err, ErrNoTenant) {
			return Directory{}, fmt.Errorf("%w (@%s)", ErrNoDirectory, alias)
		}

		return Directory{}, err
	}
	s.dir = Directory{Id: id, Alias: alias, Name: name}

	return s.dir, nil
}

// inDirectory is a context that calls roster as Rove's holder in the
// directory, and the directory.
func (s *Store) inDirectory(ctx context.Context) (context.Context, Directory, error) {
	d, err := s.Directory(ctx)
	if err != nil {
		return nil, Directory{}, err
	}
	as, err := s.as(ctx, d.Alias)
	if err != nil {
		return nil, Directory{}, err
	}

	return as, d, nil
}

// Standing is what roster says of somebody now: when everything issued to them
// before stopped being good, when they were suspended, when they were erased;
// zero for not.
type Standing struct {
	Holder, Tenant                pdid.Id
	Invalidated, Disabled, Erased time.Time
}

// Since is the latest of the three: what was issued before it, or at all when
// they are suspended or erased, is not good. Zero is good standing.
func (v Standing) Since() time.Time {
	t := v.Invalidated
	for _, u := range []time.Time{v.Disabled, v.Erased} {
		if u.After(t) {
			t = u
		}
	}

	return t
}

// Good says whether something issued at `at` is still good: a session that
// began then, say.
func (v Standing) Good(at time.Time) bool {
	return v.Disabled.IsZero() && v.Erased.IsZero() && !at.Before(v.Invalidated)
}

// StandingOf is what roster says of an account now. ErrNoPerson is somebody
// roster does not have any more; anything else is a roster that could not be
// asked. It is what a session is held to on every call it makes.
func (s *Store) StandingOf(ctx context.Context, account pdid.Id) (Standing, error) {
	as, d, err := s.inDirectory(ctx)
	if err != nil {
		return Standing{}, err
	}
	v, err := rstr.NewHolderServiceClient(s.conn).Get(as, rstr.HolderGetRequest_builder{
		Ref:    rstr.HolderRef_builder{Id: account.Bytes()}.Build(),
		Select: rstr.HolderSelect_builder{All: z.Ptr(true)}.Build(),
	}.Build())
	if err != nil {
		if status.Code(err) == codes.NotFound {
			return Standing{}, ErrNoPerson
		}

		return Standing{}, fmt.Errorf("roster: %w", err)
	}
	st := Standing{Holder: account, Tenant: d.Id}
	if t := v.GetDateInvalidated(); t != nil {
		st.Invalidated = t.AsTime()
	}
	if t := v.GetDateDisabled(); t != nil {
		st.Disabled = t.AsTime()
	}
	if t := v.GetDateErased(); t != nil {
		st.Erased = t.AsTime()
	}

	return st, nil
}

// HeldTtl is how long what roster says of a person is believed before it is
// asked again: how late a suspension, an erasure or signing out everywhere
// reaches a session already open.
const HeldTtl = 30 * time.Second

// heldRetry is how long a roster that could not be asked is not asked again,
// so that one that is down is not asked once per call.
const heldRetry = 5 * time.Second

type held struct {
	st  Standing
	err error
	at  time.Time
}

// Held says whether a session that began at `began` is still good, by what
// roster says of the account it names: ErrRefused when they were suspended,
// erased or signed out everywhere since -- or are not there at all -- and
// ErrUnreachable when roster could not be asked, which is a refusal too.
func (s *Store) Held(ctx context.Context, holder pdid.Id, began time.Time) error {
	now := s.now()
	s.hmu.Lock()
	v, ok := s.held[holder]
	s.hmu.Unlock()
	// Somebody roster does not have is an answer, and kept as long as one.
	known := v.err == nil || errors.Is(v.err, ErrNoPerson)
	fresh := ok && (known && now.Sub(v.at) < HeldTtl || !known && now.Sub(v.at) < heldRetry)
	if !fresh {
		st, err := s.StandingOf(ctx, holder)
		v = held{st: st, err: err, at: now}
		s.hmu.Lock()
		s.held[holder] = v
		s.hmu.Unlock()
	}

	switch {
	case errors.Is(v.err, ErrNoPerson):
		return fmt.Errorf("%w: roster has no such person", ErrRefused)
	case v.err != nil:
		return fmt.Errorf("%w: %w", ErrUnreachable, v.err)
	case !v.st.Good(began):
		return fmt.Errorf("%w: suspended, erased or signed out everywhere since", ErrRefused)
	}

	return nil
}

// Recheck drops what is known of a person, for a change made here to be seen
// on the next call rather than after [HeldTtl].
func (s *Store) Recheck(holder pdid.Id) {
	s.hmu.Lock()
	delete(s.held, holder)
	s.hmu.Unlock()
}
