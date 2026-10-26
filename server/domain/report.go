package domain

import (
	"context"
	stdsql "database/sql"
	"fmt"
	"math"
	"slices"
	"strconv"
	"strings"
	"time"
	"uuid"

	"github.com/lesomnus/payday/pdid"
	"github.com/protobuf-orm/ent/dialect/sql"
	"google.golang.org/protobuf/types/known/timestamppb"

	app "github.com/lesomnus/rove"
	"github.com/lesomnus/rove/internal/ent"
	"github.com/lesomnus/rove/internal/ent/asset"
	"github.com/lesomnus/rove/internal/ent/assettype"
	"github.com/lesomnus/rove/internal/ent/bookable"
	"github.com/lesomnus/rove/internal/ent/countfinding"
	"github.com/lesomnus/rove/internal/ent/inventorycount"
	"github.com/lesomnus/rove/internal/ent/stock"
)

// The reports (design 9.14) are what the people who run the register read of
// it, added up: what there is, what is out, how what may be booked was used,
// what the counts found, and how the stock moved. The database adds each one
// up, so that what a report reads is about the size of what it answers rather
// than of the rows it is made of -- a census of a hundred thousand assets is a
// few hundred rows -- and every statement is narrowed to the caller's tenant
// by hand, as the domain's own reads are.
//
// A report writes nothing: no event, and no row of the trail. The one that
// names people -- who has what is out -- puts them on the access record of its
// call by the names it says ([namer]), and its CSV is a download of them
// (`cmd.Downloads`). The others name nobody, and their CSVs are no download of
// anybody (rove#136). None says anybody's address or number.

// reportDays is the longest a report's period is: a year of days, a leap
// year's among them.
const reportDays = 366

// given says whether a request said a moment: a timestamp of none reads as
// the zero one, as [Tx.at] reads it.
func given(v *timestamppb.Timestamp) bool {
	return v != nil && (v.GetSeconds() != 0 || v.GetNanos() != 0)
}

// period is the span a report reads, from `from` and before `to`: as the
// request says it, or -- what it leaves unsaid -- ending as today does on the
// workspace's wall, and beginning where `back` of that end says. Never
// backwards, and never longer than a year.
func (t *Tx) period(from, to *timestamppb.Timestamp, back func(time.Time) time.Time) (time.Time, time.Time, error) {
	z := t.zone()
	var f, e time.Time
	if given(to) {
		e = moment(to.AsTime())
	} else {
		n := t.now.In(z)
		e = moment(time.Date(n.Year(), n.Month(), n.Day()+1, 0, 0, 0, 0, z))
	}
	if given(from) {
		f = moment(from.AsTime())
	} else {
		f = moment(back(e.In(z)))
	}
	if !f.Before(e) {
		return time.Time{}, time.Time{}, invalid("to", "기간의 끝은 시작보다 뒤여야 합니다")
	}
	if e.After(f.In(z).AddDate(0, 0, reportDays)) {
		return time.Time{}, time.Time{}, invalid("from", "기간은 1년 안에서 정합니다")
	}
	return f, e, nil
}

// lastDays is the `n` days before the end of a period: the default of a report
// of what happened lately.
func lastDays(n int) func(time.Time) time.Time {
	return func(end time.Time) time.Time { return end.AddDate(0, 0, -n) }
}

// daysLate is how many of the workspace's days `due` is behind now: one due
// yesterday is a day late, and one due earlier today none, late as it is.
func (t *Tx) daysLate(due time.Time) uint32 {
	z := t.zone()
	a, b := due.In(z), t.now.In(z)
	d := time.Date(b.Year(), b.Month(), b.Day(), 0, 0, 0, 0, time.UTC).
		Sub(time.Date(a.Year(), a.Month(), a.Day(), 0, 0, 0, 0, time.UTC))
	return uint32(max(0, int(d.Hours()/24)))
}

// day is a moment's day on the workspace's wall, as a file's name says it;
// and span a period's, its first day and its last.
func (t *Tx) day(at time.Time) string { return at.In(t.zone()).Format("20060102") }
func (t *Tx) span(from, to time.Time) string {
	return t.day(from) + "-" + t.day(to.Add(-time.Nanosecond))
}

// cell is a moment as a CSV of a report writes it, on the workspace's wall,
// and nothing for none.
func (t *Tx) cell(v *timestamppb.Timestamp) string {
	if !given(v) {
		return ""
	}
	return v.AsTime().In(t.zone()).Format("2006-01-02 15:04")
}

// number and hours are counts and minutes as a CSV writes them: hours to a
// tenth.
func number[N uint32 | int64 | uint64](v N) string { return strconv.FormatInt(int64(v), 10) }
func hours(minutes uint64) string {
	return strconv.FormatFloat(float64(minutes)/60, 'f', 1, 64)
}

// q is a statement in the dialect of the database it is put to: what it is
// written with S is SQL, and with Arg a parameter, numbered as the dialect
// numbers them.
func (t *Tx) q() *sql.Builder {
	b := &sql.Builder{}
	b.SetDialect(t.drv.Dialect())
	return b
}

// rows runs what `b` built and reads what it answers into `into`, a slice of
// structs whose fields' `sql` tags name its columns.
func (t *Tx) rows(b *sql.Builder, into any) error {
	q, args := b.Query()
	rs := &sql.Rows{}
	if err := t.drv.Query(t.ctx, q, args, rs); err != nil {
		return err
	}
	defer rs.Close()
	return sql.ScanSlice(rs, into)
}

// seconds is SQL for a moment the database keeps, as seconds since 1970:
// PostgreSQL says one by its epoch, and SQLite keeps it as the nanoseconds
// since then already (payday's `_timefmt=unixepoch_nano`). NULL for none. The
// difference of two is how long it was between them, said alike by both.
func (t *Tx) seconds(col string) string {
	if t.postgres() {
		return "CAST(EXTRACT(EPOCH FROM " + col + ") AS DOUBLE PRECISION)"
	}
	return "(" + col + " / 1000000000.0)"
}

// least and greatest are the names of the smaller and the larger of values
// none of which is NULL, as each database calls them.
func (t *Tx) least() string {
	if t.postgres() {
		return "LEAST"
	}
	return "MIN"
}

func (t *Tx) greatest() string {
	if t.postgres() {
		return "GREATEST"
	}
	return "MAX"
}

