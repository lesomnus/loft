package payment

import (
	"fmt"
	"testing"
	"time"

	levyanv1 "github.com/lesomnus/levyan/api/levyan/v1"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/known/timestamppb"

	app "github.com/lesomnus/loft"
)

// pairs is every message this package moves, beside levyan's it is moved
// from or to: what a page reads, and what a payer's request is.
var pairs = []struct{ ours, levyans proto.Message }{
	{&app.PaymentListBillingAccountsRequest{}, &levyanv1.ListBillingAccountsRequest{}},
	{&app.PaymentListBillingAccountsResponse{}, &levyanv1.ListBillingAccountsResponse{}},
	{&app.PaymentCreateBillingAccountRequest{}, &levyanv1.CreateBillingAccountRequest{}},
	{&app.PaymentCreateBillingAccountResponse{}, &levyanv1.CreateBillingAccountResponse{}},
	{&app.PaymentUpdateBillingAccountRequest{}, &levyanv1.UpdateBillingAccountRequest{}},
	{&app.PaymentUpdateBillingAccountResponse{}, &levyanv1.UpdateBillingAccountResponse{}},
	{&app.PaymentGetBillingAccountRequest{}, &levyanv1.GetBillingAccountRequest{}},
	{&app.PaymentGetBillingAccountResponse{}, &levyanv1.GetBillingAccountResponse{}},
	{&app.PaymentStartCheckoutRequest{}, &levyanv1.StartCheckoutRequest{}},
	{&app.PaymentStartCheckoutResponse{}, &levyanv1.StartCheckoutResponse{}},
	{&app.PaymentCompleteCheckoutRequest{}, &levyanv1.CompleteCheckoutRequest{}},
	{&app.PaymentCompleteCheckoutResponse{}, &levyanv1.CompleteCheckoutResponse{}},
	{&app.PaymentListOffersRequest{}, &levyanv1.ListOffersRequest{}},
	{&app.PaymentListOffersResponse{}, &levyanv1.ListOffersResponse{}},
	{&app.PaymentAcceptRequest{}, &levyanv1.AcceptRequest{}},
	{&app.PaymentAcceptResponse{}, &levyanv1.AcceptResponse{}},
	{&app.PaymentDeclineRequest{}, &levyanv1.DeclineRequest{}},
	{&app.PaymentDeclineResponse{}, &levyanv1.DeclineResponse{}},
	{&app.PaymentListLinksRequest{}, &levyanv1.ListLinksRequest{}},
	{&app.PaymentListLinksResponse{}, &levyanv1.ListLinksResponse{}},
	{&app.PaymentUseRequest{}, &levyanv1.UseRequest{}},
	{&app.PaymentUseResponse{}, &levyanv1.UseResponse{}},
	{&app.PaymentStopRequest{}, &levyanv1.StopRequest{}},
	{&app.PaymentStopResponse{}, &levyanv1.StopResponse{}},
	{&app.PaymentListInvoicesRequest{}, &levyanv1.ListInvoicesRequest{}},
	{&app.PaymentListInvoicesResponse{}, &levyanv1.ListInvoicesResponse{}},
	{&app.PaymentGetInvoiceRequest{}, &levyanv1.GetInvoiceRequest{}},
	{&app.PaymentGetInvoiceResponse{}, &levyanv1.GetInvoiceResponse{}},
	{&app.PaymentListCreditNotesRequest{}, &levyanv1.ListCreditNotesRequest{}},
	{&app.PaymentListCreditNotesResponse{}, &levyanv1.ListCreditNotesResponse{}},
	{&app.PaymentListPayerStatementsRequest{}, &levyanv1.PayerServiceListStatementsRequest{}},
	{&app.PaymentListPayerStatementsResponse{}, &levyanv1.PayerServiceListStatementsResponse{}},
	{&app.PaymentGetPayerStatementRequest{}, &levyanv1.PayerServiceGetStatementRequest{}},
	{&app.PaymentGetPayerStatementResponse{}, &levyanv1.PayerServiceGetStatementResponse{}},
	{&app.PaymentGetPayerResponse{}, &levyanv1.GetPayerResponse{}},
	{&app.PaymentGetUsageResponse{}, &levyanv1.GetUsageResponse{}},
	{&app.PaymentListStatementsResponse{}, &levyanv1.ListStatementsResponse{}},
	{&app.PaymentOfferResponse{}, &levyanv1.OfferResponse{}},
}

