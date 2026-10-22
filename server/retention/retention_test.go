package retention_test

import (
	"context"
	"testing"
	"time"

	"uuid"

	"github.com/lesomnus/flob"
	"github.com/lesomnus/payday/pdid"
	"github.com/lesomnus/payday/pdtest"
	"github.com/lesomnus/payday/trail"
	"github.com/lesomnus/z"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/timestamppb"

	app "github.com/lesomnus/rove"
	"github.com/lesomnus/rove/cli"
	"github.com/lesomnus/rove/cmd"
	"github.com/lesomnus/rove/internal/ent/audit"
	"github.com/lesomnus/rove/server/pd"
	"github.com/lesomnus/rove/server/retention"
)

const day = 24 * time.Hour

type env struct {
	t      *testing.T
	s      *cmd.Server
	ctx    context.Context
	tenant pdid.Id
	now    time.Time
}

func newEnv(t *testing.T) *env {
	t.Helper()
	x := require.New(t)
	ctx := context.Background()

	driver, dsn := pdtest.DB(t)
	c := cmd.Config{}
	c.Db.Driver = driver
	c.Db.Dsn = dsn
	c.Watch.Broker = "memory"
	c.App.Files = t.TempDir()
	c.Auth.Roster.Db.Driver, c.Auth.Roster.Db.Dsn = pdtest.DB(t)

	s, err := cmd.Build(ctx, c)
	x.NoError(err)
	t.Cleanup(func() { s.Close() })
	x.NoError(cli.Migrate(ctx, s))

	v, err := s.Base.Tenant().Add(ctx, app.TenantAddRequest_builder{Alias: "acme", Name: "Acme"}.Build())
	x.NoError(err)

	return &env{t: t, s: s, ctx: ctx, tenant: must(pdid.From(v.GetId())), now: time.Now()}
}

// says gives the workspace settings of its own, as they would be once
// Configure has said them and any grace is over: written beneath the domain,
// as an operator's shell would.
func (e *env) says(keep, access, restore uint32) {
	e.t.Helper()

	_, err := e.s.Base.Tenant().Patch(e.ctx, app.TenantPatchRequest_builder{
		Ref:              app.TenantRef_builder{Id: e.tenant.Bytes()}.Build(),
		KeepDays:         &keep,
		AccessKeepDays:   &access,
		RestoreDays:      &restore,
		DateUpdatedForce: z.Ptr(true),
	}.Build())
	require.NoError(e.t, err)
}

// shortened says the workspace's settings were made shorter at `at`, from
// what they said before.
func (e *env) shortened(keepBefore, accessBefore uint32, at time.Time) {
	e.t.Helper()

	_, err := e.s.Base.Tenant().Patch(e.ctx, app.TenantPatchRequest_builder{
		Ref:                     app.TenantRef_builder{Id: e.tenant.Bytes()}.Build(),
		KeepDaysBefore:          &keepBefore,
		DateKeepShortened:       timestamppb.New(at),
		AccessKeepDaysBefore:    &accessBefore,
		DateAccessKeepShortened: timestamppb.New(at),
		DateUpdatedForce:        z.Ptr(true),
	}.Build())
	require.NoError(e.t, err)
}

// contract writes one, the way `rove contract set` does.
func (e *env) contract(name string, purges bool, effective time.Time) *app.TenantContract {
	e.t.Helper()

	v, err := e.s.Base.TenantContract().Add(e.ctx, app.TenantContractAddRequest_builder{
		Tenant:        app.TenantRef_builder{Id: e.tenant.Bytes()}.Build(),
		Name:          name,
		PurgeFloors:   z.Ptr(purges),
		DateEffective: timestamppb.New(effective),
	}.Build())
	require.NoError(e.t, err)

	return v
}

func (e *env) window(at time.Time) retention.Window {
	e.t.Helper()

	return e.windowWith(at, retention.Defaults{})
}

func (e *env) windowWith(at time.Time, d retention.Defaults) retention.Window {
	e.t.Helper()

	w, err := retention.Of(e.ctx, e.s.Ent, e.tenant, at, d)
	require.NoError(e.t, err)

	return w
}

