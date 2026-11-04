package admincli_test

import (
	"context"
	"io"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lesomnus/payday/config"
	"github.com/lesomnus/payday/pdpb"
	"github.com/lesomnus/payday/pdtest"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	app "github.com/lesomnus/loft"
	"github.com/lesomnus/loft/cli"
	admincli "github.com/lesomnus/loft/cli/admin"
	"github.com/lesomnus/loft/cmd"
	entaccess "github.com/lesomnus/loft/internal/ent/access"
	"github.com/lesomnus/loft/internal/ent/aliashold"
	entaudit "github.com/lesomnus/loft/internal/ent/audit"
	"github.com/lesomnus/loft/internal/ent/event"
	"github.com/lesomnus/loft/internal/ent/tenant"
	"github.com/lesomnus/loft/server/admin"
	"github.com/lesomnus/loft/server/offboard"
	"github.com/lesomnus/loft/server/operator"
)

// archived is a deployment that keeps a trail's archive and signs it, and
// keeps a floor -- what a retirement is for -- with two workspaces.
func archived(t *testing.T) *deployment {
	t.Helper()
	x := require.New(t)
	c := cmd.Config{}
	c.Db.Driver, c.Db.Dsn = pdtest.DB(t)
	c.Watch.Broker = "memory"
	c.App.Files = t.TempDir()
	c.Auth.Roster.Db.Driver, c.Auth.Roster.Db.Dsn = pdtest.DB(t)
	c.Audit.Archive = t.TempDir()
	c.Audit.Checkpoints.Key = pdtest.Key(t)
	c.Audit.By = map[string]config.AuditKeepConfig{"holder": {Min: config.AuditMinConfig{Destroy: 365 * 24 * time.Hour}}}
	_, err := run(t, cli.NewCmdInit(&c), "--tenant", "acme", "--login", "owner", "--password", "owner1234")
	x.NoError(err)
	_, err = run(t, cli.NewCmdHolder(&c), "add", "--tenant", "acme", "--login", "kim", "--role", "member", "--password", "kim-password")
	x.NoError(err)
	_, err = run(t, cli.NewCmdInit(&c), "--tenant", "beta", "--login", "owner")
	x.NoError(err)

	s, err := cmd.Build(t.Context(), c)
	x.NoError(err)
	t.Cleanup(func() { s.Close() })
	ops, err := admin.Operators(map[string]string{"alice": key})
	x.NoError(err)
	h, err := admin.Routes{S: s, C: c, Operators: ops}.Handler(t.Context())
	x.NoError(err)
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return &deployment{c: c, s: s, url: srv.URL}
}

// written is how many rows of what a change writes the deployment holds:
// what a dry run is to leave as it was.
func written(t *testing.T, d *deployment) map[string]int {
	t.Helper()
	x := require.New(t)
	ctx := context.Background()
	out := map[string]int{}
	var err error
	out["tenant"], err = d.s.Ent.Tenant.Query().Count(ctx)
	x.NoError(err)
	out["holder"], err = d.s.Ent.Holder.Query().Count(ctx)
	x.NoError(err)
	out["event"], err = d.s.Ent.Event.Query().Count(ctx)
	x.NoError(err)
	out["audit"], err = d.s.Ent.Audit.Query().Count(ctx)
	x.NoError(err)
	out["alias-hold"], err = d.s.Ent.AliasHold.Query().Count(ctx)
	x.NoError(err)
	out["levyan-notice"], err = d.s.Ent.LevyanNotice.Query().Count(ctx)
	x.NoError(err)
	return out
}

