package domain

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"
	"uuid"

	"github.com/lesomnus/payday/past"
	"github.com/lesomnus/payday/pderr"
	"github.com/lesomnus/payday/pdid"
	"github.com/lesomnus/z"
	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"

	app "github.com/lesomnus/rove"
	"github.com/lesomnus/rove/internal/ent"
	"github.com/lesomnus/rove/internal/ent/asset"
	"github.com/lesomnus/rove/internal/ent/assettype"
	"github.com/lesomnus/rove/internal/ent/itemmodel"
	"github.com/lesomnus/rove/internal/ent/predicate"
	"github.com/lesomnus/rove/server/bare"
	"github.com/lesomnus/rove/server/pd"
	"github.com/lesomnus/rove/server/policy"
)

// MaxRegisterRows is the most assets one bulk registration makes; a larger
// intake is registered in parts. It is the old sheet import's, until it is
// measured again (design 9.13).
const MaxRegisterRows = 5000

// refusedAtMost is how many issues a refusal names. A dry run answers with
// all of them; a refusal is for a client that did not ask for one first, and
// a message of five thousand sentences is read by nobody.
const refusedAtMost = 100

// AddMany registers new assets as one operation: bulk registration, an intake
// (design 9.13, D34). It makes assets only. A tag that is here already is an
// issue, not a row to change, so it compares nothing with what exists.
//
// Its checks ask nothing for each item. The role is the call's (the policy
// table). The wall is asked once for each distinct place, type and model the
// items name. The rest -- names, words, dates, and tags among the items and
// against the tenant's, in one query -- is read in memory. A dry run answers
// with what it found and makes nothing. Anything else with an issue is
// refused with them, and makes nothing either.
//
// What it writes, it writes a few hundred rows to a statement: the assets,
// their first facts, their placements and their events, each through the
// generated AddMany. The operation is one event, "대량 등록: N개", and each
// asset has a child of it -- the "등록" its Add would have written -- so that
// what happened to an asset is in its own history, and who brought it in is
// one step up.
func (s domainAsset) AddMany(ctx context.Context, req *app.AssetAddManyRequest) (*app.AssetAddManyResponse, error) {
	items := req.GetItems()
	if len(items) == 0 {
		return nil, invalid("items", "등록할 자산이 없습니다")
	}
	if len(items) > MaxRegisterRows {
		return nil, invalid("items", "한 번에 %d개까지 등록합니다 (%d개). 나눠서 등록하세요", MaxRegisterRows, len(items))
	}

	if req.GetDryRun() {
		t, err := s.read(ctx)
		if err != nil {
			return nil, err
		}
		in, err := t.intake(items, req.GetOp())
		if err != nil {
			return nil, err
		}
		return in.answer(), nil
	}

	var out *app.AssetAddManyResponse
	err := s.tx(ctx, func(t *Tx) error {
		// A copy sent again is answered with what the first one made, before
		// anything is checked: the tags the first one gave are taken now.
		if err := t.done(req.GetOp()); err != nil {
			return err
		}
		// Somewhere is a change to the tree: its lock before the moment
		// ([Tx.lockTree]).
		if slices.ContainsFunc(items, (*app.AssetAddRequest).HasTo) {
			if err := t.lockTree(); err != nil {
				return err
			}
		}

		in, err := t.intake(items, req.GetOp())
		if err != nil {
			return err
		}
		if len(in.issues) > 0 {
			return in.refusal()
		}
		out, err = t.register(req, in)
		return err
	})
	if errors.Is(err, errDone) {
		return s.registered(ctx, req)
	}
	return out, err
}

// intake is a bulk registration read and checked, before anything is written.
type intake struct {
	rows   []*intakeRow
	issues []*app.AssetAddManyIssue

	// What the rows name, each looked up once: by `id:` or `name:`.
	types  map[string]*pick
	models map[string]*pick
	places map[string]*app.Asset

	// What the operation makes, in the order it is first named.
	newTypes  []*pick
	newModels []*pick
}

