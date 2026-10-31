// Package domain is the layer that answers what loft means: the operations a
// person does with an asset, and the reads that say what it was then.
//
// It sits above payday's Gate (design 9.3, D20), so every write it issues on
// a caller's behalf goes through the same edge checks a caller's own write
// would. Every operation that writes more than one row is one transaction:
// [Domain.tx] begins it on the driver this layer was built with, so inside a
// batch it joins the batch's transaction instead of opening a second one.
//
// Reads that a generated List cannot express -- a subtree at a moment, a
// timeline, a search -- go to ent directly, and every one of them is narrowed
// to the caller's tenant by hand; [Tx.tenant] is the value they all use. What
// an asset was and what was done are payday's past (payday#151), read through
// its `past` package, which narrows each read to the tenants the caller's
// frame may see.
package domain

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"time"
	"uuid"

	"github.com/lesomnus/otx/log"
	"github.com/lesomnus/payday/audit"
	"github.com/lesomnus/payday/frame"
	"github.com/lesomnus/payday/gate"
	"github.com/lesomnus/payday/past"
	"github.com/lesomnus/payday/pdid"
	"github.com/protobuf-orm/ent/dialect"
	"github.com/protobuf-orm/ent/dialect/sql"
	"github.com/protobuf-orm/protoc-gen-orm-ent/runtime/enttx"
	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"

	app "github.com/lesomnus/loft"
	"github.com/lesomnus/loft/internal/ent"
	"github.com/lesomnus/loft/internal/ent/event"
	"github.com/lesomnus/loft/internal/ent/treelock"
	"github.com/lesomnus/loft/internal/identity"
	"github.com/lesomnus/loft/server/apphost"
	"github.com/lesomnus/loft/server/mail"
	"github.com/lesomnus/loft/server/opensearch"
	"github.com/lesomnus/loft/server/pd"
	"github.com/lesomnus/loft/server/retention"
	"github.com/lesomnus/loft/server/storage"
)

