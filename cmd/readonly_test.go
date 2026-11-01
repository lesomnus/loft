package cmd_test

import (
	"archive/zip"
	"bytes"
	"context"
	"net/http"
	"net/url"
	"testing"
	"time"

	"github.com/lesomnus/z"
	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/anypb"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/lesomnus/payday/access"
	"github.com/lesomnus/payday/entitle"
	"github.com/lesomnus/payday/frame"
	"github.com/lesomnus/payday/pdid"
	"github.com/lesomnus/payday/pdpb"

	app "github.com/lesomnus/loft"
	"github.com/lesomnus/loft/cmd"
	"github.com/lesomnus/loft/internal/ent/attachment"
	"github.com/lesomnus/loft/internal/levyantest"
	"github.com/lesomnus/loft/server/domain"
	"github.com/lesomnus/loft/server/pd"
)

// refusedReadOnly says that `err` is a write a read-only workspace does not
// make: FailedPrecondition, saying the workspace is read-only, with the
// reason a page tells it by and the method it refused.
func (w *world) refusedReadOnly(err error, method string) {
	w.x.Equal(codes.FailedPrecondition, status.Code(err), "%s: %v", method, err)
	w.x.Contains(status.Convert(err).Message(), "이 워크스페이스는 읽기 전용이라", method)
	var info *errdetails.ErrorInfo
	for _, d := range status.Convert(err).Details() {
		if v, ok := d.(*errdetails.ErrorInfo); ok {
			info = v
		}
	}
	w.x.NotNil(info, "%s: no reason a page can tell it by", method)
	w.x.Equal(domain.ErrorReadOnly, info.GetReason(), method)
	w.x.Equal("loft", info.GetDomain(), method)
	w.x.Equal(domain.ReadOnly, info.GetMetadata()["entitlement"], method)
	w.x.Equal(method, info.GetMetadata()["refused"], method)
}

// readOnly is whether the copy makes `t` read-only now.
func (w *world) readOnly(t *app.Tenant) bool {
	on, err := entitle.Has(inside(w.x, t), pd.Entitlements(w.s.Drv), domain.ReadOnly)
	w.x.NoError(err)
	return on
}

// makeReadOnly has levyan make the world's workspace read-only, and the copy
// hear it.
func makeReadOnly(w *world, fake *levyantest.Levyan) []byte {
	id := fake.ReadOnly(tenantId(w.x, w.tenant))
	_, err := w.s.Follower.Sync(context.Background(), tenantId(w.x, w.tenant))
	w.x.NoError(err)
	w.x.True(w.readOnly(w.tenant))
	return id
}

