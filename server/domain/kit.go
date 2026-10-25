package domain

import (
	"fmt"
	"slices"
	"strings"
	"time"
	"uuid"

	"github.com/lesomnus/payday/past"
	"github.com/lesomnus/payday/pdid"
	"github.com/lesomnus/z"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	app "github.com/lesomnus/rove"
	"github.com/lesomnus/rove/internal/ent"
	"github.com/lesomnus/rove/internal/ent/allocation"
	"github.com/lesomnus/rove/internal/ent/asset"
	"github.com/lesomnus/rove/internal/ent/bookable"
	"github.com/lesomnus/rove/internal/ent/link"
	"github.com/lesomnus/rove/internal/ent/reservation"
	"github.com/lesomnus/rove/internal/ent/reservationitem"
)

// A kit is what its `member_of` links say it is made of, and a reservation of
// it takes the members those links say it needs, an allocation for each
// (design 4). What a kit is made of changes after it was booked -- a member
// given to it, taken off it, made optional or needed, a link said never to
// have held, a member voided -- and what is here keeps the reservations it
// has in step: in the transaction that changed it, the reservations of the
// kit still to be fulfilled take what it was given and let go of what it no
// longer needs ([Tx.recheckKits]).
//
// A member something else holds then is not taken from it, and does not
// stop the change. The reservation waits for it instead, by an allocation
// that blocks nothing ([allocWaiting]), whoever has to settle it is told, and
// the reservation shows it wherever it is acted on. It takes the member once
// nothing holds it any more ([Tx.take]): in the transaction that let it go,
// when that held the member's schedule, and in the sweep otherwise.
//
// What each of them did to what a reservation holds is on the history of
// both, the reservation and the member, as events of their own ([Tx.changed],
// design D39).

// allocWaiting is the allocation of a reservation that waits for a member of
// its kit: one the kit was given after the reservation was made, which
// something else held then. It blocks nothing, since the time is somebody
// else's -- and the exclusion that keeps two blocking allocations of a thing
// apart (`cli/pg.sql`) would refuse it -- and it is made the reservation's
// own once the member is free ([Tx.take]). It says nothing once the
// reservation has been fulfilled or has ended: what is read of it is what a
// reservation still to be fulfilled waits for.
const allocWaiting = "waiting"

// waits says whether a reservation in `state` is still to be fulfilled: it
// takes time, and nothing has been picked up or checked into yet. What a
// change to a kit does to the kit's reservations is done to these.
func waits(state string) bool {
	switch state {
	case resHeld, resRequested, resConfirmed:
		return true
	}
	return false
}

// kitMembers answers the members a reservation of a kit takes with it: the
// ones its links say it needs, now, in identifier order.
func (t *Tx) kitMembers(kit uuid.UUID) ([]uuid.UUID, error) {
	ls, err := t.db.Link.Query().Where(
		link.TenantId(t.tenant.Uuid()),
		link.TargetId(kit),
		link.Kind("member_of"),
		link.Required(true),
		link.SupersededAtIsNil(),
		link.ValidToIsNil(),
	).All(t.ctx)
	if err != nil {
		return nil, err
	}
	out := make([]uuid.UUID, 0, len(ls))
	for _, l := range ls {
		out = append(out, l.SourceId)
	}
	slices.SortFunc(out, func(a, b uuid.UUID) int { return a.Compare(b) })
	return slices.Compact(out), nil
}

// member is one of a kit's members as a reservation takes it: by its own
// rules where it may be reserved, and otherwise as one unit with no buffers,
// in `zone` -- by an allocation of its own either way.
func (t *Tx) member(id uuid.UUID, zone string) (*resource, error) {
	r, err := t.resourceOf(id)
	if err == nil {
		return r, nil
	}
	a, aerr := t.db.Asset.Query().Where(asset.Id(id), asset.TenantId(t.tenant.Uuid())).Only(t.ctx)
	if aerr != nil {
		return nil, err
	}
	return &resource{asset: a, book: &ent.Bookable{AssetId: a.Id, Units: 1, Timezone: zone}}, nil
}

// holding says whether this transaction holds the schedule of `r`
// ([Tx.lockBookables]), which every booking of it takes before it looks at
// what is booked. A member that has none -- nobody may reserve it on its own
// -- is only ever taken with a kit, under the kit's.
func (t *Tx) holding(r *resource) bool {
	if r.book.Id == (uuid.UUID{}) {
		return true
	}
	return t.shared != nil && t.shared.schedules[r.book.AssetId]
}

