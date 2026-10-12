// Package clientaddr is where a request came from, for what is counted or
// shown by it: the sign-in limits (`server/session`, `server/clilogin`) and the
// address a terminal's sign-in is shown as asking from.
//
// # Whose word is taken
//
// The connection's, unless it is a proxy this deployment says it trusts. A
// proxy in front of rove makes every request arrive from the proxy, so a limit
// counted by the connection is one limit for everybody behind it: ten wrong
// guesses by anybody, and a name is refused to all of them. A proxy says whom
// it is passing on in X-Forwarded-For, and so does anybody else who writes the
// header. Believed from a caller, it is an address the caller chose, and a
// limit counted by it is one nobody is held to.
//
// So the header is read only when the connection is a trusted proxy
// (`app.trusted_proxies`), and only as far as the proxies go: from the right,
// past every hop a trusted proxy appended, to the first that is not one. That
// is the address the outermost trusted proxy saw. Everything to its left is
// what the caller wrote.
package clientaddr

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"strings"
)

// Proxies is the addresses whose X-Forwarded-For is believed.
type Proxies []netip.Prefix

// Parse reads the proxies a deployment trusts, each a network (`10.0.0.0/8`)
// or one address.
func Parse(vs []string) (Proxies, error) {
	out := Proxies{}
	for _, v := range vs {
		v = strings.TrimSpace(v)
		if v == "" {
			continue
		}
		if p, err := netip.ParsePrefix(v); err == nil {
			out = append(out, p.Masked())
			continue
		}
		a, err := netip.ParseAddr(v)
		if err != nil {
			return nil, fmt.Errorf("%q is neither an address nor a network", v)
		}
		a = a.Unmap()
		out = append(out, netip.PrefixFrom(a, a.BitLen()))
	}

	return out, nil
}

func (p Proxies) trusts(a netip.Addr) bool {
	a = a.Unmap()
	for _, v := range p {
		if v.Contains(a) {
			return true
		}
	}

	return false
}

// Resolve is the address `r` came from: the connection's, or, when that is a
// trusted proxy, the rightmost address in X-Forwarded-For that is not one.
func (p Proxies) Resolve(r *http.Request) string {
	peer := hostOf(r.RemoteAddr)
	a, err := netip.ParseAddr(peer)
	if err != nil || !p.trusts(a) {
		return peer
	}

	hops := []string{}
	for _, h := range r.Header.Values("X-Forwarded-For") {
		for _, v := range strings.Split(h, ",") {
			if v = strings.TrimSpace(v); v != "" {
				hops = append(hops, v)
			}
		}
	}

	// From the right, which is the order the proxies wrote in.
	at := a
	for i := len(hops) - 1; i >= 0; i-- {
		v, err := netip.ParseAddr(hops[i])
		if err != nil {
			// Not an address where a trusted proxy should have written one:
			// the chain cannot be read past it, and the last hop that could
			// be is the best that is known.
			return at.String()
		}
		v = v.Unmap()
		if !p.trusts(v) {
			return v.String()
		}
		at = v
	}

	// A proxy all the way down, or one that said nothing: as far as anybody
	// said.
	return at.String()
}

type key struct{}

// Handler puts [Proxies.Resolve]'s answer on every request's context, for [Of]
// to read.
func (p Proxies) Handler(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), key{}, p.Resolve(r))))
	})
}

// Of is where `r` came from, as the [Proxies.Handler] in front of it resolved.
// Without one it is the connection's own address, and X-Forwarded-For is not
// read at all.
func Of(r *http.Request) string {
	if v, ok := r.Context().Value(key{}).(string); ok {
		return v
	}

	return hostOf(r.RemoteAddr)
}

func hostOf(v string) string {
	host, _, err := net.SplitHostPort(v)
	if err != nil {
		return v
	}

	return host
}
