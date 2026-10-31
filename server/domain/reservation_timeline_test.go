package domain_test

import (
	"context"
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/lesomnus/payday/access"
	"github.com/lesomnus/payday/pdid"
	"google.golang.org/grpc/codes"
	"google.golang.org/protobuf/types/known/timestamppb"

	app "github.com/lesomnus/loft"
	"github.com/lesomnus/loft/server/pd"
)

// A reservation's history (D45) is every event it is the subject of and every
// one of the hand-overs that fulfilled it, newest first, each said as it is
// read: its people as they are called now, and what an operation over many
// things did to it under that operation.

// reservationTimeline is a reservation's history as `ctx` reads it.
func (e *env) reservationTimeline(ctx context.Context, r *app.Reservation) []*app.TimelineEntry {
	e.t.Helper()
	v, err := e.timelineOf(ctx, r)
	e.x.NoError(err)
	return v
}

func (e *env) timelineOf(ctx context.Context, r *app.Reservation) ([]*app.TimelineEntry, error) {
	v, err := e.app().Reservation().Timeline(ctx, app.ReservationTimelineRequest_builder{Ref: ref[app.ReservationRef](r.GetId())}.Build())
	return v.GetEntries(), err
}

// kindsOf is the kind of each entry's event.
func kindsOf(es []*app.TimelineEntry) []string {
	out := []string{}
	for _, en := range es {
		out = append(out, en.GetEventKind())
	}
	return out
}

// TestAReservationsHistorySaysWhatWasDoneWithIt: equipment that asks for an
// approval, booked by a member, approved, picked up as a loan, acknowledged
// and returned says each in its history, newest first -- the pickup and what
// became of it as the loan's that fulfilled it -- by whom and why, each
// person as they are called now; and nothing of what the booking and the
// hand-over did to the things, which is theirs.
func TestAReservationsHistorySaysWhatWasDoneWithIt(t *testing.T) {
	e := newEnv(t)
	x := e.x
	camera := e.item("카메라", "CM-1", nil, day)
	e.bookable(camera, func(r *app.BookableAddRequest) { r.SetApproval(true) })
	kim, kimParty := e.person("member")
	boss, bossParty := e.person("manager")

	r, err := e.reserve(kim, camera, e.tomorrow(9, 0), e.tomorrow(12, 0), func(r *app.ReservationAddRequest) { r.SetName("제품 촬영") })
	x.NoError(err)
	x.Equal("requested", r.GetStatus())
	e.now = e.now.Add(time.Minute)
	_, err = e.app().Reservation().Approve(boss, app.ReservationDecideRequest_builder{Ref: ref[app.ReservationRef](r.GetId()), Reason: "촬영 일정 확인"}.Build())
	x.NoError(err)
	e.now = e.now.Add(time.Minute)
	loan, err := e.app().Custody().Add(boss, app.CustodyAddRequest_builder{
		Party:         ref[app.PartyRef](kimParty.GetId()),
		DueAt:         timestamppb.New(e.tomorrow(12, 0)),
		ReservationId: r.GetId(),
		Lines:         []*app.CustodyLineSpec{app.CustodyLineSpec_builder{Asset: assetRef(camera)}.Build()},
	}.Build())
	x.NoError(err)
	e.now = e.now.Add(time.Minute)
	_, err = e.app().Custody().Acknowledge(kim, app.CustodyAcknowledgeRequest_builder{Ref: ref[app.CustodyRef](loan.GetId())}.Build())
	x.NoError(err)
	e.now = e.now.Add(time.Minute)
	_, err = e.app().Custody().Return(boss, app.CustodyReturnRequest_builder{Ref: ref[app.CustodyRef](loan.GetId())}.Build())
	x.NoError(err)
	got, err := e.app().Reservation().Get(kim, app.ReservationGetRequest_builder{Ref: ref[app.ReservationRef](r.GetId())}.Build())
	x.NoError(err)
	x.Equal("completed", got.GetStatus(), "over once all of it is back")

	es := e.reservationTimeline(kim, r)
	x.Equal([]string{"custody.return", "custody.acknowledge", "custody.issue", "reservation.approve", "reservation.add"}, kindsOf(es))
	x.Equal([]string{
		"반납",
		"인수 확인",
		"대여: " + kimParty.GetName(),
		"예약 승인: 제품 촬영",
		"예약: 카메라 " + begins(r),
	}, summaries(es))
	x.Equal([]string{bossParty.GetName(), kimParty.GetName(), bossParty.GetName(), bossParty.GetName(), kimParty.GetName()}, actorsOf(es))
	x.Equal("촬영 일정 확인", es[3].GetReason(), "why, in the words of whoever decided")
	for _, en := range es {
		x.Equal("event", en.GetKind())
		x.Empty(en.GetCauseId(), "%s is an operation's own", en.GetEventKind())
	}
	x.Equal(pdid.Id(idUUID(loan.GetId())).String(), es[2].GetDetail()["custody"], "the loan, which has no name of its own")
	x.Equal("제품 촬영", es[2].GetDetail()["reservation"], "the booking it fulfilled, by its name")

	// Whoever is named is on the access record of the call that names them,
	// and so is the decision whose reason it shows.
	rctx, call := access.Into(kim, "test")
	_, err = e.timelineOf(rctx, r)
	x.NoError(err)
	_, carried := call.Unsaid()
	approved := e.about("reservation.approve")[idUUID(r.GetId())]
	x.NotNil(approved)
	for _, id := range []pdid.Id{pdid.Id(idUUID(kimParty.GetId())), pdid.Id(idUUID(bossParty.GetId())), pdid.Id(approved.Id)} {
		x.Contains(carried, access.Subject{Id: id, Tenant: e.tenant})
	}

	// Called what they are called now.
	_, err = e.app().Party().Update(e.owner, app.PartyUpdateRequest_builder{Ref: ref[app.PartyRef](kimParty.GetId()), Name: "김 대리"}.Build())
	x.NoError(err)
	es = e.reservationTimeline(kim, r)
	x.Equal("대여: 김 대리", es[2].GetSummary())
	x.Equal("김 대리", es[4].GetActor())
}

