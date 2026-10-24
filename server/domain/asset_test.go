package domain_test

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/lesomnus/payday/pdid"
	"google.golang.org/grpc/codes"
	"google.golang.org/protobuf/types/known/timestamppb"

	app "github.com/lesomnus/rove"
	"github.com/lesomnus/rove/server/pd"
)

const day = 24 * time.Hour

// where answers what an asset was in at a moment, as known at another (or
// now), read through QueryAt over `root`.
func (e *env) where(root, a *app.Asset, at time.Time, known *time.Time) []byte {
	req := app.AssetQueryAtRequest_builder{Root: assetRef(root), At: timestamppb.New(at)}.Build()
	if known != nil {
		req.SetKnown(timestamppb.New(*known))
	}
	v, err := e.app().Asset().QueryAt(e.owner, req)
	e.x.NoError(err)
	for _, s := range v.GetItems() {
		if sameId(s.GetId(), a.GetId()) && s.GetExisted() {
			return s.GetParentId()
		}
	}
	return nil
}

func TestTimeTravel(t *testing.T) {
	e := newEnv(t)
	x := e.x

	hq := e.space("본사", nil)
	a := e.space("A", hq)
	b := e.space("B", hq)
	laptop := e.item("노트북", "NB-1", a, 30*day)

	// Found out today that it went to B ten days ago.
	_, err := e.app().Asset().Move(e.owner, app.AssetMoveRequest_builder{
		Ref:    assetRef(laptop),
		To:     assetRef(b),
		At:     e.ago(10 * day),
		Reason: "늦게 알게 됨",
	}.Build())
	x.NoError(err)

	x.Equal(b.GetId(), e.get(laptop).GetParentId(), "the row says where it is now")
	x.Equal(a.GetId(), e.where(hq, laptop, e.now.Add(-20*day), nil), "it was in A twenty days ago")
	x.Equal(b.GetId(), e.where(hq, laptop, e.now.Add(-5*day), nil), "and in B five days ago")
	x.Nil(e.where(hq, laptop, e.now.Add(-40*day), nil), "and nowhere before it was registered")

	tl, err := e.app().Asset().Timeline(e.owner, app.AssetTimelineRequest_builder{Ref: assetRef(laptop)}.Build())
	x.NoError(err)
	var toB *app.TimelineEntry
	for _, en := range tl.GetEntries() {
		if en.GetKind() == "placement" && sameId(en.GetOtherId(), b.GetId()) {
			toB = en
		}
	}
	x.NotNil(toB)
	x.WithinDuration(e.now.Add(-10*day), toB.GetValidFrom().AsTime(), time.Second)

	// It turns out the move was fifteen days ago, not ten.
	before := e.now
	e.now = e.now.Add(time.Minute)
	_, err = e.app().Asset().Correct(e.owner, app.AssetCorrectRequest_builder{
		Ref:       assetRef(laptop),
		RowId:     toB.GetRowId(),
		ValidFrom: e.ago(15 * day),
		Reason:    "영수증 확인",
	}.Build())
	x.NoError(err)
	x.Equal(b.GetId(), e.where(hq, laptop, e.now.Add(-12*day), nil), "corrected: in B twelve days ago")
	x.Equal(a.GetId(), e.where(hq, laptop, e.now.Add(-12*day), &before), "as known before the correction: still in A")

	// And now that it never moved at all.
	tl, err = e.app().Asset().Timeline(e.owner, app.AssetTimelineRequest_builder{Ref: assetRef(laptop)}.Build())
	x.NoError(err)
	toB = nil
	for _, en := range tl.GetEntries() {
		if en.GetKind() == "placement" && sameId(en.GetOtherId(), b.GetId()) && !en.HasSupersededAt() {
			toB = en
		}
	}
	x.NotNil(toB)
	_, err = e.app().Asset().Correct(e.owner, app.AssetCorrectRequest_builder{
		Ref:     assetRef(laptop),
		RowId:   toB.GetRowId(),
		Retract: true,
		Reason:  "잘못 기록",
	}.Build())
	x.NoError(err)
	x.Equal(a.GetId(), e.get(laptop).GetParentId(), "retracted: back in A, as if the move never happened")
	x.Equal(a.GetId(), e.where(hq, laptop, e.now.Add(-5*day), nil))

	// The history keeps what was corrected, for whoever asks to see it.
	tl, err = e.app().Asset().Timeline(e.owner, app.AssetTimelineRequest_builder{Ref: assetRef(laptop), Superseded: true}.Build())
	x.NoError(err)
	gone := 0
	for _, en := range tl.GetEntries() {
		if en.HasSupersededAt() {
			gone++
		}
	}
	x.GreaterOrEqual(gone, 2)
}

