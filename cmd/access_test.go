package cmd_test

import (
	"archive/zip"
	"bytes"
	"context"
	"io"
	"net/http"
	"net/url"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/anypb"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/lesomnus/payday/access"
	"github.com/lesomnus/payday/pdid"
	"github.com/lesomnus/payday/pdpb"

	app "github.com/lesomnus/rove"
	"github.com/lesomnus/rove/internal/ent"
	entaccess "github.com/lesomnus/rove/internal/ent/access"
	"github.com/lesomnus/rove/internal/ent/event"
	"github.com/lesomnus/rove/internal/ent/notification"
)

// person is somebody of the world's workspace who signs in nowhere: a person
// record, which holds a name and an address.
func (w *world) person(name, email string) *app.Party {
	p, err := w.s.Base.Party().Add(context.Background(), app.PartyAddRequest_builder{
		Tenant: app.TenantRef_builder{Id: w.tenant.GetId()}.Build(),
		Name:   name, Kind: "person", Email: email,
	}.Build())
	w.x.NoError(err)
	return p
}

// accessed is the access record's rows of `action`, once what waits has been
// written.
func (w *world) accessed(t *testing.T, action string) []*ent.Access {
	t.Helper()
	ctx := context.Background()
	require.NoError(t, w.s.Access.Flush(ctx))

	vs, err := w.s.Ent.Access.Query().
		Where(entaccess.ActionEQ(action)).
		Order(ent.Asc(entaccess.FieldDateCreated, entaccess.FieldId)).
		All(ctx)
	require.NoError(t, err)

	return vs
}

func detailOf(t *testing.T, v *ent.Access) *pdpb.AccessDetail {
	t.Helper()
	d := &pdpb.AccessDetail{}
	require.NoError(t, proto.Unmarshal(v.Value, d))
	return d
}

// TestWhoReadWhoseDataIsRecorded is rove#136 through the chain a page calls:
// a list of people is a row naming each of them, filed under the workspace
// and naming the member who asked; and a history that reads somebody's name
// out in a sentence -- which no `personal` field of its answer carries -- says
// it showed them.
func TestWhoReadWhoseDataIsRecorded(t *testing.T) {
	w := newWorld(t)
	x := w.x
	park := w.person("박지민", "park@acme.example")
	kim := w.holder("kim", "member")
	asKim := w.signIn(kim)

	vs, err := app.NewPartyServiceClient(w.conn).List(asKim, app.PartyListRequest_builder{}.Build())
	x.NoError(err)
	x.NotEmpty(vs.GetItems())

	rs := w.accessed(t, app.PartyService_List_FullMethodName)
	x.Len(rs, 1)
	x.Equal("read", rs[0].Kind)
	x.Contains(rs[0].Subjects, park.GetId())
	tenant, err := pdid.From(w.tenant.GetId())
	x.NoError(err)
	actor, err := pdid.From(kim.GetId())
	x.NoError(err)
	x.Equal(tenant.Uuid(), rs[0].TenantId)
	x.Equal(actor.Uuid(), rs[0].ActorId)
	x.NotEmpty(detailOf(t, rs[0]).GetAddress())

	t.Run("and a sentence that names somebody", func(t *testing.T) {
		x := require.New(t)
		assets := app.NewAssetServiceClient(w.conn)

		laptop, err := assets.Add(w.owner, app.AssetAddRequest_builder{Name: "노트북"}.Build())
		x.NoError(err)
		_, err = assets.Assign(w.owner, app.AssetAssignRequest_builder{
			Ref:   app.AssetRef_builder{Id: laptop.GetId()}.Build(),
			Role:  "owner",
			Party: app.PartyRef_builder{Id: park.GetId()}.Build(),
		}.Build())
		x.NoError(err)

		_, err = assets.Timeline(asKim, app.AssetTimelineRequest_builder{Ref: app.AssetRef_builder{Id: laptop.GetId()}.Build()}.Build())
		x.NoError(err)

		rs := w.accessed(t, app.AssetService_Timeline_FullMethodName)
		x.Len(rs, 1)
		x.Contains(rs[0].Subjects, park.GetId(), "the history read her name out, and did not say so")
	})
}

