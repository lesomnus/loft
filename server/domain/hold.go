package domain

import (
	"bytes"
	"cmp"
	"context"
	"fmt"
	"slices"
	"strings"
	"time"
	"uuid"

	"github.com/lesomnus/payday/pdid"
	"github.com/lesomnus/z"
	"github.com/protobuf-orm/ent/dialect"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	app "github.com/lesomnus/rove"
	"github.com/lesomnus/rove/internal/ent"
	"github.com/lesomnus/rove/internal/ent/allocation"
	"github.com/lesomnus/rove/internal/ent/asset"
	"github.com/lesomnus/rove/internal/ent/bookable"
	"github.com/lesomnus/rove/internal/ent/custody"
	"github.com/lesomnus/rove/internal/ent/custodyline"
	"github.com/lesomnus/rove/internal/ent/reservation"
)

// What somebody has out takes its time (2026-10-26, design 4, D53). A thing
// handed over -- an issue for keeps, a loan due back -- is out from its
// hand-over until it is back, and what is out is not there to be promised to
// anybody else: so the hand-over holds the thing's time, by an allocation of
// its own ([allocCustody]), from the hand-over until a loan is due back and
// the thing's buffer after that, as a booking until then would take it; or,
// of an issue, and of a loan past its due, until it is back ([forever]). What
// a kit needs goes out with it, as a booking of the kit takes it. Everything
// that asks what a thing's time is taken by reads it as it reads a booking's
// or maintenance's: a booking of the thing, of a kit that takes it or of a
// pool it is one of the units of; a change to a kit, which waits for it; what
// a pool's pickup is offered; a calendar.
//
// The hand-over is not refused for what was booked before it: the desk hands
// things out. A reservation still to be fulfilled that took the thing for some
// of that time keeps its time -- it was promised first, and the thing may be
// back before it is wanted -- and says it wherever it is shown ([Tx.outOf],
// `CalendarEntry.out`); whoever has to act on it is told ([Tx.tellOut]); and
// a pool the thing is one of the units of has one fewer to give then, which
// its latest reservations wait for (D41). Once the thing is back, or due back
// sooner, what its hand-over held of the rest is let go of: whatever waits
// for it takes it ([Tx.heal]), and whoever a reservation that was told of it
// is for is told it is not out then any more ([Tx.tellBack]).
//
// The allocation blocks, so that nothing is booked over it, and is not
// exclusive: the exclusion that keeps two blocking allocations of one thing
// apart (`cli/pg.sql`) would refuse it over the reservations it lies over and
// takes nothing from. So it is the domain's to hold to: a hand-over holds what
// a booking of the thing holds before it reads what was booked
// ([Tx.holdThings]), as the booking holds that before it reads what is out.

// allocCustody is the allocation by which a hand-over holds the time of a
// thing it handed over, its `ref_id` the hand-over's. Blocking while the thing
// is out, and not once it is back, when it ends at its return.
const allocCustody = "custody"

// forever is when what is out until it is back is held until: an issue, and a
// loan past its due. Far past anything booked, and a moment each database
// keeps as it is: SQLite keeps one as the nanoseconds since 1970 (payday's
// `_timefmt=unixepoch_nano`), which run out in 2262.
var forever = time.Date(2200, 1, 1, 0, 0, 0, 0, time.UTC)

// takes is the kinds of allocation by which a reservation takes a thing of its
// own: held, or booked over. Not one it waits by, which takes nothing yet.
var takes = []string{"reservation", "override"}

// outSpan is some of a thing's time: what a hand-over took of it, or let go.
type outSpan struct {
	thing    uuid.UUID
	from, to time.Time
}

// heldWith answers what handing these things over holds the time of, in
// identifier order: each of them, and the members a kit needs, which go out
// with it as a booking of it takes them. What a kit is given or loses while it
// is out does not change what went out with it (design 4, D38).
func (t *Tx) heldWith(ids []uuid.UUID) ([]uuid.UUID, error) {
	out := slices.Clone(ids)
	if len(ids) > 0 {
		kits, err := t.db.Asset.Query().Where(asset.TenantId(t.tenant.Uuid()), asset.IdIn(ids...), asset.Kind("kit")).Ids(t.ctx)
		if err != nil {
			return nil, err
		}
		for _, k := range kits {
			ms, err := t.kitMembers(k)
			if err != nil {
				return nil, err
			}
			out = append(out, ms...)
		}
	}
	slices.SortFunc(out, func(a, b uuid.UUID) int { return a.Compare(b) })
	return slices.Compact(out), nil
}

