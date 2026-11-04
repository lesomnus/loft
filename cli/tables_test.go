package cli_test

import (
	"context"
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
		for _, q := range []string{
			`SELECT count(*) FROM pg_constraint WHERE conname = 'domain_tenant_tenant'`,
			`SELECT count(*) FROM pg_constraint WHERE conname = 'label_domain_domain'`,
			`SELECT count(*) FROM pg_constraint WHERE conname = 'domain_pkey'`,
		} {
			n := 0
			x.NoError(s.Db.QueryRowContext(ctx, q).Scan(&n))
			x.Equal(1, n, q)
		}
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
