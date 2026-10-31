package cmd

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"github.com/lesomnus/otx/log"
	"github.com/lesomnus/z"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	"github.com/lesomnus/payday/auth"
	"github.com/lesomnus/payday/auth/authsession"
	"github.com/lesomnus/payday/frame"
	"github.com/lesomnus/payday/gate"
	"github.com/lesomnus/payday/pdid"

	app "github.com/lesomnus/loft"
	"github.com/lesomnus/loft/internal/ent"
	"github.com/lesomnus/loft/internal/ent/holder"
	"github.com/lesomnus/loft/internal/ent/tenant"
	"github.com/lesomnus/loft/internal/identity"
	"github.com/lesomnus/loft/server/apphost"
	"github.com/lesomnus/loft/server/consent"
)

// Resolver turns a claim into the frame a request is served in.
//
// It is the app's and not payday's, and this is the whole of why: looking
// somebody up is a query against servers generated from this app's schema, and
// payday has no name for them. What payday supplies is the interface and the
// interceptor that uses it -- and the rule that what comes back is a row read
// from the database rather than one the caller described.
//
// It reads through the server the wall was never installed on. Working out who
// is calling happens **before** there is anybody to be walled by, so a resolver
// behind the wall would be asking a question whose answer it needs in order to
// ask it.
//
// A session names an **account** and not a holder (D27). The holder a call is
// served as is the account's membership of the workspace the call names in
// [WorkspaceHeader] -- by alias or by identifier -- or its only membership when
// the call names none. Every other credential names a holder, as before.
//
// A session's account that has not agreed to what this deployment asks
// (`consents`, design 9.11) -- which roster's ledger says (D37) -- is refused
// every call, with [ConsentOwed]: what it agrees to is HTTP beside the RPCs
// (`server/account`), and its first page.
//
// A call that came to a workspace's own domain is pinned to that workspace
// ([apphost.From], loft#155), and served as a membership of it or not at all:
// a [WorkspaceHeader] that names another is refused rather than re-pointed,
// a credential of another workspace's holder is refused, and so is a call an
// account makes as itself across its workspaces ([AccountMethods]).
//
// A session handed to a workspace's own domain (`server/session`'s handoff)
// is bound to that workspace, and is nobody but on a host that pins it: at
// the service's address, or on another workspace's domain, it is no
// credential at all. Its tenant is that workspace's, which payday holds the
// frame to besides ([auth.Identity.TenantId]).
func Resolver(s app.Server, db *ent.Client, consents *consent.Consents) auth.Resolver {
	return auth.ResolverFunc(func(ctx context.Context, id auth.Identity) (*frame.Frame, error) {
		pin, pinned := apphost.From(ctx)
		var ref *app.HolderRef
		var err error
		if id.Method == authsession.Method {
			if id.TenantId != "" && (!pinned || id.TenantId != pin.Workspace.String()) {
				return nil, fmt.Errorf("%w: this sign-in was handed to a workspace's own domain, and is good there alone", auth.ErrNoCredential)
			}
			if err := settled(ctx, consents, id.Id); err != nil {
				return nil, err
			}
			if m, ok := grpc.Method(ctx); ok && AccountMethods[m] {
				if pinned {
					return nil, status.Errorf(codes.PermissionDenied, "this is @%s's address: what an account does across its workspaces is done at the service's", pin.Alias)
				}
				return asAccount(id.Id)
			}
			ref, err = membership(ctx, db, id.Id)
		} else {
			ref, err = refOf(id)
		}
		if err != nil {
			return nil, err
		}

		v, err := s.Holder().Get(ctx, app.HolderGetRequest_builder{
			Ref: ref,
			// The tenant travels with the actor, since almost every rule about
			// a request is about the tenant it is from.
			Select: app.HolderSelect_builder{
				All:    z.Ptr(true),
				Tenant: app.TenantSelect_builder{All: z.Ptr(true)}.Build(),
			}.Build(),
		}.Build())
		if err != nil {
			if status.Code(err) == codes.NotFound {
				// A credential that names nobody who is here is a **bad**
				// credential and not a missing one, which is what
				// `auth.Resolver` asks for -- and the difference is what a
				// client is told to do about it. NotFound is a client that
				// retries the same call and keeps failing; Unauthenticated is
				// one that goes and authenticates again, which is right,
				// because a session held by somebody who has since been erased
				// is exactly this case.
				return nil, fmt.Errorf("%w: %s", auth.ErrNoCredential, err)
			}

			return nil, err
		}

		// A closed workspace is nobody's to use, and its owner's to restore or
		// to read (D29). Here, at the call that came in, and not in the
		// policy: what the domain layer calls on the way to a restore goes
		// through the gate too, as the same owner.
		if v.GetTenant().HasDateClosed() {
			m, _ := grpc.Method(ctx)
			if v.GetRole() != "owner" || !WhileClosed[m] {
				return nil, status.Error(codes.FailedPrecondition, "닫힌 워크스페이스입니다")
			}
		}

		actor, err := pdid.From(v.GetId())
		if err != nil {
			return nil, err
		}
		tenant, err := pdid.From(v.GetTenant().GetId())
		if err != nil {
			return nil, err
		}
		// Whatever the credential named, a pinned call is of its workspace.
		if pinned && tenant != pin.Workspace {
			return nil, status.Errorf(codes.PermissionDenied, "this is @%s's address, and serves no other workspace", pin.Alias)
		}

		// The grant is not set here. The interceptor takes it from the
		// credential rather than from whatever a resolver felt like answering,
		// so a resolver cannot widen a token by forgetting.
		return frame.New(actor, tenant, frame.Grant{}).WithRow(v), nil
	})
}

