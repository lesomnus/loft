package domain

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"uuid"

	"github.com/lesomnus/payday/pdid"
	"github.com/lesomnus/z"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	app "github.com/lesomnus/rove"
	"github.com/lesomnus/rove/internal/ent"
	"github.com/lesomnus/rove/internal/ent/asset"
	"github.com/lesomnus/rove/internal/ent/countfinding"
	"github.com/lesomnus/rove/internal/ent/event"
	"github.com/lesomnus/rove/server/pd"
)

type domainCount struct {
	Domain
	app.InventoryCountServiceServer
}

func (s Domain) InventoryCount() app.InventoryCountServiceServer {
	return domainCount{s, s.Next().InventoryCount()}
}

// Add starts a count of what is under a space.
func (s domainCount) Add(ctx context.Context, req *app.InventoryCountAddRequest) (*app.InventoryCount, error) {
	var out *app.InventoryCount
	err := s.tx(ctx, func(t *Tx) error {
		sp, _, err := t.get(req.GetScope(), "scope")
		if err != nil {
			return err
		}
		if sp.GetKind() != "space" {
			return invalid("scope", "실사 범위는 공간입니다")
		}
		name := strings.TrimSpace(req.GetName())
		if name == "" {
			name = fmt.Sprintf("%s 실사 %s", sp.GetName(), t.now.In(t.zone()).Format("2006-01-02"))
		}
		id := pdid.New(pd.InventoryCountDomain)
		if err := t.begin(nil, "count.start", idOf(sp.GetId()), t.now, "", app.CountStartedOrClosed_builder{
			Count: id.Bytes(),
			Name:  name,
			Scope: sp.GetId(),
		}.Build()); err != nil {
			return err
		}
		out, err = t.next.InventoryCount().Add(t.ctx, app.InventoryCountAddRequest_builder{
			Id:          id.Bytes(),
			Tenant:      t.tenantRef(),
			Name:        name,
			Desc:        req.GetDesc(),
			Scope:       req.GetScope(),
			Status:      "open",
			StartedAt:   ts(t.now),
			StartedBy:   t.actor.Bytes(),
			SelfService: req.GetSelfService(),
		}.Build())
		return err
	})
	return out, err
}

