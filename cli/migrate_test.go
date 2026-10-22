package cli_test

import (
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
