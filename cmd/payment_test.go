package cmd_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	levyanv1 "github.com/lesomnus/levyan/api/levyan/v1"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/lesomnus/payday/pdid"

	app "github.com/lesomnus/loft"
	"github.com/lesomnus/loft/cmd"
	"github.com/lesomnus/loft/internal/levyantest"
	"github.com/lesomnus/loft/server/checkout"
	"github.com/lesomnus/loft/server/payment"
)

// paid is the world billing to a levyan of its own, with a checkout page in
// the sandbox: what a deployment that collects is.
func paid(t *testing.T) (*world, *levyantest.Levyan) {
	fake := levyantest.Start(t)
	return newWorldWith(t, func(c *cmd.Config) {
		c.App.Billing.Levyan = fake.Config()
		c.App.Billing.Checkout = checkout.Config{Environment: checkout.Sandbox, ClientToken: checkout.NewToken(sandboxToken)}
	}), fake
}

func (w *world) payments() app.PaymentServiceClient { return app.NewPaymentServiceClient(w.conn) }

// TestAPayersCallsAreRelayedAsTheAccount (design 7.9): a payer's own calls
// are served as the account itself and relayed to levyan's PayerService with
// the product key and the account in `levyan-account` -- the caller's, at
// roster -- and what levyan answers is answered: a billing account in a
// country, with the channel and currency levyan says of it; a checkout of
// Paddle's, still open the first time it is asked about and done the next,
// whose card is its kind, brand and last four digits; and an offer answered.
func TestAPayersCallsAreRelayedAsTheAccount(t *testing.T) {
	w, fake := paid(t)
	x := w.x
	pay := w.payments()
	park := w.holder("park", "member")
	me := w.session(park)
	account := w.accountOf(park).String()

	made, err := pay.CreateBillingAccount(me, app.PaymentCreateBillingAccountRequest_builder{Country: "KR"}.Build())
	x.NoError(err)
	ba := made.GetBillingAccount()
	x.Equal("KR", ba.GetCountry())
	x.Equal(app.BillingChannel_BILLING_CHANNEL_MOR, ba.GetChannel(), "the channel is levyan's to say")
	x.Equal("KRW", ba.GetCurrency(), "and so is the currency")
	x.Equal([]levyantest.Relayed{{Method: "/levyan.v1.PayerService/CreateBillingAccount", Account: account}}, fake.Relays())

	list, err := pay.ListBillingAccounts(me, app.PaymentListBillingAccountsRequest_builder{}.Build())
	x.NoError(err)
	x.Len(list.GetBillingAccounts(), 1)

	got, err := pay.GetBillingAccount(me, app.PaymentGetBillingAccountRequest_builder{Id: ba.GetId()}.Build())
	x.NoError(err)
	x.Equal(app.BillingCollectionState_BILLING_COLLECTION_STATE_NO_SUBSCRIPTION, got.GetCollection().GetState())
	x.False(got.HasSubscription())

	// The checkout: begun, opened by its transaction on the checkout page,
	// and asked about as the payer comes back.
	started, err := pay.StartCheckout(me, app.PaymentStartCheckoutRequest_builder{BillingAccount: ba.GetId(), SuccessUrl: "http://localhost:8080/billing"}.Build())
	x.NoError(err)
	co := started.GetCheckout()
	x.Equal(app.BillingCheckoutState_BILLING_CHECKOUT_STATE_OPEN, co.GetState())
	x.Regexp(`^txn_[a-z0-9]+$`, co.GetWindow().GetTransaction())
	x.Equal("sandbox", co.GetWindow().GetEnvironment())

	done, err := pay.CompleteCheckout(me, app.PaymentCompleteCheckoutRequest_builder{Checkout: co.GetId()}.Build())
	x.NoError(err)
	x.Equal(app.BillingCheckoutState_BILLING_CHECKOUT_STATE_OPEN, done.GetCheckout().GetState(), "the merchant is making the subscription")
	done, err = pay.CompleteCheckout(me, app.PaymentCompleteCheckoutRequest_builder{Checkout: co.GetId()}.Build())
	x.NoError(err)
	x.Equal(app.BillingCheckoutState_BILLING_CHECKOUT_STATE_COMPLETED, done.GetCheckout().GetState())
	x.Equal(app.BillingCollectionState_BILLING_COLLECTION_STATE_COLLECTABLE, done.GetCollection().GetState())
	m := done.GetSubscription().GetMethod()
	x.Equal([]string{"card", "visa", "4242"}, []string{m.GetType(), m.GetBrand(), m.GetLast4()})

	got, err = pay.GetBillingAccount(me, app.PaymentGetBillingAccountRequest_builder{Id: ba.GetId()}.Build())
	x.NoError(err)
	x.Equal(app.BillingSubscriptionStatus_BILLING_SUBSCRIPTION_STATUS_ACTIVE, got.GetSubscription().GetStatus())

	// Another account reads none of it: levyan answers each its own.
	other := w.session(w.holder("lee", "member"))
	list, err = pay.ListBillingAccounts(other, app.PaymentListBillingAccountsRequest_builder{}.Build())
	x.NoError(err)
	x.Empty(list.GetBillingAccounts())
	_, err = pay.CompleteCheckout(other, app.PaymentCompleteCheckoutRequest_builder{Checkout: co.GetId()}.Build())
	x.Equal(codes.NotFound, status.Code(err))
	for _, r := range fake.Relays() {
		x.NotEmpty(r.Account, r.Method)
	}

	// Invoices are levyan's, with their lines and what credit notes take
	// back of them.
	fake.Bill(levyanv1.Invoice_builder{
		Id: "inv-1", BillingAccount: ba.GetId(), State: levyanv1.InvoiceState_INVOICE_STATE_PAID,
		Start: timestamppb.New(time.Date(2026, 9, 30, 15, 0, 0, 0, time.UTC)), End: timestamppb.New(time.Date(2026, 10, 31, 15, 0, 0, 0, time.UTC)),
		Channel: levyanv1.Channel_CHANNEL_MOR, Currency: "KRW", Subtotal: 4300, Total: 4300, Due: 4300, Paid: 4300,
		Lines: []*levyanv1.InvoiceLine{levyanv1.InvoiceLine_builder{Statement: "st-1", Workspace: tenantId(x, w.tenant).String(), Label: "Acme", Total: 4300}.Build()},
	}.Build(), levyanv1.CreditNote_builder{Id: "cn-1", Invoice: "inv-1", Reason: levyanv1.CreditNoteReason_CREDIT_NOTE_REASON_CORRECTION, Amount: 300, Total: 300, Credited: 300}.Build())
	invs, err := pay.ListInvoices(me, app.PaymentListInvoicesRequest_builder{}.Build())
	x.NoError(err)
	x.Len(invs.GetInvoices(), 1)
	x.Empty(invs.GetInvoices()[0].GetLines(), "a list is without lines")
	inv, err := pay.GetInvoice(me, app.PaymentGetInvoiceRequest_builder{Id: "inv-1"}.Build())
	x.NoError(err)
	x.Equal("Acme", inv.GetInvoice().GetLines()[0].GetLabel())
	x.EqualValues(300, inv.GetCreditNotes()[0].GetTotal())
	notes, err := pay.ListCreditNotes(me, app.PaymentListCreditNotesRequest_builder{}.Build())
	x.NoError(err)
	x.Len(notes.GetCreditNotes(), 1)

	// An invoice whose charge failed is said with what is done of it: of the
	// billing account it bills, and of itself -- from when what it bills is
	// restricted, and from when it is read-only (D64).
	soon, later := time.Now().Add(7*24*time.Hour), time.Now().Add(60*24*time.Hour)
	fake.Dun(levyanv1.Dunning_builder{
		Invoice: "inv-1", BillingAccount: ba.GetId(), Kind: levyanv1.DeclineKind_DECLINE_KIND_REPLACE,
		Failed: timestamppb.Now(), GraceEnds: timestamppb.New(soon), RestrictedFrom: timestamppb.New(soon),
		ReadonlyFrom: timestamppb.New(later),
	}.Build(), tenantId(x, w.tenant).String())
	got, err = pay.GetBillingAccount(me, app.PaymentGetBillingAccountRequest_builder{Id: ba.GetId()}.Build())
	x.NoError(err)
	x.Len(got.GetDunning(), 1)
	x.Equal(app.BillingDeclineKind_BILLING_DECLINE_KIND_REPLACE, got.GetDunning()[0].GetKind())
	x.False(got.GetDunning()[0].HasNextRetry(), "a method to replace is tried again as it is never")
	x.WithinDuration(later, got.GetDunning()[0].GetReadonlyFrom().AsTime(), time.Second)
	inv, err = pay.GetInvoice(me, app.PaymentGetInvoiceRequest_builder{Id: "inv-1"}.Build())
	x.NoError(err)
	x.WithinDuration(soon, inv.GetDunning().GetRestrictedFrom().AsTime(), time.Second)
	x.WithinDuration(later, inv.GetDunning().GetReadonlyFrom().AsTime(), time.Second)
}

