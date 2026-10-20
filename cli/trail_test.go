package cli_test

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"io"
	"os"
	"testing"
	"time"

	"github.com/lesomnus/payday/pdtest"
	"github.com/lesomnus/xli"
	"github.com/stretchr/testify/require"

	"github.com/lesomnus/rove/cli"
	"github.com/lesomnus/rove/cmd"
	"github.com/lesomnus/rove/internal/ent"
	entaudit "github.com/lesomnus/rove/internal/ent/audit"
	"github.com/lesomnus/rove/internal/ent/holder"
	"github.com/lesomnus/rove/server/pd"
)

// TestRowsFromBeforeTheKeyAreAcceptedOnce is the one step a deployment that
// signs its checkpoints takes on the payday that signs the manifest's rows too
// (lesomnus/payday#58, rove#58). A row nobody signed looks the same whether it
// was written before rows were signed or had its signature taken off, so with
// a key each one is a finding until somebody who has looked runs `rove trail
// accept`, once. After that `verify` is clean, and what a pass writes from then
// on is signed as it goes.
func TestRowsFromBeforeTheKeyAreAcceptedOnce(t *testing.T) {
	x := require.New(t)
	ctx := t.Context()

	c := cmd.Config{}
	c.Db.Driver, c.Db.Dsn = pdtest.DB(t)
	c.Watch.Broker = "memory"
	c.App.Files = t.TempDir()
	c.Auth.Roster.Db.Driver, c.Auth.Roster.Db.Dsn = pdtest.DB(t)
	c.Audit.Archive = t.TempDir()
	c.Audit.Retain = time.Nanosecond
	run(t, cli.NewCmdInit(&c), "--tenant", "acme", "--login", "owner", "--password", "owner-pass-1234")

	// A pass of the policy, as the sweep in `serve` runs it.
	pass := func() {
		s, err := cmd.Build(ctx, c)
		x.NoError(err)
		defer s.Close()
		s.Trail.Pass(ctx, pd.TrailStore(s.Ent))
	}
	trail := func(args ...string) error { return cli.NewCmdTrail(&c).Run(ctx, args) }

	// Written while the deployment had no key.
	pass()
	x.NoError(trail("verify"))

	_, priv, err := ed25519.GenerateKey(rand.Reader)
	x.NoError(err)
	der, err := x509.MarshalPKCS8PrivateKey(priv)
	x.NoError(err)
	c.Audit.Checkpoints.Key = string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}))

	x.ErrorContains(trail("verify"), "findings", "rows nobody signed were taken for signed ones")
	x.ErrorContains(trail("accept"), "--why")
	x.NoError(trail("accept", "--why", "written before this deployment signed its rows"))
	x.NoError(trail("verify"))

	// And what is written now is signed as it is written.
	run(t, cli.NewCmdHolder(&c), "add", "--tenant", "acme", "--login", "kim", "--role", "member", "--password", "kim-password")
	pass()
	x.NoError(trail("verify"))
}

// TestADatabaseRestoredFromABackupIsAcceptedOnce is the runbook's step after the
// database is restored from a backup older than the latest checkpoint
// (docs/runbook/trail-integrity.md, lesomnus/payday#70). `verify` finds it
// behind the checkpoint store, and a pass takes no checkpoint while it is; one
// `rove trail accept` carries it on, numbered past the store's checkpoints,
// and `verify` is clean at once.
func TestADatabaseRestoredFromABackupIsAcceptedOnce(t *testing.T) {
	x := require.New(t)
	ctx := t.Context()

	_, priv, err := ed25519.GenerateKey(rand.Reader)
	x.NoError(err)
	der, err := x509.MarshalPKCS8PrivateKey(priv)
	x.NoError(err)

	c := cmd.Config{}
	c.Db.Driver, c.Db.Dsn = pdtest.DB(t)
	c.Watch.Broker = "memory"
	c.App.Files = t.TempDir()
	c.Auth.Roster.Db.Driver, c.Auth.Roster.Db.Dsn = pdtest.DB(t)
	c.Audit.Archive = t.TempDir()
	c.Audit.Retain = time.Nanosecond
	c.Audit.Checkpoints.Key = string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}))
	run(t, cli.NewCmdInit(&c), "--tenant", "acme", "--login", "owner", "--password", "owner-pass-1234")

	// The deployment as `serve` builds it, for what is done here and not at
	// a shell: a pass, as the sweep runs one, and the database by hand.
	with := func(fn func(s *cmd.Server)) {
		s, err := cmd.Build(ctx, c)
		x.NoError(err)
		defer s.Close()
		fn(s)
	}
	pass := func() { with(func(s *cmd.Server) { s.Trail.Pass(ctx, pd.TrailStore(s.Ent)) }) }
	latest := func() (k int) {
		with(func(s *cmd.Server) {
			k, _, err = pd.TrailStore(s.Ent).Latest(ctx)
			x.NoError(err)
		})
		return k
	}
	holder := func(login string) {
		run(t, cli.NewCmdHolder(&c), "add", "--tenant", "acme", "--login", login, "--role", "member", "--password", login+"-password")
	}
	trail := func(args ...string) error { return cli.NewCmdTrail(&c).Run(ctx, args) }

	pass()
	x.NoError(trail("verify"))
	then := latest()

	// The backup, of the database's account of the archive.
	var backup []*ent.Archived
	with(func(s *cmd.Server) {
		backup, err = s.Ent.Archived.Query().All(ctx)
		x.NoError(err)
	})

	holder("kim")
	pass()
	since := latest()
	x.Greater(since, then)

	// Restored, and the archive and its checkpoints not.
	with(func(s *cmd.Server) {
		_, err := s.Ent.Archived.Delete().Exec(ctx)
		x.NoError(err)
		for _, v := range backup {
			x.NoError(s.Ent.Archived.Create().
				SetId(v.Id).
				SetNamespace(v.Namespace).
				SetDigest(v.Digest).
				SetIntent(v.Intent).
				SetLabels(v.Labels).
				SetState(v.State).
				SetSince(v.Since).
				SetGone(v.Gone).
				SetSignature(v.Signature).
				SetSignedBy(v.SignedBy).
				SetDateCreated(v.DateCreated).
				SetDateUpdated(v.DateUpdated).
				Exec(ctx))
		}

		v, err := s.Trail.Verify(ctx, pd.TrailStore(s.Ent), false)
		x.NoError(err)
		found := []string{}
		for _, f := range v.Findings {
			found = append(found, f.Kind)
		}
		x.Contains(found, "behind")
	})
	x.ErrorContains(trail("verify"), "findings")

	holder("lee")
	pass()
	x.Equal(then, latest(), "a checkpoint was taken over a database behind the store")

	x.NoError(trail("accept", "--why", "the database restored from the backup, and looked at"))
	x.Equal(since+1, latest())
	x.NoError(trail("verify", "--full"))

	// And it goes on from there.
	holder("park")
	pass()
	x.Greater(latest(), since+1)
	x.NoError(trail("verify", "--full"))
}

