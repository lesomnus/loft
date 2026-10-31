package cli_test

import (
	"bytes"
	"compress/gzip"
	"encoding/base64"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/lesomnus/flob"
	payaudit "github.com/lesomnus/payday/audit"
	"github.com/lesomnus/payday/pdid"
	"github.com/lesomnus/payday/pdtest"
	pdtrail "github.com/lesomnus/payday/trail"
	"github.com/lesomnus/xli"
	entsql "github.com/protobuf-orm/ent/dialect/sql"
	"github.com/stretchr/testify/require"

	"github.com/lesomnus/loft/cli"
	"github.com/lesomnus/loft/cmd"
	"github.com/lesomnus/loft/internal/ent"
	entaudit "github.com/lesomnus/loft/internal/ent/audit"
	"github.com/lesomnus/loft/internal/ent/holder"
	"github.com/lesomnus/loft/internal/ent/rowseal"
	enttenant "github.com/lesomnus/loft/internal/ent/tenant"
	"github.com/lesomnus/loft/server/bare"
	"github.com/lesomnus/loft/server/pd"
)

// TestWhatTheTrailOwesIsSaid (payday#115): `loft trail owed` says what is
// owed of the trail's erasures and not paid -- how many, and since when -- and
// how many of them a legal hold keeps, by workspace, which is what waits for
// it to lift. With nothing owed it says that.
func TestWhatTheTrailOwesIsSaid(t *testing.T) {
	x := require.New(t)
	ctx := t.Context()

	c := cmd.Config{}
	c.Db.Driver, c.Db.Dsn = pdtest.DB(t)
	c.Watch.Broker = "memory"
	c.App.Files = t.TempDir()
	c.Auth.Roster.Db.Driver, c.Auth.Roster.Db.Dsn = pdtest.DB(t)
	run(t, cli.NewCmdInit(&c), "--tenant", "acme", "--login", "owner", "--password", "owner-pass-1234")

	out, err := printed(t, cli.NewCmdTrail(&c), "owed")
	x.NoError(err)
	x.Contains(out, "nothing of the trail's erasures is owed")

	// Two owed, and one of them kept by acme's hold, as a payment under a
	// hold leaves it.
	s, err := cmd.Build(ctx, c)
	x.NoError(err)
	defer s.Close()
	acme, err := s.Ent.Tenant.Query().Where(enttenant.Alias("acme")).Only(ctx)
	x.NoError(err)
	store := pd.TrailStore(s.Ent)
	x.NoError(store.Owe(ctx, []pdid.Id{pdid.New(pd.HolderDomain), pdid.New(pd.HolderDomain)}))
	vs, err := store.Owed(ctx, pdtrail.Cursor{}, 10)
	x.NoError(err)
	x.Len(vs, 2)
	x.NoError(store.Kept(ctx, vs[0].Key, []pdid.Id{pdid.Id(acme.Id)}))

	out, err = printed(t, cli.NewCmdTrail(&c), "owed")
	x.NoError(err)
	x.Contains(out, "2 erasure(s) owed, the oldest since ")
	x.Contains(out, "1 kept by a legal hold of @acme (")
}

// TestRowsNobodySignedAreAcceptedOnce is the one step a deployment takes whose
// archive's account has rows nobody signed: written by a build from before
// rows were signed (lesomnus/payday#58, loft#58), or by one that kept the
// archive without a key at all (payday#115) -- which is not a state this build
// can make, since it keeps no archive without one. A row nobody signed looks the
// same whether it was written so or had its signature taken off, which is how
// this makes one, and each is a finding until somebody who has looked runs
// `loft trail accept`, once. After that `verify` is clean, and what a pass
// writes from then on is signed as it goes.
func TestRowsNobodySignedAreAcceptedOnce(t *testing.T) {
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
	run(t, cli.NewCmdInit(&c), "--tenant", "acme", "--login", "owner", "--password", "owner-pass-1234")

	// The deployment as `serve` builds it: a pass, as the sweep runs one, and
	// the database by hand.
	with := func(fn func(s *cmd.Server)) {
		s, err := cmd.Build(ctx, c)
		x.NoError(err)
		defer s.Close()
		fn(s)
	}
	pass := func() { with(func(s *cmd.Server) { s.Trail.Pass(ctx, pd.TrailStore(s.Ent)) }) }
	trail := func(args ...string) error { return cli.NewCmdTrail(&c).Run(ctx, args) }

	pass()
	x.NoError(trail("verify"))

	// The rows as a build that did not sign them left them.
	with(func(s *cmd.Server) {
		n, err := s.Ent.Archived.Update().SetSignature([]byte{}).SetSignedBy([]byte{}).Save(ctx)
		x.NoError(err)
		x.NotZero(n, "the account has no rows, so this proves nothing")
	})

	out, err := printed(t, cli.NewCmdTrail(&c), "verify")
	x.ErrorContains(err, "findings", "rows nobody signed were taken for signed ones")
	of := digestOf(t, out)
	x.ErrorContains(err, "--of "+of, "verify does not say what an acceptance of it names")

	x.ErrorContains(trail("accept", "--why", "looked at"), "--of")
	x.ErrorContains(trail("accept", "--of", of), "--why")

	// Of findings other than these, it is refused, and says to verify again.
	err = trail("accept", "--of", nothingLookedAt, "--why", "not what was looked at")
	x.ErrorIs(err, pdtrail.ErrUnreviewed)
	x.ErrorContains(err, "loft trail verify")

	x.NoError(trail("accept", "--of", of, "--by", "the test", "--why", "written before this deployment signed its rows"))
	out, err = printed(t, cli.NewCmdTrail(&c), "verify")
	x.NoError(err)
	x.Contains(out, "by the test: written before this deployment signed its rows (unsigned ", "verify does not say what was accepted")

	// And what is written now is signed as it is written.
	run(t, cli.NewCmdHolder(&c), "add", "--tenant", "acme", "--login", "kim", "--role", "member", "--password", "kim-password")
	pass()
	x.NoError(trail("verify"))
}

