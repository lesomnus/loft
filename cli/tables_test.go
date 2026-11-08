package cli_test

import (
	"context"
	"database/sql"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/lesomnus/payday/pdid"
	"github.com/lesomnus/payday/pdtest"
	entschema "github.com/protobuf-orm/ent/dialect/sql/schema"
	"github.com/stretchr/testify/require"

	"github.com/lesomnus/loft/cli"
	"github.com/lesomnus/loft/cmd"
	entmigrate "github.com/lesomnus/loft/internal/ent/migrate"
	"github.com/lesomnus/loft/internal/ent/tenant"
	"github.com/lesomnus/loft/server/offboard"
	"github.com/lesomnus/loft/server/pd"
)

// asTenantDomain makes a database of this build's shape into the one a build
// before D72 made: the workspaces' domains in `tenantdomain`, and what ent
// named after the table named after that -- its indexes, its primary key and
// its foreign key, and the label's foreign key to it.
//
// PostgreSQL renames each in place. SQLite renames no constraint, and keeps a
// table's in its definition, which is rewritten here as that build wrote it.
func asTenantDomain(ctx context.Context, t *testing.T, s *cmd.Server) {
	t.Helper()
	x := require.New(t)

	if s.Dialect == "postgres" {
		execOn(t, s.Db,
			`ALTER TABLE domain RENAME TO tenantdomain`,
			`ALTER INDEX domain_page RENAME TO tenantdomain_page`,
			`ALTER INDEX domain_host RENAME TO tenantdomain_host`,
			`ALTER INDEX domain_pkey RENAME TO tenantdomain_pkey`,
			`ALTER TABLE tenantdomain RENAME CONSTRAINT domain_tenant_tenant TO tenantdomain_tenant_tenant`,
			`ALTER TABLE label RENAME CONSTRAINT label_domain_domain TO label_tenantdomain_domain`)
		// And what PostgreSQL 18 named after the table it made them in: each
		// column's NOT NULL, `tenantdomain_<column>_not_null` in that build's.
		// PostgreSQL 17 keeps no such constraint.
		for _, n := range names(t, s.Db, `SELECT conname FROM pg_constraint WHERE conrelid = 'tenantdomain'::regclass AND contype = 'n'`) {
			rest, ok := strings.CutPrefix(n, "domain_")
			x.True(ok, "a NOT NULL of the table named %s", n)
			execOn(t, s.Db, `ALTER TABLE tenantdomain RENAME CONSTRAINT `+n+` TO tenantdomain_`+rest)
		}
		return
	}

	conn, err := s.Db.Conn(ctx)
	x.NoError(err)
	defer conn.Close()
	for _, q := range []string{
		`ALTER TABLE domain RENAME TO tenantdomain`,
		`DROP INDEX domain_page`,
		`DROP INDEX domain_host`,
		`CREATE INDEX tenantdomain_page ON tenantdomain (tenant_id, date_created, id)`,
		`CREATE UNIQUE INDEX tenantdomain_host ON tenantdomain (host) WHERE date_erased IS NULL`,
		`PRAGMA writable_schema = ON`,
		`UPDATE sqlite_master SET sql = replace(sql, 'domain_tenant_tenant', 'tenantdomain_tenant_tenant') WHERE type = 'table' AND name = 'tenantdomain'`,
		`UPDATE sqlite_master SET sql = replace(sql, 'label_domain_domain', 'label_tenantdomain_domain') WHERE type = 'table' AND name = 'label'`,
		`PRAGMA writable_schema = OFF`,
	} {
		_, err := conn.ExecContext(ctx, q)
		x.NoError(err, q)
	}
	var ok string
	x.NoError(conn.QueryRowContext(ctx, `PRAGMA integrity_check`).Scan(&ok))
	x.Equal("ok", ok)
}

// names is the first column of each row `q` answers.
func names(t *testing.T, db *sql.DB, q string) []string {
	t.Helper()

	rows, err := db.QueryContext(t.Context(), q)
	require.NoError(t, err, q)
	defer rows.Close()

	out := []string{}
	for rows.Next() {
		var v string
		require.NoError(t, rows.Scan(&v))
		out = append(out, v)
	}
	require.NoError(t, rows.Err())

	return out
}

