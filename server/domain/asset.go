package domain

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strconv"
	"strings"
	"time"
	"uuid"

	"github.com/lesomnus/payday/past"
	"github.com/lesomnus/payday/pdid"
	"github.com/lesomnus/z"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"

	app "github.com/lesomnus/rove"
	"github.com/lesomnus/rove/internal/ent"
	"github.com/lesomnus/rove/internal/ent/asset"
	"github.com/lesomnus/rove/internal/ent/bookable"
	"github.com/lesomnus/rove/internal/ent/fact"
	"github.com/lesomnus/rove/internal/ent/link"
	"github.com/lesomnus/rove/internal/ent/placement"
	"github.com/lesomnus/rove/internal/ent/predicate"
	"github.com/lesomnus/rove/internal/ent/stewardship"
	"github.com/lesomnus/rove/server/pd"
)

// The values an asset's fields may hold.
var (
	Kinds      = []string{"item", "space", "kit", "group"}
	Statuses   = []string{"ordered", "active", "in_repair", "lost", "retired", "disposed"}
	Conditions = []string{"good", "fair", "damaged", "broken"}
	Modes      = []string{"located", "installed", "part"}
	// Roles an asset has stewards in. A custodian is opened and closed by a
	// custody document only (design D6).
	StewardRoles = []string{"owner", "manager", "custodian"}
	LinkKinds    = []string{"member_of", "connected_to"}
)

// OutOfUse is the statuses an asset is out of use in: lost until it is found,
// and for good. What it is a member of, it may leave as it goes out of use
// (AssetSetAttributesRequest.leave), and nothing in them is handed over --
// which a reservation that still takes one says -- and nothing of them is
// booked anew, whatever it is (outofuse.go).
var OutOfUse = []string{"lost", "retired", "disposed"}

type domainAsset struct {
	Domain
	app.AssetServiceServer
}

func (s Domain) Asset() app.AssetServiceServer { return domainAsset{s, s.Next().Asset()} }

// place is where a child was: the state of a placement row.
type place struct {
	parent uuid.UUID
	mode   string
	slot   string
	uFrom  int32
	uTo    int32
}

type steward struct{ party uuid.UUID }

type linked struct{ required bool }

var allAsset = app.AssetSelect_builder{All: z.Ptr(true)}.Build()

// get reads an asset through the stack, which is the check that the caller
// may see it.
func (t *Tx) get(ref *app.AssetRef, field string) (*app.Asset, pdid.Id, error) {
	if ref == nil {
		return nil, pdid.Nil, invalid(field, "자산을 지정하세요")
	}
	v, err := t.next.Asset().Get(t.ctx, app.AssetGetRequest_builder{Ref: ref, Select: allAsset}.Build())
	if err != nil {
		if status.Code(err) == codes.NotFound {
			return nil, pdid.Nil, status.Error(codes.NotFound, "자산을 찾을 수 없습니다")
		}
		return nil, pdid.Nil, err
	}
	id, err := pdid.From(v.GetId())
	return v, id, err
}

func assetRef(id uuid.UUID) *app.AssetRef {
	return app.AssetRef_builder{Id: pdid.Id(id).Bytes()}.Build()
}

// Add registers an asset: the row, the facts it starts with, and where it is
// (design 6, D24). The facts are valid from `since`, which is how a register
// imported from a spreadsheet keeps its past.
func (s domainAsset) Add(ctx context.Context, req *app.AssetAddRequest) (*app.Asset, error) {
	named := req.GetId()
	if len(named) == 0 {
		named = made(req.GetOp(), pd.AssetDomain, 0).Bytes()
	}
	var out *app.Asset
	err := s.tx(ctx, func(t *Tx) error {
		// Somewhere is a change to the tree: its lock before the moment
		// ([Tx.lockTree]).
		if req.HasTo() {
			if err := t.lockTree(); err != nil {
				return err
			}
		}
		since, err := t.at(req.GetSince(), "since")
		if err != nil {
			return err
		}
		if req.HasCustodian() {
			return invalid("custodian", "사용자는 지급·대여로 정해집니다")
		}

		row := proto.Clone(req).(*app.AssetAddRequest)
		row.SetTenant(t.tenantRef())

		id, err := pdid.From(named)
		if err != nil {
			return invalid("id", "%v", err)
		}
		row.SetId(id.Bytes())

		var spec *app.TypeSpec
		if req.HasType() {
			ty, err := t.next.AssetType().Get(t.ctx, app.AssetTypeGetRequest_builder{
				Ref:    req.GetType(),
				Select: app.AssetTypeSelect_builder{All: z.Ptr(true)}.Build(),
			}.Build())
			if err != nil {
				return err
			}
			spec = specOf(t, ty)
			if row.GetKind() == "" {
				row.SetKind(ty.GetKind())
			}
		}
		if row.GetKind() == "" {
			row.SetKind("item")
		}
		if !slices.Contains(Kinds, row.GetKind()) {
			return invalid("kind", "종류가 올바르지 않습니다 (%s)", strings.Join(Kinds, ", "))
		}
		if row.GetStatus() == "" {
			row.SetStatus("active")
		}
		if !slices.Contains(Statuses, row.GetStatus()) {
			return invalid("status", "상태가 올바르지 않습니다 (%s)", strings.Join(Statuses, ", "))
		}
		if row.GetCondition() == "" {
			row.SetCondition("good")
		}
		if !slices.Contains(Conditions, row.GetCondition()) {
			return invalid("condition", "컨디션이 올바르지 않습니다 (%s)", strings.Join(Conditions, ", "))
		}
		if strings.TrimSpace(row.GetName()) == "" {
			return invalid("name", "이름을 입력하세요")
		}
		row.SetName(strings.TrimSpace(row.GetName()))
		if err := attributeKeys("attributes", row.GetAttributes()); err != nil {
			return err
		}
		if err := checkAttributes(spec, row.GetAttributes(), true); err != nil {
			return err
		}

		tag := strings.TrimSpace(row.GetTag())
		if tag == "" {
			if tag, err = t.nextTag(row.GetKind()); err != nil {
				return err
			}
		}
		row.SetTag(tag)

		// Where it is: the cache on the row now, and the placement row below.
		var to *app.Asset
		mode := req.GetMode()
		if req.HasTo() {
			if to, _, err = t.get(req.GetTo(), "to"); err != nil {
				return err
			}
			if err := canHold(to, row.GetKind()); err != nil {
				return err
			}
			if mode == "" {
				mode = "located"
			}
			if !slices.Contains(Modes, mode) {
				return invalid("mode", "배치 방식이 올바르지 않습니다 (%s)", strings.Join(Modes, ", "))
			}
			row.SetParentId(to.GetId())
			row.SetPlacementMode(mode)
		} else {
			row.ClearParentId()
			row.SetPlacementMode("")
			row.SetSlot("")
		}

		if err := t.begin(req.GetOp(), "asset.add", id, since, req.GetReason(), app.AssetAdded_builder{
			Asset: id.Bytes(),
			Tag:   tag,
			Name:  row.GetName(),
			Kind:  row.GetKind(),
		}.Build()); err != nil {
			return err
		}

		a, err := t.next.Asset().Add(t.ctx, row)
		if err != nil {
			return err
		}

		// What it starts as, from when it was: each of its fields that is a
		// fact of it, in one statement (payday#84).
		if err := t.op.Set(t.ctx, past.Fact{Subject: id, From: since, Values: past.Changes(nil, assetFacts(a))}); err != nil {
			return err
		}

		if to != nil {
			old := timeline[place]{}
			tl, _ := old.set(since, &place{parent: uuidOf(to.GetId()), mode: mode, slot: req.GetSlot()})
			if err := t.checkPlacements(id.Uuid(), tl, old, true); err != nil {
				return err
			}
			if err := t.writePlacements(id.Uuid(), old, tl); err != nil {
				return err
			}
			if err := t.bump(uuidOf(to.GetId())); err != nil {
				return err
			}
		}

		out, _, err = t.get(assetRef(id.Uuid()), "id")
		return err
	})
	if errors.Is(err, errDone) {
		return s.AssetServiceServer.Get(ctx, app.AssetGetRequest_builder{Ref: app.AssetRef_builder{Id: named}.Build(), Select: allAsset}.Build())
	}
	return out, err
}

