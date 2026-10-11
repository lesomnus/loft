package domain

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"
	"uuid"

	"github.com/lesomnus/payday/pdid"
	"github.com/lesomnus/payday/slug"
	"github.com/lesomnus/z"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"

	app "github.com/lesomnus/rove"
	"github.com/lesomnus/rove/internal/ent/audit"
	"github.com/lesomnus/rove/internal/ent/holder"
	"github.com/lesomnus/rove/internal/ent/party"
	"github.com/lesomnus/rove/internal/ent/session"
	"github.com/lesomnus/rove/internal/identity"
)

// The kinds of party and the roles a holder acts with.
var (
	PartyKinds = []string{"person", "team", "org", "vendor"}
	Roles      = []string{"owner", "admin", "manager", "member", "auditor"}
)

type domainParty struct {
	Domain
	app.PartyServiceServer
}

func (s Domain) Party() app.PartyServiceServer { return domainParty{s, s.Next().Party()} }

// Add checks what a party is before it is kept.
func (s domainParty) Add(ctx context.Context, req *app.PartyAddRequest) (*app.Party, error) {
	var out *app.Party
	err := s.tx(ctx, func(t *Tx) error {
		row := proto.Clone(req).(*app.PartyAddRequest)
		row.SetTenant(t.tenantRef())
		if row.GetKind() == "" {
			row.SetKind("person")
		}
		if !slices.Contains(PartyKinds, row.GetKind()) {
			return invalid("kind", "구분이 올바르지 않습니다 (%s)", strings.Join(PartyKinds, ", "))
		}
		row.SetName(strings.TrimSpace(row.GetName()))
		if row.GetName() == "" {
			return invalid("name", "이름을 입력하세요")
		}
		if req.HasHolder() {
			return invalid("holder", "로그인은 로그인 발급으로 만듭니다")
		}
		if p := idOf(req.GetParentId()); !p.IsZero() {
			if err := t.parentParty(p); err != nil {
				return err
			}
		}
		if err := t.begin(nil, "party.add", pdid.Nil, t.now, "추가: "+row.GetName(), "", nil); err != nil {
			return err
		}
		v, err := t.next.Party().Add(t.ctx, row)
		out = v
		return err
	})
	return out, err
}

// parentParty refuses a parent that is not a team or an organization of this
// tenant.
func (t *Tx) parentParty(id pdid.Id) error {
	p, err := t.db.Party.Query().Where(party.Id(id.Uuid()), party.TenantId(t.tenant.Uuid()), party.DateErasedIsNil()).Only(t.ctx)
	if err != nil {
		return status.Error(codes.NotFound, "소속을 찾을 수 없습니다")
	}
	if p.Kind != "team" && p.Kind != "org" {
		return invalid("parent_id", "소속은 팀이나 조직이어야 합니다")
	}
	return nil
}

// Me answers who is calling.
func (s domainParty) Me(ctx context.Context, _ *app.PartyMeRequest) (*app.PartyMeResponse, error) {
	t, err := s.read(ctx)
	if err != nil {
		return nil, err
	}
	h, err := t.next.Holder().Get(ctx, app.HolderGetRequest_builder{Ref: app.HolderRef_builder{Id: t.actor.Bytes()}.Build()}.Build())
	if err != nil {
		return nil, err
	}
	tn, err := t.next.Tenant().Get(ctx, app.TenantGetRequest_builder{Ref: app.TenantRef_builder{Id: t.tenant.Bytes()}.Build()}.Build())
	if err != nil {
		return nil, err
	}
	out := app.PartyMeResponse_builder{
		Holder:       h,
		Tenant:       tn,
		Role:         h.GetRole(),
		AccountsHere: s.deps.People != nil && s.deps.People.Embedded(),
	}.Build()
	if s.deps.People != nil {
		out.SetAccountUrl(s.deps.People.AccountPage())
	}
	if w, err := t.window(); err != nil {
		return nil, err
	} else if since := w.Since(t.now); !since.IsZero() {
		out.SetHistorySince(ts(since))
		out.SetHistoryDays(uint32(w.View / (24 * time.Hour)))
	}
	if p, err := t.db.Party.Query().Where(party.TenantId(t.tenant.Uuid()), party.HolderId(t.actor.Uuid()), party.DateErasedIsNil()).First(ctx); err == nil {
		if v, err := t.next.Party().Get(ctx, app.PartyGetRequest_builder{Ref: app.PartyRef_builder{Id: pdid.Id(p.Id).Bytes()}.Build()}.Build()); err == nil {
			out.SetParty(v)
		}
	}
	return out, nil
}

