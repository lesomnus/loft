package cli_test

import (
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/lesomnus/payday/pdid"
	"github.com/lesomnus/payday/pdtest"
	"github.com/lesomnus/payday/rename"
	"github.com/lesomnus/payday/valid"
	entschema "github.com/protobuf-orm/ent/dialect/sql/schema"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/timestamppb"

	app "github.com/lesomnus/rove"
	"github.com/lesomnus/rove/cli"
	"github.com/lesomnus/rove/cmd"
	entmigrate "github.com/lesomnus/rove/internal/ent/migrate"
	"github.com/lesomnus/rove/internal/ent/tenant"
	"github.com/lesomnus/rove/server/offboard"
	"github.com/lesomnus/rove/server/pd"
	"github.com/lesomnus/rove/server/retention"
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
// it runs in Kubernetes, before a pod serves, with none of its volumes mounted
// and the digest's key alone beside it (deploy/k8s). That key too it reads
// only when a migration has rows to put on the trail ([cmd.Server.Contents]),
// and a database nothing has written to has none: so a fresh one migrates
// with a key that is not there, where a server could not start.
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

// TestAMigrationReadsTheDigestsKeyWhenItIsAsked: a row a migration rewrites
// beneath the servers is put on the trail as the act it is (`pd.Record`),
// digested with the deployment's key -- which `rove migrate` reads when a step
// first asks for it, and not before ([cmd.Server.Contents]). A key that
// cannot be read is said then, which stops the step that asked; a deployment
// that names no key records with no digest; and what was read once is what
// every step is told. A server read it before it built, and is told the same.
func TestAMigrationReadsTheDigestsKeyWhenItIsAsked(t *testing.T) {
	x := require.New(t)
	ctx := t.Context()

	c := cmd.Config{}
	c.Db.Driver, c.Db.Dsn = pdtest.DB(t)
	c.Watch.Broker = "memory"
	c.App.Files = t.TempDir()
	c.Auth.Roster.Db.Driver, c.Auth.Roster.Db.Dsn = pdtest.DB(t)
	opened := func(key string) *cmd.Server {
		c := c
		c.Audit.Digest.Key = key
		s, err := cmd.Open(ctx, c)
		x.NoError(err, "opened with %q, which it does not read", key)
		return s
	}

	s := opened(filepath.Join(t.TempDir(), "absent.key"))
	_, err := s.Contents()
	x.ErrorContains(err, "absent.key")
	s.Close()

	s = opened("")
	cs, err := s.Contents()
	x.NoError(err)
	x.Nil(cs.Digest, "no key, and no digest")
	s.Close()

	key := filepath.Join(t.TempDir(), "digest.key")
	b := make([]byte, 32)
	_, err = rand.Read(b)
	x.NoError(err)
	x.NoError(os.WriteFile(key, []byte(base64.StdEncoding.EncodeToString(b)+"\n"), 0o600))
	s = opened(key)
	cs, err = s.Contents()
	x.NoError(err)
	x.NotNil(cs.Digest)
	x.NoError(os.Rename(key, key+".gone"))
	again, err := s.Contents()
	x.NoError(err, "read once")
	x.Equal(cs.Digest.Fingerprint(), again.Digest.Fingerprint())
	s.Close()

	x.NoError(os.Rename(key+".gone", key))
	c.Audit.Digest.Key = key
	built, err := cmd.Build(ctx, c)
	x.NoError(err)
	defer built.Close()
	cs, err = built.Contents()
	x.NoError(err)
	x.Equal(again.Digest.Fingerprint(), cs.Digest.Fingerprint())
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

// TestAnIndexWhoseBuildDidNotFinishIsRefused (payday#176): README tells the
// operator of a big deployment to build `audit_by_time` and `access_by_time`
// ahead of the migration, `CONCURRENTLY`, in Rove's database and in the
// embedded roster's. A build that does not finish is left in the catalog,
// invalid -- kept up by every write and read by nothing -- and a migration
// found it by its name and said the database was in this build's shape.
// `rove migrate` refuses it now, in either database, naming it and what to run;
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
		{"rove's", c.Db.Driver, c.Db.Dsn, ""},
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

// TestATableAnEarlierBuildHadIsDropped: a table this build no longer has
// (offboard.Dropped) goes when the database is migrated, rows and all. A
// migration only adds, and a row of it left pointing at its workspace would
// keep the workspace from being destroyed -- and one naming an account, which
// nothing reads or destroys any more, would be kept for good.
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

	// And the agreements it kept before roster's ledger did (D37): an
	// account's, of no workspace. Agreed again, to roster.
	x.Contains(offboard.Dropped, "consent")
	for _, q := range []string{
		"CREATE TABLE consent (id uuid PRIMARY KEY, account uuid NOT NULL, document varchar NOT NULL, version varchar NOT NULL, client varchar NOT NULL)",
		"INSERT INTO consent (id, account, document, version, client) SELECT id, id, 'terms', '2026-10-01', 'web' FROM tenant WHERE alias = 'acme'",
	} {
		_, err := s.Db.ExecContext(ctx, q)
		x.NoError(err, q)
	}
	x.True(has("consent"))

	x.NoError(cli.Migrate(ctx, s))
	x.False(has("usagesnapshot"), "a table this build does not have stayed")
	x.False(has("consent"), "the agreements this build does not keep stayed")
	x.NoError(cli.Migrate(ctx, s), "a second time")

	// And the workspace can be destroyed.
	tid, err := s.Ent.Tenant.Query().Where(tenant.Alias("acme")).OnlyId(ctx)
	x.NoError(err)
	_, err = s.Offboard(c).Purge(ctx, pdid.Id(tid), false)
	x.NoError(err)
	x.False(s.Ent.Tenant.Query().Where(tenant.Id(tid)).ExistX(ctx))
}

// TestTheContractsTermsMoveOntoTheWorkspace (D36): what a workspace's
// contracts said of how long it keeps its history and its access record and
// may be restored once closed moves onto the workspace once, as its own
// settings -- the contract in force's, and the one before it as what was kept
// before, from when the one in force took effect, for the grace to hold a
// shorter window to -- and the contracts' columns for them go.
func TestTheContractsTermsMoveOntoTheWorkspace(t *testing.T) {
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
	has := func(column string) bool {
		q := "SELECT count(*) FROM pragma_table_info('tenantcontract') WHERE name = ?"
		if s.Dialect == "postgres" {
			q = "SELECT count(*) FROM information_schema.columns WHERE table_schema = current_schema() AND table_name = 'tenantcontract' AND column_name = $1"
		}
		n := 0
		x.NoError(s.Db.QueryRowContext(ctx, q, column).Scan(&n))
		return n > 0
	}
	exec := func(q string, args ...any) {
		if s.Dialect == "postgres" {
			for i := range args {
				q = strings.Replace(q, "?", fmt.Sprintf("$%d", i+1), 1)
			}
		}
		_, err := s.Db.ExecContext(ctx, q, args...)
		x.NoError(err, q)
	}

	// The columns a build before this had, and its contracts: one from long
	// ago that kept more, one in force since ten days ago, and one signed for
	// next month.
	for _, col := range []string{"view_days", "keep_days", "grace_days", "access_keep_days", "restore_days"} {
		exec("ALTER TABLE tenantcontract ADD COLUMN " + col + " integer NOT NULL DEFAULT 0")
	}
	tn, err := s.Ent.Tenant.Query().Where(tenant.Alias("acme")).Only(ctx)
	x.NoError(err)
	now := time.Now().Truncate(time.Second)
	contract := func(name string, effective time.Time, keep, access, restore uint32) {
		v, err := s.Base.TenantContract().Add(ctx, app.TenantContractAddRequest_builder{
			Tenant:        app.TenantRef_builder{Id: tn.Id[:]}.Build(),
			Name:          name,
			DateEffective: timestamppb.New(effective),
		}.Build())
		x.NoError(err)
		exec("UPDATE tenantcontract SET view_days = 365, keep_days = ?, access_keep_days = ?, restore_days = ?, grace_days = 7 WHERE id = ?",
			keep, access, restore, pdid.Id(v.GetId()).String())
	}
	contract("old", now.Add(-100*24*time.Hour), 730, 400, 0)
	contract("now", now.Add(-10*24*time.Hour), 365, 0, 90)
	contract("next", now.Add(10*24*time.Hour), 30, 30, 30)

	x.NoError(cli.Migrate(ctx, s))
	for _, col := range []string{"view_days", "keep_days", "grace_days", "access_keep_days", "restore_days"} {
		x.False(has(col), "the contracts kept %s", col)
	}
	x.NoError(cli.Migrate(ctx, s), "a second time")

	got, err := s.Ent.Tenant.Get(ctx, tn.Id)
	x.NoError(err)
	x.EqualValues(365, got.KeepDays, "the contract in force's")
	x.EqualValues(0, got.AccessKeepDays)
	x.EqualValues(90, got.RestoreDays)
	x.EqualValues(730, got.KeepDaysBefore, "what the one before kept")
	x.NotNil(got.DateKeepShortened)
	x.WithinDuration(now.Add(-10*24*time.Hour), *got.DateKeepShortened, time.Second, "from when the one in force took effect")
	x.EqualValues(400, got.AccessKeepDaysBefore)
	x.NotNil(got.DateAccessKeepShortened)

	// So the grace holds the longer window from then.
	w, err := retention.Of(ctx, s.Ent, pdid.Id(tn.Id), now, retention.Defaults{Grace: 30 * 24 * time.Hour})
	x.NoError(err)
	x.Equal(730*24*time.Hour, w.Keep)
	w, err = retention.Of(ctx, s.Ent, pdid.Id(tn.Id), now.Add(20*24*time.Hour), retention.Defaults{Grace: 30 * 24 * time.Hour})
	x.NoError(err)
	x.Equal(365*24*time.Hour, w.Keep)

	// And a contract is written as it is now.
	_, err = s.Base.TenantContract().Add(ctx, app.TenantContractAddRequest_builder{
		Tenant: app.TenantRef_builder{Id: tn.Id[:]}.Build(),
		Name:   "after",
	}.Build())
	x.NoError(err)
}

// TestTheContractsTermsMoveFromABuildLongBefore (D36): on SQLite, a contract
// table from a build long before this one -- before `purge_floors`, with its
// index under the name of its columns, its dates in nanoseconds -- is made
// again by the schema's migration, and what is made again has the columns the
// schema declares and no others. Its terms were set aside before, and are on
// the workspace after; and nothing of them is left behind.
func TestTheContractsTermsMoveFromABuildLongBefore(t *testing.T) {
	x := require.New(t)
	ctx := t.Context()

	c := cmd.Config{}
	c.Db.Driver = "sqlite3"
	c.Db.Dsn = "file:" + filepath.Join(t.TempDir(), "rove.db") + "?_pragma=foreign_keys(1)&_pragma=busy_timeout(10000)"
	c.Watch.Broker = "memory"
	c.App.Files = t.TempDir()
	c.Auth.Roster.Db.Driver, c.Auth.Roster.Db.Dsn = "sqlite3", "file:"+filepath.Join(t.TempDir(), "roster.db")
	run(t, cli.NewCmdInit(&c), "--tenant", "acme", "--login", "owner", "--password", "owner1234")

	s, err := cmd.Build(ctx, c)
	x.NoError(err)
	defer s.Close()
	exec := func(q string, args ...any) {
		_, err := s.Db.ExecContext(ctx, q, args...)
		x.NoError(err, q)
	}

	// The table as staging's build (a11e37d) made it, and a contract in it as
	// that build wrote one: in force since yesterday, its dates in
	// nanoseconds, keeping its history two years.
	var tenantId string
	x.NoError(s.Db.QueryRowContext(ctx, "SELECT id FROM tenant WHERE alias = 'acme'").Scan(&tenantId))
	exec("DROP TABLE tenantcontract")
	exec("CREATE TABLE `tenantcontract` (`id` uuid NOT NULL, `name` text NOT NULL, `desc` text NOT NULL, `view_days` integer NOT NULL, " +
		"`keep_days` integer NOT NULL, `grace_days` integer NOT NULL, `date_effective` datetime NULL, `access_keep_days` integer NULL DEFAULT (0), " +
		"`restore_days` integer NULL DEFAULT (0), `date_erased` datetime NULL, `date_created` datetime NULL, `tenant_id` uuid NOT NULL, " +
		"PRIMARY KEY (`id`), CONSTRAINT `tenantcontract_tenant_tenant` FOREIGN KEY (`tenant_id`) REFERENCES `tenant` (`id`) ON DELETE NO ACTION)")
	exec("CREATE INDEX `tenantcontract_tenant_id_date_effective_id` ON `tenantcontract` (`tenant_id`, `date_effective`, `id`)")
	effective := time.Now().Add(-24 * time.Hour)
	id := pdid.New(pd.TenantContractDomain)
	exec("INSERT INTO tenantcontract (id, name, desc, view_days, keep_days, grace_days, date_effective, access_keep_days, restore_days, date_created, tenant_id) "+
		"VALUES (?, 'before', '', 365, 730, 0, ?, 400, 90, ?, ?)", id.String(), effective.UnixNano(), effective.UnixNano(), tenantId)
	var stored string
	x.NoError(s.Db.QueryRowContext(ctx, "SELECT typeof(date_effective) FROM tenantcontract").Scan(&stored))
	x.Equal("integer", stored)

	x.NoError(cli.Migrate(ctx, s))

	tn, err := s.Ent.Tenant.Query().Where(tenant.Alias("acme")).Only(ctx)
	x.NoError(err)
	x.EqualValues(730, tn.KeepDays, "the contract's keep, lost with the table made again")
	x.EqualValues(400, tn.AccessKeepDays)
	x.EqualValues(90, tn.RestoreDays)
	v, err := s.Ent.TenantContract.Get(ctx, id.Uuid())
	x.NoError(err, "the contract, as the app reads it")
	x.WithinDuration(effective, v.DateEffective, time.Microsecond)

	for _, q := range []string{
		"SELECT count(*) FROM pragma_table_info('tenantcontract') WHERE name IN ('view_days', 'keep_days', 'grace_days', 'access_keep_days', 'restore_days')",
		"SELECT count(*) FROM sqlite_master WHERE type = 'table' AND name = 'rove_contract_terms'",
	} {
		n := 0
		x.NoError(s.Db.QueryRowContext(ctx, q).Scan(&n))
		x.Zero(n, q)
	}
	x.NoError(cli.Migrate(ctx, s), "a second time")
}
