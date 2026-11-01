package levyantest

import (
	"context"
	"crypto/rand"
	"net/url"
	"slices"
	"sort"
	"strings"
	"time"

	levyanv1 "github.com/lesomnus/levyan/api/levyan/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/lesomnus/payday/pdid"

	"github.com/lesomnus/loft/server/levyan"
)

// NoRoster is what levyan answers an account's call while it has no roster to
// ask about the account (levyan's `server/relay`): what a deployment shows
// while its levyan is set up so.
const NoRoster = "levyan asks roster whether an account is in good standing before it serves a call made as one, and no roster is configured (auth.roster)"

// ClientToken is the client-side token the fake's checkouts are opened with:
// made up, of the shape Paddle documents.
const ClientToken = "test_000000000000000000000000000"

// Grace is how long after its last payer left a workspace is restricted, and
// ReadOnlyAfter how long after it is read-only, as levyan's policy table's row
// of a workspace with no payer (`dunning.kinds.payerless`) says by default.
const (
	Grace         = 30 * 24 * time.Hour
	ReadOnlyAfter = 60 * 24 * time.Hour
)

// payers is what the fake keeps of who pays (levyan's design 14) and of how
// (12, 15): as much of levyan as a product's pages ask of it, kept under the
// fake's lock.
type payers struct {
	billing    map[string][]*levyanv1.BillingAccount // by roster account
	checkouts  []*checkout
	subs       map[string]*levyanv1.Subscription // by billing account
	offers     []*offer
	workspaces map[string]*paying                // by workspace
	invoices   map[string][]*levyanv1.Invoice    // by billing account
	notes      map[string][]*levyanv1.CreditNote // by invoice
	statements map[string][]*levyanv1.Statement  // by workspace
	collected  map[string]*levyanv1.Collection   // by billing account, as Collect says it
	dunning    []*dunned
	relayed    []Relayed
	noRoster   bool
	opens      int
}

func newPayers() payers {
	return payers{
		billing:    map[string][]*levyanv1.BillingAccount{},
		subs:       map[string]*levyanv1.Subscription{},
		workspaces: map[string]*paying{},
		invoices:   map[string][]*levyanv1.Invoice{},
		notes:      map[string][]*levyanv1.CreditNote{},
		statements: map[string][]*levyanv1.Statement{},
		collected:  map[string]*levyanv1.Collection{},
		opens:      1,
	}
}

// Bank opens a billing account of `account`'s paid by transfer, called
// `name`: in Korea, in won, billed to `business` by its registration -- as
// levyan's operator opens one after a contract (levyan's #9), since no
// account opens one itself. The business it is billed to is then the
// contract's, which the account may not change ([Levyan.UpdateBillingAccount]).
func (v *Levyan) Bank(account, name string, business *levyanv1.Business) *levyanv1.BillingAccount {
	b := levyanv1.BillingAccount_builder{
		Id:       newId(),
		Name:     name,
		Country:  "KR",
		Channel:  levyanv1.Channel_CHANNEL_BANK_KR,
		Currency: "KRW",
		Business: proto.CloneOf(business),
		Created:  timestamppb.Now(),
	}.Build()

	v.mu.Lock()
	defer v.mu.Unlock()
	v.billing[account] = append(v.billing[account], b)
	return proto.CloneOf(b)
}

// Collect has billing account `id` collected as `state` says from now,
// whatever its subscription says: what the merchant of record says of it
// (levyan's 12) -- a charge failing and tried again, a subscription paused
// or ended, a payment waiting on the payer. UNSPECIFIED has it said by its
// subscription again, as a checkout completed does.
func (v *Levyan) Collect(id string, state levyanv1.CollectionState) {
	v.mu.Lock()
	defer v.mu.Unlock()
	if state == levyanv1.CollectionState_COLLECTION_STATE_UNSPECIFIED {
		delete(v.collected, id)
		return
	}
	v.collected[id] = levyanv1.Collection_builder{State: state, Since: timestamppb.Now()}.Build()
}

type checkout struct {
	v       *levyanv1.Checkout
	account string
	asked   int
}

type offer struct {
	v        *levyanv1.Offer
	answered bool
}

// paying is who pays for one workspace: now, from the end of the period, and
// since when nobody has -- and from when it is restricted and read-only for
// that.
type paying struct {
	current, next  *levyanv1.Link
	payerlessSince time.Time
	restrictedFrom time.Time
	readonlyFrom   time.Time
}

// dunned is what is done of an invoice whose charge failed, and the
// workspaces it bills.
type dunned struct {
	v          *levyanv1.Dunning
	workspaces []string
}

