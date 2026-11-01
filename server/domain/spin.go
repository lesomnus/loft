package domain

import (
	"context"
	"fmt"
	"log/slog"
	"time"
	"uuid"

	"github.com/lesomnus/otx/log"
	"github.com/lesomnus/payday/frame"
	"github.com/lesomnus/payday/past"
	"github.com/lesomnus/payday/pdid"
	"github.com/lesomnus/payday/spin"
	"github.com/lesomnus/z"
	"github.com/protobuf-orm/ent/dialect"
	"github.com/protobuf-orm/ent/dialect/sql"
	"google.golang.org/protobuf/proto"

	app "github.com/lesomnus/loft"
	"github.com/lesomnus/loft/internal/ent"
	"github.com/lesomnus/loft/internal/ent/allocation"
	"github.com/lesomnus/loft/internal/ent/asset"
	"github.com/lesomnus/loft/internal/ent/custody"
	"github.com/lesomnus/loft/internal/ent/custodyline"
	"github.com/lesomnus/loft/internal/ent/holder"
	"github.com/lesomnus/loft/internal/ent/notification"
	"github.com/lesomnus/loft/internal/ent/party"
	"github.com/lesomnus/loft/internal/ent/predicate"
	"github.com/lesomnus/loft/internal/ent/reservation"
	"github.com/lesomnus/loft/internal/ent/reservationitem"
	entsession "github.com/lesomnus/loft/internal/ent/session"
	"github.com/lesomnus/loft/internal/ent/tenantdomain"
	"github.com/lesomnus/loft/internal/ent/workorder"
)

// Sweeper is the deployment's own work in every tenant: what happens because
// time passed rather than because somebody asked (design 9.10) -- a hold that
// ran out, a room nobody came to, a loan that is late.
//
// It runs on the ungated stack, as nobody: its events have no actor, which is
// how the history tells "the system released it" from "somebody did". Every
// query it makes is narrowed to the tenant it is in by hand, as the domain's
// own reads are.
type Sweeper struct {
	Server app.Server
	Drv    dialect.Driver
	Deps   *Deps

	// Every is the wait between passes.
	Every time.Duration
}

var _ spin.Spinner = Sweeper{}

func (w Sweeper) SpinName() string { return "loft.sweep" }

func (w Sweeper) Spin(ctx context.Context) error {
	every := w.Every
	if every <= 0 {
		every = time.Minute
	}
	return spin.Every(every, w.Pass).Spin(ctx)
}

type sweep struct {
	name string
	f    func(t *Tx) error
}

var sweeps = []sweep{
	{"holds", sweepHolds},
	{"requests", sweepRequests},
	{"no-shows", sweepNoShows},
	{"finished", sweepFinished},
	{"kits", sweepKits},
	{"loans", sweepLoans},
	{"work", sweepWork},
	{"domains", sweepDomains},
}

// Pass runs every sweep in every tenant once. One that fails is logged and the
// rest go on: a tenant whose data trips a sweep is not a reason to stop the
// others, nor the server.
func (w Sweeper) Pass(ctx context.Context) error {
	db := ent.NewClient(ent.Driver(w.Drv))
	tenants, err := db.Tenant.Query().Ids(ctx)
	if err != nil {
		log.From(ctx).WarnContext(ctx, "sweep: tenants", slog.Any("err", err))
		return nil
	}
	for _, k := range tenants {
		tenant := pdid.Id(k)
		fctx := frame.Into(ctx, frame.New(pdid.Nil, tenant, frame.Grant{}).WithScope(frame.Only(tenant)))
		for _, s := range sweeps {
			if ctx.Err() != nil {
				return nil
			}
			if err := w.Deps.System(fctx, w.Server, w.Drv, tenant, s.f); err != nil {
				log.From(ctx).WarnContext(ctx, "sweep",
					slog.String("sweep", s.name),
					slog.String("tenant", tenant.String()),
					slog.Any("err", err))
			}
		}
	}
	return nil
}