// assetsNamed is the assets `ids` names, as a report says them -- their tag,
// name and kind, and what each is in -- read a few hundred to a statement off
// the caller's tenant. An asset is nobody's, so saying one shows nobody.
func (t *Tx) assetsNamed(ids []uuid.UUID) (map[uuid.UUID]*ent.Asset, error) {
	out := map[uuid.UUID]*ent.Asset{}
	for some := range slices.Chunk(slices.Compact(sortedIds(ids)), 500) {
		vs, err := t.db.Asset.Query().
			Where(asset.TenantId(t.tenant.Uuid()), asset.IdIn(some...)).
			Select(asset.FieldId, asset.FieldTag, asset.FieldName, asset.FieldKind, asset.FieldParentId).
			All(t.ctx)
		if err != nil {
			return nil, err
		}
		for _, v := range vs {
			out[v.Id] = v
		}
	}
	return out, nil
}

func sortedIds(ids []uuid.UUID) []uuid.UUID {
	vs := slices.DeleteFunc(slices.Clone(ids), func(v uuid.UUID) bool { return v == (uuid.UUID{}) })
	slices.SortFunc(vs, func(a, b uuid.UUID) int { return a.Compare(b) })
	return vs
}

// --- 자산 현황 ---

// Census counts the register as it is now.
func (s domainAsset) Census(ctx context.Context, req *app.AssetCensusRequest) (*app.AssetCensusResponse, error) {
	t, err := s.read(ctx)
	if err != nil {
		return nil, err
	}
	return t.census(req)
}

// ExportCensus writes the census as a CSV: a row each type or space, its total
// and how many of each status, and the total of all of them last.
func (s domainAsset) ExportCensus(ctx context.Context, req *app.AssetCensusRequest) (*app.AssetExportResponse, error) {
	t, err := s.read(ctx)
	if err != nil {
		return nil, err
	}
	v, err := t.census(req)
	if err != nil {
		return nil, err
	}
	places := v.GetBy() == "location"
	head := []string{"유형", "합계"}
	if places {
		head = []string{"위치", "태그", "합계", "하위 포함"}
	}
	for _, st := range Statuses {
		head = append(head, statusSay[st])
	}
	rows := [][]string{head}
	line := func(name string, r *app.AssetCensusRow) {
		row := []string{name, number(r.GetTotal())}
		if places {
			row = []string{name, r.GetTag(), number(r.GetTotal()), number(r.GetWithin())}
		}
		for _, st := range Statuses {
			row = append(row, number(r.GetStatuses()[st]))
		}
		rows = append(rows, row)
	}
	for _, r := range v.GetRows() {
		line(strings.Join(append(slices.Clone(r.GetPath()), r.GetName()), " / "), r)
	}
	line("합계", v.GetTotal())
	return writeCSV(fmt.Sprintf("rove-census-%s-%s", v.GetKind(), t.day(t.now)), rows)
}

// census is how many assets of a kind there are now, by type or by location,
// in each status. The database counts them by the column and the status; what
// it answers is a row for each pair there is. By location it says with each
// what holds them -- whether it is a space, and what it is in -- which is a
// few thousand rows to read of a hundred thousand counted.
func (t *Tx) census(req *app.AssetCensusRequest) (*app.AssetCensusResponse, error) {
	by, kind := or(req.GetBy(), "type"), or(req.GetKind(), "item")
	if by != "type" && by != "location" {
		return nil, invalid("by", "유형별(type)이나 위치별(location)로 셉니다")
	}
	if !slices.Contains(Kinds, kind) {
		return nil, invalid("kind", "종류는 물품, 공간, 키트, 그룹 가운데 하나입니다")
	}
	tid := t.tenant.Uuid()

	b := t.q()
	if by == "type" {
		b.S(`SELECT "type_id" AS "key", "status", COUNT(*) AS "n" FROM "asset" WHERE "tenant_id" = `).Arg(tid).
			S(` AND "kind" = `).Arg(kind).
			S(` AND "date_erased" IS NULL GROUP BY "type_id", "status"`)
	} else {
		b.S(`SELECT g."key", p."kind" AS "holder", p."parent_id" AS "up", g."status", g."n"` +
			` FROM (SELECT "parent_id" AS "key", "status", COUNT(*) AS "n" FROM "asset" WHERE "tenant_id" = `).Arg(tid).
			S(` AND "kind" = `).Arg(kind).
			S(` AND "date_erased" IS NULL GROUP BY "parent_id", "status") g` +
				` LEFT JOIN "asset" p ON p."id" = g."key" AND p."tenant_id" = `).Arg(tid)
	}
	var ns []struct {
		Key    stdsql.Null[uuid.UUID] `sql:"key"`
		Holder stdsql.NullString      `sql:"holder"`
		Up     stdsql.Null[uuid.UUID] `sql:"up"`
		Status string                 `sql:"status"`
		N      int64                  `sql:"n"`
	}
	if err := t.rows(b, &ns); err != nil {
		return nil, err
	}
	counts := map[uuid.UUID]map[string]uint32{}
	holders := map[uuid.UUID]keeper{}
	total := app.AssetCensusRow_builder{Name: "합계", Statuses: map[string]uint32{}}.Build()
	for _, v := range ns {
		k := uuid.UUID{}
		if v.Key.Valid {
			k = v.Key.V
			h := keeper{space: v.Holder.String == "space"}
			if v.Up.Valid {
				h.up = &v.Up.V
			}
			holders[k] = h
		}
		if counts[k] == nil {
			counts[k] = map[string]uint32{}
		}
		counts[k][v.Status] += uint32(v.N)
		total.GetStatuses()[v.Status] += uint32(v.N)
		total.SetTotal(total.GetTotal() + uint32(v.N))
	}
	total.SetWithin(total.GetTotal())

	var rows []*app.AssetCensusRow
	var err error
	if by == "type" {
		rows, err = t.byType(counts)
	} else {
		rows, err = t.byLocation(counts, holders)
	}
	if err != nil {
		return nil, err
	}
	return app.AssetCensusResponse_builder{Rows: rows, Total: total, At: ts(t.now), By: by, Kind: kind}.Build(), nil
}

// keeper is what holds something counted, as the census by location reads it
// beside the count: whether it is a space, and what it is in -- nothing for
// one it did not find, which is not the tenant's, or gone.
type keeper struct {
	space bool
	up    *uuid.UUID
}

func censusRow(id uuid.UUID, statuses map[string]uint32) *app.AssetCensusRow {
	n := uint32(0)
	for _, v := range statuses {
		n += v
	}
	r := app.AssetCensusRow_builder{Statuses: statuses, Total: n}.Build()
	if id != (uuid.UUID{}) {
		r.SetId(pdid.Id(id).Bytes())
	}
	return r
}

