package domain

import (
	"cmp"
	"context"
	"maps"
	"slices"
	"strings"
	"time"
	"uuid"

	"github.com/lesomnus/payday/pdid"
	"github.com/lesomnus/z"

	app "github.com/lesomnus/loft"
	"github.com/lesomnus/loft/internal/ent"
	"github.com/lesomnus/loft/internal/ent/allocation"
	"github.com/lesomnus/loft/internal/ent/asset"
	"github.com/lesomnus/loft/internal/ent/reservation"
	"github.com/lesomnus/loft/internal/ent/reservationitem"
)

// A reservation of a pool is a promise of some of its units, and its pickup is
// when they are picked: the design's "체크아웃할 때 실물 자산을 지정해 그 자산의
// 독점 할당으로 바꾼다" (design 4, 2026-10-26, D46). What its pickup is offered
// of each pool it booked is as many of the pool's units as it holds, free from
// the pickup until it ends ([Tx.handoutOf], ReservationService.Pickup); and
// what its pickup hands out of the pool is its own from then on, each unit an
// allocation of the unit's for the reservation in place of one it held of the
// pool ([Tx.handOver], from domainCustody.Add). So the time a unit handed out
// is taken is the unit's own, as a booking of the unit is: nothing books it
// over the reservation, and the pools around it count it as the one it is.

// handout is what the pickup of a reservation hands out of the pools it
// booked, read as they are now.
type handout struct {
	r *ent.Reservation
	// from is when what it is handed is its own, the pickup; until is when
	// the last of what it holds of the pools ends, and end when each does.
	from, until time.Time
	end         map[uuid.UUID]time.Time
	l           *ledger
	// pools is each pool it booked, in identifier order, and held what it
	// holds of each: its allocations of the pool that block -- one for each
	// time it booked it -- none while it waits for what it booked.
	pools []*resource
	held  map[uuid.UUID][]*ent.Allocation
	// mine is what else it holds -- the members a kit it booked takes with
	// it -- which is handed out as that.
	mine map[uuid.UUID]bool
}

// handoutOf reads what the pickup of `r` hands out of the pools it booked,
// from now on: the pools, what it holds of each, and the pools around them
// ([Tx.ledgerOf]) until the last of it ends.
func (t *Tx) handoutOf(r *ent.Reservation) (*handout, error) {
	tid := t.tenant.Uuid()
	h := &handout{r: r, from: t.now, until: t.now, end: map[uuid.UUID]time.Time{}, held: map[uuid.UUID][]*ent.Allocation{}, mine: map[uuid.UUID]bool{}}
	items, err := t.db.ReservationItem.Query().Where(reservationitem.TenantId(tid), reservationitem.ReservationId(r.Id)).All(t.ctx)
	if err != nil {
		return nil, err
	}
	booked := map[uuid.UUID]bool{}
	ids := []uuid.UUID{}
	for _, it := range items {
		if !booked[it.ResourceId] {
			booked[it.ResourceId] = true
			ids = append(ids, it.ResourceId)
		}
	}
	around, err := t.circle(ids)
	if err != nil {
		return nil, err
	}
	as, err := t.db.Allocation.Query().Where(allocation.TenantId(tid), allocation.RefId(r.Id)).All(t.ctx)
	if err != nil {
		return nil, err
	}
	for _, pr := range around {
		if !booked[pr.asset.Id] {
			continue
		}
		p, err := t.poolOf(pr.asset.Id)
		if err != nil {
			return nil, err
		}
		if p == nil {
			// A pool of no members is handed out as nothing of its own.
			continue
		}
		_, end := window(pr.book, r.BeginsAt, r.EndsAt)
		over := false
		for _, a := range as {
			if a.ResourceId != pr.asset.Id {
				continue
			}
			end = a.EndsAt
			over = over || a.Kind == "override"
			if a.Blocking && a.Kind == "reservation" {
				h.held[pr.asset.Id] = append(h.held[pr.asset.Id], a)
			}
		}
		if over || !end.After(t.now) {
			// Booked over what the pool had, on purpose, it took nothing of
			// what is counted (D41), and its pickup takes nothing of it
			// either: what it hands out it hands out as anything else.
			delete(h.held, pr.asset.Id)
			continue
		}
		h.pools = append(h.pools, pr)
		h.end[pr.asset.Id] = end
		if end.After(h.until) {
			h.until = end
		}
	}
	for _, a := range as {
		if a.Blocking && !booked[a.ResourceId] {
			h.mine[a.ResourceId] = true
		}
	}
	if len(h.pools) == 0 {
		return h, nil
	}
	if h.l, err = t.ledgerOf(around, h.from, h.until, nil); err != nil {
		return nil, err
	}
	return h, nil
}

// poolsOf answers which of the pools it booked `m` is one of the units of.
func (h *handout) poolsOf(m uuid.UUID) []*resource {
	out := []*resource{}
	for _, pr := range h.pools {
		if slices.Contains(h.l.units[pr.asset.Id], m) {
			out = append(out, pr)
		}
	}
	return out
}