// Scan records that something was seen, where. The moment is the device's:
// a scan made offline arrives later and still says when it was seen.
func (s domainCount) Scan(ctx context.Context, req *app.InventoryCountScanRequest) (*app.CountFinding, error) {
	var out *app.CountFinding
	err := s.tx(ctx, func(t *Tx) error {
		// A scan from a queue that did not hear back arrives again, maybe
		// once the count is closed.
		if err := t.done(req.GetOp()); err != nil {
			return err
		}
		c, err := t.next.InventoryCount().Get(t.ctx, app.InventoryCountGetRequest_builder{Ref: req.GetRef()}.Build())
		if err != nil {
			return err
		}
		if c.GetStatus() != "open" {
			return failed("종료된 실사입니다")
		}
		if !c.GetSelfService() && !manages(t.roleOf()) {
			return status.Error(codes.PermissionDenied, "이 실사는 매니저만 스캔합니다. 자가 실사에서는 누구나 스캔할 수 있습니다")
		}
		seen, err := t.at(req.GetSeenAt(), "seen_at")
		if err != nil {
			return err
		}
		where := idOf(c.GetScope().GetId())
		if req.HasAt() {
			sp, id, err := t.get(req.GetAt(), "at")
			if err != nil {
				return err
			}
			if sp.GetKind() != "space" {
				return invalid("at", "발견 위치는 공간이어야 합니다")
			}
			where = id
		}

		// What was scanned.
		var a *app.Asset
		var aid, label pdid.Id
		switch {
		case req.HasAsset():
			if a, aid, err = t.get(req.GetAsset(), "asset"); err != nil {
				return err
			}
		case strings.TrimSpace(req.GetCode()) != "":
			res, err := t.layer().Label().Resolve(t.ctx, app.LabelResolveRequest_builder{Code: req.GetCode()}.Build())
			if err != nil {
				if status.Code(err) != codes.NotFound {
					return err
				}
			} else {
				a = res.GetAsset()
				aid = idOf(a.GetId())
				label = idOf(res.GetLabel().GetId())
			}
			if a == nil {
				if id, ok := ParseLabel(req.GetCode()); ok {
					label = id
				}
			}
		default:
			return invalid("code", "스캔한 코드를 보내 주세요")
		}

		scanned := app.CountScanned_builder{
			Count: c.GetId(),
			Space: where.Bytes(),
			Code:  strings.TrimSpace(req.GetCode()),
		}.Build()
		if !aid.IsZero() {
			scanned.SetAsset(aid.Bytes())
		}
		if !label.IsZero() {
			scanned.SetLabel(label.Bytes())
		}
		if err := t.begin(req.GetOp(), "count.scan", aid, seen, req.GetNote(), scanned); err != nil {
			return err
		}
		if err := t.findingsChanged(c); err != nil {
			return err
		}

		row := app.CountFindingAddRequest_builder{
			Id:               made(req.GetOp(), pd.CountFindingDomain, 0).Bytes(),
			Tenant:           t.tenantRef(),
			Desc:             req.GetNote(),
			Count:            app.InventoryCountRef_builder{Id: c.GetId()}.Build(),
			ObservedParentId: where.Bytes(),
			SeenAt:           ts(seen),
			RecordedBy:       t.actor.Bytes(),
			Resolution:       "open",
		}.Build()
		if !label.IsZero() {
			row.SetLabelId(label.Bytes())
		}
		if a == nil {
			row.SetKind("unknown")
			out, err = t.next.CountFinding().Add(t.ctx, row)
			return err
		}

		expected := idOf(a.GetParentId())
		row.SetAsset(app.AssetRef_builder{Id: aid.Bytes()}.Build())
		if !expected.IsZero() {
			row.SetExpectedParentId(expected.Bytes())
		}
		kind := "seen"
		if expected != where {
			kind = "misplaced"
		}
		row.SetKind(kind)
		if kind == "seen" {
			row.SetResolution("ok")
		}

		// One finding per asset per count: a second scan says where it was
		// seen last.
		prev, err := t.db.CountFinding.Query().Where(
			countfinding.TenantId(t.tenant.Uuid()),
			countfinding.CountId(uuidOf(c.GetId())),
			countfinding.AssetId(aid.Uuid()),
		).First(t.ctx)
		if err == nil {
			out, err = t.next.CountFinding().Patch(t.ctx, app.CountFindingPatchRequest_builder{
				Ref:              app.CountFindingRef_builder{Id: pdid.Id(prev.Id).Bytes()}.Build(),
				Kind:             z.Ptr(kind),
				ObservedParentId: where.Bytes(),
				SeenAt:           ts(seen),
				Resolution:       z.Ptr(row.GetResolution()),
				DateUpdatedForce: z.Ptr(true),
			}.Build())
			return err
		}
		out, err = t.next.CountFinding().Add(t.ctx, row)
		return err
	})
	if err == errDone {
		return s.scanned(ctx, req)
	}
	return out, err
}

// scanned answers a scan that already happened with the finding it left: the
// one it made, by the name it gave it, or -- when it was another scan of a
// thing this count had seen -- the one it updated, which is that thing's.
func (s domainCount) scanned(ctx context.Context, req *app.InventoryCountScanRequest) (*app.CountFinding, error) {
	v, err := s.Next().CountFinding().Get(ctx, app.CountFindingGetRequest_builder{
		Ref: app.CountFindingRef_builder{Id: made(req.GetOp(), pd.CountFindingDomain, 0).Bytes()}.Build(),
	}.Build())
	if status.Code(err) != codes.NotFound {
		return v, err
	}

	t, err := s.read(ctx)
	if err != nil {
		return nil, err
	}
	ev, err := t.db.Event.Query().Where(event.Id(uuidOf(req.GetOp())), event.TenantId(t.tenant.Uuid())).Only(ctx)
	if err != nil {
		return nil, err
	}
	if ev.SubjectId == nil {
		return nil, status.Error(codes.NotFound, "스캔이 남긴 결과를 찾을 수 없습니다")
	}
	c, err := s.InventoryCountServiceServer.Get(ctx, app.InventoryCountGetRequest_builder{Ref: req.GetRef()}.Build())
	if err != nil {
		return nil, err
	}
	f, err := t.db.CountFinding.Query().Where(
		countfinding.TenantId(t.tenant.Uuid()),
		countfinding.CountId(uuidOf(c.GetId())),
		countfinding.AssetId(*ev.SubjectId),
	).First(ctx)
	if err != nil {
		if ent.IsNotFound(err) {
			return nil, status.Error(codes.NotFound, "스캔이 남긴 결과를 찾을 수 없습니다")
		}
		return nil, err
	}
	return s.Next().CountFinding().Get(ctx, app.CountFindingGetRequest_builder{
		Ref: app.CountFindingRef_builder{Id: pdid.Id(f.Id).Bytes()}.Build(),
	}.Build())
}

