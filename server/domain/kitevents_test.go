package domain_test

import (
	"fmt"
	"slices"
	"testing"

	"uuid"

	app "github.com/lesomnus/rove"
	"github.com/lesomnus/rove/internal/ent"
	"github.com/lesomnus/rove/server/domain"
)

// What a kit's bookings do as the kit changes (kit_test.go) is on the history
// of each booking and of each member, as events of their own (design D39):
// the booking's, and the member's, holding the same, under the operation that
// did it -- the change to the kit, or what let the member go -- and, for what
// the sweep settles, an operation of the sweep's own.

// history is what the workspace's history says of a reservation, newest
// first: each event about it, as it is said when it is read.
func (e *env) history(r *app.Reservation) []string {
	e.t.Helper()
	v, err := e.app().Event().Recent(e.owner, app.EventRecentRequest_builder{Size: 500, Children: true}.Build())
	e.x.NoError(err)
	out := []string{}
	for _, ev := range v.GetItems() {
		if sameId(ev.GetSubjectId(), r.GetId()) {
			out = append(out, ev.GetDesc())
		}
	}
	return out
}

// happened is the entries of an asset's history for the events of these
// kinds, newest first.
func (e *env) happened(a *app.Asset, kinds ...string) []*app.TimelineEntry {
	e.t.Helper()
	out := []*app.TimelineEntry{}
	for _, en := range e.timeline(a, false) {
		if en.GetKind() == "event" && slices.Contains(kinds, en.GetEventKind()) {
			out = append(out, en)
		}
	}
	return out
}

// summaries is what each entry says.
func summaries(es []*app.TimelineEntry) []string {
	out := []string{}
	for _, en := range es {
		out = append(out, en.GetSummary())
	}
	return out
}

// begins is when a reservation begins, as a sentence of it says it.
func begins(r *app.Reservation) string {
	return r.GetBeginsAt().AsTime().In(domain.DefaultZone).Format("01-02 15:04")
}

// about is the events of `kind` by their subject, each the only one of it.
func (e *env) about(kind string) map[uuid.UUID]*ent.Event {
	e.t.Helper()
	out := map[uuid.UUID]*ent.Event{}
	for _, v := range e.eventsOf(kind) {
		e.x.NotNil(v.SubjectId, "%s about nothing", kind)
		e.x.NotContains(out, *v.SubjectId, "two of %s about one thing", kind)
		out[*v.SubjectId] = v
	}
	return out
}

// subjects is what these are about.
func subjects(vs map[uuid.UUID]*ent.Event) []uuid.UUID {
	out := []uuid.UUID{}
	for k := range vs {
		out = append(out, k)
	}
	return out
}

