package cli

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"google.golang.org/grpc"
	"google.golang.org/protobuf/proto"

	"github.com/lesomnus/payday/pdid"

	app "github.com/lesomnus/loft"
	"github.com/lesomnus/loft/internal/money"
	"github.com/lesomnus/loft/internal/search"
)

// The rest of the register the assistant changes (D33, loft#94): stock,
// counts, work orders, purchases, models and labels -- and the lists that
// answer the ids their plans take.

var (
	workKindWord   = map[string]string{"repair": "수리", "inspection": "점검", "maintenance": "정비", "rma": "RMA"}
	workStatusWord = map[string]string{"open": "접수", "scheduled": "예정", "in_progress": "진행 중", "done": "완료", "cancelled": "취소"}
	resolutionWord = map[string]string{"moved": "위치 반영", "lost": "분실 처리", "ignored": "무시"}
	receiveAsWord  = map[string]string{"asset": "자산", "stock": "재고"}
)

// filters is a list's one filter, or none for one that names nothing, which
// a list refuses: "a filter that names nothing".
func filters[F proto.Message](f F) []F {
	if proto.Size(f) == 0 {
		return nil
	}
	return []F{f}
}

// unitOf is what a stock is counted in.
func unitOf(s *app.Stock) string { return or(s.GetUnit(), "개") }

// Lists.

type mcpStockIn struct {
	Name  string `json:"name,omitempty" jsonschema:"only stock whose name has this in it"`
	Space string `json:"space,omitempty" jsonschema:"only what is kept in this space, by id or tag"`
	Model string `json:"model,omitempty" jsonschema:"only stock of this model, an id find_models answers with"`
	Low   bool   `json:"low,omitempty" jsonschema:"only what is at or below its threshold"`
	Limit int    `json:"limit,omitempty" jsonschema:"how many: 50 if left out, 200 at most"`
}

type mcpStock struct {
	Id        string  `json:"id"`
	Name      string  `json:"name"`
	Quantity  int64   `json:"quantity"`
	Unit      string  `json:"unit,omitempty"`
	Threshold int64   `json:"threshold,omitempty" jsonschema:"at or below it, it is low"`
	Low       bool    `json:"low"`
	Space     *mcpRef `json:"space,omitempty" jsonschema:"where it is kept"`
	Model     *mcpRef `json:"model,omitempty"`
}

type mcpStocks struct {
	Items []mcpStock `json:"items"`
	More  bool       `json:"more" jsonschema:"whether there are more than these"`
}

func (t mcpTools) listStock(ctx context.Context, _ *mcp.CallToolRequest, in mcpStockIn) (*mcp.CallToolResult, mcpStocks, error) {
	out := mcpStocks{Items: []mcpStock{}}
	c, err := t.call(ctx)
	if err != nil {
		return nil, out, err
	}
	f := &app.StockFilter{}
	if in.Space != "" {
		ref, err := c.assetRef(in.Space)
		if err != nil {
			return nil, out, err
		}
		f.SetSpace(ref)
	}
	if in.Model != "" {
		id, err := idOf("model", in.Model)
		if err != nil {
			return nil, out, err
		}
		f.SetModel(app.ItemModelById(id.Bytes()))
	}
	n := in.Limit
	if n <= 0 {
		n = 50
	}
	n = min(n, 200)
	want := search.Fold(strings.TrimSpace(in.Name))
	found := []*app.Stock{}
	after := ""
	for range 10 {
		v, err := app.NewStockServiceClient(c.conn).List(ctx, app.StockListRequest_builder{Filters: filters(f), Size: 200, After: after}.Build())
		if err != nil {
			return nil, out, failed(err)
		}
		for _, s := range v.GetItems() {
			if want != "" && !strings.Contains(search.Fold(s.GetName()), want) {
				continue
			}
			if in.Low && (s.GetThreshold() <= 0 || s.GetQuantity() > s.GetThreshold()) {
				continue
			}
			found = append(found, s)
		}
		after = v.GetNext()
		if after == "" || len(found) > n {
			break
		}
	}
	if len(found) > n {
		found, out.More = found[:n], true
	} else if after != "" {
		out.More = true
	}
	spaces, models := []pdid.Id{}, []pdid.Id{}
	for _, s := range found {
		spaces = append(spaces, pid(s.GetSpace().GetId()))
		models = append(models, pid(s.GetModel().GetId()))
	}
	fetch(c, c.assets, spaces, func(id pdid.Id) (*app.Asset, error) {
		return app.NewAssetServiceClient(c.conn).Get(ctx, app.AssetGetById(id.Bytes()))
	})
	fetch(c, c.models, models, func(id pdid.Id) (string, error) {
		v, err := app.NewItemModelServiceClient(c.conn).Get(ctx, app.ItemModelGetRequest_builder{Ref: app.ItemModelById(id.Bytes())}.Build())
		return v.GetName(), err
	})
	for _, s := range found {
		item := mcpStock{
			Id:        idString(s.GetId()),
			Name:      s.GetName(),
			Quantity:  s.GetQuantity(),
			Unit:      s.GetUnit(),
			Threshold: s.GetThreshold(),
			Low:       s.GetThreshold() > 0 && s.GetQuantity() <= s.GetThreshold(),
			Model:     c.named(c.models, s.GetModel().GetId()),
		}
		if a := c.assets[pid(s.GetSpace().GetId())]; a != nil {
			item.Space = &mcpRef{Id: idString(a.GetId()), Name: a.GetName()}
		}
		out.Items = append(out.Items, item)
	}
	return nil, out, nil
}

type mcpWorkIn struct {
	Asset  string `json:"asset,omitempty" jsonschema:"only work on this asset, by id or tag"`
	Status string `json:"status,omitempty" jsonschema:"only in this status"`
	Kind   string `json:"kind,omitempty" jsonschema:"only of this kind"`
	Limit  int    `json:"limit,omitempty" jsonschema:"how many: 20 if left out, 100 at most"`
	Cursor string `json:"cursor,omitempty" jsonschema:"where to carry on from: the last answer's next"`
}

type mcpWork struct {
	Id       string  `json:"id"`
	Name     string  `json:"name"`
	Kind     string  `json:"kind" jsonschema:"repair, inspection, maintenance or rma"`
	Status   string  `json:"status" jsonschema:"open, scheduled, in_progress, done or cancelled"`
	Asset    *mcpRef `json:"asset,omitempty" jsonschema:"what it is on"`
	Vendor   *mcpRef `json:"vendor,omitempty" jsonschema:"who does it, for work outside"`
	Begins   string  `json:"begins,omitempty"`
	Ends     string  `json:"ends,omitempty"`
	Done     string  `json:"done,omitempty" jsonschema:"when it was completed"`
	Cost     int64   `json:"cost,omitempty" jsonschema:"in the currency's minor unit: won for KRW, cents for USD"`
	Currency string  `json:"currency,omitempty" jsonschema:"ISO 4217"`
	Blocking bool    `json:"blocking,omitempty" jsonschema:"the asset cannot be reserved meanwhile"`
	Every    int32   `json:"every_days,omitempty" jsonschema:"for a recurring inspection: days until the next"`
	Desc     string  `json:"desc,omitempty"`
}

type mcpWorks struct {
	Items []mcpWork `json:"items"`
	Next  string    `json:"next,omitempty" jsonschema:"the cursor for the next page; absent when this was the last"`
}

