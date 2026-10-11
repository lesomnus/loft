// Package closing is what becomes of a closed workspace, and of a workspace's
// address (D29, design 8.4).
//
// A workspace is closed and restored by the domain layer (`TenantService.Close`
// and `Restore`, `domain.Deps.CloseWorkspace`), which destroys nothing. What is
// here is what comes after, and it is here because it destroys:
//
//   - [Closing.Sweep]: a workspace still closed once its restore window is
//     over, and held by nothing, is destroyed -- every row, file and trail
//     row of it, as `rove tenant purge` does -- and its address is held;
//   - [Closing.Rename]: an operator moving a workspace to another address,
//     which holds the old one and sends whoever arrives at it on.
//
// A legal hold on a workspace stops the destroying, not the closing: the
// workspace stays closed, and is destroyed once the hold is lifted.
package closing

import (
	"context"
	"errors"
	"fmt"
	"time"
	"uuid"

	"github.com/lesomnus/otx/log"
	"github.com/lesomnus/payday/pdid"
	"github.com/lesomnus/payday/trail"
	"github.com/lesomnus/z"
	"github.com/protobuf-orm/ent/dialect"

	app "github.com/lesomnus/rove"
	"github.com/lesomnus/rove/internal/ent"
	"github.com/lesomnus/rove/internal/ent/aliashold"
	"github.com/lesomnus/rove/internal/ent/holder"
	enttenant "github.com/lesomnus/rove/internal/ent/tenant"
	"github.com/lesomnus/rove/internal/identity"
	"github.com/lesomnus/rove/server/domain"
	"github.com/lesomnus/rove/server/offboard"
	"github.com/lesomnus/rove/server/pd"
	"github.com/lesomnus/rove/server/tenancy"
)

// Closing is a workspace's end, and its address's.
type Closing struct {
	Db       *ent.Client
	Offboard offboard.Deployment
	// People is roster: on the one in this process, the accounts a destroyed
	// workspace held and nobody else does go with it.
	People *identity.Store

	// Base, Drv and Deps are what an operator's change is written through,
	// as the deployment's own work in the workspace.
	Base app.Server
	Drv  dialect.Driver
	Deps *domain.Deps

	Aliases tenancy.Aliases
	// Hold is how long an address is kept from anybody else.
	Hold time.Duration
}

func (c Closing) now() time.Time {
	if c.Deps != nil {
		return c.Deps.Clock()
	}
	return time.Now()
}

// Sweep destroys the workspaces still closed once their window is over, and
// held by nothing. One that is held waits, closed, and is looked at again.
func (c Closing) Sweep(ctx context.Context) error {
	ts, err := c.Db.Tenant.Query().Where(
		enttenant.DateClosedNotNil(),
		enttenant.DateDestroyNotNil(),
		enttenant.DateDestroyLT(c.now()),
	).All(ctx)
	if err != nil {
		return err
	}
	var errs []error
	for _, t := range ts {
		if err := c.Destroy(ctx, t); err != nil {
			if errors.Is(err, trail.ErrHeld) {
				log.From(ctx).InfoContext(ctx, "closing: a closed workspace is held, and waits", "workspace", t.Alias)
				continue
			}
			errs = append(errs, fmt.Errorf("@%s: %w", t.Alias, err))
		}
	}
	return errors.Join(errs...)
}

// Destroy is a closed workspace taken out of the deployment, and its address
// held. A hold on it refuses, with [trail.ErrHeld], before anything is done.
func (c Closing) Destroy(ctx context.Context, t *ent.Tenant) error {
	id := pdid.Id(t.Id)
	if _, err := c.Offboard.Purge(ctx, id, true); err != nil {
		return err
	}

	// The address first: held before the workspace is gone, so that no
	// moment comes when it is free.
	if err := c.hold(ctx, t.Alias, "destroyed", "", id); err != nil {
		return err
	}

	// The accounts of the roster in this process that were here and nowhere
	// else go with it, and before it: a destroy that stops after them is run
	// again and finishes. An account at a roster of its own is never Rove's
	// to take.
	if c.People != nil && c.People.Embedded() {
		accounts, err := OnlyHere(ctx, c.Db, t.Id)
		if err != nil {
			return err
		}
		for _, a := range accounts {
			if err := c.People.ForgetPerson(ctx, a); err != nil {
				return fmt.Errorf("roster: %w", err)
			}
		}
	}

	x, err := c.Offboard.Purge(ctx, id, false)
	if err != nil {
		return err
	}
	log.From(ctx).InfoContext(ctx, "closing: a workspace was destroyed", "workspace", t.Alias, "rows", x.Total(), "files", x.Files)
	return nil
}

