package cli_test

import (
	"testing"

	"github.com/lesomnus/payday/pdtest"
	"github.com/stretchr/testify/require"

	"github.com/lesomnus/rove/cli"
	"github.com/lesomnus/rove/cmd"
)

// TestServeIsToldWhichProcessRunsTheLoops: `spin.lease` is named rather than
// defaulted, as `watch.broker` is -- the default would run every sweep in every
// process -- and `serve` refuses before it builds anything when it is not.
func TestServeIsToldWhichProcessRunsTheLoops(t *testing.T) {
	x := require.New(t)

	c := cmd.Config{}
	c.Db.Driver, c.Db.Dsn = pdtest.DB(t)
	c.Watch.Broker = "memory"
	c.App.Files = t.TempDir()
	c.Auth.Roster.Db.Driver, c.Auth.Roster.Db.Dsn = pdtest.DB(t)

	err := cli.NewCmdServe(&c).Run(t.Context(), nil)
	x.ErrorContains(err, "spin.lease")
	x.ErrorContains(err, "one replica", "and why")
}

// TestServeRefusesSeveralProcessesWithAKeyEach: a process that runs none of the
// loops says there are others, and a download link it signs with a key of its
// own is refused by them -- `serve` says so before it builds anything.
func TestServeRefusesSeveralProcessesWithAKeyEach(t *testing.T) {
	x := require.New(t)

	c := cmd.Config{}
	c.Db.Driver, c.Db.Dsn = pdtest.DB(t)
	c.Watch.Broker = "memory"
	c.Spin.Lease = "none"
	c.App.Files = t.TempDir()
	c.Auth.Roster.Db.Driver, c.Auth.Roster.Db.Dsn = pdtest.DB(t)

	err := cli.NewCmdServe(&c).Run(t.Context(), nil)
	x.ErrorContains(err, "app.signing_key")
	x.ErrorContains(err, "spin.lease: none")
}