// holdThings holds what a change to what these things hold of their time holds
// before it reads any of it, in the one order everything that takes them takes
// them in -- after the tree, and after the row of the reservation a pickup
// fulfils ([Tx.lockReservations]): each thing's row, which a change of its
// status holds before its schedules ([Tx.holdRow]), and then, at once, what a
// booking of any of them holds as it looks at what is booked
// ([Tx.schedulesOf]) -- its schedule, the kits that take it and the pools
// around it -- with those of `also`, which the caller holds besides: the
// things a pickup's reservation holds.
func (t *Tx) holdThings(things, also []uuid.UUID) error {
	rows := slices.Clone(things)
	slices.SortFunc(rows, func(a, b uuid.UUID) int { return a.Compare(b) })
	for _, v := range slices.Compact(rows) {
		if err := t.holdRow(v); err != nil {
			return err
		}
	}
	hold, err := t.schedulesOf(slices.Concat(things, also))
	if err != nil {
		return err
	}
	return t.lockSchedules(hold)
}

// allocated answers the things a reservation holds an allocation of, of its
// own or waited for: what its pickup takes the time of besides what it hands
// out.
func (t *Tx) allocated(rid pdid.Id) ([]uuid.UUID, error) {
	as, err := t.db.Allocation.Query().Where(allocation.TenantId(t.tenant.Uuid()), allocation.RefId(rid.Uuid())).All(t.ctx)
	if err != nil {
		return nil, err
	}
	out := make([]uuid.UUID, 0, len(as))
	for _, a := range as {
		out = append(out, a.ResourceId)
	}
	return out, nil
}

// heldAs is how custody `c` holds `thing` as it is now: until when -- a
// loan's due and the thing's buffer after it, which a booking of it until then
// would take, or [forever] of an issue and of a loan past its due -- and how
// much of it: all there is, one, or the units of one asset that stands for
// many (thirty folding chairs).
func (t *Tx) heldAs(c *ent.Custody, thing uuid.UUID) (time.Time, uint32, error) {
	b, err := t.db.Bookable.Query().Where(bookable.TenantId(t.tenant.Uuid()), bookable.AssetId(thing), bookable.DateErasedIsNil()).Only(t.ctx)
	if err != nil && !ent.IsNotFound(err) {
		return time.Time{}, 0, err
	}
	end, units := forever, uint32(1)
	if b != nil {
		units = max(b.Units, 1)
	}
	if c.Kind == "loan" && c.DueAt != nil && c.DueAt.After(t.now) {
		end = moment(*c.DueAt)
		if b != nil {
			end = end.Add(time.Duration(b.BufferAfter) * time.Minute)
		}
	}
	return end, units, nil
}

// holdsOf answers what custody `c` holds of each thing now, by the thing: its
// allocations that block, one a thing.
func (t *Tx) holdsOf(c uuid.UUID) ([]*ent.Allocation, error) {
	return t.db.Allocation.Query().Where(
		allocation.TenantId(t.tenant.Uuid()),
		allocation.RefId(c),
		allocation.Kind(allocCustody),
		allocation.Blocking(true),
	).Order(allocation.ByResourceId()).All(t.ctx)
}

// custodyOf is custody `id` as its row is now.
func (t *Tx) custodyOf(id uuid.UUID) (*ent.Custody, error) {
	return t.db.Custody.Query().Where(custody.Id(id), custody.TenantId(t.tenant.Uuid())).Only(t.ctx)
}