// nextTag makes up a tag for an asset nobody gave one: a letter for its kind
// and the next number the tenant has not used.
func (t *Tx) nextTag(kind string) (string, error) {
	prefix := map[string]string{"item": "A", "space": "S", "kit": "K", "group": "G"}[kind]
	n, err := t.db.Asset.Query().Where(asset.TenantId(t.tenant.Uuid()), asset.TagHasPrefix(prefix+"-")).Count(t.ctx)
	if err != nil {
		return "", err
	}
	for i := n + 1; ; i++ {
		tag := fmt.Sprintf("%s-%05d", prefix, i)
		taken, err := t.db.Asset.Query().Where(asset.TenantId(t.tenant.Uuid()), asset.Tag(tag), asset.DateErasedIsNil()).Exist(t.ctx)
		if err != nil {
			return "", err
		}
		if !taken {
			return tag, nil
		}
	}
}

// canHold says whether `parent` may physically hold an asset of `kind`: a
// space holds anything, and only a space holds a space.
func canHold(parent *app.Asset, kind string) error {
	if kind == "space" && parent.GetKind() != "space" {
		return invalid("to", "공간은 다른 공간 안에만 둘 수 있습니다")
	}
	if parent.GetKind() == "group" {
		return invalid("to", "그룹은 장소가 아니라 묶음입니다. 안에 넣지 말고 연결하세요")
	}
	return nil
}

// Erase voids an asset registered by mistake: the row is erased softly and
// every current time row about it is superseded, so the history says it never
// existed rather than that it stopped (design 3.4).
func (s domainAsset) Erase(ctx context.Context, ref *app.AssetRef) (*app.AssetEraseResponse, error) {
	var out *app.AssetEraseResponse
	err := s.tx(ctx, func(t *Tx) error {
		if err := t.lockTree(); err != nil {
			return err
		}
		a, id, err := t.get(ref, "ref")
		if err != nil {
			return err
		}
		inside, err := t.db.Asset.Query().Where(asset.TenantId(t.tenant.Uuid()), asset.ParentIdEQ(id.Uuid()), asset.DateErasedIsNil()).Count(t.ctx)
		if err != nil {
			return err
		}
		if inside > 0 {
			return failed("안에 자산이 %d개 있습니다. 먼저 옮기세요", inside)
		}
		// Its row, which this erases last, held first: a change of its
		// status holds it before the pools it is in ([Tx.recheckPoolsOf]),
		// which this holds before the row otherwise.
		if err := t.holdRow(id.Uuid()); err != nil {
			return err
		}

		if err := t.begin(nil, "asset.void", id, t.now, "", app.AssetVoided_builder{Asset: id.Bytes(), Tag: a.GetTag()}.Build()); err != nil {
			return err
		}
		// And where it was told, before any schedule is held, for the same.
		if p := idOf(a.GetParentId()); !p.IsZero() {
			if err := t.bump(p.Uuid()); err != nil {
				return err
			}
		}

		ps, err := t.db.Placement.Query().Where(placement.TenantId(t.tenant.Uuid()), placement.ChildId(id.Uuid()), placement.SupersededAtIsNil()).Ids(t.ctx)
		if err != nil {
			return err
		}
		for _, v := range ps {
			if err := t.supersedePlacement(v); err != nil {
				return err
			}
		}
		ss, err := t.db.Stewardship.Query().Where(stewardship.TenantId(t.tenant.Uuid()), stewardship.AssetId(id.Uuid()), stewardship.SupersededAtIsNil()).Ids(t.ctx)
		if err != nil {
			return err
		}
		for _, v := range ss {
			if err := t.supersedeStewardship(v); err != nil {
				return err
			}
		}
		ls, err := t.db.Link.Query().Where(link.TenantId(t.tenant.Uuid()), link.Or(link.SourceId(id.Uuid()), link.TargetId(id.Uuid())), link.SupersededAtIsNil()).All(t.ctx)
		if err != nil {
			return err
		}
		// Every kit it was in, and itself if it is one: what each is made
		// of changed, and so what its bookings take. And every pool it was
		// one of the units of, which has one fewer to give. And, of a group,
		// what was in it, which the pools it shares them with need not leave
		// it any more (D44, D47).
		kits, pools, left := []uuid.UUID{id.Uuid()}, []uuid.UUID{}, []uuid.UUID{}
		for _, l := range ls {
			if err := t.supersedeLink(l.Id); err != nil {
				return err
			}
			if l.Kind == "member_of" && l.SourceId == id.Uuid() {
				kits = append(kits, l.TargetId)
				pools = append(pools, l.TargetId)
			}
			if l.Kind == "member_of" && l.TargetId == id.Uuid() {
				left = append(left, l.SourceId)
			}
		}
		changed := append([]uuid.UUID{id.Uuid()}, left...)
		// What both hold, at once: one after the other, the second would
		// take some after the first's, out of the one order.
		reservable, err := t.reservableKits(kits)
		if err != nil {
			return err
		}
		hold, err := t.kitHolds(reservable)
		if err != nil {
			return err
		}
		_, also, err := t.poolHolds(pools, changed)
		if err != nil {
			return err
		}
		if err := t.lockSchedules(append(hold, also...)); err != nil {
			return err
		}
		if err := t.recheckKits(kits...); err != nil {
			return err
		}
		if err := t.recheckPools(pools, changed); err != nil {
			return err
		}
		// And what booked it itself is told it is no more: out of use to
		// it for good, which it is neither approved nor fulfilled through,
		// and -- a pool -- not counted short of either (D47). What made it
		// reservable goes with it: nothing books what never was, and a
		// page that shows what is to book does not ask for it.
		if err := t.voidedOf(id.Uuid()); err != nil {
			return err
		}
		b, err := t.db.Bookable.Query().Where(bookable.TenantId(t.tenant.Uuid()), bookable.AssetId(id.Uuid()), bookable.DateErasedIsNil()).Only(t.ctx)
		switch {
		case err == nil:
			if _, err := t.next.Bookable().Erase(t.ctx, app.BookableRef_builder{Id: pdid.Id(b.Id).Bytes()}.Build()); err != nil {
				return err
			}
		case !ent.IsNotFound(err):
			return err
		}
		// What it was said to be, never: every fact of it that stands,
		// superseded by nothing.
		sts, err := past.Facts(t.ctx, t.past, past.FactQuery{Subjects: []pdid.Id{id}})
		if err != nil {
			return err
		}
		for _, st := range sts {
			for _, r := range st.Rows {
				if err := t.op.Retract(t.ctx, r.Id); err != nil {
					return err
				}
			}
		}

		out, err = t.next.Asset().Erase(t.ctx, app.AssetRef_builder{Id: id.Bytes()}.Build())
		return err
	})
	return out, err
}

