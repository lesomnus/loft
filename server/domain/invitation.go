package domain

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	netmail "net/mail"
	"slices"
	"strings"
	"time"

	"github.com/lesomnus/otx/log"
	"github.com/lesomnus/payday/access"
	"github.com/lesomnus/payday/frame"
	"github.com/lesomnus/payday/gate"
	"github.com/lesomnus/payday/pdid"
	"github.com/lesomnus/z"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"uuid"

	app "github.com/lesomnus/rove"
	"github.com/lesomnus/rove/internal/ent"
	"github.com/lesomnus/rove/internal/ent/holder"
	"github.com/lesomnus/rove/internal/ent/invitation"
	entparty "github.com/lesomnus/rove/internal/ent/party"
	"github.com/lesomnus/rove/internal/ent/predicate"
	enttenant "github.com/lesomnus/rove/internal/ent/tenant"
	"github.com/lesomnus/rove/server/mail"
	"github.com/lesomnus/rove/server/pd"
	"github.com/lesomnus/rove/server/policy"
)

// The invitations of a workspace (design 9.11).
//
// # Two sides, and the wall serves one
//
// The people who run a workspace invite into it and see what they sent: that
// is the workspace's own rows, written as their membership, behind the wall
// like everything else (`Add`, `Revoke`). The account an invitation is
// addressed to is nobody's member there. It reads its invitations out of every
// workspace that sent one, and its answer writes into a workspace it cannot
// see -- so `List`, `Claim`, `Accept` and `Deny` are served as the account
// (`cmd.AccountMethods`), and what the wall would have kept is kept here, a
// rule at a time:
//
//   - an account reads the invitations addressed to it, and nothing else: the
//     ones it holds, and the ones sent to an address roster holds as checked
//     for it ([identity.Store.Addresses]) -- never to an address nobody
//     checked, which any directory's administrator can put on a user of their
//     own (nOAuth);
//   - a workspace's own invitations are read by somebody who runs it;
//   - an answer is written in the invitation's workspace as the deployment's
//     own work ([Deps.System]), and what it makes is what the invitation says.
//
// # What the people who invite learn
//
// What became of what they sent, and nothing about who was asked: whether an
// address belongs to an account here is not said by an invitation going out,
// nor by the mail, which is the same one either way.

// The states an invitation is in. One still pending after it expires is
// `expired`, which is worked out where it is read.
const (
	invitePending  = "pending"
	inviteAccepted = "accepted"
	inviteDenied   = "denied"
	inviteRevoked  = "revoked"
	inviteExpired  = "expired"
)

// InvitationRules is how long an invitation waits, and how many a workspace
// may have out.
type InvitationRules struct {
	// Ttl is how long one waits for an answer; zero is 30 days.
	Ttl time.Duration
	// Pending is how many may wait at once, and Daily how many may be sent in
	// a day: what keeps a workspace from mailing the world. Zero is the
	// default, 50 and 100; negative is no limit.
	Pending int
	Daily   int
}

func (r InvitationRules) ttl() time.Duration {
	if r.Ttl <= 0 {
		return 30 * 24 * time.Hour
	}
	return r.Ttl
}

func limitOf(v, otherwise int) int {
	if v == 0 {
		return otherwise
	}
	return v
}

type domainInvitation struct {
	Domain
	app.InvitationServiceServer
}

func (s Domain) Invitation() app.InvitationServiceServer {
	return domainInvitation{s, s.Next().Invitation()}
}

var roleWords = map[string]string{"owner": "소유자", "admin": "관리자", "manager": "매니저", "member": "구성원", "auditor": "감사자"}