// lockSchedules holds the schedules of these assets, the ones that have one,
// in the order every booking takes them in ([Tx.lockBookables]).
func (t *Tx) lockSchedules(ids []uuid.UUID) error {
	if len(ids) == 0 {
		return nil
	}
	bs, err := t.db.Bookable.Query().Where(bookable.TenantId(t.tenant.Uuid()), bookable.AssetIdIn(ids...)).All(t.ctx)
	if err != nil {
		return err
	}
	rs := make([]*resource, 0, len(bs))
	for _, b := range bs {
		rs = append(rs, &resource{book: b})
	}
	return t.lockBookables(rs)
}

// kitStep is what bringing one reservation in step with its kits does.
type kitStep struct {
	r *ent.Reservation
	// override is a reservation booked over what was there, on purpose: what
	// it takes it takes as it took the rest, blocking nothing.
	override bool
	// take is each member of a kit being rechecked that it holds nothing of,
	// with the kit.
	take [][2]uuid.UUID
	// release is what it holds of anything none of its kits needs now.
	release []*ent.Allocation
	// waiting is what it waits for that a kit being rechecked still needs.
	waiting []*ent.Allocation
}

// kitPlan is what bringing the reservations of some kits in step does: what
// each kit is made of now, and a step for each reservation still to be
// fulfilled.
type kitPlan struct {
	members map[uuid.UUID][]uuid.UUID
	steps   []kitStep
}

// schedules is every asset whose schedule the plan holds: the kits, all each
// is made of -- which a reservation booked while this looked takes as well --
// and what it lets go of.
func (p kitPlan) schedules() []uuid.UUID {
	ids := []uuid.UUID{}
	for k, ms := range p.members {
		ids = append(ids, k)
		ids = append(ids, ms...)
	}
	for _, s := range p.steps {
		for _, a := range s.release {
			ids = append(ids, a.ResourceId)
		}
	}
	slices.SortFunc(ids, func(a, b uuid.UUID) int { return a.Compare(b) })
	return slices.Compact(ids)
}