// Deps is what every copy of the layer shares, however often a transaction
// rebuilds it.
type Deps struct {
	// Now is the clock; tests replace it.
	Now func() time.Time

	// Files is where attachments are kept, and Sign makes the short-lived
	// addresses they are downloaded from.
	Files storage.Store
	Sign  storage.Signer

	// LabelSuffix is what a default label subdomain ends with, such as
	// "l.localhost": a tenant's default host is `<sub>.<LabelSuffix>`.
	LabelSuffix string
	// LabelTarget is what a custom label domain's CNAME points at.
	LabelTarget string
	// ServiceHosts is this deployment's own hosts -- where people sign in,
	// and where the app is when a workspace has no domain of its own -- which
	// no workspace may take as its domain, nor any name under them.
	ServiceHosts []string

	// Links is where every link made here begins: a workspace's on its
	// active app domain, and the rest at the service's address (loft#155).
	// A label, a signed file and a mail's link alike are made by it.
	Links apphost.Links

	// RestoreWithin is how long a closed workspace may be restored before it
	// is destroyed (D29); zero is 30 days. AliasHold is how long its address
	// is kept from anybody else after that, which its owner is told; zero is
	// a year.
	RestoreWithin time.Duration
	AliasHold     time.Duration

	// LookupTXT reads TXT records, for verifying a custom domain; and
	// LookupCNAME and LookupHost what a domain points at, for whether it
	// points here (`net.Resolver`'s). Nil reads nothing, and a deployment
	// that cannot read DNS verifies nothing and lapses nothing.
	LookupTXT   func(ctx context.Context, name string) ([]string, error)
	LookupCNAME func(ctx context.Context, host string) (string, error)
	LookupHost  func(ctx context.Context, host string) ([]string, error)

	// DomainRedirect is how long a workspace's app domain that another took
	// the place of sends its pages on to where the app is now (loft#155);
	// zero is 90 days.
	DomainRedirect time.Duration

	// Locale is what a workspace that says nothing of its own is told in: its
	// zone and its currency (`app.locale`, #144).
	Locale Locale

	// NoShowAfter is how long after a room reservation begins it is released
	// when nobody checked in. Zero holds nobody to checking in.
	NoShowAfter time.Duration

	// Finder is the search engine a search asks, when the deployment names
	// one (D31); nil asks the database (`internal/search`).
	Finder *opensearch.Client

	// Retention is the windows of a tenant with no contract (design 8). The
	// zero value shows all of the history.
	Retention retention.Defaults

	// Forget blanks what the trail kept of these objects in the database `db`
	// is a client on, which is the operation's transaction, and owes the
	// archive's half there (payday's `trail.Policy.Forget`, payday#115): a row
	// of payday's `owed` for each, which a rollback takes with the rest. What
	// a legal hold is on stays as it was, and it answers how many of the
	// database's rows that kept; what a hold keeps in the archive stays owed
	// (`loft trail owed`). Nil blanks the database's rows only, which is all
	// there is when the trail has no archive.
	Forget func(ctx context.Context, db *ent.Client, objects []pdid.Id) (held int, err error)

	// Pay pays the archive's half of what a transaction forgot, once it has
	// committed (payday's `trail.Policy.Pay`, naming them): the archive takes
	// part in no transaction, and an erasure a person is owed is not left for
	// later when it can be done at once. What it cannot pay stays owed, and
	// the trail's next pass pays it before it moves anything. Nil leaves all
	// of it to the pass.
	Pay func(ctx context.Context, objects []pdid.Id) error

	// People is where the people of a tenant are: roster (design 9.10). Nil
	// is a deployment whose people cannot be changed from here.
	People People

	// Mail sends what goes to somebody's mailbox -- an invitation, a
	// notification -- with a link [Deps.Links] makes. Nil sends nothing.
	Mail mail.Sender

	// Invitations is how long an invitation waits and how many a workspace
	// may have out.
	Invitations InvitationRules

	// Base is the stack with neither the wall nor this layer: what an
	// account's answer to an invitation is written through, in a workspace it
	// is not a member of yet. Never handed to anything a caller reaches; this
	// layer writes through it, after its own checks, and nothing else does.
	Base app.Server

	// Audit is what the trail keeps of each kind's writes (`audit.content`,
	// `audit.digest.key`), as the recorder is given it: what a write this
	// package makes beneath the servers on purpose is put on the trail with,
	// as the act it is (payday#108, `pd.Record`). The zero value records the
	// act and keeps no digest of the row.
	Audit audit.Contents
}

// People is roster, as the people operations reach it: the accounts of this
// deployment's directory (design 9.10, D27). On the roster in this process
// Loft makes accounts and gives them passwords there; on an external one those
// are roster's, and the operations that would do them are refused.
type People interface {
	Embedded() bool
	AddPerson(ctx context.Context, alias, name string, id pdid.Id) (identity.Person, error)
	ById(ctx context.Context, sub string) (identity.Person, error)
	SetPassword(ctx context.Context, account pdid.Id, password string) error
	IssuePassword(ctx context.Context, account pdid.Id) (string, error)
	Verify(ctx context.Context, login, password string) (identity.Person, error)
	Invalidate(ctx context.Context, account pdid.Id) error
	ForgetPerson(ctx context.Context, account pdid.Id) error
	// Addresses is an account's addresses somebody checked: what an
	// invitation is found by.
	Addresses(ctx context.Context, account pdid.Id) ([]string, error)
	// AccountPage is roster's page where somebody manages their own
	// account, for a roster of its own; empty for none.
	AccountPage() string
}

// here answers the people an account is made and changed at, or the refusal a
// deployment whose accounts are an external roster's gives.
func (d *Deps) here() (People, error) {
	if d.People == nil || !d.People.Embedded() {
		return nil, failed("계정과 비밀번호는 roster에서 관리합니다")
	}
	return d.People, nil
}

// mailing says whether this deployment sends mail at all. One with no SMTP
// server writes what it would have sent to the log, which is right for an
// invitation, whose link somebody is waiting for, and wrong for every
// notification: those are not queued.
func (d *Deps) mailing() bool {
	if d == nil || d.Mail == nil {
		return false
	}
	_, logged := d.Mail.(mail.Log)
	return !logged
}

