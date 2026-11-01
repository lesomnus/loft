package domain_test

import (
	"context"
	"testing"
	"time"
	"uuid"

	"github.com/lesomnus/z"
	"google.golang.org/protobuf/types/known/timestamppb"

	app "github.com/lesomnus/loft"
	"github.com/lesomnus/loft/internal/ent"
	"github.com/lesomnus/loft/internal/ent/event"
	"github.com/lesomnus/loft/server/domain"
)

// What the deployment lets go of when nobody could take it up, or nobody was
// held to, is an expiry and no no-show (D70, design 7.15): while the
// workspace is read-only, the sweeps write it so; what passed its time while
// it was closed, its restore does; and equipment nobody picked up where
// nobody is held to checking in, the end of its time does. The booking
// report counts no expiry.

// releases is every event that let `r` go as nobody took it up: an expiry,
// or a no-show.
func (e *env) releases(r *app.Reservation) []*ent.Event {
	e.t.Helper()
	vs, err := e.s.Ent.Event.Query().Where(
		event.TenantId(e.tenant.Uuid()),
		event.SubjectId(idUUID(r.GetId())),
		event.KindIn("reservation.expire", "reservation.no_show"),
	).All(context.Background())
	e.x.NoError(err)
	return vs
}

// usage is the booking report over [from, to), each bookable's row by its
// name.
func (e *env) usage(from, to time.Time) map[string]*app.BookableUsageRow {
	e.t.Helper()
	v, err := e.app().Bookable().Usage(e.owner, app.BookableUsageRequest_builder{From: timestamppb.New(from), To: timestamppb.New(to)}.Build())
	e.x.NoError(err)
	out := map[string]*app.BookableUsageRow{}
	for _, r := range v.GetRows() {
		out[r.GetName()] = r
	}
	return out
}

// named is a reservation called `name`, and held for a while when `hold`.
func named(name string, hold bool) func(*app.ReservationAddRequest) {
	return func(r *app.ReservationAddRequest) {
		r.SetName(name)
		if hold {
			r.SetHold(true)
		}
	}
}

// expired is what `r` is held to once it expired from `from`, saying `said`
// and of a workspace that was `while` then: one expiry of its own, its time
// let go of.
type expired struct {
	r     *app.Reservation
	from  string
	while string
	said  string
}

// expiry is the one event that let `c.r` go, held to what `c` says.
func (e *env) expiry(c expired) *ent.Event {
	e.t.Helper()
	x := e.x
	x.Equal("expired", e.reservation(c.r).GetStatus(), c.said)
	vs := e.releases(c.r)
	x.Len(vs, 1, "%s: let go of once", c.said)
	if len(vs) != 1 {
		return nil
	}
	x.Equal("reservation.expire", vs[0].Kind, "%s: no no-show", c.said)
	m := value[*app.ReservationReleased](e, vs[0])
	x.Equal(c.from, m.GetFrom(), c.said)
	x.Equal(c.while, m.GetWorkspace(), c.said)
	x.Equal(c.said, e.say(vs[0]))
	x.False(e.blocking(c.r), "%s: its time let go of", c.said)
	return vs[0]
}