// TestWhoPaysIsTheWorkspacesToReadAndItsOwnersToOffer: who pays for a
// workspace, what it used and its statements are read by its owner and its
// administrators, as the product, about the caller's workspace alone; asking
// a member to pay for it is the owner's, of a membership of this workspace
// that is an account's; and the member answers it as itself -- and pays from
// the end of the period.
func TestWhoPaysIsTheWorkspacesToReadAndItsOwnersToOffer(t *testing.T) {
	w, fake := paid(t)
	x := w.x
	ctx := context.Background()
	pay := w.payments()
	ws := tenantId(x, w.tenant)

	// levyan is told who made the workspace, who pays for it from then.
	x.NoError(w.s.Teller.Pass(ctx))
	p, err := pay.GetPayer(w.owner, app.PaymentGetPayerRequest_builder{}.Build())
	x.NoError(err)
	x.Equal(ws.String(), p.GetPayer().GetWorkspace())
	owner := fake.Payer(ws.String()).GetCurrent().GetAccount()
	x.NotEmpty(owner)
	x.Equal(owner, p.GetPayer().GetCurrent().GetAccount())
	x.Equal(app.BillingLinkBegan_BILLING_LINK_BEGAN_ARRIVED, p.GetPayer().GetCurrent().GetBegan())

	admin := w.signIn(w.holder("kim", "admin"))
	member := w.holder("park", "member")
	_, err = pay.GetPayer(admin, app.PaymentGetPayerRequest_builder{}.Build())
	x.NoError(err, "an administrator reads who pays")
	_, err = pay.GetPayer(w.signIn(member), app.PaymentGetPayerRequest_builder{}.Build())
	x.Equal(codes.PermissionDenied, status.Code(err), "and a member does not")

	// The owner asks a member, by their membership.
	_, err = pay.Offer(admin, app.PaymentOfferRequest_builder{Holder: member.GetId()}.Build())
	x.Equal(codes.PermissionDenied, status.Code(err), "asking is the owner's")
	offered, err := pay.Offer(w.owner, app.PaymentOfferRequest_builder{Holder: member.GetId()}.Build())
	x.NoError(err)
	x.Equal(w.accountOf(member).String(), offered.GetOffer().GetAccount())
	x.Equal(ws.String(), offered.GetOffer().GetWorkspace())
	p, err = pay.GetPayer(w.owner, app.PaymentGetPayerRequest_builder{}.Build())
	x.NoError(err)
	x.Equal(offered.GetOffer().GetId(), p.GetPayer().GetOffer().GetId())

	// Not of another workspace, nor of a membership that is no account's.
	elsewhere, err := w.s.Base.Tenant().Add(ctx, app.TenantAddRequest_builder{Alias: "hooli"}.Build())
	x.NoError(err)
	stranger, err := w.s.Identity.AddPerson(ctx, "choi", "최", pdid.Nil)
	x.NoError(err)
	theirs := w.member(elsewhere, stranger, "member")
	_, err = pay.Offer(w.owner, app.PaymentOfferRequest_builder{Holder: theirs.GetId()}.Build())
	x.Equal(codes.InvalidArgument, status.Code(err))
	nobody, err := w.s.Base.Holder().Add(ctx, app.HolderAddRequest_builder{
		Tenant: app.TenantRef_builder{Id: w.tenant.GetId()}.Build(), Alias: "robot",
	}.Build())
	x.NoError(err)
	_, err = pay.Offer(w.owner, app.PaymentOfferRequest_builder{Holder: nobody.GetId()}.Build())
	x.Equal(codes.FailedPrecondition, status.Code(err))

	// The member answers as itself.
	me := w.session(member)
	offers, err := pay.ListOffers(me, app.PaymentListOffersRequest_builder{}.Build())
	x.NoError(err)
	x.Len(offers.GetOffers(), 1)
	link, err := pay.Accept(me, app.PaymentAcceptRequest_builder{Offer: offers.GetOffers()[0].GetId()}.Build())
	x.NoError(err)
	x.Equal(app.BillingLinkBegan_BILLING_LINK_BEGAN_ACCEPTED, link.GetLink().GetBegan())
	p, err = pay.GetPayer(w.owner, app.PaymentGetPayerRequest_builder{}.Build())
	x.NoError(err)
	x.Equal(owner, p.GetPayer().GetCurrent().GetAccount(), "who pays now pays until the end of the period")
	x.Equal(w.accountOf(member).String(), p.GetPayer().GetNext().GetAccount())
	links, err := pay.ListLinks(me, app.PaymentListLinksRequest_builder{}.Build())
	x.NoError(err)
	x.Len(links.GetLinks(), 1)

	// What is done of an invoice billing it whose charge failed: when it is
	// charged again, and from when the workspace is restricted and read-only.
	retry, from, readonly := time.Now().Add(24*time.Hour), time.Now().Add(7*24*time.Hour), time.Now().Add(60*24*time.Hour)
	fake.Dun(levyanv1.Dunning_builder{
		Invoice: "inv-1", Kind: levyanv1.DeclineKind_DECLINE_KIND_TEMPORARY, Failed: timestamppb.Now(),
		NextRetry: timestamppb.New(retry), GraceEnds: timestamppb.New(from), RestrictedFrom: timestamppb.New(from),
		ReadonlyFrom: timestamppb.New(readonly),
	}.Build(), ws.String())
	p, err = pay.GetPayer(w.owner, app.PaymentGetPayerRequest_builder{}.Build())
	x.NoError(err)
	x.Len(p.GetPayer().GetDunning(), 1)
	d := p.GetPayer().GetDunning()[0]
	x.Equal(app.BillingDeclineKind_BILLING_DECLINE_KIND_TEMPORARY, d.GetKind())
	x.WithinDuration(retry, d.GetNextRetry().AsTime(), time.Second)
	x.WithinDuration(from, d.GetRestrictedFrom().AsTime(), time.Second)
	x.WithinDuration(readonly, d.GetReadonlyFrom().AsTime(), time.Second)

	// A workspace whose payer left is said to be, and when it is restricted
	// and read-only for it.
	fake.Payerless(ws.String(), time.Now().Add(levyantest.Grace))
	p, err = pay.GetPayer(w.owner, app.PaymentGetPayerRequest_builder{}.Build())
	x.NoError(err)
	x.True(p.GetPayer().HasPayerlessSince())
	x.WithinDuration(time.Now().Add(levyantest.Grace), p.GetPayer().GetRestrictedFrom().AsTime(), time.Minute)
	x.WithinDuration(time.Now().Add(levyantest.ReadOnlyAfter), p.GetPayer().GetReadonlyFrom().AsTime(), time.Minute)
}