// Add invites an address into the caller's workspace, as a role no wider than
// the caller's own, and mails it the way in.
func (s domainInvitation) Add(ctx context.Context, req *app.InvitationAddRequest) (*app.Invitation, error) {
	email, err := addressOf(req.GetEmail())
	if err != nil {
		if req.GetParty() == nil || req.GetEmail() != "" {
			return nil, err
		}
		email = ""
	}
	role := or(req.GetRole(), "member")
	if !slices.Contains(Roles, role) {
		return nil, invalid("role", "역할이 올바르지 않습니다 (%s)", strings.Join(Roles, ", "))
	}

	var out *app.Invitation
	var code, inviter, workspace string
	err = s.tx(ctx, func(t *Tx) error {
		me, err := t.db.Holder.Get(t.ctx, t.actor.Uuid())
		if err != nil {
			return err
		}
		// Nobody makes somebody wider than themselves.
		if policy.Levels[role] > policy.Levels[me.Role] {
			return refuse(codes.PermissionDenied, "role", "자기 역할보다 넓은 역할로는 초대할 수 없습니다")
		}

		var party *app.PartyRef
		if ref := req.GetParty(); ref != nil {
			p, err := t.next.Party().Get(t.ctx, app.PartyGetRequest_builder{Ref: ref}.Build())
			if err != nil {
				return err
			}
			if p.GetKind() != "person" {
				return invalid("party", "사람만 초대합니다")
			}
			if p.HasHolder() {
				return failed("%s은(는) 이미 이 워크스페이스의 구성원입니다", p.GetName())
			}
			if email == "" {
				if email, err = addressOf(p.GetEmail()); err != nil {
					return invalid("email", "%s의 이메일이 없습니다. 초대할 주소를 적어 주세요", p.GetName())
				}
			}
			party = app.PartyRef_builder{Id: p.GetId()}.Build()
		}

		waiting := t.db.Invitation.Query().Where(
			invitation.TenantId(t.tenant.Uuid()),
			invitation.State(invitePending),
			invitation.DateExpiresGT(t.now),
			invitation.DateErasedIsNil(),
		)
		if taken, err := waiting.Clone().Where(invitation.Email(email)).Exist(t.ctx); err != nil {
			return err
		} else if taken {
			return refuse(codes.AlreadyExists, "email", "이 주소로 보낸 초대가 아직 기다리고 있습니다")
		}
		rules := s.deps.Invitations
		if max := limitOf(rules.Pending, 50); max >= 0 {
			if n, err := waiting.Clone().Count(t.ctx); err != nil {
				return err
			} else if n >= max {
				return status.Errorf(codes.ResourceExhausted, "기다리는 초대가 %d개라 더 보낼 수 없습니다. 답이 없는 초대를 취소하거나 기다려 주세요", n)
			}
		}
		if max := limitOf(rules.Daily, 100); max >= 0 {
			if n, err := t.db.Invitation.Query().Where(
				invitation.TenantId(t.tenant.Uuid()),
				invitation.DateCreatedGT(t.now.Add(-24*time.Hour)),
			).Count(t.ctx); err != nil {
				return err
			} else if n >= max {
				return status.Errorf(codes.ResourceExhausted, "오늘은 초대를 %d개 보냈습니다. 내일 다시 보내 주세요", n)
			}
		}

		ws, err := t.db.Tenant.Get(t.ctx, t.tenant.Uuid())
		if err != nil {
			return err
		}
		workspace = or(ws.Name, ws.Alias)
		inviter = nameOf(t.ctx, t.db, me)

		code = secret()
		id := pdid.New(pd.InvitationDomain)
		if err := t.begin(nil, "invitation.add", id, t.now, "초대 ("+roleWords[role]+")", "", map[string]string{"role": role}); err != nil {
			return err
		}
		out, err = t.next.Invitation().Add(t.ctx, app.InvitationAddRequest_builder{
			Id:          id.Bytes(),
			Tenant:      t.tenantRef(),
			Inviter:     app.HolderRef_builder{Id: t.actor.Bytes()}.Build(),
			Email:       email,
			Role:        role,
			Party:       party,
			State:       invitePending,
			DateExpires: ts(t.now.Add(rules.ttl())),
			Claim:       digest(code),
		}.Build())
		return err
	})
	if err != nil {
		return nil, err
	}

	// After the invitation is kept: a mail that did not go is an invitation
	// that is still there, and still in the inbox of an account roster holds
	// the address of. It is said, and sent again by inviting again.
	if err := s.deps.mailInvitation(ctx, out, code, inviter, workspace); err != nil {
		log.From(ctx).WarnContext(ctx, "invitation: the mail did not go", "invitation", idOf(out.GetId()).String(), "err", err.Error())
	}
	return withoutCode(out), nil
}

