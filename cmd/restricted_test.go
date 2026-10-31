package cmd_test

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/lesomnus/z"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/anypb"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/lesomnus/payday/access"
	"github.com/lesomnus/payday/audit"
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
	"github.com/lesomnus/loft/server/policy"
)

// refusedRestricted says that `err` is a write a restricted workspace may
// not make: FailedPrecondition, saying what it may not do, with the reason a
// page tells it by.
func (w *world) refusedRestricted(err error, what string) {
	w.x.Equal(codes.FailedPrecondition, status.Code(err), "%s: %v", what, err)
	w.x.Contains(status.Convert(err).Message(), "이 워크스페이스는 제한되어")
	w.x.Contains(status.Convert(err).Message(), what)
	var info *errdetails.ErrorInfo
	for _, d := range status.Convert(err).Details() {
		if v, ok := d.(*errdetails.ErrorInfo); ok {
			info = v
		}
	}
	w.x.NotNil(info, "%s: no reason a page can tell it by", what)
	w.x.Equal(domain.ErrorRestricted, info.GetReason(), what)
	w.x.Equal(domain.Restricted, info.GetMetadata()["entitlement"], what)
}

// restrict has levyan restrict the world's workspace, and the copy hear it.
func restrict(w *world, fake *levyantest.Levyan) []byte {
	id := fake.Restrict(tenantId(w.x, w.tenant))
	_, err := w.s.Follower.Sync(context.Background(), tenantId(w.x, w.tenant))
	w.x.NoError(err)
	w.x.True(w.restricted(w.tenant))
	return id
}

