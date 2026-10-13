package domain

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/lesomnus/otx/log"
	"github.com/lesomnus/payday/frame"
	"github.com/lesomnus/payday/pdid"
	"github.com/lesomnus/payday/spin"
	"github.com/protobuf-orm/ent/dialect"
	"github.com/protobuf-orm/ent/dialect/sql"

	app "github.com/lesomnus/rove"
	"github.com/lesomnus/rove/internal/ent"
	"github.com/lesomnus/rove/internal/ent/allocation"
	"github.com/lesomnus/rove/internal/ent/attachment"
	"github.com/lesomnus/rove/internal/ent/countfinding"
	"github.com/lesomnus/rove/internal/ent/custody"
	"github.com/lesomnus/rove/internal/ent/custodyline"
	"github.com/lesomnus/rove/internal/ent/event"
	"github.com/lesomnus/rove/internal/ent/fact"
	"github.com/lesomnus/rove/internal/ent/inventorycount"
	"github.com/lesomnus/rove/internal/ent/link"
	"github.com/lesomnus/rove/internal/ent/placement"
	"github.com/lesomnus/rove/internal/ent/predicate"
	"github.com/lesomnus/rove/internal/ent/reservation"
	"github.com/lesomnus/rove/internal/ent/reservationitem"
	"github.com/lesomnus/rove/internal/ent/stewardship"
	"github.com/lesomnus/rove/internal/ent/stockmovement"
	"github.com/lesomnus/rove/internal/ent/workorder"
)

// Expired is what an expiry took, or would take, out of one tenant's history:
// the rows its keep window no longer reaches (design 8.2).
type Expired struct {
	Tenant pdid.Id

	// Before is where the keep window begins. What was over before it is gone;
	// the zero time is a window that keeps everything, and nothing is.
	Before time.Time

	// Held is what the legal holds on the tenant are for. While there is one,
	// nothing is taken, whatever the window says.
	Held []string

	// The time rows, and the events nothing left points at.
	Placements, Links, Stewardships, Facts, Events int

	// The documents that were over before the window, with their parts: a
	// reservation's items and the time it held, a loan's lines, a count's
	// findings, a work order's blocks. And the stock movements.
	Reservations, Custodies, Counts, WorkOrders, StockMovements int

	// Attachments is the files kept for the documents that went, which went
	// with them, and Lost the stored files of those that could not be removed
	// and are no row's any more.
	Attachments int
	Lost        []string
}

// Total is how many rows, documents counted once with their parts.
func (x Expired) Total() int {
	return x.Placements + x.Links + x.Stewardships + x.Facts + x.Events +
		x.Reservations + x.Custodies + x.Counts + x.WorkOrders + x.StockMovements +
		x.Attachments
}

