package cli

import (
	"context"
	"fmt"
	"time"

	"github.com/lesomnus/xli"
	"github.com/lesomnus/z"
	"google.golang.org/protobuf/types/known/timestamppb"

	app "github.com/lesomnus/rove"
	"github.com/lesomnus/rove/server/domain"
)

// DemoPassword is what every person `init --demo` makes signs in with.
const DemoPassword = "demo1234"

// seedDemo fills a tenant the way an office of five teams would be: people,
// rooms, a hundred-odd assets with a past, hand-overs, bookings, stock and
// work. It goes through the domain layer as the owner, so it is exactly what
// doing it by hand would have written.
func seedDemo(ctx context.Context, s app.Server, out *xli.Command) error {
	d := &demo{ctx: ctx, s: s, types: map[string][]byte{}, models: map[string][]byte{}}
	if err := d.loadTypes(); err != nil {
		return err
	}

	steps := []struct {
		name string
		f    func() error
	}{
		{"people", d.people},
		{"spaces", d.spaces},
		{"models", d.catalog},
		{"assets", d.assets},
		{"hand-overs", d.handOver},
		{"bookings", d.bookings},
		{"stock", d.stock},
		{"work", d.work},
	}
	for _, st := range steps {
		if err := st.f(); err != nil {
			return fmt.Errorf("%s: %w", st.name, err)
		}
	}

	out.Printf("demo: %d people, %d assets; everybody signs in with %q, for example:\n", len(d.persons), d.count, DemoPassword)
	for _, p := range d.persons {
		if p.role != "member" {
			out.Printf("  %-16s %-8s %s\n", p.alias, p.role, p.team)
		}
	}
	out.Printf("  %-16s %-8s %s\n", d.persons[1].alias, d.persons[1].role, d.persons[1].team)
	return nil
}

type person struct {
	name, alias, email, team, role string
	id                             []byte
}

type demo struct {
	ctx context.Context
	s   app.Server

	types  map[string][]byte
	models map[string][]byte

	teams   map[string][]byte
	persons []*person
	space   map[string][]byte
	asset   map[string][]byte
	count   int
}

func (d *demo) loadTypes() error {
	vs, err := d.s.AssetType().List(d.ctx, app.AssetTypeListRequest_builder{Size: 100}.Build())
	if err != nil {
		return err
	}
	for _, v := range vs.GetItems() {
		d.types[v.GetName()] = v.GetId()
	}
	return nil
}

var (
	surnames = []struct{ ko, en string }{
		{"김", "kim"}, {"이", "lee"}, {"박", "park"}, {"최", "choi"}, {"정", "jung"},
		{"강", "kang"}, {"조", "cho"}, {"윤", "yoon"}, {"장", "jang"}, {"임", "lim"},
	}
	given = []struct{ ko, en string }{
		{"민준", "minjun"}, {"서연", "seoyeon"}, {"도윤", "doyun"}, {"지우", "jiwoo"}, {"하준", "hajun"},
		{"서윤", "seoyun"}, {"은우", "eunwoo"}, {"하은", "haeun"}, {"시우", "siwoo"}, {"지민", "jimin"},
		{"예준", "yejun"}, {"수아", "sua"}, {"주원", "juwon"}, {"지아", "jia"}, {"유준", "yujun"},
		{"채원", "chaewon"}, {"건우", "gunwoo"}, {"다은", "daeun"}, {"우진", "woojin"}, {"소윤", "soyun"},
		{"현우", "hyunwoo"}, {"예린", "yerin"}, {"준서", "junseo"}, {"나은", "naeun"}, {"선우", "sunwoo"},
	}
	teamNames = []string{"개발팀", "디자인팀", "운영팀", "영업팀", "경영지원팀"}
)

