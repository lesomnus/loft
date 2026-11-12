package cli

import (
	"io"
	"testing"

	"github.com/lesomnus/payday/pdid"
	"github.com/lesomnus/payday/pdtest"
	"github.com/lesomnus/xli"
	"github.com/stretchr/testify/require"

	"github.com/lesomnus/loft/cmd"
	"github.com/lesomnus/loft/internal/identity"
)

// Elsewhere puts up a workspace beside the one the deployment `c` serves, as
// `loft tenant import` puts back one exported from another deployment: its
// people are another roster's -- a roster of its own, made for it here -- and
// nobody at this deployment's. `loft init` refuses a second workspace (D85); a
// test of what keeps workspaces apart, or of an operator's command over more
// than one, puts one up with this. With `demo` it is filled as `init --demo`
// fills one. It answers the workspace.
func Elsewhere(t testing.TB, c cmd.Config, alias, login string, demo bool) pdid.Id {
	t.Helper()
	x := require.New(t)
	ctx := t.Context()

	c.Auth.Roster = identity.Config{Tenant: alias}
	c.Auth.Roster.Db.Driver, c.Auth.Roster.Db.Dsn = pdtest.DB(t)
	c.Auth.Sso.Issuer = ""
	c.Auth.SsoOnly = false
	s, err := cmd.Build(ctx, c)
	x.NoError(err)
	defer s.Close()

	rt, p, _, err := s.Identity.MakeTenant(ctx, "", login, "")
	x.NoError(err)
	out := &xli.Command{}
	out.Writer = io.Discard
	ws, err := putUp(ctx, s, rt, "", p, "", "", demo, out)
	x.NoError(err)

	id, err := pdid.From(ws.GetId())
	x.NoError(err)
	return id
}