// TestWhatAReadOnlyWorkspaceLetsGoExpires (D70): while a workspace is
// read-only, nobody may confirm, approve, check in or pick up (D64), and what
// the sweeps let go of meanwhile -- a hold run out, a request begun, a room
// past its grace, a camera past its time -- is an expiry from what it was,
// saying the workspace was read-only: no no-show, which the booking report
// counts none of, nor a booking. Whose each is, is told so as an expiry, in
// the words it was told in before (D68), and nothing is let go of or told
// twice. Once the workspace writes again, nobody coming is a no-show, and
// counted one.
func TestWhatAReadOnlyWorkspaceLetsGoExpires(t *testing.T) {
	e := newPaying(t)
	x := e.x
	start := e.now
	room := e.space("회의실", nil)
	e.bookable(room, nil)
	hall := e.space("강당", nil)
	e.bookable(hall, func(r *app.BookableAddRequest) { r.SetApproval(true) })
	small := e.space("소회의실", nil)
	e.bookable(small, nil)
	camera := e.item("카메라", "CM-1", nil, day)
	e.bookable(camera, nil)
	kim, _ := e.person("member")

	met, err := e.reserve(kim, room, e.now.Add(time.Hour), e.now.Add(3*time.Hour), named("주간 회의", false))
	x.NoError(err)
	asked, err := e.reserve(kim, hall, e.now.Add(time.Hour), e.now.Add(90*time.Minute), named("전체 회의", false))
	x.NoError(err)
	x.Equal("requested", asked.GetStatus())
	shot, err := e.reserve(kim, camera, e.now.Add(time.Hour), e.now.Add(2*time.Hour), named("제품 촬영", false))
	x.NoError(err)
	held, err := e.reserve(kim, small, e.now.Add(time.Hour), e.now.Add(2*time.Hour), named("면담", true))
	x.NoError(err)
	x.Equal("held", held.GetStatus())

	e.readOnly()
	e.now = e.now.Add(150 * time.Minute)
	e.sweep()

	for _, c := range []expired{
		{met, "confirmed", "readonly", "예약 만료: 주간 회의 (워크스페이스가 읽기 전용이던 때)"},
		{asked, "requested", "readonly", "승인 대기 만료: 전체 회의 (워크스페이스가 읽기 전용이던 때)"},
		{shot, "confirmed", "readonly", "예약 만료: 제품 촬영 (워크스페이스가 읽기 전용이던 때)"},
		{held, "held", "readonly", "홀드 만료: 면담 (워크스페이스가 읽기 전용이던 때)"},
	} {
		if v := e.expiry(c); v != nil {
			x.Nil(v.CauseId, "%s: the sweep's own", c.said)
		}
	}
	x.Empty(e.eventsOf("reservation.no_show"), "nobody was a no-show")

	// Whose each is, is told so, as an expiry -- and a hold, as ever, to
	// nobody.
	x.Empty(e.notices(kim, "reservation.no_show"))
	told := e.notices(kim, "reservation.expired")
	x.Len(told, 3)
	said := map[string]string{}
	for _, n := range told {
		for _, r := range []*app.Reservation{met, asked, shot} {
			if sameId(n.GetSubjectId(), r.GetId()) {
				said[r.GetName()] = n.GetDesc()
			}
		}
	}
	x.Contains(said["주간 회의"], "워크스페이스가 읽기 전용이라 체크인할 수 없는 동안")
	x.Contains(said["제품 촬영"], "워크스페이스가 읽기 전용이라 수령할 수 없는 동안")
	x.Contains(said["전체 회의"], "워크스페이스가 읽기 전용이라 시작 전까지 승인할 수 없어")

	// Counted as nothing: no no-show, nor a booking used or not.
	used := e.usage(start.Add(-day), e.now.Add(day))
	for _, name := range []string{"회의실", "강당", "소회의실", "카메라"} {
		x.NotNil(used[name], name)
		x.Zero(used[name].GetNoShows(), name)
		x.Zero(used[name].GetReservations(), name)
	}

	// And once.
	events, notices := len(e.eventsOf("reservation.expire")), len(told)
	e.now = e.now.Add(time.Minute)
	e.sweep()
	x.Len(e.eventsOf("reservation.expire"), events, "let go of once")
	x.Len(e.notices(kim, "reservation.expired"), notices, "and told once")

	// Written again, nobody coming is nobody coming, and counted so.
	e.paid()
	again, err := e.reserve(kim, room, e.now.Add(time.Hour), e.now.Add(2*time.Hour), named("다음 회의", false))
	x.NoError(err)
	e.now = e.now.Add(90 * time.Minute)
	e.sweep()
	x.Equal("no_show", e.reservation(again).GetStatus())
	vs := e.releases(again)
	x.Len(vs, 1)
	x.Equal("reservation.no_show", vs[0].Kind)
	x.Equal("노쇼: 다음 회의", e.say(vs[0]))
	x.Len(e.notices(kim, "reservation.no_show"), 1)
	x.EqualValues(1, e.usage(start.Add(-day), e.now.Add(day))["회의실"].GetNoShows())
}

