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
// from: what a page reads.
var pairs = []struct{ ours, levyans proto.Message }{
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

// TestTheMessagesAreLevyans: what levyan answers is moved out of levyan's
// own messages, field for field and number for number, down to every message
// and enum they hold -- so that moving one is reading back what was written,
// and nothing is said under a number levyan means otherwise. What this build
// leaves out of levyan's it reserves (a line's parts and its unpriced
// stretches).
func TestTheMessagesAreLevyans(t *testing.T) {
	seen := map[protoreflect.FullName]bool{}
	var out []string
	for _, p := range pairs {
		out = append(out, same(p.ours.ProtoReflect().Descriptor(), p.levyans.ProtoReflect().Descriptor(), seen)...)
	}
	require.Empty(t, out)
	require.Greater(t, len(seen), 15, "every message under them")

	// What levyan#43 added -- an allowance, a pool and what a line took of
	// it, and credit -- is held here, and not reserved as a line's parts are.
	for _, m := range []protoreflect.FullName{"loft.BillingAllowance", "loft.BillingPool", "loft.BillingLinePool", "loft.BillingCredit"} {
		require.True(t, seen[m], "%s is levyan's, field for field", m)
	}

	// And it sees what is not levyan's: another message, one field off.
	require.NotEmpty(t, same((&app.BillingOffer{}).ProtoReflect().Descriptor(), (&levyanv1.Link{}).ProtoReflect().Descriptor(), map[protoreflect.FullName]bool{}))
	require.NotEmpty(t, same((&app.BillingLine{}).ProtoReflect().Descriptor(), (&levyanv1.LinePart{}).ProtoReflect().Descriptor(), map[protoreflect.FullName]bool{}))
}

// TestWhatIsMovedIsWhatWasSaid: levyan's answer, moved, reads as what levyan
// said -- who pays and with what, whether that can be collected, and a
// charge that failed -- and what this build does not declare is left out
// rather than carried along unread.
func TestWhatIsMovedIsWhatWasSaid(t *testing.T) {
	x := require.New(t)

	now := timestamppb.Now()
	v := levyanv1.GetPayerResponse_builder{Payer: levyanv1.Payer_builder{
		Workspace:  "ws",
		Current:    levyanv1.Link_builder{Id: "ln", Workspace: "ws", Account: "ac", BillingAccount: "ba", DateStart: now, Began: levyanv1.LinkBegan_LINK_BEGAN_ACCEPTED}.Build(),
		Collection: levyanv1.Collection_builder{State: levyanv1.CollectionState_COLLECTION_STATE_PAST_DUE, DateSince: now}.Build(),
		Dunning:    []*levyanv1.Dunning{levyanv1.Dunning_builder{Invoice: "inv", Kind: levyanv1.DeclineKind_DECLINE_KIND_REPLACE, DateReadonly: now}.Build()},
	}.Build()}.Build()
	got := &app.PaymentGetPayerResponse{}
	x.NoError(move(v, got))
	p := got.GetPayer()
	x.Equal("ba", p.GetCurrent().GetBillingAccount())
	x.Equal(app.BillingLinkBegan_BILLING_LINK_BEGAN_ACCEPTED, p.GetCurrent().GetBegan())
	x.Equal(app.BillingCollectionState_BILLING_COLLECTION_STATE_PAST_DUE, p.GetCollection().GetState())
	x.Equal(app.BillingDeclineKind_BILLING_DECLINE_KIND_REPLACE, p.GetDunning()[0].GetKind())
	x.True(now.AsTime().Equal(p.GetDunning()[0].GetDateReadonly().AsTime()))

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
// workspace was given -- the allowances and the pools a period is rated with,
// what each line was given and took, and credit -- reads as levyan said it
// once moved, a decimal as the text it was and a moment as the moment.
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
		Allowances: []*levyanv1.Allowance{levyanv1.Allowance_builder{Id: "al-1", Meter: "loft.assets.active", Quantity: 500, DateEffective: timestamppb.Now(), DateExpires: until}.Build()},
		Pools:      []*levyanv1.Pool{levyanv1.Pool_builder{Id: "pl-1", Meter: "loft.assets.active", Granted: 10000, Left: "9300", DateEffective: timestamppb.Now(), DateExpires: expires}.Build()},
		Total:      7419,
	}.Build()}.Build()
	usage := &app.PaymentGetUsageResponse{}
	x.NoError(move(u, usage))
	l := usage.GetUsage().GetLines()[0]
	x.Equal([]string{"438.709677", "338.709677", "774.193548", "700"}, []string{l.GetIncluded(), l.GetAllowance(), l.GetBillable(), l.GetPooled()})
	x.Equal([]string{"pl-1", "700"}, []string{l.GetPools()[0].GetPool(), l.GetPools()[0].GetTaken()})
	a := usage.GetUsage().GetAllowances()[0]
	x.Equal([]any{"al-1", "loft.assets.active", int64(500)}, []any{a.GetId(), a.GetMeter(), a.GetQuantity()})
	x.True(until.AsTime().Equal(a.GetDateExpires().AsTime()))
	p := usage.GetUsage().GetPools()[0]
	x.Equal([]any{"pl-1", int64(10000), "9300"}, []any{p.GetId(), p.GetGranted(), p.GetLeft()})
	x.True(expires.AsTime().Equal(p.GetDateExpires().AsTime()))

	credit := levyanv1.Credit_builder{Id: "cr-1", Workspace: "ws-1", Granted: 100000, Left: 70000, Currency: "KRW", DateExpires: expires, DateCreated: timestamppb.Now()}.Build()
	payer := &app.PaymentGetPayerResponse{}
	x.NoError(move(levyanv1.GetPayerResponse_builder{Payer: levyanv1.Payer_builder{Workspace: "ws-1", Credits: []*levyanv1.Credit{credit}}.Build()}.Build(), payer))
	c := payer.GetPayer().GetCredits()[0]
	x.Equal([]any{"cr-1", "ws-1", int64(100000), int64(70000), "KRW"}, []any{c.GetId(), c.GetWorkspace(), c.GetGranted(), c.GetLeft(), c.GetCurrency()})
	x.True(expires.AsTime().Equal(c.GetDateExpires().AsTime()))
}
