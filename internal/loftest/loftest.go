// Package loftest is what tests stand a deployment up with that no command
// of the box does any more.
package loftest

import (
	"errors"
	"testing"

	"github.com/lesomnus/payday/pdid"
	"github.com/lesomnus/z"
	"github.com/stretchr/testify/require"

	app "github.com/lesomnus/loft"
	"github.com/lesomnus/loft/cmd"
	"github.com/lesomnus/loft/internal/ent/tenant"
	"github.com/lesomnus/loft/internal/identity"
)

// Member puts `login` in the workspace `alias` as `role`, with a person
// record, and answers the membership: by their identifier at roster (D85),
// made on the roster in this process with `password` when there is nobody by
// that login, as `loft holder add` made them before an operator asked the
// entry point for it (D74).
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
		Id:      p.Id.Bytes(),
		Tenant:  ref,
		Alias:   p.Alias,
		Name:    login,
		Role:    z.Ptr(role),
		Account: p.Id.Bytes(),
	}.Build())
	x.NoError(err)
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
