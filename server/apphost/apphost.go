// Package apphost is the workspace a request's host serves alone (rove#155).
//
// A workspace may have a domain of its own that its whole app is on: an
// active `app` domain (design 9.9). A request that came to one is pinned to
// that workspace, which `cmd.Hosts` decides where the request comes in and
// puts on its context, and every part that serves it reads from there: the
// resolver, which serves no call as a membership of any other workspace, and
// the HTTP beside the RPCs. A pin only ever narrows what a credential reaches
// -- it names no member and grants nothing -- so where it is read, it is a
// refusal of the rest.
package apphost

import (
	"context"
	"net"
	"net/http"
	"strings"

	"github.com/lesomnus/payday/pdid"
)

// Pinned is the workspace a request's host serves alone: its identifier, and
// the alias the workspace is at now.
type Pinned struct {
	Workspace pdid.Id
	Alias     string
}

// Names answers whether `v` -- what a call or a path names a workspace by:
// its alias, with or without `@`, or its identifier -- is the pinned one.
func (p Pinned) Names(v string) bool {
	v = strings.TrimPrefix(strings.TrimSpace(v), "@")
	if v == p.Alias {
		return true
	}
	id, err := pdid.Parse(v)
	return err == nil && id == p.Workspace
}

type key struct{}

// Into is `ctx` with the request pinned to `p`.
func Into(ctx context.Context, p Pinned) context.Context {
	return context.WithValue(ctx, key{}, p)
}

// From is the workspace the request `ctx` is of is pinned to, if its host
// pins it to one.
func From(ctx context.Context) (Pinned, bool) {
	p, ok := ctx.Value(key{}).(Pinned)
	return p, ok
}

// Host is the host a request came to, as it is compared with a domain: lower
// case, with no port and no trailing dot.
func Host(r *http.Request) string {
	h := r.Host
	if v, _, err := net.SplitHostPort(h); err == nil {
		h = v
	}
	return strings.TrimSuffix(strings.ToLower(strings.Trim(h, "[]")), ".")
}
