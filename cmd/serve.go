package cmd

import (
	"context"
	"crypto/rand"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/lesomnus/otx/log"
	"github.com/protobuf-orm/ent/dialect"
	entsql "github.com/protobuf-orm/ent/dialect/sql"
	"google.golang.org/grpc"

	"github.com/lesomnus/payday/auth"
	"github.com/lesomnus/payday/auth/authsession"
	"github.com/lesomnus/payday/gate"
	"github.com/lesomnus/payday/grpcx"
	"github.com/lesomnus/payday/pdid"
	"github.com/lesomnus/payday/pdpb"
	"github.com/lesomnus/payday/spin"
	"github.com/lesomnus/payday/trail"
	"github.com/lesomnus/payday/watch"
	"github.com/lesomnus/payday/web"

	app "github.com/lesomnus/rove"
	"github.com/lesomnus/rove/internal/ent"
	"github.com/lesomnus/rove/internal/identity"
	"github.com/lesomnus/rove/internal/search"
	"github.com/lesomnus/rove/server/account"
	"github.com/lesomnus/rove/server/bare"
	"github.com/lesomnus/rove/server/clilogin"
	"github.com/lesomnus/rove/server/closing"
	"github.com/lesomnus/rove/server/consent"
	"github.com/lesomnus/rove/server/domain"
	"github.com/lesomnus/rove/server/lifecycle"
	"github.com/lesomnus/rove/server/mail"
	"github.com/lesomnus/rove/server/offboard"
	"github.com/lesomnus/rove/server/pd"
	"github.com/lesomnus/rove/server/policy"
	"github.com/lesomnus/rove/server/retention"
	"github.com/lesomnus/rove/server/session"
	"github.com/lesomnus/rove/server/sso"
	"github.com/lesomnus/rove/server/storage"
	"github.com/lesomnus/rove/server/tenancy"
)

// Server is a built app: the database it runs on and the two stacks it answers
// through.
type Server struct {
	Db  *sql.DB
	Ent *ent.Client

	// Drv is what the client was built on, kept because a transaction is begun
	// on a driver and a `*ent.Client` does not hand out the one it holds. It is
	// what `pd.Batch` puts a whole stack onto.
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

	// Identity is roster, beside this process or inside it: who people are,
	// and the password they sign in with (design 9.10).
	Identity *identity.Store

	// Sessions mints and reads the cookie a browser signs in with.
	Sessions *authsession.Sessions

	// Lifecycle is what roster's word about an account does here: its
	// sessions ended, its memberships leaving (`server/lifecycle`).
	Lifecycle lifecycle.Lifecycle

	// Logins is a terminal signed in by somebody approving it in a browser
	// (`rove login`, `server/clilogin`).
	Logins *clilogin.Logins

	// Closing is what becomes of a closed workspace and of a workspace's
	// address (D29, `server/closing`).
	Closing closing.Closing

	// Sso is this deployment as a relying party of roster's issuer, and what
	// the sign-in page is told about the ways in.
	Sso *sso.Rp

	// Consents is what an account agrees to before it uses this deployment,
	// and the record that it did (design 9.11, `server/consent`).
	Consents *consent.Consents

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
	// and what that means here -- and because the two places it has to be
	// installed are not one place. `gate.Interceptor` covers the calls gRPC
	// dispatches; `c.Server.Guard` covers the operations inside a batch, which
	// arrive as one method carrying many and would otherwise be authorised as
	// the batch rather than as themselves.
	//
	// It is what a second binary is built from. A deployment that wants an
	// operator's path serves this app again with a policy that answers
	// differently, on an address only an operator can reach, rather than
	// putting a rule in here that says some row is special.
	Policy gate.Policy

	// Spin is whatever this deployment has to run besides answering requests.
	// It is a slice rather than a method because a server with nothing to run
	// should write nothing at all; see `payday/spin`.
	Spin []any
}

