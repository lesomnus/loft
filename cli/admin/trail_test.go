package admincli_test

import (
	"bytes"
	"compress/gzip"
	"crypto/ed25519"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"filippo.io/age"
	"github.com/lesomnus/flob"
	payaudit "github.com/lesomnus/payday/audit"
	"github.com/lesomnus/payday/config"
	"github.com/lesomnus/payday/pdid"
	"github.com/lesomnus/payday/pdpb"
	"github.com/lesomnus/payday/pdtest"
	pdtrail "github.com/lesomnus/payday/trail"
	entsql "github.com/protobuf-orm/ent/dialect/sql"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	app "github.com/lesomnus/loft"
	admincli "github.com/lesomnus/loft/cli/admin"
	"github.com/lesomnus/loft/cmd"
	entaccess "github.com/lesomnus/loft/internal/ent/access"
	entaudit "github.com/lesomnus/loft/internal/ent/audit"
	"github.com/lesomnus/loft/internal/ent/holder"
	"github.com/lesomnus/loft/internal/ent/rowseal"
	enttenant "github.com/lesomnus/loft/internal/ent/tenant"
	"github.com/lesomnus/loft/internal/loftest"
	"github.com/lesomnus/loft/server/admin"
	"github.com/lesomnus/loft/server/bare"
	"github.com/lesomnus/loft/server/operator"
	"github.com/lesomnus/loft/server/pd"
)

// digestOf is the digest a check printed of what it found.
func digestOf(t *testing.T, out string) string {
	t.Helper()
	m := regexp.MustCompile(`(?m)^digest (sha256:[0-9a-f]+)$`).FindStringSubmatch(out)
	require.NotNil(t, m, "no digest in what it printed:\n%s", out)
	return m[1]
}

// nothingLookedAt is a digest no check printed.
var nothingLookedAt = "sha256:" + strings.Repeat("0", 64)

// withArchive is a deployment that archives its trail at once, and signs it.
func withArchive(t *testing.T, dir string) func(*cmd.Config) {
	return func(c *cmd.Config) {
		c.Audit.Archive = dir
		c.Audit.Retain = time.Nanosecond
		c.Audit.Checkpoints.Key = pdtest.Key(t)
	}
}

// pass is a pass of the trail, as the sweep runs one.
func (d *deployment) pass(t *testing.T) {
	t.Helper()
	d.s.Trail.Pass(t.Context(), pd.TrailStore(d.s.Ent))
}

// holderAdd makes another account and its membership of acme, the
// deployment's writes; what a pass then has to archive.
func (d *deployment) holderAdd(t *testing.T, login string) {
	t.Helper()
	loftest.Member(t, d.s, "acme", login, "member", login+"-password")
}

// TestWhatTheTrailOwesIsSaid (payday#115): `trail owed` says what is owed of
// the trail's erasures and not paid -- how many, and since when -- and how
// many of them a legal hold keeps, by workspace. With nothing owed it says
// that.
func TestWhatTheTrailOwesIsSaid(t *testing.T) {
	x := require.New(t)
	ctx := t.Context()
	d := deploy(t)

	out, err := d.admin(t, "trail", "owed")
	x.NoError(err)
	x.Contains(out, "nothing of the trail's erasures is owed")

	acme, err := d.s.Ent.Tenant.Query().Where(enttenant.Alias("acme")).Only(ctx)
	x.NoError(err)
	store := pd.TrailStore(d.s.Ent)
	x.NoError(store.Owe(ctx, []pdid.Id{pdid.New(pd.HolderDomain), pdid.New(pd.HolderDomain)}))
	vs, err := store.Owed(ctx, pdtrail.Cursor{}, 10)
	x.NoError(err)
	x.Len(vs, 2)
	x.NoError(store.Kept(ctx, vs[0].Key, []pdid.Id{pdid.Id(acme.Id)}))

	out, err = d.admin(t, "trail", "owed")
	x.NoError(err)
	x.Contains(out, "2 erasure(s) owed, the oldest since ")
	x.Contains(out, "1 kept by a legal hold of @acme (")
}

