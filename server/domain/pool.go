package domain

import (
	"fmt"
	"slices"
	"time"
	"uuid"

	"github.com/lesomnus/payday/pdid"
	"github.com/lesomnus/z"
	"github.com/protobuf-orm/ent/dialect/sql"

	app "github.com/lesomnus/rove"
	"github.com/lesomnus/rove/internal/ent"
	"github.com/lesomnus/rove/internal/ent/allocation"
	"github.com/lesomnus/rove/internal/ent/asset"
	"github.com/lesomnus/rove/internal/ent/bookable"
	"github.com/lesomnus/rove/internal/ent/link"
	"github.com/lesomnus/rove/internal/ent/reservation"
	"github.com/lesomnus/rove/internal/ent/reservationitem"
)

// A pool is a group made reservable: things any one of which does as well as
// another, of which a reservation takes as many as it asks for, and its
// hand-over picks which (design 4). What it has to give is its members --
// what its open `member_of` links say is in it -- that are in use ([usable]),
// less the ones whose own time is taken then: by maintenance, by a booking of
// the thing itself, or by a booking of a kit that takes it. A group nothing is
// a member of, and anything else reservable, has the units somebody set
// (`Bookable.units`): thirty folding chairs registered as one asset.
//
// What a pool can give changes after it was booked -- a member given to it or
// taken off it, one lost, retired, disposed or received, maintenance booked on
// one -- and what is here keeps its reservations in step, in the transaction
// that changed it ([Tx.recheckPools]). Where the busiest moment of a
// reservation's time no longer fits, the ones made last wait, by an allocation
// that blocks nothing ([allocWaiting]), as a kit's reservation waits for a
// member (kit.go); whoever has to settle it is told, and the reservation shows
// it wherever it is acted on. Once there is room again, the ones that have
// waited longest take it first ([Tx.take]). What each of them did to what a
// reservation holds of the pool is on the history of both, the reservation
// and the pool, by how many of it, as a kit's is ([Tx.changed], design D39).
//
// And a thing that is one of a pool's units is the pool's to spare: booked on
// its own, or with a kit, while the pool has as many of its units promised as
// it can give without it, it is refused -- the pool's reservations came first
// ([Tx.poolsShort]).
//
// A thing that is a member of two pools is one unit to one of them at a time,
// so none of this is of one pool alone: each count is of the pools around it
// ([Tx.circle], ledger.go), which all give what they are booked for at once or
// do not (D44).

// usable says whether a member in `status` is one of its pool's units: there
// to be handed out, or to be once its maintenance is over. One being repaired
// is one -- what keeps a reservation from it is the time its work order
// blocks, as for anything booked on its own, and a repair that blocks none
// takes none -- and one ordered and not received, lost, retired or disposed is
// not.
func usable(status string) bool {
	switch status {
	case "ordered", "lost", "retired", "disposed":
		return false
	}
	return true
}

// poolOutOfUse says whether `a` is a group that is out of use itself: one
// whose reservations are said to take what is out of use ([Tx.outOfUseOf]),
// and so are not counted short of it as well, whatever its members are --
// nor take any of it they wait for -- until it is in use again (D43).
func poolOutOfUse(a *ent.Asset) bool {
	return a != nil && a.Kind == "group" && slices.Contains(OutOfUse, a.Status)
}

// pool is what a group is made of, as the units it is booked by.
type pool struct {
	// members is everything in it now, and units the ones of them that count,
	// each in identifier order.
	members, units []uuid.UUID
}

// poolOf answers what `group` is made of now, or nil when nothing is a member
// of it: then its units are the ones that were set.
func (t *Tx) poolOf(group uuid.UUID) (*pool, error) {
	ls, err := t.db.Link.Query().Where(
		link.TenantId(t.tenant.Uuid()),
		link.TargetId(group),
		link.Kind("member_of"),
		link.SupersededAtIsNil(),
		link.ValidToIsNil(),
	).All(t.ctx)
	if err != nil || len(ls) == 0 {
		return nil, err
	}
	ids := make([]uuid.UUID, 0, len(ls))
	for _, l := range ls {
		ids = append(ids, l.SourceId)
	}
	as, err := t.db.Asset.Query().Where(asset.TenantId(t.tenant.Uuid()), asset.IdIn(ids...), asset.DateErasedIsNil()).All(t.ctx)
	if err != nil || len(as) == 0 {
		return nil, err
	}
	p := &pool{}
	for _, a := range as {
		p.members = append(p.members, a.Id)
		if usable(a.Status) {
			p.units = append(p.units, a.Id)
		}
	}
	slices.SortFunc(p.members, func(a, b uuid.UUID) int { return a.Compare(b) })
	slices.SortFunc(p.units, func(a, b uuid.UUID) int { return a.Compare(b) })
	return p, nil
}