// TestAShorterWindowWaitsOutTheGrace (D36): a window a workspace makes
// shorter is the shorter one only once the deployment's grace is over, the
// longer one held meanwhile, so a mistake undone in time has destroyed
// nothing. Made shorter again within the grace, the longest window any of it
// still owed is held until the grace of the last; made as long or longer, it
// is in force at once; said again as it is, a grace running runs on.
func TestAShorterWindowWaitsOutTheGrace(t *testing.T) {
	x := require.New(t)
	now := time.Now()
	grace := 30 * day
	set := func(s retention.Setting, v uint32, at time.Time) retention.Setting {
		return s.Set(v, at, grace, retention.Days)
	}
	at := func(s retention.Setting, when time.Time) time.Duration { return s.At(when, grace, retention.Days) }

	s := set(retention.Setting{}, 365, now)
	x.Zero(at(s, now), "forever lost to a number at once")
	x.Zero(at(s, now.Add(grace-time.Second)))
	x.Equal(365*day, at(s, now.Add(grace)))
	x.Equal(now.Add(grace), s.Until(now, grace, retention.Days))
	x.True(s.Until(now.Add(grace), grace, retention.Days).IsZero(), "a grace over, and still waiting")

	s = set(s, 730, now.Add(10*day))
	x.Zero(at(s, now.Add(grace)), "a window still shorter than what was held, in force before its own grace")
	x.Equal(730*day, at(s, now.Add(10*day+grace)))

	again := set(s, 730, now.Add(20*day))
	x.Equal(s, again, "said again as it is, and its grace began anew")

	x.Equal(retention.Setting{}, set(s, 0, now.Add(11*day)), "forever waited out a grace")

	held := set(retention.Setting{Days: 365}, 730, now)
	x.Equal(retention.Setting{Days: 730}, held, "a longer window waited")
	shorter := set(held, 365, now)
	x.Equal(730*day, at(shorter, now))
	x.Equal(365*day, at(shorter, now.Add(grace)))
}

// TestAWorkspacesWindowsAreItsOwn (D36): how long a workspace keeps its
// history and its access record, and may be restored once closed, are what it
// says -- forever, the deployment's access record, and the deployment's
// restore window while it says nothing -- and one that is not there, a
// workspace destroyed whose trail is still asked about, keeps forever.
func TestAWorkspacesWindowsAreItsOwn(t *testing.T) {
	x := require.New(t)
	e := newEnv(t)
	d := retention.Defaults{Access: 730 * day, AccessFloor: 365 * day}

	w := e.windowWith(e.now, d)
	x.Zero(w.Keep)
	x.Equal(730*day, w.Access, "the deployment's access record")
	x.Zero(w.Restore)

	e.says(180, 400, 90)
	w = e.windowWith(e.now, d)
	x.Equal(180*day, w.Keep)
	x.Equal(400*day, w.Access)
	x.Equal(90*day, w.Restore)

	e.says(180, 30, 90)
	x.Equal(365*day, e.windowWith(e.now, d).Access, "an access record below the floor")

	e.shortened(0, 0, e.now.Add(-day))
	w = e.windowWith(e.now, retention.Defaults{Access: 730 * day, Grace: 30 * day})
	x.Zero(w.Keep, "made shorter a day ago, and the grace not over")
	x.Equal(730*day, w.Access)
	w = e.windowWith(e.now.Add(30*day), retention.Defaults{Access: 730 * day, Grace: 30 * day})
	x.Equal(180*day, w.Keep)
	x.Equal(30*day, w.Access)

	gone, err := retention.Of(e.ctx, e.s.Ent, pdid.New(pd.TenantDomain), e.now, d)
	x.NoError(err, "a workspace no longer there")
	x.Zero(gone.Keep)
	x.Equal(730*day, gone.Access)
}

// TestOnlyTheContractInForceCounts.
func TestOnlyTheContractInForceCounts(t *testing.T) {
	x := require.New(t)
	e := newEnv(t)

	x.Nil(e.window(e.now).Contract, "a tenant with no contract has one")

	e.contract("standard", false, e.now.Add(-10*day))
	e.contract("abroad", true, e.now.Add(10*day))
	x.Equal("standard", e.window(e.now).Contract.Name, "a contract from next month is in force today")

	// A withdrawn one, as `Erase` leaves it.
	wrong := e.contract("mistake", true, e.now.Add(-day))
	_, err := e.s.Base.TenantContract().Erase(e.ctx, app.TenantContractRef_builder{Id: wrong.GetId()}.Build())
	x.NoError(err)
	x.Equal("standard", e.window(e.now).Contract.Name, "a withdrawn contract is in force")
}

