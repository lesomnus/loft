package cli_test

import (
	"context"
	"testing"

	"github.com/lesomnus/payday/pdid"
	"github.com/lesomnus/payday/pdtest"
	"github.com/stretchr/testify/require"

	app "github.com/lesomnus/rove"
	"github.com/lesomnus/rove/cli"
	"github.com/lesomnus/rove/cmd"
)

// TestEverySchemaHasTheBackstop: what `pg.sql` adds is asked after in the
// schema it is added to, so a second one in the same database -- every test
// has its own -- has it as well as the first.
func TestEverySchemaHasTheBackstop(t *testing.T) {
	x := require.New(t)
	ctx := context.Background()

	for range 2 {
		c := cmd.Config{}
		c.Db.Driver, c.Db.Dsn = pdtest.DB(t)
		if c.Db.Driver != "pgx" {
			t.Skip("PostgreSQL only; set PDTEST_POSTGRES")
		}
		c.Watch.Broker = "memory"
		c.App.Files = t.TempDir()
		c.Auth.Roster.Db.Driver, c.Auth.Roster.Db.Dsn = pdtest.DB(t)

		s, err := cmd.Build(ctx, c)
		x.NoError(err)
		x.NoError(cli.Migrate(ctx, s))
		for table, name := range map[string]string{
			"allocation":  "allocation_exclusive_no_overlap",
			"placement":   "placement_one_parent",
			"stewardship": "stewardship_one_per_role",
			"link":        "link_once",
		} {
			n := 0
			x.NoError(s.Db.QueryRowContext(ctx, "SELECT count(*) FROM pg_constraint WHERE conname = $1 AND conrelid = $2::regclass", name, table).Scan(&n))
			x.Equal(1, n, "%s on %s", name, table)
		}
		x.NoError(s.Close())
	}
}

// TestAnAccountIsInAWorkspaceOnce: two memberships of one account in one
// workspace are refused by PostgreSQL too, for the two writes at once the
// domain layer's check cannot see.
func TestAnAccountIsInAWorkspaceOnce(t *testing.T) {
	x := require.New(t)
	ctx := context.Background()

	c := cmd.Config{}
	c.Db.Driver, c.Db.Dsn = pdtest.DB(t)
	if c.Db.Driver != "pgx" {
		t.Skip("PostgreSQL only; set PDTEST_POSTGRES")
	}
	c.Watch.Broker = "memory"
	c.App.Files = t.TempDir()
	c.Auth.Roster.Db.Driver, c.Auth.Roster.Db.Dsn = pdtest.DB(t)

	s, err := cmd.Build(ctx, c)
	x.NoError(err)
	defer s.Close()
	x.NoError(cli.Migrate(ctx, s))

	ws, err := s.Base.Tenant().Add(ctx, app.TenantAddRequest_builder{Alias: "acme"}.Build())
	x.NoError(err)
	account := pdid.New(pdid.Domain(2))
	add := func(alias string) error {
		_, err := s.Base.Holder().Add(ctx, app.HolderAddRequest_builder{
			Tenant:  app.TenantRef_builder{Id: ws.GetId()}.Build(),
			Alias:   alias,
			Account: account.Bytes(),
		}.Build())
		return err
	}
	x.NoError(add("kim"))
	x.Error(add("kim-again"), "the same account twice in one workspace")
}