// TestADatabaseRestoredFromABackupIsAcceptedOnce is the runbook's step after the
// database is restored from a backup older than the latest checkpoint
// (docs/runbook/trail-integrity.md, lesomnus/payday#70). `verify` finds it
// behind the checkpoint store, and a pass takes no checkpoint while it is; one
// `loft trail accept --restored`, naming the checkpoint the backup holds,
// carries it on, numbered past the store's checkpoints, and `verify` is clean
// at once. An acceptance that names no backup, or another, is refused
// (payday#115).
func TestADatabaseRestoredFromABackupIsAcceptedOnce(t *testing.T) {
	x := require.New(t)
	ctx := t.Context()

	var err error
	c := cmd.Config{}
	c.Db.Driver, c.Db.Dsn = pdtest.DB(t)
	c.Watch.Broker = "memory"
	c.App.Files = t.TempDir()
	c.Auth.Roster.Db.Driver, c.Auth.Roster.Db.Dsn = pdtest.DB(t)
	c.Audit.Archive = t.TempDir()
	c.Audit.Retain = time.Nanosecond
	c.Audit.Checkpoints.Key = pdtest.Key(t)
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
	_, err = printed(t, cli.NewCmdTrail(&c), "verify")
	x.ErrorContains(err, "--restored", "verify does not say a restore names its checkpoint")

	holder("lee")
	pass()
	x.Equal(then, latest(), "a checkpoint was taken over a database behind the store")

	out, _ := printed(t, cli.NewCmdTrail(&c), "verify")
	of := digestOf(t, out)

	// A restore is accepted as one, naming the checkpoint its backup holds.
	err = trail("accept", "--of", of, "--why", "the database restored from the backup")
	x.ErrorIs(err, pdtrail.ErrRestored)
	x.ErrorContains(err, "loft trail verify")
	x.ErrorIs(trail("accept", "--of", of, "--restored", strconv.Itoa(then+1), "--why", "the wrong backup"), pdtrail.ErrRestored)

	x.NoError(trail("accept", "--of", of, "--restored", strconv.Itoa(then), "--why", "the database restored from the backup, and looked at"))
	x.Equal(since+1, latest())
	out, err = printed(t, cli.NewCmdTrail(&c), "verify")
	x.NoError(err)
	x.Contains(out, ", restoring checkpoint "+strconv.Itoa(then), "verify does not say what was accepted was a restore")
	x.NoError(trail("verify", "--full"))

	// And it goes on from there.
	holder("park")
	pass()
	x.Greater(latest(), since+1)
	x.NoError(trail("verify", "--full"))
}

// TestTheLiveRowsAreCheckedAgainstTheTrail is `loft trail rows`: with a digest
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

	out, err := printed(t, cli.NewCmdTrail(&c), "rows")
	x.ErrorContains(err, "1 findings")
	of := digestOf(t, out)
	x.ErrorContains(err, "--of "+of, "rows does not say what an acceptance of it names")

	// Somebody who has looked accepts it as it is: an act on the trail, which
	// says why (payday#108), and of what they looked at (payday#115).
	x.ErrorContains(trail("rows", "--accept", "--why", "looked at"), "--of: the digest")
	x.ErrorContains(trail("rows", "--accept", "--of", of), "--why")
	x.ErrorContains(trail("rows", "--accept", "--of", nothingLookedAt, "--why", "not what was looked at"), "--of: the check looked at was")
	x.ErrorContains(trail("rows"), "1 findings", "an acceptance of another check recorded something")
	x.NoError(trail("rows", "--accept", "--of", of, "--why", "the owner asked to step down, and a migration did it"))
	x.NoError(trail("rows"))

	// And a row nothing on the trail is about -- what one added in the
	// database past the servers looks like.
	s, err = cmd.Build(ctx, c)
	x.NoError(err)
	_, err = s.Ent.Audit.Delete().Where(entaudit.ObjectId(owner.Id)).Exec(ctx)
	x.NoError(err)
	s.Close()
	x.ErrorContains(trail("rows"), "1 findings")
	out = runErr(t, cli.NewCmdTrail(&c), "rows")
	x.Contains(out, "not-on-trail by kind: Holder 1", "named as this app names it")
	x.Contains(out, "not-on-trail Holder ")

	c.Audit.Digest.Key = ""
	x.ErrorContains(trail("rows"), "audit.digest.key", "no key, nothing to check against")
}

