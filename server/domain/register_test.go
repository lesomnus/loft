package domain_test

import (
	"archive/zip"
	"bytes"
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/lesomnus/payday/pderr"
	"github.com/lesomnus/z"
	"golang.org/x/text/encoding/korean"
	"google.golang.org/grpc/codes"

	app "github.com/lesomnus/rove"
	"github.com/lesomnus/rove/cmd"
	"github.com/lesomnus/rove/internal/ent/asset"
	"github.com/lesomnus/rove/internal/ent/event"
	"github.com/lesomnus/rove/internal/ent/fact"
	"github.com/lesomnus/rove/server/domain"
)

// Bulk registration, an intake: new assets only, as one operation (design
// 9.13, D34).

// atTag is a place as a sheet names one, by its tag.
func atTag(tag string) *app.AssetRef {
	return app.AssetRef_builder{Tag: app.AssetRefByTag_builder{Tag: z.Ptr(tag)}.Build()}.Build()
}

// assets counts the env's workspace's live assets.
func (e *env) assets() int {
	n, err := e.s.Ent.Asset.Query().Where(asset.TenantId(e.tenant.Uuid()), asset.DateErasedIsNil()).Count(context.Background())
	e.x.NoError(err)
	return n
}

// TestABulkRegistrationIsOneOperation: the assets, each with the first facts
// and the place an Add gives one, a model the rows name made once, and one
// event of the operation's own with a child for each asset -- so that an
// asset's history says it was registered, and by what intake.
func TestABulkRegistrationIsOneOperation(t *testing.T) {
	e := newEnv(t)
	x := e.x
	ctx, _ := e.person("manager")
	room := e.space("창고", nil)
	op := newOp()

	res, err := e.app().Asset().AddMany(ctx, app.AssetAddManyRequest_builder{
		Op:     op,
		Reason: "10월 입고",
		Items: []*app.AssetAddRequest{
			app.AssetAddRequest_builder{Name: "노트북", To: atTag(room.GetTag()), ModelName: z.Ptr("ThinkPad X1"), Maker: z.Ptr("Lenovo"), Since: e.ago(day)}.Build(),
			app.AssetAddRequest_builder{Name: "노트북", Tag: "NB-9", To: atTag(room.GetTag()), ModelName: z.Ptr("thinkpad x1"), Maker: z.Ptr("lenovo")}.Build(),
			app.AssetAddRequest_builder{Name: "의자", Serial: "C-1"}.Build(),
		},
	}.Build())
	x.NoError(err)
	x.Empty(res.GetIssues())
	x.Len(res.GetItems(), 3)
	x.Equal([]string{"A-00001", "NB-9", "A-00002"}, res.GetTags(), "a tag made up for each that has none, in order")
	x.Equal([]string{"Lenovo ThinkPad X1"}, res.GetNewModels(), "one model, named twice")
	x.Equal(op, res.GetEventId())

	ops := e.eventsOf("asset.add_many")
	x.Len(ops, 1)
	parent := ops[0]
	x.Nil(parent.CauseId)
	x.Equal(idUUID(op), parent.Id, "the operation's event is its op")
	x.Equal("대량 등록: 3개", e.say(parent))
	x.Equal(uint32(3), value[*app.AssetsAdded](e, parent).GetCount())
	x.Equal("10월 입고", parent.Reason)

	kids, err := e.s.Ent.Event.Query().Where(event.CauseId(parent.Id)).All(context.Background())
	x.NoError(err)
	kinds := map[string]int{}
	for _, k := range kids {
		kinds[k.Kind]++
	}
	x.Equal(map[string]int{"asset.add": 3, "model.add": 1}, kinds, "a child for each asset, and the model it made")

	for i, a := range res.GetItems() {
		id := idUUID(a.GetId())
		mine := ""
		for _, k := range kids {
			if k.SubjectId != nil && *k.SubjectId == id {
				x.Empty(mine, "two events of %s's own", a.GetTag())
				mine = e.say(k)
				x.Equal(a.GetTag(), value[*app.AssetAdded](e, k).GetTag())
				fs, err := e.s.Ent.Fact.Query().Where(fact.SubjectId(id)).All(context.Background())
				x.NoError(err)
				x.NotEmpty(fs)
				for _, f := range fs {
					x.Equal(k.Id, f.EventId, "a fact of %s points at another event", a.GetTag())
				}
			}
		}
		x.Equal(res.GetTags()[i]+" 등록", mine)

		found := false
		for _, en := range e.timeline(a, false) {
			if en.GetKind() == "event" {
				x.Equal("대량 등록: 3개", en.GetCause())
				found = true
			}
		}
		x.True(found, "%s's history has no event", a.GetTag())
	}

	laptop, other := e.get(res.GetItems()[0]), e.get(res.GetItems()[1])
	x.Equal(room.GetId(), laptop.GetParentId())
	x.Equal("located", laptop.GetPlacementMode())
	x.Equal(laptop.GetModel().GetId(), other.GetModel().GetId(), "the same model")
	x.Empty(e.get(res.GetItems()[2]).GetParentId())

	// The feed says what was done once, not once an asset.
	feed, err := e.app().Event().Recent(ctx, app.EventRecentRequest_builder{Size: 20}.Build())
	x.NoError(err)
	n := 0
	for _, v := range feed.GetItems() {
		if v.GetKind() == "asset.add" && len(v.GetCauseId()) > 0 {
			n++
		}
	}
	x.Zero(n, "an intake's children are under it, not in the feed")
}