// Update changes a party's own fields. An empty string leaves a field alone.
func (s domainParty) Update(ctx context.Context, req *app.PartyUpdateRequest) (*app.Party, error) {
	var out *app.Party
	err := s.tx(ctx, func(t *Tx) error {
		p, err := t.next.Party().Get(t.ctx, app.PartyGetRequest_builder{Ref: req.GetRef()}.Build())
		if err != nil {
			return err
		}
		patch := app.PartyPatchRequest_builder{Ref: req.GetRef(), DateUpdatedForce: z.Ptr(true)}.Build()
		if v := strings.TrimSpace(req.GetName()); v != "" {
			patch.SetName(v)
		}
		if v := req.GetDesc(); v != "" {
			patch.SetDesc(v)
		}
		if v := req.GetKind(); v != "" {
			if !slices.Contains(PartyKinds, v) {
				return invalid("kind", "구분이 올바르지 않습니다 (%s)", strings.Join(PartyKinds, ", "))
			}
			patch.SetKind(v)
		}
		if req.GetParentNull() {
			patch.SetParentIdNull(true)
		} else if v := idOf(req.GetParentId()); !v.IsZero() {
			if v == idOf(p.GetId()) {
				return invalid("parent_id", "자기 자신에게 소속될 수 없습니다")
			}
			if err := t.parentParty(v); err != nil {
				return err
			}
			patch.SetParentId(v.Bytes())
		}
		if v := req.GetEmail(); v != "" {
			patch.SetEmail(v)
		}
		if v := req.GetPhone(); v != "" {
			patch.SetPhone(v)
		}
		if v := req.GetCode(); v != "" {
			patch.SetCode(v)
		}
		if len(req.GetLabels()) > 0 {
			patch.SetLabels(req.GetLabels())
		}
		if err := t.begin(nil, "party.update", pdid.Nil, t.now, "수정: "+p.GetName(), "", nil); err != nil {
			return err
		}
		out, err = t.next.Party().Patch(t.ctx, patch)
		return err
	})
	return out, err
}