// Revoke takes back an invitation nobody answered.
func (s domainInvitation) Revoke(ctx context.Context, req *app.InvitationRevokeRequest) (*app.Invitation, error) {
	var out *app.Invitation
	err := s.tx(ctx, func(t *Tx) error {
		v, err := t.next.Invitation().Get(t.ctx, app.InvitationGetRequest_builder{Ref: req.GetRef()}.Build())
		if err != nil {
			return err
		}
		if v.GetState() != invitePending {
			return failed("이미 답했거나 취소된 초대입니다")
		}
		if err := t.begin(nil, "invitation.revoke", idOf(v.GetId()), t.now, "초대 취소", "", nil); err != nil {
			return err
		}
		out, err = t.next.Invitation().Patch(t.ctx, app.InvitationPatchRequest_builder{
			Ref:              app.InvitationRef_builder{Id: v.GetId()}.Build(),
			State:            z.Ptr(inviteRevoked),
			DateSettled:      ts(t.now),
			ClaimNull:        z.Ptr(true),
			DateUpdatedForce: z.Ptr(true),
		}.Build())
		return err
	})
	if err != nil {
		return nil, err
	}
	return withoutCode(out), nil
}

// List answers one of two questions, and only one: the invitations a
// workspace sent (`tenant`), for somebody who runs it, or the invitations
// addressed to the caller (`account`, which has to be the caller's own).
//
// It is served as the account, so the wall narrows nothing here; each side's
// rule is written below, and anything else is refused rather than answered
// with everything.
func (s domainInvitation) List(ctx context.Context, req *app.InvitationListRequest) (*app.InvitationListResponse, error) {
	fs := req.GetFilters()
	if len(fs) != 1 || (fs[0].GetTenant() == nil) == (len(fs[0].GetAccount()) == 0) || fs[0].GetRef() != nil {
		return nil, invalid("filters", "초대는 워크스페이스(tenant)나 받는 계정(account) 하나로 찾습니다")
	}
	f := fs[0]
	me, err := accountOf(ctx)
	if err != nil {
		return nil, err
	}
	db := ent.NewClient(ent.Driver(s.drv))
	now := s.deps.now()
	limit := int(req.GetSize())
	if limit <= 0 || limit > 200 {
		limit = 200
	}

	var rows []*ent.Invitation
	if f.GetTenant() != nil {
		// A workspace's own, for somebody who runs it.
		ws, err := workspaceOf(ctx, db, f.GetTenant())
		if err != nil {
			return nil, err
		}
		m, err := db.Holder.Query().Where(holder.TenantId(ws), holder.Account(me.Uuid()), holder.DateErasedIsNil()).Only(ctx)
		if err != nil || policy.Levels[m.Role] < policy.Admin {
			return nil, status.Error(codes.PermissionDenied, "이 워크스페이스의 초대는 관리자가 봅니다")
		}
		q := db.Invitation.Query().Where(invitation.TenantId(ws), invitation.DateErasedIsNil())
		if f.HasState() {
			q = byState(q, f.GetState(), now)
		}
		if rows, err = q.Order(ent.Desc(invitation.FieldDateCreated)).Limit(limit).WithInviter().All(ctx); err != nil {
			return nil, err
		}
	} else {
		// The caller's own, from every workspace.
		if !bytes.Equal(f.GetAccount(), me.Bytes()) {
			return nil, status.Error(codes.PermissionDenied, "다른 계정의 초대는 볼 수 없습니다")
		}
		mine, err := s.addressed(ctx, me)
		if err != nil {
			return nil, err
		}
		q := db.Invitation.Query().Where(mine, invitation.DateErasedIsNil())
		state := invitePending
		if f.HasState() {
			state = f.GetState()
		}
		if rows, err = byState(q, state, now).Order(ent.Desc(invitation.FieldDateCreated)).Limit(limit).WithInviter().WithTenant().All(ctx); err != nil {
			return nil, err
		}
	}

	names := inviterNames(ctx, db, rows)
	out := app.InvitationListResponse_builder{}.Build()
	for _, v := range rows {
		out.SetItems(append(out.GetItems(), protoOf(v, now, names)))
	}
	return out, nil
}

