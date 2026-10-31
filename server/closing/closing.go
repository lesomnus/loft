// Package closing is what becomes of a closed workspace, and of a workspace's
// address (D29, design 8.4).
//
// A workspace is closed and restored by the domain layer (`TenantService.Close`
// and `Restore`, `domain.Deps.CloseWorkspace`), which destroys nothing. What is
// here is what comes after, and it is here because it destroys:
//
//   - [Closing.Sweep]: a workspace still closed once its restore window is
//     over, and held by nothing, is destroyed -- every row and file of it,
//     and its trail as its contract says: retired, keeping what the
//     deployment's floors are on until they pass, or purged -- and its
//     address is held. `loft tenant retire` and `purge` are the same
//     destruction by an operator's hand ([Closing.Destroy]);
//   - [Closing.Rename]: an operator moving a workspace to another address,
//     which holds the old one and sends whoever arrives at it on.
//
// A legal hold on a workspace stops the destroying, not the closing: the
// workspace stays closed, and is destroyed once the hold is lifted.
//
// And a workspace this deployment bills is measured before it is destroyed
// (`app.billing.levyan`, design 7.3): its final reading, handed to levyan
// while the rows it is measured from are there. As well as that can be done
// and no more: a reading that cannot be made or handed over is said in the log,
// and the workspace is destroyed all the same ([Destroyed.Unmeasured]).
package closing

import (
	"context"
	"errors"
	"fmt"
	"time"
	"uuid"

	"github.com/lesomnus/otx/log"
	"github.com/lesomnus/payday/audit"
	"github.com/lesomnus/payday/meter"
	"github.com/lesomnus/payday/pdid"
	"github.com/lesomnus/payday/trail"
	"github.com/lesomnus/z"
	"github.com/protobuf-orm/ent/dialect"

	app "github.com/lesomnus/loft"
	"github.com/lesomnus/loft/internal/ent"
	"github.com/lesomnus/loft/internal/ent/aliashold"
	"github.com/lesomnus/loft/internal/ent/holder"
	enttenant "github.com/lesomnus/loft/internal/ent/tenant"
	"github.com/lesomnus/loft/internal/identity"
	"github.com/lesomnus/loft/server/domain"
	"github.com/lesomnus/loft/server/offboard"
	"github.com/lesomnus/loft/server/pd"
	"github.com/lesomnus/loft/server/tenancy"
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

	// Meters is what a workspace holds, read and handed to levyan before it
	// is destroyed, as its final reading: the reader the deployment measures
	// every workspace with (payday#186). Nil, for a deployment that bills
	// nobody, destroys with nothing measured.
	Meters *meter.Reader
}

func (c Closing) now() time.Time {
	if c.Deps != nil {
		return c.Deps.Clock()
	}
	return time.Now()
}

// Sweep destroys the workspaces still closed once their window is over, and
// held by nothing, as their contracts say. One that is held waits, closed, and
// is looked at again; so does one whose contract retires it in a deployment
// that keeps floors and no archive to keep them in ([offboard.ErrNoArchive]),
// which an operator settles -- naming an archive, or purging it. One whose
// final reading could not be handed to levyan is destroyed all the same
// ([Closing.Destroy]): a billing service is never what keeps a workspace past
// its window.
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
		if _, err := c.Destroy(ctx, t, AsContracted, false); err != nil {
			if errors.Is(err, trail.ErrHeld) {
				log.From(ctx).InfoContext(ctx, "closing: a closed workspace is held, and waits", "workspace", t.Alias)
				continue
			}
			if errors.Is(err, offboard.ErrNoArchive) {
				log.From(ctx).WarnContext(ctx, "closing: a closed workspace waits: its contract retires it, keeping what the deployment's floors are on, "+
					"and audit.archive names no archive to keep it in -- name one, or purge it (`loft tenant purge`)", "workspace", t.Alias)
				continue
			}
			errs = append(errs, fmt.Errorf("@%s: %w", t.Alias, err))
		}
	}
	return errors.Join(errs...)
}

// How is which way a workspace's trail leaves with it.
type How int

const (
	// AsContracted is what its contract says now: retired, unless it says to
	// purge (`offboard.Deployment.Leave`).
	AsContracted How = iota

	// Retire keeps what the deployment's floors are still on until they have
	// passed (`offboard.Deployment.Retire`).
	Retire

	// Purge takes all of it, what the floors are on too
	// (`offboard.Deployment.Purge`).
	Purge
)

// Destroyed is what destroying a workspace did, or would.
type Destroyed struct {
	offboard.Purged

	// Forgotten is the accounts of the roster in this process that were here
	// and in no other workspace, which went with it.
	Forgotten int

	// Unmeasured is why its final reading was not measured and handed to
	// levyan before it went, and nil when it was -- or when the deployment
	// bills nobody, or for a dry run, which measures nothing.
	Unmeasured error
}