// Expire takes out of a tenant's history what its keep window no longer
// reaches, in one transaction -- or, `dry`, does all of it and undoes it, so
// that what a dry run says is what a run would do, to the row.
//
// What goes is design 8.2's. A time row that ended, or was superseded, before
// the window began; a row the window begins inside of is the state it begins
// in and stays. A fact the attribute had stopped having by then, except the
// status that took an asset out of use. An event before the window that no
// row left points at, except the one that registered an asset. A document
// that was over before the window: a document still open is the present,
// however old.
//
// Nothing of a tenant under a legal hold goes, and nothing of one kept
// forever. The receipt is an event that says how many of what, and before
// when -- never what they said. It is the same work every time it runs:
// what a pass has taken is not there to take again.
//
// `server` is the ungated stack, as for every sweep: this is the deployment's
// own work, and nobody's in the tenant.
func (d *Deps) Expire(ctx context.Context, server app.Server, drv dialect.Driver, tenant pdid.Id, dry bool) (Expired, error) {
	out := Expired{Tenant: tenant}
	var files []string
	fctx := frame.Into(ctx, frame.New(pdid.Nil, tenant, frame.Grant{}).WithScope(frame.Only(tenant)))
	err := d.System(fctx, server, drv, tenant, func(t *Tx) error {
		w, err := t.window()
		if err != nil {
			return err
		}
		if w.Keep <= 0 {
			return nil
		}

		out.Before = t.now.Add(-w.Keep)
		if w.Held() {
			out.Held = w.Holds
			return nil
		}

		files, err = t.expire(out.Before, &out)
		if err != nil {
			return err
		}
		if dry {
			return errDry
		}
		if out.Total() == 0 {
			return nil
		}

		return t.begin(nil, "retention.expired", pdid.Nil, t.now,
			fmt.Sprintf("보존 기간이 지난 이력 %d건 삭제 (%s 이전)", out.Total(), out.Before.In(Zone).Format("2006년 1월 2일")),
			"", map[string]string{
				"before":          out.Before.UTC().Format(time.RFC3339),
				"placements":      fmt.Sprint(out.Placements),
				"links":           fmt.Sprint(out.Links),
				"stewardships":    fmt.Sprint(out.Stewardships),
				"facts":           fmt.Sprint(out.Facts),
				"events":          fmt.Sprint(out.Events),
				"reservations":    fmt.Sprint(out.Reservations),
				"custodies":       fmt.Sprint(out.Custodies),
				"counts":          fmt.Sprint(out.Counts),
				"work_orders":     fmt.Sprint(out.WorkOrders),
				"stock_movements": fmt.Sprint(out.StockMovements),
				"attachments":     fmt.Sprint(out.Attachments),
			})
	})
	if errors.Is(err, errDry) {
		return out, nil
	}
	if err != nil {
		return out, err
	}

	// The files, once nothing points at them; a file that stays is one
	// nobody can reach, and is said so.
	for _, k := range files {
		if d.Files == nil || d.Files.Delete(ctx, k) != nil {
			out.Lost = append(out.Lost, k)
		}
	}

	return out, nil
}

