package levyantest

import (
	"math/big"
	"slices"
	"sort"
	"strings"
	"time"

	levyanv1 "github.com/lesomnus/levyan/api/levyan/v1"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// benefits is what levyan's operator gave beyond what the prices include
// (levyan#43, levyan's design 19), as the fake keeps it under its lock:
// allowances, more of a meter each period; pools, bundles of a meter that
// what a period is billable for past what is included is taken out of
// first; and credit, money an invoice is paid with before its channel is
// asked for anything -- lots of a billing account's or of one workspace's,
// and a billing account's balance. And the readings a test hands over
// itself ([Levyan.Handed]).
type benefits struct {
	allowances map[string][]*levyanv1.Allowance // by workspace, in the order given
	pools      map[string][]*levyanv1.Pool      // by workspace: `left` is what the periods closed left
	credits    []*levyanv1.Credit               // every lot, in the order given
	balances   map[string]int64                 // by billing account
	handed     []*levyanv1.Reading
}

func newBenefits() benefits {
	return benefits{
		allowances: map[string][]*levyanv1.Allowance{},
		pools:      map[string][]*levyanv1.Pool{},
		balances:   map[string]int64{},
	}
}

// Allow gives `workspace` the allowance `a` -- `a.quantity` more of
// `a.meter` each period -- as levyan's operator does (`levyan-admin allowance
// add`): from now and until it is ended, unless `a` says from when and until
// when. It is answered by GetUsage while it is in force at any moment of the
// period, and each line of its meter is given its share of the period, as
// levyan gives one. It answers the allowance with its identifier.
func (v *Levyan) Allow(workspace string, a *levyanv1.Allowance) *levyanv1.Allowance {
	a = proto.CloneOf(a)
	if a.GetId() == "" {
		a.SetId(newId())
	}
	if !a.HasFrom() {
		a.SetFrom(timestamppb.Now())
	}

	v.mu.Lock()
	defer v.mu.Unlock()
	v.allowances[workspace] = append(v.allowances[workspace], a)
	return proto.CloneOf(a)
}

// Pool gives `workspace` the pool `p` -- `p.granted` of `p.meter`, in its
// unit for a period -- as levyan's operator does (`levyan-admin pool
// grant`): from now, expiring a year after, all of it left, unless `p` says
// otherwise. Every period it is in force for at any moment takes from it, the
// soonest to expire first ([Levyan.ClosePeriod], GetUsage). It answers the
// pool with its identifier.
func (v *Levyan) Pool(workspace string, p *levyanv1.Pool) *levyanv1.Pool {
	p = proto.CloneOf(p)
	if p.GetId() == "" {
		p.SetId(newId())
	}
	if !p.HasFrom() {
		p.SetFrom(timestamppb.Now())
	}
	if !p.HasExpires() {
		p.SetExpires(timestamppb.New(p.GetFrom().AsTime().AddDate(1, 0, 0)))
	}
	if p.GetLeft() == "" {
		p.SetLeft(decimal(new(big.Rat).SetInt64(p.GetGranted())))
	}

	v.mu.Lock()
	defer v.mu.Unlock()
	v.pools[workspace] = append(v.pools[workspace], p)
	return proto.CloneOf(p)
}

// Credit grants the lot `c` -- money of a billing account's
// (`c.billing_account`) or of one workspace's (`c.workspace`) -- as levyan's
// operator does (`levyan-admin credit grant`): now, expiring a year after,
// all of it left; a billing account's in its currency, and a workspace's in
// that of the billing account that pays for it now, or in won, unless `c`
// says otherwise. It answers the lot with its identifier.
func (v *Levyan) Credit(c *levyanv1.Credit) *levyanv1.Credit {
	c = proto.CloneOf(c)
	if c.GetId() == "" {
		c.SetId(newId())
	}
	if !c.HasCreated() {
		c.SetCreated(timestamppb.Now())
	}
	if !c.HasExpires() {
		c.SetExpires(timestamppb.New(c.GetCreated().AsTime().AddDate(1, 0, 0)))
	}
	if c.GetLeft() == 0 {
		c.SetLeft(c.GetGranted())
	}

	v.mu.Lock()
	defer v.mu.Unlock()
	if c.GetCurrency() == "" {
		ba := c.GetBillingAccount()
		if p, ok := v.workspaces[c.GetWorkspace()]; ok && ba == "" && p.current != nil {
			ba = p.current.GetBillingAccount()
		}
		c.SetCurrency("KRW")
		if b := v.billingAccount(ba); b != nil {
			c.SetCurrency(b.GetCurrency())
		}
	}
	v.credits = append(v.credits, c)
	return proto.CloneOf(c)
}

// Balance has billing account `id` owed `amount` beside its lots -- of an
// invoice that came to less than nothing, a credit note, a payment of more
// than was due -- which never expires and is spent after them.
func (v *Levyan) Balance(id string, amount int64) {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.balances[id] = amount
}

// Handed has the fake keep `r` as a reading the deployment handed over: what
// a browser test has a workspace it made be measured at, which the deployment
// measures at midnight.
func (v *Levyan) Handed(r *levyanv1.Reading) {
	r = proto.CloneOf(r)
	if !r.HasMeasuredAt() {
		r.SetMeasuredAt(timestamppb.Now())
	}

	v.mu.Lock()
	defer v.mu.Unlock()
	v.handed = append(v.handed, r)
}

// ClosePeriod closes the period before the one now open for `workspace`, as
// levyan closes one: its statement is what the workspace comes to over that
// period ([Levyan.rated]) -- kept, and answered, and its payer's to read
// ([Levyan.paid]) -- and what it took of each pool is taken off what is left
// of it.
func (v *Levyan) ClosePeriod(workspace string) *levyanv1.Statement {
	now := time.Now()
	start, end := PeriodBefore(now)

	v.mu.Lock()
	defer v.mu.Unlock()
	lines, total, took := v.rated(workspace, start, end)
	st := levyanv1.Statement_builder{
		Id:        newId(),
		Workspace: workspace,
		Period:    levyanv1.Period_builder{Start: timestamppb.New(start), End: timestamppb.New(end), Zone: "Asia/Seoul"}.Build(),
		Channel:   levyanv1.Channel_CHANNEL_MOR,
		Currency:  "KRW",
		Lines:     lines,
		Total:     total,
		Closed:    timestamppb.New(now),
	}.Build()
	v.statements[workspace] = append(v.statements[workspace], st)
	v.paid(st)
	for _, p := range v.pools[workspace] {
		if n, ok := took[p.GetId()]; ok {
			p.SetLeft(decimal(new(big.Rat).Sub(rat(p.GetLeft()), n)))
		}
	}
	return proto.CloneOf(st)
}

// Finalize bills billing account `ba` an invoice of the period before the
// one now open, of `lines` -- a statement each -- finalized as levyan
// finalizes one (its `Invoices.decide`, design 19): paid first with the
// credit it may be paid with, in its currency and neither spent nor expired,
// the soonest to expire first -- a workspace's no further than what that
// workspace's own lines come to, the billing account's own lots counting as
// having paid the other lines first -- and then with its balance; what is
// left is due, and one that leaves nothing due is paid. It answers the
// invoice as GetInvoice does.
func (v *Levyan) Finalize(ba string, lines ...*levyanv1.InvoiceLine) *levyanv1.Invoice {
	now := time.Now()
	start, end := PeriodBefore(now)

	v.mu.Lock()
	defer v.mu.Unlock()
	inv := levyanv1.Invoice_builder{
		Id:             newId(),
		BillingAccount: ba,
		Start:          timestamppb.New(start),
		End:            timestamppb.New(end),
		Channel:        levyanv1.Channel_CHANNEL_MOR,
		Currency:       "KRW",
		Finalized:      timestamppb.New(now),
	}.Build()
	if b := v.billingAccount(ba); b != nil {
		inv.SetChannel(b.GetChannel())
		inv.SetCurrency(b.GetCurrency())
	}
	var total int64
	of := map[string]int64{} // what each workspace's lines come to
	for _, l := range lines {
		l = proto.CloneOf(l)
		if !l.HasPeriod() {
			l.SetPeriod(levyanv1.Period_builder{Start: timestamppb.New(start), End: timestamppb.New(end), Zone: "Asia/Seoul"}.Build())
		}
		inv.SetLines(append(inv.GetLines(), l))
		total += l.GetTotal()
		of[l.GetWorkspace()] += l.GetTotal()
	}
	inv.SetSubtotal(total)
	inv.SetTotal(total)

	lots := []*levyanv1.Credit{}
	for _, c := range v.credits {
		mine := c.GetBillingAccount() == ba || (c.GetWorkspace() != "" && of[c.GetWorkspace()] > 0)
		if mine && c.GetLeft() > 0 && c.GetCurrency() == inv.GetCurrency() && c.GetExpires().AsTime().After(now) {
			lots = append(lots, c)
		}
	}
	soonestFirst(lots)

	var paid int64
	byOwn := map[string]int64{} // what each workspace's own lots paid
	for _, c := range lots {
		n := min(c.GetLeft(), total-paid)
		if w := c.GetWorkspace(); w != "" {
			// What was paid of the invoice beside its own lots is counted
			// against the other workspaces' lines first.
			beside := paid - byOwn[w]
			n = min(n, of[w]-byOwn[w]-max(0, beside-(total-of[w])))
			byOwn[w] += max(n, 0)
		}
		if n <= 0 {
			continue
		}
		c.SetLeft(c.GetLeft() - n)
		paid += n
		inv.SetCredits(append(inv.GetCredits(), levyanv1.InvoiceCredit_builder{
			Credit: c.GetId(), Amount: n, Workspace: c.GetWorkspace(), Expires: c.GetExpires(),
		}.Build()))
	}
	if b := v.balances[ba]; b > 0 && paid < total {
		n := min(b, total-paid)
		v.balances[ba] -= n
		paid += n
		inv.SetCredits(append(inv.GetCredits(), levyanv1.InvoiceCredit_builder{Amount: n}.Build()))
	}

	inv.SetCredited(paid)
	inv.SetDue(total - paid)
	if inv.GetDue() == 0 {
		inv.SetState(levyanv1.InvoiceState_INVOICE_STATE_PAID)
		inv.SetSettled(timestamppb.New(now))
	} else {
		inv.SetState(levyanv1.InvoiceState_INVOICE_STATE_OPEN)
		inv.SetRemaining(inv.GetDue())
	}
	v.invoices[ba] = append(v.invoices[ba], inv)
	return proto.CloneOf(inv)
}

// Note has the invoice billing account `ba` was billed last have a credit
// note take `total` back of it, for `reason`, as levyan's operator writes
// one: what the payer's page lists beside its invoices. Nil when the billing
// account was billed nothing.
func (v *Levyan) Note(ba string, reason levyanv1.CreditNoteReason, total int64) *levyanv1.CreditNote {
	v.mu.Lock()
	defer v.mu.Unlock()
	invs := v.invoices[ba]
	if len(invs) == 0 {
		return nil
	}
	inv := invs[len(invs)-1]
	n := levyanv1.CreditNote_builder{
		Id:       newId(),
		Invoice:  inv.GetId(),
		Reason:   reason,
		Amount:   total,
		Total:    total,
		Refunded: total,
		Created:  timestamppb.New(time.Now()),
	}.Build()
	v.notes[inv.GetId()] = append(v.notes[inv.GetId()], n)
	return proto.CloneOf(n)
}

// creditOf is the lots of `workspace`'s neither spent nor expired, the
// soonest to expire first: what GetPayer answers of it.
func (v *Levyan) creditOf(workspace string, now time.Time) []*levyanv1.Credit {
	out := []*levyanv1.Credit{}
	for _, c := range v.credits {
		if c.GetWorkspace() == workspace && c.GetLeft() > 0 && c.GetExpires().AsTime().After(now) {
			out = append(out, proto.CloneOf(c))
		}
	}
	soonestFirst(out)
	return out
}

// creditFor is what billing account `ba`'s next invoices may be paid with:
// its own lots, and those of the workspaces it pays for now or will from the
// end of the period, neither spent nor expired, the soonest to expire first.
func (v *Levyan) creditFor(ba string, now time.Time) []*levyanv1.Credit {
	out := []*levyanv1.Credit{}
	for _, c := range v.credits {
		if c.GetLeft() <= 0 || !c.GetExpires().AsTime().After(now) {
			continue
		}
		if c.GetBillingAccount() == ba || (c.GetWorkspace() != "" && v.paysFor(ba, c.GetWorkspace())) {
			out = append(out, proto.CloneOf(c))
		}
	}
	soonestFirst(out)
	return out
}

// paysFor is whether billing account `ba` pays for `workspace` now, or will
// from the end of the period.
func (v *Levyan) paysFor(ba, workspace string) bool {
	p, ok := v.workspaces[workspace]
	if !ok || ba == "" {
		return false
	}
	for _, l := range []*levyanv1.Link{p.current, p.next} {
		if l != nil && l.GetBillingAccount() == ba {
			return true
		}
	}
	return false
}

// billingAccount is billing account `id`, whoever's it is, or nil.
func (v *Levyan) billingAccount(id string) *levyanv1.BillingAccount {
	for _, bs := range v.billing {
		for _, b := range bs {
			if b.GetId() == id {
				return b
			}
		}
	}
	return nil
}

func soonestFirst(cs []*levyanv1.Credit) {
	sort.SliceStable(cs, func(i, j int) bool {
		return cs[i].GetExpires().AsTime().Before(cs[j].GetExpires().AsTime())
	})
}

// allowancesIn is `workspace`'s allowances in force at any moment of [start,
// end), by when each begins.
func (v *Levyan) allowancesIn(workspace string, start, end time.Time) []*levyanv1.Allowance {
	out := []*levyanv1.Allowance{}
	for _, a := range v.allowances[workspace] {
		if a.GetFrom().AsTime().Before(end) && (!a.HasUntil() || a.GetUntil().AsTime().After(start)) {
			out = append(out, a)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].GetFrom().AsTime().Before(out[j].GetFrom().AsTime()) })
	return out
}