func (d *demo) people() error {
	org, err := d.s.Party().Add(d.ctx, app.PartyAddRequest_builder{Name: "본사", Kind: "org"}.Build())
	if err != nil {
		return err
	}
	d.teams = map[string][]byte{}
	for _, t := range teamNames {
		v, err := d.s.Party().Add(d.ctx, app.PartyAddRequest_builder{Name: t, Kind: "team", ParentId: org.GetId()}.Build())
		if err != nil {
			return err
		}
		d.teams[t] = v.GetId()
	}

	seen := map[string]int{}
	for i := range 50 {
		sn := surnames[i%len(surnames)]
		gn := given[(i*7+i/10)%len(given)]
		team := teamNames[i/10]
		alias := gn.en + "-" + sn.en
		if n := seen[alias]; n > 0 {
			alias = fmt.Sprintf("%s%d", alias, n+1)
		}
		seen[gn.en+"-"+sn.en]++

		role := "member"
		switch {
		case i%10 == 0:
			role = "manager"
		case i == 41:
			role = "admin"
		case i == 42:
			role = "auditor"
		}
		p := &person{
			name:  sn.ko + gn.ko,
			alias: alias,
			email: alias + "@example.com",
			team:  team,
			role:  role,
		}
		v, err := d.s.Party().Add(d.ctx, app.PartyAddRequest_builder{
			Name:     p.name,
			Kind:     "person",
			Email:    p.email,
			ParentId: d.teams[team],
			Code:     fmt.Sprintf("E%04d", 1001+i),
		}.Build())
		if err != nil {
			return err
		}
		p.id = v.GetId()
		if _, err := d.s.Party().Invite(d.ctx, app.PartyInviteRequest_builder{
			Ref:      app.PartyRef_builder{Id: p.id}.Build(),
			Alias:    alias,
			Role:     role,
			Password: DemoPassword,
		}.Build()); err != nil {
			return err
		}
		d.persons = append(d.persons, p)
	}
	return nil
}

func ago(days int) *timestamppb.Timestamp {
	return timestamppb.New(time.Now().AddDate(0, 0, -days))
}

func (d *demo) add(req *app.AssetAddRequest) ([]byte, error) {
	v, err := d.s.Asset().Add(d.ctx, req)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", req.GetName(), err)
	}
	d.count++
	return v.GetId(), nil
}

func (d *demo) ty(name string) *app.AssetTypeRef {
	return app.AssetTypeRef_builder{Id: d.types[name]}.Build()
}

func ref(id []byte) *app.AssetRef { return app.AssetRef_builder{Id: id}.Build() }

func (d *demo) spaces() error {
	d.space = map[string][]byte{}
	tree := []struct {
		key, name, ty, parent string
		attrs                 map[string]string
	}{
		{"hq", "본사", "건물", "", nil},
		{"3f", "3층", "층", "hq", nil},
		{"4f", "4층", "층", "hq", nil},
		{"room-a", "회의실 A", "회의실", "3f", map[string]string{"capacity": "8", "display": "true"}},
		{"room-b", "회의실 B", "회의실", "3f", map[string]string{"capacity": "4", "display": "false"}},
		{"개발팀", "개발팀 사무실", "사무실", "3f", map[string]string{"seats": "12"}},
		{"디자인팀", "디자인팀 사무실", "사무실", "3f", map[string]string{"seats": "12"}},
		{"hall", "대회의실", "회의실", "4f", map[string]string{"capacity": "20", "display": "true"}},
		{"운영팀", "운영팀 사무실", "사무실", "4f", map[string]string{"seats": "12"}},
		{"영업팀", "영업팀 사무실", "사무실", "4f", map[string]string{"seats": "12"}},
		{"경영지원팀", "경영지원팀 사무실", "사무실", "4f", map[string]string{"seats": "12"}},
		{"store", "창고", "창고", "4f", nil},
		{"srv", "서버실", "서버실", "4f", nil},
	}
	for i, sp := range tree {
		req := app.AssetAddRequest_builder{
			Name:       sp.name,
			Kind:       "space",
			Type:       d.ty(sp.ty),
			Tag:        fmt.Sprintf("SP-%02d", i+1),
			Attributes: sp.attrs,
			Since:      ago(900),
		}.Build()
		if sp.parent != "" {
			req.SetTo(ref(d.space[sp.parent]))
		}
		id, err := d.add(req)
		if err != nil {
			return err
		}
		d.space[sp.key] = id
	}
	return nil
}