// closeReservation moves a reservation to a state that takes no time -- if it
// is still what the sweep found it to be once this holds it
// ([Tx.lockReservations]): a check-in, a confirm or a pickup that went first is
// not undone by a sweep that read the reservation before it. It answers whether
// it did.
func (t *Tx) closeReservation(r *ent.Reservation, to, kind string, said proto.Message) (bool, error) {
	id := pdid.Id(r.Id)
	if err := t.lockReservations(id); err != nil {
		return false, err
	}
	now, err := t.db.Reservation.Query().Where(reservation.Id(r.Id), reservation.TenantId(t.tenant.Uuid())).Only(t.ctx)
	if err != nil {
		return false, err
	}
	if now.Status != r.Status {
		return false, nil
	}
	if err := t.begin(nil, kind, id, t.now, "", said); err != nil {
		return false, err
	}
	if err := t.lockAllocations(id); err != nil {
		return false, err
	}
	if _, err := t.next.Reservation().Patch(t.ctx, app.ReservationPatchRequest_builder{
		Ref:              app.ReservationRef_builder{Id: id.Bytes()}.Build(),
		Status:           z.Ptr(to),
		ExpiresAtNull:    z.Ptr(true),
		DateUpdatedForce: z.Ptr(true),
	}.Build()); err != nil {
		return false, err
	}
	return true, t.settleAllocations(id, to)
}

// sweepHolds lets go of holds nobody confirmed in time.
func sweepHolds(t *Tx) error {
	rs, err := t.db.Reservation.Query().Where(
		reservation.TenantId(t.tenant.Uuid()),
		reservation.Status(resHeld),
		reservation.ExpiresAtLT(t.now),
		reservation.DateErasedIsNil(),
	).Limit(200).All(t.ctx)
	if err != nil {
		return err
	}
	for _, r := range rs {
		if _, err := t.closeReservation(r, resExpired, "reservation.expire", app.ReservationReleased_builder{
			Reservation: pdid.Id(r.Id).Bytes(),
			Name:        r.Name,
			From:        resHeld,
		}.Build()); err != nil {
			return err
		}
	}
	return nil
}

// sweepRequests ends requests nobody decided before they began: a manager can
// no longer say yes to time that has started.
func sweepRequests(t *Tx) error {
	rs, err := t.db.Reservation.Query().Where(
		reservation.TenantId(t.tenant.Uuid()),
		reservation.Status(resRequested),
		reservation.BeginsAtLT(t.now),
		reservation.DateErasedIsNil(),
	).Limit(200).All(t.ctx)
	if err != nil {
		return err
	}
	for _, r := range rs {
		closed, err := t.closeReservation(r, resExpired, "reservation.expire", app.ReservationReleased_builder{
			Reservation: pdid.Id(r.Id).Bytes(),
			Name:        r.Name,
			From:        resRequested,
		}.Build())
		if err != nil {
			return err
		}
		if !closed {
			continue
		}
		if err := t.notifyParty(r.PartyId, "reservation.expired", "예약 만료: "+r.Name,
			"시작 전까지 승인되지 않아 예약이 만료되었습니다", pdid.Id(r.Id), "/reservations"); err != nil {
			return err
		}
	}
	return nil
}

// spacesOnly answers whether everything a reservation takes is a space, which
// is what is checked into rather than picked up.
func (t *Tx) spacesOnly(rid uuid.UUID) (bool, error) {
	items, err := t.db.ReservationItem.Query().Where(reservationitem.TenantId(t.tenant.Uuid()), reservationitem.ReservationId(rid)).All(t.ctx)
	if err != nil {
		return false, err
	}
	if len(items) == 0 {
		return false, nil
	}
	ids := []uuid.UUID{}
	for _, it := range items {
		ids = append(ids, it.ResourceId)
	}
	n, err := t.db.Asset.Query().Where(asset.TenantId(t.tenant.Uuid()), asset.IdIn(ids...), asset.KindNEQ("space")).Count(t.ctx)
	return n == 0, err
}

