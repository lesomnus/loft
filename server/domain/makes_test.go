package domain_test

import (
	"context"
	"testing"
	"time"

	"github.com/lesomnus/payday/pderr"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	app "github.com/lesomnus/rove"
	"github.com/lesomnus/rove/internal/ent"
	"github.com/lesomnus/rove/internal/ent/asset"
	"github.com/lesomnus/rove/internal/ent/event"
	"github.com/lesomnus/rove/internal/ent/fact"
	"github.com/lesomnus/rove/internal/ent/placement"
	"github.com/lesomnus/rove/internal/ent/purchaseline"
	"github.com/lesomnus/rove/internal/ent/stock"
)

// A stock converted and a purchase received make their assets as a bulk
// registration makes its own: one operation, each asset's registration a
// child of it, a few hundred rows to a statement.

// TestAPurchaseReceivedIsOneOperation: what arrives as assets is registered
// as a bulk registration registers what it makes -- each asset's own "등록" a
// child of the receipt, its facts and its place from when it arrived, under
// the tags the receipt gave, in the order of the lines -- and what arrives as
// stock goes into the space's stock, all of it one operation, and the space's
// watchers told once.
func TestAPurchaseReceivedIsOneOperation(t *testing.T) {
	e := newEnv(t)
	x := e.x
	ctx := context.Background()
	room := e.space("창고", nil)
	ty, err := e.app().AssetType().Add(e.owner, app.AssetTypeAddRequest_builder{Name: "모니터류", Kind: "item"}.Build())
	x.NoError(err)
	m, err := e.app().ItemModel().Add(e.owner, app.ItemModelAddRequest_builder{Name: "U2723QE", Maker: "Dell", Type: ref[app.AssetTypeRef](ty.GetId())}.Build())
	x.NoError(err)
	battery := e.model("AA 건전지")
	p, err := e.app().Purchase().Add(e.owner, app.PurchaseAddRequest_builder{
		Name: "10월 구매",
		Lines: []*app.PurchaseLineSpec{
			app.PurchaseLineSpec_builder{Model: ref[app.ItemModelRef](m.GetId()), Quantity: 2, UnitCost: 450000}.Build(),
			app.PurchaseLineSpec_builder{Desc: "HDMI 케이블", Quantity: 1, UnitCost: 12000}.Build(),
			app.PurchaseLineSpec_builder{Model: ref[app.ItemModelRef](battery.GetId()), Quantity: 30, UnitCost: 500, ReceiveAs: "stock"}.Build(),
		},
	}.Build())
	x.NoError(err)
	version := e.get(room).GetContentVersion()

	at := e.now.Add(-2 * time.Hour)
	v, err := e.app().Purchase().Receive(e.owner, app.PurchaseReceiveRequest_builder{
		Ref: ref[app.PurchaseRef](p.GetId()), Into: assetRef(room), At: e.ago(2 * time.Hour), TagPrefix: "PO",
	}.Build())
	x.NoError(err)
	x.Equal("received", v.GetPurchase().GetStatus())
	x.EqualValues(30, v.GetStocked())
	tags := []string{}
	for _, a := range v.GetAssets() {
		tags = append(tags, a.GetTag())
	}
	x.Equal([]string{"PO-001", "PO-002", "PO-003"}, tags, "a tag for each, in the order of the lines")
	x.Equal(version+1, e.get(room).GetContentVersion(), "three things in, the space told once")

	receipt := e.eventOf("purchase.receive")
	x.Nil(receipt.CauseId)
	x.True(at.Equal(receipt.OccurredAt), "when it arrived")
	x.Equal("입고: 10월 구매", e.say(receipt))
	adds, err := e.s.Ent.Event.Query().Where(event.CauseId(receipt.Id)).All(ctx)
	x.NoError(err)
	x.Len(adds, 3, "an event for each asset it made, under the receipt")

	for i, a := range v.GetAssets() {
		id := idUUID(a.GetId())
		got := e.get(a)
		x.Equal(room.GetId(), got.GetParentId())
		x.Equal("located", got.GetPlacementMode())
		x.True(at.Equal(got.GetAcquiredAt().AsTime()), "%s acquired when it arrived", a.GetTag())
		if i < 2 {
			x.Equal("U2723QE", got.GetName())
			x.Equal(m.GetId(), got.GetModel().GetId())
			x.Equal(ty.GetId(), got.GetType().GetId(), "the model's type")
		} else {
			x.Equal("HDMI 케이블", got.GetName(), "a line of no model, by what it says")
			x.False(got.HasModel())
		}

		var mine *ent.Event
		for _, ev := range adds {
			if ev.SubjectId != nil && *ev.SubjectId == id {
				x.Nil(mine, "two events of %s's own", a.GetTag())
				mine = ev
			}
		}
		x.NotNil(mine, "%s has no event of its own", a.GetTag())
		x.Equal("asset.add", mine.Kind, "the kind a registration of one is")
		x.Equal(a.GetTag()+" 등록", e.say(mine))
		x.Equal(a.GetTag(), value[*app.AssetAdded](e, mine).GetTag())
		x.True(at.Equal(mine.OccurredAt))

		fs, err := e.s.Ent.Fact.Query().Where(fact.SubjectId(id)).All(ctx)
		x.NoError(err)
		x.NotEmpty(fs)
		for _, f := range fs {
			x.Equal(mine.Id, f.EventId, "a fact of %s points at another event", a.GetTag())
			x.True(at.Equal(f.ValidFrom), "a fact of %s from another moment", a.GetTag())
		}
		pl, err := e.s.Ent.Placement.Query().Where(placement.ChildId(id)).Only(ctx)
		x.NoError(err)
		x.Equal(mine.Id, pl.EventId, "%s's place is its own event's", a.GetTag())
		x.True(at.Equal(pl.ValidFrom))

		found := false
		for _, en := range e.timeline(a, false) {
			if en.GetKind() == "event" {
				x.Equal("입고: 10월 구매", en.GetCause())
				found = true
			}
		}
		x.True(found, "%s's history has no event", a.GetTag())
	}

	kept, err := e.s.Ent.Stock.Query().Where(stock.TenantId(e.tenant.Uuid()), stock.ModelId(idUUID(battery.GetId())), stock.SpaceId(idUUID(room.GetId()))).Only(ctx)
	x.NoError(err)
	x.EqualValues(30, kept.Quantity)
	lines, err := e.s.Ent.PurchaseLine.Query().Where(purchaseline.PurchaseId(idUUID(p.GetId()))).All(ctx)
	x.NoError(err)
	x.Len(lines, 3)
	for _, l := range lines {
		x.Equal(l.Quantity, l.ReceivedQuantity, "every line received whole")
	}
}

