package offboard_test

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"testing"
	"time"

	"github.com/lesomnus/payday/config"
	"github.com/lesomnus/payday/pdid"
	"github.com/lesomnus/payday/trail"
	"github.com/lesomnus/z"
	"google.golang.org/protobuf/types/known/timestamppb"

	app "github.com/lesomnus/rove"
	"github.com/lesomnus/rove/cmd"
	"github.com/lesomnus/rove/internal/ent"
	"github.com/lesomnus/rove/internal/ent/aliashold"
	"github.com/lesomnus/rove/internal/ent/audit"
	"github.com/lesomnus/rove/server/closing"
	"github.com/lesomnus/rove/server/offboard"
	"github.com/lesomnus/rove/server/pd"
)

// floorOnHolders is a deployment that keeps a floor on a kind of the app's
// own, the holder -- what plan.md M5 asks of a membership -- so that what a
// retirement keeps of a tenant's trail is about rows it took.
func floorOnHolders(c *cmd.Config) {
	c.Audit.By = map[string]config.AuditKeepConfig{"holder": {Min: config.AuditMinConfig{Destroy: 365 * 24 * time.Hour}}}
}

// row is a tenant's own row, as what destroys one is handed it.
func (e *env) row(t pdid.Id) *ent.Tenant {
	v, err := e.s.Ent.Tenant.Get(context.Background(), t.Uuid())
	e.x.NoError(err)
	return v
}

// TestARetirementKeepsWhatAFloorIsOn (payday#115): a tenant retired is taken
// out of the deployment as a purge takes it but for the trail a floor of the
// deployment's is still on, which stays until the floor has passed. What stays
// still says what became of the rows the retirement took -- each is on the
// trail as gone, and only of the kind the floor is on -- so a check of the
// rows finds nothing missing.
func TestARetirementKeepsWhatAFloorIsOn(t *testing.T) {
	archive := t.TempDir()
	e, _ := signed(t, floorOnHolders, func(c *cmd.Config) {
		c.Audit.Archive = archive
		c.Audit.Digest.Key = "base64:" + base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{7}, 32))
	})
	x := e.x
	ctx := context.Background()
	acme, other := e.tenant("acme"), e.tenant("other")
	d := e.s.Offboard(e.c)
	x.True(d.Floored())

	before, theirs := e.count(acme.id), e.count(other.id)
	plan, err := d.Retire(ctx, acme.id, true)
	x.NoError(err)
	x.True(plan.Retired)
	x.Equal(before, e.count(acme.id), "a dry run took something")
	x.NotEmpty(plan.Trail.Kept, "a retirement would keep nothing a floor is on")

	got, err := d.Retire(ctx, acme.id, false)
	x.NoError(err)
	x.Equal(plan.Rows, got.Rows, "the retirement took other than the dry run said")
	x.NotEmpty(got.Trail.Kept)
	x.False(got.Trail.Until.IsZero(), "kept, and until nobody knows when")
	x.False(e.file(acme.photo.GetObjectKey()), "acme's photo")
	x.Equal(theirs, e.count(other.id), "something of another tenant went")

	// Of the tenant, only what its trail keeps is left.
	left := e.count(acme.id)
	x.Positive(left["audit"], "the trail kept nothing of it")
	delete(left, "audit")
	x.Empty(left, "something of acme is left beside its trail")

	// And that is of the kind the floor is on, the holder it took among it,
	// as gone.
	vs, err := e.s.Ent.Audit.Query().Where(audit.TenantId(acme.id.Uuid())).All(ctx)
	x.NoError(err)
	gone := 0
	for _, v := range vs {
		x.Equal(uint32(pd.HolderDomain), v.Domain, "a line no floor is on was kept: %s", v.Action)
		if v.Action == offboard.Retired {
			gone++
		}
	}
	x.Equal(1, gone, "the holder it took is not on the trail as gone")

	cs, err := e.c.Audit.Contents()
	x.NoError(err)
	rows, err := pd.Digests(ctx, e.s.Ent, cs.Digest, e.s.Trail)
	x.NoError(err)
	x.Empty(rows.Missing, "a row the retirement took is missing to the trail")
	x.True(rows.Ok(), "%+v", rows)
}

// TestARetirementWithFloorsAndNoArchiveIsRefused: a deployment that keeps
// floors and no archive has nowhere to keep what a retirement keeps, nor the
// record that the tenant left, so it is refused -- as itself, before anything
// is taken. An operator's purge still takes it, what the floor is on with it.
func TestARetirementWithFloorsAndNoArchiveIsRefused(t *testing.T) {
	e := newEnv(t, floorOnHolders)
	x := e.x
	ctx := context.Background()
	acme := e.tenant("acme")
	d := e.s.Offboard(e.c)

	before := e.count(acme.id)
	for _, dry := range []bool{true, false} {
		_, err := d.Retire(ctx, acme.id, dry)
		x.ErrorIs(err, offboard.ErrNoArchive)
		_, err = d.Leave(ctx, acme.id, dry)
		x.ErrorIs(err, offboard.ErrNoArchive, "a contract that says nothing retires")
	}
	x.Equal(before, e.count(acme.id), "a refused retirement took something")

	_, err := d.Purge(ctx, acme.id, false)
	x.NoError(err)
	x.Empty(e.count(acme.id))
}