// Move puts an asset somewhere from a moment on, or takes it out.
func (s domainAsset) Move(ctx context.Context, req *app.AssetMoveRequest) (*app.Asset, error) {
	var out *app.Asset
	err := s.tx(ctx, func(t *Tx) error {
		// The lock, and then the moment ([Tx.lockTree]).
		if err := t.lockTree(); err != nil {
			return err
		}
		at, err := t.at(req.GetAt(), "at")
		if err != nil {
			return err
		}
		a, id, err := t.get(req.GetRef(), "ref")
		if err != nil {
			return err
		}

		var v *place
		var to *app.Asset
		if req.HasTo() {
			if to, _, err = t.get(req.GetTo(), "to"); err != nil {
				return err
			}
			if err := canHold(to, a.GetKind()); err != nil {
				return err
			}
			mode := req.GetMode()
			if mode == "" {
				mode = "located"
			}
			if !slices.Contains(Modes, mode) {
				return invalid("mode", "배치 방식이 올바르지 않습니다 (%s)", strings.Join(Modes, ", "))
			}
			if req.GetUFrom() < 0 || req.GetUTo() < req.GetUFrom() {
				return invalid("u_from", "랙 위치는 1U부터이고, 시작이 끝보다 클 수 없습니다")
			}
			v = &place{parent: uuidOf(to.GetId()), mode: mode, slot: strings.TrimSpace(req.GetSlot()), uFrom: req.GetUFrom(), uTo: req.GetUTo()}
			if v.uFrom > 0 && v.uTo == 0 {
				v.uTo = v.uFrom
			}
		}

		old, err := t.placements(id.Uuid())
		if err != nil {
			return err
		}
		tl, changed := old.set(at, v)
		if !changed {
			out = a
			return nil
		}
		if err := t.checkPlacements(id.Uuid(), tl, old, false); err != nil {
			return err
		}

		moved := app.AssetMoved_builder{Asset: id.Bytes(), Tag: a.GetTag()}.Build()
		if to != nil {
			moved.SetTo(to.GetId())
			moved.SetToTag(to.GetTag())
			moved.SetMode(v.mode)
			moved.SetSlot(v.slot)
			moved.SetUFrom(v.uFrom)
			moved.SetUTo(v.uTo)
		}
		if err := t.begin(req.GetOp(), "asset.move", id, at, req.GetReason(), moved); err != nil {
			return err
		}
		if err := t.writePlacements(id.Uuid(), old, tl); err != nil {
			return err
		}

		out, err = t.refresh(id.Uuid(), old, tl)
		return err
	})
	return s.settle(ctx, req.GetRef(), out, err)
}

// settle answers an operation: what it wrote, or -- when its op had already
// happened -- the asset as it is now, so that a retry reads as a success.
func (s domainAsset) settle(ctx context.Context, ref *app.AssetRef, out *app.Asset, err error) (*app.Asset, error) {
	if errors.Is(err, errDone) {
		return s.AssetServiceServer.Get(ctx, app.AssetGetRequest_builder{Ref: ref, Select: allAsset}.Build())
	}
	return out, err
}

func or(v, otherwise string) string {
	if v == "" {
		return otherwise
	}
	return v
}

// placements reads a child's current-knowledge placement rows.
func (t *Tx) placements(child uuid.UUID) (timeline[place], error) {
	rs, err := t.db.Placement.Query().
		Where(placement.TenantId(t.tenant.Uuid()), placement.ChildId(child), placement.SupersededAtIsNil()).
		Order(placement.ByValidFrom()).
		All(t.ctx)
	if err != nil {
		return nil, err
	}

	tl := timeline[place]{}
	for _, r := range rs {
		tl = append(tl, span[place]{
			id:    r.Id,
			from:  r.ValidFrom,
			to:    r.ValidTo,
			state: place{parent: r.ParentId, mode: r.Mode, slot: r.Slot, uFrom: r.UFrom, uTo: r.UTo},
		})
	}
	return tl, nil
}

// checkPlacements refuses a placement timeline that puts the child inside
// itself, or into a slot or rack position another asset holds at the time.
//
// A child this operation made (`fresh`) has nothing inside it, so no parent
// can be inside it, and the parents' chain is not walked for one.
func (t *Tx) checkPlacements(child uuid.UUID, tl, old timeline[place], fresh bool) error {
	_, added := tl.diff(old)
	for _, s := range added {
		p := s.state.parent
		if p == child {
			return invalid("to", "자산을 자기 안에 둘 수 없습니다")
		}

		// Its ancestors at the start, and at every change among them during
		// the span: a cycle is a property of a moment, and the tree lock is
		// what keeps the moments from moving while this looks.
		times := []time.Time{s.from}
		if s.contains(t.now) && !s.from.Equal(t.now) {
			times = append(times, t.now)
		}
		for i := 0; !fresh && i < len(times) && i < 32; i++ {
			chain, changes, err := t.ancestors(p, times[i], s)
			if err != nil {
				return err
			}
			if slices.Contains(chain, child) {
				return failed("그렇게 옮기면 자산이 자기 안에 들어가게 됩니다")
			}
			for _, c := range changes {
				if !slices.ContainsFunc(times, c.Equal) {
					times = append(times, c)
				}
			}
		}

		if s.state.slot != "" {
			n, err := t.db.Placement.Query().Where(
				placement.TenantId(t.tenant.Uuid()),
				placement.ParentId(p),
				placement.Slot(s.state.slot),
				placement.ChildIdNEQ(child),
				placement.SupersededAtIsNil(),
				overlapsPlacement(s.from, s.to),
			).Count(t.ctx)
			if err != nil {
				return err
			}
			if n > 0 {
				return failed("그 시점에 자리 %s는 이미 차 있습니다", s.state.slot)
			}
		}
		if s.state.uFrom > 0 {
			n, err := t.db.Placement.Query().Where(
				placement.TenantId(t.tenant.Uuid()),
				placement.ParentId(p),
				placement.UFromGT(0),
				placement.UFromLTE(s.state.uTo),
				placement.UToGTE(s.state.uFrom),
				placement.ChildIdNEQ(child),
				placement.SupersededAtIsNil(),
				overlapsPlacement(s.from, s.to),
			).Count(t.ctx)
			if err != nil {
				return err
			}
			if n > 0 {
				return failed("그 시점에 랙 %d–%dU는 이미 차 있습니다", s.state.uFrom, s.state.uTo)
			}
		}
	}
	return nil
}

// overlapsPlacement is a row whose valid span meets [from, to).
func overlapsPlacement(from time.Time, to *time.Time) predicate.Placement {
	ps := []predicate.Placement{placement.Or(placement.ValidToIsNil(), placement.ValidToGT(from))}
	if to != nil {
		ps = append(ps, placement.ValidFromLT(*to))
	}
	return placement.And(ps...)
}

