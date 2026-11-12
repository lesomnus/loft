package cli_test

import (
	"bytes"
	"context"
	"encoding/base64"
	"path/filepath"
	"testing"

	levyanv1 "github.com/lesomnus/levyan/api/levyan/v1"
	"github.com/lesomnus/payday/pdid"
	"github.com/lesomnus/payday/trail"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/lesomnus/loft/cli"
	"github.com/lesomnus/loft/cmd"
	"github.com/lesomnus/loft/internal/ent"
	"github.com/lesomnus/loft/internal/ent/holder"
	"github.com/lesomnus/loft/internal/ent/levyannotice"
	"github.com/lesomnus/loft/internal/ent/tenant"
	"github.com/lesomnus/loft/internal/levyantest"
	"github.com/lesomnus/loft/server/levyan"
)

// TestAPurgeHandsLevyanTheWorkspacesFinalReading (D59): `tenant purge` and
// `retire` destroy as the sweep does, and so a deployment that bills hands
// levyan what the workspace held, as its final reading, before anything of it
// is taken. A levyan that does not take it is a line saying so, and the
// workspace taken all the same -- a destruction waits for no billing service
// (Claude, in place of the rule that it was not taken). A deployment that
// bills nobody says nothing of meters.
func TestAPurgeHandsLevyanTheWorkspacesFinalReading(t *testing.T) {
	x := require.New(t)
	ctx := context.Background()

	out, err := deploy(t).loft(t, "tenant", "purge", "acme")
	x.NoError(err, out)
	x.NotContains(out, "meters", "a deployment that bills nobody")

	fake := levyantest.Start(t)
	d := deployWith(t, func(c *cmd.Config) { c.App.Billing.Levyan = fake.Config() })
	cli.Elsewhere(t, d.c, "beta", "owner", false)
	alive := func(alias string) bool {
		v, err := d.s.Ent.Tenant.Query().Where(tenant.Alias(alias)).Exist(ctx)
		x.NoError(err)
		return v
	}

	out, err = d.loft(t, "tenant", "purge", "acme")
	x.NoError(err, out)
	x.Contains(out, "meters: its final reading is measured and handed to levyan first; if levyan cannot take it, it goes all the same")
	x.Empty(fake.Calls(), "a dry run measures nothing")

	// levyan down: the purge goes on, and says what it could not do.
	fake.Answer(func(context.Context, *levyanv1.RecordRequest) (*levyanv1.RecordResponse, error) {
		return nil, status.Error(codes.Unavailable, "levyan is down")
	})
	out, err = d.loft(t, "tenant", "purge", "--yes", "acme")
	x.NoError(err, out)
	x.Contains(out, "acme: took")
	x.Contains(out, "meters: its final reading could not be handed to levyan, and it went all the same; levyan bills it by its last daily reading")
	x.Contains(out, "levyan is down")
	x.NotContains(out, levyantest.Key)
	x.False(alive("acme"), "destroyed, with levyan down")

	// levyan up: the final reading is levyan's before the rows go.
	fake.Answer(nil)
	before := len(fake.Calls())
	out, err = d.loft(t, "tenant", "purge", "--yes", "beta")
	x.NoError(err, out)
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

// billed is a deployment that bills, by the fake levyan `fake`, its trail
// signed and its rows digested, and nothing in it yet.
func billed(t *testing.T, fake *levyantest.Levyan) cmd.Config {
	t.Helper()
	key, _, err := trail.GenerateKey()
	require.NoError(t, err)
	c := embeddedConfig(t)
	c.Audit.Checkpoints.Key = string(key)
	c.Audit.Digest.Key = "base64:" + base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{7}, 32))
	c.App.Billing.Levyan = fake.Config()
	return c
}

// notices is what levyan is to be told, in the order it happened.
func (d *deployment) notices(t *testing.T) []*ent.LevyanNotice {
	t.Helper()
	vs, err := d.s.Ent.LevyanNotice.Query().
		Where(levyannotice.KindNEQ(levyan.Past)).
		Order(ent.Asc(levyannotice.FieldDateOccurred), ent.Asc(levyannotice.FieldId)).
		All(t.Context())
	require.NoError(t, err)
	return vs
}