// TestWhatAChangeToAKitDidToItsBookingsIsOnTheHistoryOfEach: a member given
// to a booked kit is taken by a booking it is free for and waited for by one
// it is not. Each is an event of the booking's and one of the member's, under
// the change that did it, by whoever made it, named as a person reads them;
// and the workspace's feed says the change once. The wait, settled once the
// member is let go, is the same, under what let it go.
func TestWhatAChangeToAKitDidToItsBookingsIsOnTheHistoryOfEach(t *testing.T) {
	e := newEnv(t)
	x := e.x
	camera := e.item("카메라", "CM-1", nil, day)
	tripod := e.item("삼각대", "TR-1", nil, day)
	e.bookable(tripod, nil)
	kit := e.kit("촬영 키트", camera)
	kim, _ := e.person("member")
	lee, leeParty := e.person("member")

	shoot, err := e.reserve(kim, kit, e.tomorrow(9, 0), e.tomorrow(12, 0), func(r *app.ReservationAddRequest) { r.SetName("제품 촬영") })
	x.NoError(err)
	rehearsal, err := e.reserve(kim, kit, e.tomorrow(14, 0), e.tomorrow(16, 0), func(r *app.ReservationAddRequest) { r.SetName("리허설") })
	x.NoError(err)
	interview, err := e.reserve(lee, tripod, e.tomorrow(10, 0), e.tomorrow(11, 0), func(r *app.ReservationAddRequest) { r.SetName("인터뷰") })
	x.NoError(err)

	e.relate(tripod, kit, true)
	x.Equal("waiting", e.allocation(shoot, tripod).Kind)
	x.Equal("reservation", e.allocation(rehearsal, tripod).Kind)

	// Under the change: the tripod's relation to the kit, by the owner.
	relate := e.about("asset.relate")[idUUID(tripod.GetId())]
	x.NotNil(relate)
	waits, takes := e.about("reservation.wait"), e.about("reservation.take")
	x.ElementsMatch([]uuid.UUID{idUUID(shoot.GetId()), idUUID(tripod.GetId())}, subjects(waits), "the booking that waits, and the member")
	x.ElementsMatch([]uuid.UUID{idUUID(rehearsal.GetId()), idUUID(tripod.GetId())}, subjects(takes), "the booking that took it, and the member")
	for _, v := range []*ent.Event{waits[idUUID(shoot.GetId())], waits[idUUID(tripod.GetId())], takes[idUUID(rehearsal.GetId())], takes[idUUID(tripod.GetId())]} {
		x.NotNil(v.CauseId, v.Kind)
		x.Equal(relate.Id, *v.CauseId, "%s is part of the change", v.Kind)
		x.Equal(relate.ActorId, v.ActorId, "by whoever made it")
	}
	w := value[*app.ReservationAllocationChanged](e, waits[idUUID(tripod.GetId())])
	x.Equal(shoot.GetId(), w.GetReservation())
	x.Equal(tripod.GetId(), w.GetAsset())
	x.Equal("제품 촬영", w.GetName())
	x.Zero(w.GetUnits(), "a member is one thing")

	// Each booking's history says it.
	x.Contains(e.history(shoot), fmt.Sprintf("예약 대기: 삼각대 (제품 촬영 %s)", begins(shoot)))
	x.Contains(e.history(rehearsal), fmt.Sprintf("예약에 잡힘: 삼각대 (리허설 %s)", begins(rehearsal)))

	// And the member's, as the change it is part of, by whom, with what it
	// names as it is called.
	cause := fmt.Sprintf("TR-1 연결 %s (구성품 / 소속)", e.get(kit).GetTag())
	got := e.happened(tripod, "reservation.wait", "reservation.take")
	x.ElementsMatch([]string{
		fmt.Sprintf("예약 대기: 삼각대 (제품 촬영 %s)", begins(shoot)),
		fmt.Sprintf("예약에 잡힘: 삼각대 (리허설 %s)", begins(rehearsal)),
	}, summaries(got))
	for _, en := range got {
		x.Equal(cause, en.GetCause())
		x.Equal("Owner of acme", en.GetActor())
		x.Equal("TR-1 삼각대", en.GetDetail()["asset"])
		if en.GetEventKind() == "reservation.wait" {
			x.Equal("제품 촬영", en.GetDetail()["reservation"], "the booking, by its name")
		}
	}

	// Said once in the feed, as the change.
	x.Empty(e.said("reservation.wait", "reservation.take"), "what the change did to each is theirs")

	// The wait settled once the member is let go: under what let it go, by
	// whoever did.
	e.cancel(lee, interview)
	x.Equal("reservation", e.allocation(shoot, tripod).Kind)
	cancel := e.about("reservation.cancel")[idUUID(interview.GetId())]
	x.NotNil(cancel)
	resolves := e.about("reservation.resolve")
	x.ElementsMatch([]uuid.UUID{idUUID(shoot.GetId()), idUUID(tripod.GetId())}, subjects(resolves))
	for _, v := range resolves {
		x.NotNil(v.CauseId)
		x.Equal(cancel.Id, *v.CauseId, "part of the cancellation")
	}
	x.Contains(e.history(shoot), fmt.Sprintf("예약 확보: 삼각대 (제품 촬영 %s)", begins(shoot)))
	got = e.happened(tripod, "reservation.resolve")
	x.Len(got, 1)
	x.Equal(fmt.Sprintf("예약 확보: 삼각대 (제품 촬영 %s)", begins(shoot)), got[0].GetSummary())
	x.Equal("예약 취소: 인터뷰", got[0].GetCause())
	x.Equal(leeParty.GetName(), got[0].GetActor())
}