// hold makes the allocations by which `c` holds the time of what it handed
// over of these things, and of what goes out with them ([Tx.heldWith]): from
// `from`, when they were handed over, as [Tx.heldAs] says -- what it holds
// already it holds as it does -- and then does what that does to what was
// booked of them ([Tx.taken]). The caller holds what a booking of any of them
// holds ([Tx.holdThings]).
func (t *Tx) hold(c *ent.Custody, things []uuid.UUID, from time.Time) error {
	all, err := t.heldWith(things)
	if err != nil || len(all) == 0 {
		return err
	}
	have, err := t.holdsOf(c.Id)
	if err != nil {
		return err
	}
	took := []outSpan{}
	for _, v := range all {
		if slices.ContainsFunc(have, func(a *ent.Allocation) bool { return a.ResourceId == v }) {
			continue
		}
		end, units, err := t.heldAs(c, v)
		if err != nil {
			return err
		}
		if _, err := t.next.Allocation().Add(t.ctx, app.AllocationAddRequest_builder{
			Tenant:   t.tenantRef(),
			Resource: assetRef(v),
			Kind:     allocCustody,
			RefId:    pdid.Id(c.Id).Bytes(),
			BeginsAt: ts(from),
			EndsAt:   ts(end),
			Blocking: true,
			Units:    units,
		}.Build()); err != nil {
			return err
		}
		took = append(took, outSpan{thing: v, from: later(from, t.now), to: end})
	}
	return t.taken(c, took)
}

// rehold moves the end of what `c` holds of each thing to what [Tx.heldAs]
// says now: later, of a loan due later or past its due; sooner, of one due
// sooner. What it takes then that it did not hold is [Tx.taken], and what it
// lets go of [Tx.released]. The caller holds what a booking of the things
// holds ([Tx.holdThings]).
func (t *Tx) rehold(c *ent.Custody) error {
	as, err := t.holdsOf(c.Id)
	if err != nil {
		return err
	}
	took, let := []outSpan{}, []outSpan{}
	freed := []*ent.Allocation{}
	for _, a := range as {
		end, _, err := t.heldAs(c, a.ResourceId)
		if err != nil {
			return err
		}
		if end.Equal(a.EndsAt) {
			continue
		}
		if _, err := t.next.Allocation().Patch(t.ctx, app.AllocationPatchRequest_builder{
			Ref:              app.AllocationRef_builder{Id: pdid.Id(a.Id).Bytes()}.Build(),
			EndsAt:           ts(end),
			DateUpdatedForce: z.Ptr(true),
		}.Build()); err != nil {
			return err
		}
		if end.After(a.EndsAt) {
			took = append(took, outSpan{thing: a.ResourceId, from: later(a.EndsAt, t.now), to: end})
			continue
		}
		freed = append(freed, a)
		let = append(let, outSpan{thing: a.ResourceId, from: later(end, t.now), to: a.EndsAt})
	}
	if err := t.taken(c, took); err != nil {
		return err
	}
	return t.released(c, freed, let, false)
}

// letGo ends what `c` holds of what came back at `at` -- these things, and of
// a kit, what went out with it -- but of what is still out: the things of its
// lines that are not back, and what goes out with them. What it lets go of is
// [Tx.released]. The caller holds what a booking of what came back holds
// ([Tx.holdThings]).
func (t *Tx) letGo(c *ent.Custody, back, out []uuid.UUID, at time.Time) error {
	gone, err := t.heldWith(back)
	if err != nil || len(gone) == 0 {
		return err
	}
	still, err := t.heldWith(out)
	if err != nil {
		return err
	}
	as, err := t.holdsOf(c.Id)
	if err != nil {
		return err
	}
	freed, let := []*ent.Allocation{}, []outSpan{}
	for _, a := range as {
		if !slices.Contains(gone, a.ResourceId) || slices.Contains(still, a.ResourceId) {
			continue
		}
		// It was out until it came back, and holds no more of the time
		// after: a return said to have been before it went out ends it as
		// it began.
		end := later(at, a.BeginsAt)
		if end.After(a.EndsAt) {
			end = a.EndsAt
		}
		if _, err := t.next.Allocation().Patch(t.ctx, app.AllocationPatchRequest_builder{
			Ref:              app.AllocationRef_builder{Id: pdid.Id(a.Id).Bytes()}.Build(),
			EndsAt:           ts(end),
			Blocking:         z.Ptr(false),
			DateUpdatedForce: z.Ptr(true),
		}.Build()); err != nil {
			return err
		}
		freed = append(freed, a)
		let = append(let, outSpan{thing: a.ResourceId, from: later(end, t.now), to: a.EndsAt})
	}
	return t.released(c, freed, let, true)
}

