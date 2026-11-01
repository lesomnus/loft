// Package lifecycle carries what roster says of an account into this
// deployment (design 9.10, M2 step 5).
//
// roster is where somebody stops being good -- signed out everywhere,
// suspended, erased -- and this deployment holds copies of the decision that
// they were: sessions, and memberships. Each call a session makes is held to
// roster already ([identity.Store.Held]); this is what makes it prompt, from
// roster's own stream (roster's `standing`, its follower in `cmd/serve.go`),
// and what an erasure goes on to mean here:
//
//   - signed out everywhere: the sessions that began before it end;
//   - suspended or erased: every session of the account ends;
//   - erased: each of its memberships begins to leave (`Holder.date_leaving`),
//     from when roster erased it, and once roster's grace is over the account
//     leaves every workspace it is in ([Lifecycle.Sweep], [Lifecycle.Depart]):
//     its person records pseudonymized, its memberships ended, a workspace it
//     was the last owner of handed on.
//
// # Leaving, and not gone
//
// Because roster's erasure is not: it destroys nothing for a grace (roster's
// `holder.forget_after`), and `roster restore` brings the account back -- for
// a mistaken deletion, a compromised account deleting things, a billing
// dispute. A membership pseudonymized the moment roster erased its account
// would come back as nobody.
//
// So this waits as long as roster does (`app.accounts.forget_after`, kept
// equal to roster's `holder.forget_after`), and somebody who asked to be
// forgotten now is forgotten now by the operator (`loft identity depart`).
//
// roster's restore and forget are on the stream since roster#215: a restore
// comes as an event about the account that is not an erasure, which clears
// the mark, and a forget as an erasure. What the stream does not carry is what
// was said while this process was not listening, so the sweep asks roster
// again before an account leaves -- which is also how a restore made in the
// meantime is heard. A mark is acted on only after asking roster again, which
// is roster's rule for every app that marks (`standing.Accounts.Settle`,
// roster's `docs/relying-app.md`).
package lifecycle

import (
	"context"
	"fmt"
	"time"
	"uuid"

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
	"github.com/lesomnus/loft/internal/ent/invitation"
	"github.com/lesomnus/loft/internal/identity"
	"github.com/lesomnus/loft/server/domain"
	"github.com/lesomnus/loft/server/session"
)

// Lifecycle is what an account's standing at roster does here.
type Lifecycle struct {
	// Db is this deployment's database, Sessions its sessions on it.
	Db       *ent.Client
	Sessions session.Store

	// Base is the stack with neither the wall nor the domain layer: what a
	// membership's leaving is written through, as the deployment's own work
	// in its workspace, so that the trail says it and a watch hears it.
	Base app.Server

	// People is roster, asked again before an account leaves; Deps and Drv
	// are what a departure is run with ([domain.Deps.Depart]).
	People *identity.Store
	Deps   *domain.Deps
	Drv    dialect.Driver

	// Grace is how long after roster erased an account it leaves here, if
	// roster has not restored it (`app.accounts.forget_after`).
	Grace time.Duration
}

// Apply is one event: what roster says of an account now. It carries state and
// not a movement, so applying it twice is applying it once, and an event older
// than what was done before cannot undo it -- a session ended stays ended.
func (l Lifecycle) Apply(ctx context.Context, v identity.Standing) error {
	switch {
	case !v.Erased.IsZero() || !v.Disabled.IsZero() || !v.Closed.IsZero():
		// A directory closed is every one of its accounts shut out, and no
		// more: closing destroys nothing, and a directory restored lets them
		// sign in again. Leaving is what an erasure starts, below.
		if _, err := l.Sessions.End(ctx, v.Holder, time.Time{}); err != nil {
			return err
		}
	case !v.Invalidated.IsZero():
		if _, err := l.Sessions.End(ctx, v.Holder, v.Invalidated); err != nil {
			return err
		}
	}

	hs, err := l.Db.Holder.Query().Where(holder.Account(v.Holder.Uuid()), holder.DateErasedIsNil()).All(ctx)
	if err != nil {
		return err
	}
	for _, h := range hs {
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
			continue
		}
		tenant := pdid.Id(h.TenantId)
		as := frame.Into(ctx, frame.New(pdid.Nil, tenant, frame.Whole()).WithScope(frame.Only(tenant)))
		if _, err := l.Base.Holder().Patch(as, req); err != nil {
			return fmt.Errorf("holder %s: %w", pdid.Id(h.Id), err)
		}
	}
	return nil
}

