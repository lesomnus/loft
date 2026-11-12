package cmd

import (
	"context"
	"crypto/rand"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/lesomnus/otx/log"
	"github.com/protobuf-orm/ent/dialect"
	entsql "github.com/protobuf-orm/ent/dialect/sql"
	"google.golang.org/grpc"

	"github.com/lesomnus/payday/access"
	"github.com/lesomnus/payday/audit"
	"github.com/lesomnus/payday/auth"
	"github.com/lesomnus/payday/auth/authsession"
	"github.com/lesomnus/payday/config"
	"github.com/lesomnus/payday/entitle"
	"github.com/lesomnus/payday/gate"
	"github.com/lesomnus/payday/grpcx"
	"github.com/lesomnus/payday/meter"
	"github.com/lesomnus/payday/pdid"
	"github.com/lesomnus/payday/spin"
	"github.com/lesomnus/payday/trail"
	"github.com/lesomnus/payday/watch"
	"github.com/lesomnus/payday/web"

	app "github.com/lesomnus/loft"
	"github.com/lesomnus/loft/internal/ent"
	"github.com/lesomnus/loft/internal/identity"
	"github.com/lesomnus/loft/server/bare"
	"github.com/lesomnus/loft/server/clientaddr"
	"github.com/lesomnus/loft/server/clilogin"
	"github.com/lesomnus/loft/server/closing"
	"github.com/lesomnus/loft/server/csp"
	"github.com/lesomnus/loft/server/domain"
	"github.com/lesomnus/loft/server/levyan"
	"github.com/lesomnus/loft/server/lifecycle"
	"github.com/lesomnus/loft/server/mail"
	"github.com/lesomnus/loft/server/offboard"
	"github.com/lesomnus/loft/server/opensearch"
	"github.com/lesomnus/loft/server/payment"
	"github.com/lesomnus/loft/server/pd"
	"github.com/lesomnus/loft/server/policy"
	"github.com/lesomnus/loft/server/retention"
	"github.com/lesomnus/loft/server/session"
	"github.com/lesomnus/loft/server/sso"
	"github.com/lesomnus/loft/server/storage"
	"github.com/lesomnus/loft/server/tenancy"
)

// Server is a built app: the database it runs on and the two stacks it answers
// through.
type Server struct {
	Db  *sql.DB
	Ent *ent.Client

	// Drv is what the client was built on, kept because a transaction is begun
	// on a driver and a `*ent.Client` does not hand out the one it holds: the
	// deployment's own work that is one transaction -- `init`, a sweep, an
	// operator closing or restoring a workspace -- begins it here, and
	// `migrate` makes the schema on it.
	Drv dialect.Driver

	// Dialect is what that driver speaks, which `Open` worked out and nothing
	// else can. It is kept because the guard below needs it, and re-deriving it
	// from the configuration would be a second answer to a question already
	// answered.
	Dialect string

	// Watch is what a change is published to once the call that made it has
	// answered. The broker is named rather than defaulted: the one that
	// publishes in this process is right for one replica and **silently wrong**
	// for two, since a subscriber on one never hears about a write on another.
	Watch *watch.Watch

	// Walled is what a caller reaches, and Ungated is what the deployment does
	// its own work through -- putting the first tenant there, working out who
	// is calling. Neither is a privilege anybody holds: the second is a server
	// instance somebody was handed, so going around the wall is a line of
	// wiring a reader can find rather than a rule that opens up whenever
	// nobody is asking.
	Walled  app.Server
	Ungated app.Server

	// Base is Ungated without the domain layer: what puts up a tenant and the
	// first person in it, before there is anybody to act as.
	Base app.Server

	// Deps is what the domain layer of both stacks shares: the clock, the
	// files, how labels are addressed.
	Deps *domain.Deps

	// Trail is the trail's policy with every tenant's windows and legal holds
	// answered, whether or not `app.retention.apply` lets a pass destroy by
	// them: what an erasure, an export and a purge go by.
	Trail trail.Policy

	// Contents is what the trail keeps of each kind's writes, the digest's
	// key with it (`audit.content`, `audit.digest.key`): what a row written
	// beneath the servers on purpose is put on the trail with, as the act it
	// is (`pd.Record`). [Build] has read it, and builds nothing without it.
	// [Open] reads it the first time it is asked and not before, so that a
	// migration with no row to put on the trail opens no key: the key that
	// cannot be read is said then, and stops the migration that needed it,
	// and none configured records the rows with no digest.
	Contents func() (audit.Contents, error)

	// Identity is roster, beside this process or inside it: who people are,
	// and the password they sign in with (design 9.10).
	Identity *identity.Store

	// Sessions mints and reads the cookie a browser signs in with.
	Sessions *authsession.Sessions

	// Lifecycle is what roster's word about somebody does here: their
	// sessions ended, their membership leaving (`server/lifecycle`).
	Lifecycle lifecycle.Lifecycle

	// Logins is a terminal signed in by somebody approving it in a browser
	// (`loft auth login`, `server/clilogin`).
	Logins *clilogin.Logins

	// Members is how somebody roster vouched for comes to be a member here:
	// the first time they sign in (`tenancy.Members`, D85).
	Members tenancy.Members

	// Closing is what becomes of a closed workspace and of a workspace's
	// address (D29, `server/closing`).
	Closing closing.Closing

	// Meters is what each workspace holds, measured every day and before it
	// is destroyed, and handed to levyan (`app.billing.levyan`, payday#186,
	// D59); and Levyan the connection the readings go over. Both are nil for
	// a deployment that bills nobody, which measures nothing.
	Meters *meter.Reader
	Levyan *levyan.Client

	// Follower keeps this deployment's copy of what levyan grants each
	// workspace, from levyan's `payday.GrantService`, under the lease
	// (payday#185, design 7.5): what a restricted workspace is held to
	// (`domain.Grows`). Notices is what levyan is to be told became of a
	// workspace, written beside the change that tells it, and Teller the loop
	// that tells it (design 7.8). All three are nil for a deployment that
	// bills nobody, which keeps no copy, restricts nothing and tells nothing.
	Follower *entitle.Follower
	Notices  *levyan.Notices
	Teller   *levyan.Teller

	// Payment is how a workspace is paid for, relayed to levyan as the
	// product (design 7.9, `server/payment`): the workspace's own calls. Served
	// whatever is configured, and answering every method FailedPrecondition
	// while no levyan is named.
	Payment payment.Service

	// Search is the OpenSearch cluster the index of assets is kept on, and
	// nil when the configuration names none (D31, `server/opensearch`).
	Search *opensearch.Client

	// Sso is this deployment as a relying party of roster's issuer, and what
	// the sign-in page is told about the ways in.
	Sso *sso.Rp

	// Auth is how a credential is read, and nothing that faces anybody is
	// [auth.Plain] -- which believes what the caller writes about themselves.
	// Replacing it is the first thing this app has to do that payday cannot do
	// for it.
	//
	// It is set after [Build] rather than being one of its arguments, because
	// what reads a credential is often built from what [Build] made: a session
	// handler needs a store, and a store is a table in this database.
	Auth auth.Handler

	// Policy is what a caller may do and which tenants they may see, and nil is
	// the answer payday gives on its own: their own tenant and nothing more.
	//
	// A field beside [Server.Auth] because they are the pair -- who is calling,
	// and what that means here -- and are installed on the one chain every
	// call goes through (`gate.Interceptor`).
	//
	// It is what a second binary is built from. A deployment that wants an
	// operator's path serves this app again with a policy that answers
	// differently, on an address only an operator can reach, rather than
	// putting a rule in here that says some row is special.
	Policy gate.Policy

	// Access is the access record's writer (payday#111, loft#136): what a
	// read's row waits in to be written with the others of its second, and
	// what writes a download's before it is answered. Its loop is in
	// [Server.Spin], each process's own.
	Access *access.Writer

	// Spin is whatever this deployment has to run besides answering requests.
	// It is a slice rather than a method because a server with nothing to run
	// should write nothing at all; see `payday/spin`.
	Spin []any
}

