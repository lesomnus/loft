// Package loftest is what tests stand a deployment up with that no command
// of the box does any more.
package loftest

import (
	"errors"
	"testing"

	"github.com/lesomnus/payday/pdid"
	"github.com/stretchr/testify/require"

	app "github.com/lesomnus/loft"
	"github.com/lesomnus/loft/cmd"
	"github.com/lesomnus/loft/internal/ent/tenant"
	"github.com/lesomnus/loft/internal/identity"
	"github.com/lesomnus/loft/server/policy"
)

// Member puts `login` in the workspace `alias` with a person record, bound at
// the roster in this process to `role` -- one Loft ships, by its word
// (`policy.RoleOf`) -- and answers the membership: by their identifier at
// roster (D85), made there with `password` when there is nobody by that login,
// as `loft holder add` made them before an operator asked the entry point for
// it (D74). What they may call is what roster says of them (D89).
// The deployment's own writes, through the servers, by no operator and no
// member, so that what a test then holds an operator's or a member's rows to
// is theirs alone.
func Member(t testing.TB, s *cmd.Server, alias, login, role, password string) pdid.Id {
	t.Helper()
	x := require.New(t)
	ctx := t.Context()

	tn, err := s.Ent.Tenant.Query().Where(tenant.Alias(alias)).Only(ctx)
	x.NoError(err, "@%s", alias)
	p, err := s.Identity.Lookup(ctx, login)
	if errors.Is(err, identity.ErrNoPerson) {
		p, err = s.Identity.AddPerson(ctx, login, login, pdid.Nil)
		x.NoError(err)
		x.NoError(s.Identity.SetPassword(ctx, p.Id, password))
	}
	x.NoError(err)

	ref := app.TenantRef_builder{Id: tn.Id[:]}.Build()
	h, err := s.Base.Holder().Add(ctx, app.HolderAddRequest_builder{
		Id:     p.Id.Bytes(),
		Tenant: ref,
		Alias:  p.Alias,
		Name:   login,
	}.Build())
	x.NoError(err)
	r, ok := policy.RoleOf(role)
	x.True(ok, "a role Loft ships: %s", role)
	// Bound at roster when the workspace is the tenant there; a workspace of
	// this database alone -- one an import put up -- is nobody's at roster.
	if rt, err := s.Identity.Tenant(ctx); err == nil && rt.Id == pdid.Id(tn.Id) {
		x.NoError(s.Identity.Bind(ctx, r.Alias, p.Id))
	}
	_, err = s.Base.Party().Add(ctx, app.PartyAddRequest_builder{
		Tenant: ref,
		Name:   login,
		Kind:   "person",
		Holder: app.HolderRef_builder{Id: h.GetId()}.Build(),
	}.Build())
	x.NoError(err)

	id, err := pdid.From(h.GetId())
	x.NoError(err)
	return id
}
