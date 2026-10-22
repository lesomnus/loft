package cli_test

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/base64"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lesomnus/payday/config"
	"github.com/lesomnus/payday/pdtest"
	"github.com/lesomnus/payday/trail"
	"github.com/lesomnus/xli"
	"github.com/stretchr/testify/require"

	"github.com/lesomnus/rove/cli"
	"github.com/lesomnus/rove/cmd"
	"github.com/lesomnus/rove/internal/ent/tenant"
	"github.com/lesomnus/rove/internal/identity"
	"github.com/lesomnus/rove/server/offboard"
)

// run is a command's output, run the way a shell runs it.
func run(t *testing.T, k *xli.Command, args ...string) string {
	t.Helper()
	x := require.New(t)

	r, w, err := os.Pipe()
	x.NoError(err)
	was := os.Stdout
	os.Stdout = w
	err = k.Run(t.Context(), args)
	os.Stdout = was
	x.NoError(w.Close())

	b, e := io.ReadAll(r)
	x.NoError(e)
	x.NoError(err, "%s: %s", strings.Join(args, " "), b)

	return string(b)
}

// TestAPurgeTakesTheWorkspacesOwnAccountsOutOfTheRosterInThisProcess.
//
// The roster in this process holds nothing but Rove's accounts, and nothing
// but Rove can reach it: an account that was in the purged workspace and in no
// other goes with it. One in another workspace is still somebody's.
func TestAPurgeTakesTheWorkspacesOwnAccountsOutOfTheRosterInThisProcess(t *testing.T) {
	x := require.New(t)
	ctx := context.Background()

	c := cmd.Config{}
	c.Db.Driver, c.Db.Dsn = pdtest.DB(t)
	c.Watch.Broker = "memory"
	c.App.Files = t.TempDir()
	c.Auth.Roster.Db.Driver, c.Auth.Roster.Db.Dsn = pdtest.DB(t)

	run(t, cli.NewCmdInit(&c), "--tenant", "acme", "--login", "owner", "--password", "owner1234")
	run(t, cli.NewCmdHolder(&c), "add", "--tenant", "acme", "--login", "kim", "--password", "kim-password")
	out := run(t, cli.NewCmdInit(&c), "--tenant", "beta", "--login", "owner")
	x.NotContains(out, "password", "an account there already is given a password")
	x.Error(cli.NewCmdInit(&c).Run(ctx, []string{"--tenant", "gamma", "--login", "owner", "--password", "something-else"}),
		"an account's password changed by making a workspace")

	signsIn := func(login, password string) error {
		s, err := cmd.Build(ctx, c)
		x.NoError(err)
		defer s.Close()
		_, err = s.Identity.Verify(ctx, login, password)
		return err
	}
	x.NoError(signsIn("kim", "kim-password"))

	out = run(t, cli.NewCmdTenant(&c), "purge", "--tenant", "acme")
	x.Contains(out, "roster in this process: would forget 1 account(s)")
	x.NoError(signsIn("kim", "kim-password"), "a purge that was only asked about")

	out = run(t, cli.NewCmdTenant(&c), "purge", "--tenant", "acme", "--yes")
	x.Contains(out, "roster in this process: forgot 1 account(s)")
	x.ErrorIs(signsIn("kim", "kim-password"), identity.ErrRefused)
	x.NoError(signsIn("owner", "owner1234"), "an account another workspace has went with it")

	s, err := cmd.Build(ctx, c)
	x.NoError(err)
	defer s.Close()
	gone, err := s.Ent.Tenant.Query().Where(tenant.Alias("acme")).Exist(ctx)
	x.NoError(err)
	x.False(gone, "the workspace is still here")
}

