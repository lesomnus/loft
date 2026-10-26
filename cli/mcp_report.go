package cli

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"google.golang.org/protobuf/types/known/timestamppb"

	app "github.com/lesomnus/rove"
)

// The reports, as the assistant reads them (design 9.14, D32): the RPCs the
// reports screen calls, through the same table -- a manager's and an
// auditor's -- each a row a person reads, by name, and nobody's address or
// number. Reading is all it does; a CSV is the screen's and the terminal's,
// and the one of who has what is a download that says what it is for, which
// an assistant has nobody to ask.

type mcpReportIn struct {
	Report  string `json:"report" jsonschema:"which report: assets -- how many there are, by type or by space, in each status; loans -- what is out now, who has it and how late; bookings -- each resource that may be booked over a period, the hours booked and used and the no-shows; counts -- each inventory count over a period, what it found and what was lost; stock -- each stock over a period, what came in and went out"`
	By      string `json:"by,omitempty" jsonschema:"assets: a row each type (type, the default) or each space (location)"`
	Kind    string `json:"kind,omitempty" jsonschema:"assets: what is counted -- item (the default), kit, group or space; loans: issue or loan, both if left out"`
	Overdue bool   `json:"overdue,omitempty" jsonschema:"loans: only what is past its due date"`
	From    string `json:"from,omitempty" jsonschema:"bookings, counts, stock: the period's first day, or its first moment in RFC 3339; the last thirty days (a year for counts) today among them if left out"`
	To      string `json:"to,omitempty" jsonschema:"bookings, counts, stock: the period's last day, which it includes, or the moment it ends before in RFC 3339"`
	Limit   int    `json:"limit,omitempty" jsonschema:"loans: how many rows, 50 by default and 500 at most"`
	Offset  int    `json:"offset,omitempty" jsonschema:"loans: how many rows to pass over, for the next page"`
}

type mcpReport struct {
	Report string `json:"report"`
	At     string `json:"at,omitempty" jsonschema:"when it was read"`
	From   string `json:"from,omitempty" jsonschema:"the period it read, from"`
	To     string `json:"to,omitempty" jsonschema:"and before"`

	Assets   *mcpCensus       `json:"assets,omitempty"`
	Loans    *mcpLoans        `json:"loans,omitempty"`
	Bookings []mcpUsage       `json:"bookings,omitempty"`
	Counts   []mcpCountResult `json:"counts,omitempty"`
	Stock    []mcpFlow        `json:"stock,omitempty"`
}

type mcpCensus struct {
	By    string         `json:"by" jsonschema:"type or location"`
	Kind  string         `json:"kind"`
	Rows  []mcpCensusRow `json:"rows"`
	Total mcpCensusRow   `json:"total"`
}

type mcpCensusRow struct {
	Name     string            `json:"name" jsonschema:"the type, or the space with the spaces it is in"`
	Tag      string            `json:"tag,omitempty"`
	Total    uint32            `json:"total"`
	Within   uint32            `json:"within,omitempty" jsonschema:"a space's with everything under it"`
	Statuses map[string]uint32 `json:"statuses" jsonschema:"how many in each status: ordered, active, in_repair, lost, retired, disposed"`
}

type mcpLoans struct {
	Total     uint32    `json:"total" jsonschema:"the rows there are as asked, on every page"`
	Custodies uint32    `json:"custodies" jsonschema:"hand-overs open, whatever was asked"`
	Overdue   uint32    `json:"overdue" jsonschema:"of them, past their due date"`
	People    uint32    `json:"people" jsonschema:"who has something"`
	Assets    uint32    `json:"assets" jsonschema:"assets out"`
	Stocks    uint32    `json:"stocks" jsonschema:"lines of stock out"`
	Rows      []mcpLoan `json:"rows" jsonschema:"the most overdue first, then what is due soonest, then what is held for keeps"`
	More      bool      `json:"more,omitempty" jsonschema:"more rows than these: ask again with the offset past them"`
}

type mcpLoan struct {
	Custody      string  `json:"custody" jsonschema:"the hand-over, as list_custody and plan_custody take it"`
	Who          *mcpRef `json:"who"`
	Kind         string  `json:"kind" jsonschema:"issue or loan"`
	What         *mcpRef `json:"what" jsonschema:"the asset, by its tag and name, or the stock"`
	Quantity     int64   `json:"quantity"`
	Unit         string  `json:"unit,omitempty"`
	Issued       string  `json:"issued,omitempty"`
	Due          string  `json:"due,omitempty"`
	Overdue      bool    `json:"overdue,omitempty"`
	OverdueDays  uint32  `json:"overdue_days,omitempty" jsonschema:"how many of the workspace's days late"`
	Acknowledged bool    `json:"acknowledged,omitempty"`
}