// TestAReadOnlyWorkspaceIsReadPaidForAndClosed (design 7.10, D64): while
// levyan makes a workspace read-only, every write of every kind is refused --
// the register and its catalog, files, labels, bookings, hand-overs, stock,
// counts, work, purchases, people and their roles, invitations sent and
// answered, the record of an access review, its settings and its domains, as
// an RPC and as an operation of a batch, and a write that begins no
// operation as one that does -- with the reason a page tells it by. What it
// still does, as levyan's `docs/apps.md` says an app takes of one, it does:
// reads and reports, takes its data out and its files, is paid for, and lets
// its members leave and a person be forgotten; and a password, which is the
// account's, is changed, and what a person is told, which is theirs (D67), is
// marked read and kept off their mail. Once levyan ends the grant it writes
// again; and read-only or not, it closes.
func TestAReadOnlyWorkspaceIsReadPaidForAndClosed(t *testing.T) {
	fake := levyantest.Start(t)
	w := newInvites(t, func(c *cmd.Config) { c.App.Billing.Levyan = fake.Config() })
	x := w.x
	ctx := context.Background()

	assets := app.NewAssetServiceClient(w.conn)
	types := app.NewAssetTypeServiceClient(w.conn)
	models := app.NewItemModelServiceClient(w.conn)
	attachments := app.NewAttachmentServiceClient(w.conn)
	labels := app.NewLabelServiceClient(w.conn)
	bookables := app.NewBookableServiceClient(w.conn)
	reservations := app.NewReservationServiceClient(w.conn)
	custody := app.NewCustodyServiceClient(w.conn)
	stocks := app.NewStockServiceClient(w.conn)
	counts := app.NewInventoryCountServiceClient(w.conn)
	works := app.NewWorkOrderServiceClient(w.conn)
	purchases := app.NewPurchaseServiceClient(w.conn)
	parties := app.NewPartyServiceClient(w.conn)
	tenants := app.NewTenantServiceClient(w.conn)
	domains := app.NewTenantDomainServiceClient(w.conn)
	notifications := app.NewNotificationServiceClient(w.conn)
	tref := app.TenantRef_builder{Id: w.tenant.GetId()}.Build()
	aref := func(a *app.Asset) *app.AssetRef { return app.AssetRef_builder{Id: a.GetId()}.Build() }
	pref := func(p *app.Party) *app.PartyRef { return app.PartyRef_builder{Id: p.GetId()}.Build() }

	// What the workspace has before it is read-only.
	room, err := assets.Add(w.owner, app.AssetAddRequest_builder{Name: "회의실", Kind: "space"}.Build())
	x.NoError(err)
	laptop, err := assets.Add(w.owner, app.AssetAddRequest_builder{Name: "노트북", To: aref(room)}.Build())
	x.NoError(err)
	monitor, err := assets.Add(w.owner, app.AssetAddRequest_builder{Name: "모니터"}.Build())
	x.NoError(err)
	kind, err := types.Add(w.owner, app.AssetTypeAddRequest_builder{Name: "입력 장치", Kind: "item"}.Build())
	x.NoError(err)
	model, err := models.Add(w.owner, app.ItemModelAddRequest_builder{Name: "AA 건전지"}.Build())
	x.NoError(err)
	mref := app.ItemModelRef_builder{Id: model.GetId()}.Build()
	batteries, err := stocks.Add(w.owner, app.StockAddRequest_builder{Model: mref, Space: aref(room), Quantity: 10}.Build())
	x.NoError(err)
	sref := app.StockRef_builder{Id: batteries.GetId()}.Build()
	file, err := attachments.Upload(w.owner, app.AttachmentUploadRequest_builder{SubjectId: laptop.GetId(), Name: "보증서.pdf", ContentType: "application/pdf", Data: []byte("%PDF-1.7 warranty")}.Build())
	x.NoError(err)
	fref := app.AttachmentRef_builder{Id: file.GetId()}.Build()
	_, err = bookables.Add(w.owner, app.BookableAddRequest_builder{Asset: aref(room)}.Build())
	x.NoError(err)
	booker := w.holder("yun", "member")
	partyOf(w.world, booker)
	asBooker := w.signIn(booker)
	at := time.Now().Add(48 * time.Hour).Truncate(time.Hour)
	booked, err := reservations.Add(asBooker, app.ReservationAddRequest_builder{
		Name: "회의", BeginsAt: timestamppb.New(at), EndsAt: timestamppb.New(at.Add(time.Hour)),
		Items: []*app.ReservationItemSpec{app.ReservationItemSpec_builder{Resource: aref(room)}.Build()},
	}.Build())
	x.NoError(err)
	kim := w.person("김철수", "kim@example.com")
	park := w.person("박지민", "park@example.com")
	lent, err := custody.Add(w.owner, app.CustodyAddRequest_builder{
		Party: pref(kim),
		Lines: []*app.CustodyLineSpec{app.CustodyLineSpec_builder{Asset: aref(laptop)}.Build()},
	}.Build())
	x.NoError(err)
	leaving := partyOf(w.world, w.holder("choi", "member"))
	jung := w.holder("jung", "member")
	x.NoError(w.s.Identity.SetPassword(ctx, w.accountOf(jung), "password1234"))
	asJung := w.signIn(jung)
	lee := w.account("lee", []string{"lee@example.com"}, nil)
	sent, err := w.invite(w.owner, "lee@example.com", "member")
	x.NoError(err)
	held, err := w.invite(w.owner, "han@example.com", "member")
	x.NoError(err)

	grant := makeReadOnly(w.world, fake)
	files := func() int {
		n := 0
		for range walk(t, w.c.App.Files) {
			n++
		}
		return n
	}
	before := files()

	// Refused, every kind of write, each saying so.
	_, err = assets.Add(w.owner, app.AssetAddRequest_builder{Name: "키보드"}.Build())
	w.refusedReadOnly(err, app.AssetService_Add_FullMethodName)
	_, err = assets.AddMany(w.owner, app.AssetAddManyRequest_builder{Items: []*app.AssetAddRequest{
		app.AssetAddRequest_builder{Name: "의자"}.Build(),
	}}.Build())
	w.refusedReadOnly(err, app.AssetService_AddMany_FullMethodName)
	_, err = assets.Move(w.owner, app.AssetMoveRequest_builder{Ref: aref(monitor), To: aref(room)}.Build())
	w.refusedReadOnly(err, app.AssetService_Move_FullMethodName)
	_, err = assets.SetAttributes(w.owner, app.AssetSetAttributesRequest_builder{Ref: aref(monitor), Set: map[string]string{"name": "모니터 1"}}.Build())
	w.refusedReadOnly(err, app.AssetService_SetAttributes_FullMethodName)
	_, err = assets.Erase(w.owner, aref(monitor))
	w.refusedReadOnly(err, app.AssetService_Erase_FullMethodName)
	_, err = types.Add(w.owner, app.AssetTypeAddRequest_builder{Name: "모니터", Kind: "item"}.Build())
	w.refusedReadOnly(err, app.AssetTypeService_Add_FullMethodName)
	_, err = types.Erase(w.owner, app.AssetTypeRef_builder{Id: kind.GetId()}.Build())
	w.refusedReadOnly(err, app.AssetTypeService_Erase_FullMethodName)
	_, err = models.Erase(w.owner, mref)
	w.refusedReadOnly(err, app.ItemModelService_Erase_FullMethodName)
	_, err = attachments.Upload(w.owner, app.AttachmentUploadRequest_builder{SubjectId: laptop.GetId(), Name: "영수증.pdf", ContentType: "application/pdf", Data: []byte("%PDF-1.7")}.Build())
	w.refusedReadOnly(err, app.AttachmentService_Upload_FullMethodName)
	x.Equal(before, files(), "and the file was not kept")
	_, err = attachments.Erase(w.owner, fref)
	w.refusedReadOnly(err, app.AttachmentService_Erase_FullMethodName)
	_, err = labels.Print(w.owner, app.LabelPrintRequest_builder{Count: 1}.Build())
	w.refusedReadOnly(err, app.LabelService_Print_FullMethodName)
	_, err = reservations.Add(asBooker, app.ReservationAddRequest_builder{
		Name: "회의", BeginsAt: timestamppb.New(at.Add(24 * time.Hour)), EndsAt: timestamppb.New(at.Add(25 * time.Hour)),
		Items: []*app.ReservationItemSpec{app.ReservationItemSpec_builder{Resource: aref(room)}.Build()},
	}.Build())
	w.refusedReadOnly(err, app.ReservationService_Add_FullMethodName)
	_, err = reservations.Cancel(asBooker, app.ReservationDecideRequest_builder{Ref: app.ReservationRef_builder{Id: booked.GetId()}.Build()}.Build())
	w.refusedReadOnly(err, app.ReservationService_Cancel_FullMethodName)
	_, err = custody.Add(w.owner, app.CustodyAddRequest_builder{
		Party: pref(park),
		Lines: []*app.CustodyLineSpec{app.CustodyLineSpec_builder{Asset: aref(monitor)}.Build()},
	}.Build())
	w.refusedReadOnly(err, app.CustodyService_Add_FullMethodName)
	_, err = custody.Return(w.owner, app.CustodyReturnRequest_builder{Ref: app.CustodyRef_builder{Id: lent.GetId()}.Build()}.Build())
	w.refusedReadOnly(err, app.CustodyService_Return_FullMethodName)
	_, err = stocks.Receive(w.owner, app.StockChangeRequest_builder{Ref: sref, Quantity: 5}.Build())
	w.refusedReadOnly(err, app.StockService_Receive_FullMethodName)
	_, err = stocks.Consume(w.signIn(booker), app.StockChangeRequest_builder{Ref: sref, Quantity: 1}.Build())
	w.refusedReadOnly(err, app.StockService_Consume_FullMethodName)
	_, err = counts.Add(w.owner, app.InventoryCountAddRequest_builder{Scope: aref(room)}.Build())
	w.refusedReadOnly(err, app.InventoryCountService_Add_FullMethodName)
	_, err = works.Add(w.owner, app.WorkOrderAddRequest_builder{Asset: aref(laptop), Kind: "repair", Name: "안 켜짐"}.Build())
	w.refusedReadOnly(err, app.WorkOrderService_Add_FullMethodName)
	_, err = purchases.Add(w.owner, app.PurchaseAddRequest_builder{
		Name:  "모니터 구매",
		Lines: []*app.PurchaseLineSpec{app.PurchaseLineSpec_builder{Desc: "모니터", Quantity: 1, UnitCost: 100}.Build()},
	}.Build())
	w.refusedReadOnly(err, app.PurchaseService_Add_FullMethodName)
	_, err = parties.Add(w.owner, app.PartyAddRequest_builder{Name: "이영희", Kind: "person"}.Build())
	w.refusedReadOnly(err, app.PartyService_Add_FullMethodName)
	_, err = parties.Update(w.owner, app.PartyUpdateRequest_builder{Ref: pref(kim), Name: "김 대리"}.Build())
	w.refusedReadOnly(err, app.PartyService_Update_FullMethodName)
	_, err = parties.Invite(w.owner, app.PartyInviteRequest_builder{Ref: pref(park), Alias: "park", Role: "member"}.Build())
	w.refusedReadOnly(err, app.PartyService_Invite_FullMethodName)
	_, err = w.s.Identity.Lookup(ctx, "park")
	x.Error(err, "and no account was made for it at roster")
	_, err = parties.SetRole(w.owner, app.PartySetRoleRequest_builder{Ref: pref(leaving), Role: "manager"}.Build())
	w.refusedReadOnly(err, app.PartyService_SetRole_FullMethodName)
	mailed := len(w.box.sent)
	_, err = w.invite(w.owner, "kang@example.com", "member")
	w.refusedReadOnly(err, app.InvitationService_Add_FullMethodName)
	x.Len(w.box.sent, mailed, "and nobody was mailed")
	_, err = w.svc.Revoke(w.owner, app.InvitationRevokeRequest_builder{Ref: ref(held)}.Build())
	w.refusedReadOnly(err, app.InvitationService_Revoke_FullMethodName)
	_, err = w.svc.Accept(w.as(lee), app.InvitationAcceptRequest_builder{Ref: ref(sent)}.Build())
	w.refusedReadOnly(err, app.InvitationService_Accept_FullMethodName)
	_, err = tenants.Configure(w.owner, app.TenantConfigureRequest_builder{Ref: tref, Timezone: z.Ptr("Asia/Tokyo")}.Build())
	w.refusedReadOnly(err, app.TenantService_Configure_FullMethodName)
	_, err = domains.Add(w.owner, app.TenantDomainAddRequest_builder{Host: "assets.acme.example"}.Build())
	w.refusedReadOnly(err, app.TenantDomainService_Add_FullMethodName)

	// And as an operation of a batch, which the domain layer serves in the
	// batch's transaction: refused as its own method.
	add, err := anypb.New(app.AssetAddRequest_builder{Name: "프린터"}.Build())
	x.NoError(err)
	_, err = pdpb.NewBatchServiceClient(w.conn).Do(w.owner, pdpb.BatchRequest_builder{Ops: []*pdpb.Op{
		pdpb.Op_builder{Method: app.AssetService_Add_FullMethodName, Request: add}.Build(),
	}}.Build())
	x.Equal(codes.FailedPrecondition, status.Code(err), "%v", err)
	x.Contains(status.Convert(err).Message(), "읽기 전용", "%v", err)

	// Nothing of any of it was written.
	got, err := assets.Search(w.owner, app.AssetSearchRequest_builder{}.Build())
	x.NoError(err, "reading")
	x.EqualValues(3, got.GetTotal(), "what it had, and nothing more")
	n, err := w.s.Ent.Attachment.Query().Where(attachment.TenantId(tenantId(x, w.tenant).Uuid()), attachment.DateErasedIsNil()).Count(ctx)
	x.NoError(err)
	x.Equal(1, n)
	x.NotEqual("Asia/Tokyo", w.tenantRow().GetTimezone(), "a setting changed")

	// What it still does: reads, and reports.
	_, err = assets.Get(w.owner, app.AssetGetRequest_builder{Ref: aref(laptop)}.Build())
	x.NoError(err)
	_, err = assets.Timeline(w.owner, app.AssetTimelineRequest_builder{Ref: aref(laptop)}.Build())
	x.NoError(err)
	_, err = assets.Census(w.owner, &app.AssetCensusRequest{})
	x.NoError(err)
	_, err = parties.Search(w.owner, app.PartySearchRequest_builder{Q: "김"}.Build())
	x.NoError(err)
	_, err = reservations.Calendar(asBooker, app.ReservationCalendarRequest_builder{From: timestamppb.New(at.Add(-time.Hour)), To: timestamppb.New(at.Add(2 * time.Hour))}.Build())
	x.NoError(err)
	_, err = tenants.Preview(w.owner, app.TenantPreviewRequest_builder{Ref: tref, KeepDays: z.Ptr(uint32(365))}.Build())
	x.NoError(err, "what a change would take, which changes nothing")

	// Every member is told it is read-only, by the copy its writes are held
	// to.
	me, err := parties.Me(w.owner, &app.PartyMeRequest{})
	x.NoError(err)
	x.True(me.GetReadonly())
	x.False(me.GetRestricted(), "a stage of its own: what restricts it is another grant")
	me, err = parties.Me(asBooker, &app.PartyMeRequest{})
	x.NoError(err)
	x.True(me.GetReadonly())

	// Its data taken out: a CSV of the register, a file it keeps, and all of
	// it -- the last two by the doors that are no RPC, which only read.
	_, err = assets.Export(metadata.AppendToOutgoingContext(w.owner, access.ReasonKey, "결제 정리"), &app.AssetExportRequest{})
	x.NoError(err, "the register's CSV")
	link, err := attachments.Url(w.owner, app.AttachmentUrlRequest_builder{Ref: fref}.Build())
	x.NoError(err)
	u, err := url.Parse(link.GetUrl())
	x.NoError(err)
	res := w.page(context.Background(), http.MethodGet, u.RequestURI())
	x.Equal(http.StatusOK, res.Code, "a file it keeps, downloaded: %s", res.Body.String())
	x.Equal("%PDF-1.7 warranty", res.Body.String())
	res = w.page(w.owner, http.MethodGet, "/account/workspaces/"+w.tenant.GetAlias()+"/export?reason="+url.QueryEscape("결제 정리"))
	x.Equal(http.StatusOK, res.Code, "all of it: %s", res.Body.String())
	_, err = zip.NewReader(bytes.NewReader(res.Body.Bytes()), int64(res.Body.Len()))
	x.NoError(err, "a zip")

	// Who saw whose data, read -- and not a review of it recorded, which is
	// a write.
	from, to := time.Now().Add(-time.Hour), time.Now().Add(time.Minute)
	_, err = app.NewAccessServiceClient(w.conn).Review(w.owner, app.AccessReviewRequest_builder{From: timestamppb.New(from), To: timestamppb.New(to)}.Build())
	x.NoError(err)
	_, err = app.NewAccessReviewServiceClient(w.conn).Add(w.owner, app.AccessReviewAddRequest_builder{PeriodFrom: timestamppb.New(from), PeriodTo: timestamppb.New(to), Desc: "이상 없음"}.Build())
	w.refusedReadOnly(err, app.AccessReviewService_Add_FullMethodName)

	// Paid for: who pays for it, asked by the people who run it, and the
	// payer's own billing accounts, asked as the account.
	_, err = w.payments().GetPayer(w.owner, &app.PaymentGetPayerRequest{})
	x.NoError(err)
	owner := w.session(w.ownerHolder())
	made, err := w.payments().CreateBillingAccount(owner, app.PaymentCreateBillingAccountRequest_builder{Country: "KR"}.Build())
	x.NoError(err, "a billing account to pay with")
	_, err = w.payments().GetBillingAccount(owner, app.PaymentGetBillingAccountRequest_builder{Id: made.GetBillingAccount().GetId()}.Build())
	x.NoError(err)

	// A password, which is the account's at roster and how it signs in.
	_, err = parties.SetPassword(asJung, app.PartySetPasswordRequest_builder{Current: "password1234", Password: "another1234"}.Build())
	x.NoError(err, "a password changed")
	// And what a person is told here, which is theirs (D67): marked read,
	// and kept off their mail -- a write that begins no operation, which a
	// read-only workspace still commits for these two.
	_, err = notifications.MarkRead(asJung, &app.NotificationMarkReadRequest{})
	x.NoError(err, "notices marked read")
	off, err := notifications.SetMail(asJung, app.NotificationSetMailRequest_builder{Off: true}.Build())
	x.NoError(err, "mail turned off")
	x.True(off.GetOff())
	me, err = parties.Me(asJung, &app.PartyMeRequest{})
	x.NoError(err)
	x.True(me.GetHolder().GetMailOff(), "what the person's page reads")
	// But an invitation into it turned down is the workspace's own.
	_, err = w.svc.Deny(w.as(lee), app.InvitationDenyRequest_builder{Ref: ref(sent)}.Build())
	w.refusedReadOnly(err, app.InvitationService_Deny_FullMethodName)

	// Members leaving it, and a person forgotten.
	_, err = parties.Deactivate(w.owner, app.PartyDeactivateRequest_builder{Ref: pref(leaving)}.Build())
	x.NoError(err, "a membership ended")
	_, err = parties.Pseudonymize(w.owner, app.PartyPseudonymizeRequest_builder{Ref: pref(park)}.Build())
	x.NoError(err, "a person forgotten")

	// levyan ends the grant once the invoice is paid: it writes again.
	fake.End(grant)
	_, err = w.s.Follower.Sync(ctx, tenantId(x, w.tenant))
	x.NoError(err)
	x.False(w.readOnly(w.tenant))
	_, err = assets.Add(w.owner, app.AssetAddRequest_builder{Name: "키보드"}.Build())
	x.NoError(err, "a grant ended is read-only no longer")
	_, err = attachments.Erase(w.owner, fref)
	x.NoError(err)
	me, err = parties.Me(w.owner, &app.PartyMeRequest{})
	x.NoError(err)
	x.False(me.GetReadonly())

	// And it closes, read-only or not -- by what it is called (D57) -- and
	// is not restored until it is paid: restoring is opening it again.
	makeReadOnly(w.world, fake)
	me, err = parties.Me(w.owner, &app.PartyMeRequest{})
	x.NoError(err)
	_, err = tenants.Close(w.owner, app.TenantCloseRequest_builder{Ref: tref, Confirm: me.GetWorkspaceName()}.Build())
	x.NoError(err, "closing")
	_, err = tenants.Restore(w.owner, app.TenantRestoreRequest_builder{Ref: tref}.Build())
	w.refusedReadOnly(err, app.TenantService_Restore_FullMethodName)
}