// Reconcile marks what was expected under the scope and not seen.
func (s domainCount) Reconcile(ctx context.Context, req *app.InventoryCountReconcileRequest) (*app.InventoryCountReconcileResponse, error) {
	var out *app.InventoryCountReconcileResponse
	err := s.tx(ctx, func(t *Tx) error {
		c, err := t.next.InventoryCount().Get(t.ctx, app.InventoryCountGetRequest_builder{Ref: req.GetRef()}.Build())
		if err != nil {
			return err
		}
		cid := uuidOf(c.GetId())
		under, err := t.descendants(uuidOf(c.GetScope().GetId()))
		if err != nil {
			return err
		}
		expected, err := t.db.Asset.Query().Where(
			asset.TenantId(t.tenant.Uuid()),
			asset.IdIn(under...),
			asset.KindIn("item", "kit"),
			asset.StatusNotIn("disposed", "retired", "lost"),
		).All(t.ctx)
		if err != nil {
			return err
		}
		fs, err := t.db.CountFinding.Query().Where(countfinding.TenantId(t.tenant.Uuid()), countfinding.CountId(cid)).All(t.ctx)
		if err != nil {
			return err
		}
		found := map[uuid.UUID]bool{}
		out = &app.InventoryCountReconcileResponse{}
		for _, f := range fs {
			switch f.Kind {
			case "seen":
				out.SetSeen(out.GetSeen() + 1)
			case "misplaced":
				out.SetMisplaced(out.GetMisplaced() + 1)
			case "unknown":
				out.SetUnknown(out.GetUnknown() + 1)
			case "missing":
				out.SetMissing(out.GetMissing() + 1)
			}
			if f.AssetId != (uuid.UUID{}) {
				found[f.AssetId] = true
			}
		}

		var missing []*ent.Asset
		for _, a := range expected {
			if !found[a.Id] {
				missing = append(missing, a)
			}
		}
		if len(missing) == 0 {
			return nil
		}
		if err := t.begin(nil, "count.reconcile", pdid.Nil, t.now, "", app.CountReconciled_builder{
			Count:   c.GetId(),
			Missing: uint32(len(missing)),
		}.Build()); err != nil {
			return err
		}
		if err := t.findingsChanged(c); err != nil {
			return err
		}
		for _, a := range missing {
			row := app.CountFindingAddRequest_builder{
				Tenant:     t.tenantRef(),
				Count:      app.InventoryCountRef_builder{Id: c.GetId()}.Build(),
				Asset:      assetRef(a.Id),
				Kind:       "missing",
				SeenAt:     ts(t.now),
				RecordedBy: t.actor.Bytes(),
				Resolution: "open",
			}.Build()
			if a.ParentId != nil {
				row.SetExpectedParentId(pdid.Id(*a.ParentId).Bytes())
			}
			if _, err := t.next.CountFinding().Add(t.ctx, row); err != nil {
				return err
			}
			out.SetMissing(out.GetMissing() + 1)
		}
		return nil
	})
	return out, err
}

