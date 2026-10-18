package offboard_test

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"io"
	"slices"
	"testing"
	"time"

	"github.com/lesomnus/payday/frame"
	"github.com/lesomnus/payday/pdid"
	"github.com/lesomnus/payday/trail"
	"github.com/lesomnus/z"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/encoding/protojson"

	app "github.com/lesomnus/rove"
	"github.com/lesomnus/rove/cmd"
	"github.com/lesomnus/rove/internal/ent/aliashold"
	"github.com/lesomnus/rove/internal/ent/event"
	"github.com/lesomnus/rove/internal/ent/holder"
	"github.com/lesomnus/rove/internal/ent/searchstale"
	"github.com/lesomnus/rove/server/domain"
	"github.com/lesomnus/rove/server/offboard"
	"github.com/lesomnus/rove/server/opensearch"
	"github.com/lesomnus/rove/server/pd"
)

// signed is a deployment that signs its exports, and the public half of its
// key, for another to trust.
func signed(t *testing.T, opts ...func(*cmd.Config)) (*env, string) {
	key, pub, err := trail.GenerateKey()
	require.NoError(t, err)
	e := newEnv(t, append([]func(*cmd.Config){func(c *cmd.Config) { c.Audit.Checkpoints.Key = string(key) }}, opts...)...)
	return e, pub
}

// export is the tenant's export, as bytes.
func (e *env) export(t pdid.Id) []byte {
	var buf bytes.Buffer
	_, err := e.s.Offboard(e.c).Export(context.Background(), t, &buf)
	e.x.NoError(err)
	return buf.Bytes()
}

func (e *env) restore(b []byte, o offboard.ImportOptions) (offboard.Restored, error) {
	return e.s.Offboard(e.c).Import(context.Background(), bytes.NewReader(b), int64(len(b)), o)
}

// rows is every row of the tenant an export writes, by table, each as its
// line, in no order.
func (e *env) rows(t pdid.Id) map[string][]string {
	out := map[string][]string{}
	for k, read := range offboard.Exported {
		vs, err := read(context.Background(), e.s.Ent, t.Uuid())
		e.x.NoError(err)
		for _, v := range vs {
			b, err := protojson.Marshal(v)
			e.x.NoError(err)
			out[k] = append(out[k], string(b))
		}
		slices.Sort(out[k])
	}
	return out
}

func (e *env) tenantRow(t pdid.Id) *app.Tenant {
	v, err := e.s.Ent.Tenant.Get(context.Background(), t.Uuid())
	e.x.NoError(err)
	return v.Proto()
}

// TestARestoreIsTheWorkspaceAsItWasExported: purged, and put back from its
// export, a workspace is every row it was, under the identifiers it had, and
// its files -- and one event more, which says so (design D34).
func TestARestoreIsTheWorkspaceAsItWasExported(t *testing.T) {
	e, _ := signed(t)
	x := e.x
	ctx := context.Background()
	acme, other := e.tenant("acme"), e.tenant("other")

	before, self, theirs := e.rows(acme.id), e.tenantRow(acme.id), e.rows(other.id)
	b := e.export(acme.id)
	_, err := e.s.Offboard(e.c).Purge(ctx, acme.id, false)
	x.NoError(err)
	x.Empty(e.count(acme.id))
	x.False(e.file(acme.photo.GetObjectKey()))

	plan, err := e.restore(b, offboard.ImportOptions{Dry: true})
	x.NoError(err)
	x.Empty(e.count(acme.id), "a dry run put something back")
	x.False(e.file(acme.photo.GetObjectKey()), "a dry run put a file back")
	x.Equal(acme.id, plan.Tenant)
	x.Equal("acme", plan.Alias)
	x.Empty(plan.Asked)
	x.Equal(1, plan.Files)

	got, err := e.restore(b, offboard.ImportOptions{})
	x.NoError(err)
	x.Equal(plan.Rows, got.Rows, "the restore put back other than the dry run said")
	x.Equal(len(before["asset"]), got.Rows["asset"])
	x.Empty(got.Missing)
	x.Empty(got.Unlinked)

	after := e.rows(acme.id)
	for k := range before {
		if k == "event" {
			continue
		}
		x.Equal(before[k], after[k], "%s is not what was exported", k)
	}
	x.Subset(after["event"], before["event"])
	x.Len(after["event"], len(before["event"])+1)
	x.Empty(cmpTenant(self, e.tenantRow(acme.id)))
	x.True(e.file(acme.photo.GetObjectKey()), "the photo")
	x.Equal(theirs, e.rows(other.id), "another tenant's rows changed")

	ev, err := e.s.Ent.Event.Query().Where(event.TenantId(acme.id.Uuid()), event.Kind("workspace.import")).Only(ctx)
	x.NoError(err)
	x.Nil(ev.ActorId, "somebody in the workspace did it")
	x.Contains(ev.Desc, "워크스페이스 복구")
	x.Contains(ev.Payload, "rows=")

	// It names the export it came from, which holds the trail it does not
	// put back: by its manifest, and by the key that signed it.
	zr, err := zip.NewReader(bytes.NewReader(b), int64(len(b)))
	x.NoError(err)
	f, err := zr.Open("manifest.json")
	x.NoError(err)
	mb, err := io.ReadAll(f)
	x.NoError(err)
	f.Close()
	der, err := x509.MarshalPKIXPublicKey(e.s.Offboard(e.c).Trusted()[0])
	x.NoError(err)
	ms, ks := sha256.Sum256(mb), sha256.Sum256(der)
	x.Contains(ev.Payload, "manifest=sha256:"+hex.EncodeToString(ms[:]))
	x.Contains(ev.Payload, "signed_by=sha256:"+hex.EncodeToString(ks[:]))
	x.Equal("sha256:"+hex.EncodeToString(ms[:]), got.Manifest)

	// And it is a workspace: its owner reads its register through the wall.
	h, err := e.s.Ent.Holder.Query().Where(holder.TenantId(acme.id.Uuid()), holder.Alias("owner")).Only(ctx)
	x.NoError(err)
	owner := frame.Into(ctx, frame.New(pdid.Id(h.Id), acme.id, frame.Whole()).WithScope(frame.Only(acme.id)).WithRow(h.Proto()))
	vs, err := e.s.Walled.Asset().List(owner, app.AssetListRequest_builder{}.Build())
	x.NoError(err)
	x.Len(vs.GetItems(), 2)

	_, err = e.restore(b, offboard.ImportOptions{})
	x.ErrorIs(err, offboard.ErrNotNew, "a workspace restored over itself")
}