// TestAHoldIsOnUntilItIsLifted.
func TestAHoldIsOnUntilItIsLifted(t *testing.T) {
	x := require.New(t)
	e := newEnv(t)

	h, err := e.s.Base.LegalHold().Add(e.ctx, app.LegalHoldAddRequest_builder{
		Tenant: app.TenantRef_builder{Id: e.tenant.Bytes()}.Build(),
		Name:   "case 1",
	}.Build())
	x.NoError(err)
	x.Equal([]string{"case 1"}, e.window(e.now).Holds)

	_, err = e.s.Base.LegalHold().Patch(e.ctx, app.LegalHoldPatchRequest_builder{
		Ref:        app.LegalHoldRef_builder{Id: h.GetId()}.Build(),
		DateLifted: timestamppb.Now(),
	}.Build())
	x.NoError(err)
	x.False(e.window(e.now).Held(), "a lifted hold is still on")
}

// TestTheTrailOfTheHistoryLastsAsLongAsTheHistory.
//
// The trail holds the value of every write, so a trail that outlived the
// history would be the history kept anyway. Its history kinds take the
// tenant's keep window, and its holds as payday's; the rest of the trail is
// the deployment's.
func TestTheTrailOfTheHistoryLastsAsLongAsTheHistory(t *testing.T) {
	x := require.New(t)
	e := newEnv(t)

	base := trail.Policy{Archive: flob.NewMemStores(), By: map[pdid.Domain]trail.Keep{pd.FactDomain: {Retain: 90 * day}}}
	answer := retention.Trail(e.s.Ent, base, retention.Defaults{Apply: true})

	got, err := answer(e.ctx, e.tenant)
	x.NoError(err)
	x.Nil(got.By, "a tenant kept forever names windows")
	x.Nil(got.Hold)

	e.says(730, 0, 0)
	_, err = e.s.Base.LegalHold().Add(e.ctx, app.LegalHoldAddRequest_builder{
		Tenant: app.TenantRef_builder{Id: e.tenant.Bytes()}.Build(),
		Name:   "case 1",
	}.Build())
	x.NoError(err)

	got, err = answer(e.ctx, e.tenant)
	x.NoError(err)
	x.NotNil(got.Hold)
	x.Len(got.By, len(retention.Kinds))
	x.Equal(trail.Keep{Retain: 90 * day, Destroy: 730 * day}, got.By[pd.FactDomain],
		"the deployment's own retain for a kind was not kept")
	x.Equal(trail.Keep{Retain: 730 * day, Discard: true}, got.By[pd.EventDomain])
	x.NotContains(got.By, pd.HolderDomain, "who signed in is on a workspace's window")
}

// TestAPassTakesTheHistorysTrailOnTheTenantsWindow.
//
// Through rove's own generated trail store and payday's pass, which is what
// `serve` runs when `app.retention.apply` is on.
func TestAPassTakesTheHistorysTrailOnTheTenantsWindow(t *testing.T) {
	x := require.New(t)
	e := newEnv(t)

	e.says(100, 0, 0)

	old := e.now.Add(-200 * day)
	history := e.trailRow(pd.AssetDomain, old)
	account := e.trailRow(pd.HolderDomain, old)

	// With a key: a pass over an archive is refused without one to sign its
	// account with (payday#115), and would take nothing.
	p := trail.Policy{Archive: flob.NewMemStores(), Signer: pdtest.Signer(t)}
	p.Tenants = retention.Trail(e.s.Ent, p, retention.Defaults{Apply: true})
	p.Pass(e.ctx, pd.TrailStore(e.s.Ent))

	x.False(e.exists(history), "the trail of the history outlived the tenant's keep window")
	x.True(e.exists(account), "the trail of an account went on the tenant's window")
}

