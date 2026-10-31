package domain_test

import (
	"context"
	"testing"
	"time"
	"uuid"

	"google.golang.org/grpc/codes"
	"google.golang.org/protobuf/types/known/timestamppb"

	app "github.com/lesomnus/loft"
	"github.com/lesomnus/loft/internal/ent"
	"github.com/lesomnus/loft/internal/ent/event"
	"github.com/lesomnus/loft/internal/ent/link"
	"github.com/lesomnus/loft/server/domain"
)

// What goes out of use -- lost, retired, disposed -- may leave what it is a
// member of in the operation that puts it out of use: its membership of each
// kit and group ends then, as a relation of its own that operation is the
// cause of (design D34), and a kit's bookings let it go as they do on any
// change of what the kit is made of (design 4). Going out of use alone
// changes nothing a kit is made of.

// outOfUse sets `a`'s status, from `at` (now when nil), leaving `leave`.
func (e *env) outOfUse(a *app.Asset, status string, at *timestamppb.Timestamp, leave ...*app.Asset) error {
	e.t.Helper()
	req := app.AssetSetAttributesRequest_builder{Ref: assetRef(a), Set: map[string]string{"status": status}, At: at, Reason: "파손"}.Build()
	for _, v := range leave {
		req.SetLeave(append(req.GetLeave(), assetRef(v)))
	}
	_, err := e.app().Asset().SetAttributes(e.owner, req)
	return err
}

// membership is `member`'s rows of `of`'s membership as known now, the
// oldest first.
func (e *env) membership(member, of *app.Asset) []*ent.Link {
	e.t.Helper()
	vs, err := e.s.Ent.Link.Query().Where(
		link.SourceId(idUUID(member.GetId())),
		link.TargetId(idUUID(of.GetId())),
		link.Kind("member_of"),
		link.SupersededAtIsNil(),
	).Order(link.ByValidFrom()).All(context.Background())
	e.x.NoError(err)
	return vs
}

// causedBy is the events the event `of` is the cause of.
func (e *env) causedBy(of *ent.Event) []*ent.Event {
	e.t.Helper()
	vs, err := e.s.Ent.Event.Query().Where(event.CauseId(of.Id)).All(context.Background())
	e.x.NoError(err)
	return vs
}

