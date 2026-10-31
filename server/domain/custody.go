package domain

import (
	"context"
	"fmt"
	"slices"
	"uuid"

	"github.com/lesomnus/payday/pdid"
	"github.com/lesomnus/z"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	app "github.com/lesomnus/loft"
	"github.com/lesomnus/loft/internal/ent"
	"github.com/lesomnus/loft/internal/ent/custodyline"
	"github.com/lesomnus/loft/internal/ent/reservation"
	"github.com/lesomnus/loft/server/pd"
)

type domainCustody struct {
	Domain
	app.CustodyServiceServer
}

func (s Domain) Custody() app.CustodyServiceServer { return domainCustody{s, s.Next().Custody()} }

// Add hands things over: an issue for keeps, or a loan with a due date. Each
// asset line opens the custodian stewardship, and each stock line takes the
// quantity out of the stock (design D6).
func (s domainCustody) Add(ctx context.Context, req *app.CustodyAddRequest) (*app.Custody, error) {
	id := made(req.GetOp(), pd.CustodyDomain, 0)
	var out *app.Custody
	err := s.tx(ctx, func(t *Tx) error {
		// Before the due date is checked against now, and the things against
		// who has them: a hand-over sent again finds them handed over.
		if err := t.done(req.GetOp()); err != nil {
			return err
		}
		p, err := t.next.Party().Get(t.ctx, app.PartyGetRequest_builder{Ref: req.GetParty()}.Build())
		if err != nil {
			return err
		}
		if p.GetKind() == "vendor" {
			return invalid("party", "거래처에는 지급할 수 없습니다")
		}
		if len(req.GetLines()) == 0 {
			return invalid("lines", "건넬 품목을 고르세요")
		}
		kind := req.GetKind()
		if kind == "" {
			kind = "issue"
			if req.HasDueAt() {
				kind = "loan"
			}
		}
		if kind != "issue" && kind != "loan" {
			return invalid("kind", "지급 또는 대여입니다")
		}
		// The lock before the due date is held against now: the moment is
		// taken once it is held ([Tx.lockTree]).
		if err := t.lockTree(); err != nil {
			return err
		}
		if req.HasDueAt() && !req.GetDueAt().AsTime().After(t.now) {
			return invalid("due_at", "반납 기한은 지금 이후여야 합니다")
		}
		// What was handed over, to whom; each asset an event of its own
		// below, which says the same of it. Why is the hand-over's memo, in
		// the words of whoever made it.
		said := func(asset []byte) *app.CustodyIssued {
			v := app.CustodyIssued_builder{
				Custody:     id.Bytes(),
				Party:       p.GetId(),
				Kind:        kind,
				Reservation: req.GetReservationId(),
				Asset:       asset,
			}.Build()
			if asset == nil {
				v.SetLines(uint32(len(req.GetLines())))
			}
			if req.HasDueAt() {
				v.SetDue(req.GetDueAt())
			}
			return v
		}
		if err := t.begin(req.GetOp(), "custody.issue", id, t.now, req.GetDesc(), said(nil)); err != nil {
			return err
		}

		row := app.CustodyAddRequest_builder{
			Id:       id.Bytes(),
			Tenant:   t.tenantRef(),
			Desc:     req.GetDesc(),
			Party:    app.PartyRef_builder{Id: p.GetId()}.Build(),
			Kind:     kind,
			Status:   "open",
			IssuedAt: ts(t.now),
			IssuedBy: t.actor.Bytes(),
		}.Build()
		if req.HasDueAt() {
			row.SetDueAt(req.GetDueAt())
		}
		// What it hands over, whose time it holds from now on (hold.go).
		things := []uuid.UUID{}
		for i, l := range req.GetLines() {
			if !l.HasAsset() {
				continue
			}
			_, aid, err := t.get(l.GetAsset(), fmt.Sprintf("lines[%d].asset", i))
			if err != nil {
				return err
			}
			things = append(things, aid.Uuid())
		}
		var picked *ent.Reservation
		var rid pdid.Id
		also := []uuid.UUID{}
		if len(req.GetReservationId()) > 0 {
			// A pickup fulfils a reservation: it is in use from now, and what
			// it held is now held by this custody instead. Held first and read
			// after ([Tx.lockReservations]): a no-show sweep at the same moment
			// is one or the other, and not a custody against a no-show.
			var err error
			if rid, err = pdid.From(req.GetReservationId()); err != nil {
				return invalid("reservation_id", "예약 식별자가 올바르지 않습니다")
			}
			if err := t.lockReservations(rid); err != nil {
				return err
			}
			r, err := t.next.Reservation().Get(t.ctx, app.ReservationGetRequest_builder{Ref: app.ReservationRef_builder{Id: req.GetReservationId()}.Build()}.Build())
			if err != nil {
				return err
			}
			if r.GetStatus() != resConfirmed {
				return failed("예약이 %s 상태입니다. 확정된 예약만 수령할 수 있습니다", or(reservationSay[r.GetStatus()], r.GetStatus()))
			}
			if also, err = t.allocated(rid); err != nil {
				return err
			}
		}
		// What it hands over, and what goes out with it, and what a booking
		// of any of it holds -- with what the reservation it fulfils holds --
		// held before anything of them is read: a booking of any of it at the
		// same moment is one this hand-over sees, or one that sees what this
		// holds (hold.go).
		held, err := t.heldWith(things)
		if err != nil {
			return err
		}
		if err := t.holdThings(held, also); err != nil {
			return err
		}
		if !rid.IsZero() {
			// Nor through what it booked that is out of use and is handed
			// over as what it is made of -- a kit, a group -- or is no
			// hand-over's (D43), read as what it booked is held
			// ([Tx.unusable]). An item out of use is left out of what is
			// handed over, a line at a time, below. And what it hands out
			// held with it: of a pool it booked, what it hands out is its
			// own from now on ([Tx.handOver]).
			gone, err := t.unusable(rid.Uuid())
			if err != nil {
				return err
			}
			if gone = slices.DeleteFunc(gone, func(a *ent.Asset) bool { return a.Kind == "item" }); len(gone) > 0 {
				return failed("예약한 것 가운데 쓸 수 없는 것이 있어 수령할 수 없습니다: %s", outOfUseNamed(gone))
			}
			if picked, err = t.db.Reservation.Query().Where(reservation.Id(rid.Uuid()), reservation.TenantId(t.tenant.Uuid())).Only(t.ctx); err != nil {
				return err
			}
			if _, err := t.next.Reservation().Patch(t.ctx, app.ReservationPatchRequest_builder{
				Ref:              app.ReservationRef_builder{Id: rid.Bytes()}.Build(),
				Status:           z.Ptr(resInUse),
				CheckedInAt:      ts(t.now),
				DateUpdatedForce: z.Ptr(true),
			}.Build()); err != nil {
				return err
			}
			row.SetReservationId(req.GetReservationId())
		}
		c, err := t.next.Custody().Add(t.ctx, row)
		if err != nil {
			return err
		}

		for i, l := range req.GetLines() {
			line := app.CustodyLineAddRequest_builder{
				Tenant:       t.tenantRef(),
				Custody:      app.CustodyRef_builder{Id: id.Bytes()}.Build(),
				OutAt:        ts(t.now),
				ConditionOut: l.GetCondition(),
			}.Build()
			switch {
			case l.HasAsset():
				a, aid, err := t.get(l.GetAsset(), fmt.Sprintf("lines[%d].asset", i))
				if err != nil {
					return err
				}
				if slices.Contains(OutOfUse, a.GetStatus()) {
					return failed("%s은(는) %s 상태입니다", a.GetTag(), or(statusSay[a.GetStatus()], a.GetStatus()))
				}
				if a.GetKind() == "space" || a.GetKind() == "group" {
					return invalid(fmt.Sprintf("lines[%d].asset", i), "%s은(는) 건넬 수 없습니다", or(kindSay[a.GetKind()], a.GetKind()))
				}
				if a.HasCustodian() {
					return failed("%s은(는) 이미 누군가 가지고 있습니다. 먼저 반납을 받으세요", a.GetTag())
				}
				line.SetAsset(app.AssetRef_builder{Id: aid.Bytes()}.Build())
				line.SetQuantity(1)
				// What the hand-over did to this asset is an event of its own,
				// the hand-over its cause: the asset's history says it was
				// handed over, and to whom (design D34).
				if err := t.child("custody.issue", aid, t.now, req.GetDesc(), said(aid.Bytes()), func() error {
					if _, err := t.steward(aid.Uuid(), "custodian", t.now, &steward{party: uuidOf(p.GetId())}, nil); err != nil {
						return err
					}
					if c := l.GetCondition(); c != "" && c != a.GetCondition() && slices.Contains(Conditions, c) {
						if err := t.addFact(aid.Uuid(), "condition", c, false, t.now); err != nil {
							return err
						}
						if _, err := t.sync(aid.Uuid()); err != nil {
							return err
						}
					}
					return nil
				}); err != nil {
					return err
				}
			case l.HasStock():
				q := l.GetQuantity()
				if q <= 0 {
					return invalid(fmt.Sprintf("lines[%d].quantity", i), "수량은 1 이상입니다")
				}
				st, err := t.next.Stock().Get(t.ctx, app.StockGetRequest_builder{Ref: l.GetStock()}.Build())
				if err != nil {
					return err
				}
				if err := t.moveStock(st, -q, "issue", id, t.now); err != nil {
					return err
				}
				line.SetStock(app.StockRef_builder{Id: st.GetId()}.Build())
				line.SetQuantity(q)
			default:
				return invalid(fmt.Sprintf("lines[%d]", i), "품목은 자산이나 재고입니다")
			}
			if _, err := t.next.CustodyLine().Add(t.ctx, line); err != nil {
				return err
			}
		}
		// What it hands out of a pool the reservation booked is the
		// reservation's own from now on, in place of what it held of the
		// pool (design 4, D46).
		if picked != nil {
			if err := t.handOver(picked, things); err != nil {
				return err
			}
		}
		// And what it hands out is out from now on, which takes its time
		// until it is due back, or back (D53): what was booked of it then
		// says so, and a pool it is one of has one fewer to give. After the
		// pickup has made what it hands out its own, which this is then of
		// too.
		k, err := t.custodyOf(id.Uuid())
		if err != nil {
			return err
		}
		if err := t.hold(k, things, t.now); err != nil {
			return err
		}

		if err := t.notifyParty(uuidOf(p.GetId()), "custody.issued", "인수 확인 요청",
			fmt.Sprintf("%d건을 받으셨다면 인수 확인을 눌러 주세요", len(req.GetLines())), id, "/custody"); err != nil {
			return err
		}
		out = c
		return nil
	})
	if err == errDone {
		return s.CustodyServiceServer.Get(ctx, app.CustodyGetRequest_builder{Ref: app.CustodyRef_builder{Id: id.Bytes()}.Build()}.Build())
	}
	return out, err
}

