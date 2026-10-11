package domain_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/lesomnus/payday/pdid"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/protobuf/types/known/timestamppb"

	app "github.com/lesomnus/rove"
	"github.com/lesomnus/rove/server/domain"
)

func (e *env) model(name string) *app.ItemModel {
	v, err := e.app().ItemModel().Add(e.owner, app.ItemModelAddRequest_builder{Name: name}.Build())
	e.x.NoError(err)
	return v
}

func (e *env) stock(m *app.ItemModel, in *app.Asset, qty, threshold int64) *app.Stock {
	v, err := e.app().Stock().Add(e.owner, app.StockAddRequest_builder{
		Model:     ref[app.ItemModelRef](m.GetId()),
		Space:     assetRef(in),
		Quantity:  qty,
		Threshold: threshold,
	}.Build())
	e.x.NoError(err)
	return v
}

func (e *env) quantity(s *app.Stock) int64 {
	v, err := e.app().Stock().Get(e.owner, app.StockGetRequest_builder{Ref: ref[app.StockRef](s.GetId())}.Build())
	e.x.NoError(err)
	return v.GetQuantity()
}

func TestHandOverAndBack(t *testing.T) {
	e := newEnv(t)
	x := e.x
	store := e.space("창고", nil)
	desk := e.space("자리", nil)
	laptop := e.item("노트북", "NB-1", store, 10*day)
	paper := e.stock(e.model("A4 용지"), store, 10, 3)
	kimCtx, kim := e.person("member")
	_, lee := e.person("member")
	boss, _ := e.person("manager")

	c, err := e.app().Custody().Add(e.owner, app.CustodyAddRequest_builder{
		Party: ref[app.PartyRef](kim.GetId()),
		Lines: []*app.CustodyLineSpec{
			app.CustodyLineSpec_builder{Asset: assetRef(laptop)}.Build(),
			app.CustodyLineSpec_builder{Stock: ref[app.StockRef](paper.GetId()), Quantity: 8}.Build(),
		},
	}.Build())
	x.NoError(err)
	x.Equal("issue", c.GetKind())
	x.True(sameId(kim.GetId(), e.get(laptop).GetCustodian().GetId()))
	x.EqualValues(2, e.quantity(paper))

	// The other managers heard the paper ran low -- not the one who took
	// it -- and the receiver was asked to acknowledge.
	in, err := e.app().Notification().Inbox(boss, &app.NotificationInboxRequest{})
	x.NoError(err)
	x.Equal("stock.low", in.GetItems()[0].GetKind())
	in, err = e.app().Notification().Inbox(e.owner, &app.NotificationInboxRequest{})
	x.NoError(err)
	x.Zero(in.GetUnread())
	in, err = e.app().Notification().Inbox(kimCtx, &app.NotificationInboxRequest{})
	x.NoError(err)
	x.Equal("custody.issued", in.GetItems()[0].GetKind())

	_, err = e.app().Custody().Add(e.owner, app.CustodyAddRequest_builder{
		Party: ref[app.PartyRef](lee.GetId()),
		Lines: []*app.CustodyLineSpec{app.CustodyLineSpec_builder{Asset: assetRef(laptop)}.Build()},
	}.Build())
	x.Equal(codes.FailedPrecondition, codeOf(err), "kim has it")

	_, err = e.app().Custody().Acknowledge(kimCtx, app.CustodyAcknowledgeRequest_builder{Ref: ref[app.CustodyRef](c.GetId())}.Build())
	x.NoError(err)

	got, err := e.app().Custody().Return(e.owner, app.CustodyReturnRequest_builder{
		Ref: ref[app.CustodyRef](c.GetId()),
		To:  assetRef(desk),
	}.Build())
	x.NoError(err)
	x.Equal("returned", got.GetStatus())
	a := e.get(laptop)
	x.False(a.HasCustodian())
	x.True(sameId(desk.GetId(), a.GetParentId()), "and it was put back where it was said to go")
	x.EqualValues(10, e.quantity(paper))
}

