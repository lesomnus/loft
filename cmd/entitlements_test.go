package cmd_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/lesomnus/payday/entitle"
	"github.com/lesomnus/payday/frame"
	"github.com/lesomnus/payday/pdid"
	"github.com/lesomnus/payday/pdpb"

	app "github.com/lesomnus/loft"
	"github.com/lesomnus/loft/internal/ent/entitlement"
	"github.com/lesomnus/loft/internal/levyantest"
	"github.com/lesomnus/loft/server/domain"
	"github.com/lesomnus/loft/server/pd"
)

// inside is a context of the deployment's own work in workspace `t`, which
// a read of the copy is asked in.
func inside(x *require.Assertions, t *app.Tenant) context.Context {
	id := tenantId(x, t)
	return frame.Into(context.Background(), frame.New(pdid.Nil, id, frame.Grant{}).WithScope(frame.Only(id)))
}

// grants is what the copy holds of `key` for `t` now, as a write reads it.
func (w *world) grants(t *app.Tenant, key string) entitle.Entitlement {
	v, err := entitle.Get(inside(w.x, t), pd.Entitlements(w.s.Drv), key)
	w.x.NoError(err)
	return v
}

// restricted is whether the copy restricts `t` now.
func (w *world) restricted(t *app.Tenant) bool {
	on, err := entitle.Has(inside(w.x, t), pd.Entitlements(w.s.Drv), domain.Restricted)
	w.x.NoError(err)
	return on
}

// follow runs the follower for the rest of the test, as `serve` does under its
// lease, and waits until it follows: the stream open, and the list compared.
func follow(t *testing.T, w *world, fake *levyantest.Levyan) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	lists := fake.Lists()
	go func() {
		defer close(done)
		w.s.Follower.Spin(ctx)
	}()
	t.Cleanup(func() {
		cancel()
		<-done
	})
	w.x.Eventually(func() bool { return fake.Watching() == 1 && fake.Lists() > lists }, 10*time.Second, 10*time.Millisecond, "the follower never followed")
}

// TestTheCopyFollowsLevyansGrants (design 7.5): a deployment that bills keeps
// what levyan grants each workspace in its own database, by payday's follower
// over the one connection to levyan, carrying the product key -- what levyan
// granted before it began, from the list; what it grants, ends and changes
// after, from the stream; never an older version of a grant over a newer;
// and what changed while the stream was cut, from the list it compares
// with once it is open again.
func TestTheCopyFollowsLevyansGrants(t *testing.T) {
	w, fake := billed(t)
	x := w.x

	x.NotNil(w.s.Follower)
	x.Contains(spun(w.s), entitle.FollowerName, "under the lease, by payday's name for it")

	beta, err := w.s.Base.Tenant().Add(context.Background(), app.TenantAddRequest_builder{Alias: "beta"}.Build())
	x.NoError(err)

	// Granted before the follower began: heard of from the list.
	early := fake.Restrict(tenantId(x, w.tenant))
	follow(t, w, fake)
	x.Eventually(func() bool { return w.restricted(w.tenant) }, 5*time.Second, 10*time.Millisecond, "what levyan granted before")
	x.False(w.restricted(beta), "and to that workspace alone")

	// Granted after: heard of from the stream.
	fake.Restrict(tenantId(x, beta))
	x.Eventually(func() bool { return w.restricted(beta) }, 5*time.Second, 10*time.Millisecond, "what levyan grants while the stream is open")

	// Ended: in force no longer.
	fake.End(early)
	x.Eventually(func() bool { return !w.restricted(w.tenant) }, 5*time.Second, 10*time.Millisecond, "a grant levyan ended")
	x.True(w.restricted(beta))

	// A limit, then an older version of it heard after the newer: the newer
	// stays.
	seats := fake.Grant(tenantId(x, w.tenant), "loft.seats", time.Now().Add(24*time.Hour))
	g := fake.Of(seats)
	g.SetVersion(3)
	g.SetLimit(10)
	fake.Put(g)
	x.Eventually(func() bool {
		v := w.grants(w.tenant, "loft.seats")
		n, ok := v.Value.Number()
		return ok && n == 10
	}, 5*time.Second, 10*time.Millisecond)
	old := proto.CloneOf(g)
	old.SetVersion(2)
	old.SetLimit(1)
	fake.Put(old)
	// And something after it, to know the old one was heard and passed over.
	fake.Restrict(tenantId(x, w.tenant))
	x.Eventually(func() bool { return w.restricted(w.tenant) }, 5*time.Second, 10*time.Millisecond)
	v := w.grants(w.tenant, "loft.seats")
	n, _ := v.Value.Number()
	x.EqualValues(10, n, "an older version of a grant is never written over a newer")
	x.EqualValues(3, v.Grant.Version)

	// The stream cut, and the grants changed meanwhile: put right from the
	// list once it is open again.
	lists := fake.Lists()
	fake.Cut()
	gone := fake.Of(seats)
	gone.SetVersion(4)
	gone.SetValidUntil(timestamppb.Now())
	fake.Quietly(gone)
	x.Eventually(func() bool { return fake.Watching() == 1 && fake.Lists() > lists }, 10*time.Second, 10*time.Millisecond, "the follower opened the stream again")
	x.Eventually(func() bool { return w.grants(w.tenant, "loft.seats").Value.IsNone() }, 5*time.Second, 10*time.Millisecond,
		"what changed while the stream was cut")

	// Every write of the copy is the deployment's, on the trail as the act
	// it is (payday#185), under the workspace.
	rows, err := w.s.Ent.Entitlement.Query().Where(entitlement.TenantId(tenantId(x, w.tenant).Uuid())).All(context.Background())
	x.NoError(err)
	x.NotEmpty(rows)
}

// TestAMissingGrantRestrictsNothing (design 7.5, levyan's design 13): what
// restricts a workspace is the grant of `loft.restricted`, on and in force,
// and nothing else -- not a copy that holds nothing, not a grant of it that is
// off or has ended, not a grant of another key. A gap in levyan never locks a
// workspace.
func TestAMissingGrantRestrictsNothing(t *testing.T) {
	w, fake := billed(t)
	x := w.x
	ctx := context.Background()
	assets := app.NewAssetServiceClient(w.conn)
	sync := func() {
		_, err := w.s.Follower.Sync(ctx, tenantId(x, w.tenant))
		x.NoError(err)
	}

	_, err := assets.Add(w.owner, app.AssetAddRequest_builder{Name: "노트북"}.Build())
	x.NoError(err, "a copy that holds nothing")

	off := pdpb.EntitlementGrant_builder{
		Id: []byte("restricted-off"), TenantId: tenantId(x, w.tenant).Bytes(), Key: domain.Restricted, Version: 1,
		On: proto.Bool(false), ValidUntil: timestamppb.New(time.Now().Add(time.Hour)),
	}.Build()
	fake.Put(off)
	fake.Grant(tenantId(x, w.tenant), "loft.export", time.Now().Add(time.Hour))
	ended := fake.Restrict(tenantId(x, w.tenant))
	fake.End(ended)
	sync()
	rows, err := w.s.Ent.Entitlement.Query().Where(entitlement.TenantId(tenantId(x, w.tenant).Uuid())).Count(ctx)
	x.NoError(err)
	x.Equal(3, rows, "all three are in the copy")
	x.False(w.restricted(w.tenant))

	_, err = assets.Add(w.owner, app.AssetAddRequest_builder{Name: "모니터"}.Build())
	x.NoError(err, "a grant of it that is off, one that ended, and one of another key")
}