// Invite gives a person a login.
func (s domainParty) Invite(ctx context.Context, req *app.PartyInviteRequest) (*app.PartyInviteResponse, error) {
	people, err := s.deps.here()
	if err != nil {
		return nil, err
	}

	var out *app.PartyInviteResponse
	var made pdid.Id
	err = s.tx(ctx, func(t *Tx) error {
		p, err := t.next.Party().Get(t.ctx, app.PartyGetRequest_builder{Ref: req.GetRef()}.Build())
		if err != nil {
			return err
		}
		if p.GetKind() != "person" {
			return invalid("ref", "사람에게만 로그인을 발급합니다")
		}
		if p.HasHolder() {
			return failed("%s은(는) 이미 로그인이 있습니다", p.GetName())
		}
		alias, err := slug.ParseAlias(req.GetAlias())
		if err != nil {
			return invalid("alias", "아이디는 영문 소문자, 숫자, -로 씁니다 (%v)", err)
		}
		role := or(req.GetRole(), "member")
		if !slices.Contains(Roles, role) {
			return invalid("role", "역할이 올바르지 않습니다 (%s)", strings.Join(Roles, ", "))
		}
		if taken, err := t.db.Holder.Query().Where(holder.TenantId(t.tenant.Uuid()), holder.Alias(alias), holder.DateErasedIsNil()).Exist(t.ctx); err != nil {
			return err
		} else if taken {
			return invalid("alias", "이미 있는 아이디입니다")
		}

		// The account first, in the directory at roster, which is where they
		// sign in; the holder here is its membership of this workspace.
		person, err := people.AddPerson(t.ctx, alias, p.GetName(), pdid.Nil)
		if err != nil {
			if status.Code(err) == codes.AlreadyExists {
				return invalid("alias", "이미 있는 아이디입니다")
			}
			return err
		}
		made = person.Id
		shown := ""
		if pw := req.GetPassword(); pw != "" {
			if err := people.SetPassword(t.ctx, person.Id, pw); err != nil {
				return passwordRefused(err)
			}
		} else if shown, err = people.IssuePassword(t.ctx, person.Id); err != nil {
			return err
		}

		if err := t.begin(nil, "party.invite", pdid.Nil, t.now, fmt.Sprintf("로그인 발급: %s (@%s)", p.GetName(), alias), "", map[string]string{"role": role}); err != nil {
			return err
		}
		h, err := t.next.Holder().Add(t.ctx, app.HolderAddRequest_builder{
			Tenant:  t.tenantRef(),
			Alias:   alias,
			Name:    p.GetName(),
			Role:    z.Ptr(role),
			Account: person.Id.Bytes(),
		}.Build())
		if err != nil {
			return err
		}
		v, err := t.next.Party().Patch(t.ctx, app.PartyPatchRequest_builder{
			Ref:              req.GetRef(),
			Holder:           app.HolderRef_builder{Id: h.GetId()}.Build(),
			DateUpdatedForce: z.Ptr(true),
		}.Build())
		if err != nil {
			return err
		}
		out = app.PartyInviteResponse_builder{Party: v, Holder: h, Password: shown}.Build()
		return nil
	})
	if err != nil && made != pdid.Nil {
		// Rove kept nothing of them, so roster keeps nothing of the account
		// made for it either.
		if e := people.ForgetPerson(ctx, made); e != nil {
			err = fmt.Errorf("%w (and the person made at roster for it is still there: %v)", err, e)
		}
	}
	return out, err
}

// passwordRefused is a password roster would not take, as the field it was
// typed into.
func passwordRefused(err error) error {
	if status.Code(err) == codes.InvalidArgument {
		return invalid("password", "%s", status.Convert(err).Message())
	}
	return err
}

// SetRole changes what a person may do. The last owner of a tenant cannot be
// demoted, or nobody could manage it.
func (s domainParty) SetRole(ctx context.Context, req *app.PartySetRoleRequest) (*app.Party, error) {
	var out *app.Party
	err := s.tx(ctx, func(t *Tx) error {
		if !slices.Contains(Roles, req.GetRole()) {
			return invalid("role", "역할이 올바르지 않습니다 (%s)", strings.Join(Roles, ", "))
		}
		p, err := t.next.Party().Get(t.ctx, app.PartyGetRequest_builder{Ref: req.GetRef()}.Build())
		if err != nil {
			return err
		}
		h := idOf(p.GetHolder().GetId())
		if h.IsZero() {
			return failed("%s은(는) 로그인이 없습니다", p.GetName())
		}
		cur, err := t.db.Holder.Query().Where(holder.Id(h.Uuid())).Only(t.ctx)
		if err != nil {
			return err
		}
		if cur.Role == "owner" && req.GetRole() != "owner" {
			n, err := t.db.Holder.Query().Where(holder.TenantId(t.tenant.Uuid()), holder.Role("owner"), holder.DateErasedIsNil()).Count(t.ctx)
			if err != nil {
				return err
			}
			if n <= 1 {
				return failed("마지막 소유자입니다. 먼저 다른 사람을 소유자로 정하세요")
			}
		}
		if err := t.begin(nil, "party.role", pdid.Nil, t.now, fmt.Sprintf("역할 변경: %s → %s", p.GetName(), req.GetRole()), "", nil); err != nil {
			return err
		}
		if _, err := t.next.Holder().Patch(t.ctx, app.HolderPatchRequest_builder{
			Ref:              app.HolderRef_builder{Id: h.Bytes()}.Build(),
			Role:             z.Ptr(req.GetRole()),
			DateUpdatedForce: z.Ptr(true),
		}.Build()); err != nil {
			return err
		}
		out, err = t.next.Party().Get(t.ctx, app.PartyGetRequest_builder{Ref: req.GetRef()}.Build())
		return err
	})
	return out, err
}

