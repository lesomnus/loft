package domain_test

import (
	"encoding/csv"
	"fmt"
	"maps"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/lesomnus/z"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/protobuf/types/known/timestamppb"

	app "github.com/lesomnus/rove"
)

// zoned puts the workspace in the zone `name`, and answers it.
func (e *env) zoned(name string) *time.Location {
	_, err := e.app().Tenant().Configure(e.owner, app.TenantConfigureRequest_builder{
		Ref:      app.TenantRef_builder{Id: e.tenant.Bytes()}.Build(),
		Timezone: z.Ptr(name),
	}.Build())
	e.x.NoError(err)
	l, err := time.LoadLocation(name)
	e.x.NoError(err)
	return l
}

// written is a CSV a report wrote, read back: its header, and its rows by what
// their first column says.
func written(t *testing.T, f *app.AssetExportResponse) ([]string, map[string][]string) {
	t.Helper()
	x := require.New(t)
	x.Equal("text/csv; charset=utf-8", f.GetContentType())
	rs, err := csv.NewReader(strings.NewReader(strings.TrimPrefix(string(f.GetData()), "\ufeff"))).ReadAll()
	x.NoError(err)
	x.NotEmpty(rs)
	by := map[string][]string{}
	for _, r := range rs[1:] {
		by[r[0]] = r
	}
	return rs[0], by
}

func (e *env) set(a *app.Asset, status string) {
	_, err := e.app().Asset().SetAttributes(e.owner, app.AssetSetAttributesRequest_builder{
		Ref: assetRef(a),
		Set: map[string]string{"status": status},
	}.Build())
	e.x.NoError(err)
}

// TestTheCensusCountsWhatThereIsByTypeAndBySpace: a row each type and each
// space, of how many there are in each status. What is inside something that
// is no space -- a part in a computer -- is counted in the space that thing
// is in, a space with what the spaces in it hold beside, and what has no type
// or is in no space is a row of its own. What was erased is not counted, nor
// another kind.
func TestTheCensusCountsWhatThereIsByTypeAndBySpace(t *testing.T) {
	e := newEnv(t)
	x := e.x
	kind := func(name string) *app.AssetType {
		v, err := e.app().AssetType().Add(e.owner, app.AssetTypeAddRequest_builder{Name: name, Kind: "item"}.Build())
		x.NoError(err)
		return v
	}
	notebook, monitor := kind("노트북"), kind("모니터")
	typed := func(name, tag string, ty *app.AssetType, in *app.Asset) *app.Asset {
		req := app.AssetAddRequest_builder{Name: name, Tag: tag, Kind: "item", To: assetRef(in)}.Build()
		if ty != nil {
			req.SetType(ref[app.AssetTypeRef](ty.GetId()))
		}
		v, err := e.app().Asset().Add(e.owner, req)
		x.NoError(err)
		return v
	}

	hq := e.space("본관", nil)
	floor := e.space("3층", hq)
	room := e.space("301호", floor)
	store := e.space("창고", nil)
	e.space("빈 방", nil)

	nb1 := typed("노트북 1", "NB-1", notebook, room)
	nb2 := typed("노트북 2", "NB-2", notebook, room)
	nb3 := typed("노트북 3", "NB-3", notebook, store)
	typed("모니터 1", "MN-1", monitor, floor)
	e.set(nb2, "in_repair")
	e.set(nb3, "lost")
	// A part installed in a laptop, of no type: counted in the laptop's room.
	ram, err := e.app().Asset().Add(e.owner, app.AssetAddRequest_builder{Name: "메모리", Tag: "RAM-1", Kind: "item"}.Build())
	x.NoError(err)
	_, err = e.app().Asset().Move(e.owner, app.AssetMoveRequest_builder{Ref: assetRef(ram), To: assetRef(nb1), Mode: "installed"}.Build())
	x.NoError(err)
	// Of no type and in no space.
	_, err = e.app().Asset().Add(e.owner, app.AssetAddRequest_builder{Name: "의자", Tag: "CH-1", Kind: "item"}.Build())
	x.NoError(err)
	// Erased, and another kind: neither is an item counted.
	gone := typed("노트북 4", "NB-4", notebook, room)
	_, err = e.app().Asset().Erase(e.owner, assetRef(gone))
	x.NoError(err)
	_, err = e.app().Asset().Add(e.owner, app.AssetAddRequest_builder{Name: "촬영 키트", Tag: "KT-1", Kind: "kit", To: assetRef(store)}.Build())
	x.NoError(err)

	type counted struct {
		total, within uint32
		statuses      map[string]uint32
		path          string
	}
	read := func(by, kind string) (map[string]counted, []string, *app.AssetCensusRow) {
		v, err := e.app().Asset().Census(e.owner, app.AssetCensusRequest_builder{By: by, Kind: kind}.Build())
		x.NoError(err)
		out := map[string]counted{}
		order := []string{}
		for _, r := range v.GetRows() {
			out[r.GetName()] = counted{r.GetTotal(), r.GetWithin(), r.GetStatuses(), strings.Join(r.GetPath(), "/")}
			order = append(order, r.GetName())
		}
		return out, order, v.GetTotal()
	}

	types, order, total := read("", "")
	x.Equal([]string{"노트북", "모니터", "(유형 없음)"}, order, "by type, by name, the item's kind when none is said")
	x.Equal(counted{3, 0, map[string]uint32{"active": 1, "in_repair": 1, "lost": 1}, ""}, types["노트북"])
	x.Equal(counted{1, 0, map[string]uint32{"active": 1}, ""}, types["모니터"])
	x.Equal(counted{2, 0, map[string]uint32{"active": 2}, ""}, types["(유형 없음)"])
	x.EqualValues(6, total.GetTotal())
	x.Equal(map[string]uint32{"active": 4, "in_repair": 1, "lost": 1}, total.GetStatuses())

	places, order, total := read("location", "item")
	x.Equal([]string{"본관", "3층", "301호", "창고", "(위치 없음)"}, order, "as the tree reads, and nothing for an empty room")
	x.Equal(counted{0, 4, map[string]uint32{}, ""}, places["본관"])
	x.Equal(counted{1, 4, map[string]uint32{"active": 1}, "본관"}, places["3층"])
	x.Equal(counted{3, 3, map[string]uint32{"active": 2, "in_repair": 1}, "본관/3층"}, places["301호"], "the memory in the laptop is in the laptop's room")
	x.Equal(counted{1, 1, map[string]uint32{"lost": 1}, ""}, places["창고"])
	x.Equal(counted{1, 1, map[string]uint32{"active": 1}, ""}, places["(위치 없음)"])
	x.EqualValues(6, total.GetTotal())

	kits, _, total := read("location", "kit")
	x.EqualValues(1, kits["창고"].total)
	x.EqualValues(1, total.GetTotal())

	_, err = e.app().Asset().Census(e.owner, app.AssetCensusRequest_builder{By: "owner"}.Build())
	x.Equal(codes.InvalidArgument, codeOf(err))
	_, err = e.app().Asset().Census(e.owner, app.AssetCensusRequest_builder{Kind: "person"}.Build())
	x.Equal(codes.InvalidArgument, codeOf(err))

	t.Run("and writes it as a CSV", func(t *testing.T) {
		x := require.New(t)
		f, err := e.app().Asset().ExportCensus(e.owner, app.AssetCensusRequest_builder{By: "location"}.Build())
		x.NoError(err)
		x.Regexp(`^rove-census-item-\d{8}\.csv$`, f.GetName())
		head, rows := written(t, f)
		x.Equal([]string{"위치", "태그", "합계", "하위 포함", "주문", "사용중", "수리중", "분실", "불용", "폐기"}, head)
		x.Equal([]string{"본관 / 3층 / 301호", room.GetTag(), "3", "3", "0", "2", "1", "0", "0", "0"}, rows["본관 / 3층 / 301호"])
		x.Equal([]string{"합계", "", "6", "6", "0", "4", "1", "1", "0", "0"}, rows["합계"])
	})
}