// poolFor answers what `r` is made of as a pool: nil for anything but a group
// with members.
func (t *Tx) poolFor(r *resource) (*pool, error) {
	if r.asset == nil || r.asset.Kind != "group" {
		return nil, nil
	}
	return t.poolOf(r.asset.Id)
}

// capacity is how many of `r` there are to book: a pool's units, or the
// units that were set. And the pool, for one.
func (t *Tx) capacity(r *resource) (uint32, *pool, error) {
	p, err := t.poolFor(r)
	if err != nil || p == nil {
		return r.book.Units, nil, err
	}
	return uint32(len(p.units)), p, nil
}

// heldOf answers the blocking allocations of these things' own that touch
// [from, to), but those of the reservations in `except`: what takes the time
// of a pool's members then.
func (t *Tx) heldOf(ids []uuid.UUID, from, to time.Time, except []uuid.UUID) ([]*ent.Allocation, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	q := t.db.Allocation.Query().Where(
		allocation.TenantId(t.tenant.Uuid()),
		allocation.ResourceIdIn(ids...),
		allocation.Blocking(true),
		allocation.BeginsAtLT(to),
		allocation.EndsAtGT(from),
	)
	if len(except) > 0 {
		q = q.Where(allocation.Or(allocation.RefIdIsNil(), allocation.RefIdNotIn(except...)))
	}
	return q.All(t.ctx)
}

// near answers the ones of these that touch [from, to).
func near(as []*ent.Allocation, from, to time.Time) []*ent.Allocation {
	out := []*ent.Allocation{}
	for _, a := range as {
		if a.BeginsAt.Before(to) && a.EndsAt.After(from) {
			out = append(out, a)
		}
	}
	return out
}

// poolsOf answers, for each of these things, the reservable pools it is one
// of the units of now: the groups it is a member of that may be booked, of a
// thing in use ([usable]).
func (t *Tx) poolsOf(ids []uuid.UUID) (map[uuid.UUID][]*resource, error) {
	out := map[uuid.UUID][]*resource{}
	if len(ids) == 0 {
		return out, nil
	}
	tid := t.tenant.Uuid()
	ls, err := t.db.Link.Query().Where(
		link.TenantId(tid),
		link.SourceIdIn(ids...),
		link.Kind("member_of"),
		link.SupersededAtIsNil(),
		link.ValidToIsNil(),
	).All(t.ctx)
	if err != nil || len(ls) == 0 {
		return out, err
	}
	members, targets := []uuid.UUID{}, []uuid.UUID{}
	for _, l := range ls {
		members = append(members, l.SourceId)
		targets = append(targets, l.TargetId)
	}
	things, err := t.db.Asset.Query().Where(asset.TenantId(tid), asset.IdIn(members...), asset.DateErasedIsNil()).All(t.ctx)
	if err != nil {
		return out, err
	}
	units := map[uuid.UUID]bool{}
	for _, a := range things {
		units[a.Id] = usable(a.Status)
	}
	groups, err := t.db.Asset.Query().Where(asset.TenantId(tid), asset.IdIn(targets...), asset.Kind("group"), asset.DateErasedIsNil()).All(t.ctx)
	if err != nil || len(groups) == 0 {
		return out, err
	}
	gs := make([]uuid.UUID, 0, len(groups))
	for _, g := range groups {
		gs = append(gs, g.Id)
	}
	bs, err := t.db.Bookable.Query().Where(bookable.TenantId(tid), bookable.AssetIdIn(gs...), bookable.DateErasedIsNil()).All(t.ctx)
	if err != nil {
		return out, err
	}
	pools := map[uuid.UUID]*resource{}
	for _, b := range bs {
		for _, g := range groups {
			if g.Id == b.AssetId {
				pools[g.Id] = &resource{asset: g, book: b}
			}
		}
	}
	for _, l := range ls {
		if p := pools[l.TargetId]; p != nil && units[l.SourceId] && !slices.Contains(out[l.SourceId], p) {
			out[l.SourceId] = append(out[l.SourceId], p)
		}
	}
	return out, nil
}