// TestAFullCheckIsAcceptedAsAFullOne is `loft trail accept --full`. Only
// `verify --full` reads a chunk's bytes, which is the one way a chunk whose
// bytes are not its digest is found (`altered`), so a full check's digest is
// not what a light one finds: an acceptance verifies again the way the check
// it names did, and one of a full check made without `--full` is of findings
// it never looked at (payday#115).
func TestAFullCheckIsAcceptedAsAFullOne(t *testing.T) {
	x := require.New(t)
	ctx := t.Context()

	dir := t.TempDir()
	c := cmd.Config{}
	c.Db.Driver, c.Db.Dsn = pdtest.DB(t)
	c.Watch.Broker = "memory"
	c.App.Files = t.TempDir()
	c.Auth.Roster.Db.Driver, c.Auth.Roster.Db.Dsn = pdtest.DB(t)
	c.Audit.Archive = dir
	c.Audit.Retain = time.Nanosecond
	c.Audit.Checkpoints.Key = pdtest.Key(t)
	run(t, cli.NewCmdInit(&c), "--tenant", "acme", "--login", "owner", "--password", "owner-pass-1234")
	trail := func(args ...string) error { return cli.NewCmdTrail(&c).Run(ctx, args) }

	s, err := cmd.Build(ctx, c)
	x.NoError(err)
	s.Trail.Pass(ctx, pd.TrailStore(s.Ent))
	s.Close()

	// A chunk's bytes written again beneath its name, as somebody who can
	// write the archive's disk would.
	cs, err := pdtrail.Chunks(ctx, flob.NewOsStores(dir))
	x.NoError(err)
	x.NotEmpty(cs, "the pass archived nothing, so this proves nothing")
	d := cs[0].Digest
	enc := d.Encoded()
	path := filepath.Join(dir, "share", d.Algorithm().String(), enc[0:2], enc[2:4], enc[4:])
	var buf bytes.Buffer
	z := gzip.NewWriter(&buf)
	_, err = z.Write([]byte(`{"id":"nobody wrote this"}` + "\n"))
	x.NoError(err)
	x.NoError(z.Close())
	x.NoError(os.Chmod(path, 0o600))
	x.NoError(os.WriteFile(path, buf.Bytes(), 0o600))

	x.NoError(trail("verify"), "a light check read a chunk's bytes")
	out, err := printed(t, cli.NewCmdTrail(&c), "verify", "--full")
	x.ErrorContains(err, "1 findings")
	x.ErrorContains(err, "--full --why", "verify --full does not say its acceptance is a full one")
	of := digestOf(t, out)

	x.ErrorIs(trail("accept", "--of", of, "--why", "looked at"), pdtrail.ErrUnreviewed, "a full check was accepted by a light one")
	x.NoError(trail("accept", "--of", of, "--full", "--why", "the chunk's bytes, looked at"))

	out, err = printed(t, cli.NewCmdTrail(&c), "verify")
	x.NoError(err)
	x.Contains(out, ": the chunk's bytes, looked at (altered 1)", "verify does not say what was accepted")
}