// TestARestoreExpiresWhatPassedWhileItWasClosed (D70): a workspace closed
// for days is swept by nothing (D68), and nobody may confirm, approve, check
// in or pick up in it (D29). Its restore lets go, in its own transaction, of
// what passed its time meanwhile -- a hold run out, a request begun, a room
// past its grace and a desk past its grace whose time is not over, a camera
// past its time -- each an expiry from what it was, saying the workspace was
// closed, a child of the restore done by whoever restored it, and told to
// nobody; the desk's time to come is anybody's to book again. And of nothing
// else: not a hold that ran out before it closed, a room checked in to, a
// room whose grace is not over, equipment whose time is not, nor a request
// not begun. The restore says how many it let go of only from the release
// after this one. The next pass lets go of nothing of it, and does what is
// its own; and the booking report counts none of it.
func TestARestoreExpiresWhatPassedWhileItWasClosed(t *testing.T) {
	e := newEnv(t)
	x := e.x
	start := e.now
	const closure = 3 * day
	room := e.space("회의실", nil)
	e.bookable(room, nil)
	desk := e.space("자리", nil)
	e.bookable(desk, nil)
	busy := e.space("소강당", nil)
	e.bookable(busy, nil)
	hall := e.space("강당", nil)
	e.bookable(hall, func(r *app.BookableAddRequest) { r.SetApproval(true) })
	small := e.space("소회의실", nil)
	e.bookable(small, nil)
	booth := e.space("상담실", nil)
	e.bookable(booth, nil)
	camera := e.item("카메라", "CM-1", nil, day)
	e.bookable(camera, nil)
	tripod := e.item("삼각대", "TR-1", nil, day)
	e.bookable(tripod, nil)
	kimCtx, _ := e.person("member")
	leeCtx, _ := e.person("member")
	decide := func(r *app.Reservation) *app.ReservationDecideRequest {
		return app.ReservationDecideRequest_builder{Ref: ref[app.ReservationRef](r.GetId())}.Build()
	}

	// While it is open: a hold that runs out unswept, and a room checked in
	// to whose time ends once it is closed.
	before, err := e.reserve(kimCtx, booth, e.now.Add(time.Hour), e.now.Add(2*time.Hour), named("이전 홀드", true))
	x.NoError(err)
	inUse, err := e.reserve(kimCtx, busy, e.now.Add(20*time.Minute), e.now.Add(2*time.Hour), named("진행 중 회의", false))
	x.NoError(err)
	_, err = e.app().Reservation().CheckIn(kimCtx, decide(inUse))
	x.NoError(err)
	e.now = e.now.Add(domain.HoldFor + time.Minute)

	// What it has as it closes, each due while it is closed.
	held, err := e.reserve(kimCtx, small, e.now.Add(time.Hour), e.now.Add(2*time.Hour), named("면담", true))
	x.NoError(err)
	asked, err := e.reserve(kimCtx, hall, e.now.Add(time.Hour), e.now.Add(90*time.Minute), named("전체 회의", false))
	x.NoError(err)
	x.Equal("requested", asked.GetStatus())
	met, err := e.reserve(kimCtx, room, e.now.Add(time.Hour), e.now.Add(3*time.Hour), named("주간 회의", false))
	x.NoError(err)
	long, err := e.reserve(kimCtx, desk, e.now.Add(time.Hour), e.now.Add(5*day), named("프로젝트 자리", false))
	x.NoError(err)
	shot, err := e.reserve(kimCtx, camera, e.now.Add(time.Hour), e.now.Add(2*time.Hour), named("제품 촬영", false))
	x.NoError(err)
	// And what is due once it is open again.
	kept, err := e.reserve(kimCtx, tripod, e.now.Add(time.Hour), e.now.Add(5*day), named("장기 촬영", false))
	x.NoError(err)
	fresh, err := e.reserve(kimCtx, room, e.now.Add(closure-5*time.Minute), e.now.Add(closure+time.Hour), named("복구 직후 회의", false))
	x.NoError(err)
	next, err := e.reserve(kimCtx, hall, e.now.Add(closure+2*time.Hour), e.now.Add(closure+3*time.Hour), named("다음 전체 회의", false))
	x.NoError(err)
	x.Equal("requested", next.GetStatus())

	e.closeIt()
	e.now = e.now.Add(closure)
	e.sweep()
	notices, _ := e.toldAndDone()
	x.Equal("held", e.reservation(held).GetStatus(), "swept by nothing while it is closed")

	e.restoreIt()
	restored := e.eventOf("workspace.restore")
	x.Zero(value[*app.WorkspaceRestored](e, restored).GetExpired(), "how many: written a release after it is declared")
	x.Equal("워크스페이스 복구", e.say(restored))
	expiries := []expired{
		{held, "held", "closed", "홀드 만료: 면담 (워크스페이스가 닫혀 있던 때)"},
		{asked, "requested", "closed", "승인 대기 만료: 전체 회의 (워크스페이스가 닫혀 있던 때)"},
		{met, "confirmed", "closed", "예약 만료: 주간 회의 (워크스페이스가 닫혀 있던 때)"},
		{long, "confirmed", "closed", "예약 만료: 프로젝트 자리 (워크스페이스가 닫혀 있던 때)"},
		{shot, "confirmed", "closed", "예약 만료: 제품 촬영 (워크스페이스가 닫혀 있던 때)"},
	}
	for _, c := range expiries {
		v := e.expiry(c)
		if v == nil {
			continue
		}
		x.NotNil(v.CauseId, "%s: the restore's", c.said)
		if v.CauseId != nil {
			x.Equal(restored.Id, *v.CauseId, "%s: the restore's", c.said)
		}
		x.Equal(restored.ActorId, v.ActorId, "%s: done by whoever restored it", c.said)
	}
	x.Len(e.eventsOf("reservation.expire"), len(expiries), "and nothing else")

	// Nothing else.
	x.Equal("held", e.reservation(before).GetStatus(), "ran out while it was open: the sweeps' to let go of")
	x.Equal("in_use", e.reservation(inUse).GetStatus(), "checked in to")
	x.Equal("confirmed", e.reservation(fresh).GetStatus(), "its grace not over")
	x.Equal("confirmed", e.reservation(kept).GetStatus(), "to be picked up till its end")
	x.Equal("requested", e.reservation(next).GetStatus(), "not begun")

	// Told to nobody.
	n, _ := e.toldAndDone()
	x.Equal(notices, n, "a notice written")

	// What the desk's booking would have held of its time to come is
	// anybody's again.
	other, err := e.reserve(leeCtx, desk, e.now.Add(time.Hour), e.now.Add(2*time.Hour), named("다른 자리", false))
	x.NoError(err)
	x.Equal("confirmed", other.GetStatus())

	// The next pass lets go of nothing of it, and does what is its own: the
	// hold that ran out while it was open, as ever, and the room's end.
	e.sweep()
	for _, c := range expiries {
		x.Len(e.releases(c.r), 1, "%s: let go of once", c.said)
	}
	e.expiry(expired{before, "held", "", "홀드 만료: 이전 홀드"})
	x.Nil(e.releases(before)[0].CauseId, "the sweep's own")
	x.Equal("completed", e.reservation(inUse).GetStatus())
	x.Equal("confirmed", e.reservation(fresh).GetStatus())
	x.Equal("confirmed", e.reservation(kept).GetStatus())
	x.Equal("requested", e.reservation(next).GetStatus())
	x.Empty(e.eventsOf("reservation.no_show"), "nobody was a no-show")
	n, _ = e.toldAndDone()
	x.Equal(notices, n, "a notice written")

	// Counted as nothing.
	used := e.usage(start.Add(-day), e.now.Add(day))
	for name, r := range used {
		x.Zero(r.GetNoShows(), name)
	}
	x.Zero(used["카메라"].GetReservations(), "the camera's expired")
	x.Zero(used["강당"].GetReservations(), "the request expired, and the next not decided")
	x.EqualValues(1, used["자리"].GetReservations(), "the other booking, and not the desk's that expired")
	x.EqualValues(1, used["소강당"].GetUsed(), "checked in to, and used")

	// And again, nothing.
	events := len(e.eventsOf("reservation.expire"))
	e.now = e.now.Add(time.Minute)
	e.sweep()
	x.Len(e.eventsOf("reservation.expire"), events)
	x.Empty(e.eventsOf("reservation.no_show"))
}

