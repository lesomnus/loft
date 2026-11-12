package levyantest

import (
	"context"
	"slices"
	"sort"
	"time"

	levyanv1 "github.com/lesomnus/levyan/api/levyan/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/lesomnus/payday/pdid"
)

// Grace is how long after its last payer left a workspace is restricted, and
// ReadOnlyAfter how long after it is read-only, as levyan's policy table's row
// of a workspace with no payer (`dunning.kinds.payerless`) says by default.
const (
	Grace         = 30 * 24 * time.Hour
	ReadOnlyAfter = 60 * 24 * time.Hour
)

// payers is what the fake keeps of who pays (levyan's design 14) and of how
// (12, 15): as much of levyan as a product's pages ask of it, kept under the
// fake's lock. What a payer does with what it pays with -- its billing
// accounts, an offer answered -- is done where a payer pays and not by the
// product, so a test says it with [Levyan.OpenBillingAccount] and
// [Levyan.Accept].
type payers struct {
	billing    map[string][]*levyanv1.BillingAccount // by roster account
	offers     []*offer
	workspaces map[string]*paying               // by workspace
	statements map[string][]*levyanv1.Statement // by workspace
	collected  map[string]*levyanv1.Collection  // by billing account, as Collect says it
	dunning    []*dunned
}

func newPayers() payers {
	return payers{
		billing:    map[string][]*levyanv1.BillingAccount{},
		workspaces: map[string]*paying{},
		statements: map[string][]*levyanv1.Statement{},
		collected:  map[string]*levyanv1.Collection{},
	}
}

// OpenBillingAccount is a billing account of `account`'s, called `name`, on
// the merchant of record in Korea, in won -- as its payer makes one where a
// payer pays (levyan's `PayerService.CreateBillingAccount`), with nothing to
// collect through yet: a workspace that account makes is paid for with the
// first one it opened.
func (v *Levyan) OpenBillingAccount(account, name string) *levyanv1.BillingAccount {
	b := levyanv1.BillingAccount_builder{
		Id:          newId(),
		Name:        name,
		Country:     "KR",
		Channel:     levyanv1.Channel_CHANNEL_MOR,
		Currency:    "KRW",
		DateCreated: timestamppb.Now(),
	}.Build()

	v.mu.Lock()
	defer v.mu.Unlock()
	v.billing[account] = append(v.billing[account], b)
	return proto.CloneOf(b)
}

// Collect has billing account `id` collected as `state` says from now: what
// the merchant of record says of it (levyan's 12) -- collectable, a charge
// failing and tried again, a subscription paused or ended, a payment waiting
// on the payer. UNSPECIFIED has it said as one with nothing to collect
// through again.
func (v *Levyan) Collect(id string, state levyanv1.CollectionState) {
	v.mu.Lock()
	defer v.mu.Unlock()
	if state == levyanv1.CollectionState_COLLECTION_STATE_UNSPECIFIED {
		delete(v.collected, id)
		return
	}
	v.collected[id] = levyanv1.Collection_builder{State: state, DateSince: timestamppb.Now()}.Build()
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
// `docs/apps.md`, 6), answered with who pays for each workspace -- the one
// that restricts soonest first.
func (v *Levyan) Dun(d *levyanv1.Dunning, workspaces ...string) {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.dunning = append(v.dunning, &dunned{v: proto.CloneOf(d), workspaces: slices.Clone(workspaces)})
	sort.SliceStable(v.dunning, func(i, j int) bool {
		return v.dunning[i].v.GetDateRestricted().AsTime().Before(v.dunning[j].v.GetDateRestricted().AsTime())
	})
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
		p.current.SetDateEnd(timestamppb.Now())
		p.current.SetEnded(levyanv1.LinkEnded_LINK_ENDED_LEFT)
	}
	p.current, p.next = nil, nil
	p.payerlessSince = time.Now().UTC().Truncate(time.Microsecond)
	p.restrictedFrom = from.UTC().Truncate(time.Microsecond)
	p.readonlyFrom = readonly.UTC().Truncate(time.Microsecond)
}