type mcpUsage struct {
	Resource     *mcpRef `json:"resource"`
	Kind         string  `json:"kind"`
	Units        uint32  `json:"units" jsonschema:"how many it gives at once"`
	Reservations uint32  `json:"reservations" jsonschema:"begun in the period and promised the time"`
	Used         uint32  `json:"used"`
	NoShows      uint32  `json:"no_shows"`
	BookedHours  float64 `json:"booked_hours"`
	UsedHours    float64 `json:"used_hours" jsonschema:"from the check-in or the pickup to the return or the finish"`
}

type mcpCountResult struct {
	Count     *mcpRef `json:"count" jsonschema:"as list_counts and plan_count take it"`
	Scope     *mcpRef `json:"scope"`
	Status    string  `json:"status"`
	Started   string  `json:"started,omitempty"`
	Closed    string  `json:"closed,omitempty"`
	Seen      uint32  `json:"seen"`
	Misplaced uint32  `json:"misplaced"`
	Missing   uint32  `json:"missing"`
	Unknown   uint32  `json:"unknown"`
	Moved     uint32  `json:"moved"`
	Lost      uint32  `json:"lost" jsonschema:"the losses: reported lost"`
	Ignored   uint32  `json:"ignored"`
	Unsettled uint32  `json:"unsettled"`
}

type mcpFlow struct {
	Stock          *mcpRef `json:"stock" jsonschema:"as list_stock and plan_stock take it"`
	Space          *mcpRef `json:"space"`
	Unit           string  `json:"unit,omitempty"`
	Opening        int64   `json:"opening"`
	Received       int64   `json:"received"`
	Returned       int64   `json:"returned"`
	TransferredIn  int64   `json:"transferred_in"`
	AdjustedUp     int64   `json:"adjusted_up"`
	Consumed       int64   `json:"consumed"`
	Issued         int64   `json:"issued"`
	TransferredOut int64   `json:"transferred_out"`
	Converted      int64   `json:"converted"`
	AdjustedDown   int64   `json:"adjusted_down"`
	CameIn         int64   `json:"came_in"`
	WentOut        int64   `json:"went_out"`
	Closing        int64   `json:"closing"`
}

// span is the period an assistant gave, as the workspace's days begin: a date
// alone is the day on its wall, and `to` the last day, which it includes.
// Unsaid is the server's own default.
func (c *mcpCall) span(from, to string) (*timestamppb.Timestamp, *timestamppb.Timestamp, error) {
	if from == "" && to == "" {
		return nil, nil, nil
	}
	me, err := c.whom()
	if err != nil {
		return nil, nil, err
	}
	zone := time.Local
	if l, err := time.LoadLocation(me.GetTimezone()); err == nil && me.GetTimezone() != "" {
		zone = l
	}
	read := func(field, v string, end bool) (*timestamppb.Timestamp, error) {
		v = strings.TrimSpace(v)
		if v == "" {
			return nil, nil
		}
		if t, err := time.Parse(time.RFC3339, v); err == nil {
			return timestamppb.New(t), nil
		}
		t, err := time.ParseInLocation(time.DateOnly, v, zone)
		if err != nil {
			return nil, fmt.Errorf("%s: %q is not RFC 3339, nor a date (2026-10-16)", field, v)
		}
		if end {
			t = t.AddDate(0, 0, 1)
		}
		return timestamppb.New(t), nil
	}
	f, err := read("from", from, false)
	if err != nil {
		return nil, nil, err
	}
	e, err := read("to", to, true)
	if err != nil {
		return nil, nil, err
	}
	return f, e, nil
}

