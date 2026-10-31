package cmd

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/lesomnus/otx/log"
	"github.com/lesomnus/z"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/lesomnus/payday/account"
	"github.com/lesomnus/payday/auth"
	"github.com/lesomnus/payday/auth/authsession"
	"github.com/lesomnus/payday/frame"
	"github.com/lesomnus/payday/pdid"

	app "github.com/lesomnus/loft"
	"github.com/lesomnus/loft/internal/ent"
	"github.com/lesomnus/loft/internal/identity"
	"github.com/lesomnus/loft/server/apphost"
	"github.com/lesomnus/loft/server/consent"
	"github.com/lesomnus/loft/server/pd"
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
// A session names an **account** and not a holder (D27), and which of its
// memberships a call is is payday's to work out (`pd.AccountResolver`,
// payday#184): the one of the workspace the call names in [WorkspaceHeader] --
// by alias or by identifier -- or its only one when the call names none; and
// for a method the schema marks as the account's own (`pd.AccountMethods`, the
// invitations addressed to it), the account itself, in no workspace. Every
// other credential names a holder, as before, and so does the membership a
// session's call was resolved to: both are read here ([holders]).
//
// What is the app's around that stays here. A session's account that has not
// agreed to what this deployment asks (`consents`, design 9.11) -- which
// roster's ledger says (D37) -- is refused every call first, with
// [ConsentOwed]: what it agrees to is HTTP beside the RPCs (`server/account`),
// and its first page. And a closed workspace is refused last, to all but its
// owner's [WhileClosed].
//
// A call that came to a workspace's own domain is pinned to that workspace
// ([apphost.From], loft#155), and served as a membership of it or not at all:
// a session is bound to it before it is resolved, so a [WorkspaceHeader] that
// names another is refused rather than re-pointed, and so is a call an
// account makes as itself across its workspaces; and a credential of another
// workspace's holder is refused.
//
// A session handed to a workspace's own domain (`server/session`'s handoff)
// is bound to that workspace, and is nobody but on a host that pins it: at
// the service's address, or on another workspace's domain, it is no
// credential at all. Its tenant is that workspace's, which payday holds the
// frame to besides ([auth.Identity.TenantId]).
func Resolver(s app.Server, db *ent.Client, consents *consent.Consents) auth.Resolver {
	accounts := pd.AccountResolver(db, WorkspaceHeader, signedIn, holders(s))
	return auth.ResolverFunc(func(ctx context.Context, id auth.Identity) (*frame.Frame, error) {
		pin, pinned := apphost.From(ctx)
		if signedIn(id) {
			if id.TenantId != "" && (!pinned || id.TenantId != pin.Workspace.String()) {
				return nil, fmt.Errorf("%w: this sign-in was handed to a workspace's own domain, and is good there alone", auth.ErrNoCredential)
			}
			if err := settled(ctx, consents, id.Id); err != nil {
				return nil, err
			}
			if pinned {
				if err := pinnedTo(ctx, pin); err != nil {
					return nil, err
				}
				// Bound to the domain's workspace, as a sign-in handed to
				// it is: served as the account's membership of it, and as
				// nothing an account does outside every workspace.
				id.TenantId = pin.Workspace.String()
			}
		}

		f, err := accounts.Resolve(ctx, id)
		if err != nil {
			return nil, err
		}
		if f.AsAccount() {
			// The account itself, for a method of its own: in no workspace,
			// so in no closed one either.
			return f, nil
		}

		// A closed workspace is nobody's to use, and its owner's to restore or
		// to read (D29). Here, at the call that came in, and not in the
		// policy: what the domain layer calls on the way to a restore goes
		// through the gate too, as the same owner.
		v, _ := f.Row.(*app.Holder)
		if v.GetTenant().HasDateClosed() {
			m, _ := grpc.Method(ctx)
			if v.GetRole() != "owner" || !WhileClosed[m] {
				return nil, status.Error(codes.FailedPrecondition, "닫힌 워크스페이스입니다")
			}
		}
		// Whatever the credential named, a pinned call is of its workspace.
		if pinned && f.Tenant != pin.Workspace {
			return nil, status.Errorf(codes.PermissionDenied, "this is @%s's address, and serves no other workspace", pin.Alias)
		}

		return f, nil
	})
}

// signedIn says whether a credential is a session, which names an account:
// what [pd.AccountResolver] serves as one of its memberships, or as itself.
func signedIn(id auth.Identity) bool { return id.Method == authsession.Method }

// holders resolves a credential that names a holder: every credential but a
// session, and the membership a session's call was resolved to, which
// [pd.AccountResolver] asks about by its identifier. The holder is read with
// its tenant, since almost every rule about a request is about the tenant it
// is from.
func holders(s app.Server) auth.Resolver {
	return auth.ResolverFunc(func(ctx context.Context, id auth.Identity) (*frame.Frame, error) {
		ref, err := refOf(id)
		if err != nil {
			return nil, err
		}

		v, err := s.Holder().Get(ctx, app.HolderGetRequest_builder{
			Ref: ref,
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

		actor, err := pdid.From(v.GetId())
		if err != nil {
			return nil, err
		}
		tenant, err := pdid.From(v.GetTenant().GetId())
		if err != nil {
			return nil, err
		}

		// The grant is not set here. The interceptor takes it from the
		// credential rather than from whatever a resolver felt like answering,
		// so a resolver cannot widen a token by forgetting.
		return frame.New(actor, tenant, frame.Grant{}).WithRow(v), nil
	})
}

// pinnedTo refuses a call to a workspace's own domain that names another
// workspace (loft#155): one named otherwise is refused, rather than served as
// the account's membership somewhere else, and none named is the domain's.
func pinnedTo(ctx context.Context, pin apphost.Pinned) error {
	in, err := account.SelectorOf(ctx, WorkspaceHeader)
	if err != nil {
		return err
	}
	if in.IsZero() || in.Id == pin.Workspace || (in.Alias != "" && in.Alias == pin.Alias) {
		return nil
	}

	return status.Errorf(codes.PermissionDenied, "this is @%s's address, and serves no other workspace: %s is at the service's", pin.Alias, in)
}

// WhileClosed is what a closed workspace's owner may still call: the
// workspace itself, to see it, and its restore. Taking its data out is HTTP
// (`server/account`).
var WhileClosed = map[string]bool{
	app.TenantService_Get_FullMethodName:     true,
	app.TenantService_Restore_FullMethodName: true,
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

// WorkspaceHeader is the metadata a call names its workspace in: an alias,
// with or without its `@`, or a tenant identifier, as payday reads it
// (`account.ParseSelector`). A page served at `/@<alias>/...` sends the alias
// on every call (design 9.11).
const WorkspaceHeader = "loft-workspace"

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