// TestWhatAConversionOrAReceiptCannotMakeIsRefusedAsItsOwn: the assets a
// conversion and a receipt make are checked as bulk registration checks what
// it makes, and what is wrong is refused as the request's own -- a tag
// another asset has already as what came of its tag prefix, an attribute the
// type asks for as the stock or the purchase it is of -- once, however many
// of the units it is about. Nothing is made, moved or received.
func TestWhatAConversionOrAReceiptCannotMakeIsRefusedAsItsOwn(t *testing.T) {
	e := newEnv(t)
	x := e.x
	room := e.space("창고", nil)
	e.item("모니터", "MN-002", room, day)
	m := e.model("모니터")
	st := e.stock(m, room, 5, 0)
	before := e.assets()

	_, err := e.app().Stock().Convert(e.owner, app.StockConvertRequest_builder{Ref: ref[app.StockRef](st.GetId()), Quantity: 3, TagPrefix: "MN"}.Build())
	x.Equal(codes.InvalidArgument, codeOf(err))
	taken := "태그 MN-002는 이미 있는 자산의 것입니다. 태그 앞부분을 바꾸세요"
	x.Equal(taken, status.Convert(err).Message(), "said as a person reads it")
	x.Equal([]pderr.Violation{{Field: "tag_prefix", Why: taken}}, pderr.Violations(err))
	x.EqualValues(5, e.quantity(st), "nothing taken out")

	// A type that asks for what nobody converting says.
	ty, err := e.app().AssetType().Add(e.owner, app.AssetTypeAddRequest_builder{
		Name: "측정 장비",
		Kind: "item",
		Spec: app.TypeSpec_builder{Attributes: []*app.AttributeDef{
			app.AttributeDef_builder{Key: "calibrated", Label: "교정일", Type: "date", Required: true}.Build(),
		}}.Build(),
	}.Build())
	x.NoError(err)
	meter, err := e.app().ItemModel().Add(e.owner, app.ItemModelAddRequest_builder{Name: "멀티미터", Type: ref[app.AssetTypeRef](ty.GetId())}.Build())
	x.NoError(err)
	meters := e.stock(meter, room, 4, 0)
	_, err = e.app().Stock().Convert(e.owner, app.StockConvertRequest_builder{Ref: ref[app.StockRef](meters.GetId()), Quantity: 2}.Build())
	x.Equal(codes.InvalidArgument, codeOf(err))
	x.Equal([]pderr.Violation{{Field: "ref", Why: "교정일을(를) 입력하세요"}}, pderr.Violations(err), "once, for both units")
	x.EqualValues(4, e.quantity(meters))
	x.Empty(e.eventsOf("stock.convert"))

	p, err := e.app().Purchase().Add(e.owner, app.PurchaseAddRequest_builder{
		Name:  "모니터 구매",
		Lines: []*app.PurchaseLineSpec{app.PurchaseLineSpec_builder{Model: ref[app.ItemModelRef](m.GetId()), Quantity: 2, UnitCost: 100}.Build()},
	}.Build())
	x.NoError(err)
	_, err = e.app().Purchase().Receive(e.owner, app.PurchaseReceiveRequest_builder{Ref: ref[app.PurchaseRef](p.GetId()), Into: assetRef(room), TagPrefix: "MN"}.Build())
	x.Equal(codes.InvalidArgument, codeOf(err))
	x.Equal([]pderr.Violation{{Field: "tag_prefix", Why: taken}}, pderr.Violations(err))
	still, err := e.app().Purchase().Get(e.owner, app.PurchaseGetRequest_builder{Ref: ref[app.PurchaseRef](p.GetId())}.Build())
	x.NoError(err)
	x.Equal("ordered", still.GetStatus(), "not received")
	x.Empty(e.eventsOf("purchase.receive"))
	x.Equal(before, e.assets(), "nothing was made")
}

