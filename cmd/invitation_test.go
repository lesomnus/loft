package cmd_test

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/lesomnus/payday/auth/authsession"
	"github.com/lesomnus/payday/config"
	"github.com/lesomnus/payday/frame"
	"github.com/lesomnus/payday/pdid"
	"github.com/lesomnus/payday/web"
	"github.com/lesomnus/z"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	app "github.com/lesomnus/loft"
	"github.com/lesomnus/loft/cmd"
	"github.com/lesomnus/loft/internal/identity"
	"github.com/lesomnus/loft/server/mail"
)

// Inviting an address into a workspace, and the account it belongs to
// answering (design 9.11). The account is nobody's member when it reads and
// answers -- the calls are served as the account (`cmd.AccountMethods`) -- so
// what is pinned here is the rule the wall would have kept, written down
// instead: whose an invitation is.

// mailbox is every message the deployment sent.
type mailbox struct {
	mu   sync.Mutex
	sent []mail.Message
}

func (m *mailbox) Send(_ context.Context, v mail.Message) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.sent = append(m.sent, v)
	return nil
}

// code is what the last mail to an address carried, which is what opening its
// link hands the page.
func (m *mailbox) code(t *testing.T, to string) string {
	t.Helper()
	m.mu.Lock()
	defer m.mu.Unlock()
	for i := len(m.sent) - 1; i >= 0; i-- {
		if m.sent[i].To != to {
			continue
		}
		_, rest, ok := strings.Cut(m.sent[i].Body, "/invitations?code=")
		require.True(t, ok, "the mail carries no link: %s", m.sent[i].Body)
		code, _, _ := strings.Cut(rest, "\n")
		return strings.TrimSpace(code)
	}
	t.Fatalf("nothing was sent to %s", to)
	return ""
}

type invites struct {
	*world
	box *mailbox
	svc app.InvitationServiceClient
}

func newInvites(t *testing.T, with func(*cmd.Config)) *invites {
	t.Helper()
	w := newWorldWith(t, func(c *cmd.Config) {
		c.App.AppUrl = "https://loft.example"
		if with != nil {
			with(c)
		}
	})
	box := &mailbox{}
	w.s.Deps.Mail = box
	return &invites{world: w, box: box, svc: app.NewInvitationServiceClient(w.conn)}
}

// account is somebody at roster, in no workspace, with the addresses given:
// checked ones, and ones nobody checked.
func (w *invites) account(alias string, checked, unchecked []string) identity.Person {
	ctx := context.Background()
	p, err := w.s.Identity.AddPerson(ctx, alias, alias, pdid.Nil)
	w.x.NoError(err)
	for _, a := range checked {
		w.x.NoError(w.s.Identity.AddAddress(ctx, p.Id, a, true))
	}
	for _, a := range unchecked {
		w.x.NoError(w.s.Identity.AddAddress(ctx, p.Id, a, false))
	}
	return p
}

// as is a browser signed in as an account, on a page about no workspace.
func (w *invites) as(p identity.Person) context.Context {
	_, c, err := w.s.Sessions.Mint(context.Background(), authsession.Session{Id: p.Id.String(), Grant: frame.Whole()})
	w.x.NoError(err)
	return metadata.AppendToOutgoingContext(context.Background(), "cookie", c.Name+"="+c.Value)
}

func (w *invites) invite(as context.Context, email, role string) (*app.Invitation, error) {
	return w.svc.Add(as, app.InvitationAddRequest_builder{
		Tenant: app.TenantRef_builder{Id: w.tenant.GetId()}.Build(),
		Email:  email,
		Role:   role,
	}.Build())
}

func (w *invites) inbox(as context.Context, p identity.Person) []*app.Invitation {
	v, err := w.svc.List(as, app.InvitationListRequest_builder{
		Filters: []*app.InvitationFilter{app.InvitationFilter_builder{Account: p.Id.Bytes()}.Build()},
	}.Build())
	w.x.NoError(err)
	return v.GetItems()
}

func (w *invites) sent(as context.Context) ([]*app.Invitation, error) {
	v, err := w.svc.List(as, app.InvitationListRequest_builder{
		Filters: []*app.InvitationFilter{app.InvitationFilter_builder{
			Tenant: app.TenantRef_builder{Id: w.tenant.GetId()}.Build(),
		}.Build()},
	}.Build())
	return v.GetItems(), err
}

func ref(v *app.Invitation) *app.InvitationRef {
	return app.InvitationRef_builder{Id: v.GetId()}.Build()
}

