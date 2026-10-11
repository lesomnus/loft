package cli_test

import (
	"context"
	"testing"
	"time"

	"github.com/lesomnus/payday/auth/authsession"
	"github.com/lesomnus/payday/frame"
	"github.com/lesomnus/payday/pdid"
	"github.com/lesomnus/payday/pdtest"
	"github.com/stretchr/testify/require"

	"github.com/lesomnus/rove/cli"
	"github.com/lesomnus/rove/cmd"
	"github.com/lesomnus/rove/internal/ent"
	"github.com/lesomnus/rove/internal/ent/holder"
	entsession "github.com/lesomnus/rove/internal/ent/session"
	"github.com/lesomnus/rove/internal/identity"
)

// aDeployment is the roster in this process, with acme's owner and kim, and
// kim's own workspace beta.
func aDeployment(t *testing.T) cmd.Config {
	t.Helper()
	c := cmd.Config{}
	c.Db.Driver, c.Db.Dsn = pdtest.DB(t)
	c.Watch.Broker = "memory"
	c.App.Files = t.TempDir()
	c.Auth.Roster.Db.Driver, c.Auth.Roster.Db.Dsn = pdtest.DB(t)

	run(t, cli.NewCmdInit(&c), "--tenant", "acme", "--login", "owner", "--password", "owner1234")
	run(t, cli.NewCmdHolder(&c), "add", "--tenant", "acme", "--login", "kim", "--password", "kim-password")
	run(t, cli.NewCmdHolder(&c), "add", "--tenant", "acme", "--login", "lee", "--password", "lee-password")
	run(t, cli.NewCmdInit(&c), "--tenant", "beta", "--login", "kim")
	return c
}

// on runs `f` against the deployment, the way a command does.
func on(t *testing.T, c cmd.Config, f func(s *cmd.Server)) {
	t.Helper()
	s, err := cmd.Build(context.Background(), c)
	require.NoError(t, err)
	defer s.Close()
	f(s)
}

func accountOf(t *testing.T, s *cmd.Server, login string) pdid.Id {
	t.Helper()
	h, err := s.Ent.Holder.Query().Where(holder.Alias(login), holder.DateErasedIsNil()).First(context.Background())
	require.NoError(t, err)
	require.NotNil(t, h.Account)
	return pdid.Id(*h.Account)
}

func membershipsOf(t *testing.T, s *cmd.Server, account pdid.Id) []*ent.Holder {
	t.Helper()
	hs, err := s.Ent.Holder.Query().Where(holder.Account(account.Uuid())).All(context.Background())
	require.NoError(t, err)
	return hs
}

// TestAnOperatorDepartsAnAccountAtOnce.
//
// Somebody who asked to be forgotten now is not made to wait out a grace:
// `rove identity depart` takes their account out of every workspace it is in,
// says what became of each, ends where they are signed in, and -- on the
// roster in this process, which is the deployment's alone -- forgets the
// account there too.
func TestAnOperatorDepartsAnAccountAtOnce(t *testing.T) {
	x := require.New(t)
	ctx := context.Background()
	c := aDeployment(t)

	var kim pdid.Id
	on(t, c, func(s *cmd.Server) {
		kim = accountOf(t, s, "kim")
		_, _, err := s.Sessions.Mint(ctx, authsession.Session{Id: kim.String(), Grant: frame.Whole()})
		x.NoError(err)
	})
	signsIn := func(login, password string) error {
		var err error
		on(t, c, func(s *cmd.Server) { _, err = s.Identity.Verify(ctx, login, password) })
		return err
	}

	for _, args := range [][]string{
		{"depart"},
		{"depart", "--member", "acme/kim"},
		{"depart", "--member", "@acme/nobody"},
		{"depart", "--member", "@acme/kim", "--account", kim.String()},
	} {
		x.Error(cli.NewCmdIdentity(&c).Run(ctx, args), "%v", args)
	}

	out := run(t, cli.NewCmdIdentity(&c), "depart", "--member", "@acme/kim")
	x.Contains(out, "@acme: @kim, member")
	x.Contains(out, "@beta: @kim, owner")
	x.Contains(out, "nothing was done")
	x.NoError(signsIn("kim", "kim-password"), "only asked about")

	out = run(t, cli.NewCmdIdentity(&c), "depart", "--member", "@acme/kim", "--yes")
	x.Contains(out, "left @acme\n")
	x.Contains(out, "left @beta; its last owner, and nobody was there to hand it to")
	x.Contains(out, "forgot "+kim.String())
	x.ErrorIs(signsIn("kim", "kim-password"), identity.ErrRefused)
	x.NoError(signsIn("owner", "owner1234"), "nobody else")

	on(t, c, func(s *cmd.Server) {
		for _, h := range membershipsOf(t, s, kim) {
			x.NotNil(h.DateErased)
			x.Empty(h.Alias)
		}
		n, err := s.Ent.Session.Query().Where(entsession.Account(kim.Uuid())).Count(ctx)
		x.NoError(err)
		x.Zero(n, "where they were signed in")
	})
}

