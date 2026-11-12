package cmd

import (
	"context"
	"fmt"

	"github.com/lesomnus/z"
	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/lesomnus/payday/auth"
	"github.com/lesomnus/payday/auth/authsession"
	"github.com/lesomnus/payday/frame"
	"github.com/lesomnus/payday/pdid"

	app "github.com/lesomnus/loft"
	"github.com/lesomnus/loft/internal/ent"
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
// What is the app's around that stays here: a call into a closed workspace is
// refused, whoever makes it and whatever it asks ([refusedClosed]) -- what
// becomes of a closed workspace is its operator's (D83).
func Resolver(s app.Server, db *ent.Client) auth.Resolver {
	accounts := pd.AccountResolver(db, WorkspaceHeader, signedIn, holders(s))
	return auth.ResolverFunc(func(ctx context.Context, id auth.Identity) (*frame.Frame, error) {
		f, err := accounts.Resolve(ctx, id)
		if err != nil {
			return nil, err
		}
		if f.AsAccount() {
			// The account itself, for a method of its own: in no workspace,
			// so in no closed one either.
			return f, nil
		}

		// A closed workspace is nobody's to use, its owner's neither (D29,
		// D83): restoring it and taking it out are the operator's, at the
		// entry point. Here, at the call that came in, so that nothing of it
		// is read for whoever asked.
		v, _ := f.Row.(*app.Holder)
		if v.GetTenant().HasDateClosed() {
			return nil, refusedClosed()
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

// ErrorClosed is the reason a call into a closed workspace is refused with
// (`errdetails.ErrorInfo`, domain `loft`): for a page to tell it from any
// other refusal, and draw what a closed workspace shows (D83).
const ErrorClosed = "CLOSED"

// refusedClosed is what a call into a closed workspace is answered: that it is
// closed, and who to ask about it -- nobody in it can do anything about it.
func refusedClosed() error {
	st := status.New(codes.FailedPrecondition, "닫힌 워크스페이스입니다. 다시 열거나 데이터를 받으려면 운영자에게 문의하세요")
	v, err := st.WithDetails(&errdetails.ErrorInfo{Reason: ErrorClosed, Domain: "loft"})
	if err != nil {
		return st.Err()
	}
	return v.Err()
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