// taken does what `c` taking these stretches of things' time does to what
// else has them: the reservations still to be fulfilled that took some of it
// keep it, and whoever has to act on them is told ([Tx.tellOut]); and the
// pools the things are units of have one fewer to give then, which their
// latest reservations wait for (D41) -- the hand-over their cause, when it is
// an operation of somebody's.
func (t *Tx) taken(c *ent.Custody, ss []outSpan) error {
	if len(ss) == 0 {
		return nil
	}
	by, err := t.takers(ss)
	if err != nil {
		return err
	}
	if err := t.tellOut(c, by); err != nil {
		return err
	}
	things := make([]uuid.UUID, 0, len(ss))
	for _, s := range ss {
		things = append(things, s.thing)
	}
	return t.recheckPoolsOf(things...)
}

// released gives what `c` let go of -- these allocations as they were, the
// stretches `let` of them now free -- to whatever waits for it then, the one
// that has waited longest first ([Tx.heal]), and tells whoever a reservation
// it was out over is for that it is not any more ([Tx.tellBack]): `back`, of
// a thing come back, and of one due back sooner otherwise.
func (t *Tx) released(c *ent.Custody, freed []*ent.Allocation, let []outSpan, back bool) error {
	if len(freed) == 0 {
		return nil
	}
	if err := t.heal(freed); err != nil {
		return err
	}
	return t.tellBack(c, let, back)
}

// takers answers, by the thing, the reservations still to be fulfilled -- not
// over, nor deleted -- that take one of these stretches' things for some of
// its stretch, by an allocation of their own of it ([takes]): that booked it,
// or whose kit takes it with it. The ones that begin first first. A pool's
// reservations take none of its members but some of the pool, which the pool
// sees to.
func (t *Tx) takers(ss []outSpan) (map[uuid.UUID][]*ent.Reservation, error) {
	things := make([]uuid.UUID, 0, len(ss))
	for _, s := range ss {
		things = append(things, s.thing)
	}
	as, err := t.db.Allocation.Query().Where(
		allocation.TenantId(t.tenant.Uuid()),
		allocation.ResourceIdIn(things...),
		allocation.KindIn(takes...),
		allocation.RefIdNotNil(),
		allocation.EndsAtGT(t.now),
	).All(t.ctx)
	if err != nil || len(as) == 0 {
		return nil, err
	}
	of := map[uuid.UUID][]uuid.UUID{}
	refs := []uuid.UUID{}
	for _, a := range as {
		if !slices.ContainsFunc(ss, func(s outSpan) bool {
			return s.thing == a.ResourceId && a.BeginsAt.Before(s.to) && a.EndsAt.After(s.from)
		}) {
			continue
		}
		of[*a.RefId] = append(of[*a.RefId], a.ResourceId)
		refs = append(refs, *a.RefId)
	}
	if len(refs) == 0 {
		return nil, nil
	}
	rs, err := t.db.Reservation.Query().Where(
		reservation.TenantId(t.tenant.Uuid()),
		reservation.IdIn(refs...),
		reservation.StatusIn(resHeld, resRequested, resConfirmed),
		reservation.EndsAtGT(t.now),
		reservation.DateErasedIsNil(),
	).Order(reservation.ByBeginsAt(), reservation.ById()).All(t.ctx)
	if err != nil {
		return nil, err
	}
	out := map[uuid.UUID][]*ent.Reservation{}
	for _, r := range rs {
		for _, v := range of[r.Id] {
			if !slices.Contains(out[v], r) {
				out[v] = append(out[v], r)
			}
		}
	}
	return out, nil
}

