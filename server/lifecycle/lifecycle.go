// Package lifecycle carries what roster says of a member into this deployment
// (design 9.10, M2 step 5).
//
// A member is the holder of the tenant this deployment serves, by the same
// identifier (D85). roster is where somebody stops being good -- signed out
// everywhere, suspended, erased -- and this deployment holds copies of the
// decision that they were: sessions, and their membership. Each call a
// session makes is held to roster already ([identity.Store.Held]); this is
// what makes it prompt, from roster's own stream (roster's `standing`, its
// follower in `cmd/serve.go`), and what an erasure goes on to mean here:
//
//   - signed out everywhere: the sessions that began before it end;
//   - suspended or erased, or the tenant closed: every session of the member
//     ends;
//   - erased: their membership begins to leave (`Holder.date_leaving`), from
//     when roster erased them, and once roster's grace is over they leave
//     the workspace ([Lifecycle.Sweep], [Lifecycle.Depart]): their person
//     record pseudonymized, their membership ended, the workspace handed on
//     when they were its last owner.
//
// # Leaving, and not gone
//
// Because roster's erasure is not: it destroys nothing for a grace (roster's
// `holder.forget_after`), and `roster restore` brings the holder back -- for
// a mistaken deletion, a compromised account deleting things, a billing
// dispute. A membership pseudonymized the moment roster erased its holder
// would come back as nobody.
//
// So this waits as long as roster does (`app.members.forget_after`, kept
// equal to roster's `holder.forget_after`), and somebody who asked to be
// forgotten now is forgotten now by the operator (`loft identity depart`).
//
// roster's restore and forget are on the stream since roster#215: a restore
// comes as an event about the holder that is not an erasure, which clears the
// mark, and a forget as an erasure. What the stream does not carry is what was
// said while this process was not listening, so the sweep asks roster again
// before a member leaves -- which is also how a restore made in the meantime
// is heard. A mark is acted on only after asking roster again, which is
// roster's rule for every app that marks (`standing.Accounts.Settle`,
// roster's `docs/relying-app.md`).
package lifecycle

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/lesomnus/otx/log"
	"github.com/lesomnus/payday/frame"
	"github.com/lesomnus/payday/pdid"
	"github.com/lesomnus/z"
	"github.com/protobuf-orm/ent/dialect"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"

	app "github.com/lesomnus/loft"
	"github.com/lesomnus/loft/internal/ent"
	"github.com/lesomnus/loft/internal/ent/holder"
	enttenant "github.com/lesomnus/loft/internal/ent/tenant"
	"github.com/lesomnus/loft/internal/identity"
	"github.com/lesomnus/loft/server/domain"
	"github.com/lesomnus/loft/server/policy"
	"github.com/lesomnus/loft/server/session"
)

// Lifecycle is what a member's standing at roster does here.
type Lifecycle struct {
	// Db is this deployment's database, Sessions its sessions on it.
	Db       *ent.Client
	Sessions session.Store

	// Base is the stack with neither the wall nor the domain layer: what a
	// membership's leaving is written through, as the deployment's own work
	// in its workspace, so that the trail says it and a watch hears it.
	Base app.Server

	// People is roster, asked again before a member leaves; Deps and Drv are
	// what a departure is run with ([domain.Deps.Depart]).
	People *identity.Store
	Deps   *domain.Deps
	Drv    dialect.Driver

	// Grace is how long after roster erased a member's holder they leave
	// here, if roster has not restored it (`app.members.forget_after`).
	Grace time.Duration
}