func (t mcpTools) listWorkOrders(ctx context.Context, _ *mcp.CallToolRequest, in mcpWorkIn) (*mcp.CallToolResult, mcpWorks, error) {
	out := mcpWorks{Items: []mcpWork{}}
	c, err := t.call(ctx)
	if err != nil {
		return nil, out, err
	}
	f := &app.WorkOrderFilter{}
	if in.Asset != "" {
		ref, err := c.assetRef(in.Asset)
		if err != nil {
			return nil, out, err
		}
		f.SetAsset(ref)
	}
	if in.Status != "" {
		f.SetStatus(in.Status)
	}
	if in.Kind != "" {
		f.SetKind(in.Kind)
	}
	n := in.Limit
	if n <= 0 {
		n = 20
	}
	v, err := app.NewWorkOrderServiceClient(c.conn).List(ctx, app.WorkOrderListRequest_builder{
		Filters: filters(f), Size: int32(min(n, 100)), After: in.Cursor, Reverse: true,
	}.Build())
	if err != nil {
		return nil, out, failed(err)
	}
	out.Next = v.GetNext()
	assets, vendors := []pdid.Id{}, []pdid.Id{}
	for _, w := range v.GetItems() {
		assets = append(assets, pid(w.GetAsset().GetId()))
		vendors = append(vendors, pid(w.GetVendor().GetId()))
	}
	fetch(c, c.assets, assets, func(id pdid.Id) (*app.Asset, error) {
		return app.NewAssetServiceClient(c.conn).Get(ctx, app.AssetGetById(id.Bytes()))
	})
	c.fetchParties(vendors)
	for _, w := range v.GetItems() {
		item := mcpWork{
			Id: idString(w.GetId()), Name: w.GetName(), Kind: w.GetKind(), Status: w.GetStatus(),
			Vendor: c.named(c.parties, w.GetVendor().GetId()),
			Begins: stamp(w.GetBeginsAt()), Ends: stamp(w.GetEndsAt()), Done: stamp(w.GetCompletedAt()),
			Cost: w.GetCost(), Currency: w.GetCurrency(), Blocking: w.GetBlocking(), Every: w.GetEveryDays(), Desc: w.GetDesc(),
		}
		if a := c.assets[pid(w.GetAsset().GetId())]; a != nil {
			item.Asset = &mcpRef{Id: idString(a.GetId()), Name: strings.TrimSpace(a.GetTag() + " " + a.GetName())}
		}
		out.Items = append(out.Items, item)
	}
	return nil, out, nil
}

type mcpCountsIn struct {
	Count  string `json:"count,omitempty" jsonschema:"one count, by id, with what it found"`
	Status string `json:"status,omitempty" jsonschema:"only counts in this status: open or closed"`
	Limit  int    `json:"limit,omitempty" jsonschema:"how many counts, or findings of one: 20 if left out, 200 at most"`
}

type mcpFinding struct {
	Id         string  `json:"id" jsonschema:"what plan_count's resolve takes"`
	Kind       string  `json:"kind" jsonschema:"seen, misplaced (seen elsewhere than it is kept), missing (kept here and never seen) or unknown (a label of nothing)"`
	Resolution string  `json:"resolution" jsonschema:"open, moved, lost or ignored"`
	Asset      *mcpRef `json:"asset,omitempty"`
	Seen       *mcpRef `json:"seen,omitempty" jsonschema:"where it was seen"`
	Expected   *mcpRef `json:"expected,omitempty" jsonschema:"where it is kept"`
	At         string  `json:"at,omitempty" jsonschema:"when it was seen"`
	Desc       string  `json:"desc,omitempty"`
}

type mcpCount struct {
	Id       string       `json:"id"`
	Name     string       `json:"name"`
	Status   string       `json:"status"`
	Scope    *mcpRef      `json:"scope,omitempty" jsonschema:"what is counted: everything inside it"`
	Self     bool         `json:"self_service,omitempty" jsonschema:"one everybody may scan in"`
	Started  string       `json:"started,omitempty"`
	Closed   string       `json:"closed,omitempty"`
	Findings []mcpFinding `json:"findings,omitempty"`
}

type mcpCounts struct {
	Items []mcpCount `json:"items"`
}

func (t mcpTools) listCounts(ctx context.Context, _ *mcp.CallToolRequest, in mcpCountsIn) (*mcp.CallToolResult, mcpCounts, error) {
	out := mcpCounts{Items: []mcpCount{}}
	c, err := t.call(ctx)
	if err != nil {
		return nil, out, err
	}
	n := in.Limit
	if n <= 0 {
		n = 20
	}
	n = min(n, 200)
	f := &app.InventoryCountFilter{}
	if in.Status != "" {
		f.SetStatus(in.Status)
	}
	if in.Count != "" {
		id, err := idOf("count", in.Count)
		if err != nil {
			return nil, out, err
		}
		f.SetRef(app.InventoryCountById(id.Bytes()))
	}
	v, err := app.NewInventoryCountServiceClient(c.conn).List(ctx, app.InventoryCountListRequest_builder{
		Filters: filters(f), Size: int32(n), Reverse: true,
	}.Build())
	if err != nil {
		return nil, out, failed(err)
	}
	var findings []*app.CountFinding
	if in.Count != "" && len(v.GetItems()) == 1 {
		fs, err := app.NewCountFindingServiceClient(c.conn).List(ctx, app.CountFindingListRequest_builder{
			Filters: []*app.CountFindingFilter{app.CountFindingFilter_builder{Count: app.InventoryCountById(v.GetItems()[0].GetId())}.Build()},
			Size:    int32(n),
		}.Build())
		if err != nil {
			return nil, out, failed(err)
		}
		findings = fs.GetItems()
	}
	ids := []pdid.Id{}
	for _, k := range v.GetItems() {
		ids = append(ids, pid(k.GetScope().GetId()))
	}
	for _, f := range findings {
		ids = append(ids, pid(f.GetAsset().GetId()), pid(f.GetObservedParentId()), pid(f.GetExpectedParentId()))
	}
	fetch(c, c.assets, ids, func(id pdid.Id) (*app.Asset, error) {
		return app.NewAssetServiceClient(c.conn).Get(ctx, app.AssetGetById(id.Bytes()))
	})
	named := func(b []byte) *mcpRef {
		a := c.assets[pid(b)]
		if a == nil {
			return nil
		}
		return &mcpRef{Id: idString(a.GetId()), Name: strings.TrimSpace(a.GetTag() + " " + a.GetName())}
	}
	for _, k := range v.GetItems() {
		item := mcpCount{
			Id: idString(k.GetId()), Name: k.GetName(), Status: k.GetStatus(), Scope: named(k.GetScope().GetId()),
			Self: k.GetSelfService(), Started: stamp(k.GetStartedAt()), Closed: stamp(k.GetClosedAt()),
		}
		for _, f := range findings {
			item.Findings = append(item.Findings, mcpFinding{
				Id: idString(f.GetId()), Kind: f.GetKind(), Resolution: or(f.GetResolution(), "open"),
				Asset: named(f.GetAsset().GetId()), Seen: named(f.GetObservedParentId()), Expected: named(f.GetExpectedParentId()),
				At: stamp(f.GetSeenAt()), Desc: f.GetDesc(),
			})
		}
		out.Items = append(out.Items, item)
	}
	return nil, out, nil
}

