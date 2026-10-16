package opensearch_test

import (
	"context"
	"net"
	"testing"
	"time"

	"github.com/lesomnus/payday/pdid"
	"github.com/lesomnus/payday/pdtest"
	"github.com/lesomnus/z"
	"github.com/stretchr/testify/require"

	app "github.com/lesomnus/rove"
	"github.com/lesomnus/rove/cli"
	"github.com/lesomnus/rove/cmd"
	"github.com/lesomnus/rove/internal/ent"
	"github.com/lesomnus/rove/internal/ent/asset"
	"github.com/lesomnus/rove/internal/opensearchtest"
	"github.com/lesomnus/rove/server/opensearch"
)

// demo is a deployment with the demo's workspace in it, searching on the
// cluster PDTEST_OPENSEARCH names when search is; and its server.
func demo(t *testing.T, search bool) (cmd.Config, *cmd.Server) {
	t.Helper()
	ctx := context.Background()
	c := cmd.Config{}
	c.Db.Driver, c.Db.Dsn = pdtest.DB(t)
	c.Watch.Broker = "memory"
	c.App.Files = t.TempDir()
	c.Auth.Roster.Db.Driver, c.Auth.Roster.Db.Dsn = pdtest.DB(t)
	if search {
		c.App.Search.Opensearch = opensearchtest.Config(t)
	}
	require.NoError(t, cli.NewCmdInit(&c).Run(ctx, []string{"--tenant", "acme", "--login", "owner", "--password", "owner1234", "--demo"}))

	s, err := cmd.Build(ctx, c)
	require.NoError(t, err)
	t.Cleanup(func() { s.Close() })
	return c, s
}

// indexed is s with its index made, and the queue the demo left -- every
// write of it written down -- worked off.
func indexed(t *testing.T, s *cmd.Server) opensearch.Reindexed {
	t.Helper()
	ctx := context.Background()
	x := require.New(t)
	x.Positive(queued(t, s), "the demo was written down as it was made")
	r, err := opensearch.Reindex(ctx, s.Search, s.Ent, time.Now(), nil)
	x.NoError(err)
	for {
		n, err := opensearch.Indexer{Client: s.Search, Db: s.Ent}.Pass(ctx)
		x.NoError(err)
		if n == 0 {
			return r
		}
	}
}

func queued(t *testing.T, s *cmd.Server) int {
	t.Helper()
	n, err := s.Ent.SearchStale.Query().Count(context.Background())
	require.NoError(t, err)
	return n
}

// TestNothingIsWrittenDownWithoutACluster: a deployment that searches its
// database has no index to keep current, and no queue growing for one.
func TestNothingIsWrittenDownWithoutACluster(t *testing.T) {
	ctx := context.Background()
	_, s := demo(t, false)
	a, err := s.Ent.Asset.Query().First(ctx)
	require.NoError(t, err)
	_, err = s.Base.Asset().Patch(ctx, app.AssetPatchRequest_builder{
		Ref: app.AssetRef_builder{Id: pdid.Id(a.Id).Bytes()}.Build(), Name: z.Ptr("새 이름"), DateUpdatedForce: z.Ptr(true),
	}.Build())
	require.NoError(t, err)
	require.Zero(t, queued(t, s))
}

// TestTheIndexHearsOfWhatChanged: an asset renamed, the model it is of
// renamed, the person who has it forgotten, the asset erased, and the
// workspace taken out of the deployment -- each of them in the index after a
// pass, and the queue empty again.
func TestTheIndexHearsOfWhatChanged(t *testing.T) {
	x := require.New(t)
	ctx := context.Background()
	c, s := demo(t, true)
	indexed(t, s)
	idx := s.Search.Alias()
	pass := func() {
		t.Helper()
		_, err := opensearch.Indexer{Client: s.Search, Db: s.Ent}.Pass(ctx)
		x.NoError(err)
		x.Zero(queued(t, s), "a pass takes out what it made again")
	}
	docOf := func(a *ent.Asset) *opensearch.Doc {
		t.Helper()
		d, _, err := opensearch.DocIn(s.Search, idx, a.Id.String())
		x.NoError(err)
		return d
	}

	a, err := s.Ent.Asset.Query().Where(asset.HasModel(), asset.HasCustodian(), asset.DateErasedIsNil()).
		WithModel().WithCustodian().First(ctx)
	x.NoError(err)
	ref := app.AssetRef_builder{Id: pdid.Id(a.Id).Bytes()}.Build()

	// The asset itself.
	_, err = s.Base.Asset().Patch(ctx, app.AssetPatchRequest_builder{Ref: ref, Name: z.Ptr("고성능GPU서버"), DateUpdatedForce: z.Ptr(true)}.Build())
	x.NoError(err)
	x.Equal(1, queued(t, s), "written down with the write")
	pass()
	x.Equal("고성능gpu서버", docOf(a).Name)

	// What its document takes a name from: every asset of the model.
	same, err := s.Ent.Asset.Query().Where(asset.ModelId(a.ModelId), asset.DateErasedIsNil()).All(ctx)
	x.NoError(err)
	_, err = s.Base.ItemModel().Patch(ctx, app.ItemModelPatchRequest_builder{
		Ref: app.ItemModelRef_builder{Id: pdid.Id(a.ModelId).Bytes()}.Build(), Name: z.Ptr("PowerEdge R770"), DateUpdatedForce: z.Ptr(true),
	}.Build())
	x.NoError(err)
	pass()
	for _, v := range same {
		x.Equal("poweredge r770", docOf(v).Model, "%s takes its model's name", v.Name)
	}

	// The person who has it, forgotten as 개인정보 삭제 forgets them: the
	// name they had is not in the index any more.
	was := docOf(a).Custodian
	x.NotEmpty(was)
	_, err = s.Base.Party().Patch(ctx, app.PartyPatchRequest_builder{
		Ref: app.PartyRef_builder{Id: pdid.Id(a.CustodianId).Bytes()}.Build(), Name: z.Ptr("익명 1a2b3c4d"), DateUpdatedForce: z.Ptr(true),
	}.Build())
	x.NoError(err)
	pass()
	x.Equal("익명 1a2b3c4d", docOf(a).Custodian)

	// A write that has nothing to do with a document writes nothing down.
	h, err := s.Ent.Holder.Query().First(ctx)
	x.NoError(err)
	_, err = s.Base.Holder().Patch(ctx, app.HolderPatchRequest_builder{
		Ref: app.HolderRef_builder{Id: pdid.Id(h.Id).Bytes()}.Build(), DateUpdatedForce: z.Ptr(true),
	}.Build())
	x.NoError(err)
	x.Zero(queued(t, s))

	// Erased: no document.
	_, err = s.Base.Asset().Erase(ctx, ref)
	x.NoError(err)
	pass()
	x.Nil(docOf(a))

	// The workspace, taken out of the deployment: none of its documents.
	_, err = s.Offboard(c).Purge(ctx, pdid.Id(a.TenantId), false)
	x.NoError(err)
	x.Equal(1, queued(t, s), "the purge says so itself, since its rows go past the recorder")
	pass()
	st, err := s.Search.Status(ctx)
	x.NoError(err)
	x.Zero(st.Docs)
}