// poolsShort answers what keeps `r` from being taken for [from, to) as one of
// the units of a pool it is in: where the pools around it cannot give all
// they have promised then without it, what holds them -- their reservations,
// and their members' own time. The reservations in `except` are none of it.
//
// The caller holds the schedule of every pool around `r` ([Tx.holdsPools]),
// as a booking of any of them does as it looks.
func (t *Tx) poolsShort(r *resource, from, to time.Time, except []uuid.UUID) ([]*ent.Allocation, error) {
	if r.asset == nil || !usable(r.asset.Status) {
		return nil, nil
	}
	ps, err := t.poolsOf([]uuid.UUID{r.asset.Id})
	if err != nil || len(ps[r.asset.Id]) == 0 {
		return nil, err
	}
	around, err := t.circle([]uuid.UUID{r.asset.Id})
	if err != nil {
		return nil, err
	}
	l, err := t.ledgerOf(around, from, to, except)
	if err != nil {
		return nil, err
	}
	// It, taken then: one fewer to give, of whichever of them.
	it := &ent.Allocation{ResourceId: r.asset.Id, BeginsAt: from, EndsAt: to, Units: 1}
	if l.short(l.own, append(slices.Clone(l.held), it), from, to) {
		return slices.Concat(l.own, l.held), nil
	}
	return nil, nil
}

// holdsPools says whether this transaction holds the schedule of every pool
// around `r` ([Tx.circle]), which a booking of `r` is held to
// ([Tx.poolsShort]).
func (t *Tx) holdsPools(r *resource) (bool, error) {
	if r.asset == nil {
		return true, nil
	}
	around, err := t.circle([]uuid.UUID{r.asset.Id})
	if err != nil {
		return false, err
	}
	for _, p := range around {
		if !t.holding(p) {
			return false, nil
		}
	}
	return true, nil
}

// poolSchedules answers the pools around these things ([Tx.circle]), by the
// asset: what a booking that takes any of them holds besides their own
// schedules.
func (t *Tx) poolSchedules(ids []uuid.UUID) ([]uuid.UUID, error) {
	around, err := t.circle(ids)
	if err != nil {
		return nil, err
	}
	out := make([]uuid.UUID, 0, len(around))
	for _, r := range around {
		out = append(out, r.asset.Id)
	}
	return out, nil
}

// schedulesOf answers what a booking that takes any of these things holds as
// it looks, by the asset: each thing, the kits that take it with them, and the
// pools it is one of the units of. A change to what pools a thing is in holds
// these before it rechecks them, so that a booking of the thing that began
// before the change and did not see it is waited for, and what it booked is
// seen ([Tx.recheckPools]).
func (t *Tx) schedulesOf(ids []uuid.UUID) ([]uuid.UUID, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	out := slices.Clone(ids)
	ks, err := t.db.Link.Query().Where(
		link.TenantId(t.tenant.Uuid()),
		link.SourceIdIn(ids...),
		link.Kind("member_of"),
		link.Required(true),
		link.SupersededAtIsNil(),
		link.ValidToIsNil(),
	).All(t.ctx)
	if err != nil {
		return nil, err
	}
	for _, l := range ks {
		out = append(out, l.TargetId)
	}
	ps, err := t.poolSchedules(ids)
	if err != nil {
		return nil, err
	}
	out = append(out, ps...)
	slices.SortFunc(out, func(a, b uuid.UUID) int { return a.Compare(b) })
	return slices.Compact(out), nil
}

// poolWait is a reservation of a pool that waits for some of it, as it is
// told.
type poolWait struct {
	r     *ent.Reservation
	pool  *resource
	units uint32
	from  time.Time
}

