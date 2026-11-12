package cmd_test

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/lesomnus/payday/pdid"

	app "github.com/lesomnus/loft"
	"github.com/lesomnus/loft/internal/ent/tenant"
	"github.com/lesomnus/loft/server/policy"
	"github.com/lesomnus/loft/server/tenancy"
)

// counted is roster, counting what it is asked to widen.
type counted struct {
	tenancy.Widener
	asked int
}

func (c *counted) WidenRole(ctx context.Context, alias string, methods []string) error {
	c.asked++
	return c.Widener.WidenRole(ctx, alias, methods)
}

// TestAServedDeploymentGivesTheRolesWhatTheReleasesSinceAdded (D90): what a
// deployment does as it starts, and every hour after -- the deployment's own
// work in the workspace it serves -- gives its roles at roster what each
// release since theirs added and records it, and with nothing left to give
// asks roster nothing.
func TestAServedDeploymentGivesTheRolesWhatTheReleasesSinceAdded(t *testing.T) {
	w := newWorld(t)
	x := w.x
	ctx := t.Context()
	kh := w.holder("kim", "member")
	kim, err := pdid.From(kh.GetId())
	x.NoError(err)
	at := func() uint32 {
		v, err := w.s.Ent.Tenant.Query().Where(tenant.Alias(w.tenant.GetAlias())).Only(ctx)
		x.NoError(err)
		return v.RolesRelease
	}

	// Its roles as this release wrote them, and a release after it.
	last := policy.Releases.Latest()
	x.NoError(w.s.Ent.Tenant.Update().Where(tenant.Alias(w.tenant.GetAlias())).SetRolesRelease(last).Exec(ctx))
	types := app.AssetTypeService_Add_FullMethodName
	w.s.Roles.Ledger = append(slices.Clone(policy.Releases), policy.Release{Name: "next", Adds: map[string][]string{
		"loft-member": {types},
	}})
	roster := &counted{Widener: w.s.Roles.People}
	w.s.Roles.People = roster

	x.NoError(w.s.WidenRoles(ctx))
	x.Equal(1, roster.asked)
	x.Equal(last+1, at())
	w.s.Identity.Reach().Recheck(kim)
	x.NoError(w.s.Identity.Reach().May(ctx, kim, types), "what the release gave a member")

	x.NoError(w.s.WidenRoles(ctx))
	x.Equal(1, roster.asked, "nothing left to give, and roster asked nothing")
}

// TestTheRolesLoopSaysWhatItDecided (D90): what the loop decides of the roles
// is in the log as it decides it, since what a later release gives them
// follows from it -- once, the release it took them as having when the
// workspace recorded none, and what it gave each role, by its methods. A pass
// with nothing to give says nothing.
func TestTheRolesLoopSaysWhatItDecided(t *testing.T) {
	w := newWorld(t)
	x := w.x
	ctx, said := logged(t.Context())
	where := tenant.Alias(w.tenant.GetAlias())
	last := policy.Releases.Latest()

	// A workspace that recorded nothing of its roles.
	x.NoError(w.s.Ent.Tenant.Update().Where(where).SetRolesRelease(0).Exec(ctx))
	x.NoError(w.s.WidenRoles(ctx))
	x.Equal(1, strings.Count(said.String(), "taken as given the last"), "%s", said)
	x.Contains(said.String(), fmt.Sprintf("release=%d", last))
	said.Reset()
	x.NoError(w.s.WidenRoles(ctx))
	x.Empty(said.String(), "recorded now, and nothing to say")

	// A release after the one they have, which gives two roles something.
	types, census := app.AssetTypeService_Add_FullMethodName, app.AssetService_Census_FullMethodName
	w.s.Roles.Ledger = append(slices.Clone(policy.Releases), policy.Release{Name: "next", Adds: map[string][]string{
		"loft-member":  {types, census},
		"loft-auditor": {types},
	}})
	x.NoError(w.s.WidenRoles(ctx))
	line := said.String()
	x.Equal(1, strings.Count(line, "given at roster what the releases since theirs added"), "%s", line)
	x.Contains(line, fmt.Sprintf("from=%d to=%d", last, last+1))
	x.Contains(line, "given.loft-member="+census+","+types)
	x.Contains(line, "given.loft-auditor="+types)
	x.NotContains(line, "taken as given")
}
