package cli_test

import (
	"context"
	"fmt"
	"slices"
	"testing"

	"github.com/stretchr/testify/require"

	app "github.com/lesomnus/loft"
	"github.com/lesomnus/loft/internal/ent/tenant"
	"github.com/lesomnus/loft/internal/identity"
	"github.com/lesomnus/loft/server/policy"
	"github.com/lesomnus/loft/server/tenancy"
)

// tookAway is roster with one of the roles Loft wrote taken away by the
// tenant.
type tookAway struct {
	tenancy.Widener
	role string
}

func (r tookAway) WidenRole(ctx context.Context, alias string, methods []string) error {
	if alias == r.role {
		return fmt.Errorf("%w: %s", identity.ErrRoleGone, alias)
	}
	return r.Widener.WidenRole(ctx, alias, methods)
}

// TestAnOperatorGivesTheRolesWhatEachReleaseAdded (D90): `loft init` records
// the release of the roles it writes, and `role widen` gives them what each
// release since added -- said first, done with `--yes`, and recorded, so that
// asking again gives nothing; a role the tenant took away is said and given
// nothing, and a workspace that says nothing of its roles is taken as given
// what there is.
func TestAnOperatorGivesTheRolesWhatEachReleaseAdded(t *testing.T) {
	x := require.New(t)
	ctx := context.Background()
	d := aDeployment(t)
	recorded := func() uint32 {
		v, err := d.s.Ent.Tenant.Query().Where(tenant.Alias("acme")).Only(ctx)
		x.NoError(err)
		return v.RolesRelease
	}
	x.Equal(policy.Releases.Latest(), recorded(), "what init wrote")

	out, err := d.loft(t, "role", "widen")
	x.NoError(err, out)
	x.Contains(out, fmt.Sprintf("the roles are at release %[1]d of %[1]d", policy.Releases.Latest()))
	x.Contains(out, "nothing to give")

	// A release after this one, which gives a member what adds a type, and
	// an auditor -- whose role the tenant took away -- what adds an asset.
	types := app.AssetTypeService_Add_FullMethodName
	last := policy.Releases.Latest()
	d.s.Roles.Ledger = append(slices.Clone(policy.Releases), policy.Release{Name: "next", Adds: map[string][]string{
		"loft-member":  {types},
		"loft-auditor": {app.AssetService_Add_FullMethodName},
	}})
	d.s.Roles.People = tookAway{Widener: d.s.Identity, role: "loft-auditor"}
	kim := memberIdOf(t, d.s, "kim")

	out, err = d.loft(t, "role", "widen")
	x.NoError(err, out)
	x.Contains(out, fmt.Sprintf("the roles are at release %d of %d", last, last+1))
	x.Contains(out, "loft-member: would be given "+types)
	x.Contains(out, "(nothing was done")
	x.Equal(last, recorded())

	out, err = d.loft(t, "role", "widen", "--yes")
	x.NoError(err, out)
	x.Contains(out, "loft-member: given "+types)
	x.Contains(out, "loft-auditor: the tenant has no role by this alias at roster, so it was given nothing")
	x.Contains(out, fmt.Sprintf("the roles are at release %d now", last+1))
	x.Equal(last+1, recorded())
	d.s.Identity.Reach().Recheck(kim)
	x.NoError(d.s.Identity.Reach().May(ctx, kim, types), "what the release gave a member, at roster")

	out, err = d.loft(t, "role", "widen", "--yes")
	x.NoError(err, out)
	x.Contains(out, "nothing to give", "given once")

	// Given again from the release before, which roster takes as given once.
	out, err = d.loft(t, "role", "widen", "--from", fmt.Sprint(last), "--yes")
	x.NoError(err, out)
	x.Contains(out, "loft-member: given "+types)

	// A workspace that says nothing of its roles.
	x.NoError(d.s.Ent.Tenant.Update().Where(tenant.Alias("acme")).SetRolesRelease(0).Exec(ctx))
	out, err = d.loft(t, "role", "widen", "--yes")
	x.NoError(err, out)
	x.Contains(out, fmt.Sprintf("taken as release %d, the last, and given nothing", last+1))
	x.NotContains(out, "given "+types)
	x.Equal(last+1, recorded())
}
