// Package tenancy is what a workspace starts with, and how somebody comes to
// be in it (design 9.10, 9.11).
//
// The deployment serves one roster tenant, and that is its workspace, by the
// same identifier (D85). Who is in it is its holders, each a holder of the
// tenant at roster by the same identifier, made here the first time they sign
// in ([Members]).
package tenancy

import (
	"context"

	app "github.com/lesomnus/loft"
)

// SetUp is what a tenant starts with: the types an asset register of an office
// has. It is written as whoever `ctx` is in the tenant, through the stack with
// the domain layer.
func SetUp(ctx context.Context, s app.Server) error {
	return seedTypes(ctx, s)
}

func or(v, otherwise string) string {
	if v == "" {
		return otherwise
	}
	return v
}
