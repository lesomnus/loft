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
	"github.com/lesomnus/loft/server/payment"
)

// paid is the world billing to a levyan of its own: what a deployment that
// bills is.
func paid(t *testing.T) (*world, *levyantest.Levyan) {
	fake := levyantest.Start(t)
	return newWorldWith(t, func(c *cmd.Config) {
		c.App.Billing.Levyan = fake.Config()
	}), fake
}

func (w *world) payments() app.PaymentServiceClient { return app.NewPaymentServiceClient(w.conn) }

// TestWhoPaysIsTheWorkspacesToReadAndItsOwnersToOffer: who pays for a
// workspace, what it used and its statements are read by its owner and its
// administrators, as the product, about the caller's workspace alone; asking
// a member to pay for it is the owner's, of a membership of this workspace
// that is an account's; and once the member answers it where a payer pays,
// which is not here (D81), the workspace reads that they pay from the end of
// the period.
func TestWhoPaysIsTheWorkspacesToReadAndItsOwnersToOffer(t *testing.T) {
	w, fake := paid(t)
	x := w.x
	ctx := context.Background()
	pay := w.payments()
	ws := tenantId(x, w.tenant)

	// levyan is told who made the workspace, who pays for it from then: by
	// the portal, and not by this deployment (D84).
	w.arrived(fake, ws, w.memberId(w.ownerHolder()))
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
	x.Equal(w.memberId(member).String(), offered.GetOffer().GetAccount())
	x.Equal(ws.String(), offered.GetOffer().GetWorkspace())
	p, err = pay.GetPayer(w.owner, app.PaymentGetPayerRequest_builder{}.Build())
	x.NoError(err)
	x.Equal(offered.GetOffer().GetId(), p.GetPayer().GetOffer().GetId())

	// Not of another workspace.
	elsewhere, err := w.s.Base.Tenant().Add(ctx, app.TenantAddRequest_builder{Alias: "hooli"}.Build())
	x.NoError(err)
	stranger, err := w.s.Identity.AddPerson(ctx, "choi", "최", pdid.Nil)
	x.NoError(err)
	theirs := w.member(elsewhere, stranger, "member")
	_, err = pay.Offer(w.owner, app.PaymentOfferRequest_builder{Holder: theirs.GetId()}.Build())
	x.Equal(codes.InvalidArgument, status.Code(err))

	// The member answers where a payer pays.
	x.NotNil(fake.Accept(ws.String()))
	p, err = pay.GetPayer(w.owner, app.PaymentGetPayerRequest_builder{}.Build())
	x.NoError(err)
	x.Equal(owner, p.GetPayer().GetCurrent().GetAccount(), "who pays now pays until the end of the period")
	x.Equal(w.memberId(member).String(), p.GetPayer().GetNext().GetAccount())
	x.Equal(app.BillingLinkBegan_BILLING_LINK_BEGAN_ACCEPTED, p.GetPayer().GetNext().GetBegan())
	x.False(p.GetPayer().HasOffer(), "what was offered is answered")

	// What is done of an invoice billing it whose charge failed: when it is
	// charged again, and from when the workspace is restricted and read-only.
	retry, from, readonly := time.Now().Add(24*time.Hour), time.Now().Add(7*24*time.Hour), time.Now().Add(60*24*time.Hour)
	fake.Dun(levyanv1.Dunning_builder{
		Invoice: "inv-1", Kind: levyanv1.DeclineKind_DECLINE_KIND_TEMPORARY, DateFailed: timestamppb.Now(),
		DateNextRetry: timestamppb.New(retry), DateGraceEnds: timestamppb.New(from), DateRestricted: timestamppb.New(from),
		DateReadonly: timestamppb.New(readonly),
	}.Build(), ws.String())
	p, err = pay.GetPayer(w.owner, app.PaymentGetPayerRequest_builder{}.Build())
	x.NoError(err)
	x.Len(p.GetPayer().GetDunning(), 1)
	d := p.GetPayer().GetDunning()[0]
	x.Equal(app.BillingDeclineKind_BILLING_DECLINE_KIND_TEMPORARY, d.GetKind())
	x.WithinDuration(retry, d.GetDateNextRetry().AsTime(), time.Second)
	x.WithinDuration(from, d.GetDateRestricted().AsTime(), time.Second)
	x.WithinDuration(readonly, d.GetDateReadonly().AsTime(), time.Second)

	// A workspace whose payer left is said to be, and when it is restricted
	// and read-only for it.
	fake.Payerless(ws.String(), time.Now().Add(levyantest.Grace))
	p, err = pay.GetPayer(w.owner, app.PaymentGetPayerRequest_builder{}.Build())
	x.NoError(err)
	x.True(p.GetPayer().HasDatePayerless())
	x.WithinDuration(time.Now().Add(levyantest.Grace), p.GetPayer().GetDateRestricted().AsTime(), time.Minute)
	x.WithinDuration(time.Now().Add(levyantest.ReadOnlyAfter), p.GetPayer().GetDateReadonly().AsTime(), time.Minute)
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
		Period: levyanv1.Period_builder{DateStart: timestamppb.New(time.Date(2026, 8, 31, 15, 0, 0, 0, time.UTC)), DateEnd: timestamppb.New(time.Date(2026, 9, 30, 15, 0, 0, 0, time.UTC)), Zone: "Asia/Seoul"}.Build(),
	}.Build())
	fake.State(levyanv1.Statement_builder{Id: "elsewhere", Workspace: pdid.New(1).String(), Total: 1}.Build())
	sts, err := pay.ListStatements(w.owner, app.PaymentListStatementsRequest_builder{}.Build())
	x.NoError(err)
	x.Len(sts.GetStatements(), 1)
	x.Equal("st-1", sts.GetStatements()[0].GetId())
}