// TestADomainsTableAnEarlierBuildMadeIsMovedToItsName (D72): a workspace's
// domains were `TenantDomain`, in `tenantdomain`, with everything ent named
// after the table named after that. Migrated, they are in `domain` with their
// rows, named as this build names them -- the database in this build's shape,
// a label printed with a domain still pointing at it, and the foreign key
// still holding -- and a second migration finds nothing to do.
func TestADomainsTableAnEarlierBuildMadeIsMovedToItsName(t *testing.T) {
	x := require.New(t)
	ctx := t.Context()

	x.Equal("domain", offboard.Renamed["tenantdomain"])

	c := cmd.Config{}
	c.Db.Driver, c.Db.Dsn = pdtest.DB(t)
	c.Watch.Broker = "memory"
	c.App.Files = t.TempDir()
	c.Auth.Roster.Db.Driver, c.Auth.Roster.Db.Dsn = pdtest.DB(t)
	run(t, cli.NewCmdInit(&c), "--tenant", "acme", "--login", "owner", "--password", "owner1234")

	s, err := cmd.Build(ctx, c)
	x.NoError(err)
	defer s.Close()
	is := func(kind, name string) bool {
		q := "SELECT count(*) FROM sqlite_master WHERE type = '" + kind + "' AND name = ?"
		if s.Dialect == "postgres" {
			q = "SELECT count(*) FROM pg_tables WHERE schemaname = current_schema() AND tablename = $1"
			if kind == "index" {
				q = "SELECT count(*) FROM pg_indexes WHERE schemaname = current_schema() AND indexname = $1"
			}
		}
		n := 0
		x.NoError(s.Db.QueryRowContext(ctx, q, name).Scan(&n))
		return n > 0
	}

	// A domain of the workspace's, and a label printed with it.
	tid, err := s.Ent.Tenant.Query().Where(tenant.Alias("acme")).OnlyId(ctx)
	x.NoError(err)
	now := time.Now()
	dom := pdid.New(pd.DomainDomain).Uuid()
	x.NoError(s.Ent.Domain.Create().SetId(dom).SetTenantId(tid).
		SetHost("assets.acme.example").SetPurpose("label").SetState("active").SetToken("loft-test").SetSource("custom").
		SetDateUpdated(now).SetDateCreated(now).Exec(ctx))
	label := pdid.New(pd.LabelDomain).Uuid()
	x.NoError(s.Ent.Label.Create().SetId(label).SetTenantId(tid).SetState("new").SetBatch("b1").SetDomainId(dom).
		SetDateUpdated(now).SetDateCreated(now).Exec(ctx))

	asTenantDomain(ctx, t, s)
	x.True(is("table", "tenantdomain"))
	x.False(is("table", "domain"))
	x.Error(entschema.Check(ctx, s.Db, s.Dialect, entmigrate.Tables), "the build before's database is in this build's shape")

	x.NoError(cli.Migrate(ctx, s))
	x.False(is("table", "tenantdomain"), "the old table stayed")
	x.NoError(entschema.Check(ctx, s.Db, s.Dialect, entmigrate.Tables))
	for _, n := range []string{"tenantdomain_page", "tenantdomain_host", "tenantdomain_pkey"} {
		x.False(is("index", n), "%s stayed beside the one this build makes", n)
	}
	x.True(is("index", "domain_page"))
	x.True(is("index", "domain_host"))
	if s.Dialect == "postgres" {
		// Each of its table, which is this test's: the database holds other
		// tests' beside it, each with its own.
		for _, c := range []struct{ name, table string }{
			{"domain_tenant_tenant", "domain"},
			{"label_domain_domain", "label"},
			{"domain_pkey", "domain"},
		} {
			n := 0
			x.NoError(s.Db.QueryRowContext(ctx, `SELECT count(*) FROM pg_constraint WHERE conname = $1 AND conrelid = $2::regclass`, c.name, c.table).Scan(&n))
			x.Equal(1, n, "constraint %s of %s", c.name, c.table)
		}
		// And nothing in its schema is still called after the table it was:
		// a database migrated is named as one this build made.
		x.Empty(names(t, s.Db, `
			SELECT c.conname FROM pg_constraint c JOIN pg_namespace n ON n.oid = c.connamespace
			WHERE n.nspname = current_schema() AND c.conname LIKE '%tenantdomain%'
			UNION ALL
			SELECT indexname FROM pg_indexes WHERE schemaname = current_schema() AND indexname LIKE '%tenantdomain%'`),
			"named after the table it was")
	}

	d, err := s.Ent.Domain.Get(ctx, dom)
	x.NoError(err, "the domain is not in the table this build reads")
	x.Equal("assets.acme.example", d.Host)
	l, err := s.Ent.Label.Get(ctx, label)
	x.NoError(err)
	x.Equal(dom, l.DomainId, "the label no longer points at its domain")

	// And what holds the two together still does.
	err = s.Ent.Label.Create().SetId(pdid.New(pd.LabelDomain).Uuid()).SetTenantId(tid).SetState("new").SetBatch("b2").
		SetDomainId(pdid.New(pd.DomainDomain).Uuid()).SetDateUpdated(now).SetDateCreated(now).Exec(ctx)
	x.Error(err, "a label printed with a domain there is not")

	x.NoError(cli.Migrate(ctx, s), "a second time")
	x.NoError(entschema.Check(ctx, s.Db, s.Dialect, entmigrate.Tables))
}

