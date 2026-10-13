package domain

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/lesomnus/payday/pdid"
	"github.com/lesomnus/z"
	"google.golang.org/grpc/codes"
	grpcstatus "google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"

	app "github.com/lesomnus/rove"
	"github.com/lesomnus/rove/internal/ent"
	"github.com/lesomnus/rove/internal/ent/allocation"
	"github.com/lesomnus/rove/internal/ent/purchaseline"
	"github.com/lesomnus/rove/internal/ent/stock"
)

var (
	WorkKinds    = []string{"repair", "inspection", "maintenance", "rma"}
	WorkStatuses = []string{"open", "scheduled", "in_progress", "done", "cancelled"}
)

type domainWorkOrder struct {
	Domain
	app.WorkOrderServiceServer
}

func (s Domain) WorkOrder() app.WorkOrderServiceServer {
	return domainWorkOrder{s, s.Next().WorkOrder()}
}

// Add opens a work order. One that blocks takes the asset's time from its
// beginning to its end, as maintenance, which reservations then cannot take
// (design 4).
func (s domainWorkOrder) Add(ctx context.Context, req *app.WorkOrderAddRequest) (*app.WorkOrder, error) {
	var out *app.WorkOrder
	err := s.tx(ctx, func(t *Tx) error {
		a, aid, err := t.get(req.GetAsset(), "asset")
		if err != nil {
			return err
		}
		kind := or(req.GetKind(), "repair")
		if !slices.Contains(WorkKinds, kind) {
			return invalid("kind", "작업 구분이 올바르지 않습니다 (%s)", strings.Join(WorkKinds, ", "))
		}
		if strings.TrimSpace(req.GetName()) == "" {
			return invalid("name", "작업 내용을 적어 주세요")
		}
		mgr := manages(t.roleOf())
		if !mgr {
			// What a member can do is report something broken. When it is
			// looked at, by whom and for how much is a manager's to decide.
			if kind != "repair" {
				return grpcstatus.Error(codes.PermissionDenied, "구성원은 고장 신고만 할 수 있습니다")
			}
			if req.GetBlocking() || req.HasBeginsAt() || req.HasEndsAt() || req.GetEveryDays() != 0 || req.GetCost() != 0 || req.HasVendor() {
				return grpcstatus.Error(codes.PermissionDenied, "일정과 비용은 매니저가 정합니다")
			}
		}
		status := "open"
		if req.HasBeginsAt() {
			status = "scheduled"
		}
		if req.HasBeginsAt() && req.HasEndsAt() && !req.GetEndsAt().AsTime().After(req.GetBeginsAt().AsTime()) {
			return invalid("ends_at", "끝은 시작보다 뒤여야 합니다")
		}
		if err := t.begin(nil, "work.open", aid, t.now, fmt.Sprintf("작업 열림: %s (%s)", req.GetName(), a.GetTag()), "", map[string]string{"kind": kind}); err != nil {
			return err
		}
		row := app.WorkOrderAddRequest_builder{
			Tenant:    t.tenantRef(),
			Name:      strings.TrimSpace(req.GetName()),
			Desc:      req.GetDesc(),
			Asset:     app.AssetRef_builder{Id: aid.Bytes()}.Build(),
			Kind:      kind,
			Status:    status,
			Cost:      req.GetCost(),
			Currency:  or(req.GetCurrency(), "KRW"),
			Blocking:  req.GetBlocking(),
			EveryDays: req.GetEveryDays(),
		}.Build()
		if req.HasBeginsAt() {
			row.SetBeginsAt(req.GetBeginsAt())
		}
		if req.HasEndsAt() {
			row.SetEndsAt(req.GetEndsAt())
		}
		if req.HasVendor() {
			row.SetVendor(req.GetVendor())
		}
		w, err := t.next.WorkOrder().Add(t.ctx, row)
		if err != nil {
			return err
		}
		if err := t.block(w); err != nil {
			return err
		}
		if !mgr {
			if err := t.notifyManagers("work.reported", "고장 신고: "+a.GetTag()+" "+a.GetName(), w.GetName(), idOf(w.GetId()), "/work"); err != nil {
				return err
			}
		}
		out = w
		return nil
	})
	return out, err
}