// byType is a row each type, by its name, and one of what has none last.
func (t *Tx) byType(counts map[uuid.UUID]map[string]uint32) ([]*app.AssetCensusRow, error) {
	ts, err := t.db.AssetType.Query().
		Where(assettype.TenantId(t.tenant.Uuid())).
		Select(assettype.FieldId, assettype.FieldName).
		All(t.ctx)
	if err != nil {
		return nil, err
	}
	names := map[uuid.UUID]string{}
	for _, v := range ts {
		names[v.Id] = v.Name
	}
	rows := make([]*app.AssetCensusRow, 0, len(counts))
	for k, c := range counts {
		r := censusRow(k, c)
		switch {
		case k == (uuid.UUID{}):
			r.SetName("(유형 없음)")
		case names[k] != "":
			r.SetName(names[k])
		default:
			r.SetName("?")
		}
		rows = append(rows, r)
	}
	slices.SortFunc(rows, func(a, b *app.AssetCensusRow) int {
		if d := compareNone(a, b); d != 0 {
			return d
		}
		if d := strings.Compare(a.GetName(), b.GetName()); d != 0 {
			return d
		}
		return strings.Compare(string(a.GetId()), string(b.GetId()))
	})
	return rows, nil
}

// compareNone puts the row of what has no type or no space after the rest.
func compareNone(a, b *app.AssetCensusRow) int {
	switch {
	case len(a.GetId()) == 0 && len(b.GetId()) > 0:
		return 1
	case len(a.GetId()) > 0 && len(b.GetId()) == 0:
		return -1
	}
	return 0
}

// byLocation is a row each space, as the tree reads: what is in it, counted in
// the nearest space up from whatever holds it -- a part in a computer in the
// computer's room -- and, beside, everything under it. What is in no space
// is a row of its own, last.
func (t *Tx) byLocation(counts map[uuid.UUID]map[string]uint32, holders map[uuid.UUID]keeper) ([]*app.AssetCensusRow, error) {
	tid := t.tenant.Uuid()
	ss, err := t.db.Asset.Query().
		Where(asset.TenantId(tid), asset.Kind("space"), asset.DateErasedIsNil()).
		Select(asset.FieldId, asset.FieldName, asset.FieldTag, asset.FieldParentId).
		All(t.ctx)
	if err != nil {
		return nil, err
	}
	spaces := map[uuid.UUID]*ent.Asset{}
	for _, s := range ss {
		spaces[s.Id] = s
	}

	// What holds something and is no space -- a rack, a computer, a kit -- is
	// in one, or in something that is. What each is in came with the count;
	// what that is in, when it is no space either, is read a level a
	// statement until a space or nothing, which is seldom more than one.
	up := map[uuid.UUID]*uuid.UUID{}
	frontier := []uuid.UUID{}
	for k, h := range holders {
		if h.space && spaces[k] != nil {
			continue
		}
		// One not found is not the tenant's, or gone: in nothing it may say.
		up[k] = h.up
	}
	for _, p := range up {
		if p != nil && spaces[*p] == nil {
			if _, known := up[*p]; !known {
				frontier = append(frontier, *p)
			}
		}
	}
	for depth := 0; len(frontier) > 0 && depth < 64; depth++ {
		frontier = slices.Compact(sortedIds(frontier))
		vs, err := t.assetsNamed(frontier)
		if err != nil {
			return nil, err
		}
		next := []uuid.UUID{}
		for _, k := range frontier {
			v, ok := vs[k]
			if !ok {
				up[k] = nil
				continue
			}
			up[k] = v.ParentId
			if p := v.ParentId; p != nil && spaces[*p] == nil {
				if _, known := up[*p]; !known {
					next = append(next, *p)
				}
			}
		}
		frontier = next
	}
	in := func(k uuid.UUID) uuid.UUID {
		for range 64 {
			if k == (uuid.UUID{}) || spaces[k] != nil {
				return k
			}
			p := up[k]
			if p == nil {
				return uuid.UUID{}
			}
			k = *p
		}
		return uuid.UUID{}
	}

	direct := map[uuid.UUID]map[string]uint32{}
	for k, c := range counts {
		at := in(k)
		if direct[at] == nil {
			direct[at] = map[string]uint32{}
		}
		for st, n := range c {
			direct[at][st] += n
		}
	}

	// Each space's own, added to it and to every space it is in.
	within := map[uuid.UUID]uint32{}
	for k, c := range direct {
		n := uint32(0)
		for _, v := range c {
			n += v
		}
		for at, hops := k, 0; at != (uuid.UUID{}) && hops < 64; hops++ {
			within[at] += n
			s := spaces[at]
			if s == nil || s.ParentId == nil || spaces[*s.ParentId] == nil {
				break
			}
			at = *s.ParentId
		}
	}
	path := func(k uuid.UUID) []string {
		names := []string{}
		s := spaces[k]
		for hops := 0; s != nil && s.ParentId != nil && hops < 64; hops++ {
			s = spaces[*s.ParentId]
			if s != nil {
				names = append([]string{s.Name}, names...)
			}
		}
		return names
	}

	rows := []*app.AssetCensusRow{}
	for k, n := range within {
		s := spaces[k]
		if s == nil {
			continue
		}
		r := censusRow(k, direct[k])
		if r.GetStatuses() == nil {
			r.SetStatuses(map[string]uint32{})
		}
		r.SetName(s.Name)
		r.SetTag(s.Tag)
		r.SetPath(path(k))
		r.SetWithin(n)
		rows = append(rows, r)
	}
	if c, ok := direct[uuid.UUID{}]; ok {
		r := censusRow(uuid.UUID{}, c)
		r.SetName("(위치 없음)")
		r.SetWithin(r.GetTotal())
		rows = append(rows, r)
	}
	slices.SortFunc(rows, func(a, b *app.AssetCensusRow) int {
		if d := compareNone(a, b); d != 0 {
			return d
		}
		if d := slices.Compare(append(slices.Clone(a.GetPath()), a.GetName()), append(slices.Clone(b.GetPath()), b.GetName())); d != 0 {
			return d
		}
		if d := strings.Compare(a.GetTag(), b.GetTag()); d != 0 {
			return d
		}
		return strings.Compare(string(a.GetId()), string(b.GetId()))
	})
	return rows, nil
}

// --- 대여 현황 ---

// Outstanding is what is out now, a page of it, and how much in all.
func (s domainCustody) Outstanding(ctx context.Context, req *app.CustodyOutstandingRequest) (*app.CustodyOutstandingResponse, error) {
	t, err := s.read(ctx)
	if err != nil {
		return nil, err
	}
	return t.outstanding(req, false)
}