// TestEveryMemberIsToldTheWorkspaceIsRestricted (design 7.9): what levyan
// restricts the workspace by -- the copy of its grants every write is held to
// -- is said to every member's page as it is read, whatever their role, and
// no longer once the grant ends.
func TestEveryMemberIsToldTheWorkspaceIsRestricted(t *testing.T) {
	w, fake := paid(t)
	x := w.x
	follow(t, w, fake)
	me := app.NewPartyServiceClient(w.conn)
	member := w.signIn(w.holder("park", "member"))

	v, err := me.Me(member, app.PartyMeRequest_builder{}.Build())
	x.NoError(err)
	x.False(v.GetRestricted())

	g := fake.Restrict(tenantId(x, w.tenant))
	x.Eventually(func() bool { return w.restricted(w.tenant) }, 10*time.Second, 10*time.Millisecond)
	for _, as := range []context.Context{member, w.owner} {
		v, err = me.Me(as, app.PartyMeRequest_builder{}.Build())
		x.NoError(err)
		x.True(v.GetRestricted())
	}

	fake.End(g)
	x.Eventually(func() bool { return !w.restricted(w.tenant) }, 10*time.Second, 10*time.Millisecond)
	v, err = me.Me(member, app.PartyMeRequest_builder{}.Build())
	x.NoError(err)
	x.False(v.GetRestricted())

	t.Run("a deployment that bills nobody restricts nobody", func(t *testing.T) {
		x := require.New(t)
		w := newWorld(t)
		v, err := app.NewPartyServiceClient(w.conn).Me(w.owner, app.PartyMeRequest_builder{}.Build())
		x.NoError(err)
		x.False(v.GetRestricted())
	})
}

// TestUsageAndStatementsAreOfTheCallersWorkspace: what a workspace used in
// the period now open is what levyan works out of what it was handed -- the
// meters this deployment measured -- and its statements are levyan's; both
// asked of the caller's workspace, which no request names.
func TestUsageAndStatementsAreOfTheCallersWorkspace(t *testing.T) {
	w, fake := paid(t)
	x := w.x
	ctx := context.Background()
	pay := w.payments()
	ws := tenantId(x, w.tenant)

	_, err := app.NewAssetServiceClient(w.conn).Add(w.owner, app.AssetAddRequest_builder{Name: "노트북"}.Build())
	x.NoError(err)
	x.NoError(w.s.Meters.Read(ctx, ws))

	u, err := pay.GetUsage(w.owner, app.PaymentGetUsageRequest_builder{}.Build())
	x.NoError(err)
	x.Equal(ws.String(), u.GetUsage().GetWorkspace())
	x.Equal("KRW", u.GetUsage().GetCurrency())
	lines := map[string]string{}
	for _, l := range u.GetUsage().GetLines() {
		lines[l.GetMeter()] = l.GetUsage()
	}
	x.Equal("1", lines["loft.assets.active"])
	x.Equal("1", lines["loft.members"])
	x.EqualValues(levyantest.Prices["loft.assets.active"]+levyantest.Prices["loft.members"], u.GetUsage().GetTotal())

	fake.State(levyanv1.Statement_builder{
		Id: "st-1", Workspace: ws.String(), Currency: "KRW", Total: 1100,
		Period: levyanv1.Period_builder{Start: timestamppb.New(time.Date(2026, 8, 31, 15, 0, 0, 0, time.UTC)), End: timestamppb.New(time.Date(2026, 9, 30, 15, 0, 0, 0, time.UTC)), Zone: "Asia/Seoul"}.Build(),
	}.Build())
	fake.State(levyanv1.Statement_builder{Id: "elsewhere", Workspace: pdid.New(1).String(), Total: 1}.Build())
	sts, err := pay.ListStatements(w.owner, app.PaymentListStatementsRequest_builder{}.Build())
	x.NoError(err)
	x.Len(sts.GetStatements(), 1)
	x.Equal("st-1", sts.GetStatements()[0].GetId())
}