// Acknowledge is the receiver saying they have it.
func (s domainCustody) Acknowledge(ctx context.Context, req *app.CustodyAcknowledgeRequest) (*app.Custody, error) {
	var out *app.Custody
	err := s.tx(ctx, func(t *Tx) error {
		c, err := t.next.Custody().Get(t.ctx, app.CustodyGetRequest_builder{Ref: req.GetRef()}.Build())
		if err != nil {
			return err
		}
		if c.HasAcknowledgedAt() {
			out = c
			return nil
		}
		if !manages(t.roleOf()) {
			me, err := t.partyOf()
			if err != nil {
				return err
			}
			if pdid.Id(me.Id) != idOf(c.GetParty().GetId()) {
				return status.Error(codes.PermissionDenied, "받은 사람만 인수 확인을 할 수 있습니다")
			}
		}
		if err := t.begin(nil, "custody.acknowledge", idOf(c.GetId()), t.now, "", app.CustodyAcknowledged_builder{Custody: c.GetId()}.Build()); err != nil {
			return err
		}
		out, err = t.next.Custody().Patch(t.ctx, app.CustodyPatchRequest_builder{
			Ref:              req.GetRef(),
			AcknowledgedAt:   ts(t.now),
			DateUpdatedForce: z.Ptr(true),
		}.Build())
		return err
	})
	return out, err
}