// Dun has `d` be what is done of its invoice, whose charge failed, which
// bills `workspaces`: what levyan's dunning says of it (levyan's
// `docs/apps.md`, 6), answered with the billing account, the invoice and who
// pays for each workspace -- the one that restricts soonest first.
func (v *Levyan) Dun(d *levyanv1.Dunning, workspaces ...string) {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.dunning = append(v.dunning, &dunned{v: proto.CloneOf(d), workspaces: slices.Clone(workspaces)})
	sort.SliceStable(v.dunning, func(i, j int) bool {
		return v.dunning[i].v.GetRestrictedFrom().AsTime().Before(v.dunning[j].v.GetRestrictedFrom().AsTime())
	})
}

// Relayed is one call of PayerService that came with the key: the method, and
// the account it was relayed for.
type Relayed struct {
	Method  string
	Account string
}

// Relays is every PayerService call it was asked, in the order they came.
func (v *Levyan) Relays() []Relayed {
	v.mu.Lock()
	defer v.mu.Unlock()
	return slices.Clone(v.relayed)
}

// WithoutRoster has every PayerService call refused as levyan refuses it when
// it has no roster to ask ([NoRoster]); false takes them again.
func (v *Levyan) WithoutRoster(on bool) {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.noRoster = on
}

// Opens has a checkout asked about `n` times before it is completed: the
// payer back before the merchant made the subscription. Unset is once.
func (v *Levyan) Opens(n int) {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.opens = n
}

// Payerless has `workspace` paid for by nobody since now, restricted from
// `from`, and read-only [ReadOnlyAfter] after now: what levyan says once its
// payer left (its policy table's row of a workspace with no payer).
func (v *Levyan) Payerless(workspace string, from time.Time) {
	v.PayerlessUntil(workspace, from, time.Now().Add(ReadOnlyAfter))
}

// PayerlessUntil is [Levyan.Payerless] read-only from `readonly`.
func (v *Levyan) PayerlessUntil(workspace string, from, readonly time.Time) {
	v.mu.Lock()
	defer v.mu.Unlock()
	p := v.paying(workspace)
	if p.current != nil {
		p.current.SetEnd(timestamppb.Now())
		p.current.SetEnded(levyanv1.LinkEnded_LINK_ENDED_LEFT)
	}
	p.current, p.next = nil, nil
	p.payerlessSince = time.Now().UTC().Truncate(time.Microsecond)
	p.restrictedFrom = from.UTC().Truncate(time.Microsecond)
	p.readonlyFrom = readonly.UTC().Truncate(time.Microsecond)
}

// Turn is the period over for `workspace`, as levyan turns it at its end:
// whoever was to pay from then -- having accepted an offer -- pays from now,
// with the billing account they named, and whoever paid until now does no
// longer. With nobody to pay from then, nothing changes.
func (v *Levyan) Turn(workspace string) {
	v.mu.Lock()
	defer v.mu.Unlock()
	p := v.paying(workspace)
	if p.next == nil {
		return
	}
	now := timestamppb.New(time.Now().UTC().Truncate(time.Microsecond))
	l := proto.CloneOf(p.next)
	l.SetStart(now)
	p.current, p.next = l, nil
}

// Bill has the billing account `inv` names billed it, with what `notes` take
// back of it.
func (v *Levyan) Bill(inv *levyanv1.Invoice, notes ...*levyanv1.CreditNote) {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.invoices[inv.GetBillingAccount()] = append(v.invoices[inv.GetBillingAccount()], proto.CloneOf(inv))
	for _, n := range notes {
		v.notes[inv.GetId()] = append(v.notes[inv.GetId()], proto.CloneOf(n))
	}
}

// State has `st` be one of its workspace's statements.
func (v *Levyan) State(st *levyanv1.Statement) {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.statements[st.GetWorkspace()] = append(v.statements[st.GetWorkspace()], proto.CloneOf(st))
}

// BillingAccounts is what `account` pays with.
func (v *Levyan) BillingAccounts(account string) []*levyanv1.BillingAccount {
	v.mu.Lock()
	defer v.mu.Unlock()
	out := []*levyanv1.BillingAccount{}
	for _, b := range v.billing[account] {
		out = append(out, proto.CloneOf(b))
	}
	return out
}

// Payer is who pays for `workspace`, as GetPayer answers.
func (v *Levyan) Payer(workspace string) *levyanv1.Payer {
	v.mu.Lock()
	defer v.mu.Unlock()
	return v.payerOf(workspace)
}

func (v *Levyan) paying(workspace string) *paying {
	p, ok := v.workspaces[workspace]
	if !ok {
		p = &paying{}
		v.workspaces[workspace] = p
	}
	return p
}

// newId is an identifier as levyan writes one: the canonical text of a pdid.
func newId() string { return pdid.New(19).String() }

