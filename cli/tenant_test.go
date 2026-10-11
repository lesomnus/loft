package cli_test

import (
	"context"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/lesomnus/payday/pdtest"
	"github.com/lesomnus/xli"
	"github.com/stretchr/testify/require"

	"github.com/lesomnus/rove/cli"
	"github.com/lesomnus/rove/cmd"
	"github.com/lesomnus/rove/internal/ent/tenant"
	"github.com/lesomnus/rove/internal/identity"
)

// run is a command's output, run the way a shell runs it.
func run(t *testing.T, k *xli.Command, args ...string) string {
	t.Helper()
	x := require.New(t)

	r, w, err := os.Pipe()
	x.NoError(err)
	was := os.Stdout
	os.Stdout = w
	err = k.Run(t.Context(), args)
	os.Stdout = was
	x.NoError(w.Close())

	b, e := io.ReadAll(r)
	x.NoError(e)
	x.NoError(err, "%s: %s", strings.Join(args, " "), b)

	return string(b)
}

// TestAPurgeTakesTheWorkspacesOwnAccountsOutOfTheRosterInThisProcess.
//
// The roster in this process holds nothing but Rove's accounts, and nothing
// but Rove can reach it: an account that was in the purged workspace and in no
// other goes with it. One in another workspace is still somebody's.
func TestAPurgeTakesTheWorkspacesOwnAccountsOutOfTheRosterInThisProcess(t *testing.T) {
	x := require.New(t)
	ctx := context.Background()

	c := cmd.Config{}
	c.Db.Driver, c.Db.Dsn = pdtest.DB(t)
	c.Watch.Broker = "memory"
	c.App.Files = t.TempDir()
	c.Auth.Roster.Db.Driver, c.Auth.Roster.Db.Dsn = pdtest.DB(t)

	run(t, cli.NewCmdInit(&c), "--tenant", "acme", "--login", "owner", "--password", "owner1234")
	run(t, cli.NewCmdHolder(&c), "add", "--tenant", "acme", "--login", "kim", "--password", "kim-password")
	out := run(t, cli.NewCmdInit(&c), "--tenant", "beta", "--login", "owner")
	x.NotContains(out, "password", "an account there already is given a password")
	x.Error(cli.NewCmdInit(&c).Run(ctx, []string{"--tenant", "gamma", "--login", "owner", "--password", "something-else"}),
		"an account's password changed by making a workspace")

	signsIn := func(login, password string) error {
		s, err := cmd.Build(ctx, c)
		x.NoError(err)
		defer s.Close()
		_, err = s.Identity.Verify(ctx, login, password)
		return err
	}
	x.NoError(signsIn("kim", "kim-password"))

	out = run(t, cli.NewCmdTenant(&c), "purge", "--tenant", "acme")
	x.Contains(out, "roster in this process: would forget 1 account(s)")
	x.NoError(signsIn("kim", "kim-password"), "a purge that was only asked about")

	out = run(t, cli.NewCmdTenant(&c), "purge", "--tenant", "acme", "--yes")
	x.Contains(out, "roster in this process: forgot 1 account(s)")
	x.ErrorIs(signsIn("kim", "kim-password"), identity.ErrRefused)
	x.NoError(signsIn("owner", "owner1234"), "an account another workspace has went with it")

	s, err := cmd.Build(ctx, c)
	x.NoError(err)
	defer s.Close()
	gone, err := s.Ent.Tenant.Query().Where(tenant.Alias("acme")).Exist(ctx)
	x.NoError(err)
	x.False(gone, "the workspace is still here")
}
