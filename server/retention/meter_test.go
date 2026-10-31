package retention_test

import (
	"testing"
	"time"

	"github.com/lesomnus/z"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/lesomnus/payday/meter"
	"github.com/lesomnus/payday/pdid"

	app "github.com/lesomnus/loft"
	"github.com/lesomnus/loft/server/pd"
	"github.com/lesomnus/loft/server/retention"
)

// keeps has the workspace say it keeps its history `v` days from `at`, as
// `TenantService.Configure` writes it: a shorter window waiting out the
// grace, holding to the longer one meanwhile.
func (e *env) keeps(v uint32, at time.Time, grace time.Duration) {
	e.t.Helper()

	row, err := e.s.Ent.Tenant.Get(e.ctx, e.tenant.Uuid())
	require.NoError(e.t, err)
	s := retention.KeepOf(row).Set(v, at, grace, retention.Days)
	req := app.TenantPatchRequest_builder{
		Ref:              app.TenantRef_builder{Id: e.tenant.Bytes()}.Build(),
		KeepDays:         z.Ptr(s.Days),
		KeepDaysBefore:   z.Ptr(s.Before),
		DateUpdatedForce: z.Ptr(true),
	}.Build()
	if s.Shortened != nil {
		req.SetDateKeepShortened(timestamppb.New(*s.Shortened))
	} else {
		req.SetDateKeepShortenedNull(true)
	}
	_, err = e.s.Base.Tenant().Patch(e.ctx, req)
	require.NoError(e.t, err)
}

// TestTheMeterIsTheWindowInForce (D59): how long a workspace keeps its
// history, as levyan bills it, in whole days and 0 for forever -- the window
// in force when it is measured. A shorter one is the shorter one only once
// the grace is over, and a longer one is the longer one at once.
func TestTheMeterIsTheWindowInForce(t *testing.T) {
	x := require.New(t)
	e := newEnv(t)
	grace := 7 * day
	measured := func(at time.Time) int64 {
		t.Helper()
		m := retention.Meter(e.s.Ent, retention.Defaults{Grace: grace}, func() time.Time { return at })
		v, err := m.Measure(e.ctx, e.tenant)
		x.NoError(err)
		return v
	}

	m := retention.Meter(e.s.Ent, retention.Defaults{}, time.Now)
	x.Equal("loft.history.retention_days", m.Key)
	x.Equal("d", m.Unit)
	x.NoError(meter.CheckKey(m.Key))
	x.NoError(meter.CheckUnit(m.Unit))

	// Forever, which is what a workspace that says nothing keeps.
	x.Zero(measured(e.now))

	// Made shorter, from forever to a year: forever until the grace is over.
	e.keeps(365, e.now, grace)
	x.Zero(measured(e.now), "inside its grace")
	x.Zero(measured(e.now.Add(grace-time.Second)), "inside its grace")
	x.EqualValues(365, measured(e.now.Add(grace)), "from the day the grace is over")
	x.EqualValues(365, measured(e.now.Add(400*day)))

	// Made longer: the longer one at once.
	later := e.now.Add(30 * day)
	e.keeps(730, later, grace)
	x.EqualValues(730, measured(later))

	// From two years to one: two until the grace is over, and one after.
	e.keeps(365, later, grace)
	x.EqualValues(730, measured(later.Add(day)))
	x.EqualValues(365, measured(later.Add(grace)))

	// And made longer again inside that grace: at once, with nothing waiting.
	e.keeps(1000, later.Add(day), grace)
	x.EqualValues(1000, measured(later.Add(day)))

	// The deployment's grace, when it says none: 30 days.
	e.keeps(365, later.Add(2*day), retention.Grace)
	m = retention.Meter(e.s.Ent, retention.Defaults{}, func() time.Time { return later.Add(2*day + 29*day) })
	v, err := m.Measure(e.ctx, e.tenant)
	x.NoError(err)
	x.EqualValues(1000, v)

	// A workspace that is not there keeps nothing, and is not measured as one
	// that keeps everything.
	_, err = m.Measure(e.ctx, pdid.New(pd.TenantDomain))
	x.Error(err)
}