// TestTheDashboardCountsWhatThereIs: the numbers the dashboard shows
// (`AssetService.Report`) -- how many there are of each kind, status and
// type, what is out and late, and the work under way -- and what is late, by
// who has it. Every kind is counted here, and what was erased is not.
func TestTheDashboardCountsWhatThereIs(t *testing.T) {
	e := newEnv(t)
	x := e.x
	seoul := e.zoned("Asia/Seoul")
	kind := func(name, of string) *app.AssetType {
		v, err := e.app().AssetType().Add(e.owner, app.AssetTypeAddRequest_builder{Name: name, Kind: of}.Build())
		x.NoError(err)
		return v
	}
	notebook, room := kind("노트북", "item"), kind("회의실", "space")
	add := func(name, tag, of string, ty *app.AssetType) *app.Asset {
		req := app.AssetAddRequest_builder{Name: name, Tag: tag, Kind: of}.Build()
		if ty != nil {
			req.SetType(ref[app.AssetTypeRef](ty.GetId()))
		}
		v, err := e.app().Asset().Add(e.owner, req)
		x.NoError(err)
		return v
	}

	add("본관", "", "space", nil)
	add("대회의실", "", "space", room)
	nb1 := add("노트북 1", "NB-1", "item", notebook)
	nb2 := add("노트북 2", "NB-2", "item", notebook)
	nb3 := add("노트북 3", "NB-3", "item", notebook)
	mn1 := add("모니터 1", "MN-1", "item", nil)
	chair := add("의자", "CH-1", "item", nil)
	add("촬영 키트", "KT-1", "kit", nil)
	e.set(nb2, "in_repair")
	e.set(chair, "lost")
	_, err := e.app().Asset().Erase(e.owner, assetRef(add("노트북 4", "NB-4", "item", notebook)))
	x.NoError(err)

	// NB-1 lent to one until an hour from now, NB-3 issued to another for
	// keeps, and MN-1 lent and back.
	_, kim := e.person("member")
	_, lee := e.person("member")
	give := func(to *app.Party, a *app.Asset, due *time.Time) *app.Custody {
		req := app.CustodyAddRequest_builder{
			Party: ref[app.PartyRef](to.GetId()),
			Lines: []*app.CustodyLineSpec{app.CustodyLineSpec_builder{Asset: assetRef(a)}.Build()},
		}.Build()
		if due != nil {
			req.SetDueAt(timestamppb.New(*due))
		}
		v, err := e.app().Custody().Add(e.owner, req)
		x.NoError(err)
		return v
	}
	soon := e.now.Add(time.Hour)
	lent := give(kim, nb1, &soon)
	give(lee, nb3, nil)
	back := give(lee, mn1, &soon)
	_, err = e.app().Custody().Return(e.owner, app.CustodyReturnRequest_builder{Ref: ref[app.CustodyRef](back.GetId())}.Build())
	x.NoError(err)

	// A repair under way on NB-2, and one done on NB-3.
	work := func(a *app.Asset, name string) *app.WorkOrder {
		v, err := e.app().WorkOrder().Add(e.owner, app.WorkOrderAddRequest_builder{Asset: ref[app.AssetRef](a.GetId()), Name: name}.Build())
		x.NoError(err)
		return v
	}
	work(nb2, "팬 소음")
	_, err = e.app().WorkOrder().Complete(e.owner, app.WorkOrderCompleteRequest_builder{Ref: ref[app.WorkOrderRef](work(nb3, "키보드 교체").GetId())}.Build())
	x.NoError(err)

	// Two days on, NB-1 is late.
	e.now = soon.Add(48 * time.Hour)

	v, err := e.app().Asset().Report(e.owner, app.AssetReportRequest_builder{Kind: "summary"}.Build())
	x.NoError(err)
	got, types := map[string]float64{}, []string{}
	for _, r := range v.GetRows() {
		k := r.GetGroup() + "/" + r.GetKey()
		if r.GetGroup() == "type" {
			k = "type/" + r.GetLabel()
			types = append(types, r.GetLabel())
			if r.GetLabel() == "노트북" {
				x.Equal(idOf(notebook.GetId()).String(), r.GetKey(), "a type by its identifier")
			}
		}
		got[k] = r.GetValue()
	}
	x.Equal(map[string]float64{
		"kind/item": 5, "kind/space": 2, "kind/kit": 1,
		"status/active": 6, "status/in_repair": 1, "status/lost": 1,
		"type/노트북": 3, "type/회의실": 1, "type/(유형 없음)": 4,
		"custody/open": 2, "custody/overdue": 1,
		"work/open": 1,
	}, got)
	x.Equal([]string{"노트북", "회의실", "(유형 없음)"}, types, "as the census says them: by name, and what has none last")

	v, err = e.app().Asset().Report(e.owner, app.AssetReportRequest_builder{Kind: "custody"}.Build())
	x.NoError(err)
	late, held := []*app.ReportRow{}, map[string]float64{}
	for _, r := range v.GetRows() {
		switch r.GetGroup() {
		case "overdue":
			late = append(late, r)
		case "party":
			held[r.GetLabel()] = r.GetValue()
			x.Contains([]string{idOf(kim.GetId()).String(), idOf(lee.GetId()).String()}, r.GetKey())
		}
	}
	x.Len(late, 1)
	x.Equal(idOf(lent.GetId()).String(), late[0].GetKey())
	x.Equal(kim.GetName(), late[0].GetLabel(), "who has it, by name")
	x.InDelta(2, late[0].GetValue(), 1e-9, "days since it was due")
	x.Equal(soon.In(seoul).Format(time.DateOnly), late[0].GetDetail()["due"])
	x.Equal(map[string]float64{kim.GetName(): 1, lee.GetName(): 1}, held, "how much each has out")
}

