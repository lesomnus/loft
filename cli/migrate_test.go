package cli_test

import (
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lesomnus/payday/pdtest"
	"github.com/lesomnus/payday/valid"
	entschema "github.com/protobuf-orm/ent/dialect/sql/schema"
	"github.com/stretchr/testify/require"

	"github.com/lesomnus/loft/cli"
	"github.com/lesomnus/loft/cmd"
	entmigrate "github.com/lesomnus/loft/internal/ent/migrate"
)

// TestADeploymentMigratesBeforeItServes: `loft migrate` brings a database
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

// TestAMigrationOpensOnlyTheDatabases: `loft migrate` brings the tables along
// where no file, archive or key a server needs is to be had -- which is where
// it runs in Kubernetes, before a pod serves, with none of its volumes mounted
// (deploy/k8s). It reads no key at all: so it migrates with keys that are not
// there, where a server could not start.
func TestAMigrationOpensOnlyTheDatabases(t *testing.T) {
	x := require.New(t)
	ctx := t.Context()

	c := cmd.Config{}
	c.Db.Driver, c.Db.Dsn = pdtest.DB(t)
	c.Watch.Broker = "memory"
	c.Auth.Roster.Db.Driver, c.Auth.Roster.Db.Dsn = pdtest.DB(t)

	// Each of them is what a server cannot start without: somewhere to keep
	// its files, the key it signs the trail with, and the key it digests the
	// trail's rows with. None of them is to be had here.
	blocked := filepath.Join(t.TempDir(), "blocked")
	x.NoError(os.WriteFile(blocked, nil, 0o600))
	none := map[string]func(c *cmd.Config){
		"its files":    func(c *cmd.Config) { c.App.Files = filepath.Join(blocked, "files") },
		"the trail's":  func(c *cmd.Config) { c.Audit.Checkpoints.Key = filepath.Join(t.TempDir(), "absent.pem") },
		"the digest's": func(c *cmd.Config) { c.Audit.Digest.Key = filepath.Join(t.TempDir(), "absent.key") },
	}
	for what, without := range none {
		c := c
		c.App.Files = t.TempDir()
		without(&c)
		_, err := cmd.Build(ctx, c)
		x.Error(err, "a server without %s", what)
	}
	for _, without := range none {
		without(&c)
	}

	x.Contains(run(t, cli.NewCmdMigrate(&c)), "this build's shape")

	// And what it made is the shape a server checks for.
	c.App.Files = t.TempDir()
	c.Audit.Checkpoints.Key = ""
	c.Audit.Digest.Key = ""
	s, err := cmd.Build(ctx, c)
	x.NoError(err)
	defer s.Close()
	x.NoError(entschema.Check(ctx, s.Db, s.Dialect, entmigrate.Tables))
}

// TestAnIndexWhoseBuildDidNotFinishIsRefused (payday#176): docs/dev/postgres.md tells the
// operator of a big deployment to build an index a new build declares ahead
// of the migration, `CONCURRENTLY`, in Loft's database and in the embedded
// roster's. A build that does not finish is left in the catalog,
// invalid -- kept up by every write and read by nothing -- and a migration
// found it by its name and said the database was in this build's shape.
// `loft migrate` refuses it now, in either database, naming it and what to run;
// and what it says to run makes it.
func TestAnIndexWhoseBuildDidNotFinishIsRefused(t *testing.T) {
	x := require.New(t)
	ctx := t.Context()

	c := cmd.Config{}
	c.Db.Driver, c.Db.Dsn = pdtest.DB(t)
	c.Watch.Broker = "memory"
	c.App.Files = t.TempDir()
	c.Auth.Roster.Db.Driver, c.Auth.Roster.Db.Dsn = pdtest.DB(t)
	x.Contains(run(t, cli.NewCmdMigrate(&c)), "this build's shape")
	if c.Db.Driver != "pgx" {
		t.Skipf("only PostgreSQL keeps an index a build did not finish; set %s", pdtest.Postgres)
	}

	for _, at := range []struct {
		name, driver, dsn string
		// says is what the refusal is prefixed with, which is how the
		// operator knows which database to run it in.
		says string
	}{
		{"loft's", c.Db.Driver, c.Db.Dsn, ""},
		{"the embedded roster's", c.Auth.Roster.Db.Driver, c.Auth.Roster.Db.Dsn, "embedded roster: "},
	} {
		t.Run(at.name, func(t *testing.T) {
			x := require.New(t)
			db, err := sql.Open(at.driver, at.dsn)
			x.NoError(err)
			defer db.Close()

			// As a build of it is left that a write on the table held up past
			// the session's timeout; and the same statement again, which finds
			// it there and skips it.
			build := "CREATE INDEX CONCURRENTLY IF NOT EXISTS audit_by_time ON audit (date_created, id)"
			execOn(t, db, "DROP INDEX audit_by_time")
			cancelled(t, db, "audit", build)
			execOn(t, db, build)
			x.False(isValidIndex(t, db, "audit_by_time"))

			err = cli.NewCmdMigrate(&c).Run(ctx, nil)
			x.ErrorIs(err, valid.ErrInvalid)
			x.True(strings.HasPrefix(err.Error(), at.says+valid.ErrInvalid.Error()), err.Error())
			x.ErrorContains(err, "audit_by_time, on audit")

			// What it says to run, as it says to: a statement at a time,
			// outside a transaction.
			statements := []string{}
			for _, line := range strings.Split(err.Error(), "\n") {
				if line = strings.TrimSpace(line); strings.HasSuffix(line, ";") && !strings.HasPrefix(line, "--") {
					statements = append(statements, line)
				}
			}
			x.Len(statements, 2)
			execOn(t, db, statements...)
			x.True(isValidIndex(t, db, "audit_by_time"))
			x.Contains(run(t, cli.NewCmdMigrate(&c)), "this build's shape")
		})
	}
}

// cancelled runs `q`, a build `CONCURRENTLY` on `table`, as a timeout leaves
// one: a write is open on the table, the build waits for it to end before it
// reads a row, and the session's `statement_timeout` runs out first.
func cancelled(t *testing.T, db *sql.DB, table string, q string) {
	t.Helper()
	x := require.New(t)
	ctx := t.Context()

	tx, err := db.BeginTx(ctx, nil)
	x.NoError(err)
	defer tx.Rollback()
	_, err = tx.ExecContext(ctx, "LOCK TABLE "+table+" IN ROW EXCLUSIVE MODE")
	x.NoError(err)

	conn, err := db.Conn(ctx)
	x.NoError(err)
	defer conn.Close()
	_, err = conn.ExecContext(ctx, "SET statement_timeout = '200ms'")
	x.NoError(err)
	_, err = conn.ExecContext(ctx, q)
	x.ErrorContains(err, "statement timeout")
	_, err = conn.ExecContext(ctx, "RESET statement_timeout")
	x.NoError(err)
}

// isValidIndex says PostgreSQL holds an index called `name` and uses it.
func isValidIndex(t *testing.T, db *sql.DB, name string) bool {
	t.Helper()

	ok := false
	require.NoError(t, db.QueryRowContext(t.Context(), `SELECT i.indisvalid FROM pg_index i
		JOIN pg_class c ON c.oid = i.indexrelid JOIN pg_namespace n ON n.oid = c.relnamespace
		WHERE n.nspname = current_schema() AND c.relname = $1`, name).Scan(&ok))

	return ok
}

func execOn(t *testing.T, db *sql.DB, qs ...string) {
	t.Helper()

	for _, q := range qs {
		_, err := db.ExecContext(t.Context(), q)
		require.NoError(t, err, q)
	}
}