func TestLoansGoLate(t *testing.T) {
	e := newEnv(t)
	x := e.x
	store := e.space("창고", nil)
	phone := e.item("업무폰", "PH-1", store, day)
	kimCtx, kim := e.person("member")

	c, err := e.app().Custody().Add(e.owner, app.CustodyAddRequest_builder{
		Party: ref[app.PartyRef](kim.GetId()),
		DueAt: e.in(2 * day),
		Lines: []*app.CustodyLineSpec{app.CustodyLineSpec_builder{Asset: assetRef(phone)}.Build()},
	}.Build())
	x.NoError(err)
	x.Equal("loan", c.GetKind())

	count := func(kind string) int {
		in, err := e.app().Notification().Inbox(kimCtx, app.NotificationInboxRequest_builder{Size: 200}.Build())
		x.NoError(err)
		n := 0
		for _, v := range in.GetItems() {
			if v.GetKind() == kind {
				n++
			}
		}
		return n
	}

	e.now = e.now.Add(day + time.Hour)
	e.sweep()
	e.sweep()
	x.Equal(1, count("custody.due"), "reminded once, the day before")

	e.now = e.now.Add(2 * day)
	e.sweep()
	e.sweep()
	x.Equal(1, count("custody.overdue"), "told once that it is late")

	_, err = e.app().Custody().Extend(e.owner, app.CustodyExtendRequest_builder{Ref: ref[app.CustodyRef](c.GetId()), DueAt: e.in(7 * day)}.Build())
	x.NoError(err)
	e.now = e.now.Add(8 * day)
	e.sweep()
	x.Equal(2, count("custody.overdue"), "extended and late again is a second notice")
}

func TestStockNeverNegative(t *testing.T) {
	e := newEnv(t)
	x := e.x
	store := e.space("창고", nil)
	other := e.space("3층 탕비실", nil)
	m := e.model("AA 건전지")
	s := e.stock(m, store, 5, 0)

	_, err := e.app().Stock().Consume(e.owner, app.StockChangeRequest_builder{Ref: ref[app.StockRef](s.GetId()), Quantity: 6}.Build())
	x.Equal(codes.FailedPrecondition, codeOf(err))
	x.EqualValues(5, e.quantity(s))

	_, err = e.app().Stock().Transfer(e.owner, app.StockTransferRequest_builder{Ref: ref[app.StockRef](s.GetId()), To: assetRef(other), Quantity: 2}.Build())
	x.NoError(err)
	x.EqualValues(3, e.quantity(s))
	list, err := e.app().Stock().List(e.owner, app.StockListRequest_builder{
		Filters: []*app.StockFilter{app.StockFilter_builder{Space: assetRef(other)}.Build()},
	}.Build())
	x.NoError(err)
	x.Len(list.GetItems(), 1)
	x.EqualValues(2, list.GetItems()[0].GetQuantity(), "a stock was started where it went")

	conv, err := e.app().Stock().Convert(e.owner, app.StockConvertRequest_builder{Ref: ref[app.StockRef](s.GetId()), Quantity: 2, TagPrefix: "BT"}.Build())
	x.NoError(err)
	x.Len(conv.GetAssets(), 2)
	x.Equal("BT-001", conv.GetAssets()[0].GetTag())
	x.EqualValues(1, e.quantity(s))

	// A retry with the same op is the same change, not a second one.
	op := newOp()
	for range 2 {
		_, err = e.app().Stock().Receive(e.owner, app.StockChangeRequest_builder{Ref: ref[app.StockRef](s.GetId()), Quantity: 10, Op: op}.Build())
		x.NoError(err)
	}
	x.EqualValues(11, e.quantity(s))
}

