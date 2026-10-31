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

// Who pays for a workspace, as levyan is told it (levyan#14, design 7.6).
//
// levyan bills a workspace to its payer, a roster account, one at a time; the
// workspace's maker by default, and whoever accepts after that (levyan's
// design 14). What it cannot know by itself is Loft's: that a workspace was
// made, and by whom, and that an account is no longer in one. So Loft tells
// it, by levyan's `WorkspaceService`: `Arrive` and `Leave`.
//
// # Beside the change, and told after it
//
// A notice ([Notices]) is a row (`LevyanNotice`) written in the transaction
// of the change it tells -- a workspace made, a membership ended -- so a change
// that commits is one levyan will be told of, and one rolled back is not; and
// no change waits for levyan, nor fails for it. A loop of its own ([Teller],
// `loft.levyan.notices`) tells them, and marks each told once levyan has
// answered it.
//
// A workspace's notices are told in the order they happened, each only once
// the one before it is: a membership that ended before levyan heard the
// workspace arrive would otherwise be told of a workspace levyan does not
// know, which it takes as nothing, and then the account that left would pay.
// Another workspace's notices do not wait for this one's.
//
// # What fails
//
// A levyan that cannot be reached holds nothing up: the notices wait, the
// loop asks again a little later each time, up to a minute, and tells them
// once it answers. levyan takes each twice as once -- said again, `Arrive` and
// `Leave` change nothing -- so a notice told by two processes around a
// failover, or told and not marked, is told once. A notice levyan refused is
// one it will refuse again (an identifier that is not one): it is marked
// told with what levyan said, logged, and not told again, as a reading
// levyan refused is not handed over again ([Client.Send]).

// The kinds of notice, as `LevyanNotice.kind` holds them.
const (
	// Arrived is a workspace made: `Arrive`, its maker the account, from when
	// it was made.
	Arrived = "arrive"

	// Left is an account no longer a member of the workspace: `Leave`.
	Left = "leave"
)

// How a notice came to be written, as `LevyanNotice.origin` holds it.
const (
	// ByChange is a notice the change it tells wrote, in its transaction.
	ByChange = "change"

	// ByBackfill is a workspace made before this deployment told levyan of
	// any, told by the loop's first pass ([Teller.Backfill]) -- its maker its
	// first owner, since nothing recorded who made it.
	ByBackfill = "backfill"
)

// Notices writes what levyan is to be told of who pays for a workspace, on the
// client of the transaction of the change that tells it. A nil one writes
// nothing: a deployment that bills nobody tells levyan nothing.
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

// noticesPage is how many waiting notices a pass reads at a time, and
// tenantsPage how many workspaces the first pass looks at at a time.
const (
	noticesPage = 100
	tenantsPage = 100
)

// Teller tells levyan what the notices say ([Notices]): the loop
// `loft.levyan.notices`, every [NoticesEvery] -- longer after a pass levyan
// did not answer -- under the lease.
//
// Its first pass in a process also tells levyan of every workspace it was
// never told of ([Teller.Backfill]).
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
	// both told, and not told again.
	Told, Refused int

	// Waiting is the notices it could not be told, and are told later: the
	// one levyan did not answer, and every later one of its workspace.
	Waiting int
}

// Pass is one pass of the loop: the workspaces levyan was never told of, until
// that has been done once in this process, and then whatever waits -- unless
// levyan did not answer the pass before, and it is not time to ask again. It
// logs what failed and answers nil: a levyan that cannot be reached is a
// thing to ask again, and never a reason to stop serving.
func (t *Teller) Pass(ctx context.Context) error {
	t.mu.Lock()
	defer t.mu.Unlock()

	if !t.backfilled {
		n, err := t.backfill(ctx)
		switch {
		case err != nil:
			log.From(ctx).WarnContext(ctx, "levyan: the workspaces it was never told of could not be found, and are looked for again next pass", "err", err)
		default:
			t.backfilled = true
			if n > 0 {
				log.From(ctx).InfoContext(ctx, "levyan: the workspaces made before it was told of any are to be told it, each with its first owner", "workspaces", n)
			}
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
	log.From(ctx).WarnContext(ctx, "levyan: who pays for a workspace waits to be told, since levyan could not be told it",
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
				log.From(ctx).WarnContext(ctx, "levyan: a notice of who pays was refused, and is not told again",
					"workspace", pdid.Id(v.Workspace).String(), "kind", v.Kind, "account", pdid.Id(v.Account).String(), "err", err)
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
				log.From(ctx).WarnContext(ctx, "levyan: a notice of who pays was not taken, and is told again later with what follows it",
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
	switch v.Kind {
	case Arrived:
		return t.Levyan.Arrive(ctx, pdid.Id(v.Workspace), pdid.Id(v.Account), v.At)
	case Left:
		return t.Levyan.Leave(ctx, pdid.Id(v.Workspace), pdid.Id(v.Account))
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

// Backfill writes, for every workspace levyan was never told arrived, that it
// did: once, as the loop starts, for the workspaces made before this
// deployment told levyan of any -- or while it told it nothing, `app.billing.
// levyan` unset -- and so for none made since, each of which its making told.
// It answers how many.
//
// Each is told as made when it was (`Tenant.date_created`), by its first
// owner: nothing recorded who made a workspace before this, and the one who
// has been its owner longest is who would have to say otherwise
// (`tenancy.FirstOwners`, what a workspace with no name is called after,
// D57). A workspace with no owner now, or whose first owner names no account,
// is nobody levyan could bill: it is logged and left, and looked at again the
// next time the loop starts. Closed workspaces are told as the rest: they are
// measured until they are destroyed (design 7.4).
func (t *Teller) Backfill(ctx context.Context) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()

	return t.backfill(ctx)
}

func (t *Teller) backfill(ctx context.Context) (int, error) {
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