func (d *Deps) now() time.Time {
	if d == nil || d.Now == nil {
		return time.Now()
	}
	return d.Now()
}

// Clock is the time everything in the stack stamps: the domain layer's
// moments and the servers' `date_created`. Two clocks would be two answers to
// "when was this recorded", and an as-of read compares the two.
func (d *Deps) Clock() time.Time { return d.now() }

// Domain is the layer.
type Domain struct {
	app.Overlay

	drv  dialect.Driver
	deps *Deps
}

var (
	_ app.Server               = Domain{}
	_ enttx.Binder[app.Server] = Domain{}
)

// New makes the layer in front of `next`. `drv` is what the stack below talks
// to, and is what a transaction is begun on.
func New(next app.Server, drv dialect.Driver, deps *Deps) Domain {
	return Domain{app.NewOverlay(next), drv, deps}
}

// Build makes a builder of the layer so it can be stacked.
func Build(drv dialect.Driver, deps *Deps) app.Builder { return builder{drv, deps} }

type builder struct {
	drv  dialect.Driver
	deps *Deps
}

func (b builder) Build(next app.Server) (app.Server, error) { return New(next, b.drv, b.deps), nil }

// WithDriver carries the driver it is given: inside a batch that driver is the
// batch's transaction, and a transaction begun on it joins rather than opens a
// second one.
func (s Domain) WithDriver(drv dialect.Driver) (app.Server, error) {
	next, err := enttx.Rebind(s.Next(), drv)
	if err != nil {
		return nil, err
	}

	return New(next, drv, s.deps), nil
}

// Tx is one operation in progress.
type Tx struct {
	ctx context.Context
	// base is the context the transaction began with: what each event it
	// begins is begun on ([Tx.begin]), so that a second is part of what the
	// transaction is part of, as the first is, and not of the first.
	base context.Context
	next app.Server
	db   *ent.Client
	deps *Deps
	// drv is the transaction's driver: a layer built on it joins this
	// transaction, which is how one operation calls another.
	drv dialect.Driver

	actor  pdid.Id
	tenant pdid.Id
	now    time.Time

	// locked is whether this operation holds a lock it may have waited at,
	// and has taken its moment again since ([Tx.holds]).
	locked bool

	// past is where the operation's event and the facts it writes are
	// recorded, on its transaction, and what a read of either asks
	// (payday#151): payday's past, told the time by this operation's clock,
	// so that its event, its facts and its time rows are of one moment.
	// Made when a read first asks, for a read outside a transaction
	// ([Tx.store]).
	past past.Store

	// op is the operation once it has begun ([Tx.begin]): what every row it
	// writes points at ([Tx.ev]), and what writes its facts. An operation
	// another one in the same transaction calls is part of that one -- its
	// event's cause is the caller's (design D34) -- which payday's past reads
	// off the context the caller began with.
	op *past.Op

	// win is the tenant's history windows, once something asked.
	win *retention.Window

	// own is the workspace's zone, currency and check-in rule, once
	// something asked ([Tx.settings]).
	own *workspaceSettings

	// shared is what every operation in this transaction shares, the ones
	// it is made of included ([sharedOf]).
	shared *shared
}

// shared is kept for a whole transaction, across the operations one is made
// of: each is a Tx of its own on the same driver ([Tx.layer]), and what one
// did is what the next need not do again.
type shared struct {
	// bumped is the containers whose watchers were already told this
	// transaction ([Tx.bump]): told once, they look once, and see all of it.
	bumped map[uuid.UUID]bool

	// forgot is what the trail's erasure was asked of in this transaction
	// ([Tx.forget]), the archive's half of which it owes: paid by the
	// operation that began the transaction, once it has committed
	// ([Deps.Pay]).
	forgot []pdid.Id

	// schedules is the assets whose schedule this transaction holds, by the
	// asset ([Tx.lockBookables]): what nothing else books meanwhile, and so
	// what it may give to a reservation waiting for it ([Tx.heal]).
	schedules map[uuid.UUID]bool
}

type sharedKey struct{}