func TestAnInvitationIsAnsweredByTheAccountItIsAddressedTo(t *testing.T) {
	w := newInvites(t, nil)
	x := w.x

	v, err := w.invite(w.owner, " Kim@Acme.Example ", "manager")
	x.NoError(err)
	x.Equal("kim@acme.example", v.GetEmail())
	x.Equal("pending", v.GetState())
	x.Empty(v.GetClaim(), "the code went back to the people who invited")

	// The mail: who asked, into what, and the way in.
	x.Len(w.box.sent, 1)
	m := w.box.sent[0]
	x.Equal("kim@acme.example", m.To)
	x.Contains(m.Body, "매니저로")
	x.Contains(m.Body, "https://loft.example/invitations?code=")

	// An account with that address checked, in no workspace at all.
	kim := w.account("kim", []string{"kim@acme.example"}, nil)
	as := w.as(kim)
	got := w.inbox(as, kim)
	x.Len(got, 1)
	x.Equal("acme", got[0].GetTenant().GetAlias())
	x.Equal("owner", got[0].GetInviter().GetName())
	x.Equal("manager", got[0].GetRole())

	// Accepted: a member, as what it said, with a person record of its own.
	v, err = w.svc.Accept(as, app.InvitationAcceptRequest_builder{Ref: ref(got[0])}.Build())
	x.NoError(err)
	x.Equal("accepted", v.GetState())
	me, err := app.NewPartyServiceClient(w.conn).Me(metadata.AppendToOutgoingContext(as, cmd.WorkspaceHeader, "acme"), &app.PartyMeRequest{})
	x.NoError(err)
	x.Equal("manager", me.GetRole())
	x.Equal("kim@acme.example", me.GetParty().GetEmail())
	x.Empty(w.inbox(as, kim), "an answered invitation is still waiting")

	// And the people who invited see what became of it.
	out, err := w.sent(w.owner)
	x.NoError(err)
	x.Len(out, 1)
	x.Equal("accepted", out[0].GetState())

	t.Run("twice is once", func(t *testing.T) {
		_, err := w.svc.Accept(as, app.InvitationAcceptRequest_builder{Ref: ref(got[0])}.Build())
		require.Equal(t, codes.FailedPrecondition, status.Code(err), "%v", err)
	})
}

// TestAnAddressNobodyCheckedIsNobodysInvitation is nOAuth, made into a test: a
// directory whose administrator typed somebody else's address on a user of
// their own hands roster that address with nothing checked, and the
// invitation sent to it is not that user's.
func TestAnAddressNobodyCheckedIsNobodysInvitation(t *testing.T) {
	w := newInvites(t, nil)
	x := w.x

	v, err := w.invite(w.owner, "kim@acme.example", "admin")
	x.NoError(err)

	mallory := w.account("mallory", nil, []string{"kim@acme.example"})
	as := w.as(mallory)
	x.Empty(w.inbox(as, mallory), "an address nobody checked found an invitation")
	_, err = w.svc.Accept(as, app.InvitationAcceptRequest_builder{Ref: ref(v)}.Build())
	x.Equal(codes.NotFound, status.Code(err), "%v", err)
	_, err = w.svc.Deny(as, app.InvitationDenyRequest_builder{Ref: ref(v)}.Build())
	x.Equal(codes.NotFound, status.Code(err), "%v", err)

	// Nor is anybody's inbox readable by naming them.
	lee := w.account("lee", nil, nil)
	_, err = w.svc.List(as, app.InvitationListRequest_builder{
		Filters: []*app.InvitationFilter{app.InvitationFilter_builder{Account: lee.Id.Bytes()}.Build()},
	}.Build())
	x.Equal(codes.PermissionDenied, status.Code(err), "%v", err)
}

