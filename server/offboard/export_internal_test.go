package offboard

import "testing"

// SetPage makes an export read n rows at a time until the test is over.
func SetPage(t *testing.T, n int) {
	was := page
	page = n
	t.Cleanup(func() { page = was })
}
