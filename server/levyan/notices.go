package levyan

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"
	"uuid"

	"github.com/lesomnus/otx/log"
	"github.com/protobuf-orm/ent/dialect/sql"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/lesomnus/payday/pdid"

	"github.com/lesomnus/loft/internal/ent"
	"github.com/lesomnus/loft/internal/ent/levyannotice"
	"github.com/lesomnus/loft/server/pd"
)

// What became of a workspace, as levyan is told it (levyan#14, design 7.8).
//
// levyan bills a workspace to its payer, a roster account. Who that is, is
// the portal's to tell it -- who made a workspace, who no longer is in one
// (#326, D84) -- and nothing here says it. What levyan cannot know by itself
// and Loft can is what became of a workspace: that it was closed, which it is
// billed for none of until it is restored, or destroyed, after which it is
// billed for nothing (levyan's `docs/apps.md`, 5). So Loft tells it, by
// levyan's `WorkspaceService`: `Close`, `Restore` and `Destroy`.
//
// # Beside the change, and told after it
//
// A notice ([Notices]) is a row (`LevyanNotice`) written in the transaction
// of the change it tells -- a workspace closed or restored -- so a change that
// commits is one levyan will be told of, and one rolled back is not; and no
// change waits for levyan, nor fails for it. A destruction is no one
// transaction: its notice is written once its final reading is handed over and
// before anything of it is taken, of a moment after that reading, and it
// outlives the workspace's rows (`closing.Destroy`). A loop of its own
// ([Teller], `loft.levyan.notices`) tells them, and marks each told once levyan
// has answered it.
//
// A workspace's notices are told in the order they happened, each only once
// the one before it is: what became of a workspace is said of a moment, which
// levyan reads them in the order of -- so one told late takes its place, and
// one told again is told of the same moment. Another workspace's notices do
// not wait for this one's.
//
// # What fails
//
// A levyan that cannot be reached holds nothing up: the notices wait, the
// loop asks again a little later each time, up to a minute, and tells them
// once it answers. levyan takes each twice as once -- said again, every call
// changes nothing -- so a notice told by two processes around a failover, or
// told and not marked, is told once. A notice levyan refused is one it will
// refuse again (an identifier that is not one; a workspace closed after it
// was destroyed, or closed and restored at one moment, which is a fault of
// Loft's): it is marked told with what levyan said, logged, and not told
// again, as a reading levyan refused is not handed over again ([Client.Send]).
// levyan saying it heard nothing of the workspace (NotFound) is one of those:
// nobody told it the workspace arrived, so it bills nobody for it, and nothing
// here can tell it who would pay.

// The kinds of notice, as `LevyanNotice.kind` holds them.
const (
	// Closed is a workspace closed, at the moment it says it was
	// (`Tenant.date_closed`): `Close`. From then until it is restored it is
	// billed for none of what it holds, and still measured.
	Closed = "close"

	// Restored is a closed workspace opened again: `Restore`, billed again
	// from then.
	Restored = "restore"

	// Destroyed is a workspace destroyed, for good: `Destroy`, at a moment
	// after its final reading, before anything of it was taken.
	Destroyed = "destroy"

	// Past is no call of levyan's: the mark, written told, that the closes
	// and restores the workspaces' events kept from before this deployment
	// told levyan of any were written to be told -- once, by the loop's first
	// pass ([Teller.Backfill]).
	Past = "past"
)

// How a notice came to be written, as `LevyanNotice.origin` holds it.
const (
	// ByChange is a notice the change it tells wrote, in its transaction.
	ByChange = "change"

	// ByBackfill is what became of the workspaces before this deployment told
	// levyan of it, told by the loop's first pass ([Teller.Backfill]).
	ByBackfill = "backfill"
)

// The domain's events of a workspace closed and restored (`server/domain`),
// whose kinds the past keeps under these names for good
// (`proto/loft/payday/past.lock`): what the first pass reads what became of
// the workspaces before from.
const (
	closeKind   = "workspace.close"
	restoreKind = "workspace.restore"
)

// Notices writes what levyan is to be told of what became of a workspace, on
// the client of the transaction of the change that tells it. A nil one writes
// nothing: a deployment that bills nobody tells levyan nothing.
type Notices struct {
	// Now is the clock a notice is stamped by: the deployment's, the domain
	// layer's (`domain.Deps.Clock`).
	Now func() time.Time
}

