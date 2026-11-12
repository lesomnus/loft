package domain

import (
	"context"
	"fmt"
	"log/slog"
	netmail "net/mail"
	"slices"
	"strings"
	"time"

	"github.com/lesomnus/otx/log"
	"github.com/lesomnus/payday/access"
	"github.com/lesomnus/payday/gate"
	"github.com/lesomnus/payday/pdid"
	"github.com/lesomnus/z"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"uuid"

	app "github.com/lesomnus/loft"
	"github.com/lesomnus/loft/internal/ent"
	"github.com/lesomnus/loft/internal/ent/invitation"
	entparty "github.com/lesomnus/loft/internal/ent/party"
	enttenant "github.com/lesomnus/loft/internal/ent/tenant"
	"github.com/lesomnus/loft/server/mail"
	"github.com/lesomnus/loft/server/pd"
	"github.com/lesomnus/loft/server/policy"
	"github.com/lesomnus/loft/server/tenancy"
)

// The invitations of a workspace (design 9.11).
//
// The people who run a workspace invite into it and see what they sent: the
// workspace's own rows, written and read as their membership, behind the wall
// like everything else (`Add`, `List`, `Revoke`). Answering one is nobody's
// call here any more (D85): who is a member is who roster has in the tenant
// this deployment serves, made here the first time they sign in, and what
// takes the place of an invitation is #332's.
//
// # What the people who invite learn
//
// What became of what they sent, and nothing about who was asked: whether an
// address belongs to somebody here is not said by an invitation going out,
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
	var inviter, workspace string
	zone := DefaultZone
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

		// What the mail calls the workspace: what it is called, which for one
		// with no name of its own is after its owner (D57), put in a mail.
		called, err := t.workspaceName()
		if err != nil {
			return err
		}
		workspace = called.Said
		access.Saw(t.ctx, called.Saw...)
		inviter = nameOf(t.ctx, t.db, me)
		zone = t.zone()

		id := pdid.New(pd.InvitationDomain)
		said := app.InvitationAdded_builder{Invitation: id.Bytes(), Role: role}.Build()
		if party != nil {
			said.SetParty(party.GetId())
		}
		if err := t.begin(nil, "invitation.add", id, t.now, "", said); err != nil {
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
		}.Build())
		return err
	})
	if err != nil {
		return nil, err
	}

	// After the invitation is kept: a mail that did not go is an invitation
	// that is still there. It is said, and sent again by inviting again.
	if err := s.deps.mailInvitation(ctx, out, inviter, workspace, zone); err != nil {
		log.From(ctx).WarnContext(ctx, "invitation: the mail did not go", "invitation", idOf(out.GetId()).String(), slog.Any("err", err))
	}
	return out, nil
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
		if err := t.begin(nil, "invitation.revoke", idOf(v.GetId()), t.now, "", app.InvitationChanged_builder{Invitation: v.GetId()}.Build()); err != nil {
			return err
		}
		out, err = t.next.Invitation().Patch(t.ctx, app.InvitationPatchRequest_builder{
			Ref:              app.InvitationRef_builder{Id: v.GetId()}.Build(),
			State:            z.Ptr(inviteRevoked),
			DateSettled:      ts(t.now),
			DateUpdatedForce: z.Ptr(true),
		}.Build())
		return err
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// List is the invitations a workspace sent, for somebody who runs it: the
// caller's own workspace, named (`tenant`), which is the one question it
// answers.
//
// It is served as the caller's membership, which the policy holds to an
// administrator's. The rows are read past the wall, for what they are shown
// with -- who sent each, by name, and the state of one that ran out -- so the
// wall's rule is written here: the workspace named is the caller's own, and
// anything else is refused rather than answered with everything.
func (s domainInvitation) List(ctx context.Context, req *app.InvitationListRequest) (*app.InvitationListResponse, error) {
	fs := req.GetFilters()
	if len(fs) != 1 || fs[0].GetTenant() == nil || len(fs[0].GetAccount()) > 0 || fs[0].GetRef() != nil {
		return nil, invalid("filters", "보낸 초대는 워크스페이스(tenant) 하나로 찾습니다")
	}
	f := fs[0]
	fr, err := gate.Actor(ctx)
	if err != nil {
		return nil, err
	}
	db := ent.NewClient(ent.Driver(s.drv))
	ws, err := workspaceOf(ctx, db, f.GetTenant())
	if err != nil {
		return nil, err
	}
	if ws != fr.Tenant.Uuid() {
		return nil, status.Error(codes.PermissionDenied, "이 워크스페이스의 초대는 관리자가 봅니다")
	}
	now := s.deps.now()

	q := db.Invitation.Query().Where(invitation.TenantId(ws), invitation.DateErasedIsNil())
	if f.HasState() {
		q = byState(q, f.GetState(), now)
	}
	rows, err := q.Order(ent.Desc(invitation.FieldDateCreated)).Limit(sizeOf(int(req.GetSize()))).WithInviter().All(ctx)
	if err != nil {
		return nil, err
	}

	items, err := shown(ctx, db, rows, now)
	if err != nil {
		return nil, err
	}
	return app.InvitationListResponse_builder{Items: items}.Build(), nil
}

