package cmd_test

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/lesomnus/payday/frame"
	"github.com/lesomnus/payday/pdid"
	"github.com/lesomnus/z"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"

	app "github.com/lesomnus/rove"
	"github.com/lesomnus/rove/cmd"
	"github.com/lesomnus/rove/internal/ent/aliashold"
	"github.com/lesomnus/rove/internal/ent/holder"
	enttenant "github.com/lesomnus/rove/internal/ent/tenant"
	"github.com/lesomnus/rove/server/account"
	"github.com/lesomnus/rove/server/tenancy"
)

// closingWorld is the world with the page built, so the routes that only
// a deployment serving its UI has are there.
func closingWorld(t *testing.T) *world {
	web := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(web, "index.html"), []byte("<html></html>"), 0o644))
	return newWorldWith(t, func(c *cmd.Config) { c.App.Web = web })
}

// page is one plain request, as a browser signed in as `ctx`'s session makes
// it.
func (w *world) page(ctx context.Context, method, path string) *httptest.ResponseRecorder {
	mux := http.NewServeMux()
	w.s.Routes(mux, w.c)
	r := httptest.NewRequest(method, path, nil)
	if c := cookieOf(ctx); c != "" {
		r.Header.Set("Cookie", c)
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, r)
	return rec
}

// cookieOf is the session cookie a signed-in context carries.
func cookieOf(ctx context.Context) string {
	md, ok := metadata.FromOutgoingContext(ctx)
	if !ok {
		return ""
	}
	if v := md.Get("cookie"); len(v) > 0 {
		return v[0]
	}
	return ""
}

func timestampNow() *timestamppb.Timestamp { return timestamppb.Now() }

func (w *world) tenantRow() *app.Tenant {
	v, err := w.s.Base.Tenant().Get(context.Background(), app.TenantGetRequest_builder{Ref: app.TenantRef_builder{Id: w.tenant.GetId()}.Build()}.Build())
	w.x.NoError(err)
	return v
}

// TestAWorkspaceIsClosedByItsOwnerAndRestored.
//
// Closing destroys nothing and shuts everybody out -- its owner too, but for
// seeing it and restoring it -- and restoring puts it back as it was.
func TestAWorkspaceIsClosedByItsOwnerAndRestored(t *testing.T) {
	w := closingWorld(t)
	x := w.x
	kim := w.signIn(w.holder("kim", "admin"))
	tenants := app.NewTenantServiceClient(w.conn)
	assets := app.NewAssetServiceClient(w.conn)
	ref := app.TenantRef_builder{Id: w.tenant.GetId()}.Build()
	_, err := assets.Add(w.owner, app.AssetAddRequest_builder{Name: "노트북"}.Build())
	x.NoError(err)

	_, err = tenants.Close(kim, app.TenantCloseRequest_builder{Ref: ref, Confirm: w.tenant.GetName()}.Build())
	x.Equal(codes.PermissionDenied, status.Code(err), "an admin does not close the owner's workspace")
	_, err = tenants.Close(w.owner, app.TenantCloseRequest_builder{Ref: ref, Confirm: "something else"}.Build())
	x.Equal(codes.InvalidArgument, status.Code(err), "closed by typing its name")
	v, err := tenants.Close(w.owner, app.TenantCloseRequest_builder{Ref: ref, Confirm: w.tenant.GetName(), Reason: "그만 씁니다"}.Build())
	x.NoError(err)
	x.True(v.HasDateClosed())
	x.Equal("owner", v.GetClosedBy())
	x.WithinDuration(time.Now().Add(30*24*time.Hour), v.GetDateDestroy().AsTime(), time.Minute)

	_, err = assets.Search(kim, app.AssetSearchRequest_builder{}.Build())
	x.Equal(codes.FailedPrecondition, status.Code(err), "nobody uses a closed workspace")
	_, err = assets.Search(w.owner, app.AssetSearchRequest_builder{}.Build())
	x.Equal(codes.FailedPrecondition, status.Code(err), "its owner neither")
	_, err = tenants.Restore(kim, app.TenantRestoreRequest_builder{Ref: ref}.Build())
	x.Equal(codes.FailedPrecondition, status.Code(err), "nor restores it but its owner")
	_, err = tenants.Get(w.owner, app.TenantGetRequest_builder{Ref: ref}.Build())
	x.NoError(err, "who sees it")

	// What the page is told: which workspaces are closed, until when, and
	// how long their address is then held -- the deployment's, a year unset.
	res := w.page(w.owner, http.MethodGet, "/account")
	x.Equal(http.StatusOK, res.Code)
	var acc account.Account
	x.NoError(json.Unmarshal(res.Body.Bytes(), &acc))
	x.NotNil(acc.Workspaces[0].Closed)
	x.NotNil(acc.Workspaces[0].Destroy)
	x.EqualValues(365, acc.Workspaces[0].AliasHoldDays)

	// And what its owner takes out before it goes.
	res = w.page(w.owner, http.MethodGet, "/account/workspaces/"+w.tenant.GetAlias()+"/export?reason=closing")
	x.Equal(http.StatusOK, res.Code)
	zr, err := zip.NewReader(bytes.NewReader(res.Body.Bytes()), int64(res.Body.Len()))
	x.NoError(err, "a zip")
	x.NotEmpty(zr.File)
	x.Equal(strconv.Itoa(res.Body.Len()), res.Header().Get("Content-Length"), "written whole before it was sent")
	x.Equal(http.StatusForbidden, w.page(kim, http.MethodGet, "/account/workspaces/"+w.tenant.GetAlias()+"/export").Code)

	_, err = tenants.Restore(w.owner, app.TenantRestoreRequest_builder{Ref: ref}.Build())
	x.NoError(err)
	x.False(w.tenantRow().HasDateClosed())
	got, err := assets.Search(kim, app.AssetSearchRequest_builder{Q: "노트북"}.Build())
	x.NoError(err)
	x.Len(got.GetItems(), 1, "as it was")
}

