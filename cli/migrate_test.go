package cli_test

import (
	"testing"

	"github.com/lesomnus/payday/pdtest"
	entschema "github.com/protobuf-orm/ent/dialect/sql/schema"
	"github.com/stretchr/testify/require"

	"github.com/lesomnus/rove/cli"
	"github.com/lesomnus/rove/cmd"
	entmigrate "github.com/lesomnus/rove/internal/ent/migrate"
)

// TestADeploymentMigratesBeforeItServes: `rove migrate` brings a database
// nothing has touched into this build's shape, which is the shape a `serve`
// that may not alter tables (`db.migrate: false`) checks for before it starts
// -- and asked again, it finds nothing to do.
func TestADeploymentMigratesBeforeItServes(t *testing.T) {
	x := require.New(t)
	ctx := t.Context()

	c := cmd.Config{}
	c.Db.Driver, c.Db.Dsn = pdtest.DB(t)
	c.Watch.Broker = "memory"
	c.App.Files = t.TempDir()
	c.Auth.Roster.Db.Driver, c.Auth.Roster.Db.Dsn = pdtest.DB(t)

	// What serve asks first.
	check := func() error {
		s, err := cmd.Build(ctx, c)
		x.NoError(err)
		defer s.Close()
		return entschema.Check(ctx, s.Db, s.Dialect, entmigrate.Tables)
	}
	x.Error(check(), "a database nothing made is in this build's shape")

	x.Contains(run(t, cli.NewCmdMigrate(&c)), "this build's shape")
	x.NoError(check())
	x.Contains(run(t, cli.NewCmdMigrate(&c)), "this build's shape", "a second time")
	x.NoError(check())
}