// TestAReservationsHistoryIsOnTheRecord: a reservation's history (D45) names
// whoever did something with it and says why, which no `personal` field of
// its answer carries: the call that reads it says it showed them, and the
// decision whose reason it read out.
func TestAReservationsHistoryIsOnTheRecord(t *testing.T) {
	w := newWorld(t)
	x := w.x
	ctx := context.Background()
	camera, err := app.NewAssetServiceClient(w.conn).Add(w.owner, app.AssetAddRequest_builder{Name: "카메라", Tag: "CM-1"}.Build())
	x.NoError(err)
	_, err = app.NewBookableServiceClient(w.conn).Add(w.owner, app.BookableAddRequest_builder{Asset: app.AssetRef_builder{Id: camera.GetId()}.Build(), Approval: true}.Build())
	x.NoError(err)
	kh := w.holder("kim", "member")
	kp, err := w.s.Base.Party().Add(ctx, app.PartyAddRequest_builder{
		Tenant: app.TenantRef_builder{Id: w.tenant.GetId()}.Build(),
		Name:   "김민수", Kind: "person",
		Holder: app.HolderRef_builder{Id: kh.GetId()}.Build(),
	}.Build())
	x.NoError(err)
	kim := w.signIn(kh)
	reservations := app.NewReservationServiceClient(w.conn)
	at := time.Now().Add(24 * time.Hour).Truncate(time.Hour)
	r, err := reservations.Add(kim, app.ReservationAddRequest_builder{
		Name:     "인터뷰",
		BeginsAt: timestamppb.New(at),
		EndsAt:   timestamppb.New(at.Add(time.Hour)),
		Items:    []*app.ReservationItemSpec{app.ReservationItemSpec_builder{Resource: app.AssetRef_builder{Id: camera.GetId()}.Build()}.Build()},
	}.Build())
	x.NoError(err)
	_, err = reservations.Approve(w.owner, app.ReservationDecideRequest_builder{Ref: app.ReservationRef_builder{Id: r.GetId()}.Build(), Reason: "일정 확인"}.Build())
	x.NoError(err)
	approved, err := w.s.Ent.Event.Query().Where(event.Kind("reservation.approve")).Only(ctx)
	x.NoError(err)

	h, err := reservations.Timeline(kim, app.ReservationTimelineRequest_builder{Ref: app.ReservationRef_builder{Id: r.GetId()}.Build()}.Build())
	x.NoError(err)
	x.Len(h.GetEntries(), 2)
	x.Equal("김민수", h.GetEntries()[1].GetActor())

	rs := w.accessed(t, app.ReservationService_Timeline_FullMethodName)
	x.Len(rs, 1)
	x.Equal("read", rs[0].Kind)
	x.Contains(rs[0].Subjects, kp.GetId(), "who booked it, by name")
	x.Contains(rs[0].Subjects, pdid.Id(approved.Id).Bytes(), "the decision, whose reason it showed")
}