// Return takes back some or all of what is out.
func (s domainCustody) Return(ctx context.Context, req *app.CustodyReturnRequest) (*app.Custody, error) {
	var out *app.Custody
	err := s.tx(ctx, func(t *Tx) error {
		// The lock first, so that what is still out is read as nobody else
		// is changing it; and then whether this is a return sent again, which
		// finds the loan closed -- by itself.
		if err := t.lockTree(); err != nil {
			return err
		}
		if err := t.done(req.GetOp()); err != nil {
			return err
		}
		at, err := t.at(req.GetAt(), "at")
		if err != nil {
			return err
		}
		c, err := t.next.Custody().Get(t.ctx, app.CustodyGetRequest_builder{Ref: req.GetRef()}.Build())
		if err != nil {
			return err
		}
		if c.GetStatus() != "open" {
			return failed("이 지급은 %s 상태입니다", or(custodySay[c.GetStatus()], c.GetStatus()))
		}
		cid := idOf(c.GetId())
		lines, err := t.db.CustodyLine.Query().Where(custodyline.TenantId(t.tenant.Uuid()), custodyline.CustodyId(cid.Uuid())).All(t.ctx)
		if err != nil {
			return err
		}
		want := map[string]*app.CustodyReturnLine{}
		for _, l := range req.GetLines() {
			want[string(l.GetLine().GetId())] = l
		}
		// What comes back of the assets out, and what stays out.
		back, still := []uuid.UUID{}, []uuid.UUID{}
		for _, l := range lines {
			if l.AssetId == (uuid.UUID{}) || l.Quantity-l.ReturnedQuantity <= 0 {
				continue
			}
			if _, ok := want[string(pdid.Id(l.Id).Bytes())]; len(want) > 0 && !ok {
				still = append(still, l.AssetId)
				continue
			}
			back = append(back, l.AssetId)
		}
		// The reservation it fulfilled first, whose row everything that
		// changes it takes before its schedules (loft#35); and then what
		// comes back, and what a booking of any of it holds, at once, with
		// what the reservation holds: what this lets go of of their time is
		// theirs to give to what waits for it ([Tx.letGo], [Tx.heal]).
		var also []uuid.UUID
		if len(c.GetReservationId()) > 0 {
			rid := idOf(c.GetReservationId())
			if err := t.lockReservations(rid); err != nil {
				return err
			}
			if also, err = t.allocated(rid); err != nil {
				return err
			}
		}
		gone, err := t.heldWith(back)
		if err != nil {
			return err
		}
		if err := t.holdThings(gone, also); err != nil {
			return err
		}

		var to *app.Asset
		if req.HasTo() {
			if to, _, err = t.get(req.GetTo(), "to"); err != nil {
				return err
			}
		}
		returned := func(asset []byte) *app.CustodyReturned {
			v := app.CustodyReturned_builder{Custody: cid.Bytes(), Asset: asset}.Build()
			if to != nil {
				v.SetTo(to.GetId())
			}
			return v
		}
		if err := t.begin(req.GetOp(), "custody.return", cid, at, "", returned(nil)); err != nil {
			return err
		}

		open := 0
		for _, l := range lines {
			left := l.Quantity - l.ReturnedQuantity
			if left <= 0 {
				continue
			}
			ask := left
			cond := ""
			if len(want) > 0 {
				w, ok := want[string(pdid.Id(l.Id).Bytes())]
				if !ok {
					open++
					continue
				}
				if w.GetQuantity() > 0 {
					ask = min(w.GetQuantity(), left)
				}
				cond = w.GetCondition()
			}

			patch := app.CustodyLinePatchRequest_builder{
				Ref:              app.CustodyLineRef_builder{Id: pdid.Id(l.Id).Bytes()}.Build(),
				ReturnedQuantity: z.Ptr(l.ReturnedQuantity + ask),
				DateUpdatedForce: z.Ptr(true),
			}.Build()
			if cond != "" {
				patch.SetConditionIn(cond)
			}
			if ask == left {
				patch.SetReturnedAt(ts(at))
			} else {
				open++
			}

			if l.AssetId != (uuid.UUID{}) {
				a := l.AssetId
				// What the return did to this asset is an event of its own, the
				// return its cause (design D34).
				if err := t.child("custody.return", pdid.Id(a), at, "", returned(pdid.Id(a).Bytes()), func() error {
					if _, err := t.steward(a, "custodian", at, nil, nil); err != nil {
						return err
					}
					if cond != "" && slices.Contains(Conditions, cond) {
						if err := t.addFact(a, "condition", cond, false, at); err != nil {
							return err
						}
					}
					if to != nil {
						old, err := t.placements(a)
						if err != nil {
							return err
						}
						tl, changed := old.set(at, &place{parent: uuidOf(to.GetId()), mode: "located"})
						if changed {
							if err := t.checkPlacements(a, tl, old, false); err != nil {
								return err
							}
							if err := t.writePlacements(a, old, tl); err != nil {
								return err
							}
							if _, err := t.refresh(a, old, tl); err != nil {
								return err
							}
						}
					}
					_, err := t.sync(a)
					return err
				}); err != nil {
					return err
				}
			}
			if l.StockId != (uuid.UUID{}) {
				st, err := t.next.Stock().Get(t.ctx, app.StockGetRequest_builder{Ref: app.StockRef_builder{Id: pdid.Id(l.StockId).Bytes()}.Build()}.Build())
				if err != nil {
					return err
				}
				if err := t.moveStock(st, ask, "return", cid, at); err != nil {
					return err
				}
			}
			if _, err := t.next.CustodyLine().Patch(t.ctx, patch); err != nil {
				return err
			}
		}

		patch := app.CustodyPatchRequest_builder{Ref: req.GetRef(), DateUpdatedForce: z.Ptr(true)}.Build()
		if open == 0 {
			patch.SetStatus("returned")
			patch.SetReturnedAt(ts(at))
		}
		if out, err = t.next.Custody().Patch(t.ctx, patch); err != nil {
			return err
		}
		// What came back is out no more: what its hand-over held of the rest
		// of its time is let go of, for whatever waits for it (D53).
		k, err := t.custodyOf(cid.Uuid())
		if err != nil {
			return err
		}
		if err := t.letGo(k, back, still, at); err != nil {
			return err
		}
		if open > 0 || len(c.GetReservationId()) == 0 {
			return nil
		}

		// Everything is back, so the reservation it fulfilled is over and
		// whatever time it still held is free -- if it is still in use, as
		// it is read once this holds it (above, [Tx.lockReservations]),
		// with what it holds.
		r, err := t.db.Reservation.Query().Where(reservation.Id(uuidOf(c.GetReservationId())), reservation.TenantId(t.tenant.Uuid())).Only(t.ctx)
		if err != nil || r.Status != resInUse {
			return nil
		}
		if _, err := t.next.Reservation().Patch(t.ctx, app.ReservationPatchRequest_builder{
			Ref:              app.ReservationRef_builder{Id: pdid.Id(r.Id).Bytes()}.Build(),
			Status:           z.Ptr(resCompleted),
			DateUpdatedForce: z.Ptr(true),
		}.Build()); err != nil {
			return err
		}
		return t.settleAllocations(pdid.Id(r.Id), resCompleted)
	})
	if err == errDone {
		return s.CustodyServiceServer.Get(ctx, app.CustodyGetRequest_builder{Ref: req.GetRef()}.Build())
	}
	return out, err
}