// sizeOf is how many a list answers with: what it asked for, up to 200, and
// 200 when it asked for none.
func sizeOf(v int) int {
	if v <= 0 || v > 200 {
		return 200
	}
	return v
}

// shown is invitations as the people who sent them read them ([protoOf]).
func shown(ctx context.Context, db *ent.Client, rows []*ent.Invitation, now time.Time) ([]*app.Invitation, error) {
	names := inviterNames(ctx, db, rows)
	called, err := workspaceNames(ctx, db, rows)
	if err != nil {
		return nil, err
	}
	out := make([]*app.Invitation, 0, len(rows))
	for _, v := range rows {
		out = append(out, protoOf(v, now, names, called))
	}
	return out, nil
}

// protoOf is an invitation as the people who sent it read it: the workspace by
// what it is called and its alias, who invited by their name, and never the
// code its mail carried.
func protoOf(v *ent.Invitation, now time.Time, names map[uuid.UUID]string, called map[uuid.UUID]tenancy.Name) *app.Invitation {
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
		t.Alias, t.Name = w.Alias, called[w.Id].Said
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
// sends, so the call says it carried it (loft#136).
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

// workspaceNames is what the workspaces these were sent from are called, for
// the ones read with them: each its own name, or after its owner (D57), whose
// name the call shows.
func workspaceNames(ctx context.Context, db *ent.Client, rows []*ent.Invitation) (map[uuid.UUID]tenancy.Name, error) {
	ts := []*ent.Tenant{}
	for _, v := range rows {
		if v.Edges.Tenant != nil {
			ts = append(ts, v.Edges.Tenant)
		}
	}
	out, err := tenancy.Names(ctx, db, ts...)
	if err != nil {
		return nil, err
	}
	for _, v := range out {
		access.Saw(ctx, v.Saw...)
	}
	return out, nil
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

// addressOf is an address as one is compared: trimmed and folded.
func addressOf(v string) (string, error) {
	v = strings.ToLower(strings.TrimSpace(v))
	a, err := netmail.ParseAddress(v)
	if err != nil || a.Address != v || !strings.Contains(v, "@") {
		return "", invalid("email", "이메일 주소가 올바르지 않습니다")
	}
	return v, nil
}

// mailInvitation writes to the address an invitation went to: who asked, into
// what, and where the workspace is signed in to -- as a holder of the tenant
// at roster this deployment serves, which is who comes to be a member (D85),
// and whom its administrator there makes. One mail either way, so that what is
// sent says nothing about who the address belongs to.
func (d *Deps) mailInvitation(ctx context.Context, v *app.Invitation, inviter, workspace string, zone *time.Location) error {
	if d.Mail == nil {
		return nil
	}
	link := d.Links.Page()
	role := roleWords[v.GetRole()]
	until := v.GetDateExpires().AsTime().In(zone).Format("2006년 1월 2일")
	body := fmt.Sprintf(`%s님이 Loft의 %s에 %s%s 초대했습니다.

아래 주소에서 이 워크스페이스의 계정으로 로그인하면 들어올 수 있습니다.
계정이 아직 없다면 이 워크스페이스의 관리자에게 요청하세요.

%s

이 초대는 %s까지 유효합니다. 모르는 초대라면 이 메일을 무시해 주세요.
`, inviter, workspaceWords(workspace), role, ro(role), link, until)

	return d.Mail.Send(ctx, mail.Message{
		To:      v.GetEmail(),
		Subject: fmt.Sprintf("%s님이 Loft의 %s에 초대했습니다", inviter, workspace),
		Body:    body,
	})
}

// workspaceWords is a workspace as a sentence names it, by what it is called:
// "연구실 워크스페이스", and one called after its owner (D57) as it is --
// "김민준의 워크스페이스" -- rather than with the word twice.
func workspaceWords(name string) string {
	if strings.HasSuffix(name, "워크스페이스") {
		return name
	}
	return name + " 워크스페이스"
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
