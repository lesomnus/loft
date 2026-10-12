package cli_test

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"testing"
	"time"

	"github.com/lesomnus/payday/pdtest"
	"github.com/stretchr/testify/require"

	"github.com/lesomnus/rove/cli"
	"github.com/lesomnus/rove/cmd"
	"github.com/lesomnus/rove/server/pd"
)

// TestRowsFromBeforeTheKeyAreAcceptedOnce is the one step a deployment that
// signs its checkpoints takes on the payday that signs the manifest's rows too
// (lesomnus/payday#58, rove#58). A row nobody signed looks the same whether it
// was written before rows were signed or had its signature taken off, so with
// a key each one is a finding until somebody who has looked runs `rove trail
// accept`, once. After that `verify` is clean, and what a pass writes from then
// on is signed as it goes.
func TestRowsFromBeforeTheKeyAreAcceptedOnce(t *testing.T) {
	x := require.New(t)
	ctx := t.Context()

	c := cmd.Config{}
	c.Db.Driver, c.Db.Dsn = pdtest.DB(t)
	c.Watch.Broker = "memory"
	c.App.Files = t.TempDir()
	c.Auth.Roster.Db.Driver, c.Auth.Roster.Db.Dsn = pdtest.DB(t)
	c.Audit.Archive = t.TempDir()
	c.Audit.Retain = time.Nanosecond
	run(t, cli.NewCmdInit(&c), "--tenant", "acme", "--login", "owner", "--password", "owner-pass-1234")

	// A pass of the policy, as the sweep in `serve` runs it.
	pass := func() {
		s, err := cmd.Build(ctx, c)
		x.NoError(err)
		defer s.Close()
		s.Trail.Pass(ctx, pd.TrailStore(s.Ent))
	}
	trail := func(args ...string) error { return cli.NewCmdTrail(&c).Run(ctx, args) }

	// Written while the deployment had no key.
	pass()
	x.NoError(trail("verify"))

	_, priv, err := ed25519.GenerateKey(rand.Reader)
	x.NoError(err)
	der, err := x509.MarshalPKCS8PrivateKey(priv)
	x.NoError(err)
	c.Audit.Checkpoints.Key = string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}))

	x.ErrorContains(trail("verify"), "findings", "rows nobody signed were taken for signed ones")
	x.ErrorContains(trail("accept"), "--why")
	x.NoError(trail("accept", "--why", "written before this deployment signed its rows"))
	x.NoError(trail("verify"))

	// And what is written now is signed as it is written.
	run(t, cli.NewCmdHolder(&c), "add", "--tenant", "acme", "--login", "kim", "--role", "member", "--password", "kim-password")
	pass()
	x.NoError(trail("verify"))
}
