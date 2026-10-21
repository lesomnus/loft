// Package retention is what a tenant's contract says about its history, now
// (design 8).
//
// Two windows, and they are not the same thing (design 8.1). The **view**
// window is how far back the tenant may look: what the history calls answer,
// and it shrinks the moment a contract says so. The **keep** window is how long
// the history exists at all, which is what a sweep destroys by -- and it shrinks
// only after the grace a contract gives, so a plan that comes back in time has
// lost nothing.
//
// One answer, read by everything that keeps history: rove's own time rows and
// events, and payday's trail of the writes that made them (payday#35). Two
// answers would be a trail that outlives the history it records, or history the
// trail has already forgotten the making of.
//
// What answers is [Of], from [app.TenantContract] and [app.LegalHold]: the
// operator's rows, which a tenant reads and only `rove contract` and `rove hold`
// write. Nothing here destroys anything. Whether anything may is the operator's
// switch, `app.retention.apply`, read where the sweeps are wired.
package retention

import (
	"context"
	"strings"
	"time"

	"github.com/lesomnus/payday/pdid"
	"github.com/lesomnus/payday/trail"

	"github.com/lesomnus/rove/internal/ent"
	"github.com/lesomnus/rove/internal/ent/legalhold"
	"github.com/lesomnus/rove/internal/ent/tenantcontract"
	"github.com/lesomnus/rove/server/pd"
)

const day = 24 * time.Hour

// Days is a contract's count of days as a duration, zero staying zero.
func Days(n uint32) time.Duration { return time.Duration(n) * day }

// Defaults is what a tenant with no contract gets: the deployment's own plan,
// from `app.retention`. The zero value is the safe one -- all of the history
// shown, all of it kept.
type Defaults struct {
	View time.Duration
	Keep time.Duration

	// Access is how long the deployment keeps the access record, from
	// `audit.by.access`, which a contract's `access_keep_days` is in place of
	// (#144). Zero is forever.
	Access time.Duration

	// Apply is `app.retention.apply`: whether a contract may have destroyed
	// what the deployment's own windows would keep. Off, a contract still
	// keeps the access record longer than the deployment would.
	Apply bool
}

// Window is a tenant's history windows at one instant.
type Window struct {
	// View is how far back the tenant may look. Zero is all of it.
	View time.Duration

	// Keep is how long its history is kept, with every grace still running
	// already in it. Zero is forever.
	Keep time.Duration

	// Access is how long its access record is kept, the same way: the
	// contract's, or the deployment's where it says nothing (#144).
	Access time.Duration

	// Restore is how long it may be restored once closed (D29): the
	// contract's, or zero for the deployment's.
	Restore time.Duration

	// Holds is what the legal holds on it are for. Empty is none; while there
	// is one, nothing of the tenant's history is destroyed, whatever Keep says.
	Holds []string

	// Contract is the contract in force, and nil when there is none and the
	// deployment's defaults are what applies.
	Contract *ent.TenantContract
}

// Held answers whether a legal hold is on.
func (w Window) Held() bool { return len(w.Holds) > 0 }

// Since is the oldest instant the tenant may look at, and the zero time when
// it may look at all of it.
func (w Window) Since(now time.Time) time.Time {
	if w.View <= 0 {
		return time.Time{}
	}

	return now.Add(-w.View)
}

// Of answers a tenant's windows at `at`.
//
// The view window is the contract in force's. The keep window is the longest of
// that contract's and of every contract before it whose successor's grace has
// not run out: a downgrade on the first of the month with thirty days' grace
// keeps what the old plan kept until the thirty-first, and a plan that comes
// back before then has its history whole. Forever is longer than any number of
// days.
func Of(ctx context.Context, db *ent.Client, tenant pdid.Id, at time.Time, d Defaults) (Window, error) {
	vs, err := db.TenantContract.Query().
		Where(
			tenantcontract.TenantIdEQ(tenant.Uuid()),
			tenantcontract.DateErasedIsNil(),
			tenantcontract.DateEffectiveLTE(at),
		).
		Order(ent.Desc(tenantcontract.FieldDateEffective, tenantcontract.FieldId)).
		All(ctx)
	if err != nil {
		return Window{}, err
	}

	hs, err := db.LegalHold.Query().
		Where(legalhold.TenantIdEQ(tenant.Uuid()), legalhold.DateLiftedIsNil()).
		Order(ent.Asc(legalhold.FieldDateCreated)).
		All(ctx)
	if err != nil {
		return Window{}, err
	}

	w := Window{View: d.View, Keep: d.Keep, Access: d.Access}
	for _, h := range hs {
		w.Holds = append(w.Holds, strings.TrimSpace(h.Name+" "+h.Desc))
	}
	if len(vs) == 0 {
		return w, nil
	}

	w.Contract = vs[0]
	w.View = Days(vs[0].ViewDays)
	w.Keep = Days(vs[0].KeepDays)
	w.Access = accessOf(vs[0], d)
	w.Restore = Days(vs[0].RestoreDays)
	for i := 1; i < len(vs); i++ {
		// The one after this, whose grace is what keeps this one in force.
		next := vs[i-1]
		if !at.Before(next.DateEffective.Add(Days(next.GraceDays))) {
			break
		}

		w.Keep = longer(w.Keep, Days(vs[i].KeepDays))
		w.Access = longer(w.Access, accessOf(vs[i], d))
	}

	return w, nil
}