// Open opens what holds a deployment's state and nothing that serves it: the
// database, and the roster in this process, which brings its own database
// along as it opens. No files, no trail, and no key until one is asked for
// ([Server.Contents]).
//
// It is what `loft migrate` stands on, which runs where none of those are to
// be had. In Kubernetes that is before a pod serves, while the volumes it will
// serve from may still be held by the one before it (deploy/k8s).
func Open(ctx context.Context, c Config) (*Server, error) {
	db, dialect, err := c.Db.Open(ctx)
	if err != nil {
		return nil, err
	}
	people, err := identity.Open(ctx, c.Auth.Roster, c.StateDir(), log.From(ctx))
	if err != nil {
		db.Close()
		return nil, err
	}
	drv := entsql.OpenDB(dialect, db)

	// What the domain layer is told of the deployment that opens nothing: a
	// workspace that says nothing of where it is is told in the
	// deployment's zone.
	locale := domain.Locale{Timezone: c.App.Locale.Timezone, Currency: c.App.Locale.Currency}
	if err := locale.Check(); err != nil {
		people.Close()
		db.Close()
		return nil, fmt.Errorf("app.locale.%w", err)
	}
	s := &Server{
		Db: db, Ent: ent.NewClient(ent.Driver(drv)), Drv: drv, Dialect: dialect, Identity: people,
		Deps:     &domain.Deps{Locale: locale, NoShowAfter: c.App.NoShowAfter, Links: c.App.Links()},
		Contents: sync.OnceValues(c.Audit.Contents),
	}
	return s, nil
}

