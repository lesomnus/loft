package cli_test

import (
	"bytes"
	"context"
	"encoding/base64"
	"path/filepath"
	"testing"
	"time"

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
	_, err = ran(t, cli.NewCmdInit(&d.c), "--tenant", "beta", "--login", "owner")
	x.NoError(err)
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

// TestLevyanIsToldOfWhatAnOperatorMakes (design 7.6): a workspace `loft
// init` puts up -- with `--demo`'s teams and people in it, which are of that
// one workspace -- is told to levyan as made by its owner, in the transaction
// that makes it; and one `tenant import` puts back as made when it was, by
// its first owner, with what levyan grants it copied at once. An operator's
// `holder add` writes as the deployment's work at their word, and a
// workspace levyan restricts takes it (D60).
func TestLevyanIsToldOfWhatAnOperatorMakes(t *testing.T) {
	x := require.New(t)
	ctx := context.Background()
	fake := levyantest.Start(t)
	c := billed(t, fake)

	_, err := ran(t, cli.NewCmdInit(&c), "--tenant", "acme", "--login", "owner", "--password", "owner1234", "--demo")
	x.NoError(err)
	d := entry(t, c)
	workspace := func(alias string) (pdid.Id, time.Time) {
		v, err := d.s.Ent.Tenant.Query().Where(tenant.Alias(alias)).Only(ctx)
		x.NoError(err)
		return pdid.Id(v.Id), v.DateCreated.UTC().Truncate(time.Microsecond)
	}
	ownerOf := func(alias string) pdid.Id {
		h, err := d.s.Ent.Holder.Query().Where(holder.HasTenantWith(tenant.Alias(alias)), holder.Role("owner")).Order(ent.Asc(holder.FieldDateCreated)).First(ctx)
		x.NoError(err)
		return pdid.Id(*h.Account)
	}

	acme, made := workspace("acme")
	vs := d.notices(t)
	x.Len(vs, 1, "one workspace, whatever `--demo` puts in it")
	x.Equal(levyan.Arrived, vs[0].Kind)
	x.Equal(acme.Uuid(), vs[0].Workspace)
	x.Equal(ownerOf("acme").Uuid(), vs[0].Account)
	x.Equal(made, vs[0].DateOccurred.UTC())
	x.Equal(levyan.ByChange, vs[0].Origin)
	// The owner is in another workspace too, so its account outlives the
	// purge below.
	_, err = ran(t, cli.NewCmdInit(&c), "--tenant", "beta", "--login", "owner")
	x.NoError(err)
	x.Len(d.notices(t), 2)

	// Restricted, and an operator's hand all the same.
	fake.Restrict(acme)
	_, err = d.s.Follower.Sync(ctx, acme)
	x.NoError(err)
	out, err := d.loft(t, "holder", "add", "--tenant", "acme", "--login", "kim", "--password", "kim-password")
	x.NoError(err, out)

	// Out, gone, and back: told as made when it was, by its first owner.
	zip := filepath.Join(t.TempDir(), "acme.zip")
	out, err = d.loft(t, "--reason", "leaving", "tenant", "export", "--out", zip, "acme")
	x.NoError(err, out)
	out, err = d.loft(t, "tenant", "purge", "--yes", "acme")
	x.NoError(err, out)
	vs = d.notices(t)
	x.Len(vs, 3, "and that it was destroyed (TestLevyanIsToldWhatAnOperatorEnds)")
	x.Equal(levyan.Destroyed, vs[2].Kind)
	dry, err := d.loft(t, "tenant", "import", "--file", zip)
	x.NoError(err, dry)
	x.NotContains(dry, "levyan grants")
	x.Len(d.notices(t), 3, "a dry run tells nothing")
	done, err := d.loft(t, "tenant", "import", "--file", zip, "--yes")
	x.NoError(err, done)
	x.Contains(done, "what levyan grants it is copied")
	vs = d.notices(t)
	x.Len(vs, 4)
	back := vs[0]
	for _, v := range vs {
		if v.Workspace == acme.Uuid() && v.Kind == levyan.Arrived && v.Id != vs[0].Id {
			back = v
		}
	}
	x.NotEqual(vs[0].Id, back.Id)
	x.Equal(levyan.Arrived, back.Kind)
	x.Equal(ownerOf("acme").Uuid(), back.Account)
	x.Equal(made, back.DateOccurred.UTC(), "made when it was, as levyan was told first")
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
// does not. `loft serve`'s teller tells them, after its arrival, in the order
// they happened.
func TestLevyanIsToldWhatAnOperatorEnds(t *testing.T) {
	x := require.New(t)
	ctx := context.Background()
	fake := levyantest.Start(t)
	c := billed(t, fake)
	_, err := ran(t, cli.NewCmdInit(&c), "--tenant", "acme", "--login", "owner", "--password", "owner1234")
	x.NoError(err)
	d := entry(t, c)
	kinds := func() []string {
		out := []string{}
		for _, v := range d.notices(t) {
			out = append(out, v.Kind)
		}
		return out
	}

	out, err := d.loft(t, "tenant", "close", "--reason", "미납", "acme")
	x.NoError(err, out)
	x.Equal([]string{levyan.Arrived}, kinds(), "without --yes, nothing is done")
	out, err = d.loft(t, "tenant", "close", "--reason", "미납", "--yes", "acme")
	x.NoError(err, out)
	out, err = d.loft(t, "tenant", "restore", "--yes", "acme")
	x.NoError(err, out)
	x.Equal([]string{levyan.Arrived, levyan.Closed, levyan.Restored}, kinds())

	dry, err := d.loft(t, "tenant", "purge", "acme")
	x.NoError(err, dry)
	x.Contains(dry, "levyan: it is told the workspace was destroyed")
	x.Len(kinds(), 3, "a dry run tells nothing")
	done, err := d.loft(t, "tenant", "purge", "--yes", "acme")
	x.NoError(err, done)
	x.Contains(done, "levyan: it is to be told the workspace was destroyed")
	x.Equal([]string{levyan.Arrived, levyan.Closed, levyan.Restored, levyan.Destroyed}, kinds())

	told, err := d.s.Teller.Tell(ctx)
	x.NoError(err)
	x.Equal(levyan.Told{Told: 4}, told)
	got := []string{}
	for _, v := range fake.Heard() {
		got = append(got, v.Kind)
	}
	x.Equal([]string{levyan.Arrived, levyan.Closed, levyan.Restored, levyan.Destroyed}, got)

	// And every row is what the trail says it is: the notices are kept off it.
	out, err = d.loft(t, "trail", "rows")
	x.NoError(err, out)
}