// period is the period now open: this month, in Korea.
func period(now time.Time) (time.Time, time.Time) {
	seoul, err := time.LoadLocation("Asia/Seoul")
	if err != nil {
		seoul = time.FixedZone("KST", 9*60*60)
	}
	t := now.In(seoul)
	start := time.Date(t.Year(), t.Month(), 1, 0, 0, 0, 0, seoul)
	return start.UTC(), start.AddDate(0, 1, 0).UTC()
}

// madeBy is a workspace made by its creator: who pays for it from when it was
// made, with the first billing account they made, if they made one; said
// again, it changes nothing.
func (v *Levyan) madeBy(t Told) *levyanv1.Payer {
	v.mu.Lock()
	defer v.mu.Unlock()
	p := v.paying(t.Workspace)
	if p.current == nil && p.next == nil && p.payerlessSince.IsZero() {
		at := t.Created
		if at.IsZero() {
			at = time.Now()
		}
		l := levyanv1.Link_builder{
			Id:        newId(),
			Workspace: t.Workspace,
			Account:   t.Account,
			Start:     timestamppb.New(at),
			Began:     levyanv1.LinkBegan_LINK_BEGAN_ARRIVED,
		}.Build()
		if bs := v.billing[t.Account]; len(bs) > 0 {
			l.SetBillingAccount(bs[0].GetId())
		}
		p.current = l
	}
	return v.payerOf(t.Workspace)
}

// goneFrom is an account gone from a workspace: if it paid for it, nobody
// does from now, and the workspace is restricted [Grace] after and read-only
// [ReadOnlyAfter] after; what was offered to it is withdrawn.
func (v *Levyan) goneFrom(t Told) *levyanv1.Payer {
	v.mu.Lock()
	defer v.mu.Unlock()
	p := v.paying(t.Workspace)
	if p.next != nil && p.next.GetAccount() == t.Account {
		p.next = nil
	}
	if p.current != nil && p.current.GetAccount() == t.Account {
		p.current = nil
		if p.next == nil {
			now := time.Now().UTC().Truncate(time.Microsecond)
			p.payerlessSince, p.restrictedFrom, p.readonlyFrom = now, now.Add(Grace), now.Add(ReadOnlyAfter)
		}
	}
	for _, o := range v.offers {
		if o.v.GetWorkspace() == t.Workspace && o.v.GetAccount() == t.Account {
			o.answered = true
		}
	}
	return v.payerOf(t.Workspace)
}

// payerOf is who pays for a workspace, as levyan says it, and the credit
// granted the workspace itself that is left.
func (v *Levyan) payerOf(workspace string) *levyanv1.Payer {
	out := levyanv1.Payer_builder{Workspace: workspace, Credits: v.creditOf(workspace, time.Now())}.Build()
	for _, d := range v.dunning {
		if slices.Contains(d.workspaces, workspace) {
			out.SetDunning(append(out.GetDunning(), proto.CloneOf(d.v)))
		}
	}
	p, ok := v.workspaces[workspace]
	if !ok {
		return out
	}
	if p.current != nil {
		out.SetCurrent(proto.CloneOf(p.current))
		// Of the billing account it is paid for with, on the merchant of
		// record's channel: none is said of another's, as levyan says none.
		if c := v.collection(p.current.GetBillingAccount()); c.GetState() != levyanv1.CollectionState_COLLECTION_STATE_UNSPECIFIED {
			out.SetCollection(c)
		}
	}
	if p.next != nil {
		out.SetNext(proto.CloneOf(p.next))
	}
	for _, o := range v.offers {
		if o.v.GetWorkspace() == workspace && !o.answered {
			out.SetOffer(proto.CloneOf(o.v))
		}
	}
	if !p.payerlessSince.IsZero() {
		out.SetPayerlessSince(timestamppb.New(p.payerlessSince))
		out.SetRestrictedFrom(timestamppb.New(p.restrictedFrom))
		out.SetReadonlyFrom(timestamppb.New(p.readonlyFrom))
	}
	return out
}

// own is billing account `id` of `account`'s, or NotFound.
func (v *Levyan) own(account, id string) (*levyanv1.BillingAccount, error) {
	for _, b := range v.billing[account] {
		if b.GetId() == id {
			return b, nil
		}
	}
	return nil, status.Errorf(codes.NotFound, "levyantest: no billing account %q of the account's", id)
}