// Resolve settles a finding. "moved" records the asset where it was seen, from
// when it was seen -- a backdated move, which the time rows take as any other
// fact (design 3.3).
func (s domainCount) Resolve(ctx context.Context, req *app.InventoryCountResolveRequest) (*app.CountFinding, error) {
	var out *app.CountFinding
	err := s.tx(ctx, func(t *Tx) error {
		f, err := t.next.CountFinding().Get(t.ctx, app.CountFindingGetRequest_builder{Ref: req.GetRef()}.Build())
		if err != nil {
			return err
		}
		res := req.GetResolution()
		if !slices.Contains([]string{"moved", "lost", "ignored"}, res) {
			return invalid("resolution", "처리는 위치 반영, 분실, 무시 가운데 하나입니다")
		}
		aid := idOf(f.GetAsset().GetId())
		// Before what it calls: the move or the loss it records is part of
		// this, and says so (design D34).
		resolved := app.CountResolved_builder{
			Count:      f.GetCount().GetId(),
			Finding:    f.GetId(),
			Resolution: res,
		}.Build()
		if !aid.IsZero() {
			resolved.SetAsset(aid.Bytes())
		}
		if err := t.begin(nil, "count.resolve", aid, t.now, req.GetReason(), resolved); err != nil {
			return err
		}
		layer := t.layer()
		switch res {
		case "moved":
			if aid.IsZero() || len(f.GetObservedParentId()) == 0 {
				return failed("발견된 위치가 있는 것만 옮길 수 있습니다")
			}
			if _, err := layer.Asset().Move(t.ctx, app.AssetMoveRequest_builder{
				Ref:    app.AssetRef_builder{Id: aid.Bytes()}.Build(),
				To:     app.AssetRef_builder{Id: f.GetObservedParentId()}.Build(),
				At:     f.GetSeenAt(),
				Reason: req.GetReason(),
			}.Build()); err != nil {
				return err
			}
		case "lost":
			if aid.IsZero() {
				return failed("등록된 자산만 분실 처리할 수 있습니다")
			}
			if _, err := layer.Asset().SetAttributes(t.ctx, app.AssetSetAttributesRequest_builder{
				Ref:    app.AssetRef_builder{Id: aid.Bytes()}.Build(),
				Set:    map[string]string{"status": "lost"},
				Reason: req.GetReason(),
			}.Build()); err != nil {
				return err
			}
		}
		c, err := t.next.InventoryCount().Get(t.ctx, app.InventoryCountGetRequest_builder{Ref: app.InventoryCountRef_builder{Id: f.GetCount().GetId()}.Build()}.Build())
		if err != nil {
			return err
		}
		if err := t.findingsChanged(c); err != nil {
			return err
		}
		out, err = t.next.CountFinding().Patch(t.ctx, app.CountFindingPatchRequest_builder{
			Ref:              req.GetRef(),
			Resolution:       z.Ptr(res),
			ResolvedAt:       ts(t.now),
			DateUpdatedForce: z.Ptr(true),
		}.Build())
		return err
	})
	return out, err
}

func (s domainCount) Close(ctx context.Context, req *app.InventoryCountCloseRequest) (*app.InventoryCount, error) {
	var out *app.InventoryCount
	err := s.tx(ctx, func(t *Tx) error {
		c, err := t.next.InventoryCount().Get(t.ctx, app.InventoryCountGetRequest_builder{Ref: req.GetRef()}.Build())
		if err != nil {
			return err
		}
		if c.GetStatus() == "closed" {
			out = c
			return nil
		}
		if err := t.begin(nil, "count.close", idOf(c.GetScope().GetId()), t.now, "", app.CountStartedOrClosed_builder{
			Count: c.GetId(),
			Name:  c.GetName(),
			Scope: c.GetScope().GetId(),
		}.Build()); err != nil {
			return err
		}
		out, err = t.next.InventoryCount().Patch(t.ctx, app.InventoryCountPatchRequest_builder{
			Ref:              req.GetRef(),
			Status:           z.Ptr("closed"),
			ClosedAt:         ts(t.now),
			DateUpdatedForce: z.Ptr(true),
		}.Build())
		return err
	})
	return out, err
}

// findingsChanged tells whoever watches a count that its findings changed:
// a page drawing its progress watches the count's row, since a watch names
// the rows it follows and a finding that is new is named by nobody (design
// 9.6). It is written before the finding, so two scans into one count take
// their turns at the count's row.
func (t *Tx) findingsChanged(c *app.InventoryCount) error {
	_, err := t.next.InventoryCount().Patch(t.ctx, app.InventoryCountPatchRequest_builder{
		Ref:              app.InventoryCountRef_builder{Id: c.GetId()}.Build(),
		FindingsVersion:  z.Ptr(c.GetFindingsVersion() + 1),
		DateUpdatedForce: z.Ptr(true),
	}.Build())
	return err
}

var _ = pd.CountFindingDomain
