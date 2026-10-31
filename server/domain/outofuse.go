package domain

import (
	"bytes"
	"cmp"
	"fmt"
	"slices"
	"strings"
	"uuid"

	"github.com/lesomnus/payday/pdid"

	app "github.com/lesomnus/loft"
	"github.com/lesomnus/loft/internal/ent"
	"github.com/lesomnus/loft/internal/ent/allocation"
	"github.com/lesomnus/loft/internal/ent/asset"
	"github.com/lesomnus/loft/internal/ent/predicate"
	"github.com/lesomnus/loft/internal/ent/reservation"
	"github.com/lesomnus/loft/internal/ent/reservationitem"
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
// And what is out of use is not booked anew, whatever it is (2026-10-26,
// design 4, D43): an item, a space, a kit or a group that is lost, retired or
// disposed is refused a booking (domainReservation.Add), and a reservation
// made of it before is not approved or confirmed -- saying yes to it is
// booking it -- nor fulfilled through it: a space is not checked into, a kit
// or a group is not picked up, and an item is left out of what a pickup hands
// over, as it was ([Tx.unusable]). A kit whose member is out of use is booked
// still, and says so. What a reservation booked that goes out of use is said
// and told as an item it takes is, whatever it is; what a kit takes with it
// is said of its items, which a pickup hands over. A pool's members are its
// units, which it has one fewer of, said by the pool's conflict (pool.go);
// and a pool out of use itself is said to be, and not counted short as well.

// voided is what is said of a thing erased as registered by mistake, as its
// status would be: out of use to whatever booked it, for good (2026-10-26,
// D48). No asset is in it; it is what [goneAs] says of one erased.
const voided = "voided"

// goneAs is what `a` is said to be, out of use: its status, or [voided].
func goneAs(a *ent.Asset) string {
	if a.DateErased != nil {
		return voided
	}
	return a.Status
}

// goneSay is what `a` is said to be out of use as, in words: "불용", "삭제".
func goneSay(a *ent.Asset) string {
	if a.DateErased != nil {
		return "삭제"
	}
	return or(statusSay[a.Status], a.Status)
}

// gone is what a reservation takes that is out of use: lost, retired or
// disposed, or erased as registered by mistake (D48).
func gone() predicate.Asset {
	return asset.Or(asset.And(asset.StatusIn(OutOfUse...), asset.DateErasedIsNil()), asset.DateErasedNotNil())
}

// outOfUseOf answers what each of these reservations takes that is out of use,
// of the ones still to be fulfilled ([waits]) and not over, by their tags:
// what it booked itself, whatever it is, and the items its kits take with it
// -- an allocation of each, held or waited for -- that are lost, retired or
// disposed; and what it booked that was voided since (D48).
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
	items, err := t.db.ReservationItem.Query().Where(
		reservationitem.TenantId(tid),
		reservationitem.ReservationIdIn(ids...),
	).All(t.ctx)
	if err != nil {
		return nil, err
	}
	as, err := t.db.Allocation.Query().Where(
		allocation.TenantId(tid),
		allocation.RefIdIn(ids...),
	).All(t.ctx)
	if err != nil {
		return nil, err
	}
	// What each takes, by the reservation and the thing: what it booked, and
	// then what it holds an allocation of.
	takes := make([][2]uuid.UUID, 0, len(items)+len(as))
	booked := map[[2]uuid.UUID]bool{}
	for _, it := range items {
		k := [2]uuid.UUID{it.ReservationId, it.ResourceId}
		booked[k] = true
		takes = append(takes, k)
	}
	for _, a := range as {
		takes = append(takes, [2]uuid.UUID{*a.RefId, a.ResourceId})
	}
	if len(takes) == 0 {
		return nil, nil
	}
	things := make([]uuid.UUID, 0, len(takes))
	for _, k := range takes {
		things = append(things, k[1])
	}
	vs, err := t.db.Asset.Query().Where(
		asset.TenantId(tid),
		asset.IdIn(things...),
		gone(),
	).All(t.ctx)
	if err != nil || len(vs) == 0 {
		return nil, err
	}
	of := map[uuid.UUID]*ent.Asset{}
	for _, a := range vs {
		of[a.Id] = a
	}

	out := map[uuid.UUID][]*app.ReservationOutOfUse{}
	seen := map[[2]uuid.UUID]bool{}
	for _, k := range takes {
		v, ok := of[k[1]]
		// Of what its kits take with it, the items, which a pickup hands
		// over.
		if !ok || seen[k] || ((v.Kind != "item" || v.DateErased != nil) && !booked[k]) {
			continue
		}
		seen[k] = true
		out[k[0]] = append(out[k[0]], app.ReservationOutOfUse_builder{
			ResourceId:   pdid.Id(v.Id).Bytes(),
			ResourceTag:  v.Tag,
			ResourceName: v.Name,
			Status:       goneAs(v),
			ResourceKind: v.Kind,
		}.Build())
	}
	for _, vs := range out {
		slices.SortFunc(vs, func(a, b *app.ReservationOutOfUse) int {
			return cmp.Or(strings.Compare(a.GetResourceTag(), b.GetResourceTag()), bytes.Compare(a.GetResourceId(), b.GetResourceId()))
		})
	}
	return out, nil
}

