package cmd_test

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/lesomnus/payday/gate"
	"github.com/lesomnus/payday/past"
	"github.com/lesomnus/payday/pdid"
	"github.com/lesomnus/payday/pdtest"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	app "github.com/lesomnus/rove"
	"github.com/lesomnus/rove/internal/ent"
	entaccess "github.com/lesomnus/rove/internal/ent/access"
	"github.com/lesomnus/rove/internal/ent/event"
	"github.com/lesomnus/rove/server/policy"
)

// What a person asked to be erased, said in a reason.
const erased = "박지민 씨 개인 사정으로 이동 (010-2345-6789)"

// moved is an event the world's owner recorded with `why`: an asset put in a
// room, over the wire.
func (w *world) moved(why string) pdid.Id {
	assets := app.NewAssetServiceClient(w.conn)
	room, err := assets.Add(w.owner, app.AssetAddRequest_builder{Name: "회의실", Kind: "space"}.Build())
	w.x.NoError(err)
	a, err := assets.Add(w.owner, app.AssetAddRequest_builder{Name: "노트북"}.Build())
	w.x.NoError(err)
	_, err = assets.Move(w.owner, app.AssetMoveRequest_builder{Ref: app.AssetById(a.GetId()), To: app.AssetById(room.GetId()), Reason: why}.Build())
	w.x.NoError(err)
	id, err := pdid.From(a.GetId())
	w.x.NoError(err)
	v, err := w.s.Ent.Event.Query().Where(event.Kind("asset.move"), event.SubjectId(id.Uuid())).Only(context.Background())
	w.x.NoError(err)
	return pdid.Id(v.Id)
}

func redactOf(id pdid.Id, why string) *app.EventRedactRequest {
	return app.EventRedactRequest_builder{Ref: app.EventRef_builder{Id: id.Bytes()}.Build(), Reason: why}.Build()
}

// TestNoRoleTakesAnEventsReasonOutYet: `EventService.Redact` is served, and the
// table gives it to no role (`server/policy`): who may is the owner's to
// decide. Until then the workspace's owner, an administrator and everybody
// below are refused by the table, and the reason stays as it was.
func TestNoRoleTakesAnEventsReasonOutYet(t *testing.T) {
	w := newWorld(t)
	x := w.x
	id := w.moved(erased)

	events := app.NewEventServiceClient(w.conn)
	for _, role := range []string{"owner", "admin", "manager", "member", "auditor"} {
		as := w.owner
		if role != "owner" {
			as = w.signIn(w.holder("as-"+role, role))
		}
		_, err := events.Redact(as, redactOf(id, "삭제 요청 2026-19"))
		x.Equal(codes.PermissionDenied, status.Code(err), role)
		x.True(refusedByTable(err, app.EventService_Redact_FullMethodName), "%s: %v", role, err)
	}

	v, err := w.s.Ent.Event.Get(context.Background(), id.Uuid())
	x.NoError(err)
	x.Equal(erased, v.Reason)
	n, err := w.s.Ent.Event.Query().Where(event.Kind("event.redact")).Count(context.Background())
	x.NoError(err)
	x.Zero(n)
}

// redactOpened is the table with Redact given to a workspace's administrators --
// what the one line that opens it would say -- for the door behind it to be
// tried over the wire.
type redactOpened struct{ policy.Policy }

func (p redactOpened) May(ctx context.Context, c gate.Call) error {
	if c.Action == app.EventService_Redact_FullMethodName && policy.Levels[policy.Role(c)] >= policy.Admin {
		return nil
	}
	return p.Policy.May(ctx, c)
}

// TestTheDoorOnceOpenedKeepsNoReasonAndRecordsWhatItRead: the RPC behind the
// table, called as the table will let somebody once the owner says who. The
// call's access record names the event it read the reason of and the act it
// wrote, by identifier, and holds neither reason: not the one taken out, nor
// the request's reference, which the call's own event keeps. The trail says
// what was done to the event, by whom, and every row is as it says.
func TestTheDoorOnceOpenedKeepsNoReasonAndRecordsWhatItRead(t *testing.T) {
	w, d := digestWorld(t, nil)
	x := w.x
	ctx := context.Background()
	id := w.moved(erased)
	h := w.holder("adm", "admin")
	admin := w.signIn(h)
	hid, err := pdid.From(h.GetId())
	x.NoError(err)

	w.s.Policy = redactOpened{policy.Policy{}}
	g, err := w.s.Grpc(ctx, w.c)
	x.NoError(err)
	events := app.NewEventServiceClient(pdtest.Serve(t, g))

	_, err = events.Redact(w.signIn(w.holder("kim", "member")), redactOf(id, "삭제 요청 2026-19"))
	x.True(refusedByTable(err, app.EventService_Redact_FullMethodName), "a member, still: %v", err)
	v, err := events.Redact(admin, redactOf(id, "삭제 요청 2026-19"))
	x.NoError(err)
	x.Empty(v.GetReason())
	act, err := w.s.Ent.Event.Query().Where(event.Kind("event.redact")).Only(ctx)
	x.NoError(err)
	x.Equal("삭제 요청 2026-19", act.Reason)
	x.NotNil(act.ActorId)
	x.Equal(hid.Uuid(), *act.ActorId, "the administrator's")

	// The access record: the event read, the act written, by identifier --
	// and nothing anybody said, in any row of it.
	rs := w.accessed(t, app.EventService_Redact_FullMethodName)
	by := map[string][]pdid.Id{}
	for _, r := range rs {
		if r.Result != "ok" {
			continue
		}
		for _, s := range r.Subjects {
			by[r.Kind] = append(by[r.Kind], pdid.Id(s))
		}
	}
	x.Contains(by["read"], id, "what it read: the reason it took out")
	x.Contains(by["write"], pdid.Id(act.Id), "what it wrote: the act, whose reason is a request's reference")
	all, err := w.s.Ent.Access.Query().Order(ent.Asc(entaccess.FieldDateCreated)).All(ctx)
	x.NoError(err)
	x.NotEmpty(all)
	for _, r := range all {
		dv := detailOf(t, r)
		for _, s := range []string{string(r.Value), dv.GetReason(), string(dv.GetRequest())} {
			x.NotContains(s, "박지민", r.Action)
			x.NotContains(s, "2026-19", r.Action)
		}
	}

	// The trail: what was done to the event, by the administrator, and not
	// what anybody said; and every row as the trail says it is.
	acts := w.acts(t, id)
	x.NotEmpty(acts)
	last := acts[len(acts)-1]
	x.Equal(past.RedactAction, last.Action)
	x.Equal(hid.Uuid(), last.ActorId)
	lines, err := w.s.Ent.Audit.Query().All(ctx)
	x.NoError(err)
	for _, l := range lines {
		for _, b := range [][]byte{l.Patch, l.Value} {
			x.False(strings.Contains(string(b), "박지민") || strings.Contains(string(b), "2026-19"), "%s %s", l.Action, pdid.Id(l.ObjectId))
		}
	}
	got := w.check(t, d)
	x.True(got.Ok(), "%+v", got)
	x.False(slices.ContainsFunc(lines, func(l *ent.Audit) bool { return l.Action == past.RedactAction && pdid.Id(l.ObjectId) != id }))
}