// Build opens the database and stacks the servers.
//
// The two hooks are the whole of what payday puts in the write and read paths,
// and both come out of what the schema declared: [pd.Minter] stamps a new row
// with the domain of its entity and refuses one of another, [pd.Wall] narrows
// every read to the tenants the caller may see -- and [domain.View] to the
// part of their history their contract lets them look at.
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
	b, err := c.Watch.Build(c.Db)
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
	rec := bare.Recorders{pd.Recorder(), pd.WatchRecorder(w)}
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
	// Which index answers a search, as the last migration left the database;
	// `cli.Migrate` asks again after it has made them.
	if dialect == "postgres" {
		if deps.Search, err = search.Detect(ctx, db); err != nil {
			db.Close()
			return nil, fmt.Errorf("search: %w", err)
		}
	}

	// Who people are is roster's: one of its own, or one in this process on a
	// database of its own.
	people, err := identity.Open(ctx, c.Auth.Roster, c.StateDir(), log.From(ctx))
	if err != nil {
		db.Close()
		return nil, err
	}
	deps.People = people

	// One clock for every stamp, the domain layer's and the servers' alike.
	opts := []bare.Option{bare.WithMinter(pd.Minter()), bare.WithRecorder(rec), bare.WithClock(deps.Clock)}

	sink, err := pd.NewSink(client, opts...)
	if err != nil {
		people.Close()
		db.Close()
		return nil, err
	}

	// The wall, and inside it the tenant's view window over what a predicate
	// can narrow to it (design 8.1).
	scope := bare.Scopes{pd.Wall(), domain.View(client, deps)}
	walled, err := pd.NewSink(client, append(opts, bare.WithScope(scope))...)
	if err != nil {
		people.Close()
		db.Close()
		return nil, err
	}

	// The stack a caller reaches. The domain layer is outermost, **above**
	// the gate: every write it makes on a caller's behalf goes through the
	// same edge checks the caller's own would (design D20). The policy is
	// asked once, about the method the caller named, before any of this.
	stacked, err := app.Build(walled.WithWatch(w), pd.AuditBuild(), pd.SecretBuild(), pd.GateBuild(), domain.Build(drv, deps))
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
	base, err := app.Build(sink.WithWatch(w), pd.AuditBuild(), pd.SecretBuild())
	if err != nil {
		people.Close()
		db.Close()
		return nil, err
	}
	ungated := domain.New(base, drv, deps)
	// What an account's answer to an invitation is written through, in a
	// workspace it is not a member of yet: the domain layer's alone.
	deps.Base = base

	s := &Server{Db: db, Ent: client, Drv: drv, Dialect: dialect, Watch: w, Walled: stacked, Ungated: ungated, Base: base, Deps: deps, Identity: people}

	// Who is calling is a session cookie, held on every call to what roster
	// says of its person, and what they may do is their role.
	store := session.Store{Db: client, People: people}
	opts2 := []authsession.Option{
		authsession.WithIdle(or(c.App.Session.Idle, 24*time.Hour)),
		authsession.WithLifetime(or(c.App.Session.Lifetime, 7*24*time.Hour)),
	}
	if c.App.Public().Scheme != "https" {
		opts2 = append(opts2, authsession.Insecure())
	}
	s.Sessions = authsession.New(store, opts2...)
	s.Auth = s.Sessions.Handler()
	s.Policy = policy.Policy{}
	cookie := authsession.DefaultCookie
	if c.App.Public().Scheme != "https" {
		cookie = authsession.InsecureCookie
	}
	s.Logins = &clilogin.Logins{
		Db: client, Sessions: s.Sessions, Store: store, People: people, Cookie: cookie,
		Idle: or(c.App.Session.Idle, 24*time.Hour), Lifetime: or(c.App.Session.Lifetime, 7*24*time.Hour),
		App: c.App.App(),
	}
	s.Lifecycle = lifecycle.Lifecycle{
		Db: client, Sessions: store, Base: base,
		People: people, Deps: deps, Drv: drv, Grace: c.App.Accounts.Grace(),
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
		log.From(ctx).WarnContext(ctx, "auth.sso: the roster in this process has no issuer of its own; the issuer named has to vouch for accounts in it")
	}
	docs, err := c.App.Consent.Documents()
	if err != nil {
		people.Close()
		db.Close()
		return nil, err
	}
	s.Consents = &consent.Consents{Db: client, Documents: docs, Now: deps.Clock}
	if s.Sso, err = sso.New(c.Auth.Sso, c.App.App(), !c.Auth.SsoOnly, s.Sessions, people, s.Arrive(c)); err != nil {
		people.Close()
		db.Close()
		return nil, err
	}

	// What happens because time passed: holds that ran out, rooms nobody came
	// to, loans that are late, the day's usage.
	s.Spin = append(s.Spin,
		domain.Sweeper{Server: base, Drv: drv, Deps: deps, Every: c.App.Sweep},
		// What the notifications worth a mailbox say, sent after the
		// operation that made them (`app.mail`; nothing without a server).
		domain.Mailer{Drv: drv, Deps: deps},
		spinEvery("rove.sessions", time.Hour, store.Sweep),
		spinEvery("rove.sign-in-failures", session.Window, session.Failures{Db: client}.Sweep),
		spinEvery("rove.cli-logins", time.Hour, s.Logins.Sweep),
		// Closed workspaces whose window is over, destroyed unless held.
		// Read when it runs: s.Closing is put together after the trail.
		spinEvery("rove.closing", or(c.App.Workspaces.Every, time.Hour), func(ctx context.Context) error { return s.Closing.Sweep(ctx) }),
		// The roster in this process's own housekeeping; nothing, for one of
		// its own. Its loops are the deployment's like these, so it is one
		// of them under a lease. (It was handed over as the store itself,
		// which is no spin.Spinner, and so never ran.)
		spin.Called("rove.roster", spin.Func(people.Run)),
	)
	if !people.Embedded() {
		// What roster says of an account as it says it: sessions that end
		// when roster ends them rather than at their next call, and an
		// erased account beginning to leave. The roster in this process has
		// no stream, and every change to its people is made from here.
		//
		// Each process holds a stream of its own: what roster says is also
		// what each process's own answers about an account are dropped by
		// (`identity.Store.Recheck`), and what it writes here comes to the
		// same thing written twice.
		s.Spin = append(s.Spin, spin.InEveryProcess(spin.Called("rove.roster-stream", spin.Func(func(ctx context.Context) error {
			return people.Follow(ctx, s.Lifecycle.Apply)
		}))))
	}
	// And the accounts still erased once roster's grace is over, leaving
	// every workspace they were in.
	s.Spin = append(s.Spin, spinEvery("rove.departures", c.App.Accounts.Interval(), s.Lifecycle.Sweep))
	// And every account asked about again, for an erasure the stream did not
	// carry because this was not listening (design 9.10).
	if d := c.App.Accounts.ReconcileEvery(); d > 0 {
		s.Spin = append(s.Spin, spinEvery("rove.reconcile", d, s.Lifecycle.Reconciling))
	}

	// What the keep windows no longer reach, taken out of the history (design
	// 8.2) -- only when the deployment has said a window may destroy.
	if c.App.Retention.Apply {
		s.Spin = append(s.Spin, domain.Expirer{Server: base, Drv: drv, Deps: deps, Every: c.App.Retention.Every})
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

	// And per tenant, from its contract, for the kinds that are history: their
	// trail lasts as long as the history does, and a legal hold holds both.
	// Only when the deployment has said a window may destroy, since this is
	// what turns the sweep on for a deployment that configured no `audit:`.
	if c.App.Retention.Apply {
		p.Tenants = retention.Trail(client, p, c.App.Retention.Defaults())
	}

	// What the trail kept of a person, forgotten in the database and in the
	// archive both, when they are pseudonymized. Every tenant's legal holds
	// are asked whether or not the windows may destroy: a hold outweighs an
	// erasure either way.
	s.Trail = p
	s.Trail.Tenants = retention.Trail(client, p, c.App.Retention.Defaults())
	// After the trail's policy, which a destroy purges by.
	s.Closing = closing.Closing{
		Db: client, Offboard: s.Offboard(c), People: people,
		Base: base, Drv: drv, Deps: deps,
		Aliases: s.Aliases(c), Hold: c.App.Workspaces.Hold(),
	}
	deps.Forget = func(ctx context.Context, db *ent.Client, objects []pdid.Id) (int, error) {
		v, err := s.Trail.Forget(ctx, pd.TrailStore(db), objects)
		return v.Held.Rows + v.Held.Chunks, err
	}

	if p.On() {
		log.From(ctx).InfoContext(ctx, "trail: retention", "policy", p.String())

		s.Spin = append(s.Spin, trail.Sweep(pd.TrailStore(client), p))
	}

	return s, nil
}