// TestWhatLevyansOperatorGaveIsRelayed (design 7.11, levyan#43): what
// levyan's operator gave a workspace and a billing account is read as levyan
// says it, and granted by nothing here. Whoever runs the workspace reads the
// allowances in force this period and the pools it takes from -- what the
// periods closed left of each -- what each line of the usage and of a
// statement closed was given and took, and the credit granted the workspace
// itself; and the payer reads what their billing account's next invoices may
// be paid with -- its own credit, and that of the workspaces it pays for --
// its balance, and what paid an invoice, which the list of them does not
// say. Another account reads none of the payer's.
func TestWhatLevyansOperatorGaveIsRelayed(t *testing.T) {
	w, fake := paid(t)
	x := w.x
	ctx := context.Background()
	pay := w.payments()
	ws := tenantId(x, w.tenant).String()

	// The owner pays for it, with a billing account made before levyan is
	// told who made the workspace.
	made, err := pay.CreateBillingAccount(w.owner, app.PaymentCreateBillingAccountRequest_builder{Country: "KR"}.Build())
	x.NoError(err)
	ba := made.GetBillingAccount().GetId()
	x.NoError(w.s.Teller.Pass(ctx))

	// Three assets and the owner, measured.
	assets := app.NewAssetServiceClient(w.conn)
	for _, name := range []string{"노트북", "모니터", "의자"} {
		_, err := assets.Add(w.owner, app.AssetAddRequest_builder{Name: name}.Build())
		x.NoError(err)
	}
	x.NoError(w.s.Meters.Read(ctx, tenantId(x, w.tenant)))

	// From the period before: two more assets each period for half a year, a
	// pool of ten asset-months, and one of five member-months that expires
	// later. The period before closed, and took one of each.
	now := time.Now()
	before, _ := levyantest.PeriodBefore(now)
	until, expires := now.AddDate(0, 6, 0).UTC().Truncate(time.Second), now.AddDate(1, 0, 0).UTC().Truncate(time.Second)
	allowance := fake.Allow(ws, levyanv1.Allowance_builder{Meter: "loft.assets.active", Quantity: 2, From: timestamppb.New(before), Until: timestamppb.New(until)}.Build())
	ten := fake.Pool(ws, levyanv1.Pool_builder{Meter: "loft.assets.active", Granted: 10, From: timestamppb.New(before), Expires: timestamppb.New(expires)}.Build())
	five := fake.Pool(ws, levyanv1.Pool_builder{Meter: "loft.members", Granted: 5, From: timestamppb.New(before), Expires: timestamppb.New(expires.AddDate(1, 0, 0))}.Build())
	closed := fake.ClosePeriod(ws)

	// What is billable past what is included, taken out of the pools before
	// it is priced: nothing is left to price.
	u, err := pay.GetUsage(w.owner, app.PaymentGetUsageRequest_builder{}.Build())
	x.NoError(err)
	usage := u.GetUsage()
	x.Len(usage.GetAllowances(), 1)
	a := usage.GetAllowances()[0]
	x.Equal([]any{allowance.GetId(), "loft.assets.active", int64(2)}, []any{a.GetId(), a.GetMeter(), a.GetQuantity()})
	x.Equal(before, a.GetFrom().AsTime())
	x.Equal(until, a.GetUntil().AsTime())
	x.Len(usage.GetPools(), 2)
	x.Equal([]string{ten.GetId(), five.GetId()}, []string{usage.GetPools()[0].GetId(), usage.GetPools()[1].GetId()}, "the soonest to expire first")
	x.Equal([]any{int64(10), "9"}, []any{usage.GetPools()[0].GetGranted(), usage.GetPools()[0].GetLeft()}, "what the period closed left")
	x.Equal(expires, usage.GetPools()[0].GetExpires().AsTime())
	x.Equal("4", usage.GetPools()[1].GetLeft())
	lines := map[string]*app.BillingLine{}
	for _, l := range usage.GetLines() {
		lines[l.GetMeter()] = l
	}
	l := lines["loft.assets.active"]
	x.Equal([]string{"3", "2", "2", "1", "1", "0"}, []string{l.GetUsage(), l.GetIncluded(), l.GetAllowance(), l.GetBillable(), l.GetPooled(), l.GetAmount()})
	x.Len(l.GetPools(), 1)
	x.Equal([]string{ten.GetId(), "1"}, []string{l.GetPools()[0].GetPool(), l.GetPools()[0].GetTaken()})
	l = lines["loft.members"]
	x.Equal([]string{"1", "0", "0", "1", "1", "0"}, []string{l.GetUsage(), l.GetIncluded(), l.GetAllowance(), l.GetBillable(), l.GetPooled(), l.GetAmount()})
	x.EqualValues(0, usage.GetTotal())

	// And so do the lines of the statement that closed.
	sts, err := pay.ListStatements(w.owner, app.PaymentListStatementsRequest_builder{}.Build())
	x.NoError(err)
	x.Len(sts.GetStatements(), 1)
	x.Equal(closed.GetId(), sts.GetStatements()[0].GetId())
	for _, l := range sts.GetStatements()[0].GetLines() {
		if l.GetMeter() == "loft.assets.active" {
			x.Equal([]string{"2", "1"}, []string{l.GetAllowance(), l.GetPooled()})
			x.Equal(ten.GetId(), l.GetPools()[0].GetPool())
		}
	}

	// Credit granted the workspace, the billing account and another
	// workspace, and what the billing account is owed beside it.
	mine := fake.Credit(levyanv1.Credit_builder{Workspace: ws, Granted: 70000, Expires: timestamppb.New(until)}.Build())
	theirs := fake.Credit(levyanv1.Credit_builder{BillingAccount: ba, Granted: 30000, Expires: timestamppb.New(expires)}.Build())
	fake.Credit(levyanv1.Credit_builder{Workspace: pdid.New(1).String(), Granted: 1000}.Build())
	fake.Balance(ba, 5000)

	// Whoever runs it reads the workspace's, and only that.
	admin := w.signIn(w.holder("kim", "admin"))
	for _, as := range []context.Context{w.owner, admin} {
		p, err := pay.GetPayer(as, app.PaymentGetPayerRequest_builder{}.Build())
		x.NoError(err)
		x.Len(p.GetPayer().GetCredits(), 1)
		c := p.GetPayer().GetCredits()[0]
		x.Equal([]any{mine.GetId(), ws, "", int64(70000), int64(70000), "KRW"}, []any{c.GetId(), c.GetWorkspace(), c.GetBillingAccount(), c.GetGranted(), c.GetLeft(), c.GetCurrency()})
		x.Equal(until, c.GetExpires().AsTime())
	}

	// The payer reads what their billing account may be paid with: the
	// workspace's it pays for, then its own, and its balance.
	got, err := pay.GetBillingAccount(w.owner, app.PaymentGetBillingAccountRequest_builder{Id: ba}.Build())
	x.NoError(err)
	x.Len(got.GetCredits(), 2)
	x.Equal([]string{mine.GetId(), theirs.GetId()}, []string{got.GetCredits()[0].GetId(), got.GetCredits()[1].GetId()}, "the soonest to expire first")
	x.Equal(ba, got.GetCredits()[1].GetBillingAccount())
	x.EqualValues(5000, got.GetBalance())
	_, err = pay.GetBillingAccount(w.session(w.holder("lee", "member")), app.PaymentGetBillingAccountRequest_builder{Id: ba}.Build())
	x.Equal(codes.NotFound, status.Code(err), "another account reads none of it")

	// An invoice of the period before is paid with it first: the
	// workspace's credit as far as its lines go, then the billing
	// account's.
	inv := fake.Finalize(ba, levyanv1.InvoiceLine_builder{Statement: closed.GetId(), Workspace: ws, Label: "Acme", Total: 80000}.Build())
	v, err := pay.GetInvoice(w.owner, app.PaymentGetInvoiceRequest_builder{Id: inv.GetId()}.Build())
	x.NoError(err)
	x.Equal(app.BillingInvoiceState_BILLING_INVOICE_STATE_PAID, v.GetInvoice().GetState())
	x.Equal([]int64{80000, 80000, 0}, []int64{v.GetInvoice().GetTotal(), v.GetInvoice().GetCredited(), v.GetInvoice().GetDue()})
	paid := v.GetInvoice().GetCredits()
	x.Len(paid, 2)
	x.Equal([]any{mine.GetId(), int64(70000), ws}, []any{paid[0].GetCredit(), paid[0].GetAmount(), paid[0].GetWorkspace()})
	x.Equal(until, paid[0].GetExpires().AsTime())
	x.Equal([]any{theirs.GetId(), int64(10000), ""}, []any{paid[1].GetCredit(), paid[1].GetAmount(), paid[1].GetWorkspace()})
	invs, err := pay.ListInvoices(w.owner, app.PaymentListInvoicesRequest_builder{}.Build())
	x.NoError(err)
	x.Len(invs.GetInvoices(), 1)
	x.Empty(invs.GetInvoices()[0].GetCredits(), "a list says what credit paid, and not which")
	x.EqualValues(80000, invs.GetInvoices()[0].GetCredited())

	got, err = pay.GetBillingAccount(w.owner, app.PaymentGetBillingAccountRequest_builder{Id: ba}.Build())
	x.NoError(err)
	x.Len(got.GetCredits(), 1, "the workspace's is spent")
	x.Equal([]any{theirs.GetId(), int64(20000)}, []any{got.GetCredits()[0].GetId(), got.GetCredits()[0].GetLeft()})
	x.EqualValues(5000, got.GetBalance(), "spent after the credit, and not needed")
	p, err := pay.GetPayer(w.owner, app.PaymentGetPayerRequest_builder{}.Build())
	x.NoError(err)
	x.Empty(p.GetPayer().GetCredits())
}

