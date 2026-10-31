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
	"github.com/lesomnus/loft/internal/ent/stock"
	"github.com/lesomnus/loft/server/pd"
)

type domainStock struct {
	Domain
	app.StockServiceServer
}

func (s Domain) Stock() app.StockServiceServer { return domainStock{s, s.Next().Stock()} }

// Add starts keeping a model in a space, with what is there already.
func (s domainStock) Add(ctx context.Context, req *app.StockAddRequest) (*app.Stock, error) {
	id := req.GetId()
	if len(id) == 0 {
		id = made(req.GetOp(), pd.StockDomain, 0).Bytes()
	}
	var out *app.Stock
	err := s.tx(ctx, func(t *Tx) error {
		// Before the stock already there is refused: sent again, it is this.
		if err := t.done(req.GetOp()); err != nil {
			return err
		}
		m, err := t.next.ItemModel().Get(t.ctx, app.ItemModelGetRequest_builder{Ref: req.GetModel()}.Build())
		if err != nil {
			return err
		}
		sp, spid, err := t.get(req.GetSpace(), "space")
		if err != nil {
			return err
		}
		if sp.GetKind() != "space" {
			return invalid("space", "재고는 공간에 보관합니다")
		}
		dup, err := t.db.Stock.Query().Where(
			stock.TenantId(t.tenant.Uuid()),
			stock.ModelId(uuidOf(m.GetId())),
			stock.SpaceId(spid.Uuid()),
			stock.DateErasedIsNil(),
		).Exist(t.ctx)
		if err != nil {
			return err
		}
		if dup {
			return failed("%s은(는) 이미 %s에 재고가 있습니다. 그 재고의 수량을 바꾸세요", m.GetName(), sp.GetName())
		}
		if req.GetQuantity() < 0 || req.GetThreshold() < 0 {
			return invalid("quantity", "수량은 0 이상입니다")
		}

		row := proto.Clone(req).(*app.StockAddRequest)
		row.SetId(id)
		row.SetTenant(t.tenantRef())
		row.SetName(or(strings.TrimSpace(row.GetName()), m.GetName()))
		row.SetQuantity(0)
		if row.GetUnit() == "" {
			row.SetUnit("개")
		}
		if err := t.begin(req.GetOp(), "stock.add", pdid.Nil, t.now, "", app.StockAdded_builder{
			Stock:     id,
			Name:      row.GetName(),
			Model:     m.GetId(),
			Space:     spid.Bytes(),
			SpaceName: sp.GetName(),
			Quantity:  req.GetQuantity(),
		}.Build()); err != nil {
			return err
		}
		v, err := t.next.Stock().Add(t.ctx, row)
		if err != nil {
			return err
		}
		if q := req.GetQuantity(); q > 0 {
			if err := t.moveStock(v, q, "receive", pdid.Nil, t.now); err != nil {
				return err
			}
		}
		out, err = t.next.Stock().Get(t.ctx, app.StockGetRequest_builder{Ref: app.StockRef_builder{Id: v.GetId()}.Build()}.Build())
		return err
	})
	if err == errDone {
		return s.StockServiceServer.Get(ctx, app.StockGetRequest_builder{Ref: app.StockRef_builder{Id: id}.Build()}.Build())
	}
	return out, err
}

// lockStocks holds these stocks' rows, one at a time in identifier order. An
// operation that moves stock between two holds both before it moves either:
// taking each as it moved, two transfers between the same two shelves in
// opposite directions at once each held one and waited for the other, and
// PostgreSQL ended it by refusing one as a deadlock.
func (t *Tx) lockStocks(ids ...uuid.UUID) error {
	us := slices.Clone(ids)
	slices.SortFunc(us, func(a, b uuid.UUID) int { return a.Compare(b) })
	for _, u := range slices.Compact(us) {
		if err := t.db.Stock.Update().
			Where(stock.Id(u), stock.TenantId(t.tenant.Uuid())).
			AddQuantity(0).
			Exec(t.ctx); err != nil {
			return err
		}
	}
	return nil
}