// TestAnOperatorsRestoreExpiresWhatPassedWhileItWasClosed (D70): restored by
// an operator, which is the deployment's own work, what passed its time while
// the workspace was closed is let go of all the same, as children of that
// restore, done by nobody.
func TestAnOperatorsRestoreExpiresWhatPassedWhileItWasClosed(t *testing.T) {
	e := newEnv(t)
	x := e.x
	room := e.space("회의실", nil)
	e.bookable(room, nil)
	kimCtx, _ := e.person("member")
	met, err := e.reserve(kimCtx, room, e.now.Add(time.Hour), e.now.Add(2*time.Hour), named("주간 회의", false))
	x.NoError(err)

	e.closeIt()
	e.now = e.now.Add(day)
	_, err = e.s.Deps.RestoreWorkspace(context.Background(), e.s.Base, e.s.Drv, e.tenant)
	x.NoError(err)
	restored := e.eventOf("workspace.restore")
	x.Equal(domain.ClosedByOperator, value[*app.WorkspaceRestored](e, restored).GetBy())
	x.True(restored.ActorId == nil || *restored.ActorId == (uuid.UUID{}), "the deployment's own")
	v := e.expiry(expired{met, "confirmed", "closed", "예약 만료: 주간 회의 (워크스페이스가 닫혀 있던 때)"})
	if v != nil {
		x.NotNil(v.CauseId)
		if v.CauseId != nil {
			x.Equal(restored.Id, *v.CauseId, "the operator's restore's")
		}
		x.Equal(restored.ActorId, v.ActorId, "done by nobody")
	}
}