// fits says whether `m` may be handed out for what the reservation booked of
// `pool`, beside `given` -- the units handed out for it already, by the pool:
// one of the pool's units that is nothing else of the reservation's, whose own
// time nothing else takes until the reservation's of the pool ends, and that
// the pools around it can do without until then, given what they promised and
// each unit given in place of one the reservation held of its pool.
func (h *handout) fits(pool, m uuid.UUID, given map[uuid.UUID][]uuid.UUID) bool {
	if !slices.Contains(h.l.units[pool], m) || h.mine[m] {
		return false
	}
	for _, ms := range given {
		if slices.Contains(ms, m) {
			return false
		}
	}
	end := h.end[pool]
	for _, a := range h.l.held {
		if a.ResourceId == m && (a.RefId == nil || *a.RefId != h.r.Id) && a.BeginsAt.Before(end) && a.EndsAt.After(h.from) {
			return false
		}
	}
	with := map[uuid.UUID][]uuid.UUID{}
	for p, ms := range given {
		with[p] = slices.Clone(ms)
	}
	with[pool] = append(with[pool], m)
	own, held := h.with(with)
	return !h.l.short(own, held, h.from, h.until)
}

// units is how many of `pool` the reservation holds.
func (h *handout) units(pool uuid.UUID) uint32 {
	n := uint32(0)
	for _, a := range h.held[pool] {
		n += a.Units
	}
	return n
}

// left answers how many of `pool` each allocation of it the reservation holds
// still holds once `n` of its units are given in place of what it held, by
// the allocation: what it held first, given first.
func (h *handout) left(pool uuid.UUID, n int) map[uuid.UUID]uint32 {
	out := map[uuid.UUID]uint32{}
	for _, a := range h.held[pool] {
		k := min(n, int(a.Units))
		out[a.Id] = a.Units - uint32(k)
		n -= k
	}
	return out
}

// with answers what the pools around the reservation are asked for, and what
// takes their units' own time, were `given` handed out for it: each unit its
// own from the pickup until what the reservation holds of its pool ends, and
// what it holds of each pool less as many.
func (h *handout) with(given map[uuid.UUID][]uuid.UUID) ([]*ent.Allocation, []*ent.Allocation) {
	left := map[uuid.UUID]uint32{}
	for p := range h.held {
		maps.Copy(left, h.left(p, len(given[p])))
	}
	own := make([]*ent.Allocation, 0, len(h.l.own))
	for _, a := range h.l.own {
		if n, ok := left[a.Id]; ok {
			if n > 0 {
				c := *a
				c.Units = n
				own = append(own, &c)
			}
			continue
		}
		own = append(own, a)
	}
	held := slices.Clone(h.l.held)
	for p, ms := range given {
		for _, m := range ms {
			held = append(held, &ent.Allocation{ResourceId: m, BeginsAt: h.from, EndsAt: h.end[p], Units: 1})
		}
	}
	return own, held
}

// Pickup answers what the pickup of a reservation is to hand out of each pool
// it booked: of the pool's units free until it ends, in the order a person
// reads them, as many as it holds, and the others any of which may be handed
// out instead -- any one that fits on its own (D46). Not one with somebody:
// it is not here to be handed out.
func (s domainReservation) Pickup(ctx context.Context, req *app.ReservationPickupRequest) (*app.ReservationPickupResponse, error) {
	t, err := s.read(ctx)
	if err != nil {
		return nil, err
	}
	v, err := t.next.Reservation().Get(ctx, app.ReservationGetRequest_builder{Ref: req.GetRef()}.Build())
	if err != nil {
		return nil, err
	}
	r, err := t.db.Reservation.Query().Where(reservation.Id(uuidOf(v.GetId())), reservation.TenantId(t.tenant.Uuid())).Only(ctx)
	if err != nil {
		return nil, err
	}
	out := app.ReservationPickupResponse_builder{}.Build()
	if !waits(r.Status) || !r.EndsAt.After(t.now) {
		// Handed out already, or over: nothing to pick up.
		return out, nil
	}
	h, err := t.handoutOf(r)
	if err != nil {
		return nil, err
	}
	for _, pr := range h.pools {
		pool := pr.asset.Id
		v := app.PoolPickup_builder{PoolId: pdid.Id(pool).Bytes(), Units: h.units(pool)}.Build()
		as, err := t.db.Asset.Query().Where(asset.TenantId(t.tenant.Uuid()), asset.IdIn(h.l.units[pool]...), asset.DateErasedIsNil()).All(ctx)
		if err != nil {
			return nil, err
		}
		// One being repaired after one that is not (D41 counts it, and only
		// its work order's time keeps it), and each by its tag.
		repaired := func(a *ent.Asset) int {
			if a.Status == "in_repair" {
				return 1
			}
			return 0
		}
		slices.SortFunc(as, func(a, b *ent.Asset) int {
			return cmp.Or(cmp.Compare(repaired(a), repaired(b)), strings.Compare(a.Tag, b.Tag), a.Id.Compare(b.Id))
		})
		as = slices.DeleteFunc(as, func(a *ent.Asset) bool { return a.CustodianId != (uuid.UUID{}) })
		given := map[uuid.UUID][]uuid.UUID{}
		for _, p := range h.pools {
			if p.asset.Id != pool {
				// What it is offered of the pools before this one.
				given[p.asset.Id] = slices.Clone(h.offered(p.asset.Id, out))
			}
		}
		for _, a := range as {
			if len(given[pool]) < int(v.GetUnits()) && h.fits(pool, a.Id, given) {
				given[pool] = append(given[pool], a.Id)
				v.SetOffered(append(v.GetOffered(), pdid.Id(a.Id).Bytes()))
			}
		}
		alone := map[uuid.UUID][]uuid.UUID{}
		for _, a := range as {
			if !slices.Contains(given[pool], a.Id) && h.fits(pool, a.Id, alone) {
				v.SetOthers(append(v.GetOthers(), pdid.Id(a.Id).Bytes()))
			}
		}
		out.SetPools(append(out.GetPools(), v))
	}
	return out, nil
}