// Extend moves a loan's due date -- and with it the time what it lent holds
// (D53): later takes more of what was booked of it, and sooner lets go of the
// rest. An issue so made a loan holds until it is due.
func (s domainCustody) Extend(ctx context.Context, req *app.CustodyExtendRequest) (*app.Custody, error) {
	var out *app.Custody
	err := s.tx(ctx, func(t *Tx) error {
		// The tree's lock first, as a hand-over and a return take it, and
		// the moment after it ([Tx.lockTree]): what is out is read as
		// nothing else changes it.
		if err := t.lockTree(); err != nil {
			return err
		}
		c, err := t.next.Custody().Get(t.ctx, app.CustodyGetRequest_builder{Ref: req.GetRef()}.Build())
		if err != nil {
			return err
		}
		if c.GetStatus() != "open" {
			return failed("이 지급은 %s 상태입니다", or(custodySay[c.GetStatus()], c.GetStatus()))
		}
		if !req.HasDueAt() || !req.GetDueAt().AsTime().After(t.now) {
			return invalid("due_at", "반납 기한은 지금 이후여야 합니다")
		}
		// What it holds the time of, and what a booking of any of it holds,
		// before it moves (hold.go).
		cid := idOf(c.GetId())
		hs, err := t.holdsOf(cid.Uuid())
		if err != nil {
			return err
		}
		things := make([]uuid.UUID, 0, len(hs))
		for _, h := range hs {
			things = append(things, h.ResourceId)
		}
		if err := t.holdThings(things, nil); err != nil {
			return err
		}
		if err := t.begin(nil, "custody.extend", cid, t.now, "", app.CustodyExtended_builder{Custody: c.GetId(), Due: req.GetDueAt()}.Build()); err != nil {
			return err
		}
		out, err = t.next.Custody().Patch(t.ctx, app.CustodyPatchRequest_builder{
			Ref:              req.GetRef(),
			DueAt:            req.GetDueAt(),
			Kind:             z.Ptr("loan"),
			OverdueNoticed:   z.Ptr(false),
			DateUpdatedForce: z.Ptr(true),
		}.Build())
		if err != nil {
			return err
		}
		k, err := t.custodyOf(cid.Uuid())
		if err != nil {
			return err
		}
		return t.rehold(k)
	})
	return out, err
}
