package cli_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/lesomnus/rove/cli"
	"github.com/lesomnus/rove/cmd"
	"github.com/lesomnus/rove/internal/ent/party"
	enttenant "github.com/lesomnus/rove/internal/ent/tenant"
)

// TestTheOwnerInitMakesIsCalledTheSameEverywhere.
//
// The account `init` makes is called at roster what its person record is
// called here, so that a workspace it makes later calls its owner that and
// not their login.
func TestTheOwnerInitMakesIsCalledTheSameEverywhere(t *testing.T) {
	x := require.New(t)
	ctx := context.Background()
	c := embeddedConfig(t)

	run(t, cli.NewCmdInit(&c), "--tenant", "acme", "--login", "kim", "--person", "김민준", "--password", "kim-password")

	s, err := cmd.Build(ctx, c)
	x.NoError(err)
	defer s.Close()
	p, err := s.Identity.Lookup(ctx, "kim")
	x.NoError(err)
	x.Equal("김민준", p.Name)

	m, err := s.Workspaces(c).Create(ctx, p, "두 번째")
	x.NoError(err)
	owner, err := s.Ent.Party.Query().Where(party.TenantId(m.Workspace.Uuid())).Only(ctx)
	x.NoError(err)
	x.Equal("김민준", owner.Name)

	// And with no name given, what `init` calls the person it made.
	c = embeddedConfig(t)
	run(t, cli.NewCmdInit(&c), "--tenant", "beta", "--login", "lee")
	s2, err := cmd.Build(ctx, c)
	x.NoError(err)
	defer s2.Close()
	p, err = s2.Identity.Lookup(ctx, "lee")
	x.NoError(err)
	t2, err := s2.Ent.Tenant.Query().Where(enttenant.Alias("beta")).Only(ctx)
	x.NoError(err)
	here, err := s2.Ent.Party.Query().Where(party.TenantId(t2.Id)).Only(ctx)
	x.NoError(err)
	x.Equal(here.Name, p.Name)
}