// sharedOf is the transaction's [shared], made by the outermost operation and
// found on the context by every operation it calls -- `drv` being one already
// in that transaction. An operation that begins a transaction of its own,
// whatever context it was handed (the deployment's own work in a tenant, say),
// shares nothing with the one that called it: what that one did may yet be
// rolled back.
func sharedOf(ctx context.Context, drv dialect.Driver) (context.Context, *shared) {
	if in, ok := drv.(dialect.InTxDriver); ok && in.InTx() {
		if v, ok := ctx.Value(sharedKey{}).(*shared); ok {
			return ctx, v
		}
	}
	v := &shared{bumped: map[uuid.UUID]bool{}, schedules: map[uuid.UUID]bool{}}
	return context.WithValue(ctx, sharedKey{}, v), v
}

// errDone is an operation that already happened: its op was seen before. The
// past's own ([past.ErrDone]), which [Tx.begin] answers as it is, so that a
// copy refused there and one refused by [Tx.done] read alike.
var errDone = past.ErrDone

// tx runs `f` as one transaction, for the caller in `ctx`.
func (s Domain) tx(ctx context.Context, f func(t *Tx) error) error {
	fr, err := gate.Actor(ctx)
	if err != nil {
		return err
	}

	return s.deps.run(ctx, s.Next(), s.drv, fr.Actor, fr.Tenant, f)
}

// read runs `f` outside any transaction, for the caller in `ctx`.
func (s Domain) read(ctx context.Context) (*Tx, error) {
	fr, err := gate.Actor(ctx)
	if err != nil {
		return nil, err
	}

	return &Tx{
		ctx:    ctx,
		base:   ctx,
		next:   s.Next(),
		db:     ent.NewClient(ent.Driver(s.drv)),
		deps:   s.deps,
		drv:    s.drv,
		actor:  fr.Actor,
		tenant: fr.Tenant,
		now:    moment(s.deps.now()),
	}, nil
}

// store is the past the operation reads: its transaction's, or -- for a read
// outside one -- the past on the driver this layer was built with, made the
// first time it is asked.
func (t *Tx) store() (past.Store, error) {
	if t.past != nil {
		return t.past, nil
	}
	st, err := pd.Past(t.next, t.drv, t.audit())
	if err != nil {
		return nil, err
	}
	t.past = past.WithClock(st, func() time.Time { return t.now })
	return t.past, nil
}

// moment is a time as the database keeps it, and as payday's past writes one
// down: in UTC, to the microsecond. An operation's own moment is one, so that
// what it writes and what it reads back are the same instant.
func moment(v time.Time) time.Time { return v.UTC().Truncate(time.Microsecond) }

// run is a transaction for an actor in a tenant, over `server`. A deployment's
// own work passes pdid.Nil as the actor and an ungated server.
func (d *Deps) run(ctx context.Context, server app.Server, drv dialect.Driver, actor, tenant pdid.Id, f func(t *Tx) error) error {
	// Whether this begins the transaction or joins one: what it forgot is
	// paid by the one that began it, which is the one whose commit counts.
	began := !dialect.InTx(drv)
	tdrv, tx, err := dialect.BeginTx(ctx, drv)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	next, err := enttx.Rebind(server, tdrv)
	if err != nil {
		return err
	}

	ctx, sh := sharedOf(ctx, drv)
	st, err := pd.Past(next, tdrv, d.Audit)
	if err != nil {
		return err
	}
	t := &Tx{
		ctx:    ctx,
		base:   ctx,
		next:   next,
		db:     ent.NewClient(ent.Driver(tdrv)),
		deps:   d,
		drv:    tdrv,
		actor:  actor,
		tenant: tenant,
		now:    moment(d.now()),
		shared: sh,
	}
	t.past = past.WithClock(st, func() time.Time { return t.now })
	if err := f(t); err != nil {
		if errors.Is(err, errDone) {
			return errDone
		}
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}

	// The archive's half of what it forgot, owed now that it has committed.
	// A payment that fails has undone nothing: the debt is written, and the
	// trail's next pass pays it. Not the caller's to cancel, since what it
	// asked for is done: a caller gone away leaves it to finish.
	if began && len(sh.forgot) > 0 && d.Pay != nil {
		if err := d.Pay(context.WithoutCancel(ctx), sh.forgot); err != nil {
			log.From(ctx).WarnContext(ctx, "domain: the trail's erasure is owed, for its next pass to pay", "objects", len(sh.forgot), "err", err)
		}
	}

	return nil
}