// block keeps a work order's maintenance allocation in step with it.
func (t *Tx) block(w *app.WorkOrder) error {
	wid := idOf(w.GetId())
	as, err := t.db.Allocation.Query().Where(allocation.TenantId(t.tenant.Uuid()), allocation.RefId(wid.Uuid()), allocation.Kind("maintenance")).All(t.ctx)
	if err != nil {
		return err
	}
	want := w.GetBlocking() && w.HasBeginsAt() && w.HasEndsAt() && !slices.Contains([]string{"done", "cancelled"}, w.GetStatus())
	if !want {
		for _, a := range as {
			if a.Blocking {
				if _, err := t.next.Allocation().Patch(t.ctx, app.AllocationPatchRequest_builder{
					Ref:              app.AllocationRef_builder{Id: pdid.Id(a.Id).Bytes()}.Build(),
					Blocking:         z.Ptr(false),
					DateUpdatedForce: z.Ptr(true),
				}.Build()); err != nil {
					return err
				}
			}
		}
		return nil
	}
	if len(as) > 0 {
		_, err := t.next.Allocation().Patch(t.ctx, app.AllocationPatchRequest_builder{
			Ref:              app.AllocationRef_builder{Id: pdid.Id(as[0].Id).Bytes()}.Build(),
			BeginsAt:         w.GetBeginsAt(),
			EndsAt:           w.GetEndsAt(),
			Blocking:         z.Ptr(true),
			DateUpdatedForce: z.Ptr(true),
		}.Build())
		return err
	}
	_, err = t.next.Allocation().Add(t.ctx, app.AllocationAddRequest_builder{
		Tenant:    t.tenantRef(),
		Resource:  app.AssetRef_builder{Id: w.GetAsset().GetId()}.Build(),
		Kind:      "maintenance",
		RefId:     wid.Bytes(),
		BeginsAt:  w.GetBeginsAt(),
		EndsAt:    w.GetEndsAt(),
		Blocking:  true,
		Exclusive: true,
		Units:     1,
	}.Build())
	return err
}

func (s domainWorkOrder) Update(ctx context.Context, req *app.WorkOrderUpdateRequest) (*app.WorkOrder, error) {
	var out *app.WorkOrder
	err := s.tx(ctx, func(t *Tx) error {
		w, err := t.next.WorkOrder().Get(t.ctx, app.WorkOrderGetRequest_builder{Ref: req.GetRef()}.Build())
		if err != nil {
			return err
		}
		patch := app.WorkOrderPatchRequest_builder{Ref: req.GetRef(), DateUpdatedForce: z.Ptr(true), Blocking: z.Ptr(req.GetBlocking())}.Build()
		if v := strings.TrimSpace(req.GetName()); v != "" {
			patch.SetName(v)
		}
		if v := req.GetDesc(); v != "" {
			patch.SetDesc(v)
		}
		if v := req.GetStatus(); v != "" {
			if !slices.Contains(WorkStatuses, v) || v == "done" || v == "cancelled" {
				return invalid("status", "상태는 접수, 예정, 진행 중 가운데 하나입니다. 끝내려면 완료나 취소를 쓰세요")
			}
			patch.SetStatus(v)
		}
		if req.HasBeginsAt() {
			patch.SetBeginsAt(req.GetBeginsAt())
		}
		if req.HasEndsAt() {
			patch.SetEndsAt(req.GetEndsAt())
		}
		if req.GetCost() != 0 {
			patch.SetCost(req.GetCost())
		}
		if v := req.GetCurrency(); v != "" {
			patch.SetCurrency(v)
		}
		if err := t.begin(nil, "work.update", idOf(w.GetAsset().GetId()), t.now, "작업 수정: "+w.GetName(), "", nil); err != nil {
			return err
		}
		if out, err = t.next.WorkOrder().Patch(t.ctx, patch); err != nil {
			return err
		}
		if req.GetStatus() == "in_progress" && w.GetKind() == "repair" {
			if _, err := t.layer().Asset().SetAttributes(t.ctx, app.AssetSetAttributesRequest_builder{
				Ref:    app.AssetRef_builder{Id: w.GetAsset().GetId()}.Build(),
				Set:    map[string]string{"status": "in_repair"},
				Reason: "수리 시작: " + w.GetName(),
			}.Build()); err != nil {
				return err
			}
		}
		return t.block(out)
	})
	return out, err
}