// TestWhatAnOperatorMakesTellsLevyanNothingOfWhoPays (D84): the workspace
// `loft init` puts up -- with `--demo`'s teams and people in it -- and put
// back by `tenant import` is told to levyan by nobody here: who made it, and
// who pays for it, is the portal's to tell it. What levyan grants one put
// back is copied at once. An operator's `holder add` writes as the
// deployment's work at their word, and a workspace levyan restricts takes it
// (D60).
func TestWhatAnOperatorMakesTellsLevyanNothingOfWhoPays(t *testing.T) {
	x := require.New(t)
	ctx := context.Background()
	fake := levyantest.Start(t)
	c := billed(t, fake)

	c.Auth.Roster.Tenant = "acme"
	_, err := ran(t, cli.NewCmdInit(&c), "--login", "owner", "--password", "owner1234", "--demo")
	x.NoError(err)
	d := entry(t, c)
	v, err := d.s.Ent.Tenant.Query().Where(tenant.Alias("acme")).Only(ctx)
	x.NoError(err)
	acme := pdid.Id(v.Id)
	x.Empty(d.notices(t), "nothing of who made it")

	// Restricted, and an operator's hand all the same.
	fake.Restrict(acme)
	_, err = d.s.Follower.Sync(ctx, acme)
	x.NoError(err)
	out, err := d.loft(t, "holder", "add", "--tenant", "acme", "--login", "kim", "--password", "kim-password")
	x.NoError(err, out)

	// Out, gone, and back: what levyan grants it copied, and nothing told.
	zip := filepath.Join(t.TempDir(), "acme.zip")
	out, err = d.loft(t, "--reason", "leaving", "tenant", "export", "--out", zip, "acme")
	x.NoError(err, out)
	out, err = d.loft(t, "tenant", "purge", "--yes", "acme")
	x.NoError(err, out)
	vs := d.notices(t)
	x.Len(vs, 1, "that it was destroyed (TestLevyanIsToldWhatAnOperatorEnds)")
	x.Equal(levyan.Destroyed, vs[0].Kind)
	dry, err := d.loft(t, "tenant", "import", "--file", zip)
	x.NoError(err, dry)
	x.NotContains(dry, "levyan grants")
	done, err := d.loft(t, "tenant", "import", "--file", zip, "--yes")
	x.NoError(err, done)
	x.Contains(done, "what levyan grants it is copied")
	x.Len(d.notices(t), 1, "nothing of who made it")
	n, err := d.s.Ent.Entitlement.Query().Count(ctx)
	x.NoError(err)
	x.Equal(1, n, "what levyan says of it, copied: the restriction")

	// And every row is what the trail says it is: the copy's, written by
	// the follower and recorded; the notices, which are kept off it.
	out, err = d.loft(t, "trail", "rows")
	x.NoError(err, out)
}

// TestLevyanIsToldWhatAnOperatorEnds (design 7.8): a workspace an operator
// closes and restores at the entry point (`tenant close|restore`) is written,
// in the transaction that does it, as what levyan is to be told; and one
// `tenant purge` destroys, before anything of it is taken -- which a dry run
// does not. `loft serve`'s teller tells them, in the order they happened, to
// a levyan the portal told the workspace arrived.
func TestLevyanIsToldWhatAnOperatorEnds(t *testing.T) {
	x := require.New(t)
	ctx := context.Background()
	fake := levyantest.Start(t)
	c := billed(t, fake)
	c.Auth.Roster.Tenant = "acme"
	_, err := ran(t, cli.NewCmdInit(&c), "--login", "owner", "--password", "owner1234")
	x.NoError(err)
	d := entry(t, c)
	kinds := func() []string {
		out := []string{}
		for _, v := range d.notices(t) {
			out = append(out, v.Kind)
		}
		return out
	}
	acme, err := d.s.Ent.Tenant.Query().Where(tenant.Alias("acme")).Only(ctx)
	x.NoError(err)
	owner, err := d.s.Ent.Holder.Query().Where(holder.TenantId(acme.Id), holder.Role("owner")).Only(ctx)
	x.NoError(err)
	fake.Arrived(pdid.Id(acme.Id), pdid.Id(*owner.Account), acme.DateCreated)

	out, err := d.loft(t, "tenant", "close", "--reason", "미납", "acme")
	x.NoError(err, out)
	x.Empty(kinds(), "without --yes, nothing is done")
	out, err = d.loft(t, "tenant", "close", "--reason", "미납", "--yes", "acme")
	x.NoError(err, out)
	out, err = d.loft(t, "tenant", "restore", "--yes", "acme")
	x.NoError(err, out)
	x.Equal([]string{levyan.Closed, levyan.Restored}, kinds())

	dry, err := d.loft(t, "tenant", "purge", "acme")
	x.NoError(err, dry)
	x.Contains(dry, "levyan: it is told the workspace was destroyed")
	x.Len(kinds(), 2, "a dry run tells nothing")
	done, err := d.loft(t, "tenant", "purge", "--yes", "acme")
	x.NoError(err, done)
	x.Contains(done, "levyan: it is to be told the workspace was destroyed")
	x.Equal([]string{levyan.Closed, levyan.Restored, levyan.Destroyed}, kinds())

	told, err := d.s.Teller.Tell(ctx)
	x.NoError(err)
	x.Equal(levyan.Told{Told: 3}, told)
	got := []string{}
	for _, v := range fake.Heard() {
		got = append(got, v.Kind)
	}
	x.Equal([]string{levyan.Closed, levyan.Restored, levyan.Destroyed}, got)

	// And every row is what the trail says it is: the notices are kept off it.
	out, err = d.loft(t, "trail", "rows")
	x.NoError(err, out)
}