// recheckPools brings the reservations of these groups, the reservable ones,
// in step with what each can give now, in the transaction that changed it
// (design 4) -- and of every pool around them ([Tx.circle]), and around the
// pools `changed` are units of, since a unit one of them has or lacks is one
// another may have instead (D44). Each pool's units are what its members say
// ([Tx.poolOf]), kept on its row as well; where a reservation still to be
// fulfilled no longer fits beside the ones made before it and what cannot
// wait, it waits for what it booked, from now on; and the reservations that
// wait take what there is room for, the one that has waited longest first.
// `changed` is the things whose membership of them this transaction changed.
//
// It holds the pools' schedules before it reads what they are booked for and
// what their members' own time is taken by, which every booking of any of them
// holds before it looks, and every booking of one of their units as well
// ([Tx.poolsShort]): a booking that went first is read here, and one that goes
// after reads the pools as this left them. And for a thing given to a pool, it
// holds what a booking of the thing holds ([Tx.schedulesOf]): a booking of it
// that looked before the thing was the pool's is waited for, and what it
// booked is seen. All at once, in the one order everything takes schedules in.
//
// A reservation in use is not moved: it was handed out, or checked into, and
// what it holds of the pool is what is out.
func (t *Tx) recheckPools(groups []uuid.UUID, changed []uuid.UUID) error {
	pools, hold, err := t.poolHolds(groups, changed)
	if err != nil || len(pools) == 0 {
		return err
	}
	if err := t.lockSchedules(hold); err != nil {
		return err
	}
	// Read again, now that nothing changes them.
	around, err := t.circle(pools)
	if err != nil {
		return err
	}
	short, err := t.rebook(around)
	if err != nil {
		return err
	}
	return t.tellShort(short)
}

// poolHolds answers the pools [Tx.recheckPools] rechecks for these groups and
// the things in `changed`, by the asset -- the reservable ones of the groups,
// the ones the things are members of, and the pools around them -- and every
// schedule it holds to recheck them: theirs, and what a booking of a thing in
// `changed` holds ([Tx.schedulesOf]).
func (t *Tx) poolHolds(groups, changed []uuid.UUID) ([]uuid.UUID, []uuid.UUID, error) {
	seeds := slices.Clone(groups)
	if len(changed) > 0 {
		ls, err := t.db.Link.Query().Where(
			link.TenantId(t.tenant.Uuid()),
			link.SourceIdIn(changed...),
			link.Kind("member_of"),
			link.SupersededAtIsNil(),
			link.ValidToIsNil(),
		).All(t.ctx)
		if err != nil {
			return nil, nil, err
		}
		for _, l := range ls {
			seeds = append(seeds, l.TargetId)
		}
	}
	if len(seeds) == 0 {
		return nil, nil, nil
	}
	around, err := t.circle(seeds)
	if err != nil || len(around) == 0 {
		// Never reservable, so never reserved.
		return nil, nil, err
	}
	pools := make([]uuid.UUID, 0, len(around))
	for _, r := range around {
		pools = append(pools, r.asset.Id)
	}
	also, err := t.schedulesOf(changed)
	if err != nil {
		return nil, nil, err
	}
	return pools, append(slices.Clone(pools), also...), nil
}

