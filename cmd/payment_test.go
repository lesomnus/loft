package cmd_test

import (
	"context"
	"errors"
	"testing"
	"time"

	levyanv1 "github.com/lesomnus/levyan/api/levyan/v1"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
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
	// billing account it bills, and of itself.
	soon := time.Now().Add(7 * 24 * time.Hour)
	fake.Dun(levyanv1.Dunning_builder{
		Invoice: "inv-1", BillingAccount: ba.GetId(), Kind: levyanv1.DeclineKind_DECLINE_KIND_REPLACE,
		Failed: timestamppb.Now(), GraceEnds: timestamppb.New(soon), RestrictedFrom: timestamppb.New(soon),
	}.Build(), tenantId(x, w.tenant).String())
	got, err = pay.GetBillingAccount(me, app.PaymentGetBillingAccountRequest_builder{Id: ba.GetId()}.Build())
	x.NoError(err)
	x.Len(got.GetDunning(), 1)
	x.Equal(app.BillingDeclineKind_BILLING_DECLINE_KIND_REPLACE, got.GetDunning()[0].GetKind())
	x.False(got.GetDunning()[0].HasNextRetry(), "a method to replace is tried again as it is never")
	inv, err = pay.GetInvoice(me, app.PaymentGetInvoiceRequest_builder{Id: "inv-1"}.Build())
	x.NoError(err)
	x.WithinDuration(soon, inv.GetDunning().GetRestrictedFrom().AsTime(), time.Second)
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
	// charged again, and from when the workspace is restricted.
	retry, from := time.Now().Add(24*time.Hour), time.Now().Add(7*24*time.Hour)
	fake.Dun(levyanv1.Dunning_builder{
		Invoice: "inv-1", Kind: levyanv1.DeclineKind_DECLINE_KIND_TEMPORARY, Failed: timestamppb.Now(),
		NextRetry: timestamppb.New(retry), GraceEnds: timestamppb.New(from), RestrictedFrom: timestamppb.New(from),
	}.Build(), ws.String())
	p, err = pay.GetPayer(w.owner, app.PaymentGetPayerRequest_builder{}.Build())
	x.NoError(err)
	x.Len(p.GetPayer().GetDunning(), 1)
	d := p.GetPayer().GetDunning()[0]
	x.Equal(app.BillingDeclineKind_BILLING_DECLINE_KIND_TEMPORARY, d.GetKind())
	x.WithinDuration(retry, d.GetNextRetry().AsTime(), time.Second)
	x.WithinDuration(from, d.GetRestrictedFrom().AsTime(), time.Second)

	// A workspace whose payer left is said to be, and when it is restricted.
	fake.Payerless(ws.String(), time.Now().Add(levyantest.Grace))
	p, err = pay.GetPayer(w.owner, app.PaymentGetPayerRequest_builder{}.Build())
	x.NoError(err)
	x.True(p.GetPayer().HasPayerlessSince())
	x.WithinDuration(time.Now().Add(levyantest.Grace), p.GetPayer().GetRestrictedFrom().AsTime(), time.Minute)
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
