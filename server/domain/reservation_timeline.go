package domain

import (
	"context"
	"sort"
	"uuid"

	"github.com/lesomnus/payday/past"
	"github.com/lesomnus/payday/pdid"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	app "github.com/lesomnus/rove"
	"github.com/lesomnus/rove/internal/ent/custody"
)

// Timeline answers a reservation's history, newest first (D45): what was done
// with it -- booked, decided, checked into, let go of -- and what it took and
// let go of as what it booked changed (D39), and the hand-overs that fulfilled
// it, its pickup and its return (design 4) -- every event the reservation is
// the subject of, and every one of those hand-overs'.
//
// Each is said as it is read (payday#151), in the workspace's zone, its people
// as they are called now, on the access record of the call that shows them
// (design 8.2); and why, in the words of whoever asked, on it too, as payday's
// past reads one. What an operation over many things did to it is said with
// that operation, the event's cause (design D34): what a change to a kit it
// booked did to it, under the change.
//
// It is read by the people it is theirs and by the people who decide on it: a
// member reads their own, as they act on their own ([Tx.mineOrManager]), and
// whoever runs the register or audits it reads anybody's ([Tx.readsHistory]).
func (s domainReservation) Timeline(ctx context.Context, req *app.ReservationTimelineRequest) (*app.ReservationTimelineResponse, error) {
	t, err := s.read(ctx)
	if err != nil {
		return nil, err
	}
	// Through the wall: another workspace's is none.
	r, err := t.next.Reservation().Get(ctx, app.ReservationGetRequest_builder{Ref: req.GetRef()}.Build())
	if err != nil {
		return nil, err
	}
	if err := t.readsHistory(r); err != nil {
		return nil, err
	}
	rid := idOf(r.GetId())

	// And what fulfilled it: equipment is picked up as a loan, and the
	// reservation is over once all of it is back (design 4). What became of
	// the loan -- acknowledged, extended, back -- is that loan's, and so the
	// reservation's.
	cs, err := t.db.Custody.Query().Where(custody.TenantId(t.tenant.Uuid()), custody.ReservationId(rid.Uuid())).Ids(ctx)
	if err != nil {
		return nil, err
	}
	subjects := []pdid.Id{rid}
	for _, c := range cs {
		subjects = append(subjects, pdid.Id(c))
	}

	st, err := t.store()
	if err != nil {
		return nil, err
	}
	evs, err := past.Events(ctx, st, past.EventQuery{Subjects: subjects})
	if err != nil {
		return nil, err
	}
	byId := map[uuid.UUID]past.EventRow{}
	for _, v := range evs {
		byId[v.Id.Uuid()] = v
	}
	// The operations what was done to it was part of, which are about
	// something else: a kit, the occurrence of its series a decision was
	// made on.
	var causes []pdid.Id
	for _, v := range evs {
		if _, ok := byId[v.Cause.Uuid()]; !ok && !v.Cause.IsZero() {
			causes = append(causes, v.Cause)
		}
	}
	more, err := t.events(st, causes)
	if err != nil {
		return nil, err
	}
	causeOf := map[uuid.UUID]past.EventRow{}
	for _, v := range more {
		causeOf[v.Id.Uuid()] = v
	}

	names := namer{t: t}
	if err := names.before(byId, causeOf); err != nil {
		return nil, err
	}
	rd := t.reading(&names)
	held := details{n: &names, events: byId, say: rd.Say}
	if err := held.read(evs); err != nil {
		return nil, err
	}
	entries := make([]*app.TimelineEntry, 0, len(evs))
	for _, v := range evs {
		e := entry("event", v.Id.Uuid(), v.At, nil, v.Recorded, nil, v.Id.Uuid())
		e.SetSummary(rd.Say(v))
		e.SetEventKind(v.Kind)
		e.SetReason(v.Reason)
		e.SetActor(names.actorOf(v.Actor))
		c, ok := byId[v.Cause.Uuid()]
		if !ok {
			c, ok = causeOf[v.Cause.Uuid()]
		}
		if ok && !v.Cause.IsZero() {
			e.SetCauseId(v.Cause.Bytes())
			// Not where it says no more than the entry: a series cancelled
			// from one of it on says of each what it says of the first.
			if said := rd.Say(c); said != e.GetSummary() {
				e.SetCause(said)
			}
		}
		if l, ok := names.legacies[v.Id.Uuid()]; ok && l.payload != "" {
			e.SetDetail(decode(l.payload))
		} else if d, err := held.of(v); err != nil {
			return nil, err
		} else if len(d) > 0 {
			e.SetDetail(d)
		}
		entries = append(entries, e)
	}
	if err := rd.Err(); err != nil {
		return nil, err
	}

	sort.SliceStable(entries, func(i, j int) bool {
		a, b := entries[i], entries[j]
		if !a.GetValidFrom().AsTime().Equal(b.GetValidFrom().AsTime()) {
			return a.GetValidFrom().AsTime().After(b.GetValidFrom().AsTime())
		}
		return a.GetRecordedAt().AsTime().After(b.GetRecordedAt().AsTime())
	})
	return app.ReservationTimelineResponse_builder{Entries: entries}.Build(), nil
}

// readsHistory lets a person read the history of a reservation that is
// theirs -- for them, or asked for by them -- and whoever runs the register
// or audits it anybody's: who booked what for whom, who said no and why, is
// the people's whose reservation it is and theirs who decide on it.
func (t *Tx) readsHistory(r *app.Reservation) error {
	if role := t.roleOf(); manages(role) || role == "auditor" {
		return nil
	}
	if by := idOf(r.GetRequestedBy()); !by.IsZero() && by == t.actor {
		return nil
	}
	if me, err := t.partyOf(); err == nil && pdid.Id(me.Id) == idOf(r.GetParty().GetId()) {
		return nil
	}
	return status.Error(codes.PermissionDenied, "다른 사람의 예약 이력은 볼 수 없습니다")
}