// Complete closes a work order. The asset comes back from repair, in the
// condition the work left it, and a recurring inspection opens its next one.
func (s domainWorkOrder) Complete(ctx context.Context, req *app.WorkOrderCompleteRequest) (*app.WorkOrder, error) {
	return s.finish(ctx, req, "done")
}

func (s domainWorkOrder) Cancel(ctx context.Context, req *app.WorkOrderCompleteRequest) (*app.WorkOrder, error) {
	return s.finish(ctx, req, "cancelled")
}

func (s domainWorkOrder) finish(ctx context.Context, req *app.WorkOrderCompleteRequest, state string) (*app.WorkOrder, error) {
	var out *app.WorkOrder
	err := s.tx(ctx, func(t *Tx) error {
		w, err := t.next.WorkOrder().Get(t.ctx, app.WorkOrderGetRequest_builder{Ref: req.GetRef()}.Build())
		if err != nil {
			return err
		}
		if w.GetStatus() == "done" || w.GetStatus() == "cancelled" {
			out = w
			return nil
		}
		a, aid, err := t.get(app.AssetRef_builder{Id: w.GetAsset().GetId()}.Build(), "asset")
		if err != nil {
			return err
		}
		if err := t.begin(nil, "work."+state, aid, t.now, fmt.Sprintf("작업 %s: %s", map[string]string{"done": "완료", "cancelled": "취소"}[state], w.GetName()), req.GetReason(), nil); err != nil {
			return err
		}
		patch := app.WorkOrderPatchRequest_builder{Ref: req.GetRef(), Status: z.Ptr(state), DateUpdatedForce: z.Ptr(true)}.Build()
		if state == "done" {
			patch.SetCompletedAt(ts(t.now))
			if req.GetCost() != 0 {
				patch.SetCost(req.GetCost())
			}
		}
		if out, err = t.next.WorkOrder().Patch(t.ctx, patch); err != nil {
			return err
		}
		if err := t.block(out); err != nil {
			return err
		}

		set := map[string]string{}
		if a.GetStatus() == "in_repair" {
			set["status"] = "active"
		}
		if c := req.GetCondition(); c != "" && slices.Contains(Conditions, c) && c != a.GetCondition() {
			set["condition"] = c
		}
		if len(set) > 0 {
			if _, err := t.layer().Asset().SetAttributes(t.ctx, app.AssetSetAttributesRequest_builder{
				Ref:    app.AssetRef_builder{Id: aid.Bytes()}.Build(),
				Set:    set,
				Reason: "작업 " + state + ": " + w.GetName(),
			}.Build()); err != nil {
				return err
			}
		}

		if state == "done" && w.GetEveryDays() > 0 {
			next := t.now.AddDate(0, 0, int(w.GetEveryDays()))
			row := app.WorkOrderAddRequest_builder{
				Tenant:    t.tenantRef(),
				Name:      w.GetName(),
				Desc:      w.GetDesc(),
				Asset:     app.AssetRef_builder{Id: aid.Bytes()}.Build(),
				Kind:      w.GetKind(),
				Status:    "scheduled",
				BeginsAt:  timestamppb.New(next),
				Currency:  w.GetCurrency(),
				Blocking:  w.GetBlocking(),
				EveryDays: w.GetEveryDays(),
			}.Build()
			if w.HasBeginsAt() && w.HasEndsAt() {
				row.SetEndsAt(timestamppb.New(next.Add(w.GetEndsAt().AsTime().Sub(w.GetBeginsAt().AsTime()))))
			}
			nw, err := t.next.WorkOrder().Add(t.ctx, row)
			if err != nil {
				return err
			}
			if err := t.block(nw); err != nil {
				return err
			}
		}
		return nil
	})
	return out, err
}

