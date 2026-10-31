// Package retention is how long a workspace keeps its history, now (design 8).
//
// One window, and what it keeps is what is seen (D36): nothing narrows a read
// to less of the history than is kept. The **keep** window is how long the
// history exists at all, which is what a sweep destroys by. The workspace says
// it -- its owner, through `TenantService.Configure`, as the rest of its own
// settings -- and one made shorter waits out the deployment's grace before it
// is the shorter one, so a mistake undone in time has lost nothing.
//
// One answer, read by everything that keeps history: loft's own time rows and
// events, and payday's trail of the writes that made them (payday#35). Two
// answers would be a trail that outlives the history it records, or history the
// trail has already forgotten the making of.
//
// What answers is [Of], from the workspace's own settings (`Tenant.keep_days`
// and the ones beside it), the operator's [app.TenantContract] for what the
// deployment owes beyond them, and [app.LegalHold]. Nothing here destroys
// anything. Whether anything may is the operator's switch,
// `app.retention.apply`, read where the sweeps are wired.
package retention

import (
	"context"
	"strings"
	"time"

	"github.com/lesomnus/payday/pdid"
	"github.com/lesomnus/payday/trail"

	"github.com/lesomnus/loft/internal/ent"
	"github.com/lesomnus/loft/internal/ent/legalhold"
	"github.com/lesomnus/loft/internal/ent/tenantcontract"
	"github.com/lesomnus/loft/server/pd"
)

const day = 24 * time.Hour

// Days is a count of days as a duration, zero staying zero.
func Days(n uint32) time.Duration { return time.Duration(n) * day }

// Grace is how long a window made shorter waits before it is the shorter one,
// when the deployment says nothing (`app.retention.grace`): as long as a
// closed workspace may be restored by default (D29), which is the time a
// deployment gives a mistake to be undone in.
const Grace = 30 * day

// Defaults is what the deployment says beside a workspace's own settings:
// what a workspace that says nothing of its access record gets, the least it
// is kept, the grace, and whether a window destroys at all.
type Defaults struct {
	// Access is how long the deployment keeps the access record, from
	// `audit.by.access`, which a workspace's `access_keep_days` is in place of
	// (#144). Zero is forever.
	Access time.Duration

	// AccessFloor is the least the access record is kept whatever a workspace
	// says, `audit.by.access.min`: what the law the deployment is under asks.
	// Zero is no floor.
	AccessFloor time.Duration

	// Grace is how long a window made shorter waits before it is the shorter
	// one (`app.retention.grace`). Zero is [Grace].
	Grace time.Duration

	// Apply is `app.retention.apply`: whether a workspace's window may destroy
	// what the deployment's own windows would keep. Off, a workspace still
	// keeps the access record longer than the deployment would.
	Apply bool
}

// Wait is how long a window made shorter waits before it is the shorter one:
// Grace, or [Grace] when it says none.
func (d Defaults) Wait() time.Duration {
	if d.Grace <= 0 {
		return Grace
	}

	return d.Grace
}

// AccessDays is a workspace's count of days of its access record as a window,
// as the trail keeps it: the deployment's for none, and never below the
// floor. Zero is forever.
func (d Defaults) AccessDays(n uint32) time.Duration {
	w := d.Access
	if n > 0 {
		w = Days(n)
	}
	if w > 0 && w < d.AccessFloor {
		return d.AccessFloor
	}

	return w
}

// Window is a tenant's history windows at one instant.
type Window struct {
	// Keep is how long its history is kept, with a grace still running already
	// in it. Zero is forever.
	Keep time.Duration

	// Access is how long its access record is kept, the same way: the
	// workspace's, or the deployment's where it says nothing (#144).
	Access time.Duration

	// Restore is how long it may be restored once closed (D29): the
	// workspace's, or zero for the deployment's.
	Restore time.Duration

	// Purge is whether its trail goes whole when it is destroyed, what the
	// deployment's floors are still on with it (`purge_floors`, payday#115).
	// False, which is also a tenant with no contract, is a retirement: what a
	// floor is on stays until it has passed. A contract that purges waits out
	// the grace while one before it retires, as a shorter keep does.
	Purge bool

	// Holds is what the legal holds on it are for. Empty is none; while there
	// is one, nothing of the tenant's history is destroyed, whatever Keep says.
	Holds []string

	// Contract is the contract in force, and nil when there is none.
	Contract *ent.TenantContract
}

// Held answers whether a legal hold is on.
func (w Window) Held() bool { return len(w.Holds) > 0 }

// Setting is one window as a workspace says it: the days it says now, and
// what it said before it was last made shorter and when, which the grace
// holds it to meanwhile. Shortened nil is nothing waiting.
type Setting struct {
	Days      uint32
	Before    uint32
	Shortened *time.Time
}

