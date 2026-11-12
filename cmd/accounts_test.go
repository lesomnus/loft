package cmd_test

import (
	"context"
	"slices"
	"sort"
	"strings"
	"testing"

	"github.com/lesomnus/payday/frame"
	"github.com/lesomnus/payday/gate"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	app "github.com/lesomnus/loft"
	"github.com/lesomnus/loft/cmd"
	"github.com/lesomnus/loft/server/pd"
	"github.com/lesomnus/loft/server/policy"
)

// An account's own methods (payday#184): what a session calls as the account
// itself, in no workspace -- the invitations addressed to it, and what it is
// in -- and what keeps that apart from everything a membership calls.

// TestAnAccountReachesItsOwnMethodsAndNoOthers: a call framed as the account
// itself -- what a session's call to a method the schema marks is resolved to
// -- is refused every other method this deployment serves before any policy
// is asked, by the gate's interceptor, which asks `gate.Decide`. It sees no
// workspace. And the other way round: the table gives a membership none of
// the account's own, whatever its role.
func TestAnAccountReachesItsOwnMethodsAndNoOthers(t *testing.T) {
	w := newWorld(t)
	x := w.x
	ctx := context.Background()

	g, err := w.s.Grpc(ctx, w.c)
	x.NoError(err)
	var methods []string
	for svc, info := range g.GetServiceInfo() {
		if strings.HasPrefix(svc, "grpc.") {
			continue
		}
		for _, m := range info.Methods {
			methods = append(methods, "/"+svc+"/"+m.Name)
		}
	}
	sort.Strings(methods)
	x.Greater(len(methods), 200, "every method the stack serves")

	a := w.accountOf(w.holder("kim", "member"))
	var own []string
	for _, m := range methods {
		in := frame.Into(ctx, frame.OfAccount(a, frame.Whole(), pd.AccountMethod))
		got, err := gate.Decide(in, policy.Policy{}, m)
		if !pd.AccountMethod(m) {
			x.Equal(codes.PermissionDenied, status.Code(err), "%s, called as the account itself: %v", m, err)
			continue
		}
		own = append(own, m)
		x.NoError(err, m)
		f := frame.Must(got)
		x.True(f.AsAccount(), m)
		x.Equal(a, f.Account, m)
		x.True(f.Tenant.IsZero(), "%s: the account itself is in no workspace", m)
		x.False(f.Scope.All(), m)
		x.Empty(f.Scope.Ids(), "%s: and sees none", m)
	}
	x.ElementsMatch(pd.AccountMethods, own, "every one of the account's own is served")
	x.ElementsMatch([]string{
		app.InvitationService_Inbox_FullMethodName,
		app.InvitationService_Claim_FullMethodName,
		app.InvitationService_Accept_FullMethodName,
		app.InvitationService_Deny_FullMethodName,
		// What it is in, and a new one (D73).
		app.TenantService_Mine_FullMethodName,
		app.TenantService_Create_FullMethodName,
	}, pd.AccountMethods, "the invitations addressed to it, what it is in, and nothing else")

	// No membership is given them, whatever its role -- and the table gives
	// the account nothing else.
	for _, role := range []string{"auditor", "member", "manager", "admin", "owner"} {
		for _, m := range pd.AccountMethods {
			err := policy.Policy{}.May(ctx, gate.Call{Action: m, Row: app.Holder_builder{Role: role}.Build(), Account: a})
			x.Equal(codes.PermissionDenied, status.Code(err), "%s, as a membership with the role %s: %v", m, role, err)
		}
	}
	for _, m := range methods {
		err := policy.Policy{}.May(ctx, gate.Call{Action: m, Actor: a, Account: a, AsAccount: true})
		if slices.Contains(pd.AccountMethods, m) {
			x.NoError(err, m)
		} else if !strings.HasPrefix(m, "/grpc.") {
			x.Equal(codes.PermissionDenied, status.Code(err), "%s, by the table, as the account itself: %v", m, err)
		}
	}
}