// TestWhatABookingLetGoOfIsOnTheHistoryOfEach: every way a member stops being
// one a kit needs -- the relation ended, said never to have held, the member
// voided -- has the booking let go of it, on the history of both, under the
// change; and nothing of what the kit still needs.
func TestWhatABookingLetGoOfIsOnTheHistoryOfEach(t *testing.T) {
	for _, how := range []string{"end", "retract", "void"} {
		t.Run(how, func(t *testing.T) {
			e := newEnv(t)
			x := e.x
			camera := e.item("카메라", "CM-1", nil, day)
			lens := e.item("렌즈", "LN-1", nil, day)
			e.bookable(lens, nil)
			kit := e.kit("촬영 키트", camera, lens)
			r, err := e.reserve(e.owner, kit, e.tomorrow(9, 0), e.tomorrow(12, 0), nil)
			x.NoError(err)

			var change, cause string
			switch how {
			case "end":
				e.unrelate(lens, kit)
				change, cause = "asset.relate", fmt.Sprintf("LN-1 연결 끝 %s (구성품 / 소속)", e.get(kit).GetTag())
			case "retract":
				i := slices.IndexFunc(e.timeline(lens, false), func(en *app.TimelineEntry) bool { return en.GetKind() == "link" })
				x.GreaterOrEqual(i, 0)
				_, err = e.app().Asset().Correct(e.owner, app.AssetCorrectRequest_builder{
					Ref: assetRef(lens), RowId: e.timeline(lens, false)[i].GetRowId(), Retract: true, Reason: "다른 키트의 렌즈",
				}.Build())
				x.NoError(err)
				change, cause = "asset.correct", "LN-1 관계 정정"
			case "void":
				_, err := e.app().Asset().Erase(e.owner, assetRef(lens))
				x.NoError(err)
				change = "asset.void"
			}
			x.Nil(e.allocation(r, lens))

			lets := e.about("reservation.let_go")
			x.ElementsMatch([]uuid.UUID{idUUID(r.GetId()), idUUID(lens.GetId())}, subjects(lets), "the camera is still taken")
			for _, v := range lets {
				x.NotNil(v.CauseId)
				c, err := e.s.Ent.Event.Get(t.Context(), *v.CauseId)
				x.NoError(err)
				x.Equal(change, c.Kind)
				x.Equal(idUUID(lens.GetId()), *c.SubjectId)
			}
			x.Contains(e.history(r), fmt.Sprintf("예약에서 빠짐: 렌즈 (회의 %s)", begins(r)))
			if how == "void" {
				// Voided, it has no history to read.
				return
			}
			got := e.happened(lens, "reservation.let_go")
			x.Len(got, 1)
			x.Equal(fmt.Sprintf("예약에서 빠짐: 렌즈 (회의 %s)", begins(r)), got[0].GetSummary())
			x.Equal(cause, got[0].GetCause())
		})
	}
}

