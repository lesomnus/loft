package cmd_test

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"testing"
	"time"

	levyanv1 "github.com/lesomnus/levyan/api/levyan/v1"
	"github.com/lesomnus/otx/log"
	"github.com/lesomnus/z"
	"github.com/stretchr/testify/require"

	"github.com/lesomnus/payday/meter"
	"github.com/lesomnus/payday/pdid"
	"github.com/lesomnus/payday/spin"

	app "github.com/lesomnus/loft"
	"github.com/lesomnus/loft/cmd"
	"github.com/lesomnus/loft/internal/ent/aliashold"
	enttenant "github.com/lesomnus/loft/internal/ent/tenant"
	"github.com/lesomnus/loft/internal/levyantest"
	"github.com/lesomnus/loft/server/pd"
)

// billed is the world billing to a levyan of its own.
func billed(t *testing.T) (*world, *levyantest.Levyan) {
	fake := levyantest.Start(t)
	return newWorldWith(t, func(c *cmd.Config) { c.App.Billing.Levyan = fake.Config() }), fake
}

func tenantId(x *require.Assertions, t *app.Tenant) pdid.Id {
	id, err := pdid.From(t.GetId())
	x.NoError(err)
	return id
}

// measure is what one of the schema's meters reads of a workspace now.
func (w *world) measure(key string, t *app.Tenant) int64 {
	for _, m := range pd.Meters(w.s.Ent) {
		if m.Key != key {
			continue
		}
		v, err := m.Measure(context.Background(), tenantId(w.x, t))
		w.x.NoError(err)
		return v
	}
	w.x.Failf("no meter", "the schema declares no %s", key)
	return 0
}

// held is what levyan was last handed of a workspace, by meter.
func held(x *require.Assertions, fake *levyantest.Levyan, t *app.Tenant) map[string]int64 {
	out := map[string]int64{}
	for _, r := range fake.Readings() {
		if r.GetWorkspace() == tenantId(x, t).String() {
			out[r.GetMeter()] = r.GetValue()
		}
	}
	return out
}

// spun is the names of the loops the server runs.
func spun(s *cmd.Server) []string {
	out := []string{}
	for _, v := range s.Spin {
		if n, ok := v.(spin.Named); ok {
			out = append(out, n.SpinName())
		}
	}
	return out
}

// TestTheMetersCountWhatAWorkspaceHolds (D59): the queries `pd gen` wrote of
// `proto/loft/meter.proto`, on the database the tests run on -- SQLite, and
// PostgreSQL with PDTEST_POSTGRES. A workspace's own rows, and not another's;
// none that is erased; and of the assets, none disposed of or lost.
func TestTheMetersCountWhatAWorkspaceHolds(t *testing.T) {
	w := newWorld(t)
	x := w.x
	ctx := context.Background()
	beta, err := w.s.Base.Tenant().Add(ctx, app.TenantAddRequest_builder{Alias: "beta"}.Build())
	x.NoError(err)
	empty, err := w.s.Base.Tenant().Add(ctx, app.TenantAddRequest_builder{Alias: "gamma"}.Build())
	x.NoError(err)
	ref := func(t *app.Tenant) *app.TenantRef { return app.TenantRef_builder{Id: t.GetId()}.Build() }

	asset := func(t *app.Tenant, tag, status string) *app.Asset {
		v, err := w.s.Base.Asset().Add(ctx, app.AssetAddRequest_builder{Tenant: ref(t), Name: tag, Tag: tag, Kind: "item", Status: status}.Build())
		x.NoError(err)
		return v
	}
	for _, s := range []string{"active", "in_repair", "ordered", "retired", ""} {
		asset(w.tenant, "A-"+s, s)
	}
	asset(w.tenant, "A-disposed", "disposed")
	asset(w.tenant, "A-lost", "lost")
	erased := asset(w.tenant, "A-erased", "active")
	_, err = w.s.Base.Asset().Erase(ctx, app.AssetRef_builder{Id: erased.GetId()}.Build())
	x.NoError(err)
	asset(beta, "B-1", "active")
	asset(beta, "B-2", "active")
	asset(beta, "B-3", "disposed")

	file := func(t *app.Tenant, size uint64) *app.Attachment {
		v, err := w.s.Base.Attachment().Add(ctx, app.AttachmentAddRequest_builder{
			Tenant: ref(t), Name: "f", SubjectId: pdid.New(pd.AssetDomain).Bytes(), ObjectKey: "k", SizeBytes: size,
		}.Build())
		x.NoError(err)
		return v
	}
	file(w.tenant, 100)
	file(w.tenant, 2000)
	gone := file(w.tenant, 5000)
	_, err = w.s.Base.Attachment().Erase(ctx, app.AttachmentRef_builder{Id: gone.GetId()}.Build())
	x.NoError(err)
	file(beta, 7)

	w.holder("kim", "member")
	lee := w.holder("lee", "admin")
	_, err = w.s.Base.Holder().Erase(ctx, app.HolderRef_builder{Id: lee.GetId()}.Build())
	x.NoError(err, "a membership ended")
	p, err := w.s.Identity.AddPerson(ctx, "park", "park", pdid.Nil)
	x.NoError(err)
	w.member(beta, p, "owner")

	x.EqualValues(5, w.measure("loft.assets.active", w.tenant), "every status but disposed and lost, and none erased")
	x.EqualValues(2, w.measure("loft.assets.active", beta))
	x.EqualValues(2100, w.measure("loft.attachments.bytes", w.tenant))
	x.EqualValues(7, w.measure("loft.attachments.bytes", beta))
	x.EqualValues(2, w.measure("loft.members", w.tenant), "its owner and kim")
	x.EqualValues(1, w.measure("loft.members", beta))
	for _, k := range []string{"loft.assets.active", "loft.attachments.bytes", "loft.members"} {
		x.Zero(w.measure(k, empty), "%s of a workspace that holds nothing", k)
	}

	units := map[string]string{}
	for _, m := range pd.Meters(w.s.Ent) {
		units[m.Key] = m.Unit
	}
	x.Equal(map[string]string{"loft.assets.active": "{asset}", "loft.attachments.bytes": "By", "loft.members": "{member}"}, units)
}