func (n *Notices) now() time.Time {
	if n == nil || n.Now == nil {
		return moment(time.Now())
	}
	return moment(n.Now())
}

// moment is a time as the database keeps one, and levyan: in UTC, to the
// microsecond.
func moment(v time.Time) time.Time { return v.UTC().Truncate(time.Microsecond) }

// Close is `workspace` closed at `at`, which is what it says it was
// (`Tenant.date_closed`): by an operator, or the deployment when nobody was
// left in it, whoever closed it.
func (n *Notices) Close(ctx context.Context, db *ent.Client, workspace pdid.Id, at time.Time) error {
	if n == nil {
		return nil
	}
	return n.write(ctx, db, Closed, workspace, at, ByChange)
}

// Restore is the closed `workspace` opened again at `at`: the notice keeps the
// moment, which nothing else does once the workspace no longer says it was
// closed.
func (n *Notices) Restore(ctx context.Context, db *ent.Client, workspace pdid.Id, at time.Time) error {
	if n == nil {
		return nil
	}
	return n.write(ctx, db, Restored, workspace, at, ByChange)
}

// Destroy is `workspace` destroyed at `at`, a moment after its final reading,
// written before anything of it is taken (`closing.Destroy`). Once of a
// workspace: a destruction that stopped part of the way, run again, told
// levyan when it first ran, and is told nothing more.
func (n *Notices) Destroy(ctx context.Context, db *ent.Client, workspace pdid.Id, at time.Time) error {
	if n == nil {
		return nil
	}
	told, err := db.LevyanNotice.Query().
		Where(levyannotice.Workspace(workspace.Uuid()), levyannotice.KindEQ(Destroyed)).
		Exist(ctx)
	if err != nil || told {
		return err
	}
	return n.write(ctx, db, Destroyed, workspace, at, ByChange)
}

func (n *Notices) write(ctx context.Context, db *ent.Client, kind string, workspace pdid.Id, at time.Time, origin string) error {
	return n.create(db, kind, workspace, at, origin).Exec(ctx)
}

func (n *Notices) create(db *ent.Client, kind string, workspace pdid.Id, at time.Time, origin string) *ent.LevyanNoticeCreate {
	return db.LevyanNotice.Create().
		SetId(pdid.New(pd.LevyanNoticeDomain).Uuid()).
		SetKind(kind).
		SetWorkspace(workspace.Uuid()).
		SetDateOccurred(moment(at)).
		SetDateCreated(n.now()).
		SetOrigin(origin).
		SetRefused("")
}

// Workspaces is what a [Teller] tells: levyan's `WorkspaceService`, as
// [Client] calls it.
type Workspaces interface {
	CloseWorkspace(ctx context.Context, workspace pdid.Id, at time.Time) error
	RestoreWorkspace(ctx context.Context, workspace pdid.Id, at time.Time) error
	DestroyWorkspace(ctx context.Context, workspace pdid.Id, at time.Time) error
}

var _ Workspaces = (*Client)(nil)

// NoticesName is what the loop that tells levyan the notices is called, under
// a lease too: one process of the deployment tells them.
const NoticesName = "loft.levyan.notices"

// NoticesEvery is how often the loop looks for notices to tell, while levyan
// answers.
const NoticesEvery = 10 * time.Second

// noticesMost is how long the loop waits at most before it asks a levyan
// that did not answer again, doubling from [NoticesEvery].
const noticesMost = time.Minute

// noticesPage is how many waiting notices a pass reads at a time,
// tenantsPage how many workspaces the first pass looks at at a time, and
// writesPage how many notices it writes in one statement.
const (
	noticesPage = 100
	tenantsPage = 100
	writesPage  = 100
)

// Teller tells levyan what the notices say ([Notices]): the loop
// `loft.levyan.notices`, every [NoticesEvery] -- longer after a pass levyan
// did not answer -- under the lease.
//
// Its first pass in a process also writes what levyan was never told: every
// workspace it was never told arrived, and what became of the workspaces
// before it was told of that ([Teller.Backfill]).
type Teller struct {
	// Db is the deployment's database: the notices, and the workspaces.
	Db *ent.Client

	// Levyan is who is told.
	Levyan Workspaces

	// Now is the clock; nil is the system's.
	Now func() time.Time

	mu         sync.Mutex
	backfilled bool
	wait       time.Duration
	next       time.Time
}

