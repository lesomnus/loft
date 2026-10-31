package domain

import (
	"slices"
	"time"
	"uuid"

	"github.com/lesomnus/loft/internal/ent"
	"github.com/lesomnus/loft/internal/ent/asset"
	"github.com/lesomnus/loft/internal/ent/bookable"
	"github.com/lesomnus/loft/internal/ent/link"
)

// A thing may be a member of more than one pool: a camera in "카메라" and in
// "촬영 장비". It is one of the units of each, and one unit to one of them at a
// time -- what one of them promises of it, the other does not have then. So a
// pool is never counted alone (2026-10-26, D44): a count of it reads the
// pools around it ([Tx.circle]) -- every pool it shares a member with, and
// every pool those share one with -- and asks whether all of them can give
// what they are booked for at once, each unit to one of them ([ledger]). A
// booking of any of them, of a thing that is a unit of any of them, a change
// to what any of them is made of and a recheck of them all read the same
// pools, and so all hold their schedules, in the one order schedules are taken
// in -- as a booking of a pool holds the pool's and a booking of its member
// holds the pool's too (D41).

// circle answers the reservable pools around these things, in identifier
// order: each of them that is one, each one any of them is a member of, and
// each pool any of those shares a member with, on and on. What a count of any
// of them reads ([Tx.ledgerOf]), and so what everything that books or changes
// one of them holds.
//
// A member is any thing whose membership is open, in use or not: whether it is
// in use is what a count asks of it, and what holds the pools around it is the
// same either way, so that a change of its status does not change them.
func (t *Tx) circle(ids []uuid.UUID) ([]*resource, error) {
	tid := t.tenant.Uuid()
	pools := map[uuid.UUID]*resource{}
	seen := map[uuid.UUID]bool{}
	things := slices.Clone(ids)
	for _, v := range things {
		seen[v] = true
	}
	for len(things) > 0 {
		// The things themselves, and what they are members of.
		ls, err := t.db.Link.Query().Where(
			link.TenantId(tid),
			link.SourceIdIn(things...),
			link.Kind("member_of"),
			link.SupersededAtIsNil(),
			link.ValidToIsNil(),
		).All(t.ctx)
		if err != nil {
			return nil, err
		}
		maybe := slices.Clone(things)
		for _, l := range ls {
			maybe = append(maybe, l.TargetId)
		}
		maybe = slices.DeleteFunc(maybe, func(v uuid.UUID) bool { return pools[v] != nil })
		if len(maybe) == 0 {
			break
		}
		groups, err := t.db.Asset.Query().Where(asset.TenantId(tid), asset.IdIn(maybe...), asset.Kind("group"), asset.DateErasedIsNil()).All(t.ctx)
		if err != nil {
			return nil, err
		}
		if len(groups) == 0 {
			break
		}
		gs := make([]uuid.UUID, 0, len(groups))
		for _, g := range groups {
			gs = append(gs, g.Id)
		}
		bs, err := t.db.Bookable.Query().Where(bookable.TenantId(tid), bookable.AssetIdIn(gs...), bookable.DateErasedIsNil()).All(t.ctx)
		if err != nil {
			return nil, err
		}
		fresh := []uuid.UUID{}
		for _, b := range bs {
			for _, g := range groups {
				if g.Id == b.AssetId && pools[g.Id] == nil {
					pools[g.Id] = &resource{asset: g, book: b}
					fresh = append(fresh, g.Id)
				}
			}
		}
		if len(fresh) == 0 {
			break
		}
		// And their members, which may be units of others.
		ms, err := t.db.Link.Query().Where(
			link.TenantId(tid),
			link.TargetIdIn(fresh...),
			link.Kind("member_of"),
			link.SupersededAtIsNil(),
			link.ValidToIsNil(),
		).All(t.ctx)
		if err != nil {
			return nil, err
		}
		things = []uuid.UUID{}
		for _, l := range ms {
			if !seen[l.SourceId] {
				seen[l.SourceId] = true
				things = append(things, l.SourceId)
			}
		}
	}
	out := make([]*resource, 0, len(pools))
	for _, r := range pools {
		out = append(out, r)
	}
	slices.SortFunc(out, func(a, b *resource) int { return a.asset.Id.Compare(b.asset.Id) })
	return out, nil
}