func (t mcpTools) report(ctx context.Context, _ *mcp.CallToolRequest, in mcpReportIn) (*mcp.CallToolResult, mcpReport, error) {
	out := mcpReport{Report: in.Report}
	c, err := t.call(ctx)
	if err != nil {
		return nil, out, err
	}
	from, to, err := c.span(in.From, in.To)
	if err != nil {
		return nil, out, err
	}
	period := func(f, e *timestamppb.Timestamp) {
		out.From, out.To = stamp(f), stamp(e)
	}

	switch in.Report {
	case "assets":
		v, err := app.NewAssetServiceClient(c.conn).Census(ctx, app.AssetCensusRequest_builder{By: in.By, Kind: in.Kind}.Build())
		if err != nil {
			return nil, out, failed(err)
		}
		row := func(r *app.AssetCensusRow) mcpCensusRow {
			name := strings.Join(append(append([]string{}, r.GetPath()...), r.GetName()), " / ")
			return mcpCensusRow{Name: name, Tag: r.GetTag(), Total: r.GetTotal(), Within: r.GetWithin(), Statuses: r.GetStatuses()}
		}
		out.At = stamp(v.GetAt())
		out.Assets = &mcpCensus{By: v.GetBy(), Kind: v.GetKind(), Rows: []mcpCensusRow{}, Total: row(v.GetTotal())}
		for _, r := range v.GetRows() {
			out.Assets.Rows = append(out.Assets.Rows, row(r))
		}

	case "loans":
		n := in.Limit
		if n <= 0 {
			n = 50
		}
		v, err := app.NewCustodyServiceClient(c.conn).Outstanding(ctx, app.CustodyOutstandingRequest_builder{
			Overdue: in.Overdue,
			Kind:    in.Kind,
			Size:    uint32(min(n, 500)),
			Offset:  uint32(max(in.Offset, 0)),
		}.Build())
		if err != nil {
			return nil, out, failed(err)
		}
		out.At = stamp(v.GetAt())
		l := &mcpLoans{
			Total: v.GetTotal(), Custodies: v.GetCustodies(), Overdue: v.GetOverdue(),
			People: v.GetPeople(), Assets: v.GetAssets(), Stocks: v.GetStocks(), Rows: []mcpLoan{},
		}
		for _, r := range v.GetRows() {
			what := &mcpRef{Id: idString(r.GetAssetId()), Name: strings.TrimSpace(r.GetAssetTag() + " " + r.GetAssetName())}
			if len(r.GetStockId()) > 0 {
				what = &mcpRef{Id: idString(r.GetStockId()), Name: r.GetStockName()}
			}
			l.Rows = append(l.Rows, mcpLoan{
				Custody:      idString(r.GetCustodyId()),
				Who:          &mcpRef{Id: idString(r.GetPartyId()), Name: r.GetPartyName()},
				Kind:         r.GetKind(),
				What:         what,
				Quantity:     r.GetQuantity(),
				Unit:         r.GetUnit(),
				Issued:       stamp(r.GetIssuedAt()),
				Due:          stamp(r.GetDueAt()),
				Overdue:      r.GetOverdue(),
				OverdueDays:  r.GetOverdueDays(),
				Acknowledged: r.GetAcknowledged(),
			})
		}
		l.More = uint32(max(in.Offset, 0)+len(l.Rows)) < v.GetTotal()
		out.Loans = l

	case "bookings":
		v, err := app.NewBookableServiceClient(c.conn).Usage(ctx, app.BookableUsageRequest_builder{From: from, To: to}.Build())
		if err != nil {
			return nil, out, failed(err)
		}
		period(v.GetFrom(), v.GetTo())
		out.Bookings = []mcpUsage{}
		for _, r := range v.GetRows() {
			out.Bookings = append(out.Bookings, mcpUsage{
				Resource:     &mcpRef{Id: idString(r.GetResourceId()), Name: strings.TrimSpace(r.GetTag() + " " + r.GetName())},
				Kind:         r.GetKind(),
				Units:        r.GetUnits(),
				Reservations: r.GetReservations(),
				Used:         r.GetUsed(),
				NoShows:      r.GetNoShows(),
				BookedHours:  float64(r.GetBookedMinutes()) / 60,
				UsedHours:    float64(r.GetUsedMinutes()) / 60,
			})
		}

	case "counts":
		v, err := app.NewInventoryCountServiceClient(c.conn).Results(ctx, app.InventoryCountResultsRequest_builder{From: from, To: to}.Build())
		if err != nil {
			return nil, out, failed(err)
		}
		period(v.GetFrom(), v.GetTo())
		out.Counts = []mcpCountResult{}
		for _, r := range v.GetRows() {
			out.Counts = append(out.Counts, mcpCountResult{
				Count:   &mcpRef{Id: idString(r.GetCountId()), Name: r.GetName()},
				Scope:   &mcpRef{Id: idString(r.GetScopeId()), Name: r.GetScopeName()},
				Status:  r.GetStatus(),
				Started: stamp(r.GetStartedAt()), Closed: stamp(r.GetClosedAt()),
				Seen: r.GetSeen(), Misplaced: r.GetMisplaced(), Missing: r.GetMissing(), Unknown: r.GetUnknown(),
				Moved: r.GetMoved(), Lost: r.GetLost(), Ignored: r.GetIgnored(), Unsettled: r.GetUnsettled(),
			})
		}

	case "stock":
		v, err := app.NewStockServiceClient(c.conn).Flow(ctx, app.StockFlowRequest_builder{From: from, To: to}.Build())
		if err != nil {
			return nil, out, failed(err)
		}
		period(v.GetFrom(), v.GetTo())
		out.Stock = []mcpFlow{}
		for _, r := range v.GetRows() {
			out.Stock = append(out.Stock, mcpFlow{
				Stock: &mcpRef{Id: idString(r.GetStockId()), Name: r.GetName()},
				Space: &mcpRef{Id: idString(r.GetSpaceId()), Name: r.GetSpaceName()},
				Unit:  r.GetUnit(), Opening: r.GetOpening(),
				Received: r.GetReceived(), Returned: r.GetReturned(), TransferredIn: r.GetTransferredIn(), AdjustedUp: r.GetAdjustedUp(),
				Consumed: r.GetConsumed(), Issued: r.GetIssued(), TransferredOut: r.GetTransferredOut(), Converted: r.GetConverted(), AdjustedDown: r.GetAdjustedDown(),
				CameIn: r.GetCameIn(), WentOut: r.GetWentOut(), Closing: r.GetClosing(),
			})
		}

	default:
		return nil, out, fmt.Errorf("report: %q is none of assets, loans, bookings, counts and stock", in.Report)
	}
	return nil, out, nil
}
