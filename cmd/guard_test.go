package cmd

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/lesomnus/rove/server/csp"
)

// TestEveryAnswerSaysHowItMayBeUsed: whatever answered, an answer is not
// sniffed, not framed, tells no other site where it came from, and is asked
// for over HTTPS only once a deployment is behind it -- except that an
// attachment's framing is its own to say.
func TestEveryAnswerSaysHowItMayBeUsed(t *testing.T) {
	x := require.New(t)
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/files/a" {
			w.Header().Set("Content-Security-Policy", "sandbox")
		}
		w.WriteHeader(http.StatusTeapot)
	})
	policy := csp.Policy("https://id.example.com/oauth2")
	ask := func(https bool, mode csp.Mode, path string) http.Header {
		rec := httptest.NewRecorder()
		guarded(h, https, mode, policy).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		x.Equal(http.StatusTeapot, rec.Code, "what answered still answers")
		return rec.Header()
	}

	for _, path := range []string{"/", "/@acme/assets", "/rove.AssetService/Search", "/account"} {
		v := ask(false, csp.ReportOnly, path)
		x.Equal("nosniff", v.Get("X-Content-Type-Options"), path)
		x.Equal("same-origin", v.Get("Referrer-Policy"), path)
		x.Equal("DENY", v.Get("X-Frame-Options"), path)
		x.Equal("frame-ancestors 'none'", v.Get("Content-Security-Policy"), path)
		x.Equal(policy, v.Get("Content-Security-Policy-Report-Only"), path)
		x.Equal(`csp="/csp-report"`, v.Get("Reporting-Endpoints"), path)
		x.Empty(v.Get("Strict-Transport-Security"), "a deployment on plain HTTP cannot promise HTTPS")
	}
	x.Equal("max-age=31536000", ask(true, csp.ReportOnly, "/").Get("Strict-Transport-Security"))

	v := ask(false, csp.ReportOnly, "/files/a")
	x.Equal("nosniff", v.Get("X-Content-Type-Options"))
	x.Empty(v.Get("X-Frame-Options"), "an attachment says its own")
	x.Equal("sandbox", v.Get("Content-Security-Policy"))
	x.Empty(v.Get("Content-Security-Policy-Report-Only"), "an attachment says its own")

	t.Run("and the policy is enforced, or said not at all, as app.csp says", func(t *testing.T) {
		x := require.New(t)

		v := ask(false, csp.Enforce, "/")
		x.Equal(policy, v.Get("Content-Security-Policy"), "enforced, the policy frames nothing in either")
		x.Empty(v.Get("Content-Security-Policy-Report-Only"))

		v = ask(false, csp.Off, "/")
		x.Equal("frame-ancestors 'none'", v.Get("Content-Security-Policy"), "framing is refused whatever app.csp says")
		x.Empty(v.Get("Content-Security-Policy-Report-Only"))
		x.Empty(v.Get("Reporting-Endpoints"))
	})
}