func (t *Teller) now() time.Time {
	if t.Now == nil {
		return moment(time.Now())
	}
	return moment(t.Now())
}

// Told is what one telling did.
type Told struct {
	// Told is the notices levyan took, and Refused the ones it refused:
	// both told, and not told again. A notice of a workspace levyan heard
	// nothing of is one of the refused: levyan has nothing to bill of it.
	Told, Refused int

	// Waiting is the notices it could not be told, and are told later: the
	// one levyan did not answer, and every later one of its workspace.
	Waiting int
}

// Pass is one pass of the loop: what levyan was never told, until that has
// been written once in this process, and then whatever waits -- unless levyan
// did not answer the pass before, and it is not time to ask again. It logs
// what failed and answers nil: a levyan that cannot be reached is a thing to
// ask again, and never a reason to stop serving.
func (t *Teller) Pass(ctx context.Context) error {
	t.mu.Lock()
	defer t.mu.Unlock()

	if !t.backfilled {
		if _, err := t.backfill(ctx); err != nil {
			log.From(ctx).WarnContext(ctx, "levyan: what it was never told could not be found, and is looked for again next pass", "err", err)
		} else {
			t.backfilled = true
		}
	}

	now := t.now()
	if now.Before(t.next) {
		return nil
	}
	x, err := t.tell(ctx)
	if err == nil && (x.Waiting == 0 || x.Told+x.Refused > 0) {
		t.wait, t.next = 0, time.Time{}
		return nil
	}

	// Nothing was told: levyan did not answer, or answered every one of them
	// with a failure. Asked again later each time.
	t.wait = min(max(2*t.wait, NoticesEvery), noticesMost)
	t.next = now.Add(t.wait)
	if err == nil {
		err = errors.New("levyan answered every notice it was told with a failure")
	}
	log.From(ctx).WarnContext(ctx, "levyan: what it is to be told of the workspaces waits, since levyan could not be told it",
		"waiting", x.Waiting, "again_in", t.wait.String(), "err", err)
	return nil
}

// Tell tells levyan what waits, now: each workspace's notices in the order
// they happened, a workspace stopping at the first levyan does not answer.
// It answers what it did, and an error for a levyan that could not be reached
// at all, which stops it where it was.
func (t *Teller) Tell(ctx context.Context) (Told, error) {
	t.mu.Lock()
	defer t.mu.Unlock()

	return t.tell(ctx)
}

func (t *Teller) tell(ctx context.Context) (Told, error) {
	var out Told
	held := map[uuid.UUID]bool{}
	var last *ent.LevyanNotice
	for {
		q := t.Db.LevyanNotice.Query().Where(levyannotice.DateToldIsNil())
		if last != nil {
			at, id := last.DateOccurred, last.Id
			q = q.Where(func(s *sql.Selector) {
				s.Where(sql.CompositeGT([]string{s.C(levyannotice.FieldDateOccurred), s.C(levyannotice.FieldId)}, at, id))
			})
		}
		vs, err := q.Order(ent.Asc(levyannotice.FieldDateOccurred), ent.Asc(levyannotice.FieldId)).Limit(noticesPage).All(ctx)
		if err != nil {
			return out, err
		}
		for _, v := range vs {
			last = v
			if held[v.Workspace] {
				out.Waiting++
				continue
			}

			err := t.say(ctx, v)
			switch {
			case err == nil:
				if err := t.mark(ctx, v, ""); err != nil {
					return out, err
				}
				out.Told++
			case refused(err):
				t.refusal(ctx, v, err)
				if err := t.mark(ctx, v, status.Convert(err).Message()); err != nil {
					return out, err
				}
				out.Refused++
			case unreachable(ctx, err):
				out.Waiting = t.waiting(ctx)
				return out, err
			default:
				// This one waits, and so does everything after it of its
				// workspace; the others are told.
				log.From(ctx).WarnContext(ctx, "levyan: a notice was not taken, and is told again later with what follows it of its workspace",
					"workspace", pdid.Id(v.Workspace).String(), "kind", v.Kind, "err", err)
				held[v.Workspace] = true
				out.Waiting++
			}
		}
		if len(vs) < noticesPage {
			return out, nil
		}
	}
}