// TestADeploymentThatNamesNoLevyanMeasuresNothing: `app.billing.levyan`
// unset is Loft as it was -- no reader, no loop, and a workspace destroyed
// with nothing read of it first (`TestAClosedWorkspaceIsDestroyedOnceItsWindowIsOver`).
func TestADeploymentThatNamesNoLevyanMeasuresNothing(t *testing.T) {
	w := newWorld(t)
	x := w.x

	x.Nil(w.s.Meters)
	x.Nil(w.s.Levyan)
	x.Nil(w.s.Closing.Meters)
	x.NotContains(spun(w.s), meter.ReaderName)
	x.NoError(w.s.Close(), "nothing of levyan's to close")
}

// TestEveryWorkspaceIsMeasuredForLevyan (D59): a deployment that names levyan
// runs the reader under the lease -- the schema's three meters, and how long
// the workspace keeps its history -- and hands each workspace's readings over
// together, from one moment, as the canonical text of its identifier.
func TestEveryWorkspaceIsMeasuredForLevyan(t *testing.T) {
	w, fake := billed(t)
	x := w.x
	ctx := context.Background()

	x.NotNil(w.s.Meters)
	x.Same(w.s.Meters, w.s.Closing.Meters, "the one reader, which a destruction reads a workspace with first")
	x.Contains(spun(w.s), meter.ReaderName)
	keys := []string{}
	for _, m := range w.s.Meters.Meters {
		keys = append(keys, m.Key+" "+m.Unit)
	}
	x.Equal([]string{"loft.assets.active {asset}", "loft.attachments.bytes By", "loft.members {member}", "loft.history.retention_days d"}, keys)

	// Under a lease nothing holds, every loop's name is one the processes
	// agree on -- the reader's among them.
	run, cancel := context.WithTimeout(t.Context(), 300*time.Millisecond)
	defer cancel()
	x.NoError(spin.Run(run, func(yield func(any) bool) {
		for _, v := range w.s.Spin {
			if !yield(v) {
				return
			}
		}
	}, spin.WithLease(spin.Nowhere())))

	_, err := app.NewAssetServiceClient(w.conn).Add(w.owner, app.AssetAddRequest_builder{Name: "노트북"}.Build())
	x.NoError(err)
	x.NoError(w.s.Meters.Read(ctx, tenantId(x, w.tenant)))

	calls := fake.Calls()
	x.Len(calls, 1)
	x.Len(calls[0], 4, "every meter of it, in one call")
	for _, r := range calls[0] {
		x.Equal(tenantId(x, w.tenant).String(), r.GetWorkspace())
		x.Equal(calls[0][0].GetMeasuredAt().AsTime(), r.GetMeasuredAt().AsTime(), "one moment")
	}
	x.Equal(map[string]int64{
		"loft.assets.active": 1, "loft.attachments.bytes": 0, "loft.members": 1, "loft.history.retention_days": 0,
	}, held(x, fake, w.tenant))
}

