// Package domain is the layer that answers what rove means: the operations a
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
// to the caller's tenant by hand; [Tx.tenant] is the value they all use.
package domain

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
	"uuid"

	"github.com/lesomnus/payday/frame"
	"github.com/lesomnus/payday/gate"
	"github.com/lesomnus/payday/pdid"
	"github.com/protobuf-orm/ent/dialect"
	"github.com/protobuf-orm/protoc-gen-orm-ent/runtime/enttx"
	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"

	app "github.com/lesomnus/rove"
	"github.com/lesomnus/rove/internal/ent"
	"github.com/lesomnus/rove/internal/ent/event"
	"github.com/lesomnus/rove/internal/ent/treelock"
	"github.com/lesomnus/rove/internal/identity"
	"github.com/lesomnus/rove/server/mail"
	"github.com/lesomnus/rove/server/pd"
	"github.com/lesomnus/rove/server/retention"
	"github.com/lesomnus/rove/server/storage"
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
	// LabelScheme is "https" in a deployment and "http" on a checkout.
	LabelScheme string
	// LabelPort is appended to label URLs when it is not the scheme's own,
	// which is what a checkout on :8080 needs.
	LabelPort string

	// LookupTXT reads TXT records, for verifying a custom domain.
	LookupTXT func(ctx context.Context, name string) ([]string, error)

	// NoShowAfter is how long after a room reservation begins it is released
	// when nobody checked in. Zero holds nobody to checking in.
	NoShowAfter time.Duration

	// Retention is the windows of a tenant with no contract (design 8). The
	// zero value shows all of the history.
	Retention retention.Defaults

	// Forget blanks what the trail kept of these objects -- its rows in the
	// database `db` is a client on, which is the operation's transaction, and
	// their copies in the archive -- except what a legal hold is on, and
	// answers how much a hold kept (payday's `trail.Policy.Forget`). Nil
	// blanks the database's rows only, which is all there is when the trail
	// has no archive.
	Forget func(ctx context.Context, db *ent.Client, objects []pdid.Id) (held int, err error)

	// People is where the people of a tenant are: roster (design 9.10). Nil
	// is a deployment whose people cannot be changed from here.
	People People

	// Mail sends what goes to somebody's mailbox -- an invitation -- and
	// AppUrl is where the link in it sends a browser. Nil sends nothing.
	Mail   mail.Sender
	AppUrl string

	// Invitations is how long an invitation waits and how many a workspace
	// may have out.
	Invitations InvitationRules

	// Base is the stack with neither the wall nor this layer: what an
	// account's answer to an invitation is written through, in a workspace it
	// is not a member of yet. Never handed to anything a caller reaches; this
	// layer writes through it, after its own checks, and nothing else does.
	Base app.Server
}

// People is roster, as the people operations reach it: the accounts of this
// deployment's directory (design 9.10, D27). On the roster in this process
// Rove makes accounts and gives them passwords there; on an external one those
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
}

// here answers the people an account is made and changed at, or the refusal a
// deployment whose accounts are an external roster's gives.
func (d *Deps) here() (People, error) {
	if d.People == nil || !d.People.Embedded() {
		return nil, failed("계정과 비밀번호는 roster에서 관리합니다")
	}
	return d.People, nil
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
	ctx  context.Context
	next app.Server
	db   *ent.Client
	deps *Deps
	// drv is the transaction's driver: a layer built on it joins this
	// transaction, which is how one operation calls another.
	drv dialect.Driver

	actor  pdid.Id
	tenant pdid.Id
	now    time.Time

	// The event every row written by this operation points at.
	ev pdid.Id

	// win is the tenant's history windows, once something asked.
	win *retention.Window
}

// errDone is an operation that already happened: its op was seen before.
var errDone = errors.New("domain: this operation already happened")

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
		next:   s.Next(),
		db:     ent.NewClient(ent.Driver(s.drv)),
		deps:   s.deps,
		drv:    s.drv,
		actor:  fr.Actor,
		tenant: fr.Tenant,
		now:    s.deps.now(),
	}, nil
}

// run is a transaction for an actor in a tenant, over `server`. A deployment's
// own work passes pdid.Nil as the actor and an ungated server.
func (d *Deps) run(ctx context.Context, server app.Server, drv dialect.Driver, actor, tenant pdid.Id, f func(t *Tx) error) error {
	tdrv, tx, err := dialect.BeginTx(ctx, drv)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	next, err := enttx.Rebind(server, tdrv)
	if err != nil {
		return err
	}

	t := &Tx{
		ctx:    ctx,
		next:   next,
		db:     ent.NewClient(ent.Driver(tdrv)),
		deps:   d,
		drv:    tdrv,
		actor:  actor,
		tenant: tenant,
		now:    d.now(),
	}
	if err := f(t); err != nil {
		if errors.Is(err, errDone) {
			return errDone
		}
		return err
	}

	return tx.Commit()
}