// SetPassword changes an account's password: the caller's own after checking
// the current one, or somebody else's for an admin, which the policy decides.
//
// A password is the account's, and the account may be in workspaces this
// admin has nothing to do with (D27). So somebody else's is changed only when
// this workspace is the only one their account is in; otherwise it is theirs to
// change, or the operator's (`rove holder password`).
func (s domainParty) SetPassword(ctx context.Context, req *app.PartySetPasswordRequest) (*app.PartySetPasswordResponse, error) {
	people, err := s.deps.here()
	if err != nil {
		return nil, err
	}

	err = s.tx(ctx, func(t *Tx) error {
		target := t.actor
		own := true
		if req.HasRef() {
			p, err := t.next.Party().Get(t.ctx, app.PartyGetRequest_builder{Ref: req.GetRef()}.Build())
			if err != nil {
				return err
			}
			if h := idOf(p.GetHolder().GetId()); !h.IsZero() && h != t.actor {
				target, own = h, false
			}
		}
		if !own {
			me, err := t.db.Holder.Query().Where(holder.Id(t.actor.Uuid())).Only(t.ctx)
			if err != nil {
				return err
			}
			if me.Role != "owner" && me.Role != "admin" {
				return status.Error(codes.PermissionDenied, "다른 사람의 비밀번호는 관리자만 바꿀 수 있습니다")
			}
		}

		h, err := t.db.Holder.Get(t.ctx, target.Uuid())
		if err != nil || h.DateErased != nil || h.Account == nil {
			return failed("바꿀 비밀번호가 없는 사람입니다")
		}
		account := pdid.Id(*h.Account)
		if own {
			p, err := people.ById(t.ctx, account.String())
			if err != nil {
				return err
			}
			if _, err := people.Verify(t.ctx, p.Alias, req.GetCurrent()); err != nil {
				if errors.Is(err, identity.ErrRefused) {
					return invalid("current", "지금 비밀번호가 맞지 않습니다")
				}
				return err
			}
		} else {
			elsewhere, err := t.db.Holder.Query().Where(
				holder.Account(account.Uuid()),
				holder.TenantIdNEQ(t.tenant.Uuid()),
				holder.DateErasedIsNil(),
			).Exist(t.ctx)
			if err != nil {
				return err
			}
			if elsewhere {
				return failed("다른 워크스페이스에도 있는 계정이라 여기서 비밀번호를 바꿀 수 없습니다")
			}
		}
		if err := t.begin(nil, "party.password", pdid.Nil, t.now, "비밀번호 변경", "", nil); err != nil {
			return err
		}
		if err := people.SetPassword(t.ctx, account, req.GetPassword()); err != nil {
			return passwordRefused(err)
		}
		if !own {
			// A password reset by somebody else is somebody being locked out
			// of wherever they are signed in: here, and at roster.
			if err := people.Invalidate(t.ctx, account); err != nil {
				return err
			}
			return t.signOut(account)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return &app.PartySetPasswordResponse{}, nil
}

// Deactivate ends a login and keeps the person and their history.
//
// What ends is the membership of this workspace. The account is the person's
// and may be in other workspaces (D27), so it is not touched, and neither are
// its sessions: a call about this workspace stops finding a holder to be
// served as the moment the membership is gone.
func (s domainParty) Deactivate(ctx context.Context, req *app.PartyDeactivateRequest) (*app.Party, error) {
	var out *app.Party
	err := s.tx(ctx, func(t *Tx) error {
		p, err := t.next.Party().Get(t.ctx, app.PartyGetRequest_builder{Ref: req.GetRef()}.Build())
		if err != nil {
			return err
		}
		h := idOf(p.GetHolder().GetId())
		if h.IsZero() {
			out = p
			return nil
		}
		if h == t.actor {
			return failed("자기 로그인은 중지할 수 없습니다")
		}
		if err := t.begin(nil, "party.deactivate", pdid.Nil, t.now, "로그인 비활성화: "+p.GetName(), "", nil); err != nil {
			return err
		}
		if _, err := t.next.Holder().Erase(t.ctx, app.HolderRef_builder{Id: h.Bytes()}.Build()); err != nil {
			return err
		}
		out, err = t.next.Party().Patch(t.ctx, app.PartyPatchRequest_builder{
			Ref:              req.GetRef(),
			HolderNull:       z.Ptr(true),
			DateUpdatedForce: z.Ptr(true),
		}.Build())
		return err
	})
	return out, err
}

// Pseudonymize forgets a person: the party keeps its identifier, so the
// history that names it still reads, and loses everything that said who it
// was -- on the row, and in the trail's copies of it and of their login, in
// the database and in the archive (design 8.2, D13).
//
// What a legal hold is on stays as it was, since a legal claim outweighs an
// erasure request; the event says how much, and asking again once the hold is
// lifted finishes it.
//
// It forgets them **in this workspace**. Their account is theirs and may be in
// others (D27); leaving the deployment altogether is the account's own act
// (design 8.4).
func (s domainParty) Pseudonymize(ctx context.Context, req *app.PartyPseudonymizeRequest) (*app.Party, error) {
	var out *app.Party
	err := s.tx(ctx, func(t *Tx) error {
		p, err := t.next.Party().Get(t.ctx, app.PartyGetRequest_builder{Ref: req.GetRef()}.Build())
		if err != nil {
			return err
		}
		if p.GetKind() != "person" {
			return invalid("ref", "개인정보 삭제는 사람에게만 합니다")
		}
		v, held, err := t.pseudonymize(p)
		if err != nil {
			return err
		}
		out = v

		payload := map[string]string{}
		if held > 0 {
			payload["held"] = fmt.Sprint(held)
		}
		return t.begin(nil, "party.pseudonymize", pdid.Nil, t.now, "개인정보 삭제", "", payload)
	})
	return out, err
}

// forget blanks what the trail kept of these objects; see [Deps.Forget].
func (t *Tx) forget(objects []pdid.Id) (int, error) {
	if t.deps != nil && t.deps.Forget != nil {
		return t.deps.Forget(t.ctx, t.db, objects)
	}

	ids := make([]uuid.UUID, 0, len(objects))
	for _, v := range objects {
		ids = append(ids, v.Uuid())
	}
	q := t.db.Audit.Update().Where(audit.ObjectIdIn(ids...))
	if w, err := t.window(); err != nil {
		return 0, err
	} else if w.Held() {
		n, err := t.db.Audit.Query().Where(audit.ObjectIdIn(ids...)).Count(t.ctx)
		return n, err
	}
	_, err := q.SetValue([]byte{}).SetPatch([]byte{}).Save(t.ctx)
	return 0, err
}

type domainAssetType struct {
	Domain
	app.AssetTypeServiceServer
}

func (s Domain) AssetType() app.AssetTypeServiceServer {
	return domainAssetType{s, s.Next().AssetType()}
}

func (s domainAssetType) Add(ctx context.Context, req *app.AssetTypeAddRequest) (*app.AssetType, error) {
	var out *app.AssetType
	err := s.tx(ctx, func(t *Tx) error {
		row := proto.Clone(req).(*app.AssetTypeAddRequest)
		row.SetTenant(t.tenantRef())
		if strings.TrimSpace(row.GetName()) == "" {
			return invalid("name", "이름을 입력하세요")
		}
		if row.GetKind() == "" {
			row.SetKind("item")
		}
		if !slices.Contains(Kinds, row.GetKind()) {
			return invalid("kind", "종류가 올바르지 않습니다 (%s)", strings.Join(Kinds, ", "))
		}
		if err := checkSpec(row.GetSpec()); err != nil {
			return err
		}
		if p := idOf(req.GetParentId()); !p.IsZero() {
			if _, err := t.next.AssetType().Get(t.ctx, app.AssetTypeGetRequest_builder{Ref: app.AssetTypeRef_builder{Id: p.Bytes()}.Build()}.Build()); err != nil {
				return err
			}
		}
		row.SetSchemaVersion(1)
		if err := t.begin(nil, "type.add", pdid.Nil, t.now, "유형 추가: "+row.GetName(), "", nil); err != nil {
			return err
		}
		v, err := t.next.AssetType().Add(t.ctx, row)
		out = v
		return err
	})
	return out, err
}

func checkSpec(spec *app.TypeSpec) error {
	seen := map[string]bool{}
	for i, a := range spec.GetAttributes() {
		k := a.GetKey()
		if k == "" || strings.ContainsAny(k, " .") {
			return invalid(fmt.Sprintf("spec.attributes[%d].key", i), "속성 키는 띄어쓰기나 점 없이 씁니다")
		}
		if seen[k] {
			return invalid(fmt.Sprintf("spec.attributes[%d].key", i), "속성 키 %q가 두 번 있습니다", k)
		}
		seen[k] = true
		switch a.GetType() {
		case "", "text", "number", "bool", "date":
		case "enum":
			if len(a.GetOptions()) == 0 {
				return invalid(fmt.Sprintf("spec.attributes[%d].options", i), "선택형 속성에는 선택지가 있어야 합니다")
			}
		default:
			return invalid(fmt.Sprintf("spec.attributes[%d].type", i), "속성 형식이 올바르지 않습니다")
		}
	}
	return nil
}

// Update changes a type. A changed spec is a new schema version; the history
// keeps reading because facts are keyed by the attribute's key, not its label.
func (s domainAssetType) Update(ctx context.Context, req *app.AssetTypeUpdateRequest) (*app.AssetType, error) {
	var out *app.AssetType
	err := s.tx(ctx, func(t *Tx) error {
		v, err := t.next.AssetType().Get(t.ctx, app.AssetTypeGetRequest_builder{Ref: req.GetRef(), Select: app.AssetTypeSelect_builder{All: z.Ptr(true)}.Build()}.Build())
		if err != nil {
			return err
		}
		patch := app.AssetTypePatchRequest_builder{Ref: req.GetRef(), DateUpdatedForce: z.Ptr(true)}.Build()
		if n := strings.TrimSpace(req.GetName()); n != "" {
			patch.SetName(n)
		}
		if d := req.GetDesc(); d != "" {
			patch.SetDesc(d)
		}
		if k := req.GetKind(); k != "" {
			if !slices.Contains(Kinds, k) {
				return invalid("kind", "종류가 올바르지 않습니다 (%s)", strings.Join(Kinds, ", "))
			}
			patch.SetKind(k)
		}
		if req.GetParentNull() {
			patch.SetParentIdNull(true)
		} else if p := idOf(req.GetParentId()); !p.IsZero() {
			// A type is not its own ancestor.
			cur := p
			for range 16 {
				if cur == idOf(v.GetId()) {
					return invalid("parent_id", "유형이 자기 자신의 상위가 될 수 없습니다")
				}
				pv, err := t.next.AssetType().Get(t.ctx, app.AssetTypeGetRequest_builder{Ref: app.AssetTypeRef_builder{Id: cur.Bytes()}.Build()}.Build())
				if err != nil {
					return err
				}
				cur = idOf(pv.GetParentId())
				if cur.IsZero() {
					break
				}
			}
			patch.SetParentId(p.Bytes())
		}
		if req.HasSpec() {
			if err := checkSpec(req.GetSpec()); err != nil {
				return err
			}
			if !proto.Equal(req.GetSpec(), v.GetSpec()) {
				patch.SetSpec(req.GetSpec())
				patch.SetSchemaVersion(v.GetSchemaVersion() + 1)
			}
		}
		if err := t.begin(nil, "type.update", pdid.Nil, t.now, "유형 수정: "+v.GetName(), "", nil); err != nil {
			return err
		}
		out, err = t.next.AssetType().Patch(t.ctx, patch)
		return err
	})
	return out, err
}

type domainItemModel struct {
	Domain
	app.ItemModelServiceServer
}

func (s Domain) ItemModel() app.ItemModelServiceServer {
	return domainItemModel{s, s.Next().ItemModel()}
}

// Add keeps a maker's model, in the caller's tenant.
func (s domainItemModel) Add(ctx context.Context, req *app.ItemModelAddRequest) (*app.ItemModel, error) {
	var out *app.ItemModel
	err := s.tx(ctx, func(t *Tx) error {
		row := proto.Clone(req).(*app.ItemModelAddRequest)
		row.SetTenant(t.tenantRef())
		row.SetName(strings.TrimSpace(row.GetName()))
		row.SetMaker(strings.TrimSpace(row.GetMaker()))
		if row.GetName() == "" {
			return invalid("name", "이름을 입력하세요")
		}
		if req.HasType() {
			if _, err := t.next.AssetType().Get(t.ctx, app.AssetTypeGetRequest_builder{Ref: req.GetType()}.Build()); err != nil {
				return err
			}
		}
		for i, sl := range row.GetSpec().GetSlots() {
			if strings.TrimSpace(sl.GetName()) == "" {
				return invalid(fmt.Sprintf("spec.slots[%d].name", i), "이름을 입력하세요")
			}
		}
		if err := t.begin(nil, "model.add", pdid.Nil, t.now, "모델 추가: "+strings.TrimSpace(row.GetMaker()+" "+row.GetName()), "", nil); err != nil {
			return err
		}
		v, err := t.next.ItemModel().Add(t.ctx, row)
		out = v
		return err
	})
	return out, err
}

func (s domainItemModel) Update(ctx context.Context, req *app.ItemModelUpdateRequest) (*app.ItemModel, error) {
	var out *app.ItemModel
	err := s.tx(ctx, func(t *Tx) error {
		v, err := t.next.ItemModel().Get(t.ctx, app.ItemModelGetRequest_builder{Ref: req.GetRef()}.Build())
		if err != nil {
			return err
		}
		patch := app.ItemModelPatchRequest_builder{Ref: req.GetRef(), DateUpdatedForce: z.Ptr(true)}.Build()
		if n := strings.TrimSpace(req.GetName()); n != "" {
			patch.SetName(n)
		}
		if d := req.GetDesc(); d != "" {
			patch.SetDesc(d)
		}
		if m := req.GetMaker(); m != "" {
			patch.SetMaker(m)
		}
		if c := req.GetCode(); c != "" {
			patch.SetCode(c)
		}
		if req.GetTypeNull() {
			patch.SetTypeNull(true)
		} else if req.HasType() {
			patch.SetType(req.GetType())
		}
		if req.HasSpec() {
			patch.SetSpec(req.GetSpec())
		}
		if err := t.begin(nil, "model.update", pdid.Nil, t.now, "모델 수정: "+v.GetName(), "", nil); err != nil {
			return err
		}
		out, err = t.next.ItemModel().Patch(t.ctx, patch)
		return err
	})
	return out, err
}

// signOut ends every session an account has, in every workspace: a session is
// the account's.
func (t *Tx) signOut(account pdid.Id) error {
	_, err := t.db.Session.Delete().Where(session.Account(account.Uuid())).Exec(t.ctx)
	return err
}