// outOf answers what each of these reservations takes that somebody has out
// over some of its time, of the ones still to be fulfilled and not over
// ([waits]): each thing it takes by an allocation of its own ([takes]) that a
// hand-over holds then, once, by its tag -- the hand-over that holds it
// longest. What it waits for is said of what it waits for, which says what
// holds it ([Tx.waitingOf]).
func (t *Tx) outOf(rs []*ent.Reservation) (map[uuid.UUID][]*app.ReservationOut, error) {
	ids := []uuid.UUID{}
	for _, r := range rs {
		if waits(r.Status) && r.EndsAt.After(t.now) {
			ids = append(ids, r.Id)
		}
	}
	if len(ids) == 0 {
		return nil, nil
	}
	tid := t.tenant.Uuid()
	as, err := t.db.Allocation.Query().Where(allocation.TenantId(tid), allocation.RefIdIn(ids...), allocation.KindIn(takes...)).All(t.ctx)
	if err != nil || len(as) == 0 {
		return nil, err
	}
	things := []uuid.UUID{}
	for _, a := range as {
		things = append(things, a.ResourceId)
	}
	hs, err := t.db.Allocation.Query().Where(
		allocation.TenantId(tid),
		allocation.ResourceIdIn(things...),
		allocation.Kind(allocCustody),
		allocation.Blocking(true),
		allocation.EndsAtGT(t.now),
	).All(t.ctx)
	if err != nil || len(hs) == 0 {
		return nil, err
	}

	// Each thing a reservation takes, and what holds it longest then.
	held := map[[2]uuid.UUID]*ent.Allocation{}
	order := [][2]uuid.UUID{}
	for _, a := range as {
		for _, h := range hs {
			if h.ResourceId != a.ResourceId || !h.BeginsAt.Before(a.EndsAt) || !h.EndsAt.After(a.BeginsAt) {
				continue
			}
			k := [2]uuid.UUID{*a.RefId, a.ResourceId}
			if v, ok := held[k]; !ok {
				order = append(order, k)
				held[k] = h
			} else if h.EndsAt.After(v.EndsAt) {
				held[k] = h
			}
		}
	}
	if len(order) == 0 {
		return nil, nil
	}
	cids, aids := []uuid.UUID{}, []uuid.UUID{}
	for _, k := range order {
		cids = append(cids, *held[k].RefId)
		aids = append(aids, k[1])
	}
	cs, err := t.db.Custody.Query().Where(custody.TenantId(tid), custody.IdIn(cids...)).All(t.ctx)
	if err != nil {
		return nil, err
	}
	custodies := map[uuid.UUID]*ent.Custody{}
	for _, c := range cs {
		custodies[c.Id] = c
	}
	named, err := t.db.Asset.Query().Where(asset.TenantId(tid), asset.IdIn(aids...)).All(t.ctx)
	if err != nil {
		return nil, err
	}
	assets := map[uuid.UUID]*ent.Asset{}
	for _, a := range named {
		assets[a.Id] = a
	}

	out := map[uuid.UUID][]*app.ReservationOut{}
	for _, k := range order {
		a, c := assets[k[1]], custodies[*held[k].RefId]
		if a == nil || c == nil {
			continue
		}
		v := app.ReservationOut_builder{
			ResourceId:   pdid.Id(a.Id).Bytes(),
			ResourceTag:  a.Tag,
			ResourceName: a.Name,
			CustodyId:    pdid.Id(c.Id).Bytes(),
			Kind:         c.Kind,
			Overdue:      t.late(c),
		}.Build()
		if c.Kind == "loan" && c.DueAt != nil {
			v.SetDueAt(ts(*c.DueAt))
		}
		out[k[0]] = append(out[k[0]], v)
	}
	for _, vs := range out {
		slices.SortFunc(vs, func(a, b *app.ReservationOut) int {
			return cmp.Or(strings.Compare(a.GetResourceTag(), b.GetResourceTag()), bytes.Compare(a.GetResourceId(), b.GetResourceId()))
		})
	}
	return out, nil
}

// late says whether `c` is a loan past its due that is not all back.
func (t *Tx) late(c *ent.Custody) bool {
	return c.Status == "open" && c.Kind == "loan" && c.DueAt != nil && c.DueAt.Before(t.now)
}

// outSay is how a hand-over has a thing out, as a notice says it: lent until
// a moment, issued until it is returned, or lent and late.
type outSay struct {
	// word is what a notice's title calls it; until what the thing is out
	// until, as a sentence says it; and back what brings it back.
	word, until, back string
}

