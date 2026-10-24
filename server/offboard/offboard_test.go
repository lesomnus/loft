package offboard_test

import (
	"archive/zip"
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"crypto"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"slices"
	"strings"
	"testing"
	"time"
	"uuid"

	"github.com/lesomnus/flob"
	"github.com/lesomnus/payday/frame"
	"github.com/lesomnus/payday/pdid"
	"github.com/lesomnus/payday/pdtest"
	"github.com/lesomnus/payday/trail"
	"github.com/lesomnus/z"
	"github.com/protobuf-orm/ent/dialect/sql"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/encoding/protojson"

	app "github.com/lesomnus/rove"
	"github.com/lesomnus/rove/cli"
	"github.com/lesomnus/rove/cmd"
	"github.com/lesomnus/rove/internal/ent"
	"github.com/lesomnus/rove/internal/ent/audit"
	"github.com/lesomnus/rove/internal/ent/event"
	"github.com/lesomnus/rove/internal/ent/migrate"
	"github.com/lesomnus/rove/internal/ent/rowseal"
	"github.com/lesomnus/rove/server/offboard"
	"github.com/lesomnus/rove/server/pd"
)

type env struct {
	t *testing.T
	x *require.Assertions
	s *cmd.Server
	c cmd.Config
}

func newEnv(t *testing.T, opts ...func(*cmd.Config)) *env {
	x := require.New(t)
	driver, dsn := pdtest.DB(t)
	c := cmd.Config{}
	c.Db.Driver = driver
	c.Db.Dsn = dsn
	c.Watch.Broker = "memory"
	c.App.Files = t.TempDir()
	c.Auth.Roster.Db.Driver, c.Auth.Roster.Db.Dsn = pdtest.DB(t)
	for _, opt := range opts {
		opt(&c)
	}

	s, err := cmd.Build(context.Background(), c)
	x.NoError(err)
	t.Cleanup(func() { s.Close() })
	x.NoError(cli.Migrate(context.Background(), s))

	return &env{t: t, x: x, s: s, c: c}
}

// tenant is one with an owner, a room, a laptop in it, a loan of it, and a
// photo of the laptop.
type tenant struct {
	id    pdid.Id
	owner context.Context
	photo *app.Attachment
}

func (e *env) tenant(alias string) tenant {
	x := e.x
	ctx := context.Background()
	t, err := e.s.Base.Tenant().Add(ctx, app.TenantAddRequest_builder{Alias: alias, Name: alias}.Build())
	x.NoError(err)
	tref := app.TenantRef_builder{Id: t.GetId()}.Build()
	h, err := e.s.Base.Holder().Add(ctx, app.HolderAddRequest_builder{Tenant: tref, Alias: "owner", Name: "Owner", Role: z.Ptr("owner")}.Build())
	x.NoError(err)
	p, err := e.s.Base.Party().Add(ctx, app.PartyAddRequest_builder{Tenant: tref, Name: "Owner of " + alias, Kind: "person", Holder: app.HolderRef_builder{Id: h.GetId()}.Build()}.Build())
	x.NoError(err)

	id, _ := pdid.From(t.GetId())
	hid, _ := pdid.From(h.GetId())
	owner := frame.Into(ctx, frame.New(hid, id, frame.Whole()).WithScope(frame.Only(id)).WithRow(h))

	w := e.s.Walled
	room, err := w.Asset().Add(owner, app.AssetAddRequest_builder{Name: "창고", Kind: "space"}.Build())
	x.NoError(err)
	laptop, err := w.Asset().Add(owner, app.AssetAddRequest_builder{Name: "노트북", Tag: "NB-1", Kind: "item", To: app.AssetRef_builder{Id: room.GetId()}.Build()}.Build())
	x.NoError(err)
	_, err = w.Custody().Add(owner, app.CustodyAddRequest_builder{
		Party: app.PartyRef_builder{Id: p.GetId()}.Build(),
		Lines: []*app.CustodyLineSpec{app.CustodyLineSpec_builder{Asset: app.AssetRef_builder{Id: laptop.GetId()}.Build()}.Build()},
	}.Build())
	x.NoError(err)
	photo, err := w.Attachment().Upload(owner, app.AttachmentUploadRequest_builder{
		SubjectId:   laptop.GetId(),
		Name:        "laptop.jpg",
		ContentType: "image/jpeg",
		Data:        []byte("the photo of " + alias),
	}.Build())
	x.NoError(err)

	return tenant{id: id, owner: owner, photo: photo}
}