func TestNoCycles(t *testing.T) {
	e := newEnv(t)
	hq := e.space("본사", nil)
	floor := e.space("3층", hq)
	room := e.space("회의실", floor)

	_, err := e.app().Asset().Move(e.owner, app.AssetMoveRequest_builder{Ref: assetRef(hq), To: assetRef(room)}.Build())
	e.x.Error(err, "a building cannot go inside its own room")

	// Nor at a moment in the past, which a later fact would make a loop of.
	_, err = e.app().Asset().Move(e.owner, app.AssetMoveRequest_builder{Ref: assetRef(floor), To: assetRef(room), At: e.ago(day)}.Build())
	e.x.Error(err)
}

func TestAttributesAndHistory(t *testing.T) {
	e := newEnv(t)
	x := e.x
	room := e.space("창고", nil)
	a := e.item("모니터", "MN-1", room, 10*day)

	_, err := e.app().Asset().SetAttributes(e.owner, app.AssetSetAttributesRequest_builder{
		Ref: assetRef(a),
		Set: map[string]string{"status": "in_repair", "condition": "broken", "attr.size": "27"},
		At:  e.ago(2 * day),
	}.Build())
	x.NoError(err)
	v := e.get(a)
	x.Equal("in_repair", v.GetStatus())
	x.Equal("broken", v.GetCondition())
	x.Equal("27", v.GetAttributes()["size"])

	_, err = e.app().Asset().SetAttributes(e.owner, app.AssetSetAttributesRequest_builder{
		Ref: assetRef(a),
		Set: map[string]string{"status": "nonsense"},
	}.Build())
	x.Equal(codes.InvalidArgument, codeOf(err))

	q, err := e.app().Asset().QueryAt(e.owner, app.AssetQueryAtRequest_builder{Root: assetRef(room), At: e.ago(5 * day)}.Build())
	x.NoError(err)
	for _, s := range q.GetItems() {
		if sameId(s.GetId(), a.GetId()) {
			x.Equal("active", s.GetFacts()["status"], "five days ago it was still working")
		}
	}

	// Erase keeps the history and refuses a space with something in it.
	_, err = e.app().Asset().Erase(e.owner, assetRef(room))
	x.Error(err)
	_, err = e.app().Asset().Erase(e.owner, assetRef(a))
	x.NoError(err)
	_, err = e.app().Asset().Get(e.owner, app.AssetGetRequest_builder{Ref: assetRef(a)}.Build())
	x.Equal(codes.NotFound, codeOf(err))
	// The tag of what was voided is free again: the unique index covers the
	// rows that are still here.
	e.item("모니터", "MN-1", room, 0)
}

func TestTheWall(t *testing.T) {
	e := newEnv(t)
	x := e.x
	room := e.space("회의실", nil)
	mine := e.item("노트북", "NB-1", room, day)

	_, other := e.newTenant("other")
	theirs, err := e.app().Asset().Add(other, app.AssetAddRequest_builder{Name: "남의 것", Kind: "item"}.Build())
	x.NoError(err)

	_, err = e.app().Asset().Get(other, app.AssetGetRequest_builder{Ref: assetRef(mine)}.Build())
	x.Equal(codes.NotFound, codeOf(err), "another tenant's asset is not there")

	found, err := e.app().Asset().Search(other, app.AssetSearchRequest_builder{Q: "노트북"}.Build())
	x.NoError(err)
	x.Zero(found.GetTotal())

	_, err = e.app().Asset().Move(other, app.AssetMoveRequest_builder{Ref: assetRef(theirs), To: assetRef(room)}.Build())
	x.Error(err, "nor can anything be put into it")

	_, err = e.app().Asset().Timeline(other, app.AssetTimelineRequest_builder{Ref: assetRef(mine)}.Build())
	x.Equal(codes.NotFound, codeOf(err))

	_, err = e.app().Label().Resolve(other, app.LabelResolveRequest_builder{Code: "NB-1"}.Build())
	x.Equal(codes.NotFound, codeOf(err), "a tag is read in the caller's tenant only")
}

