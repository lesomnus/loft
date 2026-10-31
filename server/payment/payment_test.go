package payment

import (
	"fmt"
	"testing"

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