// WhileClosed is what a closed workspace's owner may still call: the
// workspace itself, to see it, and its restore. Taking its data out is HTTP
// (`server/account`).
var WhileClosed = map[string]bool{
	app.TenantService_Get_FullMethodName:     true,
	app.TenantService_Restore_FullMethodName: true,
}

// AccountMethods are the calls a session makes as its account and not as a
// membership: what an account asks before it is anybody's member, about
// invitations addressed to it in workspaces it is not in (design 9.11).
//
// They are the one exception to every call being served as a membership of
// one workspace, and the wall cannot serve them: the rows are other
// workspaces'. So the frame sees no tenant at all, the policy gives such a
// caller these methods and nothing else (`policy.Account`), and the domain
// layer holds each to the account's own invitations
// (`server/domain/invitation.go`).
var AccountMethods = map[string]bool{
	app.InvitationService_List_FullMethodName:   true,
	app.InvitationService_Claim_FullMethodName:  true,
	app.InvitationService_Accept_FullMethodName: true,
	app.InvitationService_Deny_FullMethodName:   true,
}

// ConsentOwed is what a call is refused with while its account has not agreed
// to what this deployment asks. A page that is told it reads `loft consent`
// in it and loads again, which is the agreement; a terminal is told to run it.
const ConsentOwed = "이용약관과 개인정보 처리방침에 동의해야 이용할 수 있습니다 (loft consent)"

// settled refuses a session whose account has not agreed to what this
// deployment asks, and one whose agreement roster could not be asked about:
// not owed, but not known to be settled either -- as a session is refused
// while roster cannot say what its account is (`session.Store`).
func settled(ctx context.Context, consents *consent.Consents, account string) error {
	if !consents.Asks() {
		return nil
	}
	a, err := pdid.Parse(account)
	if err != nil {
		return fmt.Errorf("%w: %s", auth.ErrNoCredential, err)
	}
	ok, err := consents.Settled(ctx, a)
	switch {
	case errors.Is(err, identity.ErrNoPerson):
		return fmt.Errorf("%w: %w", auth.ErrNoCredential, err)
	case err != nil:
		// What roster said stays here: it is about this deployment's
		// standing at roster, and not the caller's to read.
		log.From(ctx).WarnContext(ctx, "consent: what an account agreed to could not be asked", slog.Any("err", err))
		return status.Error(codes.Unavailable, "cannot say just now whether this account agreed to the terms")
	case !ok:
		return status.Error(codes.FailedPrecondition, ConsentOwed)
	}
	return nil
}