// rebook is [Tx.recheckPools] of the pools around something, the ones of
// which this holds the schedules: the rest -- around it since by a change this
// did not wait for, which holds these as well -- are only read, as what the
// others fit beside. It answers the reservations it made wait.
func (t *Tx) rebook(pools []*resource) ([]poolWait, error) {
	tid := t.tenant.Uuid()
	l := &ledger{units: map[uuid.UUID][]uuid.UUID{}, set: map[uuid.UUID]uint32{}}
	of := map[uuid.UUID]*resource{}
	ids, members := []uuid.UUID{}, []uuid.UUID{}
	for _, pr := range pools {
		mine := t.holding(pr)
		p, err := t.poolOf(pr.asset.Id)
		if err != nil {
			return nil, err
		}
		units := pr.book.Units
		if p != nil {
			units = uint32(len(p.units))
		} else {
			// Nothing is a member of it now. A group whose last member left
			// it in this operation has none to give -- what it had were its
			// members -- until it is given members again, or units by hand;
			// any other has what was set, as it had.
			left, err := t.db.Link.Query().Where(
				link.TenantId(tid),
				link.TargetId(pr.asset.Id),
				link.Kind("member_of"),
				link.ValidToIsNil(),
				link.SupersededAtGTE(t.now),
			).Exist(t.ctx)
			if err != nil {
				return nil, err
			}
			if left {
				units = 0
			}
		}
		if mine && units != pr.book.Units {
			if _, err := t.next.Bookable().Patch(t.ctx, app.BookablePatchRequest_builder{
				Ref:              app.BookableRef_builder{Id: pdid.Id(pr.book.Id).Bytes()}.Build(),
				Units:            z.Ptr(units),
				DateUpdatedForce: z.Ptr(true),
			}.Build()); err != nil {
				return nil, err
			}
			pr.book.Units = units
		}
		of[pr.asset.Id] = pr
		ids = append(ids, pr.asset.Id)
		if p == nil {
			l.set[pr.asset.Id] = units
			continue
		}
		l.units[pr.asset.Id] = p.units
		members = append(members, p.units...)
	}
	if len(ids) == 0 {
		return nil, nil
	}

	// What they are booked for from now on, and what of it waits.
	as, err := t.db.Allocation.Query().Where(
		allocation.TenantId(tid),
		allocation.ResourceIdIn(ids...),
		allocation.EndsAtGT(t.now),
		allocation.Or(allocation.Blocking(true), allocation.Kind(allocWaiting)),
	).Order(allocation.ByDateCreated(), allocation.ById()).All(t.ctx)
	if err != nil || len(as) == 0 {
		return nil, err
	}
	refs := []uuid.UUID{}
	until := t.now
	for _, a := range as {
		if a.RefId != nil {
			refs = append(refs, *a.RefId)
		}
		if a.EndsAt.After(until) {
			until = a.EndsAt
		}
	}
	rs, err := t.db.Reservation.Query().Where(reservation.TenantId(tid), reservation.IdIn(refs...)).All(t.ctx)
	if err != nil {
		return nil, err
	}
	res := map[uuid.UUID]*ent.Reservation{}
	for _, r := range rs {
		res[r.Id] = r
	}
	// What may wait: what a reservation still to be fulfilled holds of a pool
	// this holds. The rest -- one in use, maintenance of a group itself, what
	// a pool out of use itself is booked for (D43), what another pool is --
	// stays as it is, and is what the others fit beside.
	fixed, flex, waiting := []*ent.Allocation{}, []*ent.Allocation{}, []*ent.Allocation{}
	for _, a := range as {
		var r *ent.Reservation
		if a.RefId != nil {
			r = res[*a.RefId]
		}
		pr := of[a.ResourceId]
		pending := r != nil && waits(r.Status) && r.EndsAt.After(t.now) && r.DateErased == nil &&
			t.holding(pr) && !poolOutOfUse(pr.asset)
		switch {
		case a.Kind == allocWaiting:
			// Out of use itself, what a pool's reservations wait for they
			// do not take, until it is in use again -- which rechecks it.
			if pending {
				waiting = append(waiting, a)
			}
		case pending && a.Kind == "reservation":
			flex = append(flex, a)
		default:
			fixed = append(fixed, a)
		}
	}
	slices.SortFunc(members, func(a, b uuid.UUID) int { return a.Compare(b) })
	held, err := t.heldOf(slices.Compact(members), t.now, until, nil)
	if err != nil {
		return nil, err
	}

	// Each keeps its time while it fits beside the ones made before it, in
	// the order they were made, of whichever pool; the ones made last wait.
	slices.SortStableFunc(flex, func(a, b *ent.Allocation) int {
		ra, rb := res[*a.RefId], res[*b.RefId]
		if c := ra.DateCreated.Compare(rb.DateCreated); c != 0 {
			return c
		}
		return ra.Id.Compare(rb.Id)
	})
	kept := fixed
	short := []poolWait{}
	// What it did to each, an event of the reservation's and one of the
	// pool's, children of the change, written at once ([Tx.changed]).
	did := []allocChange{}
	for _, a := range flex {
		from := a.BeginsAt
		if from.Before(t.now) {
			from = t.now
		}
		if !a.EndsAt.After(from) || !l.short(append(near(kept, from, a.EndsAt), a), near(held, from, a.EndsAt), from, a.EndsAt) {
			kept = append(kept, a)
			continue
		}
		r, pr := res[*a.RefId], of[a.ResourceId]
		if _, err := t.next.Allocation().Erase(t.ctx, app.AllocationRef_builder{Id: pdid.Id(a.Id).Bytes()}.Build()); err != nil {
			return nil, err
		}
		if _, err := t.next.Allocation().Add(t.ctx, app.AllocationAddRequest_builder{
			Tenant:   t.tenantRef(),
			Resource: assetRef(pr.asset.Id),
			Kind:     allocWaiting,
			RefId:    pdid.Id(r.Id).Bytes(),
			BeginsAt: ts(from),
			EndsAt:   ts(a.EndsAt),
			Units:    a.Units,
		}.Build()); err != nil {
			return nil, err
		}
		short = append(short, poolWait{r: r, pool: pr, units: a.Units, from: from})
		did = append(did, allocChange{kind: "reservation.wait", r: r, thing: pr.asset, units: max(a.Units, 1)})
	}
	if err := t.changed(did...); err != nil {
		return nil, err
	}

	// And then what waits takes what there is room for, the one that has
	// waited longest first, of whichever pool.
	for _, w := range waiting {
		if _, err := t.take(w, false); err != nil {
			return nil, err
		}
	}
	return short, nil
}