// TestAWorkspaceLeavesAsItsContractSays: a retirement, unless the contract in
// force when it leaves says to purge (`purge_floors`).
func TestAWorkspaceLeavesAsItsContractSays(t *testing.T) {
	e := newEnv(t)
	x := e.x
	ctx := context.Background()
	acme := e.tenant("acme")
	d := e.s.Offboard(e.c)

	plan, err := d.Leave(ctx, acme.id, true)
	x.NoError(err)
	x.True(plan.Retired, "a contract that says nothing purges")

	_, err = e.s.Base.TenantContract().Add(ctx, app.TenantContractAddRequest_builder{
		Tenant:        app.TenantRef_builder{Id: acme.id.Bytes()}.Build(),
		Name:          "abroad",
		PurgeFloors:   z.Ptr(true),
		DateEffective: timestamppb.New(time.Now().Add(-time.Minute)),
	}.Build())
	x.NoError(err)
	plan, err = d.Leave(ctx, acme.id, true)
	x.NoError(err)
	x.False(plan.Retired, "a contract that purges retires")
}

// TestAWorkspaceThatRetiredIsRestoredUnderItsIdentifier: a workspace that
// retired from a deployment with floors is a tenant that left to the trail
// there, which would take what it wrote back under the same identifier at the
// next pass. So a restore of it there has the trail take it back first
// (`trail.Policy.Return`, payday#141) -- and what it writes afterwards is
// kept, and archived when its window says, as any workspace's is. A dry run
// takes nothing back. A purge leaves the trail no such record, and a purged
// workspace comes back as it always has. Each destruction holds the address,
// saying which it was.
func TestAWorkspaceThatRetiredIsRestoredUnderItsIdentifier(t *testing.T) {
	archive := t.TempDir()
	e, pub := signed(t, floorOnHolders, func(c *cmd.Config) {
		c.Audit.Archive = archive
		c.Audit.Retain = time.Nanosecond
	})
	x := e.x
	ctx := context.Background()
	store := pd.TrailStore(e.s.Ent)
	acme, again := e.tenant("acme"), e.tenant("again")
	reason := func(t pdid.Id) string {
		v, err := e.s.Ent.AliasHold.Query().Where(aliashold.Tenant(t.Uuid())).Only(ctx)
		x.NoError(err)
		return v.Reason
	}
	retired := func(t pdid.Id) bool {
		vs, err := e.s.Trail.Retired(ctx, store)
		x.NoError(err)
		_, ok := vs[t]
		return ok
	}

	b := e.export(acme.id)
	_, err := e.s.Closing.Destroy(ctx, e.row(acme.id), closing.Retire, false)
	x.NoError(err)
	x.Equal(offboard.ReasonRetired, reason(acme.id))
	x.True(retired(acme.id), "the trail has no record that acme left")

	got, err := e.restore(b, offboard.ImportOptions{Dry: true})
	x.NoError(err)
	x.True(got.Retired)
	x.Zero(got.Returned)
	x.True(retired(acme.id), "a dry run took acme back")
	x.Empty(e.count(acme.id)["tenant"], "a dry run put something back")

	got, err = e.restore(b, offboard.ImportOptions{})
	x.NoError(err)
	x.True(got.Retired)
	x.Positive(got.Returned, "the trail took nothing back")
	x.False(retired(acme.id), "the trail still has acme as a tenant that left")

	// What it writes now is a workspace's like any other: in the archive
	// after the next pass, rather than gone with a tenant that left.
	party, err := e.s.Base.Party().Add(ctx, app.PartyAddRequest_builder{
		Tenant: app.TenantRef_builder{Id: acme.id.Bytes()}.Build(),
		Name:   "Back again",
		Kind:   "person",
	}.Build())
	x.NoError(err)
	pid, err := pdid.From(party.GetId())
	x.NoError(err)
	e.s.Trail.Pass(ctx, store)
	n, err := e.s.Ent.Audit.Query().Where(audit.ObjectId(pid.Uuid())).Count(ctx)
	x.NoError(err)
	x.Zero(n, "the pass left the new row's trail in the database")
	kept := 0
	x.NoError(trail.ReadTenant(ctx, e.s.Trail.Archive, acme.id, func(doc []byte) error {
		var v struct {
			ObjectId []byte `json:"objectId"`
		}
		if err := json.Unmarshal(doc, &v); err != nil {
			return err
		}
		if bytes.Equal(v.ObjectId, party.GetId()) {
			kept++
		}
		return nil
	}))
	x.Positive(kept, "what the restored workspace wrote is in neither the database nor the archive")

	elsewhere := newEnv(t, func(c *cmd.Config) { c.Audit.Checkpoints.Trust = []string{pub} })
	got, err = elsewhere.restore(b, offboard.ImportOptions{})
	x.NoError(err, "refused where it never retired")
	x.False(got.Retired)

	b = e.export(again.id)
	_, err = e.s.Closing.Destroy(ctx, e.row(again.id), closing.Purge, false)
	x.NoError(err)
	x.Equal(offboard.ReasonDestroyed, reason(again.id))
	got, err = e.restore(b, offboard.ImportOptions{})
	x.NoError(err, "a purged workspace is not restored where it was purged")
	x.False(got.Retired)
}