// Claim makes the invitation whose mail carried this code the caller's: the
// proof is that the caller read that mailbox. It is how an account whose
// address roster holds no check of -- a Microsoft account with no `xms_edov` --
// is reached at all.
func (s domainInvitation) Claim(ctx context.Context, req *app.InvitationClaimRequest) (*app.Invitation, error) {
	me, err := accountOf(ctx)
	if err != nil {
		return nil, err
	}
	gone := status.Error(codes.NotFound, "이 초대 링크는 쓸 수 없습니다. 이미 쓰였거나, 취소되었거나, 기한이 지났습니다")
	code := strings.TrimSpace(req.GetCode())
	if code == "" {
		return nil, gone
	}
	db := ent.NewClient(ent.Driver(s.drv))
	now := s.deps.now()
	v, err := db.Invitation.Query().Where(invitation.Claim(digest(code)), invitation.DateErasedIsNil()).Only(ctx)
	if err != nil || v.State != invitePending || !now.Before(v.DateExpires) || (v.Account != nil && *v.Account != me.Uuid()) {
		// One answer for a code nobody sent, one spent, and somebody else's.
		return nil, gone
	}

	err = s.answer(ctx, v, pdid.Nil, func(t *Tx) error {
		if err := t.begin(nil, "invitation.claim", pdid.Id(v.Id), t.now, "초대 링크로 받음", "", nil); err != nil {
			return err
		}
		_, err := t.next.Invitation().Patch(t.ctx, app.InvitationPatchRequest_builder{
			Ref:              app.InvitationRef_builder{Id: pdid.Id(v.Id).Bytes()}.Build(),
			Account:          me.Bytes(),
			ClaimNull:        z.Ptr(true),
			DateUpdatedForce: z.Ptr(true),
		}.Build())
		return err
	})
	if err != nil {
		return nil, err
	}
	return s.mine(ctx, db, v.Id, now)
}