// KeepOf is how long a workspace says its history is kept.
func KeepOf(t *ent.Tenant) Setting {
	return Setting{Days: t.KeepDays, Before: t.KeepDaysBefore, Shortened: t.DateKeepShortened}
}

// AccessOf is how long a workspace says its access record is kept.
func AccessOf(t *ent.Tenant) Setting {
	return Setting{Days: t.AccessKeepDays, Before: t.AccessKeepDaysBefore, Shortened: t.DateAccessKeepShortened}
}

// held is the days the setting holds to at `at`, as they were said: what it
// says, or -- while the grace of its shortening runs, and what it said before
// keeps longer -- that.
func (s Setting) held(at time.Time, grace time.Duration, days func(uint32) time.Duration) uint32 {
	if s.Shortened != nil && at.Before(s.Shortened.Add(grace)) && outlasts(days(s.Before), days(s.Days)) {
		return s.Before
	}

	return s.Days
}

// At is the window the setting comes to at `at`, its days read by `days`.
func (s Setting) At(at time.Time, grace time.Duration, days func(uint32) time.Duration) time.Duration {
	return days(s.held(at, grace, days))
}

// Until is when the grace of the setting's shortening is over and the window
// is what it says, and the zero time when nothing waits at `at`.
func (s Setting) Until(at time.Time, grace time.Duration, days func(uint32) time.Duration) time.Time {
	if s.held(at, grace, days) == s.Days {
		return time.Time{}
	}

	return s.Shortened.Add(grace)
}

// Set is the setting once it is said to be `v` at `at`. Said as it is, it is
// as it was, and a grace running runs to its end. A window as long as the one
// it holds to then, or longer, is that window at once, and nothing waits. A
// shorter one waits out the grace from `at`, holding meanwhile to what it held
// to: said again within a grace, the longest window any of it still owed is
// held until the grace of the last.
func (s Setting) Set(v uint32, at time.Time, grace time.Duration, days func(uint32) time.Duration) Setting {
	if v == s.Days {
		return s
	}

	held := s.held(at, grace, days)
	if !outlasts(days(held), days(v)) {
		return Setting{Days: v}
	}

	return Setting{Days: v, Before: held, Shortened: &at}
}

// outlasts answers whether window a keeps longer than b, where zero is
// forever.
func outlasts(a, b time.Duration) bool {
	switch {
	case b <= 0:
		return false
	case a <= 0:
		return true
	default:
		return a > b
	}
}

// Of answers a tenant's windows at `at`.
//
// The keep window and the access record's are the workspace's own, each held
// to the longer one it said before while the grace of its shortening runs. A
// tenant that is not there -- one destroyed, whose trail a pass still asks
// about -- keeps forever, and its access record the deployment's.
//
// Whether its trail goes whole when it is destroyed is the contract in force's,
// and a contract that purges waits out the grace while the one before it
// retires: a contract signed to purge from today purges only once the grace
// is over.
func Of(ctx context.Context, db *ent.Client, tenant pdid.Id, at time.Time, d Defaults) (Window, error) {
	w := Window{Access: d.AccessDays(0)}
	grace := d.Wait()

	t, err := db.Tenant.Get(ctx, tenant.Uuid())
	switch {
	case ent.IsNotFound(err):
	case err != nil:
		return Window{}, err
	default:
		w.Keep = KeepOf(t).At(at, grace, Days)
		w.Access = AccessOf(t).At(at, grace, d.AccessDays)
		w.Restore = Days(t.RestoreDays)
	}

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
	if len(vs) > 0 {
		w.Contract = vs[0]
		w.Purge = vs[0].PurgeFloors
		for i := 1; i < len(vs); i++ {
			// The one after this, whose grace is what keeps this one in force.
			if !at.Before(vs[i-1].DateEffective.Add(grace)) {
				break
			}

			w.Purge = w.Purge && vs[i].PurgeFloors
		}
	}

	hs, err := db.LegalHold.Query().
		Where(legalhold.TenantIdEQ(tenant.Uuid()), legalhold.DateLiftedIsNil()).
		Order(ent.Asc(legalhold.FieldDateCreated)).
		All(ctx)
	if err != nil {
		return Window{}, err
	}
	for _, h := range hs {
		w.Holds = append(w.Holds, strings.TrimSpace(h.Name+" "+h.Desc))
	}

	return w, nil
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
// workspace says.
//
// A row moves out of the database on the deployment's own `retain` for the
// kind, or at the keep window when that comes first; with no `retain` at all it
// stays in the database until the keep window ends it.
//
// And the access record on the workspace's `access_keep_days` (#144), which
// the deployment's floor (`audit.by.access.min`) raises a shorter one to.
// With `app.retention.apply` off a workspace destroys nothing the deployment
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