// moveStock changes a stock by `delta` and writes the movement, refusing to go
// below zero and telling the managers when it falls to its threshold.
//
// It is part of an operation that has begun, whose event the movement points
// at: every one that moves stock records its own first, saying what it was --
// a receipt, a hand-over, a transfer -- and a movement outside one is a
// mistake here, refused before anything is written.
func (t *Tx) moveStock(st *app.Stock, delta int64, reason string, ref pdid.Id, at time.Time) error {
	if t.op == nil {
		return status.Errorf(codes.Internal, "domain: stock moved (%s) outside an operation, which records no event for it", reason)
	}

	// The row's lock before its quantity is read, so that two changes at once
	// land one after the other, each from what the last left, rather than both
	// from the same quantity and the second writing over the first. A plain
	// UPDATE that changes nothing, as the tree lock is ([Tx.lockTree]): what
	// matters is that the statement takes the row's lock.
	if err := t.db.Stock.Update().
		Where(stock.Id(uuidOf(st.GetId())), stock.TenantId(t.tenant.Uuid())).
		AddQuantity(0).
		Exec(t.ctx); err != nil {
		return err
	}
	cur, err := t.db.Stock.Query().Where(stock.Id(uuidOf(st.GetId())), stock.TenantId(t.tenant.Uuid())).Only(t.ctx)
	if err != nil {
		return err
	}
	next := cur.Quantity + delta
	if next < 0 {
		return failed("%s은(는) %d%s 남아 %d%s를 뺄 수 없습니다", cur.Name, cur.Quantity, cur.Unit, -delta, cur.Unit)
	}
	if _, err := t.next.Stock().Patch(t.ctx, app.StockPatchRequest_builder{
		Ref:              app.StockRef_builder{Id: st.GetId()}.Build(),
		Quantity:         z.Ptr(next),
		DateUpdatedForce: z.Ptr(true),
	}.Build()); err != nil {
		return err
	}
	req := app.StockMovementAddRequest_builder{
		Tenant:     t.tenantRef(),
		Stock:      app.StockRef_builder{Id: st.GetId()}.Build(),
		Delta:      delta,
		Reason:     reason,
		OccurredAt: ts(at),
		EventId:    t.ev().Bytes(),
		Balance:    next,
	}.Build()
	if !ref.IsZero() {
		req.SetRefId(ref.Bytes())
	}
	if _, err := t.next.StockMovement().Add(t.ctx, req); err != nil {
		return err
	}
	if delta < 0 && cur.Threshold > 0 && next <= cur.Threshold && cur.Quantity > cur.Threshold {
		return t.notifyManagers("stock.low", "재고 부족: "+cur.Name, fmt.Sprintf("%d%s 남았습니다 (기준 %d)", next, cur.Unit, cur.Threshold), pdid.Id(cur.Id), "/stock")
	}
	return nil
}

func (s domainStock) change(ctx context.Context, req *app.StockChangeRequest, reason string, sign int64) (*app.Stock, error) {
	var out *app.Stock
	err := s.tx(ctx, func(t *Tx) error {
		if err := t.done(req.GetOp()); err != nil {
			return err
		}
		at, err := t.at(req.GetAt(), "at")
		if err != nil {
			return err
		}
		st, err := t.next.Stock().Get(t.ctx, app.StockGetRequest_builder{Ref: req.GetRef()}.Build())
		if err != nil {
			return err
		}
		q := req.GetQuantity()
		if sign != 0 && q <= 0 {
			return invalid("quantity", "수량은 1 이상입니다")
		}
		if sign == 0 && q == 0 {
			return invalid("quantity", "늘리거나 줄일 수량을 적어 주세요")
		}
		if sign != 0 {
			q *= sign
		}
		if err := t.begin(req.GetOp(), "stock."+reason, pdid.Nil, at, req.GetReason(), app.StockChanged_builder{Stock: st.GetId(), Name: st.GetName(), Delta: q}.Build()); err != nil {
			return err
		}
		if err := t.moveStock(st, q, reason, pdid.Nil, at); err != nil {
			return err
		}
		out, err = t.next.Stock().Get(t.ctx, app.StockGetRequest_builder{Ref: req.GetRef()}.Build())
		return err
	})
	if err == errDone {
		return s.StockServiceServer.Get(ctx, app.StockGetRequest_builder{Ref: req.GetRef()}.Build())
	}
	return out, err
}