// TestARestrictedWorkspaceGrowsNothingElse (design 7.5, D60): while levyan
// restricts a workspace, every way it adds to what it is billed for is
// refused -- a new asset of any kind, many at once, stock made assets, an
// asset brought back from disposed or lost, a purchase received as assets, a
// file, a member by a login, an invitation sent or accepted, its history
// kept longer -- through the RPCs and a batch's operations alike, and with a
// reason a page tells it by. Everything else it does as it did: reads and
// downloads, hands things out and takes them back, books, moves, changes what
// bills nothing, disposes of and deletes, keeps its history shorter, takes
// stock in, ends a membership, and closes. Once levyan ends the grant, it
// grows again.
func TestARestrictedWorkspaceGrowsNothingElse(t *testing.T) {
	fake := levyantest.Start(t)
	w := newInvites(t, func(c *cmd.Config) {
		c.App.Billing.Levyan = fake.Config()
		// A window made shorter is the shorter one at once, for the one
		// made longer after it to be longer than the window in force.
		c.App.Retention.Grace = time.Millisecond
	})
	x := w.x
	ctx := context.Background()

	assets := app.NewAssetServiceClient(w.conn)
	stocks := app.NewStockServiceClient(w.conn)
	purchases := app.NewPurchaseServiceClient(w.conn)
	attachments := app.NewAttachmentServiceClient(w.conn)
	parties := app.NewPartyServiceClient(w.conn)
	tenants := app.NewTenantServiceClient(w.conn)
	custody := app.NewCustodyServiceClient(w.conn)
	reservations := app.NewReservationServiceClient(w.conn)
	tref := app.TenantRef_builder{Id: w.tenant.GetId()}.Build()
	aref := func(a *app.Asset) *app.AssetRef { return app.AssetRef_builder{Id: a.GetId()}.Build() }

	// What the workspace has before it is restricted.
	room, err := assets.Add(w.owner, app.AssetAddRequest_builder{Name: "회의실", Kind: "space"}.Build())
	x.NoError(err)
	laptop, err := assets.Add(w.owner, app.AssetAddRequest_builder{Name: "노트북", To: aref(room)}.Build())
	x.NoError(err)
	monitor, err := assets.Add(w.owner, app.AssetAddRequest_builder{Name: "모니터"}.Build())
	x.NoError(err)
	lost, err := assets.Add(w.owner, app.AssetAddRequest_builder{Name: "태블릿"}.Build())
	x.NoError(err)
	_, err = assets.SetAttributes(w.owner, app.AssetSetAttributesRequest_builder{Ref: aref(lost), Set: map[string]string{"status": "lost"}}.Build())
	x.NoError(err)
	model, err := app.NewItemModelServiceClient(w.conn).Add(w.owner, app.ItemModelAddRequest_builder{Name: "AA 건전지"}.Build())
	x.NoError(err)
	mref := app.ItemModelRef_builder{Id: model.GetId()}.Build()
	batteries, err := stocks.Add(w.owner, app.StockAddRequest_builder{Model: mref, Space: aref(room), Quantity: 10}.Build())
	x.NoError(err)
	sref := app.StockRef_builder{Id: batteries.GetId()}.Build()
	units, err := purchases.Add(w.owner, app.PurchaseAddRequest_builder{
		Name:  "모니터 구매",
		Lines: []*app.PurchaseLineSpec{app.PurchaseLineSpec_builder{Desc: "모니터", Quantity: 2, UnitCost: 100}.Build()},
	}.Build())
	x.NoError(err)
	stocked, err := purchases.Add(w.owner, app.PurchaseAddRequest_builder{
		Name:  "건전지 구매",
		Lines: []*app.PurchaseLineSpec{app.PurchaseLineSpec_builder{Model: mref, Quantity: 30, UnitCost: 500, ReceiveAs: "stock"}.Build()},
	}.Build())
	x.NoError(err)
	park := w.person("박지민", "park@example.com")
	kim := w.person("김철수", "kim@example.com")
	_, err = app.NewBookableServiceClient(w.conn).Add(w.owner, app.BookableAddRequest_builder{Asset: aref(room)}.Build())
	x.NoError(err)
	_, err = tenants.Configure(w.owner, app.TenantConfigureRequest_builder{Ref: tref, KeepDays: z.Ptr(uint32(365))}.Build())
	x.NoError(err)
	time.Sleep(10 * time.Millisecond)
	lee := w.account("lee", []string{"lee@example.com"}, nil)
	sent, err := w.invite(w.owner, "lee@example.com", "member")
	x.NoError(err, "an invitation sent before the workspace was restricted")
	member := partyOf(w.world, w.holder("choi", "member"))
	booker := w.holder("yun", "member")
	partyOf(w.world, booker)

	grant := restrict(w.world, fake)

	files := func() int {
		n := 0
		for range walk(t, w.c.App.Files) {
			n++
		}
		return n
	}
	before := files()

	// Refused, each saying what.
	_, err = assets.Add(w.owner, app.AssetAddRequest_builder{Name: "키보드"}.Build())
	w.refusedRestricted(err, "자산을 새로 등록할 수 없습니다")
	_, err = assets.Add(w.owner, app.AssetAddRequest_builder{Name: "창고", Kind: "space"}.Build())
	w.refusedRestricted(err, "자산을 새로 등록할 수 없습니다")
	_, err = assets.AddMany(w.owner, app.AssetAddManyRequest_builder{Items: []*app.AssetAddRequest{
		app.AssetAddRequest_builder{Name: "의자"}.Build(),
		app.AssetAddRequest_builder{Name: "책상"}.Build(),
	}}.Build())
	w.refusedRestricted(err, "자산을 대량으로 등록할 수 없습니다")
	_, err = stocks.Convert(w.owner, app.StockConvertRequest_builder{Ref: sref, Quantity: 2}.Build())
	w.refusedRestricted(err, "재고를 자산으로 전환할 수 없습니다")
	_, err = assets.SetAttributes(w.owner, app.AssetSetAttributesRequest_builder{Ref: aref(lost), Set: map[string]string{"status": "active"}}.Build())
	w.refusedRestricted(err, "분실·폐기한 자산을 다시 쓰는 상태로 바꿀 수 없습니다")
	_, err = assets.SetAttributes(w.owner, app.AssetSetAttributesRequest_builder{Ref: aref(lost), Set: map[string]string{"status": "retired"}}.Build())
	w.refusedRestricted(err, "분실·폐기한 자산을 다시 쓰는 상태로 바꿀 수 없습니다")
	_, err = purchases.Receive(w.owner, app.PurchaseReceiveRequest_builder{Ref: app.PurchaseRef_builder{Id: units.GetId()}.Build(), Into: aref(room)}.Build())
	w.refusedRestricted(err, "구매를 자산으로 입고할 수 없습니다")
	_, err = attachments.Upload(w.owner, app.AttachmentUploadRequest_builder{SubjectId: laptop.GetId(), Name: "영수증.pdf", ContentType: "application/pdf", Data: []byte("%PDF-1.7")}.Build())
	w.refusedRestricted(err, "첨부를 올릴 수 없습니다")
	x.Equal(before, files(), "and the file was not kept")
	n, err := w.s.Ent.Attachment.Query().Where(attachment.TenantId(tenantId(x, w.tenant).Uuid())).Count(ctx)
	x.NoError(err)
	x.Zero(n)
	_, err = parties.Invite(w.owner, app.PartyInviteRequest_builder{Ref: app.PartyRef_builder{Id: park.GetId()}.Build(), Alias: "park", Role: "member"}.Build())
	w.refusedRestricted(err, "로그인을 새로 발급할 수 없습니다")
	_, err = w.s.Identity.Lookup(ctx, "park")
	x.Error(err, "and no account was made for it at roster")
	_, err = w.invite(w.owner, "kim@example.com", "member")
	w.refusedRestricted(err, "구성원을 초대할 수 없습니다")
	_, err = w.svc.Accept(w.as(lee), app.InvitationAcceptRequest_builder{Ref: ref(sent)}.Build())
	w.refusedRestricted(err, "초대를 받아들일 수 없습니다")
	_, err = tenants.Configure(w.owner, app.TenantConfigureRequest_builder{Ref: tref, KeepDays: z.Ptr(uint32(730))}.Build())
	w.refusedRestricted(err, "이력을 지금보다 오래 보관하도록 바꿀 수 없습니다")
	_, err = tenants.Configure(w.owner, app.TenantConfigureRequest_builder{Ref: tref, KeepDays: z.Ptr(uint32(0))}.Build())
	w.refusedRestricted(err, "이력을 지금보다 오래 보관하도록 바꿀 수 없습니다")

	// And as an operation of a batch, which the domain layer serves in the
	// batch's transaction.
	add, err := anypb.New(app.AssetAddRequest_builder{Name: "프린터"}.Build())
	x.NoError(err)
	_, err = pdpb.NewBatchServiceClient(w.conn).Do(w.owner, pdpb.BatchRequest_builder{Ops: []*pdpb.Op{
		pdpb.Op_builder{Method: app.AssetService_Add_FullMethodName, Request: add}.Build(),
	}}.Build())
	x.Equal(codes.FailedPrecondition, status.Code(err), "%v", err)

	// Everything else, as it was.
	_, err = assets.Search(w.owner, app.AssetSearchRequest_builder{}.Build())
	x.NoError(err, "reading")
	_, err = assets.Export(metadata.AppendToOutgoingContext(w.owner, access.ReasonKey, "결제 정리"), &app.AssetExportRequest{})
	x.NoError(err, "taking its data out")
	_, err = assets.Move(w.owner, app.AssetMoveRequest_builder{Ref: aref(monitor), To: aref(room)}.Build())
	x.NoError(err, "moving")
	c, err := custody.Add(w.owner, app.CustodyAddRequest_builder{
		Party: app.PartyRef_builder{Id: kim.GetId()}.Build(),
		Lines: []*app.CustodyLineSpec{app.CustodyLineSpec_builder{Asset: aref(laptop)}.Build()},
	}.Build())
	x.NoError(err, "handing out")
	_, err = custody.Return(w.owner, app.CustodyReturnRequest_builder{Ref: app.CustodyRef_builder{Id: c.GetId()}.Build()}.Build())
	x.NoError(err, "taking back")
	at := time.Now().Add(48 * time.Hour).Truncate(time.Hour)
	_, err = reservations.Add(w.signIn(booker), app.ReservationAddRequest_builder{
		Name: "회의", BeginsAt: timestamppb.New(at), EndsAt: timestamppb.New(at.Add(time.Hour)),
		Items: []*app.ReservationItemSpec{app.ReservationItemSpec_builder{Resource: aref(room)}.Build()},
	}.Build())
	x.NoError(err, "booking")
	_, err = assets.SetAttributes(w.owner, app.AssetSetAttributesRequest_builder{Ref: aref(laptop), Set: map[string]string{"status": "in_repair", "name": "노트북 1"}}.Build())
	x.NoError(err, "what it is, changed to what is billed the same")
	_, err = assets.SetAttributes(w.owner, app.AssetSetAttributesRequest_builder{Ref: aref(monitor), Set: map[string]string{"status": "disposed"}}.Build())
	x.NoError(err, "disposing of")
	_, err = assets.Erase(w.owner, aref(lost))
	x.NoError(err, "deleting")
	_, err = purchases.Receive(w.owner, app.PurchaseReceiveRequest_builder{Ref: app.PurchaseRef_builder{Id: stocked.GetId()}.Build(), Into: aref(room)}.Build())
	x.NoError(err, "a purchase received into stock")
	_, err = stocks.Receive(w.owner, app.StockChangeRequest_builder{Ref: sref, Quantity: 5}.Build())
	x.NoError(err, "stock taken in")
	_, err = tenants.Configure(w.owner, app.TenantConfigureRequest_builder{Ref: tref, KeepDays: z.Ptr(uint32(180)), Timezone: z.Ptr("Asia/Tokyo")}.Build())
	x.NoError(err, "the history kept shorter, and a setting that bills nothing")
	_, err = parties.Deactivate(w.owner, app.PartyDeactivateRequest_builder{Ref: app.PartyRef_builder{Id: member.GetId()}.Build()}.Build())
	x.NoError(err, "a membership ended")

	// levyan ends the grant: the workspace grows again.
	fake.End(grant)
	_, err = w.s.Follower.Sync(ctx, tenantId(x, w.tenant))
	x.NoError(err)
	x.False(w.restricted(w.tenant))
	_, err = assets.Add(w.owner, app.AssetAddRequest_builder{Name: "키보드"}.Build())
	x.NoError(err, "a grant ended is a restriction lifted")
	_, err = w.svc.Accept(w.as(lee), app.InvitationAcceptRequest_builder{Ref: ref(sent)}.Build())
	x.NoError(err, "and what was sent before it, accepted")

	// And it closes, restricted or not: by what it is called (D57).
	restrict(w.world, fake)
	me, err := parties.Me(w.owner, &app.PartyMeRequest{})
	x.NoError(err)
	_, err = tenants.Close(w.owner, app.TenantCloseRequest_builder{Ref: tref, Confirm: me.GetWorkspaceName()}.Build())
	x.NoError(err, "closing")
}