// tellShort tells who has to settle it that reservations of a pool wait for
// what they booked: whoever runs the reservations, once for each pool, and
// whoever each reservation is for, once for theirs -- by mail as well
// ([Mailed]), since it is to be settled before the reservation begins.
func (t *Tx) tellShort(ws []poolWait) error {
	if len(ws) == 0 {
		return nil
	}
	byPool := map[uuid.UUID][]poolWait{}
	order := []uuid.UUID{}
	for _, w := range ws {
		k := w.pool.asset.Id
		if _, ok := byPool[k]; !ok {
			order = append(order, k)
		}
		byPool[k] = append(byPool[k], w)
	}
	for _, k := range order {
		vs := byPool[k]
		pr := vs[0].pool
		if err := t.notifyManagers("reservation.conflict", "풀 수량 부족: "+tagged(pr.asset),
			fmt.Sprintf("%s은(는) 그 시간에 쓸 수 있는 것이 모자라 예약 %d건이 기다립니다. 다시 생기면 먼저 기다린 예약부터 잡습니다.", tagged(pr.asset), len(vs)),
			pdid.Id(k), "/reservations?tab=all"); err != nil {
			return err
		}

		byParty := map[uuid.UUID][]poolWait{}
		parties := []uuid.UUID{}
		for _, v := range vs {
			if _, ok := byParty[v.r.PartyId]; !ok {
				parties = append(parties, v.r.PartyId)
			}
			byParty[v.r.PartyId] = append(byParty[v.r.PartyId], v)
		}
		for _, p := range parties {
			pv := byParty[p]
			first := pv[0]
			title := "예약 수량 부족: " + first.r.Name
			if len(pv) > 1 {
				title += fmt.Sprintf(" 외 %d건", len(pv)-1)
			}
			if err := t.notifyParty(p, "reservation.conflict", title,
				fmt.Sprintf("%s은(는) %s에 쓸 수 있는 것이 모자라 이 예약의 %d개를 잡지 못했습니다. 다시 생기면 먼저 기다린 예약부터 잡습니다.", tagged(pr.asset), t.when(first.from), first.units),
				pdid.Id(first.r.Id), "/reservations?tab=mine"); err != nil {
				return err
			}
		}
	}
	return nil
}

// recheckPoolsOf rechecks the pools whose units these things are, or were,
// and any of them that is a pool itself: what a change to a thing's status or
// to its own time does to what its pools can give.
func (t *Tx) recheckPoolsOf(ids ...uuid.UUID) error {
	if len(ids) == 0 {
		return nil
	}
	ls, err := t.db.Link.Query().Where(
		link.TenantId(t.tenant.Uuid()),
		link.SourceIdIn(ids...),
		link.Kind("member_of"),
		link.SupersededAtIsNil(),
		link.ValidToIsNil(),
	).All(t.ctx)
	if err != nil {
		return err
	}
	groups := slices.Clone(ids)
	for _, l := range ls {
		groups = append(groups, l.TargetId)
	}
	return t.recheckPools(groups, nil)
}