// planKits reads what bringing the reservations of `kits` in step with them
// would do now.
//
// A reservation is one still to be fulfilled ([waits]) and not over. A
// confirmed one that has begun and nobody has picked up yet is one: what is
// handed out then is the kit as it is then, so it takes a member from now on.
// One in use is not: it was handed out, or checked into, as the kit was then
// -- a member taken off a kit that is out stays taken until it comes back,
// and a member given to it then was not handed out with it.
func (t *Tx) planKits(kits map[uuid.UUID]*ent.Bookable) (kitPlan, error) {
	tid := t.tenant.Uuid()
	p := kitPlan{members: map[uuid.UUID][]uuid.UUID{}}
	ids := make([]uuid.UUID, 0, len(kits))
	for k := range kits {
		ms, err := t.kitMembers(k)
		if err != nil {
			return p, err
		}
		p.members[k] = ms
		ids = append(ids, k)
	}

	// Its reservations, by what they hold of it: every reservation holds an
	// allocation of each thing it booked.
	held, err := t.db.Allocation.Query().Where(
		allocation.TenantId(tid),
		allocation.ResourceIdIn(ids...),
		allocation.RefIdNotNil(),
		allocation.EndsAtGT(t.now),
	).All(t.ctx)
	if err != nil {
		return p, err
	}
	refs := []uuid.UUID{}
	for _, a := range held {
		refs = append(refs, *a.RefId)
	}
	if len(refs) == 0 {
		return p, nil
	}
	rs, err := t.db.Reservation.Query().Where(
		reservation.TenantId(tid),
		reservation.IdIn(refs...),
		reservation.StatusIn(resHeld, resRequested, resConfirmed),
		reservation.EndsAtGT(t.now),
		reservation.DateErasedIsNil(),
	).Order(reservation.ByBeginsAt(), reservation.ById()).All(t.ctx)
	if err != nil || len(rs) == 0 {
		return p, err
	}
	rids := make([]uuid.UUID, 0, len(rs))
	for _, r := range rs {
		rids = append(rids, r.Id)
	}

	// What each booked, and which of that are kits: what it needs is what
	// it booked and all its kits need, of the other kits as they are too.
	items, err := t.db.ReservationItem.Query().Where(reservationitem.TenantId(tid), reservationitem.ReservationIdIn(rids...)).All(t.ctx)
	if err != nil {
		return p, err
	}
	booked := map[uuid.UUID][]uuid.UUID{}
	things := []uuid.UUID{}
	for _, it := range items {
		booked[it.ReservationId] = append(booked[it.ReservationId], it.ResourceId)
		things = append(things, it.ResourceId)
	}
	others, err := t.db.Asset.Query().Where(asset.TenantId(tid), asset.IdIn(things...), asset.Kind("kit")).Ids(t.ctx)
	if err != nil {
		return p, err
	}
	needs := map[uuid.UUID][]uuid.UUID{}
	for k, ms := range p.members {
		needs[k] = ms
	}
	for _, k := range others {
		if _, ok := needs[k]; ok {
			continue
		}
		ms, err := t.kitMembers(k)
		if err != nil {
			return p, err
		}
		needs[k] = ms
	}

	as, err := t.db.Allocation.Query().Where(allocation.TenantId(tid), allocation.RefIdIn(rids...)).
		Order(allocation.ByDateCreated(), allocation.ById()).All(t.ctx)
	if err != nil {
		return p, err
	}
	of := map[uuid.UUID][]*ent.Allocation{}
	for _, a := range as {
		of[*a.RefId] = append(of[*a.RefId], a)
	}

	for _, r := range rs {
		s := kitStep{r: r}
		wanted := map[uuid.UUID]bool{}
		recheck := map[uuid.UUID]uuid.UUID{}
		for _, b := range booked[r.Id] {
			wanted[b] = true
			for _, m := range needs[b] {
				wanted[m] = true
			}
			if _, ok := kits[b]; ok {
				for _, m := range p.members[b] {
					if _, ok := recheck[m]; !ok {
						recheck[m] = b
					}
				}
			}
		}
		have := map[uuid.UUID]bool{}
		for _, a := range of[r.Id] {
			have[a.ResourceId] = true
			switch {
			case !wanted[a.ResourceId]:
				s.release = append(s.release, a)
			case a.Kind == allocWaiting:
				if _, ok := recheck[a.ResourceId]; ok {
					s.waiting = append(s.waiting, a)
				}
			case a.Kind == "override":
				s.override = true
			}
		}
		for _, b := range booked[r.Id] {
			if _, ok := kits[b]; !ok {
				continue
			}
			for _, m := range p.members[b] {
				if !have[m] && recheck[m] == b {
					s.take = append(s.take, [2]uuid.UUID{m, b})
					have[m] = true
				}
			}
		}
		if len(s.take) > 0 || len(s.release) > 0 || len(s.waiting) > 0 {
			p.steps = append(p.steps, s)
		}
	}
	return p, nil
}

// reservableKits answers the kits of these that may be reserved, by the
// asset: the ones that can have reservations.
func (t *Tx) reservableKits(ids []uuid.UUID) (map[uuid.UUID]*ent.Bookable, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	ks, err := t.db.Asset.Query().Where(asset.TenantId(t.tenant.Uuid()), asset.IdIn(ids...), asset.Kind("kit")).Ids(t.ctx)
	if err != nil || len(ks) == 0 {
		return nil, err
	}
	bs, err := t.db.Bookable.Query().Where(bookable.TenantId(t.tenant.Uuid()), bookable.AssetIdIn(ks...)).All(t.ctx)
	if err != nil {
		return nil, err
	}
	kits := map[uuid.UUID]*ent.Bookable{}
	for _, b := range bs {
		kits[b.AssetId] = b
	}
	return kits, nil
}

// kitHolds is every schedule [Tx.recheckKits] holds of these kits, by the
// asset ([kitPlan.schedules]), and those of the pools what it holds are units
// of: taking a member is held to them ([Tx.poolsShort]), and what it lets go
// of is theirs to give then ([Tx.heal]).
func (t *Tx) kitHolds(kits map[uuid.UUID]*ent.Bookable) ([]uuid.UUID, error) {
	before, err := t.planKits(kits)
	if err != nil {
		return nil, err
	}
	hold := before.schedules()
	pools, err := t.poolSchedules(hold)
	if err != nil {
		return nil, err
	}
	return append(hold, pools...), nil
}