// TestWhatGoesOutOfUseLeavesWhatItIsIn: put out of use in each way, a member
// leaves the kit and the group it is in as it was asked to, in the one
// operation, and the kit's booking lets go of it and of nothing else.
func TestWhatGoesOutOfUseLeavesWhatItIsIn(t *testing.T) {
	for _, status := range domain.OutOfUse {
		t.Run(status, func(t *testing.T) {
			e := newEnv(t)
			x := e.x
			camera := e.item("카메라", "CM-1", nil, day)
			lens := e.item("렌즈", "LN-1", nil, day)
			e.bookable(camera, nil)
			kit := e.kit("촬영 키트", camera, lens)
			pool, err := e.app().Asset().Add(e.owner, app.AssetAddRequest_builder{Name: "카메라 풀", Kind: "group"}.Build())
			x.NoError(err)
			e.relate(camera, pool, false)

			r, err := e.reserve(e.owner, kit, e.tomorrow(9, 0), e.tomorrow(12, 0), nil)
			x.NoError(err)
			x.NotNil(e.allocation(r, camera))

			e.now = e.now.Add(time.Hour)
			x.NoError(e.outOfUse(camera, status, nil, kit, pool))
			x.Equal(status, e.get(camera).GetStatus())
			for _, of := range []*app.Asset{kit, pool} {
				rows := e.membership(camera, of)
				x.Len(rows, 1, "the membership of %s, ended", of.GetName())
				x.NotNil(rows[0].ValidTo)
				x.True(rows[0].ValidTo.Equal(e.now), "ended as it went out of use: %s", rows[0].ValidTo)
			}
			x.Nil(e.allocation(r, camera), "the kit's booking let it go")
			x.NotNil(e.allocation(r, lens), "and nothing else")
			_, err = e.reserve(e.owner, camera, e.tomorrow(10, 0), e.tomorrow(11, 0), nil)
			x.Equal(codes.FailedPrecondition, codeOf(err), "refused as out of use (D43), and not as something of the kit's holding it: %v", err)

			// On the record as what it was: the status, and each membership's
			// end a relation of the camera's own, part of it.
			sets := e.eventsOf("asset.set")
			x.Len(sets, 1)
			x.Nil(sets[0].CauseId)
			ends := e.causedBy(sets[0])
			x.Len(ends, 2)
			for _, v := range ends {
				x.Equal("asset.relate", v.Kind)
				x.Equal(idUUID(camera.GetId()), *v.SubjectId)
				m := &app.AssetRelated{}
				x.NoError(v.Value.UnmarshalTo(m))
				x.True(m.GetEnd())
				x.Equal("member_of", m.GetRelation())
				x.Contains([]string{kit.GetTag(), pool.GetTag()}, m.GetTargetTag())
				x.Equal("파손", v.Reason, "why it went out of use, said of what that did")
			}
			for _, of := range []*app.Asset{kit, pool} {
				x.Contains([]uuid.UUID{ends[0].Id, ends[1].Id}, e.membership(camera, of)[0].EventId, "the row is the relation's")
			}
			// And what the kit's booking let go of, the end of the kit's
			// relation's doing (design D39).
			gone := []string{}
			for _, v := range ends {
				for _, k := range e.causedBy(v) {
					gone = append(gone, k.Kind)
				}
			}
			x.Equal([]string{"reservation.let_go", "reservation.let_go"}, gone, "the booking's, and the camera's")
			parts := 0
			for _, en := range e.timeline(camera, false) {
				if en.GetKind() == "event" && en.GetEventKind() == "asset.relate" && en.GetCause() != "" {
					x.Equal("CM-1 속성 변경", en.GetCause())
					parts++
				}
			}
			x.Equal(2, parts, "its history says what each end was part of")
		})
	}
}

// TestWhatGoesOutOfUseAloneStaysInItsKits: put out of use without leaving
// anything, a member is still what its kit is made of, and the kit's booking
// still takes it -- as it did before it could leave.
func TestWhatGoesOutOfUseAloneStaysInItsKits(t *testing.T) {
	e := newEnv(t)
	x := e.x
	camera := e.item("카메라", "CM-1", nil, day)
	kit := e.kit("촬영 키트", camera)
	r, err := e.reserve(e.owner, kit, e.tomorrow(9, 0), e.tomorrow(12, 0), nil)
	x.NoError(err)

	x.NoError(e.outOfUse(camera, "disposed", nil))
	x.Nil(e.membership(camera, kit)[0].ValidTo)
	x.NotNil(e.allocation(r, camera))
	x.Len(e.eventsOf("asset.relate"), 1, "the one that made it a member, and none since")
}

// TestLeavingIsOnlyAsItGoesOutOfUse: what is left is left as something goes
// out of use, of what it may be a member of; a kit it is no member of is
// passed over, since what offered it may have read it before a change that
// ended it.
func TestLeavingIsOnlyAsItGoesOutOfUse(t *testing.T) {
	e := newEnv(t)
	x := e.x
	camera := e.item("카메라", "CM-1", nil, day)
	tripod := e.item("삼각대", "TR-1", nil, day)
	kit := e.kit("촬영 키트", camera)
	other := e.kit("예비 키트")

	_, err := e.app().Asset().SetAttributes(e.owner, app.AssetSetAttributesRequest_builder{
		Ref: assetRef(camera), Set: map[string]string{"name": "새 카메라"}, Leave: []*app.AssetRef{assetRef(kit)},
	}.Build())
	x.Equal(codes.InvalidArgument, codeOf(err), "not as it goes out of use")
	x.Equal(codes.InvalidArgument, codeOf(e.outOfUse(camera, "in_repair", nil, kit)), "in repair, it is still in use")
	x.Equal(codes.InvalidArgument, codeOf(e.outOfUse(camera, "disposed", nil, tripod)), "no kit, nor a group")
	x.Nil(e.membership(camera, kit)[0].ValidTo, "nothing of it done")
	x.Equal("active", e.get(camera).GetStatus())

	e.now = e.now.Add(time.Hour)
	x.NoError(e.outOfUse(camera, "disposed", nil, other, kit))
	x.Empty(e.membership(camera, other))
	x.NotNil(e.membership(camera, kit)[0].ValidTo)
	x.Len(e.causedBy(e.eventsOf("asset.set")[0]), 1, "the one it was a member of")
}

