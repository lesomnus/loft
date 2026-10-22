package cli_test

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"filippo.io/age"
	"github.com/lesomnus/payday/config"
	"github.com/lesomnus/payday/pdid"
	"github.com/lesomnus/payday/pdpb"
	"github.com/lesomnus/payday/pdtest"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	"github.com/lesomnus/rove/cli"
	"github.com/lesomnus/rove/cmd"
	entaccess "github.com/lesomnus/rove/internal/ent/access"
	"github.com/lesomnus/rove/internal/ent/holder"
	enttenant "github.com/lesomnus/rove/internal/ent/tenant"
	"github.com/lesomnus/rove/server/pd"
)

// TestAWorkspacesTrailIsTakenOutForWhoeverKeepsIt (rove#153): a demand to
// preserve a workspace's records is answered by `rove trail export`, live or
// destroyed -- by the address its hold names once it is gone -- and by nothing
// else once its floors have passed. The file is encrypted to whoever keeps it,
// `verify-export` holds it to the deployment's key, and each export is a
// download in the access record, with its reason, naming everybody whose data
// it carries.
func TestAWorkspacesTrailIsTakenOutForWhoeverKeepsIt(t *testing.T) {
	x := require.New(t)
	ctx := t.Context()

	c := cmd.Config{}
	c.Db.Driver, c.Db.Dsn = pdtest.DB(t)
	c.Watch.Broker = "memory"
	c.App.Files = t.TempDir()
	c.Auth.Roster.Db.Driver, c.Auth.Roster.Db.Dsn = pdtest.DB(t)
	c.Audit.Archive = t.TempDir()
	c.Audit.Retain = time.Nanosecond
	c.Audit.Checkpoints.Key = pdtest.Key(t)
	// The access record's floor, as `deploy/` keeps it, and one on holders,
	// so that a retirement keeps something about the people it named.
	year := config.AuditMinConfig{Destroy: 365 * 24 * time.Hour}
	c.Audit.By = map[string]config.AuditKeepConfig{"access": {Min: year}, "holder": {Min: year}}
	run(t, cli.NewCmdInit(&c), "--tenant", "acme", "--login", "owner", "--password", "owner-pass-1234")
	run(t, cli.NewCmdInit(&c), "--tenant", "beta", "--login", "owner")
	run(t, cli.NewCmdHolder(&c), "add", "--tenant", "acme", "--login", "kim", "--role", "member", "--password", "kim-password")

	s, err := cmd.Build(ctx, c)
	x.NoError(err)
	defer s.Close()
	s.Trail.Pass(ctx, pd.TrailStore(s.Ent))
	acme, err := s.Ent.Tenant.Query().Where(enttenant.Alias("acme")).Only(ctx)
	x.NoError(err)
	kim, err := s.Ent.Holder.Query().Where(holder.TenantId(acme.Id), holder.Alias("kim")).Only(ctx)
	x.NoError(err)

	dir := t.TempDir()
	whom, err := age.GenerateX25519Identity()
	x.NoError(err)
	key := filepath.Join(dir, "keeper.key")
	x.NoError(os.WriteFile(key, []byte(whom.String()+"\n"), 0o600))
	pub := filepath.Join(dir, "keeper.pub")
	x.NoError(os.WriteFile(pub, []byte("# the keeper\n"+whom.Recipient().String()+"\n"), 0o644))
	trail := cli.NewCmdTrail(&c)

	// Why is the one thing it does not go without.
	live := filepath.Join(dir, "acme.trail")
	err = trail.Run(ctx, []string{"export", "--tenant", "acme", "--to", whom.Recipient().String(), "--out", live})
	x.ErrorContains(err, "--why")
	x.NoFileExists(live, "a file written by an export that was refused")

	out := run(t, cli.NewCmdTrail(&c), "export", "--tenant", "acme", "--why", "case 2026-4", "--to", whom.Recipient().String(), "--out", live)
	x.Contains(out, "the trail of @acme")
	x.Contains(out, "manifest sha256:")
	b, err := os.ReadFile(live)
	x.NoError(err)
	x.True(slices.Equal(b[:len("age-encryption.org/v1")], []byte("age-encryption.org/v1")), "not written encrypted")

	out = run(t, cli.NewCmdTrail(&c), "verify-export", "--file", live, "--identity", key)
	x.Contains(out, "a key the deployment trusts signed it")
	x.Contains(out, `"case 2026-4"`)

	// A download in the access record, with its reason, naming the people
	// its rows carry.
	rows, err := s.Ent.Access.Query().Where(entaccess.Action("rove trail export"), entaccess.TenantId(acme.Id)).All(ctx)
	x.NoError(err)
	x.Len(rows, 1)
	x.Equal("download", rows[0].Kind)
	d := &pdpb.AccessDetail{}
	x.NoError(proto.Unmarshal(rows[0].Value, d))
	x.Equal("case 2026-4", d.GetReason())
	x.True(slices.ContainsFunc(rows[0].Subjects, func(v []byte) bool { return pdid.Id(v) == pdid.Id(kim.Id) }), "kim's data is in it, and the record does not say so")

	// Retired, it is taken out by the address its hold names, until its
	// floors pass.
	run(t, cli.NewCmdTenant(&c), "retire", "--tenant", "acme", "--yes")
	out = run(t, cli.NewCmdTenant(&c), "retired")
	x.Contains(out, "@acme")
	x.Contains(out, "kept until "+time.Now().Add(365*24*time.Hour).Format(time.DateOnly))

	gone := filepath.Join(dir, "acme-retired.trail")
	out = run(t, cli.NewCmdTrail(&c), "export", "--tenant", "@acme", "--why", "case 2026-4, after it left", "--to", pub, "--out", gone)
	x.Contains(out, "retired ")
	out = run(t, cli.NewCmdTrail(&c), "verify-export", "--file", gone, "--identity", key)
	x.Contains(out, "kept until")

	rows, err = s.Ent.Access.Query().Where(entaccess.Action("rove trail export"), entaccess.TenantId(acme.Id)).All(ctx)
	x.NoError(err)
	x.Len(rows, 2)
	x.True(slices.ContainsFunc(rows[1].Subjects, func(v []byte) bool { return pdid.Id(v) == pdid.Id(kim.Id) }),
		"a destroyed workspace's export names nobody, though its rows name kim")

	err = cli.NewCmdTrail(&c).Run(ctx, []string{"export", "--tenant", "nobody-here", "--why", "a case", "--to", pub, "--out", filepath.Join(dir, "x")})
	x.ErrorContains(err, "no workspace is @nobody-here")
}
