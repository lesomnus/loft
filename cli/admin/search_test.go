package admincli_test

import (
	"context"
	"testing"
	"time"

	"github.com/lesomnus/payday/frame"
	"github.com/lesomnus/payday/pdid"
	"github.com/stretchr/testify/require"

	app "github.com/lesomnus/loft"
	"github.com/lesomnus/loft/cmd"
	"github.com/lesomnus/loft/internal/ent/asset"
	"github.com/lesomnus/loft/internal/ent/event"
	"github.com/lesomnus/loft/internal/ent/holder"
	"github.com/lesomnus/loft/internal/ent/placement"
	enttenant "github.com/lesomnus/loft/internal/ent/tenant"
	"github.com/lesomnus/loft/internal/opensearchtest"
	"github.com/lesomnus/loft/server/opensearch"
	"github.com/lesomnus/loft/server/operator"
)

// TestASearchCommandSaysWhenNoClusterIsNamed: a deployment that names none
// searches its database, and has no index to tell anybody about.
func TestASearchCommandSaysWhenNoClusterIsNamed(t *testing.T) {
	d := deploy(t)
	for _, v := range [][]string{{"status"}, {"reindex"}, {"remove", "--yes"}, {"analyze", "웹서버"}} {
		_, err := d.admin(t, append([]string{"search"}, v...)...)
		require.ErrorContains(t, err, "LOFT_APP_SEARCH_OPENSEARCH_ADDRS", "%v", v)
	}
}

// TestAReindexMakesTheIndexAndPointsTheAliasAtIt: the assets, every one of
// them a document, in an index that splits a Korean name into its words. A
// second reindex makes another index, moves the alias onto it and deletes
// the first; a removal says what it would delete before it does.
func TestAReindexMakesTheIndexAndPointsTheAliasAtIt(t *testing.T) {
	x := require.New(t)
	ctx := context.Background()
	d := deployWith(t, func(c *cmd.Config) { c.App.Search.Opensearch = opensearchtest.Config(t) })
	o := d.s.Search
	x.NotNil(o)
	acme, err := d.s.Ent.Tenant.Query().Where(enttenant.Alias("acme")).Only(ctx)
	x.NoError(err)
	as := ownerOf(t, d, acme.Id)
	_, err = d.s.Ungated.Asset().Add(as, app.AssetAddRequest_builder{Name: "고성능GPU서버", Tag: "SV-1"}.Build())
	x.NoError(err)
	assets, err := d.s.Ent.Asset.Query().Where(asset.DateErasedIsNil()).Count(ctx)
	x.NoError(err)

	out, err := d.admin(t, "search", "status")
	x.NoError(err)
	x.Contains(out, "points at no index")

	out, err = d.admin(t, "search", "reindex")
	x.NoError(err, out)
	x.Contains(out, "points at it")
	for _, ix := range opensearch.Indexes {
		x.Contains(out, " "+ix.Name+", and ", "every index, made: %s", out)
	}
	first, err := o.Status(ctx, opensearch.Assets)
	x.NoError(err)
	x.NotEmpty(first.Index)
	x.EqualValues(assets, first.Docs)
	out, err = d.admin(t, "search", "status")
	x.NoError(err)
	x.Contains(out, first.Index)

	out, err = d.admin(t, "search", "analyze", "GPU서버")
	x.NoError(err)
	x.Contains(out, "gpu · 서버")

	out, err = d.admin(t, "search", "reindex")
	x.NoError(err)
	x.Contains(out, "deleted "+first.Index)
	second, err := o.Status(ctx, opensearch.Assets)
	x.NoError(err)
	x.NotEqual(first.Index, second.Index)

	out, err = d.admin(t, "search", "remove")
	x.NoError(err)
	x.Contains(out, "would delete "+second.Index)
	still, err := o.Status(ctx, opensearch.Assets)
	x.NoError(err)
	x.Equal(second.Index, still.Index, "a removal asked about deleted something")
	out, err = d.admin(t, "search", "remove", "--yes")
	x.NoError(err)
	x.Contains(out, "deleted "+second.Index)
	out, err = d.admin(t, "search", "status")
	x.NoError(err)
	x.Contains(out, "points at no index")
}

// ownerOf is a context framed as acme's owner, for what a test writes
// through the domain layer as a member would.
func ownerOf(t *testing.T, d *deployment, tenant [16]byte) context.Context {
	t.Helper()
	ctx := context.Background()
	h, err := d.s.Ent.Holder.Query().Where(holder.TenantId(tenant), holder.Role("owner")).Only(ctx)
	require.NoError(t, err)
	row, err := d.s.Base.Holder().Get(ctx, app.HolderGetRequest_builder{Ref: app.HolderRef_builder{Id: h.Id[:]}.Build()}.Build())
	require.NoError(t, err)
	f := frame.New(pdid.Id(h.Id), pdid.Id(tenant), frame.Whole()).WithScope(frame.Only(pdid.Id(tenant))).WithRow(row)
	return frame.Into(ctx, f)
}