func TestCount(t *testing.T) {
	e := newEnv(t)
	x := e.x
	room := e.space("개발팀", nil)
	other := e.space("디자인팀", nil)
	a1 := e.item("노트북 1", "C-1", room, 30*day)
	a2 := e.item("노트북 2", "C-2", room, 30*day)
	e.item("노트북 3", "C-3", room, 30*day)
	stray := e.item("모니터", "D-1", other, 30*day)

	c, err := e.app().InventoryCount().Add(e.owner, app.InventoryCountAddRequest_builder{Scope: assetRef(room)}.Build())
	x.NoError(err)
	cref := ref[app.InventoryCountRef](c.GetId())

	seenAt := e.now.Add(-time.Hour)
	scan := func(code string) *app.CountFinding {
		v, err := e.app().InventoryCount().Scan(e.owner, app.InventoryCountScanRequest_builder{Ref: cref, Code: code, SeenAt: timestamppb.New(seenAt), Op: newOp()}.Build())
		x.NoError(err)
		return v
	}
	x.Equal("seen", scan("C-1").GetKind())
	f := scan("D-1")
	x.Equal("misplaced", f.GetKind())
	x.Equal("unknown", scan("no-such-thing").GetKind())

	// The same scan sent again -- an offline queue replaying -- is one scan.
	op := newOp()
	_, err = e.app().InventoryCount().Scan(e.owner, app.InventoryCountScanRequest_builder{Ref: cref, Code: "C-1", Op: op}.Build())
	x.NoError(err)
	_, err = e.app().InventoryCount().Scan(e.owner, app.InventoryCountScanRequest_builder{Ref: cref, Code: "C-1", Op: op}.Build())
	x.Equal(codes.AlreadyExists, codeOf(err))

	r, err := e.app().InventoryCount().Reconcile(e.owner, app.InventoryCountReconcileRequest_builder{Ref: cref}.Build())
	x.NoError(err)
	x.EqualValues(2, r.GetMissing(), "C-2 and C-3 were not seen")

	_, err = e.app().InventoryCount().Resolve(e.owner, app.InventoryCountResolveRequest_builder{Ref: ref[app.CountFindingRef](f.GetId()), Resolution: "moved"}.Build())
	x.NoError(err)
	s := e.get(stray)
	x.True(sameId(room.GetId(), s.GetParentId()), "moved to where it was found")
	x.Equal(room.GetId(), e.where(room, stray, seenAt.Add(time.Minute), nil), "from when it was seen there")

	fs, err := e.app().CountFinding().List(e.owner, app.CountFindingListRequest_builder{
		Filters: []*app.CountFindingFilter{app.CountFindingFilter_builder{Asset: assetRef(a2)}.Build()},
	}.Build())
	x.NoError(err)
	x.Len(fs.GetItems(), 1)
	_, err = e.app().InventoryCount().Resolve(e.owner, app.InventoryCountResolveRequest_builder{Ref: ref[app.CountFindingRef](fs.GetItems()[0].GetId()), Resolution: "lost"}.Build())
	x.NoError(err)
	x.Equal("lost", e.get(a2).GetStatus())

	_, err = e.app().InventoryCount().Close(e.owner, app.InventoryCountCloseRequest_builder{Ref: cref}.Build())
	x.NoError(err)
	_, err = e.app().InventoryCount().Scan(e.owner, app.InventoryCountScanRequest_builder{Ref: cref, Code: "C-1"}.Build())
	x.Equal(codes.FailedPrecondition, codeOf(err))

	// A member scans only in a self-audit.
	kim, _ := e.person("member")
	c2, err := e.app().InventoryCount().Add(e.owner, app.InventoryCountAddRequest_builder{Scope: assetRef(room)}.Build())
	x.NoError(err)
	_, err = e.app().InventoryCount().Scan(kim, app.InventoryCountScanRequest_builder{Ref: ref[app.InventoryCountRef](c2.GetId()), Asset: assetRef(a1)}.Build())
	x.Equal(codes.PermissionDenied, codeOf(err))
	c3, err := e.app().InventoryCount().Add(e.owner, app.InventoryCountAddRequest_builder{Scope: assetRef(room), SelfService: true}.Build())
	x.NoError(err)
	_, err = e.app().InventoryCount().Scan(kim, app.InventoryCountScanRequest_builder{Ref: ref[app.InventoryCountRef](c3.GetId()), Asset: assetRef(a1)}.Build())
	x.NoError(err)
}

