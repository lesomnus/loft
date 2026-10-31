package levyan

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sync"
	"time"
	"uuid"

	"github.com/lesomnus/otx/log"
	"github.com/protobuf-orm/ent/dialect/sql"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/lesomnus/payday/pdid"

	app "github.com/lesomnus/loft"
	"github.com/lesomnus/loft/internal/ent"
	"github.com/lesomnus/loft/internal/ent/levyannotice"
	enttenant "github.com/lesomnus/loft/internal/ent/tenant"
	"github.com/lesomnus/loft/server/bare"
	"github.com/lesomnus/loft/server/pd"
	"github.com/lesomnus/loft/server/tenancy"
)

// Who pays for a workspace, and what became of it, as levyan is told it
// (levyan#14, design 7.6, 7.8).
//
// levyan bills a workspace to its payer, a roster account, one at a time; the
// workspace's maker by default, and whoever accepts after that (levyan's
// design 14). What it cannot know by itself is Loft's: that a workspace was
// made, and by whom, and that an account is no longer in one; and that a
// workspace was closed, which it is billed for none of until it is restored,
// or destroyed, after which it is billed for nothing (levyan's `docs/apps.md`,
// 5). So Loft tells it, by levyan's `WorkspaceService`: `Arrive` and `Leave`,
// `Close`, `Restore` and `Destroy`.
//
// # Beside the change, and told after it
//
// A notice ([Notices]) is a row (`LevyanNotice`) written in the transaction
// of the change it tells -- a workspace made, a membership ended, a workspace
// closed or restored -- so a change that commits is one levyan will be told
// of, and one rolled back is not; and no change waits for levyan, nor fails
// for it. A destruction is no one transaction: its notice is written once its
// final reading is handed over and before anything of it is taken, of a moment
// after that reading, and it outlives the workspace's rows (`closing.Destroy`).
// A loop of its own ([Teller], `loft.levyan.notices`) tells them, and marks
// each told once levyan has answered it.
//
// A workspace's notices are told in the order they happened, each only once
// the one before it is: a membership that ended before levyan heard the
// workspace arrive would otherwise be told of a workspace levyan does not
// know, which it takes as nothing, and then the account that left would pay.
// Another workspace's notices do not wait for this one's. What became of a
// workspace is said of a moment, which levyan reads them in the order of --
// so one told late takes its place, and one told again is told of the same
// moment.
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
//
// One answer is told what to do instead: levyan saying it has heard nothing
// of the workspace a close, a restore or a destruction is of (NotFound). It is
// told the workspace arrived first -- as it was told before, or by the
// workspace's first owner from when it was made -- and then the notice again
// ([Teller.Tell]). A workspace that is gone and was never told arrived is
// nobody's levyan bills, and its notice is marked told with what levyan said.

