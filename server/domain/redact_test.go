package domain_test

import (
	"archive/zip"
	"bytes"
	"context"
	"fmt"
	"io"
	"maps"
	"slices"
	"strings"
	"testing"
	"time"
	"uuid"

	"github.com/lesomnus/payday/past"
	"github.com/lesomnus/payday/pdid"
	"google.golang.org/grpc/codes"
	"google.golang.org/protobuf/types/known/timestamppb"

	app "github.com/lesomnus/rove"
	"github.com/lesomnus/rove/internal/ent"
	entaudit "github.com/lesomnus/rove/internal/ent/audit"
	"github.com/lesomnus/rove/internal/ent/event"
	"github.com/lesomnus/rove/internal/ent/holder"
	"github.com/lesomnus/rove/internal/ent/notification"
	"github.com/lesomnus/rove/server/domain"
	"github.com/lesomnus/rove/server/mail"
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

// TestAReasonIsTakenOutOfTheNoticesThatToldIt: whoever asked for a
// reservation is told it was decided, and why -- in the decision's words, in
// a notice of their own (`reservation.decided`, design D51). Taking the words
// out of the decision takes them out of the notice, in the same act and
// through the servers: the notice's trail says the act wrote it, its inbox
// says what was decided and no longer why, and the rest of it is as it was.
// A series decided at once told each time it takes place, and asked of one of
// them, the words are taken out of every notice. A notice of another
// decision is as it was, whatever it says.
func TestAReasonIsTakenOutOfTheNoticesThatToldIt(t *testing.T) {
	e := newEnv(t)
	x := e.x
	ctx := context.Background()
	hall := e.space("강당", nil)
	e.bookable(hall, func(r *app.BookableAddRequest) { r.SetApproval(true); r.SetHorizonDays(120) })
	kim, _ := e.person("member")
	boss, _ := e.person("manager")
	request := func(at int, edit func(*app.ReservationAddRequest)) *app.Reservation {
		r, err := e.reserve(kim, hall, e.tomorrow(at, 0), e.tomorrow(at+1, 0), edit)
		x.NoError(err)
		x.Equal("requested", r.GetStatus())
		return r
	}
	reject := func(r *app.Reservation, why string, series bool) {
		_, err := e.app().Reservation().Reject(boss, app.ReservationDecideRequest_builder{Ref: ref[app.ReservationRef](r.GetId()), Reason: why, Series: series}.Build())
		x.NoError(err)
	}
	told := func(r *app.Reservation) *ent.Notification {
		v, err := e.s.Ent.Notification.Query().Where(notification.Kind("reservation.decided"), notification.SubjectId(idUUID(r.GetId()))).Only(ctx)
		x.NoError(err)
		return v
	}
	trail := func(n *ent.Notification) []*ent.Audit {
		lines, err := e.s.Ent.Audit.Query().Where(entaudit.ObjectId(n.Id)).Order(ent.Asc(entaudit.FieldDateCreated), ent.Asc(entaudit.FieldId)).All(ctx)
		x.NoError(err)
		return lines
	}

	// Rejected for what a person asked to have erased; and two more, one in
	// the same words and one in others, which nobody asked of.
	r, same, apart := request(9, nil), request(11, nil), request(13, nil)
	reject(r, asked, false)
	reject(same, asked, false)
	reject(apart, "강당 공사", false)
	was := told(r)
	x.Equal(asked, was.Desc, "told why, in the decision's words")
	rejected, err := e.s.Ent.Event.Query().Where(event.Kind("reservation.reject"), event.SubjectId(idUUID(r.GetId()))).Only(ctx)
	x.NoError(err)

	e.now = e.now.Add(time.Minute)
	_, err = e.redact(e.owner, pdid.Id(rejected.Id), "삭제 요청 2026-27")
	x.NoError(err)
	x.Empty(e.kept(pdid.Id(rejected.Id)).Reason)
	now := told(r)
	x.Empty(now.Desc, "taken out of the notice")
	x.Equal(was.Name, now.Name, "and the rest of it as it was")
	x.Equal(was.Kind, now.Kind)
	x.Equal(was.HolderId, now.HolderId)
	x.Equal(was.Link, now.Link)
	x.Equal(was.ReadAt, now.ReadAt)
	x.Equal(was.Mail, now.Mail)
	x.Equal(asked, told(same).Desc, "another decision's, which nobody asked of")
	x.Equal("강당 공사", told(apart).Desc)

	inbox, err := e.app().Notification().Inbox(kim, &app.NotificationInboxRequest{})
	x.NoError(err)
	i := slices.IndexFunc(inbox.GetItems(), func(v *app.Notification) bool { return idUUID(v.GetId()) == was.Id })
	x.GreaterOrEqual(i, 0)
	x.Equal(was.Name, inbox.GetItems()[i].GetName(), "what was decided")
	x.Empty(inbox.GetItems()[i].GetDesc(), "and not why")

	// The act wrote it, as the owner who did it -- over the wire the line
	// names the call (`cmd`'s test) -- and the trail holds none of anybody's
	// words.
	owner, err := e.s.Ent.Holder.Query().Where(holder.Alias("owner"), holder.TenantId(e.tenant.Uuid())).Only(ctx)
	x.NoError(err)
	lines := trail(now)
	last := lines[len(lines)-1]
	x.Equal(app.NotificationService_Patch_FullMethodName, last.Action)
	x.Equal(owner.Id, last.ActorId)
	x.Len(trail(told(same)), len(trail(told(apart))), "nothing written of the others")
	all, err := e.s.Ent.Audit.Query().All(ctx)
	x.NoError(err)
	for _, l := range all {
		for _, b := range [][]byte{l.Patch, l.Value} {
			x.NotContains(string(b), "박지민", "%s %s", l.Action, pdid.Id(l.ObjectId))
		}
	}

	// A series, rejected at once: each time it takes place was told. Asked
	// of the third, every notice of it is taken out -- here by the
	// operator, whose act says which.
	const words = "이수민 씨 병가로 반려 (010-9876-5432)"
	weekly := request(15, func(r *app.ReservationAddRequest) { r.SetRepeat("FREQ=WEEKLY;COUNT=3") })
	reject(weekly, words, true)
	list, err := e.app().Reservation().List(e.owner, app.ReservationListRequest_builder{
		Filters: []*app.ReservationFilter{app.ReservationFilter_builder{SeriesId: weekly.GetSeriesId()}.Build()},
	}.Build())
	x.NoError(err)
	times := list.GetItems()
	x.Len(times, 3)
	var notices []pdid.Id
	for _, v := range times {
		n := told(v)
		x.Equal(words, n.Desc, "each time it takes place was told why")
		notices = append(notices, pdid.Id(n.Id))
	}
	third := slices.MaxFunc(times, func(a, b *app.Reservation) int { return a.GetBeginsAt().AsTime().Compare(b.GetBeginsAt().AsTime()) })
	of, err := e.s.Ent.Event.Query().Where(event.Kind("reservation.reject"), event.SubjectId(idUUID(third.GetId()))).Only(ctx)
	x.NoError(err)
	x.NotNil(of.CauseId, "a part of the decision")
	copies, err := domain.Copies(ctx, e.s.Ent, e.tenant, pdid.Id(of.Id))
	x.NoError(err)
	found, err := domain.Told(ctx, e.s.Ent, e.tenant, pdid.Id(of.Id), copies)
	x.NoError(err)
	x.ElementsMatch(notices, found, "found before anything is done")

	v, err := e.s.Deps.RedactEvent(ctx, e.s.Base, e.s.Drv, e.tenant, pdid.Id(of.Id), "삭제 요청 2026-28")
	x.NoError(err)
	x.Len(v.Copies, 2)
	x.ElementsMatch(notices, v.Notices)
	for _, v := range times {
		n := told(v)
		x.Empty(n.Desc)
		lines := trail(n)
		x.Equal(app.NotificationService_Patch_FullMethodName, lines[len(lines)-1].Action)
		x.Equal(uuid.UUID{}, lines[len(lines)-1].ActorId, "the deployment's own work, as the act is")
	}
	x.Equal(asked, told(same).Desc, "as it was")
}

// meanwhile is a mail server that does `then` with each mail it was sent,
// once it has it: what happens while the mailer's pass is sending.
type meanwhile struct {
	*mailbox
	then func(mail.Message)
}

func (m meanwhile) Send(ctx context.Context, v mail.Message) error {
	if err := m.mailbox.Send(ctx, v); err != nil {
		return err
	}
	m.then(v)
	return nil
}

// TestWordsTakenOutOfANoticeAreNotMailed: a decision's notice goes by mail as
// well, after the operation and not inside it -- so the words can be taken
// out of it before it goes, and it goes without them: even when the mailer's
// pass had read it already, and was sending what was before it. What went by
// mail before they were taken out is not taken back (design D51).
func TestWordsTakenOutOfANoticeAreNotMailed(t *testing.T) {
	e := newEnv(t)
	x := e.x
	ctx := context.Background()
	box := e.mailing()
	hall := e.space("강당", nil)
	e.bookable(hall, func(r *app.BookableAddRequest) { r.SetApproval(true) })
	kim, kimP := e.person("member")
	const before, after = "박지민 씨 개인 사정 (먼저)", "박지민 씨 개인 사정 (나중)"
	rejected := map[string]pdid.Id{}
	for i, name := range []string{"첫 회의", "둘째 회의"} {
		r, err := e.reserve(kim, hall, e.tomorrow(9+2*i, 0), e.tomorrow(10+2*i, 0), func(r *app.ReservationAddRequest) { r.SetName(name) })
		x.NoError(err)
		e.now = e.now.Add(time.Minute)
		_, err = e.app().Reservation().Reject(e.owner, app.ReservationDecideRequest_builder{Ref: ref[app.ReservationRef](r.GetId()), Reason: []string{before, after}[i]}.Build())
		x.NoError(err)
		v, err := e.s.Ent.Event.Query().Where(event.Kind("reservation.reject"), event.SubjectId(idUUID(r.GetId()))).Only(ctx)
		x.NoError(err)
		rejected[name] = pdid.Id(v.Id)
	}

	// The first is sent, and while the pass sends it, the second's words are
	// taken out; the pass read both before it sent either.
	took := false
	e.s.Deps.Mail = meanwhile{box, func(v mail.Message) {
		if v.To == kimP.GetEmail() && strings.Contains(v.Subject, "첫 회의") {
			_, err := e.redact(e.owner, rejected["둘째 회의"], "삭제 요청 2026-29")
			x.NoError(err)
			took = true
		}
	}}
	e.mail()
	x.True(took)
	got := box.to(kimP.GetEmail())
	x.Len(got, 2)
	for _, v := range got {
		switch {
		case strings.Contains(v.Subject, "첫 회의"):
			x.Contains(v.Body, before, "sent before anybody asked")
		case strings.Contains(v.Subject, "둘째 회의"):
			x.NotContains(v.Body, "박지민", "sent as it says now")
			x.Contains(v.Body, "https://rove.example/@acme/reservations", "and still what it is about")
		default:
			x.Fail("a mail of neither", v.Subject)
		}
	}

	// Taken out of the first now, which went by mail before: the notice says
	// it no more, and what was mailed is not Rove's to take back.
	_, err := e.redact(e.owner, rejected["첫 회의"], "삭제 요청 2026-30")
	x.NoError(err)
	n, err := e.s.Ent.Notification.Query().Where(notification.Kind("reservation.decided"), notification.DescNEQ("")).Count(ctx)
	x.NoError(err)
	x.Zero(n)
	sent, err := e.s.Ent.Notification.Query().Where(notification.Kind("reservation.decided"), notification.DateMailedNotNil()).Count(ctx)
	x.NoError(err)
	x.Equal(2, sent)
}

// offTheTrail asserts that no line of the trail holds `words`: not the row a
// write left, nor what it set.
func (e *env) offTheTrail(words string) {
	e.t.Helper()
	all, err := e.s.Ent.Audit.Query().All(context.Background())
	e.x.NoError(err)
	for _, l := range all {
		for _, b := range [][]byte{l.Patch, l.Value} {
			e.x.NotContains(string(b), words, "%s %s", l.Action, pdid.Id(l.ObjectId))
		}
	}
}

// lastLine is the newest line of the trail about `id`.
func (e *env) lastLine(id uuid.UUID) *ent.Audit {
	e.t.Helper()
	v, err := e.s.Ent.Audit.Query().Where(entaudit.ObjectId(id)).Order(ent.Desc(entaudit.FieldDateCreated), ent.Desc(entaudit.FieldId)).First(context.Background())
	e.x.NoError(err)
	return v
}

// TestAReasonIsTakenOutOfTheHandOverWhoseMemoItIs: a hand-over's memo is the
// reason its event says, in the words of whoever made it -- each thing it
// handed over says it too (D49) -- and the hand-over keeps it as its own,
// declared personal as the reason is, so the trail never held it. Asked of
// one thing's event, the words are taken out of the hand-over as well, in the
// one act and through the servers, and the rest of it is as it was. Another
// hand-over in the same words, which nobody asked of, keeps them.
func TestAReasonIsTakenOutOfTheHandOverWhoseMemoItIs(t *testing.T) {
	e := newEnv(t)
	x := e.x
	ctx := context.Background()
	_, kim := e.person("member")
	room := e.space("창고", nil)
	handOver := func(things ...*app.Asset) *app.Custody {
		lines := []*app.CustodyLineSpec{}
		for _, a := range things {
			lines = append(lines, app.CustodyLineSpec_builder{Asset: assetRef(a)}.Build())
		}
		c, err := e.app().Custody().Add(e.owner, app.CustodyAddRequest_builder{
			Party: ref[app.PartyRef](kim.GetId()),
			Desc:  asked,
			DueAt: e.in(7 * day),
			Lines: lines,
		}.Build())
		x.NoError(err)
		return c
	}
	memo := func(c *app.Custody) *app.Custody {
		v, err := e.app().Custody().Get(e.owner, app.CustodyGetRequest_builder{Ref: ref[app.CustodyRef](c.GetId())}.Build())
		x.NoError(err)
		return v
	}
	c := handOver(e.item("노트북", "NB-1", room, day), e.item("모니터", "MN-1", room, day))
	same := handOver(e.item("휴대폰", "PH-1", room, day))
	x.Equal(asked, c.GetDesc(), "its memo")
	e.offTheTrail("박지민")

	issue, err := e.s.Ent.Event.Query().Where(event.Kind("custody.issue"), event.SubjectId(idUUID(c.GetId()))).Only(ctx)
	x.NoError(err)
	x.Equal(asked, issue.Reason, "the memo is why it was made")
	kids := e.causedBy(issue)
	x.Len(kids, 2)
	of := pdid.Id(kids[0].Id)
	copies, err := domain.Copies(ctx, e.s.Ent, e.tenant, of)
	x.NoError(err)
	found, err := domain.Kept(ctx, e.s.Ent, e.tenant, of, copies)
	x.NoError(err)
	x.Equal([]pdid.Id{pdid.Id(idUUID(c.GetId()))}, found, "found before anything is done")

	// Asked of what one thing's history says.
	e.now = e.now.Add(time.Minute)
	_, err = e.redact(e.owner, of, "삭제 요청 2026-31")
	x.NoError(err)
	for _, v := range append([]*ent.Event{issue}, kids...) {
		x.Empty(e.kept(pdid.Id(v.Id)).Reason)
	}
	now := memo(c)
	x.Empty(now.GetDesc(), "taken out of the memo")
	x.Equal(c.GetKind(), now.GetKind(), "and the rest of it as it was")
	x.Equal(c.GetStatus(), now.GetStatus())
	x.Equal(c.GetParty().GetId(), now.GetParty().GetId())
	x.True(c.GetDueAt().AsTime().Equal(now.GetDueAt().AsTime()))
	x.True(c.GetIssuedAt().AsTime().Equal(now.GetIssuedAt().AsTime()))
	x.Equal(asked, memo(same).GetDesc(), "another hand-over's, which nobody asked of")

	// The act wrote it, as the owner who did it -- over the wire the line
	// names the call (`cmd`'s test) -- and the trail holds none of it.
	owner, err := e.s.Ent.Holder.Query().Where(holder.Alias("owner"), holder.TenantId(e.tenant.Uuid())).Only(ctx)
	x.NoError(err)
	last := e.lastLine(idUUID(c.GetId()))
	x.Equal(app.CustodyService_Patch_FullMethodName, last.Action)
	x.Equal(owner.Id, last.ActorId)
	x.NotEqual(app.CustodyService_Patch_FullMethodName, e.lastLine(idUUID(same.GetId())).Action, "nothing written of the other")
	e.offTheTrail("박지민")
}

// TestAReasonIsTakenOutOfTheWorkspaceItClosed: why a workspace was closed is
// the reason its closing says, in the words of whoever closed it, and the
// workspace keeps it as its own while it is closed by that closing, declared
// personal as the reason is. Closed, restored and closed again in the same
// words, the workspace says why the last closing was: asked of the first,
// nothing is taken out of it, and asked of the last -- here by the operator,
// on the workspace's word -- the words go from it too, and the rest of the
// closing stays.
func TestAReasonIsTakenOutOfTheWorkspaceItClosed(t *testing.T) {
	e := newEnv(t)
	x := e.x
	ctx := context.Background()
	ws := app.TenantRef_builder{Id: e.tenant.Bytes()}.Build()
	cur, err := e.app().Tenant().Get(e.owner, app.TenantGetRequest_builder{Ref: ws}.Build())
	x.NoError(err)
	closing := func() *ent.Event {
		e.now = e.now.Add(time.Minute)
		_, err := e.app().Tenant().Close(e.owner, app.TenantCloseRequest_builder{Ref: ws, Confirm: cur.GetName(), Reason: asked}.Build())
		x.NoError(err)
		vs := e.eventsOf("workspace.close")
		return slices.MaxFunc(vs, func(a, b *ent.Event) int { return a.DateCreated.Compare(b.DateCreated) })
	}
	first := closing()
	e.now = e.now.Add(time.Minute)
	_, err = e.app().Tenant().Restore(e.owner, app.TenantRestoreRequest_builder{Ref: ws}.Build())
	x.NoError(err)
	second := closing()
	x.NotEqual(first.Id, second.Id)
	was, err := e.s.Ent.Tenant.Get(ctx, e.tenant.Uuid())
	x.NoError(err)
	x.Equal(asked, was.CloseReason)
	x.Equal(asked, second.Reason, "why it was closed")
	e.offTheTrail("박지민")

	e.now = e.now.Add(time.Minute)
	v, err := e.s.Deps.RedactEvent(ctx, e.s.Base, e.s.Drv, e.tenant, pdid.Id(first.Id), "삭제 요청 2026-32")
	x.NoError(err)
	x.Empty(v.Rows, "the first closing is not what it is closed by")
	row, err := e.s.Ent.Tenant.Get(ctx, e.tenant.Uuid())
	x.NoError(err)
	x.Equal(asked, row.CloseReason, "the last closing's words, which nobody asked of")

	v, err = e.s.Deps.RedactEvent(ctx, e.s.Base, e.s.Drv, e.tenant, pdid.Id(second.Id), "삭제 요청 2026-33")
	x.NoError(err)
	x.Equal([]pdid.Id{e.tenant}, v.Rows)
	x.Empty(e.kept(pdid.Id(second.Id)).Reason)
	row, err = e.s.Ent.Tenant.Get(ctx, e.tenant.Uuid())
	x.NoError(err)
	x.Empty(row.CloseReason, "taken out of the workspace")
	x.Equal(was.ClosedBy, row.ClosedBy, "and the rest of the closing as it was")
	x.Equal(was.DateClosed, row.DateClosed)
	x.Equal(was.DateDestroy, row.DateDestroy)
	x.Equal(was.Name, row.Name)

	last := e.lastLine(e.tenant.Uuid())
	x.Equal(app.TenantService_Patch_FullMethodName, last.Action)
	x.Equal(uuid.UUID{}, last.ActorId, "the deployment's own work, as the act is")
	e.offTheTrail("박지민")
}

// TestAReasonIsTakenOutOfTheFindingItsScanLeft: a scan's note is the reason
// its event says, and the finding it left keeps it as its own, declared
// personal as the reason is. A thing has one finding in a count, the first
// scan's: another scan of it says where it was seen last, and asked of that
// one, nothing is taken out of the finding; asked of the first, the words go
// from it too. What a scan found no thing for is a finding of its own.
func TestAReasonIsTakenOutOfTheFindingItsScanLeft(t *testing.T) {
	e := newEnv(t)
	x := e.x
	ctx := context.Background()
	room := e.space("창고", nil)
	e.item("노트북", "NB-1", room, day)
	c, err := e.app().InventoryCount().Add(e.owner, app.InventoryCountAddRequest_builder{Scope: assetRef(room)}.Build())
	x.NoError(err)
	scan := func(code, note string) (*app.CountFinding, pdid.Id) {
		e.now = e.now.Add(time.Minute)
		op := newOp()
		v, err := e.app().InventoryCount().Scan(e.owner, app.InventoryCountScanRequest_builder{Ref: ref[app.InventoryCountRef](c.GetId()), Code: code, Note: note, Op: op}.Build())
		x.NoError(err)
		return v, pdid.Id(idUUID(op))
	}
	finding := func(f *app.CountFinding) *ent.CountFinding {
		v, err := e.s.Ent.CountFinding.Get(ctx, idUUID(f.GetId()))
		x.NoError(err)
		return v
	}

	seen, first := scan("NB-1", asked)
	again, later := scan("NB-1", "책상 위")
	x.Equal(seen.GetId(), again.GetId(), "one finding of a thing")
	unknown, stray := scan("no-such-thing", asked)
	x.Equal("unknown", unknown.GetKind())
	x.Equal(asked, finding(seen).Desc, "the first scan's note")
	x.Equal(asked, finding(unknown).Desc)
	e.offTheTrail("박지민")

	e.now = e.now.Add(time.Minute)
	v, err := e.s.Deps.RedactEvent(ctx, e.s.Base, e.s.Drv, e.tenant, later, "삭제 요청 2026-34")
	x.NoError(err)
	x.Empty(v.Rows, "the finding keeps the first scan's words, and not the later one's")
	x.Equal(asked, finding(seen).Desc)

	v, err = e.s.Deps.RedactEvent(ctx, e.s.Base, e.s.Drv, e.tenant, first, "삭제 요청 2026-35")
	x.NoError(err)
	x.Equal([]pdid.Id{pdid.Id(idUUID(seen.GetId()))}, v.Rows)
	now := finding(seen)
	x.Empty(now.Desc, "taken out of the finding")
	x.Equal("seen", now.Kind, "and the rest of it as it was")
	x.Equal(asked, finding(unknown).Desc, "another scan's, which nobody asked of")

	v, err = e.s.Deps.RedactEvent(ctx, e.s.Base, e.s.Drv, e.tenant, stray, "삭제 요청 2026-36")
	x.NoError(err)
	x.Equal([]pdid.Id{pdid.Id(idUUID(unknown.GetId()))}, v.Rows)
	x.Empty(finding(unknown).Desc)
	x.Equal(app.CountFindingService_Patch_FullMethodName, e.lastLine(idUUID(unknown.GetId())).Action)
	e.offTheTrail("박지민")
}

// TestAReasonIsTakenOutOfTheReviewThatFoundIt: what a reviewer found is the
// reason the review's event says, in their words -- often whose calls they
// looked into -- and the review keeps it as its own, declared personal as the
// reason is. Asked of the review's event, the words go from the review too:
// the one write a review takes, through the servers. What else it says --
// the stretch, who, what it held -- is as it was.
func TestAReasonIsTakenOutOfTheReviewThatFoundIt(t *testing.T) {
	e := newEnv(t)
	x := e.x
	ctx := context.Background()
	month := e.now.Add(-30 * day)
	r, err := e.app().AccessReview().Add(e.owner, app.AccessReviewAddRequest_builder{
		PeriodFrom: timestamppb.New(month),
		PeriodTo:   timestamppb.New(month.Add(28 * day)),
		Desc:       asked,
	}.Build())
	x.NoError(err)
	x.Equal(asked, r.GetDesc())
	e.offTheTrail("박지민")
	reviewed := e.eventsOf("access.review")
	x.Len(reviewed, 1)
	x.Equal(asked, reviewed[0].Reason, "what they found")
	found, err := domain.Kept(ctx, e.s.Ent, e.tenant, pdid.Id(reviewed[0].Id), nil)
	x.NoError(err)
	x.Equal([]pdid.Id{pdid.Id(idUUID(r.GetId()))}, found, "found before anything is done")

	e.now = e.now.Add(time.Minute)
	_, err = e.redact(e.owner, pdid.Id(reviewed[0].Id), "삭제 요청 2026-37")
	x.NoError(err)
	now, err := e.app().AccessReview().Get(e.owner, app.AccessReviewGetRequest_builder{Ref: ref[app.AccessReviewRef](r.GetId())}.Build())
	x.NoError(err)
	x.Empty(now.GetDesc(), "taken out of the review")
	x.True(r.GetPeriodFrom().AsTime().Equal(now.GetPeriodFrom().AsTime()), "and the rest of it as it was")
	x.True(r.GetPeriodTo().AsTime().Equal(now.GetPeriodTo().AsTime()))
	x.Equal(r.GetReviewer().GetId(), now.GetReviewer().GetId())
	x.Equal(r.GetCalls(), now.GetCalls())
	x.Equal(r.GetDownloads(), now.GetDownloads())
	x.Equal(app.AccessReviewService_Patch_FullMethodName, e.lastLine(idUUID(r.GetId())).Action)
	e.offTheTrail("박지민")
}