// ancestors answers who holds `p` at `at`, all the way up, and the moments
// within `s` at which any of them moved.
func (t *Tx) ancestors(p uuid.UUID, at time.Time, s span[place]) ([]uuid.UUID, []time.Time, error) {
	chain := []uuid.UUID{p}
	changes := []time.Time{}
	cur := p
	for range 64 {
		rs, err := t.db.Placement.Query().Where(
			placement.TenantId(t.tenant.Uuid()),
			placement.ChildId(cur),
			placement.SupersededAtIsNil(),
		).All(t.ctx)
		if err != nil {
			return nil, nil, err
		}

		var up uuid.UUID
		for _, r := range rs {
			if s.contains(r.ValidFrom) && r.ValidFrom.After(s.from) {
				changes = append(changes, r.ValidFrom)
			}
			if !r.ValidFrom.After(at) && (r.ValidTo == nil || r.ValidTo.After(at)) {
				up = r.ParentId
			}
		}
		if up == (uuid.UUID{}) || slices.Contains(chain, up) {
			if up != (uuid.UUID{}) {
				chain = append(chain, up)
			}
			break
		}
		chain = append(chain, up)
		cur = up
	}
	return chain, changes, nil
}

// writePlacements writes what replacing `old` with `tl` means: the rows it
// supersedes first, then the rows it adds, which is the order the exclusion
// constraint needs (design 3.3).
func (t *Tx) writePlacements(child uuid.UUID, old, tl timeline[place]) error {
	gone, added := tl.diff(old)
	for _, id := range gone {
		if err := t.supersedePlacement(id); err != nil {
			return err
		}
	}
	for _, s := range added {
		req := app.PlacementAddRequest_builder{
			Tenant:    t.tenantRef(),
			Child:     assetRef(child),
			Parent:    assetRef(s.state.parent),
			Mode:      s.state.mode,
			Slot:      s.state.slot,
			UFrom:     s.state.uFrom,
			UTo:       s.state.uTo,
			ValidFrom: ts(s.from),
			EventId:   t.ev().Bytes(),
			// Known from the moment the rows it replaces stopped being known,
			// and not from whenever the write reads the clock; see
			// [Tx.supersedePlacement].
			DateCreated: ts(t.now),
		}.Build()
		if s.to != nil {
			req.SetValidTo(ts(*s.to))
		}
		if _, err := t.next.Placement().Add(t.ctx, req); err != nil {
			return err
		}
	}
	return nil
}

func (t *Tx) supersedePlacement(id uuid.UUID) error {
	_, err := t.next.Placement().Patch(t.ctx, app.PlacementPatchRequest_builder{
		Ref:              app.PlacementRef_builder{Id: pdid.Id(id).Bytes()}.Build(),
		SupersededAt:     ts(t.now),
		SupersededBy:     t.ev().Bytes(),
		DateUpdatedForce: z.Ptr(true),
	}.Build())
	return err
}

func (t *Tx) supersedeStewardship(id uuid.UUID) error {
	_, err := t.next.Stewardship().Patch(t.ctx, app.StewardshipPatchRequest_builder{
		Ref:              app.StewardshipRef_builder{Id: pdid.Id(id).Bytes()}.Build(),
		SupersededAt:     ts(t.now),
		SupersededBy:     t.ev().Bytes(),
		DateUpdatedForce: z.Ptr(true),
	}.Build())
	return err
}

func (t *Tx) supersedeLink(id uuid.UUID) error {
	_, err := t.next.Link().Patch(t.ctx, app.LinkPatchRequest_builder{
		Ref:              app.LinkRef_builder{Id: pdid.Id(id).Bytes()}.Build(),
		SupersededAt:     ts(t.now),
		SupersededBy:     t.ev().Bytes(),
		DateUpdatedForce: z.Ptr(true),
	}.Build())
	return err
}

// refresh writes where the child is now onto its row, and tells the containers
// it left and entered. It answers the asset as it is afterwards.
func (t *Tx) refresh(child uuid.UUID, old, tl timeline[place]) (*app.Asset, error) {
	was, hadWas := old.at(t.now)
	now, hasNow := tl.at(t.now)

	if hadWas != hasNow || was != now {
		req := app.AssetPatchRequest_builder{
			Ref:              assetRef(child),
			DateUpdatedForce: z.Ptr(true),
		}.Build()
		if hasNow {
			req.SetParentId(pdid.Id(now.parent).Bytes())
			req.SetPlacementMode(now.mode)
			req.SetSlot(now.slot)
		} else {
			req.SetParentIdNull(true)
			req.SetPlacementMode("")
			req.SetSlot("")
		}
		if _, err := t.next.Asset().Patch(t.ctx, req); err != nil {
			return nil, err
		}
		if hadWas {
			if err := t.bump(was.parent); err != nil {
				return nil, err
			}
		}
		if hasNow && (!hadWas || now.parent != was.parent) {
			if err := t.bump(now.parent); err != nil {
				return nil, err
			}
		}
	}

	a, _, err := t.get(assetRef(child), "ref")
	return a, err
}

// bump tells whoever watches a container that what is inside it changed --
// once a transaction: told once, they look once and see all of it. An import
// of a hundred things into one room told it a hundred times, each a write of
// its own through every layer (rove#103).
func (t *Tx) bump(container uuid.UUID) error {
	if t.shared != nil && t.shared.bumped[container] {
		return nil
	}
	v, err := t.db.Asset.Query().Where(asset.Id(container), asset.TenantId(t.tenant.Uuid())).Only(t.ctx)
	if err != nil {
		if ent.IsNotFound(err) {
			return nil
		}
		return err
	}
	_, err = t.next.Asset().Patch(t.ctx, app.AssetPatchRequest_builder{
		Ref:              assetRef(container),
		ContentVersion:   z.Ptr(v.ContentVersion + 1),
		DateUpdatedForce: z.Ptr(true),
	}.Build())
	if err == nil && t.shared != nil {
		t.shared.bumped[container] = true
	}
	return err
}

// addFact records that `key` is `value` from `at` on -- or, `cleared`, has
// none -- as this operation's fact of the asset (payday#151), which supersedes
// a fact recorded for the same field at the same moment.
func (t *Tx) addFact(id uuid.UUID, key, value string, cleared bool, at time.Time) error {
	v := &value
	if cleared {
		v = nil
	}
	f, err := valueOf(key, v)
	if err != nil {
		return err
	}
	return t.op.Set(t.ctx, past.Fact{Subject: pdid.Id(id), From: at, Values: []past.Value{f}})
}