// unusable answers what reservation `rid` booked itself that is out of use
// now, by tag: what it is neither approved nor confirmed with, nor fulfilled
// through (D43) -- voided too (D48). Read as what booked it is held -- the
// schedules of what it booked ([Tx.lockAllocations]), which a change of
// their status holds before it tells who booked them ([Tx.holdLeaving]) --
// what is read is what that change left, or what it is yet to see.
func (t *Tx) unusable(rid uuid.UUID) ([]*ent.Asset, error) {
	tid := t.tenant.Uuid()
	items, err := t.db.ReservationItem.Query().Where(
		reservationitem.TenantId(tid),
		reservationitem.ReservationId(rid),
	).All(t.ctx)
	if err != nil || len(items) == 0 {
		return nil, err
	}
	ids := make([]uuid.UUID, 0, len(items))
	for _, it := range items {
		ids = append(ids, it.ResourceId)
	}
	as, err := t.db.Asset.Query().Where(
		asset.TenantId(tid),
		asset.IdIn(ids...),
		gone(),
	).All(t.ctx)
	if err != nil {
		return nil, err
	}
	slices.SortFunc(as, func(a, b *ent.Asset) int { return cmp.Or(strings.Compare(a.Tag, b.Tag), a.Id.Compare(b.Id)) })
	return as, nil
}