// same says what of `ours` is not `levyans`: a field under a number levyan
// gives another name, kind or cardinality, a message or an enum that is not
// levyan's -- down to every message and enum either holds -- and a field of
// levyan's this one neither has nor reserved, which [move] would drop
// without anybody having decided to.
func same(ours, levyans protoreflect.MessageDescriptor, seen map[protoreflect.FullName]bool) []string {
	if seen[ours.FullName()] {
		return nil
	}
	seen[ours.FullName()] = true

	var out []string
	at := func(f protoreflect.FieldDescriptor) string {
		return fmt.Sprintf("%s.%s (%d)", ours.Name(), f.Name(), f.Number())
	}
	for i := 0; i < ours.Fields().Len(); i++ {
		f := ours.Fields().Get(i)
		g := levyans.Fields().ByNumber(f.Number())
		switch {
		case g == nil:
			out = append(out, at(f)+": levyan has no field of its number")
			continue
		case f.Name() != g.Name():
			out = append(out, at(f)+": levyan's is "+string(g.Name()))
		case f.Kind() != g.Kind():
			out = append(out, fmt.Sprintf("%s: %s, and levyan's %s", at(f), f.Kind(), g.Kind()))
		case f.Cardinality() != g.Cardinality():
			out = append(out, fmt.Sprintf("%s: %s, and levyan's %s", at(f), f.Cardinality(), g.Cardinality()))
		case f.HasPresence() != g.HasPresence():
			out = append(out, at(f)+": its presence is not levyan's")
		}
		switch f.Kind() {
		case protoreflect.MessageKind:
			if g.Kind() == protoreflect.MessageKind {
				if f.Message().FullName() == "google.protobuf.Timestamp" || g.Message().FullName() == "google.protobuf.Timestamp" {
					if f.Message().FullName() != g.Message().FullName() {
						out = append(out, at(f)+": a moment on one side only")
					}
					continue
				}
				out = append(out, same(f.Message(), g.Message(), seen)...)
			}
		case protoreflect.EnumKind:
			if g.Kind() == protoreflect.EnumKind {
				a, b := f.Enum().Values(), g.Enum().Values()
				if a.Len() != b.Len() {
					out = append(out, fmt.Sprintf("%s: %d values, and levyan's %s %d", at(f), a.Len(), g.Enum().Name(), b.Len()))
				}
				for j := 0; j < b.Len(); j++ {
					if a.ByNumber(b.Get(j).Number()) == nil {
						out = append(out, fmt.Sprintf("%s: no value %d, levyan's %s", at(f), b.Get(j).Number(), b.Get(j).Name()))
					}
				}
			}
		}
	}
	for i := 0; i < levyans.Fields().Len(); i++ {
		g := levyans.Fields().Get(i)
		if ours.Fields().ByNumber(g.Number()) == nil && !ours.ReservedRanges().Has(g.Number()) {
			out = append(out, fmt.Sprintf("%s: no field %d, levyan's %s, and none reserved for it", ours.Name(), g.Number(), g.Name()))
		}
	}
	return out
}

// TestTheMessagesAreLevyans: what a payer's request is moved into and what
// levyan answers is moved out of are levyan's own messages, field for field
// and number for number, down to every message and enum they hold -- so that
// moving one is reading back what was written, and nothing is said under a
// number levyan means otherwise. What this build leaves out of levyan's it
// reserves (a line's parts and its unpriced stretches).
func TestTheMessagesAreLevyans(t *testing.T) {
	seen := map[protoreflect.FullName]bool{}
	var out []string
	for _, p := range pairs {
		out = append(out, same(p.ours.ProtoReflect().Descriptor(), p.levyans.ProtoReflect().Descriptor(), seen)...)
	}
	require.Empty(t, out)
	require.Greater(t, len(seen), 30, "every message under them")

	// What levyan#43 added -- an allowance, a pool and what a line took of
	// it, credit and what paid an invoice -- is held here, and not reserved
	// as a line's parts are.
	for _, m := range []protoreflect.FullName{"loft.BillingAllowance", "loft.BillingPool", "loft.BillingLinePool", "loft.BillingCredit", "loft.BillingInvoiceCredit"} {
		require.True(t, seen[m], "%s is levyan's, field for field", m)
	}

	// And it sees what is not levyan's: another message, one field off.
	require.NotEmpty(t, same((&app.BillingAccount{}).ProtoReflect().Descriptor(), (&levyanv1.Recipient{}).ProtoReflect().Descriptor(), map[protoreflect.FullName]bool{}))
	require.NotEmpty(t, same((&app.BillingLine{}).ProtoReflect().Descriptor(), (&levyanv1.LinePart{}).ProtoReflect().Descriptor(), map[protoreflect.FullName]bool{}))
}