type intakeRow struct {
	item *app.AssetAddRequest
	id   pdid.Id

	since     time.Time
	name      string
	kind      string
	status    string
	condition string
	tag       string
	mode      string

	to    *app.Asset
	typ   *pick
	model *pick
}

// pick is a type or a model a row names: one that is here, or one the
// operation makes.
type pick struct {
	id    uuid.UUID // zero, for one to make, until it is made
	name  string
	maker string
	spec  *app.TypeSpec
	fresh bool

	// What a type to make is made with: the kind of the first row naming it;
	// and a model to make, the type of the first row naming it.
	kind string
	typ  *pick

	// Why it cannot be used, said on every row naming it.
	refused string
}

func (p *pick) say() string {
	return strings.TrimSpace(p.maker + " " + p.name)
}

func (in *intake) issue(i int, field, format string, args ...any) {
	in.issues = append(in.issues, app.AssetAddManyIssue_builder{
		Item:    uint32(i),
		Field:   field,
		Message: fmt.Sprintf(format, args...),
	}.Build())
}

// intake reads and checks `items`, writing nothing.
func (t *Tx) intake(items []*app.AssetAddRequest, op []byte) (*intake, error) {
	in := &intake{
		types:  map[string]*pick{},
		models: map[string]*pick{},
		places: map[string]*app.Asset{},
	}
	if err := in.lookUp(t, items); err != nil {
		return nil, err
	}

	given := map[string]int{}
	for i, item := range items {
		r := &intakeRow{item: item}
		in.rows = append(in.rows, r)

		if len(item.GetId()) > 0 {
			id, err := pdid.From(item.GetId())
			if err != nil {
				in.issue(i, "id", "ID가 올바르지 않습니다: %v", err)
			}
			r.id = id
		} else {
			r.id = made(op, pd.AssetDomain, i)
		}

		if item.HasCustodian() {
			in.issue(i, "custodian", "사용자는 지급·대여로 정해집니다")
		}
		since, err := t.at(item.GetSince(), "since")
		if err != nil {
			in.issue(i, "since", "미래의 날짜는 적을 수 없습니다")
			since = t.now
		}
		r.since = since
		if item.HasAcquiredAt() && item.GetAcquiredAt().AsTime().After(t.now.Add(5*time.Minute)) {
			in.issue(i, "acquired_at", "미래의 날짜는 적을 수 없습니다")
		}

		if r.name = strings.TrimSpace(item.GetName()); r.name == "" {
			in.issue(i, "name", "이름을 입력하세요")
		}

		switch {
		case item.HasType() && item.GetTypeName() != "":
			in.issue(i, "type_name", "유형을 ID와 이름으로 함께 정했습니다")
		case item.HasType():
			r.typ = in.types["id:"+uuidOf(item.GetType().GetId()).String()]
		case strings.TrimSpace(item.GetTypeName()) != "":
			r.typ = in.types["name:"+strings.ToLower(strings.TrimSpace(item.GetTypeName()))]
		}

		r.kind = item.GetKind()
		if r.kind == "" && r.typ != nil && r.typ.refused == "" {
			r.kind = r.typ.kind
		}
		if r.kind == "" {
			r.kind = "item"
		}
		if !slices.Contains(Kinds, r.kind) {
			in.issue(i, "kind", "종류가 올바르지 않습니다 (%s)", strings.Join(Kinds, ", "))
		}
		if r.status = or(item.GetStatus(), "active"); !slices.Contains(Statuses, r.status) {
			in.issue(i, "status", "상태가 올바르지 않습니다 (%s)", strings.Join(Statuses, ", "))
		}
		if r.condition = or(item.GetCondition(), "good"); !slices.Contains(Conditions, r.condition) {
			in.issue(i, "condition", "컨디션이 올바르지 않습니다 (%s)", strings.Join(Conditions, ", "))
		}

		if r.typ != nil {
			switch {
			case r.typ.refused != "":
				in.issue(i, fieldOf(item.HasType(), "type", "type_name"), "%s", r.typ.refused)
			case r.typ.fresh && r.typ.kind == "":
				r.typ.kind = r.kind
			}
			if err := checkAttributes(r.typ.spec, item.GetAttributes(), true); err != nil {
				in.issue(i, "attributes", "%s", messageOf(err))
			}
		}

		switch {
		case item.HasModel() && item.GetModelName() != "":
			in.issue(i, "model_name", "모델을 ID와 이름으로 함께 정했습니다")
		case item.HasModel():
			r.model = in.models["id:"+uuidOf(item.GetModel().GetId()).String()]
		case strings.TrimSpace(item.GetModelName()) != "":
			r.model = in.models[modelKey(item.GetModelName(), item.GetMaker())]
		}
		if r.model != nil {
			switch {
			case r.model.refused != "":
				in.issue(i, fieldOf(item.HasModel(), "model", "model_name"), "%s", r.model.refused)
			case r.model.fresh && r.model.typ == nil:
				r.model.typ = r.typ
			}
		}

		if item.HasTo() {
			to := in.places[placeKey(item.GetTo())]
			switch {
			case to == nil:
				in.issue(i, "to", "위치 %s를 찾을 수 없습니다", sayRef(item.GetTo()))
			case to.GetKind() != "space":
				in.issue(i, "to", "위치는 공간의 태그로 적습니다 (%s는 %s)", to.GetTag(), or(kindSay[to.GetKind()], to.GetKind()))
			default:
				if err := canHold(to, r.kind); err != nil {
					in.issue(i, "to", "%s", messageOf(err))
				}
				r.to = to
			}
			if r.mode = or(item.GetMode(), "located"); !slices.Contains(Modes, r.mode) {
				in.issue(i, "mode", "배치 방식이 올바르지 않습니다 (%s)", strings.Join(Modes, ", "))
			}
		} else if item.GetSlot() != "" {
			in.issue(i, "slot", "자리는 위치와 함께 적습니다")
		}

		if r.tag = strings.TrimSpace(item.GetTag()); r.tag != "" {
			if j, ok := given[r.tag]; ok {
				in.issue(i, "tag", "태그 %s가 %d번째 항목에도 있습니다", r.tag, j+1)
			} else {
				given[r.tag] = i
			}
		}
	}

	if err := in.checkTags(t, given); err != nil {
		return nil, err
	}
	if err := in.checkSlots(t); err != nil {
		return nil, err
	}
	if err := in.giveTags(t, given); err != nil {
		return nil, err
	}

	// In the order of the items, whichever check found each.
	slices.SortStableFunc(in.issues, func(a, b *app.AssetAddManyIssue) int { return int(a.GetItem()) - int(b.GetItem()) })
	return in, nil
}