// recheckKits brings the reservations of these kits in step with what each
// is made of now, in the transaction that changed it (design 4): each one
// still to be fulfilled takes a member its kit was given since it was made,
// from now on where it has begun, and lets go of one none of its kits needs
// any more ([Tx.planKits] says which).
//
// It holds each kit's schedule before it reads what is booked, which a
// booking of the kit holds before it reads what the kit is made of for good
// (domainReservation.Add): a booking that went first is read here, and one
// that goes after reads the kit as this left it. And it holds the schedule of
// every member of the kit and of what it lets go of, which a booking of the
// member holds as it looks at what the member is booked for, and those of the
// pools the members are units of, which taking one is held to (pool.go) --
// all of them at once, in the one order everything takes schedules in, since
// a booking that went first while this waited needs any of them. It takes no
// reservation's row: every change of a reservation that lets its time go
// holds the schedule of what it booked first, its kit's among them
// ([Tx.lockAllocations]).
func (t *Tx) recheckKits(ids ...uuid.UUID) error {
	kits, err := t.reservableKits(ids)
	if err != nil || len(kits) == 0 {
		return err
	}
	hold, err := t.kitHolds(kits)
	if err != nil {
		return err
	}
	if err := t.lockSchedules(hold); err != nil {
		return err
	}
	// Read again, now that nothing changes it.
	p, err := t.planKits(kits)
	if err != nil {
		return err
	}

	told := []kitWait{}
	// What it did to each reservation, an event of the reservation's and
	// one of the member's, all of them children of the change, written at
	// once.
	did := []allocChange{}
	for _, s := range p.steps {
		for _, k := range s.take {
			m, err := t.member(k[0], kits[k[1]].Timezone)
			if err != nil {
				// Not there to take.
				continue
			}
			from, to := window(m.book, s.r.BeginsAt, s.r.EndsAt)
			if from.Before(t.now) {
				from = t.now
			}
			if !to.After(from) {
				continue
			}
			row := app.AllocationAddRequest_builder{
				Tenant:    t.tenantRef(),
				Resource:  assetRef(m.asset.Id),
				Kind:      "reservation",
				RefId:     pdid.Id(s.r.Id).Bytes(),
				BeginsAt:  ts(from),
				EndsAt:    ts(to),
				Blocking:  true,
				Exclusive: m.book.Units <= 1,
				Units:     1,
			}.Build()
			pooled, err := t.holdsPools(m)
			if err != nil {
				return err
			}
			kind := "reservation.take"
			switch {
			case s.override:
				row.SetKind("override")
				row.SetBlocking(false)
			case !t.holding(m) || !pooled:
				// A member the kit was given by another change while this
				// waited, which that change took for the bookings it saw --
				// or one a pool was given meanwhile: what it is booked for is
				// not this transaction's to know for good, so this one waits,
				// and the sweep takes it once it is free.
				row.SetKind(allocWaiting)
				row.SetBlocking(false)
				kind = "reservation.wait"
			default:
				cs, err := t.conflicts(m, from, to, 1, []uuid.UUID{s.r.Id})
				if err != nil {
					return err
				}
				if len(cs) > 0 {
					row.SetKind(allocWaiting)
					row.SetBlocking(false)
					kind = "reservation.wait"
					told = append(told, kitWait{r: s.r, kit: k[1], member: m, from: from})
				}
			}
			if _, err := t.next.Allocation().Add(t.ctx, row); err != nil {
				return err
			}
			did = append(did, allocChange{kind: kind, r: s.r, thing: m.asset, override: s.override})
		}
		freed := []*ent.Allocation{}
		for _, a := range s.release {
			if _, err := t.next.Allocation().Erase(t.ctx, app.AllocationRef_builder{Id: pdid.Id(a.Id).Bytes()}.Build()); err != nil {
				return err
			}
			if a.Blocking {
				freed = append(freed, a)
			}
			// What it let go of, a member voided as well.
			m, err := t.member(a.ResourceId, "")
			if err != nil {
				return err
			}
			did = append(did, allocChange{kind: "reservation.let_go", r: s.r, thing: m.asset})
		}
		// What another kit's reservation waited for, which this one held.
		if err := t.heal(freed); err != nil {
			return err
		}
		for _, w := range s.waiting {
			m, err := t.member(w.ResourceId, "")
			if err != nil || !t.holding(m) {
				continue
			}
			if _, err := t.take(w, false); err != nil {
				return err
			}
		}
	}
	if err := t.changed(did...); err != nil {
		return err
	}
	return t.tellWaiting(told)
}

// allocChange is what an operation did to what a reservation holds of one
// thing it needs: of `kind`, a `reservation.take`, `.wait`, `.resolve` or
// `.let_go` (`rove/past.proto`) -- a member of its kit, which is one thing;
// or a pool's, by how many of it.
type allocChange struct {
	kind     string
	r        *ent.Reservation
	thing    *ent.Asset
	override bool
	units    uint32
}