type mcpPurchasesIn struct {
	Purchase string `json:"purchase,omitempty" jsonschema:"one purchase, by id, with what was bought"`
	Status   string `json:"status,omitempty" jsonschema:"only purchases in this status: ordered, received or cancelled"`
	Limit    int    `json:"limit,omitempty" jsonschema:"how many: 20 if left out, 100 at most"`
}

type mcpPurchaseLine struct {
	Model     *mcpRef `json:"model,omitempty"`
	Desc      string  `json:"desc,omitempty"`
	Quantity  int64   `json:"quantity"`
	Received  int64   `json:"received" jsonschema:"how many have arrived"`
	UnitCost  int64   `json:"unit_cost,omitempty" jsonschema:"in the purchase's currency's minor unit"`
	ReceiveAs string  `json:"receive_as,omitempty" jsonschema:"asset or stock: what each becomes when it arrives"`
}

type mcpPurchase struct {
	Id        string            `json:"id"`
	Name      string            `json:"name"`
	Status    string            `json:"status"`
	Vendor    *mcpRef           `json:"vendor,omitempty"`
	Reference string            `json:"reference,omitempty" jsonschema:"the vendor's order number, and the like"`
	Ordered   string            `json:"ordered,omitempty"`
	Received  string            `json:"received,omitempty"`
	Total     int64             `json:"total,omitempty" jsonschema:"in the currency's minor unit: won for KRW, cents for USD"`
	Currency  string            `json:"currency,omitempty" jsonschema:"ISO 4217"`
	Lines     []mcpPurchaseLine `json:"lines,omitempty"`
}

type mcpPurchases struct {
	Items []mcpPurchase `json:"items"`
}

func (t mcpTools) listPurchases(ctx context.Context, _ *mcp.CallToolRequest, in mcpPurchasesIn) (*mcp.CallToolResult, mcpPurchases, error) {
	out := mcpPurchases{Items: []mcpPurchase{}}
	c, err := t.call(ctx)
	if err != nil {
		return nil, out, err
	}
	n := in.Limit
	if n <= 0 {
		n = 20
	}
	f := &app.PurchaseFilter{}
	if in.Status != "" {
		f.SetStatus(in.Status)
	}
	if in.Purchase != "" {
		id, err := idOf("purchase", in.Purchase)
		if err != nil {
			return nil, out, err
		}
		f.SetRef(app.PurchaseById(id.Bytes()))
	}
	v, err := app.NewPurchaseServiceClient(c.conn).List(ctx, app.PurchaseListRequest_builder{
		Filters: filters(f), Size: int32(min(n, 100)), Reverse: true,
	}.Build())
	if err != nil {
		return nil, out, failed(err)
	}
	vendors := []pdid.Id{}
	for _, p := range v.GetItems() {
		vendors = append(vendors, pid(p.GetVendor().GetId()))
	}
	c.fetchParties(vendors)
	for _, p := range v.GetItems() {
		item := mcpPurchase{
			Id: idString(p.GetId()), Name: p.GetName(), Status: p.GetStatus(), Vendor: c.named(c.parties, p.GetVendor().GetId()),
			Reference: p.GetReference(), Ordered: stamp(p.GetOrderedAt()), Received: stamp(p.GetReceivedAt()),
			Total: p.GetTotal(), Currency: p.GetCurrency(),
		}
		if in.Purchase != "" {
			ls, err := app.NewPurchaseLineServiceClient(c.conn).List(ctx, app.PurchaseLineListRequest_builder{
				Filters: []*app.PurchaseLineFilter{app.PurchaseLineFilter_builder{Purchase: app.PurchaseById(p.GetId())}.Build()},
				Size:    100,
			}.Build())
			if err != nil {
				return nil, out, failed(err)
			}
			models := []pdid.Id{}
			for _, l := range ls.GetItems() {
				models = append(models, pid(l.GetModel().GetId()))
			}
			fetch(c, c.models, models, func(id pdid.Id) (string, error) {
				v, err := app.NewItemModelServiceClient(c.conn).Get(ctx, app.ItemModelGetRequest_builder{Ref: app.ItemModelById(id.Bytes())}.Build())
				return v.GetName(), err
			})
			for _, l := range ls.GetItems() {
				item.Lines = append(item.Lines, mcpPurchaseLine{
					Model: c.named(c.models, l.GetModel().GetId()), Desc: l.GetDesc(), Quantity: l.GetQuantity(),
					Received: l.GetReceivedQuantity(), UnitCost: l.GetUnitCost(), ReceiveAs: l.GetReceiveAs(),
				})
			}
		}
		out.Items = append(out.Items, item)
	}
	return nil, out, nil
}

// Stock.

type mcpStockPlan struct {
	Action string `json:"action" jsonschema:"add, receive, consume, adjust, transfer or convert"`
	Stock  string `json:"stock,omitempty" jsonschema:"every action but add: the stock, by list_stock's id"`

	Name      string `json:"name,omitempty" jsonschema:"add: what it is called"`
	Model     string `json:"model,omitempty" jsonschema:"add: what it is of, an id find_models answers with"`
	Space     string `json:"space,omitempty" jsonschema:"add: where it is kept, by id or tag"`
	Unit      string `json:"unit,omitempty" jsonschema:"add: what it is counted in: 개, 박스, m"`
	Threshold int64  `json:"threshold,omitempty" jsonschema:"add: at or below this, it is low"`

	Quantity  int64  `json:"quantity,omitempty" jsonschema:"add: how many there are; receive, consume, transfer, convert: how many; adjust: by how many, more or fewer (signed)"`
	To        string `json:"to,omitempty" jsonschema:"transfer: the space it goes to, by id or tag"`
	TagPrefix string `json:"tag_prefix,omitempty" jsonschema:"convert: what the new assets' tags begin with"`
	At        string `json:"at,omitempty" jsonschema:"when it happened: RFC 3339 or a date; now if left out"`
	Reason    string `json:"reason,omitempty" jsonschema:"why: adjust wants one"`
}

func (t mcpTools) planStock(ctx context.Context, _ *mcp.CallToolRequest, in mcpStockPlan) (*mcp.CallToolResult, mcpPlanned, error) {
	c, err := t.call(ctx)
	if err != nil {
		return nil, mcpPlanned{}, err
	}
	p, err := c.stockPlan(in)
	if err != nil {
		return nil, mcpPlanned{}, err
	}
	return t.planned(c, p)
}