// TestAListOfPeopleTellsTwoOfANameApart (D54): every answer that lists
// people -- what is out and its CSV, the dashboard's card of what is late and
// of how much each has, and who each booking is for -- names them as the
// register does (D50): by name, and two of a name by the team one is in;
// then, in a team of that name too, by 사번; and where neither tells them
// apart, by identifier. Never by how they are reached.
func TestAListOfPeopleTellsTwoOfANameApart(t *testing.T) {
	e := newEnv(t)
	x := e.x
	n := 0
	party := func(name, kind string, in *app.Party, code string) *app.Party {
		n++
		req := app.PartyAddRequest_builder{Name: name, Kind: kind, Code: code, Email: fmt.Sprintf("p%d@acme.example", n)}.Build()
		if in != nil {
			req.SetParentId(in.GetId())
		}
		v, err := e.app().Party().Add(e.owner, req)
		x.NoError(err)
		return v
	}
	dev, sales := party("개발팀", "team", nil, ""), party("영업팀", "team", nil, "")
	store := e.space("창고", nil)
	room := e.space("회의실", nil)
	e.bookable(room, nil)
	soon := e.now.Add(time.Hour)

	// Each lent a laptop until an hour from now, and the room booked for
	// each tomorrow, an hour apart.
	want := map[string]string{}
	for i, v := range []struct {
		name string
		in   *app.Party
		code string
		says string
	}{
		{"이서연", dev, "", "이서연"},
		{"김민준", sales, "", "김민준 · 영업팀"},
		{"김민준", dev, "1001", "김민준 · 개발팀 · 1001"},
		{"김민준", dev, "", ""},
	} {
		p := party(v.name, "person", v.in, v.code)
		if v.says == "" {
			// Nothing of theirs but who they are tells them apart.
			v.says = "김민준 · 개발팀 · " + idOf(p.GetId()).String()
		}
		tag := fmt.Sprintf("NB-%d", i+1)
		_, err := e.app().Custody().Add(e.owner, app.CustodyAddRequest_builder{
			Party: ref[app.PartyRef](p.GetId()),
			DueAt: timestamppb.New(soon),
			Lines: []*app.CustodyLineSpec{app.CustodyLineSpec_builder{Asset: assetRef(e.item("노트북", tag, store, 0))}.Build()},
		}.Build())
		x.NoError(err)
		from := e.tomorrow(9+i, 0)
		_, err = e.reserve(e.owner, room, from, from.Add(30*time.Minute), func(r *app.ReservationAddRequest) {
			r.SetName(tag)
			r.SetParty(ref[app.PartyRef](p.GetId()))
		})
		x.NoError(err)
		want[tag] = v.says
	}
	says := slices.Sorted(maps.Values(want))

	// What is out, and its CSV.
	v, err := e.app().Custody().Outstanding(e.owner, &app.CustodyOutstandingRequest{})
	x.NoError(err)
	got := map[string]string{}
	for _, r := range v.GetRows() {
		got[r.GetAssetTag()] = r.GetPartyName()
	}
	x.Equal(want, got)
	f, err := e.app().Custody().ExportOutstanding(e.owner, &app.CustodyOutstandingRequest{})
	x.NoError(err)
	_, rows := written(t, f)
	x.Equal(says, slices.Sorted(maps.Keys(rows)), "the first column, who has it")
	x.NotContains(string(f.GetData()), "acme.example", "nobody's address")

	// Who each booking is for.
	cal, err := e.app().Reservation().Calendar(e.owner, app.ReservationCalendarRequest_builder{
		From: timestamppb.New(e.now),
		To:   timestamppb.New(e.now.Add(72 * time.Hour)),
	}.Build())
	x.NoError(err)
	got = map[string]string{}
	for _, c := range cal.GetEntries() {
		got[c.GetReservation().GetName()] = c.GetPartyName()
	}
	x.Equal(want, got)

	// The dashboard's card, once they are late: who has each, and how much
	// each has.
	e.now = soon.Add(time.Minute)
	card, err := e.app().Asset().Report(e.owner, app.AssetReportRequest_builder{Kind: "custody"}.Build())
	x.NoError(err)
	late, held := []string{}, []string{}
	for _, r := range card.GetRows() {
		switch r.GetGroup() {
		case "overdue":
			late = append(late, r.GetLabel())
		case "party":
			held = append(held, r.GetLabel())
			x.EqualValues(1, r.GetValue())
		}
	}
	x.ElementsMatch(says, late)
	x.ElementsMatch(says, held)
}