// expire deletes, in an order no reference is left dangling by: the rows that
// point at events before the events, a document's parts before it. It answers
// the keys of the files the attachments that went kept.
func (t *Tx) expire(c time.Time, out *Expired) ([]string, error) {
	tid := t.tenant.Uuid()
	var err error
	n := func(v int, e error) int {
		if e != nil && err == nil {
			err = e
		}
		return v
	}

	// The files kept for the documents that go, before the documents.
	atts := attachment.And(attachment.TenantId(tid), attachment.Or(
		predicate.Attachment(idIn(attachment.FieldSubjectId, attachment.FieldTenantId, custody.Table, custody.FieldId, custody.FieldTenantId, custodyOver(c))),
		predicate.Attachment(idIn(attachment.FieldSubjectId, attachment.FieldTenantId, workorder.Table, workorder.FieldId, workorder.FieldTenantId, workOver(c))),
		predicate.Attachment(idIn(attachment.FieldSubjectId, attachment.FieldTenantId, countfinding.Table, countfinding.FieldId, countfinding.FieldTenantId, countfinding.HasCountWith(countOver(c)))),
	))
	files, err := t.db.Attachment.Query().Where(atts).Select(attachment.FieldObjectKey).Strings(t.ctx)
	if err != nil {
		return nil, err
	}
	out.Attachments = n(t.db.Attachment.Delete().Where(atts).Exec(t.ctx))

	out.Placements = n(t.db.Placement.Delete().Where(placement.TenantId(tid), placementExpired(c)).Exec(t.ctx))
	out.Links = n(t.db.Link.Delete().Where(link.TenantId(tid), linkExpired(c)).Exec(t.ctx))
	out.Stewardships = n(t.db.Stewardship.Delete().Where(stewardship.TenantId(tid), stewardshipExpired(c)).Exec(t.ctx))
	out.Facts = n(t.db.Fact.Delete().Where(fact.TenantId(tid), factExpired(c)).Exec(t.ctx))
	out.StockMovements = n(t.db.StockMovement.Delete().Where(stockmovement.TenantId(tid), stockmovement.OccurredAtLT(c)).Exec(t.ctx))
	if err != nil {
		return nil, err
	}

	n(t.db.ReservationItem.Delete().Where(reservationitem.TenantId(tid), reservationitem.HasReservationWith(reservationOver(c))).Exec(t.ctx))
	n(t.db.Allocation.Delete().Where(allocation.TenantId(tid), predicate.Allocation(idIn(allocation.FieldRefId, allocation.FieldTenantId, reservation.Table, reservation.FieldId, reservation.FieldTenantId, reservationOver(c)))).Exec(t.ctx))
	out.Reservations = n(t.db.Reservation.Delete().Where(reservation.TenantId(tid), reservationOver(c)).Exec(t.ctx))

	n(t.db.CustodyLine.Delete().Where(custodyline.TenantId(tid), custodyline.HasCustodyWith(custodyOver(c))).Exec(t.ctx))
	out.Custodies = n(t.db.Custody.Delete().Where(custody.TenantId(tid), custodyOver(c)).Exec(t.ctx))

	n(t.db.CountFinding.Delete().Where(countfinding.TenantId(tid), countfinding.HasCountWith(countOver(c))).Exec(t.ctx))
	out.Counts = n(t.db.InventoryCount.Delete().Where(inventorycount.TenantId(tid), countOver(c)).Exec(t.ctx))

	n(t.db.Allocation.Delete().Where(allocation.TenantId(tid), predicate.Allocation(idIn(allocation.FieldRefId, allocation.FieldTenantId, workorder.Table, workorder.FieldId, workorder.FieldTenantId, workOver(c)))).Exec(t.ctx))
	out.WorkOrders = n(t.db.WorkOrder.Delete().Where(workorder.TenantId(tid), workOver(c)).Exec(t.ctx))
	if err != nil {
		return nil, err
	}

	out.Events = n(t.db.Event.Delete().Where(event.TenantId(tid), eventExpired(c)).Exec(t.ctx))
	return files, err
}

func placementExpired(c time.Time) predicate.Placement {
	return placement.Or(
		placement.And(placement.ValidToNotNil(), placement.ValidToLTE(c)),
		placement.And(placement.SupersededAtNotNil(), placement.SupersededAtLTE(c)),
	)
}

func linkExpired(c time.Time) predicate.Link {
	return link.Or(
		link.And(link.ValidToNotNil(), link.ValidToLTE(c)),
		link.And(link.SupersededAtNotNil(), link.SupersededAtLTE(c)),
	)
}

func stewardshipExpired(c time.Time) predicate.Stewardship {
	return stewardship.Or(
		stewardship.And(stewardship.ValidToNotNil(), stewardship.ValidToLTE(c)),
		stewardship.And(stewardship.SupersededAtNotNil(), stewardship.SupersededAtLTE(c)),
	)
}

// factExpired is a fact superseded before `c`, or a value its attribute had
// stopped having by then -- a later one, known by `c` and still believed, had
// begun. Known by `c` because an as-of read inside the window asks what was
// believed then, and until a later value was recorded this one was it.
//
// The status that took an asset out of use stays: it is the record of its
// disposal, which no window ends (design 8.2).
func factExpired(c time.Time) predicate.Fact {
	return fact.Or(
		fact.And(fact.SupersededAtNotNil(), fact.SupersededAtLTE(c)),
		fact.And(
			fact.Not(fact.And(fact.Key("status"), fact.ValueIn("retired", "disposed", "lost"))),
			func(s *sql.Selector) {
				b := sql.Dialect(s.Dialect())
				g := b.Table(fact.Table).As("later")
				s.Where(sql.Exists(b.Select(g.C(fact.FieldId)).From(g).Where(sql.And(
					sql.ColumnsEQ(g.C(fact.FieldTenantId), s.C(fact.FieldTenantId)),
					sql.ColumnsEQ(g.C(fact.FieldAssetId), s.C(fact.FieldAssetId)),
					sql.ColumnsEQ(g.C(fact.FieldKey), s.C(fact.FieldKey)),
					sql.IsNull(g.C(fact.FieldSupersededAt)),
					sql.LTE(g.C(fact.FieldDateCreated), c),
					sql.ColumnsGT(g.C(fact.FieldValidFrom), s.C(fact.FieldValidFrom)),
					sql.LTE(g.C(fact.FieldValidFrom), c),
				))))
			},
		),
	)
}