func (c *mcpCall) stockPlan(in mcpStockPlan) (*plan, error) {
	if in.Action == "add" {
		name := strings.TrimSpace(in.Name)
		if name == "" {
			return nil, errors.New("name: what the stock is called")
		}
		req := app.StockAddRequest_builder{Name: name, Quantity: in.Quantity, Threshold: in.Threshold, Unit: in.Unit, Op: op()}.Build()
		what := []string{}
		if in.Model != "" {
			id, err := idOf("model", in.Model)
			if err != nil {
				return nil, err
			}
			m, err := app.NewItemModelServiceClient(c.conn).Get(c.ctx, app.ItemModelGetRequest_builder{Ref: app.ItemModelById(id.Bytes())}.Build())
			if err != nil {
				return nil, fmt.Errorf("model %s: %w", in.Model, failed(err))
			}
			req.SetModel(app.ItemModelById(id.Bytes()))
			what = append(what, "모델 "+m.GetName())
		}
		where := ""
		if in.Space != "" {
			sp, _, err := c.asset("space", in.Space)
			if err != nil {
				return nil, err
			}
			req.SetSpace(app.AssetById(sp.GetId()))
			where = ", " + c.place(sp, true)
		}
		if in.Threshold > 0 {
			what = append(what, fmt.Sprintf("%d%s 이하면 부족", in.Threshold, or(in.Unit, "개")))
		}
		summary := fmt.Sprintf("재고 등록: %s %d%s%s%s", name, in.Quantity, or(in.Unit, "개"), where, clause(" (%s)", strings.Join(what, ", ")))
		return &plan{
			method:  app.StockService_Add_FullMethodName,
			summary: summary,
			run: func(ctx context.Context, conn grpc.ClientConnInterface) (*mcpRef, error) {
				v, err := app.NewStockServiceClient(conn).Add(ctx, req)
				if err != nil {
					return nil, err
				}
				return refOf(v.GetId(), v.GetName()), nil
			},
		}, nil
	}

	id, err := idOf("stock", in.Stock)
	if err != nil {
		return nil, err
	}
	s, err := app.NewStockServiceClient(c.conn).Get(c.ctx, app.StockGetRequest_builder{Ref: app.StockById(id.Bytes())}.Build())
	if err != nil {
		return nil, fmt.Errorf("stock %s: %w", in.Stock, failed(err))
	}
	ref, unit, result := app.StockById(id.Bytes()), unitOf(s), refOf(s.GetId(), s.GetName())
	at, when, err := moment("at", in.At)
	if err != nil {
		return nil, err
	}
	tail := clause(", %s에", when) + clause(" — %s", in.Reason)
	change := func(word string, delta int64, call func(app.StockServiceClient, context.Context, *app.StockChangeRequest, ...grpc.CallOption) (*app.Stock, error), method string) (*plan, error) {
		req := app.StockChangeRequest_builder{Ref: ref, Quantity: in.Quantity, Reason: in.Reason, Op: op()}.Build()
		if at != nil {
			req.SetAt(at)
		}
		return &plan{
			method:  method,
			summary: fmt.Sprintf("%s: %s %+d%s (%d → %d)%s", word, s.GetName(), delta, unit, s.GetQuantity(), s.GetQuantity()+delta, tail),
			run: func(ctx context.Context, conn grpc.ClientConnInterface) (*mcpRef, error) {
				_, err := call(app.NewStockServiceClient(conn), ctx, req)
				return result, err
			},
		}, nil
	}
	switch in.Action {
	case "receive":
		return change("입고", in.Quantity, app.StockServiceClient.Receive, app.StockService_Receive_FullMethodName)
	case "consume":
		return change("사용", -in.Quantity, app.StockServiceClient.Consume, app.StockService_Consume_FullMethodName)
	case "adjust":
		return change("조정", in.Quantity, app.StockServiceClient.Adjust, app.StockService_Adjust_FullMethodName)
	case "transfer":
		to, _, err := c.asset("to", in.To)
		if err != nil {
			return nil, err
		}
		from := "어디에도 없음"
		if sp := pid(s.GetSpace().GetId()); !sp.IsZero() {
			if a, _, err := c.asset("space", sp.String()); err == nil {
				from = c.place(a, true)
			}
		}
		req := app.StockTransferRequest_builder{Ref: ref, To: app.AssetById(to.GetId()), Quantity: in.Quantity, Reason: in.Reason, Op: op()}.Build()
		if at != nil {
			req.SetAt(at)
		}
		return &plan{
			method:  app.StockService_Transfer_FullMethodName,
			summary: fmt.Sprintf("재고 이동: %s %d%s, %s → %s%s", s.GetName(), in.Quantity, unit, from, c.place(to, true), tail),
			run: func(ctx context.Context, conn grpc.ClientConnInterface) (*mcpRef, error) {
				_, err := app.NewStockServiceClient(conn).Transfer(ctx, req)
				return result, err
			},
		}, nil
	case "convert":
		req := app.StockConvertRequest_builder{Ref: ref, Quantity: in.Quantity, TagPrefix: in.TagPrefix, Op: op()}.Build()
		p := &plan{
			method:  app.StockService_Convert_FullMethodName,
			summary: fmt.Sprintf("자산으로 전환: %s %d%s → 자산 %d개%s", s.GetName(), in.Quantity, unit, in.Quantity, clause(" (자산 번호 %s…)", in.TagPrefix)),
		}
		p.run = func(ctx context.Context, conn grpc.ClientConnInterface) (*mcpRef, error) {
			v, err := app.NewStockServiceClient(conn).Convert(ctx, req)
			if err != nil {
				return nil, err
			}
			for _, a := range v.GetAssets() {
				p.more = append(p.more, strings.TrimSpace(a.GetTag()+" "+a.GetName())+" "+idString(a.GetId()))
			}
			return result, nil
		}
		return p, nil
	}
	return nil, errors.New("action: add, receive, consume, adjust, transfer or convert")
}

// Counts.

type mcpCountPlan struct {
	Action  string `json:"action" jsonschema:"start, scan, reconcile, resolve or close"`
	Count   string `json:"count,omitempty" jsonschema:"scan, reconcile, close: the count, by list_counts' id"`
	Finding string `json:"finding,omitempty" jsonschema:"resolve: one of a count's findings, by list_counts' id"`

	Name  string `json:"name,omitempty" jsonschema:"start: what the count is called"`
	Scope string `json:"scope,omitempty" jsonschema:"start: the space counted -- everything inside it -- by id or tag"`
	Self  bool   `json:"self_service,omitempty" jsonschema:"start: one everybody may scan in"`

	Code  string `json:"code,omitempty" jsonschema:"scan: what was read off a label"`
	Asset string `json:"asset,omitempty" jsonschema:"scan: or the asset itself, by id or tag"`
	At    string `json:"at,omitempty" jsonschema:"scan: where it was seen, by id or tag; the count's scope if left out"`
	Note  string `json:"note,omitempty" jsonschema:"scan: a note"`

	Resolution string `json:"resolution,omitempty" jsonschema:"resolve: moved (to where it was seen), lost, or ignored"`
	Reason     string `json:"reason,omitempty" jsonschema:"resolve: why"`
	Leave      bool   `json:"leave_kits,omitempty" jsonschema:"resolve, lost: take the asset out of every kit and group it is a member of as well, which the summary names, and the kits' bookings let it go; the summary of a plan without it names what it stays a member of"`
}

func (t mcpTools) planCount(ctx context.Context, _ *mcp.CallToolRequest, in mcpCountPlan) (*mcp.CallToolResult, mcpPlanned, error) {
	c, err := t.call(ctx)
	if err != nil {
		return nil, mcpPlanned{}, err
	}
	p, err := c.countPlan(in)
	if err != nil {
		return nil, mcpPlanned{}, err
	}
	return t.planned(c, p)
}

func (c *mcpCall) count(field, v string) (*app.InventoryCount, error) {
	id, err := idOf(field, v)
	if err != nil {
		return nil, err
	}
	k, err := app.NewInventoryCountServiceClient(c.conn).Get(c.ctx, app.InventoryCountGetRequest_builder{Ref: app.InventoryCountById(id.Bytes())}.Build())
	if err != nil {
		return nil, fmt.Errorf("%s %s: %w", field, v, failed(err))
	}
	return k, nil
}

