package opensearch

import (
	"context"
	"encoding/json"
	"time"
)

// SetPage makes a reindex read and write n assets at a time, so that a test
// with fewer than a thousand still turns pages.
func SetPage(n int) func() {
	was := page
	page = n
	return func() { page = was }
}

// DocIn is the document index holds of an asset, and its version.
func DocIn(c *Client, index, id string) (*Doc, int64, error) {
	return docIn[Doc](c, index, id)
}

// PartyDocIn is the document index holds of a party, and its version.
func PartyDocIn(c *Client, index, id string) (*PartyDoc, int64, error) {
	return docIn[PartyDoc](c, index, id)
}

// ModelDocIn is the document index holds of a model, and its version.
func ModelDocIn(c *Client, index, id string) (*ModelDoc, int64, error) {
	return docIn[ModelDoc](c, index, id)
}

func docIn[T any](c *Client, index, id string) (*T, int64, error) {
	b, v, err := c.doc(context.Background(), index, id)
	if err != nil || b == nil {
		return nil, v, err
	}
	var d T
	if err := json.Unmarshal(b, &d); err != nil {
		return nil, v, err
	}
	return &d, v, nil
}

// Make makes an index of ix the way a reindex begins one, and answers its
// name.
func Make(c *Client, ix *Index, now time.Time) (string, error) {
	name := c.fresh(ix, now)
	return name, c.create(context.Background(), ix, name)
}

// MakeOld makes an index of ix of an older shape and points the alias at it:
// what a deployment has after a build that changed the index.
func MakeOld(c *Client, ix *Index, now time.Time) (string, error) {
	ctx := context.Background()
	name := c.fresh(ix, now)
	old := map[string]any{"mappings": map[string]any{"_meta": map[string]any{"version": ix.Version - 1}}}
	if err := c.call(ctx, "PUT", "/"+name, old, nil); err != nil {
		return "", err
	}
	return name, c.point(ctx, ix, name)
}