// count is how many rows of each table are the tenant's.
func (e *env) count(t pdid.Id) map[string]int {
	out := map[string]int{}
	drv := e.s.Ent.Driver()
	for _, tb := range migrate.Tables {
		if tb.Name == "archived" || tb.Name == "owed" || slices.Contains(offboard.Shared, tb.Name) {
			// The manifest of the trail's archive, a row per blob, the
			// erasures the trail owes (payday#115), and the deployment's own
			// tables: no tenant's.
			continue
		}

		col := "tenant_id"
		if tb.Name == "tenant" {
			col = "id"
		}
		q, args := sql.Dialect(drv.Dialect()).Select(sql.Count("*")).From(sql.Table(tb.Name)).Where(sql.EQ(col, t.Uuid())).Query()
		rows := &sql.Rows{}
		e.x.NoError(drv.Query(context.Background(), q, args, rows))
		var n int
		e.x.True(rows.Next())
		e.x.NoError(rows.Scan(&n))
		rows.Close()
		if n > 0 {
			out[tb.Name] = n
		}
	}
	return out
}

func (e *env) file(key string) bool {
	r, err := e.s.Deps.Files.Open(context.Background(), key)
	if err != nil {
		return false
	}
	r.Close()
	return true
}

// TestEveryTableIsDecidedAbout: an export says of every table whether it is
// written out, and a purge reaches every table but the trail's.
func TestEveryTableIsDecidedAbout(t *testing.T) {
	x := require.New(t)
	order, err := offboard.Tables()
	x.NoError(err)

	for _, tb := range migrate.Tables {
		_, out := offboard.Exported[tb.Name]
		_, held := offboard.Withheld[tb.Name]
		x.True(out != held, "%s is exported, withheld, or both", tb.Name)
		_, gone := offboard.Dropped[tb.Name]
		x.False(gone, "%s is a table of this build's, and one it dropped", tb.Name)

		if tb.Name == "tenant" || slices.Contains(offboard.Trail, tb.Name) || slices.Contains(offboard.Shared, tb.Name) {
			x.NotContains(order, tb.Name)
			continue
		}
		i := slices.Index(order, tb.Name)
		x.GreaterOrEqual(i, 0, "a purge does not reach %s", tb.Name)
		for _, fk := range tb.ForeignKeys {
			if j := slices.Index(order, fk.RefTable.Name); j >= 0 && fk.RefTable.Name != tb.Name {
				x.Less(i, j, "%s goes after %s, which it points at", tb.Name, fk.RefTable.Name)
			}
		}
	}
}

// TestAPurgeTakesEverythingOfOneTenantAndNothingElse.
func TestAPurgeTakesEverythingOfOneTenantAndNothingElse(t *testing.T) {
	e := newEnv(t)
	x := e.x
	acme, other := e.tenant("acme"), e.tenant("other")
	d := e.s.Offboard(e.c)

	before, theirs := e.count(acme.id), e.count(other.id)
	x.Positive(before["asset"])
	x.Positive(before["audit"], "the trail kept nothing")

	plan, err := d.Purge(context.Background(), acme.id, true)
	x.NoError(err)
	x.Equal(before, e.count(acme.id), "a dry run took something")
	x.Equal(1, plan.Files)
	x.Positive(plan.Trail.Removed)

	got, err := d.Purge(context.Background(), acme.id, false)
	x.NoError(err)
	x.Empty(got.Lost)
	x.Equal(plan.Rows, got.Rows, "the purge took other than the dry run said")

	x.Empty(e.count(acme.id), "something of acme is left")
	x.Equal(theirs, e.count(other.id), "something of another tenant went")
	x.False(e.file(acme.photo.GetObjectKey()), "acme's photo")
	x.True(e.file(other.photo.GetObjectKey()), "another tenant's photo")

	n, err := e.s.Ent.Audit.Query().Where(audit.TenantId(acme.id.Uuid())).Count(context.Background())
	x.NoError(err)
	x.Zero(n)
}

