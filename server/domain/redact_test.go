package domain_test

import (
	"archive/zip"
	"bytes"
	"context"
	"fmt"
	"io"
	"maps"
	"slices"
	"testing"
	"time"
	"uuid"

	"github.com/lesomnus/payday/past"
	"github.com/lesomnus/payday/pdid"
	"google.golang.org/grpc/codes"

	app "github.com/lesomnus/rove"
	"github.com/lesomnus/rove/internal/ent"
	entaudit "github.com/lesomnus/rove/internal/ent/audit"
	"github.com/lesomnus/rove/internal/ent/event"
	"github.com/lesomnus/rove/server/pd"
)

// What a person asked to be erased, said in a reason: a name and a number,
// which nothing can tell are in it.
const asked = "박지민 씨 개인 사정으로 이동 (010-2345-6789)"

// moved is an asset put in a room for `why`, and the event that did it.
func (e *env) moved(why string) (*app.Asset, pdid.Id) {
	e.t.Helper()
	room := e.space("회의실", nil)
	a := e.item("노트북", "", nil, 0)
	_, err := e.app().Asset().Move(e.owner, app.AssetMoveRequest_builder{Ref: assetRef(a), To: assetRef(room), Reason: why}.Build())
	e.x.NoError(err)
	v, err := e.s.Ent.Event.Query().Where(event.Kind("asset.move"), event.SubjectId(idUUID(a.GetId()))).Only(context.Background())
	e.x.NoError(err)
	return a, pdid.Id(v.Id)
}

func (e *env) redact(ctx context.Context, id pdid.Id, why string) (*app.Event, error) {
	return e.app().Event().Redact(ctx, app.EventRedactRequest_builder{Ref: app.EventRef_builder{Id: id.Bytes()}.Build(), Reason: why}.Build())
}