// TestANoticeOfABookingIsOnTheRecord: what a booking takes that goes out of
// use is told to whoever the booking is for (design 4, D42), in a notice whose
// words are personal data (`Notification.desc`), as every notice's are: the
// change that wrote it is on the record as having written it, and the person
// it is for as having read it.
func TestANoticeOfABookingIsOnTheRecord(t *testing.T) {
	w := newWorld(t)
	x := w.x
	ctx := context.Background()
	assets := app.NewAssetServiceClient(w.conn)
	camera, err := assets.Add(w.owner, app.AssetAddRequest_builder{Name: "카메라", Tag: "CM-1"}.Build())
	x.NoError(err)
	_, err = app.NewBookableServiceClient(w.conn).Add(w.owner, app.BookableAddRequest_builder{Asset: app.AssetRef_builder{Id: camera.GetId()}.Build()}.Build())
	x.NoError(err)
	kh := w.holder("kim", "member")
	_, err = w.s.Base.Party().Add(ctx, app.PartyAddRequest_builder{
		Tenant: app.TenantRef_builder{Id: w.tenant.GetId()}.Build(),
		Name:   "김민수", Kind: "person",
		Holder: app.HolderRef_builder{Id: kh.GetId()}.Build(),
	}.Build())
	x.NoError(err)
	kim := w.signIn(kh)
	at := time.Now().Add(24 * time.Hour).Truncate(time.Hour)
	_, err = app.NewReservationServiceClient(w.conn).Add(kim, app.ReservationAddRequest_builder{
		Name:     "인터뷰",
		BeginsAt: timestamppb.New(at),
		EndsAt:   timestamppb.New(at.Add(time.Hour)),
		Items:    []*app.ReservationItemSpec{app.ReservationItemSpec_builder{Resource: app.AssetRef_builder{Id: camera.GetId()}.Build()}.Build()},
	}.Build())
	x.NoError(err)

	_, err = assets.SetAttributes(w.owner, app.AssetSetAttributesRequest_builder{
		Ref: app.AssetRef_builder{Id: camera.GetId()}.Build(),
		Set: map[string]string{"status": "retired"},
	}.Build())
	x.NoError(err)
	told, err := w.s.Ent.Notification.Query().Where(notification.Kind("reservation.out_of_use")).All(ctx)
	x.NoError(err)
	x.Len(told, 1, "to whoever it is for, and nobody runs the bookings but the owner, who did it")
	notice := pdid.Id(told[0].Id).Bytes()

	rs := w.accessed(t, app.AssetService_SetAttributes_FullMethodName)
	x.Len(rs, 1)
	x.Equal("write", rs[0].Kind)
	x.Contains(rs[0].Subjects, notice)

	_, err = app.NewNotificationServiceClient(w.conn).Inbox(kim, &app.NotificationInboxRequest{})
	x.NoError(err)
	rs = w.accessed(t, app.NotificationService_Inbox_FullMethodName)
	x.Len(rs, 1)
	x.Equal("read", rs[0].Kind)
	x.Contains(rs[0].Subjects, notice)
}

// TestADownloadOfPeopleSaysWhatItIsFor: the register's export names its
// custodians, so it is refused without a reason, and its row -- with the
// reason, in whatever words -- is written before it answers. The form a bulk
// registration is written in names nobody, and needs none.
func TestADownloadOfPeopleSaysWhatItIsFor(t *testing.T) {
	w := newWorld(t)
	x := w.x
	park := w.person("박지민", "park@acme.example")
	assets := app.NewAssetServiceClient(w.conn)

	laptop, err := assets.Add(w.owner, app.AssetAddRequest_builder{Name: "노트북"}.Build())
	x.NoError(err)
	// Issued to her, which is what makes her its 사용자 in the register.
	_, err = app.NewCustodyServiceClient(w.conn).Add(w.owner, app.CustodyAddRequest_builder{
		Party: app.PartyRef_builder{Id: park.GetId()}.Build(),
		Lines: []*app.CustodyLineSpec{
			app.CustodyLineSpec_builder{Asset: app.AssetRef_builder{Id: laptop.GetId()}.Build()}.Build(),
		},
	}.Build())
	x.NoError(err)

	_, err = assets.Export(w.owner, &app.AssetExportRequest{})
	x.Equal(codes.InvalidArgument, status.Code(err))
	x.Contains(err.Error(), access.ReasonKey)

	why := metadata.AppendToOutgoingContext(w.owner, access.ReasonKey, "10월 자산 실사")
	v, err := assets.Export(why, &app.AssetExportRequest{})
	x.NoError(err)
	x.Contains(string(v.GetData()), "park@acme.example")

	// Written before it answered: nothing waits.
	x.Zero(w.s.Access.Pending())
	rs, err := w.s.Ent.Access.Query().Where(entaccess.KindEQ("download")).All(context.Background())
	x.NoError(err)
	x.Len(rs, 1)
	x.Equal(app.AssetService_Export_FullMethodName, rs[0].Action)
	x.Equal("10월 자산 실사", detailOf(t, rs[0]).GetReason())
	x.Contains(rs[0].Subjects, park.GetId(), "the file names her custodian's column, and the row does not")

	_, err = assets.Form(w.owner, &app.AssetFormRequest{})
	x.NoError(err, "the form names nobody")

	// Called by itself and nothing else: as an operation of a batch it can
	// neither say what it is for nor be recorded before its answer leaves,
	// whatever the batch's call said.
	packed, err := anypb.New(&app.AssetExportRequest{})
	x.NoError(err)
	_, err = pdpb.NewBatchServiceClient(w.conn).Do(why, pdpb.BatchRequest_builder{
		Ops: []*pdpb.Op{pdpb.Op_builder{Method: app.AssetService_Export_FullMethodName, Request: packed}.Build()},
	}.Build())
	x.Equal(codes.InvalidArgument, status.Code(err))
	n, err := w.s.Ent.Access.Query().Where(entaccess.KindEQ("download")).Count(context.Background())
	x.NoError(err)
	x.Equal(1, n, "a batched export was let out")
}

