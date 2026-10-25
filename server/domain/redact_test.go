package domain_test

import (
	"archive/zip"
	"bytes"
	"context"
	"io"
	"slices"
	"testing"

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
// the workspace: nobody's, filed under it.
func TestAnOperatorTakesAReasonOutAsTheDeploymentsOwnWork(t *testing.T) {
	e := newEnv(t)
	x := e.x
	ctx := context.Background()
	_, id := e.moved(asked)

	v, act, err := e.s.Deps.RedactEvent(ctx, e.s.Base, e.s.Drv, e.tenant, id, "삭제 요청 2026-18")
	x.NoError(err)
	x.Empty(v.GetReason())
	row, err := e.s.Ent.Event.Get(ctx, act.Uuid())
	x.NoError(err)
	x.Equal("event.redact", row.Kind)
	x.Nil(row.ActorId, "nobody's")
	x.Equal(e.tenant.Uuid(), row.TenantId)
	x.Equal("삭제 요청 2026-18", row.Reason)

	_, _, err = e.s.Deps.RedactEvent(ctx, e.s.Base, e.s.Drv, e.tenant, id, "삭제 요청 2026-18")
	x.Equal(codes.FailedPrecondition, codeOf(err))
	other, _ := e.newTenant("beta")
	_, _, err = e.s.Deps.RedactEvent(ctx, e.s.Base, e.s.Drv, other, act, "삭제 요청 2026-18")
	x.Equal(codes.NotFound, codeOf(err), "in another workspace than its own")
}

// TestAReasonTakenOutTwiceAtOnceIsTakenOutOnce: two takings of one reason at
// once -- a request answered twice, from two places -- are one taking: one
// act recorded, and the other refused, having found nothing left to take.
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
}
