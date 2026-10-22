package cli_test

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/lesomnus/payday/pdid"
	"github.com/lesomnus/payday/pdtest"
	"github.com/lesomnus/payday/rename"
	entschema "github.com/protobuf-orm/ent/dialect/sql/schema"
	"github.com/stretchr/testify/require"

	"github.com/lesomnus/rove/cli"
	"github.com/lesomnus/rove/cmd"
	entmigrate "github.com/lesomnus/rove/internal/ent/migrate"
	"github.com/lesomnus/rove/internal/ent/tenant"
	"github.com/lesomnus/rove/server/offboard"
	"github.com/lesomnus/rove/server/pd"
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

// TestAnIndexAnEarlierBuildMadeIsMovedToItsName: an index an earlier build
// stored under the name of its columns is moved to the name the schema
// declares when the database is migrated (payday#132), rather than made again
// beside it -- which is what the list of replaced indexes this replaces dropped
// by hand (#122). Every name it moves one to is an index this build declares,
// or every migration would rename into nothing.
func TestAnIndexAnEarlierBuildMadeIsMovedToItsName(t *testing.T) {
	x := require.New(t)
	ctx := t.Context()

	declared := map[string]*entschema.Index{}
	for _, tb := range entmigrate.Tables {
		for _, i := range tb.Indexes {
			declared[i.Name] = i
		}
	}
	for _, v := range pd.IndexRenames {
		x.Contains(declared, v.To, "%s is moved to an index this build does not declare", v.From)
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

	// A list's index, where the build before this one left it: under the
	// name of its columns.
	i := slices.IndexFunc(pd.IndexRenames, func(v rename.Index) bool { return v.Table == "asset" && v.To == "asset_page" })
	x.GreaterOrEqual(i, 0)
	page := pd.IndexRenames[i]
	cols := []string{}
	for _, c := range declared[page.To].Columns {
		cols = append(cols, c.Name)
	}
	for _, q := range []string{
		"DROP INDEX " + page.To,
		"CREATE INDEX " + page.From + " ON asset (" + strings.Join(cols, ", ") + ")",
	} {
		_, err := s.Db.ExecContext(ctx, q)
		x.NoError(err, q)
	}
	x.True(has(page.From))
	x.False(has(page.To))

	x.NoError(cli.Migrate(ctx, s))
	x.False(has(page.From), "the old name stayed beside the new one")
	x.True(has(page.To))
}

// TestATableAnEarlierBuildHadIsDropped: a table this build no longer has
// (offboard.Dropped) goes when the database is migrated, rows and all. A
// migration only adds, and a row of it left pointing at its workspace would
// keep the workspace from being destroyed.
func TestATableAnEarlierBuildHadIsDropped(t *testing.T) {
	x := require.New(t)
	ctx := t.Context()

	c := cmd.Config{}
	c.Db.Driver, c.Db.Dsn = pdtest.DB(t)
	c.Watch.Broker = "memory"
	c.App.Files = t.TempDir()
	c.Auth.Roster.Db.Driver, c.Auth.Roster.Db.Dsn = pdtest.DB(t)
	run(t, cli.NewCmdInit(&c), "--tenant", "acme", "--login", "owner", "--password", "owner1234")

	s, err := cmd.Build(ctx, c)
	x.NoError(err)
	defer s.Close()
	has := func(name string) bool {
		q := "SELECT count(*) FROM sqlite_master WHERE type = 'table' AND name = ?"
		if s.Dialect == "postgres" {
			q = "SELECT count(*) FROM information_schema.tables WHERE table_schema = current_schema() AND table_name = $1"
		}
		n := 0
		x.NoError(s.Db.QueryRowContext(ctx, q, name).Scan(&n))
		return n > 0
	}

	// The usage the build before wrote once a day, where it left it: a row
	// of the workspace's, pointing at it.
	x.Contains(offboard.Dropped, "usagesnapshot")
	for _, q := range []string{
		"CREATE TABLE usagesnapshot (id uuid PRIMARY KEY, day varchar NOT NULL, tenant_id uuid NOT NULL REFERENCES tenant (id))",
		"INSERT INTO usagesnapshot (id, day, tenant_id) SELECT id, '2026-10-01', id FROM tenant WHERE alias = 'acme'",
	} {
		_, err := s.Db.ExecContext(ctx, q)
		x.NoError(err, q)
	}
	x.True(has("usagesnapshot"))

	x.NoError(cli.Migrate(ctx, s))
	x.False(has("usagesnapshot"), "a table this build does not have stayed")
	x.NoError(cli.Migrate(ctx, s), "a second time")

	// And the workspace can be destroyed.
	tid, err := s.Ent.Tenant.Query().Where(tenant.Alias("acme")).OnlyId(ctx)
	x.NoError(err)
	_, err = s.Offboard(c).Purge(ctx, pdid.Id(tid), false)
	x.NoError(err)
	x.False(s.Ent.Tenant.Query().Where(tenant.Id(tid)).ExistX(ctx))
}
