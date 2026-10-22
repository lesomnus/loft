package cli_test

import (
	"context"
	"testing"
	"time"

	"github.com/lesomnus/payday/pdtest"
	"github.com/stretchr/testify/require"

	"github.com/lesomnus/rove/cli"
	"github.com/lesomnus/rove/cmd"
	enttenant "github.com/lesomnus/rove/internal/ent/tenant"
)

// TestAnOperatorClosesRestoresAndMovesAWorkspace: what an owner does from the
// app, done for them -- and moving its address, which only an operator does.
// Each says what it would do until told `--yes`, and the day it says is UTC's,
// whatever the shell's zone.
func TestAnOperatorClosesRestoresAndMovesAWorkspace(t *testing.T) {
	x := require.New(t)
	ctx := context.Background()
	inZone(t, awayFromUTC(time.Now()))
	month := func(f func() string) (string, bool) {
		from := time.Now()
		out := f()
		return out, saysDay(out, "destroyed after ", from.Add(30*24*time.Hour), time.Now().Add(30*24*time.Hour))
	}
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

	out, ok := month(func() string { return run(t, cli.NewCmdTenant(&c), "close", "--tenant", "acme") })
	x.Contains(out, "would close @acme")
	x.True(ok, "not UTC's day, a month from now: %s", out)
	x.False(closed())
	out, ok = month(func() string {
		return run(t, cli.NewCmdTenant(&c), "close", "--tenant", "acme", "--reason", "미납", "--yes")
	})
	x.Contains(out, "closed @acme")
	x.True(ok, "not UTC's day, a month from now: %s", out)
	x.True(closed())
	out, ok = month(func() string { return run(t, cli.NewCmdTenant(&c), "close", "--tenant", "acme") })
	x.Contains(out, "closed already")
	x.True(ok, "not UTC's day, a month from closing: %s", out)
	x.True(saysDay(out, "since ", time.Now().Add(-time.Minute), time.Now()), "not UTC's day: %s", out)
	x.Contains(run(t, cli.NewCmdTenant(&c), "restore", "--tenant", "acme", "--yes"), "restored @acme")
	x.False(closed())

	x.Error(cli.NewCmdTenant(&c).Run(ctx, []string{"alias", "--tenant", "acme", "--to", "api", "--yes"}), "a reserved address")
	out = run(t, cli.NewCmdTenant(&c), "alias", "--tenant", "acme", "--to", "lab-2")
	x.Contains(out, "would move @acme to @lab-2")
	x.True(saysDay(out, "holding @acme until ", time.Now().Add(c.App.Workspaces.Hold()-time.Minute), time.Now().Add(c.App.Workspaces.Hold())), "not UTC's day: %s", out)
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