// TestRowsNobodySignedAreAcceptedOnce: rows of the archive's account nobody
// signed are findings until an operator who looked accepts them, once --
// the operator whose key the call carries, said so after by `verify` -- and
// what is written after is signed as it goes.
func TestRowsNobodySignedAreAcceptedOnce(t *testing.T) {
	x := require.New(t)
	ctx := t.Context()
	d := deployWith(t, withArchive(t, t.TempDir()))

	d.pass(t)
	_, err := d.admin(t, "trail", "verify")
	x.NoError(err)

	n, err := d.s.Ent.Archived.Update().SetSignature([]byte{}).SetSignedBy([]byte{}).Save(ctx)
	x.NoError(err)
	x.NotZero(n, "the account has no rows, so this proves nothing")

	out, err := d.admin(t, "trail", "verify")
	x.ErrorContains(err, "findings", "rows nobody signed were taken for signed ones")
	of := digestOf(t, out)
	x.ErrorContains(err, "--of "+of, "verify does not say what an acceptance of it names")

	_, err = d.admin(t, "trail", "accept", "--why", "looked at")
	x.ErrorContains(err, "--of")
	_, err = d.admin(t, "trail", "accept", "--of", of)
	x.ErrorContains(err, "--why")
	_, err = d.admin(t, "trail", "accept", "--of", nothingLookedAt, "--why", "not what was looked at")
	x.ErrorContains(err, pdtrail.ErrUnreviewed.Error())
	x.ErrorContains(err, "`trail verify` again")

	out, err = d.admin(t, "trail", "accept", "--of", of, "--why", "written before this deployment signed its rows")
	x.NoError(err, out)
	x.Contains(out, "by alice")
	out, err = d.admin(t, "trail", "verify")
	x.NoError(err)
	x.Contains(out, "by alice: written before this deployment signed its rows (unsigned ", "verify does not say who accepted what")

	d.holderAdd(t, "lee")
	d.pass(t)
	_, err = d.admin(t, "trail", "verify")
	x.NoError(err)
}