// Accept makes the caller a member of the invitation's workspace, as what it
// says, and the person record it names -- or a new one -- theirs.
func (s domainInvitation) Accept(ctx context.Context, req *app.InvitationAcceptRequest) (*app.Invitation, error) {
	me, err := accountOf(ctx)
	if err != nil {
		return nil, err
	}
	db := ent.NewClient(ent.Driver(s.drv))
	now := s.deps.now()
	v, err := s.toAnswer(ctx, db, me, req.GetRef(), now)
	if err != nil {
		return nil, err
	}
	if s.deps.People == nil {
		return nil, status.Error(codes.Unimplemented, "invitations cannot be answered on this deployment")
	}
	who, err := s.deps.People.ById(ctx, me.String())
	if err != nil {
		return nil, err
	}

	// As the membership it makes, so that what follows reads as theirs.
	hid := pdid.New(pd.HolderDomain)
	err = s.answer(ctx, v, hid, func(t *Tx) error {
		if in, err := t.db.Holder.Query().Where(holder.TenantId(v.TenantId), holder.Account(me.Uuid()), holder.DateErasedIsNil()).Exist(t.ctx); err != nil {
			return err
		} else if in {
			return failed("이미 이 워크스페이스의 구성원입니다")
		}

		if err := t.begin(nil, "invitation.accept", pdid.Id(v.Id), t.now, "초대 수락 ("+roleWords[v.Role]+")", "", map[string]string{"role": v.Role}); err != nil {
			return err
		}
		alias, err := freeAlias(t, v.TenantId, who.Alias)
		if err != nil {
			return err
		}
		name := or(who.Name, who.Alias)
		h, err := t.next.Holder().Add(t.ctx, app.HolderAddRequest_builder{
			Id:      hid.Bytes(),
			Tenant:  t.tenantRef(),
			Alias:   alias,
			Name:    name,
			Role:    z.Ptr(v.Role),
			Account: me.Bytes(),
		}.Build())
		if err != nil {
			return err
		}
		href := app.HolderRef_builder{Id: h.GetId()}.Build()

		// The person record: the one the invitation names, while nobody else
		// became it, or a new one.
		linked := false
		if v.PartyId != (uuid.UUID{}) {
			p, err := t.db.Party.Get(t.ctx, v.PartyId)
			if err != nil && !ent.IsNotFound(err) {
				return err
			}
			if err == nil && p.DateErased == nil && !hasHolder(t, p.Id) {
				if _, err := t.next.Party().Patch(t.ctx, app.PartyPatchRequest_builder{
					Ref:              app.PartyRef_builder{Id: pdid.Id(p.Id).Bytes()}.Build(),
					Holder:           href,
					DateUpdatedForce: z.Ptr(true),
				}.Build()); err != nil {
					return err
				}
				linked = true
			}
		}
		if !linked {
			if _, err := t.next.Party().Add(t.ctx, app.PartyAddRequest_builder{
				Tenant: t.tenantRef(),
				Name:   name,
				Kind:   "person",
				Email:  v.Email,
				Holder: href,
			}.Build()); err != nil {
				return err
			}
		}

		_, err = t.next.Invitation().Patch(t.ctx, app.InvitationPatchRequest_builder{
			Ref:              app.InvitationRef_builder{Id: pdid.Id(v.Id).Bytes()}.Build(),
			State:            z.Ptr(inviteAccepted),
			Account:          me.Bytes(),
			Holder:           href,
			DateSettled:      ts(t.now),
			ClaimNull:        z.Ptr(true),
			DateUpdatedForce: z.Ptr(true),
		}.Build())
		return err
	})
	if err != nil {
		return nil, err
	}
	return s.mine(ctx, db, v.Id, now)
}

// Deny says no. The people who invited see that it was answered so.
func (s domainInvitation) Deny(ctx context.Context, req *app.InvitationDenyRequest) (*app.Invitation, error) {
	me, err := accountOf(ctx)
	if err != nil {
		return nil, err
	}
	db := ent.NewClient(ent.Driver(s.drv))
	now := s.deps.now()
	v, err := s.toAnswer(ctx, db, me, req.GetRef(), now)
	if err != nil {
		return nil, err
	}
	err = s.answer(ctx, v, pdid.Nil, func(t *Tx) error {
		if err := t.begin(nil, "invitation.deny", pdid.Id(v.Id), t.now, "초대 거절", "", nil); err != nil {
			return err
		}
		_, err := t.next.Invitation().Patch(t.ctx, app.InvitationPatchRequest_builder{
			Ref:              app.InvitationRef_builder{Id: pdid.Id(v.Id).Bytes()}.Build(),
			State:            z.Ptr(inviteDenied),
			Account:          me.Bytes(),
			DateSettled:      ts(t.now),
			ClaimNull:        z.Ptr(true),
			DateUpdatedForce: z.Ptr(true),
		}.Build())
		return err
	})
	if err != nil {
		return nil, err
	}
	return s.mine(ctx, db, v.Id, now)
}

