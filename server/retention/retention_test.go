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

// contract writes one, the way `rove contract set` does.
func (e *env) contract(name string, view, keep, grace uint32, effective time.Time) *app.TenantContract {
	e.t.Helper()

	v, err := e.s.Base.TenantContract().Add(e.ctx, app.TenantContractAddRequest_builder{
		Tenant:        app.TenantRef_builder{Id: e.tenant.Bytes()}.Build(),
		Name:          name,
		ViewDays:      view,
		KeepDays:      keep,
		GraceDays:     grace,
		DateEffective: timestamppb.New(effective),
	}.Build())
	require.NoError(e.t, err)

	return v
}

func (e *env) window(at time.Time) retention.Window {
	e.t.Helper()

	w, err := retention.Of(e.ctx, e.s.Ent, e.tenant, at, retention.Defaults{})
	require.NoError(e.t, err)

	return w
}

// TestAShorterKeepWaitsOutItsGrace.
//
// What a tenant may look at shrinks the moment its contract says so; what is
// kept shrinks only once the grace is over, so a downgrade is not a deletion
// on the day it is signed.
func TestAShorterKeepWaitsOutItsGrace(t *testing.T) {
	x := require.New(t)
	e := newEnv(t)

	e.contract("free", 365, 730, 0, e.now.Add(-100*day))
	e.contract("trial", 90, 180, 30, e.now.Add(-10*day))

	w := e.window(e.now)
	x.Equal(90*day, w.View, "the view did not shrink when the contract said")
	x.Equal(730*day, w.Keep, "the keep shrank before the grace was over")
	x.Equal("trial", w.Contract.Name)

	w = e.window(e.now.Add(21 * day))
	x.Equal(180*day, w.Keep, "the grace never ended")
}

// TestAPlanThatComesBackInTimeHasLostNothing.
func TestAPlanThatComesBackInTimeHasLostNothing(t *testing.T) {
	x := require.New(t)
	e := newEnv(t)

	e.contract("pro", 0, 1825, 0, e.now.Add(-400*day))
	e.contract("free", 365, 730, 30, e.now.Add(-20*day))
	e.contract("pro", 0, 1825, 0, e.now.Add(-5*day))

	w := e.window(e.now)
	x.Equal(time.Duration(0), w.View, "the plan that came back does not show all of the history again")
	x.Equal(1825*day, w.Keep)
}

// TestForeverIsLongerThanAnyNumberOfDays.
func TestForeverIsLongerThanAnyNumberOfDays(t *testing.T) {
	x := require.New(t)
	e := newEnv(t)

	e.contract("enterprise", 0, 0, 0, e.now.Add(-400*day))
	e.contract("free", 365, 730, 30, e.now.Add(-10*day))

	x.Equal(time.Duration(0), e.window(e.now).Keep, "forever lost to a number during the grace")
	x.Equal(730*day, e.window(e.now.Add(30*day)).Keep)
}

// TestOnlyTheContractInForceCounts.
func TestOnlyTheContractInForceCounts(t *testing.T) {
	x := require.New(t)
	e := newEnv(t)

	x.Nil(e.window(e.now).Contract, "a tenant with no contract has one")

	e.contract("free", 365, 730, 0, e.now.Add(-10*day))
	e.contract("pro", 0, 1825, 0, e.now.Add(10*day))
	x.Equal("free", e.window(e.now).Contract.Name, "a contract from next month is in force today")

	// A withdrawn one, as `Erase` leaves it.
	wrong := e.contract("mistake", 1, 1, 0, e.now.Add(-day))
	_, err := e.s.Base.TenantContract().Erase(e.ctx, app.TenantContractRef_builder{Id: wrong.GetId()}.Build())
	x.NoError(err)
	x.Equal("free", e.window(e.now).Contract.Name, "a withdrawn contract is in force")

	t.Run("and without one, the deployment's", func(t *testing.T) {
		x := require.New(t)

		e := newEnv(t)
		w, err := retention.Of(e.ctx, e.s.Ent, e.tenant, e.now, retention.Defaults{View: 365 * day, Keep: 730 * day})
		x.NoError(err)
		x.Equal(365*day, w.View)
		x.Equal(730*day, w.Keep)
	})
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
	answer := retention.Trail(e.s.Ent, base, retention.Defaults{})

	got, err := answer(e.ctx, e.tenant)
	x.NoError(err)
	x.Nil(got.By, "a tenant kept forever names windows")
	x.Nil(got.Hold)

	e.contract("free", 365, 730, 0, e.now.Add(-day))
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
	x.NotContains(got.By, pd.HolderDomain, "who signed in is on a contract's window")
}

// TestAPassTakesTheHistorysTrailOnTheTenantsWindow.
//
// Through rove's own generated trail store and payday's pass, which is what
// `serve` runs when `app.retention.apply` is on.
func TestAPassTakesTheHistorysTrailOnTheTenantsWindow(t *testing.T) {
	x := require.New(t)
	e := newEnv(t)

	e.contract("trial", 30, 100, 0, e.now.Add(-day))

	old := e.now.Add(-200 * day)
	history := e.trailRow(pd.AssetDomain, old)
	account := e.trailRow(pd.HolderDomain, old)

	p := trail.Policy{Archive: flob.NewMemStores()}
	p.Tenants = retention.Trail(e.s.Ent, p, retention.Defaults{})
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