// poolsAround answers the pools around `r` ([Tx.circle]), `r` among them
// whether or not it may be reserved itself -- a group a kit takes with it is
// counted as the pool it is.
func (t *Tx) poolsAround(r *resource) ([]*resource, error) {
	around, err := t.circle([]uuid.UUID{r.asset.Id})
	if err != nil {
		return nil, err
	}
	if !slices.ContainsFunc(around, func(v *resource) bool { return v.asset.Id == r.asset.Id }) {
		around = append(around, r)
	}
	return around, nil
}

// ledger is what some pools have to give over a stretch of time, and what
// they are asked for then ([Tx.ledgerOf]).
type ledger struct {
	// units is each pool's units, by the pool ([Tx.poolOf]); a pool nothing
	// is a member of has the number that was set instead, in `set`, which
	// it shares with none.
	units map[uuid.UUID][]uuid.UUID
	set   map[uuid.UUID]uint32
	// own is what the pools are booked for -- each allocation by the units
	// it takes of the pool it is of -- and held what takes their units' own
	// time: maintenance, a booking of the unit itself, or of a kit that
	// takes it.
	own, held []*ent.Allocation
}

// ledgerOf reads what these pools have to give and are asked for over
// [from, to), but by the reservations in `except`.
func (t *Tx) ledgerOf(pools []*resource, from, to time.Time, except []uuid.UUID) (*ledger, error) {
	l := &ledger{units: map[uuid.UUID][]uuid.UUID{}, set: map[uuid.UUID]uint32{}}
	ids, members := []uuid.UUID{}, []uuid.UUID{}
	for _, pr := range pools {
		p, err := t.poolOf(pr.asset.Id)
		if err != nil {
			return nil, err
		}
		ids = append(ids, pr.asset.Id)
		if p == nil {
			l.set[pr.asset.Id] = pr.book.Units
			continue
		}
		l.units[pr.asset.Id] = p.units
		members = append(members, p.units...)
	}
	slices.SortFunc(members, func(a, b uuid.UUID) int { return a.Compare(b) })
	members = slices.Compact(members)
	var err error
	if l.own, err = t.heldOf(ids, from, to, except); err != nil {
		return nil, err
	}
	if l.held, err = t.heldOf(members, from, to, except); err != nil {
		return nil, err
	}
	return l, nil
}

// short says whether at some moment of [from, to) the pools cannot give at
// once what `own` asks of them, out of their units whose own time `held`
// leaves free then ([ledger.gives]).
func (l *ledger) short(own, held []*ent.Allocation, from, to time.Time) bool {
	for _, at := range moments(from, to, own, held) {
		asked, busy := asOf(own, held, at)
		if !l.gives(asked, busy) {
			return true
		}
	}
	return false
}

// spare answers how many more of `pool` there are to give at the busiest
// moment of [from, to), beside what `own` asks of the pools then -- every
// other pool given what it is asked first. None when [from, to) is empty, or
// what is asked already does not fit.
func (l *ledger) spare(pool uuid.UUID, own, held []*ent.Allocation, from, to time.Time) uint32 {
	least := -1
	for _, at := range moments(from, to, own, held) {
		asked, busy := asOf(own, held, at)
		if n := l.room(pool, asked, busy); least < 0 || n < least {
			least = n
		}
	}
	return uint32(max(least, 0))
}

// moments answers when each stretch of [from, to) in which nothing of these
// begins or ends begins: what is asked and held is the same all through each.
func moments(from, to time.Time, as ...[]*ent.Allocation) []time.Time {
	if !to.After(from) {
		return nil
	}
	cuts := []time.Time{from}
	for _, vs := range as {
		for _, a := range vs {
			for _, at := range []time.Time{a.BeginsAt, a.EndsAt} {
				if at.After(from) && at.Before(to) {
					cuts = append(cuts, at)
				}
			}
		}
	}
	slices.SortFunc(cuts, func(a, b time.Time) int { return a.Compare(b) })
	return slices.CompactFunc(cuts, func(a, b time.Time) bool { return a.Equal(b) })
}

