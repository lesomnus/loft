package domain_test

import (
	"context"
	"testing"
	"time"

	"github.com/lesomnus/z"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"

	app "github.com/lesomnus/rove"
	"github.com/lesomnus/rove/internal/ent/placement"
	"github.com/lesomnus/rove/server/retention"
)

// TestAWorkspaceSaysHowLongItKeepsItsHistory (D36): how long its history and
// its access record are kept, and how long it may be restored once closed, are
// the workspace's own, its owner's to say through Configure -- and a window
// made shorter waits out the deployment's grace, the longer one held
// meanwhile, so a mistake undone in time has destroyed nothing.
func TestAWorkspaceSaysHowLongItKeepsItsHistory(t *testing.T) {
	e := newEnv(t)
	x := e.x
	e.s.Deps.Retention.AccessFloor = 365 * day
	grace := e.s.Deps.Retention.Wait()
	ws := app.TenantRef_builder{Id: e.tenant.Bytes()}.Build()
	configure := func(v *app.TenantConfigureRequest) (*app.Tenant, error) {
		v.SetRef(ws)
		return e.app().Tenant().Configure(e.owner, v)
	}
	window := func() retention.Window {
		w, err := retention.Of(context.Background(), e.s.Ent, e.tenant, e.now, e.s.Deps.Retention)
		x.NoError(err)
		return w
	}

	_, err := e.app().Tenant().Patch(e.owner, app.TenantPatchRequest_builder{Ref: ws, KeepDays: z.Ptr(uint32(30)), DateUpdatedForce: z.Ptr(true)}.Build())
	x.Equal(codes.InvalidArgument, codeOf(err), "said past Configure, its grace and its event")
	_, err = e.app().Tenant().Patch(e.owner, app.TenantPatchRequest_builder{Ref: ws, DateKeepShortenedNull: z.Ptr(true), DateUpdatedForce: z.Ptr(true)}.Build())
	x.Equal(codes.InvalidArgument, codeOf(err), "a grace ended past Configure")
	_, err = e.app().Tenant().Patch(e.owner, app.TenantPatchRequest_builder{Ref: ws, Name: z.Ptr("acme"), DateUpdatedForce: z.Ptr(true)}.Build())
	x.NoError(err, "what is the workspace's to patch")
	_, err = configure(app.TenantConfigureRequest_builder{AccessKeepDays: z.Ptr(uint32(364))}.Build())
	x.Equal(codes.InvalidArgument, codeOf(err), "an access record kept less than the law asks")
	_, err = configure(app.TenantConfigureRequest_builder{KeepDays: z.Ptr(uint32(100*365 + 1))}.Build())
	x.Equal(codes.InvalidArgument, codeOf(err), "a window past what a duration holds")
	x.Zero(window().Keep, "a workspace that says nothing keeps forever")

	v, err := configure(app.TenantConfigureRequest_builder{KeepDays: z.Ptr(uint32(365)), RestoreDays: z.Ptr(uint32(90))}.Build())
	x.NoError(err)
	x.EqualValues(365, v.GetKeepDays())
	x.EqualValues(90, v.GetRestoreDays())
	x.Zero(window().Keep, "made shorter, and the grace has not run")
	evs := e.eventsOf("workspace.configure")
	x.Len(evs, 1)
	x.Equal(e.now.Add(grace).UTC(), value[*app.WorkspaceConfigured](e, evs[0]).GetKeepFrom().AsTime(), "its event says from when")

	// Said again within the grace, a window still shorter than what is held
	// is held to it until the grace of the last.
	e.now = e.now.Add(10 * day)
	_, err = configure(app.TenantConfigureRequest_builder{KeepDays: z.Ptr(uint32(730))}.Build())
	x.NoError(err)
	e.now = e.now.Add(grace - time.Minute)
	x.Zero(window().Keep, "the grace of the last is what ends it")
	e.now = e.now.Add(time.Minute)
	x.Equal(730*day, window().Keep)

	// Made longer, at once.
	_, err = configure(app.TenantConfigureRequest_builder{KeepDays: z.Ptr(uint32(0))}.Build())
	x.NoError(err)
	x.Zero(window().Keep)

	// The access record the same way, the deployment's while it says none.
	e.s.Deps.Retention.Access = 730 * day
	_, err = configure(app.TenantConfigureRequest_builder{AccessKeepDays: z.Ptr(uint32(400))}.Build())
	x.NoError(err)
	x.Equal(730*day, window().Access, "the deployment's held through the grace")
	e.now = e.now.Add(grace)
	x.Equal(400*day, window().Access)

	me, err := e.app().Party().Me(e.owner, &app.PartyMeRequest{})
	x.NoError(err)
	x.EqualValues(90, me.GetRestoreDays(), "what the owner is told before the close")
	cur, err := e.app().Tenant().Get(e.owner, app.TenantGetRequest_builder{Ref: ws}.Build())
	x.NoError(err)
	closed, err := e.app().Tenant().Close(e.owner, app.TenantCloseRequest_builder{Ref: ws, Confirm: cur.GetName()}.Build())
	x.NoError(err)
	x.WithinDuration(e.now.Add(90*day), closed.GetDateDestroy().AsTime(), time.Minute, "destroyed on its own window, not the deployment's")
}