// TestWhatIsOutSaysWhoHasItAndHowLate: a row each thing handed over and not
// back, the most overdue first and what is kept last, with who has it by
// name; how much is out in all, whatever the page; and how late, in the
// workspace's days -- a loan due late on the 20th in New York is a day late
// early on the 21st there, which is still the 21st in UTC on both sides.
func TestWhatIsOutSaysWhoHasItAndHowLate(t *testing.T) {
	e := newEnv(t)
	x := e.x
	ny := e.zoned("America/New_York")
	at := func(d, h, m int) time.Time { return time.Date(2026, 10, d, h, m, 0, 0, ny) }

	e.now = at(19, 9, 0)
	store := e.space("창고", nil)
	nb1 := e.item("노트북 1", "NB-1", store, 0)
	nb2 := e.item("노트북 2", "NB-2", store, 0)
	nb3 := e.item("노트북 3", "NB-3", store, 0)
	batteries := e.stock(e.model("건전지"), store, 10, 0)
	_, kim := e.person("member")
	_, lee := e.person("member")
	give := func(to *app.Party, due *time.Time, lines ...*app.CustodyLineSpec) *app.Custody {
		req := app.CustodyAddRequest_builder{Party: ref[app.PartyRef](to.GetId()), Lines: lines}.Build()
		if due != nil {
			req.SetDueAt(timestamppb.New(*due))
		}
		v, err := e.app().Custody().Add(e.owner, req)
		x.NoError(err)
		return v
	}
	line := func(a *app.Asset) *app.CustodyLineSpec {
		return app.CustodyLineSpec_builder{Asset: assetRef(a)}.Build()
	}

	due := at(20, 23, 0)
	give(kim, &due, line(nb1))
	e.now = at(19, 10, 0)
	give(lee, nil, line(nb2), app.CustodyLineSpec_builder{Stock: ref[app.StockRef](batteries.GetId()), Quantity: 3}.Build())
	later := at(25, 18, 0)
	back := give(kim, &later, line(nb3))
	_, err := e.app().Custody().Return(e.owner, app.CustodyReturnRequest_builder{Ref: ref[app.CustodyRef](back.GetId())}.Build())
	x.NoError(err)

	// Early on the 21st in New York, which UTC says is the 21st too.
	e.now = at(21, 1, 0)
	x.Equal(e.now.UTC().Day(), due.UTC().Day(), "the loan was due on the same day in UTC")
	v, err := e.app().Custody().Outstanding(e.owner, &app.CustodyOutstandingRequest{})
	x.NoError(err)
	x.EqualValues(3, v.GetTotal())
	x.EqualValues(2, v.GetCustodies())
	x.EqualValues(1, v.GetOverdue())
	x.EqualValues(2, v.GetPeople())
	x.EqualValues(2, v.GetAssets())
	x.EqualValues(1, v.GetStocks())
	rows := v.GetRows()
	x.Len(rows, 3)
	x.Equal("NB-1", rows[0].GetAssetTag(), "the overdue loan first")
	x.Equal(kim.GetName(), rows[0].GetPartyName())
	x.Equal("loan", rows[0].GetKind())
	x.True(rows[0].GetOverdue())
	x.EqualValues(1, rows[0].GetOverdueDays(), "a day late in New York's days")
	x.False(rows[0].GetAcknowledged())
	x.Equal(lee.GetName(), rows[1].GetPartyName())
	x.False(rows[1].GetOverdue())
	held := map[string]*app.CustodyOutstandingRow{}
	for _, r := range rows[1:] {
		held[r.GetAssetTag()+r.GetStockName()] = r
	}
	x.EqualValues(1, held["NB-2"].GetQuantity())
	x.EqualValues(3, held["건전지"].GetQuantity())
	x.Equal("개", held["건전지"].GetUnit())

	// Asked for less, the summary is still all of it.
	v, err = e.app().Custody().Outstanding(e.owner, app.CustodyOutstandingRequest_builder{Overdue: true}.Build())
	x.NoError(err)
	x.EqualValues(1, v.GetTotal())
	x.Len(v.GetRows(), 1)
	x.EqualValues(2, v.GetCustodies())
	v, err = e.app().Custody().Outstanding(e.owner, app.CustodyOutstandingRequest_builder{Kind: "issue", Size: 1, Offset: 1}.Build())
	x.NoError(err)
	x.EqualValues(2, v.GetTotal(), "two lines of the issue")
	x.Len(v.GetRows(), 1, "a page of one, the second")
	_, err = e.app().Custody().Outstanding(e.owner, app.CustodyOutstandingRequest_builder{Kind: "gift"}.Build())
	x.Equal(codes.InvalidArgument, codeOf(err))

	// Later on the 21st it is still a day late; on the 23rd, three.
	e.now = at(21, 23, 30)
	v, err = e.app().Custody().Outstanding(e.owner, app.CustodyOutstandingRequest_builder{Overdue: true}.Build())
	x.NoError(err)
	x.EqualValues(1, v.GetRows()[0].GetOverdueDays())
	e.now = at(23, 0, 30)
	v, err = e.app().Custody().Outstanding(e.owner, app.CustodyOutstandingRequest_builder{Overdue: true}.Build())
	x.NoError(err)
	x.EqualValues(3, v.GetRows()[0].GetOverdueDays())

	t.Run("and writes all of it as a CSV, on New York's wall", func(t *testing.T) {
		x := require.New(t)
		f, err := e.app().Custody().ExportOutstanding(e.owner, app.CustodyOutstandingRequest_builder{Size: 1}.Build())
		x.NoError(err)
		x.Equal("rove-loans-20261023.csv", f.GetName())
		head, rows := written(t, f)
		x.Equal([]string{"받은 사람", "구분", "태그", "이름", "수량", "단위", "건넨 날", "반납 기한", "연체(일)", "인수 확인"}, head)
		x.Equal([]string{kim.GetName(), "대여", "NB-1", "노트북 1", "1", "", "2026-10-19 09:00", "2026-10-20 23:00", "3", "대기"}, rows[kim.GetName()])
		x.Equal(3, strings.Count(string(f.GetData()), "\n")-1, "every row, whatever the page")
		x.NotContains(string(f.GetData()), "@example.com", "nobody's address")
	})
}