type domainPurchase struct {
	Domain
	app.PurchaseServiceServer
}

func (s Domain) Purchase() app.PurchaseServiceServer { return domainPurchase{s, s.Next().Purchase()} }

// Add records a purchase and what was bought in it.
func (s domainPurchase) Add(ctx context.Context, req *app.PurchaseAddRequest) (*app.Purchase, error) {
	var out *app.Purchase
	err := s.tx(ctx, func(t *Tx) error {
		if len(req.GetLines()) == 0 {
			return invalid("lines", "구매한 품목을 적어 주세요")
		}
		if req.HasVendor() {
			v, err := t.next.Party().Get(t.ctx, app.PartyGetRequest_builder{Ref: req.GetVendor()}.Build())
			if err != nil {
				return err
			}
			if v.GetKind() != "vendor" {
				return invalid("vendor", "업체는 거래처여야 합니다")
			}
		}
		var total int64
		for i, l := range req.GetLines() {
			if l.GetQuantity() <= 0 {
				return invalid(fmt.Sprintf("lines[%d].quantity", i), "수량은 1 이상입니다")
			}
			as := or(l.GetReceiveAs(), "asset")
			if as != "asset" && as != "stock" {
				return invalid(fmt.Sprintf("lines[%d].receive_as", i), "입고 형태는 자산 또는 재고입니다")
			}
			if as == "stock" && !l.HasModel() {
				return invalid(fmt.Sprintf("lines[%d].model", i), "재고로 받으려면 모델을 정하세요")
			}
			total += l.GetQuantity() * l.GetUnitCost()
		}
		name := strings.TrimSpace(req.GetName())
		if name == "" {
			name = "구매 " + t.now.Format("2006-01-02")
		}
		if err := t.begin(nil, "purchase.add", pdid.Nil, t.now, "구매 등록: "+name, "", map[string]string{"total": fmt.Sprint(total)}); err != nil {
			return err
		}
		row := app.PurchaseAddRequest_builder{
			Tenant:    t.tenantRef(),
			Name:      name,
			Desc:      req.GetDesc(),
			Reference: req.GetReference(),
			Status:    "ordered",
			OrderedAt: ts(t.now),
			Currency:  or(req.GetCurrency(), "KRW"),
			Total:     total,
		}.Build()
		if req.HasOrderedAt() {
			row.SetOrderedAt(req.GetOrderedAt())
		}
		if req.HasVendor() {
			row.SetVendor(req.GetVendor())
		}
		p, err := t.next.Purchase().Add(t.ctx, row)
		if err != nil {
			return err
		}
		for _, l := range req.GetLines() {
			line := app.PurchaseLineAddRequest_builder{
				Tenant:    t.tenantRef(),
				Desc:      l.GetDesc(),
				Purchase:  app.PurchaseRef_builder{Id: p.GetId()}.Build(),
				Quantity:  l.GetQuantity(),
				UnitCost:  l.GetUnitCost(),
				ReceiveAs: or(l.GetReceiveAs(), "asset"),
			}.Build()
			if l.HasModel() {
				line.SetModel(l.GetModel())
			}
			if _, err := t.next.PurchaseLine().Add(t.ctx, line); err != nil {
				return err
			}
		}
		out = p
		return nil
	})
	return out, err
}