// TestAWorkspaceLeavesAndComesBackThroughTheEntryPoint: its export, taken
// out by the operator for what it says it is for and on the access record as
// theirs, held to the deployment's keys where the file is; its purge, said
// before it is done; and its return from the export, sent up a part at a
// time -- each the deployment's work, at the operator's word.
func TestAWorkspaceLeavesAndComesBackThroughTheEntryPoint(t *testing.T) {
	x := require.New(t)
	d := archived(t)
	ctx := context.Background()
	alice := operator.Id("alice").Uuid()
	dir := t.TempDir()
	zip := filepath.Join(dir, "acme.zip")
	must := func(args ...string) string {
		t.Helper()
		out, err := d.admin(t, args...)
		x.NoError(err, "%s: %s", strings.Join(args, " "), out)
		return out
	}

	_, err := d.admin(t, "tenant", "export", "--out", zip, "acme")
	x.ErrorContains(err, "--reason", "a download says what it is for")
	out := must("tenant", "export", "--reason", "폐업 전 보관", "--out", zip, "acme")
	x.Contains(out, zip+": ")
	x.Contains(out, "rows of")
	row, err := d.s.Ent.Access.Query().Where(entaccess.Action(app.TenantService_Export_FullMethodName)).Only(ctx)
	x.NoError(err, "a download is on the record before its first message")
	x.Equal("download", row.Kind)
	x.Equal(alice, row.ActorId)
	dl := &pdpb.AccessDetail{}
	x.NoError(proto.Unmarshal(row.Value, dl))
	x.Equal("폐업 전 보관", dl.GetReason())
	x.Positive(row.SubjectCount, "whose data it carries")

	out = must("tenant", "verify", "--file", zip)
	x.Contains(out, "acme, exported")
	x.Contains(out, "signed by a trusted key")
	altered := filepath.Join(dir, "altered.zip")
	b, err := os.ReadFile(zip)
	x.NoError(err)
	x.NoError(os.WriteFile(altered, b[:len(b)-10], 0o600))
	_, err = d.admin(t, "tenant", "verify", "--file", altered)
	x.Error(err, "a file somebody changed")

	before := written(t, d)
	out = must("tenant", "retire", "acme")
	x.Contains(out, "acme: would take")
	x.Contains(out, "for the deployment's floors: holder")
	x.Contains(out, "`--yes` takes it")
	out = must("tenant", "purge", "acme")
	x.Contains(out, "its contract retires it")
	here, err := d.s.Ent.Tenant.Query().Where(tenant.Alias("acme")).Exist(ctx)
	x.NoError(err)
	x.True(here, "nothing was taken")
	x.Equal(before, written(t, d), "and nothing was written")

	out = must("tenant", "purge", "--yes", "acme")
	x.Contains(out, "acme: took")
	x.Contains(out, "roster in this process: forgot 1 account(s)")
	here, err = d.s.Ent.Tenant.Query().Where(tenant.Alias("acme")).Exist(ctx)
	x.NoError(err)
	x.False(here)
	// What outlives it -- its address, held -- is on the trail as the
	// operator's.
	hold, err := d.s.Ent.AliasHold.Query().Where(aliashold.Held("acme")).Only(ctx)
	x.NoError(err)
	n, err := d.s.Ent.Audit.Query().Where(entaudit.ObjectId(hold.Id), entaudit.ActorId(alice)).Count(ctx)
	x.NoError(err)
	x.Positive(n, "the address it held is the operator's act")

	before = written(t, d)
	out = must("tenant", "import", "--file", zip)
	x.Contains(out, "@acme: would put back")
	x.Contains(out, "`--yes` does it")
	here, err = d.s.Ent.Tenant.Query().Where(tenant.Alias("acme")).Exist(ctx)
	x.NoError(err)
	x.False(here, "nothing was put back")
	x.Equal(before, written(t, d), "and nothing was written")

	// From stdin, as a file leaves a box through a pipe.
	f, err := os.Open(zip)
	x.NoError(err)
	defer f.Close()
	k := admincli.Cmd(&d.c)
	k.ReadCloser = io.NopCloser(f)
	out, err = run(t, k, "--url", d.url, "--key", key, "tenant", "import", "--file", "-", "--yes")
	x.NoError(err, out)
	x.Contains(out, "@acme: put back")
	back, err := d.s.Ent.Tenant.Query().Where(tenant.Alias("acme")).Only(ctx)
	x.NoError(err)
	ev, err := d.s.Ent.Event.Query().Where(event.TenantId(back.Id), event.Kind("workspace.import")).Only(ctx)
	x.NoError(err)
	x.NotNil(ev.ActorId)
	x.Equal(alice, *ev.ActorId, "the restore is the operator's")
	n, err = d.s.Ent.Audit.Query().Where(entaudit.Action(offboard.Imported), entaudit.ActorId(alice)).Count(ctx)
	x.NoError(err)
	x.Positive(n, "and so is what the trail says it put back")

	// A retirement, and what is left of it.
	x.Contains(must("tenant", "retired"), "no workspace has retired")
	x.Contains(must("tenant", "retire", "--yes", "beta"), "beta: took")
	out = must("tenant", "retired")
	x.Contains(out, "@beta\t")
	x.Contains(out, "kept until")
}

// TestAWorkspaceUnderAHoldIsNotDestroyed: a hold refuses the destruction
// before anything is taken, said as what was refused.
func TestAWorkspaceUnderAHoldIsNotDestroyed(t *testing.T) {
	x := require.New(t)
	d := archived(t)
	_, err := d.admin(t, "hold", "place", "--why", "a case", "--yes", "acme")
	x.NoError(err)
	_, err = d.admin(t, "tenant", "purge", "--yes", "acme")
	x.ErrorContains(err, "legal hold")
	here, err := d.s.Ent.Tenant.Query().Where(tenant.Alias("acme")).Exist(context.Background())
	x.NoError(err)
	x.True(here)
}

// TestAnUnsignedExportIsSaidToBe: a deployment that signs with no key still
// exports, and says what it wrote is vouched for by nothing -- and `verify`
// has nothing to hold one to.
func TestAnUnsignedExportIsSaidToBe(t *testing.T) {
	x := require.New(t)
	d := deploy(t)
	zip := filepath.Join(t.TempDir(), "acme.zip")
	out, err := d.admin(t, "tenant", "export", "--reason", "a request", "--out", zip, "acme")
	x.NoError(err, out)
	x.Contains(out, "unsigned")
	st, err := os.Stat(zip)
	x.NoError(err)
	x.Positive(st.Size())
	_, err = d.admin(t, "tenant", "verify", "--file", zip)
	x.ErrorContains(err, "nothing to hold a file to")
}