// lookUp reads what the items name -- each distinct type, model and place
// once, through the stack, which is the wall's check that the caller may see
// it -- before any row is read.
func (in *intake) lookUp(t *Tx, items []*app.AssetAddRequest) error {
	admin := policy.Levels[t.roleOf()] >= policy.Admin

	// Types: by id, and by name in one query.
	names := []string{}
	for _, item := range items {
		if item.HasType() {
			id := uuidOf(item.GetType().GetId())
			k := "id:" + id.String()
			if _, ok := in.types[k]; ok {
				continue
			}
			in.types[k] = t.typeBy(id)
			continue
		}
		if n := strings.TrimSpace(item.GetTypeName()); n != "" {
			k := "name:" + strings.ToLower(n)
			if _, ok := in.types[k]; !ok {
				in.types[k] = &pick{name: n}
				names = append(names, n)
			}
		}
	}
	if len(names) > 0 {
		ps := make([]predicate.AssetType, len(names))
		for i, n := range names {
			ps[i] = assettype.NameEqualFold(n)
		}
		vs, err := t.db.AssetType.Query().
			Where(assettype.TenantId(t.tenant.Uuid()), assettype.DateErasedIsNil(), assettype.Or(ps...)).
			Order(ent.Asc(assettype.FieldDateCreated), ent.Asc(assettype.FieldId)).
			All(t.ctx)
		if err != nil {
			return err
		}
		for _, n := range names {
			p := in.types["name:"+strings.ToLower(n)]
			i := slices.IndexFunc(vs, func(v *ent.AssetType) bool { return strings.EqualFold(v.Name, n) })
			switch {
			case i >= 0:
				*p = *t.typeBy(vs[i].Id)
			case admin:
				p.fresh = true
				in.newTypes = append(in.newTypes, p)
			default:
				p.refused = fmt.Sprintf("유형 %q가 없습니다. 유형은 관리자가 만듭니다", n)
			}
		}
	}

	// Models: by id, and by name -- and maker, when one is said -- in one query.
	type named struct{ name, maker string }
	wanted := []named{}
	for _, item := range items {
		if item.HasModel() {
			id := uuidOf(item.GetModel().GetId())
			k := "id:" + id.String()
			if _, ok := in.models[k]; !ok {
				in.models[k] = t.modelBy(id)
			}
			continue
		}
		if n := strings.TrimSpace(item.GetModelName()); n != "" {
			k := modelKey(n, item.GetMaker())
			if _, ok := in.models[k]; !ok {
				m := strings.TrimSpace(item.GetMaker())
				in.models[k] = &pick{name: n, maker: m}
				wanted = append(wanted, named{n, m})
			}
		}
	}
	if len(wanted) > 0 {
		ps := make([]predicate.ItemModel, len(wanted))
		for i, w := range wanted {
			ps[i] = itemmodel.NameEqualFold(w.name)
		}
		vs, err := t.db.ItemModel.Query().
			Where(itemmodel.TenantId(t.tenant.Uuid()), itemmodel.DateErasedIsNil(), itemmodel.Or(ps...)).
			Order(ent.Asc(itemmodel.FieldDateCreated), ent.Asc(itemmodel.FieldId)).
			All(t.ctx)
		if err != nil {
			return err
		}
		for _, w := range wanted {
			p := in.models[modelKey(w.name, w.maker)]
			i := slices.IndexFunc(vs, func(v *ent.ItemModel) bool {
				return strings.EqualFold(v.Name, w.name) && (w.maker == "" || strings.EqualFold(v.Maker, w.maker))
			})
			if i >= 0 {
				*p = *t.modelBy(vs[i].Id)
				continue
			}
			p.fresh = true
			in.newModels = append(in.newModels, p)
		}
	}

	// Places, by what each item calls one.
	for _, item := range items {
		if !item.HasTo() {
			continue
		}
		k := placeKey(item.GetTo())
		if _, ok := in.places[k]; ok {
			continue
		}
		// A sheet names a place by its tag alone: the caller's tenant's.
		ref := item.GetTo()
		if ref.HasTag() && !ref.GetTag().HasTenant() {
			ref = proto.Clone(ref).(*app.AssetRef)
			ref.GetTag().SetTenant(t.tenantRef())
		}
		to, _, err := t.get(ref, "to")
		if err != nil && !isNotFound(err) {
			return err
		}
		in.places[k] = to
	}
	return nil
}