// TestARestoreLeavesARoomNobodyIsHeldToCheckInTo (D70): where nobody is held
// to checking in, a room booked is used as it was booked, closed or not, and
// its end completes it: its restore leaves it to the next pass. Equipment
// whose time passed while the workspace was closed nobody could pick up, and
// its restore lets it go.
func TestARestoreLeavesARoomNobodyIsHeldToCheckInTo(t *testing.T) {
	e := newEnv(t)
	x := e.x
	_, err := e.app().Tenant().Configure(e.owner, app.TenantConfigureRequest_builder{
		Ref:           app.TenantRef_builder{Id: e.tenant.Bytes()}.Build(),
		NoShowMinutes: z.Ptr(int32(-1)),
	}.Build())
	x.NoError(err)
	room := e.space("회의실", nil)
	e.bookable(room, nil)
	camera := e.item("카메라", "CM-1", nil, day)
	e.bookable(camera, nil)
	kimCtx, _ := e.person("member")
	met, err := e.reserve(kimCtx, room, e.now.Add(time.Hour), e.now.Add(2*time.Hour), named("주간 회의", false))
	x.NoError(err)
	shot, err := e.reserve(kimCtx, camera, e.now.Add(time.Hour), e.now.Add(2*time.Hour), named("제품 촬영", false))
	x.NoError(err)

	e.closeIt()
	e.now = e.now.Add(day)
	e.restoreIt()
	x.Equal("confirmed", e.reservation(met).GetStatus(), "used as it was booked")
	x.Empty(e.releases(met))
	e.expiry(expired{shot, "confirmed", "closed", "예약 만료: 제품 촬영 (워크스페이스가 닫혀 있던 때)"})

	e.sweep()
	x.Equal("completed", e.reservation(met).GetStatus())
	x.Empty(e.releases(met))
	x.Len(e.releases(shot), 1, "let go of once")
}