// Build opens the database and stacks the servers.
//
// The two hooks are the whole of what payday puts in the write and read paths,
// and both come out of what the schema declared: [pd.Minter] stamps a new row
// with the domain of its entity and refuses one of another, [pd.Wall] narrows
// every read to the tenants the caller may see.
func Build(ctx context.Context, c Config) (*Server, error) {
	db, dialect, err := c.Db.Open(ctx)
	if err != nil {
		return nil, err
	}

	// The ent client is the app's to build, which is why config hands back a
	// *sql.DB and the dialect rather than a client: the client is generated
	// into this app from this app's schema, and payday has no name for it.
	drv := entsql.OpenDB(dialect, db)
	client := ent.NewClient(ent.Driver(drv))

	// The server that talks to the database, twice: once as it is, and once
	// with the wall on it.
	//
	// Two things are said to it rather than to the stack, and for the same
	// reason -- both are about the statement that runs. The trail is kept by
	// the servers that do the writing, since every RPC that changes anything
	// has to report itself from inside the transaction that changes it. The
	// wall is a predicate and a predicate belongs in the WHERE.
	b, err := c.Watch.Build(ctx, c.Db)
	if err != nil {
		db.Close()
		return nil, err
	}

	w := watch.New(b)

	// The recorders, in the order they are told. The trail is required -- a
	// write it could not account for is undone -- and the rest say for
	// themselves whether they are; see `bare.Recorders`.
	// Every write is told to these, in this order, inside the transaction that
	// makes it. It is one value rather than the option given three times
	// because `WithRecorder` refuses to be given twice: neither losing one nor
	// quietly appending in call order is a thing a framework should decide, so
	// the order is written here where it can be read.
	//
	// What the trail keeps of each kind's writes is the deployment's to say,
	// `audit.content` (payday#92) -- read here, where the trail is wired, so
	// that a setting nobody handed over is not a setting that does nothing.
	content, err := c.Audit.Contents()
	if err != nil {
		db.Close()
		return nil, err
	}
	// The access record's after the publish (loft#136): what it writes at the
	// commit is of the people the call's writes wrote, so it is told of each
	// and writes once, in the same transaction.
	rec := bare.Recorders{pd.Recorder(content), pd.WatchRecorder(w), pd.AccessRecorder()}

	// The search engine, when one is named (D31). What makes a document of
	// its index stale is written down with the write itself, and a loop below
	// makes it again.
	engine, err := opensearch.New(c.App.Search.Opensearch, identity.ResolveRef)
	if err != nil {
		db.Close()
		return nil, err
	}
	if engine != nil {
		rec = append(rec, opensearch.Recorder())
		log.From(ctx).InfoContext(ctx, "search: OpenSearch answers, from the indexes of assets, parties and models", "assets", engine.Alias(opensearch.Assets))
	}
	// What levyan is to be told became of a workspace, for a deployment
	// that bills (design 7.8).
	var notices *levyan.Notices
	if c.App.Billing.Levyan.On() {
		notices = &levyan.Notices{}
	}
	if c.Watch.Outbox {
		// Last, so that a queue this could not be written to undoes a write
		// which the trail and the in-process publish have already agreed to
		// rather than the other way round. It makes no difference to what is
		// committed -- either way nothing is -- and it makes the log read in
		// the order things were tried.
		rec = append(rec, pd.OutboxRecorder())
	}

	deps, err := depsOf(c)
	if err != nil {
		db.Close()
		return nil, err
	}
	// What the domain writes beneath the servers is on the trail as its act,
	// kept as the recorder keeps a write (payday#108).
	deps.Audit = content
	if notices != nil {
		notices.Now = deps.Clock
		// And what became of a workspace -- closed, restored -- written by
		// the domain layer in the transaction that does it, whoever does it
		// (design 7.8).
		deps.Closings = notices
	}

	// Who people are is roster's: one of its own, or one in this process on a
	// database of its own.
	people, err := identity.Open(ctx, c.Auth.Roster, c.StateDir(), log.From(ctx))
	if err != nil {
		db.Close()
		return nil, err
	}
	deps.People = people
	// And what they may call is roster's too (D89): the domain layer asks it
	// of a permission finer than a method, as the gate does of the method.
	deps.Reaches = people.Reach()

	// One clock for every stamp, the domain layer's and the servers' alike.
	opts := []bare.Option{bare.WithMinter(pd.Minter()), bare.WithRecorder(rec), bare.WithClock(deps.Clock)}

	sink, err := pd.NewSink(client, opts...)
	if err != nil {
		people.Close()
		db.Close()
		return nil, err
	}

	// The wall. Nothing narrows a tenant's reads to less of its history than
	// it keeps (D36).
	walled, err := pd.NewSink(client, append(opts, bare.WithScope(pd.Wall()))...)
	if err != nil {
		people.Close()
		db.Close()
		return nil, err
	}

	// The stack a caller reaches. The domain layer is outermost, **above**
	// the gate: every write it makes on a caller's behalf goes through the
	// same edge checks the caller's own would (design D20). The policy is
	// asked once, about the method the caller named, before any of this.
	stacked, err := app.Build(walled.WithWatch(w), pd.AuditBuild(), pd.GateBuild(), domain.Build(drv, deps))
	if err != nil {
		people.Close()
		db.Close()
		return nil, err
	}

	// And the same servers with no wall and no gate, which is what the
	// deployment does its own work through. It is not a privilege anybody
	// holds: it is an instance somebody was handed, so going around the wall
	// is a line of wiring a reader can find rather than a rule that opens up
	// whenever nobody is asking.
	base, err := app.Build(sink.WithWatch(w), pd.AuditBuild())
	if err != nil {
		people.Close()
		db.Close()
		return nil, err
	}
	ungated := domain.New(base, drv, deps)

	s := &Server{Db: db, Ent: client, Drv: drv, Dialect: dialect, Watch: w, Walled: stacked, Ungated: ungated, Base: base, Deps: deps, Identity: people, Search: engine, Notices: notices}
	s.Contents = func() (audit.Contents, error) { return content, nil }
	deps.Finder = engine
	// Who somebody roster vouched for is here: a member, made the first time
	// they sign in (D85), and taken up again by one ended here (D88).
	s.Members = tenancy.Members{Base: base, Drv: drv, People: people, Audit: content}

	// Who is calling is a session cookie, held on every call to what roster
	// says of its person, and what they may call is what roster says of them
	// too, asked at every call (`server/policy`, D89).
	store := session.Store{Db: client, People: people, Key: session.KeyFrom(deps.Sign.Key)}
	opts2 := []authsession.Option{
		authsession.WithIdle(or(c.App.Session.Idle, 24*time.Hour)),
		authsession.WithLifetime(or(c.App.Session.Lifetime, 7*24*time.Hour)),
	}
	if c.App.Public().Scheme != "https" {
		opts2 = append(opts2, authsession.Insecure())
	}
	s.Sessions = authsession.New(store, opts2...)
	s.Auth = s.Sessions.Handler()
	s.Policy = policy.Policy{Reach: people.Reach()}
	cookie := authsession.DefaultCookie
	if c.App.Public().Scheme != "https" {
		cookie = authsession.InsecureCookie
	}
	s.Logins = &clilogin.Logins{
		Db: client, Sessions: s.Sessions, Store: store, Cookie: cookie,
		Idle: or(c.App.Session.Idle, 24*time.Hour), Lifetime: or(c.App.Session.Lifetime, 7*24*time.Hour),
		App: c.App.App(), Audit: content,
	}
	s.Lifecycle = lifecycle.Lifecycle{
		Db: client, Sessions: store, Base: base,
		People: people, Deps: deps, Drv: drv, Grace: c.App.Members.Grace(),
	}

	// Signing in through roster's issuer. With no password beside it, an
	// issuer is the only way in, so a deployment that turns passwords off and
	// names none is one nobody could sign in to.
	if c.Auth.SsoOnly && !c.Auth.Sso.On() {
		people.Close()
		db.Close()
		return nil, errors.New("auth.sso_only: with no auth.sso.issuer, nobody could sign in")
	}
	if c.Auth.Sso.On() && people.Embedded() {
		log.From(ctx).WarnContext(ctx, "auth.sso: the roster in this process has no issuer of its own; the issuer named has to vouch for people in its tenant")
	}
	if s.Sso, err = sso.New(c.Auth.Sso, c.App.App(), !c.Auth.SsoOnly, s.Sessions, people, s.Members); err != nil {
		people.Close()
		db.Close()
		return nil, err
	}
	// Who read or wrote whose personal data (loft#136). A read's row is
	// written a second later with the others, by a loop each process runs for
	// itself and that writes what it holds once more as the process stops.
	s.Access = &access.Writer{Sink: pd.AccessSink(client)}
	s.Spin = append(s.Spin, spin.Called("loft.access", s.Access))

	// What happens because time passed: holds that ran out, rooms nobody came
	// to, loans that are late.
	s.Spin = append(s.Spin,
		domain.Sweeper{Server: base, Drv: drv, Deps: deps, Every: c.App.Sweep},
		// What the notifications worth a mailbox say, sent after the
		// operation that made them (`app.mail`; nothing without a server).
		domain.Mailer{Drv: drv, Deps: deps},
		spinEvery("loft.sessions", time.Hour, store.Sweep),
		spinEvery("loft.sign-in-failures", session.Window, session.Failures{Db: client}.Sweep),
		spinEvery("loft.cli-logins", time.Hour, s.Logins.Sweep),
		// Closed workspaces whose window is over, destroyed unless held.
		// Read when it runs: s.Closing is put together after the trail.
		spinEvery("loft.closing", or(c.App.Workspaces.Every, time.Hour), func(ctx context.Context) error { return s.Closing.Sweep(ctx) }),
	)
	// The roster in this process's own housekeeping, loop by loop as roster
	// names them (`roster.*`); nothing, for one of its own. Each takes the
	// lease its name says, and its access record's writer runs in every
	// process as loft's does.
	s.Spin = append(s.Spin, people.Spinners()...)
	if !people.Embedded() {
		// What roster says of somebody as it says it: sessions that end
		// when roster ends them rather than at their next call, and a
		// member erased there beginning to leave. The roster in this process has
		// no stream, and every change to its people is made from here.
		//
		// Each process holds a stream of its own: what roster says is also
		// what each process's own answers about somebody are dropped by
		// (roster's `standing`, `docs/relying-app.md`), and what it writes
		// here comes to the same thing written twice.
		s.Spin = append(s.Spin, spin.InEveryProcess(people.Accounts().Follower("loft.roster-stream", s.Lifecycle.Apply)))
		// And a second stream, for what somebody may call (roster's
		// `reach`, `SYNC_REASON_REACH`): a role bound, widened, narrowed or
		// taken away at roster reaches the next call here at once rather
		// than once what was kept is half a minute old. Every process's own,
		// since what it drops is what that process kept; the roster in this
		// process has none, and a role Loft binds there is dropped by
		// `identity.Store.Bind`.
		s.Spin = append(s.Spin, spin.InEveryProcess(people.Reach().Follower("loft.roster-reach")))
	}
	// And the members still erased once roster's grace is over, leaving.
	s.Spin = append(s.Spin, spinEvery("loft.departures", c.App.Members.Interval(), s.Lifecycle.Sweep))
	// And every member asked about again, for an erasure the stream did not
	// carry because this was not listening (design 9.10).
	if d := c.App.Members.ReconcileEvery(); d > 0 {
		s.Spin = append(s.Spin, spinEvery("loft.reconcile", d, s.Lifecycle.Reconciling))
	}

	// What the keep windows no longer reach, taken out of the history (design
	// 8.2) -- only when the deployment has said a window may destroy.
	if c.App.Retention.Apply {
		s.Spin = append(s.Spin, domain.Expirer{Server: base, Drv: drv, Deps: deps, Every: c.App.Retention.Every})
	}
	if s.Search != nil {
		// What the recorder wrote down, into the index (D31).
		s.Spin = append(s.Spin, opensearch.Indexer{Client: s.Search, Db: client, Now: deps.Clock})
	}
	if c.Watch.Outbox && b != nil {
		// The loop that makes an event durable. It is not a layer and not a
		// method on any server -- `spin.Run` finds it in whatever is handed
		// over, which is what keeps a server that has no background work from
		// carrying an empty method saying so.
		s.Spin = append(s.Spin, pd.Drain(client, b, c.Watch.Every()))
	}

	// And the trail's retention, which is the one loop here that is a
	// **mechanism** rather than a tidy-up: nothing else applies a window, so an
	// outage of it is a deployment keeping records it said it would not.
	//
	// The policy is resolved and checked **here**, where a refusal stops the
	// process, rather than at the first pass a day later -- a `retain` with
	// nowhere to put what leaves it is a configuration that works and destroys
	// the evidence.
	//
	// Configured nowhere, this does nothing and the trail is kept forever. That
	// is the default because the alternative is a version upgrade deciding how
	// long somebody's evidence lasts; see `audit:` in the configuration file.
	p, err := c.Audit.Policy()
	if err != nil {
		db.Close()

		return nil, err
	}

	// And the turns a pass takes with whatever erases from the trail -- a
	// person pseudonymized, a workspace destroyed -- on the deployment's lease
	// (`trail.Policy.Lease`, payday#115), so that neither undoes the other: a
	// pass writing back into the archive what an erasure had just taken out.
	// Its own, and not the one the loops are run under: a loop's lease is which
	// process runs it, and the trail's turn is a lock any process takes for a
	// moment. A process that runs no loops (`none`) has nothing to take turns
	// on, and its policy says so where it is logged.
	if c.Spin.Lease != "" && c.Spin.Lease != config.LeaseNone {
		if p.Lease, err = c.Spin.Build(ctx, c.Db); err != nil {
			db.Close()

			return nil, err
		}
	}

	// And per tenant, from its own settings (D36): the kinds that are history,
	// whose trail lasts as long as the history does, and the access record
	// (#144); a legal hold holds all of it. Asked when the deployment said a
	// window may destroy, or keeps a window of its own -- since a tenant asked
	// turns the sweep on, a deployment that configured no `audit:` asks
	// nobody. With the windows off a workspace is answered only where it keeps
	// longer than the deployment would (`retention.Trail`).
	if c.App.Retention.Apply || p.On() {
		p.Tenants = retention.Trail(client, p, RetentionDefaults(c, p))
	}
	// And the domain's, which a workspace's own settings are read and held to
	// by: the deployment's access record and its floor beside the grace.
	deps.Retention = RetentionDefaults(c, p)

	// What the trail kept of a person, forgotten in the database and in the
	// archive both, when they are pseudonymized. Every tenant's legal holds
	// are asked whether or not the windows may destroy: a hold outweighs an
	// erasure either way.
	s.Trail = p
	s.Trail.Tenants = retention.Trail(client, p, RetentionDefaults(c, p))
	// After the trail's policy, which a destroy purges by.
	s.Closing = closing.Closing{
		Db: client, Offboard: s.Offboard(c), Deps: deps,
		// A workspace destroyed, told to levyan before anything of it is
		// taken (design 7.8); nil for a deployment that bills nobody.
		Notices: s.Notices,
	}

	// What each workspace holds, measured and handed to levyan, which bills
	// by it (design 7, D59): when the deployment names one, and nothing at
	// all when it does not. The meters the schema declares
	// (`proto/loft/meter.proto`) and the one it cannot, how long a workspace
	// keeps its history by the window in force -- after the trail's policy,
	// whose grace that is. A loop under the lease measures every workspace at
	// each midnight UTC and as it starts, and a workspace is measured once
	// more before it is destroyed, by the sweep or an operator's hand -- as
	// well as that can be done, since a destruction waits for no billing
	// service (`closing.Destroy`).
	if c.App.Billing.Levyan.On() {
		if s.Levyan, err = levyan.Dial(c.App.Billing.Levyan, identity.ResolveRef); err != nil {
			people.Close()
			db.Close()

			return nil, err
		}
		s.Meters = &meter.Reader{
			Tenants: pd.MeterTenants(client),
			Meters:  append(pd.Meters(client), retention.Meter(client, deps.Retention, deps.Clock)),
			Sink:    s.Levyan,
		}
		s.Closing.Meters = s.Meters
		s.Spin = append(s.Spin, s.Meters)
		log.From(ctx).InfoContext(ctx, "billing: what each workspace holds is measured and handed to levyan", "addr", c.App.Billing.Levyan.Addr)

		// What levyan grants each workspace, copied into this database by
		// payday's follower over the same connection, under the lease
		// (`payday.entitle.follow`, design 7.5): read in the transaction of
		// a write a restricted workspace may not make (`domain.Grows`). A
		// levyan that cannot be reached leaves the copy as it was -- each
		// grant until it ends -- and a workspace with none restricted by
		// nothing.
		s.Follower = &entitle.Follower{
			Source: s.Levyan.Grants(),
			Copy:   pd.EntitlementCopy(drv, content),
		}
		s.Spin = append(s.Spin, s.Follower)
		deps.Entitlements = pd.Entitlements

		// And what became of each workspace, told from the notices the
		// changes wrote, a workspace's in the order they happened, under the
		// lease (design 7.8). The first pass writes what levyan was never
		// told: what became of the workspaces before.
		s.Teller = &levyan.Teller{Db: client, Levyan: s.Levyan, Now: deps.Clock}
		s.Spin = append(s.Spin, spinEvery(levyan.NoticesName, levyan.NoticesEvery, s.Teller.Pass))
	}
	// How a workspace is paid for, relayed (design 7.9): what an offer reads
	// of the member it asks is read through the walled stack, as the caller.
	s.Payment = payment.Service{Levyan: s.Levyan, Walled: stacked}
	// The trail's erasure in the operation's transaction, the archive's half
	// owed there, and paid once it has committed (payday#115).
	deps.Forget = func(ctx context.Context, db *ent.Client, objects []pdid.Id) (int, error) {
		v, err := s.Trail.Forget(ctx, pd.TrailStore(db), objects)
		return v.Held.Rows + v.Held.Chunks, err
	}
	deps.Pay = func(ctx context.Context, objects []pdid.Id) error {
		_, err := s.Trail.Pay(ctx, pd.TrailStore(client), objects...)
		return err
	}

	if p.On() {
		log.From(ctx).InfoContext(ctx, "trail: retention", "policy", p.String())

		s.Spin = append(s.Spin, trail.Sweep(pd.TrailStore(client), p))
	}

	return s, nil
}