// TestAnOperatorRetiresAWorkspace (payday#115): `rove tenant retire` takes a
// workspace out as `purge` does but for what the deployment's floors are on of
// its trail, and says what it keeps and until when. Each says so when the
// workspace's contract says the other (`rove contract set --purge-floors`).
func TestAnOperatorRetiresAWorkspace(t *testing.T) {
	x := require.New(t)
	ctx := context.Background()

	c := cmd.Config{}
	c.Db.Driver, c.Db.Dsn = pdtest.DB(t)
	c.Watch.Broker = "memory"
	c.App.Files = t.TempDir()
	c.Auth.Roster.Db.Driver, c.Auth.Roster.Db.Dsn = pdtest.DB(t)
	c.Audit.Archive = t.TempDir()
	c.Audit.Checkpoints.Key = pdtest.Key(t)
	c.Audit.By = map[string]config.AuditKeepConfig{"holder": {Min: config.AuditMinConfig{Destroy: 365 * 24 * time.Hour}}}
	run(t, cli.NewCmdInit(&c), "--tenant", "acme", "--login", "owner", "--password", "owner1234")
	run(t, cli.NewCmdInit(&c), "--tenant", "beta", "--login", "owner")

	out := run(t, cli.NewCmdContract(&c), "show", "--tenant", "acme")
	x.Contains(out, "destroyed: retired", "a workspace with no contract is purged")

	out = run(t, cli.NewCmdTenant(&c), "retire", "--tenant", "acme")
	x.Contains(out, "acme: would take")
	x.Contains(out, "for the deployment's floors: holder", "a retirement keeps nothing a floor is on")
	x.NotContains(out, "its contract")
	out = run(t, cli.NewCmdTenant(&c), "purge", "--tenant", "acme")
	x.Contains(out, "its contract retires it", "a purge against its contract says nothing")
	x.NotContains(out, "kept until")

	out = run(t, cli.NewCmdContract(&c), "set", "--tenant", "beta", "--name", "abroad", "--desc", "under another law", "--purge-floors")
	x.Contains(out, "destroyed: purged")
	out = run(t, cli.NewCmdContract(&c), "show", "--tenant", "beta")
	x.Contains(out, "then purge  under another law")
	out = run(t, cli.NewCmdTenant(&c), "retire", "--tenant", "beta")
	x.Contains(out, "its contract purges it", "a retirement against its contract says nothing")

	out = run(t, cli.NewCmdTenant(&c), "retire", "--tenant", "acme", "--yes")
	x.Contains(out, "acme: took")
	x.Contains(out, "kept until ")

	s, err := cmd.Build(ctx, c)
	x.NoError(err)
	defer s.Close()
	gone, err := s.Ent.Tenant.Query().Where(tenant.Alias("acme")).Exist(ctx)
	x.NoError(err)
	x.False(gone, "the workspace is still here")
}

// TestAnOperatorVerifiesAnExport: what an export says of itself, held to the
// deployment's key -- an export this deployment wrote is verified, and one
// somebody changed afterwards is not (design D34).
func TestAnOperatorVerifiesAnExport(t *testing.T) {
	x := require.New(t)
	key, _, err := trail.GenerateKey()
	x.NoError(err)
	c := cmd.Config{}
	c.Db.Driver, c.Db.Dsn = pdtest.DB(t)
	c.Watch.Broker = "memory"
	c.App.Files = t.TempDir()
	c.Auth.Roster.Db.Driver, c.Auth.Roster.Db.Dsn = pdtest.DB(t)
	c.Audit.Checkpoints.Key = string(key)
	run(t, cli.NewCmdInit(&c), "--tenant", "acme", "--login", "owner", "--password", "owner1234")

	out := filepath.Join(t.TempDir(), "acme.zip")
	run(t, cli.NewCmdTenant(&c), "export", "--tenant", "acme", "--out", out)
	verified := run(t, cli.NewCmdTenant(&c), "verify", "--file", out)
	x.Contains(verified, "acme, exported")
	x.Contains(verified, "manifest sha256:")

	// The same archive with one byte of one row changed.
	b, err := os.ReadFile(out)
	x.NoError(err)
	z, err := zip.NewReader(bytes.NewReader(b), int64(len(b)))
	x.NoError(err)
	var changed bytes.Buffer
	w := zip.NewWriter(&changed)
	for _, f := range z.File {
		r, err := f.Open()
		x.NoError(err)
		v, err := io.ReadAll(r)
		r.Close()
		x.NoError(err)
		if f.Name == "rows/tenant.jsonl" || strings.HasPrefix(f.Name, "rows/holder") {
			v = bytes.Replace(v, []byte("owner"), []byte("0wner"), 1)
		}
		g, err := w.Create(f.Name)
		x.NoError(err)
		_, err = g.Write(v)
		x.NoError(err)
	}
	x.NoError(w.Close())
	x.NoError(os.WriteFile(out, changed.Bytes(), 0o600))
	err = cli.NewCmdTenant(&c).Run(t.Context(), []string{"verify", "--file", out})
	x.ErrorIs(err, offboard.ErrAltered)
}

