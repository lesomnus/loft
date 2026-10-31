package landing_test

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/lesomnus/loft/server/landing"
)

// TestALandingIsAPathHere: what a link says to land on is a path on this
// host, or `/` -- whatever it spells another site with: a scheme, `//`, a
// backslash, a control character, or a path that `net/url` decodes and
// escapes back out as `//` (`%2F` beside a character it escapes). A path is
// kept as it was asked for, its query too.
func TestALandingIsAPathHere(t *testing.T) {
	for next, want := range map[string]string{
		"":                        "/",
		"/":                       "/",
		"/@acme/assets?q=rack":    "/@acme/assets?q=rack",
		"/l/7k2m9q":               "/l/7k2m9q",
		"/%2F%2Fevil.example":     "/%2F%2Fevil.example",
		"https://evil.example/":   "/",
		"//evil.example/x":        "/",
		"///evil.example/x":       "/",
		"/\\evil.example":         "/",
		"/%2Fevil.example/ x":     "/",
		"/%2Fevil.example/\"":     "/",
		"/%2F/evil.example/<":     "/",
		"/%5Cevil.example/ x":     "/%5Cevil.example/%20x",
		"evil":                    "/",
		"javascript:alert(1)":     "/",
		"/ok\r\nSet-Cookie: x=y":  "/",
		"/\t/evil.example":        "/",
		"/%09/evil.example":       "/%09/evil.example",
		"https://loft.example/@x": "/",
	} {
		got := landing.Path(next)
		require.Equal(t, want, got, next)

		// And where a browser sent there lands: this host.
		res := httptest.NewRecorder()
		http.Redirect(res, httptest.NewRequest(http.MethodGet, "/handoff/callback", nil), got, http.StatusSeeOther)
		ref, err := url.Parse(res.Header().Get("Location"))
		require.NoError(t, err, next)
		here, err := url.Parse("https://acme.example/handoff/callback")
		require.NoError(t, err)
		require.Equal(t, "acme.example", here.ResolveReference(ref).Host, next)
	}
}