// TestAPayersStatementsAreRelayedAsTheAccount (levyan#47, design 7.12): the
// statements of the periods a payer's billing accounts paid for are levyan's
// PayerService's, relayed as the account -- each line with what levyan's
// operator gave and what a pool took, as the workspace's own settings read
// the same statement -- and each line of the payer's invoice is read by the
// statement it names. Another account reads none of them, though it runs
// the workspace and reads its statements there; and naming the payer's
// billing account is no way to them.
func TestAPayersStatementsAreRelayedAsTheAccount(t *testing.T) {
	w, fake := paid(t)
	x := w.x
	ctx := context.Background()
	pay := w.payments()
	ws := tenantId(x, w.tenant).String()

	// The owner pays for it, with a billing account made before levyan is
	// told who made the workspace.
	made, err := pay.CreateBillingAccount(w.owner, app.PaymentCreateBillingAccountRequest_builder{Country: "KR"}.Build())
	x.NoError(err)
	ba := made.GetBillingAccount().GetId()
	account := fake.Relays()[0].Account
	x.NoError(w.s.Teller.Pass(ctx))

	// Three assets and the owner, measured; two more assets each period, and
	// a pool of ten asset-months, from the period before; and the period
	// before closed.
	assets := app.NewAssetServiceClient(w.conn)
	for _, name := range []string{"노트북", "모니터", "의자"} {
		_, err := assets.Add(w.owner, app.AssetAddRequest_builder{Name: name}.Build())
		x.NoError(err)
	}
	x.NoError(w.s.Meters.Read(ctx, tenantId(x, w.tenant)))
	before, _ := levyantest.PeriodBefore(time.Now())
	fake.Allow(ws, levyanv1.Allowance_builder{Meter: "loft.assets.active", Quantity: 2, From: timestamppb.New(before)}.Build())
	ten := fake.Pool(ws, levyanv1.Pool_builder{Meter: "loft.assets.active", Granted: 10, From: timestamppb.New(before), Expires: timestamppb.New(time.Now().AddDate(1, 0, 0))}.Build())
	closed := fake.ClosePeriod(ws)

	// The payer's, as the account: every one of its billing accounts', and
	// that one's.
	n := len(fake.Relays())
	for _, of := range []string{"", ba} {
		list, err := pay.ListPayerStatements(w.owner, app.PaymentListPayerStatementsRequest_builder{BillingAccount: of}.Build())
		x.NoError(err)
		x.Len(list.GetStatements(), 1)
		x.Equal(closed.GetId(), list.GetStatements()[0].GetId())
	}
	got, err := pay.GetPayerStatement(w.owner, app.PaymentGetPayerStatementRequest_builder{Id: closed.GetId()}.Build())
	x.NoError(err)
	st := got.GetStatement()
	x.Equal(ws, st.GetWorkspace())
	x.Equal(closed.GetTotal(), st.GetTotal())
	x.Equal([]levyantest.Relayed{
		{Method: "/levyan.v1.PayerService/ListStatements", Account: account},
		{Method: "/levyan.v1.PayerService/ListStatements", Account: account},
		{Method: "/levyan.v1.PayerService/GetStatement", Account: account},
	}, fake.Relays()[n:], "relayed as the account, and never read with the product's key")
	var l *app.BillingLine
	for _, v := range st.GetLines() {
		if v.GetMeter() == "loft.assets.active" {
			l = v
		}
	}
	x.NotNil(l)
	x.Equal([]string{"3", "2", "2", "1", "1", "0"}, []string{l.GetUsage(), l.GetIncluded(), l.GetAllowance(), l.GetBillable(), l.GetPooled(), l.GetAmount()})
	x.Equal([]string{ten.GetId(), "1"}, []string{l.GetPools()[0].GetPool(), l.GetPools()[0].GetTaken()})

	// As the workspace's settings read it.
	sts, err := pay.ListStatements(w.owner, app.PaymentListStatementsRequest_builder{}.Build())
	x.NoError(err)
	x.Len(sts.GetStatements(), 1)
	x.True(proto.Equal(sts.GetStatements()[0], st))

	// Each line of the payer's invoice is the statement it names.
	inv := fake.Finalize(ba, levyanv1.InvoiceLine_builder{Statement: closed.GetId(), Workspace: ws, Label: "Acme", Total: closed.GetTotal()}.Build())
	v, err := pay.GetInvoice(w.owner, app.PaymentGetInvoiceRequest_builder{Id: inv.GetId()}.Build())
	x.NoError(err)
	x.Len(v.GetInvoice().GetLines(), 1)
	line, err := pay.GetPayerStatement(w.owner, app.PaymentGetPayerStatementRequest_builder{Id: v.GetInvoice().GetLines()[0].GetStatement()}.Build())
	x.NoError(err)
	x.True(proto.Equal(st, line.GetStatement()))

	// Another account reads none of them -- an administrator of the
	// workspace among them, who reads its statements in its settings -- and
	// the payer's billing account is none of its.
	admin := w.holder("kim", "admin")
	for _, as := range []context.Context{w.session(admin), w.session(w.holder("lee", "member"))} {
		list, err := pay.ListPayerStatements(as, app.PaymentListPayerStatementsRequest_builder{}.Build())
		x.NoError(err)
		x.Empty(list.GetStatements())
		_, err = pay.GetPayerStatement(as, app.PaymentGetPayerStatementRequest_builder{Id: closed.GetId()}.Build())
		x.Equal(codes.NotFound, status.Code(err))
		_, err = pay.ListPayerStatements(as, app.PaymentListPayerStatementsRequest_builder{BillingAccount: ba}.Build())
		x.Equal(codes.NotFound, status.Code(err))
	}
	sts, err = pay.ListStatements(w.signIn(admin), app.PaymentListStatementsRequest_builder{}.Build())
	x.NoError(err)
	x.Len(sts.GetStatements(), 1, "the workspace's, which its administrator reads in its settings")
}