// poolsIn is `workspace`'s pools in force at any moment of [start, end), the
// soonest to expire first.
func (v *Levyan) poolsIn(workspace string, start, end time.Time) []*levyanv1.Pool {
	out := []*levyanv1.Pool{}
	for _, p := range v.pools[workspace] {
		if p.GetFrom().AsTime().Before(end) && p.GetExpires().AsTime().After(start) {
			out = append(out, p)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].GetExpires().AsTime().Before(out[j].GetExpires().AsTime()) })
	return out
}

// latest is the latest reading of each of `workspace`'s meters, of what was
// recorded and what a test handed over.
func (v *Levyan) latest(workspace string) map[string]*levyanv1.Reading {
	out := map[string]*levyanv1.Reading{}
	all := slices.Concat(v.calls...)
	all = append(all, v.handed...)
	for _, r := range all {
		if r.GetWorkspace() != workspace {
			continue
		}
		if was, ok := out[r.GetMeter()]; !ok || !r.GetMeasuredAt().AsTime().Before(was.GetMeasuredAt().AsTime()) {
			out[r.GetMeter()] = r
		}
	}
	return out
}

// rated is what `workspace` comes to over the period [start, end), as the
// fake rates one: each meter's latest reading for the whole of it at
// [Prices], which include nothing of their own; what its allowances in force
// through the period give added to what is included, each by its share of
// the period; and what is billable past that taken first out of its pools in
// force at any moment of it, the soonest to expire first, as far as what is
// left of each goes -- the rest priced (levyan's `rating.Rate`, its design
// 19). Exact until the total, which is rounded once, down. It answers the
// lines, the total, and what it took of each pool. Held under the lock.
func (v *Levyan) rated(workspace string, start, end time.Time) ([]*levyanv1.Line, int64, map[string]*big.Rat) {
	latest := v.latest(workspace)
	meters := make([]string, 0, len(latest))
	for k := range latest {
		meters = append(meters, k)
	}
	sort.Strings(meters)

	span := new(big.Rat).SetInt64(end.Sub(start).Nanoseconds())
	pools := v.poolsIn(workspace, start, end)
	allowances := v.allowancesIn(workspace, start, end)
	took := map[string]*big.Rat{}
	total := new(big.Rat)
	lines := []*levyanv1.Line{}
	for _, m := range meters {
		r := latest[m]
		used := new(big.Rat).SetInt64(r.GetValue())

		given := new(big.Rat)
		for _, a := range allowances {
			if a.GetMeter() != m {
				continue
			}
			from, to := a.GetFrom().AsTime(), end
			if from.Before(start) {
				from = start
			}
			if a.HasUntil() && a.GetUntil().AsTime().Before(to) {
				to = a.GetUntil().AsTime()
			}
			if !to.After(from) {
				continue
			}
			share := new(big.Rat).Quo(new(big.Rat).SetInt64(to.Sub(from).Nanoseconds()), span)
			given.Add(given, share.Mul(share, new(big.Rat).SetInt64(a.GetQuantity())))
		}

		billable := new(big.Rat).Sub(used, given)
		if billable.Sign() < 0 {
			billable.SetInt64(0)
		}
		pooled := new(big.Rat)
		taps := []*levyanv1.LinePool{}
		for _, p := range pools {
			need := new(big.Rat).Sub(billable, pooled)
			if p.GetMeter() != m || need.Sign() <= 0 {
				continue
			}
			left := rat(p.GetLeft())
			if t, ok := took[p.GetId()]; ok {
				left.Sub(left, t)
			}
			if left.Sign() <= 0 {
				continue
			}
			n := need
			if left.Cmp(need) < 0 {
				n = left
			}
			pooled.Add(pooled, n)
			if _, ok := took[p.GetId()]; !ok {
				took[p.GetId()] = new(big.Rat)
			}
			took[p.GetId()].Add(took[p.GetId()], n)
			taps = append(taps, levyanv1.LinePool_builder{Pool: p.GetId(), Taken: decimal(n)}.Build())
		}

		amount := new(big.Rat).Sub(billable, pooled)
		amount.Mul(amount, new(big.Rat).SetInt64(Prices[m]))
		total.Add(total, amount)
		lines = append(lines, levyanv1.Line_builder{
			Meter:     m,
			Unit:      r.GetUnit(),
			Kind:      levyanv1.LineKind_LINE_KIND_USAGE,
			Usage:     decimal(used),
			Included:  decimal(given),
			Billable:  decimal(billable),
			Amount:    decimal(amount),
			Allowance: decimal(given),
			Pooled:    decimal(pooled),
			Pools:     taps,
		}.Build())
	}
	floor := new(big.Int).Quo(total.Num(), total.Denom())
	return lines, floor.Int64(), took
}