// actorsOf is who did each entry's event.
func actorsOf(es []*app.TimelineEntry) []string {
	out := []string{}
	for _, en := range es {
		out = append(out, en.GetActor())
	}
	return out
}

// TestAReservationsHistoryIsItsPeoplesAndTheirsWhoDecideOnIt: a member reads
// the history of their own reservation, made by them or for them, and nobody
// else's; whoever runs the register, or audits it, reads anybody's. Another
// workspace's is none at all.
func TestAReservationsHistoryIsItsPeoplesAndTheirsWhoDecideOnIt(t *testing.T) {
	e := newEnv(t)
	x := e.x
	room := e.space("회의실", nil)
	e.bookable(room, nil)
	kim, kimParty := e.person("member")
	lee, _ := e.person("member")
	boss, _ := e.person("manager")
	admin, _ := e.person("admin")
	auditor, _ := e.person("auditor")

	byKim, err := e.reserve(kim, room, e.tomorrow(9, 0), e.tomorrow(10, 0), nil)
	x.NoError(err)
	forKim, err := e.reserve(boss, room, e.tomorrow(10, 0), e.tomorrow(11, 0), func(r *app.ReservationAddRequest) {
		r.SetParty(ref[app.PartyRef](kimParty.GetId()))
	})
	x.NoError(err)

	for _, r := range []*app.Reservation{byKim, forKim} {
		for who, ctx := range map[string]context.Context{"its member": kim, "a manager": boss, "an admin": admin, "an auditor": auditor, "the owner": e.owner} {
			es, err := e.timelineOf(ctx, r)
			x.NoError(err, who)
			x.Equal([]string{"reservation.add"}, kindsOf(es), who)
		}
		_, err := e.timelineOf(lee, r)
		x.Equal(codes.PermissionDenied, codeOf(err), "somebody else's")
	}

	_, other := e.newTenant("beta")
	_, err = e.timelineOf(other, byKim)
	x.Equal(codes.NotFound, codeOf(err), "beyond the wall")
}

