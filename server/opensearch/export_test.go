package opensearch

// SetPage makes a reindex read and write n assets at a time, so that a test
// with fewer than a thousand still turns pages.
func SetPage(n int) func() {
	was := page
	page = n
	return func() { page = was }
}
