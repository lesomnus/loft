package cmd

import (
	"net/http"
	"strings"

	"github.com/lesomnus/rove/server/csp"
)

// guarded is what every answer over HTTP says about itself, whatever answered
// it: not to be read as another type than it says, not to be framed by
// another site -- which is how a click on a page of somebody else's becomes a
// click here -- to tell no other site where it was opened from, and, behind
// HTTPS, to be asked for over HTTPS only.
//
// And the Content Security Policy, held as `mode` says (`server/csp`) beside
// the framing, which is refused whatever it says.
//
// An attachment says its own (`server/storage`): it is served to be shown,
// sandboxed, and is left to.
func guarded(h http.Handler, https bool, mode csp.Mode, policy string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		v := w.Header()
		v.Set("X-Content-Type-Options", "nosniff")
		v.Set("Referrer-Policy", "same-origin")
		if !strings.HasPrefix(r.URL.Path, "/files/") {
			v.Set("X-Frame-Options", "DENY")
			v.Set("Content-Security-Policy", "frame-ancestors 'none'")
			csp.Headers(v, mode, policy)
		}
		if https {
			v.Set("Strict-Transport-Security", "max-age=31536000")
		}
		h.ServeHTTP(w, r)
	})
}