func (d *demo) catalog() error {
	models := []struct{ key, name, maker, ty string }{
		{"mbp", "MacBook Pro 14", "Apple", "노트북"},
		{"x1", "ThinkPad X1 Carbon", "Lenovo", "노트북"},
		{"u27", "U2723QE", "Dell", "모니터"},
		{"lg27", "27UP850", "LG", "모니터"},
		{"iphone", "iPhone 15", "Apple", "휴대폰"},
		{"galaxy", "Galaxy S24", "Samsung", "휴대폰"},
		{"a7", "α7 IV", "Sony", "촬영 장비"},
		{"gm", "FE 24-70mm F2.8 GM II", "Sony", "촬영 장비"},
		{"tripod", "190 알루미늄 삼각대", "Manfrotto", "촬영 장비"},
		{"projector", "EB-FH52", "Epson", "주변기기"},
		{"ap", "MR46", "Cisco Meraki", "네트워크 장비"},
		{"paper", "A4 복사용지 (2500매)", "", "소모품"},
		{"battery", "AA 건전지", "", "소모품"},
		{"mouse", "M650 마우스", "Logitech", "주변기기"},
		{"r660", "PowerEdge R660", "Dell", "서버"},
		{"switch", "Catalyst 9300", "Cisco", "네트워크 장비"},
	}
	for _, m := range models {
		v, err := d.s.ItemModel().Add(d.ctx, app.ItemModelAddRequest_builder{
			Name:  m.name,
			Maker: m.maker,
			Type:  d.ty(m.ty),
		}.Build())
		if err != nil {
			return err
		}
		d.models[m.key] = v.GetId()
	}
	v, err := d.s.ItemModel().Add(d.ctx, app.ItemModelAddRequest_builder{
		Name:  "42U 서버 랙",
		Maker: "APC",
		Type:  d.ty("랙"),
		Spec:  app.ModelSpec_builder{RackUnits: 42}.Build(),
	}.Build())
	if err != nil {
		return err
	}
	d.models["rack"] = v.GetId()
	return nil
}

func (d *demo) model(key string) *app.ItemModelRef {
	return app.ItemModelRef_builder{Id: d.models[key]}.Build()
}