// typeBy is the type with id, through the stack: refused, rather than an
// error, when the caller cannot see it.
func (t *Tx) typeBy(id uuid.UUID) *pick {
	v, err := t.next.AssetType().Get(t.ctx, app.AssetTypeGetRequest_builder{
		Ref:    app.AssetTypeRef_builder{Id: pdid.Id(id).Bytes()}.Build(),
		Select: app.AssetTypeSelect_builder{All: z.Ptr(true)}.Build(),
	}.Build())
	if err != nil {
		return &pick{refused: "유형을 찾을 수 없습니다"}
	}
	return &pick{id: id, name: v.GetName(), kind: v.GetKind(), spec: specOf(t, v)}
}

// modelBy is the model with id, as [Tx.typeBy] is a type.
func (t *Tx) modelBy(id uuid.UUID) *pick {
	v, err := t.next.ItemModel().Get(t.ctx, app.ItemModelGetRequest_builder{
		Ref: app.ItemModelRef_builder{Id: pdid.Id(id).Bytes()}.Build(),
	}.Build())
	if err != nil {
		return &pick{refused: "모델을 찾을 수 없습니다"}
	}
	return &pick{id: id, name: v.GetName(), maker: v.GetMaker()}
}

// checkTags says of every tag given that is a live asset's already, in one
// query.
func (in *intake) checkTags(t *Tx, given map[string]int) error {
	if len(given) == 0 {
		return nil
	}
	tags := make([]string, 0, len(given))
	for v := range given {
		tags = append(tags, v)
	}
	here, err := t.db.Asset.Query().
		Where(asset.TenantId(t.tenant.Uuid()), asset.TagIn(tags...), asset.DateErasedIsNil()).
		Select(asset.FieldTag).
		Strings(t.ctx)
	if err != nil {
		return err
	}
	slices.Sort(here)
	for _, v := range here {
		in.issue(given[v], "tag", "태그 %s는 이미 있는 자산의 것입니다. 대량 등록은 새 자산만 만듭니다", v)
	}
	return nil
}

