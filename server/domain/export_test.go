package domain

// SetFinishedPage makes the finished sweep read n ended reservations at a
// time until the test ends, so that a test can fill a page with a handful.
func SetFinishedPage(t interface{ Cleanup(func()) }, n int) {
	was := finishedPage
	finishedPage = n
	t.Cleanup(func() { finishedPage = was })
}