// TestAPreviewSaysWhatAWindowTakesAndFromWhen (D36): before a change is saved,
// its owner is told how long the workspace would keep its history and from
// when -- once the grace of a shorter window is over -- and what that takes:
// what a retention pass takes then, to the row, the events nothing left points
// at among them.
func TestAPreviewSaysWhatAWindowTakesAndFromWhen(t *testing.T) {
	e := newEnv(t)
	x := e.x
	e.aYearOfALaptop()
	e.aYearOfDocuments()
	grace := e.s.Deps.Retention.Wait()
	ws := app.TenantRef_builder{Id: e.tenant.Bytes()}.Build()

	before := e.rows()
	v, err := e.app().Tenant().Preview(e.owner, app.TenantPreviewRequest_builder{Ref: ws, KeepDays: z.Ptr(uint32(180))}.Build())
	x.NoError(err)
	x.Equal(before, e.rows(), "a preview took something")
	x.EqualValues(180, v.GetKeepDays())
	x.WithinDuration(e.now.Add(grace), v.GetKeepFrom().AsTime(), 0, "a shorter window, once the grace is over")
	x.EqualValues(grace/day, v.GetGraceDays())
	x.False(v.GetApply())
	x.Positive(v.GetKeepTakes()["events"], "an event only a row the window takes points at")

	// What a pass takes once the window is in force, then.
	e.keep(180)
	e.now = v.GetKeepFrom().AsTime()
	got := e.expire(true)
	x.Equal(map[string]uint32{
		"placements": uint32(got.Placements), "links": uint32(got.Links), "stewardships": uint32(got.Stewardships),
		"facts": uint32(got.Facts), "events": uint32(got.Events), "reservations": uint32(got.Reservations),
		"custodies": uint32(got.Custodies), "counts": uint32(got.Counts), "work_orders": uint32(got.WorkOrders),
		"stock_movements": uint32(got.StockMovements), "attachments": uint32(got.Attachments),
	}, withZeros(v.GetKeepTakes()), "the preview said other than a pass takes")

	// A window as long as the one held, or longer, is in force at once.
	v, err = e.app().Tenant().Preview(e.owner, app.TenantPreviewRequest_builder{Ref: ws, KeepDays: z.Ptr(uint32(365))}.Build())
	x.NoError(err)
	x.WithinDuration(e.now, v.GetKeepFrom().AsTime(), 0)

	_, err = e.app().Tenant().Preview(e.owner, app.TenantPreviewRequest_builder{Ref: ws, KeepDays: z.Ptr(uint32(100*365 + 1))}.Build())
	x.Equal(codes.InvalidArgument, codeOf(err), "what Configure would refuse")
}

// TestAPreviewHoldsNothing (D36): a preview is a read. Its owner asks it as
// often as they like, of however much history the workspace has, so it takes
// no row a write could be waiting for: with another transaction holding a row
// the window would take, it answers at once. PostgreSQL's row locks are what
// this is about; SQLite takes the whole database for a write.
func TestAPreviewHoldsNothing(t *testing.T) {
	e := newEnv(t)
	x := e.x
	if e.s.Dialect != "postgres" {
		t.Skip("a row lock is PostgreSQL's")
	}
	e.aYearOfALaptop()
	ctx := context.Background()

	gone, err := e.s.Ent.Placement.Query().Where(placement.TenantId(e.tenant.Uuid()), placement.ValidToNotNil(), placement.ValidToLT(e.now.Add(-300*day+time.Hour))).First(ctx)
	x.NoError(err)
	tx, err := e.s.Db.BeginTx(ctx, nil)
	x.NoError(err)
	defer tx.Rollback()
	_, err = tx.ExecContext(ctx, "UPDATE placement SET slot = slot WHERE id = $1", gone.Id.String())
	x.NoError(err)

	in, cancel := context.WithTimeout(e.owner, 5*time.Second)
	defer cancel()
	v, err := e.app().Tenant().Preview(in, app.TenantPreviewRequest_builder{
		Ref:      app.TenantRef_builder{Id: e.tenant.Bytes()}.Build(),
		KeepDays: z.Ptr(uint32(30)),
	}.Build())
	require.NoError(t, err, "a preview waited on a row another transaction holds")
	x.Positive(v.GetKeepTakes()["placements"], "the row held is one the window takes")
}

// withZeros is what a preview says it takes, with every kind it says nothing
// of at zero.
func withZeros(m map[string]uint32) map[string]uint32 {
	out := map[string]uint32{}
	for _, k := range []string{"placements", "links", "stewardships", "facts", "events", "reservations", "custodies", "counts", "work_orders", "stock_movements", "attachments"} {
		out[k] = m[k]
	}
	return out
}