func (c *mcpCall) countPlan(in mcpCountPlan) (*plan, error) {
	switch in.Action {
	case "start":
		name := strings.TrimSpace(in.Name)
		if name == "" {
			return nil, errors.New("name: what the count is called")
		}
		scope, _, err := c.asset("scope", in.Scope)
		if err != nil {
			return nil, err
		}
		req := app.InventoryCountAddRequest_builder{Name: name, Scope: app.AssetById(scope.GetId()), SelfService: in.Self}.Build()
		return &plan{
			method:  app.InventoryCountService_Add_FullMethodName,
			summary: fmt.Sprintf("실사 시작: %s, %s 안의 모든 것%s", name, c.place(scope, true), map[bool]string{true: ", 누구나 스캔"}[in.Self]),
			run: func(ctx context.Context, conn grpc.ClientConnInterface) (*mcpRef, error) {
				v, err := app.NewInventoryCountServiceClient(conn).Add(ctx, req)
				if err != nil {
					return nil, err
				}
				return refOf(v.GetId(), v.GetName()), nil
			},
		}, nil
	case "resolve":
		id, err := idOf("finding", in.Finding)
		if err != nil {
			return nil, err
		}
		f, err := app.NewCountFindingServiceClient(c.conn).Get(c.ctx, app.CountFindingGetRequest_builder{Ref: app.CountFindingById(id.Bytes())}.Build())
		if err != nil {
			return nil, fmt.Errorf("finding %s: %w", in.Finding, failed(err))
		}
		w, ok := resolutionWord[in.Resolution]
		if !ok {
			return nil, errors.New("resolution: moved, lost or ignored")
		}
		what := "?"
		var lost *app.Asset
		if a := pid(f.GetAsset().GetId()); !a.IsZero() {
			if v, label, err := c.asset("finding.asset", a.String()); err == nil {
				what, lost = label, v
			}
		}
		req := app.InventoryCountResolveRequest_builder{Ref: app.CountFindingById(id.Bytes()), Resolution: in.Resolution, Reason: in.Reason}.Build()
		stays := ""
		switch {
		case in.Resolution == "lost" && lost != nil:
			if stays, err = c.leaving(lost, "lost", in.Leave, func(leave []*app.AssetRef, names string) {
				req.SetLeave(leave)
				w += ", " + names + "에서 빼기"
			}); err != nil {
				return nil, err
			}
		case in.Leave:
			return nil, errors.New("leave_kits: only with resolution lost, of a finding of an asset")
		}
		return &plan{
			method:  app.InventoryCountService_Resolve_FullMethodName,
			summary: fmt.Sprintf("실사 처리: %s: %s%s%s", what, w, stays, clause(" — %s", in.Reason)),
			run: func(ctx context.Context, conn grpc.ClientConnInterface) (*mcpRef, error) {
				_, err := app.NewInventoryCountServiceClient(conn).Resolve(ctx, req)
				return refOf(f.GetId(), what), err
			},
		}, nil
	}

	k, err := c.count("count", in.Count)
	if err != nil {
		return nil, err
	}
	ref, result := app.InventoryCountById(k.GetId()), refOf(k.GetId(), k.GetName())
	switch in.Action {
	case "scan":
		req := app.InventoryCountScanRequest_builder{Ref: ref, Code: in.Code, Note: in.Note, Op: op()}.Build()
		what := in.Code
		if in.Asset != "" {
			a, label, err := c.asset("asset", in.Asset)
			if err != nil {
				return nil, err
			}
			req.SetAsset(app.AssetById(a.GetId()))
			what = label
		}
		if what == "" {
			return nil, errors.New("code or asset: what was seen")
		}
		where := ""
		if in.At != "" {
			a, _, err := c.asset("at", in.At)
			if err != nil {
				return nil, err
			}
			req.SetAt(app.AssetById(a.GetId()))
			where = ", " + c.place(a, true) + "에서"
		}
		return &plan{
			method:  app.InventoryCountService_Scan_FullMethodName,
			summary: fmt.Sprintf("실사 스캔: %s: %s 확인%s%s", k.GetName(), what, where, clause(" — %s", in.Note)),
			run: func(ctx context.Context, conn grpc.ClientConnInterface) (*mcpRef, error) {
				_, err := app.NewInventoryCountServiceClient(conn).Scan(ctx, req)
				return result, err
			},
		}, nil
	case "reconcile":
		req := app.InventoryCountReconcileRequest_builder{Ref: ref}.Build()
		p := &plan{
			method:  app.InventoryCountService_Reconcile_FullMethodName,
			summary: fmt.Sprintf("실사 대조: %s (있어야 하는데 못 본 것을 미발견으로 표시)", k.GetName()),
		}
		p.run = func(ctx context.Context, conn grpc.ClientConnInterface) (*mcpRef, error) {
			v, err := app.NewInventoryCountServiceClient(conn).Reconcile(ctx, req)
			if err != nil {
				return nil, err
			}
			p.more = []string{fmt.Sprintf("확인 %d, 위치 다름 %d, 미발견 %d, 미등록 %d", v.GetSeen(), v.GetMisplaced(), v.GetMissing(), v.GetUnknown())}
			return result, nil
		}
		return p, nil
	case "close":
		req := app.InventoryCountCloseRequest_builder{Ref: ref}.Build()
		return &plan{
			method:  app.InventoryCountService_Close_FullMethodName,
			summary: fmt.Sprintf("실사 마감: %s", k.GetName()),
			run: func(ctx context.Context, conn grpc.ClientConnInterface) (*mcpRef, error) {
				_, err := app.NewInventoryCountServiceClient(conn).Close(ctx, req)
				return result, err
			},
		}, nil
	}
	return nil, errors.New("action: start, scan, reconcile, resolve or close")
}

// Work orders.

type mcpWorkPlan struct {
	Action    string `json:"action" jsonschema:"report, update, complete or cancel"`
	WorkOrder string `json:"work_order,omitempty" jsonschema:"update, complete, cancel: the work order, by list_work_orders' id"`

	Asset    string `json:"asset,omitempty" jsonschema:"report: what is broken, or due an inspection, by id or tag"`
	Name     string `json:"name,omitempty" jsonschema:"report, update: what the work is"`
	Kind     string `json:"kind,omitempty" jsonschema:"report: repair if left out, inspection, maintenance or rma"`
	Desc     string `json:"desc,omitempty" jsonschema:"report, update: what is wrong, or to be done"`
	Vendor   string `json:"vendor,omitempty" jsonschema:"report: who does it, a vendor's id find_people answers with"`
	Begins   string `json:"begins,omitempty" jsonschema:"report, update: when the work begins"`
	Ends     string `json:"ends,omitempty" jsonschema:"report, update: when it ends"`
	Blocking *bool  `json:"blocking,omitempty" jsonschema:"report, update: whether the asset cannot be reserved meanwhile; update leaves it as it is if left out"`
	Every    int32  `json:"every_days,omitempty" jsonschema:"report: for a recurring inspection, days until the next"`
	Status   string `json:"status,omitempty" jsonschema:"update: open, scheduled or in_progress"`

	Cost      int64  `json:"cost,omitempty" jsonschema:"update, complete: what it cost, in the currency's minor unit: won for KRW, cents for USD"`
	Currency  string `json:"currency,omitempty" jsonschema:"update: ISO 4217, such as KRW or USD; the work's if left out"`
	Condition string `json:"condition,omitempty" jsonschema:"complete: what the asset is in afterwards: good, damaged or broken"`
	Reason    string `json:"reason,omitempty" jsonschema:"complete, cancel: a note for the history"`
}

