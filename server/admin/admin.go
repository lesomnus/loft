// Package admin is the operator's entry point (design 6장, decision 17, D74):
// what an operator does to a deployment from outside the box, served as RPCs
// on a listener of its own.
//
// It serves the services the public server serves, where an entity's RPC is
// what an operator does -- `TenantService.Close`, a contract's `Add` -- the
// same request, served as the operator; and the operator's own services
// (`Operator<X>Service`, `proto/loft/operator.proto`) for what is no
// entity's. gRPC, answered as Connect over HTTP by payday's transcoder, which
// is what the terminal speaks to the public server too. The connection is what
// says who is calling: an operator's key here, a person's session there.
//
// It is served by `loft-admin serve`, and `loft serve` links none of it: the
// public binary has no path that sees every workspace, registers none of the
// operator's services and reads no operator's key. Every call carries an
// operator's key (`admin.keys`) and is logged with the operator's name; what
// it does is the deployment's own work at the operator's word, which the past,
// the trail and the access record name them as having done
// (`server/operator`).
package admin

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"fmt"
	"maps"
	"net/http"
	"slices"
	"strings"

	"github.com/lesomnus/payday/web"

	"github.com/lesomnus/loft/cmd"
	"github.com/lesomnus/loft/internal/identity"
	"github.com/lesomnus/loft/server/operator"
)

// Operator is somebody who may use this entry point: their name in
// `admin.keys`, and the digest of the key they hold.
type Operator struct {
	Name string
	sum  [32]byte
}

// MinKey is the shortest key an operator may hold: this door opens every
// workspace there is.
const MinKey = 32

// Operators reads `admin.keys`: each operator's name, and a key as `env:NAME`,
// `file:PATH` or the key itself.
func Operators(keys map[string]string) ([]Operator, error) {
	out := []Operator{}
	for _, name := range slices.Sorted(maps.Keys(keys)) {
		if strings.TrimSpace(name) == "" {
			return nil, fmt.Errorf("admin.keys: an operator is somebody, by a name")
		}
		v, err := identity.ResolveRef(keys[name])
		if err != nil {
			return nil, fmt.Errorf("admin.keys.%s: %w", name, err)
		}
		if len(v) < MinKey {
			return nil, fmt.Errorf("admin.keys.%s: a key that opens every workspace is not %d characters long; give it %d or more", name, len(v), MinKey)
		}
		out = append(out, Operator{Name: name, sum: sha256.Sum256([]byte(v))})
	}
	return out, nil
}

// holding is the operator who holds `key`, if anybody does. Every key is
// compared, in time that does not say which matched.
func holding(ops []Operator, key string) (operator.Operator, bool) {
	sum := sha256.Sum256([]byte(strings.TrimSpace(key)))
	who := ""
	for _, o := range ops {
		if subtle.ConstantTimeCompare(sum[:], o.sum[:]) == 1 {
			who = o.Name
		}
	}
	if who == "" {
		return operator.Operator{}, false
	}
	return operator.Named(who), true
}

// Routes is the operator's entry point on a deployment.
type Routes struct {
	S         *cmd.Server
	C         cmd.Config
	Operators []Operator
}

// Who is the name of the operator a call was made by, and empty for none.
func Who(ctx context.Context) string {
	o, _ := operator.From(ctx)
	return o.Name
}

// Handler serves the entry point: the operator's RPCs, answered as Connect,
// gRPC-Web and gRPC over HTTP.
//
// A request is an RPC by what it says it is (`web.Rpc`), as on the public
// server; anything else is nothing.
func (a Routes) Handler(ctx context.Context) (http.Handler, error) {
	g, err := a.Grpc(ctx)
	if err != nil {
		return nil, err
	}
	rpc, err := web.Transcode(g)
	if err != nil {
		return nil, err
	}

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if web.Rpc(r) {
			rpc.ServeHTTP(w, r)
			return
		}
		http.NotFound(w, r)
	}), nil
}