// cmpTenant is what differs between two of a tenant's rows, but when they
// were last written.
func cmpTenant(a, b *app.Tenant) []string {
	out := []string{}
	if !bytes.Equal(a.GetId(), b.GetId()) {
		out = append(out, "id")
	}
	if a.GetAlias() != b.GetAlias() {
		out = append(out, "alias")
	}
	if a.GetName() != b.GetName() {
		out = append(out, "name")
	}
	if !a.GetDateCreated().AsTime().Equal(b.GetDateCreated().AsTime()) {
		out = append(out, "date_created")
	}
	return out
}

// TestARestoreTakesOnlyWhatTheDeploymentWrote: an export with no signature,
// or one changed since it was signed, is not restored -- nor anything of it.
func TestARestoreTakesOnlyWhatTheDeploymentWrote(t *testing.T) {
	t.Run("unsigned", func(t *testing.T) {
		e := newEnv(t)
		acme := e.tenant("acme")
		b := e.export(acme.id)
		_, err := e.s.Offboard(e.c).Purge(context.Background(), acme.id, false)
		e.x.NoError(err)

		_, err = e.restore(b, offboard.ImportOptions{})
		e.x.ErrorIs(err, offboard.ErrUnsigned)
		e.x.Empty(e.count(acme.id))
	})
	t.Run("changed", func(t *testing.T) {
		e, _ := signed(t)
		acme := e.tenant("acme")
		b := rezip(t, e.export(acme.id), func(fs map[string][]byte, order []string) []string {
			fs["rows/asset.jsonl"] = bytes.ReplaceAll(fs["rows/asset.jsonl"], []byte("노트북"), []byte("노트븍"))
			return order
		})
		_, err := e.s.Offboard(e.c).Purge(context.Background(), acme.id, false)
		e.x.NoError(err)

		_, err = e.restore(b, offboard.ImportOptions{})
		e.x.ErrorIs(err, offboard.ErrAltered)
		e.x.Empty(e.count(acme.id))
	})
}

