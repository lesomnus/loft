// Package tenancy is what a workspace starts with, and how one is made
// (design 9.11).
//
// A workspace is a Rove tenant and is Rove's alone: roster's tenant is the
// directory accounts are in, which is a different layer (D27). Who is in a
// workspace is its holders, each the membership of one account.
package tenancy

import (
	"context"
	"fmt"

	"github.com/lesomnus/z"

	app "github.com/lesomnus/rove"
)

// SetUp is what a tenant starts with: the types an asset register of an office
// has, and a default label subdomain when the deployment offers one. It is
// written as whoever `ctx` is in the tenant, through the stack with the domain
// layer.
func SetUp(ctx context.Context, s app.Server, alias, labelSuffix string) error {
	if err := seedTypes(ctx, s); err != nil {
		return err
	}
	if labelSuffix != "" {
		if _, err := s.TenantDomain().Add(ctx, app.TenantDomainAddRequest_builder{Sub: z.Ptr(alias)}.Build()); err != nil {
			return fmt.Errorf("label domain: %w", err)
		}
	}

	return nil
}

func or(v, otherwise string) string {
	if v == "" {
		return otherwise
	}
	return v
}