// TestAReasonTakenOutIsGoneAndItsTakingIsInTheHistory: an event's reason a
// person asked to have erased is blanked where it was kept -- the row, and so
// the history and the feed that say it, and the workspace's export -- and the
// trail, which never held it, records that it was taken out (payday#151). The
// taking is an event of its own, beside the one it was taken out of, whose
// reason is the request's reference. Nobody's words are anywhere on the trail.
func TestAReasonTakenOutIsGoneAndItsTakingIsInTheHistory(t *testing.T) {
	e := newEnv(t)
	x := e.x
	ctx := context.Background()
	a, id := e.moved(asked)
	// Where it was, before: the row, and so the history that says it and
	// the workspace's export.
	x.True(slices.ContainsFunc(e.timeline(a, false), func(v *app.TimelineEntry) bool { return v.GetReason() == asked }))
	x.Contains(e.exported(), "rows/event.jsonl")

	v, err := e.redact(e.owner, id, "  삭제 요청 2026-17 ")
	x.NoError(err)
	x.Equal(id.Bytes(), v.GetId())
	x.Empty(v.GetReason(), "the event as it is now")
	x.Equal("asset.move", v.GetKind(), "and the rest of it as it was")

	row, err := e.s.Ent.Event.Get(ctx, id.Uuid())
	x.NoError(err)
	x.Empty(row.Reason)

	// The act, in the history it took a reason out of.
	acts, err := e.s.Ent.Event.Query().Where(event.Kind("event.redact")).All(ctx)
	x.NoError(err)
	x.Len(acts, 1)
	act := acts[0]
	x.Equal("삭제 요청 2026-17", act.Reason, "the request's reference, trimmed")
	x.NotNil(act.SubjectId)
	x.Equal(idUUID(a.GetId()), *act.SubjectId, "the redacted event's subject")
	x.Nil(act.CauseId, "part of nothing")
	m := &app.EventRedacted{}
	x.NoError(act.Value.UnmarshalTo(m))
	x.Equal(id.Bytes(), m.GetEvent())

	byKind := map[string]*app.TimelineEntry{}
	for _, v := range e.timeline(a, false) {
		if v.GetKind() == "event" {
			byKind[v.GetEventKind()] = v
		}
	}
	x.Contains(byKind, "asset.move")
	x.Empty(byKind["asset.move"].GetReason())
	x.Contains(byKind, "event.redact")
	x.Equal("작업 기록의 사유 삭제", byKind["event.redact"].GetSummary())
	x.Equal("삭제 요청 2026-17", byKind["event.redact"].GetReason())

	feed, err := e.app().Event().Recent(e.owner, app.EventRecentRequest_builder{}.Build())
	x.NoError(err)
	saw := map[string]bool{}
	for _, v := range feed.GetItems() {
		x.NotContains(v.GetReason(), "박지민")
		saw[v.GetKind()] = true
		if v.GetKind() == "event.redact" {
			x.Equal("작업 기록의 사유 삭제", v.GetDesc())
		}
	}
	x.True(saw["event.redact"], "the feed says it")

	// The trail says it was taken out, and holds none of anybody's words.
	lines, err := e.s.Ent.Audit.Query().Where(entaudit.ObjectId(id.Uuid())).Order(ent.Asc(entaudit.FieldDateCreated), ent.Asc(entaudit.FieldId)).All(ctx)
	x.NoError(err)
	x.NotEmpty(lines)
	x.Equal(past.RedactAction, lines[len(lines)-1].Action)
	all, err := e.s.Ent.Audit.Query().All(ctx)
	x.NoError(err)
	for _, l := range all {
		for _, b := range [][]byte{l.Patch, l.Value} {
			x.NotContains(string(b), "박지민", "%s %s", l.Action, pdid.Id(l.ObjectId))
			x.NotContains(string(b), "2026-17", "%s %s: the request's reference", l.Action, pdid.Id(l.ObjectId))
		}
	}
	queued, err := e.s.Ent.Outbox.Query().All(ctx)
	x.NoError(err)
	for _, l := range queued {
		x.NotContains(string(l.Patch), "박지민")
		if l.Event != nil {
			x.NotContains(string(*l.Event), "박지민")
		}
	}

	// Nor does what the workspace takes out: its rows, its trail, its access
	// record. The reference is the workspace's own, and stays in its history.
	x.Empty(e.exported(), "taken out of the export")
	kept := false
	for name, b := range e.export() {
		if bytes.Contains(b, []byte("삭제 요청 2026-17")) {
			x.Equal("rows/event.jsonl", name, "the reference is somewhere but the history")
			kept = true
		}
	}
	x.True(kept, "the act's reason is the workspace's own history")
}

// export is the workspace's export, file by file.
func (e *env) export() map[string][]byte {
	e.t.Helper()
	var buf bytes.Buffer
	_, err := e.s.Offboard(e.cfg).Export(context.Background(), e.tenant, &buf)
	e.x.NoError(err)
	z, err := zip.NewReader(bytes.NewReader(buf.Bytes()), int64(buf.Len()))
	e.x.NoError(err)
	out := map[string][]byte{}
	for _, f := range z.File {
		r, err := f.Open()
		e.x.NoError(err)
		b, err := io.ReadAll(r)
		e.x.NoError(err)
		e.x.NoError(r.Close())
		out[f.Name] = b
	}
	return out
}

// exported is the files of the workspace's export that say what was asked to
// be erased, any of it.
func (e *env) exported() []string {
	e.t.Helper()
	var out []string
	for name, b := range e.export() {
		if bytes.Contains(b, []byte("박지민")) || bytes.Contains(b, []byte("010-2345-6789")) {
			out = append(out, name)
		}
	}
	return out
}