func (d *demo) assets() error {
	d.asset = map[string][]byte{}
	for i, p := range d.persons {
		// Bought in batches over two years, kept in the store room for a few
		// days, then taken to the team's office.
		bought := 700 - (i/5)*60
		model, ty, attrs := "x1", "노트북", map[string]string{"cpu": "Intel Core Ultra 7", "ram": "32", "disk": "1024", "os": "Windows"}
		if p.team == "개발팀" || p.team == "디자인팀" {
			model, attrs = "mbp", map[string]string{"cpu": "Apple M3 Pro", "ram": "36", "disk": "1024", "os": "macOS"}
		}
		// Three years from when it was bought.
		attrs["warranty_until"] = time.Now().AddDate(0, 0, -bought).AddDate(3, 0, 0).Format(time.DateOnly)
		nb, err := d.add(app.AssetAddRequest_builder{
			Name:       fmt.Sprintf("%s 노트북", p.name),
			Type:       d.ty(ty),
			Model:      d.model(model),
			Tag:        fmt.Sprintf("NB-%03d", i+1),
			Serial:     fmt.Sprintf("SN%08d", 31000000+i*37),
			Attributes: attrs,
			AcquiredAt: ago(bought),
			Since:      ago(bought),
			To:         ref(d.space["store"]),
		}.Build())
		if err != nil {
			return err
		}
		mn, err := d.add(app.AssetAddRequest_builder{
			Name:       fmt.Sprintf("%s 모니터", p.name),
			Type:       d.ty("모니터"),
			Model:      d.model(map[bool]string{true: "u27", false: "lg27"}[i%2 == 0]),
			Tag:        fmt.Sprintf("MN-%03d", i+1),
			Serial:     fmt.Sprintf("MN%08d", 52000000+i*13),
			Attributes: map[string]string{"size": "27", "resolution": "3840x2160", "warranty_until": time.Now().AddDate(0, 0, -bought).AddDate(2, 0, 0).Format(time.DateOnly)},
			AcquiredAt: ago(bought),
			Since:      ago(bought),
			To:         ref(d.space["store"]),
		}.Build())
		if err != nil {
			return err
		}
		for _, id := range [][]byte{nb, mn} {
			if _, err := d.s.Asset().Move(d.ctx, app.AssetMoveRequest_builder{
				Ref:    ref(id),
				To:     ref(d.space[p.team]),
				At:     ago(bought - 3),
				Reason: "자리 배치",
			}.Build()); err != nil {
				return err
			}
		}
		d.asset[fmt.Sprintf("nb-%d", i)] = nb
		d.asset[fmt.Sprintf("mn-%d", i)] = mn
	}

	// Spares in the store room.
	for i := range 5 {
		if _, err := d.add(app.AssetAddRequest_builder{
			Name:       "예비 노트북",
			Type:       d.ty("노트북"),
			Model:      d.model("x1"),
			Tag:        fmt.Sprintf("NB-%03d", 51+i),
			Serial:     fmt.Sprintf("SN%08d", 39000000+i),
			Attributes: map[string]string{"cpu": "Intel Core Ultra 7", "ram": "16", "disk": "512", "os": "Windows"},
			AcquiredAt: ago(120),
			Since:      ago(120),
			To:         ref(d.space["store"]),
		}.Build()); err != nil {
			return err
		}
	}

	// Phones, lent rather than issued.
	for i := range 10 {
		model, os := "galaxy", "Android"
		if i%2 == 0 {
			model, os = "iphone", "iOS"
		}
		id, err := d.add(app.AssetAddRequest_builder{
			Name:       fmt.Sprintf("업무폰 %d", i+1),
			Type:       d.ty("휴대폰"),
			Model:      d.model(model),
			Tag:        fmt.Sprintf("PH-%03d", i+1),
			Attributes: map[string]string{"os": os, "number": fmt.Sprintf("010-5%03d-%04d", i, 1000+i*7)},
			AcquiredAt: ago(300),
			Since:      ago(300),
			To:         ref(d.space["store"]),
		}.Build())
		if err != nil {
			return err
		}
		d.asset[fmt.Sprintf("ph-%d", i)] = id
	}

	// A camera kit, booked as one thing.
	kit, err := d.add(app.AssetAddRequest_builder{
		Name:  "촬영 키트",
		Kind:  "kit",
		Type:  d.ty("키트"),
		Tag:   "KT-001",
		Since: ago(200),
		To:    ref(d.space["store"]),
	}.Build())
	if err != nil {
		return err
	}
	d.asset["kit"] = kit
	for _, m := range []struct{ tag, name, model string }{
		{"CM-001", "촬영용 카메라", "a7"},
		{"LN-001", "표준 줌 렌즈", "gm"},
		{"TR-001", "삼각대", "tripod"},
	} {
		id, err := d.add(app.AssetAddRequest_builder{
			Name:       m.name,
			Type:       d.ty("촬영 장비"),
			Model:      d.model(m.model),
			Tag:        m.tag,
			AcquiredAt: ago(200),
			Since:      ago(200),
			To:         ref(kit),
			Mode:       z.Ptr("part"),
		}.Build())
		if err != nil {
			return err
		}
		if _, err := d.s.Asset().Relate(d.ctx, app.AssetRelateRequest_builder{
			Ref:      ref(id),
			Target:   ref(kit),
			Kind:     "member_of",
			Required: true,
			At:       ago(200),
		}.Build()); err != nil {
			return err
		}
	}

	for i := range 2 {
		id, err := d.add(app.AssetAddRequest_builder{
			Name:       fmt.Sprintf("빔프로젝터 %d", i+1),
			Type:       d.ty("주변기기"),
			Model:      d.model("projector"),
			Tag:        fmt.Sprintf("PJ-%03d", i+1),
			AcquiredAt: ago(500),
			Since:      ago(500),
			To:         ref(d.space["store"]),
		}.Build())
		if err != nil {
			return err
		}
		d.asset[fmt.Sprintf("pj-%d", i)] = id
	}
	for i, at := range []string{"3f", "3f", "4f", "4f"} {
		id, err := d.add(app.AssetAddRequest_builder{
			Name:       fmt.Sprintf("무선 AP %d", i+1),
			Type:       d.ty("네트워크 장비"),
			Model:      d.model("ap"),
			Tag:        fmt.Sprintf("AP-%03d", i+1),
			Attributes: map[string]string{"ip": fmt.Sprintf("10.0.%d.%d", 3+i/2, 10+i), "mac": fmt.Sprintf("0c:8d:db:00:00:%02x", i+1)},
			AcquiredAt: ago(800),
			Since:      ago(800),
			To:         ref(d.space[at]),
			Mode:       z.Ptr("installed"),
		}.Build())
		if err != nil {
			return err
		}
		d.asset[fmt.Sprintf("ap-%d", i)] = id
	}

	// A rack in the server room, with what is mounted in it.
	rk, err := d.add(app.AssetAddRequest_builder{
		Name:  "서버 랙 A",
		Type:  d.ty("랙"),
		Model: d.model("rack"),
		Tag:   "RK-001",
		Since: ago(600),
		To:    ref(d.space["srv"]),
	}.Build())
	if err != nil {
		return err
	}
	for _, sv := range []struct {
		tag, name, model string
		from, to         int32
		attrs            map[string]string
	}{
		{"SV-001", "웹 서버 1", "r660", 10, 10, map[string]string{"ip": "10.0.10.11", "cpu": "Xeon Silver 4510", "ram": "128"}},
		{"SV-002", "웹 서버 2", "r660", 11, 11, map[string]string{"ip": "10.0.10.12", "cpu": "Xeon Silver 4510", "ram": "128"}},
		{"SV-003", "DB 서버", "r660", 14, 15, map[string]string{"ip": "10.0.10.20", "cpu": "Xeon Gold 6526Y", "ram": "512"}},
		{"NW-001", "코어 스위치", "switch", 40, 40, nil},
	} {
		ty := "서버"
		if sv.model == "switch" {
			ty = "네트워크 장비"
		}
		id, err := d.add(app.AssetAddRequest_builder{
			Name:       sv.name,
			Type:       d.ty(ty),
			Model:      d.model(sv.model),
			Tag:        sv.tag,
			Attributes: sv.attrs,
			AcquiredAt: ago(600),
			Since:      ago(600),
		}.Build())
		if err != nil {
			return err
		}
		if _, err := d.s.Asset().Move(d.ctx, app.AssetMoveRequest_builder{
			Ref:    ref(id),
			To:     ref(rk),
			Mode:   "installed",
			UFrom:  sv.from,
			UTo:    sv.to,
			At:     ago(598),
			Reason: "랙 설치",
		}.Build()); err != nil {
			return err
		}
	}
	return nil
}