// TestACorrectionSentAgainIsTheSameCorrection: a client that did not hear back
// sends the same correction again, by its operation's identifier. The row it
// named is superseded by then -- by the first copy -- and the second answers
// what the first did instead of that.
func TestACorrectionSentAgainIsTheSameCorrection(t *testing.T) {
	e := newEnv(t)
	x := e.x
	a := e.space("A", nil)
	b := e.space("B", nil)
	laptop := e.item("노트북", "NB-1", a, 10*day)
	_, err := e.app().Asset().Move(e.owner, app.AssetMoveRequest_builder{Ref: assetRef(laptop), To: assetRef(b), At: e.ago(2 * day)}.Build())
	x.NoError(err)

	tl, err := e.app().Asset().Timeline(e.owner, app.AssetTimelineRequest_builder{Ref: assetRef(laptop)}.Build())
	x.NoError(err)
	var toB *app.TimelineEntry
	for _, en := range tl.GetEntries() {
		if en.GetKind() == "placement" && sameId(en.GetOtherId(), b.GetId()) {
			toB = en
		}
	}
	x.NotNil(toB)

	req := app.AssetCorrectRequest_builder{
		Ref: assetRef(laptop), RowId: toB.GetRowId(), ValidFrom: e.ago(5 * day), Reason: "영수증",
		Op: pdid.New(pd.EventDomain).Bytes(),
	}.Build()
	_, err = e.app().Asset().Correct(e.owner, req)
	x.NoError(err)
	again, err := e.app().Asset().Correct(e.owner, req)
	x.NoError(err, "the same correction, sent again")
	x.Equal(b.GetId(), again.GetParentId())
	x.Equal(b.GetId(), e.where(b, laptop, e.now.Add(-4*day), nil), "corrected, once")
}

// TestARegistrationSentAgainIsTheSameAsset: a registration sent again by its
// operation's identifier, by a client that named no asset, answers with the
// asset the first copy registered. It was named after the operation.
func TestARegistrationSentAgainIsTheSameAsset(t *testing.T) {
	e := newEnv(t)
	x := e.x
	room := e.space("회의실", nil)
	req := app.AssetAddRequest_builder{Name: "노트북", Kind: "item", To: assetRef(room), Op: newOp()}.Build()
	a, err := e.app().Asset().Add(e.owner, req)
	x.NoError(err)
	again, err := e.app().Asset().Add(e.owner, req)
	x.NoError(err, "the same registration, sent again")
	x.Equal(a.GetId(), again.GetId())
	x.Equal(a.GetTag(), again.GetTag())
	x.Equal(1, e.ops(req.GetOp()))
}

// TestTheWallHoldsUnderTheDomainLayer: on PostgreSQL, SQL that goes around the
// domain layer and the gate -- a script, a hand-run fix -- still cannot make a
// row of one tenant name a row of another (`rove_same_tenant`, design 6, D11).
// It is refused as a foreign key is, with 23503, written or changed.
func TestTheWallHoldsUnderTheDomainLayer(t *testing.T) {
	e := newEnv(t)
	if e.s.Dialect != "postgres" {
		t.Skip("PostgreSQL only; set PDTEST_POSTGRES")
	}
	x := e.x
	ctx := context.Background()
	room := e.space("회의실", nil)
	laptop := e.item("노트북", "NB-1", room, day)

	_, other := e.newTenant("other")
	theirs, err := e.app().Asset().Add(other, app.AssetAddRequest_builder{Name: "남의 방", Kind: "space"}.Build())
	x.NoError(err)

	refused := func(err error) {
		t.Helper()
		var pg *pgconn.PgError
		x.ErrorAs(err, &pg)
		x.Equal("23503", pg.Code, pg.Message)
	}
	place := func(parent []byte) error {
		return e.s.Ent.Placement.Create().
			SetId(pdid.New(pd.PlacementDomain).Uuid()).
			SetTenantId(e.tenant.Uuid()).
			SetChildId(idUUID(laptop.GetId())).
			SetParentId(idUUID(parent)).
			SetMode("located").
			SetSlot("").
			SetUFrom(0).
			SetUTo(0).
			SetValidFrom(e.now.Add(-10 * day)).
			SetValidTo(e.now.Add(-9 * day)).
			SetEventId(newOpUUID()).
			SetDateUpdated(e.now).
			Exec(ctx)
	}

	refused(place(theirs.GetId()))
	x.NoError(place(room.GetId()), "and a row of its own tenant is still one it may name")

	_, err = e.s.Db.ExecContext(ctx, "UPDATE placement SET parent_id = $1 WHERE child_id = $2", idUUID(theirs.GetId()), idUUID(laptop.GetId()))
	refused(err)
	_, err = e.s.Db.ExecContext(ctx, "UPDATE asset SET custodian_id = (SELECT id FROM party WHERE tenant_id = $1 LIMIT 1) WHERE id = $2", idUUID(theirs.GetTenant().GetId()), idUUID(laptop.GetId()))
	refused(err)
}