// TestTheRetentionMeterIsTheWindowPreviewSays (D59): a workspace made to keep
// its history a year, from forever, is billed for forever until the day
// `TenantService.Preview` said the year takes effect from -- once the grace
// is over -- and for the year from then.
func TestTheRetentionMeterIsTheWindowPreviewSays(t *testing.T) {
	w, fake := billed(t)
	x := w.x
	ctx := context.Background()
	tenants := app.NewTenantServiceClient(w.conn)
	ref := app.TenantRef_builder{Id: w.tenant.GetId()}.Build()
	keep := func() int64 {
		t.Helper()
		x.NoError(w.s.Meters.Read(ctx, tenantId(x, w.tenant)))
		return held(x, fake, w.tenant)["loft.history.retention_days"]
	}
	at := func(v time.Time) {
		w.s.Deps.Now = func() time.Time { return v }
	}
	t.Cleanup(func() { w.s.Deps.Now = nil })

	x.Zero(keep(), "forever")
	_, err := tenants.Configure(w.owner, app.TenantConfigureRequest_builder{Ref: ref, KeepDays: z.Ptr(uint32(365))}.Build())
	x.NoError(err)
	v, err := tenants.Preview(w.owner, app.TenantPreviewRequest_builder{Ref: ref, KeepDays: z.Ptr(uint32(365))}.Build())
	x.NoError(err)
	from := v.GetKeepFrom().AsTime()
	x.True(from.After(time.Now().Add(29*24*time.Hour)), "a shorter window waits out the grace")

	x.Zero(keep(), "forever, until the grace is over")
	at(from.Add(-time.Second))
	x.Zero(keep())
	at(from)
	x.EqualValues(365, keep(), "the year, from the day Preview said")

	// And a longer one is the longer one at once.
	_, err = tenants.Configure(w.owner, app.TenantConfigureRequest_builder{Ref: ref, KeepDays: z.Ptr(uint32(730))}.Build())
	x.NoError(err)
	x.EqualValues(730, keep())
}

// TestAWorkspaceIsMeasuredBeforeItIsDestroyed (D59): the last of what it
// held is read and handed to levyan while the rows it is read from are still
// there, by the sweep as by an operator's hand -- and only then is anything
// of it taken.
func TestAWorkspaceIsMeasuredBeforeItIsDestroyed(t *testing.T) {
	w, fake := billed(t)
	x := w.x
	ctx := context.Background()
	id := tenantId(x, w.tenant)
	_, err := app.NewAssetServiceClient(w.conn).Add(w.owner, app.AssetAddRequest_builder{Name: "노트북"}.Build())
	x.NoError(err)
	w.closed()

	// What the workspace was when levyan was told: read in levyan's handler,
	// and said here.
	type state struct {
		alive, held bool
		err         error
	}
	seen := make(chan state, 1)
	fake.Answer(func(ctx context.Context, req *levyanv1.RecordRequest) (*levyanv1.RecordResponse, error) {
		var v state
		v.alive, v.err = w.s.Ent.Tenant.Query().Where(enttenant.Id(id.Uuid())).Exist(ctx)
		if v.err == nil {
			v.held, v.err = w.s.Ent.AliasHold.Query().Where(aliashold.Held(w.tenant.GetAlias())).Exist(ctx)
		}
		seen <- v
		return kept(req), nil
	})

	x.NoError(w.s.Closing.Sweep(ctx))
	v := <-seen
	x.NoError(v.err)
	x.True(v.alive, "measured while it was there")
	x.False(v.held, "and before its address was held")
	x.Equal(map[string]int64{
		"loft.assets.active": 1, "loft.attachments.bytes": 0, "loft.members": 1, "loft.history.retention_days": 0,
	}, held(x, fake, w.tenant))
	alive, err := w.s.Ent.Tenant.Query().Where(enttenant.Id(id.Uuid())).Exist(ctx)
	x.NoError(err)
	x.False(alive, "and then destroyed")
}

// logged is a context whose log is kept, for a test to read what was said.
func logged(ctx context.Context) (context.Context, *bytes.Buffer) {
	var b bytes.Buffer
	return log.Into(ctx, slog.New(slog.NewTextHandler(&b, nil))), &b
}