// System runs `f` as the deployment's own work in a tenant: an expired hold,
// an overdue loan. `server` is the ungated stack.
func (d *Deps) System(ctx context.Context, server app.Server, drv dialect.Driver, tenant pdid.Id, f func(t *Tx) error) error {
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

	at := v.AsTime()
	if at.After(t.now.Add(5 * time.Minute)) {
		return time.Time{}, invalid(field, "미래 시각은 기록할 수 없습니다. 이미 일어난 일을 기록합니다")
	}

	return at, nil
}

// begin records the event this operation is, and answers errDone when `op`
// names one that already happened.
func (t *Tx) begin(op []byte, kind string, subject pdid.Id, at time.Time, desc, reason string, payload map[string]string) error {
	var id pdid.Id
	if len(op) > 0 {
		v, err := pdid.From(op)
		if err != nil {
			return invalid("op", "작업 식별자가 올바르지 않습니다: %v", err)
		}
		if v.Domain() != pd.EventDomain {
			return invalid("op", "작업 식별자가 올바르지 않습니다")
		}

		n, err := t.db.Event.Query().
			Where(event.Id(v.Uuid()), event.TenantId(t.tenant.Uuid())).
			Count(t.ctx)
		if err != nil {
			return err
		}
		if n > 0 {
			return errDone
		}

		id = v
	}

	req := app.EventAddRequest_builder{
		Tenant:     t.tenantRef(),
		Kind:       kind,
		OccurredAt: timestamppb.New(at),
		Reason:     reason,
		Desc:       desc,
		Payload:    encode(payload),
	}.Build()
	if !id.IsZero() {
		req.SetId(id.Bytes())
	}
	if !t.actor.IsZero() {
		req.SetActorId(t.actor.Bytes())
	}
	if !subject.IsZero() {
		req.SetSubjectId(subject.Bytes())
	}

	v, err := t.next.Event().Add(t.ctx, req)
	if err != nil {
		return err
	}

	t.ev, err = pdid.From(v.GetId())
	return err
}

// lockTree takes the tenant's one tree lock, so that nothing else changes the
// placement tree until this transaction ends (design 3.2, D9).
//
// It is a plain UPDATE rather than a Patch through the stack: the row is
// bookkeeping, not a fact anybody watches or audits, and what matters is only
// that the statement takes the row's lock.
func (t *Tx) lockTree() error {
	n, err := t.db.TreeLock.Update().
		Where(treelock.TenantId(t.tenant.Uuid())).
		AddVersion(1).
		Save(t.ctx)
	if err != nil {
		return err
	}
	if n > 0 {
		return nil
	}

	// The first change in a tenant that `init` did not seed.
	_, err = t.next.TreeLock().Add(t.ctx, app.TreeLockAddRequest_builder{
		Tenant:  t.tenantRef(),
		Version: 1,
	}.Build())
	return err
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

// window is the caller's tenant's history windows now (design 8.1), read
// once per operation.
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

// since is the oldest moment the caller's tenant may look at, and the zero
// time when it may look at all of its history.
func (t *Tx) since() (time.Time, error) {
	w, err := t.window()
	if err != nil {
		return time.Time{}, err
	}
	return w.Since(t.now), nil
}

// inView refuses a moment before the view window: what a tenant may not look
// at, it may not ask the state of either. OutOfRange rather than
// InvalidArgument, because the same moment is a fine one under a longer
// contract.
func (t *Tx) inView(field string, at time.Time) error {
	w, err := t.window()
	if err != nil {
		return err
	}
	since := w.Since(t.now)
	if since.IsZero() || !at.Before(since) {
		return nil
	}

	return refuse(codes.OutOfRange, field, fmt.Sprintf("이 조직은 최근 %d일(%s부터)의 이력만 볼 수 있습니다",
		int(w.View/(24*time.Hour)), since.In(Zone).Format("2006년 1월 2일")))
}

// gone answers whether a time row is history a view window beginning at
// `since` does not reach: it ended, or it was superseded, before the window
// began. A row the window begins inside of is the state the window begins in.
func gone(since time.Time, to, superseded *time.Time) bool {
	if since.IsZero() {
		return false
	}
	return (to != nil && !to.After(since)) || (superseded != nil && !superseded.After(since))
}

// when is a moment as a person here reads one, in a refusal.
func when(t time.Time) string { return t.In(Zone).Format("1월 2일 15:04") }

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

// encode writes a payload as `k=v` lines, sorted, for a person reading it.
func encode(m map[string]string) string {
	if len(m) == 0 {
		return ""
	}

	ks := sortedKeys(m)
	b := strings.Builder{}
	for _, k := range ks {
		if m[k] == "" {
			continue
		}
		b.WriteString(k)
		b.WriteString("=")
		b.WriteString(m[k])
		b.WriteString("\n")
	}
	return strings.TrimSuffix(b.String(), "\n")
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