// TestADatabaseRestoredFromABackupIsAcceptedOnce is the runbook's step after
// the database is restored from a backup older than the latest checkpoint
// (docs/runbook/trail-integrity.md): `verify` finds it behind, and one
// acceptance naming the checkpoint the backup holds carries it on.
func TestADatabaseRestoredFromABackupIsAcceptedOnce(t *testing.T) {
	x := require.New(t)
	ctx := t.Context()
	d := deployWith(t, withArchive(t, t.TempDir()))
	latest := func() int {
		k, _, err := pd.TrailStore(d.s.Ent).Latest(ctx)
		x.NoError(err)
		return k
	}

	d.pass(t)
	_, err := d.admin(t, "trail", "verify")
	x.NoError(err)
	then := latest()
	backup, err := d.s.Ent.Archived.Query().All(ctx)
	x.NoError(err)

	d.holderAdd(t, "lee")
	d.pass(t)
	since := latest()
	x.Greater(since, then)

	// Restored, and the archive and its checkpoints not.
	_, err = d.s.Ent.Archived.Delete().Exec(ctx)
	x.NoError(err)
	for _, v := range backup {
		x.NoError(d.s.Ent.Archived.Create().
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
	out, err := d.admin(t, "trail", "verify")
	x.ErrorContains(err, "--restored", "verify does not say a restore names its checkpoint")
	of := digestOf(t, out)

	_, err = d.admin(t, "trail", "accept", "--of", of, "--why", "the database restored from the backup")
	x.ErrorContains(err, pdtrail.ErrRestored.Error())
	_, err = d.admin(t, "trail", "accept", "--of", of, "--restored", strconv.Itoa(then+1), "--why", "the wrong backup")
	x.ErrorContains(err, pdtrail.ErrRestored.Error())

	_, err = d.admin(t, "trail", "accept", "--of", of, "--restored", strconv.Itoa(then), "--why", "the database restored from the backup, and looked at")
	x.NoError(err)
	x.Equal(since+1, latest())
	out, err = d.admin(t, "trail", "verify")
	x.NoError(err)
	x.Contains(out, ", restoring checkpoint "+strconv.Itoa(then))
	_, err = d.admin(t, "trail", "verify", "--full")
	x.NoError(err)
}

// TestTheLiveRowsAreCheckedAgainstTheTrail is `trail rows`: with a digest
// key, what the deployment wrote matches what the trail keeps of it, a row
// changed in the database itself does not, and an acceptance of what was
// looked at puts it on the trail as the operator's act (payday#108, #115).
func TestTheLiveRowsAreCheckedAgainstTheTrail(t *testing.T) {
	x := require.New(t)
	ctx := t.Context()
	key := "base64:" + base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{7}, 32))
	d := deployWith(t, func(c *cmd.Config) { c.Audit.Digest.Key = key })

	_, err := d.admin(t, "trail", "rows")
	x.NoError(err)

	owner, err := d.s.Ent.Holder.Query().Where(holder.Role("owner")).Only(ctx)
	x.NoError(err)
	x.NoError(d.s.Ent.Holder.UpdateOneId(owner.Id).SetRole("member").Exec(ctx))

	out, err := d.admin(t, "trail", "rows")
	x.ErrorContains(err, "1 findings")
	of := digestOf(t, out)
	x.ErrorContains(err, "--of "+of, "rows does not say what an acceptance of it names")

	_, err = d.admin(t, "trail", "rows", "--accept", "--why", "looked at")
	x.ErrorContains(err, "--of: the digest")
	_, err = d.admin(t, "trail", "rows", "--accept", "--of", of)
	x.ErrorContains(err, "--why")
	_, err = d.admin(t, "trail", "rows", "--accept", "--of", nothingLookedAt, "--why", "not what was looked at")
	x.ErrorContains(err, "the check looked at was")
	_, err = d.admin(t, "trail", "rows")
	x.ErrorContains(err, "1 findings", "an acceptance of another check recorded something")

	out, err = d.admin(t, "trail", "rows", "--accept", "--of", of, "--why", "the owner asked to step down, and a migration did it")
	x.NoError(err, out)
	x.Contains(out, "accepted 1 rows")
	_, err = d.admin(t, "trail", "rows")
	x.NoError(err)
	line, err := d.s.Ent.Audit.Query().Where(entaudit.ObjectId(owner.Id), entaudit.Action(admin.AcceptedRows)).Only(ctx)
	x.NoError(err)
	x.Equal(operator.Id("alice").Uuid(), line.ActorId, "the acceptance is the operator's act")

	// And a row nothing on the trail is about.
	_, err = d.s.Ent.Audit.Delete().Where(entaudit.ObjectId(owner.Id)).Exec(ctx)
	x.NoError(err)
	out, err = d.admin(t, "trail", "rows")
	x.ErrorContains(err, "1 findings")
	x.Contains(out, "not-on-trail by kind: Holder 1", "named as this app names it")
	x.Contains(out, "not-on-trail Holder ")

	_, err = deploy(t).admin(t, "trail", "rows")
	x.ErrorContains(err, "audit.digest.key", "no key, nothing to check against")
}