// TestAnAccountAnswersAsItselfWhicheverWorkspaceItsCallNames: an answer to an
// invitation is the account's own (D30), so a call that names a workspace the
// account is already in answers one from another workspace as the account,
// and the account is then a member of the workspace that asked.
func TestAnAccountAnswersAsItselfWhicheverWorkspaceItsCallNames(t *testing.T) {
	w := newInvites(t, nil)
	x := w.x
	ctx := context.Background()

	beta, err := w.s.Base.Tenant().Add(ctx, app.TenantAddRequest_builder{Alias: "beta"}.Build())
	x.NoError(err)
	kim := w.account("kim", []string{"kim@acme.example"}, nil)
	w.member(beta, kim, "owner")
	v, err := w.invite(w.owner, "kim@acme.example", "member")
	x.NoError(err)

	inBeta := metadata.AppendToOutgoingContext(w.as(kim), cmd.WorkspaceHeader, "beta")
	got, err := w.svc.Accept(inBeta, app.InvitationAcceptRequest_builder{Ref: ref(v)}.Build())
	x.NoError(err, "the account's own, whatever workspace the call names")
	x.Equal("accepted", got.GetState())
	me, err := app.NewPartyServiceClient(w.conn).Me(metadata.AppendToOutgoingContext(w.as(kim), cmd.WorkspaceHeader, "acme"), &app.PartyMeRequest{})
	x.NoError(err)
	x.Equal("member", me.GetRole())
}

// TestAWorkspaceTheAccountIsNotInIsRefused: a session's call is served as the
// account's membership of the workspace it names, or not at all -- never
// re-pointed at one of its others -- and a workspace it is not in is one
// answer whether or not there is such a workspace, so the header finds out
// nothing about which there are. What names no workspace at all, or two, is a
// bad request. The account's own methods are the account's whatever the call
// names.
func TestAWorkspaceTheAccountIsNotInIsRefused(t *testing.T) {
	w := newWorld(t)
	x := w.x
	ctx := context.Background()
	me := app.NewPartyServiceClient(w.conn)

	beta, err := w.s.Base.Tenant().Add(ctx, app.TenantAddRequest_builder{Alias: "beta"}.Build())
	x.NoError(err)
	md := w.session(w.holder("kim", "member"))
	at := func(names ...string) context.Context {
		kv := []string{}
		for _, v := range names {
			kv = append(kv, cmd.WorkspaceHeader, v)
		}
		return metadata.AppendToOutgoingContext(md, kv...)
	}

	for _, named := range []context.Context{md, at("acme"), at("@acme"), at(idString(w.tenant.GetId()))} {
		v, err := me.Me(named, &app.PartyMeRequest{})
		x.NoError(err, "its only workspace, named or not")
		x.Equal("acme", v.GetTenant().GetAlias())
	}

	_, there := me.Me(at("beta"), &app.PartyMeRequest{})
	x.Equal(codes.PermissionDenied, status.Code(there), "a workspace it is not in: %v", there)
	_, nowhere := me.Me(at("gamma"), &app.PartyMeRequest{})
	x.Equal(codes.PermissionDenied, status.Code(nowhere), "a workspace there is not: %v", nowhere)
	x.Equal(strings.ReplaceAll(status.Convert(there).Message(), "beta", "gamma"), status.Convert(nowhere).Message(),
		"one answer, whether or not there is such a workspace")
	_, err = me.Me(at(idString(beta.GetId())), &app.PartyMeRequest{})
	x.Equal(codes.PermissionDenied, status.Code(err), "by its identifier: %v", err)

	_, err = me.Me(at("acme", "beta"), &app.PartyMeRequest{})
	x.Equal(codes.InvalidArgument, status.Code(err), "two named: %v", err)
	_, err = me.Me(at("Not a name!"), &app.PartyMeRequest{})
	x.Equal(codes.InvalidArgument, status.Code(err), "nothing a workspace could be called: %v", err)

	// The account's own, named anything at all.
	invitations := app.NewInvitationServiceClient(w.conn)
	for _, named := range []context.Context{md, at("beta"), at("gamma"), at("acme", "beta")} {
		_, err := invitations.Inbox(named, app.InvitationInboxRequest_builder{}.Build())
		x.NoError(err, "the account's own inbox")
	}
}
