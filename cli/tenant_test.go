package cli_test

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/base64"
	"io"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lesomnus/payday/config"
	"github.com/lesomnus/payday/pdid"
	"github.com/lesomnus/payday/pdpb"
	"github.com/lesomnus/payday/pdtest"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	app "github.com/lesomnus/loft"
	"github.com/lesomnus/loft/cli"
	"github.com/lesomnus/loft/cmd"
	entaccess "github.com/lesomnus/loft/internal/ent/access"
	entaudit "github.com/lesomnus/loft/internal/ent/audit"
	"github.com/lesomnus/loft/internal/ent/event"
	"github.com/lesomnus/loft/internal/ent/tenant"
	"github.com/lesomnus/loft/internal/loftest"
	"github.com/lesomnus/loft/server/admin"
	"github.com/lesomnus/loft/server/offboard"
	"github.com/lesomnus/loft/server/operator"
)

// archived is a deployment that keeps a trail's archive and signs it, digests
// its rows, and keeps a floor -- what a retirement is for -- with two
// workspaces: acme, the one it serves, with its owner and kim; and beta, put
// back from another deployment.
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
	c.Audit.Digest.Key = "base64:" + base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{7}, 32))
	c.Auth.Roster.Tenant = "acme"
	_, err := ran(t, cli.NewCmdInit(&c), "--login", "owner", "--password", "owner1234")
	x.NoError(err)
	cli.Elsewhere(t, c, "beta", "owner", false)

	s, err := cmd.Build(t.Context(), c)
	x.NoError(err)
	t.Cleanup(func() { s.Close() })
	loftest.Member(t, s, "acme", "kim", "member", "kim-password")
	ops, err := admin.Operators(map[string]string{"alice": operatorKey})
	x.NoError(err)
	h, err := admin.Routes{S: s, C: c, Operators: ops}.Handler(t.Context())
	x.NoError(err)
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return &deployment{c: c, s: s, url: srv.URL, key: keyRef(t)}
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
		out, err := d.loft(t, args...)
		x.NoError(err, "%s: %s", strings.Join(args, " "), out)
		return out
	}

	_, err := d.loft(t, "tenant", "export", "--out", zip, "acme")
	x.ErrorContains(err, "--reason", "a download says what it is for")
	out := must("--reason", "폐업 전 보관", "tenant", "export", "--out", zip, "acme")
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
	x.Contains(out, "manifest sha256:")
	// And where nobody holds an operator's key: against the deployment's
	// public keys, as `trail keys` printed them.
	trust := filepath.Join(dir, "deployment.pem")
	x.NoError(os.WriteFile(trust, []byte(must("trail", "keys")), 0o644))
	out, err = ran(t, root(), "tenant", "verify", "--trust", trust, "--file", zip)
	x.NoError(err, out)
	x.Contains(out, "signed by a trusted key")
	cut := filepath.Join(dir, "cut.zip")
	b, err := os.ReadFile(zip)
	x.NoError(err)
	x.NoError(os.WriteFile(cut, b[:len(b)-10], 0o600))
	_, err = d.loft(t, "tenant", "verify", "--file", cut)
	x.Error(err, "a file cut short")
	// The same archive with one byte of one row changed (design D34).
	altered := filepath.Join(dir, "altered.zip")
	x.NoError(os.WriteFile(altered, changed(t, b, func(name string) bool {
		return name == "rows/tenant.jsonl" || strings.HasPrefix(name, "rows/holder")
	}), 0o600))
	_, err = d.loft(t, "tenant", "verify", "--file", altered)
	x.ErrorIs(err, offboard.ErrAltered)

	signsIn := func(login, password string) error {
		_, err := d.s.Identity.Verify(ctx, login, password)
		return err
	}
	before := written(t, d)
	x.Contains(must("tenant", "show", "acme"), "contract\tnone: destroyed, it retires", "a workspace with no contract retires")
	out = must("tenant", "retire", "acme")
	x.Contains(out, "acme: would take")
	x.Contains(out, "for the deployment's floors: holder", "a retirement keeps what a floor is on")
	x.Contains(out, "`--yes` takes it")
	x.NotContains(out, "its contract")
	out = must("tenant", "purge", "acme")
	x.Contains(out, "its contract retires it", "a purge against its contract says so")
	x.NotContains(out, "kept until")
	x.NotContains(out, "forget", "its people are the tenant's at roster (D85)")
	here, err := d.s.Ent.Tenant.Query().Where(tenant.Alias("acme")).Exist(ctx)
	x.NoError(err)
	x.True(here, "nothing was taken")
	x.Equal(before, written(t, d), "and nothing was written")
	x.NoError(signsIn("kim", "kim-password"), "a purge that was only asked about")

	out = must("tenant", "purge", "--yes", "acme")
	x.Contains(out, "acme: took")
	here, err = d.s.Ent.Tenant.Query().Where(tenant.Alias("acme")).Exist(ctx)
	x.NoError(err)
	x.False(here)
	x.NoError(signsIn("kim", "kim-password"), "its people are the tenant's at roster, and stay there (D85)")

	before = written(t, d)
	out = must("tenant", "import", "--file", zip)
	x.Contains(out, "@acme: would put back")
	x.NotContains(out, "roster does not have", "every member is somebody this roster has")
	x.Contains(out, "`--yes` does it")
	here, err = d.s.Ent.Tenant.Query().Where(tenant.Alias("acme")).Exist(ctx)
	x.NoError(err)
	x.False(here, "nothing was put back")
	x.Equal(before, written(t, d), "and nothing was written")

	// From stdin, as a file leaves a box through a pipe.
	f, err := os.Open(zip)
	x.NoError(err)
	defer f.Close()
	k := root()
	k.ReadCloser = io.NopCloser(f)
	out, err = ran(t, k, "--url", d.url, "--key", d.key, "tenant", "import", "--file", "-", "--yes")
	x.NoError(err, out)
	x.Contains(out, "@acme: put back")
	x.Contains(out, "from the export whose manifest is sha256:")
	_, err = d.loft(t, "tenant", "import", "--file", zip, "--yes")
	x.ErrorContains(err, offboard.ErrNotNew.Error(), "put back twice")
	// Every row put back is on the trail as the restore that put it there
	// (payday#108), so a check of the rows finds nothing.
	must("trail", "rows")
	x.NoError(signsIn("owner", "owner1234"), "as they were")
	x.Contains(must("identity", "link"), "nobody to make: roster has every member here", "every member is roster's as they were")
	must("trail", "rows")
	back, err := d.s.Ent.Tenant.Query().Where(tenant.Alias("acme")).Only(ctx)
	x.NoError(err)
	ev, err := d.s.Ent.Event.Query().Where(event.TenantId(back.Id), event.Kind("workspace.import")).Only(ctx)
	x.NoError(err)
	x.NotNil(ev.ActorId)
	x.Equal(alice, *ev.ActorId, "the restore is the operator's")
	n, err := d.s.Ent.Audit.Query().Where(entaudit.Action(offboard.Imported), entaudit.ActorId(alice)).Count(ctx)
	x.NoError(err)
	x.Positive(n, "and so is what the trail says it put back")

	// A retirement, and what is left of it -- and one a contract says purges
	// says so before it is asked to retire.
	x.Contains(must("tenant", "retired"), "no workspace has retired")
	out = must("tenant-contract", "add", "--name", "abroad", "--desc", "under another law", "--purge-floors", "--yes", "acme")
	x.Contains(out, "then purge\tunder another law")
	x.Contains(must("tenant", "retire", "acme"), "its contract purges it", "a retirement against its contract says so")
	beta, err := d.s.Ent.Tenant.Query().Where(tenant.Alias("beta")).Only(ctx)
	x.NoError(err)
	out = must("tenant", "retire", "--yes", "beta")
	x.Contains(out, "beta: took")
	x.Contains(out, "kept until ")
	// Named by its identifier alone: its address is no tenant's this
	// deployment serves at roster, and nothing else of it is left here.
	out = must("tenant", "retired")
	x.Contains(out, "-\t"+pdid.Id(beta.Id).String()+"\t")
	x.Contains(out, "kept until")
}

