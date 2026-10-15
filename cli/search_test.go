package cli_test

import (
	"context"
	"testing"

	"github.com/lesomnus/payday/pdtest"
	"github.com/stretchr/testify/require"

	"github.com/lesomnus/rove/cli"
	"github.com/lesomnus/rove/cmd"
	"github.com/lesomnus/rove/internal/ent/asset"
	"github.com/lesomnus/rove/internal/opensearchtest"
	"github.com/lesomnus/rove/server/opensearch"
)

// TestASearchCommandSaysWhenNoClusterIsNamed: a deployment that names none
// searches its database, and has no index to tell anybody about.
func TestASearchCommandSaysWhenNoClusterIsNamed(t *testing.T) {
	c := cmd.Config{}
	for _, v := range [][]string{{"status"}, {"reindex"}, {"remove", "--yes"}, {"analyze", "웹서버"}} {
		err := cli.NewCmdSearch(&c).Run(context.Background(), v)
		require.ErrorContains(t, err, "ROVE_APP_SEARCH_OPENSEARCH_ADDRS", "%v", v)
	}
}

// TestAReindexMakesTheIndexAndPointsTheAliasAtIt: the demo's assets, every
// one of them a document, in an index that splits a Korean name into its
// words. A second reindex makes another index, moves the alias onto it and
// deletes the first.
func TestAReindexMakesTheIndexAndPointsTheAliasAtIt(t *testing.T) {
	x := require.New(t)
	ctx := context.Background()

	c := cmd.Config{}
	c.Db.Driver, c.Db.Dsn = pdtest.DB(t)
	c.Watch.Broker = "memory"
	c.App.Files = t.TempDir()
	c.Auth.Roster.Db.Driver, c.Auth.Roster.Db.Dsn = pdtest.DB(t)
	c.App.Search.Opensearch = opensearchtest.Config(t)
	run(t, cli.NewCmdInit(&c), "--tenant", "acme", "--login", "owner", "--password", "owner1234", "--demo")

	assets := 0
	on(t, c, func(s *cmd.Server) {
		n, err := s.Ent.Asset.Query().Where(asset.DateErasedIsNil()).Count(ctx)
		x.NoError(err)
		assets = n
	})
	x.Positive(assets)

	x.Contains(run(t, cli.NewCmdSearch(&c), "status"), "points at no index")

	out := run(t, cli.NewCmdSearch(&c), "reindex")
	x.Contains(out, "points at it")
	o, err := opensearch.New(c.App.Search.Opensearch, func(s string) (string, error) { return s, nil })
	x.NoError(err)
	defer o.Close()
	first, err := o.Status(ctx)
	x.NoError(err)
	x.NotEmpty(first.Index)
	x.Equal(opensearch.Version, first.Version)
	x.EqualValues(assets, first.Docs)
	x.Contains(run(t, cli.NewCmdSearch(&c), "status"), first.Index)

	words, err := o.Analyze(ctx, "ko", "웹서버")
	x.NoError(err)
	x.Contains(words, "서버", "a compound is kept with its words, so the word finds it")
	words, err = o.Analyze(ctx, "ko_search", "의자를")
	x.NoError(err)
	x.Equal([]string{"의자"}, words, "a particle is not asked for")
	x.Contains(run(t, cli.NewCmdSearch(&c), "analyze", "GPU서버"), "gpu · 서버")

	out = run(t, cli.NewCmdSearch(&c), "reindex")
	x.Contains(out, "deleted "+first.Index)
	second, err := o.Status(ctx)
	x.NoError(err)
	x.NotEqual(first.Index, second.Index)
	x.EqualValues(assets, second.Docs)
	x.Empty(second.Others, "the index it replaced is gone")

	x.Contains(run(t, cli.NewCmdSearch(&c), "remove"), "would delete "+second.Index)
	x.Contains(run(t, cli.NewCmdSearch(&c), "remove", "--yes"), "deleted "+second.Index)
	x.Contains(run(t, cli.NewCmdSearch(&c), "status"), "points at no index")
	x.Contains(run(t, cli.NewCmdSearch(&c), "remove"), "no index to delete")
}