// factsAt answers each asset's facts at `at`, by the keys Rove says them
// under, by what was recorded by `known` (or by now when it is nil): what
// payday's past says each was then (`past.StateAt`), as the text each was
// said in. An asset nothing is known of then is not in it.
func (t *Tx) factsAt(ids []uuid.UUID, at time.Time, known *time.Time) (map[uuid.UUID]map[string]string, error) {
	st, err := t.store()
	if err != nil {
		return nil, err
	}
	subjects := make([]pdid.Id, len(ids))
	for i, v := range ids {
		subjects[i] = pdid.Id(v)
	}
	states, err := past.StateAt(t.ctx, st, subjects, at, known)
	if err != nil {
		return nil, err
	}

	out := map[uuid.UUID]map[string]string{}
	for id, v := range states {
		m := map[string]string{}
		for slot, r := range v.Values {
			k, ok := KeyOf(slot)
			if !ok || r.Withheld() {
				continue
			}
			text, err := textOf(slot, r.Value)
			if err != nil {
				return nil, fmt.Errorf("fact %s: %w", r.Id, err)
			}
			m[k] = text
		}
		if len(m) > 0 {
			out[id.Uuid()] = m
		}
	}
	return out, nil
}

// SetAttributes changes what an asset is from a moment on.
func (s domainAsset) SetAttributes(ctx context.Context, req *app.AssetSetAttributesRequest) (*app.Asset, error) {
	var out *app.Asset
	err := s.tx(ctx, func(t *Tx) error {
		at, err := t.at(req.GetAt(), "at")
		if err != nil {
			return err
		}
		a, id, err := t.get(req.GetRef(), "ref")
		if err != nil {
			return err
		}
		if len(req.GetLeave()) > 0 && !slices.Contains(OutOfUse, strings.TrimSpace(req.GetSet()["status"])) {
			return invalid("leave", "키트·그룹에서 빼는 것은 상태를 분실·불용·폐기로 바꿀 때 함께 합니다. 따로 빼려면 관계를 끊으세요")
		}

		changes := map[string]*string{}
		for k, v := range req.GetSet() {
			changes[k] = z.Ptr(v)
		}
		for _, k := range req.GetClear() {
			changes[k] = nil
		}
		if len(changes) == 0 {
			out = a
			return nil
		}

		spec, err := t.specOfAsset(a)
		if err != nil {
			return err
		}
		set := map[string]string{}
		for k, v := range changes {
			key, err := factKey(k)
			if err != nil {
				return err
			}
			if v == nil {
				continue
			}
			val := strings.TrimSpace(*v)
			switch key {
			case "status":
				if !slices.Contains(Statuses, val) {
					return invalid("set", "상태가 올바르지 않습니다 (%s)", strings.Join(Statuses, ", "))
				}
			case "condition":
				if !slices.Contains(Conditions, val) {
					return invalid("set", "컨디션이 올바르지 않습니다 (%s)", strings.Join(Conditions, ", "))
				}
			case "name", "tag":
				if val == "" {
					return invalid("set", "%s 항목은 비울 수 없습니다", factName(key))
				}
			case "type":
				ty, err := t.next.AssetType().Get(t.ctx, app.AssetTypeGetRequest_builder{
					Ref:    app.AssetTypeRef_builder{Id: idFromString(val).Bytes()}.Build(),
					Select: app.AssetTypeSelect_builder{All: z.Ptr(true)}.Build(),
				}.Build())
				if err != nil {
					return err
				}
				spec = specOf(t, ty)
			case "model":
				if _, err := t.next.ItemModel().Get(t.ctx, app.ItemModelGetRequest_builder{
					Ref: app.ItemModelRef_builder{Id: idFromString(val).Bytes()}.Build(),
				}.Build()); err != nil {
					return err
				}
			}
			set[key] = val
		}
		attrs := map[string]string{}
		for k, v := range set {
			if after, ok := strings.CutPrefix(k, "attr."); ok {
				attrs[after] = v
			}
		}
		if err := checkAttributes(spec, attrs, false); err != nil {
			return err
		}

		said := app.AssetAttributesSet_builder{Asset: id.Bytes(), Tag: a.GetTag()}.Build()
		for _, k := range sortedKeys(changes) {
			key, _ := factKey(k)
			if changes[k] == nil {
				said.SetCleared(append(said.GetCleared(), key))
			} else {
				said.SetSet(append(said.GetSet(), key))
			}
		}
		if err := t.begin(req.GetOp(), "asset.set", id, at, req.GetReason(), said); err != nil {
			return err
		}
		for _, k := range sortedKeys(changes) {
			key, _ := factKey(k)
			v := changes[k]
			if v == nil {
				if err := t.addFact(id.Uuid(), key, "", true, at); err != nil {
					return err
				}
				continue
			}
			if err := t.addFact(id.Uuid(), key, set[key], false, at); err != nil {
				return err
			}
		}

		if out, err = t.sync(id.Uuid()); err != nil {
			return err
		}
		// What its leaving its kits and groups and what its status does to
		// its pools and to what books it hold, at once ([Tx.holdLeaving]).
		_, status := changes["status"]
		if status || len(req.GetLeave()) > 0 {
			if err := t.holdLeaving(id, req.GetLeave()); err != nil {
				return err
			}
		}
		if err := t.leave(id, at, req.GetLeave(), req.GetReason()); err != nil {
			return err
		}
		if !status {
			return nil
		}
		// Whether it is one of its pools' units: lost, retired or disposed,
		// it is not, and received, it is.
		if err := t.recheckPoolsOf(id.Uuid()); err != nil {
			return err
		}
		// And whether the reservations that still take it may be handed it,
		// which whoever has to act on it is told (D42).
		return t.outOfUseChanged(id.Uuid(), a.GetStatus())
	})
	return s.settle(ctx, req.GetRef(), out, err)
}

// factKey answers the fact a field of SetAttributes writes.
func factKey(k string) (string, error) {
	switch k {
	case "name", "desc", "status", "condition", "serial", "tag", "type", "model":
		return k, nil
	}
	if strings.HasPrefix(k, "attr.") && len(k) > len("attr.") {
		return k, nil
	}
	if isAttributeKey(k) {
		return "attr." + k, nil
	}
	return "", notAnItem("set", k)
}

// isAttributeKey says an attribute of an asset can be keyed by `k`: by
// something, with neither a space nor a dot in it -- a space makes two words
// of it, and a dot two parts of the key of its fact, `attr.` and it. Every
// attribute an asset is given is held to it, by SetAttributes ([factKey]), by
// Add and by bulk registration ([attributeKeys]); and so are the keys a
// type's spec defines (checkSpec) and a sheet's attribute columns (`속성.키`).
func isAttributeKey(k string) bool { return k != "" && !strings.ContainsAny(k, " .") }

// attributeKeys refuses attributes an asset is given under a key no attribute
// can have ([isAttributeKey]), on `field`, in the words SetAttributes refuses
// a field it does not know with: the first such key, in order.
func attributeKeys(field string, attrs map[string]string) error {
	for _, k := range sortedKeys(attrs) {
		if !isAttributeKey(k) {
			return notAnItem(field, k)
		}
	}
	return nil
}

// notAnItem is the refusal of `k`, which is no item of an asset.
func notAnItem(field, k string) error {
	return invalid(field, "%q는 자산의 항목이 아닙니다", k)
}

func idFromString(v string) pdid.Id {
	id, err := pdid.Parse(v)
	if err != nil {
		return pdid.Nil
	}
	return id
}

