package cmd_test

import (
	"archive/zip"
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/lesomnus/payday/config"
	"github.com/lesomnus/payday/frame"
	"github.com/lesomnus/payday/pdid"
	"github.com/lesomnus/payday/pdtest"
	"github.com/lesomnus/z"
	"github.com/stretchr/testify/require"
	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"

	app "github.com/lesomnus/loft"
	"github.com/lesomnus/loft/cmd"
	"github.com/lesomnus/loft/internal/ent/aliashold"
	"github.com/lesomnus/loft/internal/ent/holder"
	enttenant "github.com/lesomnus/loft/internal/ent/tenant"
	"github.com/lesomnus/loft/server/admin"
	"github.com/lesomnus/loft/server/domain"
	"github.com/lesomnus/loft/server/offboard"
	"github.com/lesomnus/loft/server/operator"
	"github.com/lesomnus/loft/server/tenancy"
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

// operatorKey is the key alice, the world's operator, holds at its entry
// point.
const operatorKey = "an-operators-key-long-enough-to-be-one"

// entryPoint is the world's operator's entry point (`server/admin`, D74),
// served beside the public server: where a workspace is closed, restored and
// taken out (D83). A call on it carries alice's key ([byOperator]).
func (w *world) entryPoint(t *testing.T) *grpc.ClientConn {
	ops, err := admin.Operators(map[string]string{"alice": operatorKey})
	w.x.NoError(err)
	g, err := admin.Routes{S: w.s, C: w.c, Operators: ops}.Grpc(t.Context())
	w.x.NoError(err)
	return pdtest.Serve(t, g)
}

// byOperator is a call carrying alice's key, as `loft` makes one on an
// operator's connection.
func byOperator() context.Context {
	return metadata.AppendToOutgoingContext(context.Background(), "authorization", "Bearer "+operatorKey)
}

// atOperatorsWord is the deployment's own work at alice's word, as the entry
// point does it once her key is read: what [world.closeIt] and
// [world.restoreIt] do without a call.
func atOperatorsWord() context.Context {
	return operator.Into(context.Background(), operator.Named("alice"))
}

// closeIt closes the world's workspace as the entry point closes one (`loft
// tenant close`, D83), in `why`'s words.
func (w *world) closeIt(why string) *app.Tenant {
	v, err := w.s.Deps.CloseWorkspace(atOperatorsWord(), w.s.Base, w.s.Drv, tenantId(w.x, w.tenant), domain.ClosedByOperator, why)
	w.x.NoError(err)
	return v
}

// restoreIt opens the world's workspace again as the entry point opens one
// (`loft tenant restore`).
func (w *world) restoreIt() (*app.Tenant, error) {
	return w.s.Deps.RestoreWorkspace(atOperatorsWord(), w.s.Base, w.s.Drv, tenantId(w.x, w.tenant))
}

// refusedClosed says that `err` is a call into a closed workspace refused:
// FailedPrecondition, saying who to ask, with the reason a page tells it by
// (D83).
func (w *world) refusedClosed(err error, what string) {
	w.x.Equal(codes.FailedPrecondition, status.Code(err), "%s: %v", what, err)
	w.x.Contains(status.Convert(err).Message(), "운영자에게 문의하세요", what)
	var info *errdetails.ErrorInfo
	for _, d := range status.Convert(err).Details() {
		if v, ok := d.(*errdetails.ErrorInfo); ok {
			info = v
		}
	}
	w.x.NotNil(info, "%s: no reason a page can tell it by", what)
	w.x.Equal(cmd.ErrorClosed, info.GetReason(), what)
	w.x.Equal("loft", info.GetDomain(), what)
}

// TestAWorkspaceIsClosedAndRestoredByItsOperator (D29, D83): what becomes of
// a workspace is the operator's, at the entry point -- nobody in it closes
// it, restores it or takes all of it out, its owner neither. Closing destroys
// nothing and shuts everybody out, its owner too: every call into it is
// refused, with the reason a page tells it by, and the page is told which of
// an account's workspaces is closed and until when. The operator takes its
// data out while it is closed, and restoring puts it back as it was.
func TestAWorkspaceIsClosedAndRestoredByItsOperator(t *testing.T) {
	w := closingWorld(t)
	x := w.x
	kim := w.signIn(w.holder("kim", "admin"))
	tenants := app.NewTenantServiceClient(w.conn)
	assets := app.NewAssetServiceClient(w.conn)
	ref := app.TenantRef_builder{Id: w.tenant.GetId()}.Build()
	_, err := assets.Add(w.owner, app.AssetAddRequest_builder{Name: "노트북"}.Build())
	x.NoError(err)

	// None of it is anybody's in the workspace: the table gives no role any
	// of it.
	_, err = tenants.Close(w.owner, app.TenantCloseRequest_builder{Ref: ref, Confirm: w.tenant.GetAlias()}.Build())
	x.True(refusedByTable(err, app.TenantService_Close_FullMethodName), "its owner closes nothing: %v", err)
	_, err = tenants.Restore(w.owner, app.TenantRestoreRequest_builder{Ref: ref}.Build())
	x.True(refusedByTable(err, app.TenantService_Restore_FullMethodName), "%v", err)
	_, _, err = exported(w.owner, w.conn, ref, "closing")
	x.True(refusedByTable(err, app.TenantService_Export_FullMethodName), "nor takes all of it out: %v", err)

	// The operator closes it, by its address.
	entry := w.entryPoint(t)
	operated := app.NewTenantServiceClient(entry)
	v, err := operated.Close(byOperator(), app.TenantCloseRequest_builder{Ref: ref, Confirm: "@" + w.tenant.GetAlias(), Reason: "그만 씁니다"}.Build())
	x.NoError(err)
	x.True(v.HasDateClosed())
	x.Equal(domain.ClosedByOperator, v.GetClosedBy())
	x.WithinDuration(time.Now().Add(30*24*time.Hour), v.GetDateDestroy().AsTime(), time.Minute)

	// Every call into it is refused, whoever makes it: the workspace itself,
	// what its people read first, its restore and its export among them.
	for who, as := range map[string]context.Context{"its owner": w.owner, "kim": kim} {
		_, err = assets.Search(as, app.AssetSearchRequest_builder{}.Build())
		w.refusedClosed(err, who)
		_, err = tenants.Get(as, app.TenantGetRequest_builder{Ref: ref}.Build())
		w.refusedClosed(err, who)
		_, err = app.NewPartyServiceClient(w.conn).Me(as, &app.PartyMeRequest{})
		w.refusedClosed(err, who)
		_, err = tenants.Restore(as, app.TenantRestoreRequest_builder{Ref: ref}.Build())
		w.refusedClosed(err, who)
		_, _, err = exported(as, w.conn, ref, "closing")
		w.refusedClosed(err, who)
	}

	// What the page is told, which is no call into it: which of the
	// account's workspaces are closed, until when, and how long their
	// address is then held -- the deployment's, a year unset.
	mine, err := tenants.Mine(w.owner, &app.TenantMineRequest{})
	x.NoError(err)
	x.True(mine.GetWorkspaces()[0].HasDateClosed())
	x.True(mine.GetWorkspaces()[0].HasDateDestroy())
	x.EqualValues(365, mine.GetWorkspaces()[0].GetAliasHoldDays())

	// What it has, taken out by the operator before it goes.
	b, name, err := exported(byOperator(), entry, ref, "closing")
	x.NoError(err)
	zr, err := zip.NewReader(bytes.NewReader(b), int64(len(b)))
	x.NoError(err, "a zip")
	x.NotEmpty(zr.File)
	x.Contains(name, w.tenant.GetAlias()+"-")

	// And restored by the operator, as it was.
	_, err = operated.Restore(byOperator(), app.TenantRestoreRequest_builder{Ref: ref}.Build())
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
	ref := app.TenantRef_builder{Id: w.tenant.GetId()}.Build()
	w.closeIt("")

	past := func() {
		x.NoError(w.s.Ent.Tenant.UpdateOneId(id.Uuid()).SetDateDestroy(time.Now().Add(-time.Minute)).Exec(ctx))
	}
	past()
	_, err = w.restoreIt()
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

// TestAClosedWorkspaceThatCannotRetireWaits (payday#115): a closed workspace
// whose contract retires it, in a deployment that keeps floors and no archive
// to keep what they are on in, is not destroyed -- nor purged, which would
// take what the floors are on -- and the sweep goes on without failing. It
// waits, closed, as a held one does, until an operator settles it: here, a
// contract that says to purge it.
func TestAClosedWorkspaceThatCannotRetireWaits(t *testing.T) {
	w := newWorldWith(t, func(c *cmd.Config) {
		c.Audit.By = map[string]config.AuditKeepConfig{"access": {Min: config.AuditMinConfig{Destroy: 365 * 24 * time.Hour}}}
	})
	x := w.x
	ctx := context.Background()
	id, err := pdid.From(w.tenant.GetId())
	x.NoError(err)
	ref := app.TenantRef_builder{Id: w.tenant.GetId()}.Build()
	w.closeIt("")
	x.NoError(w.s.Ent.Tenant.UpdateOneId(id.Uuid()).SetDateDestroy(time.Now().Add(-time.Minute)).Exec(ctx))

	alive := func() bool {
		v, err := w.s.Ent.Tenant.Query().Where(enttenant.Alias(w.tenant.GetAlias())).Exist(ctx)
		x.NoError(err)
		return v
	}
	x.NoError(w.s.Closing.Sweep(ctx), "a workspace that cannot retire failed the sweep")
	x.True(alive(), "destroyed, and what the floors are on with it")
	n, err := w.s.Ent.AliasHold.Query().Where(aliashold.Held(w.tenant.GetAlias())).Count(ctx)
	x.NoError(err)
	x.Zero(n, "and nothing of its end has begun")

	_, err = w.s.Base.TenantContract().Add(ctx, app.TenantContractAddRequest_builder{
		Tenant:        ref,
		Name:          "abroad",
		PurgeFloors:   z.Ptr(true),
		DateEffective: timestamppb.New(time.Now().Add(-time.Minute)),
	}.Build())
	x.NoError(err)
	x.NoError(w.s.Closing.Sweep(ctx))
	x.False(alive(), "purged once its contract says so")
	held, err := w.s.Ent.AliasHold.Query().Where(aliashold.Held(w.tenant.GetAlias())).Only(ctx)
	x.NoError(err)
	x.Equal(offboard.ReasonDestroyed, held.Reason)
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
	x.Equal(codes.InvalidArgument, status.Code(err), "closing is the operator's")

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
// owner whose workspace was closed may make another.
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

	_, err = w.s.Deps.CloseWorkspace(atOperatorsWord(), w.s.Base, w.s.Drv, mine.Workspace, domain.ClosedByOperator, "")
	x.NoError(err)
	_, err = ws.Create(ctx, kim, "둘")
	x.NoError(err)
}