func TestImportExport(t *testing.T) {
	e := newEnv(t)
	x := e.x
	_, kim := e.person("member")

	sheet := "태그,이름,종류,유형,모델,제조사,위치,사용자,취득일,속성.ram\n" +
		"NB-100,새 노트북,물품,노트북,MacBook Air,Apple,본사/3층/개발팀,p1@example.com,2025-03-02,16\n" +
		"SP-100,창고,공간,,,,본사/4층,,,\n" +
		"MN-100,모니터,,,,,SP-100,,2025.03.04,\n"
	dry, err := e.app().Asset().Import(e.owner, app.AssetImportRequest_builder{Format: "csv", Data: []byte(sheet), DryRun: true}.Build())
	x.NoError(err)
	x.Empty(dry.GetErrors())
	x.EqualValues(3, dry.GetCreated())
	found, err := e.app().Asset().Search(e.owner, app.AssetSearchRequest_builder{Q: "NB-100"}.Build())
	x.NoError(err)
	x.Zero(found.GetTotal(), "a dry run changes nothing")

	v, err := e.app().Asset().Import(e.owner, app.AssetImportRequest_builder{Format: "csv", Data: []byte(sheet)}.Build())
	x.NoError(err)
	x.Empty(v.GetErrors())
	x.EqualValues(3, v.GetCreated())

	found, err = e.app().Asset().Search(e.owner, app.AssetSearchRequest_builder{Q: "NB-100"}.Build())
	x.NoError(err)
	x.EqualValues(1, found.GetTotal())
	nb := found.GetItems()[0]
	x.True(sameId(kim.GetId(), nb.GetCustodian().GetId()), "handed to the person the sheet named")
	x.Equal("16", nb.GetAttributes()["ram"])
	mn, err := e.app().Label().Resolve(e.owner, app.LabelResolveRequest_builder{Code: "MN-100"}.Build())
	x.NoError(err)
	sp, err := e.app().Label().Resolve(e.owner, app.LabelResolveRequest_builder{Code: "SP-100"}.Build())
	x.NoError(err)
	x.True(sameId(sp.GetAsset().GetId(), mn.GetAsset().GetParentId()), "placed by the tag of a row above it")

	// The same sheet again changes nothing; one fixed row changes one thing.
	v, err = e.app().Asset().Import(e.owner, app.AssetImportRequest_builder{Format: "csv", Data: []byte(sheet)}.Build())
	x.NoError(err)
	x.EqualValues(0, v.GetCreated())
	x.EqualValues(3, v.GetSkipped())

	bad := "태그,이름,상태\nX-1,,사용중\nX-2,뭔가,이상함\n"
	v, err = e.app().Asset().Import(e.owner, app.AssetImportRequest_builder{Format: "csv", Data: []byte(bad)}.Build())
	x.NoError(err)
	x.Len(v.GetErrors(), 2, "every wrong row is answered at once")

	out, err := e.app().Asset().Export(e.owner, app.AssetExportRequest_builder{Format: "xlsx"}.Build())
	x.NoError(err)
	x.NotEmpty(out.GetData())
	back, err := e.app().Asset().Import(e.owner, app.AssetImportRequest_builder{Format: "xlsx", Data: out.GetData(), DryRun: true}.Build())
	x.NoError(err)
	x.Empty(back.GetErrors(), "what export writes, import reads")
	x.EqualValues(0, back.GetCreated())
}