// TestAWorkspacesExportSaysWhatItIsFor: everybody's data, out of the
// deployment, so it is refused without a reason and recorded with one, before
// the first byte goes.
func TestAWorkspacesExportSaysWhatItIsFor(t *testing.T) {
	w := newWorld(t)
	x := w.x
	park := w.person("박지민", "park@acme.example")
	path := "/account/workspaces/" + w.tenant.GetAlias() + "/export"

	x.Equal(http.StatusBadRequest, w.page(w.owner, http.MethodGet, path).Code)
	n, err := w.s.Ent.Access.Query().Count(context.Background())
	x.NoError(err)
	x.Zero(n, "a refused export was recorded")

	res := w.page(w.owner, http.MethodGet, path+"?reason="+url.QueryEscape("폐업 전 보관"))
	x.Equal(http.StatusOK, res.Code)

	rs, err := w.s.Ent.Access.Query().Where(entaccess.KindEQ("download")).All(context.Background())
	x.NoError(err)
	x.Len(rs, 1)
	x.Equal("GET /account/workspaces/{alias}/export", rs[0].Action)
	x.Equal("폐업 전 보관", detailOf(t, rs[0]).GetReason())
	x.Contains(rs[0].Subjects, park.GetId())
	x.GreaterOrEqual(rs[0].SubjectCount, uint32(2), "the person record and the owner's membership")

	// And the workspace takes its access record out with it -- this export's
	// own row among it, written before anything was read for the file.
	zr, err := zip.NewReader(bytes.NewReader(res.Body.Bytes()), int64(res.Body.Len()))
	x.NoError(err)
	f, err := zr.Open("trail/access.jsonl")
	x.NoError(err, "the export has no access record")
	b, err := io.ReadAll(f)
	x.NoError(err)
	x.Contains(string(b), "GET /account/workspaces/{alias}/export")
}