func (t mcpTools) planWorkOrder(ctx context.Context, _ *mcp.CallToolRequest, in mcpWorkPlan) (*mcp.CallToolResult, mcpPlanned, error) {
	c, err := t.call(ctx)
	if err != nil {
		return nil, mcpPlanned{}, err
	}
	p, err := c.workPlan(in)
	if err != nil {
		return nil, mcpPlanned{}, err
	}
	return t.planned(c, p)
}

func (c *mcpCall) workPlan(in mcpWorkPlan) (*plan, error) {
	begins, from, err := moment("begins", in.Begins)
	if err != nil {
		return nil, err
	}
	ends, until, err := moment("ends", in.Ends)
	if err != nil {
		return nil, err
	}
	if in.Action == "report" {
		a, label, err := c.asset("asset", in.Asset)
		if err != nil {
			return nil, err
		}
		name := strings.TrimSpace(in.Name)
		if name == "" {
			return nil, errors.New("name: what the work is")
		}
		kind := or(in.Kind, "repair")
		blocking := in.Blocking != nil && *in.Blocking
		req := app.WorkOrderAddRequest_builder{Name: name, Desc: in.Desc, Asset: app.AssetById(a.GetId()), Kind: kind, Blocking: blocking, EveryDays: in.Every, BeginsAt: begins, EndsAt: ends}.Build()
		who := ""
		if in.Vendor != "" {
			p, name, err := c.party("vendor", in.Vendor)
			if err != nil {
				return nil, err
			}
			req.SetVendor(app.PartyById(p.GetId()))
			who = name
		}
		summary := fmt.Sprintf("작업 접수: %s: %s (%s)%s%s%s%s", label, name, said(workKindWord, kind),
			clause(", %s", who), clause(", %s부터", from), clause(" %s까지", until), map[bool]string{true: ", 그동안 예약 막음"}[blocking])
		return &plan{
			method:  app.WorkOrderService_Add_FullMethodName,
			summary: summary,
			run: func(ctx context.Context, conn grpc.ClientConnInterface) (*mcpRef, error) {
				v, err := app.NewWorkOrderServiceClient(conn).Add(ctx, req)
				if err != nil {
					return nil, err
				}
				return refOf(v.GetId(), v.GetName()), nil
			},
		}, nil
	}

	id, err := idOf("work_order", in.WorkOrder)
	if err != nil {
		return nil, err
	}
	w, err := app.NewWorkOrderServiceClient(c.conn).Get(c.ctx, app.WorkOrderGetRequest_builder{Ref: app.WorkOrderById(id.Bytes())}.Build())
	if err != nil {
		return nil, fmt.Errorf("work_order %s: %w", in.WorkOrder, failed(err))
	}
	ref, result := app.WorkOrderById(id.Bytes()), refOf(w.GetId(), w.GetName())
	switch in.Action {
	case "update":
		// Update writes whether it blocks whatever it is sent, so what was not
		// said is sent as it is.
		blocking := w.GetBlocking()
		if in.Blocking != nil {
			blocking = *in.Blocking
		}
		req := app.WorkOrderUpdateRequest_builder{Ref: ref, Name: in.Name, Desc: in.Desc, Status: in.Status, Cost: in.Cost, Currency: in.Currency, Blocking: blocking, BeginsAt: begins, EndsAt: ends}.Build()
		changes := []string{}
		for _, v := range []struct{ word, to string }{
			{"이름", in.Name}, {"설명", in.Desc}, {"상태", said(workStatusWord, in.Status)}, {"시작", from}, {"끝", until},
		} {
			if v.to != "" {
				changes = append(changes, v.word+" → "+v.to)
			}
		}
		if in.Cost != 0 {
			changes = append(changes, "비용 → "+money.String(in.Cost, or(in.Currency, w.GetCurrency())))
		} else if in.Currency != "" && in.Currency != w.GetCurrency() && w.GetCost() != 0 {
			return nil, errors.New("cost: what it cost in " + in.Currency + ", which a currency it moves to needs said again")
		}
		if blocking != w.GetBlocking() {
			changes = append(changes, map[bool]string{true: "그동안 예약 막음", false: "예약 막지 않음"}[blocking])
		}
		if len(changes) == 0 {
			return nil, errors.New("what to change: name, desc, status, begins, ends, cost or blocking")
		}
		return &plan{
			method:  app.WorkOrderService_Update_FullMethodName,
			summary: fmt.Sprintf("작업 변경: %s: %s", w.GetName(), strings.Join(changes, ", ")),
			run: func(ctx context.Context, conn grpc.ClientConnInterface) (*mcpRef, error) {
				_, err := app.NewWorkOrderServiceClient(conn).Update(ctx, req)
				return result, err
			},
		}, nil
	case "complete", "cancel":
		req := app.WorkOrderCompleteRequest_builder{Ref: ref, Reason: in.Reason, Cost: in.Cost, Condition: in.Condition}.Build()
		word, method, call := "작업 완료", app.WorkOrderService_Complete_FullMethodName, app.WorkOrderServiceClient.Complete
		if in.Action == "cancel" {
			word, method, call = "작업 취소", app.WorkOrderService_Cancel_FullMethodName, app.WorkOrderServiceClient.Cancel
		}
		extra := ""
		if in.Cost != 0 {
			extra += ", 비용 " + money.String(in.Cost, w.GetCurrency())
		}
		extra += clause(", 컨디션 → %s", said(conditionWord, in.Condition))
		return &plan{
			method:  method,
			summary: fmt.Sprintf("%s: %s%s%s", word, w.GetName(), extra, clause(" — %s", in.Reason)),
			run: func(ctx context.Context, conn grpc.ClientConnInterface) (*mcpRef, error) {
				_, err := call(app.NewWorkOrderServiceClient(conn), ctx, req)
				return result, err
			},
		}, nil
	}
	return nil, errors.New("action: report, update, complete or cancel")
}

// Purchases.

type mcpPurchaseLineIn struct {
	Model     string `json:"model,omitempty" jsonschema:"what is bought, an id find_models answers with"`
	Desc      string `json:"desc,omitempty" jsonschema:"what is bought, in words, for a line of no model"`
	Quantity  int64  `json:"quantity" jsonschema:"how many"`
	UnitCost  int64  `json:"unit_cost,omitempty" jsonschema:"what one costs, in the currency's minor unit: won for KRW, cents for USD"`
	ReceiveAs string `json:"receive_as,omitempty" jsonschema:"asset (each its own) or stock (counted): what it becomes when it arrives"`
}