// TestWhatLevyansOperatorGaveIsRelayed (design 7.11, levyan#43): what
// levyan's operator gave a workspace is read as levyan says it, and granted by
// nothing here. Whoever runs the workspace reads the allowances in force this
// period and the pools it takes from -- what the periods closed left of each
// -- what each line of the usage and of a statement closed was given and
// took, and the credit granted the workspace itself, and no other's.
func TestWhatLevyansOperatorGaveIsRelayed(t *testing.T) {
	w, fake := paid(t)
	x := w.x
	ctx := context.Background()
	pay := w.payments()
	ws := tenantId(x, w.tenant).String()

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
	allowance := fake.Allow(ws, levyanv1.Allowance_builder{Meter: "loft.assets.active", Quantity: 2, DateEffective: timestamppb.New(before), DateExpires: timestamppb.New(until)}.Build())
	ten := fake.Pool(ws, levyanv1.Pool_builder{Meter: "loft.assets.active", Granted: 10, DateEffective: timestamppb.New(before), DateExpires: timestamppb.New(expires)}.Build())
	five := fake.Pool(ws, levyanv1.Pool_builder{Meter: "loft.members", Granted: 5, DateEffective: timestamppb.New(before), DateExpires: timestamppb.New(expires.AddDate(1, 0, 0))}.Build())
	closed := fake.ClosePeriod(ws)

	// What is billable past what is included, taken out of the pools before
	// it is priced: nothing is left to price.
	u, err := pay.GetUsage(w.owner, app.PaymentGetUsageRequest_builder{}.Build())
	x.NoError(err)
	usage := u.GetUsage()
	x.Len(usage.GetAllowances(), 1)
	a := usage.GetAllowances()[0]
	x.Equal([]any{allowance.GetId(), "loft.assets.active", int64(2)}, []any{a.GetId(), a.GetMeter(), a.GetQuantity()})
	x.Equal(before, a.GetDateEffective().AsTime())
	x.Equal(until, a.GetDateExpires().AsTime())
	x.Len(usage.GetPools(), 2)
	x.Equal([]string{ten.GetId(), five.GetId()}, []string{usage.GetPools()[0].GetId(), usage.GetPools()[1].GetId()}, "the soonest to expire first")
	x.Equal([]any{int64(10), "9"}, []any{usage.GetPools()[0].GetGranted(), usage.GetPools()[0].GetLeft()}, "what the period closed left")
	x.Equal(expires, usage.GetPools()[0].GetDateExpires().AsTime())
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

	// Credit granted the workspace, and another workspace.
	mine := fake.Credit(levyanv1.Credit_builder{Workspace: ws, Granted: 70000, DateExpires: timestamppb.New(until)}.Build())
	fake.Credit(levyanv1.Credit_builder{Workspace: pdid.New(1).String(), Granted: 1000}.Build())

	// Whoever runs it reads the workspace's, and only that.
	admin := w.signIn(w.holder("kim", "admin"))
	for _, as := range []context.Context{w.owner, admin} {
		p, err := pay.GetPayer(as, app.PaymentGetPayerRequest_builder{}.Build())
		x.NoError(err)
		x.Len(p.GetPayer().GetCredits(), 1)
		c := p.GetPayer().GetCredits()[0]
		x.Equal([]any{mine.GetId(), ws, "", int64(70000), int64(70000), "KRW"}, []any{c.GetId(), c.GetWorkspace(), c.GetBillingAccount(), c.GetGranted(), c.GetLeft(), c.GetCurrency()})
		x.Equal(until, c.GetDateExpires().AsTime())
	}
}