func TestLabels(t *testing.T) {
	e := newEnv(t)
	x := e.x
	ctx := context.Background()

	// No domain to have: every label prints with the deployment's own
	// address and a code no other label in it has (D28).
	room := e.space("회의실", nil)
	a := e.item("노트북", "NB-7", room, day)
	st, err := e.app().TenantDomain().Status(e.owner, &app.TenantDomainStatusRequest{})
	x.NoError(err)
	x.True(st.GetLabels(), "labels are on without a domain")
	x.False(st.GetTenantDomains())
	x.Equal("http://localhost:8080", st.GetBase())
	_, err = e.app().TenantDomain().Add(e.owner, app.TenantDomainAddRequest_builder{Sub: ptr("acme")}.Build())
	x.Equal(codes.FailedPrecondition, codeOf(err), "a domain of its own is nothing a workspace has here")

	blank, err := e.app().Label().Print(e.owner, app.LabelPrintRequest_builder{Count: 3}.Build())
	x.NoError(err)
	x.Len(blank.GetUrls(), 3)
	seen := map[string]bool{}
	for i, l := range blank.GetLabels() {
		x.Regexp(`^[0-9A-HJKMNP-TV-Z]{8}$`, l.GetCode())
		x.Equal("http://localhost:8080/l/"+l.GetCode(), blank.GetUrls()[i])
		x.False(seen[l.GetCode()], "two labels with one code")
		seen[l.GetCode()] = true
	}
	l := blank.GetLabels()[0]
	code := l.GetCode()

	// A label printed before codes had none, and goes by its identifier.
	old := blank.GetLabels()[1]
	oldId, err := pdid.From(old.GetId())
	x.NoError(err)
	x.NoError(e.s.Ent.Label.UpdateOneId(oldId.Uuid()).ClearCode().Exec(ctx))

	// Read however it comes: its URL, its code, the code as somebody types
	// it off the label -- and the old one's URL on whatever host it was
	// printed with.
	typed := strings.ToLower(code[:4] + "-" + code[4:])
	for v, want := range map[string][]byte{
		blank.GetUrls()[0]: l.GetId(),
		code:               l.GetId(),
		typed:              l.GetId(),
		"https://acme.l.test/l/" + domain.LabelCode(oldId): old.GetId(),
		domain.LabelCode(oldId):                            old.GetId(),
	} {
		r, err := e.app().Label().Resolve(e.owner, app.LabelResolveRequest_builder{Code: v}.Build())
		x.NoError(err, v)
		x.True(sameId(want, r.GetLabel().GetId()), v)
		x.False(r.HasAsset(), "a blank label is on nothing")
	}

	_, err = e.app().Label().Bind(e.owner, app.LabelBindRequest_builder{Ref: ref[app.LabelRef](l.GetId()), Asset: assetRef(a)}.Build())
	x.NoError(err, "stuck on something without a domain either")
	r, err := e.app().Label().Resolve(e.owner, app.LabelResolveRequest_builder{Code: code}.Build())
	x.NoError(err)
	x.True(sameId(a.GetId(), r.GetAsset().GetId()))

	// Another workspace reads it as nothing, through the wall; which
	// workspace it is in is the deployment's to say, for an account to be
	// told which of its own to look in.
	_, other := e.newTenant("other")
	_, err = e.app().Label().Resolve(other, app.LabelResolveRequest_builder{Code: code}.Build())
	x.Equal(codes.NotFound, codeOf(err), "another workspace's label reads as nothing")
	aid, err := pdid.From(a.GetId())
	x.NoError(err)
	for _, v := range []string{code, typed, blank.GetUrls()[0], domain.LabelCode(oldId), aid.String()} {
		in, ok, err := domain.LabelWorkspace(ctx, e.s.Ent, v)
		x.NoError(err, v)
		x.True(ok, v)
		x.Equal(e.tenant, in, v)
	}
	for _, v := range []string{"ZZZZZZZZ", "NB-7", "", "https://example.com/"} {
		_, ok, err := domain.LabelWorkspace(ctx, e.s.Ent, v)
		x.NoError(err, v)
		x.False(ok, v)
	}
	gone := blank.GetLabels()[2]
	goneId, err := pdid.From(gone.GetId())
	x.NoError(err)
	x.NoError(e.s.Ent.Label.UpdateOneId(goneId.Uuid()).SetDateErased(e.now).Exec(ctx))
	_, ok, err := domain.LabelWorkspace(ctx, e.s.Ent, gone.GetCode())
	x.NoError(err)
	x.False(ok, "an erased label is in no workspace")
	_, err = e.app().Label().Resolve(e.owner, app.LabelResolveRequest_builder{Code: gone.GetCode()}.Build())
	x.Equal(codes.NotFound, codeOf(err), "nor read in its own")

	// One code, one label, in the whole deployment -- which the database
	// holds as well.
	theirs, err := e.app().Label().Print(other, app.LabelPrintRequest_builder{Count: 1}.Build())
	x.NoError(err)
	x.NotEqual(code, theirs.GetLabels()[0].GetCode())
	tid, err := pdid.From(theirs.GetLabels()[0].GetId())
	x.NoError(err)
	x.Error(e.s.Ent.Label.UpdateOneId(tid.Uuid()).SetCode(code).Exec(ctx), "two labels with one code")
}