// sweepNoShows frees a room nobody checked into, once the grace is over, and
// equipment nobody picked up by the time it was due back (design 4).
func sweepNoShows(t *Tx) error {
	grace := t.settings().noShow
	if grace <= 0 {
		return nil
	}
	rs, err := t.db.Reservation.Query().Where(
		reservation.TenantId(t.tenant.Uuid()),
		reservation.Status(resConfirmed),
		reservation.BeginsAtLT(t.now.Add(-grace)),
		reservation.CheckedInAtIsNil(),
		reservation.DateErasedIsNil(),
	).Limit(200).All(t.ctx)
	if err != nil {
		return err
	}
	for _, r := range rs {
		spaces, err := t.spacesOnly(r.Id)
		if err != nil {
			return err
		}
		if !spaces && r.EndsAt.After(t.now) {
			// Equipment is picked up through a custody, any time until it is
			// due back.
			continue
		}
		closed, err := t.closeReservation(r, resNoShow, "reservation.no_show", app.ReservationReleased_builder{
			Reservation: pdid.Id(r.Id).Bytes(),
			Name:        r.Name,
		}.Build())
		if err != nil {
			return err
		}
		if !closed {
			continue
		}
		body := fmt.Sprintf("시작 후 %d분 안에 체크인하지 않아 예약이 해제되었습니다", int(grace.Minutes()))
		if !spaces {
			body = "기간 안에 수령하지 않아 예약이 해제되었습니다"
		}
		if err := t.notifyParty(r.PartyId, "reservation.no_show", "예약 해제: "+r.Name, body, pdid.Id(r.Id), "/reservations"); err != nil {
			return err
		}
	}
	return nil
}

// sweepFinished completes room reservations whose time is over. Equipment is
// completed by its return instead.
func sweepFinished(t *Tx) error {
	states := []string{resInUse}
	if t.settings().noShow <= 0 {
		// Nobody is held to checking in, so a confirmed room was used.
		states = append(states, resConfirmed)
	}
	rs, err := t.db.Reservation.Query().Where(
		reservation.TenantId(t.tenant.Uuid()),
		reservation.StatusIn(states...),
		reservation.EndsAtLT(t.now),
		reservation.DateErasedIsNil(),
	).Limit(200).All(t.ctx)
	if err != nil {
		return err
	}
	for _, r := range rs {
		spaces, err := t.spacesOnly(r.Id)
		if err != nil {
			return err
		}
		if !spaces {
			continue
		}
		if _, err := t.closeReservation(r, resCompleted, "reservation.complete", app.ReservationDecided_builder{
			Reservation: pdid.Id(r.Id).Bytes(),
			Name:        r.Name,
			Ended:       true,
		}.Build()); err != nil {
			return err
		}
	}
	return nil
}

// noticed answers whether a notice of this kind about this subject was left
// already, which is how a reminder is said once without a column for it.
func (t *Tx) noticed(kind string, subject uuid.UUID) (bool, error) {
	return t.db.Notification.Query().Where(
		notification.TenantId(t.tenant.Uuid()),
		notification.Kind(kind),
		notification.SubjectId(subject),
	).Exist(t.ctx)
}