// TestWhatOneKitLetsGoOfAnothersBookingTakes: a member one kit's booking holds
// and another's waits for, taken off the first kit, is let go of by the one
// and taken by the other -- both under the one change.
func TestWhatOneKitLetsGoOfAnothersBookingTakes(t *testing.T) {
	e := newEnv(t)
	x := e.x
	lens := e.item("렌즈", "LN-1", nil, day)
	e.bookable(lens, nil)
	a := e.kit("A 키트", lens)
	b := e.kit("B 키트")
	ra, err := e.reserve(e.owner, a, e.tomorrow(9, 0), e.tomorrow(12, 0), func(r *app.ReservationAddRequest) { r.SetName("A 촬영") })
	x.NoError(err)
	rb, err := e.reserve(e.owner, b, e.tomorrow(10, 0), e.tomorrow(11, 0), func(r *app.ReservationAddRequest) { r.SetName("B 촬영") })
	x.NoError(err)
	e.relate(lens, b, true)
	x.Equal("waiting", e.allocation(rb, lens).Kind)

	e.unrelate(lens, a)
	x.Nil(e.allocation(ra, lens))
	x.Equal("reservation", e.allocation(rb, lens).Kind)

	lets, resolves := e.about("reservation.let_go"), e.about("reservation.resolve")
	x.ElementsMatch([]uuid.UUID{idUUID(ra.GetId()), idUUID(lens.GetId())}, subjects(lets))
	x.ElementsMatch([]uuid.UUID{idUUID(rb.GetId()), idUUID(lens.GetId())}, subjects(resolves))
	end := *lets[idUUID(ra.GetId())].CauseId
	for _, v := range []*ent.Event{lets[idUUID(lens.GetId())], resolves[idUUID(rb.GetId())], resolves[idUUID(lens.GetId())]} {
		x.Equal(end, *v.CauseId, "%s is part of the one change", v.Kind)
	}
	c, err := e.s.Ent.Event.Get(t.Context(), end)
	x.NoError(err)
	x.Equal("asset.relate", c.Kind)
	x.True(value[*app.AssetRelated](e, c).GetEnd())

	x.Contains(e.history(ra), fmt.Sprintf("예약에서 빠짐: 렌즈 (A 촬영 %s)", begins(ra)))
	x.Contains(e.history(rb), fmt.Sprintf("예약 확보: 렌즈 (B 촬영 %s)", begins(rb)))
	cause := fmt.Sprintf("LN-1 연결 끝 %s (구성품 / 소속)", e.get(a).GetTag())
	got := e.happened(lens, "reservation.let_go", "reservation.resolve")
	x.ElementsMatch([]string{
		fmt.Sprintf("예약에서 빠짐: 렌즈 (A 촬영 %s)", begins(ra)),
		fmt.Sprintf("예약 확보: 렌즈 (B 촬영 %s)", begins(rb)),
	}, summaries(got))
	for _, en := range got {
		x.Equal(cause, en.GetCause())
	}
}

// TestAWaitTheSweepSettlesIsItsOwn: what the sweep takes for a booking that
// waited is the deployment's own work and part of nothing else: the booking's
// event is the operation, which the feed says, and the member's its child.
func TestAWaitTheSweepSettlesIsItsOwn(t *testing.T) {
	e := newEnv(t)
	x := e.x
	battery := e.item("배터리", "BT-1", nil, day)
	kit := e.kit("촬영 키트")
	spare := e.kit("예비 키트", battery)
	kim, _ := e.person("member")

	r, err := e.reserve(kim, kit, e.tomorrow(9, 0), e.tomorrow(12, 0), func(req *app.ReservationAddRequest) { req.SetName("제품 촬영") })
	x.NoError(err)
	other, err := e.reserve(e.owner, spare, e.tomorrow(8, 0), e.tomorrow(10, 0), nil)
	x.NoError(err)
	e.relate(battery, kit, true)
	x.Equal("waiting", e.allocation(r, battery).Kind)
	e.cancel(e.owner, other)
	x.Empty(e.eventsOf("reservation.resolve"), "nobody held its schedule, so nothing took it")

	e.sweep()
	x.Equal("reservation", e.allocation(r, battery).Kind)
	own, kids := e.split(e.eventsOf("reservation.resolve"))
	x.Equal(idUUID(r.GetId()), *own.SubjectId, "the booking's is the operation")
	x.Len(kids, 1)
	x.Contains(kids, idUUID(battery.GetId()), "and the member's its child")

	said := fmt.Sprintf("예약 확보: 배터리 (제품 촬영 %s)", begins(r))
	x.Contains(e.history(r), said)
	x.Contains(e.said("reservation.resolve"), said, "the feed says what the deployment did")
	got := e.happened(battery, "reservation.resolve")
	x.Len(got, 1)
	x.Equal(said, got[0].GetSummary())
	x.Equal("시스템", got[0].GetActor())
	x.Empty(got[0].GetCause(), "the operation says what it did to the member, and is not said twice")
}