// refusal says in the log what levyan refused, which is not told again. A
// workspace levyan heard nothing of (NotFound) is one nobody told it arrived:
// it bills nobody for it, and whoever tells it who pays -- the portal (#326)
// -- never did. Anything else refused is a fault of Loft's: something it said
// that is not so -- a workspace closed after it was destroyed, or closed and
// restored at one moment -- and is looked into, not told again.
func (t *Teller) refusal(ctx context.Context, v *ent.LevyanNotice, err error) {
	if status.Code(err) == codes.NotFound {
		log.From(ctx).InfoContext(ctx, "levyan: what became of a workspace it heard nothing of is not told again, since nobody is billed for it",
			"workspace", pdid.Id(v.Workspace).String(), "kind", v.Kind, "err", err)
		return
	}
	log.From(ctx).WarnContext(ctx, "levyan: what became of a workspace was refused, which is a fault of Loft's to look into; it is not told again",
		"workspace", pdid.Id(v.Workspace).String(), "kind", v.Kind, "at", v.DateOccurred.UTC().Format(time.RFC3339Nano), "origin", v.Origin, "err", err)
}

// waiting is how many notices wait, for what a pass that stopped says.
func (t *Teller) waiting(ctx context.Context) int {
	n, err := t.Db.LevyanNotice.Query().Where(levyannotice.DateToldIsNil()).Count(context.WithoutCancel(ctx))
	if err != nil {
		return 0
	}
	return n
}

// say is one notice, told.
func (t *Teller) say(ctx context.Context, v *ent.LevyanNotice) error {
	ws := pdid.Id(v.Workspace)
	switch v.Kind {
	case Closed:
		return t.Levyan.CloseWorkspace(ctx, ws, v.DateOccurred)
	case Restored:
		return t.Levyan.RestoreWorkspace(ctx, ws, v.DateOccurred)
	case Destroyed:
		return t.Levyan.DestroyWorkspace(ctx, ws, v.DateOccurred)
	}

	// A kind a later build writes, which this one cannot tell: it waits for
	// that build, and what follows it of its workspace with it.
	return fmt.Errorf("levyan: a notice of %q, which this build does not tell", v.Kind)
}

// mark is a notice told, with what levyan said if it refused it.
func (t *Teller) mark(ctx context.Context, v *ent.LevyanNotice, why string) error {
	return t.Db.LevyanNotice.Update().
		Where(levyannotice.Id(v.Id), levyannotice.DateToldIsNil()).
		SetDateTold(t.now()).
		SetRefused(why).
		Exec(ctx)
}

// refused is an answer levyan would give again: what it was told is not
// something it takes.
func refused(err error) bool {
	switch status.Code(err) {
	case codes.InvalidArgument, codes.FailedPrecondition, codes.NotFound, codes.AlreadyExists, codes.OutOfRange:
		return true
	}
	return false
}

// unreachable is levyan not there, or not answering in time -- or the
// process going away -- which every notice after this one would meet too.
func unreachable(ctx context.Context, err error) bool {
	if ctx.Err() != nil {
		return true
	}
	switch status.Code(err) {
	case codes.Unavailable, codes.DeadlineExceeded, codes.Canceled:
		return true
	}
	return false
}

// Backfill writes what levyan was never told, as the loop starts in a
// process: what became of the workspaces before this deployment told levyan
// of it -- or while it told it nothing, `app.billing.levyan` unset -- and so
// nothing since, which each change told ([Teller.ends]). It answers how many
// notices it wrote.
func (t *Teller) Backfill(ctx context.Context) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()

	return t.backfill(ctx)
}

func (t *Teller) backfill(ctx context.Context) (int, error) {
	e, err := t.ends(ctx)
	if err != nil {
		return e.total(), err
	}
	if e.total() > 0 {
		log.From(ctx).InfoContext(ctx, "levyan: what became of the workspaces before it was told of it is to be told it",
			"closed", e.closed, "restored", e.restored)
	}
	return e.total(), nil
}
