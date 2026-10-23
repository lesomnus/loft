package domain

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"time"
	"uuid"

	"github.com/lesomnus/otx/log"
	"github.com/lesomnus/payday/audit"
	"github.com/lesomnus/payday/past"
	"github.com/lesomnus/payday/pdid"
	"github.com/lesomnus/payday/spin"
	"github.com/protobuf-orm/ent/dialect"
	"github.com/protobuf-orm/ent/dialect/sql"
	"google.golang.org/protobuf/proto"

	app "github.com/lesomnus/rove"
	"github.com/lesomnus/rove/internal/ent"
	"github.com/lesomnus/rove/internal/ent/allocation"
	"github.com/lesomnus/rove/internal/ent/attachment"
	"github.com/lesomnus/rove/internal/ent/countfinding"
	"github.com/lesomnus/rove/internal/ent/custody"
	"github.com/lesomnus/rove/internal/ent/custodyline"
	"github.com/lesomnus/rove/internal/ent/inventorycount"
	"github.com/lesomnus/rove/internal/ent/link"
	"github.com/lesomnus/rove/internal/ent/placement"
	"github.com/lesomnus/rove/internal/ent/predicate"
	"github.com/lesomnus/rove/internal/ent/reservation"
	"github.com/lesomnus/rove/internal/ent/reservationitem"
	"github.com/lesomnus/rove/internal/ent/stewardship"
	"github.com/lesomnus/rove/internal/ent/stockmovement"
	"github.com/lesomnus/rove/internal/ent/workorder"
	"github.com/lesomnus/rove/server/pd"
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
// The facts and the events are payday's past, which takes them itself
// (`past.Expire`, payday#151), once Rove's own rows that name events have
// gone: an event only they named goes with them. What it keeps beside the
// window is [expiry]'s.
//
// `server` is the ungated stack, as for every sweep: this is the deployment's
// own work, and nobody's in the tenant.
func (d *Deps) Expire(ctx context.Context, server app.Server, drv dialect.Driver, tenant pdid.Id, dry bool) (Expired, error) {
	out := Expired{Tenant: tenant}
	var files []string
	err := d.System(ctx, server, drv, tenant, func(t *Tx) error {
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

		// The tenant's, before anything is read: two expiries of one
		// tenant take turns, as the past's own does (`past.Expire`).
		if err := t.lockTenant(); err != nil {
			return err
		}
		files, err = t.expire(out.Before, &out)
		if err != nil {
			return err
		}
		x, err := past.Expire(t.ctx, t.past, t.tenant, past.Window{Keep: w.Keep}, t.now, expiry(&out))
		if err != nil {
			return err
		}
		out.Facts, out.Events = x.Facts, x.Events
		if dry {
			return errDry
		}
		return nil
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

// expire deletes Rove's own rows the window no longer reaches, in an order no
// reference is left dangling by: a document's parts before it, and all of
// them before the past's facts and events, which [past.Expire] takes after.
// It answers the keys of the files the attachments that went kept.
//
// Each row that goes is on the trail as gone, in this transaction
// (`pd.RecordErased`, payday#115): the rows are taken past the servers, and a
// check of the rows would otherwise find every one a thing the trail says is
// a row and is not.
func (t *Tx) expire(c time.Time, out *Expired) ([]string, error) {
	tid := t.tenant.Uuid()
	removed := []pdid.Id{}
	var err error
	n := func(v int, e error) int {
		if e != nil && err == nil {
			err = e
		}
		return v
	}

	// The files kept for the documents that go, before the documents.
	atts := attachmentExpired(tid, c)
	files, err := t.db.Attachment.Query().Where(atts).Select(attachment.FieldObjectKey).Strings(t.ctx)
	if err != nil {
		return nil, err
	}
	out.Attachments = n(expireRows(t.ctx, &removed, t.db.Attachment.Query().Where(atts).Ids,
		func(ctx context.Context, ids ...uuid.UUID) (int, error) {
			return t.db.Attachment.Delete().Where(attachment.IdIn(ids...)).Exec(ctx)
		}))

	out.Placements = n(expireRows(t.ctx, &removed, t.db.Placement.Query().Where(placement.TenantId(tid), placementExpired(c)).Ids,
		func(ctx context.Context, ids ...uuid.UUID) (int, error) {
			return t.db.Placement.Delete().Where(placement.IdIn(ids...)).Exec(ctx)
		}))
	out.Links = n(expireRows(t.ctx, &removed, t.db.Link.Query().Where(link.TenantId(tid), linkExpired(c)).Ids,
		func(ctx context.Context, ids ...uuid.UUID) (int, error) {
			return t.db.Link.Delete().Where(link.IdIn(ids...)).Exec(ctx)
		}))
	out.Stewardships = n(expireRows(t.ctx, &removed, t.db.Stewardship.Query().Where(stewardship.TenantId(tid), stewardshipExpired(c)).Ids,
		func(ctx context.Context, ids ...uuid.UUID) (int, error) {
			return t.db.Stewardship.Delete().Where(stewardship.IdIn(ids...)).Exec(ctx)
		}))
	out.StockMovements = n(expireRows(t.ctx, &removed, t.db.StockMovement.Query().Where(stockmovement.TenantId(tid), stockmovement.OccurredAtLT(c)).Ids,
		func(ctx context.Context, ids ...uuid.UUID) (int, error) {
			return t.db.StockMovement.Delete().Where(stockmovement.IdIn(ids...)).Exec(ctx)
		}))
	if err != nil {
		return nil, err
	}

	n(expireRows(t.ctx, &removed, t.db.ReservationItem.Query().Where(reservationitem.TenantId(tid), reservationitem.HasReservationWith(reservationOver(c))).Ids,
		func(ctx context.Context, ids ...uuid.UUID) (int, error) {
			return t.db.ReservationItem.Delete().Where(reservationitem.IdIn(ids...)).Exec(ctx)
		}))
	n(expireRows(t.ctx, &removed, t.db.Allocation.Query().Where(allocation.TenantId(tid), predicate.Allocation(idIn(allocation.FieldRefId, allocation.FieldTenantId, reservation.Table, reservation.FieldId, reservation.FieldTenantId, reservationOver(c)))).Ids,
		func(ctx context.Context, ids ...uuid.UUID) (int, error) {
			return t.db.Allocation.Delete().Where(allocation.IdIn(ids...)).Exec(ctx)
		}))
	out.Reservations = n(expireRows(t.ctx, &removed, t.db.Reservation.Query().Where(reservation.TenantId(tid), reservationOver(c)).Ids,
		func(ctx context.Context, ids ...uuid.UUID) (int, error) {
			return t.db.Reservation.Delete().Where(reservation.IdIn(ids...)).Exec(ctx)
		}))

	n(expireRows(t.ctx, &removed, t.db.CustodyLine.Query().Where(custodyline.TenantId(tid), custodyline.HasCustodyWith(custodyOver(c))).Ids,
		func(ctx context.Context, ids ...uuid.UUID) (int, error) {
			return t.db.CustodyLine.Delete().Where(custodyline.IdIn(ids...)).Exec(ctx)
		}))
	out.Custodies = n(expireRows(t.ctx, &removed, t.db.Custody.Query().Where(custody.TenantId(tid), custodyOver(c)).Ids,
		func(ctx context.Context, ids ...uuid.UUID) (int, error) {
			return t.db.Custody.Delete().Where(custody.IdIn(ids...)).Exec(ctx)
		}))

	n(expireRows(t.ctx, &removed, t.db.CountFinding.Query().Where(countfinding.TenantId(tid), countfinding.HasCountWith(countOver(c))).Ids,
		func(ctx context.Context, ids ...uuid.UUID) (int, error) {
			return t.db.CountFinding.Delete().Where(countfinding.IdIn(ids...)).Exec(ctx)
		}))
	out.Counts = n(expireRows(t.ctx, &removed, t.db.InventoryCount.Query().Where(inventorycount.TenantId(tid), countOver(c)).Ids,
		func(ctx context.Context, ids ...uuid.UUID) (int, error) {
			return t.db.InventoryCount.Delete().Where(inventorycount.IdIn(ids...)).Exec(ctx)
		}))

	n(expireRows(t.ctx, &removed, t.db.Allocation.Query().Where(allocation.TenantId(tid), predicate.Allocation(idIn(allocation.FieldRefId, allocation.FieldTenantId, workorder.Table, workorder.FieldId, workorder.FieldTenantId, workOver(c)))).Ids,
		func(ctx context.Context, ids ...uuid.UUID) (int, error) {
			return t.db.Allocation.Delete().Where(allocation.IdIn(ids...)).Exec(ctx)
		}))
	out.WorkOrders = n(expireRows(t.ctx, &removed, t.db.WorkOrder.Query().Where(workorder.TenantId(tid), workOver(c)).Ids,
		func(ctx context.Context, ids ...uuid.UUID) (int, error) {
			return t.db.WorkOrder.Delete().Where(workorder.IdIn(ids...)).Exec(ctx)
		}))
	if err != nil {
		return nil, err
	}

	if _, err := pd.RecordErased(t.ctx, t.db, t.audit(), HistoryExpired, removed); err != nil {
		return nil, fmt.Errorf("the trail: %w", err)
	}
	return files, nil
}

// HistoryExpired is the act a row of the history taken at the end of its
// tenant's window is on the trail as.
const HistoryExpired = "rove.history.expire"

// audit is what the trail keeps of each kind's writes, for an act this
// writes past the servers: the deployment's, or nothing when it said none.
func (t *Tx) audit() audit.Contents {
	if t.deps == nil {
		return audit.Contents{}
	}
	return t.deps.Audit
}

// expireRows deletes the rows `ids` reads, by identifier and a page at a time, and
// answers how many went, adding each to `removed` for the trail to be told it
// went. By identifier rather than by the reading's own condition, so that what
// is told is what went: a row a write beside this made to match after it was
// read is left for the next pass.
func expireRows(ctx context.Context, removed *[]pdid.Id, ids func(context.Context) ([]uuid.UUID, error), del func(context.Context, ...uuid.UUID) (int, error)) (int, error) {
	vs, err := ids(ctx)
	if err != nil {
		return 0, err
	}
	n := 0
	for page := range slices.Chunk(vs, audit.RowsPage) {
		k, err := del(ctx, page...)
		if err != nil {
			return n, err
		}
		n += k
	}
	for _, v := range vs {
		*removed = append(*removed, pdid.Id(v))
	}
	return n, nil
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

// attachmentExpired is a file kept for a document that was over before `c`:
// a loan given back, a work order done, a count closed.
func attachmentExpired(tenant uuid.UUID, c time.Time) predicate.Attachment {
	return attachment.And(attachment.TenantId(tenant), attachment.Or(
		predicate.Attachment(idIn(attachment.FieldSubjectId, attachment.FieldTenantId, custody.Table, custody.FieldId, custody.FieldTenantId, custodyOver(c))),
		predicate.Attachment(idIn(attachment.FieldSubjectId, attachment.FieldTenantId, workorder.Table, workorder.FieldId, workorder.FieldTenantId, workOver(c))),
		predicate.Attachment(idIn(attachment.FieldSubjectId, attachment.FieldTenantId, countfinding.Table, countfinding.FieldId, countfinding.FieldTenantId, countfinding.HasCountWith(countOver(c)))),
	))
}

// expiry is what payday's past keeps beside a window, and the receipt of an
// expiry that took anything (design 8.2): the operation that registered an
// asset, which is the record of its acquisition, and the status that took one
// out of use, the record of its disposal. `rove` is what Rove's own rows lost
// first, which the receipt counts with the past's; nil for a preview, which
// records nothing. Each row the past takes is on the trail as the act Rove's
// own are, so that one expiry is one act.
func expiry(rove *Expired) past.Expiry {
	x := past.Expiry{
		Act:       HistoryExpired,
		KeepKinds: []string{"asset.add"},
		KeepFacts: []past.FactKeep{{
			Domain: pd.AssetDomain,
			Field:  assetDesc.Fields().ByName("status").Number(),
			Values: []string{`"retired"`, `"disposed"`, `"lost"`},
		}},
	}
	if rove == nil {
		return x
	}
	x.Receipt = func(v past.Expired) proto.Message {
		all := *rove
		all.Facts, all.Events = v.Facts, v.Events
		if all.Total() == 0 {
			return nil
		}
		return app.RetentionExpired_builder{
			Before:         ts(v.Before),
			Placements:     uint32(all.Placements),
			Links:          uint32(all.Links),
			Stewardships:   uint32(all.Stewardships),
			Facts:          uint32(all.Facts),
			Events:         uint32(all.Events),
			Reservations:   uint32(all.Reservations),
			Custodies:      uint32(all.Custodies),
			Counts:         uint32(all.Counts),
			WorkOrders:     uint32(all.WorkOrders),
			StockMovements: uint32(all.StockMovements),
			Attachments:    uint32(all.Attachments),
		}.Build()
	}
	return x
}

// going is the predicate Rove's own expiry deletes each of its rows that name
// events by, by its kind: what payday's past is told of them for a preview,
// which deletes nothing ([past.Preview]). A document's rows name no event.
var going = map[pdid.Domain]past.Going{
	pd.PlacementDomain:   func(c time.Time) func(*sql.Selector) { return placementExpired(c) },
	pd.LinkDomain:        func(c time.Time) func(*sql.Selector) { return linkExpired(c) },
	pd.StewardshipDomain: func(c time.Time) func(*sql.Selector) { return stewardshipExpired(c) },
	pd.StockMovementDomain: func(c time.Time) func(*sql.Selector) {
		return stockmovement.And(stockmovement.OccurredAtNotNil(), stockmovement.OccurredAtLT(c))
	},
}

// lockTenant holds the tenant until the transaction ends, on PostgreSQL: the
// lock payday's past takes it by for an expiry -- a transaction's advisory
// lock on the last eight bytes of its identifier -- which a transaction may
// take more than once.
func (t *Tx) lockTenant() error {
	if !t.postgres() {
		return nil
	}
	u := t.tenant.Uuid()
	key := int64(binary.BigEndian.Uint64(u[8:]))
	return t.drv.Exec(t.ctx, "SELECT pg_advisory_xact_lock($1)", []any{key}, nil)
}

// takes is what [Deps.Expire] would take before `c`, read and not taken: each
// kind by the predicate the expiry deletes it by, and the facts and the
// events as payday's past counts them (`past.Preview`), told what of Rove's
// own rows that name events would go. A count, so that asking takes no lock
// and writes nothing, however much there is: what a workspace's owner may
// ask for as often as they like (TenantService.Preview).
func (t *Tx) takes(c time.Time) (Expired, error) {
	tid := t.tenant.Uuid()
	out := Expired{Tenant: t.tenant, Before: c}
	var err error
	n := func(v int, e error) int {
		if e != nil && err == nil {
			err = e
		}
		return v
	}

	out.Attachments = n(t.db.Attachment.Query().Where(attachmentExpired(tid, c)).Count(t.ctx))
	out.Placements = n(t.db.Placement.Query().Where(placement.TenantId(tid), placementExpired(c)).Count(t.ctx))
	out.Links = n(t.db.Link.Query().Where(link.TenantId(tid), linkExpired(c)).Count(t.ctx))
	out.Stewardships = n(t.db.Stewardship.Query().Where(stewardship.TenantId(tid), stewardshipExpired(c)).Count(t.ctx))
	out.StockMovements = n(t.db.StockMovement.Query().Where(stockmovement.TenantId(tid), stockmovement.OccurredAtLT(c)).Count(t.ctx))
	out.Reservations = n(t.db.Reservation.Query().Where(reservation.TenantId(tid), reservationOver(c)).Count(t.ctx))
	out.Custodies = n(t.db.Custody.Query().Where(custody.TenantId(tid), custodyOver(c)).Count(t.ctx))
	out.Counts = n(t.db.InventoryCount.Query().Where(inventorycount.TenantId(tid), countOver(c)).Count(t.ctx))
	out.WorkOrders = n(t.db.WorkOrder.Query().Where(workorder.TenantId(tid), workOver(c)).Count(t.ctx))
	if err != nil {
		return out, err
	}

	// A window of any length that begins at `c`: the past derives where a
	// window begins from the window, and a cut said as one is what this is.
	st, err := t.store()
	if err != nil {
		return out, err
	}
	x, err := past.Preview(t.ctx, st, t.tenant, past.Window{Keep: time.Hour}, c.Add(time.Hour), expiry(nil), going)
	if err != nil {
		return out, err
	}
	out.Facts, out.Events = x.Facts, x.Events

	return out, nil
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
		log.From(ctx).WarnContext(ctx, "expire: tenants", slog.Any("err", err))
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
				slog.Any("err", err))
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