// TestEachOccurrenceOfASeriesSaysWhatItTook: each time a series takes place is
// a booking of its own, and says of itself what it took and what it waits
// for, by when it begins; the member's history says each.
func TestEachOccurrenceOfASeriesSaysWhatItTook(t *testing.T) {
	e := newEnv(t)
	x := e.x
	tripod := e.item("삼각대", "TR-1", nil, day)
	e.bookable(tripod, func(r *app.BookableAddRequest) { r.SetHorizonDays(120) })
	kit := e.kit("촬영 키트")

	first, err := e.reserve(e.owner, kit, e.tomorrow(9, 0), e.tomorrow(12, 0), func(r *app.ReservationAddRequest) {
		r.SetName("주간 촬영")
		r.SetRepeat("FREQ=WEEKLY;COUNT=3")
	})
	x.NoError(err)
	_, err = e.reserve(e.owner, tripod, e.tomorrow(10, 0).AddDate(0, 0, 7), e.tomorrow(11, 0).AddDate(0, 0, 7), nil)
	x.NoError(err)

	e.relate(tripod, kit, true)
	all, err := e.app().Reservation().List(e.owner, app.ReservationListRequest_builder{
		Filters: []*app.ReservationFilter{app.ReservationFilter_builder{SeriesId: first.GetSeriesId()}.Build()},
	}.Build())
	x.NoError(err)
	x.Len(all.GetItems(), 3)
	want := []string{}
	for _, r := range all.GetItems() {
		did := "예약에 잡힘"
		if e.allocation(r, tripod).Kind == "waiting" {
			did = "예약 대기"
		}
		v := fmt.Sprintf("%s: 삼각대 (주간 촬영 %s)", did, begins(r))
		x.Equal([]string{v}, slices.DeleteFunc(e.history(r), func(s string) bool { return s != v }), "each its own")
		want = append(want, v)
	}
	x.ElementsMatch(want, summaries(e.happened(tripod, "reservation.take", "reservation.wait")))
}

// TestATakeOverWhatWasBookedSaysSo: a booking made over what was there takes
// what its kit is given the same way, and its events say so.
func TestATakeOverWhatWasBookedSaysSo(t *testing.T) {
	e := newEnv(t)
	x := e.x
	tripod := e.item("삼각대", "TR-1", nil, day)
	e.bookable(tripod, nil)
	kit := e.kit("촬영 키트")
	r, err := e.reserve(e.owner, kit, e.tomorrow(9, 0), e.tomorrow(12, 0), func(req *app.ReservationAddRequest) {
		req.SetOverride(true)
		req.SetReason("행사")
	})
	x.NoError(err)
	_, err = e.reserve(e.owner, tripod, e.tomorrow(10, 0), e.tomorrow(11, 0), nil)
	x.NoError(err)

	e.relate(tripod, kit, true)
	x.Equal("override", e.allocation(r, tripod).Kind)
	takes := e.about("reservation.take")
	x.ElementsMatch([]uuid.UUID{idUUID(r.GetId()), idUUID(tripod.GetId())}, subjects(takes))
	for _, v := range takes {
		x.True(value[*app.ReservationAllocationChanged](e, v).GetOverride())
		x.Empty(v.Reason, "the booking's reason is the booking's")
	}
}