// TestTheWindowsTakeWhatTheyNoLongerReachAtTheOperatorsWord: a plan says
// what a pass would take of a workspace that keeps 180 days of its history,
// and takes none of it; a run is refused while the deployment destroys
// nothing by its windows, and where it does, takes what the plan said, its
// receipt the operator's.
func TestTheWindowsTakeWhatTheyNoLongerReachAtTheOperatorsWord(t *testing.T) {
	x := require.New(t)
	ctx := context.Background()
	day := 24 * time.Hour

	// A year of a laptop's history, the first of it past a 180-day window.
	history := func(d *deployment) *app.Asset {
		acme, err := d.s.Ent.Tenant.Query().Where(enttenant.Alias("acme")).Only(ctx)
		x.NoError(err)
		as := ownerOf(t, d, acme.Id)
		now := time.Now()
		t.Cleanup(func() { d.s.Deps.Now = nil })
		at := func(v time.Duration) { d.s.Deps.Now = func() time.Time { return now.Add(v) } }
		at(-400 * day)
		a, err := d.s.Ungated.Asset().Add(as, app.AssetAddRequest_builder{Name: "A", Kind: "space"}.Build())
		x.NoError(err)
		b, err := d.s.Ungated.Asset().Add(as, app.AssetAddRequest_builder{Name: "B", Kind: "space"}.Build())
		x.NoError(err)
		laptop, err := d.s.Ungated.Asset().Add(as, app.AssetAddRequest_builder{
			Name: "노트북", Tag: "NB-1", Kind: "item", To: app.AssetRef_builder{Id: a.GetId()}.Build(),
		}.Build())
		x.NoError(err)
		at(-300 * day)
		_, err = d.s.Ungated.Asset().Move(as, app.AssetMoveRequest_builder{
			Ref: app.AssetRef_builder{Id: laptop.GetId()}.Build(), To: app.AssetRef_builder{Id: b.GetId()}.Build(),
		}.Build())
		x.NoError(err)
		at(-100 * day)
		_, err = d.s.Ungated.Asset().Move(as, app.AssetMoveRequest_builder{
			Ref: app.AssetRef_builder{Id: laptop.GetId()}.Build(), To: app.AssetRef_builder{Id: a.GetId()}.Build(),
		}.Build())
		x.NoError(err)
		d.s.Deps.Now = nil
		_, err = d.s.Ent.Tenant.UpdateOneId(acme.Id).SetKeepDays(180).Save(ctx)
		x.NoError(err)
		return laptop
	}
	placements := func(d *deployment) int {
		n, err := d.s.Ent.Placement.Query().Count(ctx)
		x.NoError(err)
		return n
	}

	// Off: planned, and refused to run.
	d := deploy(t)
	out, err := d.admin(t, "retention", "plan")
	x.NoError(err, out)
	x.Contains(out, "acme: kept forever")
	x.Contains(out, "app.retention.apply is off")
	history(d)
	before := placements(d)
	out, err = d.admin(t, "retention", "plan", "--tenant", "acme")
	x.NoError(err, out)
	x.Contains(out, "acme: what was over before ")
	x.Contains(out, "history would take ")
	x.NotContains(out, "would take 0:", "a plan of a year's history took nothing")
	x.Equal(before, placements(d), "a plan took something")
	_, err = d.admin(t, "retention", "run", "--tenant", "acme")
	x.ErrorContains(err, "app.retention.apply is off")
	x.Equal(before, placements(d))

	// On: taken, the receipt the operator's.
	d = deployWith(t, func(c *cmd.Config) { c.App.Retention.Apply = true })
	laptop := history(d)
	before = placements(d)
	out, err = d.admin(t, "retention", "run", "--tenant", "@acme")
	x.NoError(err, out)
	x.Contains(out, "acme: what was over before ")
	x.Contains(out, "history took ")
	x.Less(placements(d), before)
	n, err := d.s.Ent.Placement.Query().Where(placement.ChildId([16]byte(laptop.GetId()))).Count(ctx)
	x.NoError(err)
	x.Positive(n, "what the window still reaches went")
	r, err := d.s.Ent.Event.Query().Where(event.Kind("retention.expired")).Only(ctx)
	x.NoError(err)
	x.NotNil(r.ActorId)
	x.Equal(operator.Id("alice").Uuid(), *r.ActorId, "the receipt is the operator's")
}