// Accept is what is offered for `workspace` accepted by the account it is
// offered to, where a payer pays (levyan's `PayerService.Accept`): it pays
// for the workspace from the end of the period, with the first billing
// account it opened, if it opened one, and the workspace is restricted no
// longer. Nil when nothing is offered.
func (v *Levyan) Accept(workspace string) *levyanv1.Link {
	v.mu.Lock()
	defer v.mu.Unlock()
	var o *offer
	for _, x := range v.offers {
		if x.v.GetWorkspace() == workspace && !x.answered {
			o = x
		}
	}
	if o == nil {
		return nil
	}
	_, end := period(time.Now())
	l := levyanv1.Link_builder{
		Id:        newId(),
		Workspace: workspace,
		Account:   o.v.GetAccount(),
		DateStart: timestamppb.New(end),
		Began:     levyanv1.LinkBegan_LINK_BEGAN_ACCEPTED,
	}.Build()
	if bs := v.billing[o.v.GetAccount()]; len(bs) > 0 {
		l.SetBillingAccount(bs[0].GetId())
	}
	o.answered = true
	p := v.paying(workspace)
	p.next = l
	p.payerlessSince, p.restrictedFrom, p.readonlyFrom = time.Time{}, time.Time{}, time.Time{}
	return proto.CloneOf(l)
}

// State has `st` be one of its workspace's statements.
func (v *Levyan) State(st *levyanv1.Statement) {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.statements[st.GetWorkspace()] = append(v.statements[st.GetWorkspace()], proto.CloneOf(st))
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
			DateStart: timestamppb.New(at),
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
		out.SetDatePayerless(timestamppb.New(p.payerlessSince))
		out.SetDateRestricted(timestamppb.New(p.restrictedFrom))
		out.SetDateReadonly(timestamppb.New(p.readonlyFrom))
	}
	return out
}

// collection is whether what billing account `id` is billed can be collected:
// nil for none, nothing said -- UNSPECIFIED -- of one on a channel that is no
// merchant of record's, as levyan says it, and no subscription to collect
// through for one a test said nothing of ([Levyan.Collect]).
func (v *Levyan) collection(id string) *levyanv1.Collection {
	b := v.billingAccount(id)
	switch {
	case b == nil:
		return nil
	case b.GetChannel() != levyanv1.Channel_CHANNEL_MOR:
		return levyanv1.Collection_builder{}.Build()
	}
	if c, ok := v.collected[id]; ok {
		return proto.CloneOf(c)
	}
	return levyanv1.Collection_builder{State: levyanv1.CollectionState_COLLECTION_STATE_NO_SUBSCRIPTION, DateSince: b.GetDateCreated()}.Build()
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
		if l != nil && l.GetAccount() == req.GetAccount() && !l.HasDateEnd() {
			return nil, status.Error(codes.FailedPrecondition, "the account pays for the workspace already")
		}
	}
	for _, o := range v.offers {
		if o.v.GetWorkspace() == req.GetWorkspace() {
			o.answered = true
		}
	}
	o := &offer{v: levyanv1.Offer_builder{
		Id:          newId(),
		Workspace:   req.GetWorkspace(),
		Account:     req.GetAccount(),
		DateOffered: timestamppb.Now(),
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
		Period:    levyanv1.Period_builder{DateStart: timestamppb.New(start), DateEnd: timestamppb.New(end), Zone: "Asia/Seoul"}.Build(),
		DateAsOf:  timestamppb.New(now),
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
		return vs[i].GetPeriod().GetDateStart().AsTime().After(vs[j].GetPeriod().GetDateStart().AsTime())
	})
	return levyanv1.ListStatementsResponse_builder{Statements: vs}.Build(), nil
}

// GetStatement is one of the product's statements, by its identifier.
func (v *Levyan) GetStatement(ctx context.Context, req *levyanv1.GetStatementRequest) (*levyanv1.GetStatementResponse, error) {
	if err := keyed(ctx); err != nil {
		return nil, err
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	for _, sts := range v.statements {
		for _, st := range sts {
			if st.GetId() == req.GetId() {
				return levyanv1.GetStatementResponse_builder{Statement: proto.CloneOf(st)}.Build(), nil
			}
		}
	}
	return nil, status.Errorf(codes.NotFound, "levyantest: no statement is %q", req.GetId())
}
