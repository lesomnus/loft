// Package landing is where a browser is sent at the end of a round trip a
// link started -- signing in through the issuer (`server/sso`), a sign-in
// handed to a workspace's own domain (`server/session`) -- which is a path on
// the host it was asked on, and never another site: a link that named one
// would land somebody who just signed in on a page made to look like this
// one.
package landing

import (
	"net/url"
	"strings"
)

// Path is `next` as a redirect's Location says it, when it is a path on the
// host it is asked on, and `/` otherwise.
//
// What is held to that is what goes out, and not only what came in: a
// browser reads `//host` and `/\host` as another host, and `net/url` writes a
// path it decoded back out escaped its own way, so that `/%2Fevil.example/ x`
// -- a path, going in -- comes out as `//evil.example/%20x`, another site.
func Path(next string) string {
	if !strings.HasPrefix(next, "/") || strings.HasPrefix(next, "//") || strings.Contains(next, `\`) {
		return "/"
	}
	u, err := url.Parse(next)
	if err != nil || u.Scheme != "" || u.Host != "" || u.User != nil || u.Opaque != "" {
		return "/"
	}
	out := u.EscapedPath()
	if u.RawQuery != "" {
		out += "?" + u.RawQuery
	}
	if !strings.HasPrefix(out, "/") || strings.HasPrefix(out, "//") || strings.Contains(out, `\`) {
		return "/"
	}
	return out
}