// TestADryRunOfABulkRegistrationMakesNothing: it says the tags and the
// models it would make, and makes nothing.
func TestADryRunOfABulkRegistrationMakesNothing(t *testing.T) {
	e := newEnv(t)
	x := e.x
	room := e.space("창고", nil)
	before := e.assets()

	res, err := e.app().Asset().AddMany(e.owner, app.AssetAddManyRequest_builder{
		DryRun: true,
		Items: []*app.AssetAddRequest{
			app.AssetAddRequest_builder{Name: "노트북", To: atTag(room.GetTag()), ModelName: z.Ptr("X1")}.Build(),
			app.AssetAddRequest_builder{Name: "회의실 B", Kind: "space", TypeName: z.Ptr("회의실")}.Build(),
		},
	}.Build())
	x.NoError(err)
	x.Empty(res.GetIssues())
	x.Empty(res.GetItems())
	x.Equal([]string{"A-00001", "S-00002"}, res.GetTags())
	x.Equal([]string{"X1"}, res.GetNewModels())
	x.Equal([]string{"회의실"}, res.GetNewTypes(), "the owner may make a type")
	x.Equal(before, e.assets())
	x.Empty(e.eventsOf("asset.add_many"))
}

// TestABulkRegistrationSaysEveryIssueAndMakesNothing: a dry run says each,
// by item; anything else is refused with all of them, as the field
// violations a form points at, and makes nothing.
func TestABulkRegistrationSaysEveryIssueAndMakesNothing(t *testing.T) {
	e := newEnv(t)
	x := e.x
	ctx, _ := e.person("manager")
	_, kim := e.person("member")
	room := e.space("창고", nil)
	e.item("책상", "DK-1", room, day)
	before := e.assets()

	items := []*app.AssetAddRequest{
		app.AssetAddRequest_builder{Name: " "}.Build(),
		app.AssetAddRequest_builder{Name: "a", Tag: "DK-1"}.Build(),
		app.AssetAddRequest_builder{Name: "b", Tag: "X-1"}.Build(),
		app.AssetAddRequest_builder{Name: "c", Tag: "X-1"}.Build(),
		app.AssetAddRequest_builder{Name: "d", To: atTag("SP-404")}.Build(),
		app.AssetAddRequest_builder{Name: "e", To: atTag("DK-1")}.Build(),
		app.AssetAddRequest_builder{Name: "f", Since: e.in(2 * day)}.Build(),
		app.AssetAddRequest_builder{Name: "g", TypeName: z.Ptr("프로젝터")}.Build(),
		app.AssetAddRequest_builder{Name: "h", Custodian: ref[app.PartyRef](kim.GetId())}.Build(),
		app.AssetAddRequest_builder{Name: "i", Kind: "boat"}.Build(),
	}
	want := []string{"0 name", "1 tag", "3 tag", "4 to", "5 to", "6 since", "7 type_name", "8 custodian", "9 kind"}

	dry, err := e.app().Asset().AddMany(ctx, app.AssetAddManyRequest_builder{DryRun: true, Items: items}.Build())
	x.NoError(err)
	got := []string{}
	for _, v := range dry.GetIssues() {
		got = append(got, strings.Join([]string{string(rune('0' + v.GetItem())), v.GetField()}, " "))
	}
	x.Equal(want, got)
	for _, v := range dry.GetIssues() {
		if v.GetItem() == 7 {
			x.Contains(v.GetMessage(), "관리자", "a manager is told who makes types")
		}
	}

	_, err = e.app().Asset().AddMany(ctx, app.AssetAddManyRequest_builder{Items: items}.Build())
	x.Equal(codes.InvalidArgument, codeOf(err))
	fields := []string{}
	for _, v := range pderr.Violations(err) {
		fields = append(fields, v.Field)
	}
	x.Equal([]string{"items[0].name", "items[1].tag", "items[3].tag", "items[4].to", "items[5].to", "items[6].since", "items[7].type_name", "items[8].custodian", "items[9].kind"}, fields)
	x.Equal(before, e.assets(), "nothing was made")
	x.Empty(e.eventsOf("asset.add_many"))
}