// toAnswer is an invitation the caller may answer: addressed to them, and
// still waiting. One that is not addressed to them is one there is not.
func (s domainInvitation) toAnswer(ctx context.Context, db *ent.Client, me pdid.Id, ref *app.InvitationRef, now time.Time) (*ent.Invitation, error) {
	nobody := status.Error(codes.NotFound, "그런 초대가 없습니다")
	id, err := pdid.From(ref.GetId())
	if err != nil {
		return nil, nobody
	}
	mine, err := s.addressed(ctx, me)
	if err != nil {
		return nil, err
	}
	v, err := db.Invitation.Query().Where(invitation.Id(id.Uuid()), mine, invitation.DateErasedIsNil()).Only(ctx)
	if err != nil {
		if ent.IsNotFound(err) {
			return nil, nobody
		}
		return nil, err
	}
	switch {
	case v.State != invitePending:
		return nil, failed("이미 답했거나 취소된 초대입니다")
	case !now.Before(v.DateExpires):
		return nil, failed("기한이 지난 초대입니다. 초대한 사람에게 다시 보내 달라고 해 주세요")
	}
	return v, nil
}

// addressed is the invitations addressed to an account: the ones it holds,
// and the ones nobody holds yet sent to an address roster holds as checked for
// it.
func (s domainInvitation) addressed(ctx context.Context, me pdid.Id) (predicate.Invitation, error) {
	held := invitation.Account(me.Uuid())
	if s.deps.People == nil {
		return held, nil
	}
	addrs, err := s.deps.People.Addresses(ctx, me)
	if err != nil {
		return nil, err
	}
	if len(addrs) == 0 {
		return held, nil
	}
	return invitation.Or(held, invitation.And(invitation.AccountIsNil(), invitation.EmailIn(addrs...))), nil
}

// answer runs `f` in the invitation's workspace as the deployment's own work,
// as `actor` there -- the membership an acceptance is about to make, or
// nobody: the account answering is nobody there yet, and the wall would show
// it nothing to write.
//
// Through the stack with neither the wall nor this layer, as a workspace is
// put up (`tenancy.Workspaces`): what `f` writes is written as it says, and a
// layer reading the account's frame would write it into no workspace at all.
func (s domainInvitation) answer(ctx context.Context, v *ent.Invitation, actor pdid.Id, f func(t *Tx) error) error {
	if s.deps.Base == nil {
		return status.Error(codes.Unimplemented, "invitations cannot be answered on this deployment")
	}
	tenant := pdid.Id(v.TenantId)
	as := frame.Into(ctx, frame.New(actor, tenant, frame.Whole()).WithScope(frame.Only(tenant)))
	return s.deps.System(as, s.deps.Base, s.drv, tenant, func(t *Tx) error {
		t.actor = actor
		return f(t)
	})
}

// mine is an invitation as the account it is addressed to is shown it.
func (s domainInvitation) mine(ctx context.Context, db *ent.Client, id uuid.UUID, now time.Time) (*app.Invitation, error) {
	v, err := db.Invitation.Query().Where(invitation.Id(id)).WithInviter().WithTenant().Only(ctx)
	if err != nil {
		return nil, err
	}
	return protoOf(v, now, inviterNames(ctx, db, []*ent.Invitation{v})), nil
}

// protoOf is an invitation as either side reads it: the workspace by its name
// and alias, who invited by their name, and never the code its mail carried.
func protoOf(v *ent.Invitation, now time.Time, names map[uuid.UUID]string) *app.Invitation {
	b := app.Invitation_builder{
		Id:          pdid.Id(v.Id).Bytes(),
		Email:       v.Email,
		Role:        v.Role,
		State:       stateOf(v, now),
		DateExpires: ts(v.DateExpires),
		DateSettled: tsp(v.DateSettled),
		DateCreated: ts(v.DateCreated),
		DateUpdated: ts(v.DateUpdated),
	}
	t := app.Tenant_builder{Id: pdid.Id(v.TenantId).Bytes()}
	if w := v.Edges.Tenant; w != nil {
		t.Alias, t.Name = w.Alias, w.Name
	}
	b.Tenant = t.Build()
	h := app.Holder_builder{Id: pdid.Id(v.InviterId).Bytes(), Name: names[v.InviterId]}
	if w := v.Edges.Inviter; w != nil && h.Name == "" {
		h.Name = or(w.Name, w.Alias)
	}
	b.Inviter = h.Build()
	if v.Account != nil {
		b.Account = pdid.Id(*v.Account).Bytes()
	}
	if v.PartyId != (uuid.UUID{}) {
		b.Party = app.Party_builder{Id: pdid.Id(v.PartyId).Bytes()}.Build()
	}
	if v.HolderId != (uuid.UUID{}) {
		b.Holder = app.Holder_builder{Id: pdid.Id(v.HolderId).Bytes()}.Build()
	}
	return b.Build()
}

