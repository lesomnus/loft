package opensearch

import (
	"context"
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
	return c.doc(context.Background(), index, id)
}

// Make makes an index of assets the way a reindex begins one, and answers
// its name.
func Make(c *Client, now time.Time) (string, error) {
	name := c.fresh(now)
	return name, c.create(context.Background(), name)
}

// MakeOld makes an index of an older shape and points the alias at it: what a
// deployment has after a build that changed the index.
func MakeOld(c *Client, now time.Time) (string, error) {
	ctx := context.Background()
	name := c.fresh(now)
	old := map[string]any{"mappings": map[string]any{"_meta": map[string]any{"version": Version - 1}}}
	if err := c.call(ctx, "PUT", "/"+name, old, nil); err != nil {
		return "", err
	}
	return name, c.point(ctx, name)
}