// TestABulkRegistrationSentAgainIsTheSameOperation: a client that did not hear
// back sends it again, by its op, and is answered with what the first made --
// though by then the tags it gave are taken.
func TestABulkRegistrationSentAgainIsTheSameOperation(t *testing.T) {
	e := newEnv(t)
	x := e.x
	req := app.AssetAddManyRequest_builder{
		Op: newOp(),
		Items: []*app.AssetAddRequest{
			app.AssetAddRequest_builder{Name: "노트북", Tag: "NB-1"}.Build(),
			app.AssetAddRequest_builder{Name: "모니터"}.Build(),
		},
	}.Build()
	before := e.assets()

	a, err := e.app().Asset().AddMany(e.owner, req)
	x.NoError(err)
	again, err := e.app().Asset().AddMany(e.owner, req)
	x.NoError(err, "the same intake, sent again")
	x.Len(again.GetItems(), 2)
	for i := range a.GetItems() {
		x.Equal(a.GetItems()[i].GetId(), again.GetItems()[i].GetId())
	}
	x.Equal(a.GetTags(), again.GetTags())
	x.Equal(before+2, e.assets())
	x.Equal(1, e.ops(req.GetOp()))
}

// TestAnAdminMakesTheTypesASheetNames, once a name however it is written, and
// its event is a child of the intake's.
func TestAnAdminMakesTheTypesASheetNames(t *testing.T) {
	e := newEnv(t)
	x := e.x
	res, err := e.app().Asset().AddMany(e.owner, app.AssetAddManyRequest_builder{
		Items: []*app.AssetAddRequest{
			app.AssetAddRequest_builder{Name: "빔 1", TypeName: z.Ptr("프로젝터")}.Build(),
			app.AssetAddRequest_builder{Name: "빔 2", TypeName: z.Ptr(" 프로젝터 ")}.Build(),
		},
	}.Build())
	x.NoError(err)
	x.Equal([]string{"프로젝터"}, res.GetNewTypes())
	a, b := e.get(res.GetItems()[0]), e.get(res.GetItems()[1])
	x.NotEmpty(a.GetType().GetId())
	x.Equal(a.GetType().GetId(), b.GetType().GetId())
	x.Equal("item", a.GetKind())

	types := e.eventsOf("type.add")
	x.Len(types, 1)
	x.NotNil(types[0].CauseId)
	x.Equal(e.eventsOf("asset.add_many")[0].Id, *types[0].CauseId)
}

// sheet is the form's header and the rows given, as a CSV.
const sheet = "이름,종류,유형,모델,제조사,시리얼,태그,위치태그,취득일,기준일,설명,속성.색상,사용자,비고칸\n" +
	"노트북,물품,노트북,X1,Lenovo,PF-1,,SP-01,2024-03-01,2024.03.02,개발팀,검정,김민준,x\n" +
	",,,,,,,,,,,,,\n" +
	"회의실 B,공간,,,,,S-9,,,,,,,\n" +
	"모니터,,,,,,,,어제,,,,,\n"