func (s *Server) Close() error {
	s.Search.Close()
	return errors.Join(s.Levyan.Close(), s.Identity.Close(), s.Db.Close())
}

// Follow makes the workspace's address the tenant's at roster as roster has
// it now (D86), which `serve` does as it starts: a tenant renamed at roster is
// the workspace's address from the next start on. A roster with no tenant yet,
// or a deployment with no workspace of it yet, is nothing to follow. It
// answers the address the workspace was at, when it moved.
func (s *Server) Follow(ctx context.Context) (string, error) {
	t, err := s.Identity.Tenant(ctx)
	if errors.Is(err, identity.ErrNoTenant) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return tenancy.Follow(ctx, s.Base, t)
}

// Offboard is what a tenant's export and purge work on.
func (s *Server) Offboard(c Config) offboard.Deployment {
	d := offboard.Deployment{
		Ent:       s.Ent,
		Drv:       s.Drv,
		Files:     s.Deps.Files,
		Trail:     s.Trail,
		Audit:     s.Deps.Audit,
		Retention: RetentionDefaults(c, s.Trail),
		Now:       s.Deps.Clock,
	}
	if s.Search != nil {
		// A purge's rows go past the recorder, and a restore's, so each says
		// itself that the workspace's documents go, or are to be made.
		d.Unindex = opensearch.Forget
		d.Reindex = opensearch.Remember
	}
	if s.Identity != nil {
		// The tenant at roster a restore is of, and the address it is at
		// (D86): none, on a roster with none yet, which `loft identity
		// link` then makes.
		d.Serves = func(ctx context.Context) (pdid.Id, string, error) {
			t, err := s.Identity.Tenant(ctx)
			if errors.Is(err, identity.ErrNoTenant) {
				return pdid.Nil, "", nil
			}
			return t.Id, t.Alias, err
		}
		d.Known = func(ctx context.Context, member pdid.Id) (bool, error) {
			_, err := s.Identity.StandingOf(ctx, member)
			// A roster with no tenant yet -- a deployment made to restore
			// into -- has nobody.
			if errors.Is(err, identity.ErrNoPerson) || errors.Is(err, identity.ErrNoTenant) {
				return false, nil
			}
			return err == nil, err
		}
	}
	return d
}