// Receive turns what arrived into assets or stock in a space.
func (s domainPurchase) Receive(ctx context.Context, req *app.PurchaseReceiveRequest) (*app.PurchaseReceiveResponse, error) {
	var out *app.PurchaseReceiveResponse
	err := s.tx(ctx, func(t *Tx) error {
		at, err := t.at(req.GetAt(), "at")
		if err != nil {
			return err
		}
		p, err := t.next.Purchase().Get(t.ctx, app.PurchaseGetRequest_builder{Ref: req.GetRef()}.Build())
		if err != nil {
			return err
		}
		if p.GetStatus() != "ordered" {
			return failed("이 구매는 %s 상태입니다", or(purchaseSay[p.GetStatus()], p.GetStatus()))
		}
		into, intoId, err := t.get(req.GetInto(), "into")
		if err != nil {
			return err
		}
		if into.GetKind() != "space" {
			return invalid("into", "입고는 공간으로 받습니다")
		}
		lines, err := t.db.PurchaseLine.Query().Where(purchaseline.TenantId(t.tenant.Uuid()), purchaseline.PurchaseId(uuidOf(p.GetId()))).All(t.ctx)
		if err != nil {
			return err
		}
		if err := t.begin(nil, "purchase.receive", pdid.Nil, at, "입고: "+p.GetName(), "", nil); err != nil {
			return err
		}

		out = &app.PurchaseReceiveResponse{}
		layer := t.layer()
		n := 0
		for _, l := range lines {
			left := l.Quantity - l.ReceivedQuantity
			if left <= 0 {
				continue
			}
			var m *app.ItemModel
			if l.ModelId != [16]byte{} {
				if m, err = t.next.ItemModel().Get(t.ctx, app.ItemModelGetRequest_builder{Ref: app.ItemModelRef_builder{Id: pdid.Id(l.ModelId).Bytes()}.Build()}.Build()); err != nil {
					return err
				}
			}
			switch l.ReceiveAs {
			case "stock":
				st, err := t.db.Stock.Query().Where(stock.TenantId(t.tenant.Uuid()), stock.ModelId(l.ModelId), stock.SpaceId(intoId.Uuid()), stock.DateErasedIsNil()).Only(t.ctx)
				var v *app.Stock
				switch {
				case err == nil:
					v, err = t.next.Stock().Get(t.ctx, app.StockGetRequest_builder{Ref: app.StockRef_builder{Id: pdid.Id(st.Id).Bytes()}.Build()}.Build())
				case ent.IsNotFound(err):
					v, err = t.next.Stock().Add(t.ctx, app.StockAddRequest_builder{
						Tenant: t.tenantRef(),
						Name:   m.GetName(),
						Model:  app.ItemModelRef_builder{Id: m.GetId()}.Build(),
						Space:  app.AssetRef_builder{Id: intoId.Bytes()}.Build(),
						Unit:   "개",
					}.Build())
				}
				if err != nil {
					return err
				}
				if err := t.moveStock(v, left, "receive", idOf(p.GetId()), at); err != nil {
					return err
				}
				out.SetStocked(out.GetStocked() + uint32(left))
			default:
				for range left {
					n++
					add := app.AssetAddRequest_builder{
						Name:       or(m.GetName(), or(l.Desc, "구매 자산")),
						Kind:       "item",
						To:         app.AssetRef_builder{Id: intoId.Bytes()}.Build(),
						AcquiredAt: ts(at),
						Since:      ts(at),
						Reason:     z.Ptr("입고: " + p.GetName()),
						Attributes: map[string]string{},
					}.Build()
					if m != nil {
						add.SetModel(app.ItemModelRef_builder{Id: m.GetId()}.Build())
						if ty := m.GetType().GetId(); len(ty) > 0 {
							add.SetType(app.AssetTypeRef_builder{Id: ty}.Build())
						}
					}
					if pre := strings.TrimSpace(req.GetTagPrefix()); pre != "" {
						add.SetTag(fmt.Sprintf("%s-%03d", pre, n))
					}
					a, err := layer.Asset().Add(t.ctx, add)
					if err != nil {
						return err
					}
					out.SetAssets(append(out.GetAssets(), a))
				}
			}
			if _, err := t.next.PurchaseLine().Patch(t.ctx, app.PurchaseLinePatchRequest_builder{
				Ref:              app.PurchaseLineRef_builder{Id: pdid.Id(l.Id).Bytes()}.Build(),
				ReceivedQuantity: z.Ptr(l.Quantity),
				DateUpdatedForce: z.Ptr(true),
			}.Build()); err != nil {
				return err
			}
		}
		v, err := t.next.Purchase().Patch(t.ctx, app.PurchasePatchRequest_builder{
			Ref:              req.GetRef(),
			Status:           z.Ptr("received"),
			ReceivedAt:       ts(at),
			DateUpdatedForce: z.Ptr(true),
		}.Build())
		if err != nil {
			return err
		}
		out.SetPurchase(v)
		return nil
	})
	return out, err
}
