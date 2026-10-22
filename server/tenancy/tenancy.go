// Package tenancy is what a workspace starts with, and how one is made
// (design 9.11).
//
// A workspace is a Rove tenant and is Rove's alone: roster's tenant is the
// directory accounts are in, which is a different layer (D27). Who is in a
// workspace is its holders, each the membership of one account.
package tenancy

import (
	"context"

	app "github.com/lesomnus/rove"
)

// SetUp is what a tenant starts with: the types an asset register of an office
// has. It is written as whoever `ctx` is in the tenant, through the stack with
// the domain layer.
//
// No domain: a tenant prints its labels with this deployment's address until
// it adds one of its own, and a default subdomain is one it asks for
// (rove#155). Starting every tenant on one was how a deployment whose labels
// needed a domain gave them labels at all, which none does now.
func SetUp(ctx context.Context, s app.Server) error {
	return seedTypes(ctx, s)
}

func or(v, otherwise string) string {
	if v == "" {
		return otherwise
	}
	return v
}