// TestWhatAChangeToAKitDidToABookingIsSaidUnderTheChange: a member given to a
// booked kit is taken by the booking, which its history says under the change
// that did it, by whoever made it -- the entry naming the change's event,
// which the member's history names too.
func TestWhatAChangeToAKitDidToABookingIsSaidUnderTheChange(t *testing.T) {
	e := newEnv(t)
	x := e.x
	camera := e.item("카메라", "CM-1", nil, day)
	tripod := e.item("삼각대", "TR-1", nil, day)
	e.bookable(tripod, nil)
	kit := e.kit("촬영 키트", camera)
	kim, _ := e.person("member")

	r, err := e.reserve(kim, kit, e.tomorrow(9, 0), e.tomorrow(12, 0), func(r *app.ReservationAddRequest) { r.SetName("제품 촬영") })
	x.NoError(err)
	e.now = e.now.Add(time.Minute)
	e.relate(tripod, kit, true)
	relate := e.about("asset.relate")[idUUID(tripod.GetId())]
	x.NotNil(relate)

	es := e.reservationTimeline(kim, r)
	x.Equal([]string{"reservation.take", "reservation.add"}, kindsOf(es))
	took := es[0]
	x.Equal(fmt.Sprintf("예약에 잡힘: 삼각대 (제품 촬영 %s)", begins(r)), took.GetSummary())
	x.Equal(fmt.Sprintf("TR-1 연결 %s (구성품 / 소속)", e.get(kit).GetTag()), took.GetCause())
	x.Equal(relate.Id, idUUID(took.GetCauseId()))
	x.Equal("Owner of acme", took.GetActor())
	x.Equal("TR-1 삼각대", took.GetDetail()["asset"])

	member := e.happened(tripod, "reservation.take")
	x.Len(member, 1)
	x.Equal(took.GetCauseId(), member[0].GetCauseId(), "one change, on both")
}