// TestWhatIsMovedIsWhatWasSaid: levyan's answer, moved, reads as what levyan
// said -- a card as its kind, brand and last four digits -- and what this
// build does not declare is left out rather than carried along unread.
func TestWhatIsMovedIsWhatWasSaid(t *testing.T) {
	x := require.New(t)

	now := timestamppb.Now()
	v := levyanv1.GetBillingAccountResponse_builder{
		BillingAccount: levyanv1.BillingAccount_builder{Id: "ba", Country: "KR", Channel: levyanv1.Channel_CHANNEL_MOR, Currency: "KRW", Created: now}.Build(),
		Collection:     levyanv1.Collection_builder{State: levyanv1.CollectionState_COLLECTION_STATE_COLLECTABLE, Since: now}.Build(),
		Subscription: levyanv1.Subscription_builder{
			Status: levyanv1.SubscriptionStatus_SUBSCRIPTION_STATUS_ACTIVE,
			Change: levyanv1.SubscriptionChange_SUBSCRIPTION_CHANGE_CANCEL, ChangeAt: now,
			Method: levyanv1.PaymentMethod_builder{Type: "south_korea_local_card", Brand: "hyundai", Last4: "0004"}.Build(),
		}.Build(),
	}.Build()
	got := &app.PaymentGetBillingAccountResponse{}
	x.NoError(move(v, got))
	x.Equal("KRW", got.GetBillingAccount().GetCurrency())
	x.Equal(app.BillingChannel_BILLING_CHANNEL_MOR, got.GetBillingAccount().GetChannel())
	x.Equal(app.BillingCollectionState_BILLING_COLLECTION_STATE_COLLECTABLE, got.GetCollection().GetState())
	x.Equal(app.BillingSubscriptionChange_BILLING_SUBSCRIPTION_CHANGE_CANCEL, got.GetSubscription().GetChange())
	x.Equal("hyundai", got.GetSubscription().GetMethod().GetBrand())
	x.Equal("0004", got.GetSubscription().GetMethod().GetLast4())

	// A line's parts are levyan's to say: moved, they are not carried.
	u := levyanv1.GetUsageResponse_builder{Usage: levyanv1.Usage_builder{
		Lines: []*levyanv1.Line{levyanv1.Line_builder{
			Meter: "loft.assets.active", Usage: "3",
			Parts: []*levyanv1.LinePart{levyanv1.LinePart_builder{Price: "p", Amount: "300"}.Build()},
		}.Build()},
		Total: 300,
	}.Build()}.Build()
	usage := &app.PaymentGetUsageResponse{}
	x.NoError(move(u, usage))
	x.Equal("3", usage.GetUsage().GetLines()[0].GetUsage())
	x.Empty(usage.GetUsage().GetLines()[0].ProtoReflect().GetUnknown(), "what this build does not declare is left out")
	x.EqualValues(300, usage.GetUsage().GetTotal())
}