// TestTheLinkAMailCarriedMakesItYours: an account roster holds no checked
// address of -- a Microsoft one with no `xms_edov` -- reads the mailbox, and
// that is the proof. Once.
func TestTheLinkAMailCarriedMakesItYours(t *testing.T) {
	w := newInvites(t, nil)
	x := w.x

	_, err := w.invite(w.owner, "lee@acme.example", "member")
	x.NoError(err)
	code := w.box.code(t, "lee@acme.example")

	lee := w.account("lee", nil, nil)
	as := w.as(lee)
	x.Empty(w.inbox(as, lee))

	v, err := w.svc.Claim(as, app.InvitationClaimRequest_builder{Code: code}.Build())
	x.NoError(err)
	x.Equal("acme", v.GetTenant().GetAlias())
	x.Len(w.inbox(as, lee), 1)

	// A code is spent once, and is nobody else's afterwards.
	_, err = w.svc.Claim(as, app.InvitationClaimRequest_builder{Code: code}.Build())
	x.Equal(codes.NotFound, status.Code(err), "%v", err)
	park := w.account("park", nil, nil)
	_, err = w.svc.Claim(w.as(park), app.InvitationClaimRequest_builder{Code: code}.Build())
	x.Equal(codes.NotFound, status.Code(err), "%v", err)
	_, err = w.svc.Claim(as, app.InvitationClaimRequest_builder{Code: "made-up"}.Build())
	x.Equal(codes.NotFound, status.Code(err), "%v", err)

	_, err = w.svc.Accept(as, app.InvitationAcceptRequest_builder{Ref: ref(v)}.Build())
	x.NoError(err)
}

func TestWhoMayInviteAndAsWhat(t *testing.T) {
	w := newInvites(t, nil)

	admin := w.signIn(w.holder("ana", "admin"))
	member := w.signIn(w.holder("max", "member"))

	for _, c := range []struct {
		name string
		as   context.Context
		role string
		want codes.Code
	}{
		{"a member invites nobody", member, "member", codes.PermissionDenied},
		{"an admin, nobody wider than an admin", admin, "owner", codes.PermissionDenied},
		{"an admin, an admin", admin, "admin", codes.OK},
		{"an owner, an owner", w.owner, "owner", codes.OK},
		{"a role that is not one", w.owner, "emperor", codes.InvalidArgument},
	} {
		t.Run(c.name, func(t *testing.T) {
			_, err := w.invite(c.as, c.name[:2]+"-"+strings.ReplaceAll(c.role, " ", "")+"@acme.example", c.role)
			require.Equal(t, c.want, status.Code(err), "%v", err)
		})
	}

	t.Run("an address that is not one", func(t *testing.T) {
		_, err := w.invite(w.owner, "kim at acme", "member")
		require.Equal(t, codes.InvalidArgument, status.Code(err), "%v", err)
	})
	t.Run("an address already waiting", func(t *testing.T) {
		_, err := w.invite(w.owner, "twice@acme.example", "member")
		require.NoError(t, err)
		_, err = w.invite(w.owner, "Twice@Acme.Example", "admin")
		require.Equal(t, codes.AlreadyExists, status.Code(err), "%v", err)
	})

	t.Run("a workspace's invitations are its administrators' to read", func(t *testing.T) {
		x := require.New(t)
		_, err := w.sent(member)
		x.Equal(codes.PermissionDenied, status.Code(err), "%v", err)
		v, err := w.sent(admin)
		x.NoError(err)
		x.NotEmpty(v)

		// And an account that is no member of it reads none of them.
		stranger := w.account("stranger", nil, nil)
		_, err = w.sent(w.as(stranger))
		x.Equal(codes.PermissionDenied, status.Code(err), "%v", err)
	})

	t.Run("a list asks one question", func(t *testing.T) {
		x := require.New(t)
		_, err := w.svc.List(w.owner, app.InvitationListRequest_builder{}.Build())
		x.Equal(codes.InvalidArgument, status.Code(err), "%v", err)
		_, err = w.svc.List(w.owner, app.InvitationListRequest_builder{
			Filters: []*app.InvitationFilter{app.InvitationFilter_builder{
				Tenant:  app.TenantRef_builder{Id: w.tenant.GetId()}.Build(),
				Account: pdid.New(1).Bytes(),
			}.Build()},
		}.Build())
		x.Equal(codes.InvalidArgument, status.Code(err), "%v", err)
	})
}

