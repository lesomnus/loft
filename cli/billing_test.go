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

// TestAPurgeHandsLevyanTheWorkspacesFinalReading (D59): `loft tenant purge`
// and `retire` destroy as the sweep does, and so a deployment that bills
// hands levyan what the workspace held, as its final reading, before
// anything of it is taken. A levyan that does not take it is a line saying
// so, and the workspace taken all the same -- a destruction waits for no
// billing service (Claude, in place of the rule that it was not taken). A
// deployment that bills nobody says nothing of meters.
func TestAPurgeHandsLevyanTheWorkspacesFinalReading(t *testing.T) {
	x := require.New(t)
	ctx := context.Background()
	fake := levyantest.Start(t)

	c := cmd.Config{}
	c.Db.Driver, c.Db.Dsn = pdtest.DB(t)
	c.Watch.Broker = "memory"
	c.App.Files = t.TempDir()
	c.Auth.Roster.Db.Driver, c.Auth.Roster.Db.Dsn = pdtest.DB(t)
	run(t, cli.NewCmdInit(&c), "--tenant", "acme", "--login", "owner", "--password", "owner1234")
	run(t, cli.NewCmdInit(&c), "--tenant", "beta", "--login", "owner")
	alive := func(alias string) bool {
		s, err := cmd.Build(ctx, c)
		x.NoError(err)
		defer s.Close()
		v, err := s.Ent.Tenant.Query().Where(tenant.Alias(alias)).Exist(ctx)
		x.NoError(err)
		return v
	}

	out := run(t, cli.NewCmdTenant(&c), "purge", "--tenant", "acme")
	x.NotContains(out, "meters", "a deployment that bills nobody")

	c.App.Billing.Levyan = fake.Config()
	out = run(t, cli.NewCmdTenant(&c), "purge", "--tenant", "acme")
	x.Contains(out, "meters: its final reading is measured and handed to levyan first; if levyan cannot take it, it goes all the same")
	x.Empty(fake.Calls(), "a dry run measures nothing")

	// levyan down: the purge goes on, and says what it could not do.
	fake.Answer(func(context.Context, *levyanv1.RecordRequest) (*levyanv1.RecordResponse, error) {
		return nil, status.Error(codes.Unavailable, "levyan is down")
	})
	out = run(t, cli.NewCmdTenant(&c), "purge", "--tenant", "acme", "--yes")
	x.Contains(out, "acme: took")
	x.Contains(out, "meters: its final reading could not be handed to levyan, and it went all the same; levyan bills it by its last daily reading")
	x.Contains(out, "levyan is down")
	x.NotContains(out, levyantest.Key)
	x.False(alive("acme"), "destroyed, with levyan down")

	// levyan up: the final reading is levyan's before the rows go.
	fake.Answer(nil)
	before := len(fake.Calls())
	out = run(t, cli.NewCmdTenant(&c), "purge", "--tenant", "beta", "--yes")
	x.Contains(out, "beta: took")
	x.Contains(out, "meters: its final reading was measured and handed to levyan first")
	x.False(alive("beta"))
	calls := fake.Calls()
	x.Len(calls, before+1)
	got := map[string]int64{}
	for _, r := range calls[before] {
		got[r.GetMeter()] = r.GetValue()
	}
	x.Equal(map[string]int64{
		"loft.assets.active": 0, "loft.attachments.bytes": 0, "loft.members": 1, "loft.history.retention_days": 0,
	}, got, "its owner, and nothing else")
}
