package cli_test

import (
	"bytes"
	"context"
	"encoding/base64"
	"path/filepath"
	"testing"
	"time"

	"github.com/lesomnus/payday/pdid"
	"github.com/lesomnus/payday/pdtest"
	"github.com/lesomnus/payday/trail"
	"github.com/stretchr/testify/require"

	"github.com/lesomnus/loft/cli"
	"github.com/lesomnus/loft/cmd"
	"github.com/lesomnus/loft/internal/ent"
	"github.com/lesomnus/loft/internal/ent/holder"
	"github.com/lesomnus/loft/internal/ent/levyannotice"
	"github.com/lesomnus/loft/internal/ent/tenant"
	"github.com/lesomnus/loft/internal/levyantest"
	"github.com/lesomnus/loft/server/levyan"
)

// TestLevyanIsToldOfWhatAnOperatorMakes (design 7.6): a workspace `loft
// init` puts up -- with `--demo`'s teams and people in it, which are of that
// one workspace -- is told to levyan as made by its owner, in the transaction
// that makes it; and one `loft tenant import` puts back as made when it was,
// by its first owner, with what levyan grants it copied at once. An
// operator's `loft holder add` writes as the deployment, and a workspace
// levyan restricts takes it (D60).
func TestLevyanIsToldOfWhatAnOperatorMakes(t *testing.T) {
	x := require.New(t)
	ctx := context.Background()
	fake := levyantest.Start(t)
	key, _, err := trail.GenerateKey()
	x.NoError(err)

	c := cmd.Config{}
	c.Db.Driver, c.Db.Dsn = pdtest.DB(t)
	c.Watch.Broker = "memory"
	c.App.Files = t.TempDir()
	c.Auth.Roster.Db.Driver, c.Auth.Roster.Db.Dsn = pdtest.DB(t)
	c.Audit.Checkpoints.Key = string(key)
	c.Audit.Digest.Key = "base64:" + base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{7}, 32))
	c.App.Billing.Levyan = fake.Config()

	with := func(f func(s *cmd.Server)) {
		s, err := cmd.Build(ctx, c)
		x.NoError(err)
		defer s.Close()
		f(s)
	}
	notices := func() []*ent.LevyanNotice {
		var out []*ent.LevyanNotice
		with(func(s *cmd.Server) {
			out, err = s.Ent.LevyanNotice.Query().Order(ent.Asc(levyannotice.FieldAt), ent.Asc(levyannotice.FieldId)).All(ctx)
			x.NoError(err)
		})
		return out
	}
	workspace := func(alias string) (pdid.Id, time.Time) {
		var id pdid.Id
		var at time.Time
		with(func(s *cmd.Server) {
			v, err := s.Ent.Tenant.Query().Where(tenant.Alias(alias)).Only(ctx)
			x.NoError(err)
			id, at = pdid.Id(v.Id), v.DateCreated.UTC().Truncate(time.Microsecond)
		})
		return id, at
	}
	ownerOf := func(alias string) pdid.Id {
		var id pdid.Id
		with(func(s *cmd.Server) {
			h, err := s.Ent.Holder.Query().Where(holder.HasTenantWith(tenant.Alias(alias)), holder.Role("owner")).Order(ent.Asc(holder.FieldDateCreated)).First(ctx)
			x.NoError(err)
			id = pdid.Id(*h.Account)
		})
		return id
	}

	run(t, cli.NewCmdInit(&c), "--tenant", "acme", "--login", "owner", "--password", "owner1234", "--demo")
	acme, made := workspace("acme")
	vs := notices()
	x.Len(vs, 1, "one workspace, whatever `--demo` puts in it")
	x.Equal(levyan.Arrived, vs[0].Kind)
	x.Equal(acme.Uuid(), vs[0].Workspace)
	x.Equal(ownerOf("acme").Uuid(), vs[0].Account)
	x.Equal(made, vs[0].At.UTC())
	x.Equal(levyan.ByChange, vs[0].Origin)
	// The owner is in another workspace too, so its account outlives the
	// purge below.
	run(t, cli.NewCmdInit(&c), "--tenant", "beta", "--login", "owner")
	x.Len(notices(), 2)

	// Restricted, and an operator's hand all the same.
	fake.Restrict(acme)
	with(func(s *cmd.Server) {
		_, err := s.Follower.Sync(ctx, acme)
		x.NoError(err)
	})
	run(t, cli.NewCmdHolder(&c), "add", "--tenant", "acme", "--login", "kim", "--password", "kim-password")

	// Out, gone, and back: told as made when it was, by its first owner.
	out := filepath.Join(t.TempDir(), "acme.zip")
	run(t, cli.NewCmdTenant(&c), "export", "--tenant", "acme", "--out", out)
	run(t, cli.NewCmdTenant(&c), "purge", "--tenant", "acme", "--yes")
	x.Len(notices(), 2, "a workspace destroyed is the lifecycle events', and not this")
	dry := run(t, cli.NewCmdTenant(&c), "import", "--file", out)
	x.NotContains(dry, "levyan grants")
	x.Len(notices(), 2, "a dry run tells nothing")
	done := run(t, cli.NewCmdTenant(&c), "import", "--file", out, "--yes")
	x.Contains(done, "what levyan grants it is copied")
	vs = notices()
	x.Len(vs, 3)
	back := vs[0]
	for _, v := range vs {
		if v.Workspace == acme.Uuid() && v.Id != vs[0].Id {
			back = v
		}
	}
	x.NotEqual(vs[0].Id, back.Id)
	x.Equal(levyan.Arrived, back.Kind)
	x.Equal(ownerOf("acme").Uuid(), back.Account)
	x.Equal(made, back.At.UTC(), "made when it was, as levyan was told first")
	with(func(s *cmd.Server) {
		n, err := s.Ent.Entitlement.Query().Count(ctx)
		x.NoError(err)
		x.Equal(1, n, "what levyan says of it, copied: the restriction")
	})
}