// checkSlots says of a slot two rows take, or one that is taken. Few rows
// name one -- the form has no column for it -- so the ones that do are
// checked as an Add checks its own.
func (in *intake) checkSlots(t *Tx) error {
	taken := map[string]int{}
	for i, r := range in.rows {
		slot := r.item.GetSlot()
		if r.to == nil || slot == "" {
			continue
		}
		k := r.to.GetTag() + "\x00" + slot
		if j, ok := taken[k]; ok {
			in.issue(i, "slot", "자리 %s는 %d번째 항목도 씁니다", slot, j+1)
			continue
		}
		taken[k] = i

		old := timeline[place]{}
		tl, _ := old.set(r.since, &place{parent: uuidOf(r.to.GetId()), mode: r.mode, slot: slot})
		if err := t.checkPlacements(r.id.Uuid(), tl, old, true); err != nil {
			if !isRefusal(err) {
				return err
			}
			in.issue(i, "slot", "%s", messageOf(err))
		}
	}
	return nil
}

// giveTags makes up a tag for each row that has none, as Add does -- a letter
// for its kind and the next number the tenant has not used -- asking for them
// a kind at a time rather than a row at a time.
func (in *intake) giveTags(t *Tx, given map[string]int) error {
	wants := map[string][]*intakeRow{}
	kinds := []string{}
	for _, r := range in.rows {
		if r.tag != "" {
			continue
		}
		if _, ok := wants[r.kind]; !ok {
			kinds = append(kinds, r.kind)
		}
		wants[r.kind] = append(wants[r.kind], r)
	}
	for _, kind := range kinds {
		rows := wants[kind]
		tags, err := t.nextTags(kind, len(rows), given)
		if err != nil {
			return err
		}
		for i, r := range rows {
			r.tag = tags[i]
		}
	}
	return nil
}

// nextTags is [Tx.nextTag] for `n` assets at once: the numbers after the
// tenant's count of the prefix that neither a live asset nor `taken` holds.
func (t *Tx) nextTags(kind string, n int, taken map[string]int) ([]string, error) {
	prefix := or(map[string]string{"item": "A", "space": "S", "kit": "K", "group": "G"}[kind], "A")
	count, err := t.db.Asset.Query().Where(asset.TenantId(t.tenant.Uuid()), asset.TagHasPrefix(prefix+"-")).Count(t.ctx)
	if err != nil {
		return nil, err
	}

	out := make([]string, 0, n)
	next := count + 1
	for len(out) < n {
		want := make([]string, 0, n-len(out))
		for len(want) < n-len(out) {
			tag := fmt.Sprintf("%s-%05d", prefix, next)
			next++
			if _, ok := taken[tag]; !ok {
				want = append(want, tag)
			}
		}
		here, err := t.db.Asset.Query().
			Where(asset.TenantId(t.tenant.Uuid()), asset.TagIn(want...), asset.DateErasedIsNil()).
			Select(asset.FieldTag).
			Strings(t.ctx)
		if err != nil {
			return nil, err
		}
		for _, tag := range want {
			if !slices.Contains(here, tag) {
				out = append(out, tag)
			}
		}
	}
	return out, nil
}

