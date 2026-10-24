package domain_test

import (
	"context"
	"testing"
	"time"

	"github.com/lesomnus/payday/pdid"
	"github.com/lesomnus/z"
	"google.golang.org/grpc/codes"
	"google.golang.org/protobuf/types/known/timestamppb"

	app "github.com/lesomnus/rove"
	"github.com/lesomnus/rove/internal/ent/event"
	"github.com/lesomnus/rove/internal/ent/fact"
	"github.com/lesomnus/rove/internal/ent/placement"
	"github.com/lesomnus/rove/server/pd"
)

// Rove's events and an asset's facts are payday's past (payday#151): written
// by its `past` package alone, an operation and what it wrote of one moment,
// and let go by the window as the past lets go.

// TestThePastIsWrittenByThePastAlone: an event or a fact written by hand is
// refused -- an Add, an AddMany, a Patch of a fact -- through every stack, the
// one with no wall and the one with no layer above the gate among them, and
// nothing comes of it. Only what payday's past hands the servers is written.
func TestThePastIsWrittenByThePastAlone(t *testing.T) {
	e := newEnv(t)
	x := e.x
	ctx := context.Background()
	laptop := e.item("노트북", "NB-1", nil, day)
	standing, err := e.s.Ent.Fact.Query().Where(fact.SubjectId(idUUID(laptop.GetId()))).All(ctx)
	x.NoError(err)
	x.NotEmpty(standing)
	events := len(e.eventsOf("asset.add"))

	tenant := app.TenantRef_builder{Id: e.tenant.Bytes()}.Build()
	ev := app.EventAddRequest_builder{Tenant: tenant, Kind: "asset.add", OccurredAt: timestamppb.New(e.now)}.Build()
	f := app.FactAddRequest_builder{
		Tenant:      tenant,
		SubjectId:   laptop.GetId(),
		FieldNumber: z.Ptr(uint32(5)),
		Domain:      z.Ptr(uint32(pd.AssetDomain)),
		Value:       `"다른 이름"`,
		ValidFrom:   timestamppb.New(e.now),
		EventId:     pdid.New(pd.EventDomain).Bytes(),
	}.Build()
	for name, s := range map[string]app.Server{"walled": e.s.Walled, "ungated": e.s.Ungated, "base": e.s.Base} {
		_, err := s.Event().Add(e.owner, ev)
		x.Equal(codes.PermissionDenied, codeOf(err), "%s: an event", name)
		_, err = s.Event().AddMany(e.owner, app.EventAddManyRequest_builder{Items: []*app.EventAddRequest{ev}}.Build())
		x.Equal(codes.PermissionDenied, codeOf(err), "%s: events", name)
		_, err = s.Fact().Add(e.owner, f)
		x.Equal(codes.PermissionDenied, codeOf(err), "%s: a fact", name)
		_, err = s.Fact().AddMany(e.owner, app.FactAddManyRequest_builder{Items: []*app.FactAddRequest{f}}.Build())
		x.Equal(codes.PermissionDenied, codeOf(err), "%s: facts", name)
		_, err = s.Fact().Patch(e.owner, app.FactPatchRequest_builder{
			Ref:              app.FactRef_builder{Id: pdid.Id(standing[0].Id).Bytes()}.Build(),
			SupersededAt:     timestamppb.New(e.now),
			SupersededBy:     pdid.New(pd.EventDomain).Bytes(),
			DateUpdatedForce: z.Ptr(true),
		}.Build())
		x.Equal(codes.PermissionDenied, codeOf(err), "%s: a fact superseded", name)
	}

	x.Len(e.eventsOf("asset.add"), events)
	after, err := e.s.Ent.Fact.Query().Where(fact.SubjectId(idUUID(laptop.GetId()))).All(ctx)
	x.NoError(err)
	x.Len(after, len(standing))
	for _, v := range after {
		x.Nil(v.SupersededAt)
	}
}

// TestEveryEventHoldsItsKindsMessage: what an operation records is the
// message its kind declares, and no sentence -- of every kind the env's
// tests write, which the env holds each of them to when it ends (newEnv).
func TestEveryEventHoldsItsKindsMessage(t *testing.T) {
	e := newEnv(t)
	x := e.x
	room := e.space("회의실", nil)
	laptop := e.item("노트북", "NB-1", room, day)
	_, err := e.app().Asset().SetAttributes(e.owner, app.AssetSetAttributesRequest_builder{Ref: assetRef(laptop), Set: map[string]string{"status": "in_repair", "cpu": "i7"}}.Build())
	x.NoError(err)

	ev := e.eventOf("asset.set")
	said := value[*app.AssetAttributesSet](e, ev)
	x.Equal("NB-1", said.GetTag())
	x.Equal([]string{"attr.cpu", "status"}, said.GetSet())
	x.Equal("NB-1 속성 변경", e.say(ev))
	e.heldToKinds()
}