// TestAFullCheckIsAcceptedAsAFullOne is `trail accept --full`: only a full
// check reads a chunk's bytes, so an acceptance verifies again the way the
// check it names did (payday#115).
func TestAFullCheckIsAcceptedAsAFullOne(t *testing.T) {
	x := require.New(t)
	ctx := t.Context()
	dir := t.TempDir()
	d := deployWith(t, withArchive(t, dir))
	d.pass(t)

	cs, err := pdtrail.Chunks(ctx, flob.NewOsStores(dir))
	x.NoError(err)
	x.NotEmpty(cs, "the pass archived nothing, so this proves nothing")
	dg := cs[0].Digest
	enc := dg.Encoded()
	path := filepath.Join(dir, "share", dg.Algorithm().String(), enc[0:2], enc[2:4], enc[4:])
	var buf bytes.Buffer
	z := gzip.NewWriter(&buf)
	_, err = z.Write([]byte(`{"id":"nobody wrote this"}` + "\n"))
	x.NoError(err)
	x.NoError(z.Close())
	x.NoError(os.Chmod(path, 0o600))
	x.NoError(os.WriteFile(path, buf.Bytes(), 0o600))

	_, err = d.admin(t, "trail", "verify")
	x.NoError(err, "a light check read a chunk's bytes")
	out, err := d.admin(t, "trail", "verify", "--full")
	x.ErrorContains(err, "1 findings")
	x.ErrorContains(err, "--full --why", "verify --full does not say its acceptance is a full one")
	of := digestOf(t, out)

	_, err = d.admin(t, "trail", "accept", "--of", of, "--why", "looked at")
	x.ErrorContains(err, pdtrail.ErrUnreviewed.Error(), "a full check was accepted by a light one")
	_, err = d.admin(t, "trail", "accept", "--of", of, "--full", "--why", "the chunk's bytes, looked at")
	x.NoError(err)
	out, err = d.admin(t, "trail", "verify")
	x.NoError(err)
	x.Contains(out, ": the chunk's bytes, looked at (altered 1)")
}

// TestRowsFromBeforeSealsAreSealedByAnAcceptance is the one acceptance an
// operator runs after the upgrade that brought seals (payday#115): a row
// whose newest write sealed nothing is no finding, and `rows --accept` of
// that check seals it.
func TestRowsFromBeforeSealsAreSealedByAnAcceptance(t *testing.T) {
	x := require.New(t)
	ctx := t.Context()
	key := "base64:" + base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{7}, 32))
	d := deployWith(t, func(c *cmd.Config) { c.Audit.Digest.Key = key })

	cs, err := d.c.Audit.Contents()
	x.NoError(err)
	owner, err := d.s.Ent.Holder.Query().Where(holder.Role("owner")).Only(ctx)
	x.NoError(err)
	sealed := func() bool {
		ok, err := d.s.Ent.RowSeal.Query().Where(rowseal.IdEQ(owner.Id)).Exist(ctx)
		x.NoError(err)
		return ok
	}
	newest, err := d.s.Ent.Audit.Query().Where(entaudit.ObjectId(owner.Id)).Order(entaudit.ByDateCreated(entsql.OrderDesc())).First(ctx)
	x.NoError(err)
	q := d.s.Ent.Holder.Query().Where(holder.IdEQ(owner.Id))
	bare.HolderSelectInit(q, nil)
	row, err := q.Only(ctx)
	x.NoError(err)
	sum, err := cs.Digest.Of(pdid.Id(owner.Id), row.Proto(), newest.DigestFields, payaudit.Line{Id: newest.Id, Created: newest.DateCreated, Action: newest.Action})
	x.NoError(err)
	x.False(payaudit.Sealing(sum))
	x.NoError(d.s.Ent.Audit.UpdateOneId(newest.Id).SetDigest(sum).Exec(ctx))
	x.NoError(d.s.Ent.RowSeal.DeleteOneId(owner.Id).Exec(ctx))

	out, err := d.admin(t, "trail", "rows")
	x.NoError(err, "a row from before seals was taken for a finding")
	x.Contains(out, "1 rows from before seals")
	of := digestOf(t, out)

	_, err = d.admin(t, "trail", "rows", "--accept", "--of", nothingLookedAt, "--why", "not what was looked at")
	x.ErrorContains(err, "the check looked at was")
	x.False(sealed(), "an acceptance of another check sealed it")

	out, err = d.admin(t, "trail", "rows", "--accept", "--of", of, "--why", "written before seals")
	x.NoError(err)
	x.Contains(out, "sealed 1 from before seals")
	x.True(sealed())
	out, err = d.admin(t, "trail", "rows")
	x.NoError(err)
	x.NotContains(out, "from before seals")
}