// TestWhatLevyanRefusesIsSaid: a refusal of levyan's keeps its code, in this
// deployment's words with levyan's after them, and a levyan that cannot be
// reached is one to try again; a deployment that names none says so before it
// asks anything.
func TestWhatLevyanRefusesIsSaid(t *testing.T) {
	w, fake := paid(t)
	x := w.x
	pay := w.payments()

	// Who pays for it already, asked to: levyan refuses it, in its words.
	w.arrived(fake, tenantId(x, w.tenant), w.memberId(w.ownerHolder()))
	_, err := pay.Offer(w.owner, app.PaymentOfferRequest_builder{Holder: w.ownerHolder().GetId()}.Build())
	x.Equal(codes.FailedPrecondition, status.Code(err))
	x.Equal("결제 서비스가 받지 않았습니다: the account pays for the workspace already", status.Convert(err).Message())

	fake.Down(status.Error(codes.Unavailable, "levyantest: down"))
	_, err = pay.GetPayer(w.owner, app.PaymentGetPayerRequest_builder{}.Build())
	x.Equal(codes.Unavailable, status.Code(err))
	x.Contains(status.Convert(err).Message(), "결제 서비스에 지금 닿지 않습니다")
	fake.Down(nil)

	t.Run("a levyan nobody answers for", func(t *testing.T) {
		x := require.New(t)
		w := newWorldWith(t, func(c *cmd.Config) { c.App.Billing.Levyan = levyantest.Gone(t) })
		_, err := w.payments().GetUsage(w.owner, app.PaymentGetUsageRequest_builder{}.Build())
		x.Equal(codes.Unavailable, status.Code(err))
	})

	t.Run("no levyan", func(t *testing.T) {
		x := require.New(t)
		w := newWorld(t)
		_, err := w.payments().GetPayer(w.owner, app.PaymentGetPayerRequest_builder{}.Build())
		x.True(errors.Is(err, payment.ErrNoBilling) || status.Convert(err).Message() == status.Convert(payment.ErrNoBilling).Message(), "%v", err)
		_, err = w.payments().GetUsage(w.owner, app.PaymentGetUsageRequest_builder{}.Build())
		x.Equal(codes.FailedPrecondition, status.Code(err))
	})
}

// TestWhetherAWorkspacesBillingAccountCanCollectIsSaid (design 7.9, levyan's
// `docs/apps.md` 4): who runs a workspace reads whether the billing account it
// is paid for with now can be collected -- before any charge of it failed --
// as levyan says it of the billing account its payer opened where a payer
// pays.
func TestWhetherAWorkspacesBillingAccountCanCollectIsSaid(t *testing.T) {
	w, fake := paid(t)
	x := w.x
	pay := w.payments()

	// The owner's, opened before levyan is told who made the workspace: what
	// it is paid for with from then.
	ba := fake.OpenBillingAccount(w.memberId(w.ownerHolder()).String(), "회사용").GetId()
	w.arrived(fake, tenantId(x, w.tenant), w.memberId(w.ownerHolder()))
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
	fake.Collect(ba, levyanv1.CollectionState_COLLECTION_STATE_COLLECTABLE)
	x.Equal(app.BillingCollectionState_BILLING_COLLECTION_STATE_COLLECTABLE, state())
	fake.Collect(ba, levyanv1.CollectionState_COLLECTION_STATE_UNSPECIFIED)
	x.Equal(app.BillingCollectionState_BILLING_COLLECTION_STATE_NO_SUBSCRIPTION, state())
}