// TestAnOperationIsOneMoment: what an operation writes -- its event, the facts
// of what it made and where it put it -- is written down at one moment, the
// one its lock was taken at, so that what was known at an instant has all of
// it or none.
func TestAnOperationIsOneMoment(t *testing.T) {
	e := newEnv(t)
	x := e.x
	ctx := context.Background()
	room := e.space("창고", nil)
	e.now = e.now.Add(1234567 * time.Nanosecond)
	laptop := e.item("노트북", "NB-1", room, day)
	id := idUUID(laptop.GetId())

	ev, err := e.s.Ent.Event.Query().Where(event.Kind("asset.add"), event.SubjectId(id)).Only(ctx)
	x.NoError(err)
	fs, err := e.s.Ent.Fact.Query().Where(fact.SubjectId(id)).All(ctx)
	x.NoError(err)
	x.NotEmpty(fs)
	p, err := e.s.Ent.Placement.Query().Where(placement.ChildId(id)).Only(ctx)
	x.NoError(err)

	x.Equal(e.now.UTC().Truncate(time.Microsecond), ev.DateCreated.UTC(), "to the microsecond a database keeps")
	for _, f := range fs {
		x.True(ev.DateCreated.Equal(f.DateCreated), "a fact known at %s, and its event at %s", f.DateCreated, ev.DateCreated)
		x.True(f.ValidFrom.Equal(p.ValidFrom), "a fact from %s, and where it was from %s", f.ValidFrom, p.ValidFrom)
		x.Equal(ev.Id, f.EventId)
	}
	x.True(ev.DateCreated.Equal(p.DateCreated))
	x.Equal(ev.Id, p.EventId)
}

// TestOneTransactionsEventsAreEachTheirOwn: a transaction that records more
// than one event -- a sweep letting go of two holds -- records each beside
// the other, and neither part of the other.
func TestOneTransactionsEventsAreEachTheirOwn(t *testing.T) {
	e := newEnv(t)
	x := e.x
	room := e.space("회의실", nil)
	e.bookable(room, nil)
	hold := func(r *app.ReservationAddRequest) { r.SetHold(true) }
	_, err := e.reserve(e.owner, room, e.tomorrow(10, 0), e.tomorrow(11, 0), hold)
	x.NoError(err)
	_, err = e.reserve(e.owner, room, e.tomorrow(13, 0), e.tomorrow(14, 0), hold)
	x.NoError(err)

	e.now = e.now.Add(2 * time.Hour)
	e.sweep()
	gone := e.eventsOf("reservation.expire")
	x.Len(gone, 2)
	for _, v := range gone {
		x.Nil(v.CauseId, "a hold let go of as part of another")
		x.Equal("held", value[*app.ReservationReleased](e, v).GetFrom())
		x.Equal("홀드 만료: 회의", e.say(v))
	}
}

// TestAnOperationOverManyOutlivesItsWindowWhileItsChildrenDo: an operation's
// event is named by the children it is made of, and stays while one stays
// (design D34, payday#151): bulk registration long before the window, whose
// every registration is a record no window ends, is still there to say what
// they were part of. And what a preview counts is what a run takes.
func TestAnOperationOverManyOutlivesItsWindowWhileItsChildrenDo(t *testing.T) {
	e := newEnv(t)
	x := e.x
	start := e.now
	e.now = start.Add(-400 * day)
	res, err := e.app().Asset().AddMany(e.owner, app.AssetAddManyRequest_builder{Items: []*app.AssetAddRequest{
		app.AssetAddRequest_builder{Name: "의자"}.Build(),
		app.AssetAddRequest_builder{Name: "책상"}.Build(),
	}}.Build())
	x.NoError(err)
	e.now = start.Add(-399 * day)
	_, err = e.app().Asset().SetAttributes(e.owner, app.AssetSetAttributesRequest_builder{Ref: assetRef(res.GetItems()[0]), Set: map[string]string{"status": "in_repair"}}.Build())
	x.NoError(err)
	e.now = start.Add(-300 * day)
	_, err = e.app().Asset().SetAttributes(e.owner, app.AssetSetAttributesRequest_builder{Ref: assetRef(res.GetItems()[0]), Set: map[string]string{"status": "active"}}.Build())
	x.NoError(err)
	e.now = start

	e.keep(180)
	ws := app.TenantRef_builder{Id: e.tenant.Bytes()}.Build()
	pv, err := e.app().Tenant().Preview(e.owner, app.TenantPreviewRequest_builder{Ref: ws, KeepDays: z.Ptr(uint32(180))}.Build())
	x.NoError(err)
	plan := e.expire(true)
	x.Positive(plan.Facts)
	x.Positive(plan.Events)
	takes := pv.GetKeepTakes()
	x.EqualValues(plan.Facts, takes["facts"], "what a preview counts is what a run takes")
	x.EqualValues(plan.Events, takes["events"])
	x.EqualValues(plan.Placements, takes["placements"])

	got := e.expire(false)
	x.Equal(plan, got)
	x.Len(e.eventsOf("asset.add_many"), 1, "the operation, which its children are part of")
	x.Len(e.eventsOf("asset.add"), 2)
	set := e.eventsOf("asset.set")
	x.Len(set, 1, "what set what no row says any more went, and what set what it is now stays")
	x.Equal([]string{"status"}, value[*app.AssetAttributesSet](e, set[0]).GetSet())
}
