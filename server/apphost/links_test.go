package apphost_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/lesomnus/loft/server/apphost"
)

// TestADomainIsReachedAsTheServiceIs: a workspace's domain is linked to by
// the service's own scheme, on its port unless that is the scheme's own; a
// page at the service's address is where its page is, and a label or a file
// where it answers.
func TestADomainIsReachedAsTheServiceIs(t *testing.T) {
	x := require.New(t)
	for public, want := range map[string]string{
		"https://loft.example":      "https://assets.acme.example",
		"https://loft.example:443/": "https://assets.acme.example",
		"https://loft.example:8443": "https://assets.acme.example:8443",
		"http://localhost:8080":     "http://assets.acme.example:8080",
		"http://localhost":          "http://assets.acme.example",
		"not an address":            "https://assets.acme.example",
	} {
		x.Equal(want, apphost.Links{Public: public}.On("assets.acme.example"), public)
	}

	l := apphost.Links{App: "https://app.loft.example/", Public: "https://loft.example/"}
	x.Equal("https://app.loft.example", l.Home(apphost.Page))
	x.Equal("https://loft.example", l.Home(apphost.Label))
	x.Equal("https://loft.example", l.Home(apphost.File))
	x.Equal("https://loft.example", apphost.Links{Public: "https://loft.example"}.Home(apphost.Page), "a page served where the service answers")
	x.True(l.Secure())
	x.False(apphost.Links{Public: "http://localhost:8080"}.Secure())
}