// ExportOutstanding writes every row of what is out as a CSV: who has it, what
// it is, since when and until when, and how late.
func (s domainCustody) ExportOutstanding(ctx context.Context, req *app.CustodyOutstandingRequest) (*app.AssetExportResponse, error) {
	t, err := s.read(ctx)
	if err != nil {
		return nil, err
	}
	v, err := t.outstanding(req, true)
	if err != nil {
		return nil, err
	}
	rows := [][]string{{"받은 사람", "구분", "태그", "이름", "수량", "단위", "건넨 날", "반납 기한", "연체(일)", "인수 확인"}}
	for _, r := range v.GetRows() {
		name := r.GetAssetName()
		if len(r.GetStockId()) > 0 {
			name = r.GetStockName()
		}
		late := ""
		if r.GetOverdue() {
			late = number(r.GetOverdueDays())
		}
		ack := "대기"
		if r.GetAcknowledged() {
			ack = "확인"
		}
		rows = append(rows, []string{
			r.GetPartyName(), or(custodyKindSay[r.GetKind()], r.GetKind()), r.GetAssetTag(), name,
			number(r.GetQuantity()), r.GetUnit(), t.cell(r.GetIssuedAt()), t.cell(r.GetDueAt()), late, ack,
		})
	}
	return writeCSV("rove-loans-"+t.day(t.now), rows)
}

// outstanding is the lines of the open hand-overs that are not all back: a
// page of them as the request asks, or -- `all` -- every one; how many there
// are as it asks; and how much is out, whatever it asks. Each row names who
// has it, and so shows them ([namer.party]).
func (t *Tx) outstanding(req *app.CustodyOutstandingRequest, all bool) (*app.CustodyOutstandingResponse, error) {
	kind := req.GetKind()
	if kind != "" && kind != "issue" && kind != "loan" {
		return nil, invalid("kind", "지급(issue)이나 대여(loan)입니다")
	}
	size := int(req.GetSize())
	if size <= 0 {
		size = 50
	}
	size = min(size, 500)
	tid := t.tenant.Uuid()

	out := func(b *sql.Builder, asked bool) {
		b.S(` FROM "custody" c JOIN "custodyline" l ON l."custody_id" = c."id" WHERE c."tenant_id" = `).Arg(tid).
			S(` AND l."tenant_id" = `).Arg(tid).
			S(` AND c."status" = 'open' AND c."date_erased" IS NULL AND l."quantity" > l."returned_quantity"`)
		if !asked {
			return
		}
		if req.GetOverdue() {
			b.S(` AND c."due_at" < `).Arg(t.now)
		}
		if kind != "" {
			b.S(` AND c."kind" = `).Arg(kind)
		}
	}

	res := app.CustodyOutstandingResponse_builder{At: ts(t.now)}.Build()
	{
		b := t.q()
		b.S(`SELECT COUNT(DISTINCT c."id") AS "custodies", COUNT(DISTINCT CASE WHEN c."due_at" < `).Arg(t.now).
			S(` THEN c."id" END) AS "overdue", COUNT(DISTINCT c."party_id") AS "people",` +
				` COUNT(l."asset_id") AS "assets", COUNT(l."stock_id") AS "stocks"`)
		out(b, false)
		var vs []struct {
			Custodies int64 `sql:"custodies"`
			Overdue   int64 `sql:"overdue"`
			People    int64 `sql:"people"`
			Assets    int64 `sql:"assets"`
			Stocks    int64 `sql:"stocks"`
		}
		if err := t.rows(b, &vs); err != nil {
			return nil, err
		}
		for _, v := range vs {
			res.SetCustodies(uint32(v.Custodies))
			res.SetOverdue(uint32(v.Overdue))
			res.SetPeople(uint32(v.People))
			res.SetAssets(uint32(v.Assets))
			res.SetStocks(uint32(v.Stocks))
		}
	}
	{
		b := t.q()
		b.S(`SELECT COUNT(*) AS "n"`)
		out(b, true)
		var vs []struct {
			N int64 `sql:"n"`
		}
		if err := t.rows(b, &vs); err != nil {
			return nil, err
		}
		for _, v := range vs {
			res.SetTotal(uint32(v.N))
		}
	}

	b := t.q()
	b.S(`SELECT c."id" AS "custody", l."id" AS "line", c."kind", c."party_id", c."issued_at", c."due_at",` +
		` c."acknowledged_at", l."asset_id", l."stock_id", l."quantity" - l."returned_quantity" AS "out"`)
	out(b, true)
	// The most overdue first, then what is due soonest; what is held for
	// keeps after, the longest held first.
	b.S(` ORDER BY CASE WHEN c."due_at" IS NULL THEN 1 ELSE 0 END, c."due_at", c."issued_at", c."id", l."id"`)
	if !all {
		b.S(` LIMIT `).Arg(size).S(` OFFSET `).Arg(int(req.GetOffset()))
	}
	var ls []struct {
		Custody stdsql.Null[uuid.UUID] `sql:"custody"`
		Line    stdsql.Null[uuid.UUID] `sql:"line"`
		Kind    string                 `sql:"kind"`
		Party   stdsql.Null[uuid.UUID] `sql:"party_id"`
		Issued  stdsql.NullTime        `sql:"issued_at"`
		Due     stdsql.NullTime        `sql:"due_at"`
		Ack     stdsql.NullTime        `sql:"acknowledged_at"`
		Asset   stdsql.Null[uuid.UUID] `sql:"asset_id"`
		Stock   stdsql.Null[uuid.UUID] `sql:"stock_id"`
		Out     int64                  `sql:"out"`
	}
	if err := t.rows(b, &ls); err != nil {
		return nil, err
	}

	names := namer{t: t}
	parties, assets, stocks := []uuid.UUID{}, []uuid.UUID{}, []uuid.UUID{}
	for _, l := range ls {
		parties = append(parties, l.Party.V)
		if l.Asset.Valid {
			assets = append(assets, l.Asset.V)
		}
		if l.Stock.Valid {
			stocks = append(stocks, l.Stock.V)
		}
	}
	if err := names.readParties(parties); err != nil {
		return nil, err
	}
	as, err := t.assetsNamed(assets)
	if err != nil {
		return nil, err
	}
	ss := map[uuid.UUID]*ent.Stock{}
	for some := range slices.Chunk(slices.Compact(sortedIds(stocks)), 500) {
		vs, err := t.db.Stock.Query().
			Where(stock.TenantId(tid), stock.IdIn(some...)).
			Select(stock.FieldId, stock.FieldName, stock.FieldUnit).
			All(t.ctx)
		if err != nil {
			return nil, err
		}
		for _, v := range vs {
			ss[v.Id] = v
		}
	}

	rows := make([]*app.CustodyOutstandingRow, 0, len(ls))
	for _, l := range ls {
		r := app.CustodyOutstandingRow_builder{
			CustodyId: pdid.Id(l.Custody.V).Bytes(),
			LineId:    pdid.Id(l.Line.V).Bytes(),
			Kind:      l.Kind,
			PartyId:   pdid.Id(l.Party.V).Bytes(),
			// Who has it, as they are called now: a read of them.
			PartyName:    names.party(l.Party.V),
			Quantity:     l.Out,
			Acknowledged: l.Ack.Valid,
		}.Build()
		if l.Issued.Valid {
			r.SetIssuedAt(ts(l.Issued.Time))
		}
		if l.Due.Valid {
			r.SetDueAt(ts(l.Due.Time))
			if l.Due.Time.Before(t.now) {
				r.SetOverdue(true)
				r.SetOverdueDays(t.daysLate(l.Due.Time))
			}
		}
		if l.Asset.Valid {
			r.SetAssetId(pdid.Id(l.Asset.V).Bytes())
			if a := as[l.Asset.V]; a != nil {
				r.SetAssetTag(a.Tag)
				r.SetAssetName(a.Name)
			}
		}
		if l.Stock.Valid {
			r.SetStockId(pdid.Id(l.Stock.V).Bytes())
			if s := ss[l.Stock.V]; s != nil {
				r.SetStockName(s.Name)
				r.SetUnit(s.Unit)
			}
		}
		rows = append(rows, r)
	}
	res.SetRows(rows)
	return res, nil
}

