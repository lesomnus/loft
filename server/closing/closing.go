// Package closing is what becomes of a closed workspace (D29, design 8.4).
//
// A workspace is closed and restored by the domain layer, as the
// deployment's own work (`domain.Deps.CloseWorkspace` and `RestoreWorkspace`:
// an operator's `TenantService.Close` and `Restore` at the entry point, D83,
// or nobody left in it), which destroys nothing. What is here is what comes
// after, and it is here because it destroys:
//
//   - [Closing.Sweep]: a workspace still closed once its restore window is
//     over, and held by nothing, is destroyed -- every row and file of it,
//     and its trail as its contract says: retired, keeping what the
//     deployment's floors are on until they pass, or purged. `loft tenant
//     retire` and `purge` are the same destruction by an operator's hand
//     ([Closing.Destroy]).
//
// Its address is nothing of the deployment's to keep: it is the tenant's at
// roster (D86), which roster keeps or gives to another.
//
// A legal hold on a workspace stops the destroying, not the closing: the
// workspace stays closed, and is destroyed once the hold is lifted.
//
// And a workspace this deployment bills is measured before it is destroyed
// (`app.billing.levyan`, design 7.3): its final reading, handed to levyan
// while the rows it is measured from are there. As well as that can be done
// and no more: a reading that cannot be made or handed over is said in the log,
// and the workspace is destroyed all the same ([Destroyed.Unmeasured]). Then
// levyan is to be told it was destroyed, of a moment after that reading,
// before anything of it is taken (design 7.8): written down here, and told by
// the deployment's queue of what levyan is to hear, which outlives the
// workspace (`levyan.Notices`).
package closing

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/lesomnus/otx/log"
	"github.com/lesomnus/payday/meter"
	"github.com/lesomnus/payday/pdid"
	"github.com/lesomnus/payday/trail"

	"github.com/lesomnus/loft/internal/ent"
	enttenant "github.com/lesomnus/loft/internal/ent/tenant"
	"github.com/lesomnus/loft/server/domain"
	"github.com/lesomnus/loft/server/levyan"
	"github.com/lesomnus/loft/server/offboard"
)

// Closing is a workspace's end.
type Closing struct {
	Db       *ent.Client
	Offboard offboard.Deployment

	// Deps is the deployment's clock.
	Deps *domain.Deps

	// Meters is what a workspace holds, read and handed to levyan before it
	// is destroyed, as its final reading: the reader the deployment measures
	// every workspace with (payday#186). Nil, for a deployment that bills
	// nobody, destroys with nothing measured.
	Meters *meter.Reader

	// Notices is what levyan is to be told a workspace was destroyed by,
	// written after its final reading and before anything of it is taken
	// (design 7.8). Nil, for a deployment that bills nobody, tells nothing.
	Notices *levyan.Notices
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
					"and audit.archive names no archive to keep it in -- name one, or purge it (`loft tenant purge`, on an operator's connection)", "workspace", t.Alias)
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

	// Unmeasured is why its final reading was not measured and handed to
	// levyan before it went, and nil when it was -- or when the deployment
	// bills nobody, or for a dry run, which measures nothing.
	Unmeasured error
}

// Destroy is a workspace taken out of the deployment, its trail as `how`
// says. `dry` says what it would do and does none of it. A retirement leaves
// the trail its record that the workspace left, which a restore of it here
// reads to have the trail take it back first (`offboard.Deployment.Import`).
//
// A hold on it refuses, [trail.ErrHeld], and so does a retirement this
// deployment cannot keep, [offboard.ErrNoArchive]: both before anything is
// done. And what it holds is measured and handed to levyan, when this
// deployment bills ([Closing.Meters]), after those and before anything is
// taken, as its final reading -- and when that cannot be done it is destroyed
// all the same, the reason said in the log and in [Destroyed.Unmeasured].
// levyan is then to be told it was destroyed, at a moment after that reading,
// before anything is taken ([Closing.Notices]). A dry run measures nothing,
// and tells nothing.
func (c Closing) Destroy(ctx context.Context, t *ent.Tenant, how How, dry bool) (Destroyed, error) {
	id := pdid.Id(t.Id)
	plan, err := c.leave(ctx, id, how, true)
	if err != nil {
		return Destroyed{Purged: plan}, err
	}

	// Its people are the tenant's at roster, and stay there (D85): who they
	// are is roster's, and the workspace put back from its export finds
	// them as they were.
	if dry {
		return Destroyed{Purged: plan}, nil
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

	// That it was destroyed, for levyan, which bills it for nothing after:
	// at a moment after its final reading, which levyan would refuse a
	// reading of, and written before anything of it is taken -- on the
	// deployment's queue, which outlives the workspace's rows (design 7.8).
	// Once: a destruction run again told it when it first ran. levyan is
	// told whatever became of the reading: a destruction is the law's, and a
	// workspace destroyed is billed for nothing after it either way.
	at := c.now().UTC().Truncate(time.Microsecond)
	if err := c.Notices.Destroy(ctx, c.Db, id, at); err != nil {
		return out, fmt.Errorf("levyan, to be told it was destroyed: %w", err)
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