// TestAnOperatorRestoresAWorkspace: exported, purged and imported, a workspace
// is back; a member whose account went with the purge is restored with none,
// and `rove identity migrate` gives it one (design D34).
func TestAnOperatorRestoresAWorkspace(t *testing.T) {
	x := require.New(t)
	ctx := context.Background()
	key, _, err := trail.GenerateKey()
	x.NoError(err)
	c := cmd.Config{}
	c.Db.Driver, c.Db.Dsn = pdtest.DB(t)
	c.Watch.Broker = "memory"
	c.App.Files = t.TempDir()
	c.Auth.Roster.Db.Driver, c.Auth.Roster.Db.Dsn = pdtest.DB(t)
	c.Audit.Checkpoints.Key = string(key)
	c.Audit.Digest.Key = "base64:" + base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{7}, 32))

	run(t, cli.NewCmdInit(&c), "--tenant", "acme", "--login", "owner", "--password", "owner1234")
	run(t, cli.NewCmdHolder(&c), "add", "--tenant", "acme", "--login", "kim", "--password", "kim-password")
	// The owner is in another workspace too, so its account outlives the purge.
	run(t, cli.NewCmdInit(&c), "--tenant", "beta", "--login", "owner")

	out := filepath.Join(t.TempDir(), "acme.zip")
	run(t, cli.NewCmdTenant(&c), "export", "--tenant", "acme", "--out", out)
	run(t, cli.NewCmdTenant(&c), "purge", "--tenant", "acme", "--yes")

	here := func() bool {
		s, err := cmd.Build(ctx, c)
		x.NoError(err)
		defer s.Close()
		v, err := s.Ent.Tenant.Query().Where(tenant.Alias("acme")).Exist(ctx)
		x.NoError(err)
		return v
	}
	signsIn := func(login, password string) error {
		s, err := cmd.Build(ctx, c)
		x.NoError(err)
		defer s.Close()
		_, err = s.Identity.Verify(ctx, login, password)
		return err
	}

	dry := run(t, cli.NewCmdTenant(&c), "import", "--file", out)
	x.Contains(dry, "@acme: would put back")
	x.Contains(dry, "1 member(s) whose accounts this roster does not have, restored with none: kim")
	x.Contains(dry, "nothing was put back")
	x.False(here(), "a dry run put the workspace back")

	done := run(t, cli.NewCmdTenant(&c), "import", "--file", out, "--yes")
	x.Contains(done, "@acme: put back")
	x.Contains(done, "from the export whose manifest is sha256:")
	x.True(here())
	// Every row put back is on the trail as the restore that put it there
	// (payday#108), so a check of the rows finds nothing.
	run(t, cli.NewCmdTrail(&c), "rows")
	x.ErrorIs(cli.NewCmdTenant(&c).Run(ctx, []string{"import", "--file", out, "--yes"}), offboard.ErrNotNew)

	x.NoError(signsIn("owner", "owner1234"), "the owner's account is the one it had")
	x.Error(signsIn("kim", "kim-password"), "an account the purge forgot")

	m := run(t, cli.NewCmdIdentity(&c), "migrate")
	_, after, ok := strings.Cut(m, "@acme/kim  login kim  password ")
	x.True(ok, "%s", m)
	password, _, _ := strings.Cut(after, "\n")
	x.NoError(signsIn("kim", strings.TrimSpace(password)))
	run(t, cli.NewCmdTrail(&c), "rows")
}

