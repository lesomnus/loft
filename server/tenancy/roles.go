package tenancy

import (
	"context"
	"errors"

	"github.com/lesomnus/payday/pdid"
	"github.com/lesomnus/z"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	app "github.com/lesomnus/loft"
	"github.com/lesomnus/loft/internal/identity"
	"github.com/lesomnus/loft/server/policy"
)

// Widener is roster, as the roles Loft ships are widened there: a role of the
// tenant's given more methods, and nothing it holds taken
// ([identity.Store.WidenRole]), or [identity.ErrRoleGone] for a role the
// tenant has none by.
type Widener interface {
	WidenRole(ctx context.Context, alias string, methods []string) error
}

// Roles brings the roles Loft ships, as the tenant at roster has them, to this
// build's (D90, #327's decision 33): what each release after the one they were
// last given added, a release at a time, and never what [policy.Roles] holds
// by now -- the tenant may have taken a method out of a role, or the role out
// of the tenant, and those are the tenant's. The release they are at is the
// workspace's own (`Tenant.roles_release`): `loft init` records the last one,
// as it writes the roles, and this records each one after.
//
// What a served deployment does as it starts and every hour after, one
// process of it (`loft.roles`), and an operator asks for with `loft role
// widen`.
type Roles struct {
	// Base is the stack with no wall, which reads and records the release.
	Base app.Server
	// People is roster.
	People Widener
	// Ledger is the releases; nil is [policy.Releases].
	Ledger policy.Ledger
}

// Widening is what [Roles.Widen] did, or would.
type Widening struct {
	// From is the release the roles were at, and To the one they are at now.
	From, To uint32
	// Unrecorded is a workspace that says nothing of its roles: taken as
	// given what there is, recorded at the last release, and given nothing.
	Unrecorded bool
	// Roles is every role given something, or that would be, in the order of
	// [policy.Roles].
	Roles []Widened
}

// Widened is one role and what it was given: nothing, when the tenant has no
// role by its alias.
type Widened struct {
	Role    string
	Methods []string
	Missing bool
}

// Widen brings the roles of the workspace `tenant` -- what ctx is framed in,
// as the deployment or an operator -- to the last release, or with `dry` says
// what that would give. `from` is the release to give what came after,
// rather than the one recorded: for roles the tenant had before anything here
// recorded them. A workspace that is not here is nothing to do.
//
// A role the tenant has none of is said and given nothing, and the rest are
// given what they would; the release is recorded only once roster has given
// every role there is what it was to, so a pass roster stopped half way is
// done again whole.
func (r Roles) Widen(ctx context.Context, tenant pdid.Id, from uint32, dry bool) (Widening, error) {
	ledger := r.Ledger
	if ledger == nil {
		ledger = policy.Releases
	}
	ref := app.TenantRef_builder{Id: tenant.Bytes()}.Build()
	ws, err := r.Base.Tenant().Get(ctx, app.TenantGetRequest_builder{Ref: ref}.Build())
	if status.Code(err) == codes.NotFound {
		return Widening{}, nil
	}
	if err != nil {
		return Widening{}, err
	}

	out := Widening{From: ws.GetRolesRelease(), To: ledger.Latest()}
	if from > 0 {
		out.From = min(from, out.To)
	} else if out.From == 0 {
		// Nothing says which release they had: the roles written before a
		// release was recorded, or by hand. Giving them what every release
		// gave would give back what the tenant took out, so they are taken
		// as given what there is.
		out.Unrecorded = true
		out.From = out.To
	}
	if out.From > out.To {
		// Recorded by a later build than this one, which has not had its
		// releases: nothing of it is this one's to give or to record.
		out.To = out.From
		return out, nil
	}

	given := ledger.Since(out.From)
	for _, role := range policy.Roles {
		ms := given[role.Alias]
		if len(ms) == 0 {
			continue
		}
		w := Widened{Role: role.Alias, Methods: ms}
		if !dry {
			err := r.People.WidenRole(ctx, role.Alias, ms)
			switch {
			case errors.Is(err, identity.ErrRoleGone):
				w.Missing = true
			case err != nil:
				return out, err
			}
		}
		out.Roles = append(out.Roles, w)
	}

	if dry || ws.GetRolesRelease() == out.To {
		return out, nil
	}
	if _, err := r.Base.Tenant().Patch(ctx, app.TenantPatchRequest_builder{
		Ref:              ref,
		RolesRelease:     z.Ptr(out.To),
		DateUpdatedForce: z.Ptr(true),
	}.Build()); err != nil {
		return out, err
	}
	return out, nil
}