// Grpc builds the server every call arrives at.
//
// The chain is payday's and the order is this app's to read: what records a
// call is outside everything, then the recovery, then the deadline a call that
// named none is given, then how often one caller may ask, then what is closed
// to callers entirely.
//
// It is separate from [Server.Serve] so that a test can travel exactly this
// and answer on a listener that is a channel; see pdtest.
func (s *Server) Grpc(ctx context.Context, c Config, opts ...grpc.ServerOption) (*grpc.Server, error) {
	// Who is calling comes first, since everything after it reads the frame.
	// `Plain` believes what the caller writes, which is right for a sandbox
	// and for tests and is not something to serve where anyone can reach it.
	//
	// [Server.Auth] replaces it, which is how this app reads something else --
	// a certificate, a token, a session cookie. It is a field rather than a
	// configuration setting for the reason payday suggests two deployments
	// rather than a flag: a mistake in a YAML file must not be able to turn
	// authentication off.
	h := s.Auth
	if h == nil {
		h = auth.Plain()
	}

	chain := grpcx.Serving(ctx, grpcx.WithDeadline(c.Server.CallTimeout())).
		WithUnary(auth.InterceptorUnary(h, Resolver(s.Ungated), auth.PublicDefault)).
		WithStream(auth.InterceptorStream(h, Resolver(s.Ungated), auth.PublicDefault)).
		WithUnary(grpcx.LimitUnary(c.Server.Limiter(), gate.ByTenant(), pd.Cost)).
		With(s.accessed().Interceptor()).
		With(gate.Interceptor(s.Policy)).
		With(s.Watch.Interceptor())
	if s.Search != nil {
		// What a call wrote, searchable once it answers (D31).
		chain = chain.WithUnary(opensearch.Indexer{Client: s.Search, Db: s.Ent}.Interceptor())
	}
	chain = chain.WithUnary(grpcx.ClosedUnary(c.Server.Closed()))

	os := append(opts, chain.ServerOptions()...)
	// A certificate that cannot be read is a server that must not start, which
	// is why this answers with an error: `GrpcOptions` reads the files
	// `server.tls` names.
	vs, err := c.Server.GrpcOptions()
	if err != nil {
		return nil, err
	}

	os = append(os, vs...)

	g := grpc.NewServer(os...)
	app.RegisterServer(g, s.Walled)
	// And the one service that is no entity's: how a workspace is paid for,
	// relayed to levyan (design 7.9), through the same chain.
	app.RegisterPaymentServiceServer(g, s.Payment)

	return g, nil
}