// ownerHolder is the membership the world's owner signs in as.
func (w *world) ownerHolder() *app.Holder {
	v, err := app.NewPartyServiceClient(w.conn).Me(w.owner, &app.PartyMeRequest{})
	w.x.NoError(err)
	return v.GetHolder()
}

// TestReadOnlyIsTheWorkspacesAlone (D64): a workspace's being read-only holds
// that workspace, and not the accounts in it -- one of its members accepts an
// invitation to another workspace and writes there, and is told there that it
// is not read-only.
func TestReadOnlyIsTheWorkspacesAlone(t *testing.T) {
	fake := levyantest.Start(t)
	w := newInvites(t, func(c *cmd.Config) { c.App.Billing.Levyan = fake.Config() })
	x := w.x
	ctx := context.Background()

	choi := w.account("choi", []string{"choi@example.com"}, nil)
	w.here(choi, "manager")
	beta, err := w.s.Base.Tenant().Add(ctx, app.TenantAddRequest_builder{Alias: "beta"}.Build())
	x.NoError(err)
	bob := w.account("bob", nil, nil)
	w.member(beta, bob, "owner")
	inBeta := func(as context.Context) context.Context {
		return metadata.AppendToOutgoingContext(as, cmd.WorkspaceHeader, "beta")
	}
	v, err := w.svc.Add(inBeta(w.as(bob)), app.InvitationAddRequest_builder{
		Tenant: app.TenantRef_builder{Id: beta.GetId()}.Build(),
		Email:  "choi@example.com",
		Role:   "manager",
	}.Build())
	x.NoError(err)

	makeReadOnly(w.world, fake)
	assets := app.NewAssetServiceClient(w.conn)
	_, err = assets.Add(metadata.AppendToOutgoingContext(w.as(choi), cmd.WorkspaceHeader, "acme"), app.AssetAddRequest_builder{Name: "노트북"}.Build())
	w.refusedReadOnly(err, app.AssetService_Add_FullMethodName)

	_, err = w.svc.Accept(w.as(choi), app.InvitationAcceptRequest_builder{Ref: ref(v)}.Build())
	x.NoError(err, "an invitation to another workspace, accepted")
	_, err = assets.Add(inBeta(w.as(choi)), app.AssetAddRequest_builder{Name: "노트북"}.Build())
	x.NoError(err, "and written in")
	me, err := app.NewPartyServiceClient(w.conn).Me(inBeta(w.as(choi)), &app.PartyMeRequest{})
	x.NoError(err)
	x.False(me.GetReadonly())
}