// Apply is one event: what roster says of a member now. It carries state and
// not a movement, so applying it twice is applying it once, and an event older
// than what was done before cannot undo it -- a session ended stays ended.
func (l Lifecycle) Apply(ctx context.Context, v identity.Standing) error {
	switch {
	case !v.Erased.IsZero() || !v.Disabled.IsZero() || !v.Closed.IsZero():
		// The tenant closed at roster is everybody in it shut out, and no
		// more: closing destroys nothing, and a tenant restored there lets
		// them sign in again. Leaving is what an erasure starts, below.
		if _, err := l.Sessions.End(ctx, v.Holder, time.Time{}); err != nil {
			return err
		}
	case !v.Invalidated.IsZero():
		if _, err := l.Sessions.End(ctx, v.Holder, v.Invalidated); err != nil {
			return err
		}
	}

	h, err := l.Db.Holder.Query().Where(holder.Id(v.Holder.Uuid()), holder.DateErasedIsNil()).Only(ctx)
	if ent.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return err
	}
	var req *app.HolderPatchRequest
	switch {
	case !v.Erased.IsZero() && h.DateLeaving == nil:
		req = app.HolderPatchRequest_builder{
			Ref:              app.HolderRef_builder{Id: pdid.Id(h.Id).Bytes()}.Build(),
			DateLeaving:      timestamppb.New(v.Erased),
			DateUpdatedForce: z.Ptr(true),
		}.Build()
	case v.Erased.IsZero() && h.DateLeaving != nil:
		// Restored at roster, which is what the grace was for.
		req = app.HolderPatchRequest_builder{
			Ref:              app.HolderRef_builder{Id: pdid.Id(h.Id).Bytes()}.Build(),
			DateLeavingNull:  z.Ptr(true),
			DateUpdatedForce: z.Ptr(true),
		}.Build()
	default:
		return nil
	}
	tenant := pdid.Id(h.TenantId)
	as := frame.Into(ctx, frame.New(pdid.Nil, tenant, frame.Whole()).WithScope(frame.Only(tenant)))
	if _, err := l.Base.Holder().Patch(as, req); err != nil {
		return fmt.Errorf("holder %s: %w", pdid.Id(h.Id), err)
	}
	return nil
}

// Sweep is one look at the members still leaving once the grace is over.
// roster is asked about each again, since what it said while this process was
// not listening is not on the stream (`standing.Accounts.Settle`): one it
// still has nobody by leaves the workspace ([Lifecycle.Depart]); one it has
// again was restored, and stops leaving; and about one roster could not
// answer, nothing is decided -- it is asked next time.
func (l Lifecycle) Sweep(ctx context.Context) error {
	cut := l.now().Add(-l.Grace)
	marked, err := l.Db.Holder.Query().Where(
		holder.DateLeavingLT(cut),
		holder.DateErasedIsNil(),
	).Ids(ctx)
	if err != nil {
		return err
	}

	members := make([]pdid.Id, 0, len(marked))
	for _, id := range marked {
		members = append(members, pdid.Id(id))
	}
	return l.People.Accounts().Settle(ctx, members,
		func(ctx context.Context, member pdid.Id) error {
			_, err := l.Depart(ctx, member)
			return err
		},
		// Restored at roster, which is what the grace was for.
		l.Apply,
	)
}

// Departed is a member leaving the workspace.
type Departed struct {
	Workspace pdid.Id
	Member    pdid.Id
	domain.Departure

	// Closed is the workspace closed with them, nobody being left in it: its
	// data stays through the restore window, and is destroyed after (D29).
	Closed bool
}

// Depart is a member leaving the workspace, now: what the grace ends in, and
// what an operator does for somebody who asked to be forgotten at once (`loft
// identity depart`). Their sessions end with them. A membership ended already
// is nothing to do, and answers nil.
func (l Lifecycle) Depart(ctx context.Context, member pdid.Id) (*Departed, error) {
	if _, err := l.Sessions.End(ctx, member, time.Time{}); err != nil {
		return nil, err
	}
	h, err := l.Db.Holder.Query().Where(holder.Id(member.Uuid()), holder.DateErasedIsNil()).Only(ctx)
	if ent.IsNotFound(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}

	workspace := pdid.Id(h.TenantId)
	v, err := l.Deps.Depart(ctx, l.Base, l.Drv, workspace, member)
	if err != nil {
		return nil, fmt.Errorf("workspace %s: %w", workspace, err)
	}
	out := &Departed{Workspace: workspace, Member: member, Departure: v}
	if out.Closed, err = l.closeIfNobody(ctx, workspace); err != nil {
		return out, fmt.Errorf("workspace %s: %w", workspace, err)
	}
	if l.People != nil {
		l.People.Recheck(member)
	}
	return out, nil
}

