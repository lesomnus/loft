package offboard_test

import (
	"archive/zip"
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"slices"
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
	"github.com/lesomnus/rove/internal/ent/audit"
	"github.com/lesomnus/rove/internal/ent/migrate"
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
		if tb.Name == "archived" || slices.Contains(offboard.Shared, tb.Name) {
			// The manifest of the trail's archive, a row per blob, and the
			// deployment's own tables: no tenant's.
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

// TestAnExportReadsTheArchiveThroughItsManifest: what the trail moved out of
// the database is in the export, and a chunk put into the archive by somebody
// who can write it and nothing else is not.
func TestAnExportReadsTheArchiveThroughItsManifest(t *testing.T) {
	e := newEnv(t, func(c *cmd.Config) {
		c.Audit.Archive = t.TempDir()
		c.Audit.Retain = time.Nanosecond
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