func TestNormalLabelCode(t *testing.T) {
	x := require.New(t)
	for in, want := range map[string]string{
		"7K3MQ9TX":                        "7K3MQ9TX",
		"7k3m-q9tx":                       "7K3MQ9TX",
		" 7K3M Q9TX ":                     "7K3MQ9TX",
		"ab1o-ilzz":                       "AB1011ZZ",
		"https://rove.example/l/7K3MQ9TX": "7K3MQ9TX",
	} {
		got, ok := domain.NormalLabelCode(in)
		x.True(ok, in)
		x.Equal(want, got, in)
	}
	for _, in := range []string{"", "7K3MQ9T", "7K3MQ9TXX", "7K3MQ9TU", "NB-7", "https://rove.example/"} {
		_, ok := domain.NormalLabelCode(in)
		x.False(ok, in)
	}
}

// TestLabelsOnTenantDomains is a deployment whose tenants print with domains
// of their own (D23): none until one is added, and then that one's.
func TestLabelsOnTenantDomains(t *testing.T) {
	e := newEnv(t)
	x := e.x
	e.s.Deps.LabelTenantDomains = true

	// No label domain yet: labels are off, and tags still resolve.
	room := e.space("회의실", nil)
	a := e.item("노트북", "NB-7", room, day)
	_, err := e.app().Label().Print(e.owner, app.LabelPrintRequest_builder{Count: 2}.Build())
	x.Equal(codes.FailedPrecondition, codeOf(err))

	_, err = e.app().TenantDomain().Add(e.owner, app.TenantDomainAddRequest_builder{Sub: ptr("acme")}.Build())
	x.NoError(err)
	st, err := e.app().TenantDomain().Status(e.owner, &app.TenantDomainStatusRequest{})
	x.NoError(err)
	x.True(st.GetLabels())
	x.True(st.GetTenantDomains())
	x.Equal("acme.l.test", st.GetActive().GetHost())

	blank, err := e.app().Label().Print(e.owner, app.LabelPrintRequest_builder{Count: 2}.Build())
	x.NoError(err)
	x.Len(blank.GetUrls(), 2)
	x.Equal("http://acme.l.test:8080/l/"+blank.GetLabels()[0].GetCode(), blank.GetUrls()[0], "the domain, on the scheme and port the deployment is on")

	r, err := e.app().Label().Resolve(e.owner, app.LabelResolveRequest_builder{Code: blank.GetUrls()[0]}.Build())
	x.NoError(err)
	x.False(r.HasAsset(), "a blank label is on nothing")
	_, err = e.app().Label().Bind(e.owner, app.LabelBindRequest_builder{Ref: ref[app.LabelRef](r.GetLabel().GetId()), Asset: assetRef(a)}.Build())
	x.NoError(err)
	r, err = e.app().Label().Resolve(e.owner, app.LabelResolveRequest_builder{Code: blank.GetUrls()[0]}.Build())
	x.NoError(err)
	x.True(sameId(a.GetId(), r.GetAsset().GetId()))

	_, other := e.newTenant("other")
	_, err = e.app().Label().Resolve(other, app.LabelResolveRequest_builder{Code: blank.GetUrls()[0]}.Build())
	x.Equal(codes.NotFound, codeOf(err), "another tenant's label reads as nothing")
}

func ptr[T any](v T) *T { return &v }