// TestAReasonIsTakenOutOfAnEventOfTheWorkspaceThatHasOne: what the act asks
// before it does anything. It answers a request, by its reference, and a
// reason of an event the caller may see, which there is: another workspace's
// event is none, and one with nothing to take out -- or nothing left -- is
// refused rather than recorded as taken out.
func TestAReasonIsTakenOutOfAnEventOfTheWorkspaceThatHasOne(t *testing.T) {
	e := newEnv(t)
	x := e.x
	ctx := context.Background()
	_, id := e.moved(asked)

	_, err := e.redact(e.owner, id, "  ")
	x.Equal(codes.InvalidArgument, codeOf(err), "no request it answers")
	_, err = e.redact(e.owner, pdid.New(pd.AssetDomain), "삭제 요청 1")
	x.Equal(codes.InvalidArgument, codeOf(err), "an asset is no event")
	_, err = e.redact(e.owner, pdid.New(pd.EventDomain), "삭제 요청 1")
	x.Equal(codes.NotFound, codeOf(err))

	// Another workspace's, which the wall keeps from this one.
	_, beta := e.newTenant("beta")
	room, err := e.app().Asset().Add(beta, app.AssetAddRequest_builder{Name: "창고", Kind: "space"}.Build())
	x.NoError(err)
	box, err := e.app().Asset().Add(beta, app.AssetAddRequest_builder{Name: "상자"}.Build())
	x.NoError(err)
	_, err = e.app().Asset().Move(beta, app.AssetMoveRequest_builder{Ref: assetRef(box), To: assetRef(room), Reason: asked}.Build())
	x.NoError(err)
	theirs, err := e.s.Ent.Event.Query().Where(event.Kind("asset.move"), event.SubjectId(idUUID(box.GetId()))).Only(ctx)
	x.NoError(err)
	_, err = e.redact(e.owner, pdid.Id(theirs.Id), "삭제 요청 1")
	x.Equal(codes.NotFound, codeOf(err))
	v, err := e.s.Ent.Event.Get(ctx, theirs.Id)
	x.NoError(err)
	x.Equal(asked, v.Reason, "as it was")

	// One that says no reason, and one whose reason is gone already.
	quiet, err := e.s.Ent.Event.Query().Where(event.Kind("asset.add"), event.Reason("")).First(ctx)
	x.NoError(err)
	_, err = e.redact(e.owner, pdid.Id(quiet.Id), "삭제 요청 1")
	x.Equal(codes.FailedPrecondition, codeOf(err))
	_, err = e.redact(e.owner, id, "삭제 요청 2")
	x.NoError(err)
	_, err = e.redact(e.owner, id, "삭제 요청 2")
	x.Equal(codes.FailedPrecondition, codeOf(err), "taken out once")

	n, err := e.s.Ent.Event.Query().Where(event.Kind("event.redact")).Count(ctx)
	x.NoError(err)
	x.Equal(1, n, "what was refused recorded nothing")
}