// withoutCode is an invitation as written, without the code its mail carried.
func withoutCode(v *app.Invitation) *app.Invitation {
	if v == nil {
		return nil
	}
	v.SetClaim(nil)
	return v
}

func stateOf(v *ent.Invitation, now time.Time) string {
	if v.State == invitePending && !now.Before(v.DateExpires) {
		return inviteExpired
	}
	return v.State
}

func byState(q *ent.InvitationQuery, state string, now time.Time) *ent.InvitationQuery {
	switch state {
	case inviteExpired:
		return q.Where(invitation.State(invitePending), invitation.DateExpiresLTE(now))
	case invitePending:
		return q.Where(invitation.State(invitePending), invitation.DateExpiresGT(now))
	default:
		return q.Where(invitation.State(state))
	}
}

// nameOf is what somebody of the workspace is called: the person record their
// membership is, or the membership's own name.
//
// Either is a name no `personal` declaration sees in what the call answers or
// sends, so the call says it carried it (rove#136).
func nameOf(ctx context.Context, db *ent.Client, h *ent.Holder) string {
	if p, err := db.Party.Query().Where(entparty.HolderId(h.Id), entparty.DateErasedIsNil()).First(ctx); err == nil && p.Name != "" {
		access.Saw(ctx, access.Subject{Id: pdid.Id(p.Id), Tenant: pdid.Id(p.TenantId)})
		return p.Name
	}
	access.Saw(ctx, access.Subject{Id: pdid.Id(h.Id), Tenant: pdid.Id(h.TenantId)})
	return or(h.Name, h.Alias)
}

// inviterNames is what the people who sent these are called, by their
// membership: the person records they are.
func inviterNames(ctx context.Context, db *ent.Client, rows []*ent.Invitation) map[uuid.UUID]string {
	ids := make([]uuid.UUID, 0, len(rows))
	for _, v := range rows {
		ids = append(ids, v.InviterId)
	}
	out := map[uuid.UUID]string{}
	ps, err := db.Party.Query().Where(entparty.HolderIdIn(ids...), entparty.DateErasedIsNil()).All(ctx)
	if err != nil {
		return out
	}
	for _, p := range ps {
		if p.Name != "" {
			out[p.HolderId] = p.Name
			access.Saw(ctx, access.Subject{Id: pdid.Id(p.Id), Tenant: pdid.Id(p.TenantId)})
		}
	}
	return out
}

// workspaceOf is the workspace a reference names, by identifier or alias.
func workspaceOf(ctx context.Context, db *ent.Client, ref *app.TenantRef) (uuid.UUID, error) {
	if id, err := pdid.From(ref.GetId()); err == nil {
		return id.Uuid(), nil
	}
	if a := ref.GetAlias(); a != "" {
		v, err := db.Tenant.Query().Where(enttenant.Alias(a)).Only(ctx)
		if err == nil {
			return v.Id, nil
		}
	}
	return uuid.UUID{}, status.Error(codes.PermissionDenied, "이 워크스페이스의 초대는 관리자가 봅니다")
}