func TestAnAnswerIsGivenOnceAndInTime(t *testing.T) {
	w := newInvites(t, nil)

	kim := w.account("kim", []string{"kim@acme.example", "kim2@acme.example", "kim3@acme.example"}, nil)
	as := w.as(kim)

	t.Run("no, and then no yes", func(t *testing.T) {
		x := require.New(t)
		v, err := w.invite(w.owner, "kim@acme.example", "member")
		x.NoError(err)
		d, err := w.svc.Deny(as, app.InvitationDenyRequest_builder{Ref: ref(v)}.Build())
		x.NoError(err)
		x.Equal("denied", d.GetState())
		_, err = w.svc.Accept(as, app.InvitationAcceptRequest_builder{Ref: ref(v)}.Build())
		x.Equal(codes.FailedPrecondition, status.Code(err), "%v", err)
	})

	t.Run("taken back", func(t *testing.T) {
		x := require.New(t)
		v, err := w.invite(w.owner, "kim2@acme.example", "member")
		x.NoError(err)
		r, err := w.svc.Revoke(w.owner, app.InvitationRevokeRequest_builder{Ref: ref(v)}.Build())
		x.NoError(err)
		x.Equal("revoked", r.GetState())
		x.Empty(w.inbox(as, kim))
		_, err = w.svc.Accept(as, app.InvitationAcceptRequest_builder{Ref: ref(v)}.Build())
		x.Equal(codes.FailedPrecondition, status.Code(err), "%v", err)
		_, err = w.svc.Revoke(w.owner, app.InvitationRevokeRequest_builder{Ref: ref(v)}.Build())
		x.Equal(codes.FailedPrecondition, status.Code(err), "%v", err)
	})

	t.Run("run out", func(t *testing.T) {
		x := require.New(t)
		v, err := w.invite(w.owner, "kim3@acme.example", "member")
		x.NoError(err)
		code := w.box.code(t, "kim3@acme.example")
		later := time.Now().Add(31 * 24 * time.Hour)
		w.s.Deps.Now = func() time.Time { return later }
		t.Cleanup(func() { w.s.Deps.Now = nil })

		x.Empty(w.inbox(as, kim), "an invitation that ran out is still waiting")
		_, err = w.svc.Accept(as, app.InvitationAcceptRequest_builder{Ref: ref(v)}.Build())
		x.Equal(codes.FailedPrecondition, status.Code(err), "%v", err)
		_, err = w.svc.Claim(as, app.InvitationClaimRequest_builder{Code: code}.Build())
		x.Equal(codes.NotFound, status.Code(err), "%v", err)

		out, err := w.sent(w.owner)
		x.NoError(err)
		states := map[string]string{}
		for _, v := range out {
			states[v.GetEmail()] = v.GetState()
		}
		x.Equal("expired", states["kim3@acme.example"])
		x.Equal("denied", states["kim@acme.example"])
		x.Equal("revoked", states["kim2@acme.example"])
	})
}

// TestAnInvitationCanNameAPersonRecord: somebody this workspace already keeps
// a record of -- who has things on loan, say -- is invited as that record, and
// becomes it, history and all.
func TestAnInvitationCanNameAPersonRecord(t *testing.T) {
	w := newInvites(t, nil)
	x := w.x
	ctx := context.Background()

	p, err := w.s.Base.Party().Add(ctx, app.PartyAddRequest_builder{
		Tenant: app.TenantRef_builder{Id: w.tenant.GetId()}.Build(),
		Name:   "박", Kind: "person", Email: "park@acme.example",
	}.Build())
	x.NoError(err)

	v, err := w.svc.Add(w.owner, app.InvitationAddRequest_builder{
		Tenant: app.TenantRef_builder{Id: w.tenant.GetId()}.Build(),
		Party:  app.PartyRef_builder{Id: p.GetId()}.Build(),
	}.Build())
	x.NoError(err)
	x.Equal("park@acme.example", v.GetEmail(), "the record's address was not the one invited")

	park := w.account("park", []string{"park@acme.example"}, nil)
	_, err = w.svc.Accept(w.as(park), app.InvitationAcceptRequest_builder{Ref: ref(v)}.Build())
	x.NoError(err)

	me, err := app.NewPartyServiceClient(w.conn).Me(metadata.AppendToOutgoingContext(w.as(park), cmd.WorkspaceHeader, "acme"), &app.PartyMeRequest{})
	x.NoError(err)
	x.Equal(p.GetId(), me.GetParty().GetId(), "a second record was made for somebody the workspace had one of")
}