// offered answers what `out` offers of `pool` so far.
func (h *handout) offered(pool uuid.UUID, out *app.ReservationPickupResponse) []uuid.UUID {
	vs := []uuid.UUID{}
	for _, p := range out.GetPools() {
		if uuidOf(p.GetPoolId()) == pool {
			for _, b := range p.GetOffered() {
				vs = append(vs, uuidOf(b))
			}
		}
	}
	return vs
}

// handOver makes what the pickup of `r` hands out of the pools it booked its
// own: of `things`, what a pickup hands over, each that is a unit of a pool it
// booked -- in their order, to the first such pool it holds fewer of than it
// booked, or else the first -- an allocation of the unit's for `r` from now
// until what it held of the pool ends, and what it held of the pool as many
// fewer. One the pools around it cannot do without then, or that something
// else takes then, is refused, and nothing is handed out.
//
// The caller holds what the reservation holds, and what is handed out, and the
// pools around them ([Tx.lockAllocations]).
func (t *Tx) handOver(r *ent.Reservation, things []uuid.UUID) error {
	h, err := t.handoutOf(r)
	if err != nil || len(h.pools) == 0 {
		return err
	}
	given := map[uuid.UUID][]uuid.UUID{}
	for _, m := range things {
		ps := h.poolsOf(m)
		if len(ps) == 0 {
			continue
		}
		pr := ps[0]
		for _, p := range ps {
			if len(given[p.asset.Id]) < int(h.units(p.asset.Id)) {
				pr = p
				break
			}
		}
		if !h.fits(pr.asset.Id, m, given) {
			a, err := t.db.Asset.Query().Where(asset.Id(m), asset.TenantId(t.tenant.Uuid())).Only(t.ctx)
			if err != nil {
				return err
			}
			return failed("%s은(는) %s까지 다른 예약이나 정비가 잡고 있어 이 예약으로 건넬 수 없습니다. %s에서 다른 것을 고르세요",
				tagged(a), t.when(h.end[pr.asset.Id]), tagged(pr.asset))
		}
		given[pr.asset.Id] = append(given[pr.asset.Id], m)
	}

	for _, pr := range h.pools {
		pool := pr.asset.Id
		ms := given[pool]
		if len(ms) == 0 {
			continue
		}
		for _, m := range ms {
			mr, err := t.member(m, pr.book.Timezone)
			if err != nil {
				return err
			}
			n, p, err := t.capacity(mr)
			if err != nil {
				return err
			}
			if _, err := t.next.Allocation().Add(t.ctx, app.AllocationAddRequest_builder{
				Tenant:    t.tenantRef(),
				Resource:  assetRef(m),
				Kind:      "reservation",
				RefId:     pdid.Id(r.Id).Bytes(),
				BeginsAt:  ts(t.now),
				EndsAt:    ts(h.end[pool]),
				Blocking:  true,
				Exclusive: p == nil && n <= 1,
				Units:     1,
			}.Build()); err != nil {
				return err
			}
		}
		// What it held of the pool, as many fewer: and what it is handed
		// beyond that -- or of a pool it waits for -- the pools could spare.
		left := h.left(pool, len(ms))
		for _, pa := range h.held[pool] {
			n := left[pa.Id]
			if n == pa.Units {
				continue
			}
			ref := app.AllocationRef_builder{Id: pdid.Id(pa.Id).Bytes()}.Build()
			switch {
			case n > 0:
				_, err = t.next.Allocation().Patch(t.ctx, app.AllocationPatchRequest_builder{Ref: ref, Units: z.Ptr(n), DateUpdatedForce: z.Ptr(true)}.Build())
			case pa.BeginsAt.Before(t.now):
				// What it held of the pool until now, it held.
				_, err = t.next.Allocation().Patch(t.ctx, app.AllocationPatchRequest_builder{Ref: ref, EndsAt: ts(t.now), DateUpdatedForce: z.Ptr(true)}.Build())
			default:
				_, err = t.next.Allocation().Erase(t.ctx, ref)
			}
			if err != nil {
				return err
			}
		}
	}
	return nil
}
