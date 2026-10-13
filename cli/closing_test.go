package cli_test

import (
	"context"
	"testing"

	"github.com/lesomnus/payday/pdtest"
	"github.com/stretchr/testify/require"

	"github.com/lesomnus/rove/cli"
	"github.com/lesomnus/rove/cmd"
	enttenant "github.com/lesomnus/rove/internal/ent/tenant"
)

// TestAnOperatorClosesRestoresAndMovesAWorkspace: what an owner does from the
// app, done for them -- and moving its address, which only an operator does.
// Each says what it would do until told `--yes`.
func TestAnOperatorClosesRestoresAndMovesAWorkspace(t *testing.T) {
	x := require.New(t)
	ctx := context.Background()
	c := cmd.Config{}
	c.Db.Driver, c.Db.Dsn = pdtest.DB(t)
	c.Watch.Broker = "memory"
	c.App.Files = t.TempDir()
	c.Auth.Roster.Db.Driver, c.Auth.Roster.Db.Dsn = pdtest.DB(t)
	run(t, cli.NewCmdInit(&c), "--tenant", "acme", "--login", "owner", "--password", "owner1234")
	closed := func() bool {
		var v bool
		on(t, c, func(s *cmd.Server) {
			r, err := s.Ent.Tenant.Query().Where(enttenant.Alias("acme")).Only(ctx)
			x.NoError(err)
			v = r.DateClosed != nil
		})
		return v
	}

	x.Contains(run(t, cli.NewCmdTenant(&c), "close", "--tenant", "acme"), "would close @acme")
	x.False(closed())
	x.Contains(run(t, cli.NewCmdTenant(&c), "close", "--tenant", "acme", "--reason", "미납", "--yes"), "closed @acme")
	x.True(closed())
	x.Contains(run(t, cli.NewCmdTenant(&c), "close", "--tenant", "acme"), "closed already")
	x.Contains(run(t, cli.NewCmdTenant(&c), "restore", "--tenant", "acme", "--yes"), "restored @acme")
	x.False(closed())

	x.Error(cli.NewCmdTenant(&c).Run(ctx, []string{"alias", "--tenant", "acme", "--to", "api", "--yes"}), "a reserved address")
	x.Contains(run(t, cli.NewCmdTenant(&c), "alias", "--tenant", "acme", "--to", "lab-2"), "would move @acme to @lab-2")
	x.Contains(run(t, cli.NewCmdTenant(&c), "alias", "--tenant", "acme", "--to", "lab-2", "--yes"), "moved @acme to @lab-2")
	on(t, c, func(s *cmd.Server) {
		ok, err := s.Ent.Tenant.Query().Where(enttenant.Alias("lab-2")).Exist(ctx)
		x.NoError(err)
		x.True(ok)
		free, err := s.Aliases(c).Free(ctx, "acme")
		x.NoError(err)
		x.False(free, "the old address is held")
	})
}