// sweepLoans reminds whoever holds a loan the day before it is due, and tells
// them and the managers once it is late. And a loan late holds what it lent
// until it is back (D53): what was booked of it after its due says so, and is
// told ([Tx.rehold]). Found late is an operation of its own, each loan's
// ([Tx.overdue], D55), which what holding it so does is part of.
//
// A read-only workspace is told none of it (D67, [ReadOnlyTells]): each asks
// for a return it does not take. Its loans are found late all the same, once
// of each due -- `overdue_noticed` is that one was -- and once it writes
// again, what nobody was told is told, of what is so then ([Tx.lateUntold]);
// a reminder of a due is asked again every pass, and is told while it is so.
func sweepLoans(t *Tx) error {
	late, err := t.db.Custody.Query().Where(
		custody.TenantId(t.tenant.Uuid()),
		custody.Status("open"),
		custody.DueAtLT(t.now),
		custody.OverdueNoticed(false),
		custody.DateErasedIsNil(),
	).Limit(200).All(t.ctx)
	if err != nil {
		return err
	}
	if len(late) > 0 {
		// The tree's lock, which a return takes too, and then what a
		// booking of anything they lent holds, all at once (hold.go).
		if err := t.lockTree(); err != nil {
			return err
		}
		things := []uuid.UUID{}
		for _, c := range late {
			hs, err := t.holdsOf(c.Id)
			if err != nil {
				return err
			}
			for _, h := range hs {
				things = append(things, h.ResourceId)
			}
		}
		if err := t.holdThings(things, nil); err != nil {
			return err
		}
	}
	for _, c := range late {
		// As it is now that nothing changes it: returned, or given a later
		// due, meanwhile, it is not late.
		if c, err = t.custodyOf(c.Id); err != nil {
			return err
		}
		if c.Status != "open" || c.OverdueNoticed || c.DueAt == nil || !c.DueAt.Before(t.now) {
			continue
		}
		if err := t.overdue(c); err != nil {
			return err
		}
		if err := t.rehold(c); err != nil {
			return err
		}
		if err := t.tellLate(c); err != nil {
			return err
		}
		if _, err := t.next.Custody().Patch(t.ctx, app.CustodyPatchRequest_builder{
			Ref:              app.CustodyRef_builder{Id: pdid.Id(c.Id).Bytes()}.Build(),
			OverdueNoticed:   z.Ptr(true),
			DateUpdatedForce: z.Ptr(true),
		}.Build()); err != nil {
			return err
		}
	}
	// And what is late that nobody was told is: found so while the workspace
	// was read-only (D67).
	if err := t.lateUntold(len(late) > 0); err != nil {
		return err
	}

	soon, err := t.db.Custody.Query().Where(
		custody.TenantId(t.tenant.Uuid()),
		custody.Status("open"),
		custody.DueAtGTE(t.now),
		custody.DueAtLT(t.now.Add(24*time.Hour)),
		custody.DateErasedIsNil(),
	).Limit(200).All(t.ctx)
	if err != nil {
		return err
	}
	for _, c := range soon {
		ok, err := t.noticed("custody.due", c.Id)
		if err != nil || ok {
			if err != nil {
				return err
			}
			continue
		}
		if err := t.notifyParty(c.PartyId, "custody.due", "반납 예정 알림",
			fmt.Sprintf("%s까지 반납해 주세요", t.when(*c.DueAt)), pdid.Id(c.Id), "/custody"); err != nil {
			return err
		}
	}
	return nil
}

// overdue records that `c` was found past its due with some of what it lent
// not back (D55): the loan's event, at the moment it was found so -- what it
// holds says the due it missed -- and one of each asset of it still out,
// whose cause it is, so that the loan's history and each thing's say when it
// was late. Nobody is in either: whose loan it is is the loan's to say. What
// finding it late does after this is part of it: a reservation that waits
// for what it holds waits under it ([Tx.changed]).
func (t *Tx) overdue(c *ent.Custody) error {
	said := func(asset []byte) *app.CustodyOverdue {
		return app.CustodyOverdue_builder{Custody: pdid.Id(c.Id).Bytes(), Due: ts(*c.DueAt), Asset: asset}.Build()
	}
	if err := t.begin(nil, "custody.overdue", pdid.Id(c.Id), t.now, "", said(nil)); err != nil {
		return err
	}
	ls, err := t.db.CustodyLine.Query().Where(
		custodyline.TenantId(t.tenant.Uuid()),
		custodyline.CustodyId(c.Id),
	).Order(custodyline.ById()).All(t.ctx)
	if err != nil {
		return err
	}
	kids := []past.Child{}
	for _, l := range ls {
		if l.AssetId == (uuid.UUID{}) || l.Quantity-l.ReturnedQuantity <= 0 {
			continue
		}
		a := pdid.Id(l.AssetId)
		kids = append(kids, past.Child{Event: past.Event{Kind: "custody.overdue", Subject: a, At: t.now, Value: said(a.Bytes())}})
	}
	_, err = t.op.Batch(t.ctx, kids)
	return err
}