// TestAClosedWorkspaceIsDestroyedOnceItsWindowIsOver: unless something holds
// it, which keeps it closed until the hold is lifted -- and its address is
// then nobody's for a year.
func TestAClosedWorkspaceIsDestroyedOnceItsWindowIsOver(t *testing.T) {
	w := closingWorld(t)
	x := w.x
	ctx := context.Background()
	id, err := pdid.From(w.tenant.GetId())
	x.NoError(err)
	tenants := app.NewTenantServiceClient(w.conn)
	ref := app.TenantRef_builder{Id: w.tenant.GetId()}.Build()
	_, err = tenants.Close(w.owner, app.TenantCloseRequest_builder{Ref: ref, Confirm: w.tenant.GetName()}.Build())
	x.NoError(err)

	past := func() {
		x.NoError(w.s.Ent.Tenant.UpdateOneId(id.Uuid()).SetDateDestroy(time.Now().Add(-time.Minute)).Exec(ctx))
	}
	past()
	_, err = tenants.Restore(w.owner, app.TenantRestoreRequest_builder{Ref: ref}.Build())
	x.Equal(codes.FailedPrecondition, status.Code(err), "too late to restore")

	// Held: it waits.
	hold, err := w.s.Base.LegalHold().Add(ctx, app.LegalHoldAddRequest_builder{
		Tenant: ref, Name: "사건 1",
	}.Build())
	x.NoError(err)
	x.NoError(w.s.Closing.Sweep(ctx))
	alive, err := w.s.Ent.Tenant.Query().Where(enttenant.Alias(w.tenant.GetAlias())).Exist(ctx)
	x.NoError(err)
	x.True(alive, "a hold keeps it")
	n, err := w.s.Ent.AliasHold.Query().Where(aliashold.Held(w.tenant.GetAlias())).Count(ctx)
	x.NoError(err)
	x.Zero(n, "and nothing of its end has begun")

	_, err = w.s.Base.LegalHold().Patch(ctx, app.LegalHoldPatchRequest_builder{
		Ref: app.LegalHoldRef_builder{Id: hold.GetId()}.Build(), DateLifted: timestampNow(),
	}.Build())
	x.NoError(err)
	x.NoError(w.s.Closing.Sweep(ctx))
	alive, err = w.s.Ent.Tenant.Query().Where(enttenant.Alias(w.tenant.GetAlias())).Exist(ctx)
	x.NoError(err)
	x.False(alive, "destroyed once nothing holds it")

	held, err := w.s.Ent.AliasHold.Query().Where(aliashold.Held(w.tenant.GetAlias())).Only(ctx)
	x.NoError(err)
	x.Equal("destroyed", held.Reason)
	x.WithinDuration(time.Now().Add(365*24*time.Hour), held.DateUntil, time.Minute)
	free, err := w.s.Aliases(w.c).Free(ctx, w.tenant.GetAlias())
	x.NoError(err)
	x.False(free, "nobody else is that address to the people who knew it")
}