func (d *demo) handOver() error {
	for i, p := range d.persons {
		if _, err := d.s.Custody().Add(d.ctx, app.CustodyAddRequest_builder{
			Party: app.PartyRef_builder{Id: p.id}.Build(),
			Kind:  "issue",
			Desc:  "입사 지급",
			Lines: []*app.CustodyLineSpec{
				app.CustodyLineSpec_builder{Asset: ref(d.asset[fmt.Sprintf("nb-%d", i)])}.Build(),
				app.CustodyLineSpec_builder{Asset: ref(d.asset[fmt.Sprintf("mn-%d", i)])}.Build(),
			},
		}.Build()); err != nil {
			return err
		}
	}

	// Two phones out on loan, one of them due tomorrow.
	for i, days := range []int{1, 14} {
		p := d.persons[30+i]
		if _, err := d.s.Custody().Add(d.ctx, app.CustodyAddRequest_builder{
			Party: app.PartyRef_builder{Id: p.id}.Build(),
			Kind:  "loan",
			Desc:  "출장용 대여",
			DueAt: timestamppb.New(time.Now().AddDate(0, 0, days)),
			Lines: []*app.CustodyLineSpec{
				app.CustodyLineSpec_builder{Asset: ref(d.asset[fmt.Sprintf("ph-%d", i)])}.Build(),
			},
		}.Build()); err != nil {
			return err
		}
	}
	return nil
}