// TestEachTimeASeriesTakesPlaceHasAHistoryOfItsOwn: each time a series takes
// place is a reservation of its own, booked by the series and said by when it
// takes place, under the booking -- which says when the series begins and how
// often it takes place. A decision made of it from one on is on each it
// moved: the one it was made on its own, and the rest under it, saying no
// more than it does; and the feed says it once.
func TestEachTimeASeriesTakesPlaceHasAHistoryOfItsOwn(t *testing.T) {
	e := newEnv(t)
	x := e.x
	room := e.space("회의실", nil)
	e.bookable(room, func(r *app.BookableAddRequest) { r.SetHorizonDays(120) })

	first, err := e.reserve(e.owner, room, e.tomorrow(10, 0), e.tomorrow(11, 0), func(r *app.ReservationAddRequest) {
		r.SetName("주간 회의")
		r.SetRepeat("FREQ=WEEKLY;COUNT=3")
	})
	x.NoError(err)
	list, err := e.app().Reservation().List(e.owner, app.ReservationListRequest_builder{
		Filters: []*app.ReservationFilter{app.ReservationFilter_builder{SeriesId: first.GetSeriesId()}.Build()},
	}.Build())
	x.NoError(err)
	all := list.GetItems()
	x.Len(all, 3)
	slices.SortFunc(all, func(a, b *app.Reservation) int { return a.GetBeginsAt().AsTime().Compare(b.GetBeginsAt().AsTime()) })
	x.Equal(first.GetId(), all[0].GetId())

	booked, kids := e.split(e.eventsOf("reservation.add"))
	x.Equal(idUUID(first.GetId()), *booked.SubjectId)
	x.Len(kids, 3, "the room, and each time after the first")
	series := fmt.Sprintf("반복 예약: 회의실 %s부터 3회", begins(first))
	for i, r := range all {
		es := e.reservationTimeline(e.owner, r)
		x.Equal([]string{"reservation.add"}, kindsOf(es))
		if i == 0 {
			x.Equal(series, es[0].GetSummary())
			x.Empty(es[0].GetCauseId(), "the booking's own")
			continue
		}
		x.Contains(kids, idUUID(r.GetId()))
		x.Equal(fmt.Sprintf("예약: 회의실 %s", begins(r)), es[0].GetSummary(), "by when it takes place")
		x.Equal(series, es[0].GetCause())
		x.Equal(booked.Id, idUUID(es[0].GetCauseId()))
	}

	// Cancelled from the second on.
	e.now = e.now.Add(time.Minute)
	_, err = e.app().Reservation().Cancel(e.owner, app.ReservationDecideRequest_builder{Ref: ref[app.ReservationRef](all[1].GetId()), Series: true, Reason: "일정 변경"}.Build())
	x.NoError(err)
	cancel, undone := e.split(e.eventsOf("reservation.cancel"))
	x.Equal(idUUID(all[1].GetId()), *cancel.SubjectId, "the decision's own is of the one it was made on")
	x.Len(undone, 1)
	x.Contains(undone, idUUID(all[2].GetId()))
	x.Equal([]string{"reservation.add"}, kindsOf(e.reservationTimeline(e.owner, all[0])), "the first is as it was")
	for i, r := range all[1:] {
		es := e.reservationTimeline(e.owner, r)
		x.Equal([]string{"reservation.cancel", "reservation.add"}, kindsOf(es))
		x.Equal("예약 취소: 주간 회의", es[0].GetSummary())
		x.Equal("일정 변경", es[0].GetReason(), "why, which is why of each")
		x.Empty(es[0].GetCause(), "it says no more than the decision")
		if i == 0 {
			x.Empty(es[0].GetCauseId(), "the decision's own")
		} else {
			x.Equal(cancel.Id, idUUID(es[0].GetCauseId()))
		}
	}
	x.Len(e.said("reservation.cancel"), 1, "the feed says the decision once")
}

// TestARowFromBeforeIsSaidAsItWasWritten: a reservation's event from before
// Loft's events kept a message is said in the words it was written with, the
// people it marks as they are called now (design 8.2).
func TestARowFromBeforeIsSaidAsItWasWritten(t *testing.T) {
	e := newEnv(t)
	x := e.x
	room := e.space("회의실", nil)
	e.bookable(room, nil)
	kim, kimParty := e.person("member")
	r, err := e.reserve(kim, room, e.tomorrow(9, 0), e.tomorrow(10, 0), nil)
	x.NoError(err)

	// As one from before was written: no message, its sentence and what it
	// named beside it, beneath the servers.
	e.now = e.now.Add(time.Minute)
	x.NoError(e.s.Ent.Event.Create().
		SetId(pdid.New(pd.EventDomain).Uuid()).
		SetTenantId(e.tenant.Uuid()).
		SetKind("reservation.approve").
		SetDesc("예약 승인: 회의 ({party})").
		SetPayload("party=" + pdid.Id(idUUID(kimParty.GetId())).String()).
		SetReason("").
		SetSubjectId(idUUID(r.GetId())).
		SetOccurredAt(e.now).
		SetDateCreated(e.now).
		SetDateUpdated(e.now).
		Exec(context.Background()))
	_, err = e.app().Party().Update(e.owner, app.PartyUpdateRequest_builder{Ref: ref[app.PartyRef](kimParty.GetId()), Name: "김 대리"}.Build())
	x.NoError(err)

	es := e.reservationTimeline(kim, r)
	x.Equal([]string{"예약 승인: 회의 (김 대리)", "예약: 회의실 " + begins(r)}, summaries(es))
	x.Equal("시스템", es[0].GetActor(), "it names nobody who did it")
	x.Equal("김 대리", es[1].GetActor())
}
