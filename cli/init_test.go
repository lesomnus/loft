package cli_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/lesomnus/payday/trail"
	"github.com/stretchr/testify/require"

	"github.com/lesomnus/loft/cli"
	"github.com/lesomnus/loft/cmd"
	"github.com/lesomnus/loft/internal/ent/party"
	enttenant "github.com/lesomnus/loft/internal/ent/tenant"
)

// TestTheOwnerInitMakesIsCalledTheSameEverywhere.
//
// The account `init` makes is called at roster what its person record is
// called here, so that a workspace it makes later calls its owner that and
// not their login.
func TestTheOwnerInitMakesIsCalledTheSameEverywhere(t *testing.T) {
	x := require.New(t)
	ctx := context.Background()
	c := embeddedConfig(t)

	run(t, cli.NewCmdInit(&c), "--tenant", "acme", "--login", "kim", "--person", "김민준", "--password", "kim-password")

	s, err := cmd.Build(ctx, c)
	x.NoError(err)
	defer s.Close()
	p, err := s.Identity.Lookup(ctx, "kim")
	x.NoError(err)
	x.Equal("김민준", p.Name)

	m, err := s.Workspaces(c).Create(ctx, p, "두 번째")
	x.NoError(err)
	owner, err := s.Ent.Party.Query().Where(party.TenantId(m.Workspace.Uuid())).Only(ctx)
	x.NoError(err)
	x.Equal("김민준", owner.Name)

	// And with no name given, what `init` calls the person it made.
	c = embeddedConfig(t)
	run(t, cli.NewCmdInit(&c), "--tenant", "beta", "--login", "lee")
	s2, err := cmd.Build(ctx, c)
	x.NoError(err)
	defer s2.Close()
	p, err = s2.Identity.Lookup(ctx, "lee")
	x.NoError(err)
	t2, err := s2.Ent.Tenant.Query().Where(enttenant.Alias("beta")).Only(ctx)
	x.NoError(err)
	here, err := s2.Ent.Party.Query().Where(party.TenantId(t2.Id)).Only(ctx)
	x.NoError(err)
	x.Equal(here.Name, p.Name)
}

// TestInitGivesAnArchiveAKeyOfItsOwn: an archive is kept with a key that signs
// its account (payday#115), and a deployment that names one and no key is
// refused where it comes up. `init` on such a configuration writes a key of
// the deployment's own beside the archive and says where -- there is no key
// everybody knows to fall back on -- and the deployment builds once the key is
// named. A file already there is somebody's key, and is not written over.
func TestInitGivesAnArchiveAKeyOfItsOwn(t *testing.T) {
	x := require.New(t)
	ctx := context.Background()

	archive := filepath.Join(t.TempDir(), "audit")
	keyless := func() cmd.Config {
		c := embeddedConfig(t)
		c.Audit.Archive = archive
		c.Audit.Retain = 90 * 24 * time.Hour
		return c
	}

	// Refused where it comes up, on databases of its own: what a refusal
	// leaves open is nothing the rest of this has to share.
	_, err := cmd.Build(ctx, keyless())
	x.ErrorContains(err, "audit.checkpoints.key", "an archive with no key came up")

	c := keyless()
	given := c
	out := run(t, cli.NewCmdInit(&given), "--tenant", "acme", "--login", "kim", "--password", "kim-password")
	path := c.Audit.Archive + "-checkpoints.pem"
	x.Contains(out, path, "init did not say where the key is")
	x.Equal(path, given.Audit.Checkpoints.Key, "init signed with a key other than the one it wrote")

	info, err := os.Stat(path)
	x.NoError(err)
	x.Equal(os.FileMode(0o600), info.Mode().Perm(), "a key anybody on the machine can read")
	doc, err := os.ReadFile(path)
	x.NoError(err)
	_, err = trail.ParseKey(doc)
	x.NoError(err, "what init wrote is not a key")

	// Named, as init said to.
	c.Audit.Checkpoints.Key = path
	s, err := cmd.Build(ctx, c)
	x.NoError(err)
	x.NoError(s.Close())

	t.Run("and a key already there is not written over", func(t *testing.T) {
		x := require.New(t)

		again := c
		again.Audit.Checkpoints.Key = ""
		err := cli.NewCmdInit(&again).Run(ctx, []string{"--tenant", "beta", "--login", "lee", "--password", "lee-password"})
		x.ErrorContains(err, "is one already")

		now, err := os.ReadFile(path)
		x.NoError(err)
		x.Equal(doc, now, "init wrote over a key that was there")
	})
}