// System runs `f` as the deployment's own work in a tenant: an expired hold,
// an overdue loan. `server` is the ungated stack.
//
// The context is framed as nobody's work in the tenant, unless it is framed in
// that tenant already -- an invitation's answer is, as the membership it
// makes: the past reads who did an operation, and where, off the frame
// (payday#151), and an operator's command or a sweep comes with none.
func (d *Deps) System(ctx context.Context, server app.Server, drv dialect.Driver, tenant pdid.Id, f func(t *Tx) error) error {
	if fr, err := gate.Actor(ctx); err != nil || fr.Tenant != tenant {
		ctx = frame.Into(ctx, frame.New(pdid.Nil, tenant, frame.Grant{}).WithScope(frame.Only(tenant)))
	}
	return d.run(ctx, server, drv, pdid.Nil, tenant, f)
}

// layer is this layer on the transaction, for an operation that is made of
// others: what it calls joins this transaction and records its own events.
func (t *Tx) layer() Domain { return New(t.next, t.drv, t.deps) }

// tenantRef is the caller's tenant, as a reference.
func (t *Tx) tenantRef() *app.TenantRef {
	return app.TenantRef_builder{Id: t.tenant.Bytes()}.Build()
}

// at reads the moment an operation says it happened, and refuses the future:
// what is recorded is what happened, and a reservation is what is planned.
func (t *Tx) at(v *timestamppb.Timestamp, field string) (time.Time, error) {
	if v == nil || (v.GetSeconds() == 0 && v.GetNanos() == 0) {
		return t.now, nil
	}

	at := moment(v.AsTime())
	if at.After(t.now.Add(5 * time.Minute)) {
		return time.Time{}, invalid(field, "미래 시각은 기록할 수 없습니다. 이미 일어난 일을 기록합니다")
	}

	return at, nil
}

// done answers errDone when `op` names an operation that already happened
// here: what [Tx.begin] answers it for, asked before an operation checks
// anything its first copy could have changed. A correction sent again finds its
// row superseded -- by itself -- and would answer that, where the first copy
// answered success; a return sent again finds the loan closed, a booking its
// own allocation in the way. A copy sent while the first is still running
// waits for it here ([Tx.claim]), and then sees it.
func (t *Tx) done(op []byte) error {
	if len(op) == 0 {
		return nil
	}
	v, err := pdid.From(op)
	if err != nil || v.Domain() != pd.EventDomain {
		// Not one; [Tx.begin] says what is wrong with it.
		return nil
	}
	return t.claim(v)
}

// claim waits for every other transaction holding `op` to end, holds it until
// this one does, and then answers errDone when the operation happened. On
// PostgreSQL copies sent at once run at once, and without the wait two of them
// would both find nothing, both check, and the second fail on the event's key
// rather than answer what the first did. SQLite runs one writer at a time, so
// the copy after has nothing to wait for.
func (t *Tx) claim(op pdid.Id) error {
	if t.postgres() {
		u := op.Uuid()
		key := int64(binary.BigEndian.Uint64(u[8:]))
		if err := t.drv.Exec(t.ctx, "SELECT pg_advisory_xact_lock($1)", []any{key}, nil); err != nil {
			return err
		}
	}
	return t.seen(op)
}

// made names the `n`th thing an operation makes after the operation, when the
// caller named one: the operation's identifier in the thing's domain, the
// `n`th differing from the first in its last bytes, which are random. A copy
// sent again finds what the first copy made by the same name, as a client
// that names the asset it adds does (`AssetAddRequest.id`). Without an
// operation, the name is a new one.
func made(op []byte, d pdid.Domain, n int) pdid.Id {
	v, err := pdid.From(op)
	if err != nil || v.Domain() != pd.EventDomain {
		return pdid.New(d)
	}
	v[14] ^= byte(n >> 8)
	v[15] ^= byte(n)
	return pdid.WithDomain(v, d)
}

