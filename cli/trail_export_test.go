package cli_test

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"filippo.io/age"
	"github.com/lesomnus/payday/config"
	"github.com/lesomnus/payday/pdid"
	"github.com/lesomnus/payday/pdpb"
	"github.com/lesomnus/payday/pdtest"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	"github.com/lesomnus/loft/cli"
	"github.com/lesomnus/loft/cmd"
	entaccess "github.com/lesomnus/loft/internal/ent/access"
	"github.com/lesomnus/loft/internal/ent/holder"
	enttenant "github.com/lesomnus/loft/internal/ent/tenant"
	"github.com/lesomnus/loft/server/pd"
)

// TestAWorkspacesTrailIsTakenOutForWhoeverKeepsIt (loft#153): a demand to
// preserve a workspace's records is answered by `loft trail export`, live or
// destroyed -- by the address its hold names once it is gone -- and by nothing
// else once its floors have passed. The file is encrypted to whoever keeps it,
// `verify-export` holds it to the deployment's key, and each export is a
// download in the access record, with its reason, naming everybody whose data
// it carries.
func TestAWorkspacesTrailIsTakenOutForWhoeverKeepsIt(t *testing.T) {
	x := require.New(t)
	ctx := t.Context()
	// In a zone whose date is not UTC's now, as Seoul's is from midnight to
	// nine: the dates the commands say are UTC's, whatever the shell's.
	inZone(t, awayFromUTC(time.Now()))

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
	rows, err := s.Ent.Access.Query().Where(entaccess.Action("loft trail export"), entaccess.TenantId(acme.Id)).All(ctx)
	x.NoError(err)
	x.Len(rows, 1)
	x.Equal("download", rows[0].Kind)
	d := &pdpb.AccessDetail{}
	x.NoError(proto.Unmarshal(rows[0].Value, d))
	x.Equal("case 2026-4", d.GetReason())
	x.True(slices.ContainsFunc(rows[0].Subjects, func(v []byte) bool { return pdid.Id(v) == pdid.Id(kim.Id) }), "kim's data is in it, and the record does not say so")

	// Retired, it is taken out by the address its hold names, until its
	// floors pass.
	began := time.Now()
	run(t, cli.NewCmdTenant(&c), "retire", "--tenant", "acme", "--yes")
	ended := time.Now()
	out = run(t, cli.NewCmdTenant(&c), "retired")
	x.Contains(out, "@acme")
	x.True(saysDay(out, "kept until ", began.Add(365*24*time.Hour), ended.Add(365*24*time.Hour)), "not UTC's day, a year from now: %s", out)

	gone := filepath.Join(dir, "acme-retired.trail")
	out = run(t, cli.NewCmdTrail(&c), "export", "--tenant", "@acme", "--why", "case 2026-4, after it left", "--to", pub, "--out", gone)
	x.Contains(out, "retired ")
	out = run(t, cli.NewCmdTrail(&c), "verify-export", "--file", gone, "--identity", key)
	x.Contains(out, "kept until")

	rows, err = s.Ent.Access.Query().Where(entaccess.Action("loft trail export"), entaccess.TenantId(acme.Id)).All(ctx)
	x.NoError(err)
	x.Len(rows, 2)
	x.True(slices.ContainsFunc(rows[1].Subjects, func(v []byte) bool { return pdid.Id(v) == pdid.Id(kim.Id) }),
		"a destroyed workspace's export names nobody, though its rows name kim")

	err = cli.NewCmdTrail(&c).Run(ctx, []string{"export", "--tenant", "nobody-here", "--why", "a case", "--to", pub, "--out", filepath.Join(dir, "x")})
	x.ErrorContains(err, "no workspace is @nobody-here")
}

// inZone has the process's local zone be `l` for the rest of the test: what a
// shell's TZ says, which the process reads once as it starts. No test in this
// package runs beside another.
func inZone(t *testing.T, l *time.Location) {
	t.Helper()

	was := time.Local
	time.Local = l
	t.Cleanup(func() { time.Local = was })
}

// saysDay answers whether `out` says `prefix` and then the day, in UTC, of a
// moment between `from` and `to`: either one's, for when UTC's midnight came
// between the two.
func saysDay(out, prefix string, from, to time.Time) bool {
	return strings.Contains(out, prefix+from.UTC().Format(time.DateOnly)) || strings.Contains(out, prefix+to.UTC().Format(time.DateOnly))
}

// awayFromUTC is a zone whose date is not UTC's at `now`, nor for hours after:
// fourteen hours ahead of it from ten in the morning UTC, and twelve behind
// before then.
func awayFromUTC(now time.Time) *time.Location {
	if now.UTC().Hour() >= 10 {
		return time.FixedZone("UTC+14", 14*60*60)
	}

	return time.FixedZone("UTC-12", -12*60*60)
}