// Departing is what Depart would do were it asked now, and does nothing:
// whether the workspace would close with nobody left in it, by the rule Depart
// then follows. What an operator is told before it is done (`loft identity
// depart` without `--yes`). A membership ended already answers nil.
func (l Lifecycle) Departing(ctx context.Context, member pdid.Id) (*Departed, error) {
	h, err := l.Db.Holder.Query().Where(holder.Id(member.Uuid()), holder.DateErasedIsNil()).Only(ctx)
	if ent.IsNotFound(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}

	out := &Departed{Workspace: pdid.Id(h.TenantId), Member: member}
	others, err := l.Db.Holder.Query().
		Where(holder.TenantId(h.TenantId), holder.IdNEQ(h.Id), holder.DateErasedIsNil()).
		Exist(ctx)
	if err != nil {
		return nil, err
	}
	if !others {
		// closeIfNobody's, unless it is closed already.
		closed, err := l.Db.Tenant.Query().Where(enttenant.Id(h.TenantId), enttenant.DateClosedNotNil()).Exist(ctx)
		if err != nil {
			return nil, err
		}
		out.Closed = !closed
	}
	return out, nil
}

// closeIfNobody closes a workspace nobody is left in (design 8.4: one only
// that member was in goes the way a closed workspace goes), and says why when
// nobody left may change anything a member may -- auditors, say, or people
// roster binds no role of Loft's -- so that nobody can look after it, and its
// tenant's administrators at roster have to give somebody a role (D89).
func (l Lifecycle) closeIfNobody(ctx context.Context, workspace pdid.Id) (bool, error) {
	left, err := l.Db.Holder.Query().Where(holder.TenantId(workspace.Uuid()), holder.DateErasedIsNil()).All(ctx)
	if err != nil {
		return false, err
	}
	if len(left) == 0 {
		_, err := l.Deps.CloseWorkspace(ctx, l.Base, l.Drv, workspace, domain.ClosedByDeployment, "마지막 구성원이 떠나 남은 사람이 없습니다")
		if status.Code(err) == codes.FailedPrecondition {
			// Closed already.
			return false, nil
		}
		return err == nil, err
	}
	for _, h := range left {
		if l.looksAfter(ctx, pdid.Id(h.Id)) {
			return false, nil
		}
	}
	log.From(ctx).WarnContext(ctx, "lifecycle: nobody left in a workspace may change anything a member may, so nobody can look after it; "+
		"its tenant's administrators at roster have to bind somebody one of Loft's roles",
		"workspace", workspace.String())
	return false, nil
}

func (l Lifecycle) now() time.Time {
	if l.Deps != nil && l.Deps.Now != nil {
		return l.Deps.Now()
	}
	return time.Now()
}

// looksAfter says whether roster says `member` may change something a member
// may: one method the member's role ships with that the auditor's does not
// (`policy.Roles`). Somebody roster could not be asked about is taken to, so
// that a roster that is down says nothing of anybody here.
func (l Lifecycle) looksAfter(ctx context.Context, member pdid.Id) bool {
	if l.People == nil || l.People.Reach() == nil {
		return true
	}
	r, err := l.People.Reach().Reach(ctx, member)
	switch {
	case errors.Is(err, identity.ErrNoPerson):
		return false
	case err != nil:
		return true
	}
	writes, _ := policy.RoleOf("member")
	reads, _ := policy.RoleOf("auditor")
	for _, m := range writes.Methods {
		if !slices.Contains(reads.Methods, m) && r.Covers(m) {
			return true
		}
	}
	return false
}
