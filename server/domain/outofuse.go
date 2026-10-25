package domain

import (
	"bytes"
	"cmp"
	"fmt"
	"slices"
	"strings"
	"uuid"

	"github.com/lesomnus/payday/pdid"

	app "github.com/lesomnus/rove"
	"github.com/lesomnus/rove/internal/ent"
	"github.com/lesomnus/rove/internal/ent/allocation"
	"github.com/lesomnus/rove/internal/ent/asset"
	"github.com/lesomnus/rove/internal/ent/reservation"
)

// An item a reservation takes may go out of use before it is picked up --
// lost, retired or disposed ([OutOfUse]) -- and no hand-over takes it then
// (domainCustody.Add). What goes out of use may leave the kits it is in as it
// goes (D40), and a kit's reservations let it go; one that is kept in its kit
// is still taken by the kit's reservations, and one booked itself is still
// booked. So the reservation says so wherever it is shown
// ([Tx.outOfUseOf], `CalendarEntry.out_of_use`), and whoever has to act on it
// is told in the operation that put it out of use, as of a kit's member a
// reservation waits for (kit.go): whoever runs the reservations, and whoever
// each one is for. Once it is in use again, whoever each one is for is told
// that it will be handed over after all (design 4, D42).
//
// What is said is items: what a hand-over takes. A space is checked into and
// not handed over, and a kit or a pool is handed over as what it is made of
// -- a kit's members are each said here, and a pool's members are its units,
// which it has one fewer of, said by the pool's conflict (pool.go).

// outOfUseOf answers what each of these reservations takes that is out of use,
// of the ones still to be fulfilled ([waits]) and not over: the items among
// what it booked and what its kits take with it -- an allocation of each, held
// or waited for -- that are lost, retired or disposed, by their tags.
func (t *Tx) outOfUseOf(rs []*ent.Reservation) (map[uuid.UUID][]*app.ReservationOutOfUse, error) {
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
	as, err := t.db.Allocation.Query().Where(
		allocation.TenantId(tid),
		allocation.RefIdIn(ids...),
	).All(t.ctx)
	if err != nil || len(as) == 0 {
		return nil, err
	}
	things := make([]uuid.UUID, 0, len(as))
	for _, a := range as {
		things = append(things, a.ResourceId)
	}
	gone, err := t.db.Asset.Query().Where(
		asset.TenantId(tid),
		asset.IdIn(things...),
		asset.Kind("item"),
		asset.StatusIn(OutOfUse...),
		asset.DateErasedIsNil(),
	).All(t.ctx)
	if err != nil || len(gone) == 0 {
		return nil, err
	}
	of := map[uuid.UUID]*ent.Asset{}
	for _, a := range gone {
		of[a.Id] = a
	}

	out := map[uuid.UUID][]*app.ReservationOutOfUse{}
	seen := map[[2]uuid.UUID]bool{}
	for _, a := range as {
		v, ok := of[a.ResourceId]
		k := [2]uuid.UUID{*a.RefId, a.ResourceId}
		if !ok || seen[k] {
			continue
		}
		seen[k] = true
		out[*a.RefId] = append(out[*a.RefId], app.ReservationOutOfUse_builder{
			ResourceId:   pdid.Id(v.Id).Bytes(),
			ResourceTag:  v.Tag,
			ResourceName: v.Name,
			Status:       v.Status,
		}.Build())
	}
	for _, vs := range out {
		slices.SortFunc(vs, func(a, b *app.ReservationOutOfUse) int {
			return cmp.Or(strings.Compare(a.GetResourceTag(), b.GetResourceTag()), bytes.Compare(a.GetResourceId(), b.GetResourceId()))
		})
	}
	return out, nil
}

// takenBy answers the reservations still to be fulfilled ([waits]), and not
// over, that take `thing`: that booked it, or whose kit takes it with it,
// holding it or waiting for it -- the ones that begin first first. A pool's
// reservations take none of its members: they take some of the pool.
func (t *Tx) takenBy(thing uuid.UUID) ([]*ent.Reservation, error) {
	tid := t.tenant.Uuid()
	as, err := t.db.Allocation.Query().Where(
		allocation.TenantId(tid),
		allocation.ResourceId(thing),
		allocation.RefIdNotNil(),
		allocation.EndsAtGT(t.now),
	).All(t.ctx)
	if err != nil || len(as) == 0 {
		return nil, err
	}
	refs := make([]uuid.UUID, 0, len(as))
	for _, a := range as {
		refs = append(refs, *a.RefId)
	}
	return t.db.Reservation.Query().Where(
		reservation.TenantId(tid),
		reservation.IdIn(refs...),
		reservation.StatusIn(resHeld, resRequested, resConfirmed),
		reservation.EndsAtGT(t.now),
		reservation.DateErasedIsNil(),
	).Order(reservation.ByBeginsAt(), reservation.ById()).All(t.ctx)
}