// TestAnOperatorsHandIsNotRestricted (D60): the operator's commands write as
// the deployment, through no RPC, and levyan's restriction is not theirs to
// be held to -- what an operator adds to a restricted workspace is added.
func TestAnOperatorsHandIsNotRestricted(t *testing.T) {
	w, fake := billed(t)
	x := w.x
	restrict(w, fake)

	_, err := app.NewAssetServiceClient(w.conn).Add(w.owner, app.AssetAddRequest_builder{Name: "키보드"}.Build())
	x.Equal(codes.FailedPrecondition, status.Code(err), "a caller is held to it")

	// The domain layer, as an operator's command reaches it: framed as the
	// deployment's work in the workspace, and asked over no wire.
	tid := tenantId(x, w.tenant)
	as := frame.Into(context.Background(), frame.New(pdid.Nil, tid, frame.Whole()).WithScope(frame.Only(tid)))
	_, err = w.s.Ungated.Asset().Add(as, app.AssetAddRequest_builder{Name: "키보드"}.Build())
	x.NoError(err)
	h := w.holder("han", "member")
	x.NotNil(h, "a membership an operator adds (`loft holder add`)")
}

// TestADeploymentThatBillsNobodyReadsNoCopy: with `app.billing.levyan` unset
// nothing keeps a copy and nothing reads one -- a grant of `loft.restricted`
// put in it by hand restricts nothing, and Loft is as it was.
func TestADeploymentThatBillsNobodyReadsNoCopy(t *testing.T) {
	w := newWorld(t)
	x := w.x
	ctx := context.Background()

	x.Nil(w.s.Follower)
	x.Nil(w.s.Deps.Entitlements)
	x.NotContains(spun(w.s), entitle.FollowerName)

	tid := tenantId(x, w.tenant)
	x.NoError(pd.EntitlementCopy(w.s.Drv, mustContents(w)).In(ctx, tid, func(ctx context.Context, s entitle.Store) error {
		_, err := s.Put(ctx, entitle.Row{
			Tenant: tid, Key: domain.Restricted, Value: entitle.On,
			Until: time.Now().Add(time.Hour), Grant: []byte("by-hand"), Version: 1,
		})
		return err
	}))
	x.True(w.restricted(w.tenant), "the copy says so")

	_, err := app.NewAssetServiceClient(w.conn).Add(w.owner, app.AssetAddRequest_builder{Name: "노트북"}.Build())
	x.NoError(err, "and nothing reads it")
}