// TestAMonthOfTheRecordIsReviewed is rove#136's other half: the workspace's
// administrators and auditors read a stretch of its access record out --
// who, whose data by name, from where, what a download was for -- narrowed
// to a member, a person or a kind, a page at a time; and record that they
// looked it over, with what it held then. A member does neither.
func TestAMonthOfTheRecordIsReviewed(t *testing.T) {
	w := newWorld(t)
	x := w.x
	ctx := context.Background()
	park := w.person("박지민", "park@acme.example")
	kim := w.holder("kim", "member")
	lee := w.holder("lee", "admin")
	choi := w.holder("choi", "auditor")
	asKim, asLee, asChoi := w.signIn(kim), w.signIn(lee), w.signIn(choi)

	parties := app.NewPartyServiceClient(w.conn)
	for _, as := range []context.Context{asKim, asKim, asLee} {
		_, err := parties.List(as, app.PartyListRequest_builder{}.Build())
		x.NoError(err)
	}
	x.NoError(w.s.Access.Flush(ctx))

	accesses := app.NewAccessServiceClient(w.conn)
	from, to := time.Now().Add(-time.Hour), time.Now().Add(time.Hour)
	review := func(edit func(*app.AccessReviewRequest)) *app.AccessReviewResponse {
		t.Helper()
		req := app.AccessReviewRequest_builder{From: timestamppb.New(from), To: timestamppb.New(to)}.Build()
		if edit != nil {
			edit(req)
		}
		v, err := accesses.Review(asChoi, req)
		x.NoError(err)
		return v
	}

	all := review(nil)
	x.GreaterOrEqual(all.GetCalls(), uint32(3))
	x.Zero(all.GetDownloads())
	byKim := review(func(r *app.AccessReviewRequest) { r.SetActor(kim.GetId()) })
	x.Len(byKim.GetItems(), 2, "kim's two lists")
	for _, e := range byKim.GetItems() {
		x.Equal("@kim", e.GetActorName())
		x.Equal(app.PartyService_List_FullMethodName, e.GetAction())
		x.Equal("read", e.GetKind())
		x.Equal("ok", e.GetResult())
		x.NotEmpty(e.GetAddress(), "where it came from")
	}
	ofPark := review(func(r *app.AccessReviewRequest) { r.SetSubject(park.GetId()) })
	x.GreaterOrEqual(len(ofPark.GetItems()), 3)
	for _, e := range ofPark.GetItems() {
		x.Equal(park.GetId(), e.GetSubjects()[0].GetId(), "the person asked after, first")
		x.Equal("박지민", e.GetSubjects()[0].GetName())
		x.Equal("party", e.GetSubjects()[0].GetKind())
	}
	x.Empty(review(func(r *app.AccessReviewRequest) { r.SetKind("download") }).GetItems())

	// A page at a time, each row once.
	seen := map[string]bool{}
	after := ""
	for range 10 {
		v := review(func(r *app.AccessReviewRequest) { r.SetActor(kim.GetId()); r.SetSize(1); r.SetAfter(after) })
		for _, e := range v.GetItems() {
			x.False(seen[string(e.GetId())], "a row twice")
			seen[string(e.GetId())] = true
		}
		if after = v.GetNext(); after == "" {
			break
		}
	}
	x.Len(seen, 2)

	_, err := accesses.Review(asKim, app.AccessReviewRequest_builder{From: timestamppb.New(from), To: timestamppb.New(to)}.Build())
	x.Equal(codes.PermissionDenied, status.Code(err), "a member reviews nobody's record")
	_, err = accesses.Review(asChoi, app.AccessReviewRequest_builder{From: timestamppb.New(to), To: timestamppb.New(from)}.Build())
	x.Equal(codes.InvalidArgument, status.Code(err), "a stretch that ends before it begins")
	_, err = accesses.Review(asChoi, app.AccessReviewRequest_builder{From: timestamppb.New(from.Add(-100 * 24 * time.Hour)), To: timestamppb.New(to)}.Build())
	x.Equal(codes.InvalidArgument, status.Code(err), "more than a quarter at once")

	// That it was looked over, by whom, with what the stretch held then.
	reviews := app.NewAccessReviewServiceClient(w.conn)
	v, err := reviews.Add(asChoi, app.AccessReviewAddRequest_builder{
		PeriodFrom: timestamppb.New(from), PeriodTo: timestamppb.New(to), Desc: "이상 없음",
	}.Build())
	x.NoError(err)
	x.Equal(choi.GetId(), v.GetReviewer().GetId())
	x.GreaterOrEqual(v.GetCalls(), all.GetCalls())
	x.Equal("이상 없음", v.GetDesc())
	vs, err := reviews.List(asLee, app.AccessReviewListRequest_builder{}.Build())
	x.NoError(err)
	x.Len(vs.GetItems(), 1, "an administrator reads it too")
	_, err = reviews.Add(asKim, app.AccessReviewAddRequest_builder{PeriodFrom: timestamppb.New(from), PeriodTo: timestamppb.New(to)}.Build())
	x.Equal(codes.PermissionDenied, status.Code(err))
	_, err = reviews.Add(asChoi, app.AccessReviewAddRequest_builder{
		PeriodFrom: timestamppb.New(time.Now().Add(time.Hour)), PeriodTo: timestamppb.New(time.Now().Add(2 * time.Hour)),
	}.Build())
	x.Equal(codes.InvalidArgument, status.Code(err), "a stretch not come yet")
}