// --- 예약 이용률 ---

// Usage is each bookable resource over a period.
func (s domainBookable) Usage(ctx context.Context, req *app.BookableUsageRequest) (*app.BookableUsageResponse, error) {
	t, err := s.read(ctx)
	if err != nil {
		return nil, err
	}
	return t.usage(req)
}

// ExportUsage writes each resource's use over a period as a CSV, in hours.
func (s domainBookable) ExportUsage(ctx context.Context, req *app.BookableUsageRequest) (*app.AssetExportResponse, error) {
	t, err := s.read(ctx)
	if err != nil {
		return nil, err
	}
	v, err := t.usage(req)
	if err != nil {
		return nil, err
	}
	rows := [][]string{{"태그", "이름", "종류", "수량", "예약", "이용", "노쇼", "예약 시간", "이용 시간", "이용률(%)", "예약 없이 나감", "예약 없이 나간 시간"}}
	for _, r := range v.GetRows() {
		share := ""
		if r.GetBookedMinutes() > 0 {
			share = strconv.FormatFloat(100*float64(r.GetUsedMinutes())/float64(r.GetBookedMinutes()), 'f', 0, 64)
		}
		rows = append(rows, []string{
			r.GetTag(), r.GetName(), or(kindSay[r.GetKind()], r.GetKind()), number(r.GetUnits()),
			number(r.GetReservations()), number(r.GetUsed()), number(r.GetNoShows()),
			hours(r.GetBookedMinutes()), hours(r.GetUsedMinutes()), share,
			number(r.GetHandedOut()), hours(r.GetHandedOutMinutes()),
		})
	}
	return writeCSV("rove-bookings-"+t.span(v.GetFrom().AsTime(), v.GetTo().AsTime()), rows)
}

