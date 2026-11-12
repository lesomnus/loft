package domain

import "strings"

// Links is where every link this deployment makes begins: its own address
// (design 9.9, D82), and the one place that says so -- nothing else puts a
// host in an address. A label printed, a file's signed address and the link
// a mail carries are all at the service's address.
type Links struct {
	// App is where the page is -- `app.app_url`, or `app.public_url` for a
	// deployment that serves the built page itself -- and Public where this
	// deployment answers (`app.public_url`).
	App    string
	Public string
}

// Page is where a link to a page of the app begins: a scheme and a host, with
// no slash after.
func (l Links) Page() string {
	if l.App != "" {
		return strings.TrimSuffix(l.App, "/")
	}
	return strings.TrimSuffix(l.Public, "/")
}

// Served is where a link to what this deployment answers by itself begins --
// a label's `/l/<code>`, a file's signed `/files/...`: a scheme and a host,
// with no slash after.
func (l Links) Served() string { return strings.TrimSuffix(l.Public, "/") }