func (t *Tx) outSayOf(c *ent.Custody) outSay {
	switch {
	case t.late(c):
		return outSay{"연체", fmt.Sprintf("대여의 반납 기한(%s)이 지났는데 아직 돌아오지 않아 돌아올 때까지", t.when(*c.DueAt)), "돌아오는 대로"}
	case c.Kind == "loan" && c.DueAt != nil:
		return outSay{"대여", fmt.Sprintf("%s까지 대여되어 그때까지", t.when(*c.DueAt)), "그 전에 돌아오면"}
	}
	return outSay{"지급", "지급되어 반납될 때까지", "반납되면"}
}

// tellOut tells who has to act on it that what `c` handed over is out over
// some of the time of these reservations, which take it, by the thing:
// whoever runs the reservations, once for each thing, and whoever each
// reservation is for, once for theirs -- by mail as well ([Mailed]), since it
// is to be settled before they begin, as of what goes out of use (D42). As
// every notice, not whoever handed it over. Neither says who has it.
func (t *Tx) tellOut(c *ent.Custody, by map[uuid.UUID][]*ent.Reservation) error {
	if len(by) == 0 {
		return nil
	}
	things := make([]uuid.UUID, 0, len(by))
	for k := range by {
		things = append(things, k)
	}
	slices.SortFunc(things, func(a, b uuid.UUID) int { return a.Compare(b) })
	say := t.outSayOf(c)
	for _, k := range things {
		a, err := t.db.Asset.Query().Where(asset.Id(k), asset.TenantId(t.tenant.Uuid())).Only(t.ctx)
		if err != nil {
			return err
		}
		rs := by[k]
		if err := t.notifyManagers("reservation.out", fmt.Sprintf("예약된 자산 %s: %s", say.word, tagged(a)),
			fmt.Sprintf("%s은(는) %s 나가 있어, 그것을 잡은 예약 %d건의 시간과 겹칩니다. 돌아오기 전에는 그 예약의 수령 때 건넬 수 없습니다. 예약은 그 시간을 그대로 잡습니다.", tagged(a), say.until, len(rs)),
			pdid.Id(a.Id), "/reservations?tab=all"); err != nil {
			return err
		}
		if err := eachParty(rs, func(first *ent.Reservation, title string) error {
			return t.notifyParty(first.PartyId, "reservation.out", fmt.Sprintf("예약 자산 %s: %s", say.word, title),
				fmt.Sprintf("%s 예약이 잡은 %s은(는) %s 나가 있습니다. %s 수령할 때 건넵니다. 돌아오면 알려 드립니다.", t.when(first.BeginsAt), tagged(a), say.until, say.back),
				pdid.Id(first.Id), "/reservations?tab=mine")
		}); err != nil {
			return err
		}
	}
	return nil
}

// tellBack tells whoever a reservation that took one of these stretches is
// for, once for theirs, that the thing is not out over its time any more:
// `back`, come back -- and otherwise due back before it -- and that it will be
// handed over at its pickup, in the app only, as what comes back into use is
// (`reservation.resolved`, D42). Not one still out to it by another hand-over.
func (t *Tx) tellBack(c *ent.Custody, let []outSpan, back bool) error {
	if len(let) == 0 {
		return nil
	}
	by, err := t.takers(let)
	if err != nil || len(by) == 0 {
		return err
	}
	things := make([]uuid.UUID, 0, len(by))
	all := []*ent.Reservation{}
	for k, rs := range by {
		things = append(things, k)
		all = append(all, rs...)
	}
	slices.SortFunc(things, func(a, b uuid.UUID) int { return a.Compare(b) })
	still, err := t.outOf(all)
	if err != nil {
		return err
	}
	for _, k := range things {
		a, err := t.db.Asset.Query().Where(asset.Id(k), asset.TenantId(t.tenant.Uuid())).Only(t.ctx)
		if err != nil {
			return err
		}
		rs := slices.DeleteFunc(slices.Clone(by[k]), func(r *ent.Reservation) bool {
			return slices.ContainsFunc(still[r.Id], func(v *app.ReservationOut) bool { return uuidOf(v.GetResourceId()) == k })
		})
		if err := eachParty(rs, func(first *ent.Reservation, title string) error {
			if back {
				return t.notifyParty(first.PartyId, "reservation.resolved", "예약 자산 돌아옴: "+title,
					fmt.Sprintf("%s 예약이 잡은 %s이(가) 돌아와 수령할 때 건넵니다.", t.when(first.BeginsAt), tagged(a)),
					pdid.Id(first.Id), "/reservations?tab=mine")
			}
			return t.notifyParty(first.PartyId, "reservation.resolved", "예약 자산 반납 기한 앞당겨짐: "+title,
				fmt.Sprintf("%s 예약이 잡은 %s은(는) 반납 기한이 %s(으)로 바뀌어, 그 전에 돌아오기로 했습니다.", t.when(first.BeginsAt), tagged(a), t.when(*c.DueAt)),
				pdid.Id(first.Id), "/reservations?tab=mine")
		}); err != nil {
			return err
		}
	}
	return nil
}

