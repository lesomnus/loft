package cmd_test

import (
	"context"
	"testing"
	"time"

	app "github.com/lesomnus/rove"
	"github.com/lesomnus/rove/cmd"
	"github.com/lesomnus/rove/internal/opensearchtest"
	"github.com/lesomnus/rove/server/opensearch"
)

// TestWhatACallWroteIsFoundByTheNextSearch: no loop runs here, so the asset
// is in the index because the call that made it wrote it there before it
// answered -- which is what a page that saves and then searches, as
// receiving a purchase does, waits on (D31).
func TestWhatACallWroteIsFoundByTheNextSearch(t *testing.T) {
	w := newWorldWith(t, func(c *cmd.Config) { c.App.Search.Opensearch = opensearchtest.Config(t) })
	x := w.x
	ctx := context.Background()
	_, err := opensearch.Reindex(ctx, w.s.Search, w.s.Ent, time.Now(), nil)
	x.NoError(err)

	assets := app.NewAssetServiceClient(w.conn)
	_, err = assets.Add(w.owner, app.AssetAddRequest_builder{Name: "고성능GPU서버", Kind: "item", Tag: "SV-1"}.Build())
	x.NoError(err)

	v, err := assets.Search(w.owner, app.AssetSearchRequest_builder{Q: "서버를"}.Build())
	x.NoError(err)
	x.Len(v.GetItems(), 1)
	x.Equal("고성능GPU서버", v.GetItems()[0].GetName())
}
