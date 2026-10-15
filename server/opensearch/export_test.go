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
