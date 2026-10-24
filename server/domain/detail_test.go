package domain_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/lesomnus/payday/access"
	"github.com/lesomnus/payday/pdid"
	"google.golang.org/protobuf/types/known/timestamppb"

	app "github.com/lesomnus/rove"
	"github.com/lesomnus/rove/server/domain"
	"github.com/lesomnus/rove/server/pd"
)

// detailOf is what the newest event of `kind` on an asset's timeline holds,
// as its entry's detail says it.
func detailOf(es []*app.TimelineEntry, kind string) map[string]string {
	for _, en := range es {
		if en.GetKind() == "event" && en.GetEventKind() == kind {
			return en.GetDetail()
		}
	}
	return nil
}

// TestAnEventsDetailIsWhatItHeld: an event since Rove's events kept a message
// (payday#151) is detailed on a timeline by that message, field by field
// under the names its domain's table gives them, in the words a person reads
// -- what a row from before kept in its payload, which it is detailed by
// still. A row is named as it is called now: a person by name, and on the
// access record of the call for it, whatever the sentence says; an asset by
// its tag and name; any other row by its own name, through the wall; and a
// row with no name of its own by its identifier. A moment is the workspace's
// wall, and a list is joined. Nothing of a person's but their name is read:
// not their address, not their number.
func TestAnEventsDetailIsWhatItHeld(t *testing.T) {
	e := newEnv(t)
	x := e.x
	ctx := context.Background()

	store := e.space("창고", nil)
	laptop := e.item("노트북", "NB-1", nil, 24*time.Hour)
	_, err := e.app().Asset().Move(e.owner, app.AssetMoveRequest_builder{Ref: assetRef(laptop), To: assetRef(store)}.Build())
	x.NoError(err)
	_, kim := e.person("member")
	x.NotEmpty(kim.GetEmail())
	_, err = e.app().Party().Update(e.owner, app.PartyUpdateRequest_builder{Ref: ref[app.PartyRef](kim.GetId()), Phone: "010-1234-5678"}.Build())
	x.NoError(err)
	_, err = e.app().Asset().Assign(e.owner, app.AssetAssignRequest_builder{Ref: assetRef(laptop), Role: "owner", Party: ref[app.PartyRef](kim.GetId())}.Build())
	x.NoError(err)
	due := e.now.Add(7 * 24 * time.Hour)
	loan, err := e.app().Custody().Add(e.owner, app.CustodyAddRequest_builder{
		Party: ref[app.PartyRef](kim.GetId()),
		DueAt: timestamppb.New(due),
		Lines: []*app.CustodyLineSpec{app.CustodyLineSpec_builder{Asset: assetRef(laptop)}.Build()},
	}.Build())
	x.NoError(err)
	_, err = e.app().WorkOrder().Add(e.owner, app.WorkOrderAddRequest_builder{Asset: assetRef(laptop), Name: "화면 교체"}.Build())
	x.NoError(err)
	_, err = e.app().Asset().SetAttributes(e.owner, app.AssetSetAttributesRequest_builder{
		Ref:   assetRef(laptop),
		Set:   map[string]string{"serial": "SN-1", "condition": "damaged"},
		Clear: []string{"desc"},
	}.Build())
	x.NoError(err)

	// A room the owner books for somebody else: its sentence names nobody,
	// and its detail names whom it is for.
	room := e.space("회의실", nil)
	e.bookable(room, nil)
	begins, ends := e.tomorrow(10, 0), e.tomorrow(11, 0)
	_, err = e.reserve(e.owner, room, begins, ends, func(r *app.ReservationAddRequest) {
		r.SetName("주간 회의")
		r.SetParty(ref[app.PartyRef](kim.GetId()))
	})
	x.NoError(err)

	es := e.timeline(laptop, false)
	nb := "NB-1 노트북"
	there := e.get(store).GetTag() + " 창고"
	x.Equal(map[string]string{"asset": nb, "tag": "NB-1", "name": "노트북", "kind": "item"}, detailOf(es, "asset.add"))
	x.Equal(map[string]string{"asset": nb, "tag": "NB-1", "to": there, "to_tag": e.get(store).GetTag(), "mode": "located"}, detailOf(es, "asset.move"))
	x.Equal(map[string]string{"asset": nb, "tag": "NB-1", "role": "owner", "party": kim.GetName()}, detailOf(es, "asset.assign"))
	// A hand-over has no name of its own, and is said as its identifier; its
	// due date on the workspace's wall.
	x.Equal(map[string]string{
		"custody": pdid.Id(idUUID(loan.GetId())).String(),
		"party":   kim.GetName(),
		"due":     due.In(domain.DefaultZone).Format(time.RFC3339),
		"kind":    "loan",
		"asset":   nb,
	}, detailOf(es, "custody.issue"))
	x.Equal(map[string]string{"work_order": "화면 교체", "asset": nb, "name": "화면 교체", "tag": "NB-1", "kind": "repair", "status": "open"}, detailOf(es, "work.open"))
	set := detailOf(es, "asset.set")
	x.ElementsMatch([]string{"serial", "condition"}, strings.Split(set["set"], ", "), "the keys set, joined")
	x.Equal("desc", set["cleared"])

	// Whom a booking is for, by name -- on the access record of the call that
	// shows it, though its sentence names nobody and the owner made it.
	rctx, call := access.Into(e.owner, "test")
	v, err := e.app().Asset().Timeline(rctx, app.AssetTimelineRequest_builder{Ref: assetRef(room)}.Build())
	x.NoError(err)
	booked := detailOf(v.GetEntries(), "reservation.add")
	x.Equal(map[string]string{
		"reservation": "주간 회의",
		"status":      "confirmed",
		"party":       kim.GetName(),
		"occurrences": "1",
		"begins_at":   begins.In(domain.DefaultZone).Format(time.RFC3339),
		"ends_at":     ends.In(domain.DefaultZone).Format(time.RFC3339),
		"asset":       e.get(room).GetTag() + " 회의실",
		"asset_name":  "회의실",
	}, booked)
	x.NotContains(summaryOf(v.GetEntries(), "reservation.add"), kim.GetName())
	_, carried := call.Unsaid()
	x.Contains(carried, access.Subject{Id: pdid.Id(idUUID(kim.GetId())), Tenant: e.tenant}, "whom it named")

	// Called what they are called now.
	_, err = e.app().Party().Update(e.owner, app.PartyUpdateRequest_builder{Ref: ref[app.PartyRef](kim.GetId()), Name: "김 대리"}.Build())
	x.NoError(err)
	es = e.timeline(laptop, false)
	x.Equal("김 대리", detailOf(es, "asset.assign")["party"])
	x.Equal("김 대리", detailOf(es, "custody.issue")["party"])

	// Nothing of a person's but the name they are called by.
	for _, en := range append(es, v.GetEntries()...) {
		for k, s := range en.GetDetail() {
			x.NotContains(s, kim.GetEmail(), "%s %s", en.GetEventKind(), k)
			x.NotContains(s, "010-1234-5678", "%s %s", en.GetEventKind(), k)
		}
	}

	// A row from before keeps no message, and is detailed by the payload it
	// was written with, as it was.
	q := e.s.Ent.Event.Create().
		SetId(pdid.New(pd.EventDomain).Uuid()).
		SetTenantId(e.tenant.Uuid()).
		SetKind("asset.relate").
		SetDesc("NB-1 연결 K-1 (member_of)").
		SetPayload("kind=member_of\ntarget=K-1").
		SetReason("").
		SetSubjectId(idUUID(laptop.GetId())).
		SetOccurredAt(e.now).
		SetDateCreated(e.now).
		SetDateUpdated(e.now)
	x.NoError(q.Exec(ctx))
	x.Equal(map[string]string{"kind": "member_of", "target": "K-1"}, detailOf(e.timeline(laptop, false), "asset.relate"))
}
