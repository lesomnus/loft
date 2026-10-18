package cli_test

import (
	"os"
	"path/filepath"
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

// TestAMigrationOpensOnlyTheDatabases: `rove migrate` brings the tables along
// where no file, archive or key a server needs is to be had -- which is where
// it runs in Kubernetes, before a pod serves and with none of its volumes
// mounted (deploy/k8s).
func TestAMigrationOpensOnlyTheDatabases(t *testing.T) {
	x := require.New(t)
	ctx := t.Context()

	c := cmd.Config{}
	c.Db.Driver, c.Db.Dsn = pdtest.DB(t)
	c.Watch.Broker = "memory"
	c.Auth.Roster.Db.Driver, c.Auth.Roster.Db.Dsn = pdtest.DB(t)

	// Neither is anything a server could start on: its files beneath a file,
	// and a key to sign the trail with that is not there.
	blocked := filepath.Join(t.TempDir(), "blocked")
	x.NoError(os.WriteFile(blocked, nil, 0o600))
	c.App.Files = filepath.Join(blocked, "files")
	c.Audit.Checkpoints.Key = filepath.Join(t.TempDir(), "absent.pem")
	_, err := cmd.Build(ctx, c)
	x.Error(err, "a server with nowhere to keep files and no key")

	x.Contains(run(t, cli.NewCmdMigrate(&c)), "this build's shape")

	// And what it made is the shape a server checks for.
	c.App.Files = t.TempDir()
	c.Audit.Checkpoints.Key = ""
	s, err := cmd.Build(ctx, c)
	x.NoError(err)
	defer s.Close()
	x.NoError(entschema.Check(ctx, s.Db, s.Dialect, entmigrate.Tables))
}

// TestAnIndexThisBuildReplacedIsDropped: an index an earlier build made and
// this one replaced goes when the database is migrated, and what replaced it
// is there (#122). Nothing listed as replaced is an index this build makes,
// or every migration would drop what it had just made.
func TestAnIndexThisBuildReplacedIsDropped(t *testing.T) {
	x := require.New(t)
	ctx := t.Context()

	declared := map[string]bool{}
	for _, tb := range entmigrate.Tables {
		for _, i := range tb.Indexes {
			declared[i.Name] = true
		}
	}
	for _, v := range cli.Replaced {
		x.False(declared[v], "%s is replaced, and this build declares it", v)
	}

	c := cmd.Config{}
	c.Db.Driver, c.Db.Dsn = pdtest.DB(t)
	c.Watch.Broker = "memory"
	c.App.Files = t.TempDir()
	c.Auth.Roster.Db.Driver, c.Auth.Roster.Db.Dsn = pdtest.DB(t)
	x.Contains(run(t, cli.NewCmdMigrate(&c)), "this build's shape")

	s, err := cmd.Build(ctx, c)
	x.NoError(err)
	defer s.Close()
	has := func(name string) bool {
		q := "SELECT count(*) FROM sqlite_master WHERE type = 'index' AND name = ?"
		if s.Dialect == "postgres" {
			q = "SELECT count(*) FROM pg_indexes WHERE schemaname = current_schema() AND indexname = $1"
		}
		n := 0
		x.NoError(s.Db.QueryRowContext(ctx, q, name).Scan(&n))
		return n > 0
	}

	// Two the build before this one made: a list's, which began with the
	// time, and one declared (parent, valid_from) that had the edge last.
	for _, q := range []string{
		"CREATE INDEX asset_date_created_id ON asset (date_created, id)",
		"CREATE INDEX placement_valid_from_parent_id ON placement (valid_from, parent_id)",
	} {
		_, err := s.Db.ExecContext(ctx, q)
		x.NoError(err, q)
	}
	x.True(has("asset_date_created_id"))

	x.NoError(cli.Migrate(ctx, s))
	x.False(has("asset_date_created_id"))
	x.False(has("placement_valid_from_parent_id"))
	x.True(has("asset_tenant_id_date_created_id"))
	x.True(has("placement_parent_id_valid_from"))
}