// TestAVerificationSaysWhatEachWitnessHeard: `trail verify` says what each
// witness that answers heard last, beside the digest -- nothing yet before
// the first checkpoint -- and a witness that only tells is not among them.
func TestAVerificationSaysWhatEachWitnessHeard(t *testing.T) {
	x := require.New(t)
	store := "store:" + t.TempDir()
	d := deployWith(t, func(c *cmd.Config) {
		withArchive(t, t.TempDir())(c)
		c.Audit.Checkpoints.Witnesses = []string{"log:", store}
	})

	out, err := d.admin(t, "trail", "verify")
	x.NoError(err)
	x.Contains(out, "heard by "+store+": nothing yet\n")

	d.pass(t)
	out, err = d.admin(t, "trail", "verify")
	x.NoError(err)
	m := regexp.MustCompile(`(?m)^heard by (store:\S+): checkpoint ([0-9]+), (sha256:[0-9a-f]{64}), at \S+$`).FindStringSubmatch(out)
	x.NotNil(m, "verify does not say what the store witness heard:\n%s", out)
	x.Equal(store, m[1])
	x.Contains(out, "checkpoint "+m[2]+" compared")
	x.NotContains(out, "heard by log:", "a witness that only tells answered")
}

// TestAWorkspacesTrailIsTakenOutForWhoeverKeepsIt (loft#153): a demand to
// preserve a workspace's records is answered by `trail export`, live or
// destroyed -- by the address its hold names once it is gone. The file is
// encrypted to whoever keeps it, `verify-export` holds it to the
// deployment's keys where it is, and each export is a download in the access
// record, the operator's, with its reason, naming everybody whose data it
// carries.
func TestAWorkspacesTrailIsTakenOutForWhoeverKeepsIt(t *testing.T) {
	x := require.New(t)
	ctx := t.Context()
	// In a zone whose date is not UTC's now, as Seoul's is from midnight to
	// nine: the dates the commands say are UTC's, whatever the machine's.
	inZone(t, awayFromUTC(time.Now()))
	year := config.AuditMinConfig{Destroy: 365 * 24 * time.Hour}
	d := deployWith(t, func(c *cmd.Config) {
		withArchive(t, t.TempDir())(c)
		c.Audit.By = map[string]config.AuditKeepConfig{"access": {Min: year}, "holder": {Min: year}}
	})
	d.pass(t)
	acme, err := d.s.Ent.Tenant.Query().Where(enttenant.Alias("acme")).Only(ctx)
	x.NoError(err)
	kim, err := d.s.Ent.Holder.Query().Where(holder.TenantId(acme.Id), holder.Alias("kim")).Only(ctx)
	x.NoError(err)

	dir := t.TempDir()
	whom, err := age.GenerateX25519Identity()
	x.NoError(err)
	id := filepath.Join(dir, "keeper.key")
	x.NoError(os.WriteFile(id, []byte(whom.String()+"\n"), 0o600))
	pub := filepath.Join(dir, "keeper.pub")
	x.NoError(os.WriteFile(pub, []byte("# the keeper\n"+whom.Recipient().String()+"\n"), 0o644))

	live := filepath.Join(dir, "acme.trail")
	_, err = d.admin(t, "trail", "export", "--to", whom.Recipient().String(), "--out", live, "acme")
	x.ErrorContains(err, "--reason")
	x.NoFileExists(live, "a file written by an export that was refused")

	out, err := d.admin(t, "trail", "export", "--reason", "case 2026-4", "--to", whom.Recipient().String(), "--out", live, "acme")
	x.NoError(err, out)
	x.Contains(out, "the trail of @acme")
	x.Contains(out, "manifest sha256:")
	b, err := os.ReadFile(live)
	x.NoError(err)
	x.True(bytes.HasPrefix(b, []byte("age-encryption.org/v1")), "not written encrypted")

	out, err = d.admin(t, "trail", "verify-export", "--file", live, "--identity", id)
	x.NoError(err, out)
	x.Contains(out, "a key the deployment trusts signed it")
	x.Contains(out, `"case 2026-4"`)
	x.Contains(out, "by alice", "taken out by the operator whose key it was")

	// And by whoever keeps it, who holds no operator's key: against the
	// deployment's public keys, as `trail keys` printed them.
	keys, err := d.admin(t, "trail", "keys")
	x.NoError(err, keys)
	x.Contains(keys, "-----BEGIN PUBLIC KEY-----")
	trust := filepath.Join(dir, "deployment.pem")
	x.NoError(os.WriteFile(trust, []byte(keys), 0o644))
	out, err = run(t, admincli.Cmd(&d.c), "trail", "verify-export", "--trust", trust, "--file", live, "--identity", id)
	x.NoError(err, out)
	x.Contains(out, "a key the deployment trusts signed it")
	other := filepath.Join(dir, "other.pem")
	stranger, _, err := ed25519.GenerateKey(nil)
	x.NoError(err)
	b, err = x509.MarshalPKIXPublicKey(stranger)
	x.NoError(err)
	x.NoError(os.WriteFile(other, pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: b}), 0o644))
	_, err = run(t, admincli.Cmd(&d.c), "trail", "verify-export", "--trust", other, "--file", live, "--identity", id)
	x.Error(err, "held to a key the deployment never signed with")

	rows, err := d.s.Ent.Access.Query().Where(entaccess.Action(app.OperatorTrailService_Export_FullMethodName), entaccess.TenantId(acme.Id)).All(ctx)
	x.NoError(err)
	x.Len(rows, 1)
	x.Equal("download", rows[0].Kind)
	x.Equal(operator.Id("alice").Uuid(), rows[0].ActorId)
	dl := &pdpb.AccessDetail{}
	x.NoError(proto.Unmarshal(rows[0].Value, dl))
	x.Equal("case 2026-4", dl.GetReason())
	x.True(slices.ContainsFunc(rows[0].Subjects, func(v []byte) bool { return pdid.Id(v) == pdid.Id(kim.Id) }), "kim's data is in it, and the record does not say so")

	// Retired, it is taken out by the address its hold names, until its
	// floors pass.
	began := time.Now()
	_, err = d.admin(t, "tenant", "retire", "--yes", "acme")
	x.NoError(err)
	ended := time.Now()
	out, err = d.admin(t, "tenant", "retired")
	x.NoError(err, out)
	x.Contains(out, "@acme")
	x.True(saysDay(out, "kept until ", began.Add(365*24*time.Hour), ended.Add(365*24*time.Hour)), "not UTC's day, a year from now: %s", out)
	gone := filepath.Join(dir, "acme-retired.trail")
	out, err = d.admin(t, "trail", "export", "--reason", "case 2026-4, after it left", "--to", pub, "--out", gone, "@acme")
	x.NoError(err, out)
	x.Contains(out, "retired ")
	out, err = d.admin(t, "trail", "verify-export", "--file", gone, "--identity", id)
	x.NoError(err, out)
	x.Contains(out, "kept until")

	rows, err = d.s.Ent.Access.Query().Where(entaccess.Action(app.OperatorTrailService_Export_FullMethodName), entaccess.TenantId(acme.Id)).All(ctx)
	x.NoError(err)
	x.Len(rows, 2)

	_, err = d.admin(t, "trail", "export", "--reason", "a case", "--to", pub, "--out", filepath.Join(dir, "x"), "nobody-here")
	x.ErrorContains(err, "no workspace is @nobody-here")
	x.NoFileExists(filepath.Join(dir, "x"))
}