// accountOf is the account a call is from: the frame's own for a call served
// as an account, and the membership's account for one served as a member.
func accountOf(ctx context.Context) (pdid.Id, error) {
	fr, err := gate.Actor(ctx)
	if err != nil {
		return pdid.Nil, err
	}
	switch v := fr.Row.(type) {
	case *app.Account:
		return pdid.From(v.GetId())
	case *app.Holder:
		if len(v.GetAccount()) > 0 {
			return pdid.From(v.GetAccount())
		}
	}
	return pdid.Nil, status.Error(codes.PermissionDenied, "계정으로 로그인한 사람만 초대를 받습니다")
}

// freeAlias is what a new membership is called in a workspace: the account's
// own alias, or that with a number when the workspace already has one by it.
func freeAlias(t *Tx, tenant uuid.UUID, want string) (string, error) {
	for i := 0; i < 20; i++ {
		alias := want
		if i > 0 {
			alias = fmt.Sprintf("%s-%d", want, i+1)
		}
		taken, err := t.db.Holder.Query().Where(holder.TenantId(tenant), holder.Alias(alias), holder.DateErasedIsNil()).Exist(t.ctx)
		if err != nil {
			return "", err
		}
		if !taken {
			return alias, nil
		}
	}
	return "", failed("이 워크스페이스에 %s(으)로 시작하는 아이디가 너무 많습니다", want)
}

func hasHolder(t *Tx, party uuid.UUID) bool {
	n, err := t.db.Party.Query().Where(entparty.Id(party), entparty.HasHolder()).Exist(t.ctx)
	return err != nil || n
}

// addressOf is an address as one is compared: trimmed and folded.
func addressOf(v string) (string, error) {
	v = strings.ToLower(strings.TrimSpace(v))
	a, err := netmail.ParseAddress(v)
	if err != nil || a.Address != v || !strings.Contains(v, "@") {
		return "", invalid("email", "이메일 주소가 올바르지 않습니다")
	}
	return v, nil
}

func digest(code string) []byte {
	v := sha256.Sum256([]byte(code))
	return v[:]
}

// secret is a code a mail carries: 24 random bytes.
func secret() string {
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return base64.RawURLEncoding.EncodeToString(b)
}

// mailInvitation writes to the address an invitation went to: who asked, into
// what, and the link that brings an account to its inbox -- signing up first,
// for an address that is no account's yet. One mail either way, so that what
// is sent says nothing about who the address belongs to.
func (d *Deps) mailInvitation(ctx context.Context, v *app.Invitation, code, inviter, workspace string) error {
	if d.Mail == nil {
		return nil
	}
	link := strings.TrimSuffix(d.AppUrl, "/") + "/invitations?code=" + code
	role := roleWords[v.GetRole()]
	until := v.GetDateExpires().AsTime().In(Zone).Format("2006년 1월 2일")
	body := fmt.Sprintf(`%s님이 Rove의 %s 워크스페이스에 %s%s 초대했습니다.

아래 링크를 열고 로그인하면 초대함에서 수락하거나 거절할 수 있습니다.
Rove 계정이 아직 없다면 같은 화면에서 Google, Microsoft, GitHub 계정으로 가입할 수 있습니다.

%s

이 초대는 %s까지 유효합니다. 모르는 초대라면 이 메일을 무시해 주세요.
`, inviter, workspace, role, ro(role), link, until)

	return d.Mail.Send(ctx, mail.Message{
		To:      v.GetEmail(),
		Subject: fmt.Sprintf("%s님이 Rove의 %s에 초대했습니다", inviter, workspace),
		Body:    body,
	})
}

// ro is the particle a word takes for "as": 으로 after a final consonant other
// than ㄹ, and 로 otherwise.
func ro(word string) string {
	r := []rune(word)
	if len(r) == 0 {
		return "로"
	}
	last := r[len(r)-1]
	if last < 0xAC00 || last > 0xD7A3 {
		return "(으)로"
	}
	final := (last - 0xAC00) % 28
	if final == 0 || final == 8 {
		return "로"
	}
	return "으로"
}