// waitedFor says whether a reservation still to be fulfilled takes some of
// what `c` holds from now on: what whoever has it, late, is told waits for it.
func (t *Tx) waitedFor(c *ent.Custody) (bool, error) {
	hs, err := t.holdsOf(c.Id)
	if err != nil || len(hs) == 0 {
		return false, err
	}
	ss := make([]outSpan, 0, len(hs))
	for _, h := range hs {
		ss = append(ss, outSpan{thing: h.ResourceId, from: t.now, to: h.EndsAt})
	}
	by, err := t.takers(ss)
	return len(by) > 0, err
}

// heldOut is the hold of a hand-over among these allocations that is of
// `thing` itself, if there is one: what a booking of it is refused over.
func heldOut(as []*ent.Allocation, thing uuid.UUID) *ent.Allocation {
	for _, a := range as {
		if a.Kind == allocCustody && a.ResourceId == thing {
			return a
		}
	}
	return nil
}

// outRefusal is the refusal of a booking of `a` over `h`, what a hand-over of
// it holds: until when, for the person booking to choose another time.
func (t *Tx) outRefusal(a *ent.Asset, h *ent.Allocation) error {
	if h.EndsAt.Before(forever) {
		return status.Errorf(codes.Aborted, "%s은(는) %s까지 대여 중이라 그 전에는 예약할 수 없습니다. 다른 시간을 고르세요", a.Name, t.when(h.EndsAt))
	}
	if h.RefId != nil {
		if c, err := t.custodyOf(*h.RefId); err == nil && t.late(c) {
			return status.Errorf(codes.Aborted, "%s은(는) 대여의 반납 기한(%s)이 지났는데 아직 돌아오지 않아, 돌아오기 전에는 예약할 수 없습니다", a.Name, t.when(*c.DueAt))
		}
	}
	return status.Errorf(codes.Aborted, "%s은(는) 지급되어 반납되기 전에는 예약할 수 없습니다", a.Name)
}

// outLabel is what a calendar calls the time a hand-over holds: 대여, 지급,
// or a loan that is late.
func (t *Tx) outLabel(a *ent.Allocation) string {
	if a.RefId == nil {
		return custodyKindSay["issue"]
	}
	c, err := t.custodyOf(*a.RefId)
	if err != nil {
		return custodyKindSay["issue"]
	}
	if t.late(c) {
		return "대여 (연체)"
	}
	return or(custodyKindSay[c.Kind], c.Kind)
}

// unheld answers what is out in this tenant that its hand-over holds none of
// the time of, by the hand-over: what a build from before a hand-over held it
// handed over (D53).
func (t *Tx) unheld() (map[uuid.UUID][]uuid.UUID, error) {
	return unheldIn(t.ctx, t.db, t.tenant.Uuid())
}