func (s *Server) Close() error { return errors.Join(s.Identity.Close(), s.Db.Close()) }

// Workspaces is how an account comes to be in a workspace of its own.
func (s *Server) Workspaces(c Config) tenancy.Workspaces {
	return tenancy.Workspaces{
		Base:        s.Base,
		Drv:         s.Drv,
		Ungated:     s.Ungated,
		LabelSuffix: c.App.Labels.Default(),
		Personal:    c.App.Workspaces.PersonalOn(),
		Owned:       c.App.Workspaces.OwnedMax(),
		Aliases:     s.Aliases(c),
	}
}

// Arrive is what an account signing in is given: a workspace of its own when it
// is in none -- once it has agreed to what this deployment asks (design 9.11).
// Until then it is given nothing, and the workspace is made when it agrees
// (`POST /account/consents`).
func (s *Server) Arrive(c Config) func(ctx context.Context, p identity.Person) error {
	ws := s.Workspaces(c)
	return func(ctx context.Context, p identity.Person) error {
		if ok, err := s.Consents.Settled(ctx, p.Id); err != nil || !ok {
			return err
		}
		return ws.Arrive(ctx, p)
	}
}

// Aliases is what decides a workspace's address.
func (s *Server) Aliases(c Config) tenancy.Aliases {
	return tenancy.Aliases{Db: s.Ent, Length: c.App.Workspaces.Length(), Reserved: c.App.Workspaces.Reserved, Now: s.Deps.Clock}
}