// TestUsageIsTheTimeBookedAgainstTheTimeUsed: the reservations that began in
// a period, by the resource -- October in Seoul, which is the last day of
// September in UTC for its first nine hours -- the hours they booked, the
// hours they were used and the no-shows. A room is used from its check-in to
// its finish, early or at its end; what was picked up, from the pickup to its
// return, late or not yet; what was cancelled is in none of it.
func TestUsageIsTheTimeBookedAgainstTheTimeUsed(t *testing.T) {
	e := newEnv(t)
	x := e.x
	seoul := e.zoned("Asia/Seoul")
	at := func(month time.Month, d, h, m int) time.Time { return time.Date(2026, month, d, h, m, 0, 0, seoul) }

	e.now = at(9, 30, 20, 0)
	room := e.space("회의실", nil)
	e.bookable(room, nil)
	camera := e.item("카메라", "CM-1", nil, 0)
	e.bookable(camera, nil)
	_, kim := e.person("member")
	reserve := func(what *app.Asset, from, to time.Time) *app.Reservation {
		v, err := e.reserve(e.owner, what, from, to, nil)
		x.NoError(err)
		x.Equal("confirmed", v.GetStatus())
		return v
	}
	decide := func(r *app.Reservation) *app.ReservationDecideRequest {
		return app.ReservationDecideRequest_builder{Ref: ref[app.ReservationRef](r.GetId())}.Build()
	}
	pickUp := func(r *app.Reservation) *app.Custody {
		v, err := e.app().Custody().Add(e.owner, app.CustodyAddRequest_builder{
			Party:         ref[app.PartyRef](kim.GetId()),
			Lines:         []*app.CustodyLineSpec{app.CustodyLineSpec_builder{Asset: assetRef(camera)}.Build()},
			ReservationId: r.GetId(),
			DueAt:         r.GetEndsAt(),
		}.Build())
		x.NoError(err)
		return v
	}

	// Begun before October in Seoul, and in it: both on the 30th of
	// September in UTC.
	reserve(room, at(9, 30, 23, 30), at(10, 1, 0, 30))
	reserve(room, at(10, 1, 0, 30), at(10, 1, 1, 30))
	meeting := reserve(room, at(10, 1, 10, 0), at(10, 1, 12, 0))
	reserve(room, at(10, 2, 10, 0), at(10, 2, 11, 0))
	dropped := reserve(room, at(10, 4, 10, 0), at(10, 4, 11, 0))
	_, err := e.app().Reservation().Cancel(e.owner, decide(dropped))
	x.NoError(err)
	shoot := reserve(camera, at(10, 3, 9, 0), at(10, 3, 17, 0))
	out := reserve(camera, at(10, 5, 9, 0), at(10, 5, 10, 0))

	// Checked in at 10:05, finished at 11:30.
	e.now = at(10, 1, 10, 5)
	_, err = e.app().Reservation().CheckIn(e.owner, decide(meeting))
	x.NoError(err)
	e.now = at(10, 1, 11, 30)
	_, err = e.app().Reservation().Complete(e.owner, decide(meeting))
	x.NoError(err)
	// Nobody came to the others.
	e.now = at(10, 2, 10, 30)
	e.sweep()

	// Picked up at 9:10 and back at 18:30, late.
	e.now = at(10, 3, 9, 10)
	c := pickUp(shoot)
	e.now = at(10, 3, 18, 30)
	_, err = e.app().Custody().Return(e.owner, app.CustodyReturnRequest_builder{Ref: ref[app.CustodyRef](c.GetId())}.Build())
	x.NoError(err)
	// Picked up at 9:00, and still out at noon.
	e.now = at(10, 5, 9, 0)
	pickUp(out)
	e.now = at(10, 5, 12, 0)

	type used struct {
		reservations, used, noShows uint32
		booked, usedFor             uint64
	}
	read := func(req *app.BookableUsageRequest) (map[string]used, *app.BookableUsageResponse) {
		v, err := e.app().Bookable().Usage(e.owner, req)
		x.NoError(err)
		m := map[string]used{}
		for _, r := range v.GetRows() {
			m[r.GetName()] = used{r.GetReservations(), r.GetUsed(), r.GetNoShows(), r.GetBookedMinutes(), r.GetUsedMinutes()}
		}
		return m, v
	}
	october := app.BookableUsageRequest_builder{From: timestamppb.New(at(10, 1, 0, 0)), To: timestamppb.New(at(11, 1, 0, 0))}.Build()
	got, _ := read(october)
	x.Equal(used{3, 1, 2, 60 + 120 + 60, 85}, got["회의실"], "begun in October in Seoul, cancelled or not")
	x.Equal(used{2, 2, 0, 480 + 60, 560 + 180}, got["카메라"], "back late, and out until now")

	// Unsaid, the last thirty days today among them: September's too.
	got, v := read(&app.BookableUsageRequest{})
	x.EqualValues(4, got["회의실"].reservations)
	x.Equal(at(10, 6, 0, 0), v.GetTo().AsTime().In(seoul), "today ends as Seoul's does")
	x.Equal(at(9, 6, 0, 0), v.GetFrom().AsTime().In(seoul))

	_, err = e.app().Bookable().Usage(e.owner, app.BookableUsageRequest_builder{From: october.GetTo(), To: october.GetFrom()}.Build())
	x.Equal(codes.InvalidArgument, codeOf(err), "backwards")
	_, err = e.app().Bookable().Usage(e.owner, app.BookableUsageRequest_builder{From: timestamppb.New(at(1, 1, 0, 0)), To: timestamppb.New(at(10, 1, 0, 0).AddDate(1, 0, 0))}.Build())
	x.Equal(codes.InvalidArgument, codeOf(err), "longer than a year")

	t.Run("and writes it as a CSV, in hours", func(t *testing.T) {
		x := require.New(t)
		f, err := e.app().Bookable().ExportUsage(e.owner, october)
		x.NoError(err)
		x.Equal("rove-bookings-20261001-20261031.csv", f.GetName())
		head, rows := written(t, f)
		x.Equal([]string{"태그", "이름", "종류", "수량", "예약", "이용", "노쇼", "예약 시간", "이용 시간", "이용률(%)"}, head)
		x.Equal([]string{"CM-1", "카메라", "물품", "1", "2", "2", "0", "9.0", "12.3", "137"}, rows["CM-1"])
	})
}

