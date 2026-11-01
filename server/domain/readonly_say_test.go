package domain_test

import (
	"context"
	"testing"
	"time"

	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"

	app "github.com/lesomnus/loft"
	"github.com/lesomnus/loft/internal/ent/holder"
	"github.com/lesomnus/loft/internal/ent/notification"
	"github.com/lesomnus/loft/server/domain"
)

// What a read-only workspace is told is so (D68, design 7.14): what the
// deployment's own work let go of while nobody could take it up says that
// nobody could, rather than that nobody did; what a person is told they may
// do is what they may; and what an operator's command tells is told.

// TestAReadOnlyWorkspaceSaysNobodyCouldTakeUpWhatItLetGo (D68): while a
// workspace is read-only, nobody may check in, pick up or approve (D64). A
// room nobody could check in to is let go of once its grace is over, a
// camera nobody could pick up once its time is, and a request nobody could
// approve expires as it begins -- the clock moves -- and whose each is, is
// told that nobody could, and not that nobody did: each an expiry, and no
// no-show (D70). Once it writes again, a room nobody checked in to is told
// so, as ever.
func TestAReadOnlyWorkspaceSaysNobodyCouldTakeUpWhatItLetGo(t *testing.T) {
	e := newPaying(t)
	x := e.x
	room := e.space("회의실", nil)
	e.bookable(room, nil)
	hall := e.space("강당", nil)
	e.bookable(hall, func(r *app.BookableAddRequest) { r.SetApproval(true) })
	camera := e.item("카메라", "CM-1", nil, day)
	e.bookable(camera, nil)
	kim, _ := e.person("member")

	met, err := e.reserve(kim, room, e.now.Add(time.Hour), e.now.Add(3*time.Hour), nil)
	x.NoError(err)
	asked, err := e.reserve(kim, hall, e.now.Add(time.Hour), e.now.Add(90*time.Minute), nil)
	x.NoError(err)
	x.Equal("requested", asked.GetStatus())
	taken, err := e.reserve(kim, camera, e.now.Add(time.Hour), e.now.Add(2*time.Hour), nil)
	x.NoError(err)

	e.readOnly()
	e.now = e.now.Add(150 * time.Minute)
	e.sweep()
	x.Equal("expired", e.reservation(asked).GetStatus(), "the request began")
	x.Equal("expired", e.reservation(met).GetStatus(), "the room let go of, and no no-show")
	x.Equal("expired", e.reservation(taken).GetStatus(), "and the camera")

	// Each told as an expiry (D70), in the words it was told in before.
	x.Empty(e.notices(kim, "reservation.no_show"))
	expired := e.notices(kim, "reservation.expired")
	x.Len(expired, 3)
	said := map[string]string{}
	for _, n := range expired {
		switch {
		case sameId(n.GetSubjectId(), met.GetId()):
			said["room"] = n.GetDesc()
		case sameId(n.GetSubjectId(), taken.GetId()):
			said["camera"] = n.GetDesc()
		case sameId(n.GetSubjectId(), asked.GetId()):
			said["request"] = n.GetDesc()
		}
	}
	x.Contains(said["room"], "읽기 전용이라 체크인할 수 없는 동안")
	x.NotContains(said["room"], "체크인하지 않아")
	x.Contains(said["camera"], "읽기 전용이라 수령할 수 없는 동안")
	x.NotContains(said["camera"], "수령하지 않아")
	x.Contains(said["request"], "읽기 전용이라 시작 전까지 승인할 수 없어")
	x.NotContains(said["request"], "승인되지 않아")

	// And the history says so as well, since the build before this one
	// says it (D68, D70).
	for _, ev := range e.eventsOf("reservation.expire") {
		m := value[*app.ReservationReleased](e.env, ev)
		x.Equal("readonly", m.GetWorkspace())
	}
	x.Empty(e.eventsOf("reservation.no_show"))

	// Written again, nobody coming is nobody coming.
	e.paid()
	again, err := e.reserve(kim, room, e.now.Add(time.Hour), e.now.Add(2*time.Hour), nil)
	x.NoError(err)
	e.now = e.now.Add(90 * time.Minute)
	e.sweep()
	x.Equal("no_show", e.reservation(again).GetStatus())
	for _, n := range e.notices(kim, "reservation.no_show") {
		if sameId(n.GetSubjectId(), again.GetId()) {
			x.Contains(n.GetDesc(), "체크인하지 않아")
			x.NotContains(n.GetDesc(), "읽기 전용")
		}
	}
}