// sync writes an asset's facts as they are now onto its row.
func (t *Tx) sync(id uuid.UUID) (*app.Asset, error) {
	cur, err := t.db.Asset.Query().Where(asset.Id(id), asset.TenantId(t.tenant.Uuid())).Only(t.ctx)
	if err != nil {
		return nil, err
	}
	m, err := t.factsAt([]uuid.UUID{id}, t.now, nil)
	if err != nil {
		return nil, err
	}
	f := m[id]

	req := app.AssetPatchRequest_builder{Ref: assetRef(id), DateUpdatedForce: z.Ptr(true)}.Build()
	if v := f["name"]; v != "" && v != cur.Name {
		req.SetName(v)
	}
	if v := f["desc"]; v != cur.Desc {
		req.SetDesc(v)
	}
	if v := f["status"]; v != "" && v != cur.Status {
		req.SetStatus(v)
		if v == "disposed" {
			req.SetDisposedAt(ts(t.now))
		} else if cur.DisposedAt != nil {
			req.SetDisposedAtNull(true)
		}
	}
	if v := f["condition"]; v != "" && v != cur.Condition {
		req.SetCondition(v)
	}
	if v := f["serial"]; v != cur.Serial {
		req.SetSerial(v)
	}
	if v := f["tag"]; v != "" && v != cur.Tag {
		req.SetTag(v)
	}
	if v, ok := f["type"]; ok {
		if want := idFromString(v); want.Uuid() != cur.TypeId {
			req.SetType(app.AssetTypeRef_builder{Id: want.Bytes()}.Build())
		}
	} else if cur.TypeId != (uuid.UUID{}) {
		req.SetTypeNull(true)
	}
	if v, ok := f["model"]; ok {
		if want := idFromString(v); want.Uuid() != cur.ModelId {
			req.SetModel(app.ItemModelRef_builder{Id: want.Bytes()}.Build())
		}
	} else if cur.ModelId != (uuid.UUID{}) {
		req.SetModelNull(true)
	}
	attrs := map[string]string{}
	for k, v := range f {
		if after, ok := strings.CutPrefix(k, "attr."); ok {
			attrs[after] = v
		}
	}
	if !maps.Equal(attrs, cur.Attributes) {
		req.SetAttributes(attrs)
	}

	if _, err := t.next.Asset().Patch(t.ctx, req); err != nil {
		return nil, err
	}
	a, _, err := t.get(assetRef(id), "ref")
	return a, err
}

// Assign makes a party the owner or manager of an asset from a moment on, or
// ends the role.
func (s domainAsset) Assign(ctx context.Context, req *app.AssetAssignRequest) (*app.Asset, error) {
	var out *app.Asset
	err := s.tx(ctx, func(t *Tx) error {
		at, err := t.at(req.GetAt(), "at")
		if err != nil {
			return err
		}
		role := req.GetRole()
		switch role {
		case "owner", "manager":
		case "custodian":
			return invalid("role", "사용자는 지급·대여로 정합니다")
		default:
			return invalid("role", "역할은 소유 또는 관리 담당입니다")
		}
		a, id, err := t.get(req.GetRef(), "ref")
		if err != nil {
			return err
		}

		var v *steward
		said := app.AssetAssigned_builder{Asset: id.Bytes(), Tag: a.GetTag(), Role: role}.Build()
		if req.HasParty() {
			p, err := t.next.Party().Get(t.ctx, app.PartyGetRequest_builder{Ref: req.GetParty()}.Build())
			if err != nil {
				return err
			}
			v = &steward{party: uuidOf(p.GetId())}
			said.SetParty(p.GetId())
		}

		changed, err := t.steward(id.Uuid(), role, at, v, func() error {
			return t.begin(req.GetOp(), "asset.assign", id, at, req.GetReason(), said)
		})
		if err != nil {
			return err
		}
		_ = changed

		out, _, err = t.get(assetRef(id.Uuid()), "ref")
		return err
	})
	return s.settle(ctx, req.GetRef(), out, err)
}

// stewards reads an asset's current-knowledge rows in one role.
func (t *Tx) stewards(id uuid.UUID, role string) (timeline[steward], error) {
	rs, err := t.db.Stewardship.Query().
		Where(stewardship.TenantId(t.tenant.Uuid()), stewardship.AssetId(id), stewardship.Role(role), stewardship.SupersededAtIsNil()).
		Order(stewardship.ByValidFrom()).
		All(t.ctx)
	if err != nil {
		return nil, err
	}
	tl := timeline[steward]{}
	for _, r := range rs {
		tl = append(tl, span[steward]{id: r.Id, from: r.ValidFrom, to: r.ValidTo, state: steward{party: r.PartyId}})
	}
	return tl, nil
}

// steward sets who holds `role` from `at`, calling `begin` once it knows
// there is something to write.
func (t *Tx) steward(id uuid.UUID, role string, at time.Time, v *steward, begin func() error) (bool, error) {
	old, err := t.stewards(id, role)
	if err != nil {
		return false, err
	}
	tl, changed := old.set(at, v)
	if !changed {
		return false, nil
	}
	if t.op == nil {
		if err := begin(); err != nil {
			return false, err
		}
	}
	return true, t.writeStewards(id, role, old, tl)
}

func (t *Tx) writeStewards(id uuid.UUID, role string, old, tl timeline[steward]) error {
	gone, added := tl.diff(old)
	for _, v := range gone {
		if err := t.supersedeStewardship(v); err != nil {
			return err
		}
	}
	for _, s := range added {
		req := app.StewardshipAddRequest_builder{
			Tenant:    t.tenantRef(),
			Asset:     assetRef(id),
			Party:     app.PartyRef_builder{Id: pdid.Id(s.state.party).Bytes()}.Build(),
			Role:      role,
			ValidFrom: ts(s.from),
			EventId:   t.ev().Bytes(),
			// See [Tx.writePlacements].
			DateCreated: ts(t.now),
		}.Build()
		if s.to != nil {
			req.SetValidTo(ts(*s.to))
		}
		if _, err := t.next.Stewardship().Add(t.ctx, req); err != nil {
			return err
		}
	}

	if role != "custodian" {
		return nil
	}

	// The custodian is also on the asset's row, for lists and watches.
	was, hadWas := old.at(t.now)
	now, hasNow := tl.at(t.now)
	if hadWas == hasNow && was == now {
		return nil
	}
	req := app.AssetPatchRequest_builder{Ref: assetRef(id), DateUpdatedForce: z.Ptr(true)}.Build()
	if hasNow {
		req.SetCustodian(app.PartyRef_builder{Id: pdid.Id(now.party).Bytes()}.Build())
	} else {
		req.SetCustodianNull(true)
	}
	_, err := t.next.Asset().Patch(t.ctx, req)
	return err
}