// changed is the zip `b` with one byte of each file `which` names changed --
// the first `owner` in it made `0wner` -- and everything else as it was.
func changed(t *testing.T, b []byte, which func(name string) bool) []byte {
	t.Helper()
	x := require.New(t)
	z, err := zip.NewReader(bytes.NewReader(b), int64(len(b)))
	x.NoError(err)
	var out bytes.Buffer
	w := zip.NewWriter(&out)
	for _, f := range z.File {
		r, err := f.Open()
		x.NoError(err)
		v, err := io.ReadAll(r)
		x.NoError(r.Close())
		x.NoError(err)
		if which(f.Name) {
			v = bytes.Replace(v, []byte("owner"), []byte("0wner"), 1)
		}
		g, err := w.Create(f.Name)
		x.NoError(err)
		_, err = g.Write(v)
		x.NoError(err)
	}
	x.NoError(w.Close())
	return out.Bytes()
}

// TestAWorkspaceLeavesAndComesBackThroughStdio: an export written to stdout
// is the whole of stdout -- what the command says goes to stderr -- and is
// verified and restored from stdin, through temporary files that outlive
// neither the command nor the call. It is how an export leaves a container
// with nothing in it to copy a file with, and comes back into one: `kubectl
// exec` (loft#126).
func TestAWorkspaceLeavesAndComesBackThroughStdio(t *testing.T) {
	x := require.New(t)
	tmp := t.TempDir()
	t.Setenv("TMPDIR", tmp)
	d := archived(t)

	// stdio runs `loft` with `in` as its stdin, and answers with what
	// it wrote to stdout and to stderr.
	stdio := func(in []byte, args ...string) (string, string) {
		t.Helper()
		var out, said bytes.Buffer
		k := root()
		k.ReadCloser, k.Writer, k.ErrWriter = io.NopCloser(bytes.NewReader(in)), &out, &said
		x.NoError(k.Run(t.Context(), append([]string{"--url", d.url, "--key", d.key}, args...)), "%s: %s", strings.Join(args, " "), said.String())
		return out.String(), said.String()
	}

	archive, said := stdio(nil, "--reason", "leaving", "tenant", "export", "--out", "-", "acme")
	_, err := zip.NewReader(strings.NewReader(archive), int64(len(archive)))
	x.NoError(err, "stdout is the archive, and nothing else")
	x.Contains(said, "stdout: ", "and what the command says is on stderr")

	verified, _ := stdio([]byte(archive), "tenant", "verify", "--file", "-")
	x.Contains(verified, "stdin: acme, exported")

	stdio(nil, "tenant", "purge", "--yes", "acme")
	restored, _ := stdio([]byte(archive), "tenant", "import", "--file", "-", "--yes")
	x.Contains(restored, "@acme: put back")

	left, err := filepath.Glob(filepath.Join(tmp, "loft-*"))
	x.NoError(err)
	x.Empty(left, "a copy written on the way does not outlive what wrote it")
}