func (s domainStock) Receive(ctx context.Context, req *app.StockChangeRequest) (*app.Stock, error) {
	return s.change(ctx, req, "receive", 1)
}

func (s domainStock) Consume(ctx context.Context, req *app.StockChangeRequest) (*app.Stock, error) {
	return s.change(ctx, req, "consume", -1)
}

// Adjust corrects a stock to what a count found, by a signed quantity.
func (s domainStock) Adjust(ctx context.Context, req *app.StockChangeRequest) (*app.Stock, error) {
	return s.change(ctx, req, "adjust", 0)
}

// Transfer moves a quantity to the same model's stock in another space,
// starting that stock when there is none.
func (s domainStock) Transfer(ctx context.Context, req *app.StockTransferRequest) (*app.Stock, error) {
	var out *app.Stock
	err := s.tx(ctx, func(t *Tx) error {
		if err := t.done(req.GetOp()); err != nil {
			return err
		}
		at, err := t.at(req.GetAt(), "at")
		if err != nil {
			return err
		}
		q := req.GetQuantity()
		if q <= 0 {
			return invalid("quantity", "수량은 1 이상입니다")
		}
		st, err := t.next.Stock().Get(t.ctx, app.StockGetRequest_builder{Ref: req.GetRef()}.Build())
		if err != nil {
			return err
		}
		sp, spid, err := t.get(req.GetTo(), "to")
		if err != nil {
			return err
		}
		if sp.GetKind() != "space" {
			return invalid("to", "재고는 공간에 보관합니다")
		}
		if err := t.begin(req.GetOp(), "stock.transfer", pdid.Nil, at, req.GetReason(), app.StockTransferred_builder{
			Stock:       st.GetId(),
			Name:        st.GetName(),
			Quantity:    q,
			ToSpace:     spid.Bytes(),
			ToSpaceName: sp.GetName(),
		}.Build()); err != nil {
			return err
		}

		dst, err := t.db.Stock.Query().Where(
			stock.TenantId(t.tenant.Uuid()),
			stock.ModelId(uuidOf(st.GetModel().GetId())),
			stock.SpaceId(spid.Uuid()),
			stock.DateErasedIsNil(),
		).Only(t.ctx)
		var target *app.Stock
		switch {
		case err == nil:
			target, err = t.next.Stock().Get(t.ctx, app.StockGetRequest_builder{Ref: app.StockRef_builder{Id: pdid.Id(dst.Id).Bytes()}.Build()}.Build())
			if err != nil {
				return err
			}
		case ent.IsNotFound(err):
			target, err = t.next.Stock().Add(t.ctx, app.StockAddRequest_builder{
				Tenant:    t.tenantRef(),
				Name:      st.GetName(),
				Model:     app.ItemModelRef_builder{Id: st.GetModel().GetId()}.Build(),
				Space:     app.AssetRef_builder{Id: spid.Bytes()}.Build(),
				Threshold: st.GetThreshold(),
				Unit:      st.GetUnit(),
			}.Build())
			if err != nil {
				return err
			}
		default:
			return err
		}

		// Both held before either moves ([Tx.lockStocks]).
		if err := t.lockStocks(uuidOf(st.GetId()), uuidOf(target.GetId())); err != nil {
			return err
		}
		if err := t.moveStock(st, -q, "transfer_out", pdid.Nil, at); err != nil {
			return err
		}
		if err := t.moveStock(target, q, "transfer_in", pdid.Nil, at); err != nil {
			return err
		}
		out, err = t.next.Stock().Get(t.ctx, app.StockGetRequest_builder{Ref: req.GetRef()}.Build())
		return err
	})
	if err == errDone {
		return s.StockServiceServer.Get(ctx, app.StockGetRequest_builder{Ref: req.GetRef()}.Build())
	}
	return out, err
}