type mcpPurchasePlan struct {
	Action   string `json:"action" jsonschema:"add or receive"`
	Purchase string `json:"purchase,omitempty" jsonschema:"receive: the purchase, by list_purchases' id"`

	Name      string              `json:"name,omitempty" jsonschema:"add: what the purchase is"`
	Vendor    string              `json:"vendor,omitempty" jsonschema:"add: who it is bought from, a vendor's id find_people answers with"`
	Reference string              `json:"reference,omitempty" jsonschema:"add: the vendor's order number, and the like"`
	Currency  string              `json:"currency,omitempty" jsonschema:"add: ISO 4217, such as KRW or USD; the workspace's if left out"`
	Lines     []mcpPurchaseLineIn `json:"lines,omitempty" jsonschema:"add: what is bought"`

	Into      string `json:"into,omitempty" jsonschema:"receive: the space what arrived goes into, by id or tag"`
	TagPrefix string `json:"tag_prefix,omitempty" jsonschema:"receive: what the new assets' tags begin with"`
	At        string `json:"at,omitempty" jsonschema:"receive: when it arrived; now if left out"`
}

func (t mcpTools) planPurchase(ctx context.Context, _ *mcp.CallToolRequest, in mcpPurchasePlan) (*mcp.CallToolResult, mcpPlanned, error) {
	c, err := t.call(ctx)
	if err != nil {
		return nil, mcpPlanned{}, err
	}
	p, err := c.purchasePlan(in)
	if err != nil {
		return nil, mcpPlanned{}, err
	}
	return t.planned(c, p)
}

func (c *mcpCall) purchasePlan(in mcpPurchasePlan) (*plan, error) {
	switch in.Action {
	case "add":
		name := strings.TrimSpace(in.Name)
		if name == "" {
			return nil, errors.New("name: what the purchase is")
		}
		if len(in.Lines) == 0 {
			return nil, errors.New("lines: what is bought")
		}
		// The currency it is priced in, said in the request as the sentence
		// says it: the workspace's, unless it is said, as it is when planned
		// -- not as it may be by the time it is applied.
		currency := in.Currency
		if currency == "" {
			me, err := c.whom()
			if err != nil {
				return nil, err
			}
			currency = me.GetCurrency()
		}
		req := app.PurchaseAddRequest_builder{Name: name, Reference: in.Reference, Currency: currency}.Build()
		what, total := []string{}, int64(0)
		for _, l := range in.Lines {
			spec := app.PurchaseLineSpec_builder{Desc: l.Desc, Quantity: l.Quantity, UnitCost: l.UnitCost, ReceiveAs: l.ReceiveAs}.Build()
			thing := l.Desc
			if l.Model != "" {
				id, err := idOf("lines.model", l.Model)
				if err != nil {
					return nil, err
				}
				m, err := app.NewItemModelServiceClient(c.conn).Get(c.ctx, app.ItemModelGetRequest_builder{Ref: app.ItemModelById(id.Bytes())}.Build())
				if err != nil {
					return nil, fmt.Errorf("lines.model %s: %w", l.Model, failed(err))
				}
				spec.SetModel(app.ItemModelById(id.Bytes()))
				thing = m.GetName()
			}
			if thing == "" {
				return nil, errors.New("lines: a model, or desc saying what it is")
			}
			req.SetLines(append(req.GetLines(), spec))
			what = append(what, fmt.Sprintf("%s ×%d%s", thing, l.Quantity, clause(" (%s으로)", said(receiveAsWord, l.ReceiveAs))))
			total += l.Quantity * l.UnitCost
		}
		who := ""
		if in.Vendor != "" {
			p, name, err := c.party("vendor", in.Vendor)
			if err != nil {
				return nil, err
			}
			req.SetVendor(app.PartyById(p.GetId()))
			who = name
		}
		sum := ""
		if total > 0 {
			sum = ", 합계 " + money.String(total, currency)
		}
		return &plan{
			method:  app.PurchaseService_Add_FullMethodName,
			summary: fmt.Sprintf("구매 등록: %s%s: %s%s%s", name, clause(", %s에서", who), strings.Join(what, ", "), sum, clause(", 주문 번호 %s", in.Reference)),
			run: func(ctx context.Context, conn grpc.ClientConnInterface) (*mcpRef, error) {
				v, err := app.NewPurchaseServiceClient(conn).Add(ctx, req)
				if err != nil {
					return nil, err
				}
				return refOf(v.GetId(), v.GetName()), nil
			},
		}, nil
	case "receive":
		id, err := idOf("purchase", in.Purchase)
		if err != nil {
			return nil, err
		}
		pu, err := app.NewPurchaseServiceClient(c.conn).Get(c.ctx, app.PurchaseGetRequest_builder{Ref: app.PurchaseById(id.Bytes())}.Build())
		if err != nil {
			return nil, fmt.Errorf("purchase %s: %w", in.Purchase, failed(err))
		}
		into, _, err := c.asset("into", in.Into)
		if err != nil {
			return nil, err
		}
		req := app.PurchaseReceiveRequest_builder{Ref: app.PurchaseById(id.Bytes()), Into: app.AssetById(into.GetId()), TagPrefix: in.TagPrefix}.Build()
		at, when, err := moment("at", in.At)
		if err != nil {
			return nil, err
		}
		if at != nil {
			req.SetAt(at)
		}
		p := &plan{
			method:  app.PurchaseService_Receive_FullMethodName,
			summary: fmt.Sprintf("구매 입고: %s → %s%s%s", pu.GetName(), c.place(into, true), clause(" (자산 번호 %s…)", in.TagPrefix), clause(", %s에", when)),
		}
		p.run = func(ctx context.Context, conn grpc.ClientConnInterface) (*mcpRef, error) {
			v, err := app.NewPurchaseServiceClient(conn).Receive(ctx, req)
			if err != nil {
				return nil, err
			}
			for _, a := range v.GetAssets() {
				p.more = append(p.more, strings.TrimSpace(a.GetTag()+" "+a.GetName())+" "+idString(a.GetId()))
			}
			if n := v.GetStocked(); n > 0 {
				p.more = append(p.more, fmt.Sprintf("재고로 %d줄", n))
			}
			return refOf(pu.GetId(), pu.GetName()), nil
		}
		return p, nil
	}
	return nil, errors.New("action: add or receive")
}

// Models.

type mcpModelPlan struct {
	Action string `json:"action" jsonschema:"add or update"`
	Model  string `json:"model,omitempty" jsonschema:"update: the model, an id find_models answers with"`

	Name  string `json:"name,omitempty" jsonschema:"add, update: what it is called"`
	Maker string `json:"maker,omitempty" jsonschema:"add, update: who makes it"`
	Code  string `json:"code,omitempty" jsonschema:"add, update: the maker's code for it"`
	Desc  string `json:"desc,omitempty" jsonschema:"update: a note"`
	Type  string `json:"type,omitempty" jsonschema:"add, update: what it is a model of, a type id another answer gave"`
}

func (t mcpTools) planModel(ctx context.Context, _ *mcp.CallToolRequest, in mcpModelPlan) (*mcp.CallToolResult, mcpPlanned, error) {
	c, err := t.call(ctx)
	if err != nil {
		return nil, mcpPlanned{}, err
	}
	p, err := c.modelPlan(in)
	if err != nil {
		return nil, mcpPlanned{}, err
	}
	return t.planned(c, p)
}