// Relate adds or ends a logical relation from a moment on.
func (s domainAsset) Relate(ctx context.Context, req *app.AssetRelateRequest) (*app.Asset, error) {
	var out *app.Asset
	err := s.tx(ctx, func(t *Tx) error {
		at, err := t.at(req.GetAt(), "at")
		if err != nil {
			return err
		}
		kind := req.GetKind()
		if kind == "" {
			kind = "member_of"
		}
		if !slices.Contains(LinkKinds, kind) {
			return invalid("kind", "관계 종류가 올바르지 않습니다 (%s)", strings.Join(LinkKinds, ", "))
		}
		a, id, err := t.get(req.GetRef(), "ref")
		if err != nil {
			return err
		}
		b, target, err := t.get(req.GetTarget(), "target")
		if err != nil {
			return err
		}
		if id == target {
			return invalid("target", "자산을 자기 자신과 연결할 수 없습니다")
		}
		if kind == "member_of" && b.GetKind() != "kit" && b.GetKind() != "group" {
			return invalid("target", "구성품은 키트나 그룹에만 연결할 수 있습니다")
		}

		old, err := t.links(id.Uuid(), target.Uuid(), kind)
		if err != nil {
			return err
		}
		var v *linked
		if !req.GetEnd() {
			v = &linked{required: req.GetRequired()}
		}
		tl, changed := old.set(at, v)
		if !changed {
			out = a
			return nil
		}

		if err := t.begin(req.GetOp(), "asset.relate", id, at, req.GetReason(), app.AssetRelated_builder{
			Asset:     id.Bytes(),
			Tag:       a.GetTag(),
			Target:    target.Bytes(),
			TargetTag: b.GetTag(),
			Relation:  kind,
			End:       req.GetEnd(),
			Required:  v != nil && v.required,
		}.Build()); err != nil {
			return err
		}
		if err := t.writeLinks(id.Uuid(), target.Uuid(), kind, old, tl); err != nil {
			return err
		}
		if err := t.bump(target.Uuid()); err != nil {
			return err
		}
		if kind == "member_of" {
			// What the kit is made of is what its bookings take, and what a
			// pool is made of is what it has to give.
			if err := t.recheckKits(target.Uuid()); err != nil {
				return err
			}
			if err := t.recheckPools([]uuid.UUID{target.Uuid()}, []uuid.UUID{id.Uuid()}); err != nil {
				return err
			}
		}

		out, _, err = t.get(assetRef(id.Uuid()), "ref")
		return err
	})
	return s.settle(ctx, req.GetRef(), out, err)
}

// leave ends the memberships of `id` of these kits and groups as it goes out
// of use from `at` (AssetSetAttributesRequest.leave): each a relation of its
// own, called through the layer, so that it is part of this operation (design
// D34) and the bookings of a kit let it go as they do on any change of what
// the kit is made of (design 4).
//
// What ends is the membership open now, which is what a kit's bookings
// follow: from when it went out of use, or now when it was made a member
// after that -- the record said so until now, and no row of it is said not to
// have held -- and as it begins when that is yet to come. One it does not hold
// now is passed over, since what offered it may have read it before a change
// that ended it.
func (t *Tx) leave(id pdid.Id, at time.Time, refs []*app.AssetRef, reason string) error {
	for i, ref := range refs {
		field := fmt.Sprintf("leave[%d]", i)
		b, target, err := t.get(ref, field)
		if err != nil {
			return err
		}
		if b.GetKind() != "kit" && b.GetKind() != "group" {
			return invalid(field, "%s은(는) 키트나 그룹이 아닙니다", b.GetTag())
		}
		l, err := t.db.Link.Query().Where(
			link.TenantId(t.tenant.Uuid()),
			link.SourceId(id.Uuid()),
			link.TargetId(target.Uuid()),
			link.Kind("member_of"),
			link.SupersededAtIsNil(),
			link.ValidToIsNil(),
		).First(t.ctx)
		if ent.IsNotFound(err) {
			continue
		}
		if err != nil {
			return err
		}
		end := at
		if l.ValidFrom.After(end) {
			end = t.now
		}
		if l.ValidFrom.After(end) {
			end = l.ValidFrom
		}
		if _, err := t.layer().Asset().Relate(t.ctx, app.AssetRelateRequest_builder{
			Ref:    assetRef(id.Uuid()),
			Target: assetRef(target.Uuid()),
			Kind:   "member_of",
			End:    true,
			At:     ts(end),
			Reason: reason,
		}.Build()); err != nil {
			return err
		}
	}
	return nil
}

func (t *Tx) links(source, target uuid.UUID, kind string) (timeline[linked], error) {
	rs, err := t.db.Link.Query().
		Where(link.TenantId(t.tenant.Uuid()), link.SourceId(source), link.TargetId(target), link.Kind(kind), link.SupersededAtIsNil()).
		Order(link.ByValidFrom()).
		All(t.ctx)
	if err != nil {
		return nil, err
	}
	tl := timeline[linked]{}
	for _, r := range rs {
		tl = append(tl, span[linked]{id: r.Id, from: r.ValidFrom, to: r.ValidTo, state: linked{required: r.Required}})
	}
	return tl, nil
}

func (t *Tx) writeLinks(source, target uuid.UUID, kind string, old, tl timeline[linked]) error {
	gone, added := tl.diff(old)
	for _, v := range gone {
		if err := t.supersedeLink(v); err != nil {
			return err
		}
	}
	for _, s := range added {
		req := app.LinkAddRequest_builder{
			Tenant:    t.tenantRef(),
			Source:    assetRef(source),
			Target:    assetRef(target),
			Kind:      kind,
			Required:  s.state.required,
			ValidFrom: ts(s.from),
			EventId:   t.ev().Bytes(),
			// See [Tx.writePlacements].
			DateCreated: ts(t.now),
		}.Build()
		if s.to != nil {
			req.SetValidTo(ts(*s.to))
		}
		if _, err := t.next.Link().Add(t.ctx, req); err != nil {
			return err
		}
	}
	return nil
}