// TestAnAddressIsNotTheOwnersToChange: nobody chose it, and an operator alone
// moves it -- holding the old one, which sends people on.
func TestAnAddressIsNotTheOwnersToChange(t *testing.T) {
	w := closingWorld(t)
	x := w.x
	ctx := context.Background()
	tenants := app.NewTenantServiceClient(w.conn)
	ref := app.TenantRef_builder{Id: w.tenant.GetId()}.Build()

	// Over the wire the general write is closed altogether, and the domain
	// layer refuses it besides, for whatever opens it one day.
	_, err := tenants.Patch(w.owner, app.TenantPatchRequest_builder{Ref: ref, Alias: z.Ptr("mine"), DateUpdatedForce: z.Ptr(true)}.Build())
	x.Error(err)
	owner, err := w.s.Ent.Holder.Query().Where(holder.Role("owner")).First(ctx)
	x.NoError(err)
	tid, _ := pdid.From(w.tenant.GetId())
	as := frame.Into(ctx, frame.New(pdid.Id(owner.Id), tid, frame.Whole()).WithScope(frame.Only(tid)))
	_, err = w.s.Ungated.Tenant().Patch(as, app.TenantPatchRequest_builder{Ref: ref, Alias: z.Ptr("mine"), DateUpdatedForce: z.Ptr(true)}.Build())
	x.Equal(codes.InvalidArgument, status.Code(err))
	_, err = w.s.Ungated.Tenant().Patch(as, app.TenantPatchRequest_builder{Ref: ref, DateClosedNull: z.Ptr(true), DateUpdatedForce: z.Ptr(true)}.Build())
	x.Equal(codes.InvalidArgument, status.Code(err), "closing is Close's")

	old := w.tenant.GetAlias()
	x.ErrorContains(w.s.Closing.Rename(ctx, old, "admin", false), "kept for this app")
	x.NoError(w.s.Closing.Rename(ctx, old, "lab-2", false))
	x.Equal("lab-2", w.tenantRow().GetAlias())

	res := w.page(context.Background(), http.MethodGet, "/@"+old+"/assets?q=1")
	x.Equal(http.StatusFound, res.Code)
	x.Equal("/@lab-2/assets?q=1", res.Header().Get("Location"), "the old address sends people on")

	other, err := w.s.Base.Tenant().Add(ctx, app.TenantAddRequest_builder{Alias: "beta"}.Build())
	x.NoError(err)
	x.ErrorContains(w.s.Closing.Rename(ctx, other.GetAlias(), old, false), "held")
	x.NoError(w.s.Closing.Rename(ctx, other.GetAlias(), old, true), "taken anyway, on purpose")
}

// TestTheLastToLeaveClosesTheWorkspace: an account leaving a workspace it was
// the only one in closes it, so that it goes the way a closed workspace goes.
func TestTheLastToLeaveClosesTheWorkspace(t *testing.T) {
	w := newWorld(t)
	x := w.x
	ctx := context.Background()
	kim, err := w.s.Identity.AddPerson(ctx, "kim", "Kim", pdid.Nil)
	x.NoError(err)
	alone, err := w.s.Workspaces(w.c).Create(ctx, kim, "김의 워크스페이스")
	x.NoError(err)
	w.here(kim, "member")

	gone, err := w.s.Lifecycle.Depart(ctx, kim.Id)
	x.NoError(err)
	closed := map[pdid.Id]bool{}
	for _, v := range gone {
		closed[v.Workspace] = v.Closed
	}
	x.True(closed[alone.Workspace], "nobody left in it")
	acme, err := pdid.From(w.tenant.GetId())
	x.NoError(err)
	x.False(closed[acme], "acme has its owner")

	v, err := w.s.Ent.Tenant.Get(ctx, alone.Workspace.Uuid())
	x.NoError(err)
	x.NotNil(v.DateClosed)
	x.Equal("deployment", v.ClosedBy)
}

// TestAClosedWorkspaceIsNotOneMoreAnAccountOwns: it is on its way out, so an
// owner who closed one may make another.
func TestAClosedWorkspaceIsNotOneMoreAnAccountOwns(t *testing.T) {
	w := newWorldWith(t, func(c *cmd.Config) { c.App.Workspaces.Owned = 1 })
	x := w.x
	ctx := context.Background()
	kim, err := w.s.Identity.AddPerson(ctx, "kim", "Kim", pdid.Nil)
	x.NoError(err)
	ws := w.s.Workspaces(w.c)
	mine, err := ws.Create(ctx, kim, "하나")
	x.NoError(err)
	_, err = ws.Create(ctx, kim, "둘")
	x.ErrorIs(err, tenancy.ErrOwnsEnough)

	_, err = w.s.Deps.CloseWorkspace(ctx, w.s.Base, w.s.Drv, mine.Workspace, "owner", "")
	x.NoError(err)
	_, err = ws.Create(ctx, kim, "둘")
	x.NoError(err)
}