// seen answers errDone when an event with this identifier is here already.
func (t *Tx) seen(op pdid.Id) error {
	n, err := t.db.Event.Query().
		Where(event.Id(op.Uuid()), event.TenantId(t.tenant.Uuid())).
		Count(t.ctx)
	if err != nil {
		return err
	}
	if n > 0 {
		return errDone
	}
	return nil
}

// begin records the event this operation is (payday#151): of `kind`, holding
// `v` -- its kind's message (`loft/past.proto`) -- about `subject`, happened
// at `at`, for `reason`: the words of whoever asked, and nothing Loft says
// itself. It answers errDone when `op` names one that already happened.
//
// Who did it and in which tenant are the caller's frame, which the past reads
// for itself; and when the transaction is part of an operation that called
// this one ([Tx.layer]), the event is part of that. One transaction that
// records more than one -- a sweep, an account leaving that hands its
// workspace on -- records each beside the others, none part of another. What
// the operation writes from then on points at the last ([Tx.ev]), and its
// facts are its own.
func (t *Tx) begin(op []byte, kind string, subject pdid.Id, at time.Time, reason string, v proto.Message) error {
	var id pdid.Id
	if len(op) > 0 {
		v, err := pdid.From(op)
		if err != nil {
			return invalid("op", "작업 식별자가 올바르지 않습니다: %v", err)
		}
		if v.Domain() != pd.EventDomain {
			return invalid("op", "작업 식별자가 올바르지 않습니다")
		}
		id = v
	}

	ctx, o, err := past.Begin(t.base, t.past, past.Event{Id: id, Kind: kind, Subject: subject, At: at, Reason: reason, Value: v})
	if err != nil {
		return err
	}
	t.ctx, t.op = ctx, o
	return nil
}

// ev is the event every row this operation writes points at: its own, or the
// child's it is writing for ([Tx.child]). Nothing before it began.
func (t *Tx) ev() pdid.Id {
	if t.op == nil {
		return pdid.Nil
	}
	return t.op.Id()
}

// child records what this operation did to one of the many things it is over,
// as an event of that thing's own -- of `kind`, holding `v` -- `subject` its
// subject and this operation's event its cause (design D34). The rows `f`
// writes point at it, and the facts it writes are its, so that the thing's
// history holds what happened to it, and the operation's event says what the
// operation was.
//
// It is for what an operation writes itself. One it calls through [Tx.layer]
// records its own event, which is a child of this one's already.
func (t *Tx) child(kind string, subject pdid.Id, at time.Time, reason string, v proto.Message, f func() error) error {
	parent := t.op
	c, err := parent.Child(t.ctx, past.Event{Kind: kind, Subject: subject, At: at, Reason: reason, Value: v})
	if err != nil {
		return err
	}
	if f == nil {
		return nil
	}

	t.op = c
	defer func() { t.op = parent }()
	return f()
}

// lockTree takes the tenant's one tree lock, so that nothing else changes the
// placement tree until this transaction ends (design 3.2, D9).
//
// It is a plain UPDATE rather than a Patch through the stack: the row is
// bookkeeping, not a fact anybody watches or audits, and what matters is only
// that the statement takes the row's lock.
//
// And then it takes the operation's moment again. One that began first and
// waited here for another is applied after it, so it is recorded after it:
// known at any instant, the tree is what had been applied by then, in the
// order it was (loft#21; decided in loft#35). Taken before the lock, the
// moment of the one that waited was earlier than the one it waited for, and
// known at an instant between the two, its rows showed beside the state it
// was applied over. So an operation that changes the tree takes this lock
// before it reads its moment -- `t.now`, or `t.at` with nothing given -- or
// checks anything against now.
func (t *Tx) lockTree() error {
	n, err := t.db.TreeLock.Update().
		Where(treelock.TenantId(t.tenant.Uuid())).
		AddVersion(1).
		Save(t.ctx)
	if err != nil {
		return err
	}
	if n == 0 {
		// The first change in a tenant. Through ent, as every write of it
		// is: the lock is bookkeeping, and no server writes it, so it is no
		// row of the trail either (payday#108). Two first changes at once
		// make one, and the second then holds it as it would have held one
		// that was there.
		now := t.deps.now()
		if err := t.insertOnce(treelock.Table, []string{treelock.FieldTenantId},
			[]string{treelock.FieldId, treelock.FieldTenantId, treelock.FieldVersion, treelock.FieldDateCreated, treelock.FieldDateUpdated},
			pdid.New(pd.TreeLockDomain).Uuid(), t.tenant.Uuid(), 0, now, now,
		); err != nil {
			return err
		}
		if _, err := t.db.TreeLock.Update().
			Where(treelock.TenantId(t.tenant.Uuid())).
			AddVersion(1).
			Save(t.ctx); err != nil {
			return err
		}
	}

	t.holds()
	return nil
}

