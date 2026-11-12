package cmd_test

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/lesomnus/payday/pdid"
	"github.com/lesomnus/z"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	app "github.com/lesomnus/loft"
	"github.com/lesomnus/loft/cmd"
	"github.com/lesomnus/loft/server/mail"
)

// Inviting an address into a workspace (design 9.11): what the people who run
// it send, read and take back. Nothing here answers one any more -- somebody
// comes to be a member by signing in as a holder of the tenant at roster (D85)
// -- and the rest goes with #332.

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

func (w *invites) invite(as context.Context, email, role string) (*app.Invitation, error) {
	return w.svc.Add(as, app.InvitationAddRequest_builder{
		Tenant: app.TenantRef_builder{Id: w.tenant.GetId()}.Build(),
		Email:  email,
		Role:   role,
	}.Build())
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

func TestAnInvitationIsSentAndSeenByWhoeverSentIt(t *testing.T) {
	w := newInvites(t, nil)
	x := w.x

	v, err := w.invite(w.owner, " Kim@Acme.Example ", "manager")
	x.NoError(err)
	x.Equal("kim@acme.example", v.GetEmail())
	x.Equal("pending", v.GetState())

	// The mail: who asked, into what.
	x.Len(w.box.sent, 1)
	m := w.box.sent[0]
	x.Equal("kim@acme.example", m.To)
	x.Contains(m.Body, "매니저로")

	// And the people who invited see it waiting.
	out, err := w.sent(w.owner)
	x.NoError(err)
	x.Len(out, 1)
	x.Equal("pending", out[0].GetState())
	x.Equal("owner", out[0].GetInviter().GetName())
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
	})

	t.Run("a list asks one question", func(t *testing.T) {
		x := require.New(t)
		_, err := w.svc.List(w.owner, app.InvitationListRequest_builder{}.Build())
		x.Equal(codes.InvalidArgument, status.Code(err), "%v", err)
		_, err = w.svc.List(w.owner, app.InvitationListRequest_builder{
			Filters: []*app.InvitationFilter{app.InvitationFilter_builder{
				Tenant: app.TenantRef_builder{Id: w.tenant.GetId()}.Build(),
				Ref:    app.InvitationRef_builder{Id: pdid.New(1).Bytes()}.Build(),
			}.Build()},
		}.Build())
		x.Equal(codes.InvalidArgument, status.Code(err), "%v", err)
	})
}

func TestAnInvitationIsTakenBackOnceAndRunsOut(t *testing.T) {
	w := newInvites(t, nil)

	t.Run("taken back", func(t *testing.T) {
		x := require.New(t)
		v, err := w.invite(w.owner, "kim2@acme.example", "member")
		x.NoError(err)
		r, err := w.svc.Revoke(w.owner, app.InvitationRevokeRequest_builder{Ref: ref(v)}.Build())
		x.NoError(err)
		x.Equal("revoked", r.GetState())
		_, err = w.svc.Revoke(w.owner, app.InvitationRevokeRequest_builder{Ref: ref(v)}.Build())
		x.Equal(codes.FailedPrecondition, status.Code(err), "%v", err)
	})

	t.Run("run out", func(t *testing.T) {
		x := require.New(t)
		_, err := w.invite(w.owner, "kim3@acme.example", "member")
		x.NoError(err)
		later := time.Now().Add(31 * 24 * time.Hour)
		w.s.Deps.Now = func() time.Time { return later }
		t.Cleanup(func() { w.s.Deps.Now = nil })

		out, err := w.sent(w.owner)
		x.NoError(err)
		states := map[string]string{}
		for _, v := range out {
			states[v.GetEmail()] = v.GetState()
		}
		x.Equal("expired", states["kim3@acme.example"])
		x.Equal("revoked", states["kim2@acme.example"])
	})
}

// TestAnInvitationCanNameAPersonRecord: somebody this workspace already keeps
// a record of -- who has things on loan, say -- is invited at that record's
// address, and a record that is somebody's membership already is nobody to
// invite.
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
	x.Equal(p.GetId(), v.GetParty().GetId())

	kim := w.holder("kim", "member")
	record, err := w.s.Base.Party().Add(ctx, app.PartyAddRequest_builder{
		Tenant: app.TenantRef_builder{Id: w.tenant.GetId()}.Build(),
		Name:   "김", Kind: "person", Email: "kim@acme.example",
		Holder: app.HolderRef_builder{Id: kim.GetId()}.Build(),
	}.Build())
	x.NoError(err)
	_, err = w.svc.Add(w.owner, app.InvitationAddRequest_builder{
		Tenant: app.TenantRef_builder{Id: w.tenant.GetId()}.Build(),
		Party:  app.PartyRef_builder{Id: record.GetId()}.Build(),
	}.Build())
	x.Equal(codes.FailedPrecondition, status.Code(err), "a member invited: %v", err)
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