// TestASheetIsReadIntoItems: by column, in the words a sheet writes; a column
// the form does not take said once; a cell that is not what its column is
// said where it is, and its row left out. The same in UTF-8 and in the CP949
// Korean Excel saves.
func TestASheetIsReadIntoItems(t *testing.T) {
	e := newEnv(t)
	x := e.x

	cp949, err := korean.EUCKR.NewEncoder().Bytes([]byte(sheet))
	x.NoError(err)

	for name, data := range map[string][]byte{"utf-8": append([]byte("\ufeff"), sheet...), "cp949": cp949} {
		t.Run(name, func(t *testing.T) {
			x := e.x
			res, err := e.app().Asset().ReadSheet(e.owner, app.AssetReadSheetRequest_builder{Data: data}.Build())
			x.NoError(err)

			x.Len(res.GetItems(), 2)
			x.Equal([]uint32{2, 4}, res.GetLines())
			nb := res.GetItems()[0]
			x.Equal("노트북", nb.GetName())
			x.Equal("item", nb.GetKind())
			x.Equal("노트북", nb.GetTypeName())
			x.Equal("X1", nb.GetModelName())
			x.Equal("Lenovo", nb.GetMaker())
			x.Equal("PF-1", nb.GetSerial())
			x.Empty(nb.GetTag())
			x.Equal("SP-01", nb.GetTo().GetTag().GetTag())
			x.Equal("2024-03-01", nb.GetAcquiredAt().AsTime().In(domain.DefaultZone).Format(time.DateOnly))
			x.Equal("2024-03-02", nb.GetSince().AsTime().In(domain.DefaultZone).Format(time.DateOnly))
			x.Equal("개발팀", nb.GetDesc())
			x.Equal(map[string]string{"색상": "검정"}, nb.GetAttributes())
			room := res.GetItems()[1]
			x.Equal("space", room.GetKind())
			x.Equal("S-9", room.GetTag())

			said := []string{}
			for _, v := range res.GetIssues() {
				said = append(said, strings.Join([]string{string(rune('0' + v.GetLine())), v.GetColumn()}, " "))
			}
			x.Equal([]string{"0 사용자", "0 비고칸", "5 취득일"}, said)
		})
	}
}

// TestTheFormIsWhatASheetIsReadAs: the template's header is the form's, and a
// sheet written on it is read back as it was written.
func TestTheFormIsWhatASheetIsReadAs(t *testing.T) {
	e := newEnv(t)
	x := e.x

	v, err := e.app().Asset().Form(e.owner, &app.AssetFormRequest{})
	x.NoError(err)
	header := strings.TrimPrefix(strings.TrimSpace(string(v.GetData())), "\ufeff")
	x.Equal("이름,종류,유형,모델,제조사,시리얼,태그,위치태그,취득일,기준일,설명,상태,컨디션,속성.키", header)

	res, err := e.app().Asset().ReadSheet(e.owner, app.AssetReadSheetRequest_builder{
		Data: []byte(header + "\n노트북,,,,,,NB-1,,,,,사용중,손상,\n"),
	}.Build())
	x.NoError(err)
	x.Empty(res.GetIssues(), "the form's own placeholder column, left empty, is nothing")
	x.Len(res.GetItems(), 1)
	x.Equal("active", res.GetItems()[0].GetStatus())
	x.Equal("damaged", res.GetItems()[0].GetCondition())
}