// eventExpired is an event that happened and was recorded before `c` and that
// no row left points at. The one that registered an asset stays: it is the
// record of its acquisition (design 8.2).
func eventExpired(c time.Time) predicate.Event {
	return event.And(
		event.OccurredAtLT(c),
		event.DateCreatedLT(c),
		event.KindNEQ("asset.add"),
		func(s *sql.Selector) {
			b := sql.Dialect(s.Dialect())
			for _, r := range [][2]string{
				{placement.Table, placement.FieldEventId},
				{link.Table, link.FieldEventId},
				{stewardship.Table, stewardship.FieldEventId},
				{fact.Table, fact.FieldEventId},
				{stockmovement.Table, stockmovement.FieldEventId},
			} {
				t := b.Table(r[0])
				s.Where(sql.NotExists(b.Select(t.C(r[1])).From(t).Where(sql.ColumnsEQ(t.C(r[1]), s.C(event.FieldId)))))
			}
		},
	)
}

// idIn is a row whose `column` -- a plain identifier, not an edge -- names
// one of the rows of `table` that `over` matches, in the row's own tenant: the
// time a reservation held, the file kept for a loan.
func idIn[P ~func(*sql.Selector)](column, tenant, table, id, theirs string, over P) func(*sql.Selector) {
	return func(s *sql.Selector) {
		b := sql.Dialect(s.Dialect())
		t := b.Table(table)
		q := b.Select(t.C(id)).From(t).Where(sql.ColumnsEQ(t.C(theirs), s.C(tenant)))
		over(q)
		s.Where(sql.In(s.C(column), q))
	}
}

// Expirer is [Deps.Expire] in every tenant, every so often: what `serve` runs
// when the deployment has said a window may destroy (`app.retention.apply`).
type Expirer struct {
	Server app.Server
	Drv    dialect.Driver
	Deps   *Deps

	// Every is the wait between passes.
	Every time.Duration
}

var _ spin.Spinner = Expirer{}

func (w Expirer) SpinName() string { return "rove.expire" }

func (w Expirer) Spin(ctx context.Context) error {
	every := w.Every
	if every <= 0 {
		every = time.Hour
	}
	return spin.Every(every, w.Pass).Spin(ctx)
}

// Pass expires every tenant once. One that fails is logged and the rest go on.
func (w Expirer) Pass(ctx context.Context) error {
	db := ent.NewClient(ent.Driver(w.Drv))
	tenants, err := db.Tenant.Query().Ids(ctx)
	if err != nil {
		log.From(ctx).WarnContext(ctx, "expire: tenants", slog.String("error", err.Error()))
		return nil
	}
	for _, k := range tenants {
		if ctx.Err() != nil {
			return nil
		}
		x, err := w.Deps.Expire(ctx, w.Server, w.Drv, pdid.Id(k), false)
		if err != nil {
			log.From(ctx).WarnContext(ctx, "expire",
				slog.String("tenant", pdid.Id(k).String()),
				slog.String("error", err.Error()))
			continue
		}
		if x.Total() > 0 {
			log.From(ctx).InfoContext(ctx, "expire",
				slog.String("tenant", x.Tenant.String()),
				slog.String("before", x.Before.UTC().Format(time.RFC3339)),
				slog.Int("rows", x.Total()))
		}
	}
	return nil
}