// usage is the reservations that began in the period and were promised their
// time, by the resource each booked: how many, how many were used and how
// many nobody came for, the time they booked and the time they were used.
//
// A reservation counts in the period it began in, whole: one that runs past
// the period's end, or whose things come back late, is not split, and so is
// in one period and no other.
//
// What it was used for is from when it was taken to when it was given back:
//   - picked up, from the pickup to the return of everything the pickup
//     handed over, and to now while something is still out;
//   - checked in, from the check-in to its finish: the moment it was
//     completed, when that was before its end -- which is where finishing
//     early ends what it holds ([Tx.settleAllocations]) -- and its end
//     otherwise, or now while it is in use and has not ended;
//   - completed with neither, as a room is where nobody is held to checking
//     in, from its beginning to its finish.
//
// What was confirmed and not taken, and a no-show, was used for nothing. The
// database works each out and adds them up by the resource.
func (t *Tx) usage(req *app.BookableUsageRequest) (*app.BookableUsageResponse, error) {
	from, to, err := t.period(req.GetFrom(), req.GetTo(), lastDays(30))
	if err != nil {
		return nil, err
	}
	tid := t.tenant.Uuid()
	sec := t.seconds
	now := float64(t.now.UnixMicro()) / 1e6

	// The reservations, and beside each its pickup and how long it held what
	// it booked: each read once, as a table of the period's reservations
	// joined to them -- which lets the database look each one up, or read
	// them all, as it finds cheaper -- and then worked out and added up.
	b := t.q()
	b.S(`WITH "r" AS (SELECT r."id", r."status", ` +
		sec(`r."begins_at"`) + ` AS "begins", ` + sec(`r."ends_at"`) + ` AS "ends", ` + sec(`r."checked_in_at"`) + ` AS "checked"` +
		` FROM "reservation" r WHERE r."tenant_id" = `).Arg(tid).
		S(` AND r."date_erased" IS NULL AND r."status" IN ('confirmed', 'in_use', 'completed', 'no_show')` +
			` AND r."begins_at" >= `).Arg(from).S(` AND r."begins_at" < `).Arg(to).
		S(`), "p" AS (SELECT c."reservation_id" AS "id", MIN(` + sec(`c."issued_at"`) + `) AS "picked",` +
			` CASE WHEN COUNT(c."returned_at") = COUNT(*) THEN MAX(` + sec(`c."returned_at"`) + `) END AS "back"` +
			` FROM "custody" c JOIN "r" ON r."id" = c."reservation_id" WHERE c."tenant_id" = `).Arg(tid).
		S(` GROUP BY c."reservation_id"), "f" AS (SELECT a."ref_id" AS "id", a."resource_id", MAX(` + sec(`a."ends_at"`) + `) AS "finish"` +
			` FROM "allocation" a JOIN "r" ON r."id" = a."ref_id" WHERE a."tenant_id" = `).Arg(tid).
		S(` GROUP BY a."ref_id", a."resource_id")` +
			` SELECT "resource_id", COUNT(*) AS "reservations",` +
			` SUM(CASE WHEN "status" = 'no_show' THEN 1 ELSE 0 END) AS "no_shows",` +
			` SUM(CASE WHEN "used_from" IS NOT NULL THEN 1 ELSE 0 END) AS "used",` +
			` SUM("units" * ("ends" - "begins")) AS "booked",` +
			` SUM(CASE WHEN "used_from" IS NOT NULL THEN "units" * ` + t.greatest() + `(0.0, "used_to" - "used_from") ELSE 0.0 END) AS "used_for"` +
			` FROM (SELECT i."resource_id" AS "resource_id", CASE WHEN i."units" > 1 THEN i."units" ELSE 1 END AS "units",` +
			` r."status" AS "status", r."begins" AS "begins", r."ends" AS "ends",` +
			` CASE WHEN p."picked" IS NOT NULL THEN p."picked"` +
			` WHEN r."status" IN ('in_use', 'completed') AND r."checked" IS NOT NULL THEN r."checked"` +
			` WHEN r."status" = 'completed' THEN r."begins" END AS "used_from",` +
			` CASE WHEN p."picked" IS NOT NULL THEN COALESCE(p."back", `).Arg(now).S(`)` +
		` WHEN r."status" = 'in_use' THEN ` + t.least() + `(`).Arg(now).S(`, r."ends")` +
		` WHEN r."status" = 'completed' THEN ` + t.least() + `(r."ends", COALESCE(f."finish", r."ends")) END AS "used_to"` +
		` FROM "reservationitem" i JOIN "r" ON r."id" = i."reservation_id"` +
		` LEFT JOIN "p" ON p."id" = r."id"` +
		` LEFT JOIN "f" ON f."id" = r."id" AND f."resource_id" = i."resource_id"` +
		` WHERE i."tenant_id" = `).Arg(tid).
		S(`) u GROUP BY "resource_id"`)
	var vs []struct {
		Resource     stdsql.Null[uuid.UUID] `sql:"resource_id"`
		Reservations int64                  `sql:"reservations"`
		NoShows      int64                  `sql:"no_shows"`
		Used         int64                  `sql:"used"`
		Booked       float64                `sql:"booked"`
		UsedFor      float64                `sql:"used_for"`
	}
	if err := t.rows(b, &vs); err != nil {
		return nil, err
	}

	// And what was handed over with no reservation -- a loan or an issue at
	// the desk, whose time it held as a booking's (D53) -- of each hand-over
	// of a thing that began in the period, whole as a reservation is: from
	// the hand-over to the thing's return, and to now while it is out.
	h := t.q()
	h.S(`SELECT l."asset_id" AS "resource_id", COUNT(*) AS "handed",` +
		` SUM(` + t.greatest() + `(0.0, COALESCE(` + sec(`l."returned_at"`) + `, `).Arg(now).S(`) - ` + sec(`l."out_at"`) + `)) AS "out_for"` +
		` FROM "custodyline" l JOIN "custody" c ON c."id" = l."custody_id"` +
		` WHERE c."tenant_id" = `).Arg(tid).
		S(` AND c."reservation_id" IS NULL AND c."date_erased" IS NULL AND l."asset_id" IS NOT NULL` +
			` AND c."issued_at" >= `).Arg(from).S(` AND c."issued_at" < `).Arg(to).
		S(` GROUP BY l."asset_id"`)
	var hs []struct {
		Resource stdsql.Null[uuid.UUID] `sql:"resource_id"`
		Handed   int64                  `sql:"handed"`
		OutFor   float64                `sql:"out_for"`
	}
	if err := t.rows(h, &hs); err != nil {
		return nil, err
	}
	handed := map[uuid.UUID]int{}
	for i, v := range hs {
		handed[v.Resource.V] = i
	}

	// Every resource that may be booked, and every one booked that may no
	// longer be. What was handed over that may not be booked is no row of
	// this: it is the report of what is out's.
	bs, err := t.db.Bookable.Query().
		Where(bookable.TenantId(tid), bookable.DateErasedIsNil()).
		Select(bookable.FieldAssetId, bookable.FieldUnits).
		All(t.ctx)
	if err != nil {
		return nil, err
	}
	units := map[uuid.UUID]uint32{}
	ids := []uuid.UUID{}
	for _, v := range bs {
		units[v.AssetId] = max(v.Units, 1)
		ids = append(ids, v.AssetId)
	}
	used := map[uuid.UUID]int{}
	for i, v := range vs {
		used[v.Resource.V] = i
		ids = append(ids, v.Resource.V)
	}
	as, err := t.assetsNamed(ids)
	if err != nil {
		return nil, err
	}
	minutes := func(s float64) uint64 { return uint64(math.Round(max(s, 0) / 60)) }

	rows := []*app.BookableUsageRow{}
	for _, k := range slices.Compact(sortedIds(ids)) {
		a := as[k]
		if a == nil {
			continue
		}
		r := app.BookableUsageRow_builder{
			ResourceId: pdid.Id(k).Bytes(),
			Tag:        a.Tag,
			Name:       a.Name,
			Kind:       a.Kind,
			Units:      max(units[k], 1),
		}.Build()
		if i, ok := used[k]; ok {
			v := vs[i]
			r.SetReservations(uint32(v.Reservations))
			r.SetUsed(uint32(v.Used))
			r.SetNoShows(uint32(v.NoShows))
			r.SetBookedMinutes(minutes(v.Booked))
			r.SetUsedMinutes(minutes(v.UsedFor))
		}
		if i, ok := handed[k]; ok {
			// All of it is what was out: of one asset that stands for many,
			// every one of them, as its hand-over held it.
			v := hs[i]
			r.SetHandedOut(uint32(v.Handed))
			r.SetHandedOutMinutes(minutes(v.OutFor * float64(r.GetUnits())))
		}
		rows = append(rows, r)
	}
	slices.SortStableFunc(rows, func(a, b *app.BookableUsageRow) int {
		if d := strings.Compare(a.GetTag(), b.GetTag()); d != 0 {
			return d
		}
		return strings.Compare(a.GetName(), b.GetName())
	})
	return app.BookableUsageResponse_builder{Rows: rows, From: ts(from), To: ts(to)}.Build(), nil
}

// --- 실사 결과 ---

// Results is each count begun in a period, by what it found.
func (s domainCount) Results(ctx context.Context, req *app.InventoryCountResultsRequest) (*app.InventoryCountResultsResponse, error) {
	t, err := s.read(ctx)
	if err != nil {
		return nil, err
	}
	return t.results(req)
}