// TestWhatLevyansOperatorGaveIsMovedAsSaid (levyan#43): what levyan says a
// workspace and a billing account were given -- the allowances and the pools
// a period is rated with, what each line was given and took, credit and what
// paid an invoice -- reads as levyan said it once moved, a decimal as the
// text it was and a moment as the moment.
func TestWhatLevyansOperatorGaveIsMovedAsSaid(t *testing.T) {
	x := require.New(t)
	until := timestamppb.New(time.Date(2027, 4, 30, 15, 0, 0, 0, time.UTC))
	expires := timestamppb.New(time.Date(2027, 10, 31, 15, 0, 0, 0, time.UTC))

	u := levyanv1.GetUsageResponse_builder{Usage: levyanv1.Usage_builder{
		Lines: []*levyanv1.Line{levyanv1.Line_builder{
			Meter: "loft.assets.active", Unit: "{asset}", Usage: "1212.903225", Included: "438.709677", Allowance: "338.709677",
			Billable: "774.193548", Pooled: "700", Pools: []*levyanv1.LinePool{levyanv1.LinePool_builder{Pool: "pl-1", Taken: "700"}.Build()},
			Amount: "7419.3548",
		}.Build()},
		Allowances: []*levyanv1.Allowance{levyanv1.Allowance_builder{Id: "al-1", Meter: "loft.assets.active", Quantity: 500, From: timestamppb.Now(), Until: until}.Build()},
		Pools:      []*levyanv1.Pool{levyanv1.Pool_builder{Id: "pl-1", Meter: "loft.assets.active", Granted: 10000, Left: "9300", From: timestamppb.Now(), Expires: expires}.Build()},
		Total:      7419,
	}.Build()}.Build()
	usage := &app.PaymentGetUsageResponse{}
	x.NoError(move(u, usage))
	l := usage.GetUsage().GetLines()[0]
	x.Equal([]string{"438.709677", "338.709677", "774.193548", "700"}, []string{l.GetIncluded(), l.GetAllowance(), l.GetBillable(), l.GetPooled()})
	x.Equal([]string{"pl-1", "700"}, []string{l.GetPools()[0].GetPool(), l.GetPools()[0].GetTaken()})
	a := usage.GetUsage().GetAllowances()[0]
	x.Equal([]any{"al-1", "loft.assets.active", int64(500)}, []any{a.GetId(), a.GetMeter(), a.GetQuantity()})
	x.True(until.AsTime().Equal(a.GetUntil().AsTime()))
	p := usage.GetUsage().GetPools()[0]
	x.Equal([]any{"pl-1", int64(10000), "9300"}, []any{p.GetId(), p.GetGranted(), p.GetLeft()})
	x.True(expires.AsTime().Equal(p.GetExpires().AsTime()))

	credit := levyanv1.Credit_builder{Id: "cr-1", Workspace: "ws-1", Granted: 100000, Left: 70000, Currency: "KRW", Expires: expires, Created: timestamppb.Now()}.Build()
	payer := &app.PaymentGetPayerResponse{}
	x.NoError(move(levyanv1.GetPayerResponse_builder{Payer: levyanv1.Payer_builder{Workspace: "ws-1", Credits: []*levyanv1.Credit{credit}}.Build()}.Build(), payer))
	c := payer.GetPayer().GetCredits()[0]
	x.Equal([]any{"cr-1", "ws-1", int64(100000), int64(70000), "KRW"}, []any{c.GetId(), c.GetWorkspace(), c.GetGranted(), c.GetLeft(), c.GetCurrency()})

	got := &app.PaymentGetBillingAccountResponse{}
	x.NoError(move(levyanv1.GetBillingAccountResponse_builder{
		BillingAccount: levyanv1.BillingAccount_builder{Id: "ba"}.Build(),
		Credits:        []*levyanv1.Credit{credit, levyanv1.Credit_builder{Id: "cr-2", BillingAccount: "ba", Granted: 30000, Left: 30000, Currency: "KRW"}.Build()},
		Balance:        5000,
	}.Build(), got))
	x.Len(got.GetCredits(), 2)
	x.Equal("ba", got.GetCredits()[1].GetBillingAccount())
	x.EqualValues(5000, got.GetBalance())

	inv := &app.PaymentGetInvoiceResponse{}
	x.NoError(move(levyanv1.GetInvoiceResponse_builder{Invoice: levyanv1.Invoice_builder{
		Id: "inv", Total: 80000, Credited: 75000, Due: 5000,
		Credits: []*levyanv1.InvoiceCredit{
			levyanv1.InvoiceCredit_builder{Credit: "cr-1", Amount: 70000, Workspace: "ws-1", Expires: expires}.Build(),
			levyanv1.InvoiceCredit_builder{Amount: 5000}.Build(),
		},
	}.Build()}.Build(), inv))
	paid := inv.GetInvoice().GetCredits()
	x.Len(paid, 2)
	x.Equal([]any{"cr-1", int64(70000), "ws-1"}, []any{paid[0].GetCredit(), paid[0].GetAmount(), paid[0].GetWorkspace()})
	x.Equal([]any{"", int64(5000)}, []any{paid[1].GetCredit(), paid[1].GetAmount()}, "the balance's names no credit")
	x.EqualValues(5000, inv.GetInvoice().GetDue())
}