// tellLate tells whoever has loan `c` that it is late, and whoever manages
// it -- and whoever has it that a booking waits for what it lent, when one
// does.
func (t *Tx) tellLate(c *ent.Custody) error {
	body := fmt.Sprintf("%s에 반납하기로 한 물품이 아직 반납되지 않았습니다", t.when(*c.DueAt))
	waited, err := t.waitedFor(c)
	if err != nil {
		return err
	}
	if waited {
		body += ". 이 물품을 잡은 예약이 기다리고 있습니다"
	}
	name := t.partyName(c.PartyId)
	if err := t.notifyParty(c.PartyId, "custody.overdue", "반납 기한이 지났습니다", body, pdid.Id(c.Id), "/custody"); err != nil {
		return err
	}
	return t.notifyManagers("custody.overdue", "연체: "+name,
		fmt.Sprintf("%s 반납 예정이던 대여가 연체되었습니다", t.when(*c.DueAt)), pdid.Id(c.Id), "/custody")
}

// lateUntold tells of the loans found late that nobody was told are: found
// so while the workspace was read-only, which is told of none (D67), and
// told once it writes again -- the first pass it does, of what is so then,
// so that a loan given back meanwhile, or given a later due, is told of
// nothing. Whether a loan was told is whether a notice of its being late was
// written after its due: one told of a due before, and given a later one
// since, was told nothing of this one. Each is told as it would have been as
// it was found late ([Tx.tellLate]), and with what its being late did to the
// bookings of what it lent that is so still, which finding it so told then
// ([Tx.retellLate]). `locked` is that this transaction holds the tree's lock
// already.
//
// A workspace with nobody to manage it -- its last owner left, with nobody
// to hand it on to -- is told nothing, and asked nothing again each pass: a
// loan is told by a notice to its managers, which is not written while there
// are none.
func (t *Tx) lateUntold(locked bool) error {
	quiet, err := t.readonly()
	if err != nil || quiet {
		return err
	}
	untold := func() ([]*ent.Custody, error) {
		return t.db.Custody.Query().Where(
			custody.TenantId(t.tenant.Uuid()),
			custody.Status("open"),
			custody.DueAtLT(t.now),
			custody.OverdueNoticed(true),
			custody.DateErasedIsNil(),
			untoldOf("custody.overdue"),
		).Order(custody.ByDueAt(), custody.ById()).Limit(200).All(t.ctx)
	}
	cs, err := untold()
	if err != nil || len(cs) == 0 {
		return err
	}
	managed, err := t.db.Holder.Query().Where(
		holder.TenantId(t.tenant.Uuid()),
		holder.RoleIn("owner", "admin", "manager"),
		holder.DateErasedIsNil(),
	).Exist(t.ctx)
	if err != nil || !managed {
		return err
	}
	if !locked {
		// The tree's lock, which a return takes too, and then the loans as
		// they are now that nothing changes them: given back meanwhile, or
		// told by another pass that went first.
		if err := t.lockTree(); err != nil {
			return err
		}
		if cs, err = untold(); err != nil {
			return err
		}
	}
	for _, c := range cs {
		if err := t.retellLate(c); err != nil {
			return err
		}
		if err := t.tellLate(c); err != nil {
			return err
		}
	}
	return nil
}