// TestAnOperatorTakesAReasonOutAsTheDeploymentsOwnWork: the operator's door
// (`rove event redact`) is the same act, done as the deployment's own work in
// the workspace: nobody's, filed under it -- and so is what it does to the
// rest of an operation that says the same, a hand-over's memo copied into
// each thing it handed over.
func TestAnOperatorTakesAReasonOutAsTheDeploymentsOwnWork(t *testing.T) {
	e := newEnv(t)
	x := e.x
	ctx := context.Background()
	_, id := e.moved(asked)

	v, err := e.s.Deps.RedactEvent(ctx, e.s.Base, e.s.Drv, e.tenant, id, "삭제 요청 2026-18")
	x.NoError(err)
	x.Empty(v.Event.GetReason())
	x.Empty(v.Copies, "an operation over one thing, which copied it nowhere")
	act := v.Act
	row, err := e.s.Ent.Event.Get(ctx, act.Uuid())
	x.NoError(err)
	x.Equal("event.redact", row.Kind)
	x.Nil(row.ActorId, "nobody's")
	x.Equal(e.tenant.Uuid(), row.TenantId)
	x.Equal("삭제 요청 2026-18", row.Reason)

	_, err = e.s.Deps.RedactEvent(ctx, e.s.Base, e.s.Drv, e.tenant, id, "삭제 요청 2026-18")
	x.Equal(codes.FailedPrecondition, codeOf(err))
	other, _ := e.newTenant("beta")
	_, err = e.s.Deps.RedactEvent(ctx, e.s.Base, e.s.Drv, other, act, "삭제 요청 2026-18")
	x.Equal(codes.NotFound, codeOf(err), "in another workspace than its own")

	// A hand-over of two things, whose memo each of them says.
	_, kim := e.person("member")
	room := e.space("창고", nil)
	laptop := e.item("노트북", "NB-1", room, day)
	screen := e.item("모니터", "MN-1", room, day)
	_, err = e.app().Custody().Add(e.owner, app.CustodyAddRequest_builder{
		Party: ref[app.PartyRef](kim.GetId()),
		Desc:  asked,
		Lines: []*app.CustodyLineSpec{
			app.CustodyLineSpec_builder{Asset: assetRef(laptop)}.Build(),
			app.CustodyLineSpec_builder{Asset: assetRef(screen)}.Build(),
		},
	}.Build())
	x.NoError(err)
	issue, kids := e.split(e.eventsOf("custody.issue"))
	x.Len(kids, 2)
	for _, k := range kids {
		x.Equal(asked, k.Reason, "each thing's history says why")
	}

	v, err = e.s.Deps.RedactEvent(ctx, e.s.Base, e.s.Drv, e.tenant, pdid.Id(issue.Id), "삭제 요청 2026-22")
	x.NoError(err)
	x.Len(v.Copies, 2)
	for _, k := range kids {
		x.Contains(v.Copies, pdid.Id(k.Id))
		row, err := e.s.Ent.Event.Get(ctx, k.Id)
		x.NoError(err)
		x.Empty(row.Reason)
	}
	for _, c := range e.causedBy(e.kept(v.Act)) {
		x.Equal("event.redact", c.Kind)
		x.Nil(c.ActorId, "nobody's, as the act is")
		x.Equal("삭제 요청 2026-22", c.Reason)
	}
	x.Len(e.causedBy(e.kept(v.Act)), 2)
}

// kept is the event `id`, as it is kept.
func (e *env) kept(id pdid.Id) *ent.Event {
	e.t.Helper()
	v, err := e.s.Ent.Event.Get(context.Background(), id.Uuid())
	e.x.NoError(err)
	return v
}

// redacted is what the act whose own event is `act` says it took the words
// out of: its own, and each child's, by the event each names.
func (e *env) redacted(act *ent.Event) map[pdid.Id]*ent.Event {
	e.t.Helper()
	out := map[pdid.Id]*ent.Event{}
	for _, v := range append([]*ent.Event{act}, e.causedBy(act)...) {
		e.x.Equal("event.redact", v.Kind)
		m := &app.EventRedacted{}
		e.x.NoError(v.Value.UnmarshalTo(m))
		of, err := pdid.From(m.GetEvent())
		e.x.NoError(err)
		out[of] = v
	}
	return out
}