// Serve answers on `l` until the context is done.
func (s *Server) Serve(ctx context.Context, c Config, l net.Listener) error {
	g, err := s.Grpc(ctx, c)
	if err != nil {
		return err
	}

	stop, err := s.serveHttp(ctx, c, g)
	if err != nil {
		return err
	}
	defer stop()

	stopped := make(chan struct{})
	go func() {
		<-ctx.Done()
		g.GracefulStop()
		close(stopped)
	}()

	err = g.Serve(l)
	if ctx.Err() == nil {
		return err
	}

	// Asked to stop: what was being answered ends first, and what it read is
	// written after it -- the access record's own last write, as its loop
	// stops, is too early for a call that answers while the server drains.
	<-stopped
	last, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	if err := s.Access.Flush(last); err != nil {
		log.From(ctx).ErrorContext(ctx, "access: the record waiting to be written was lost as the server stopped", "rows", s.Access.Pending(), "err", err)
	}

	return err
}

// serveHttp is the second listener, for whatever cannot speak gRPC -- which is
// every browser. Nothing is opened unless the configuration named an address.
//
// It is the **same** `g`: a page reaches the handlers a gRPC client reaches,
// through the interceptors a gRPC client goes through, behind the same wall.
// There is no second stack here for a rule to be missing from.
func (s *Server) serveHttp(ctx context.Context, c Config, g *grpc.Server) (func(), error) {
	if !c.Server.Http.Serves() {
		return func() {}, nil
	}

	h, err := web.New(c.Server.Http, g)
	if err != nil {
		return nil, err
	}

	// Whatever this app serves over HTTP goes here, on the same mux and behind
	// the same cross-origin answer. A gRPC path is `/<service>/<method>`, so
	// an ordinary route cannot collide with one.
	s.Routes(h.ServeMux, c)

	front, err := Front(c, h)
	if err != nil {
		return nil, err
	}

	l, err := net.Listen("tcp", c.Server.Http.Addr)
	if err != nil {
		return nil, err
	}

	srv := httpServer(ctx, front)
	go func() {
		if err := srv.Serve(l); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.From(ctx).ErrorContext(ctx, "http", slog.Any("err", err))
		}
	}()

	log.From(ctx).InfoContext(ctx, "http", slog.String("addr", l.Addr().String()))

	return func() { srv.Close() }, nil
}

