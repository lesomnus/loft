package cli_test

import (
	"context"
	"os"
	"path/filepath"
	"regexp"
	"testing"
	"time"

	"github.com/lesomnus/payday/pdid"
	"github.com/lesomnus/payday/trail"
	"github.com/stretchr/testify/require"

	"github.com/lesomnus/loft/cli"
	"github.com/lesomnus/loft/cmd"
	"github.com/lesomnus/loft/internal/ent/party"
	"github.com/lesomnus/loft/internal/identity"
)

// TestInitPutsUpTheTenantAndItsOwner (D85): on the roster in this process,
// `init` makes the tenant `auth.roster.tenant` names and its first person,
// and puts the workspace up by the tenant's identifier and its owner by the
// person's -- called at roster what their person record is called here. A
// deployment serves one workspace, and `init` again is refused. With nothing
// named, the tenant is `loft`, its owner `admin`, called "관리자".
func TestInitPutsUpTheTenantAndItsOwner(t *testing.T) {
	x := require.New(t)
	ctx := context.Background()
	c := embeddedConfig(t)
	c.Auth.Roster.Tenant = "acme"
	run(t, cli.NewCmdInit(&c), "--name", "ACME 연구소", "--login", "kim", "--person", "김민준", "--password", "kim-password")

	on(t, c, func(s *cmd.Server) {
		rt, err := s.Identity.Tenant(ctx)
		x.NoError(err)
		x.Equal("acme", rt.Alias)
		x.Equal("ACME 연구소", rt.Name)
		ws, err := s.Ent.Tenant.Get(ctx, rt.Id.Uuid())
		x.NoError(err, "the workspace is the tenant at roster, by its identifier")
		x.Equal("acme", ws.Alias)
		x.Equal("ACME 연구소", ws.Name)

		p, err := s.Identity.Verify(ctx, "kim", "kim-password")
		x.NoError(err)
		x.Equal("김민준", p.Name)
		h, err := s.Ent.Holder.Get(ctx, p.Id.Uuid())
		x.NoError(err, "the owner is the holder at roster, by its identifier")
		x.Equal("owner", h.Role)
		x.Equal(rt.Id.Uuid(), h.TenantId)
		record, err := s.Ent.Party.Query().Where(party.HolderId(h.Id)).Only(ctx)
		x.NoError(err)
		x.Equal("김민준", record.Name, "called here what roster calls them")
	})

	err := cli.NewCmdInit(&c).Run(ctx, []string{"--login", "lee", "--password", "lee-password"})
	x.ErrorContains(err, "a deployment serves one")
	on(t, c, func(s *cmd.Server) {
		_, err := s.Identity.Lookup(ctx, "lee")
		x.ErrorIs(err, identity.ErrNoPerson, "a refused init made somebody at roster")
	})

	t.Run("named nothing", func(t *testing.T) {
		x := require.New(t)
		c := embeddedConfig(t)
		out := run(t, cli.NewCmdInit(&c))
		x.Contains(out, "workspace @loft (")
		m := regexp.MustCompile(`(?m)^password  (\S+)$`).FindStringSubmatch(out)
		x.NotNil(m, "the owner's password is said once: %s", out)
		on(t, c, func(s *cmd.Server) {
			p, err := s.Identity.Verify(ctx, identity.RosterAdministrator, m[1])
			x.NoError(err)
			x.Equal("관리자", p.Name)
		})
	})
}

// TestInitTakesAnOwnerRosterHasAsTheyAre: somebody the tenant at roster has
// already is made the owner as they are -- given no new password, and one
// that would change theirs is refused.
func TestInitTakesAnOwnerRosterHasAsTheyAre(t *testing.T) {
	x := require.New(t)
	ctx := context.Background()
	c := embeddedConfig(t)
	c.Auth.Roster.Tenant = "acme"
	var owner pdid.Id
	on(t, c, func(s *cmd.Server) {
		_, p, _, err := s.Identity.MakeTenant(ctx, "", "owner", "")
		x.NoError(err)
		x.NoError(s.Identity.SetPassword(ctx, p.Id, "owner1234"))
		owner = p.Id
	})

	x.Error(cli.NewCmdInit(&c).Run(ctx, []string{"--login", "owner", "--password", "something-else"}),
		"a password changed by putting a workspace up")
	out := run(t, cli.NewCmdInit(&c), "--login", "owner")
	x.NotContains(out, "password", "somebody there already is given a password")
	on(t, c, func(s *cmd.Server) {
		p, err := s.Identity.Verify(ctx, "owner", "owner1234")
		x.NoError(err, "their own password")
		x.Equal(owner, p.Id)
		h, err := s.Ent.Holder.Get(ctx, owner.Uuid())
		x.NoError(err)
		x.Equal("owner", h.Role)
	})
}

// TestInitOnARosterOfItsOwnTakesWhatIsThere: on a roster of its own the
// tenant and its people are its operator's, and `init` makes neither: it
// puts the workspace up by the identifier of the tenant the key is of, and
// its owner as the holder `--login` names there -- `admin`, whom roster makes
// every tenant with, unless it names another -- and makes no password.
func TestInitOnARosterOfItsOwnTakesWhatIsThere(t *testing.T) {
	x := require.New(t)
	ctx := context.Background()
	addr, key, roster := external(t, pdid.Nil)
	c := embeddedConfig(t)
	c.Auth.Roster = cmd.Config{}.Auth.Roster
	c.Auth.Roster.Addr = addr
	c.Auth.Roster.Insecure = true
	c.Auth.Roster.Key = key

	for _, args := range [][]string{
		{"--password", "owner1234"},
		{"--demo"},
	} {
		x.ErrorContains(cli.NewCmdInit(&c).Run(ctx, args), "the roster in this process's", "%v", args)
	}
	x.ErrorContains(cli.NewCmdInit(&c).Run(ctx, []string{"--login", "nobody"}), "is nobody there")

	out := run(t, cli.NewCmdInit(&c))
	x.Contains(out, "workspace @acme (")
	x.NotContains(out, "password")
	on(t, c, func(s *cmd.Server) {
		rt, err := s.Identity.Tenant(ctx)
		x.NoError(err)
		_, err = s.Ent.Tenant.Get(ctx, rt.Id.Uuid())
		x.NoError(err, "the workspace is the tenant at roster, by its identifier")
		admin, err := s.Identity.Lookup(ctx, identity.RosterAdministrator)
		x.NoError(err)
		h, err := s.Ent.Holder.Get(ctx, admin.Id.Uuid())
		x.NoError(err)
		x.Equal("owner", h.Role)
	})
	_ = roster
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
	given.Auth.Roster.Tenant = "acme"
	out := run(t, cli.NewCmdInit(&given), "--login", "kim", "--password", "kim-password")
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
		err := cli.NewCmdInit(&again).Run(ctx, []string{"--login", "lee", "--password", "lee-password"})
		x.ErrorContains(err, "is one already")

		now, err := os.ReadFile(path)
		x.NoError(err)
		x.Equal(doc, now, "init wrote over a key that was there")
	})
}
