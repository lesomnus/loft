package domain_test

import (
	"context"
	"testing"

	"google.golang.org/grpc/codes"

	app "github.com/lesomnus/rove"
)

// TestAVendorOrAModelIsTheWorkspacesOwn: a work order's vendor, a purchase's,
// and the model a purchase line is for are edges into another entity that the
// schema does not hold to the row's own tenant with `agrees:` -- payday
// compares only immutable paths, and these may be moved. What holds them is
// the gate reading each edge a write sets through the wall, which answers for
// every caller this deployment has, since each is a membership of one
// workspace: another workspace's vendor or model is not there to name
// (rove#40, payday's permissions guide §3).
func TestAVendorOrAModelIsTheWorkspacesOwn(t *testing.T) {
	e := newEnv(t)
	x := e.x
	room := e.space("창고", nil)
	projector := e.item("프로젝터", "PJ-1", room, day)

	vendorOf := func(ctx context.Context, name string) *app.PartyRef {
		v, err := e.app().Party().Add(ctx, app.PartyAddRequest_builder{Name: name, Kind: "vendor"}.Build())
		x.NoError(err)
		return ref[app.PartyRef](v.GetId())
	}
	modelOf := func(ctx context.Context, name string) *app.ItemModelRef {
		v, err := e.app().ItemModel().Add(ctx, app.ItemModelAddRequest_builder{Name: name}.Build())
		x.NoError(err)
		return ref[app.ItemModelRef](v.GetId())
	}
	order := func(vendor *app.PartyRef) error {
		_, err := e.app().WorkOrder().Add(e.owner, app.WorkOrderAddRequest_builder{Asset: assetRef(projector), Kind: "repair", Name: "수리", Vendor: vendor}.Build())
		return err
	}
	purchase := func(vendor *app.PartyRef, model *app.ItemModelRef) error {
		_, err := e.app().Purchase().Add(e.owner, app.PurchaseAddRequest_builder{
			Name:   "주문",
			Vendor: vendor,
			Lines:  []*app.PurchaseLineSpec{app.PurchaseLineSpec_builder{Model: model, Quantity: 1, UnitCost: 1000}.Build()},
		}.Build())
		return err
	}

	_, other := e.newTenant("other")
	theirs, theirModel := vendorOf(other, "남의 업체"), modelOf(other, "남의 모델")
	ours, ourModel := vendorOf(e.owner, "우리 업체"), modelOf(e.owner, "우리 모델")

	x.Equal(codes.NotFound, codeOf(order(theirs)), "a work order for another workspace's vendor")
	x.Equal(codes.NotFound, codeOf(purchase(theirs, ourModel)), "a purchase from another workspace's vendor")
	x.Equal(codes.NotFound, codeOf(purchase(ours, theirModel)), "a line for another workspace's model")

	// Its own are, which is what says the refusals above were about whose.
	x.NoError(order(ours))
	x.NoError(purchase(ours, ourModel))
}