// TestWhatAPassCannotWriteWaitsInTheQueue for the next pass, and a cluster
// that answers again.
func TestWhatAPassCannotWriteWaitsInTheQueue(t *testing.T) {
	x := require.New(t)
	ctx := context.Background()
	c, s := demo(t, true)
	indexed(t, s)

	l, err := net.Listen("tcp", "127.0.0.1:0")
	x.NoError(err)
	down := c.App.Search.Opensearch
	down.Addrs = []string{"http://" + l.Addr().String()}
	x.NoError(l.Close())
	nobody, err := opensearch.New(down, plain)
	x.NoError(err)

	a, err := s.Ent.Asset.Query().First(ctx)
	x.NoError(err)
	_, err = s.Base.Asset().Patch(ctx, app.AssetPatchRequest_builder{
		Ref: app.AssetRef_builder{Id: pdid.Id(a.Id).Bytes()}.Build(), Name: z.Ptr("웹서버"), DateUpdatedForce: z.Ptr(true),
	}.Build())
	x.NoError(err)

	_, err = opensearch.Indexer{Client: nobody, Db: s.Ent}.Pass(ctx)
	x.Error(err)
	x.Equal(1, queued(t, s))

	n, err := opensearch.Indexer{Client: s.Search, Db: s.Ent}.Pass(ctx)
	x.NoError(err)
	x.Equal(1, n)
	d, _, err := opensearch.DocIn(s.Search, s.Search.Alias(), a.Id.String())
	x.NoError(err)
	x.Equal("웹서버", d.Name)
}

// TestAPassWritesToTheIndexAReindexIsFilling as well as to the one searched,
// so that what changed while it filled is not lost when the alias moves.
func TestAPassWritesToTheIndexAReindexIsFilling(t *testing.T) {
	x := require.New(t)
	ctx := context.Background()
	_, s := demo(t, true)
	r := indexed(t, s)
	filling, err := opensearch.Make(s.Search, time.Now().Add(time.Second))
	x.NoError(err)

	a, err := s.Ent.Asset.Query().First(ctx)
	x.NoError(err)
	_, err = s.Base.Asset().Patch(ctx, app.AssetPatchRequest_builder{
		Ref: app.AssetRef_builder{Id: pdid.Id(a.Id).Bytes()}.Build(), Name: z.Ptr("DB서버"), DateUpdatedForce: z.Ptr(true),
	}.Build())
	x.NoError(err)
	_, err = opensearch.Indexer{Client: s.Search, Db: s.Ent}.Pass(ctx)
	x.NoError(err)

	for _, idx := range []string{r.Index, filling} {
		d, v, err := opensearch.DocIn(s.Search, idx, a.Id.String())
		x.NoError(err)
		x.NotNil(d, idx)
		x.Equal("db서버", d.Name, idx)
		x.Positive(v)
	}
}

// TestTheLoopMakesTheIndexWhenThereIsNone, or when what there is is of a
// shape this build does not make, and searches from it once it is whole.
func TestTheLoopMakesTheIndexWhenThereIsNone(t *testing.T) {
	x := require.New(t)
	_, s := demo(t, true)
	want, err := s.Ent.Asset.Query().Where(asset.DateErasedIsNil()).Count(context.Background())
	x.NoError(err)

	made := func() opensearch.State {
		t.Helper()
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan error)
		go func() {
			done <- (opensearch.Indexer{Client: s.Search, Db: s.Ent, Every: 10 * time.Millisecond}).Spin(ctx)
		}()
		defer func() {
			cancel()
			x.NoError(<-done)
		}()
		var st opensearch.State
		x.Eventually(func() bool {
			st, err = s.Search.Status(context.Background())
			x.NoError(err)
			// Whole, and what it replaced deleted: the alias moves before
			// the old index goes.
			return st.Index != "" && st.Version == opensearch.Version && st.Docs == int64(want) && len(st.Others) == 0
		}, 20*time.Second, 50*time.Millisecond)
		return st
	}

	first := made()

	old, err := opensearch.MakeOld(s.Search, time.Now().Add(time.Hour))
	x.NoError(err)
	second := made()
	x.NotEqual(old, second.Index, "an index of another shape is made again")
	x.NotEqual(first.Index, second.Index)
}