// httpServer is the HTTP listener's server. A client that takes its time over
// its headers holds a connection for nothing, and so does one that keeps a
// connection open between requests and never sends the next (loft#40); a
// stream, once asked for, may take as long as it likes.
//
// And every request runs on ctx, which carries this process's telemetry and
// its logger: what a route says -- a sign-in nobody could check, an issuer
// that could not be asked, a page's broken policy -- is said where the rest of
// the process says it. On the bare context a server gives a request it went to
// the OpenTelemetry globals, which discard; the deployment bundle's first run
// is what found that out.
func httpServer(ctx context.Context, h http.Handler) *http.Server {
	return &http.Server{
		Handler:           h,
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       2 * time.Minute,
		BaseContext:       func(net.Listener) context.Context { return ctx },
	}
}

// Downloads are the RPCs that hand people's data out to be kept somewhere else
// (loft#136): each says what it is for, in `payday-reason-bin`, or is refused,
// and its row of the access record is written before it is answered -- before
// each message of a stream.
var Downloads = []string{
	app.AssetService_Export_FullMethodName,
	// Who has what is out, by name (design 9.14). The other reports' CSVs
	// name nobody, and are no download of anybody.
	app.CustodyService_ExportOutstanding_FullMethodName,
}

// accessed is what the access record's interceptor is told of this app: whose
// data a message carries, read off what the schema declared `personal`; where a
// call came from, as `Front` resolved it past the proxies `app.trusted_proxies`
// names; and which calls are downloads.
func (s *Server) accessed() access.Config {
	return access.Config{
		Subjects:  pd.Subjects,
		Refs:      pd.SubjectRefs,
		Address:   addressOf,
		Downloads: Downloads,
		Writer:    s.Access,
	}
}

// addressOf is where a call came from: the request it came in on, as `Front`
// resolved it, or the connection's for one that came by no request.
func addressOf(ctx context.Context) string {
	if v, ok := clientaddr.From(ctx); ok {
		return v
	}

	return access.Peer(ctx)
}

// Front is what every HTTP request goes through before the routes: where it
// came from, believing X-Forwarded-For from the proxies `app.trusted_proxies`
// names and from nobody else (`server/clientaddr`), and the headers every
// answer carries (`guarded`).
func Front(c Config, h http.Handler) (http.Handler, error) {
	proxies, err := clientaddr.Parse(c.App.TrustedProxies)
	if err != nil {
		return nil, fmt.Errorf("app.trusted_proxies: %w", err)
	}
	mode, err := csp.Parse(c.App.Csp)
	if err != nil {
		return nil, fmt.Errorf("app.csp: %w", err)
	}

	return guarded(proxies.Handler(h), c.App.Public().Scheme == "https", mode, csp.Policy(c.Auth.Sso.Issuer)), nil
}