// Offboard is what a tenant's export and purge work on.
func (s *Server) Offboard(c Config) offboard.Deployment {
	return offboard.Deployment{
		Ent:       s.Ent,
		Drv:       s.Drv,
		Files:     s.Deps.Files,
		Trail:     s.Trail,
		Retention: c.App.Retention.Defaults(),
		Now:       s.Deps.Clock,
	}
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
		WithUnary(auth.InterceptorUnary(h, Resolver(s.Ungated, s.Ent, s.Consents), auth.PublicDefault)).
		WithStream(auth.InterceptorStream(h, Resolver(s.Ungated, s.Ent, s.Consents), auth.PublicDefault)).
		WithUnary(grpcx.LimitUnary(c.Server.Limiter(), LimitKey())).
		With(gate.Interceptor(s.Policy)).
		With(s.Watch.Interceptor()).
		WithUnary(grpcx.ClosedUnary(c.Server.Closed()))

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

	// The batch, with the same rules the chain above enforces -- read off the
	// same configuration rather than written out again, which is the only way
	// the two stay in step. What they enforce by looking at the method gRPC
	// dispatched, this enforces per operation.
	if b, err := pd.Batch(s.Walled, s.Drv, c.Server.Guard(s.Policy)); err == nil {
		pdpb.RegisterBatchServiceServer(g, b)
	} else {
		// A deployment that closed nothing, limits nothing and has no policy.
		// It is a real thing to be and it is also what a guard nobody filled in
		// looks like, so the batch is not served rather than served open.
		log.From(ctx).WarnContext(ctx, "no batch", slog.String("why", err.Error()))
	}

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

	go func() {
		<-ctx.Done()
		g.GracefulStop()
	}()

	return g.Serve(l)
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

	l, err := net.Listen("tcp", c.Server.Http.Addr)
	if err != nil {
		return nil, err
	}

	// A client that takes its time over its headers holds a connection for
	// nothing; a stream, once asked for, may take as long as it likes.
	srv := &http.Server{Handler: guarded(h, c.App.Public().Scheme == "https"), ReadHeaderTimeout: 10 * time.Second}
	go func() {
		if err := srv.Serve(l); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.From(ctx).ErrorContext(ctx, "http", slog.String("err", err.Error()))
		}
	}()

	log.From(ctx).InfoContext(ctx, "http", slog.String("addr", l.Addr().String()))

	return func() { srv.Close() }, nil
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

	pub := c.App.Public()
	port := pub.Port()
	return &domain.Deps{
		Files:              storage.Dir(files),
		Sign:               storage.Signer{Key: key, Base: strings.TrimSuffix(pub.String(), "/"), TTL: 10 * time.Minute},
		LabelSuffix:        c.App.Labels.Suffix,
		LabelTarget:        c.App.Labels.Target,
		LabelScheme:        pub.Scheme,
		LabelPort:          port,
		LabelTenantDomains: c.App.Labels.TenantDomains,
		LabelBase:          strings.TrimSuffix(pub.String(), "/"),
		LookupTXT:          net.DefaultResolver.LookupTXT,
		NoShowAfter:        c.App.NoShowAfter,
		RestoreWithin:      c.App.Workspaces.Restore(),
		Retention:          c.App.Retention.Defaults(),
		Mail:               sender,
		AppUrl:             c.App.App(),
		Invitations: domain.InvitationRules{
			Ttl:     c.App.Invitations.Ttl,
			Pending: c.App.Invitations.Pending,
			Daily:   c.App.Invitations.Daily,
		},
	}, nil
}