// gone says whether the world's workspace was destroyed: its row gone and its
// address held.
func (w *world) gone() bool {
	ctx := context.Background()
	alive, err := w.s.Ent.Tenant.Query().Where(enttenant.Id(tenantId(w.x, w.tenant).Uuid())).Exist(ctx)
	w.x.NoError(err)
	held, err := w.s.Ent.AliasHold.Query().Where(aliashold.Held(w.tenant.GetAlias())).Exist(ctx)
	w.x.NoError(err)
	return !alive && held
}

// TestAWorkspaceIsDestroyedWhenLevyanCannotBeReached (D59): measuring before
// a destruction is as well as it can be done (Claude, in place of the rule
// that a workspace not measured was not destroyed). A destruction is the
// law's and waits for no billing service: with levyan not there, the
// workspace goes all the same, and the log says it went without its final
// reading -- levyan bills it by its last daily one.
func TestAWorkspaceIsDestroyedWhenLevyanCannotBeReached(t *testing.T) {
	w := newWorldWith(t, func(c *cmd.Config) { c.App.Billing.Levyan = levyantest.Gone(t) })
	x := w.x
	ctx, said := logged(context.Background())
	x.NotNil(w.s.Closing.Meters)
	w.closed()

	x.NoError(w.s.Closing.Sweep(ctx))
	x.True(w.gone(), "destroyed, with levyan not there")
	x.Contains(said.String(), "a workspace is destroyed without its final reading")
	x.Contains(said.String(), "workspace="+w.tenant.GetAlias())
	x.Contains(said.String(), "Unavailable", "and why")
	x.NotContains(said.String(), levyantest.Key)
}

// TestAWorkspaceIsDestroyedWhenAMeterCannotBeRead (D59): the same for a meter
// that fails, and what was measured of the rest still reaches levyan first.
func TestAWorkspaceIsDestroyedWhenAMeterCannotBeRead(t *testing.T) {
	w, fake := billed(t)
	x := w.x
	ctx, said := logged(context.Background())
	broken := *w.s.Closing.Meters
	broken.Meters = append(append([]meter.Meter{}, broken.Meters...), meter.Meter{
		Key: "loft.broken", Unit: "1", Measure: func(context.Context, pdid.Id) (int64, error) { return 0, errors.New("the database blinked") },
	})
	w.s.Closing.Meters = &broken
	w.closed()

	x.NoError(w.s.Closing.Sweep(ctx))
	x.True(w.gone())
	x.Contains(said.String(), "a workspace is destroyed without its final reading")
	x.Contains(said.String(), "the database blinked")
	x.Equal(map[string]int64{
		"loft.assets.active": 0, "loft.attachments.bytes": 0, "loft.members": 1, "loft.history.retention_days": 0,
	}, held(x, fake, w.tenant), "what could be measured went first")
}

// TestARefusedReadingDoesNotHoldADestructionUp (D59): levyan refusing a
// reading -- a meter its price book has not got -- is levyan having answered,
// and it is said as a refusal and not as a reading missing.
func TestARefusedReadingDoesNotHoldADestructionUp(t *testing.T) {
	w, fake := billed(t)
	x := w.x
	ctx, said := logged(context.Background())
	fake.Refuse("loft.members", levyanv1.Refusal_REFUSAL_NO_METER)
	w.closed()

	x.NoError(w.s.Closing.Sweep(ctx))
	x.True(w.gone())
	x.Contains(said.String(), "a reading was refused")
	x.NotContains(said.String(), "without its final reading")
	x.Len(fake.Calls(), 1)
}

// closed closes the world's workspace and has its restore window over.
func (w *world) closed() {
	ctx := context.Background()
	ref := app.TenantRef_builder{Id: w.tenant.GetId()}.Build()
	_, err := app.NewTenantServiceClient(w.conn).Close(w.owner, app.TenantCloseRequest_builder{Ref: ref, Confirm: w.called()}.Build())
	w.x.NoError(err)
	w.x.NoError(w.s.Ent.Tenant.UpdateOneId(tenantId(w.x, w.tenant).Uuid()).SetDateDestroy(time.Now().Add(-time.Minute)).Exec(ctx))
}

// kept is levyan keeping every reading of a call.
func kept(req *levyanv1.RecordRequest) *levyanv1.RecordResponse {
	out := []*levyanv1.Recorded{}
	for range req.GetReadings() {
		out = append(out, levyanv1.Recorded_builder{Outcome: levyanv1.Outcome_OUTCOME_KEPT}.Build())
	}
	return levyanv1.RecordResponse_builder{Results: out}.Build()
}
