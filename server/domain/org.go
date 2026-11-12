package domain

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"
	"uuid"

	"github.com/lesomnus/payday/pdid"
	"github.com/lesomnus/z"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"

	app "github.com/lesomnus/loft"
	"github.com/lesomnus/loft/internal/ent"
	"github.com/lesomnus/loft/internal/ent/audit"
	"github.com/lesomnus/loft/internal/ent/party"
	"github.com/lesomnus/loft/server/pd"
)

// The kinds of party.
var PartyKinds = []string{"person", "team", "org", "vendor"}

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
		v, err := t.next.Party().Add(t.ctx, row)
		if err != nil {
			return err
		}
		out = v
		return t.begin(nil, "party.add", pdid.Nil, t.now, "", app.PartySaved_builder{Party: v.GetId(), Kind: v.GetKind()}.Build())
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

// Me answers who is calling, and what they may call: what roster says of them
// now (D89), which is what their page is drawn from.
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
	// What it is called, which for one with no name of its own is its
	// address (D57, D89).
	called, err := t.workspaceName()
	if err != nil {
		return nil, err
	}
	methods, err := t.methodsOf(t.actor)
	if err != nil {
		return nil, err
	}
	out := app.PartyMeResponse_builder{
		Holder:        h,
		Tenant:        tn,
		Methods:       methods,
		Timezone:      t.zone().String(),
		Currency:      t.settings().currency,
		RestoreDays:   uint32(t.restoreWithin() / (24 * time.Hour)),
		WorkspaceName: called.Said,
	}.Build()
	if s.deps.People != nil {
		out.SetAccountUrl(s.deps.People.AccountPage())
		out.SetConsoleUrl(s.deps.People.ConsolePage())
	}
	// What every member's page says above it while levyan restricts the
	// workspace or makes it read-only (design 7.9, 7.10): the copy its
	// writes are held to, read as they read it.
	restricted, err := t.restricted()
	if err != nil {
		return nil, err
	}
	out.SetRestricted(restricted)
	readonly, err := t.readonly()
	if err != nil {
		return nil, err
	}
	out.SetReadonly(readonly)
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
		if err := t.begin(nil, "party.update", pdid.Nil, t.now, "", app.PartySaved_builder{Party: p.GetId()}.Build()); err != nil {
			return err
		}
		out, err = t.next.Party().Patch(t.ctx, patch)
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
// It forgets them **in this workspace**. Their holder at roster is the
// tenant's, which roster forgets (design 8.4).
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

		// What a hold kept of the trail in the database; what it keeps in
		// the archive is learnt after the commit, and stays owed.
		return t.begin(nil, "party.pseudonymize", pdid.Nil, t.now, "", app.PartyPseudonymized_builder{Held: uint32(held)}.Build())
	})
	return out, err
}

// LinkMember makes the person record `ref` the one the membership `holder`
// is, by hand (D88): for a member whose first sign-in found none of the
// records an address roster checked could be -- none, or more than one -- or
// found the wrong one. The record is a person's that no membership is, and not one
// forgotten: what was forgotten is nobody's to take up again. The membership
// is a member's of this workspace. One that was another record's leaves it,
// and that record keeps its history and is nobody's login from then.
func (s domainParty) LinkMember(ctx context.Context, req *app.PartyLinkMemberRequest) (*app.Party, error) {
	var out *app.Party
	err := s.tx(ctx, func(t *Tx) error {
		p, err := t.next.Party().Get(t.ctx, app.PartyGetRequest_builder{Ref: req.GetRef()}.Build())
		if err != nil {
			return err
		}
		switch {
		case p.GetKind() != "person":
			return invalid("ref", "구성원은 사람 기록과만 잇습니다")
		case forgotten(p):
			return failed("개인정보를 삭제한 기록에는 구성원을 잇지 않습니다")
		case p.HasHolder():
			return failed("%s은(는) 이미 구성원과 이어져 있습니다. 먼저 끊으세요", p.GetName())
		}
		h, err := t.next.Holder().Get(t.ctx, app.HolderGetRequest_builder{Ref: req.GetHolder()}.Build())
		if status.Code(err) == codes.NotFound {
			return invalid("holder", "이 워크스페이스의 구성원이 아닙니다")
		}
		if err != nil {
			return err
		}
		member := idOf(h.GetId())

		// The record it is now, which lets it go: a membership is one record
		// at a time.
		var was []byte
		if v, err := t.db.Party.Query().Where(party.TenantId(t.tenant.Uuid()), party.HolderId(member.Uuid())).First(t.ctx); err == nil {
			was = pdid.Id(v.Id).Bytes()
		} else if !ent.IsNotFound(err) {
			return err
		}

		if err := t.begin(nil, "party.link", pdid.Nil, t.now, "", app.PartyLinked_builder{
			Party:  p.GetId(),
			Holder: member.Bytes(),
			Was:    was,
		}.Build()); err != nil {
			return err
		}
		if was != nil {
			if _, err := t.next.Party().Patch(t.ctx, app.PartyPatchRequest_builder{
				Ref:              app.PartyRef_builder{Id: was}.Build(),
				HolderNull:       z.Ptr(true),
				DateUpdatedForce: z.Ptr(true),
			}.Build()); err != nil {
				return err
			}
		}
		out, err = t.next.Party().Patch(t.ctx, app.PartyPatchRequest_builder{
			Ref:              app.PartyRef_builder{Id: p.GetId()}.Build(),
			Holder:           app.HolderRef_builder{Id: member.Bytes()}.Build(),
			DateUpdatedForce: z.Ptr(true),
		}.Build())
		return err
	})
	return out, err
}