// TestAReasonIsTakenOutOfEveryEventOfItsOperationThatSaysIt: an operation's
// reason is copied into each part of it, all the way down -- a count's
// settling of what it never saw as lost, the change of status that called,
// and the kit that change took it off -- and the words asked of are taken out
// of every one, in the one act: an event of its own about the one asked of,
// and a child of it about each of the others, in the history that one is in,
// each on the trail. What the operation caused that keeps no reason of its
// own -- the kit's booking letting go of it -- is as it was, and so are the
// same words said by another operation, which nobody asked of.
func TestAReasonIsTakenOutOfEveryEventOfItsOperationThatSaysIt(t *testing.T) {
	e := newEnv(t)
	x := e.x
	ctx := context.Background()
	_, elsewhere := e.moved(asked)
	room := e.space("창고", nil)
	camera := e.item("카메라", "CM-1", room, 30*day)
	kit := e.kit("촬영 키트", camera)
	_, err := e.reserve(e.owner, kit, e.tomorrow(9, 0), e.tomorrow(12, 0), nil)
	x.NoError(err)

	c, err := e.app().InventoryCount().Add(e.owner, app.InventoryCountAddRequest_builder{Scope: assetRef(room)}.Build())
	x.NoError(err)
	_, err = e.app().InventoryCount().Reconcile(e.owner, app.InventoryCountReconcileRequest_builder{Ref: ref[app.InventoryCountRef](c.GetId())}.Build())
	x.NoError(err)
	fs, err := e.app().CountFinding().List(e.owner, app.CountFindingListRequest_builder{
		Filters: []*app.CountFindingFilter{app.CountFindingFilter_builder{Asset: assetRef(camera)}.Build()},
	}.Build())
	x.NoError(err)
	x.Len(fs.GetItems(), 1)
	e.now = e.now.Add(time.Hour)
	_, err = e.app().InventoryCount().Resolve(e.owner, app.InventoryCountResolveRequest_builder{
		Ref: ref[app.CountFindingRef](fs.GetItems()[0].GetId()), Resolution: "lost", Reason: asked, Leave: []*app.AssetRef{assetRef(kit)},
	}.Build())
	x.NoError(err)

	resolves := e.eventsOf("count.resolve")
	x.Len(resolves, 1)
	resolve := resolves[0]
	sets := e.causedBy(resolve)
	x.Len(sets, 1)
	ends := e.causedBy(sets[0])
	x.Len(ends, 1)
	x.Equal("asset.relate", ends[0].Kind)
	parts := []*ent.Event{resolve, sets[0], ends[0]}
	for _, v := range parts {
		x.Equal(asked, v.Reason, "%s copies the operation's", v.Kind)
	}
	caused := e.causedBy(ends[0])
	x.NotEmpty(caused, "the kit's booking let go of it")
	for _, v := range caused {
		x.Empty(v.Reason, "%s is what it caused, which keeps no reason (D39)", v.Kind)
	}
	x.True(slices.ContainsFunc(e.timeline(camera, false), func(v *app.TimelineEntry) bool { return v.GetReason() == asked }))

	// Asked of what it called, all the way down: it is all one operation.
	before := e.now
	e.now = e.now.Add(time.Minute)
	v, err := e.redact(e.owner, pdid.Id(ends[0].Id), "삭제 요청 2026-21")
	x.NoError(err)
	x.Empty(v.GetReason())
	for _, p := range parts {
		row, err := e.s.Ent.Event.Get(ctx, p.Id)
		x.NoError(err)
		x.Empty(row.Reason, "%s", p.Kind)
	}
	for _, p := range caused {
		row, err := e.s.Ent.Event.Get(ctx, p.Id)
		x.NoError(err)
		x.Equal(p.Value, row.Value, "%s as it was", p.Kind)
	}
	row, err := e.s.Ent.Event.Get(ctx, elsewhere.Uuid())
	x.NoError(err)
	x.Equal(asked, row.Reason, "another operation's, which nobody asked of")

	// The act: its own about the one asked of, and one for each of the
	// rest, each about what that one was about.
	acts := e.eventsOf("event.redact")
	x.Len(acts, 3)
	var own *ent.Event
	for _, a := range acts {
		x.Equal("삭제 요청 2026-21", a.Reason, "the request's reference, which each part copies")
		x.True(a.OccurredAt.After(before))
		if a.CauseId == nil {
			x.Nil(own, "one act")
			own = a
		}
	}
	x.NotNil(own)
	took := e.redacted(own)
	x.Len(took, 3)
	x.Equal(own, took[pdid.Id(ends[0].Id)], "its own is of the one asked of")
	for _, p := range parts {
		a, ok := took[pdid.Id(p.Id)]
		x.True(ok, "%s", p.Kind)
		x.Equal(p.SubjectId, a.SubjectId, "in the history %s is in", p.Kind)
	}

	// Each on the trail as taken out, and nothing anybody said anywhere in
	// it, nor in the camera's history.
	for _, p := range parts {
		lines, err := e.s.Ent.Audit.Query().Where(entaudit.ObjectId(p.Id)).Order(ent.Asc(entaudit.FieldDateCreated), ent.Asc(entaudit.FieldId)).All(ctx)
		x.NoError(err)
		x.Equal(past.RedactAction, lines[len(lines)-1].Action, "%s", p.Kind)
	}
	all, err := e.s.Ent.Audit.Query().All(ctx)
	x.NoError(err)
	for _, l := range all {
		for _, b := range [][]byte{l.Patch, l.Value} {
			x.NotContains(string(b), "박지민", "%s %s", l.Action, pdid.Id(l.ObjectId))
		}
	}
	n := 0
	for _, en := range e.timeline(camera, false) {
		x.NotEqual(asked, en.GetReason())
		if en.GetEventKind() == "event.redact" {
			x.Equal("작업 기록의 사유 삭제", en.GetSummary())
			x.Equal("삭제 요청 2026-21", en.GetReason())
			n++
		}
	}
	x.Equal(3, n, "where each was taken out of")

	// None of it is there to take out again, whichever is asked of.
	for _, p := range parts {
		_, err := e.redact(e.owner, pdid.Id(p.Id), "삭제 요청 2026-21")
		x.Equal(codes.FailedPrecondition, codeOf(err), "%s", p.Kind)
	}
	x.Len(e.eventsOf("event.redact"), 3, "what was refused recorded nothing")
}