// TestEquipmentNobodyPickedUpExpiresWhereNobodyIsHeldToIt (D70): a workspace
// that takes no check-ins holds nobody to coming, and lets nothing go as a
// no-show. A room booked there is used as it was booked, and its end
// completes it. Equipment is used by being picked up: a camera nobody picked
// up by the end of its time expired then -- no no-show, nor used -- told to
// nobody, as a room completed is not; one picked up is ended by its return.
// While the workspace is read-only, which takes no pickup, the expiry says
// so.
func TestEquipmentNobodyPickedUpExpiresWhereNobodyIsHeldToIt(t *testing.T) {
	e := newPaying(t)
	x := e.x
	start := e.now
	_, err := e.app().Tenant().Configure(e.owner, app.TenantConfigureRequest_builder{
		Ref:           app.TenantRef_builder{Id: e.tenant.Bytes()}.Build(),
		NoShowMinutes: z.Ptr(int32(-1)),
	}.Build())
	x.NoError(err)
	camera := e.item("카메라", "CM-1", nil, day)
	e.bookable(camera, nil)
	lens := e.item("렌즈", "LN-1", nil, day)
	e.bookable(lens, nil)
	room := e.space("회의실", nil)
	e.bookable(room, nil)
	kimCtx, kim := e.person("member")

	shot, err := e.reserve(kimCtx, camera, e.now.Add(time.Hour), e.now.Add(2*time.Hour), named("제품 촬영", false))
	x.NoError(err)
	met, err := e.reserve(kimCtx, room, e.now.Add(time.Hour), e.now.Add(2*time.Hour), named("주간 회의", false))
	x.NoError(err)
	taken, err := e.reserve(kimCtx, lens, e.now.Add(time.Hour), e.now.Add(2*time.Hour), named("렌즈 대여", false))
	x.NoError(err)
	later, err := e.reserve(kimCtx, camera, e.now.Add(3*time.Hour), e.now.Add(4*time.Hour), named("다음 촬영", false))
	x.NoError(err)

	// The lens picked up, and not given back by its end.
	e.now = start.Add(70 * time.Minute)
	_, err = e.app().Custody().Add(e.owner, app.CustodyAddRequest_builder{
		Party:         ref[app.PartyRef](kim.GetId()),
		Lines:         []*app.CustodyLineSpec{app.CustodyLineSpec_builder{Asset: assetRef(lens)}.Build()},
		ReservationId: taken.GetId(),
		DueAt:         taken.GetEndsAt(),
	}.Build())
	x.NoError(err)
	x.Equal("in_use", e.reservation(taken).GetStatus())

	e.now = start.Add(2*time.Hour + time.Minute)
	e.sweep()
	if v := e.expiry(expired{shot, "confirmed", "", "예약 만료: 제품 촬영"}); v != nil {
		x.Nil(v.CauseId, "the sweep's own")
	}
	x.Equal("completed", e.reservation(met).GetStatus(), "a room used as it was booked")
	x.Contains(e.kindsOf(met), "reservation.complete")
	x.Equal("in_use", e.reservation(taken).GetStatus(), "out till it is back")
	x.Empty(e.releases(taken))
	x.Equal("confirmed", e.reservation(later).GetStatus(), "its time not over")
	x.Empty(e.eventsOf("reservation.no_show"), "nobody was a no-show")
	x.Empty(e.notices(kimCtx, "reservation.expired"), "told to nobody")
	x.Empty(e.notices(kimCtx, "reservation.no_show"), "told to nobody")

	used := e.usage(start.Add(-day), e.now.Add(day))
	x.Zero(used["카메라"].GetNoShows())
	x.EqualValues(1, used["카메라"].GetReservations(), "the next, and not the one that expired")
	x.Zero(used["카메라"].GetUsed())
	x.EqualValues(1, used["회의실"].GetUsed(), "used as it was booked")
	x.EqualValues(1, used["렌즈"].GetUsed(), "picked up")

	// And once.
	events := len(e.eventsOf("reservation.expire"))
	e.now = e.now.Add(time.Minute)
	e.sweep()
	x.Len(e.eventsOf("reservation.expire"), events)

	// While it is read-only nobody could pick it up, and it says so.
	e.readOnly()
	e.now = start.Add(4*time.Hour + time.Minute)
	e.sweep()
	e.expiry(expired{later, "confirmed", "readonly", "예약 만료: 다음 촬영 (워크스페이스가 읽기 전용이던 때)"})
	x.Empty(e.notices(kimCtx, "reservation.expired"), "told to nobody")
	x.Empty(e.eventsOf("reservation.no_show"))
	x.Zero(e.usage(start.Add(-day), e.now.Add(day))["카메라"].GetReservations())
}