// available is how many of `m` the reservation that waits by `w` could have
// at the busiest moment of [from, w's end): what it has to give -- a pool's
// units, less its members whose own time is taken then -- less what
// everything else takes of it then, the pools around it given what they are
// booked for first (D44).
func (t *Tx) available(w *ent.Allocation, from time.Time, m *resource) (uint32, error) {
	if !w.EndsAt.After(from) {
		return 0, nil
	}
	except := []uuid.UUID{}
	if w.RefId != nil {
		except = append(except, *w.RefId)
	}
	around, err := t.poolsAround(m)
	if err != nil {
		return 0, err
	}
	l, err := t.ledgerOf(around, from, w.EndsAt, except)
	if err != nil {
		return 0, err
	}
	return l.spare(m.asset.Id, l.own, l.held, from, w.EndsAt), nil
}

// booked says whether reservation `r` booked `thing` itself, rather than took
// it with what it booked: what it waits for of it is some of a pool's, by how
// many, and not a member of a kit's.
func (t *Tx) booked(r, thing uuid.UUID) (bool, error) {
	return t.db.ReservationItem.Query().Where(
		reservationitem.TenantId(t.tenant.Uuid()),
		reservationitem.ReservationId(r),
		reservationitem.ResourceId(thing),
	).Exist(t.ctx)
}

// holdLeaving holds what a change of an asset's status, its leaving these
// kits and groups, and the recheck of the pools it stays in hold, before any
// of them does anything, in the one order: the rows of what it leaves, which
// each relation it ends writes, and then every schedule the rechecks hold --
// of the kits it leaves ([Tx.kitHolds]), and of the pools it leaves and stays
// in, or is ([Tx.poolHolds]) -- and what a booking of it holds
// ([Tx.schedulesOf]): its own, which a booking of it holds before it reads
// its status for good (domainReservation.Add), and those of the kits that
// take it. One relation ended after another, each holding its own, would
// write a row after the schedules the one before held, and the pools' recheck
// take some after both; and a booking of it at the same moment would read it
// in use and not be told it went out of use (D43).
func (t *Tx) holdLeaving(id pdid.Id, refs []*app.AssetRef) error {
	targets := []uuid.UUID{}
	for _, ref := range refs {
		_, v, err := t.get(ref, "leave")
		if err != nil {
			// What leaving it says is wrong with it ([Tx.leave]).
			continue
		}
		targets = append(targets, v.Uuid())
	}
	slices.SortFunc(targets, func(a, b uuid.UUID) int { return a.Compare(b) })
	targets = slices.Compact(targets)
	for _, v := range targets {
		if err := t.holdRow(v); err != nil {
			return err
		}
	}
	kits, err := t.reservableKits(targets)
	if err != nil {
		return err
	}
	hold, err := t.kitHolds(kits)
	if err != nil {
		return err
	}
	in, err := t.db.Link.Query().Where(
		link.TenantId(t.tenant.Uuid()),
		link.SourceId(id.Uuid()),
		link.Kind("member_of"),
		link.SupersededAtIsNil(),
		link.ValidToIsNil(),
	).All(t.ctx)
	if err != nil {
		return err
	}
	groups := append(slices.Clone(targets), id.Uuid())
	for _, l := range in {
		groups = append(groups, l.TargetId)
	}
	_, also, err := t.poolHolds(groups, []uuid.UUID{id.Uuid()})
	if err != nil {
		return err
	}
	own, err := t.schedulesOf([]uuid.UUID{id.Uuid()})
	if err != nil {
		return err
	}
	return t.lockSchedules(slices.Concat(hold, also, own))
}

// holdRow takes the row of an asset that this transaction changes later and
// holds schedules before it does: written as it is, which takes the row's
// lock, so that the row is held before the schedules, as every other change
// of an asset that rechecks its pools holds them ([Tx.recheckPoolsOf]).
func (t *Tx) holdRow(id uuid.UUID) error {
	_, err := t.db.Asset.Update().
		Where(asset.Id(id), asset.TenantId(t.tenant.Uuid())).
		Modify(func(b *sql.UpdateBuilder) { b.Set(asset.FieldStatus, sql.Expr(asset.FieldStatus)) }).
		Save(t.ctx)
	return err
}