// TestAReasonTakenOutOfOneTimeOfASeriesIsTakenOutOfTheBooking: what a series
// booked is one booking, whose reason each time it takes place copies, and
// each thing it booked: asked of the third time, the words are taken out of
// the booking and every time and thing, and each one's history says so.
func TestAReasonTakenOutOfOneTimeOfASeriesIsTakenOutOfTheBooking(t *testing.T) {
	e := newEnv(t)
	x := e.x
	room := e.space("회의실", nil)
	screen := e.space("스크린", nil)
	e.bookable(room, func(r *app.BookableAddRequest) { r.SetHorizonDays(120) })
	e.bookable(screen, func(r *app.BookableAddRequest) { r.SetHorizonDays(120) })
	first, err := e.reserve(e.owner, room, e.tomorrow(10, 0), e.tomorrow(11, 0), func(r *app.ReservationAddRequest) {
		r.SetName("주간 회의")
		r.SetRepeat("FREQ=WEEKLY;COUNT=3")
		r.SetReason(asked)
		r.SetItems(append(r.GetItems(), app.ReservationItemSpec_builder{Resource: assetRef(screen)}.Build()))
	})
	x.NoError(err)
	booked, kids := e.split(e.eventsOf("reservation.add"))
	x.Len(kids, 4, "the room, the screen, and each time after the first")
	// The third time it takes place: the last, by when it begins.
	var (
		third *ent.Event
		last  time.Time
	)
	for _, k := range kids {
		x.Equal(asked, k.Reason, "each copies the booking's")
		m := &app.ReservationAdded{}
		x.NoError(k.Value.UnmarshalTo(m))
		if at := m.GetBeginsAt().AsTime(); len(m.GetAsset()) == 0 && at.After(last) {
			last, third = at, k
		}
	}
	x.NotNil(third)
	x.NotContains([]uuid.UUID{idUUID(first.GetId()), idUUID(room.GetId()), idUUID(screen.GetId())}, *third.SubjectId)

	e.now = e.now.Add(time.Minute)
	v, err := e.redact(e.owner, pdid.Id(third.Id), "삭제 요청 2026-23")
	x.NoError(err)
	x.Empty(v.GetReason())
	for _, p := range append([]*ent.Event{booked}, slices.Collect(maps.Values(kids))...) {
		x.Empty(e.kept(pdid.Id(p.Id)).Reason)
	}
	acts := e.eventsOf("event.redact")
	x.Len(acts, 5)
	own, _ := e.split(acts)
	x.Equal(*third.SubjectId, *own.SubjectId, "its own is about the time asked of")
	took := e.redacted(own)
	x.Len(took, 5)
	x.Contains(took, pdid.Id(booked.Id), "and the booking it was copied from is one of the rest")

	// Each time it takes place says so in its history, and with no words of
	// the booking's.
	list, err := e.app().Reservation().List(e.owner, app.ReservationListRequest_builder{
		Filters: []*app.ReservationFilter{app.ReservationFilter_builder{SeriesId: first.GetSeriesId()}.Build()},
	}.Build())
	x.NoError(err)
	x.Len(list.GetItems(), 3)
	for _, r := range list.GetItems() {
		es := e.reservationTimeline(e.owner, r)
		x.Equal([]string{"event.redact", "reservation.add"}, kindsOf(es))
		x.Equal("삭제 요청 2026-23", es[0].GetReason())
		x.Empty(es[1].GetReason())
	}
}