// Destroy is a workspace taken out of the deployment, its trail as `how`
// says, and its address held -- which is also this deployment's word that the
// workspace was destroyed here, read by a restore of it (`offboard.Import`).
// `dry` says what it would do and does none of it.
//
// A hold on it refuses, [trail.ErrHeld], and so does a retirement this
// deployment cannot keep, [offboard.ErrNoArchive]: both before anything is
// done. And what it holds is measured and handed to levyan, when this
// deployment bills ([Closing.Meters]), after those and before anything is
// taken, as its final reading -- and when that cannot be done it is destroyed
// all the same, the reason said in the log and in [Destroyed.Unmeasured]. A
// dry run measures nothing.
func (c Closing) Destroy(ctx context.Context, t *ent.Tenant, how How, dry bool) (Destroyed, error) {
	id := pdid.Id(t.Id)
	plan, err := c.leave(ctx, id, how, true)
	if err != nil {
		return Destroyed{Purged: plan}, err
	}

	// The accounts of the roster in this process that were here and nowhere
	// else go with it, and before it: a destroy that stops after them is run
	// again and finishes. An account at a roster of its own is never Loft's
	// to take.
	var accounts []pdid.Id
	if c.People != nil && c.People.Embedded() {
		if accounts, err = OnlyHere(ctx, c.Db, t.Id); err != nil {
			return Destroyed{Purged: plan}, err
		}
	}
	if dry {
		return Destroyed{Purged: plan, Forgotten: len(accounts)}, nil
	}

	// What it held, measured now and handed to levyan as its final reading,
	// before anything of it goes: its rows are what it is measured from
	// (payday#186). As well as that can be done, and no more (Claude, in place
	// of the rule that a workspace not measured is not destroyed): a
	// destruction is the law's, with no time to wait for a billing service
	// that is down, and what a final reading adds to the last daily one is
	// little -- nothing for a closed workspace, which takes no writes, and at
	// most a day for one an operator destroys, which levyan carries the last
	// reading over (`usage.Hold`). A destruction that stopped part of the way
	// is measured again when it is run again: what is left of the workspace,
	// which is never more than it held.
	out := Destroyed{Purged: plan}
	if c.Meters != nil {
		if err := c.Meters.Read(ctx, id); err != nil {
			out.Unmeasured = err
			log.From(ctx).WarnContext(ctx, "closing: a workspace is destroyed without its final reading: what it held could not be "+
				"measured and handed to levyan, which bills it by its last daily reading (levyan's usage.Hold)", "workspace", t.Alias, "err", err)
		}
	}

	// The address first: held before the workspace is gone, so that no
	// moment comes when it is free -- and saying how it went, which a restore
	// of it here reads to have the trail take a retired one back first
	// (`offboard.Deployment.Import`).
	reason := offboard.ReasonDestroyed
	if plan.Retired && c.Offboard.Floored() {
		reason = offboard.ReasonRetired
	}
	if err := c.hold(ctx, t.Alias, reason, "", id); err != nil {
		return out, err
	}
	for _, a := range accounts {
		if err := c.People.ForgetPerson(ctx, a); err != nil {
			return out, fmt.Errorf("roster: %d of %d accounts forgotten: %w", out.Forgotten, len(accounts), err)
		}
		out.Forgotten++
	}

	if out.Purged, err = c.leave(ctx, id, how, false); err != nil {
		return out, err
	}
	log.From(ctx).InfoContext(ctx, "closing: a workspace was destroyed", "workspace", t.Alias, "rows", out.Total(), "files", out.Files,
		"retired", out.Retired, "kept", out.Trail.Kept)
	return out, nil
}

// leave is the workspace's rows, files and trail, as `how` says.
func (c Closing) leave(ctx context.Context, id pdid.Id, how How, dry bool) (offboard.Purged, error) {
	switch how {
	case Retire:
		return c.Offboard.Retire(ctx, id, dry)
	case Purge:
		return c.Offboard.Purge(ctx, id, dry)
	default:
		return c.Offboard.Leave(ctx, id, dry)
	}
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
		if err := c.release(ctx, h.Id); err != nil {
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
	// On the trail as the act it is (payday#108, `trail: {recorded: {}}` on
	// AliasHold), by the deployment, in one transaction with the hold.
	tx, err := c.Db.Tx(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	id := pdid.New(pd.AliasHoldDomain)
	if err := tx.AliasHold.Create().
		SetId(id.Uuid()).
		SetHeld(alias).
		SetReason(reason).
		SetMovedTo(to).
		SetTenant(tenant.Uuid()).
		SetDateUntil(now.Add(hold)).
		SetDateCreated(now).
		SetDateUpdated(now).
		Exec(ctx); err != nil {
		return err
	}
	var cs audit.Contents
	if c.Deps != nil {
		cs = c.Deps.Audit
	}
	if _, err := pd.Record(ctx, tx.Client(), cs, Held, []pdid.Id{id}); err != nil {
		return err
	}
	return tx.Commit()
}

// release erases a hold before its time, and puts it on the trail as the act
// it is in the same transaction: written past the servers, an erase nobody
// recorded is a row the trail says is other than it is (payday#115).
func (c Closing) release(ctx context.Context, id uuid.UUID) error {
	tx, err := c.Db.Tx(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if err := tx.AliasHold.UpdateOneId(id).SetDateErased(c.now()).SetDateUpdated(c.now()).Exec(ctx); err != nil {
		return err
	}
	var cs audit.Contents
	if c.Deps != nil {
		cs = c.Deps.Audit
	}
	if _, err := pd.Record(ctx, tx.Client(), cs, Reused, []pdid.Id{pdid.Id(id)}); err != nil {
		return err
	}
	return tx.Commit()
}

// Held is the act a held address is on the trail as, and Reused the hold
// erased early, for an operator who took the address anyway.
const (
	Held   = "loft.alias.hold"
	Reused = "loft.alias.reuse"
)

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