// PeriodBefore is the period before the one open at `now`, as the fake has
// its periods: last month, in Korea -- from its first moment to the first
// after it.
func PeriodBefore(now time.Time) (time.Time, time.Time) {
	start, _ := period(now)
	before, _ := period(start.Add(-time.Nanosecond))
	return before, start
}

// rat is a decimal levyan writes as the fraction it is; one that is none is
// nothing.
func rat(s string) *big.Rat {
	r, ok := new(big.Rat).SetString(s)
	if !ok {
		return new(big.Rat)
	}
	return r
}

// decimal is a fraction as levyan's API writes one: exact when it ends within
// six digits after the point, and cut there, toward nothing, when it does not.
func decimal(r *big.Rat) string {
	const digits = 6
	n := new(big.Int).Mul(r.Num(), big.NewInt(1_000_000))
	n.Quo(n, r.Denom())
	sign := ""
	if n.Sign() < 0 {
		sign = "-"
		n.Neg(n)
	}
	s := n.String()
	if len(s) <= digits {
		s = strings.Repeat("0", digits-len(s)+1) + s
	}
	whole, frac := s[:len(s)-digits], strings.TrimRight(s[len(s)-digits:], "0")
	if whole == "0" && frac == "" {
		sign = ""
	}
	if frac == "" {
		return sign + whole
	}
	return sign + whole + "." + frac
}