func (e *env) trailRow(d pdid.Domain, at time.Time) uuid.UUID {
	e.t.Helper()

	v, err := e.s.Ent.Audit.Create().
		SetId(uuid.NewV7()).
		SetTenantId(e.tenant.Uuid()).
		SetActorTenantId(e.tenant.Uuid()).
		SetActorId(uuid.Nil()).
		SetTraceId([]byte{}).
		SetAction("/rove.Test/Write").
		SetObjectId(pdid.New(d).Uuid()).
		SetDomain(uint32(d)).
		SetPatch([]byte{}).
		SetValue([]byte("contents")).
		SetDateCreated(at).
		Save(e.ctx)
	require.NoError(e.t, err)

	return v.Id
}

func (e *env) exists(id uuid.UUID) bool {
	e.t.Helper()

	n, err := e.s.Ent.Audit.Query().Where(audit.IdEQ(id)).Count(e.ctx)
	require.NoError(e.t, err)

	return n > 0
}

func must[T any](v T, err error) T {
	if err != nil {
		panic(err)
	}

	return v
}

// TestTheAccessRecordIsKeptAsTheWorkspaceSays (#144, D36): how long a
// workspace's access record is kept turns on the workspace -- how many people
// it holds, what kind of data, where it is -- so it says, and the deployment's
// `audit.by.access` is what one that says nothing gets. With the windows off a
// workspace destroys nothing the deployment would keep, so only a longer one
// is answered.
func TestTheAccessRecordIsKeptAsTheWorkspaceSays(t *testing.T) {
	x := require.New(t)
	e := newEnv(t)

	twoYears := 730 * day
	base := trail.Policy{Archive: flob.NewMemStores(), By: map[pdid.Domain]trail.Keep{pd.AccessDomain: {Retain: 90 * day, Destroy: twoYears}}}
	off := retention.Defaults{Access: twoYears}
	on := retention.Defaults{Access: twoYears, Apply: true}
	answer := func(d retention.Defaults) trail.Tenant {
		t.Helper()
		v, err := retention.Trail(e.s.Ent, base, d)(e.ctx, e.tenant)
		require.NoError(t, err)
		return v
	}

	x.Nil(answer(on).By, "a workspace that says nothing was answered")

	e.says(0, 3*365, 0)
	x.Equal(trail.Keep{Retain: 90 * day, Destroy: 3 * 365 * day}, answer(off).By[pd.AccessDomain], "a longer one waits for the windows")

	e.says(0, 365, 0)
	x.NotContains(answer(off).By, pd.AccessDomain, "a shorter one destroyed with the windows off")
	x.Equal(trail.Keep{Retain: 90 * day, Destroy: 365 * day}, answer(on).By[pd.AccessDomain])
	x.NotContains(answer(on).By, pd.AssetDomain, "an access record named the history's windows")

	t.Run("and none goes below the deployment's floor", func(t *testing.T) {
		x := require.New(t)
		e := newEnv(t)
		e.says(0, 90, 0)

		p := base
		p.MinBy = map[pdid.Domain]trail.Floor{pd.AccessDomain: {Destroy: 365 * day}}
		v, err := retention.Trail(e.s.Ent, p, on)(e.ctx, e.tenant)
		x.NoError(err)
		x.Equal(trail.Keep{Retain: 90 * day, Destroy: 365 * day}, p.ForTenant(v, pd.AccessDomain), "a workspace went below the floor")
	})
}

// TestAContractThatPurgesWaitsOutTheGrace (payday#115): a workspace retires
// when it is destroyed, keeping what the deployment's floors are on, unless
// the contract in force when it is says to purge -- and one that does waits
// out the deployment's grace while a contract before it retires, as a shorter
// window does.
func TestAContractThatPurgesWaitsOutTheGrace(t *testing.T) {
	x := require.New(t)
	e := newEnv(t)
	d := retention.Defaults{Grace: 30 * day}

	x.False(e.windowWith(e.now, d).Purge, "no contract, and yet it purges")

	e.contract("kept", false, e.now.Add(-60*day))
	x.False(e.windowWith(e.now, d).Purge)

	e.contract("abroad", true, e.now.Add(-day))
	x.False(e.windowWith(e.now, d).Purge, "a purge did not wait out the grace while the contract before it retires")
	x.True(e.windowWith(e.now.Add(29*day), d).Purge, "the grace is over, and still it retires")

	e.contract("back", false, e.now)
	x.False(e.windowWith(e.now.Add(30*day), d).Purge, "the contract in force retires, and the one before it purges")
}