// UnlinkMember takes the membership off the person record `ref`, by hand
// (D88): the record keeps its history and is nobody's login from then, and the
// membership is no record's until one is linked to it -- signing in again
// does not find it one, which would find the same record again.
func (s domainParty) UnlinkMember(ctx context.Context, req *app.PartyUnlinkMemberRequest) (*app.Party, error) {
	var out *app.Party
	err := s.tx(ctx, func(t *Tx) error {
		p, err := t.next.Party().Get(t.ctx, app.PartyGetRequest_builder{Ref: req.GetRef()}.Build())
		if err != nil {
			return err
		}
		member := idOf(p.GetHolder().GetId())
		if member.IsZero() {
			return failed("%s은(는) 구성원과 이어져 있지 않습니다", p.GetName())
		}
		if err := t.begin(nil, "party.unlink", pdid.Nil, t.now, "", app.PartyUnlinked_builder{
			Party:  p.GetId(),
			Holder: member.Bytes(),
		}.Build()); err != nil {
			return err
		}
		out, err = t.next.Party().Patch(t.ctx, app.PartyPatchRequest_builder{
			Ref:              app.PartyRef_builder{Id: p.GetId()}.Build(),
			HolderNull:       z.Ptr(true),
			DateUpdatedForce: z.Ptr(true),
		}.Build())
		return err
	})
	return out, err
}

// forget blanks what the trail kept of these objects; see [Deps.Forget]. The
// archive's half it owes is paid once the transaction has committed
// ([Deps.Pay]).
func (t *Tx) forget(objects []pdid.Id) (int, error) {
	if t.deps != nil && t.deps.Forget != nil {
		held, err := t.deps.Forget(t.ctx, t.db, objects)
		if err == nil && t.shared != nil {
			t.shared.forgot = append(t.shared.forgot, objects...)
		}
		return held, err
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

// Erase is the generated one, which begins no operation, held to what a
// read-only workspace does (D64).
func (s domainAssetType) Erase(ctx context.Context, ref *app.AssetTypeRef) (*app.AssetTypeEraseResponse, error) {
	return passing(s.Domain, ctx, func(t *Tx) (*app.AssetTypeEraseResponse, error) { return t.next.AssetType().Erase(t.ctx, ref) })
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
		if !row.HasId() {
			row.SetId(pdid.New(pd.AssetTypeDomain).Bytes())
		}
		if err := t.begin(nil, "type.add", pdid.Nil, t.now, "", app.AssetTypeSaved_builder{
			Type: row.GetId(),
			Name: row.GetName(),
			Kind: row.GetKind(),
		}.Build()); err != nil {
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
		if !isAttributeKey(k) {
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
		if err := t.begin(nil, "type.update", pdid.Nil, t.now, "", app.AssetTypeSaved_builder{Type: v.GetId(), Name: v.GetName()}.Build()); err != nil {
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

// Erase is the generated one, which begins no operation, held to what a
// read-only workspace does (D64).
func (s domainItemModel) Erase(ctx context.Context, ref *app.ItemModelRef) (*app.ItemModelEraseResponse, error) {
	return passing(s.Domain, ctx, func(t *Tx) (*app.ItemModelEraseResponse, error) { return t.next.ItemModel().Erase(t.ctx, ref) })
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
		if !row.HasId() {
			row.SetId(pdid.New(pd.ItemModelDomain).Bytes())
		}
		if err := t.begin(nil, "model.add", pdid.Nil, t.now, "", app.ItemModelSaved_builder{
			Model: row.GetId(),
			Name:  row.GetName(),
			Maker: row.GetMaker(),
		}.Build()); err != nil {
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
		if err := t.begin(nil, "model.update", pdid.Nil, t.now, "", app.ItemModelSaved_builder{Model: v.GetId(), Name: v.GetName()}.Build()); err != nil {
			return err
		}
		out, err = t.next.ItemModel().Patch(t.ctx, patch)
		return err
	})
	return out, err
}