// TestAPayersStatementIsMovedAsSaid (levyan#47): a statement levyan answers
// a payer -- its billing account's, of a period it paid for -- reads as it
// was said once moved, each line with what was given and taken, and the
// request names the billing account and the page as levyan's does.
func TestAPayersStatementIsMovedAsSaid(t *testing.T) {
	x := require.New(t)
	start := timestamppb.New(time.Date(2026, 9, 30, 15, 0, 0, 0, time.UTC))
	end := timestamppb.New(time.Date(2026, 10, 31, 15, 0, 0, 0, time.UTC))

	req := &levyanv1.PayerServiceListStatementsRequest{}
	x.NoError(move(app.PaymentListPayerStatementsRequest_builder{BillingAccount: "ba", PageSize: 5, PageToken: "next"}.Build(), req))
	x.Equal([]any{"ba", int32(5), "next"}, []any{req.GetBillingAccount(), req.GetPageSize(), req.GetPageToken()})

	st := levyanv1.Statement_builder{
		Id: "st-1", Workspace: "ws-1", Channel: levyanv1.Channel_CHANNEL_MOR, Currency: "KRW", Total: 1000, Closed: end,
		Period: levyanv1.Period_builder{Start: start, End: end, Zone: "Asia/Seoul"}.Build(),
		Lines: []*levyanv1.Line{levyanv1.Line_builder{
			Meter: "loft.assets.active", Unit: "{asset}", Kind: levyanv1.LineKind_LINE_KIND_USAGE,
			Usage: "1200", Included: "500", Allowance: "500", Billable: "700", Pooled: "700",
			Pools:  []*levyanv1.LinePool{levyanv1.LinePool_builder{Pool: "pl-1", Taken: "700"}.Build()},
			Amount: "0",
			Parts:  []*levyanv1.LinePart{levyanv1.LinePart_builder{Price: "p", Amount: "0"}.Build()},
		}.Build()},
	}.Build()
	got := &app.PaymentGetPayerStatementResponse{}
	x.NoError(move(levyanv1.PayerServiceGetStatementResponse_builder{Statement: st}.Build(), got))
	s := got.GetStatement()
	x.Equal([]any{"st-1", "ws-1", app.BillingChannel_BILLING_CHANNEL_MOR, "KRW", int64(1000)}, []any{s.GetId(), s.GetWorkspace(), s.GetChannel(), s.GetCurrency(), s.GetTotal()})
	x.Equal("Asia/Seoul", s.GetPeriod().GetZone())
	x.True(start.AsTime().Equal(s.GetPeriod().GetStart().AsTime()))
	l := s.GetLines()[0]
	x.Equal([]string{"1200", "500", "500", "700", "700", "0"}, []string{l.GetUsage(), l.GetIncluded(), l.GetAllowance(), l.GetBillable(), l.GetPooled(), l.GetAmount()})
	x.Equal([]string{"pl-1", "700"}, []string{l.GetPools()[0].GetPool(), l.GetPools()[0].GetTaken()})
	x.Empty(l.ProtoReflect().GetUnknown(), "a line's parts are levyan's to say, and left out")

	list := &app.PaymentListPayerStatementsResponse{}
	x.NoError(move(levyanv1.PayerServiceListStatementsResponse_builder{Statements: []*levyanv1.Statement{st}, NextPageToken: "more"}.Build(), list))
	x.Len(list.GetStatements(), 1)
	x.Equal("more", list.GetNextPageToken())
}