func (d *demo) bookings() error {
	weekdays := &app.OpeningHours{}
	for wd := int32(1); wd <= 5; wd++ {
		weekdays.SetRanges(append(weekdays.GetRanges(), app.OpeningRange_builder{Weekday: wd, FromMinute: 8 * 60, ToMinute: 20 * 60}.Build()))
	}
	books := []struct {
		key      string
		approval bool
		hours    bool
	}{
		{"room-a", false, true},
		{"room-b", false, true},
		{"hall", true, true},
		{"kit", false, false},
		{"pj-0", false, false},
	}
	for _, b := range books {
		id := d.space[b.key]
		if id == nil {
			id = d.asset[b.key]
		}
		req := app.BookableAddRequest_builder{
			Asset:       ref(id),
			Approval:    b.approval,
			BufferAfter: 10,
			MaxMinutes:  8 * 60,
			HorizonDays: 90,
		}.Build()
		if b.hours {
			req.SetHours(weekdays)
			req.SetBufferAfter(0)
		}
		if b.key == "kit" || b.key == "pj-0" {
			req.SetMaxMinutes(7 * 24 * 60)
		}
		if _, err := d.s.Bookable().Add(d.ctx, req); err != nil {
			return err
		}
	}

	next := func(wd time.Weekday, h int) time.Time {
		t := time.Now().In(domain.Zone)
		t = time.Date(t.Year(), t.Month(), t.Day(), h, 0, 0, 0, domain.Zone).AddDate(0, 0, 1)
		for t.Weekday() != wd {
			t = t.AddDate(0, 0, 1)
		}
		return t
	}
	rs := []struct {
		name, key string
		who       int
		from      time.Time
		hours     int
		repeat    string
	}{
		{"개발팀 주간 회의", "room-a", 0, next(time.Monday, 10), 1, "FREQ=WEEKLY;COUNT=8"},
		{"디자인 리뷰", "room-b", 10, next(time.Tuesday, 14), 2, ""},
		{"전사 타운홀", "hall", 40, next(time.Thursday, 16), 1, ""},
		{"제품 촬영", "kit", 11, next(time.Wednesday, 9), 8, ""},
	}
	for _, r := range rs {
		id := d.space[r.key]
		if id == nil {
			id = d.asset[r.key]
		}
		if _, err := d.s.Reservation().Add(d.ctx, app.ReservationAddRequest_builder{
			Name:     r.name,
			Party:    app.PartyRef_builder{Id: d.persons[r.who].id}.Build(),
			BeginsAt: timestamppb.New(r.from),
			EndsAt:   timestamppb.New(r.from.Add(time.Duration(r.hours) * time.Hour)),
			Items:    []*app.ReservationItemSpec{app.ReservationItemSpec_builder{Resource: ref(id)}.Build()},
			Repeat:   z.Ptr(r.repeat),
		}.Build()); err != nil {
			return fmt.Errorf("%s: %w", r.name, err)
		}
	}
	return nil
}

func (d *demo) stock() error {
	for _, st := range []struct {
		model, unit       string
		quantity, minimum int64
	}{
		{"paper", "박스", 20, 5},
		{"battery", "개", 48, 12},
		{"mouse", "개", 6, 2},
	} {
		if _, err := d.s.Stock().Add(d.ctx, app.StockAddRequest_builder{
			Model:     d.model(st.model),
			Space:     ref(d.space["store"]),
			Quantity:  st.quantity,
			Threshold: st.minimum,
			Unit:      st.unit,
		}.Build()); err != nil {
			return err
		}
	}
	return nil
}

func (d *demo) work() error {
	if _, err := d.s.WorkOrder().Add(d.ctx, app.WorkOrderAddRequest_builder{
		Name:  "배터리 부풀음 — 배터리 교체",
		Desc:  "충전 시 하판이 들뜸. 서비스센터 접수 필요.",
		Asset: ref(d.asset["nb-12"]),
		Kind:  "repair",
	}.Build()); err != nil {
		return err
	}
	for i := range 4 {
		begins := time.Now().AddDate(0, 0, 7).Truncate(time.Hour)
		if _, err := d.s.WorkOrder().Add(d.ctx, app.WorkOrderAddRequest_builder{
			Name:      "무선 AP 정기 점검",
			Asset:     ref(d.asset[fmt.Sprintf("ap-%d", i)]),
			Kind:      "inspection",
			BeginsAt:  timestamppb.New(begins),
			EndsAt:    timestamppb.New(begins.Add(time.Hour)),
			EveryDays: 90,
		}.Build()); err != nil {
			return err
		}
	}
	_, err := d.s.Purchase().Add(d.ctx, app.PurchaseAddRequest_builder{
		Name:      "모니터 추가 구매",
		Reference: "PO-2026-0412",
		Lines: []*app.PurchaseLineSpec{
			app.PurchaseLineSpec_builder{Model: d.model("lg27"), Quantity: 5, UnitCost: 520000, ReceiveAs: "asset"}.Build(),
			app.PurchaseLineSpec_builder{Model: d.model("mouse"), Quantity: 10, UnitCost: 59000, ReceiveAs: "stock"}.Build(),
		},
	}.Build())
	return err
}
