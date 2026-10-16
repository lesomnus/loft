package cmd_test

import (
	"testing"

	app "github.com/lesomnus/rove"
	"github.com/lesomnus/rove/cmd"
	"github.com/lesomnus/rove/internal/opensearchtest"
)

// TestWhatACallWroteIsFoundByTheNextSearch: no loop runs here, so the asset
// -- and the person -- is in the index because the call that made it wrote
// it there before it answered -- which is what a page that saves and then searches, as
// receiving a purchase does, waits on (D31).
func TestWhatACallWroteIsFoundByTheNextSearch(t *testing.T) {
	w := newWorldWith(t, func(c *cmd.Config) { c.App.Search.Opensearch = opensearchtest.Config(t) })
	x := w.x
	opensearchtest.Made(t, w.s.Search, w.s.Ent)

	assets := app.NewAssetServiceClient(w.conn)
	_, err := assets.Add(w.owner, app.AssetAddRequest_builder{Name: "고성능GPU서버", Kind: "item", Tag: "SV-1"}.Build())
	x.NoError(err)

	v, err := assets.Search(w.owner, app.AssetSearchRequest_builder{Q: "서버를"}.Build())
	x.NoError(err)
	x.Len(v.GetItems(), 1)
	x.Equal("고성능GPU서버", v.GetItems()[0].GetName())

	// A person, the same way: in the index of parties before the call
	// answered, and found by the given name alone.
	people := app.NewPartyServiceClient(w.conn)
	_, err = people.Add(w.owner, app.PartyAddRequest_builder{Name: "김민수", Kind: "person"}.Build())
	x.NoError(err)
	p, err := people.Search(w.owner, app.PartySearchRequest_builder{Q: "민수"}.Build())
	x.NoError(err)
	x.Len(p.GetItems(), 1)
	x.Equal("김민수", p.GetItems()[0].GetName())
}