// asOf answers how many units of each pool `own` asks for at `at`, and which
// things `held` takes the own time of then.
func asOf(own, held []*ent.Allocation, at time.Time) (map[uuid.UUID]int, map[uuid.UUID]bool) {
	asked := map[uuid.UUID]int{}
	for _, a := range own {
		if !a.BeginsAt.After(at) && a.EndsAt.After(at) {
			asked[a.ResourceId] += int(a.Units)
		}
	}
	busy := map[uuid.UUID]bool{}
	for _, a := range held {
		if !a.BeginsAt.After(at) && a.EndsAt.After(at) {
			busy[a.ResourceId] = true
		}
	}
	return asked, busy
}

// gives says whether the pools can give at once what is asked of them, out
// of their units that are not busy: a pool nothing is a member of as many as
// it was set to have, and the rest each unit to one pool it is of -- however
// they share them.
func (l *ledger) gives(asked map[uuid.UUID]int, busy map[uuid.UUID]bool) bool {
	shared := 0
	for p, n := range asked {
		if n <= 0 {
			continue
		}
		if c, ok := l.set[p]; ok {
			if n > int(c) {
				return false
			}
			continue
		}
		if n > len(free(l.units[p], busy)) {
			return false
		}
		shared++
	}
	// One pool asked: what it has free is all there is to it.
	return shared <= 1 || l.match(asked, busy, uuid.UUID{}, 0) >= 0
}

// room answers how many more of `pool` there are to give, beside what is
// asked of every pool, given what it is asked first.
func (l *ledger) room(pool uuid.UUID, asked map[uuid.UUID]int, busy map[uuid.UUID]bool) int {
	if !l.gives(asked, busy) {
		return 0
	}
	if c, ok := l.set[pool]; ok {
		return max(int(c)-asked[pool], 0)
	}
	n := len(free(l.units[pool], busy)) - asked[pool]
	if n <= 0 {
		return 0
	}
	return max(l.match(asked, busy, pool, n), 0)
}

// free answers which of `units` are not busy.
func free(units []uuid.UUID, busy map[uuid.UUID]bool) []uuid.UUID {
	return slices.DeleteFunc(slices.Clone(units), func(v uuid.UUID) bool { return busy[v] })
}

// match gives each unit asked of a pool one of the pool's units that is not
// busy, no unit twice -- moving one already given to another of its pools
// where that makes room -- and then up to `more` more of `extra`'s. It
// answers how many of those more it gave, or -1 when what is asked does not
// fit.
func (l *ledger) match(asked map[uuid.UUID]int, busy map[uuid.UUID]bool, extra uuid.UUID, more int) int {
	pools := make([]uuid.UUID, 0, len(asked))
	for p, n := range asked {
		if _, ok := l.set[p]; !ok && n > 0 {
			pools = append(pools, p)
		}
	}
	slices.SortFunc(pools, func(a, b uuid.UUID) int { return a.Compare(b) })

	// A slot is a unit asked of a pool, and owner the slot each unit is
	// given to.
	slots := []uuid.UUID{}
	owner := map[uuid.UUID]int{}
	var give func(s int, seen map[uuid.UUID]bool) bool
	give = func(s int, seen map[uuid.UUID]bool) bool {
		for _, m := range l.units[slots[s]] {
			if busy[m] || seen[m] {
				continue
			}
			seen[m] = true
			if o, ok := owner[m]; !ok || give(o, seen) {
				owner[m] = s
				return true
			}
		}
		return false
	}
	for _, p := range pools {
		for range asked[p] {
			slots = append(slots, p)
			if !give(len(slots)-1, map[uuid.UUID]bool{}) {
				return -1
			}
		}
	}
	n := 0
	for ; n < more; n++ {
		slots = append(slots, extra)
		if !give(len(slots)-1, map[uuid.UUID]bool{}) {
			break
		}
	}
	return n
}