// TestAReasonTakenOutTwiceAtOnceIsTakenOutOnce: two takings of one reason at
// once -- a request answered twice, from two places -- are one taking: one
// act recorded, and the other refused, having found nothing left to take.
// So are two of one operation's words asked of two of its events: a
// hand-over's, and what it did to each thing it handed over.
func TestAReasonTakenOutTwiceAtOnceIsTakenOutOnce(t *testing.T) {
	e := newEnv(t)
	e.postgresOnly()
	x := e.x
	_, id := e.moved(asked)

	errs := together(8, func(i int) error {
		_, err := e.redact(e.owner, id, "삭제 요청 2026-17")
		return err
	})
	ok := 0
	for _, err := range errs {
		if err == nil {
			ok++
			continue
		}
		x.Equal(codes.FailedPrecondition, codeOf(err), "%v", err)
	}
	x.Equal(1, ok)
	n, err := e.s.Ent.Event.Query().Where(event.Kind("event.redact")).Count(context.Background())
	x.NoError(err)
	x.Equal(1, n)

	_, kim := e.person("member")
	room := e.space("창고", nil)
	things := []*app.CustodyLineSpec{}
	for i := range 3 {
		a := e.item(fmt.Sprintf("노트북 %d", i), fmt.Sprintf("NB-%d", i), room, day)
		things = append(things, app.CustodyLineSpec_builder{Asset: assetRef(a)}.Build())
	}
	_, err = e.app().Custody().Add(e.owner, app.CustodyAddRequest_builder{Party: ref[app.PartyRef](kim.GetId()), Desc: asked, Lines: things}.Build())
	x.NoError(err)
	issue, kids := e.split(e.eventsOf("custody.issue"))
	named := []pdid.Id{pdid.Id(issue.Id)}
	for _, k := range kids {
		named = append(named, pdid.Id(k.Id))
	}
	errs = together(8, func(i int) error {
		_, err := e.redact(e.owner, named[i%len(named)], "삭제 요청 2026-24")
		return err
	})
	ok = 0
	for _, err := range errs {
		if err == nil {
			ok++
			continue
		}
		x.Equal(codes.FailedPrecondition, codeOf(err), "%v", err)
	}
	x.Equal(1, ok)
	acts, err := e.s.Ent.Event.Query().Where(event.Kind("event.redact"), event.Reason("삭제 요청 2026-24")).All(context.Background())
	x.NoError(err)
	x.Len(acts, 4, "one act, of the four that said it")
	for _, v := range named {
		x.Empty(e.kept(v).Reason)
	}
}