// ExportResults writes each count's results as a CSV.
func (s domainCount) ExportResults(ctx context.Context, req *app.InventoryCountResultsRequest) (*app.AssetExportResponse, error) {
	t, err := s.read(ctx)
	if err != nil {
		return nil, err
	}
	v, err := t.results(req)
	if err != nil {
		return nil, err
	}
	head := []string{"실사", "범위", "상태", "시작", "종료"}
	for _, k := range []string{"seen", "misplaced", "missing", "unknown"} {
		head = append(head, findingSay[k])
	}
	for _, k := range []string{"moved", "lost", "ignored", "open"} {
		head = append(head, resolutionSay[k])
	}
	rows := [][]string{head}
	for _, r := range v.GetRows() {
		rows = append(rows, []string{
			r.GetName(), r.GetScopeName(), or(countSay[r.GetStatus()], r.GetStatus()), t.cell(r.GetStartedAt()), t.cell(r.GetClosedAt()),
			number(r.GetSeen()), number(r.GetMisplaced()), number(r.GetMissing()), number(r.GetUnknown()),
			number(r.GetMoved()), number(r.GetLost()), number(r.GetIgnored()), number(r.GetUnsettled()),
		})
	}
	return writeCSV("rove-counts-"+t.span(v.GetFrom().AsTime(), v.GetTo().AsTime()), rows)
}

// results is the counts begun in the period, the newest first, and what each
// found: the database adds up its findings by kind and by how each was
// settled.
func (t *Tx) results(req *app.InventoryCountResultsRequest) (*app.InventoryCountResultsResponse, error) {
	from, to, err := t.period(req.GetFrom(), req.GetTo(), func(end time.Time) time.Time { return end.AddDate(-1, 0, 0) })
	if err != nil {
		return nil, err
	}
	tid := t.tenant.Uuid()
	cs, err := t.db.InventoryCount.Query().
		Where(
			inventorycount.TenantId(tid),
			inventorycount.DateErasedIsNil(),
			inventorycount.StartedAtGTE(from),
			inventorycount.StartedAtLT(to),
		).
		Order(ent.Desc(inventorycount.FieldStartedAt), ent.Desc(inventorycount.FieldId)).
		All(t.ctx)
	if err != nil {
		return nil, err
	}
	ids, scopes := []uuid.UUID{}, []uuid.UUID{}
	for _, c := range cs {
		ids = append(ids, c.Id)
		scopes = append(scopes, c.ScopeId)
	}
	type key struct {
		count            uuid.UUID
		kind, resolution string
	}
	found := map[key]uint32{}
	for some := range slices.Chunk(ids, 500) {
		var vs []struct {
			Count      stdsql.Null[uuid.UUID] `sql:"count_id"`
			Kind       string                 `sql:"kind"`
			Resolution string                 `sql:"resolution"`
			N          int64                  `sql:"count"`
		}
		if err := t.db.CountFinding.Query().
			Where(countfinding.TenantId(tid), countfinding.CountIdIn(some...)).
			GroupBy(countfinding.FieldCountId, countfinding.FieldKind, countfinding.FieldResolution).
			Aggregate(ent.Count()).
			Scan(t.ctx, &vs); err != nil {
			return nil, err
		}
		for _, v := range vs {
			found[key{v.Count.V, v.Kind, v.Resolution}] += uint32(v.N)
		}
	}
	as, err := t.assetsNamed(scopes)
	if err != nil {
		return nil, err
	}

	rows := make([]*app.InventoryCountResult, 0, len(cs))
	for _, c := range cs {
		r := app.InventoryCountResult_builder{
			CountId:   pdid.Id(c.Id).Bytes(),
			Name:      c.Name,
			ScopeId:   pdid.Id(c.ScopeId).Bytes(),
			Status:    c.Status,
			StartedAt: ts(c.StartedAt),
			ClosedAt:  tsp(c.ClosedAt),
		}.Build()
		if a := as[c.ScopeId]; a != nil {
			r.SetScopeName(a.Name)
		}
		for k, n := range found {
			if k.count != c.Id {
				continue
			}
			switch k.kind {
			case "seen":
				r.SetSeen(r.GetSeen() + n)
			case "misplaced":
				r.SetMisplaced(r.GetMisplaced() + n)
			case "missing":
				r.SetMissing(r.GetMissing() + n)
			case "unknown":
				r.SetUnknown(r.GetUnknown() + n)
			}
			switch k.resolution {
			case "moved":
				r.SetMoved(r.GetMoved() + n)
			case "lost":
				r.SetLost(r.GetLost() + n)
			case "ignored":
				r.SetIgnored(r.GetIgnored() + n)
			case "open":
				if k.kind != "seen" {
					r.SetUnsettled(r.GetUnsettled() + n)
				}
			}
		}
		rows = append(rows, r)
	}
	return app.InventoryCountResultsResponse_builder{Rows: rows, From: ts(from), To: ts(to)}.Build(), nil
}

// --- 재고 소모 ---

// Flow is each stock over a period.
func (s domainStock) Flow(ctx context.Context, req *app.StockFlowRequest) (*app.StockFlowResponse, error) {
	t, err := s.read(ctx)
	if err != nil {
		return nil, err
	}
	return t.flow(req)
}

// ExportFlow writes each stock's flow over a period as a CSV.
func (s domainStock) ExportFlow(ctx context.Context, req *app.StockFlowRequest) (*app.AssetExportResponse, error) {
	t, err := s.read(ctx)
	if err != nil {
		return nil, err
	}
	v, err := t.flow(req)
	if err != nil {
		return nil, err
	}
	rows := [][]string{{
		"재고", "위치", "단위", "기초",
		movementSay["receive"], movementSay["return"], movementSay["transfer_in"], "조정(+)", "들어옴",
		movementSay["consume"], movementSay["issue"], movementSay["transfer_out"], movementSay["convert"], "조정(-)", "나감",
		"기말",
	}}
	for _, r := range v.GetRows() {
		rows = append(rows, []string{
			r.GetName(), r.GetSpaceName(), r.GetUnit(), number(r.GetOpening()),
			number(r.GetReceived()), number(r.GetReturned()), number(r.GetTransferredIn()), number(r.GetAdjustedUp()), number(r.GetCameIn()),
			number(r.GetConsumed()), number(r.GetIssued()), number(r.GetTransferredOut()), number(r.GetConverted()), number(r.GetAdjustedDown()), number(r.GetWentOut()),
			number(r.GetClosing()),
		})
	}
	return writeCSV("rove-stock-"+t.span(v.GetFrom().AsTime(), v.GetTo().AsTime()), rows)
}