// TestTheCountsSayWhatTheyFoundAndWhatWasLost: each count begun in a period,
// the newest first -- October in Seoul, a count begun in its first minutes
// among them and one a few minutes before not -- by what it found and how it
// was settled.
func TestTheCountsSayWhatTheyFoundAndWhatWasLost(t *testing.T) {
	e := newEnv(t)
	x := e.x
	seoul := e.zoned("Asia/Seoul")
	at := func(month time.Month, d, h, m int) time.Time { return time.Date(2026, month, d, h, m, 0, 0, seoul) }

	e.now = at(9, 30, 9, 0)
	store := e.space("창고", nil)
	desk := e.space("자리", nil)
	nb1 := e.item("노트북 1", "NB-1", store, 0)
	nb2 := e.item("노트북 2", "NB-2", store, 0)
	nb3 := e.item("노트북 3", "NB-3", store, 0)
	start := func(name string) *app.InventoryCount {
		v, err := e.app().InventoryCount().Add(e.owner, app.InventoryCountAddRequest_builder{Name: name, Scope: assetRef(store)}.Build())
		x.NoError(err)
		return v
	}
	scan := func(c *app.InventoryCount, edit func(*app.InventoryCountScanRequest)) *app.CountFinding {
		req := app.InventoryCountScanRequest_builder{Ref: ref[app.InventoryCountRef](c.GetId())}.Build()
		edit(req)
		v, err := e.app().InventoryCount().Scan(e.owner, req)
		x.NoError(err)
		return v
	}
	resolve := func(f *app.CountFinding, how string) {
		_, err := e.app().InventoryCount().Resolve(e.owner, app.InventoryCountResolveRequest_builder{
			Ref:        ref[app.CountFindingRef](f.GetId()),
			Resolution: how,
		}.Build())
		x.NoError(err)
	}

	e.now = at(9, 30, 23, 55)
	start("9월 실사")
	e.now = at(10, 1, 0, 5)
	c := start("10월 실사")
	scan(c, func(r *app.InventoryCountScanRequest) { r.SetAsset(assetRef(nb1)) })
	moved := scan(c, func(r *app.InventoryCountScanRequest) { r.SetAsset(assetRef(nb2)); r.SetAt(assetRef(desk)) })
	scan(c, func(r *app.InventoryCountScanRequest) { r.SetCode("없는-코드") })
	_, err := e.app().InventoryCount().Reconcile(e.owner, app.InventoryCountReconcileRequest_builder{Ref: ref[app.InventoryCountRef](c.GetId())}.Build())
	x.NoError(err)
	resolve(moved, "moved")
	fs, err := e.app().CountFinding().List(e.owner, app.CountFindingListRequest_builder{
		Filters: []*app.CountFindingFilter{app.CountFindingFilter_builder{Kind: z.Ptr("missing")}.Build()},
	}.Build())
	x.NoError(err)
	x.Len(fs.GetItems(), 1)
	x.Equal(nb3.GetId(), fs.GetItems()[0].GetAsset().GetId())
	resolve(fs.GetItems()[0], "lost")
	_, err = e.app().InventoryCount().Close(e.owner, app.InventoryCountCloseRequest_builder{Ref: ref[app.InventoryCountRef](c.GetId())}.Build())
	x.NoError(err)
	e.now = at(10, 2, 9, 0)
	start("빈 실사")

	october := app.InventoryCountResultsRequest_builder{From: timestamppb.New(at(10, 1, 0, 0)), To: timestamppb.New(at(11, 1, 0, 0))}.Build()
	v, err := e.app().InventoryCount().Results(e.owner, october)
	x.NoError(err)
	names := []string{}
	for _, r := range v.GetRows() {
		names = append(names, r.GetName())
	}
	x.Equal([]string{"빈 실사", "10월 실사"}, names, "the newest first, and September's not in October")
	r := v.GetRows()[1]
	x.Equal("창고", r.GetScopeName())
	x.Equal("closed", r.GetStatus())
	x.Equal([]uint32{1, 1, 1, 1}, []uint32{r.GetSeen(), r.GetMisplaced(), r.GetMissing(), r.GetUnknown()})
	x.Equal([]uint32{1, 1, 0, 1}, []uint32{r.GetMoved(), r.GetLost(), r.GetIgnored(), r.GetUnsettled()})
	empty := v.GetRows()[0]
	x.Zero(empty.GetSeen() + empty.GetMissing() + empty.GetUnsettled())

	// Unsaid, the last year, today among them.
	v, err = e.app().InventoryCount().Results(e.owner, &app.InventoryCountResultsRequest{})
	x.NoError(err)
	x.Len(v.GetRows(), 3)

	t.Run("and writes them as a CSV", func(t *testing.T) {
		x := require.New(t)
		f, err := e.app().InventoryCount().ExportResults(e.owner, october)
		x.NoError(err)
		x.Equal("rove-counts-20261001-20261031.csv", f.GetName())
		head, rows := written(t, f)
		x.Equal([]string{"실사", "범위", "상태", "시작", "종료", "확인", "위치 다름", "미발견", "미등록", "위치 반영", "분실 처리", "무시", "미처리"}, head)
		x.Equal([]string{"10월 실사", "창고", "종료", "2026-10-01 00:05", "2026-10-01 00:05", "1", "1", "1", "1", "1", "1", "0", "1"}, rows["10월 실사"])
	})
}