// asAccount is the frame of a call served as the account: the account as the
// actor, no tenant, and the account where a membership's row would be.
func asAccount(account string) (*frame.Frame, error) {
	a, err := pdid.Parse(account)
	if err != nil {
		return nil, fmt.Errorf("%w: %s", auth.ErrNoCredential, err)
	}
	return frame.New(a, pdid.Nil, frame.Grant{}).WithRow(app.Account_builder{Id: a.Bytes()}.Build()), nil
}

// LimitKey is what a call is counted against: its workspace, or -- for a call
// served as an account, which is in none -- that account, so that one account
// asking often is not every account's limit.
func LimitKey() func(ctx context.Context, method string) string {
	byTenant := gate.ByTenant()
	return func(ctx context.Context, method string) string {
		if f, ok := frame.From(ctx); ok && f.Tenant.IsZero() && !f.Actor.IsZero() {
			return "account:" + f.Actor.String()
		}
		return byTenant(ctx, method)
	}
}

// WorkspaceHeader is the metadata a call names its workspace in: an alias,
// with or without its `@`, or a tenant identifier. A page served at
// `/@<alias>/...` sends the alias on every call (design 9.11).
const WorkspaceHeader = "loft-workspace"

// membership is the holder an account is in the workspace a call names, or in
// its only one.
//
// Not being in the workspace named is one answer whether or not there is such
// a workspace, so a name says nothing about who else is here.
func membership(ctx context.Context, db *ent.Client, account string) (*app.HolderRef, error) {
	a, err := pdid.Parse(account)
	if err != nil {
		return nil, fmt.Errorf("%w: %s", auth.ErrNoCredential, err)
	}
	q := db.Holder.Query().Where(holder.Account(a.Uuid()), holder.DateErasedIsNil())
	ws := ""
	if md, ok := metadata.FromIncomingContext(ctx); ok {
		if vs := md.Get(WorkspaceHeader); len(vs) > 0 {
			ws = strings.TrimPrefix(strings.TrimSpace(vs[0]), "@")
		}
	}
	// On a workspace's own domain the workspace is the domain's (loft#155):
	// one named otherwise is refused, and none named is that one.
	named := ws
	if pin, ok := apphost.From(ctx); ok {
		if ws != "" && !pin.Names(ws) {
			return nil, status.Errorf(codes.PermissionDenied, "this is @%s's address, and serves no other workspace: @%s is at the service's", pin.Alias, ws)
		}
		ws, named = pin.Workspace.String(), pin.Alias
	}
	if ws != "" {
		if t, err := pdid.Parse(ws); err == nil {
			q = q.Where(holder.TenantId(t.Uuid()))
		} else {
			q = q.Where(holder.HasTenantWith(tenant.Alias(ws)))
		}
	}
	hs, err := q.Limit(2).Ids(ctx)
	if err != nil {
		return nil, err
	}
	switch {
	case len(hs) == 1:
		return app.HolderRef_builder{Id: pdid.Id(hs[0]).Bytes()}.Build(), nil
	case ws != "":
		return nil, status.Errorf(codes.PermissionDenied, "this account is not in @%s", named)
	case len(hs) == 0:
		return nil, status.Error(codes.FailedPrecondition, "this account is in no workspace")
	default:
		return nil, status.Errorf(codes.InvalidArgument, "this account is in more than one workspace: name one, in %s", WorkspaceHeader)
	}
}

// refOf is the claim as a reference: an identifier when the credential named
// one, and otherwise the pair that names a holder inside a tenant.
func refOf(id auth.Identity) (*app.HolderRef, error) {
	if id.Id != "" {
		k, err := pdid.Parse(id.Id)
		if err != nil {
			return nil, fmt.Errorf("%w: %s", auth.ErrNoCredential, err)
		}

		return app.HolderRef_builder{Id: k.Bytes()}.Build(), nil
	}
	if id.Tenant == "" || id.Alias == "" {
		return nil, fmt.Errorf("names nobody: %w", auth.ErrNoCredential)
	}

	return app.HolderRef_builder{
		Slug: app.HolderRefBySlug_builder{
			Alias:  z.Ptr(id.Alias),
			Tenant: app.TenantRef_builder{Alias: z.Ptr(id.Tenant)}.Build(),
		}.Build(),
	}.Build(), nil
}