func (c *mcpCall) modelPlan(in mcpModelPlan) (*plan, error) {
	typeName := ""
	var typeRef *app.AssetTypeRef
	if in.Type != "" {
		id, err := idOf("type", in.Type)
		if err != nil {
			return nil, err
		}
		v, err := app.NewAssetTypeServiceClient(c.conn).Get(c.ctx, app.AssetTypeGetRequest_builder{Ref: app.AssetTypeById(id.Bytes())}.Build())
		if err != nil {
			return nil, fmt.Errorf("type %s: %w", in.Type, failed(err))
		}
		typeRef, typeName = app.AssetTypeById(id.Bytes()), v.GetName()
	}
	switch in.Action {
	case "add":
		name := strings.TrimSpace(in.Name)
		if name == "" {
			return nil, errors.New("name: what the model is called")
		}
		req := app.ItemModelAddRequest_builder{Name: name, Maker: in.Maker, Code: in.Code, Type: typeRef}.Build()
		return &plan{
			method:  app.ItemModelService_Add_FullMethodName,
			summary: fmt.Sprintf("모델 등록: %s%s%s%s", name, clause(" (%s)", in.Maker), clause(", 코드 %s", in.Code), clause(", 유형 %s", typeName)),
			run: func(ctx context.Context, conn grpc.ClientConnInterface) (*mcpRef, error) {
				v, err := app.NewItemModelServiceClient(conn).Add(ctx, req)
				if err != nil {
					return nil, err
				}
				return refOf(v.GetId(), v.GetName()), nil
			},
		}, nil
	case "update":
		id, err := idOf("model", in.Model)
		if err != nil {
			return nil, err
		}
		m, err := app.NewItemModelServiceClient(c.conn).Get(c.ctx, app.ItemModelGetRequest_builder{Ref: app.ItemModelById(id.Bytes())}.Build())
		if err != nil {
			return nil, fmt.Errorf("model %s: %w", in.Model, failed(err))
		}
		req := app.ItemModelUpdateRequest_builder{Ref: app.ItemModelById(id.Bytes()), Name: in.Name, Desc: in.Desc, Maker: in.Maker, Code: in.Code, Type: typeRef}.Build()
		changes := []string{}
		for _, v := range []struct{ word, to string }{{"이름", in.Name}, {"제조사", in.Maker}, {"코드", in.Code}, {"설명", in.Desc}, {"유형", typeName}} {
			if v.to != "" {
				changes = append(changes, v.word+" → "+v.to)
			}
		}
		if len(changes) == 0 {
			return nil, errors.New("what to change: name, maker, code, desc or type")
		}
		return &plan{
			method:  app.ItemModelService_Update_FullMethodName,
			summary: fmt.Sprintf("모델 변경: %s: %s", m.GetName(), strings.Join(changes, ", ")),
			run: func(ctx context.Context, conn grpc.ClientConnInterface) (*mcpRef, error) {
				v, err := app.NewItemModelServiceClient(conn).Update(ctx, req)
				if err != nil {
					return nil, err
				}
				return refOf(v.GetId(), v.GetName()), nil
			},
		}, nil
	}
	return nil, errors.New("action: add or update")
}

// Labels.

type mcpLabelPlan struct {
	Action string   `json:"action" jsonschema:"print, bind or unbind"`
	Count  int      `json:"count,omitempty" jsonschema:"print: how many new labels, bound to nothing yet"`
	Assets []string `json:"assets,omitempty" jsonschema:"print: one label for each of these, bound already, by id or tag"`
	Batch  string   `json:"batch,omitempty" jsonschema:"print: a name for the batch"`
	Label  string   `json:"label,omitempty" jsonschema:"bind, unbind: the label, by the code printed on it"`
	Asset  string   `json:"asset,omitempty" jsonschema:"bind: what it is stuck on, by id or tag"`
	Void   bool     `json:"void,omitempty" jsonschema:"unbind: the label is damaged or lost, and is never used again"`
}

func (t mcpTools) planLabel(ctx context.Context, _ *mcp.CallToolRequest, in mcpLabelPlan) (*mcp.CallToolResult, mcpPlanned, error) {
	c, err := t.call(ctx)
	if err != nil {
		return nil, mcpPlanned{}, err
	}
	p, err := c.labelPlan(in)
	if err != nil {
		return nil, mcpPlanned{}, err
	}
	return t.planned(c, p)
}

func (c *mcpCall) labelPlan(in mcpLabelPlan) (*plan, error) {
	switch in.Action {
	case "print":
		req := app.LabelPrintRequest_builder{Count: uint32(max(in.Count, 0)), Batch: in.Batch}.Build()
		what := ""
		if len(in.Assets) > 0 {
			names := []string{}
			for _, v := range in.Assets {
				a, label, err := c.asset("assets", v)
				if err != nil {
					return nil, err
				}
				req.SetAssetIds(append(req.GetAssetIds(), a.GetId()))
				names = append(names, label)
			}
			what = strings.Join(names, ", ") + "에 붙일 라벨"
		} else if in.Count > 0 {
			what = fmt.Sprintf("새 라벨 %d장", in.Count)
		} else {
			return nil, errors.New("count: how many new labels, or assets: one for each")
		}
		p := &plan{
			method:  app.LabelService_Print_FullMethodName,
			summary: fmt.Sprintf("라벨 만들기: %s%s", what, clause(" (묶음 %s)", in.Batch)),
		}
		p.run = func(ctx context.Context, conn grpc.ClientConnInterface) (*mcpRef, error) {
			v, err := app.NewLabelServiceClient(conn).Print(ctx, req)
			if err != nil {
				return nil, err
			}
			for i, l := range v.GetLabels() {
				line := l.GetCode()
				if i < len(v.GetUrls()) {
					line += " " + v.GetUrls()[i]
				}
				p.more = append(p.more, line)
			}
			return nil, nil
		}
		return p, nil
	case "bind", "unbind":
		code := strings.TrimSpace(in.Label)
		if code == "" {
			return nil, errors.New("label: the code printed on it")
		}
		ref := app.LabelRef_builder{Code: &code}.Build()
		if in.Action == "unbind" {
			req := app.LabelUnbindRequest_builder{Ref: ref, Void: in.Void}.Build()
			return &plan{
				method:  app.LabelService_Unbind_FullMethodName,
				summary: fmt.Sprintf("라벨 해제: %s%s", code, map[bool]string{true: " (폐기, 다시 쓰지 않음)"}[in.Void]),
				run: func(ctx context.Context, conn grpc.ClientConnInterface) (*mcpRef, error) {
					v, err := app.NewLabelServiceClient(conn).Unbind(ctx, req)
					if err != nil {
						return nil, err
					}
					return refOf(v.GetId(), v.GetCode()), nil
				},
			}, nil
		}
		a, label, err := c.asset("asset", in.Asset)
		if err != nil {
			return nil, err
		}
		req := app.LabelBindRequest_builder{Ref: ref, Asset: app.AssetById(a.GetId()), Op: op()}.Build()
		return &plan{
			method:  app.LabelService_Bind_FullMethodName,
			summary: fmt.Sprintf("라벨 연결: %s → %s", code, label),
			run: func(ctx context.Context, conn grpc.ClientConnInterface) (*mcpRef, error) {
				v, err := app.NewLabelServiceClient(conn).Bind(ctx, req)
				if err != nil {
					return nil, err
				}
				return refOf(v.GetId(), v.GetCode()), nil
			},
		}, nil
	}
	return nil, errors.New("action: print, bind or unbind")
}