// Sweep is one look at the accounts whose memberships are still leaving once
// the grace is over. roster is asked about each again, since what it said
// while this process was not listening is not on the stream
// (`standing.Accounts.Settle`): one it still
// has nobody by leaves every workspace it is in ([Lifecycle.Depart]); one it
// has again was restored, and stops leaving; and about one roster could not
// answer, nothing is decided -- it is asked next time.
func (l Lifecycle) Sweep(ctx context.Context) error {
	cut := l.now().Add(-l.Grace)
	hs, err := l.Db.Holder.Query().Where(
		holder.DateLeavingLT(cut),
		holder.DateErasedIsNil(),
		holder.AccountNotNil(),
	).All(ctx)
	if err != nil {
		return err
	}

	marked := make([]pdid.Id, 0, len(hs))
	for _, h := range hs {
		marked = append(marked, pdid.Id(*h.Account))
	}
	return l.People.Accounts().Settle(ctx, marked,
		func(ctx context.Context, account pdid.Id) error {
			_, err := l.Depart(ctx, account)
			return err
		},
		// Restored at roster, which is what the grace was for.
		l.Apply,
	)
}

// Departed is one workspace an account left.
type Departed struct {
	Workspace pdid.Id
	Member    pdid.Id
	domain.Departure

	// Closed is a workspace the account was the last of, closed with it:
	// its data stays through the restore window, and is destroyed after
	// (D29).
	Closed bool
}

// Depart is an account leaving every workspace it is in, now: what the grace
// ends in, and what an operator does for somebody who asked to be forgotten
// at once (`loft identity depart`). Its sessions end with it. A workspace
// already done is not done again, so a departure that stopped part way is
// finished by asking again.
func (l Lifecycle) Depart(ctx context.Context, account pdid.Id) ([]Departed, error) {
	if _, err := l.Sessions.End(ctx, account, time.Time{}); err != nil {
		return nil, err
	}
	hs, err := l.Db.Holder.Query().Where(holder.Account(account.Uuid()), holder.DateErasedIsNil()).All(ctx)
	if err != nil {
		return nil, err
	}

	out := make([]Departed, 0, len(hs))
	done := map[uuid.UUID]bool{}
	for _, h := range hs {
		done[h.TenantId] = true
		workspace := pdid.Id(h.TenantId)
		v, err := l.Deps.Depart(ctx, l.Base, l.Drv, workspace, pdid.Id(h.Id))
		if err != nil {
			return out, fmt.Errorf("workspace %s: %w", workspace, err)
		}
		if v.Ownerless {
			log.From(ctx).WarnContext(ctx, "lifecycle: a workspace's last owner left, and nobody was there to hand it to",
				"workspace", workspace.String())
		}
		d := Departed{Workspace: workspace, Member: pdid.Id(h.Id), Departure: v}
		if d.Closed, err = l.closeIfNobody(ctx, workspace); err != nil {
			return out, fmt.Errorf("workspace %s: %w", workspace, err)
		}
		out = append(out, d)
	}

	// And the invitations it claimed or answered where it never became a
	// member, which carry its address as much as the rest.
	sent, err := l.Db.Invitation.Query().Where(invitation.Account(account.Uuid()), invitation.EmailNEQ("")).All(ctx)
	if err != nil {
		return out, err
	}
	for _, v := range sent {
		if done[v.TenantId] {
			continue
		}
		done[v.TenantId] = true
		if err := l.Deps.Unaddress(ctx, l.Base, l.Drv, pdid.Id(v.TenantId), account); err != nil {
			return out, fmt.Errorf("workspace %s: %w", pdid.Id(v.TenantId), err)
		}
	}

	if l.People != nil {
		l.People.Recheck(account)
	}
	return out, nil
}

// closeIfNobody closes a workspace nobody is left in (design 8.4: one only
// that account was in goes the way a closed workspace goes), and says why when
// only auditors are -- who may change nothing, so nobody can look after it,
// and an operator has to.
func (l Lifecycle) closeIfNobody(ctx context.Context, workspace pdid.Id) (bool, error) {
	left, err := l.Db.Holder.Query().Where(holder.TenantId(workspace.Uuid()), holder.DateErasedIsNil()).All(ctx)
	if err != nil {
		return false, err
	}
	if len(left) == 0 {
		_, err := l.Deps.CloseWorkspace(ctx, l.Base, l.Drv, workspace, domain.ClosedByDeployment, "계정 탈퇴로 남은 구성원이 없습니다")
		if status.Code(err) == codes.FailedPrecondition {
			// Closed already.
			return false, nil
		}
		return err == nil, err
	}
	for _, h := range left {
		if h.Role != "auditor" {
			return false, nil
		}
	}
	log.From(ctx).WarnContext(ctx, "lifecycle: only auditors are left in a workspace, and nobody can look after it",
		"workspace", workspace.String())
	return false, nil
}

func (l Lifecycle) now() time.Time {
	if l.Deps != nil && l.Deps.Now != nil {
		return l.Deps.Now()
	}
	return time.Now()
}