// answer is what a dry run says.
func (in *intake) answer() *app.AssetAddManyResponse {
	res := app.AssetAddManyResponse_builder{
		Issues: in.issues,
		Tags:   in.tags(),
	}.Build()
	for _, p := range in.newTypes {
		res.SetNewTypes(append(res.GetNewTypes(), p.say()))
	}
	for _, p := range in.newModels {
		res.SetNewModels(append(res.GetNewModels(), p.say()))
	}
	return res
}

func (in *intake) tags() []string {
	vs := make([]string, len(in.rows))
	for i, r := range in.rows {
		vs[i] = r.tag
	}
	return vs
}

// refusal is the issues, as the InvalidArgument a form points at: a field
// violation for each, under the item it is about.
func (in *intake) refusal() error {
	vs := make([]pderr.Violation, 0, min(len(in.issues), refusedAtMost)+1)
	for _, v := range in.issues[:min(len(in.issues), refusedAtMost)] {
		vs = append(vs, pderr.Violation{Field: fmt.Sprintf("items[%d].%s", v.GetItem(), v.GetField()), Why: v.GetMessage()})
	}
	if n := len(in.issues) - refusedAtMost; n > 0 {
		vs = append(vs, pderr.Violation{Why: fmt.Sprintf("이 밖에도 %d건이 있습니다. 미리 보기(dry_run)가 모두 보여 줍니다", n)})
	}
	return pderr.Invalid(vs...)
}

// refusalAs is what `in` found, of rows an operation made up itself -- the
// units of a stock converted, what a purchase received -- as a refusal of the
// request that operation was asked with: each said once, however many of the
// rows it is about, under the field of that request it is a matter of (`as`,
// by a row's field, and the request as a whole for one `as` does not name);
// and the tags other assets have already as what came of the prefix the
// request gave.
func (in *intake) refusalAs(as map[string]string) error {
	type said struct{ field, why string }
	vs := []said{}
	taken := []string{}
	for _, v := range in.issues {
		if v.GetField() == "tag" {
			taken = append(taken, in.rows[v.GetItem()].tag)
			continue
		}
		w := said{as[v.GetField()], v.GetMessage()}
		if !slices.Contains(vs, w) {
			vs = append(vs, w)
		}
	}
	if len(taken) > 0 {
		tags := strings.Join(taken[:min(len(taken), 3)], ", ")
		if n := len(taken) - 3; n > 0 {
			tags += fmt.Sprintf(" 외 %d개", n)
		}
		vs = append(vs, said{as["tag"], fmt.Sprintf("태그 %s는 이미 있는 자산의 것입니다. 태그 앞부분을 바꾸세요", tags)})
	}

	whys := make([]string, len(vs))
	fs := make([]*errdetails.BadRequest_FieldViolation, len(vs))
	for i, v := range vs {
		whys[i] = v.why
		fs[i] = &errdetails.BadRequest_FieldViolation{Field: v.field, Description: v.why}
	}
	st := status.New(codes.InvalidArgument, strings.Join(whys, " "))
	if d, err := st.WithDetails(&errdetails.BadRequest{FieldViolations: fs}); err == nil {
		return d.Err()
	}
	return st.Err()
}

// register writes what `in` checked: the operation's event, and then what it
// makes ([Tx.makeAssets]).
func (t *Tx) register(req *app.AssetAddManyRequest, in *intake) (*app.AssetAddManyResponse, error) {
	if err := t.begin(req.GetOp(), "asset.add_many", pdid.Nil, t.now, req.GetReason(), app.AssetsAdded_builder{Count: uint32(len(in.rows))}.Build()); err != nil {
		return nil, err
	}
	out, err := t.makeAssets(in, req.GetReason())
	if err != nil {
		return nil, err
	}

	res := in.answer()
	res.SetItems(out)
	res.SetEventId(t.op.Id().Bytes())
	return res, nil
}