// TestAHoldStopsAPurge.
func TestAHoldStopsAPurge(t *testing.T) {
	e := newEnv(t)
	x := e.x
	acme := e.tenant("acme")
	_, err := e.s.Base.LegalHold().Add(context.Background(), app.LegalHoldAddRequest_builder{
		Tenant: app.TenantRef_builder{Id: acme.id.Bytes()}.Build(),
		Name:   "사건 1",
	}.Build())
	x.NoError(err)

	before := e.count(acme.id)
	_, err = e.s.Offboard(e.c).Purge(context.Background(), acme.id, false)
	x.True(errors.Is(err, trail.ErrHeld), "%v", err)
	x.Equal(before, e.count(acme.id))
}

// TestAnExportHasAllOfIt.
func TestAnExportHasAllOfIt(t *testing.T) {
	e := newEnv(t)
	x := e.x
	acme := e.tenant("acme")
	e.tenant("other")

	var buf bytes.Buffer
	m, err := e.s.Offboard(e.c).Export(context.Background(), acme.id, &buf)
	x.NoError(err)
	x.Equal(2, m.Rows["asset"])
	x.Equal(1, m.Files)
	x.Positive(m.Trail.Database)

	z, err := zip.NewReader(bytes.NewReader(buf.Bytes()), int64(buf.Len()))
	x.NoError(err)
	read := func(name string) []byte {
		f, err := z.Open(name)
		x.NoError(err, name)
		defer f.Close()
		b, err := io.ReadAll(f)
		x.NoError(err)
		return b
	}

	var got offboard.Manifest
	x.NoError(json.Unmarshal(read("manifest.json"), &got))
	x.Equal(m.Rows, got.Rows)

	names := []string{}
	s := bufio.NewScanner(bytes.NewReader(read("rows/asset.jsonl")))
	for s.Scan() {
		var a app.Asset
		x.NoError(protojson.Unmarshal(s.Bytes(), &a))
		names = append(names, a.GetName())
	}
	x.ElementsMatch([]string{"창고", "노트북"}, names)
	x.Equal("the photo of acme", string(read("files/"+acme.photo.GetObjectKey())))

	for _, f := range z.File {
		x.NotContains(f.Name, "credential")
		x.NotContains(f.Name, "session")
	}
	x.NotContains(string(read("rows/party.jsonl")), "other", "another tenant's row")
}

// TestAnExportIsTheSameReadARowAtATime: read a row at a time, an export is the
// one read a page at a time -- every row of every table and of the trail, once
// and in the same order -- so that what an export holds is a page and not the
// workspace (rove#120).
func TestAnExportIsTheSameReadARowAtATime(t *testing.T) {
	e := newEnv(t)
	x := e.x
	ctx := context.Background()
	acme, other := e.tenant("acme"), e.tenant("other")

	// Rows of the trail that name acme from another tenant, written among its
	// own: what somebody of acme's did there, and what was done to acme from
	// there. The trail of a tenant is three kinds of row, each read by an
	// index of its own.
	own, err := e.s.Ent.Audit.Query().Where(audit.TenantId(acme.id.Uuid())).Order(ent.Asc(audit.FieldDateCreated)).All(ctx)
	x.NoError(err)
	x.Greater(len(own), 2)
	v := own[len(own)/2]
	elsewhere := []uuid.UUID{uuid.NewV7(), uuid.NewV7()}
	for i, set := range []func(*ent.AuditCreate) *ent.AuditCreate{
		func(c *ent.AuditCreate) *ent.AuditCreate { return c.SetActorTenantId(acme.id.Uuid()) },
		func(c *ent.AuditCreate) *ent.AuditCreate {
			return c.SetActorTenantId(other.id.Uuid()).SetCounterpartTenantId(acme.id.Uuid())
		},
	} {
		x.NoError(set(e.s.Ent.Audit.Create().
			SetId(elsewhere[i]).
			SetTenantId(other.id.Uuid()).
			SetActorId(v.ActorId).
			SetTraceId(v.TraceId).
			SetAction(v.Action).
			SetObjectId(v.ObjectId).
			SetPatch(v.Patch).
			SetValue(v.Value).
			SetFields(v.Fields).
			SetDomain(v.Domain).
			SetDateCreated(v.DateCreated)).
			Exec(ctx))
	}

	files := func() map[string]string {
		var buf bytes.Buffer
		_, err := e.s.Offboard(e.c).Export(ctx, acme.id, &buf)
		x.NoError(err)
		z, err := zip.NewReader(bytes.NewReader(buf.Bytes()), int64(buf.Len()))
		x.NoError(err)
		out := map[string]string{}
		for _, f := range z.File {
			if f.Name == "manifest.json" || f.Name == "signature.json" {
				// Of the moment it was made.
				continue
			}
			r, err := f.Open()
			x.NoError(err)
			b, err := io.ReadAll(r)
			r.Close()
			x.NoError(err)
			out[f.Name] = string(b)
		}
		return out
	}

	whole := files()
	offboard.SetPage(t, 1)
	paged := files()
	x.Equal(whole, paged)
	x.Greater(strings.Count(paged["rows/asset.jsonl"], "\n"), 1, "every page was one row")

	ids := []string{}
	at := time.Time{}
	s := bufio.NewScanner(strings.NewReader(paged["trail/database.jsonl"]))
	for s.Scan() {
		var r app.Audit
		x.NoError(protojson.Unmarshal(s.Bytes(), &r))
		ids = append(ids, base64.StdEncoding.EncodeToString(r.GetId()))
		x.False(r.GetDateCreated().AsTime().Before(at), "the trail in the order it was written")
		at = r.GetDateCreated().AsTime()
	}
	x.Len(ids, len(own)+len(elsewhere))
	for _, id := range elsewhere {
		x.Contains(ids, base64.StdEncoding.EncodeToString(id[:]))
	}
	slices.Sort(ids)
	x.Len(slices.Compact(ids), len(own)+len(elsewhere), "a row of the trail twice")
}