// collection is whether what billing account `id` is billed can be collected:
// nil for none, and nothing said -- UNSPECIFIED -- of one on a channel that is
// no merchant of record's, as levyan says it.
func (v *Levyan) collection(id string) *levyanv1.Collection {
	var b *levyanv1.BillingAccount
	for _, bs := range v.billing {
		for _, c := range bs {
			if c.GetId() == id {
				b = c
			}
		}
	}
	switch {
	case b == nil:
		return nil
	case b.GetChannel() != levyanv1.Channel_CHANNEL_MOR:
		return levyanv1.Collection_builder{}.Build()
	}
	if c, ok := v.collected[id]; ok {
		return proto.CloneOf(c)
	}
	if s, ok := v.subs[id]; ok && s.GetStatus() == levyanv1.SubscriptionStatus_SUBSCRIPTION_STATUS_ACTIVE {
		return levyanv1.Collection_builder{State: levyanv1.CollectionState_COLLECTION_STATE_COLLECTABLE, Since: s.GetPeriodStart()}.Build()
	}
	return levyanv1.Collection_builder{State: levyanv1.CollectionState_COLLECTION_STATE_NO_SUBSCRIPTION, Since: b.GetCreated()}.Build()
}

// relayedFor is the account a PayerService call was relayed for: the key, and
// the account beside it, held to what levyan holds them to.
func (v *Levyan) relayedFor(ctx context.Context) (string, error) {
	if err := keyed(ctx); err != nil {
		return "", err
	}
	md, _ := metadata.FromIncomingContext(ctx)
	as := md.Get(levyan.AccountHeader)
	if len(as) != 1 {
		return "", status.Error(codes.PermissionDenied, "levyantest: a product's own key is refused every method of a payer's")
	}
	if _, err := pdid.Parse(as[0]); err != nil {
		return "", status.Errorf(codes.InvalidArgument, "levyantest: %q is not an account's identifier", as[0])
	}
	m, _ := grpc.Method(ctx)

	v.mu.Lock()
	defer v.mu.Unlock()
	v.relayed = append(v.relayed, Relayed{Method: m, Account: as[0]})
	if v.noRoster {
		return "", status.Error(codes.FailedPrecondition, NoRoster)
	}
	return as[0], nil
}

func (v *Levyan) ListBillingAccounts(ctx context.Context, req *levyanv1.ListBillingAccountsRequest) (*levyanv1.ListBillingAccountsResponse, error) {
	a, err := v.relayedFor(ctx)
	if err != nil {
		return nil, err
	}
	return levyanv1.ListBillingAccountsResponse_builder{BillingAccounts: v.BillingAccounts(a)}.Build(), nil
}

// CreateBillingAccount bills one in a country as levyan does by default: the
// merchant of record, in won in KR and in US dollars anywhere else.
func (v *Levyan) CreateBillingAccount(ctx context.Context, req *levyanv1.CreateBillingAccountRequest) (*levyanv1.CreateBillingAccountResponse, error) {
	a, err := v.relayedFor(ctx)
	if err != nil {
		return nil, err
	}
	c := req.GetCountry()
	if len(c) != 2 || strings.ToUpper(c) != c {
		return nil, status.Errorf(codes.InvalidArgument, "country: %q is no country as ISO 3166-1 alpha-2 writes one in capitals", c)
	}
	currency := "USD"
	if c == "KR" {
		currency = "KRW"
	}
	b := levyanv1.BillingAccount_builder{
		Id:        newId(),
		Name:      req.GetName(),
		Country:   c,
		Channel:   levyanv1.Channel_CHANNEL_MOR,
		Currency:  currency,
		Recipient: req.GetRecipient(),
		Business:  req.GetBusiness(),
		Created:   timestamppb.Now(),
	}.Build()

	v.mu.Lock()
	defer v.mu.Unlock()
	v.billing[a] = append(v.billing[a], b)
	return levyanv1.CreateBillingAccountResponse_builder{BillingAccount: proto.CloneOf(b)}.Build(), nil
}