// accessOf is how long a contract keeps the access record: its own days, or
// the deployment's when it says none.
func accessOf(c *ent.TenantContract, d Defaults) time.Duration {
	if c.AccessKeepDays == 0 {
		return d.Access
	}

	return Days(c.AccessKeepDays)
}

// longer is the longer of two keep windows, where zero is forever.
func longer(a, b time.Duration) time.Duration {
	if a <= 0 || b <= 0 {
		return 0
	}

	return max(a, b)
}

// Kinds is what the trail calls the history: the asset's own writes, the time
// rows, and the events (design 8.2). Their trail lasts as long as the history
// does, so that a destroyed history is not a history the trail still holds as
// values.
var Kinds = []pdid.Domain{
	pd.AssetDomain,
	pd.PlacementDomain,
	pd.LinkDomain,
	pd.StewardshipDomain,
	pd.FactDomain,
	pd.EventDomain,
}

// Trail is the answer the trail's policy asks of each tenant, `trail.Policy.
// Tenants`: the history kinds on the tenant's keep window, and its legal holds
// as payday's.
//
// The rest of the trail -- who signed in, who changed whose role -- is the
// deployment's, under `audit:`, and its floors (`audit.min`) hold whatever a
// contract says.
//
// A row moves out of the database on the deployment's own `retain` for the
// kind, or at the keep window when that comes first; with no `retain` at all it
// stays in the database until the keep window ends it.
//
// And the access record on the contract's `access_keep_days` (#144), which
// the deployment's floor (`audit.by.access.min`) raises a shorter one to.
// With `app.retention.apply` off a contract destroys nothing the deployment
// would keep, so it is answered only where it keeps longer -- and the
// history kinds not at all.
func Trail(db *ent.Client, base trail.Policy, d Defaults) func(context.Context, pdid.Id) (trail.Tenant, error) {
	return func(ctx context.Context, tenant pdid.Id) (trail.Tenant, error) {
		w, err := Of(ctx, db, tenant, time.Now(), d)
		if err != nil {
			return trail.Tenant{}, err
		}

		out := trail.Tenant{By: map[pdid.Domain]trail.Keep{}}
		if w.Held() {
			out.Hold = &trail.Hold{Why: strings.Join(w.Holds, "; ")}
		}

		// Kept forever is the deployment's windows standing, which is what
		// an answer that names no kind means.
		if d.Apply && w.Keep > 0 {
			for _, k := range Kinds {
				out.By[k] = keepFor(w.Keep, base.For(k).Retain)
			}
		}

		if w.Access != d.Access && (d.Apply || longer(w.Access, d.Access) == w.Access) {
			out.By[pd.AccessDomain] = keepFor(w.Access, base.For(pd.AccessDomain).Retain)
		}
		if len(out.By) == 0 {
			out.By = nil
		}

		return out, nil
	}
}

// keepFor is a window of `life` as the trail keeps it: in the database for the
// deployment's `retain` and in the archive after, or -- when it is shorter
// than that -- in the database for the whole of it, with no copy kept.
func keepFor(life, retain time.Duration) trail.Keep {
	switch {
	case life <= 0:
		return trail.Keep{Retain: retain}
	case retain <= 0 || retain >= life:
		return trail.Keep{Retain: life, Discard: true}
	default:
		return trail.Keep{Retain: retain, Destroy: life}
	}
}