// TestAnExportHoldsNoSeal: a row's seal is a MAC of what a person is, kept in
// the database alone for exactly as long as the row (payday#115), and an
// export is a copy of the workspace kept somewhere else, for years -- so it
// carries none. The manifest says the table is withheld, and no file of the
// export holds a seal, whatever it is named. A restore seals what it puts back,
// under the digest key of the deployment it comes back to.
func TestAnExportHoldsNoSeal(t *testing.T) {
	key := "base64:" + base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{7}, 32))
	e, pub := signed(t, func(c *cmd.Config) { c.Audit.Digest.Key = key })
	x := e.x
	ctx := context.Background()
	acme := e.tenant("acme")

	seals, err := e.s.Ent.RowSeal.Query().Where(rowseal.TenantId(acme.id.Uuid())).All(ctx)
	x.NoError(err)
	x.NotEmpty(seals, "acme has nothing sealed, so this proves nothing")

	b := e.export(acme.id)
	m, err := offboard.Verify(bytes.NewReader(b), int64(len(b)), e.s.Offboard(e.c).Trusted())
	x.NoError(err)
	x.Contains(m.Withheld, "rowseal")
	x.Zero(m.Rows["rowseal"])

	z, err := zip.NewReader(bytes.NewReader(b), int64(len(b)))
	x.NoError(err)
	for _, f := range z.File {
		r, err := f.Open()
		x.NoError(err)
		body, err := io.ReadAll(r)
		r.Close()
		x.NoError(err)
		for _, v := range seals {
			x.False(bytes.Contains(body, v.Sum), "%s holds a seal", f.Name)
			x.NotContains(string(body), base64.StdEncoding.EncodeToString(v.Sum), "%s holds a seal", f.Name)
		}
	}

	elsewhere := newEnv(t, func(c *cmd.Config) {
		c.Audit.Checkpoints.Trust = []string{pub}
		c.Audit.Digest.Key = key
	})
	_, err = elsewhere.restore(b, offboard.ImportOptions{})
	x.NoError(err)
	// What it put back, sealed; and the restore's own event beside it, which
	// is of a kind with a field of somebody's -- why, its reason -- and is
	// sealed as any such row is (payday#151).
	ev, err := elsewhere.s.Ent.Event.Query().Where(event.TenantId(acme.id.Uuid()), event.Kind("workspace.import")).Only(ctx)
	x.NoError(err)
	n, err := elsewhere.s.Ent.RowSeal.Query().Where(rowseal.TenantId(acme.id.Uuid()), rowseal.IdNEQ(ev.Id)).Count(ctx)
	x.NoError(err)
	x.Equal(len(seals), n, "the restore did not seal what it put back")
	x.True(elsewhere.s.Ent.RowSeal.Query().Where(rowseal.Id(ev.Id)).ExistX(ctx), "the restore's own event is not sealed")
}