// TestAWorkspaceLeavesAndComesBackThroughStdio: an export written to stdout is
// the whole of stdout -- what the command says goes to stderr -- and is
// verified and restored from stdin, through a temporary file that does not
// outlive the command. It is how an export leaves a container with nothing in
// it to copy a file with, and comes back into one: `kubectl exec` (rove#126).
func TestAWorkspaceLeavesAndComesBackThroughStdio(t *testing.T) {
	x := require.New(t)
	ctx := context.Background()
	key, _, err := trail.GenerateKey()
	x.NoError(err)
	c := cmd.Config{}
	c.Db.Driver, c.Db.Dsn = pdtest.DB(t)
	c.Watch.Broker = "memory"
	c.App.Files = t.TempDir()
	c.Auth.Roster.Db.Driver, c.Auth.Roster.Db.Dsn = pdtest.DB(t)
	c.Audit.Checkpoints.Key = string(key)
	tmp := t.TempDir()
	t.Setenv("TMPDIR", tmp)

	run(t, cli.NewCmdInit(&c), "--tenant", "acme", "--login", "owner", "--password", "owner1234")
	run(t, cli.NewCmdInit(&c), "--tenant", "beta", "--login", "owner")

	// stdio runs `rove tenant` with in as its stdin, and answers with what it
	// wrote to stdout and to stderr.
	stdio := func(in []byte, args ...string) (string, string) {
		t.Helper()
		var out, said bytes.Buffer
		k := cli.NewCmdTenant(&c)
		k.ReadCloser, k.Writer, k.ErrWriter = io.NopCloser(bytes.NewReader(in)), &out, &said
		x.NoError(k.Run(ctx, args), "%s: %s", strings.Join(args, " "), said.String())
		return out.String(), said.String()
	}

	archive, said := stdio(nil, "export", "--tenant", "acme", "--out", "-")
	_, err = zip.NewReader(strings.NewReader(archive), int64(len(archive)))
	x.NoError(err, "stdout is the archive, and nothing else")
	x.Contains(said, "stdout: ", "and what the command says is on stderr")

	verified, _ := stdio([]byte(archive), "verify", "--file", "-")
	x.Contains(verified, "stdin: acme, exported")

	run(t, cli.NewCmdTenant(&c), "purge", "--tenant", "acme", "--yes")
	restored, _ := stdio([]byte(archive), "import", "--file", "-", "--yes")
	x.Contains(restored, "@acme: put back")

	left, err := filepath.Glob(filepath.Join(tmp, "rove-export-*.zip"))
	x.NoError(err)
	x.Empty(left, "the copy stdin was written to does not outlive the command")
}

// TestAContractShowsWhatTheWorkspaceKeeps (D36): how long a workspace keeps its
// history is its own setting, which `rove contract show` says beside its
// contracts -- a window made shorter, until when the longer one holds -- and
// which no contract writes any more.
func TestAContractShowsWhatTheWorkspaceKeeps(t *testing.T) {
	x := require.New(t)
	ctx := context.Background()

	c := cmd.Config{}
	c.Db.Driver, c.Db.Dsn = pdtest.DB(t)
	c.Watch.Broker = "memory"
	c.App.Files = t.TempDir()
	c.Auth.Roster.Db.Driver, c.Auth.Roster.Db.Dsn = pdtest.DB(t)
	run(t, cli.NewCmdInit(&c), "--tenant", "acme", "--login", "owner", "--password", "owner1234")

	out := run(t, cli.NewCmdContract(&c), "show", "--tenant", "acme")
	x.Contains(out, "history: kept forever", "a workspace that says nothing")
	x.Contains(out, "no contract: a destroyed workspace retires")

	s, err := cmd.Build(ctx, c)
	x.NoError(err)
	tn, err := s.Ent.Tenant.Query().Where(tenant.Alias("acme")).Only(ctx)
	x.NoError(err)
	since := time.Now().Add(-24 * time.Hour)
	_, err = s.Ent.Tenant.UpdateOneId(tn.Id).SetKeepDays(180).SetKeepDaysBefore(0).SetDateKeepShortened(since).Save(ctx)
	x.NoError(err)
	s.Close()

	out = run(t, cli.NewCmdContract(&c), "show", "--tenant", "acme")
	x.Contains(out, "history: kept forever", "made shorter yesterday, and the grace not over")
	x.Contains(out, "until "+since.Add(30*24*time.Hour).UTC().Format(time.DateOnly)+", then 180d")

	err = cli.NewCmdContract(&c).Run(ctx, []string{"set", "--tenant", "acme", "--name", "pro", "--keep", "30"})
	x.Error(err, "a workspace's own window, written by a contract")
}