// Convert takes units out of a stock and registers each as an asset of its
// own, in the stock's space: a monitor that turned out to be worth tracking.
//
// It is one operation, and makes what it makes as a bulk registration does
// ([Tx.makeAssets]): each asset's registration a child of it, all of them a
// few hundred rows to a statement, rather than each by an Add of its own.
func (s domainStock) Convert(ctx context.Context, req *app.StockConvertRequest) (*app.StockConvertResponse, error) {
	var out *app.StockConvertResponse
	err := s.tx(ctx, func(t *Tx) error {
		if err := t.done(req.GetOp()); err != nil {
			return err
		}
		q := req.GetQuantity()
		if q <= 0 || q > 200 {
			return invalid("quantity", "한 번에 1~200개까지 전환합니다")
		}
		// What it makes is put in the stock's space, a change to the tree:
		// its lock before the moment ([Tx.lockTree]).
		if err := t.lockTree(); err != nil {
			return err
		}
		st, err := t.next.Stock().Get(t.ctx, app.StockGetRequest_builder{Ref: req.GetRef()}.Build())
		if err != nil {
			return err
		}
		m, err := t.next.ItemModel().Get(t.ctx, app.ItemModelGetRequest_builder{Ref: app.ItemModelRef_builder{Id: st.GetModel().GetId()}.Build()}.Build())
		if err != nil {
			return err
		}

		// Each unit an asset of the stock's model, in its space, checked as
		// bulk registration checks what it makes. Named after the
		// conversion (`made`), for a copy of it sent again to answer with.
		items := make([]*app.AssetAddRequest, q)
		for i := range items {
			add := app.AssetAddRequest_builder{
				Name:  m.GetName(),
				Kind:  "item",
				Model: app.ItemModelRef_builder{Id: m.GetId()}.Build(),
				To:    app.AssetRef_builder{Id: st.GetSpace().GetId()}.Build(),
			}.Build()
			if ty := m.GetType().GetId(); len(ty) > 0 {
				add.SetType(app.AssetTypeRef_builder{Id: ty}.Build())
			}
			if p := strings.TrimSpace(req.GetTagPrefix()); p != "" {
				add.SetTag(fmt.Sprintf("%s-%03d", p, i+1))
			}
			items[i] = add
		}
		in, err := t.intake(items, req.GetOp())
		if err != nil {
			return err
		}
		if len(in.issues) > 0 {
			// The stock's model, type and space are what the request names.
			return in.refusalAs(map[string]string{"tag": "tag_prefix", "type": "ref", "model": "ref", "attributes": "ref", "to": "ref"})
		}

		if err := t.begin(req.GetOp(), "stock.convert", pdid.Nil, t.now, "", app.StockConverted_builder{
			Stock:    st.GetId(),
			Name:     st.GetName(),
			Quantity: q,
		}.Build()); err != nil {
			return err
		}
		if err := t.moveStock(st, -q, "convert", pdid.Nil, t.now); err != nil {
			return err
		}
		assets, err := t.makeAssets(in, "")
		if err != nil {
			return err
		}

		st, err = t.next.Stock().Get(t.ctx, app.StockGetRequest_builder{Ref: req.GetRef()}.Build())
		out = app.StockConvertResponse_builder{Stock: st, Assets: assets}.Build()
		return err
	})
	if err == errDone {
		return s.converted(ctx, req)
	}
	return out, err
}

// converted answers a conversion that already happened: the stock as it is
// now, and the assets it made, by the names it gave them.
func (s domainStock) converted(ctx context.Context, req *app.StockConvertRequest) (*app.StockConvertResponse, error) {
	st, err := s.StockServiceServer.Get(ctx, app.StockGetRequest_builder{Ref: req.GetRef()}.Build())
	if err != nil {
		return nil, err
	}
	out := app.StockConvertResponse_builder{Stock: st}.Build()
	for i := range req.GetQuantity() {
		a, err := s.Next().Asset().Get(ctx, app.AssetGetRequest_builder{
			Ref:    app.AssetRef_builder{Id: made(req.GetOp(), pd.AssetDomain, int(i)).Bytes()}.Build(),
			Select: allAsset,
		}.Build())
		if err != nil {
			if status.Code(err) == codes.NotFound {
				// Made and erased since.
				continue
			}
			return nil, err
		}
		out.SetAssets(append(out.GetAssets(), a))
	}
	return out, nil
}