// flow is each stock over the period: the database adds up what moved it in
// the period, by what moved it, and what moved it since -- which, taken from
// what it has now, is what it had when the period ended. Every change of a
// stock is a movement, the quantity it was registered with among them, so
// what it had when the period began is that less what moved it in between.
// Each is read from the workspace's stocks, so that the database may read
// each stock's movements of the period by the index it keeps of them by the
// stock and the moment, rather than every movement there is.
func (t *Tx) flow(req *app.StockFlowRequest) (*app.StockFlowResponse, error) {
	from, to, err := t.period(req.GetFrom(), req.GetTo(), lastDays(30))
	if err != nil {
		return nil, err
	}
	tid := t.tenant.Uuid()
	ss, err := t.db.Stock.Query().
		Where(stock.TenantId(tid), stock.DateErasedIsNil()).
		Select(stock.FieldId, stock.FieldName, stock.FieldUnit, stock.FieldQuantity, stock.FieldSpaceId).
		All(t.ctx)
	if err != nil {
		return nil, err
	}

	sum := func(when, what string) string {
		return `CAST(SUM(CASE WHEN ` + when + ` THEN ` + what + ` ELSE 0 END) AS BIGINT)`
	}
	b := t.q()
	b.S(`SELECT m."stock_id", ` +
		sum(`m."reason" = 'receive'`, `m."delta"`) + ` AS "received", ` +
		sum(`m."reason" = 'return'`, `m."delta"`) + ` AS "returned", ` +
		sum(`m."reason" = 'transfer_in'`, `m."delta"`) + ` AS "transferred_in", ` +
		sum(`m."reason" = 'adjust' AND m."delta" > 0`, `m."delta"`) + ` AS "adjusted_up", ` +
		sum(`m."reason" = 'consume'`, `-m."delta"`) + ` AS "consumed", ` +
		sum(`m."reason" = 'issue'`, `-m."delta"`) + ` AS "issued", ` +
		sum(`m."reason" = 'transfer_out'`, `-m."delta"`) + ` AS "transferred_out", ` +
		sum(`m."reason" = 'convert'`, `-m."delta"`) + ` AS "converted", ` +
		sum(`m."reason" = 'adjust' AND m."delta" < 0`, `-m."delta"`) + ` AS "adjusted_down", ` +
		sum(`m."delta" > 0`, `m."delta"`) + ` AS "came_in", ` +
		sum(`m."delta" < 0`, `-m."delta"`) + ` AS "went_out"` +
		` FROM "stock" s JOIN "stockmovement" m ON m."stock_id" = s."id" WHERE s."tenant_id" = `).Arg(tid).
		S(` AND m."tenant_id" = `).Arg(tid).
		S(` AND m."occurred_at" >= `).Arg(from).S(` AND m."occurred_at" < `).Arg(to).
		S(` GROUP BY m."stock_id"`)
	var moved []struct {
		Stock          stdsql.Null[uuid.UUID] `sql:"stock_id"`
		Received       int64                  `sql:"received"`
		Returned       int64                  `sql:"returned"`
		TransferredIn  int64                  `sql:"transferred_in"`
		AdjustedUp     int64                  `sql:"adjusted_up"`
		Consumed       int64                  `sql:"consumed"`
		Issued         int64                  `sql:"issued"`
		TransferredOut int64                  `sql:"transferred_out"`
		Converted      int64                  `sql:"converted"`
		AdjustedDown   int64                  `sql:"adjusted_down"`
		CameIn         int64                  `sql:"came_in"`
		WentOut        int64                  `sql:"went_out"`
	}
	if err := t.rows(b, &moved); err != nil {
		return nil, err
	}
	b = t.q()
	b.S(`SELECT m."stock_id", CAST(SUM(m."delta") AS BIGINT) AS "since"` +
		` FROM "stock" s JOIN "stockmovement" m ON m."stock_id" = s."id" WHERE s."tenant_id" = `).Arg(tid).
		S(` AND m."tenant_id" = `).Arg(tid).
		S(` AND m."occurred_at" >= `).Arg(to).
		S(` GROUP BY m."stock_id"`)
	var after []struct {
		Stock stdsql.Null[uuid.UUID] `sql:"stock_id"`
		Since int64                  `sql:"since"`
	}
	if err := t.rows(b, &after); err != nil {
		return nil, err
	}
	since := map[uuid.UUID]int64{}
	for _, v := range after {
		since[v.Stock.V] = v.Since
	}
	in := map[uuid.UUID]int{}
	for i, v := range moved {
		in[v.Stock.V] = i
	}
	spaces := []uuid.UUID{}
	for _, s := range ss {
		spaces = append(spaces, s.SpaceId)
	}
	as, err := t.assetsNamed(spaces)
	if err != nil {
		return nil, err
	}

	rows := make([]*app.StockFlowRow, 0, len(ss))
	for _, s := range ss {
		closing := s.Quantity - since[s.Id]
		r := app.StockFlowRow_builder{
			StockId: pdid.Id(s.Id).Bytes(),
			Name:    s.Name,
			Unit:    s.Unit,
			SpaceId: pdid.Id(s.SpaceId).Bytes(),
			Opening: closing,
			Closing: closing,
		}.Build()
		if a := as[s.SpaceId]; a != nil {
			r.SetSpaceName(a.Name)
		}
		if i, ok := in[s.Id]; ok {
			v := moved[i]
			r.SetReceived(v.Received)
			r.SetReturned(v.Returned)
			r.SetTransferredIn(v.TransferredIn)
			r.SetAdjustedUp(v.AdjustedUp)
			r.SetConsumed(v.Consumed)
			r.SetIssued(v.Issued)
			r.SetTransferredOut(v.TransferredOut)
			r.SetConverted(v.Converted)
			r.SetAdjustedDown(v.AdjustedDown)
			r.SetCameIn(v.CameIn)
			r.SetWentOut(v.WentOut)
			r.SetOpening(closing - v.CameIn + v.WentOut)
		}
		rows = append(rows, r)
	}
	slices.SortStableFunc(rows, func(a, b *app.StockFlowRow) int {
		if d := strings.Compare(a.GetName(), b.GetName()); d != 0 {
			return d
		}
		return strings.Compare(a.GetSpaceName(), b.GetSpaceName())
	})
	return app.StockFlowResponse_builder{Rows: rows, From: ts(from), To: ts(to)}.Build(), nil
}