// depsOf is what the domain layer is told by the configuration.
func depsOf(c Config) (*domain.Deps, error) {
	key := []byte(c.App.SigningKey)
	if len(key) == 0 {
		key = make([]byte, 32)
		if _, err := rand.Read(key); err != nil {
			return nil, err
		}
	}
	files := c.App.Files
	if files == "" {
		files = filepath.Join("data", "files")
	}
	if err := os.MkdirAll(files, 0o750); err != nil {
		return nil, err
	}

	sender, err := mail.New(c.App.Mail, identity.ResolveRef, slog.Default())
	if err != nil {
		return nil, err
	}

	// What a workspace that says nothing of its own is told in, refused here
	// rather than at the first date told in it.
	locale := domain.Locale{Timezone: c.App.Locale.Timezone, Currency: c.App.Locale.Currency}
	if err := locale.Check(); err != nil {
		return nil, fmt.Errorf("app.locale.%w", err)
	}

	return &domain.Deps{
		Files:         storage.Dir(files),
		Sign:          storage.Signer{Key: key, TTL: 10 * time.Minute},
		Links:         c.App.Links(),
		NoShowAfter:   c.App.NoShowAfter,
		Locale:        locale,
		RestoreWithin: c.App.Workspaces.Restore(),
		Retention:     c.App.Retention.Defaults(),
		Mail:          sender,
	}, nil
}

func or[T comparable](v, otherwise T) T {
	var zero T
	if v == zero {
		return otherwise
	}
	return v
}

const notBuilt = `<!doctype html><meta charset="utf-8"><title>Loft</title>
<body style="font-family:sans-serif;max-width:40em;margin:4em auto;line-height:1.6">
<h1>UI가 아직 빌드되지 않았습니다</h1>
<p><code>%s</code>에 빌드된 화면이 없습니다. 저장소에서 다음을 실행한 뒤 새로 고치세요.</p>
<pre>cd ts &amp;&amp; npm install &amp;&amp; npm run build</pre>
<p>API는 이미 돌고 있습니다.</p>`

// spinEvery runs `f` every `d`, and logs a pass that failed rather than
// stopping: tidying a table is not a reason to stop serving. It is the
// deployment's work, so under a lease one process runs it, and its name is what
// the processes agree on.
func spinEvery(name string, d time.Duration, f func(ctx context.Context) error) spin.Spinner {
	return spin.Called(name, spin.Every(d, func(ctx context.Context) error {
		if err := f(ctx); err != nil {
			log.From(ctx).WarnContext(ctx, "tidy", slog.Any("err", err))
		}
		return nil
	}))
}

// Routes is what this app serves over HTTP besides its Rpcs.
func (s *Server) Routes(mux *http.ServeMux, c Config) {
	// Signing in and out. What is checked is a password, by roster, and
	// somebody new to the workspace is made a member; see `server/session`.
	login := session.SignIn(s.Sessions, s.Identity, s.Ent, s.Members)
	if c.Auth.SsoOnly {
		// No form to post to, rather than one that answers no: a password is
		// not a way in here (D28).
		mux.HandleFunc("POST /session", func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, "this deployment signs people in through single sign-on only", http.StatusForbidden)
		})
	} else {
		mux.Handle("POST /session", login)
	}
	mux.Handle("DELETE /session", login)
	// Who the session is, and the workspace it is in: what the page asks
	// first, which a closed workspace answers too (D83, D85).
	mux.Handle("GET /session", session.Who{
		Sessions: s.Sessions,
		Db:       s.Ent,
		Zone:     s.Deps.Locale.Zone(),
		// Whether the page offers the workspace's billing (design 7.9).
		Billing: c.App.Billing.Levyan.On(),
	})

	// A terminal signed in by somebody approving it here; see
	// `server/clilogin`.
	s.Logins.Mount(mux)

	// Through roster's issuer, and what the sign-in page is told about the
	// ways in; see `server/sso`.
	s.Sso.Mount(mux)

	// Attachments, by the signed links the API answers with.
	mux.Handle("GET /files/", s.Deps.Sign.Handler(s.Deps.Files, time.Now))

	// A printed label: the code is sent to the page that resolves it, at the
	// service's address. The page asks the API, through the wall, so a label
	// of another tenant reads as nothing there. Which workspace a code is
	// never decides where it is sent, so that a label says no more than what
	// was printed on it.
	page := c.App.Links().Page()
	mux.HandleFunc("GET /l/{code}", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, page+"/scan/"+url.PathEscape(r.PathValue("code")), http.StatusFound)
	})

	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("ok"))
	})

	// What browsers report a page did that its policy does not allow
	// (`app.csp`), logged.
	mux.Handle("POST "+csp.Path, &csp.Reports{})

	// The built UI, with every path it does not have answered by its index,
	// which is how a page that routes on the client is served.
	if dir := c.App.Web; dir != "" {
		files := http.FileServer(http.Dir(dir))
		mux.HandleFunc("GET /", func(w http.ResponseWriter, r *http.Request) {
			p := filepath.Join(dir, filepath.FromSlash(filepath.Clean("/"+r.URL.Path)))
			if st, err := os.Stat(p); err != nil || st.IsDir() {
				if strings.HasPrefix(r.URL.Path, "/static/") {
					http.NotFound(w, r)
					return
				}
				index := filepath.Join(dir, "index.html")
				if _, err := os.Stat(index); err != nil {
					// A checkout that has not built the UI yet: say how,
					// rather than answer a 404 nobody can act on.
					w.Header().Set("Content-Type", "text/html; charset=utf-8")
					w.WriteHeader(http.StatusServiceUnavailable)
					fmt.Fprintf(w, notBuilt, dir)
					return
				}
				w.Header().Set("Cache-Control", "no-cache")
				http.ServeFile(w, r, index)
				return
			}
			if strings.HasPrefix(r.URL.Path, "/static/") {
				w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
			}
			files.ServeHTTP(w, r)
		})
	}
}