// TestAnExportReadsTheArchiveThroughItsManifest: what the trail moved out of
// the database is in the export, and a chunk put into the archive by somebody
// who can write it and nothing else is not.
func TestAnExportReadsTheArchiveThroughItsManifest(t *testing.T) {
	e := newEnv(t, func(c *cmd.Config) {
		c.Audit.Archive = t.TempDir()
		c.Audit.Retain = time.Nanosecond
		c.Audit.Checkpoints.Key = pdtest.Key(t)
	})
	x := e.x
	ctx := context.Background()
	acme := e.tenant("acme")
	e.tenant("other")

	e.s.Trail.Pass(ctx, pd.TrailStore(e.s.Ent))
	n, err := e.s.Ent.Audit.Query().Where(audit.TenantId(acme.id.Uuid())).Count(ctx)
	x.NoError(err)
	x.Zero(n, "the pass left acme's trail in the database")

	export := func() (offboard.Manifest, []string) {
		var buf bytes.Buffer
		m, err := e.s.Offboard(e.c).Export(ctx, acme.id, &buf)
		x.NoError(err)

		z, err := zip.NewReader(bytes.NewReader(buf.Bytes()), int64(buf.Len()))
		x.NoError(err)
		f, err := z.Open("trail/archive.jsonl")
		x.NoError(err)
		defer f.Close()

		ids := []string{}
		s := bufio.NewScanner(f)
		for s.Scan() {
			var v struct {
				Id string `json:"id"`
			}
			x.NoError(json.Unmarshal(s.Bytes(), &v))
			ids = append(ids, v.Id)
		}

		return m, ids
	}

	m, ids := export()
	x.Positive(m.Trail.Archive)
	x.Len(ids, m.Trail.Archive)

	// One of acme's rows under another identifier, beside the chunk it came
	// from and with that chunk's labels.
	a := e.s.Trail.Archive
	cs, err := trail.Chunks(ctx, a)
	x.NoError(err)
	i := slices.IndexFunc(cs, func(c trail.Chunk) bool { return c.Namespace == acme.id.String() })
	x.GreaterOrEqual(i, 0, "acme has no chunk of its own")
	c := cs[i]

	info, err := a.Use(c.Namespace).Stat(ctx, c.Digest)
	x.NoError(err)
	l, err := info.Labels(ctx)
	x.NoError(err)

	var row map[string]any
	x.NoError(trail.ReadTenant(ctx, a, acme.id, func(doc []byte) error {
		if row == nil {
			return json.Unmarshal(doc, &row)
		}
		return nil
	}))
	forged := uuid.NewV7()
	row["id"] = base64.StdEncoding.EncodeToString(forged[:])
	b, err := json.Marshal(row)
	x.NoError(err)

	var gz bytes.Buffer
	w := gzip.NewWriter(&gz)
	_, err = w.Write(append(b, '\n'))
	x.NoError(err)
	x.NoError(w.Close())
	_, err = a.Use(c.Namespace).Add(ctx, flob.Meta{Labels: l}, &gz)
	x.NoError(err)

	got, ids := export()
	x.Equal(m.Trail.Archive, got.Trail.Archive, "the export took in a chunk nobody accounts for")
	x.NotContains(ids, row["id"])
}

// rezip is an export with its files as `edit` leaves them, in the order they
// were in: what somebody holding the file could make of it.
func rezip(t *testing.T, b []byte, edit func(files map[string][]byte, order []string) []string) []byte {
	t.Helper()
	z, err := zip.NewReader(bytes.NewReader(b), int64(len(b)))
	require.NoError(t, err)
	files := map[string][]byte{}
	order := []string{}
	for _, f := range z.File {
		r, err := f.Open()
		require.NoError(t, err)
		v, err := io.ReadAll(r)
		r.Close()
		require.NoError(t, err)
		files[f.Name] = v
		order = append(order, f.Name)
	}
	order = edit(files, order)

	var out bytes.Buffer
	w := zip.NewWriter(&out)
	for _, name := range order {
		f, err := w.Create(name)
		require.NoError(t, err)
		_, err = f.Write(files[name])
		require.NoError(t, err)
	}
	require.NoError(t, w.Close())
	return out.Bytes()
}

