package apphost_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/lesomnus/rove/server/apphost"
)

// TestADomainIsReachedAsTheServiceIs: a workspace's domain is linked to by
// the service's own scheme, on its port unless that is the scheme's own; a
// page at the service's address is where its page is, and a label or a file
// where it answers.
func TestADomainIsReachedAsTheServiceIs(t *testing.T) {
	x := require.New(t)
	for public, want := range map[string]string{
		"https://rove.example":      "https://assets.acme.example",
		"https://rove.example:443/": "https://assets.acme.example",
		"https://rove.example:8443": "https://assets.acme.example:8443",
		"http://localhost:8080":     "http://assets.acme.example:8080",
		"http://localhost":          "http://assets.acme.example",
		"not an address":            "https://assets.acme.example",
	} {
		x.Equal(want, apphost.Links{Public: public}.On("assets.acme.example"), public)
	}

	l := apphost.Links{App: "https://app.rove.example/", Public: "https://rove.example/"}
	x.Equal("https://app.rove.example", l.Home(apphost.Page))
	x.Equal("https://rove.example", l.Home(apphost.Label))
	x.Equal("https://rove.example", l.Home(apphost.File))
	x.Equal("https://rove.example", apphost.Links{Public: "https://rove.example"}.Home(apphost.Page), "a page served where the service answers")
	x.True(l.Secure())
	x.False(apphost.Links{Public: "http://localhost:8080"}.Secure())
}