// TestAMissingGrantIsNotReadOnly (design 7.10, levyan's design 13): what
// makes a workspace read-only is the grant of `loft.readonly`, on and in
// force, and nothing else -- not a grant of it that is off or has ended, nor
// one of another key. And on a deployment that bills nobody, nothing reads
// the copy: a grant of it put there by hand changes nothing.
func TestAMissingGrantIsNotReadOnly(t *testing.T) {
	w, fake := billed(t)
	x := w.x
	ctx := context.Background()
	assets := app.NewAssetServiceClient(w.conn)

	off := pdpb.EntitlementGrant_builder{
		Id: []byte("readonly-off"), TenantId: tenantId(x, w.tenant).Bytes(), Key: domain.ReadOnly, Version: 1,
		On: proto.Bool(false), ValidUntil: timestamppb.New(time.Now().Add(time.Hour)),
	}.Build()
	fake.Put(off)
	fake.Grant(tenantId(x, w.tenant), "loft.export", time.Now().Add(time.Hour))
	ended := fake.ReadOnly(tenantId(x, w.tenant))
	fake.End(ended)
	_, err := w.s.Follower.Sync(ctx, tenantId(x, w.tenant))
	x.NoError(err)
	x.False(w.readOnly(w.tenant))

	_, err = assets.Add(w.owner, app.AssetAddRequest_builder{Name: "모니터"}.Build())
	x.NoError(err, "a grant of it that is off, one that ended, and one of another key")
	me, err := app.NewPartyServiceClient(w.conn).Me(w.owner, &app.PartyMeRequest{})
	x.NoError(err)
	x.False(me.GetReadonly())

	t.Run("and a deployment that bills nobody reads no copy", func(t *testing.T) {
		w := newWorld(t)
		x := w.x
		tid := tenantId(x, w.tenant)
		x.NoError(pd.EntitlementCopy(w.s.Drv, mustContents(w)).In(ctx, tid, func(ctx context.Context, s entitle.Store) error {
			_, err := s.Put(ctx, entitle.Row{
				Tenant: tid, Key: domain.ReadOnly, Value: entitle.On,
				Until: time.Now().Add(time.Hour), Grant: []byte("by-hand"), Version: 1,
			})
			return err
		}))
		x.True(w.readOnly(w.tenant), "the copy says so")

		_, err := app.NewAssetServiceClient(w.conn).Add(w.owner, app.AssetAddRequest_builder{Name: "노트북"}.Build())
		x.NoError(err, "and nothing reads it")
		me, err := app.NewPartyServiceClient(w.conn).Me(w.owner, &app.PartyMeRequest{})
		x.NoError(err)
		x.False(me.GetReadonly())
	})
}

