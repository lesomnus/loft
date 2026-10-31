package cli_test

import (
	"context"
	"testing"

	levyanv1 "github.com/lesomnus/levyan/api/levyan/v1"
	"github.com/lesomnus/payday/pdtest"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/lesomnus/loft/cli"
	"github.com/lesomnus/loft/cmd"
	"github.com/lesomnus/loft/internal/ent/tenant"
	"github.com/lesomnus/loft/internal/levyantest"
)

// TestAPurgeMeasuresTheWorkspaceFirst (D59): `loft tenant purge` and `retire`
// destroy as the sweep does, and so a deployment that bills hands levyan what
// the workspace held before anything of it is taken. A levyan that does not
// take it is the command failing with why, and the workspace as it was; one
// that does, and the workspace goes. A deployment that bills nobody says
// nothing of meters.
func TestAPurgeMeasuresTheWorkspaceFirst(t *testing.T) {
	x := require.New(t)
	ctx := context.Background()
	fake := levyantest.Start(t)

	c := cmd.Config{}
	c.Db.Driver, c.Db.Dsn = pdtest.DB(t)
	c.Watch.Broker = "memory"
	c.App.Files = t.TempDir()
	c.Auth.Roster.Db.Driver, c.Auth.Roster.Db.Dsn = pdtest.DB(t)
	run(t, cli.NewCmdInit(&c), "--tenant", "acme", "--login", "owner", "--password", "owner1234")

	out := run(t, cli.NewCmdTenant(&c), "purge", "--tenant", "acme")
	x.NotContains(out, "meters", "a deployment that bills nobody")

	c.App.Billing.Levyan = fake.Config()
	out = run(t, cli.NewCmdTenant(&c), "purge", "--tenant", "acme")
	x.Contains(out, "meters: what it held is measured and handed to levyan first")
	x.Empty(fake.Calls(), "a dry run measures nothing")

	fake.Answer(func(context.Context, *levyanv1.RecordRequest) (*levyanv1.RecordResponse, error) {
		return nil, status.Error(codes.Unavailable, "levyan is down")
	})
	err := cli.NewCmdTenant(&c).Run(ctx, []string{"purge", "--tenant", "acme", "--yes"})
	x.ErrorContains(err, "acme: closing: its meters could not be read and handed to levyan, so it is not destroyed")
	x.ErrorContains(err, "levyan is down")
	alive := func() bool {
		s, err := cmd.Build(ctx, c)
		x.NoError(err)
		defer s.Close()
		v, err := s.Ent.Tenant.Query().Where(tenant.Alias("acme")).Exist(ctx)
		x.NoError(err)
		return v
	}
	x.True(alive(), "nothing was taken")

	fake.Answer(nil)
	out = run(t, cli.NewCmdTenant(&c), "purge", "--tenant", "acme", "--yes")
	x.Contains(out, "acme: took")
	x.Contains(out, "meters: what it held was measured and handed to levyan first")
	x.False(alive())

	got := map[string]int64{}
	for _, r := range fake.Readings() {
		got[r.GetMeter()] = r.GetValue()
	}
	x.Equal(map[string]int64{
		"loft.assets.active": 0, "loft.attachments.bytes": 0, "loft.members": 1, "loft.history.retention_days": 0,
	}, got, "its owner, and nothing else")
}