// TestAContractShowsWhatTheWorkspaceKeeps (D36): how long a workspace keeps
// its history is its own setting, which `tenant show` says beside its
// contracts -- a window made shorter, until when the longer one holds -- and
// which no contract writes.
func TestAContractShowsWhatTheWorkspaceKeeps(t *testing.T) {
	x := require.New(t)
	ctx := context.Background()
	d := deploy(t)

	out, err := d.loft(t, "tenant", "show", "acme")
	x.NoError(err, out)
	x.Contains(out, "keeps\thistory forever,", "a workspace that says nothing")
	x.Contains(out, "contract\tnone: destroyed, it retires")

	tn, err := d.s.Ent.Tenant.Query().Where(tenant.Alias("acme")).Only(ctx)
	x.NoError(err)
	since := time.Now().Add(-24 * time.Hour)
	_, err = d.s.Ent.Tenant.UpdateOneId(tn.Id).SetKeepDays(180).SetKeepDaysBefore(0).SetDateKeepShortened(since).Save(ctx)
	x.NoError(err)
	out, err = d.loft(t, "tenant", "show", "acme")
	x.NoError(err, out)
	x.Contains(out, "keeps\thistory forever,", "made shorter yesterday, and the grace not over")
	x.Contains(out, "history\tuntil "+since.Add(30*24*time.Hour).UTC().Format(time.DateOnly)+", then 180d")
}

// TestAWorkspaceUnderAHoldIsNotDestroyed: a hold refuses the destruction
// before anything is taken, said as what was refused.
func TestAWorkspaceUnderAHoldIsNotDestroyed(t *testing.T) {
	x := require.New(t)
	d := archived(t)
	_, err := d.loft(t, "legal-hold", "add", "--why", "a case", "--yes", "acme")
	x.NoError(err)
	_, err = d.loft(t, "tenant", "purge", "--yes", "acme")
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
	out, err := d.loft(t, "--reason", "a request", "tenant", "export", "--out", zip, "acme")
	x.NoError(err, out)
	x.Contains(out, "unsigned")
	st, err := os.Stat(zip)
	x.NoError(err)
	x.Positive(st.Size())
	_, err = d.loft(t, "tenant", "verify", "--file", zip)
	x.ErrorContains(err, "nothing to hold a file to")
}