func mustContents(w *world) audit.Contents {
	v, err := w.s.Contents()
	w.x.NoError(err)
	return v
}

// partyOf is the person record of a membership.
func partyOf(w *world, h *app.Holder) *app.Party {
	p, err := w.s.Base.Party().Add(context.Background(), app.PartyAddRequest_builder{
		Tenant: app.TenantRef_builder{Id: w.tenant.GetId()}.Build(),
		Name:   h.GetAlias(), Kind: "person",
		Holder: app.HolderRef_builder{Id: h.GetId()}.Build(),
	}.Build())
	w.x.NoError(err)
	return p
}

// walk is every file under `dir`.
func walk(t *testing.T, dir string) func(yield func(string) bool) {
	return func(yield func(string) bool) {
		var all []string
		var visit func(string)
		visit = func(d string) {
			es, err := os.ReadDir(d)
			if err != nil {
				return
			}
			for _, e := range es {
				p := d + "/" + e.Name()
				if e.IsDir() {
					visit(p)
					continue
				}
				all = append(all, p)
			}
		}
		visit(dir)
		for _, p := range all {
			if !yield(p) {
				return
			}
		}
	}
}

// TestWhatARestrictedWorkspaceMayNotDoIsWhatItIsBilledBy (D60): the statuses
// an asset brought back from is refused for are the ones
// `loft.assets.active` does not count, as `proto/loft/meter.proto` declares
// them; and every method [domain.Grows] names is one somebody may call, so
// that none of it is a name that refuses nothing.
func TestWhatARestrictedWorkspaceMayNotDoIsWhatItIsBilledBy(t *testing.T) {
	x := require.New(t)

	declared := proto.GetExtension(app.File_loft_meter_proto.Options(), pdpb.E_App).(*pdpb.App)
	var notIn []string
	for _, m := range declared.GetMeters() {
		if m.GetKey() != "loft.assets.active" {
			continue
		}
		for _, w := range m.GetCount().GetWhere() {
			notIn = append(notIn, w.GetNotIn()...)
		}
	}
	x.ElementsMatch(notIn, domain.Unmetered)

	for key := range domain.Grows {
		if !strings.HasPrefix(key, "/") {
			continue
		}
		_, ok := policy.Table[key]
		x.True(ok, "%s is no method anybody may call", key)
	}
}