// untoldOf is a loan of which no notice of `kind` was written after its due:
// nobody was told of the due it missed.
func untoldOf(kind string) predicate.Custody {
	return func(s *sql.Selector) {
		b := sql.Dialect(s.Dialect())
		told := b.Table(notification.Table).As("told")
		s.Where(sql.NotExists(
			b.Select(told.C(notification.FieldId)).From(told).Where(sql.And(
				sql.ColumnsEQ(told.C(notification.FieldTenantId), s.C(custody.FieldTenantId)),
				sql.EQ(told.C(notification.FieldKind), kind),
				sql.ColumnsEQ(told.C(notification.FieldSubjectId), s.C(custody.FieldId)),
				sql.ColumnsGT(told.C(notification.FieldDateCreated), s.C(custody.FieldDueAt)),
			)),
		))
	}
}

// retellLate tells, of a loan found late while its workspace was read-only
// and told only now (D67), what its being late did to the bookings of what
// it lent that is so still -- as finding it so told it then ([Tx.taken]):
// whoever runs the reservations, and whoever each is for, that what it holds
// is out over the time of the bookings that take it from now on
// ([Tx.tellOut]); and, of the pools around what it lent, the bookings that
// wait for some of one and that nobody was told wait ([Tx.tellShort]).
func (t *Tx) retellLate(c *ent.Custody) error {
	hs, err := t.holdsOf(c.Id)
	if err != nil || len(hs) == 0 {
		return err
	}
	ss := make([]outSpan, 0, len(hs))
	things := make([]uuid.UUID, 0, len(hs))
	for _, h := range hs {
		things = append(things, h.ResourceId)
		if h.EndsAt.After(t.now) {
			ss = append(ss, outSpan{thing: h.ResourceId, from: t.now, to: h.EndsAt})
		}
	}
	if len(ss) > 0 {
		by, err := t.takers(ss)
		if err != nil {
			return err
		}
		if err := t.tellOut(c, by); err != nil {
			return err
		}
	}
	ws, err := t.waitingUntold(things)
	if err != nil {
		return err
	}
	return t.tellShort(ws)
}

// waitingUntold answers the bookings that wait for some of a pool around
// these things ([Tx.circle]) and that nobody was told wait: no notice of a
// pool's being short was written about the booking since it began to (D67)
// -- of the pool, one told since may have been of another booking of it. The
// one that has waited longest first.
func (t *Tx) waitingUntold(things []uuid.UUID) ([]poolWait, error) {
	around, err := t.circle(things)
	if err != nil || len(around) == 0 {
		return nil, err
	}
	of := make(map[uuid.UUID]*resource, len(around))
	ids := make([]uuid.UUID, 0, len(around))
	for _, p := range around {
		of[p.asset.Id] = p
		ids = append(ids, p.asset.Id)
	}
	as, err := t.db.Allocation.Query().Where(
		allocation.TenantId(t.tenant.Uuid()),
		allocation.ResourceIdIn(ids...),
		allocation.Kind(allocWaiting),
		allocation.EndsAtGT(t.now),
		allocation.RefIdNotNil(),
	).Order(allocation.ByDateCreated(), allocation.ById()).All(t.ctx)
	if err != nil || len(as) == 0 {
		return nil, err
	}
	out := []poolWait{}
	for _, a := range as {
		r, err := t.db.Reservation.Query().Where(reservation.Id(*a.RefId), reservation.TenantId(t.tenant.Uuid())).Only(t.ctx)
		if ent.IsNotFound(err) {
			continue
		}
		if err != nil {
			return nil, err
		}
		if !waits(r.Status) || !r.EndsAt.After(t.now) || r.DateErased != nil {
			continue
		}
		told, err := t.db.Notification.Query().Where(
			notification.TenantId(t.tenant.Uuid()),
			notification.Kind("reservation.conflict"),
			notification.SubjectId(r.Id),
			notification.DateCreatedGTE(a.DateUpdated),
		).Exist(t.ctx)
		if err != nil {
			return nil, err
		}
		if told {
			continue
		}
		out = append(out, poolWait{r: r, pool: of[a.ResourceId], units: a.Units, from: later(a.BeginsAt, t.now)})
	}
	return out, nil
}