// TestWhatLevyanRefusesIsSaid: a refusal of levyan's keeps its code, in this
// deployment's words with levyan's after them -- what a page shows of a levyan
// with no roster to ask about an account, as staging's is -- and a levyan that
// cannot be reached is one to try again; a deployment that names none, or
// has no page to open a checkout on, says so before it asks anything.
func TestWhatLevyanRefusesIsSaid(t *testing.T) {
	w, fake := paid(t)
	x := w.x
	pay := w.payments()
	me := w.session(w.holder("park", "member"))

	fake.WithoutRoster(true)
	_, err := pay.ListBillingAccounts(me, app.PaymentListBillingAccountsRequest_builder{}.Build())
	x.Equal(codes.FailedPrecondition, status.Code(err))
	x.Equal("결제 서비스가 받지 않았습니다: "+levyantest.NoRoster, status.Convert(err).Message())
	fake.WithoutRoster(false)

	fake.Down(status.Error(codes.Unavailable, "levyantest: down"))
	_, err = pay.GetPayer(w.owner, app.PaymentGetPayerRequest_builder{}.Build())
	x.Equal(codes.Unavailable, status.Code(err))
	x.Contains(status.Convert(err).Message(), "결제 서비스에 지금 닿지 않습니다")
	fake.Down(nil)

	t.Run("a levyan nobody answers for", func(t *testing.T) {
		x := require.New(t)
		w := newWorldWith(t, func(c *cmd.Config) { c.App.Billing.Levyan = levyantest.Gone(t) })
		_, err := w.payments().ListOffers(w.session(w.holder("park", "member")), app.PaymentListOffersRequest_builder{}.Build())
		x.Equal(codes.Unavailable, status.Code(err))
	})

	t.Run("no levyan", func(t *testing.T) {
		x := require.New(t)
		w := newWorld(t)
		me := w.session(w.holder("park", "member"))
		_, err := w.payments().ListBillingAccounts(me, app.PaymentListBillingAccountsRequest_builder{}.Build())
		x.True(errors.Is(err, payment.ErrNoBilling) || status.Convert(err).Message() == status.Convert(payment.ErrNoBilling).Message(), "%v", err)
		_, err = w.payments().GetUsage(w.owner, app.PaymentGetUsageRequest_builder{}.Build())
		x.Equal(codes.FailedPrecondition, status.Code(err))
	})

	t.Run("no checkout page, or one of the other environment", func(t *testing.T) {
		x := require.New(t)
		fake := levyantest.Start(t)
		w := newWorldWith(t, func(c *cmd.Config) { c.App.Billing.Levyan = fake.Config() })
		me := w.session(w.holder("park", "member"))
		ba, err := w.payments().CreateBillingAccount(me, app.PaymentCreateBillingAccountRequest_builder{Country: "US"}.Build())
		x.NoError(err)
		x.Equal("USD", ba.GetBillingAccount().GetCurrency())
		_, err = w.payments().StartCheckout(me, app.PaymentStartCheckoutRequest_builder{BillingAccount: ba.GetBillingAccount().GetId(), SuccessUrl: "http://localhost:8080/billing"}.Build())
		x.Equal(codes.FailedPrecondition, status.Code(err))
		x.Contains(status.Convert(err).Message(), "app.billing.checkout")

		live := newWorldWith(t, func(c *cmd.Config) {
			c.App.Billing.Levyan = fake.Config()
			c.App.Billing.Checkout = checkout.Config{Environment: checkout.Live, ClientToken: checkout.NewToken(liveToken)}
		})
		me = live.session(live.holder("park", "member"))
		ba, err = live.payments().CreateBillingAccount(me, app.PaymentCreateBillingAccountRequest_builder{Country: "KR"}.Build())
		x.NoError(err)
		_, err = live.payments().StartCheckout(me, app.PaymentStartCheckoutRequest_builder{BillingAccount: ba.GetBillingAccount().GetId(), SuccessUrl: "http://localhost:8080/billing"}.Build())
		x.Equal(codes.FailedPrecondition, status.Code(err))
		x.Contains(status.Convert(err).Message(), "sandbox")
	})
}