// said is what the change's events hold: the reservation and the thing, as
// each was called then.
func (c allocChange) said() *app.ReservationAllocationChanged {
	return app.ReservationAllocationChanged_builder{
		Reservation: pdid.Id(c.r.Id).Bytes(),
		Name:        c.r.Name,
		BeginsAt:    ts(c.r.BeginsAt),
		Asset:       pdid.Id(c.thing.Id).Bytes(),
		AssetName:   c.thing.Name,
		Override:    c.override,
		Units:       c.units,
	}.Build()
}

// changed records each of these as an event of the reservation's and one of
// the thing's, the two holding the same, children of the operation in progress
// -- whose cause it is -- a page of them to a statement (design D34, D39). So
// the reservation's history and the thing's each hold what happened to it,
// and the operation's own says what the operation was.
//
// With none in progress, each is an operation of its own ([Tx.settled]).
func (t *Tx) changed(cs ...allocChange) error {
	if len(cs) == 0 {
		return nil
	}
	if t.op == nil {
		for _, c := range cs {
			if err := t.settled(c); err != nil {
				return err
			}
		}
		return nil
	}
	kids := make([]past.Child, 0, 2*len(cs))
	for _, c := range cs {
		v := c.said()
		kids = append(kids,
			past.Child{Event: past.Event{Kind: c.kind, Subject: pdid.Id(c.r.Id), At: t.now, Value: v}},
			past.Child{Event: past.Event{Kind: c.kind, Subject: pdid.Id(c.thing.Id), At: t.now, Value: v}},
		)
	}
	_, err := t.op.Batch(t.ctx, kids)
	return err
}

// settled records `c` as an operation of its own, part of no other: the
// reservation's event, whose child the thing's is -- a wait the sweep settles,
// which is the deployment's own work, as each reservation it lets go of is.
func (t *Tx) settled(c allocChange) error {
	v := c.said()
	if err := t.begin(nil, c.kind, pdid.Id(c.r.Id), t.now, "", v); err != nil {
		return err
	}
	return t.child(c.kind, pdid.Id(c.thing.Id), t.now, "", v, nil)
}

// take makes the allocation a reservation waits for a member with its own,
// once nothing else holds the member then: what the reservation would have
// taken had the member been free when its kit was given it. Of a pool, what
// the reservation booked of it, once the pool has as many to give then
// (pool.go). It answers whether it did, and tells whoever the reservation is
// for.
//
// That it did is a `reservation.resolve` of the reservation's and of the
// member's: children of the operation that let the member go, or -- `own`,
// the sweep's -- an operation of its own ([Tx.changed]).
//
// The caller holds what keeps the two from changing under it: the member's
// schedule, which a booking of the member holds as it looks, and those of the
// pools it is one of the units of ([Tx.holdsPools]); and what a reservation
// holds as it lets its time go -- its row, or the schedule of anything it
// booked ([Tx.lockAllocations]).
func (t *Tx) take(w *ent.Allocation, own bool) (bool, error) {
	if w.RefId == nil {
		return false, nil
	}
	r, err := t.db.Reservation.Query().Where(reservation.Id(*w.RefId), reservation.TenantId(t.tenant.Uuid())).Only(t.ctx)
	if err != nil {
		if ent.IsNotFound(err) {
			return false, nil
		}
		return false, err
	}
	if !waits(r.Status) || !r.EndsAt.After(t.now) {
		return false, nil
	}
	m, err := t.member(w.ResourceId, "")
	if err != nil {
		return false, nil
	}
	// From now on: what it took before now, it did not.
	from := w.BeginsAt
	if from.Before(t.now) {
		from = t.now
	}
	if !w.EndsAt.After(from) {
		return false, nil
	}
	cs, err := t.conflicts(m, from, w.EndsAt, max(w.Units, 1), []uuid.UUID{r.Id})
	if err != nil || len(cs) > 0 {
		return false, err
	}
	n, p, err := t.capacity(m)
	if err != nil {
		return false, err
	}
	if _, err := t.next.Allocation().Patch(t.ctx, app.AllocationPatchRequest_builder{
		Ref:              app.AllocationRef_builder{Id: pdid.Id(w.Id).Bytes()}.Build(),
		Kind:             z.Ptr("reservation"),
		BeginsAt:         ts(from),
		Blocking:         z.Ptr(true),
		Exclusive:        z.Ptr(p == nil && n <= 1),
		DateUpdatedForce: z.Ptr(true),
	}.Build()); err != nil {
		if status.Code(err) == codes.NotFound {
			// Let go of by a change of its kit that went first.
			return false, nil
		}
		return false, err
	}
	took := allocChange{kind: "reservation.resolve", r: r, thing: m.asset}
	counted, err := t.booked(r.Id, m.asset.Id)
	if err != nil {
		return false, err
	}
	if counted {
		// Some of what it booked, of a pool: by how many.
		took.units = max(w.Units, 1)
	}
	if own {
		err = t.settled(took)
	} else {
		err = t.changed(took)
	}
	if err != nil {
		return false, err
	}
	if counted {
		return true, t.notifyParty(r.PartyId, "reservation.resolved", "예약 수량 확보: "+r.Name,
			fmt.Sprintf("기다리던 %s %d개를 이 예약에 잡았습니다", tagged(m.asset), took.units), pdid.Id(r.Id), "/reservations?tab=mine")
	}
	return true, t.notifyParty(r.PartyId, "reservation.resolved", "예약 구성품 확보: "+r.Name,
		fmt.Sprintf("기다리던 %s이(가) 비어 이 예약에 잡았습니다", tagged(m.asset)), pdid.Id(r.Id), "/reservations?tab=mine")
}