func (v *Levyan) UpdateBillingAccount(ctx context.Context, req *levyanv1.UpdateBillingAccountRequest) (*levyanv1.UpdateBillingAccountResponse, error) {
	a, err := v.relayedFor(ctx)
	if err != nil {
		return nil, err
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	b, err := v.own(a, req.GetId())
	if err != nil {
		return nil, err
	}
	if b.GetChannel() == levyanv1.Channel_CHANNEL_BANK_KR && (req.HasBusiness() || req.GetClearBusiness()) {
		// levyan's words (`server/payers`): the account's call is refused
		// whole, and changes nothing.
		return nil, status.Error(codes.FailedPrecondition, "the business a billing account paid by transfer is billed to is its contract's, and an operator's to change")
	}
	if req.HasName() {
		b.SetName(req.GetName())
	}
	if req.HasRecipient() {
		b.SetRecipient(req.GetRecipient())
	}
	if req.HasBusiness() {
		b.SetBusiness(req.GetBusiness())
	}
	if req.GetClearBusiness() {
		b.ClearBusiness()
	}
	return levyanv1.UpdateBillingAccountResponse_builder{BillingAccount: proto.CloneOf(b)}.Build(), nil
}

func (v *Levyan) GetBillingAccount(ctx context.Context, req *levyanv1.GetBillingAccountRequest) (*levyanv1.GetBillingAccountResponse, error) {
	a, err := v.relayedFor(ctx)
	if err != nil {
		return nil, err
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	b, err := v.own(a, req.GetId())
	if err != nil {
		return nil, err
	}
	out := levyanv1.GetBillingAccountResponse_builder{
		BillingAccount: proto.CloneOf(b),
		Collection:     v.collection(b.GetId()),
		Credits:        v.creditFor(b.GetId(), time.Now()),
		Balance:        v.balances[b.GetId()],
	}.Build()
	if s, ok := v.subs[b.GetId()]; ok {
		out.SetSubscription(proto.CloneOf(s))
	}
	for _, d := range v.dunning {
		if d.v.GetBillingAccount() == b.GetId() {
			out.SetDunning(append(out.GetDunning(), proto.CloneOf(d.v)))
		}
	}
	return out, nil
}

// StartCheckout opens a checkout as levyan's Paddle adapter does: of a
// transaction made up, in the sandbox, opened with [ClientToken] -- and the
// one open already, when there is one.
func (v *Levyan) StartCheckout(ctx context.Context, req *levyanv1.StartCheckoutRequest) (*levyanv1.StartCheckoutResponse, error) {
	a, err := v.relayedFor(ctx)
	if err != nil {
		return nil, err
	}
	if u, err := url.Parse(req.GetSuccessUrl()); err != nil || !u.IsAbs() {
		return nil, status.Error(codes.InvalidArgument, "success_url: where the checkout sends the payer back, an absolute URL")
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	b, err := v.own(a, req.GetBillingAccount())
	if err != nil {
		return nil, err
	}
	if c := b.GetChannel(); c != levyanv1.Channel_CHANNEL_MOR {
		return nil, status.Errorf(codes.FailedPrecondition, "the billing account is billed through %s, which takes no checkout of the merchant of record's", c)
	}
	if s, ok := v.subs[b.GetId()]; ok && s.GetStatus() == levyanv1.SubscriptionStatus_SUBSCRIPTION_STATUS_ACTIVE {
		return nil, status.Error(codes.FailedPrecondition, "the billing account is subscribed already (active): one subscription is collected through at a time")
	}
	for _, c := range v.checkouts {
		if c.v.GetBillingAccount() == b.GetId() && c.v.GetState() == levyanv1.CheckoutState_CHECKOUT_STATE_OPEN {
			return levyanv1.StartCheckoutResponse_builder{Checkout: proto.CloneOf(c.v)}.Build(), nil
		}
	}

	txn := "txn_" + word(26)
	c := &checkout{account: a, v: levyanv1.Checkout_builder{
		Id:             newId(),
		BillingAccount: b.GetId(),
		State:          levyanv1.CheckoutState_CHECKOUT_STATE_OPEN,
		Started:        timestamppb.Now(),
		Window: levyanv1.CheckoutWindow_builder{
			Merchant:    "paddle",
			Environment: "sandbox",
			Transaction: txn,
			ClientToken: ClientToken,
			Script:      "https://cdn.paddle.com/paddle/v2/paddle.js",
			Url:         "https://loft.example/billing/checkout?_ptxn=" + txn,
			Country:     b.GetCountry(),
		}.Build(),
	}.Build()}
	v.checkouts = append(v.checkouts, c)
	return levyanv1.StartCheckoutResponse_builder{Checkout: proto.CloneOf(c.v)}.Build(), nil
}

// CompleteCheckout completes a checkout once it was asked about [Levyan.Opens]
// times -- the merchant making the subscription meanwhile -- with a card's
// subscription, paid up.
func (v *Levyan) CompleteCheckout(ctx context.Context, req *levyanv1.CompleteCheckoutRequest) (*levyanv1.CompleteCheckoutResponse, error) {
	a, err := v.relayedFor(ctx)
	if err != nil {
		return nil, err
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	var c *checkout
	for _, x := range v.checkouts {
		if x.v.GetId() == req.GetCheckout() && x.account == a {
			c = x
		}
	}
	if c == nil {
		return nil, status.Errorf(codes.NotFound, "levyantest: no checkout %q of the account's", req.GetCheckout())
	}
	ba := c.v.GetBillingAccount()
	if c.v.GetState() == levyanv1.CheckoutState_CHECKOUT_STATE_OPEN {
		c.asked++
		if c.asked > v.opens {
			now := time.Now()
			start, end := period(now)
			c.v.SetState(levyanv1.CheckoutState_CHECKOUT_STATE_COMPLETED)
			c.v.SetDone(timestamppb.New(now))
			// Collected through the subscription it made from now, whatever
			// the merchant said of the one before.
			delete(v.collected, ba)
			b, _ := v.own(a, ba)
			v.subs[ba] = levyanv1.Subscription_builder{
				Id:          newId(),
				Merchant:    "paddle",
				Status:      levyanv1.SubscriptionStatus_SUBSCRIPTION_STATUS_ACTIVE,
				Currency:    b.GetCurrency(),
				PeriodStart: timestamppb.New(start),
				PeriodEnd:   timestamppb.New(end),
				NextBilled:  timestamppb.New(end),
				Method:      levyanv1.PaymentMethod_builder{Type: "card", Brand: "visa", Last4: "4242"}.Build(),
				Updated:     timestamppb.New(now),
				Checked:     timestamppb.New(now),
			}.Build()
		}
	}
	out := levyanv1.CompleteCheckoutResponse_builder{Checkout: proto.CloneOf(c.v), Collection: v.collection(ba)}.Build()
	if c.v.GetState() == levyanv1.CheckoutState_CHECKOUT_STATE_COMPLETED {
		out.SetSubscription(proto.CloneOf(v.subs[ba]))
	}
	return out, nil
}

func (v *Levyan) ListOffers(ctx context.Context, req *levyanv1.ListOffersRequest) (*levyanv1.ListOffersResponse, error) {
	a, err := v.relayedFor(ctx)
	if err != nil {
		return nil, err
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	out := levyanv1.ListOffersResponse_builder{}.Build()
	for _, o := range v.offers {
		if o.v.GetAccount() == a && !o.answered {
			out.SetOffers(append(out.GetOffers(), proto.CloneOf(o.v)))
		}
	}
	return out, nil
}

// Accept is the account paying for the offer's workspace from the end of the
// period, with the billing account it names or none yet: the workspace is
// restricted no longer.
func (v *Levyan) Accept(ctx context.Context, req *levyanv1.AcceptRequest) (*levyanv1.AcceptResponse, error) {
	a, err := v.relayedFor(ctx)
	if err != nil {
		return nil, err
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	o, err := v.offerOf(a, req.GetOffer())
	if err != nil {
		return nil, err
	}
	if ba := req.GetBillingAccount(); ba != "" {
		if _, err := v.own(a, ba); err != nil {
			return nil, err
		}
	}
	_, end := period(time.Now())
	l := levyanv1.Link_builder{
		Id:             newId(),
		Workspace:      o.v.GetWorkspace(),
		Account:        a,
		BillingAccount: req.GetBillingAccount(),
		Start:          timestamppb.New(end),
		Began:          levyanv1.LinkBegan_LINK_BEGAN_ACCEPTED,
	}.Build()
	o.answered = true
	p := v.paying(o.v.GetWorkspace())
	p.next = l
	p.payerlessSince, p.restrictedFrom, p.readonlyFrom = time.Time{}, time.Time{}, time.Time{}
	return levyanv1.AcceptResponse_builder{Link: proto.CloneOf(l)}.Build(), nil
}

func (v *Levyan) Decline(ctx context.Context, req *levyanv1.DeclineRequest) (*levyanv1.DeclineResponse, error) {
	a, err := v.relayedFor(ctx)
	if err != nil {
		return nil, err
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	o, err := v.offerOf(a, req.GetOffer())
	if err != nil {
		return nil, err
	}
	o.answered = true
	return &levyanv1.DeclineResponse{}, nil
}

func (v *Levyan) offerOf(account, id string) (*offer, error) {
	for _, o := range v.offers {
		if o.v.GetId() == id && o.v.GetAccount() == account {
			if o.answered {
				return nil, status.Error(codes.FailedPrecondition, "the offer was answered already")
			}
			return o, nil
		}
	}
	return nil, status.Errorf(codes.NotFound, "levyantest: no offer %q to the account", id)
}

// ListLinks is what the account pays for now, or will from the end of the
// period.
func (v *Levyan) ListLinks(ctx context.Context, req *levyanv1.ListLinksRequest) (*levyanv1.ListLinksResponse, error) {
	a, err := v.relayedFor(ctx)
	if err != nil {
		return nil, err
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	out := levyanv1.ListLinksResponse_builder{}.Build()
	keys := make([]string, 0, len(v.workspaces))
	for k := range v.workspaces {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		p := v.workspaces[k]
		for _, l := range []*levyanv1.Link{p.current, p.next} {
			if l != nil && l.GetAccount() == a {
				out.SetLinks(append(out.GetLinks(), proto.CloneOf(l)))
			}
		}
	}
	return out, nil
}

func (v *Levyan) Use(ctx context.Context, req *levyanv1.UseRequest) (*levyanv1.UseResponse, error) {
	a, err := v.relayedFor(ctx)
	if err != nil {
		return nil, err
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	p := v.paying(req.GetWorkspace())
	pays := (p.current != nil && p.current.GetAccount() == a) || (p.next != nil && p.next.GetAccount() == a)
	if !pays {
		return nil, status.Errorf(codes.FailedPrecondition, "the account does not pay for workspace %s from the end of the period", req.GetWorkspace())
	}
	if _, err := v.own(a, req.GetBillingAccount()); err != nil {
		return nil, err
	}
	_, end := period(time.Now())
	p.next = levyanv1.Link_builder{
		Id:             newId(),
		Workspace:      req.GetWorkspace(),
		Account:        a,
		BillingAccount: req.GetBillingAccount(),
		Start:          timestamppb.New(end),
		Began:          levyanv1.LinkBegan_LINK_BEGAN_USED,
	}.Build()
	return levyanv1.UseResponse_builder{Link: proto.CloneOf(p.next)}.Build(), nil
}

// Stop ends what the account pays for a workspace at the end of the period.
func (v *Levyan) Stop(ctx context.Context, req *levyanv1.StopRequest) (*levyanv1.StopResponse, error) {
	a, err := v.relayedFor(ctx)
	if err != nil {
		return nil, err
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	p := v.paying(req.GetWorkspace())
	if p.current == nil || p.current.GetAccount() != a {
		return nil, status.Errorf(codes.FailedPrecondition, "the account does not pay for workspace %s", req.GetWorkspace())
	}
	_, end := period(time.Now())
	p.current.SetEnd(timestamppb.New(end))
	p.current.SetEnded(levyanv1.LinkEnded_LINK_ENDED_STOPPED)
	p.next = nil
	return levyanv1.StopResponse_builder{Link: proto.CloneOf(p.current)}.Build(), nil
}

// ListInvoices is the invoices [Levyan.Bill] gave the account's billing
// accounts, the latest first.
func (v *Levyan) ListInvoices(ctx context.Context, req *levyanv1.ListInvoicesRequest) (*levyanv1.ListInvoicesResponse, error) {
	a, err := v.relayedFor(ctx)
	if err != nil {
		return nil, err
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	vs := []*levyanv1.Invoice{}
	for _, b := range v.billing[a] {
		if f := req.GetBillingAccount(); f != "" && f != b.GetId() {
			continue
		}
		for _, inv := range v.invoices[b.GetId()] {
			// Its lines, and which credit paid what of it, are
			// GetInvoice's.
			c := proto.CloneOf(inv)
			c.SetLines(nil)
			c.SetCredits(nil)
			vs = append(vs, c)
		}
	}
	sort.SliceStable(vs, func(i, j int) bool { return vs[i].GetStart().AsTime().After(vs[j].GetStart().AsTime()) })
	return levyanv1.ListInvoicesResponse_builder{Invoices: vs}.Build(), nil
}

func (v *Levyan) GetInvoice(ctx context.Context, req *levyanv1.GetInvoiceRequest) (*levyanv1.GetInvoiceResponse, error) {
	a, err := v.relayedFor(ctx)
	if err != nil {
		return nil, err
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	for _, b := range v.billing[a] {
		for _, inv := range v.invoices[b.GetId()] {
			if inv.GetId() == req.GetId() {
				out := levyanv1.GetInvoiceResponse_builder{Invoice: proto.CloneOf(inv)}.Build()
				for _, n := range v.notes[inv.GetId()] {
					out.SetCreditNotes(append(out.GetCreditNotes(), proto.CloneOf(n)))
				}
				for _, d := range v.dunning {
					if d.v.GetInvoice() == inv.GetId() {
						out.SetDunning(proto.CloneOf(d.v))
					}
				}
				return out, nil
			}
		}
	}
	return nil, status.Errorf(codes.NotFound, "levyantest: no invoice %q of the account's", req.GetId())
}

func (v *Levyan) ListCreditNotes(ctx context.Context, req *levyanv1.ListCreditNotesRequest) (*levyanv1.ListCreditNotesResponse, error) {
	a, err := v.relayedFor(ctx)
	if err != nil {
		return nil, err
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	out := levyanv1.ListCreditNotesResponse_builder{}.Build()
	for _, b := range v.billing[a] {
		if f := req.GetBillingAccount(); f != "" && f != b.GetId() {
			continue
		}
		for _, inv := range v.invoices[b.GetId()] {
			if f := req.GetInvoice(); f != "" && f != inv.GetId() {
				continue
			}
			for _, n := range v.notes[inv.GetId()] {
				out.SetCreditNotes(append(out.GetCreditNotes(), proto.CloneOf(n)))
			}
		}
	}
	return out, nil
}

// Offer asks an account to pay for a workspace, withdrawing what was offered
// before.
func (v *Levyan) Offer(ctx context.Context, req *levyanv1.OfferRequest) (*levyanv1.OfferResponse, error) {
	if err := keyed(ctx); err != nil {
		return nil, err
	}
	if _, err := pdid.Parse(req.GetAccount()); err != nil {
		return nil, status.Error(codes.InvalidArgument, "account: the account asked to pay, at roster")
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.down != nil {
		return nil, v.down
	}
	p := v.paying(req.GetWorkspace())
	for _, l := range []*levyanv1.Link{p.current, p.next} {
		if l != nil && l.GetAccount() == req.GetAccount() && !l.HasEnd() {
			return nil, status.Error(codes.FailedPrecondition, "the account pays for the workspace already")
		}
	}
	for _, o := range v.offers {
		if o.v.GetWorkspace() == req.GetWorkspace() {
			o.answered = true
		}
	}
	o := &offer{v: levyanv1.Offer_builder{
		Id:        newId(),
		Workspace: req.GetWorkspace(),
		Account:   req.GetAccount(),
		Offered:   timestamppb.Now(),
	}.Build()}
	v.offers = append(v.offers, o)
	return levyanv1.OfferResponse_builder{Offer: proto.CloneOf(o.v)}.Build(), nil
}

func (v *Levyan) GetPayer(ctx context.Context, req *levyanv1.GetPayerRequest) (*levyanv1.GetPayerResponse, error) {
	if err := keyed(ctx); err != nil {
		return nil, err
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.down != nil {
		return nil, v.down
	}
	return levyanv1.GetPayerResponse_builder{Payer: v.payerOf(req.GetWorkspace())}.Build(), nil
}

// Prices are what the fake prices each meter at, a unit for a month, in won.
var Prices = map[string]int64{
	"loft.assets.active":          100,
	"loft.attachments.bytes":      0,
	"loft.members":                1000,
	"loft.history.retention_days": 0,
}

// GetUsage is what a workspace's latest readings come to, as a whole month at
// [Prices], on the merchant of record in won -- with what its allowances give
// and its pools take, as the fake rates a period ([Levyan.rated]) -- and the
// allowances in force in the month and the pools it takes from, each with
// what the periods closed left of it.
func (v *Levyan) GetUsage(ctx context.Context, req *levyanv1.GetUsageRequest) (*levyanv1.GetUsageResponse, error) {
	if err := keyed(ctx); err != nil {
		return nil, err
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.down != nil {
		return nil, v.down
	}
	now := time.Now()
	start, end := period(now)
	lines, total, _ := v.rated(req.GetWorkspace(), start, end)
	u := levyanv1.Usage_builder{
		Workspace: req.GetWorkspace(),
		Period:    levyanv1.Period_builder{Start: timestamppb.New(start), End: timestamppb.New(end), Zone: "Asia/Seoul"}.Build(),
		AsOf:      timestamppb.New(now),
		Channel:   levyanv1.Channel_CHANNEL_MOR,
		Currency:  "KRW",
		Lines:     lines,
		Total:     total,
	}.Build()
	for _, a := range v.allowancesIn(req.GetWorkspace(), start, end) {
		u.SetAllowances(append(u.GetAllowances(), proto.CloneOf(a)))
	}
	for _, p := range v.poolsIn(req.GetWorkspace(), start, end) {
		u.SetPools(append(u.GetPools(), proto.CloneOf(p)))
	}
	return levyanv1.GetUsageResponse_builder{Usage: u}.Build(), nil
}

func (v *Levyan) ListStatements(ctx context.Context, req *levyanv1.ListStatementsRequest) (*levyanv1.ListStatementsResponse, error) {
	if err := keyed(ctx); err != nil {
		return nil, err
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	vs := []*levyanv1.Statement{}
	for _, st := range v.statements[req.GetWorkspace()] {
		vs = append(vs, proto.CloneOf(st))
	}
	sort.SliceStable(vs, func(i, j int) bool {
		return vs[i].GetPeriod().GetStart().AsTime().After(vs[j].GetPeriod().GetStart().AsTime())
	})
	return levyanv1.ListStatementsResponse_builder{Statements: vs}.Build(), nil
}

// word is n lower-case letters and digits, made up.
func word(n int) string {
	const letters = "abcdefghijklmnopqrstuvwxyz0123456789"
	b := make([]byte, n)
	rand.Read(b)
	for i := range b {
		b[i] = letters[int(b[i])%len(letters)]
	}
	return string(b)
}