func unheldIn(ctx context.Context, db *ent.Client, tenant uuid.UUID) (map[uuid.UUID][]uuid.UUID, error) {
	ls, err := db.CustodyLine.Query().Where(
		custodyline.TenantId(tenant),
		custodyline.AssetIdNotNil(),
		custodyline.ReturnedAtIsNil(),
		custodyline.HasCustodyWith(custody.Status("open"), custody.DateErasedIsNil()),
	).All(ctx)
	if err != nil || len(ls) == 0 {
		return nil, err
	}
	cs := []uuid.UUID{}
	for _, l := range ls {
		cs = append(cs, l.CustodyId)
	}
	hs, err := db.Allocation.Query().Where(
		allocation.TenantId(tenant),
		allocation.RefIdIn(cs...),
		allocation.Kind(allocCustody),
	).All(ctx)
	if err != nil {
		return nil, err
	}
	held := map[[2]uuid.UUID]bool{}
	for _, h := range hs {
		held[[2]uuid.UUID{*h.RefId, h.ResourceId}] = true
	}
	out := map[uuid.UUID][]uuid.UUID{}
	for _, l := range ls {
		if l.Quantity-l.ReturnedQuantity <= 0 || held[[2]uuid.UUID{l.CustodyId, l.AssetId}] {
			continue
		}
		if !slices.Contains(out[l.CustodyId], l.AssetId) {
			out[l.CustodyId] = append(out[l.CustodyId], l.AssetId)
		}
	}
	return out, nil
}

// holdOut makes the hold of everything out in the tenant that its hand-over
// holds none of the time of ([Tx.unheld]), from when it was handed over, and
// does what that does ([Tx.hold]): of a loan due back, until then; of an issue
// or a loan past its due, until it is back. It answers how many things it
// held. The tree's lock first, which every hand-over and return takes, and
// then what a booking of any of it holds.
func holdOut(t *Tx) (int, error) {
	out, err := t.unheld()
	if err != nil || len(out) == 0 {
		return 0, err
	}
	if err := t.lockTree(); err != nil {
		return 0, err
	}
	// Read again, now that no hand-over or return changes it.
	if out, err = t.unheld(); err != nil || len(out) == 0 {
		return 0, err
	}
	ids := make([]uuid.UUID, 0, len(out))
	things := []uuid.UUID{}
	for k, vs := range out {
		ids = append(ids, k)
		things = append(things, vs...)
	}
	slices.SortFunc(ids, func(a, b uuid.UUID) int { return a.Compare(b) })
	all, err := t.heldWith(things)
	if err != nil {
		return 0, err
	}
	if err := t.holdThings(all, nil); err != nil {
		return 0, err
	}
	n := 0
	for _, k := range ids {
		c, err := t.custodyOf(k)
		if err != nil {
			return 0, err
		}
		before, err := t.holdsOf(k)
		if err != nil {
			return 0, err
		}
		if err := t.hold(c, out[k], c.IssuedAt); err != nil {
			return 0, err
		}
		after, err := t.holdsOf(k)
		if err != nil {
			return 0, err
		}
		n += len(after) - len(before)
	}
	return n, nil
}

// HoldOut makes the hold of everything out in `tenant` that its hand-over
// holds none of the time of -- what a build from before a hand-over held it
// handed over (2026-10-26, D53) -- with what holding it does: what was booked
// over it says so and is told, and a pool's latest reservations wait. The
// deployment's own work, as a sweep's is; `server` is the ungated stack. It
// answers how many things it held.
func (d *Deps) HoldOut(ctx context.Context, server app.Server, drv dialect.Driver, tenant pdid.Id) (int, error) {
	n := 0
	err := d.System(ctx, server, drv, tenant, func(t *Tx) error {
		var err error
		n, err = holdOut(t)
		return err
	})
	return n, err
}

// Unheld answers the tenants something is out in that its hand-over holds
// none of the time of ([Deps.HoldOut]), in identifier order: read as it is,
// holding nothing and writing nothing.
func Unheld(ctx context.Context, db *ent.Client) ([]pdid.Id, error) {
	ts, err := db.Tenant.Query().Ids(ctx)
	if err != nil {
		return nil, err
	}
	slices.SortFunc(ts, func(a, b uuid.UUID) int { return a.Compare(b) })
	out := []pdid.Id{}
	for _, k := range ts {
		v, err := unheldIn(ctx, db, k)
		if err != nil {
			return nil, err
		}
		if len(v) > 0 {
			out = append(out, pdid.Id(k))
		}
	}
	return out, nil
}

// later is the later of two moments.
func later(a, b time.Time) time.Time {
	if a.After(b) {
		return a
	}
	return b
}