func or[T comparable](v, otherwise T) T {
	var zero T
	if v == zero {
		return otherwise
	}
	return v
}

const notBuilt = `<!doctype html><meta charset="utf-8"><title>Rove</title>
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
			log.From(ctx).WarnContext(ctx, "tidy", slog.String("error", err.Error()))
		}
		return nil
	}))
}

// Routes is what this app serves over HTTP besides its Rpcs.
func (s *Server) Routes(mux *http.ServeMux, c Config) {
	// Signing in and out. What is checked is a password, by roster, and an
	// account in no workspace is given one; see `server/session`.
	ws := s.Workspaces(c)
	login := session.SignIn(s.Sessions, s.Identity, s.Ent, s.Arrive(c))
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

	// A terminal signed in by somebody approving it here; see
	// `server/clilogin`.
	s.Logins.Mount(mux)

	// Through roster's issuer, and what the sign-in page is told about the
	// ways in; see `server/sso`.
	s.Sso.Mount(mux)

	// What an account has outside any one workspace: which it is in, and a
	// new one.
	account.Routes{
		Sessions:   s.Sessions,
		People:     s.Identity,
		Workspaces: ws,
		Export: func(ctx context.Context, t pdid.Id, w io.Writer) error {
			_, err := s.Offboard(c).Export(ctx, t, w)
			return err
		},
		// A label scanned in a workspace it is not in: the one of the
		// account's own it is in, when there is one (D28).
		Label: func(ctx context.Context, code string) (pdid.Id, bool, error) {
			return domain.LabelWorkspace(ctx, s.Ent, code)
		},
		Manage: s.Identity.AccountPage(),
		// What it agrees to before anything else, and the workspace an
		// account that just agreed is given (design 9.11).
		Consent: s.Consents,
		Arrive:  s.Arrive(c),
	}.Mount(mux)

	// Attachments, by the signed links the API answers with.
	mux.Handle("GET /files/", s.Deps.Sign.Handler(s.Deps.Files, time.Now))

	// A printed label: whatever host it was printed with, the code is sent to
	// the page that resolves it. The page asks the API, through the wall, so
	// a label of another tenant reads as nothing there -- and then asks
	// `/account/labels/{code}` which of the account's own it is in.
	app := strings.TrimSuffix(c.App.App(), "/")
	mux.HandleFunc("GET /l/{code}", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, app+"/scan/"+r.PathValue("code"), http.StatusFound)
	})

	// What a reverse proxy issuing certificates on demand asks is the
	// operator's entry point's to answer (`rove-admin serve`), which this
	// binary does not link (design 6장).

	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("ok"))
	})

	// The built UI, with every path it does not have answered by its index,
	// which is how a page that routes on the client is served.
	if dir := c.App.Web; dir != "" {
		files := http.FileServer(http.Dir(dir))
		aliases := s.Aliases(c)
		mux.HandleFunc("GET /", func(w http.ResponseWriter, r *http.Request) {
			// An address a workspace moved off sends whoever arrives at it on,
			// for as long as it is held (D29).
			if rest, ok := strings.CutPrefix(r.URL.Path, "/@"); ok {
				alias, tail, _ := strings.Cut(rest, "/")
				if to, err := aliases.Moved(r.Context(), alias); err == nil && to != "" {
					u := "/@" + to
					if tail != "" {
						u += "/" + tail
					}
					if r.URL.RawQuery != "" {
						u += "?" + r.URL.RawQuery
					}
					http.Redirect(w, r, u, http.StatusFound)
					return
				}
			}
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