// insertOnce writes a row of bookkeeping -- a lock, a counter -- that the
// first operation in a tenant to need it makes, unless the row `unique` names
// is there already. One that another transaction is writing meanwhile is
// waited for and left as it is: the statement is one that says so, which
// ent's own Create cannot, and without it the second of two first operations
// at once was refused on the row's index.
func (t *Tx) insertOnce(table string, unique, columns []string, values ...any) error {
	q, args := sql.Dialect(t.drv.Dialect()).
		Insert(table).
		Columns(columns...).
		Values(values...).
		OnConflict(sql.ConflictColumns(unique...), sql.DoNothing()).
		Query()
	return t.drv.Exec(t.ctx, q, args, nil)
}

// holds takes the operation's moment again the first time it holds a lock it
// may have waited at -- the tree's ([Tx.lockTree]) or a reservation's
// ([Tx.lockReservations]) -- so that one that waited for another is recorded
// after it. Once: an operation's rows are all of one moment.
func (t *Tx) holds() {
	if !t.locked {
		t.locked = true
		t.now = moment(t.deps.now())
	}
}

// invalid is a refusal about one field of a request: the sentence a person
// reads, and the field beside it, in a BadRequest, for a form to point at.
func invalid(field, format string, args ...any) error {
	return refuse(codes.InvalidArgument, field, fmt.Sprintf(format, args...))
}

func refuse(c codes.Code, field, msg string) error {
	st := status.New(c, msg)
	v, err := st.WithDetails(&errdetails.BadRequest{FieldViolations: []*errdetails.BadRequest_FieldViolation{{Field: field, Description: msg}}})
	if err != nil {
		return st.Err()
	}
	return v.Err()
}

// window is the caller's tenant's history windows now (design 8), read once
// per operation.
func (t *Tx) window() (retention.Window, error) {
	if t.win != nil {
		return *t.win, nil
	}

	var d retention.Defaults
	if t.deps != nil {
		d = t.deps.Retention
	}
	w, err := retention.Of(t.ctx, t.db, t.tenant, t.now, d)
	if err != nil {
		return retention.Window{}, err
	}

	t.win = &w
	return w, nil
}

// when is a moment as a person here reads one, in a refusal.

// minutes is a duration as a person says it.
func minutes(n int32) string {
	if n%60 == 0 {
		return fmt.Sprintf("%d시간", n/60)
	}
	return fmt.Sprintf("%d분", n)
}

// failed is a refusal because of the state things are in.
func failed(format string, args ...any) error {
	return status.Errorf(codes.FailedPrecondition, format, args...)
}

// idOf reads an identifier out of bytes, and the zero one out of nothing.
func idOf(b []byte) pdid.Id {
	if len(b) == 0 {
		return pdid.Nil
	}
	v, err := pdid.From(b)
	if err != nil {
		return pdid.Nil
	}
	return v
}

func uuidOf(b []byte) uuid.UUID { return idOf(b).Uuid() }

func ts(v time.Time) *timestamppb.Timestamp { return timestamppb.New(v) }

func tsp(v *time.Time) *timestamppb.Timestamp {
	if v == nil {
		return nil
	}
	return timestamppb.New(*v)
}

// frameOf is the caller, for reads that do not need a transaction.
func frameOf(ctx context.Context) (*frame.Frame, error) { return gate.Actor(ctx) }