// TestTheTreeHoldsUnderTheDomainLayer: on PostgreSQL, SQL that goes around the
// domain layer still cannot put a thing in two places at once, two things in
// one slot or one rack position, two owners on a thing, one relation twice, or
// two values on one key at one moment (`cli/pg.sql`, design 9.4). Each row below would be the
// second of a pair the domain layer refuses; written straight to the table, it
// is refused by the database, and the same row a moment or a place over is
// not.
func TestTheTreeHoldsUnderTheDomainLayer(t *testing.T) {
	e := newEnv(t)
	if e.s.Dialect != "postgres" {
		t.Skip("PostgreSQL only; set PDTEST_POSTGRES")
	}
	x := e.x
	ctx := context.Background()
	room := e.space("서버실", nil)
	server := e.item("서버", "SV-1", room, 30*day)
	rack := e.item("랙", "RK-1", room, 30*day)
	dimm := e.item("메모리", "MM-1", room, 30*day)
	host := e.item("서버 2", "SV-2", room, 30*day)
	spare := e.item("예비 메모리", "MM-2", room, day)
	spareHost := e.item("예비 서버", "SV-3", room, day)
	_, kim := e.person("member")
	_, lee := e.person("member")

	// What the domain layer wrote: a part in a slot and a server in a rack
	// from twenty days ago, and an owner from as long.
	_, err := e.app().Asset().Move(e.owner, app.AssetMoveRequest_builder{Ref: assetRef(dimm), To: assetRef(server), Mode: "installed", Slot: "DIMM1", At: e.ago(20 * day)}.Build())
	x.NoError(err)
	_, err = e.app().Asset().Move(e.owner, app.AssetMoveRequest_builder{Ref: assetRef(host), To: assetRef(rack), Mode: "installed", UFrom: 10, UTo: 11, At: e.ago(20 * day)}.Build())
	x.NoError(err)
	_, err = e.app().Asset().Assign(e.owner, app.AssetAssignRequest_builder{Ref: assetRef(dimm), Role: "owner", Party: ref[app.PartyRef](kim.GetId()), At: e.ago(20 * day)}.Build())
	x.NoError(err)
	_, err = e.app().Asset().Relate(e.owner, app.AssetRelateRequest_builder{Ref: assetRef(server), Target: assetRef(host), Kind: "connected_to", At: e.ago(20 * day)}.Build())
	x.NoError(err)

	refused := func(err error, constraint string) {
		t.Helper()
		var pg *pgconn.PgError
		x.ErrorAs(err, &pg)
		x.Contains([]string{"23P01", "23505"}, pg.Code, pg.Message)
		x.Equal(constraint, pg.ConstraintName, pg.Message)
	}
	// A spare had no place ten days ago, so a row of it then is in nobody's
	// way but the place's.
	from, to := e.now.Add(-10*day), e.now.Add(-9*day)
	place := func(child, parent *app.Asset, slot string, u int32) error {
		q := e.s.Ent.Placement.Create().
			SetId(pdid.New(pd.PlacementDomain).Uuid()).
			SetTenantId(e.tenant.Uuid()).
			SetChildId(idUUID(child.GetId())).
			SetParentId(idUUID(parent.GetId())).
			SetMode("installed").
			SetSlot(slot).
			SetUFrom(u).
			SetUTo(0).
			SetValidFrom(from).
			SetValidTo(to).
			SetEventId(newOpUUID()).
			SetDateUpdated(e.now)
		if u > 0 {
			q.SetUTo(u + 1)
		}
		return q.Exec(ctx)
	}

	refused(place(spare, server, "DIMM1", 0), "placement_slot_once")
	x.NoError(place(spare, server, "DIMM2", 0), "another slot")
	refused(place(spareHost, rack, "", 11), "placement_rack_once")
	x.NoError(place(spareHost, rack, "", 12), "the units above, touching and not overlapping")
	refused(place(dimm, room, "", 0), "placement_one_parent")

	steward := func(party *app.Party, since time.Time) error {
		return e.s.Ent.Stewardship.Create().
			SetId(pdid.New(pd.StewardshipDomain).Uuid()).
			SetTenantId(e.tenant.Uuid()).
			SetAssetId(idUUID(dimm.GetId())).
			SetRole("owner").
			SetPartyId(idUUID(party.GetId())).
			SetValidFrom(since).
			SetValidTo(since.Add(day)).
			SetEventId(newOpUUID()).
			SetDateUpdated(e.now).
			Exec(ctx)
	}
	refused(steward(lee, from), "stewardship_one_per_role")
	x.NoError(steward(lee, e.now.Add(-40*day)), "before kim's began")

	relate := func(target *app.Asset) error {
		return e.s.Ent.Link.Create().
			SetId(pdid.New(pd.LinkDomain).Uuid()).
			SetTenantId(e.tenant.Uuid()).
			SetSourceId(idUUID(server.GetId())).
			SetTargetId(idUUID(target.GetId())).
			SetKind("connected_to").
			SetRequired(false).
			SetValidFrom(from).
			SetValidTo(to).
			SetEventId(newOpUUID()).
			SetDateUpdated(e.now).
			Exec(ctx)
	}
	refused(relate(host), "link_once")
	x.NoError(relate(rack), "to another thing")

	// The name the asset was registered with is a fact from its first day:
	// its field 5, as payday's past keeps it (payday#151).
	name := func(superseded bool) error {
		q := e.s.Ent.Fact.Create().
			SetId(pdid.New(pd.FactDomain).Uuid()).
			SetTenantId(e.tenant.Uuid()).
			SetSubjectId(idUUID(dimm.GetId())).
			SetDomain(uint32(pd.AssetDomain)).
			SetFieldNumber(5).
			SetKey("").
			SetValue(`"다른 이름"`).
			SetValidFrom(e.now.Add(-30 * day)).
			SetEventId(newOpUUID()).
			SetDateUpdated(e.now)
		if superseded {
			q.SetSupersededAt(e.now)
		}
		return q.Exec(ctx)
	}
	refused(name(false), "fact_once")
	x.NoError(name(true), "a value known no more is one of many")
}

// TestAContainerIsToldOnceATransaction: what is inside a container changing
// is said to whoever watches it by its content version -- once a transaction,
// however much of it changed. Three things made into a room at once move the
// room's version by one, where they moved it by three, each a write of its own
// through every layer (rove#103); the next change moves it again.
func TestAContainerIsToldOnceATransaction(t *testing.T) {
	e := newEnv(t)
	x := e.x

	room := e.space("창고", nil)
	st := e.stock(e.model("의자"), room, 4, 0)
	version := func() uint64 { return e.get(room).GetContentVersion() }
	was := version()

	v, err := e.app().Stock().Convert(e.owner, app.StockConvertRequest_builder{Ref: ref[app.StockRef](st.GetId()), Quantity: 3}.Build())
	x.NoError(err)
	x.Len(v.GetAssets(), 3)
	x.Equal(was+1, version(), "three things in, the room told once")

	_, err = e.app().Stock().Convert(e.owner, app.StockConvertRequest_builder{Ref: ref[app.StockRef](st.GetId()), Quantity: 1}.Build())
	x.NoError(err)
	x.Equal(was+2, version(), "and told again by the next")
}