// TestTheLiveRowsAreCheckedAgainstTheTrail is `rove trail rows`: with a digest
// key, what the deployment wrote matches what the trail keeps of it, and a row
// changed in the database itself, past the servers, does not (payday#102).
func TestTheLiveRowsAreCheckedAgainstTheTrail(t *testing.T) {
	x := require.New(t)
	ctx := t.Context()

	c := cmd.Config{}
	c.Db.Driver, c.Db.Dsn = pdtest.DB(t)
	c.Watch.Broker = "memory"
	c.App.Files = t.TempDir()
	c.Auth.Roster.Db.Driver, c.Auth.Roster.Db.Dsn = pdtest.DB(t)
	c.Audit.Digest.Key = "base64:" + base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{7}, 32))
	run(t, cli.NewCmdInit(&c), "--tenant", "acme", "--login", "owner", "--password", "owner-pass-1234")
	trail := func(args ...string) error { return cli.NewCmdTrail(&c).Run(ctx, args) }

	x.NoError(trail("rows"))

	// The owner's role, written in the database itself.
	s, err := cmd.Build(ctx, c)
	x.NoError(err)
	owner, err := s.Ent.Holder.Query().Where(holder.Role("owner")).Only(ctx)
	x.NoError(err)
	x.NoError(s.Ent.Holder.UpdateOneId(owner.Id).SetRole("member").Exec(ctx))
	s.Close()

	x.ErrorContains(trail("rows"), "1 rows differ")

	// Somebody who has looked accepts it as it is: an act on the trail, which
	// says why (payday#108).
	x.ErrorContains(trail("rows", "--accept"), "--why")
	x.NoError(trail("rows", "--accept", "--why", "the owner asked to step down, and a migration did it"))
	x.NoError(trail("rows"))

	// And a row nothing on the trail is about -- what one added in the
	// database past the servers looks like.
	s, err = cmd.Build(ctx, c)
	x.NoError(err)
	_, err = s.Ent.Audit.Delete().Where(entaudit.ObjectId(owner.Id)).Exec(ctx)
	x.NoError(err)
	s.Close()
	x.ErrorContains(trail("rows"), "1 are not on it")
	out := runErr(t, cli.NewCmdTrail(&c), "rows")
	x.Contains(out, "not-on-trail by kind: Holder 1", "named as this app names it")
	x.Contains(out, "not-on-trail Holder ")

	c.Audit.Digest.Key = ""
	x.ErrorContains(trail("rows"), "audit.digest.key", "no key, nothing to check against")
}

// runErr is a command's output, run the way a shell runs it, whatever it ends
// in.
func runErr(t *testing.T, k *xli.Command, args ...string) string {
	t.Helper()
	x := require.New(t)

	r, w, err := os.Pipe()
	x.NoError(err)
	was := os.Stdout
	os.Stdout = w
	_ = k.Run(t.Context(), args)
	os.Stdout = was
	x.NoError(w.Close())

	b, err := io.ReadAll(r)
	x.NoError(err)

	return string(b)
}