// The kinds of notice, as `LevyanNotice.kind` holds them.
const (
	// Arrived is a workspace made: `Arrive`, its maker the account, from when
	// it was made.
	Arrived = "arrive"

	// Left is an account no longer a member of the workspace: `Leave`.
	Left = "leave"

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

// ending says whether a notice of `kind` is of what became of a workspace --
// its end, or a step towards it: said of a moment, and of no account.
func ending(kind string) bool {
	switch kind {
	case Closed, Restored, Destroyed:
		return true
	}
	return false
}

// How a notice came to be written, as `LevyanNotice.origin` holds it.
const (
	// ByChange is a notice the change it tells wrote, in its transaction.
	ByChange = "change"

	// ByBackfill is a workspace made before this deployment told levyan of
	// any, told by the loop's first pass ([Teller.Backfill]) -- its maker its
	// first owner, since nothing recorded who made it -- and what became of
	// the workspaces before this deployment told levyan of it.
	ByBackfill = "backfill"

	// ByNotFound is a workspace's arrival told first because levyan answered
	// a notice of what became of it that it had heard nothing of the
	// workspace ([Teller.Tell]).
	ByNotFound = "notfound"
)

// The domain's events of a workspace closed and restored (`server/domain`),
// whose kinds the past keeps under these names for good
// (`proto/loft/payday/past.lock`): what the first pass reads what became of
// the workspaces before from.
const (
	closeKind   = "workspace.close"
	restoreKind = "workspace.restore"
)

// What an address held says of how its workspace went (`AliasHold.reason`,
// `offboard.ReasonDestroyed` and `ReasonRetired`): a workspace destroyed here,
// whatever became of its trail. The third, `renamed`, is a workspace that
// moved.
var gone = []string{"destroyed", "retired"}

// Notices writes what levyan is to be told of a workspace -- who pays for it,
// what became of it -- on the client of the transaction of the change that
// tells it. A nil one writes nothing: a deployment that bills nobody tells
// levyan nothing.
type Notices struct {
	// Now is the clock a notice is stamped by: the deployment's, the domain
	// layer's (`domain.Deps.Clock`).
	Now func() time.Time
}

var _ tenancy.Told = (*Notices)(nil)

func (n *Notices) now() time.Time {
	if n == nil || n.Now == nil {
		return moment(time.Now())
	}
	return moment(n.Now())
}

// moment is a time as the database keeps one, and levyan: in UTC, to the
// microsecond.
func moment(v time.Time) time.Time { return v.UTC().Truncate(time.Microsecond) }

// Arrive is a workspace made by `creator` at `created`, who pays for it from
// then.
func (n *Notices) Arrive(ctx context.Context, db *ent.Client, workspace, creator pdid.Id, created time.Time) error {
	if n == nil {
		return nil
	}
	return n.write(ctx, db, Arrived, workspace, creator, created, ByChange)
}

// Leave is `account` no longer in `workspace`, from `at`.
func (n *Notices) Leave(ctx context.Context, db *ent.Client, workspace, account pdid.Id, at time.Time) error {
	if n == nil {
		return nil
	}
	return n.write(ctx, db, Left, workspace, account, at, ByChange)
}

// Close is `workspace` closed at `at`, which is what it says it was
// (`Tenant.date_closed`): by its owner, an operator, or the deployment when
// nobody was left in it, whoever closed it.
func (n *Notices) Close(ctx context.Context, db *ent.Client, workspace pdid.Id, at time.Time) error {
	if n == nil {
		return nil
	}
	return n.write(ctx, db, Closed, workspace, pdid.Nil, at, ByChange)
}

// Restore is the closed `workspace` opened again at `at`: the notice keeps the
// moment, which nothing else does once the workspace no longer says it was
// closed.
func (n *Notices) Restore(ctx context.Context, db *ent.Client, workspace pdid.Id, at time.Time) error {
	if n == nil {
		return nil
	}
	return n.write(ctx, db, Restored, workspace, pdid.Nil, at, ByChange)
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
	return n.write(ctx, db, Destroyed, workspace, pdid.Nil, at, ByChange)
}

func (n *Notices) write(ctx context.Context, db *ent.Client, kind string, workspace, account pdid.Id, at time.Time, origin string) error {
	return n.create(db, kind, workspace, account, at, origin).Exec(ctx)
}

func (n *Notices) create(db *ent.Client, kind string, workspace, account pdid.Id, at time.Time, origin string) *ent.LevyanNoticeCreate {
	return db.LevyanNotice.Create().
		SetId(pdid.New(pd.LevyanNoticeDomain).Uuid()).
		SetKind(kind).
		SetWorkspace(workspace.Uuid()).
		SetAccount(account.Uuid()).
		SetAt(moment(at)).
		SetDateCreated(n.now()).
		SetOrigin(origin).
		SetRefused("")
}

// Recorder is what hears every membership that ends, through any of the
// servers -- an administrator's 로그인 중지 or 개인정보 삭제, an account
// leaving the deployment (`domain.Deps.Depart`), an operator's hand -- and
// writes, in the transaction that ends it, that its account is no longer in
// the workspace ([Notices.Leave]). One of no account is nobody levyan knows,
// and tells it nothing.
//
// A membership whose account roster erased only begins to leave
// (`Holder.date_leaving`, `server/lifecycle`), and that is not this: roster may
// restore the account within its grace, and a mark is acted on only once
// roster is asked again. levyan hears roster's erasure itself; this tells it
// when the account does leave, once the grace is over.
func (n *Notices) Recorder() bare.Recorder { return recorder{n} }

type recorder struct{ n *Notices }

func (r recorder) Record(ctx context.Context, s bare.Server, c bare.Change) error {
	if r.n == nil || c.By != app.HolderService_Erase_FullMethodName {
		return nil
	}
	key, ok := c.Key.(uuid.UUID)
	if !ok {
		return fmt.Errorf("levyan: a membership's key is a %T", c.Key)
	}
	h, err := s.Db.Holder.Get(ctx, key)
	if ent.IsNotFound(err) {
		// Taken out whole, as a workspace destroyed is: that it went is not
		// a membership ending.
		return nil
	}
	if err != nil {
		return err
	}
	if h.Account == nil {
		return nil
	}
	at := r.n.now()
	if h.DateErased != nil {
		at = *h.DateErased
	}
	return r.n.Leave(ctx, s.Db, pdid.Id(h.TenantId), pdid.Id(*h.Account), at)
}

// Workspaces is what a [Teller] tells: levyan's `WorkspaceService`, as
// [Client] calls it.
type Workspaces interface {
	Arrive(ctx context.Context, workspace, creator pdid.Id, created time.Time) error
	Leave(ctx context.Context, workspace, account pdid.Id) error
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
	// both told, and not told again. A notice of what became of a workspace
	// that is gone and was never told arrived is one of the refused: levyan
	// has nothing to bill of it.
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
// A close, a restore or a destruction levyan answers it heard nothing of the
// workspace for is told after the workspace's arrival ([Teller.unheard]).
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
	// told is what this pass told out of turn: a workspace's arrival, told
	// before what became of it ([Teller.unheard]).
	told := map[uuid.UUID]bool{}
	var last *ent.LevyanNotice
	for {
		q := t.Db.LevyanNotice.Query().Where(levyannotice.DateToldIsNil())
		if last != nil {
			at, id := last.At, last.Id
			q = q.Where(func(s *sql.Selector) {
				s.Where(sql.CompositeGT([]string{s.C(levyannotice.FieldAt), s.C(levyannotice.FieldId)}, at, id))
			})
		}
		vs, err := q.Order(ent.Asc(levyannotice.FieldAt), ent.Asc(levyannotice.FieldId)).Limit(noticesPage).All(ctx)
		if err != nil {
			return out, err
		}
		for _, v := range vs {
			last = v
			if told[v.Id] {
				continue
			}
			if held[v.Workspace] {
				out.Waiting++
				continue
			}

			err := t.say(ctx, v)
			if ending(v.Kind) && status.Code(err) == codes.NotFound {
				err = t.unheard(ctx, v, &out, told)
			}
			var none nobody
			switch {
			case err == nil:
				if err := t.mark(ctx, v, ""); err != nil {
					return out, err
				}
				out.Told++
			case errors.As(err, &none):
				log.From(ctx).InfoContext(ctx, "levyan: what became of a workspace it heard nothing of is not told, since nobody is billed for it",
					"workspace", pdid.Id(v.Workspace).String(), "kind", v.Kind, "why", string(none))
				if err := t.mark(ctx, v, none.Error()); err != nil {
					return out, err
				}
				out.Refused++
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

// refusal says in the log what levyan refused, which is not told again. What
// became of a workspace, refused, is a fault of Loft's: something it said that
// is not so -- a workspace closed after it was destroyed, or closed and
// restored at one moment -- and is looked into, not told again.
func (t *Teller) refusal(ctx context.Context, v *ent.LevyanNotice, err error) {
	if ending(v.Kind) {
		log.From(ctx).WarnContext(ctx, "levyan: what became of a workspace was refused, which is a fault of Loft's to look into; it is not told again",
			"workspace", pdid.Id(v.Workspace).String(), "kind", v.Kind, "at", v.At.UTC().Format(time.RFC3339Nano), "origin", v.Origin, "err", err)
		return
	}
	log.From(ctx).WarnContext(ctx, "levyan: a notice of who pays was refused, and is not told again",
		"workspace", pdid.Id(v.Workspace).String(), "kind", v.Kind, "account", pdid.Id(v.Account).String(), "origin", v.Origin, "err", err)
}

// unheard is notice `v`, of what became of a workspace, told again after the
// workspace's arrival, which levyan answered `v` it had heard nothing of
// (NotFound): the arrival waiting to be told, or one written now -- as levyan
// was told it before, or, of a workspace still here, by its first owner from
// when it was made, as the first pass tells one ([Teller.arrival]) -- told,
// and then `v`. It answers what levyan said of `v`; or why it is not told:
// there is no arrival to tell ([nobody]), or levyan refused the arrival.
func (t *Teller) unheard(ctx context.Context, v *ent.LevyanNotice, out *Told, told map[uuid.UUID]bool) error {
	a, none, err := t.arrival(ctx, v.Workspace)
	if err != nil {
		return err
	}
	if a == nil {
		return none
	}

	if err := t.say(ctx, a); err != nil {
		if !refused(err) {
			// levyan not there, or not answering it: both wait, the arrival
			// first.
			return err
		}
		t.refusal(ctx, a, err)
		if err := t.mark(ctx, a, status.Convert(err).Message()); err != nil {
			return err
		}
		out.Refused++
		return status.Errorf(codes.NotFound, "levyan heard nothing of the workspace, and refused that it arrived: %s", status.Convert(err).Message())
	}
	if err := t.mark(ctx, a, ""); err != nil {
		return err
	}
	out.Told++
	told[a.Id] = true

	return t.say(ctx, v)
}

// nobody is why there is no arrival to tell levyan before what became of a
// workspace it heard nothing of: the workspace is gone and levyan was never
// told it arrived, or no account owns it. levyan bills nobody for it, and the
// notice is marked told with this.
type nobody string

func (e nobody) Error() string {
	return "levyan heard nothing of the workspace, and there is no arrival to tell it first: " + string(e)
}

// arrival is the workspace's arrival levyan is to be told before what became
// of it: one waiting to be told; or one written now, of origin `notfound` --
// as levyan was told it before, or, of a workspace still here, by its first
// owner from when it was made. A workspace that is gone and was never told
// arrived, or one nobody owns, has none, and why is said.
func (t *Teller) arrival(ctx context.Context, workspace uuid.UUID) (*ent.LevyanNotice, nobody, error) {
	first := func(ps ...func(*sql.Selector)) (*ent.LevyanNotice, error) {
		q := t.Db.LevyanNotice.Query().Where(levyannotice.Workspace(workspace), levyannotice.KindEQ(Arrived))
		for _, p := range ps {
			q = q.Where(p)
		}
		v, err := q.Order(ent.Asc(levyannotice.FieldAt), ent.Asc(levyannotice.FieldId)).First(ctx)
		if ent.IsNotFound(err) {
			return nil, nil
		}
		return v, err
	}

	waits, err := first(levyannotice.DateToldIsNil())
	if err != nil || waits != nil {
		return waits, "", err
	}

	var account uuid.UUID
	var at time.Time
	was, err := first(levyannotice.RefusedEQ(""))
	switch {
	case err != nil:
		return nil, "", err
	case was != nil:
		account, at = was.Account, was.At
	default:
		w, err := t.Db.Tenant.Get(ctx, workspace)
		if ent.IsNotFound(err) {
			return nil, "it is gone, and levyan was never told it arrived", nil
		}
		if err != nil {
			return nil, "", err
		}
		owners, err := tenancy.FirstOwners(ctx, t.Db, workspace)
		if err != nil {
			return nil, "", err
		}
		h := owners[workspace]
		if h == nil || h.Account == nil {
			return nil, "no account owns it, and so nobody would pay for it", nil
		}
		account, at = *h.Account, w.DateCreated
	}

	v, err := (&Notices{Now: t.Now}).create(t.Db, Arrived, pdid.Id(workspace), pdid.Id(account), at, ByNotFound).Save(ctx)
	return v, "", err
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
	case Arrived:
		return t.Levyan.Arrive(ctx, ws, pdid.Id(v.Account), v.At)
	case Left:
		return t.Levyan.Leave(ctx, ws, pdid.Id(v.Account))
	case Closed:
		return t.Levyan.CloseWorkspace(ctx, ws, v.At)
	case Restored:
		return t.Levyan.RestoreWorkspace(ctx, ws, v.At)
	case Destroyed:
		return t.Levyan.DestroyWorkspace(ctx, ws, v.At)
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
// process: for the workspaces made, and what became of them, before this
// deployment told levyan of any -- or while it told it nothing,
// `app.billing.levyan` unset -- and so for none since, each of which its
// change told. It answers how many notices it wrote.
//
//   - **Every workspace levyan was never told arrived**, as made when it was
//     (`Tenant.date_created`), by its first owner: nothing recorded who made
//     a workspace before this, and the one who has been its owner longest is
//     who would have to say otherwise (`tenancy.FirstOwners`, what a
//     workspace with no name is called after, D57). A workspace with no owner
//     now, or whose first owner names no account, is nobody levyan could
//     bill: it is logged and left, and looked at again the next time the
//     loop starts. Closed workspaces are told as the rest: they are measured
//     until they are destroyed (design 7.4).
//   - **What became of the workspaces** ([Teller.ends]): the closes and
//     restores their events still keep, oldest first, once; every workspace
//     closed now, from when it says it was; and every workspace destroyed
//     that levyan was told arrived, from when its address was held. Nothing
//     levyan was told already, or is to be, is written again.
func (t *Teller) Backfill(ctx context.Context) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()

	return t.backfill(ctx)
}

func (t *Teller) backfill(ctx context.Context) (int, error) {
	n, err := t.arrivals(ctx)
	if err != nil {
		return n, err
	}
	if n > 0 {
		log.From(ctx).InfoContext(ctx, "levyan: the workspaces made before it was told of any are to be told it, each with its first owner", "workspaces", n)
	}

	e, err := t.ends(ctx)
	if err != nil {
		return n + e.total(), err
	}
	if e.total() > 0 {
		log.From(ctx).InfoContext(ctx, "levyan: what became of the workspaces before it was told of it is to be told it",
			"closed", e.closed, "restored", e.restored, "destroyed", e.destroyed)
	}
	return n + e.total(), nil
}

// arrivals writes, for every workspace levyan was never told arrived, that it
// did ([Teller.Backfill]), and answers how many.
func (t *Teller) arrivals(ctx context.Context) (int, error) {
	n := 0
	after := uuid.UUID{}
	for {
		ts, err := t.Db.Tenant.Query().
			Where(enttenant.IdGT(after)).
			Order(ent.Asc(enttenant.FieldId)).
			Limit(tenantsPage).
			All(ctx)
		if err != nil {
			return n, err
		}
		if len(ts) == 0 {
			return n, nil
		}
		after = ts[len(ts)-1].Id

		ids := make([]uuid.UUID, 0, len(ts))
		for _, v := range ts {
			ids = append(ids, v.Id)
		}
		told, err := t.Db.LevyanNotice.Query().
			Where(levyannotice.KindEQ(Arrived), levyannotice.WorkspaceIn(ids...)).
			Select(levyannotice.FieldWorkspace).
			All(ctx)
		if err != nil {
			return n, err
		}
		missing := slices.DeleteFunc(ids, func(id uuid.UUID) bool {
			return slices.ContainsFunc(told, func(v *ent.LevyanNotice) bool { return v.Workspace == id })
		})
		if len(missing) == 0 {
			continue
		}
		owners, err := tenancy.FirstOwners(ctx, t.Db, missing...)
		if err != nil {
			return n, err
		}

		notices := (&Notices{Now: t.Now})
		creates := []*ent.LevyanNoticeCreate{}
		for _, v := range ts {
			if !slices.Contains(missing, v.Id) {
				continue
			}
			h := owners[v.Id]
			if h == nil || h.Account == nil {
				log.From(ctx).WarnContext(ctx, "levyan: a workspace no account owns is nobody's to pay for, and is not told; it is looked at again when the loop starts",
					"workspace", pdid.Id(v.Id).String())
				continue
			}
			creates = append(creates, notices.create(t.Db, Arrived, pdid.Id(v.Id), pdid.Id(*h.Account), v.DateCreated, ByBackfill))
		}
		if len(creates) == 0 {
			continue
		}
		if err := t.Db.LevyanNotice.CreateBulk(creates...).Exec(ctx); err != nil {
			return n, err
		}
		n += len(creates)
	}
}