// TestReconcilingFindsWhatTheStreamMissed.
//
// roster's stream replays nothing, so an account erased while this deployment
// was not listening was never marked and would never leave. `rove identity
// reconcile` asks roster about every account with a membership here, marks
// the ones it has nobody by -- from now, so they wait the grace like anybody
// -- and unmarks the ones it has again. It refuses to mark when roster has
// nobody at all, which is a wrong directory far more often than everybody
// leaving.
func TestReconcilingFindsWhatTheStreamMissed(t *testing.T) {
	x := require.New(t)
	ctx := context.Background()
	c := aDeployment(t)

	var kim, lee, owner pdid.Id
	on(t, c, func(s *cmd.Server) {
		kim, lee, owner = accountOf(t, s, "kim"), accountOf(t, s, "lee"), accountOf(t, s, "owner")
		// kim erased at roster, and nobody here heard; lee marked as
		// leaving, and roster has lee all along.
		x.NoError(s.Identity.ForgetPerson(ctx, kim))
		x.NoError(s.Lifecycle.Apply(ctx, identity.Standing{Holder: lee, Erased: time.Now()}))
	})
	leaving := func(account pdid.Id) (n int) {
		on(t, c, func(s *cmd.Server) {
			for _, h := range membershipsOf(t, s, account) {
				if h.DateLeaving != nil {
					n++
				}
			}
		})
		return n
	}

	out := run(t, cli.NewCmdIdentity(&c), "reconcile")
	x.Contains(out, "3 account(s) with a membership here; roster has 2 of them")
	x.Contains(out, "gone at roster, not yet leaving here: "+kim.String()+" (@acme/kim, @beta/kim)")
	x.Contains(out, "leaving here, and roster has it again: "+lee.String())
	x.Contains(out, "nothing was done")
	x.Zero(leaving(kim))
	x.Equal(1, leaving(lee))

	before := time.Now()
	out = run(t, cli.NewCmdIdentity(&c), "reconcile", "--yes")
	x.Contains(out, "marked 1 as leaving, and 1 as not")
	x.Equal(2, leaving(kim), "in both its workspaces")
	x.Zero(leaving(lee))
	on(t, c, func(s *cmd.Server) {
		for _, h := range membershipsOf(t, s, kim) {
			x.False(h.DateLeaving.Before(before.Add(-time.Second)), "from now, to wait the whole grace")
		}
	})

	out = run(t, cli.NewCmdIdentity(&c), "reconcile")
	x.Contains(out, "nothing to do")

	// roster with nobody in it is not everybody leaving.
	on(t, c, func(s *cmd.Server) {
		x.NoError(s.Identity.ForgetPerson(ctx, lee))
		x.NoError(s.Identity.ForgetPerson(ctx, owner))
	})
	err := cli.NewCmdIdentity(&c).Run(ctx, []string{"reconcile", "--yes"})
	x.ErrorContains(err, "auth.roster.directory")
	x.Zero(leaving(lee))
	x.Zero(leaving(owner))
}
