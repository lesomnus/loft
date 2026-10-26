package cmd_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/lesomnus/payday/auth/authsession"
	"github.com/lesomnus/payday/frame"
	"github.com/lesomnus/payday/pdid"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	app "github.com/lesomnus/rove"
	"github.com/lesomnus/rove/cmd"
	"github.com/lesomnus/rove/internal/identity"
	"github.com/lesomnus/rove/server/account"
)

// TestAWorkspaceGivenOnArrivalIsCalledAfterItsOwner is D57: the workspace an
// account is given when it arrives has no name of its own -- its person's name
// would be in a field the trail keeps as a value, where nothing that forgets
// them reaches it -- and is called after its owner wherever it is read, as
// they are called now, on the access record of whoever it is shown to.
func TestAWorkspaceGivenOnArrivalIsCalledAfterItsOwner(t *testing.T) {
	web := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(web, "index.html"), []byte("<html></html>"), 0o644))
	w := newWorldWith(t, func(c *cmd.Config) {
		c.App.Web = web
		c.App.AppUrl = "https://rove.example"
	})
	x := w.x
	ctx := context.Background()
	box := &mailbox{}
	w.s.Deps.Mail = box
	as := func(p identity.Person) context.Context {
		_, c, err := w.s.Sessions.Mint(ctx, authsession.Session{Id: p.Id.String(), Grant: frame.Whole()})
		x.NoError(err)
		return metadata.AppendToOutgoingContext(ctx, "cookie", c.Name+"="+c.Value)
	}
	parties := app.NewPartyServiceClient(w.conn)
	tenants := app.NewTenantServiceClient(w.conn)

	park, err := w.s.Identity.AddPerson(ctx, "park", "박지민", pdid.Nil)
	x.NoError(err)
	x.NoError(w.s.Workspaces(w.c).Arrive(ctx, park))
	asPark := as(park)

	me, err := parties.Me(asPark, &app.PartyMeRequest{})
	x.NoError(err)
	x.Equal("owner", me.GetRole())
	x.Empty(me.GetTenant().GetName(), "the workspace holds nobody's name")
	x.Equal("박지민의 워크스페이스", me.GetWorkspaceName(), "and is called after its owner")
	ws, err := pdid.From(me.GetTenant().GetId())
	x.NoError(err)
	ref := app.TenantRef_builder{Id: ws.Bytes()}.Build()

	// Nor is it on the trail, at any line about the workspace.
	lines := w.acts(t, ws)
	x.NotEmpty(lines)
	for _, v := range lines {
		x.False(bytes.Contains(v.Patch, []byte("박지민")), "the trail keeps the owner's name at %s", v.Action)
		x.False(bytes.Contains(v.Value, []byte("박지민")), "the trail keeps the owner's name at %s", v.Action)
	}

	// The name they go by now: renamed, the workspace is called after the
	// new one.
	_, err = parties.Update(asPark, app.PartyUpdateRequest_builder{
		Ref:  app.PartyRef_builder{Id: me.GetParty().GetId()}.Build(),
		Name: "박서준",
	}.Build())
	x.NoError(err)
	me, err = parties.Me(asPark, &app.PartyMeRequest{})
	x.NoError(err)
	x.Equal("박서준의 워크스페이스", me.GetWorkspaceName())

	// A member is told it in their account and in the workspace, and both
	// say whose name they showed.
	kim, err := w.s.Identity.AddPerson(ctx, "kim", "김", pdid.Nil)
	x.NoError(err)
	kh := w.member(me.GetTenant(), kim, "member")
	asKim := as(kim)
	res := w.page(asKim, http.MethodGet, "/account")
	x.Equal(http.StatusOK, res.Code)
	var acc account.Account
	x.NoError(json.Unmarshal(res.Body.Bytes(), &acc))
	x.Len(acc.Workspaces, 1)
	x.Equal("박서준의 워크스페이스", acc.Workspaces[0].Name)
	rs := w.accessed(t, "GET /account")
	x.Len(rs, 1, "the account's answer showed the owner's name")
	x.Equal(ws.Uuid(), rs[0].TenantId)
	kid, err := pdid.From(kh.GetId())
	x.NoError(err)
	x.Equal(kid.Uuid(), rs[0].ActorId, "by the account's membership of the workspace")
	x.Equal("read", rs[0].Kind)
	x.Contains(rs[0].Subjects, me.GetParty().GetId(), "of the owner's person record")

	seen, err := parties.Me(asKim, &app.PartyMeRequest{})
	x.NoError(err)
	x.Equal("박서준의 워크스페이스", seen.GetWorkspaceName())
	found := false
	for _, v := range w.accessed(t, app.PartyService_Me_FullMethodName) {
		if v.ActorId == kid.Uuid() {
			x.Contains(v.Subjects, me.GetParty().GetId(), "Me showed the member the owner's name")
			found = true
		}
	}
	x.True(found, "the member's Me is not on the record")

	// An invitation out of it, in the mail and in the inbox of the account
	// it is addressed to.
	lee, err := w.s.Identity.AddPerson(ctx, "lee", "이", pdid.Nil)
	x.NoError(err)
	x.NoError(w.s.Identity.AddAddress(ctx, lee.Id, "lee@example.com", true))
	_, err = app.NewInvitationServiceClient(w.conn).Add(asPark, app.InvitationAddRequest_builder{
		Tenant: ref, Email: "lee@example.com", Role: "member",
	}.Build())
	x.NoError(err)
	x.Len(box.sent, 1)
	x.Equal("박서준님이 Rove의 박서준의 워크스페이스에 초대했습니다", box.sent[0].Subject)
	x.Contains(box.sent[0].Body, "박서준님이 Rove의 박서준의 워크스페이스에 구성원으로 초대했습니다.")
	inbox, err := app.NewInvitationServiceClient(w.conn).List(as(lee), app.InvitationListRequest_builder{
		Filters: []*app.InvitationFilter{app.InvitationFilter_builder{Account: lee.Id.Bytes()}.Build()},
	}.Build())
	x.NoError(err)
	x.Len(inbox.GetItems(), 1)
	x.Equal("박서준의 워크스페이스", inbox.GetItems()[0].GetTenant().GetName())
	found = false
	for _, v := range w.accessed(t, app.InvitationService_List_FullMethodName) {
		if v.TenantId == ws.Uuid() {
			x.Contains(v.Subjects, me.GetParty().GetId(), "the inbox showed the owner's name")
			found = true
		}
	}
	x.True(found, "the inbox is not on the workspace's record")

	// One the account makes has the name it chose, and saying it shows
	// nobody: its account's answer is on the record of the one called after
	// its owner alone.
	lab, err := w.s.Workspaces(w.c).Create(ctx, park, "연구실")
	x.NoError(err)
	x.Empty(lab.Saw)
	res = w.page(asPark, http.MethodGet, "/account")
	x.Equal(http.StatusOK, res.Code)
	x.NoError(json.Unmarshal(res.Body.Bytes(), &acc))
	x.Len(acc.Workspaces, 2)
	x.Equal("박서준의 워크스페이스", acc.Workspaces[0].Name)
	x.Equal("연구실", acc.Workspaces[1].Name)
	rs = w.accessed(t, "GET /account")
	x.Len(rs, 2, "a row for each answer that showed somebody")
	x.Equal(ws.Uuid(), rs[1].TenantId, "and of the workspace that did")

	// Closing it takes typing what it is called: there is no name to leave
	// empty.
	there := metadata.AppendToOutgoingContext(asPark, cmd.WorkspaceHeader, me.GetTenant().GetAlias())
	_, err = tenants.Close(there, app.TenantCloseRequest_builder{Ref: ref}.Build())
	x.Equal(codes.InvalidArgument, status.Code(err), "closed by typing nothing")
	x.Contains(status.Convert(err).Message(), "박서준의 워크스페이스")
	_, err = tenants.Close(there, app.TenantCloseRequest_builder{Ref: ref, Confirm: "박서준의 워크스페이스"}.Build())
	x.NoError(err)
}