// TestAWorkbookIsRefusedUnopened: a workbook is told by how it begins and
// refused, saying how to save it as the form is; nothing reads it. An .xlsx
// is a zip, and an .xls -- and an encrypted .xlsx -- an OLE container.
func TestAWorkbookIsRefusedUnopened(t *testing.T) {
	e := newEnv(t)
	x := e.x

	xlsx := bytes.Buffer{}
	book := zip.NewWriter(&xlsx)
	part, err := book.Create("[Content_Types].xml")
	x.NoError(err)
	_, err = part.Write([]byte(`<?xml version="1.0" encoding="UTF-8"?><Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types"/>`))
	x.NoError(err)
	x.NoError(book.Close())

	for name, data := range map[string][]byte{
		"xlsx": xlsx.Bytes(),
		"xls":  append([]byte("\xd0\xcf\x11\xe0\xa1\xb1\x1a\xe1"), make([]byte, 512)...),
	} {
		t.Run(name, func(t *testing.T) {
			x := e.x
			_, err := e.app().Asset().ReadSheet(e.owner, app.AssetReadSheetRequest_builder{Data: data}.Build())
			x.Equal(codes.InvalidArgument, codeOf(err))
			x.Contains(err.Error(), "CSV UTF-8")
		})
	}
}

// TestALongNumberExcelWroteShortIsSaid: Excel writes a long number into a CSV
// the way it shows it, 3.57951E+14, unless its cell is text. In an
// identifier -- a serial, a tag, an attribute -- the digits it dropped are
// gone, so the line is said and left out rather than registered short.
func TestALongNumberExcelWroteShortIsSaid(t *testing.T) {
	e := newEnv(t)
	x := e.x

	res, err := e.app().Asset().ReadSheet(e.owner, app.AssetReadSheetRequest_builder{
		Data: []byte("이름,시리얼,태그,속성.IMEI,설명\n" +
			"휴대폰,3.57951E+14,,,\n" +
			"휴대폰,,1.2E+11,,\n" +
			"휴대폰,,,3.5795E+14,\n" +
			"휴대폰,357951234567890,P-1,357951234567890,1E+3\n"),
	}.Build())
	x.NoError(err)

	said := []string{}
	for _, v := range res.GetIssues() {
		said = append(said, strings.Join([]string{string(rune('0' + v.GetLine())), v.GetColumn()}, " "))
	}
	x.Equal([]string{"2 시리얼", "3 태그", "4 속성.IMEI"}, said)
	x.Equal([]uint32{5}, res.GetLines(), "the digits written out are read, and a description is not an identifier")
}

// TestAnAssetRegisteredInBulkIsAFewStatements is [TestAnAssetMadeInBulkIsAFewStatements]
// for the intake: the assets, events, facts and placements a few hundred rows
// to a statement, where the conversion writes each asset as an Add of its
// own. A ceiling with room in it, as that one is.
func TestAnAssetRegisteredInBulkIsAFewStatements(t *testing.T) {
	if os.Getenv("PDTEST_POSTGRES") == "" {
		t.Skip("counts what pgx is asked: set PDTEST_POSTGRES")
	}

	c := &perConn{n: map[*pgx.Conn]int{}}
	e := newEnvWith(t, func(cfg *cmd.Config) {
		v, err := pgx.ParseConfig(cfg.Db.Dsn)
		if err != nil {
			t.Fatal(err)
		}
		v.Tracer = c

		name := stdlib.RegisterConnConfig(v)
		t.Cleanup(func() { stdlib.UnregisterConnConfig(name) })
		cfg.Db.Dsn = name
	})
	x := e.x
	room := e.space("창고", nil)

	items := func(n int) []*app.AssetAddRequest {
		vs := make([]*app.AssetAddRequest, n)
		for i := range vs {
			vs[i] = app.AssetAddRequest_builder{Name: "모니터", To: atTag(room.GetTag()), ModelName: z.Ptr("U2723QE"), Maker: z.Ptr("Dell")}.Build()
		}
		return vs
	}
	// One first: what the first of a kind costs is not what every one costs.
	_, err := e.app().Asset().AddMany(e.owner, app.AssetAddManyRequest_builder{Items: items(1)}.Build())
	x.NoError(err)

	const n = 300
	c.count(true)
	v, err := e.app().Asset().AddMany(e.owner, app.AssetAddManyRequest_builder{Items: items(n)}.Build())
	c.count(false)
	x.NoError(err)
	x.Len(v.GetItems(), n)

	per := float64(c.most()) / n
	t.Logf("%.2f statements an asset", per)
	x.LessOrEqual(per, 3.0, "an asset registered in bulk is %.2f statements; it was 2.1 when this was written (the conversion, an Add an asset, is 11.2)", per)
}