// TestTheStockSaysWhatCameInAndWentOut: each stock over a period -- the 1st
// of October in New York, which begins at four in the morning in UTC and ends
// at four the next -- what it had at each end, and what moved it in between,
// by what moved it.
func TestTheStockSaysWhatCameInAndWentOut(t *testing.T) {
	e := newEnv(t)
	x := e.x
	ny := e.zoned("America/New_York")
	at := func(month time.Month, d, h, m int) time.Time { return time.Date(2026, month, d, h, m, 0, 0, ny) }

	e.now = at(9, 30, 20, 0)
	store := e.space("창고", nil)
	shelf := e.space("선반", nil)
	_, kim := e.person("member")
	// 20 when it was registered, on the 1st in UTC and the 30th here.
	e.now = at(9, 30, 22, 0)
	batteries := e.stock(e.model("건전지"), store, 20, 0)
	change := func(do func(app.Server) func(*app.StockChangeRequest) error, q int64, when time.Time) {
		e.now = when
		x.NoError(do(e.app())(app.StockChangeRequest_builder{Ref: ref[app.StockRef](batteries.GetId()), Quantity: q}.Build()))
	}
	consume := func(s app.Server) func(*app.StockChangeRequest) error {
		return func(r *app.StockChangeRequest) error { _, err := s.Stock().Consume(e.owner, r); return err }
	}
	receive := func(s app.Server) func(*app.StockChangeRequest) error {
		return func(r *app.StockChangeRequest) error { _, err := s.Stock().Receive(e.owner, r); return err }
	}
	adjust := func(s app.Server) func(*app.StockChangeRequest) error {
		return func(r *app.StockChangeRequest) error { _, err := s.Stock().Adjust(e.owner, r); return err }
	}

	change(consume, 3, at(10, 1, 9, 0))
	change(receive, 5, at(10, 1, 12, 0))
	change(adjust, -1, at(10, 1, 13, 0))
	e.now = at(10, 1, 14, 0)
	c, err := e.app().Custody().Add(e.owner, app.CustodyAddRequest_builder{
		Party: ref[app.PartyRef](kim.GetId()),
		Lines: []*app.CustodyLineSpec{app.CustodyLineSpec_builder{Stock: ref[app.StockRef](batteries.GetId()), Quantity: 4}.Build()},
	}.Build())
	x.NoError(err)
	e.now = at(10, 1, 15, 0)
	lines, err := e.app().CustodyLine().List(e.owner, app.CustodyLineListRequest_builder{
		Filters: []*app.CustodyLineFilter{app.CustodyLineFilter_builder{Custody: ref[app.CustodyRef](c.GetId())}.Build()},
	}.Build())
	x.NoError(err)
	_, err = e.app().Custody().Return(e.owner, app.CustodyReturnRequest_builder{
		Ref:   ref[app.CustodyRef](c.GetId()),
		Lines: []*app.CustodyReturnLine{app.CustodyReturnLine_builder{Line: ref[app.CustodyLineRef](lines.GetItems()[0].GetId()), Quantity: 1}.Build()},
	}.Build())
	x.NoError(err)
	e.now = at(10, 1, 16, 0)
	_, err = e.app().Stock().Transfer(e.owner, app.StockTransferRequest_builder{Ref: ref[app.StockRef](batteries.GetId()), To: assetRef(shelf), Quantity: 2}.Build())
	x.NoError(err)
	// On the 2nd in UTC and still the 1st here; and then the 2nd here.
	change(consume, 2, at(10, 1, 22, 0))
	change(consume, 1, at(10, 2, 9, 0))
	x.EqualValues(20-3+5-1-4+1-2-2-1, e.quantity(batteries))

	day := app.StockFlowRequest_builder{From: timestamppb.New(at(10, 1, 0, 0)), To: timestamppb.New(at(10, 2, 0, 0))}.Build()
	v, err := e.app().Stock().Flow(e.owner, day)
	x.NoError(err)
	x.Len(v.GetRows(), 2, "the stock the transfer began on the shelf too")
	var r, there *app.StockFlowRow
	for _, v := range v.GetRows() {
		if v.GetSpaceName() == "창고" {
			r = v
		} else {
			there = v
		}
	}
	x.EqualValues(20, r.GetOpening())
	x.Equal([]int64{5, 1, 0, 0}, []int64{r.GetReceived(), r.GetReturned(), r.GetTransferredIn(), r.GetAdjustedUp()})
	x.Equal([]int64{3 + 2, 4, 2, 0, 1}, []int64{r.GetConsumed(), r.GetIssued(), r.GetTransferredOut(), r.GetConverted(), r.GetAdjustedDown()})
	x.EqualValues(6, r.GetCameIn())
	x.EqualValues(12, r.GetWentOut())
	x.EqualValues(14, r.GetClosing())
	x.EqualValues(0, there.GetOpening())
	x.EqualValues(2, there.GetTransferredIn())
	x.EqualValues(2, there.GetClosing())

	t.Run("and writes it as a CSV", func(t *testing.T) {
		x := require.New(t)
		f, err := e.app().Stock().ExportFlow(e.owner, day)
		x.NoError(err)
		x.Equal("rove-stock-20261001-20261001.csv", f.GetName())
		head, _ := written(t, f)
		x.Equal([]string{"재고", "위치", "단위", "기초", "입고", "반납", "이동 입고", "조정(+)", "들어옴", "사용", "지급", "이동 출고", "자산 전환", "조정(-)", "나감", "기말"}, head)
		x.Contains(string(f.GetData()), "건전지,창고,개,20,5,1,0,0,6,5,4,2,0,1,12,14\n")
	})
}