// makeAssets writes what `in` checked as what the operation that has begun
// makes, each asset a child of it -- a bulk registration's, a stock
// converted, a purchase received: the types and models it makes, and then
// the assets, their events, facts and placements, each table a few hundred
// rows to a statement, and each container's watchers told once. Each asset's
// event is the "등록" its Add would have written, for `reason`, the
// operation's own. It answers the assets as they were made, in the order of
// the rows.
func (t *Tx) makeAssets(in *intake, reason string) ([]*app.Asset, error) {
	n := len(in.rows)
	parent := t.op
	if parent == nil {
		return nil, status.Errorf(codes.Internal, "domain: assets made outside an operation, which records no event for them")
	}

	// The types and models it makes, first: the assets point at them. Each
	// is made as its own Add makes it, recording its own event, a child of
	// this one's.
	for _, p := range in.newTypes {
		v, err := t.layer().AssetType().Add(t.ctx, app.AssetTypeAddRequest_builder{Name: p.name, Kind: or(p.kind, "item")}.Build())
		if err != nil {
			return nil, err
		}
		p.id = uuidOf(v.GetId())
	}
	for _, p := range in.newModels {
		add := app.ItemModelAddRequest_builder{Name: p.name, Maker: p.maker}.Build()
		if p.typ != nil && p.typ.refused == "" && p.typ.id != (uuid.UUID{}) {
			add.SetType(app.AssetTypeRef_builder{Id: pdid.Id(p.typ.id).Bytes()}.Build())
		}
		v, err := t.layer().ItemModel().Add(t.ctx, add)
		if err != nil {
			return nil, err
		}
		p.id = uuidOf(v.GetId())
	}

	rows := make([]*app.AssetAddRequest, 0, n)
	places := []*app.PlacementAddRequest{}
	containers := []uuid.UUID{}
	for i, r := range in.rows {
		item := r.item
		// Each asset's own event, named after the operation's as the
		// asset is (`made`).
		ev := parent.Made(pd.EventDomain, i+1)

		row := app.AssetAddRequest_builder{
			Tenant:     t.tenantRef(),
			Id:         r.id.Bytes(),
			Name:       r.name,
			Desc:       item.GetDesc(),
			Labels:     item.GetLabels(),
			Kind:       r.kind,
			Tag:        r.tag,
			Serial:     strings.TrimSpace(item.GetSerial()),
			Status:     r.status,
			Condition:  r.condition,
			Attributes: item.GetAttributes(),
		}.Build()
		if item.HasAcquiredAt() {
			row.SetAcquiredAt(item.GetAcquiredAt())
		}
		if r.typ != nil {
			row.SetType(app.AssetTypeRef_builder{Id: pdid.Id(r.typ.id).Bytes()}.Build())
		}
		if r.model != nil {
			row.SetModel(app.ItemModelRef_builder{Id: pdid.Id(r.model.id).Bytes()}.Build())
		}
		if r.to != nil {
			row.SetParentId(r.to.GetId())
			row.SetPlacementMode(r.mode)
			row.SetSlot(item.GetSlot())
		}
		rows = append(rows, row)

		if r.to != nil {
			parentId := uuidOf(r.to.GetId())
			places = append(places, app.PlacementAddRequest_builder{
				Tenant:      t.tenantRef(),
				Child:       assetRef(r.id.Uuid()),
				Parent:      assetRef(parentId),
				Mode:        r.mode,
				Slot:        item.GetSlot(),
				ValidFrom:   ts(r.since),
				EventId:     ev.Bytes(),
				DateCreated: ts(t.now),
			}.Build())
			if !slices.Contains(containers, parentId) {
				containers = append(containers, parentId)
			}
		}
	}

	// The assets first, and then what is about them: each one's event, a
	// child of the operation's, with the facts it starts with from when it
	// was -- what Add starts one with, told by the asset's own event (D34) --
	// and then where it is. The past holds a fact to a subject there is a row
	// of, so the assets are there before it is asked.
	out := make([]*app.Asset, 0, n)
	for lo := 0; lo < len(rows); lo += bare.AssetAddManyMax {
		vs, err := t.next.Asset().AddMany(t.ctx, app.AssetAddManyRequest_builder{Items: rows[lo:min(lo+bare.AssetAddManyMax, len(rows))]}.Build())
		if err != nil {
			return nil, err
		}
		out = append(out, vs.GetItems()...)
	}
	answered := map[uuid.UUID]*app.Asset{}
	for _, v := range out {
		answered[uuidOf(v.GetId())] = v
	}
	kids := make([]past.Child, n)
	for i, r := range in.rows {
		a, ok := answered[r.id.Uuid()]
		if !ok {
			return nil, status.Errorf(codes.Internal, "domain: %s was added and not answered", r.tag)
		}
		kids[i] = past.Child{
			Event: past.Event{
				Id:      parent.Made(pd.EventDomain, i+1),
				Kind:    "asset.add",
				Subject: r.id,
				At:      r.since,
				Reason:  reason,
				Value:   app.AssetAdded_builder{Asset: r.id.Bytes(), Tag: r.tag, Name: r.name, Kind: r.kind}.Build(),
			},
			Facts: []past.Fact{{Subject: r.id, From: r.since, Values: past.Changes(nil, assetFacts(a))}},
		}
	}
	if _, err := parent.Batch(t.ctx, kids); err != nil {
		return nil, err
	}
	for lo := 0; lo < len(places); lo += bare.PlacementAddManyMax {
		if _, err := t.next.Placement().AddMany(t.ctx, app.PlacementAddManyRequest_builder{Items: places[lo:min(lo+bare.PlacementAddManyMax, len(places))]}.Build()); err != nil {
			return nil, err
		}
	}

	// Each container's watchers, once.
	for _, c := range containers {
		if err := t.bump(c); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// registered is what an operation made, answered to a copy of it sent again:
// the assets named after its op, in the order of the items.
func (s domainAsset) registered(ctx context.Context, req *app.AssetAddManyRequest) (*app.AssetAddManyResponse, error) {
	t, err := s.read(ctx)
	if err != nil {
		return nil, err
	}
	items := req.GetItems()
	ids := make([]uuid.UUID, len(items))
	for i, item := range items {
		if len(item.GetId()) > 0 {
			ids[i] = uuidOf(item.GetId())
		} else {
			ids[i] = made(req.GetOp(), pd.AssetDomain, i).Uuid()
		}
	}
	vs, err := t.db.Asset.Query().Where(asset.TenantId(t.tenant.Uuid()), asset.IdIn(ids...)).All(t.ctx)
	if err != nil {
		return nil, err
	}
	by := map[uuid.UUID]*ent.Asset{}
	for _, v := range vs {
		by[v.Id] = v
	}

	res := app.AssetAddManyResponse_builder{EventId: req.GetOp()}.Build()
	for _, id := range ids {
		v, ok := by[id]
		if !ok {
			continue
		}
		p := v.Proto()
		res.SetItems(append(res.GetItems(), p))
		res.SetTags(append(res.GetTags(), p.GetTag()))
	}
	return res, nil
}

// fieldOf is the field a row named something by: its id, or its name.
func fieldOf(byID bool, id, name string) string {
	if byID {
		return id
	}
	return name
}

// modelKey is how a row names a model: by its name, and by its maker when it
// says one.
func modelKey(name, maker string) string {
	return "name:" + strings.ToLower(strings.TrimSpace(maker)) + "\x00" + strings.ToLower(strings.TrimSpace(name))
}

// placeKey is how a row names a place: by id, or by tag.
func placeKey(ref *app.AssetRef) string {
	if ref.HasId() {
		return "id:" + uuidOf(ref.GetId()).String()
	}
	return "tag:" + strings.TrimSpace(ref.GetTag().GetTag())
}

// sayRef is a place as a row named it, for a person.
func sayRef(ref *app.AssetRef) string {
	if ref.HasTag() {
		return ref.GetTag().GetTag()
	}
	return uuidOf(ref.GetId()).String()
}

// messageOf is the sentence a refusal says.
func messageOf(err error) string {
	if st, ok := status.FromError(err); ok {
		return st.Message()
	}
	return err.Error()
}

// isRefusal says `err` is a refusal of a request, rather than a failure to
// answer one.
func isRefusal(err error) bool {
	switch status.Code(err) {
	case codes.InvalidArgument, codes.FailedPrecondition, codes.NotFound, codes.AlreadyExists:
		return true
	}
	return false
}

func isNotFound(err error) bool { return status.Code(err) == codes.NotFound }