// TestADomainsTableUnderBothNames: a table under both its names is a
// migration of one build made beside what another left. The empty one goes
// -- the new one a migration made first, or the old one a build before made
// again once this one had moved it -- and with rows under both, the migration
// stops and changes nothing: which rows are the table's is for somebody to
// decide.
func TestADomainsTableUnderBothNames(t *testing.T) {
	setup := func(t *testing.T) (*require.Assertions, context.Context, *cmd.Server, func(string) int) {
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
		t.Cleanup(func() { s.Close() })

		tid, err := s.Ent.Tenant.Query().Where(tenant.Alias("acme")).OnlyId(ctx)
		x.NoError(err)
		now := time.Now()
		x.NoError(s.Ent.Domain.Create().SetId(pdid.New(pd.DomainDomain).Uuid()).SetTenantId(tid).
			SetHost("assets.acme.example").SetPurpose("label").SetState("active").SetToken("loft-test").SetSource("custom").
			SetDateUpdated(now).SetDateCreated(now).Exec(ctx))

		rows := func(table string) int {
			n := -1
			if err := s.Db.QueryRowContext(ctx, "SELECT count(*) FROM "+table).Scan(&n); err != nil {
				return -1
			}
			return n
		}
		return x, ctx, s, rows
	}

	t.Run("the new one empty, made by a migration that ran first", func(t *testing.T) {
		x, ctx, s, rows := setup(t)
		asTenantDomain(ctx, t, s)
		execOn(t, s.Db, `CREATE TABLE domain (id uuid NOT NULL, PRIMARY KEY (id))`)

		x.NoError(cli.Migrate(ctx, s))
		x.Equal(1, rows("domain"))
		x.Equal(-1, rows("tenantdomain"), "the old table stayed")
		x.NoError(entschema.Check(ctx, s.Db, s.Dialect, entmigrate.Tables))
	})

	t.Run("the old one empty, made again by a build before", func(t *testing.T) {
		x, ctx, s, rows := setup(t)
		execOn(t, s.Db, `CREATE TABLE tenantdomain (id uuid NOT NULL, PRIMARY KEY (id))`)

		x.NoError(cli.Migrate(ctx, s))
		x.Equal(1, rows("domain"))
		x.Equal(-1, rows("tenantdomain"), "the empty old table stayed")
		x.NoError(entschema.Check(ctx, s.Db, s.Dialect, entmigrate.Tables))
	})

	t.Run("rows under both", func(t *testing.T) {
		x, ctx, s, rows := setup(t)
		execOn(t, s.Db,
			`CREATE TABLE tenantdomain (id uuid NOT NULL, PRIMARY KEY (id))`,
			`INSERT INTO tenantdomain (id) SELECT id FROM domain`)

		x.ErrorContains(cli.Migrate(ctx, s), "under both its names")
		x.Equal(1, rows("domain"))
		x.Equal(1, rows("tenantdomain"))
	})
}