// TestAnExportSaysItIsWhatWasExported: every file of it is in a manifest the
// deployment's key signed, so that a restore takes only what this deployment
// wrote, unchanged -- a file anybody could edit would be a history anybody
// could write (design D34).
func TestAnExportSaysItIsWhatWasExported(t *testing.T) {
	key, _, err := trail.GenerateKey()
	require.NoError(t, err)
	e := newEnv(t, func(c *cmd.Config) { c.Audit.Checkpoints.Key = string(key) })
	x := e.x
	acme := e.tenant("acme")

	d := e.s.Offboard(e.c)
	var buf bytes.Buffer
	_, err = d.Export(context.Background(), acme.id, &buf)
	x.NoError(err)
	b := buf.Bytes()
	verify := func(b []byte, trust ...crypto.PublicKey) (offboard.Manifest, error) {
		if trust == nil {
			trust = d.Trusted()
		}
		return offboard.Verify(bytes.NewReader(b), int64(len(b)), trust)
	}

	m, err := verify(b)
	x.NoError(err)
	x.Equal(offboard.Format, m.Format)
	x.Equal(2, m.Rows["asset"])
	x.Contains(m.Digests, "rows/asset.jsonl")
	x.Contains(m.Digests, "files/"+acme.photo.GetObjectKey())

	t.Run("a row changed", func(t *testing.T) {
		_, err := verify(rezip(t, b, func(fs map[string][]byte, order []string) []string {
			fs["rows/asset.jsonl"] = bytes.ReplaceAll(fs["rows/asset.jsonl"], []byte("노트북"), []byte("노트븍"))
			return order
		}))
		require.ErrorIs(t, err, offboard.ErrAltered)
	})
	t.Run("the manifest changed to say so", func(t *testing.T) {
		_, err := verify(rezip(t, b, func(fs map[string][]byte, order []string) []string {
			fs["manifest.json"] = bytes.Replace(fs["manifest.json"], []byte(`"asset": 2`), []byte(`"asset": 3`), 1)
			return order
		}))
		require.ErrorIs(t, err, offboard.ErrForged)
	})
	t.Run("a file put in", func(t *testing.T) {
		_, err := verify(rezip(t, b, func(fs map[string][]byte, order []string) []string {
			fs["rows/extra.jsonl"] = []byte("{}\n")
			return append(order, "rows/extra.jsonl")
		}))
		require.ErrorIs(t, err, offboard.ErrAltered)
		require.ErrorContains(t, err, "rows/extra.jsonl is not in its manifest")
	})
	t.Run("a file taken out", func(t *testing.T) {
		_, err := verify(rezip(t, b, func(fs map[string][]byte, order []string) []string {
			return slices.DeleteFunc(order, func(v string) bool { return v == "rows/asset.jsonl" })
		}))
		require.ErrorIs(t, err, offboard.ErrAltered)
	})
	t.Run("by a key nobody said to trust, until somebody does", func(t *testing.T) {
		other, _, err := trail.GenerateKey()
		require.NoError(t, err)
		k, err := trail.ParseKey(other)
		require.NoError(t, err)

		_, err = verify(b, k.Public())
		require.ErrorIs(t, err, offboard.ErrUntrusted)

		// The other deployment's, named: an export that came from there.
		e2 := newEnv(t, func(c *cmd.Config) { c.Audit.Checkpoints.Key = string(other) })
		var buf bytes.Buffer
		_, err = e2.s.Offboard(e2.c).Export(context.Background(), e2.tenant("acme").id, &buf)
		require.NoError(t, err)
		_, err = verify(buf.Bytes())
		require.ErrorIs(t, err, offboard.ErrUntrusted)
		_, err = verify(buf.Bytes(), append(d.Trusted(), k.Public())...)
		require.NoError(t, err)
	})
	t.Run("and by no key at all", func(t *testing.T) {
		e := newEnv(t)
		var buf bytes.Buffer
		_, err := e.s.Offboard(e.c).Export(context.Background(), e.tenant("acme").id, &buf)
		require.NoError(t, err, "a deployment with no key still gives a tenant its data")
		_, err = verify(buf.Bytes())
		require.ErrorIs(t, err, offboard.ErrUnsigned)
	})
}