// TestARestoreIsAllOfItOrNoneOfIt: a row that cannot be put back -- a label's
// code another workspace has taken since -- leaves nothing of the workspace
// behind, and no file.
func TestARestoreIsAllOfItOrNoneOfIt(t *testing.T) {
	e, _ := signed(t)
	x := e.x
	ctx := context.Background()
	acme, other := e.tenant("acme"), e.tenant("other")
	label := func(t pdid.Id) {
		_, err := e.s.Base.Label().Add(ctx, app.LabelAddRequest_builder{
			Tenant: app.TenantRef_builder{Id: t.Bytes()}.Build(),
			Code:   z.Ptr("QR-1"),
			State:  "unbound",
			Batch:  "b1",
		}.Build())
		x.NoError(err)
	}
	label(acme.id)
	b := e.export(acme.id)
	_, err := e.s.Offboard(e.c).Purge(ctx, acme.id, false)
	x.NoError(err)
	label(other.id)

	_, err = e.restore(b, offboard.ImportOptions{})
	x.Error(err)
	x.Contains(err.Error(), "label")
	x.Empty(e.count(acme.id), "a restore that failed left some of it")
	x.False(e.file(acme.photo.GetObjectKey()))
}

// TestARestoreFindsTheWorkspaceItsAddress: the export's when nobody has it,
// taken back from the hold put on it when the workspace left, another when
// another workspace has it -- and never one another workspace's hold is on
// without the operator saying so.
func TestARestoreFindsTheWorkspaceItsAddress(t *testing.T) {
	hold := func(e *env, alias string, tenant pdid.Id, reason string) {
		now := time.Now()
		e.x.NoError(e.s.Ent.AliasHold.Create().
			SetId(pdid.New(pd.AliasHoldDomain).Uuid()).
			SetHeld(alias).
			SetReason(reason).
			SetMovedTo("").
			SetTenant(tenant.Uuid()).
			SetDateUntil(now.Add(time.Hour)).
			SetDateCreated(now).
			SetDateUpdated(now).
			Exec(context.Background()))
	}
	held := func(e *env, alias string) bool {
		v, err := e.s.Ent.AliasHold.Query().Where(aliashold.Held(alias), aliashold.DateErasedIsNil()).Exist(context.Background())
		e.x.NoError(err)
		return v
	}
	gone := func(t *testing.T) (*env, tenant, []byte) {
		e, _ := signed(t)
		acme := e.tenant("acme")
		b := e.export(acme.id)
		_, err := e.s.Offboard(e.c).Purge(context.Background(), acme.id, false)
		e.x.NoError(err)
		return e, acme, b
	}

	t.Run("held for it", func(t *testing.T) {
		e, acme, b := gone(t)
		hold(e, "acme", acme.id, "destroyed")
		got, err := e.restore(b, offboard.ImportOptions{})
		e.x.NoError(err)
		e.x.Equal("acme", got.Alias)
		e.x.Equal([]string{"destroyed"}, got.Released)
		e.x.False(held(e, "acme"))
	})
	t.Run("held for another", func(t *testing.T) {
		e, _, b := gone(t)
		other := e.tenant("other")
		hold(e, "acme", other.id, "renamed")

		_, err := e.restore(b, offboard.ImportOptions{})
		e.x.ErrorContains(err, "--reuse")
		e.x.True(held(e, "acme"))

		got, err := e.restore(b, offboard.ImportOptions{Reuse: true})
		e.x.NoError(err)
		e.x.Equal("acme", got.Alias)
		e.x.Equal([]string{"renamed"}, got.Released)
		e.x.False(held(e, "acme"))
	})
	t.Run("another workspace's", func(t *testing.T) {
		e, acme, b := gone(t)
		e.tenant("acme")

		_, err := e.restore(b, offboard.ImportOptions{Alias: "acme"})
		e.x.ErrorContains(err, "another workspace's")

		got, err := e.restore(b, offboard.ImportOptions{})
		e.x.NoError(err)
		e.x.Equal("acme", got.Asked)
		e.x.NotEqual("acme", got.Alias)
		e.x.Equal(got.Alias, e.tenantRow(acme.id).GetAlias())
	})
	t.Run("asked for", func(t *testing.T) {
		e, acme, b := gone(t)
		_, err := e.restore(b, offboard.ImportOptions{Alias: "x"})
		e.x.Error(err, "an address no workspace may have")

		got, err := e.restore(b, offboard.ImportOptions{Alias: "@acme-again"})
		e.x.NoError(err)
		e.x.Equal("acme-again", got.Alias)
		e.x.Equal("acme", got.Asked)
		e.x.Equal("acme-again", e.tenantRow(acme.id).GetAlias())
	})
}