// outOfUseChanged tells whoever has to act on it that the item `id`, which
// was in status `was` before this operation, went out of use in it or came
// back into use: called once its status is what this operation leaves it in,
// and once what left the kits it was in let it go. A change from one status
// out of use to another -- lost, and then disposed of -- changes nothing a
// reservation can be handed, and tells nobody.
func (t *Tx) outOfUseChanged(id uuid.UUID, was string) error {
	a, err := t.db.Asset.Query().Where(asset.Id(id), asset.TenantId(t.tenant.Uuid())).Only(t.ctx)
	if err != nil {
		return err
	}
	gone := slices.Contains(OutOfUse, a.Status)
	if a.Kind != "item" || gone == slices.Contains(OutOfUse, was) {
		return nil
	}
	rs, err := t.takenBy(a.Id)
	if err != nil || len(rs) == 0 {
		return err
	}
	if gone {
		return t.tellOutOfUse(a, rs)
	}
	return t.tellInUse(a, rs)
}

// tellOutOfUse tells who has to act on it that `a`, which these reservations
// take, is out of use: whoever runs the reservations, once, and whoever each
// is for, once for theirs -- by mail as well ([Mailed]), since it is to be
// settled before they begin. As every notice, not whoever put it out of use.
func (t *Tx) tellOutOfUse(a *ent.Asset, rs []*ent.Reservation) error {
	say := or(statusSay[a.Status], a.Status)
	if err := t.notifyManagers("reservation.out_of_use", fmt.Sprintf("예약된 자산 %s: %s", say, tagged(a)),
		fmt.Sprintf("%s은(는) %s 상태라 그것을 잡은 예약 %d건의 수령 때 건네지 않습니다. 키트의 구성품으로 남아 있으면 키트에서 빼기 전까지 그 키트의 예약이 계속 잡습니다.", tagged(a), say, len(rs)),
		pdid.Id(a.Id), "/reservations?tab=all"); err != nil {
		return err
	}
	return eachParty(rs, func(first *ent.Reservation, title string) error {
		return t.notifyParty(first.PartyId, "reservation.out_of_use", fmt.Sprintf("예약 자산 %s: %s", say, title),
			fmt.Sprintf("%s 예약이 잡은 %s은(는) %s 상태라 수령할 때 건네지 않습니다. 다시 쓸 수 있게 되면 알려 드립니다.", t.when(first.BeginsAt), tagged(a), say),
			pdid.Id(first.Id), "/reservations?tab=mine")
	})
}

// tellInUse tells whoever each of these reservations is for, once for theirs,
// that `a`, which they take, is in use again and will be handed over, as a
// reservation that took what it waited for does (`reservation.resolved`):
// in the app only, since there is nothing left to settle.
func (t *Tx) tellInUse(a *ent.Asset, rs []*ent.Reservation) error {
	say := or(statusSay[a.Status], a.Status)
	return eachParty(rs, func(first *ent.Reservation, title string) error {
		return t.notifyParty(first.PartyId, "reservation.resolved", "예약 자산 돌아옴: "+title,
			fmt.Sprintf("%s 예약이 잡은 %s은(는) 이제 %s 상태라 수령할 때 건넵니다.", t.when(first.BeginsAt), tagged(a), say),
			pdid.Id(first.Id), "/reservations?tab=mine")
	})
}

// eachParty calls `tell` once for each party these reservations are for, in
// the order they are, with the first of theirs and its name as a title says
// it: "외 N건" for the rest.
func eachParty(rs []*ent.Reservation, tell func(first *ent.Reservation, title string) error) error {
	by := map[uuid.UUID][]*ent.Reservation{}
	parties := []uuid.UUID{}
	for _, r := range rs {
		if _, ok := by[r.PartyId]; !ok {
			parties = append(parties, r.PartyId)
		}
		by[r.PartyId] = append(by[r.PartyId], r)
	}
	for _, p := range parties {
		vs := by[p]
		title := vs[0].Name
		if len(vs) > 1 {
			title += fmt.Sprintf(" 외 %d건", len(vs)-1)
		}
		if err := tell(vs[0], title); err != nil {
			return err
		}
	}
	return nil
}
