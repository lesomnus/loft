package cmd_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/lesomnus/payday/pdid"
	"github.com/lesomnus/z"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	app "github.com/lesomnus/loft"
	"github.com/lesomnus/loft/cmd"
	"github.com/lesomnus/loft/server/session"
)

// TestAWorkspaceWithNoNameIsCalledAfterItsOwner is D57: a workspace put up
// with no name -- neither `loft init --name` nor the tenant's at roster --
// keeps none of its own, since its person's name would be in a field the
// trail keeps as a value, where nothing that forgets them reaches it. It is
// called after its owner wherever it is read, as they are called now, on the
// access record of whoever it is shown to.
func TestAWorkspaceWithNoNameIsCalledAfterItsOwner(t *testing.T) {
	web := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(web, "index.html"), []byte("<html></html>"), 0o644))
	w := newWorldWith(t, func(c *cmd.Config) {
		c.App.Web = web
		c.App.AppUrl = "https://loft.example"
	})
	x := w.x
	ctx := context.Background()
	parties := app.NewPartyServiceClient(w.conn)

	// The owner's person record, which is what they are called.
	owner, err := w.s.Base.Party().Add(ctx, app.PartyAddRequest_builder{
		Tenant: app.TenantRef_builder{Id: w.tenant.GetId()}.Build(),
		Name:   "박지민", Kind: "person",
		Holder: app.HolderRef_builder{Id: w.ownerHolder().GetId()}.Build(),
	}.Build())
	x.NoError(err)

	me, err := parties.Me(w.owner, &app.PartyMeRequest{})
	x.NoError(err)
	x.Empty(me.GetTenant().GetName(), "the workspace holds nobody's name")
	x.Equal("박지민의 워크스페이스", me.GetWorkspaceName(), "and is called after its owner")
	ws, err := pdid.From(me.GetTenant().GetId())
	x.NoError(err)

	// Nor is it on the trail, at any line about the workspace.
	lines := w.acts(t, ws)
	x.NotEmpty(lines)
	for _, v := range lines {
		x.False(bytes.Contains(v.Patch, []byte("박지민")), "the trail keeps the owner's name at %s", v.Action)
		x.False(bytes.Contains(v.Value, []byte("박지민")), "the trail keeps the owner's name at %s", v.Action)
	}

	// The name they go by now: renamed, the workspace is called after the
	// new one.
	_, err = parties.Update(w.owner, app.PartyUpdateRequest_builder{
		Ref:  app.PartyRef_builder{Id: owner.GetId()}.Build(),
		Name: "박서준",
	}.Build())
	x.NoError(err)
	me, err = parties.Me(w.owner, &app.PartyMeRequest{})
	x.NoError(err)
	x.Equal("박서준의 워크스페이스", me.GetWorkspaceName())

	// A member is told it, and the call says whose name it showed.
	kh := w.holder("kim", "member")
	kid, err := pdid.From(kh.GetId())
	x.NoError(err)
	seen, err := parties.Me(w.signIn(kh), &app.PartyMeRequest{})
	x.NoError(err)
	x.Equal("박서준의 워크스페이스", seen.GetWorkspaceName())
	found := false
	for _, v := range w.accessed(t, app.PartyService_Me_FullMethodName) {
		if v.ActorId == kid.Uuid() {
			x.Contains(v.Subjects, owner.GetId(), "Me showed the member the owner's name")
			found = true
		}
	}
	x.True(found, "the member's Me is not on the record")

	// What `GET /session` says of it -- for the page of a closed one, which
	// no call is there to say otherwise -- is its address, and shows nobody.
	rec := w.page(w.signIn(kh), http.MethodGet, "/session")
	x.Equal(http.StatusOK, rec.Code, rec.Body.String())
	var said session.Answer
	x.NoError(json.Unmarshal(rec.Body.Bytes(), &said))
	x.Equal(w.tenant.GetAlias(), said.Workspace.Name)
}

// TestAWorkspaceIsRenamedByItsOwnerAlone is D58 over the wire: the owner of a
// workspace names it, and it is called so wherever it is read -- showing
// nobody -- and takes the name back, and it is called after its owner again
// (D57). An administrator of it may not: what it is called is its owner's to
// say, as all of TenantService's writes are. And the general write stays
// closed.
func TestAWorkspaceIsRenamedByItsOwnerAlone(t *testing.T) {
	web := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(web, "index.html"), []byte("<html></html>"), 0o644))
	w := newWorldWith(t, func(c *cmd.Config) { c.App.Web = web })
	x := w.x
	ctx := context.Background()
	parties := app.NewPartyServiceClient(w.conn)
	tenants := app.NewTenantServiceClient(w.conn)
	ref := app.TenantRef_builder{Id: w.tenant.GetId()}.Build()

	owner, err := w.s.Base.Party().Add(ctx, app.PartyAddRequest_builder{
		Tenant: ref, Name: "박지민", Kind: "person",
		Holder: app.HolderRef_builder{Id: w.ownerHolder().GetId()}.Build(),
	}.Build())
	x.NoError(err)
	kh := w.holder("kim", "admin")
	kid, err := pdid.From(kh.GetId())
	x.NoError(err)
	asKim := w.signIn(kh)
	called := func(as context.Context) (string, string) {
		me, err := parties.Me(as, &app.PartyMeRequest{})
		x.NoError(err)
		rec := w.page(as, http.MethodGet, "/session")
		x.Equal(http.StatusOK, rec.Code, rec.Body.String())
		var said session.Answer
		x.NoError(json.Unmarshal(rec.Body.Bytes(), &said))
		return me.GetWorkspaceName(), said.Workspace.Name
	}
	showedOwner := func() bool {
		shown := false
		for _, v := range w.accessed(t, app.PartyService_Me_FullMethodName) {
			if v.ActorId == kid.Uuid() {
				shown = false
				for _, s := range v.Subjects {
					shown = shown || bytes.Equal(s, owner.GetId())
				}
			}
		}
		return shown
	}

	_, err = tenants.Configure(asKim, app.TenantConfigureRequest_builder{Ref: ref, Name: z.Ptr("김의 장비")}.Build())
	x.Equal(codes.PermissionDenied, status.Code(err), "named by an administrator")
	_, err = tenants.Patch(w.owner, app.TenantPatchRequest_builder{Ref: ref, Name: z.Ptr("연구실 장비"), DateUpdatedForce: z.Ptr(true)}.Build())
	x.Equal(codes.Unimplemented, status.Code(err), "named by the general write")

	v, err := tenants.Configure(w.owner, app.TenantConfigureRequest_builder{Ref: ref, Name: z.Ptr("연구실 장비")}.Build())
	x.NoError(err)
	x.Equal("연구실 장비", v.GetName())
	for _, who := range []context.Context{w.owner, asKim} {
		inside, said := called(who)
		x.Equal("연구실 장비", inside)
		x.Equal("연구실 장비", said)
	}
	x.False(showedOwner(), "its own name shows nobody")

	_, err = tenants.Configure(w.owner, app.TenantConfigureRequest_builder{Ref: ref, Name: z.Ptr("")}.Build())
	x.NoError(err)
	inside, said := called(asKim)
	x.Equal("박지민의 워크스페이스", inside)
	x.Equal(w.tenant.GetAlias(), said, "its address, where no call is there to say more")
	x.True(showedOwner(), "called after its owner again, which shows them")
}