// TestWhoABillingAccountIsBilledToPassesThrough (design 7.9): who receives
// what a billing account is billed and the business it is billed to are the
// payer's to say as it is made and again later, and to clear -- relayed to
// levyan as they are, and kept nowhere here, since they are somebody's. The
// business one paid by transfer is billed to is its contract's: levyan
// refuses the account a change of it, and the account is told so in this
// deployment's words; who receives it the account still says.
func TestWhoABillingAccountIsBilledToPassesThrough(t *testing.T) {
	w, fake := paid(t)
	x := w.x
	pay := w.payments()
	park := w.holder("park", "member")
	me := w.session(park)
	account := w.accountOf(park).String()

	// Made up, every word of it.
	recipient := app.BillingRecipient_builder{Name: "박지민", Email: "billing@example.com", Address: "서울시 중구 예시로 1"}.Build()
	business := app.BillingBusiness_builder{
		Registration: "000-12-34567", Name: "(주)예시상사", Representative: "박지민", Address: "서울시 중구 예시로 1",
		Kind: "서비스", Item: "소프트웨어", Email: "tax@example.com",
	}.Build()
	made, err := pay.CreateBillingAccount(me, app.PaymentCreateBillingAccountRequest_builder{Country: "KR", Recipient: recipient, Business: business}.Build())
	x.NoError(err)
	ba := made.GetBillingAccount()
	x.True(proto.Equal(recipient, ba.GetRecipient()), "%v", ba.GetRecipient())
	x.True(proto.Equal(business, ba.GetBusiness()), "%v", ba.GetBusiness())
	held := fake.BillingAccounts(account)
	x.Len(held, 1)
	x.Equal("billing@example.com", held[0].GetRecipient().GetEmail(), "levyan holds it")
	x.Equal("000-12-34567", held[0].GetBusiness().GetRegistration())

	// Said again: who receives it, whole; what is left out stays as it is.
	moved := app.BillingRecipient_builder{Name: "박지민", Email: "pay@example.com"}.Build()
	got, err := pay.UpdateBillingAccount(me, app.PaymentUpdateBillingAccountRequest_builder{Id: ba.GetId(), Recipient: moved}.Build())
	x.NoError(err)
	x.True(proto.Equal(moved, got.GetBillingAccount().GetRecipient()))
	x.True(proto.Equal(business, got.GetBillingAccount().GetBusiness()), "the business is left as it was")
	renamed := proto.CloneOf(business)
	renamed.SetName("(주)예시")
	got, err = pay.UpdateBillingAccount(me, app.PaymentUpdateBillingAccountRequest_builder{Id: ba.GetId(), Business: renamed}.Build())
	x.NoError(err)
	x.Equal("(주)예시", got.GetBillingAccount().GetBusiness().GetName())
	got, err = pay.UpdateBillingAccount(me, app.PaymentUpdateBillingAccountRequest_builder{Id: ba.GetId(), ClearBusiness: true}.Build())
	x.NoError(err)
	x.False(got.GetBillingAccount().HasBusiness(), "billed as a person")
	x.True(proto.Equal(moved, got.GetBillingAccount().GetRecipient()))

	// One paid by transfer, opened by levyan's operator after a contract.
	bank := fake.Bank(account, "계약", levyanv1.Business_builder{Registration: "000-98-76543", Name: "(주)계약상사", Email: "contract@example.com"}.Build())
	for _, req := range []*app.PaymentUpdateBillingAccountRequest{
		app.PaymentUpdateBillingAccountRequest_builder{Id: bank.GetId(), Business: business}.Build(),
		app.PaymentUpdateBillingAccountRequest_builder{Id: bank.GetId(), ClearBusiness: true}.Build(),
		app.PaymentUpdateBillingAccountRequest_builder{Id: bank.GetId(), Recipient: moved, Business: business}.Build(),
	} {
		_, err = pay.UpdateBillingAccount(me, req)
		x.Equal(codes.FailedPrecondition, status.Code(err))
		x.Contains(status.Convert(err).Message(), "결제 서비스가 받지 않았습니다: the business a billing account paid by transfer is billed to is its contract's")
	}
	got, err = pay.UpdateBillingAccount(me, app.PaymentUpdateBillingAccountRequest_builder{Id: bank.GetId(), Recipient: moved}.Build())
	x.NoError(err, "who receives it is the account's to say")
	x.True(proto.Equal(moved, got.GetBillingAccount().GetRecipient()))
	x.Equal(app.BillingChannel_BILLING_CHANNEL_BANK_KR, got.GetBillingAccount().GetChannel())
	x.Equal("000-98-76543", got.GetBillingAccount().GetBusiness().GetRegistration(), "and its business the contract's")
	detail, err := pay.GetBillingAccount(me, app.PaymentGetBillingAccountRequest_builder{Id: bank.GetId()}.Build())
	x.NoError(err)
	x.Equal(app.BillingCollectionState_BILLING_COLLECTION_STATE_UNSPECIFIED, detail.GetCollection().GetState(), "nothing is said of collecting on a channel that is no merchant of record's")
	_, err = pay.StartCheckout(me, app.PaymentStartCheckoutRequest_builder{BillingAccount: bank.GetId(), SuccessUrl: "http://localhost:8080/billing"}.Build())
	x.Equal(codes.FailedPrecondition, status.Code(err), "and it takes no checkout")

	keptNowhere(t, w.s, "000-12-34567", "billing@example.com", "pay@example.com", "tax@example.com", "000-98-76543")
}

