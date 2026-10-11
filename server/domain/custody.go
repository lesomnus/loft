package domain

import (
	"context"
	"fmt"
	"slices"
	"time"
	"uuid"

	"github.com/lesomnus/payday/pdid"
	"github.com/lesomnus/z"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	app "github.com/lesomnus/rove"
	"github.com/lesomnus/rove/internal/ent/custodyline"
	"github.com/lesomnus/rove/internal/ent/reservation"
	"github.com/lesomnus/rove/server/pd"
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
	var out *app.Custody
	err := s.tx(ctx, func(t *Tx) error {
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
		if req.HasDueAt() && !req.GetDueAt().AsTime().After(t.now) {
			return invalid("due_at", "반납 기한은 지금 이후여야 합니다")
		}

		id := pdid.New(pd.CustodyDomain)
		if err := t.lockTree(); err != nil {
			return err
		}
		if err := t.begin(req.GetOp(), "custody.issue", id, t.now, map[string]string{"issue": "지급", "loan": "대여"}[kind]+": {party}", req.GetDesc(), map[string]string{"lines": fmt.Sprint(len(req.GetLines())), "party": idOf(p.GetId()).String()}); err != nil {
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
		if len(req.GetReservationId()) > 0 {
			// A pickup fulfils a reservation: it is in use from now, and what
			// it held is now held by this custody instead.
			r, err := t.next.Reservation().Get(t.ctx, app.ReservationGetRequest_builder{Ref: app.ReservationRef_builder{Id: req.GetReservationId()}.Build()}.Build())
			if err != nil {
				return err
			}
			if r.GetStatus() != resConfirmed {
				return failed("예약이 %s 상태입니다. 확정된 예약만 수령할 수 있습니다", or(reservationSay[r.GetStatus()], r.GetStatus()))
			}
			if _, err := t.next.Reservation().Patch(t.ctx, app.ReservationPatchRequest_builder{
				Ref:              app.ReservationRef_builder{Id: r.GetId()}.Build(),
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
				if slices.Contains([]string{"disposed", "lost", "retired"}, a.GetStatus()) {
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

		if err := t.notifyParty(uuidOf(p.GetId()), "custody.issued", "인수 확인 요청",
			fmt.Sprintf("%d건을 받으셨다면 인수 확인을 눌러 주세요", len(req.GetLines())), id, "/custody"); err != nil {
			return err
		}
		out = c
		return nil
	})
	if err == errDone {
		return nil, status.Error(codes.AlreadyExists, "이미 처리된 지급입니다")
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
		if err := t.begin(nil, "custody.acknowledge", idOf(c.GetId()), t.now, "인수 확인", "", nil); err != nil {
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

		var to *app.Asset
		if req.HasTo() {
			if to, _, err = t.get(req.GetTo(), "to"); err != nil {
				return err
			}
		}
		if err := t.lockTree(); err != nil {
			return err
		}
		if err := t.begin(req.GetOp(), "custody.return", cid, at, "반납", "", nil); err != nil {
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
						if err := t.checkPlacements(a, tl, old); err != nil {
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
				if _, err := t.sync(a); err != nil {
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
		if open > 0 || len(c.GetReservationId()) == 0 {
			return nil
		}

		// Everything is back, so the reservation it fulfilled is over and
		// whatever time it still held is free.
		r, err := t.db.Reservation.Query().Where(reservation.Id(uuidOf(c.GetReservationId())), reservation.TenantId(t.tenant.Uuid())).Only(t.ctx)
		if err != nil || r.Status != resInUse {
			return nil
		}
		if err := t.lockAllocations(pdid.Id(r.Id)); err != nil {
			return err
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
	return out, err
}

// Extend moves a loan's due date.
func (s domainCustody) Extend(ctx context.Context, req *app.CustodyExtendRequest) (*app.Custody, error) {
	var out *app.Custody
	err := s.tx(ctx, func(t *Tx) error {
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
		if err := t.begin(nil, "custody.extend", idOf(c.GetId()), t.now, "반납 기한 연장: "+req.GetDueAt().AsTime().Format(time.DateOnly), "", nil); err != nil {
			return err
		}
		out, err = t.next.Custody().Patch(t.ctx, app.CustodyPatchRequest_builder{
			Ref:              req.GetRef(),
			DueAt:            req.GetDueAt(),
			Kind:             z.Ptr("loan"),
			OverdueNoticed:   z.Ptr(false),
			DateUpdatedForce: z.Ptr(true),
		}.Build())
		return err
	})
	return out, err
}