func (t *Tx) partyName(id uuid.UUID) string {
	p, err := t.db.Party.Query().Where(party.Id(id), party.TenantId(t.tenant.Uuid())).Only(t.ctx)
	if err != nil {
		return "-"
	}
	return p.Name
}

// sweepWork tells the managers about scheduled work a day ahead.
func sweepWork(t *Tx) error {
	ws, err := t.db.WorkOrder.Query().Where(
		workorder.TenantId(t.tenant.Uuid()),
		workorder.Status("scheduled"),
		workorder.BeginsAtLT(t.now.Add(24*time.Hour)),
		workorder.DateErasedIsNil(),
	).Limit(200).All(t.ctx)
	if err != nil {
		return err
	}
	for _, w := range ws {
		ok, err := t.noticed("work.due", w.Id)
		if err != nil {
			return err
		}
		if ok {
			continue
		}
		when := "오늘"
		if w.BeginsAt != nil {
			when = t.when(*w.BeginsAt)
		}
		if err := t.notifyManagers("work.due", "예정된 작업: "+w.Name, when+" 예정입니다", pdid.Id(w.Id), "/work"); err != nil {
			return err
		}
	}
	return nil
}

// sweepDomains asks about the DNS of a workspace's custom domains (loft#155):
//
//   - A pending one every few minutes for a week from when it was added, and
//     a lapsed one for a week from when it lapsed. One whose TXT record holds
//     its token and that points here is verified, and made active when the
//     workspace has no active domain -- a lapsed one comes back so -- and left
//     for a person to switch to otherwise: changing the host labels are
//     printed with is a decision.
//   - The active one every [RecheckEvery], which lapses once its DNS said no
//     [RecheckFailures] times in a row, over [RecheckSpan] ([Tx.lapse]).
func sweepDomains(t *Tx) error {
	if t.deps.LookupTXT == nil {
		return nil
	}
	week := t.now.Add(-7 * 24 * time.Hour)
	ds, err := t.db.TenantDomain.Query().Where(
		tenantdomain.TenantId(t.tenant.Uuid()),
		tenantdomain.Or(
			tenantdomain.And(tenantdomain.State(domainPending), tenantdomain.DateCreatedGT(week)),
			tenantdomain.And(tenantdomain.State(domainLapsed), tenantdomain.DateLapsedGT(week)),
		),
		tenantdomain.DateUpdatedLT(t.now.Add(-5*time.Minute)),
		tenantdomain.DateErasedIsNil(),
	).Limit(20).All(t.ctx)
	if err != nil {
		return err
	}
	for _, d := range ds {
		if t.deps.isHere(t.ctx, d.Host, d.Token) != heardYes {
			// Looked at, so the next look is five minutes from now.
			if _, err := t.next.TenantDomain().Patch(t.ctx, app.TenantDomainPatchRequest_builder{
				Ref:              app.TenantDomainRef_builder{Id: pdid.Id(d.Id).Bytes()}.Build(),
				DateUpdatedForce: z.Ptr(true),
			}.Build()); err != nil {
				return err
			}
			continue
		}
		if _, err := t.verified(pdid.Id(d.Id).Bytes(), d.Purpose, d.Host, d.State == domainLapsed, true); err != nil {
			return err
		}
	}
	return t.recheckDomains()
}