// outOfUseNamed is what is out of use as a refusal names it: each by its tag
// and name, with its status -- "BT-1 배터리(불용), R-1 회의실(폐기)", or
// "G-1 카메라 풀(삭제)" of one voided.
func outOfUseNamed(as []*ent.Asset) string {
	names := make([]string, 0, len(as))
	for _, a := range as {
		names = append(names, fmt.Sprintf("%s(%s)", tagged(a), goneSay(a)))
	}
	return strings.Join(names, ", ")
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

// bookedOf answers the ones of these reservations that booked `thing` itself,
// in the order they are.
func (t *Tx) bookedOf(rs []*ent.Reservation, thing uuid.UUID) ([]*ent.Reservation, error) {
	if len(rs) == 0 {
		return nil, nil
	}
	ids := make([]uuid.UUID, 0, len(rs))
	for _, r := range rs {
		ids = append(ids, r.Id)
	}
	its, err := t.db.ReservationItem.Query().Where(
		reservationitem.TenantId(t.tenant.Uuid()),
		reservationitem.ReservationIdIn(ids...),
		reservationitem.ResourceId(thing),
	).All(t.ctx)
	if err != nil {
		return nil, err
	}
	in := map[uuid.UUID]bool{}
	for _, it := range its {
		in[it.ReservationId] = true
	}
	return slices.DeleteFunc(slices.Clone(rs), func(r *ent.Reservation) bool { return !in[r.Id] }), nil
}

// outOfUseChanged tells whoever has to act on it that `id`, which was in
// status `was` before this operation, went out of use in it or came back into
// use: called once its status is what this operation leaves it in, and once
// what left the kits it was in let it go. Of an item, the reservations that
// take it -- that booked it, or whose kits take it with it; of a space, a kit
// or a group, the ones that booked it (D43). A change from one status out of
// use to another -- lost, and then disposed of -- changes nothing a
// reservation may have, and tells nobody.
func (t *Tx) outOfUseChanged(id uuid.UUID, was string) error {
	a, err := t.db.Asset.Query().Where(asset.Id(id), asset.TenantId(t.tenant.Uuid())).Only(t.ctx)
	if err != nil {
		return err
	}
	gone := slices.Contains(OutOfUse, a.Status)
	if gone == slices.Contains(OutOfUse, was) {
		return nil
	}
	rs, err := t.takenBy(a.Id)
	if err != nil {
		return err
	}
	if a.Kind != "item" {
		if rs, err = t.bookedOf(rs, a.Id); err != nil {
			return err
		}
	}
	if len(rs) == 0 {
		return nil
	}
	if gone {
		return t.tellOutOfUse(a, rs)
	}
	return t.tellInUse(a, rs)
}

// keeps is what a thing out of use keeps a reservation of it from, as its
// notices say it: an item is not handed over at a pickup, a space is not
// checked into, and a kit or a group is not picked up (D42, D43).
type keeps struct {
	// noun is what a notice's title calls it; cannot is what it keeps a
	// reservation from, and can what it lets one do again once it is in use.
	noun, cannot, can string
}

func keepsOf(a *ent.Asset) keeps {
	switch a.Kind {
	case "item":
		return keeps{"자산", "수령할 때 건네지 않습니다", "수령할 때 건넵니다"}
	case "space":
		return keeps{"공간", "체크인할 수 없습니다", "체크인할 수 있습니다"}
	}
	return keeps{"자산", "수령할 수 없습니다", "수령할 수 있습니다"}
}

// tellOutOfUse tells who has to act on it that `a`, which these reservations
// take, is out of use: whoever runs the reservations, once, and whoever each
// is for, once for theirs -- by mail as well ([Mailed]), since it is to be
// settled before they begin. As every notice, not whoever put it out of use.
// What it keeps them from is said as [keepsOf] says it.
func (t *Tx) tellOutOfUse(a *ent.Asset, rs []*ent.Reservation) error {
	say := or(statusSay[a.Status], a.Status)
	k := keepsOf(a)
	body := fmt.Sprintf("%s은(는) %s 상태라 그것을 잡은 예약 %d건의 수령 때 건네지 않습니다. 키트의 구성품으로 남아 있으면 키트에서 빼기 전까지 그 키트의 예약이 계속 잡습니다.", tagged(a), say, len(rs))
	if a.Kind != "item" {
		body = fmt.Sprintf("%s은(는) %s 상태라 그것의 예약 %d건은 %s. 새로 예약할 수도 없고, 예약은 취소하기 전까지 그 시간을 잡습니다.", tagged(a), say, len(rs), k.cannot)
	}
	if err := t.notifyManagers("reservation.out_of_use", fmt.Sprintf("예약된 %s %s: %s", k.noun, say, tagged(a)), body,
		pdid.Id(a.Id), "/reservations?tab=all"); err != nil {
		return err
	}
	return eachParty(rs, func(first *ent.Reservation, title string) error {
		body := fmt.Sprintf("%s 예약이 잡은 %s은(는) %s 상태라 %s. 다시 쓸 수 있게 되면 알려 드립니다.", t.when(first.BeginsAt), tagged(a), say, k.cannot)
		if a.Kind != "item" {
			body = fmt.Sprintf("%s 예약의 %s은(는) %s 상태라 %s. 다시 쓸 수 있게 되면 알려 드립니다.", t.when(first.BeginsAt), tagged(a), say, k.cannot)
		}
		return t.notifyParty(first.PartyId, "reservation.out_of_use", fmt.Sprintf("예약 %s %s: %s", k.noun, say, title), body,
			pdid.Id(first.Id), "/reservations?tab=mine")
	})
}

// voidedOf tells whoever has to act on it that `id`, which this operation
// voids, was booked by reservations still to be fulfilled: the ones that
// booked it itself, which it is out of use to for good -- a kit or a pool it
// was a member of let it go as it left them (D48). Called once what it was
// in let it go, and before it is erased.
func (t *Tx) voidedOf(id uuid.UUID) error {
	a, err := t.db.Asset.Query().Where(asset.Id(id), asset.TenantId(t.tenant.Uuid())).Only(t.ctx)
	if err != nil {
		return err
	}
	rs, err := t.takenBy(id)
	if err != nil {
		return err
	}
	if rs, err = t.bookedOf(rs, id); err != nil || len(rs) == 0 {
		return err
	}
	return t.tellVoided(a, rs)
}

// tellVoided tells who has to act on it that `a`, which these reservations
// booked, is voided -- erased as registered by mistake -- and is out of use
// to them for good (D48): whoever runs the reservations, once, and whoever
// each is for, once for theirs, by mail as well ([Mailed]), as of what goes
// out of use ([Tx.tellOutOfUse]). Nothing brings it back, so nobody is told
// it will be, and whoever each is for is told to book something else.
func (t *Tx) tellVoided(a *ent.Asset, rs []*ent.Reservation) error {
	k := keepsOf(a)
	if err := t.notifyManagers("reservation.out_of_use", fmt.Sprintf("예약된 %s 삭제: %s", k.noun, tagged(a)),
		fmt.Sprintf("%s은(는) 삭제되어 그것의 예약 %d건은 %s. 예약은 취소하기 전까지 남습니다.", tagged(a), len(rs), k.cannot),
		pdid.Id(a.Id), "/reservations?tab=all"); err != nil {
		return err
	}
	return eachParty(rs, func(first *ent.Reservation, title string) error {
		return t.notifyParty(first.PartyId, "reservation.out_of_use", fmt.Sprintf("예약 %s 삭제: %s", k.noun, title),
			fmt.Sprintf("%s 예약의 %s은(는) 삭제되어 %s. 취소하고 다른 것으로 다시 예약하세요.", t.when(first.BeginsAt), tagged(a), k.cannot),
			pdid.Id(first.Id), "/reservations?tab=mine")
	})
}

// tellInUse tells whoever each of these reservations is for, once for theirs,
// that `a`, which they take, is in use again and will be handed over -- or
// checked into, or picked up -- as a reservation that took what it waited for
// does (`reservation.resolved`): in the app only, since there is nothing left
// to settle.
func (t *Tx) tellInUse(a *ent.Asset, rs []*ent.Reservation) error {
	say := or(statusSay[a.Status], a.Status)
	k := keepsOf(a)
	return eachParty(rs, func(first *ent.Reservation, title string) error {
		body := fmt.Sprintf("%s 예약이 잡은 %s은(는) 이제 %s 상태라 %s.", t.when(first.BeginsAt), tagged(a), say, k.can)
		if a.Kind != "item" {
			body = fmt.Sprintf("%s 예약의 %s은(는) 이제 %s 상태라 %s.", t.when(first.BeginsAt), tagged(a), say, k.can)
		}
		return t.notifyParty(first.PartyId, "reservation.resolved", fmt.Sprintf("예약 %s 돌아옴: %s", k.noun, title), body,
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