// heal gives what these allocations let go of to whatever waits for it then,
// the one that has waited longest first ([Tx.take]): of the things whose
// schedule this transaction holds, which keeps a booking from taking them
// meanwhile -- and the schedules of the pools a thing is one of the units of,
// which it is held to. A thing let go of is one more for each pool it is one
// of the units of to give then, which goes the same way to what waits for the
// pool, when this holds the pool's schedule. What waits for a thing whose
// schedule this does not hold -- a member nobody may reserve on its own -- is
// the sweep's to take ([sweepKits]).
func (t *Tx) heal(freed []*ent.Allocation) error {
	for _, a := range freed {
		if t.shared == nil {
			return nil
		}
		ok := t.shared.schedules[a.ResourceId]
		if m, err := t.member(a.ResourceId, ""); ok && err == nil {
			if ok, err = t.holdsPools(m); err != nil {
				return err
			}
		}
		if ok {
			if err := t.takeWaiting(a.ResourceId, a); err != nil {
				return err
			}
		}
		ps, err := t.poolsOf([]uuid.UUID{a.ResourceId})
		if err != nil {
			return err
		}
		for _, p := range ps[a.ResourceId] {
			if t.holding(p) {
				if err := t.takeWaiting(p.asset.Id, a); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

// takeWaiting takes, for whatever waits for `resource` during what `a` let go
// of, what is free now, the one that has waited longest first ([Tx.take]).
func (t *Tx) takeWaiting(resource uuid.UUID, a *ent.Allocation) error {
	ws, err := t.db.Allocation.Query().Where(
		allocation.TenantId(t.tenant.Uuid()),
		allocation.ResourceId(resource),
		allocation.Kind(allocWaiting),
		allocation.BeginsAtLT(a.EndsAt),
		allocation.EndsAtGT(a.BeginsAt),
		allocation.EndsAtGT(t.now),
	).Order(allocation.ByDateCreated(), allocation.ById()).All(t.ctx)
	if err != nil {
		return err
	}
	for _, w := range ws {
		if w.RefId != nil && a.RefId != nil && *w.RefId == *a.RefId {
			continue
		}
		// Part of what let it go.
		if _, err := t.take(w, false); err != nil {
			return err
		}
	}
	return nil
}

// sweepKits takes, for the reservations that wait for a member of their kit,
// what is free now: what let it go took it already when it held the member's
// schedule, and this takes the rest -- a member nobody may reserve on its own,
// maintenance that finished early or moved. And for the ones that wait for
// some of a pool, what the pool has to give again that nothing gave them: a
// unit of it that a booking of its own let go of (pool.go).
func sweepKits(t *Tx) error {
	ws, err := t.db.Allocation.Query().Where(
		allocation.TenantId(t.tenant.Uuid()),
		allocation.Kind(allocWaiting),
		allocation.EndsAtGT(t.now),
		allocation.RefIdNotNil(),
	).Order(allocation.ByDateCreated(), allocation.ById()).Limit(200).All(t.ctx)
	if err != nil || len(ws) == 0 {
		return err
	}
	refs := []uuid.UUID{}
	for _, w := range ws {
		refs = append(refs, *w.RefId)
	}
	rs, err := t.db.Reservation.Query().Where(
		reservation.TenantId(t.tenant.Uuid()),
		reservation.IdIn(refs...),
		reservation.StatusIn(resHeld, resRequested, resConfirmed),
		reservation.EndsAtGT(t.now),
	).Ids(t.ctx)
	if err != nil || len(rs) == 0 {
		return err
	}

	// What looks free now, read before anything is held: a pass that finds
	// nothing to take holds nothing and writes nothing, however long what
	// waits waits -- a pool's reservations may wait for days. What looks free
	// is read again once it is held, before it is taken ([Tx.take]).
	members := []uuid.UUID{}
	ids := []uuid.UUID{}
	refs = refs[:0]
	for _, w := range ws {
		if !slices.Contains(rs, *w.RefId) {
			continue
		}
		free, err := t.free(w)
		if err != nil {
			return err
		}
		if free {
			members = append(members, w.ResourceId)
			ids = append(ids, w.Id)
			refs = append(refs, *w.RefId)
		}
	}
	if len(ids) == 0 {
		return nil
	}

	// Each reservation held, then each member's schedule: the order every
	// operation that takes both takes them in ([Tx.lockReservations]).
	rids := make([]pdid.Id, 0, len(refs))
	for _, r := range refs {
		rids = append(rids, pdid.Id(r))
	}
	if err := t.lockReservations(rids...); err != nil {
		return err
	}
	// With the pools they are units of, which taking one is held to.
	pools, err := t.poolSchedules(members)
	if err != nil {
		return err
	}
	if err := t.lockSchedules(append(members, pools...)); err != nil {
		return err
	}

	// What still waits, now that nothing changes it.
	ws, err = t.db.Allocation.Query().Where(allocation.TenantId(t.tenant.Uuid()), allocation.IdIn(ids...), allocation.Kind(allocWaiting)).
		Order(allocation.ByDateCreated(), allocation.ById()).All(t.ctx)
	if err != nil {
		return err
	}
	// Each an operation of its own, the deployment's, as each reservation
	// the sweeps let go of is: part of nothing else that happened.
	for _, w := range ws {
		if _, err := t.take(w, true); err != nil {
			return err
		}
	}
	return nil
}

// free says whether what `w` waits for looks free now, from now on: nothing
// else holds the member then, or the pool has as many to give then. Read as
// it is, holding nothing.
func (t *Tx) free(w *ent.Allocation) (bool, error) {
	m, err := t.member(w.ResourceId, "")
	if err != nil {
		return false, nil
	}
	from := w.BeginsAt
	if from.Before(t.now) {
		from = t.now
	}
	if !w.EndsAt.After(from) {
		return false, nil
	}
	cs, err := t.conflicts(m, from, w.EndsAt, max(w.Units, 1), []uuid.UUID{*w.RefId})
	return len(cs) == 0, err
}

// kitWait is a member a reservation of a kit waits for, as it is told.
type kitWait struct {
	r      *ent.Reservation
	kit    uuid.UUID
	member *resource
	from   time.Time
}

// tellWaiting tells who has to settle it that reservations of a kit wait for
// members the kit was given: whoever runs the kits, once for each kit, and
// whoever each reservation is for, once for theirs -- by mail as well
// ([Mailed]), since it is to be settled before the reservation begins.
func (t *Tx) tellWaiting(ws []kitWait) error {
	if len(ws) == 0 {
		return nil
	}
	byKit := map[uuid.UUID][]kitWait{}
	order := []uuid.UUID{}
	for _, w := range ws {
		if _, ok := byKit[w.kit]; !ok {
			order = append(order, w.kit)
		}
		byKit[w.kit] = append(byKit[w.kit], w)
	}
	for _, k := range order {
		vs := byKit[k]
		kit, err := t.db.Asset.Query().Where(asset.Id(k), asset.TenantId(t.tenant.Uuid())).Only(t.ctx)
		if err != nil {
			return err
		}
		if err := t.notifyManagers("reservation.conflict", "키트 구성품 충돌: "+tagged(kit),
			fmt.Sprintf("%s은(는) 그 시간에 다른 예약이나 정비가 있어 이 키트의 예약 %d건에 잡지 못했습니다. 비면 그 예약이 잡습니다.", waitedFor(vs), len(reservationsOf(vs))),
			pdid.Id(k), "/reservations?tab=all"); err != nil {
			return err
		}

		byParty := map[uuid.UUID][]kitWait{}
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
			title := "예약 구성품 충돌: " + first.r.Name
			if n := len(reservationsOf(pv)); n > 1 {
				title += fmt.Sprintf(" 외 %d건", n-1)
			}
			if err := t.notifyParty(p, "reservation.conflict", title,
				fmt.Sprintf("%s에 더해진 %s은(는) %s에 다른 예약이나 정비가 있어 이 예약에 잡지 못했습니다. 비면 이 예약이 잡습니다.", tagged(kit), waitedFor(pv), t.when(first.from)),
				pdid.Id(first.r.Id), "/reservations?tab=mine"); err != nil {
				return err
			}
		}
	}
	return nil
}

// tagged is an asset as a notice names it: by its tag and its name.
func tagged(a *ent.Asset) string { return strings.TrimSpace(a.Tag + " " + a.Name) }

// waitedFor names the members these wait for, each once.
func waitedFor(ws []kitWait) string {
	names := []string{}
	for _, w := range ws {
		if v := tagged(w.member.asset); !slices.Contains(names, v) {
			names = append(names, v)
		}
	}
	return strings.Join(names, ", ")
}

func reservationsOf(ws []kitWait) []uuid.UUID {
	out := []uuid.UUID{}
	for _, w := range ws {
		if !slices.Contains(out, w.r.Id) {
			out = append(out, w.r.Id)
		}
	}
	return out
}

// waitingOf answers what each of these reservations waits for, of the ones
// still to be fulfilled: each member of a kit it should take and does not
// hold, and what holds the member then. A member nothing holds any more is
// still waited for until it is taken, which is the sweep's ([sweepKits]).
func (t *Tx) waitingOf(rs []*ent.Reservation) (map[uuid.UUID][]*app.ReservationConflict, error) {
	ids := []uuid.UUID{}
	for _, r := range rs {
		if waits(r.Status) && r.EndsAt.After(t.now) {
			ids = append(ids, r.Id)
		}
	}
	if len(ids) == 0 {
		return nil, nil
	}
	ws, err := t.db.Allocation.Query().Where(
		allocation.TenantId(t.tenant.Uuid()),
		allocation.RefIdIn(ids...),
		allocation.Kind(allocWaiting),
	).Order(allocation.ByBeginsAt(), allocation.ById()).All(t.ctx)
	if err != nil {
		return nil, err
	}
	out := map[uuid.UUID][]*app.ReservationConflict{}
	for _, w := range ws {
		m, err := t.member(w.ResourceId, "")
		if err != nil {
			continue
		}
		from := w.BeginsAt
		if from.Before(t.now) {
			from = t.now
		}
		v := app.ReservationConflict_builder{
			ResourceId:   pdid.Id(m.asset.Id).Bytes(),
			ResourceTag:  m.asset.Tag,
			ResourceName: m.asset.Name,
			BeginsAt:     ts(w.BeginsAt),
			EndsAt:       ts(w.EndsAt),
		}.Build()
		if w.EndsAt.After(from) {
			cs, err := t.conflicts(m, from, w.EndsAt, max(w.Units, 1), []uuid.UUID{*w.RefId})
			if err != nil {
				return nil, err
			}
			for _, c := range cs {
				v.SetHeldBy(append(v.GetHeldBy(), t.busy(c, nil)))
			}
		}
		// Some of what it booked, of a pool: how many it waits for and
		// could have then.
		counted, err := t.booked(*w.RefId, m.asset.Id)
		if err != nil {
			return nil, err
		}
		if counted {
			n, err := t.available(w, from, m)
			if err != nil {
				return nil, err
			}
			v.SetUnits(max(w.Units, 1))
			v.SetAvailable(n)
		}
		out[*w.RefId] = append(out[*w.RefId], v)
	}
	return out, nil
}

// busy is an allocation as a calendar shows it: what took the time, by what
// that is called -- a reservation by its name, read off `r` when the caller
// has it.
func (t *Tx) busy(a *ent.Allocation, r *ent.Reservation) *app.BusySpan {
	label := a.Kind
	if r == nil && a.RefId != nil {
		r, _ = t.db.Reservation.Query().Where(reservation.Id(*a.RefId), reservation.TenantId(t.tenant.Uuid())).Only(t.ctx)
	}
	if r != nil {
		label = r.Name
	}
	v := app.BusySpan_builder{
		ResourceId: pdid.Id(a.ResourceId).Bytes(),
		BeginsAt:   ts(a.BeginsAt),
		EndsAt:     ts(a.EndsAt),
		Kind:       a.Kind,
		Units:      a.Units,
		Blocking:   a.Blocking,
		Label:      label,
	}.Build()
	if a.RefId != nil {
		v.SetRefId(pdid.Id(*a.RefId).Bytes())
	}
	return v
}