// TestAClosedWorkspaceIsRestoredOpen: exported closed, a workspace restored
// is open again -- or the next sweep would destroy it once more, its window
// long over.
func TestAClosedWorkspaceIsRestoredOpen(t *testing.T) {
	e, _ := signed(t)
	x := e.x
	ctx := context.Background()
	acme := e.tenant("acme")
	_, err := e.s.Deps.CloseWorkspace(ctx, e.s.Base, e.s.Drv, acme.id, domain.ClosedByOwner, "")
	x.NoError(err)
	x.True(e.tenantRow(acme.id).HasDateClosed())

	b := e.export(acme.id)
	_, err = e.s.Offboard(e.c).Purge(ctx, acme.id, false)
	x.NoError(err)

	got, err := e.restore(b, offboard.ImportOptions{})
	x.NoError(err)
	x.True(got.Reopened)
	v := e.tenantRow(acme.id)
	x.False(v.HasDateClosed())
	x.False(v.HasDateDestroy())
	x.Empty(v.GetClosedBy())

	ev, err := e.s.Ent.Event.Query().Where(event.TenantId(acme.id.Uuid()), event.Kind("workspace.import")).Only(ctx)
	x.NoError(err)
	x.Contains(ev.Payload, "reopened=true")
}

// TestAMemberIsRestoredWithTheAccountThisRosterHas: a member keeps an account
// the deployment's roster has, and one from another deployment's -- an
// identifier nobody here is -- is restored naming none, for `rove identity
// migrate` to give one to.
func TestAMemberIsRestoredWithTheAccountThisRosterHas(t *testing.T) {
	a, pub := signed(t)
	ctx := context.Background()
	acme := a.tenant("acme")

	_, err := a.s.Identity.EnsureDirectory(ctx)
	a.x.NoError(err)
	kim, err := a.s.Identity.AddPerson(ctx, "kim", "Kim", pdid.New(pd.HolderDomain))
	a.x.NoError(err)
	_, err = a.s.Base.Holder().Add(ctx, app.HolderAddRequest_builder{
		Tenant:  app.TenantRef_builder{Id: acme.id.Bytes()}.Build(),
		Alias:   "kim",
		Name:    "Kim",
		Account: kim.Id.Bytes(),
	}.Build())
	a.x.NoError(err)
	b := a.export(acme.id)

	accountOf := func(t *testing.T, e *env, alias string) []byte {
		h, err := e.s.Ent.Holder.Query().Where(holder.TenantId(acme.id.Uuid()), holder.Alias(alias)).Only(ctx)
		require.NoError(t, err)
		if h.Account == nil {
			return nil
		}
		return h.Account[:]
	}

	t.Run("the roster it came from", func(t *testing.T) {
		x := require.New(t)
		_, err := a.s.Offboard(a.c).Purge(ctx, acme.id, false)
		x.NoError(err)
		got, err := a.restore(b, offboard.ImportOptions{})
		x.NoError(err)
		x.Empty(got.Unlinked)
		x.Equal(kim.Id.Bytes(), accountOf(t, a, "kim"))
	})
	t.Run("another deployment's", func(t *testing.T) {
		e := newEnv(t, func(c *cmd.Config) { c.Audit.Checkpoints.Trust = []string{pub} })
		x := e.x
		got, err := e.restore(b, offboard.ImportOptions{})
		x.NoError(err)
		x.Equal([]string{"kim"}, got.Unlinked)
		x.Nil(accountOf(t, e, "kim"))

		ev, err := e.s.Ent.Event.Query().Where(event.TenantId(acme.id.Uuid()), event.Kind("workspace.import")).Only(ctx)
		x.NoError(err)
		x.Contains(ev.Payload, "unlinked=1")
	})
}

// TestARestoreIsSearchedOnceItIsBack: the rows go past the recorders, so the
// restore says itself what the index is to make.
func TestARestoreIsSearchedOnceItIsBack(t *testing.T) {
	e, _ := signed(t)
	x := e.x
	ctx := context.Background()
	acme := e.tenant("acme")
	b := e.export(acme.id)
	_, err := e.s.Offboard(e.c).Purge(ctx, acme.id, false)
	x.NoError(err)
	_, err = e.s.Ent.SearchStale.Delete().Exec(ctx)
	x.NoError(err)

	d := e.s.Offboard(e.c)
	d.Reindex = opensearch.Remember
	_, err = d.Import(ctx, bytes.NewReader(b), int64(len(b)), offboard.ImportOptions{})
	x.NoError(err)

	kinds := map[string]int{}
	vs, err := e.s.Ent.SearchStale.Query().All(ctx)
	x.NoError(err)
	for _, v := range vs {
		kinds[v.Kind]++
	}
	x.Equal(map[string]int{opensearch.KindAsset: 2, opensearch.KindParty: 1}, kinds)

	n, err := e.s.Ent.SearchStale.Query().Where(searchstale.KindIn(opensearch.KindAsset)).Count(ctx)
	x.NoError(err)
	x.Equal(2, n)
}