// TestAnOperatorsHandIsNotReadOnly (D64, as D60): the operator's commands
// write as the deployment, through no RPC, and what levyan makes of a
// workspace is not theirs to be held to.
func TestAnOperatorsHandIsNotReadOnly(t *testing.T) {
	w, fake := billed(t)
	x := w.x
	makeReadOnly(w, fake)

	_, err := app.NewAssetServiceClient(w.conn).Add(w.owner, app.AssetAddRequest_builder{Name: "키보드"}.Build())
	w.refusedReadOnly(err, app.AssetService_Add_FullMethodName)

	// The domain layer, as an operator's command reaches it: framed as the
	// deployment's work in the workspace, and asked over no wire.
	tid := tenantId(x, w.tenant)
	as := frame.Into(context.Background(), frame.New(pdid.Nil, tid, frame.Whole()).WithScope(frame.Only(tid)))
	_, err = w.s.Ungated.Asset().Add(as, app.AssetAddRequest_builder{Name: "키보드"}.Build())
	x.NoError(err)
	_, err = w.s.Ungated.AssetType().Add(as, app.AssetTypeAddRequest_builder{Name: "입력 장치", Kind: "item"}.Build())
	x.NoError(err)
	h := w.holder("han", "member")
	x.NotNil(h, "a membership an operator adds (`loft holder add`)")
}

// TestReadOnlyRefusesWithItsOwnReason (D64): a workspace levyan both
// restricts and makes read-only is refused as read-only, the stage that
// holds it to more, and its members are told both.
func TestReadOnlyRefusesWithItsOwnReason(t *testing.T) {
	w, fake := billed(t)
	x := w.x
	makeReadOnly(w, fake)
	fake.Restrict(tenantId(x, w.tenant))
	_, err := w.s.Follower.Sync(context.Background(), tenantId(x, w.tenant))
	x.NoError(err)
	x.True(w.restricted(w.tenant))

	_, err = app.NewAssetServiceClient(w.conn).Add(w.owner, app.AssetAddRequest_builder{Name: "키보드"}.Build())
	w.refusedReadOnly(err, app.AssetService_Add_FullMethodName)
	me, err := app.NewPartyServiceClient(w.conn).Me(w.owner, &app.PartyMeRequest{})
	x.NoError(err)
	x.True(me.GetReadonly())
	x.True(me.GetRestricted())
}