// TestRowsFromBeforeSealsAreSealedByAnAcceptance is the one acceptance an
// operator runs after the upgrade that brought seals (payday#115,
// docs/runbook/trail-integrity.md). A holder whose newest write sealed nothing
// -- made here by writing its newest line's digest as a build before seals
// would have, and taking its seal off -- is counted as from before seals, and
// is no finding. `rows --accept` of that check seals it, though the check found
// nothing, held to the digest looked at like any acceptance; the next check
// counts none.
func TestRowsFromBeforeSealsAreSealedByAnAcceptance(t *testing.T) {
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

	var owner *ent.Holder
	sealed := func() bool {
		s, err := cmd.Build(ctx, c)
		x.NoError(err)
		defer s.Close()
		ok, err := s.Ent.RowSeal.Query().Where(rowseal.IdEQ(owner.Id)).Exist(ctx)
		x.NoError(err)
		return ok
	}

	// As a build before seals left the owner's row.
	s, err := cmd.Build(ctx, c)
	x.NoError(err)
	cs, err := c.Audit.Contents()
	x.NoError(err)
	owner, err = s.Ent.Holder.Query().Where(holder.Role("owner")).Only(ctx)
	x.NoError(err)
	newest, err := s.Ent.Audit.Query().Where(entaudit.ObjectId(owner.Id)).Order(entaudit.ByDateCreated(entsql.OrderDesc())).First(ctx)
	x.NoError(err)
	q := s.Ent.Holder.Query().Where(holder.IdEQ(owner.Id))
	bare.HolderSelectInit(q, nil)
	row, err := q.Only(ctx)
	x.NoError(err)
	sum, err := cs.Digest.Of(pdid.Id(owner.Id), row.Proto(), newest.DigestFields, payaudit.Line{Id: newest.Id, Created: newest.DateCreated, Action: newest.Action})
	x.NoError(err)
	x.False(payaudit.Sealing(sum))
	x.NoError(s.Ent.Audit.UpdateOneId(newest.Id).SetDigest(sum).Exec(ctx))
	x.NoError(s.Ent.RowSeal.DeleteOneId(owner.Id).Exec(ctx))
	s.Close()

	out, err := printed(t, cli.NewCmdTrail(&c), "rows")
	x.NoError(err, "a row from before seals was taken for a finding")
	x.Contains(out, "1 rows from before seals")
	of := digestOf(t, out)

	x.ErrorContains(trail("rows", "--accept", "--of", nothingLookedAt, "--why", "not what was looked at"), "--of: the check looked at was")
	x.False(sealed(), "an acceptance of another check sealed it")

	out, err = printed(t, cli.NewCmdTrail(&c), "rows", "--accept", "--of", of, "--why", "written before seals")
	x.NoError(err)
	x.Contains(out, "sealed 1 from before seals")
	x.True(sealed(), "the acceptance sealed nothing")

	out, err = printed(t, cli.NewCmdTrail(&c), "rows")
	x.NoError(err)
	x.NotContains(out, "from before seals")
}

// TestAVerificationSaysWhatEachWitnessHeard: a deployment that names
// witnesses has every checkpoint heard outside the database and the archive
// (payday#115) -- `log:` and `store:`, which this binary links -- and `loft
// trail verify` says what each that answers heard last, beside the digest:
// nothing yet before the first checkpoint, and then the newest checkpoint's
// number and what the store keeps it under. A witness that only tells answers
// nothing, and is not among them.
func TestAVerificationSaysWhatEachWitnessHeard(t *testing.T) {
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
	c.Audit.Checkpoints.Witnesses = []string{"log:", "store:" + t.TempDir()}
	run(t, cli.NewCmdInit(&c), "--tenant", "acme", "--login", "owner", "--password", "owner-pass-1234")

	out, err := printed(t, cli.NewCmdTrail(&c), "verify")
	x.NoError(err)
	x.Contains(out, "heard by "+c.Audit.Checkpoints.Witnesses[1]+": nothing yet\n")

	s, err := cmd.Build(ctx, c)
	x.NoError(err)
	s.Trail.Pass(ctx, pd.TrailStore(s.Ent))
	s.Close()

	out, err = printed(t, cli.NewCmdTrail(&c), "verify")
	x.NoError(err)
	m := regexp.MustCompile(`(?m)^heard by (store:\S+): checkpoint ([0-9]+), (sha256:[0-9a-f]{64}), at \S+$`).FindStringSubmatch(out)
	x.NotNil(m, "verify does not say what the store witness heard:\n%s", out)
	x.Equal(c.Audit.Checkpoints.Witnesses[1], m[1])
	x.Contains(out, "checkpoint "+m[2]+" compared", "the witness heard another checkpoint than the newest")
	x.NotContains(out, "heard by log:", "a witness that only tells answered")
}

// printed is what a command printed, run the way a shell runs it, and what it
// ended in.
func printed(t *testing.T, k *xli.Command, args ...string) (string, error) {
	t.Helper()

	r, w, err := os.Pipe()
	require.NoError(t, err)

	read := make(chan []byte)
	go func() {
		b, _ := io.ReadAll(r)
		read <- b
	}()

	was := os.Stdout
	os.Stdout = w
	err = k.Run(t.Context(), args)
	os.Stdout = was
	require.NoError(t, w.Close())

	return string(<-read), err
}

// digestOf is the digest a check printed of what it found.
func digestOf(t *testing.T, out string) string {
	t.Helper()

	m := regexp.MustCompile(`(?m)^digest (sha256:[0-9a-f]+)$`).FindStringSubmatch(out)
	require.NotNil(t, m, "no digest in what it printed:\n%s", out)

	return m[1]
}

// nothingLookedAt is a digest no check printed.
var nothingLookedAt = "sha256:" + strings.Repeat("0", 64)

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
