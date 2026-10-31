package opensearch_test

import (
	"context"
	"testing"
	"time"

	"github.com/lesomnus/payday/pdtest"
	"github.com/stretchr/testify/require"

	"github.com/lesomnus/loft/cli"
	"github.com/lesomnus/loft/cmd"
	"github.com/lesomnus/loft/internal/ent/asset"
	"github.com/lesomnus/loft/internal/opensearchtest"
	"github.com/lesomnus/loft/server/opensearch"
)

// TestAReindexTurnsPagesInKeyOrder: every asset that is not erased is one
// document, however many pages it takes, and one that is erased is none.
func TestAReindexTurnsPagesInKeyOrder(t *testing.T) {
	x := require.New(t)
	ctx := context.Background()
	defer opensearch.SetPage(17)()

	c := cmd.Config{}
	c.Db.Driver, c.Db.Dsn = pdtest.DB(t)
	c.Watch.Broker = "memory"
	c.App.Files = t.TempDir()
	c.Auth.Roster.Db.Driver, c.Auth.Roster.Db.Dsn = pdtest.DB(t)
	c.App.Search.Opensearch = opensearchtest.Config(t)
	x.NoError(cli.NewCmdInit(&c).Run(ctx, []string{"--tenant", "acme", "--login", "owner", "--password", "owner1234", "--demo"}))

	s, err := cmd.Build(ctx, c)
	x.NoError(err)
	defer s.Close()
	gone, err := s.Ent.Asset.Query().Order(asset.ByTag()).First(ctx)
	x.NoError(err)
	x.NoError(s.Ent.Asset.UpdateOne(gone).SetDateErased(time.Now()).Exec(ctx))
	want, err := s.Ent.Asset.Query().Where(asset.DateErasedIsNil()).Count(ctx)
	x.NoError(err)
	x.Greater(want, 3*17, "enough to turn pages")

	o, err := opensearch.New(c.App.Search.Opensearch, plain)
	x.NoError(err)
	defer o.Close()
	pages := 0
	r, err := opensearch.Reindex(ctx, o, s.Ent, opensearch.Assets, time.Now(), func(int) { pages++ })
	x.NoError(err)
	x.Equal(want, r.Docs)
	x.Equal((want+16)/17, pages)

	st, err := o.Status(ctx, opensearch.Assets)
	x.NoError(err)
	x.Equal(r.Index, st.Index)
	x.EqualValues(want, st.Docs)
}