// TestAConversionSentAgainMakesItsChildrenOnce: a client that did not hear
// back sends a conversion again, by its op, and is answered with what the
// first copy made; the first copy's children are all there are.
func TestAConversionSentAgainMakesItsChildrenOnce(t *testing.T) {
	e := newEnv(t)
	x := e.x
	ctx := context.Background()
	room := e.space("창고", nil)
	m := e.model("의자")
	st := e.stock(m, room, 6, 0)

	req := app.StockConvertRequest_builder{Ref: ref[app.StockRef](st.GetId()), Quantity: 3, Op: newOp()}.Build()
	first, err := e.app().Stock().Convert(e.owner, req)
	x.NoError(err)
	again, err := e.app().Stock().Convert(e.owner, req)
	x.NoError(err, "the same conversion, sent again")
	x.Len(again.GetAssets(), 3)
	for i, a := range first.GetAssets() {
		x.Equal(a.GetId(), again.GetAssets()[i].GetId())
		x.Equal(a.GetTag(), again.GetAssets()[i].GetTag())
	}

	x.Equal(1, e.ops(req.GetOp()))
	kids, err := e.s.Ent.Event.Query().Where(event.CauseId(idUUID(req.GetOp()))).All(ctx)
	x.NoError(err)
	x.Len(kids, 3, "an asset's registration each, once")
	n, err := e.s.Ent.Asset.Query().Where(asset.TenantId(e.tenant.Uuid()), asset.ModelId(idUUID(m.GetId()))).Count(ctx)
	x.NoError(err)
	x.Equal(3, n, "made once")
	x.EqualValues(3, e.quantity(st))
}