// TestAMembershipEndsWhenItWentOutOfUse: written down later, something put
// out of use days ago left the kit it was in then from then on; one it was
// put in after that -- which the record said it was in until now -- it
// leaves now, and no row of either is said not to have held.
func TestAMembershipEndsWhenItWentOutOfUse(t *testing.T) {
	e := newEnv(t)
	x := e.x
	camera := e.item("카메라", "CM-1", nil, 30*day)
	kit := e.kit("촬영 키트")
	spare := e.kit("예비 키트")
	for _, v := range []struct {
		kit *app.Asset
		ago time.Duration
	}{{kit, 10 * day}, {spare, day}} {
		_, err := e.app().Asset().Relate(e.owner, app.AssetRelateRequest_builder{
			Ref: assetRef(camera), Target: assetRef(v.kit), Kind: "member_of", Required: true, At: e.ago(v.ago),
		}.Build())
		x.NoError(err)
	}

	x.NoError(e.outOfUse(camera, "lost", e.ago(3*day), kit, spare))
	got := e.membership(camera, kit)
	x.Len(got, 1)
	x.True(got[0].ValidFrom.Equal(e.now.Add(-10*day)), "%s", got[0].ValidFrom)
	x.True(got[0].ValidTo.Equal(e.now.Add(-3*day)), "from when it was lost: %s", got[0].ValidTo)
	got = e.membership(camera, spare)
	x.Len(got, 1)
	x.True(got[0].ValidFrom.Equal(e.now.Add(-day)), "%s", got[0].ValidFrom)
	x.True(got[0].ValidTo.Equal(e.now), "now, put in after it was lost: %s", got[0].ValidTo)
}

// TestWhatACountFindsLostLeavesWhatItIsIn: settling what a count never saw
// as lost may leave what it is in as well -- the count's settling the cause
// of its going out of use, which is the cause of its leaving.
func TestWhatACountFindsLostLeavesWhatItIsIn(t *testing.T) {
	e := newEnv(t)
	x := e.x
	room := e.space("창고", nil)
	camera := e.item("카메라", "CM-1", room, 30*day)
	kit := e.kit("촬영 키트", camera)
	r, err := e.reserve(e.owner, kit, e.tomorrow(9, 0), e.tomorrow(12, 0), nil)
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
	finding := ref[app.CountFindingRef](fs.GetItems()[0].GetId())
	e.now = e.now.Add(time.Hour)

	_, err = e.app().InventoryCount().Resolve(e.owner, app.InventoryCountResolveRequest_builder{
		Ref: finding, Resolution: "ignored", Leave: []*app.AssetRef{assetRef(kit)},
	}.Build())
	x.Equal(codes.InvalidArgument, codeOf(err), "left only as it is lost")

	_, err = e.app().InventoryCount().Resolve(e.owner, app.InventoryCountResolveRequest_builder{
		Ref: finding, Resolution: "lost", Reason: "찾지 못함", Leave: []*app.AssetRef{assetRef(kit)},
	}.Build())
	x.NoError(err)
	x.Equal("lost", e.get(camera).GetStatus())
	x.NotNil(e.membership(camera, kit)[0].ValidTo)
	x.Nil(e.allocation(r, camera))

	resolves := e.eventsOf("count.resolve")
	x.Len(resolves, 1)
	sets := e.causedBy(resolves[0])
	x.Len(sets, 1)
	x.Equal("asset.set", sets[0].Kind)
	ends := e.causedBy(sets[0])
	x.Len(ends, 1)
	x.Equal("asset.relate", ends[0].Kind)
}