// recheckDomains asks again about the active custom domain's DNS, once every
// [RecheckEvery]: a yes starts the count of noes again, a no is counted, and
// an answer that says nothing is not.
func (t *Tx) recheckDomains() error {
	ds, err := t.db.TenantDomain.Query().Where(
		tenantdomain.TenantId(t.tenant.Uuid()),
		tenantdomain.State(domainActive),
		tenantdomain.Source("custom"),
		tenantdomain.Or(tenantdomain.CheckedAtIsNil(), tenantdomain.CheckedAtLTE(t.now.Add(-RecheckEvery))),
		tenantdomain.DateErasedIsNil(),
	).Limit(20).All(t.ctx)
	if err != nil {
		return err
	}
	for _, d := range ds {
		patch := app.TenantDomainPatchRequest_builder{
			Ref:              app.TenantDomainRef_builder{Id: pdid.Id(d.Id).Bytes()}.Build(),
			CheckedAt:        ts(t.now),
			DateUpdatedForce: z.Ptr(true),
		}.Build()
		switch t.deps.isHere(t.ctx, d.Host, d.Token) {
		case heardYes:
			patch.SetFailures(0)
			patch.SetFailingSinceNull(true)
		case heardNo:
			since := t.now
			if d.FailingSince != nil {
				since = *d.FailingSince
			}
			n := d.Failures + 1
			if n >= RecheckFailures && !t.now.Before(since.Add(RecheckSpan)) {
				if err := t.lapse(d, n, since); err != nil {
					return err
				}
				continue
			}
			patch.SetFailures(n)
			patch.SetFailingSince(ts(since))
		}
		if _, err := t.next.TenantDomain().Patch(t.ctx, patch); err != nil {
			return err
		}
	}
	return nil
}

// lapse takes an active domain whose DNS no longer says it is here out of
// use (loft#155). No sign-in is handed to it and no certificate asked for it
// (`/internal/tls-ask`), the sessions handed to it end, and links to the
// workspace are made at the service's address again (`apphost.Links`); labels
// printed with it still open while it points here. Whoever may act on it is
// told, by mail as well. It is active again only once its TXT record verifies
// again: the sweep asks for a week, and a person may at any time (`Verify`).
func (t *Tx) lapse(d *ent.TenantDomain, failures uint32, since time.Time) error {
	words := domainWords(d.Purpose)
	// Why is the deployment's to say, and is what the event holds: no
	// person's words, which a reason is (payday#151).
	if err := t.begin(nil, "domain.lapse", pdid.Nil, t.now, "", app.TenantDomainLapsed_builder{
		Domain:       pdid.Id(d.Id).Bytes(),
		Host:         d.Host,
		Purpose:      d.Purpose,
		FailingSince: ts(since),
		Failures:     failures,
	}.Build()); err != nil {
		return err
	}
	if _, err := t.next.TenantDomain().Patch(t.ctx, app.TenantDomainPatchRequest_builder{
		Ref:              app.TenantDomainRef_builder{Id: pdid.Id(d.Id).Bytes()}.Build(),
		State:            z.Ptr(domainLapsed),
		DateLapsed:       ts(t.now),
		CheckedAt:        ts(t.now),
		Failures:         z.Ptr(failures),
		FailingSince:     ts(since),
		DateUpdatedForce: z.Ptr(true),
	}.Build()); err != nil {
		return err
	}
	// The sessions handed to it: every session bound to this workspace is
	// one handed to its domain (`server/session`). A session is no act on the
	// trail, and is written through ent, as its own package writes it.
	if _, err := t.db.Session.Delete().Where(entsession.Workspace(t.tenant.Uuid())).Exec(t.ctx); err != nil {
		return err
	}
	return t.notifyAdmins("domain.lapsed", words+" 사용 중지: "+d.Host,
		"DNS가 "+t.when(since)+"부터 이 서비스를 가리키지 않거나 _loft-challenge TXT 레코드가 없어 사용을 멈췄습니다. 링크와 로그인은 서비스 주소로 돌아갔고, 이 주소로 인쇄한 라벨은 DNS가 이곳을 가리키는 동안 계속 열립니다. 레코드를 되돌리면 일주일 동안 저절로 다시 확인하고, 설정에서 바로 확인할 수도 있습니다.",
		pdid.Id(d.Id), "/settings")
}