// asColumnsBefore makes a database of this build's shape into the one a build
// before D76 made: each column renamed since under the name it had then
// (offboard.RenamedColumns, and payday's own) -- and on PostgreSQL 18 a NOT
// NULL named after it, as that build's would have been. Every column renamed
// so far takes NULL and has none; one that does not is held by the test below
// to have no constraint left named after its old name, which a rename would
// leave.
func asColumnsBefore(t *testing.T, s *cmd.Server) {
	t.Helper()
	q := func(v string) string { return `"` + v + `"` }
	for _, c := range slices.Concat(pd.ColumnRenames, offboard.RenamedColumns) {
		execOn(t, s.Db, `ALTER TABLE `+q(c.Table)+` RENAME COLUMN `+q(c.To)+` TO `+q(c.From))
		if s.Dialect != "postgres" {
			continue
		}
		// PostgreSQL 17 keeps no such constraint.
		for _, n := range names(t, s.Db, `SELECT conname FROM pg_constraint WHERE conrelid = '`+c.Table+`'::regclass AND contype = 'n' AND conname = '`+c.Table+`_`+c.To+`_not_null'`) {
			execOn(t, s.Db, `ALTER TABLE `+q(c.Table)+` RENAME CONSTRAINT `+q(n)+` TO `+q(c.Table+"_"+c.From+"_not_null"))
		}
	}
}

// TestColumnsAnEarlierBuildMadeAreMovedToTheirNames (D76): every time Loft
// keeps is a `date_*`, and a database a build before made keeps them under the
// names it had then. Migrated, each is under its name now with what it held
// -- the database in this build's shape, and on PostgreSQL nothing named
// after the column it was -- and a second migration finds nothing to do.
func TestColumnsAnEarlierBuildMadeAreMovedToTheirNames(t *testing.T) {
	x := require.New(t)
	ctx := t.Context()

	c := cmd.Config{}
	c.Db.Driver, c.Db.Dsn = pdtest.DB(t)
	c.Watch.Broker = "memory"
	c.App.Files = t.TempDir()
	c.Auth.Roster.Db.Driver, c.Auth.Roster.Db.Dsn = pdtest.DB(t)
	run(t, cli.NewCmdInit(&c), "--tenant", "acme", "--login", "owner", "--password", "owner1234", "--demo")

	s, err := cmd.Build(ctx, c)
	x.NoError(err)
	defer s.Close()

	// What each holds, by its row, under the name it has: as text, which is
	// what it was before and after alike.
	held := func(table, column string) []string {
		rows, err := s.Db.QueryContext(ctx, `SELECT id, "`+column+`" FROM "`+table+`" ORDER BY id`)
		x.NoError(err)
		defer rows.Close()
		out := []string{}
		for rows.Next() {
			var id, v any
			x.NoError(rows.Scan(&id, &v))
			out = append(out, fmt.Sprintf("%v=%v", id, v))
		}
		x.NoError(rows.Err())
		return out
	}
	cs := slices.Concat(pd.ColumnRenames, offboard.RenamedColumns)
	before := map[string][]string{}
	some := 0
	for _, c := range cs {
		before[c.Table+"."+c.To] = held(c.Table, c.To)
		some += len(before[c.Table+"."+c.To])
	}
	x.Positive(some, "the demo wrote no row with a column renamed")

	asColumnsBefore(t, s)
	x.Error(entschema.Check(ctx, s.Db, s.Dialect, entmigrate.Tables), "the build before's database is in this build's shape")

	x.NoError(cli.Migrate(ctx, s))
	x.NoError(entschema.Check(ctx, s.Db, s.Dialect, entmigrate.Tables))
	for _, c := range cs {
		x.Equal(before[c.Table+"."+c.To], held(c.Table, c.To), "%s.%s, which was %s", c.Table, c.To, c.From)
	}
	if s.Dialect == "postgres" {
		for _, c := range cs {
			x.Empty(names(t, s.Db, `
				SELECT c.conname FROM pg_constraint c JOIN pg_namespace n ON n.oid = c.connamespace
				WHERE n.nspname = current_schema() AND c.conname = '`+c.Table+`_`+c.From+`_not_null'`),
				"named after the column %s.%s was", c.Table, c.From)
		}
	}

	x.NoError(cli.Migrate(ctx, s), "a second time")
	x.NoError(entschema.Check(ctx, s.Db, s.Dialect, entmigrate.Tables))
	for _, c := range cs {
		x.Equal(before[c.Table+"."+c.To], held(c.Table, c.To), "%s.%s, migrated again", c.Table, c.To)
	}
}