// Correct fixes a time row that was wrong: it never happened, or it happened
// at another moment. The row is superseded and what replaces it is written
// new, so what the system said before stays readable (design 3.3).
func (s domainAsset) Correct(ctx context.Context, req *app.AssetCorrectRequest) (*app.Asset, error) {
	var out *app.Asset
	err := s.tx(ctx, func(t *Tx) error {
		if strings.TrimSpace(req.GetReason()) == "" {
			return invalid("reason", "정정 사유를 적어 주세요")
		}
		if !req.GetRetract() && !req.HasValidFrom() {
			return invalid("valid_from", "실제 시점을 적거나, 없었던 일로 취소하세요")
		}
		if err := t.lockTree(); err != nil {
			return err
		}
		var when time.Time
		if req.HasValidFrom() {
			var err error
			if when, err = t.at(req.GetValidFrom(), "valid_from"); err != nil {
				return err
			}
		}
		if err := t.done(req.GetOp()); err != nil {
			return err
		}
		a, id, err := t.get(req.GetRef(), "ref")
		if err != nil {
			return err
		}
		row := uuidOf(req.GetRowId())
		// What it corrects: the row, and -- of a stewardship and a fact,
		// whose rows are of more than one sort -- which.
		begin := func(role, key string) error {
			said := app.AssetCorrected_builder{
				Asset:   id.Bytes(),
				Tag:     a.GetTag(),
				Row:     pdid.Id(row).Bytes(),
				Role:    role,
				Key:     key,
				Retract: req.GetRetract(),
			}.Build()
			if !req.GetRetract() {
				said.SetValidFrom(ts(when))
			}
			return t.begin(req.GetOp(), "asset.correct", id, t.now, req.GetReason(), said)
		}

		switch pdid.Id(row).Domain() {
		case pd.PlacementDomain:
			old, err := t.placements(id.Uuid())
			if err != nil {
				return err
			}
			tl, gone, ok := old.retract(row)
			if !ok {
				return status.Error(codes.NotFound, "이 자산의 현재 위치 기록이 아닙니다")
			}
			if !req.GetRetract() {
				tl, _ = tl.set(when, &gone.state)
			}
			if err := t.checkPlacements(id.Uuid(), tl, old, false); err != nil {
				return err
			}
			if err := begin("", ""); err != nil {
				return err
			}
			if err := t.writePlacements(id.Uuid(), old, tl); err != nil {
				return err
			}
			out, err = t.refresh(id.Uuid(), old, tl)
			return err

		case pd.StewardshipDomain:
			r, err := t.db.Stewardship.Query().Where(stewardship.Id(row), stewardship.TenantId(t.tenant.Uuid()), stewardship.AssetId(id.Uuid()), stewardship.SupersededAtIsNil()).Only(t.ctx)
			if err != nil {
				return status.Error(codes.NotFound, "이 자산의 현재 담당 기록이 아닙니다")
			}
			old, err := t.stewards(id.Uuid(), r.Role)
			if err != nil {
				return err
			}
			tl, gone, _ := old.retract(row)
			if !req.GetRetract() {
				tl, _ = tl.set(when, &gone.state)
			}
			if err := begin(r.Role, ""); err != nil {
				return err
			}
			if err := t.writeStewards(id.Uuid(), r.Role, old, tl); err != nil {
				return err
			}

		case pd.LinkDomain:
			r, err := t.db.Link.Query().Where(link.Id(row), link.TenantId(t.tenant.Uuid()), link.SourceId(id.Uuid()), link.SupersededAtIsNil()).Only(t.ctx)
			if err != nil {
				return status.Error(codes.NotFound, "이 자산의 현재 관계 기록이 아닙니다")
			}
			old, err := t.links(id.Uuid(), r.TargetId, r.Kind)
			if err != nil {
				return err
			}
			tl, gone, _ := old.retract(row)
			if !req.GetRetract() {
				tl, _ = tl.set(when, &gone.state)
			}
			if err := begin("", ""); err != nil {
				return err
			}
			if err := t.writeLinks(id.Uuid(), r.TargetId, r.Kind, old, tl); err != nil {
				return err
			}
			if r.Kind == "member_of" {
				if err := t.recheckKits(r.TargetId); err != nil {
					return err
				}
				if err := t.recheckPools([]uuid.UUID{r.TargetId}, []uuid.UUID{id.Uuid()}); err != nil {
					return err
				}
			}

		case pd.FactDomain:
			r, err := t.db.Fact.Query().Where(fact.Id(row), fact.TenantId(t.tenant.Uuid()), fact.SubjectId(id.Uuid()), fact.SupersededAtIsNil()).Only(t.ctx)
			if err != nil {
				return status.Error(codes.NotFound, "이 자산의 현재 항목 기록이 아닙니다")
			}
			key, _ := KeyOf(past.Slot{Field: protoreflect.FieldNumber(r.FieldNumber), Key: r.Key})
			if err := begin("", key); err != nil {
				return err
			}
			// Superseded, and -- unless it never held -- said again from
			// when it did, as this correction's (payday#151).
			if req.GetRetract() {
				err = t.op.Retract(t.ctx, pdid.Id(row))
			} else {
				err = t.op.Correct(t.ctx, pdid.Id(row), when)
			}
			if err != nil {
				return err
			}
			if out, err = t.sync(id.Uuid()); err != nil {
				return err
			}
			if key != "status" {
				return nil
			}
			// Whether it is one of its pools' units, and whether the
			// reservations that take it may be handed it -- or may book it
			// at all (D43): what both hold, at once ([Tx.holdLeaving]).
			if err := t.holdLeaving(id, nil); err != nil {
				return err
			}
			if err := t.recheckPoolsOf(id.Uuid()); err != nil {
				return err
			}
			return t.outOfUseChanged(id.Uuid(), a.GetStatus())

		default:
			return invalid("row_id", "정정할 수 있는 기록이 아닙니다")
		}

		out, _, err = t.get(assetRef(id.Uuid()), "ref")
		return err
	})
	return s.settle(ctx, req.GetRef(), out, err)
}

// specOf reads an asset type's spec, with what it inherits.
func specOf(t *Tx, ty *app.AssetType) *app.TypeSpec {
	out := &app.TypeSpec{}
	seen := map[string]bool{}
	cur := ty
	for range 16 {
		if cur == nil {
			break
		}
		spec := cur.GetSpec()
		for _, a := range spec.GetAttributes() {
			if !seen[a.GetKey()] {
				seen[a.GetKey()] = true
				out.SetAttributes(append(out.GetAttributes(), a))
			}
		}
		out.SetCapabilities(append(out.GetCapabilities(), spec.GetCapabilities()...))
		p := idOf(cur.GetParentId())
		if p.IsZero() {
			break
		}
		next, err := t.next.AssetType().Get(t.ctx, app.AssetTypeGetRequest_builder{
			Ref:    app.AssetTypeRef_builder{Id: p.Bytes()}.Build(),
			Select: app.AssetTypeSelect_builder{All: z.Ptr(true)}.Build(),
		}.Build())
		if err != nil {
			break
		}
		cur = next
	}
	return out
}

func (t *Tx) specOfAsset(a *app.Asset) (*app.TypeSpec, error) {
	ty := idOf(a.GetType().GetId())
	if ty.IsZero() {
		return nil, nil
	}
	v, err := t.next.AssetType().Get(t.ctx, app.AssetTypeGetRequest_builder{
		Ref:    app.AssetTypeRef_builder{Id: ty.Bytes()}.Build(),
		Select: app.AssetTypeSelect_builder{All: z.Ptr(true)}.Build(),
	}.Build())
	if err != nil {
		return nil, err
	}
	return specOf(t, v), nil
}

// checkAttributes refuses values a type does not allow: a number that is not
// one, an option that is not offered, and -- when an asset is made -- a
// required attribute left out.
func checkAttributes(spec *app.TypeSpec, attrs map[string]string, creating bool) error {
	if spec == nil {
		return nil
	}
	defs := map[string]*app.AttributeDef{}
	for _, d := range spec.GetAttributes() {
		defs[d.GetKey()] = d
	}
	for k, v := range attrs {
		d, ok := defs[k]
		if !ok || v == "" {
			continue
		}
		switch d.GetType() {
		case "number":
			if _, err := strconv.ParseFloat(v, 64); err != nil {
				return invalid("attributes", "%s은(는) 숫자입니다", d.GetLabel())
			}
		case "bool":
			if v != "true" && v != "false" {
				return invalid("attributes", "%s은(는) 예 또는 아니오입니다", d.GetLabel())
			}
		case "date":
			if _, err := time.Parse(time.DateOnly, v); err != nil {
				return invalid("attributes", "%s은(는) 날짜입니다 (예: 2024-03-01)", d.GetLabel())
			}
		case "enum":
			if !slices.Contains(d.GetOptions(), v) {
				return invalid("attributes", "%s은(는) %s 중 하나입니다", d.GetLabel(), strings.Join(d.GetOptions(), ", "))
			}
		}
	}
	if creating {
		for _, d := range spec.GetAttributes() {
			if d.GetRequired() && attrs[d.GetKey()] == "" {
				return invalid("attributes", "%s을(를) 입력하세요", d.GetLabel())
			}
		}
	}
	return nil
}