// TestAReadOnlyWorkspacesDomainIsToldWhatCanBeDone (D68): a read-only
// workspace takes no `Verify` and no `Activate` (D64), and its domains are
// asked about all the same. A domain that lapses then tells whoever runs it
// that it is asked about again once its records are back -- and not that they
// may check it in the settings, which they may not -- and is: once they are,
// it is the workspace's again. One verified beside it is told ready, to be
// switched to once the workspace writes again.
func TestAReadOnlyWorkspacesDomainIsToldWhatCanBeDone(t *testing.T) {
	e := newPaying(t)
	x := e.x
	ctx := context.Background()
	d := &dns{}
	e.reading(d)
	v := e.pointed(d, "assets.acme.example")
	e.later(10 * time.Minute)
	x.Equal("active", e.domainRow(v).State)
	// And one added beside it, whose records are not there yet.
	other, err := e.app().TenantDomain().Add(e.owner, app.TenantDomainAddRequest_builder{Host: "app.acme.example"}.Build())
	x.NoError(err)
	owner, err := e.s.Ent.Holder.Query().Where(holder.Alias("owner"), holder.TenantId(e.tenant.Uuid())).Only(ctx)
	x.NoError(err)
	told := func(kind string) []string {
		t.Helper()
		ns, err := e.s.Ent.Notification.Query().Where(notification.HolderId(owner.Id), notification.Kind(kind)).All(ctx)
		x.NoError(err)
		out := []string{}
		for _, n := range ns {
			out = append(out, n.Desc)
		}
		return out
	}

	e.readOnly()
	token := v.GetToken()
	d.set(func(d *dns) { delete(d.txt, "_loft-challenge.assets.acme.example") })
	for range 6 {
		e.later(time.Hour)
	}
	x.Equal("lapsed", e.domainRow(v).State)
	lapsed := told("domain.lapsed")
	x.Len(lapsed, 1)
	x.Contains(lapsed[0], "저절로 다시 확인합니다")
	x.NotContains(lapsed[0], "설정에서 바로 확인할 수도 있습니다")

	d.set(func(d *dns) { d.txt["_loft-challenge.assets.acme.example"] = []string{token} })
	e.later(10 * time.Minute)
	x.Equal("active", e.domainRow(v).State, "asked about again, read-only or not")

	d.set(func(d *dns) {
		d.txt["_loft-challenge.app.acme.example"] = []string{other.GetToken()}
		d.cname["app.acme.example"] = edge
	})
	e.later(10 * time.Minute)
	x.Equal("ready", e.domainRow(other).State)
	ready := told("domain.ready")
	x.Len(ready, 1)
	x.Contains(ready[0], "다시 바꿀 수 있게 되면 설정에서 활성화할 수 있고")
}

// TestAnOperatorsNoticesAreToldInAReadOnlyWorkspace (D68): an operator's
// command changes a read-only workspace -- the deployment's own work, which
// is not refused -- and what it tells is of what the operator did, told as it
// does it, whatever its kind: nothing would tell it later. A loan an operator
// gives a later due, over a booking, tells whose the booking is and who runs
// the reservations that what it booked is out then -- where the same, a
// sweep's, waits until the workspace writes again (D67).
func TestAnOperatorsNoticesAreToldInAReadOnlyWorkspace(t *testing.T) {
	e := newPaying(t)
	x := e.x
	camera := e.item("카메라", "CM-1", nil, day)
	e.bookable(camera, nil)
	kim, _ := e.person("member")
	_, lee := e.person("member")
	boss, _ := e.person("manager")
	loan := e.lend(lee, e.now.Add(time.Hour), camera)
	booked, err := e.reserve(kim, camera, e.tomorrow(10, 0), e.tomorrow(11, 0), nil)
	x.NoError(err)
	x.Empty(e.notices(kim, "reservation.out"))

	e.readOnly()
	_, err = e.s.Ungated.Custody().Extend(e.operator(), app.CustodyExtendRequest_builder{Ref: ref[app.CustodyRef](loan.GetId()), DueAt: timestamppb.New(e.tomorrow(12, 0))}.Build())
	x.NoError(err)
	x.Equal([]string{"CM-1 loan"}, e.out(booked))
	x.Len(e.notices(kim, "reservation.out"), 1, "to whose the booking is")
	x.NotNil(about(e.notices(boss, "reservation.out"), camera), "and to who runs the reservations")
}

// TestAReadOnlyRefusalSaysWhatItStillDoes (D68): what a read-only workspace
// says of a write it refuses is that it is read-only, and what it still does
// -- and not that that is all: a password, which is the account's, and what
// a person is told, which is theirs, it takes besides (D64, D67).
func TestAReadOnlyRefusalSaysWhatItStillDoes(t *testing.T) {
	e := newPaying(t)
	x := e.x
	e.readOnly()
	_, err := e.app().Asset().Add(rpc(e.owner, app.AssetService_Add_FullMethodName), app.AssetAddRequest_builder{Name: "프로젝터"}.Build())
	x.Equal(domain.ErrorReadOnly, reasonOf(err))
	said := status.Convert(err).Message()
	x.Contains(said, "이 워크스페이스는 읽기 전용이라 바꿀 수 없습니다")
	x.Contains(said, "보기, 내보내기, 결제, 닫기, 떠나기는 할 수 있고")
	x.NotContains(said, "만 할 수 있")
}
