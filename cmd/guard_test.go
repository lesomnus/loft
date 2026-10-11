package cmd

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
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
	ask := func(https bool, path string) http.Header {
		rec := httptest.NewRecorder()
		guarded(h, https).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		x.Equal(http.StatusTeapot, rec.Code, "what answered still answers")
		return rec.Header()
	}

	for _, path := range []string{"/", "/@acme/assets", "/rove.AssetService/Search", "/account"} {
		v := ask(false, path)
		x.Equal("nosniff", v.Get("X-Content-Type-Options"), path)
		x.Equal("same-origin", v.Get("Referrer-Policy"), path)
		x.Equal("DENY", v.Get("X-Frame-Options"), path)
		x.Equal("frame-ancestors 'none'", v.Get("Content-Security-Policy"), path)
		x.Empty(v.Get("Strict-Transport-Security"), "a deployment on plain HTTP cannot promise HTTPS")
	}
	x.Equal("max-age=31536000", ask(true, "/").Get("Strict-Transport-Security"))

	v := ask(false, "/files/a")
	x.Equal("nosniff", v.Get("X-Content-Type-Options"))
	x.Empty(v.Get("X-Frame-Options"), "an attachment says its own")
	x.Equal("sandbox", v.Get("Content-Security-Policy"))
}