func TestAWorkspaceDoesNotMailTheWorld(t *testing.T) {
	w := newInvites(t, func(c *cmd.Config) { c.App.Invitations.Pending, c.App.Invitations.Daily = 2, 3 })
	x := w.x

	a, err := w.invite(w.owner, "a@acme.example", "member")
	x.NoError(err)
	_, err = w.invite(w.owner, "b@acme.example", "member")
	x.NoError(err)
	_, err = w.invite(w.owner, "c@acme.example", "member")
	x.Equal(codes.ResourceExhausted, status.Code(err), "more were waiting than a workspace may have out: %v", err)

	_, err = w.svc.Revoke(w.owner, app.InvitationRevokeRequest_builder{Ref: ref(a)}.Build())
	x.NoError(err)
	_, err = w.invite(w.owner, "c@acme.example", "member")
	x.NoError(err)

	_, err = w.svc.Revoke(w.owner, app.InvitationRevokeRequest_builder{Ref: app.InvitationRef_builder{Id: mustFirst(t, w).GetId()}.Build()}.Build())
	x.NoError(err)
	_, err = w.invite(w.owner, "d@acme.example", "member")
	x.Equal(codes.ResourceExhausted, status.Code(err), "more went out in a day than a workspace may send: %v", err)
}

func mustFirst(t *testing.T, w *invites) *app.Invitation {
	t.Helper()
	v, err := w.svc.List(w.owner, app.InvitationListRequest_builder{
		Filters: []*app.InvitationFilter{app.InvitationFilter_builder{
			Tenant: app.TenantRef_builder{Id: w.tenant.GetId()}.Build(),
			State:  z.Ptr("pending"),
		}.Build()},
	}.Build())
	require.NoError(t, err)
	require.NotEmpty(t, v.GetItems())
	return v.GetItems()[0]
}

// TestAnAccountAsksFromAPageAboutNoWorkspace: the inbox is a page with no
// workspace in its address, so its calls name none -- over HTTP, the way a
// browser makes them -- and are served as the account all the same.
func TestAnAccountAsksFromAPageAboutNoWorkspace(t *testing.T) {
	w := newInvites(t, nil)
	x := w.x

	_, err := w.invite(w.owner, "kim@acme.example", "member")
	x.NoError(err)
	kim := w.account("kim", []string{"kim@acme.example"}, nil)
	// In two workspaces already, so a call naming none is no membership.
	_, err = w.s.Workspaces(w.c).Create(context.Background(), kim, "하나")
	x.NoError(err)
	_, err = w.s.Workspaces(w.c).Create(context.Background(), kim, "둘")
	x.NoError(err)

	g, err := w.s.Grpc(context.Background(), w.c)
	x.NoError(err)
	h, err := web.New(config.HttpConfig{AllowWeb: true}, g)
	x.NoError(err)
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)

	_, c, err := w.s.Sessions.Mint(context.Background(), authsession.Session{Id: kim.Id.String(), Grant: frame.Whole()})
	x.NoError(err)
	call := func(method, body string) (int, string) {
		req, err := http.NewRequest(http.MethodPost, srv.URL+"/loft."+method, bytes.NewBufferString(body))
		x.NoError(err)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Connect-Protocol-Version", "1")
		req.Header.Set("Cookie", c.Name+"="+url.QueryEscape(c.Value))
		res, err := http.DefaultClient.Do(req)
		x.NoError(err)
		defer res.Body.Close()
		b, _ := io.ReadAll(res.Body)
		return res.StatusCode, string(b)
	}

	code, body := call("InvitationService/List", `{"filters":[{"account":"`+base64.StdEncoding.EncodeToString(kim.Id.Bytes())+`"}]}`)
	x.Equal(http.StatusOK, code, body)
	var got struct {
		Items []map[string]any `json:"items"`
	}
	x.NoError(json.Unmarshal([]byte(body), &got))
	x.Len(got.Items, 1)

	// Anything else from that page is still a membership's, and names one.
	code, body = call("PartyService/Me", `{}`)
	x.NotEqual(http.StatusOK, code, body)
}

// TestAMemberIsNotMadeAMemberTwice: an invitation to somebody already here is
// answered no, whatever role it says -- a role is changed by the people who run
// the workspace, and not by an invitation.
func TestAMemberIsNotMadeAMemberTwice(t *testing.T) {
	w := newInvites(t, nil)
	x := w.x

	kim := w.account("kim", []string{"kim@acme.example"}, nil)
	w.here(kim, "member")
	v, err := w.invite(w.owner, "kim@acme.example", "admin")
	x.NoError(err)
	_, err = w.svc.Accept(w.as(kim), app.InvitationAcceptRequest_builder{Ref: ref(v)}.Build())
	x.Equal(codes.FailedPrecondition, status.Code(err), "%v", err)
	me, err := app.NewPartyServiceClient(w.conn).Me(metadata.AppendToOutgoingContext(w.as(kim), cmd.WorkspaceHeader, "acme"), &app.PartyMeRequest{})
	x.NoError(err)
	x.Equal("member", me.GetRole())
}
