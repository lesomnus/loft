package retention

import (
	"context"
	"time"

	"github.com/lesomnus/payday/meter"
	"github.com/lesomnus/payday/pdid"

	"github.com/lesomnus/loft/internal/ent"
)

// MeterKey is what the meter of how long a workspace keeps its history is
// filed under at levyan (`proto/loft/meter.proto`, D59).
const MeterKey = "loft.history.retention_days"

// Meter is how long a workspace keeps its history, as it is billed: a meter
// the schema cannot declare, written beside the ones it does (payday#186).
//
// It is the workspace's own window in force at `now`, in whole days, and 0
// for forever: read as [Of] reads it, so that a window made shorter is the
// shorter one only once the deployment's grace is over (`app.retention.
// grace`) -- the day `TenantService.Preview` says it takes effect from --
// and one made longer is the longer one at once. It is what the workspace
// keeps by its settings whether or not `app.retention.apply` lets a window
// destroy anything, and what a legal hold keeps beyond it is not the
// workspace's to be billed for.
//
// A workspace that is not there keeps nothing, and is not measured: forever
// is what a window of 0 says, and the most a reading can.
func Meter(db *ent.Client, d Defaults, now func() time.Time) meter.Meter {
	return meter.Meter{
		Key:  MeterKey,
		Unit: "d",
		Measure: func(ctx context.Context, tenant pdid.Id) (int64, error) {
			t, err := db.Tenant.Get(ctx, tenant.Uuid())
			if err != nil {
				return 0, err
			}

			return int64(KeepOf(t).At(now(), d.Wait(), Days) / day), nil
		},
	}
}
