package domain

import (
	"context"
	"time"
	"uuid"

	"github.com/lesomnus/payday/frame"
	"github.com/lesomnus/payday/pdid"
	"github.com/protobuf-orm/ent/dialect/sql"

	"github.com/lesomnus/rove/internal/ent"
	"github.com/lesomnus/rove/internal/ent/audit"
	"github.com/lesomnus/rove/internal/ent/countfinding"
	"github.com/lesomnus/rove/internal/ent/custody"
	"github.com/lesomnus/rove/internal/ent/custodyline"
	"github.com/lesomnus/rove/internal/ent/event"
	"github.com/lesomnus/rove/internal/ent/inventorycount"
	"github.com/lesomnus/rove/internal/ent/predicate"
	"github.com/lesomnus/rove/internal/ent/reservation"
	"github.com/lesomnus/rove/internal/ent/reservationitem"
	"github.com/lesomnus/rove/internal/ent/stockmovement"
	"github.com/lesomnus/rove/internal/ent/workorder"
	"github.com/lesomnus/rove/server/bare"
	"github.com/lesomnus/rove/server/retention"
)

// View is the scope that keeps what a caller reads through the generated
// servers inside the view window of the tenant they are in (design 8.1). It is
// installed on the walled servers beside the wall.
//
// It covers what a predicate can say on its own: the events, the trail of the
// history, and the documents that are over -- a reservation finished, a loan
// given back, a count closed, a work order done, a stock movement -- before the
// window began. A document still open is the present, however old it is.
//
// The time rows are not here, and cannot be: the domain layer supersedes them
// through these same servers, and an asset voided today supersedes rows that
// ended years ago, which a scope would make not found to the write as much as
// to the read. A caller reads them only through Timeline, QueryAt and Diff,
// which keep to the window themselves; see `policy`. Nor are the allocations,
// which a late return or a late completion settles however long ago they were.
func View(db *ent.Client, deps *Deps) bare.Scope { return view{db: db, deps: deps} }

type view struct {
	bare.Unscoped

	db   *ent.Client
	deps *Deps
}

// sinces is when the view window of each tenant the caller may see begins; a
// tenant whose window is all of its history is left out. `all` is a caller
// nothing narrows.
func (v view) sinces(ctx context.Context) (map[uuid.UUID]time.Time, bool, error) {
	ts, all, err := frame.Narrow(ctx)
	if all || err != nil {
		return nil, all, err
	}

	var d retention.Defaults
	if v.deps != nil {
		d = v.deps.Retention
	}
	now := v.deps.now()
	out := map[uuid.UUID]time.Time{}
	for _, t := range ts {
		w, err := retention.Of(ctx, v.db, pdid.Id(t), now, d)
		if err != nil {
			return nil, false, err
		}
		if s := w.Since(now); !s.IsZero() {
			out[t] = s
		}
	}

	return out, false, nil
}

// within narrows rows to each tenant's window: a row of the tenant is in it
// when `in` says so, and a row of another is left to that tenant's.
func within[P ~func(*sql.Selector)](
	v view, ctx context.Context,
	other func(uuid.UUID) P, in func(time.Time) P,
	or, and func(...P) P,
) (P, error) {
	ss, all, err := v.sinces(ctx)
	if all || err != nil || len(ss) == 0 {
		return nil, err
	}

	ps := make([]P, 0, len(ss))
	for t, s := range ss {
		ps = append(ps, or(other(t), in(s)))
	}

	return and(ps...), nil
}

func (v view) EventScope(ctx context.Context) (predicate.Event, error) {
	return within(v, ctx, event.TenantIdNEQ, retention.EventIn, event.Or, event.And)
}

func (v view) AuditScope(ctx context.Context) (predicate.Audit, error) {
	return within(v, ctx, audit.TenantIdNEQ, retention.AuditIn, audit.Or, audit.And)
}

func (v view) ReservationScope(ctx context.Context) (predicate.Reservation, error) {
	return within(v, ctx, reservation.TenantIdNEQ, func(s time.Time) predicate.Reservation {
		return reservation.Not(reservationOver(s))
	}, reservation.Or, reservation.And)
}

func (v view) ReservationItemScope(ctx context.Context) (predicate.ReservationItem, error) {
	return within(v, ctx, reservationitem.TenantIdNEQ, func(s time.Time) predicate.ReservationItem {
		return reservationitem.Not(reservationitem.HasReservationWith(reservationOver(s)))
	}, reservationitem.Or, reservationitem.And)
}

func (v view) CustodyScope(ctx context.Context) (predicate.Custody, error) {
	return within(v, ctx, custody.TenantIdNEQ, func(s time.Time) predicate.Custody {
		return custody.Not(custodyOver(s))
	}, custody.Or, custody.And)
}

func (v view) CustodyLineScope(ctx context.Context) (predicate.CustodyLine, error) {
	return within(v, ctx, custodyline.TenantIdNEQ, func(s time.Time) predicate.CustodyLine {
		return custodyline.Not(custodyline.HasCustodyWith(custodyOver(s)))
	}, custodyline.Or, custodyline.And)
}

func (v view) InventoryCountScope(ctx context.Context) (predicate.InventoryCount, error) {
	return within(v, ctx, inventorycount.TenantIdNEQ, func(s time.Time) predicate.InventoryCount {
		return inventorycount.Not(countOver(s))
	}, inventorycount.Or, inventorycount.And)
}

func (v view) CountFindingScope(ctx context.Context) (predicate.CountFinding, error) {
	return within(v, ctx, countfinding.TenantIdNEQ, func(s time.Time) predicate.CountFinding {
		return countfinding.Not(countfinding.HasCountWith(countOver(s)))
	}, countfinding.Or, countfinding.And)
}

func (v view) WorkOrderScope(ctx context.Context) (predicate.WorkOrder, error) {
	return within(v, ctx, workorder.TenantIdNEQ, func(s time.Time) predicate.WorkOrder {
		return workorder.Not(workOver(s))
	}, workorder.Or, workorder.And)
}

func (v view) StockMovementScope(ctx context.Context) (predicate.StockMovement, error) {
	return within(v, ctx, stockmovement.TenantIdNEQ, stockmovement.OccurredAtGTE, stockmovement.Or, stockmovement.And)
}

// reservationOver is a reservation that came to an end before `s`. One still
// held, asked for or in use has not, whatever its time says.
func reservationOver(s time.Time) predicate.Reservation {
	return reservation.And(
		reservation.StatusIn(resCompleted, resCancelled, resRejected, resExpired, resNoShow),
		reservation.EndsAtLT(s),
	)
}

// custodyOver is a hand-over all of which was back before `s`. A loan still
// out is the present, however long ago it was made.
func custodyOver(s time.Time) predicate.Custody {
	return custody.And(custody.Status("returned"), custody.ReturnedAtNotNil(), custody.ReturnedAtLT(s))
}

// countOver is a count closed before `s`.
func countOver(s time.Time) predicate.InventoryCount {
	return inventorycount.And(inventorycount.Status("closed"), inventorycount.ClosedAtNotNil(), inventorycount.ClosedAtLT(s))
}

// workOver is a work order done or called off before `s`.
func workOver(s time.Time) predicate.WorkOrder {
	return workorder.And(
		workorder.StatusIn("done", "cancelled"),
		workorder.Or(
			workorder.And(workorder.CompletedAtNotNil(), workorder.CompletedAtLT(s)),
			workorder.And(workorder.CompletedAtIsNil(), workorder.DateUpdatedLT(s)),
		),
	)
}