// Rename moves a workspace to another address, as an operator: the address is
// one a workspace may have and nobody has, and the old one is held -- sending
// whoever arrives at it on to the new -- for as long as a destroyed
// workspace's is. `reuse` takes an address that is held anyway, and says so.
func (c Closing) Rename(ctx context.Context, from, to string, reuse bool) error {
	t, err := c.Db.Tenant.Query().Where(enttenant.Alias(from)).Only(ctx)
	if err != nil {
		if ent.IsNotFound(err) {
			return fmt.Errorf("no workspace is @%s", from)
		}
		return err
	}
	if err := c.Aliases.Valid(to); err != nil {
		return err
	}
	taken, err := c.Db.Tenant.Query().Where(enttenant.Alias(to)).Exist(ctx)
	if err != nil {
		return err
	}
	if taken {
		return fmt.Errorf("@%s is another workspace's", to)
	}
	h, err := c.Aliases.Held(ctx, to)
	if err != nil {
		return err
	}
	if h != nil {
		if !reuse {
			return fmt.Errorf("@%s is held until %s (%s); --reuse takes it anyway", to, h.DateUntil.Format(time.DateOnly), h.Reason)
		}
		if err := c.Db.AliasHold.UpdateOneId(h.Id).SetDateErased(c.now()).SetDateUpdated(c.now()).Exec(ctx); err != nil {
			return err
		}
		log.From(ctx).WarnContext(ctx, "closing: a held address was taken early", "alias", to, "held", h.Reason)
	}

	id := pdid.Id(t.Id)
	if _, err := c.Base.Tenant().Patch(ctx, app.TenantPatchRequest_builder{
		Ref:              app.TenantRef_builder{Id: id.Bytes()}.Build(),
		Alias:            z.Ptr(to),
		DateUpdatedForce: z.Ptr(true),
	}.Build()); err != nil {
		return err
	}
	return c.hold(ctx, from, "renamed", to, id)
}

func (c Closing) hold(ctx context.Context, alias, reason, to string, tenant pdid.Id) error {
	now := c.now()
	hold := c.Hold
	if hold <= 0 {
		hold = 365 * 24 * time.Hour
	}
	exists, err := c.Db.AliasHold.Query().Where(
		aliashold.Held(alias), aliashold.Tenant(tenant.Uuid()), aliashold.Reason(reason),
		aliashold.DateUntilGT(now), aliashold.DateErasedIsNil(),
	).Exist(ctx)
	if err != nil || exists {
		return err
	}
	return c.Db.AliasHold.Create().
		SetId(pdid.New(pd.AliasHoldDomain).Uuid()).
		SetHeld(alias).
		SetReason(reason).
		SetMovedTo(to).
		SetTenant(tenant.Uuid()).
		SetDateUntil(now.Add(hold)).
		SetDateCreated(now).
		SetDateUpdated(now).
		Exec(ctx)
}

// OnlyHere is the accounts of a workspace's members that are members of no
// other workspace.
func OnlyHere(ctx context.Context, db *ent.Client, workspace uuid.UUID) ([]pdid.Id, error) {
	hs, err := db.Holder.Query().Where(holder.TenantId(workspace), holder.AccountNotNil()).All(ctx)
	if err != nil {
		return nil, err
	}
	seen := map[uuid.UUID]bool{}
	out := []pdid.Id{}
	for _, h := range hs {
		a := *h.Account
		if seen[a] {
			continue
		}
		seen[a] = true
		elsewhere, err := db.Holder.Query().Where(
			holder.Account(a),
			holder.TenantIdNEQ(workspace),
			holder.DateErasedIsNil(),
		).Exist(ctx)
		if err != nil {
			return nil, err
		}
		if !elsewhere {
			out = append(out, pdid.Id(a))
		}
	}

	return out, nil
}