// TestWhetherAWorkspacesBillingAccountCanCollectIsSaid (design 7.9, levyan's
// `docs/apps.md` 4): who runs a workspace reads whether the billing account it
// is paid for with now can be collected -- before any charge of it failed --
// and the payer reads the same of the billing account itself; nothing is said
// of it while it is paid for with one on a channel that is no merchant of
// record's.
func TestWhetherAWorkspacesBillingAccountCanCollectIsSaid(t *testing.T) {
	w, fake := paid(t)
	x := w.x
	ctx := context.Background()
	pay := w.payments()
	ws := tenantId(x, w.tenant)

	// The owner's, made before levyan is told who made the workspace: what
	// it is paid for with from then.
	made, err := pay.CreateBillingAccount(w.owner, app.PaymentCreateBillingAccountRequest_builder{Country: "KR"}.Build())
	x.NoError(err)
	ba := made.GetBillingAccount().GetId()
	x.NoError(w.s.Teller.Pass(ctx))
	state := func() app.BillingCollectionState {
		t.Helper()
		p, err := pay.GetPayer(w.owner, app.PaymentGetPayerRequest_builder{}.Build())
		x.NoError(err)
		x.Equal(ba, p.GetPayer().GetCurrent().GetBillingAccount())
		return p.GetPayer().GetCollection().GetState()
	}
	x.Equal(app.BillingCollectionState_BILLING_COLLECTION_STATE_NO_SUBSCRIPTION, state(), "no method registered yet")
	admin := w.signIn(w.holder("kim", "admin"))
	p, err := pay.GetPayer(admin, app.PaymentGetPayerRequest_builder{}.Build())
	x.NoError(err)
	x.Equal(app.BillingCollectionState_BILLING_COLLECTION_STATE_NO_SUBSCRIPTION, p.GetPayer().GetCollection().GetState(), "its administrators read it too")

	fake.Collect(ba, levyanv1.CollectionState_COLLECTION_STATE_PAST_DUE)
	x.Equal(app.BillingCollectionState_BILLING_COLLECTION_STATE_PAST_DUE, state())
	got, err := pay.GetBillingAccount(w.owner, app.PaymentGetBillingAccountRequest_builder{Id: ba}.Build())
	x.NoError(err)
	x.Equal(app.BillingCollectionState_BILLING_COLLECTION_STATE_PAST_DUE, got.GetCollection().GetState(), "and so does the payer, of the billing account")
	fake.Collect(ba, levyanv1.CollectionState_COLLECTION_STATE_UNSPECIFIED)
	x.Equal(app.BillingCollectionState_BILLING_COLLECTION_STATE_NO_SUBSCRIPTION, state())

	// Paid for with one paid by transfer from the end of the period: nothing
	// is said of collecting it.
	owner := fake.Payer(ws.String()).GetCurrent().GetAccount()
	bank := fake.Bank(owner, "계약", levyanv1.Business_builder{Registration: "000-98-76543", Name: "(주)계약상사", Email: "contract@example.com"}.Build())
	_, err = pay.Use(w.owner, app.PaymentUseRequest_builder{Workspace: ws.String(), BillingAccount: bank.GetId()}.Build())
	x.NoError(err)
	fake.Turn(ws.String())
	p, err = pay.GetPayer(w.owner, app.PaymentGetPayerRequest_builder{}.Build())
	x.NoError(err)
	x.Equal(bank.GetId(), p.GetPayer().GetCurrent().GetBillingAccount())
	x.False(p.GetPayer().HasCollection())
}

// keptNowhere fails `t` for a row of the deployment's database that holds
// any of `said`, in any column that could: what passes through to levyan is
// no row of Loft's.
func keptNowhere(t *testing.T, s *cmd.Server, said ...string) {
	t.Helper()
	x := require.New(t)
	ctx := t.Context()
	pg := s.Dialect == "postgres"

	q := `SELECT m.name, p.name, p.type FROM sqlite_master m, pragma_table_info(m.name) p WHERE m.type = 'table'`
	if pg {
		q = `SELECT table_name, column_name, data_type FROM information_schema.columns WHERE table_schema = current_schema()`
	}
	type column struct{ table, name, kind string }
	var cols []column
	rows, err := s.Db.QueryContext(ctx, q)
	x.NoError(err)
	for rows.Next() {
		var c column
		x.NoError(rows.Scan(&c.table, &c.name, &c.kind))
		cols = append(cols, c)
	}
	x.NoError(rows.Err())
	x.NoError(rows.Close())
	x.NotEmpty(cols)

	for _, c := range cols {
		var has string
		switch {
		case !pg:
			has = fmt.Sprintf(`instr(CAST(%q AS BLOB), CAST(? AS BLOB)) > 0`, c.name)
		case c.kind == "bytea":
			has = fmt.Sprintf(`position(convert_to($%%d, 'UTF8') in %q) > 0`, c.name)
		case c.kind == "text" || c.kind == "character varying" || c.kind == "json" || c.kind == "jsonb" || c.kind == "ARRAY":
			has = fmt.Sprintf(`strpos(%q::text, $%%d) > 0`, c.name)
		default:
			continue
		}
		ors := make([]string, len(said))
		args := make([]any, len(said))
		for i, v := range said {
			ors[i], args[i] = has, v
			if pg {
				ors[i] = fmt.Sprintf(has, i+1)
			}
		}
		n := 0
		x.NoError(s.Db.QueryRowContext(ctx, fmt.Sprintf(`SELECT count(*) FROM %q WHERE %s`, c.table, strings.Join(ors, " OR ")), args...).Scan(&n))
		x.Zero(n, "%s.%s holds what was said of a billing account", c.table, c.name)
	}
}
