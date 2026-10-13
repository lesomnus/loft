package domain

import (
	"bytes"
	"context"
	"encoding/csv"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
	"uuid"

	"github.com/lesomnus/payday/pdid"
	"github.com/lesomnus/z"
	"github.com/xuri/excelize/v2"
	"golang.org/x/text/encoding/korean"
	"golang.org/x/text/transform"
	"google.golang.org/grpc/status"

	app "github.com/lesomnus/rove"
	"github.com/lesomnus/rove/internal/ent"
	"github.com/lesomnus/rove/internal/ent/asset"
	"github.com/lesomnus/rove/internal/ent/assettype"
	"github.com/lesomnus/rove/internal/ent/itemmodel"
	"github.com/lesomnus/rove/internal/ent/party"
)

// Zone is where a date a spreadsheet writes without a zone is: the pilot's,
// until a tenant says otherwise.
var Zone = time.FixedZone("KST", 9*60*60)

// MaxImportRows is the most rows one import takes; a register larger than
// this is imported in parts.
const MaxImportRows = 5000

// importColumns is what a header may say, in either language, and what it
// means.
var importColumns = map[string]string{
	"tag": "tag", "태그": "tag", "자산번호": "tag", "관리번호": "tag",
	"name": "name", "이름": "name", "자산명": "name", "품명": "name",
	"kind": "kind", "종류": "kind", "구분": "kind",
	"type": "type", "유형": "type", "분류": "type",
	"model": "model", "모델": "model", "모델명": "model",
	"maker": "maker", "제조사": "maker",
	"serial": "serial", "시리얼": "serial", "일련번호": "serial", "s/n": "serial",
	"status": "status", "상태": "status",
	"condition": "condition", "컨디션": "condition",
	"location": "location", "위치": "location", "장소": "location",
	"location_tag": "location_tag", "위치태그": "location_tag",
	"custodian": "custodian", "사용자": "custodian", "담당자": "custodian",
	"acquired_at": "acquired_at", "취득일": "acquired_at", "구매일": "acquired_at",
	"since": "since", "기준일": "since",
	"desc": "desc", "설명": "desc", "비고": "desc", "메모": "desc",
}

// The Korean words a sheet may use for rove's own, and the ones export writes.
var (
	kindWords      = map[string]string{"물품": "item", "자산": "item", "공간": "space", "키트": "kit", "그룹": "group"}
	statusWords    = map[string]string{"주문": "ordered", "사용중": "active", "운용": "active", "수리중": "in_repair", "분실": "lost", "불용": "retired", "폐기": "disposed"}
	conditionWords = map[string]string{"양호": "good", "손상": "damaged", "고장": "broken"}

	kindSay        = map[string]string{"item": "물품", "space": "공간", "kit": "키트", "group": "그룹"}
	reservationSay = map[string]string{"held": "임시 홀드", "requested": "승인 대기", "confirmed": "확정", "in_use": "이용 중", "completed": "완료", "cancelled": "취소", "rejected": "거절", "expired": "만료", "no_show": "노쇼"}
	custodySay     = map[string]string{"open": "진행 중", "returned": "반납 완료"}
	purchaseSay    = map[string]string{"ordered": "주문", "received": "입고 완료", "cancelled": "취소"}
	statusSay      = map[string]string{"ordered": "주문", "active": "사용중", "in_repair": "수리중", "lost": "분실", "retired": "불용", "disposed": "폐기"}
	conditionSay   = map[string]string{"good": "양호", "damaged": "손상", "broken": "고장"}
)

func word(v string, words map[string]string) string {
	v = strings.TrimSpace(v)
	if w, ok := words[v]; ok {
		return w
	}
	return strings.ToLower(v)
}

// readTable reads the first sheet of a workbook, or a CSV in UTF-8 or in the
// CP949 that Korean Excel saves by default.
func readTable(format string, data []byte) ([][]string, error) {
	switch strings.ToLower(format) {
	case "xlsx":
		f, err := excelize.OpenReader(bytes.NewReader(data))
		if err != nil {
			return nil, fmt.Errorf("not an xlsx workbook: %w", err)
		}
		defer f.Close()
		sheets := f.GetSheetList()
		if len(sheets) == 0 {
			return nil, errors.New("the workbook has no sheet")
		}
		return f.GetRows(sheets[0])

	case "", "csv":
		data = bytes.TrimPrefix(data, []byte("\xef\xbb\xbf"))
		if !utf8.Valid(data) {
			v, _, err := transform.Bytes(korean.EUCKR.NewDecoder(), data)
			if err != nil {
				return nil, errors.New("the file is neither UTF-8 nor CP949")
			}
			data = v
		}
		r := csv.NewReader(bytes.NewReader(data))
		r.FieldsPerRecord = -1
		r.LazyQuotes = true
		return r.ReadAll()
	}
	return nil, fmt.Errorf("format %q is csv or xlsx", format)
}

var dateLayouts = []string{
	"2006-01-02", "2006.01.02", "2006/01/02", "20060102",
	"2006-1-2", "2006.1.2", "2006/1/2", "2006. 1. 2",
	"2006-01-02 15:04", "2006-01-02 15:04:05", "2006-01-02T15:04:05Z07:00",
	"01-02-06", "1/2/06", "1/2/2006",
}

// parseDate reads a date as people write one, or as Excel stores one.
func parseDate(v string) (time.Time, error) {
	v = strings.TrimSuffix(strings.TrimSpace(v), ".")
	for _, l := range dateLayouts {
		if t, err := time.ParseInLocation(l, v, Zone); err == nil {
			return t, nil
		}
	}
	if n, err := strconv.ParseFloat(v, 64); err == nil && n > 1 && n < 100000 {
		t, err := excelize.ExcelDateToTime(n, false)
		if err == nil {
			return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, Zone), nil
		}
	}
	return time.Time{}, fmt.Errorf("%q는 날짜가 아닙니다 (2024-03-01처럼 적어 주세요)", v)
}

type importRow struct {
	line  int
	v     map[string]string
	attrs map[string]string

	// The asset the tag names, when there is one: the row then updates it.
	cur  *ent.Asset
	kind string

	since    *time.Time
	acquired *time.Time

	// Where it goes: a tag, or a path of space names.
	loc  string
	path []string

	custodian uuid.UUID
	done      bool
}

type importer struct {
	t   *Tx
	out *app.AssetImportResponse

	rows  []*importRow
	byTag map[string]*importRow

	types   map[string]uuid.UUID
	models  map[string]uuid.UUID
	paths   map[string]uuid.UUID
	waiting map[string]int

	issue map[uuid.UUID][]uuid.UUID
	order []uuid.UUID
}

func (im *importer) errorf(line int, format string, args ...any) {
	im.out.SetErrors(append(im.out.GetErrors(), fmt.Sprintf("%d행: ", line)+fmt.Sprintf(format, args...)))
}

func (im *importer) warnf(line int, format string, args ...any) {
	msg := fmt.Sprintf(format, args...)
	if line > 0 {
		msg = fmt.Sprintf("%d행: ", line) + msg
	}
	im.out.SetWarnings(append(im.out.GetWarnings(), msg))
}

// errDry is how a dry run ends: everything was done, and is rolled back.
var errDry = errors.New("domain: dry run")

// Import registers or updates assets from a sheet (design 9.8). A row whose
// tag names an asset updates it; any other row registers one. Spaces named by
// a path that does not exist yet are made, and so are types and models named
// by a name nothing has yet.
//
// It is one transaction: a sheet with an error in it changes nothing, and the
// answer lists every row that is wrong so that one round of fixes is enough.
func (s domainAsset) Import(ctx context.Context, req *app.AssetImportRequest) (*app.AssetImportResponse, error) {
	if len(req.GetData()) > MaxUpload {
		return nil, invalid("data", "파일은 %dMB까지입니다", MaxUpload>>20)
	}
	table, err := readTable(req.GetFormat(), req.GetData())
	if err != nil {
		return nil, invalid("data", "파일을 읽을 수 없습니다: %v", err)
	}
	if len(table) < 2 {
		return nil, invalid("data", "머리글 줄과 자료가 한 줄 이상 있어야 합니다")
	}
	if len(table)-1 > MaxImportRows {
		return nil, invalid("data", "한 번에 %d줄까지입니다. 나눠서 가져오세요", MaxImportRows)
	}

	out := &app.AssetImportResponse{}
	err = s.tx(ctx, func(t *Tx) error {
		im := &importer{
			t:       t,
			out:     out,
			byTag:   map[string]*importRow{},
			types:   map[string]uuid.UUID{},
			models:  map[string]uuid.UUID{},
			paths:   map[string]uuid.UUID{},
			waiting: map[string]int{},
			issue:   map[uuid.UUID][]uuid.UUID{},
		}
		im.read(table)
		if len(out.GetErrors()) > 0 {
			return errDry
		}
		if err := im.check(); err != nil {
			return err
		}
		if len(out.GetErrors()) > 0 {
			return errDry
		}
		if err := t.begin(nil, "asset.import", pdid.Nil, t.now, fmt.Sprintf("가져오기: %d행", len(im.rows)), "", map[string]string{"format": or(req.GetFormat(), "csv")}); err != nil {
			return err
		}
		if err := im.write(); err != nil {
			return err
		}
		if req.GetDryRun() {
			return errDry
		}
		return nil
	})
	if errors.Is(err, errDry) {
		return out, nil
	}
	if err != nil {
		return nil, err
	}
	return out, nil
}

// read takes the header and the rows.
func (im *importer) read(table [][]string) {
	header := table[0]
	cols := make([]string, len(header))
	seen := map[string]bool{}
	for i, h := range header {
		raw := strings.TrimSpace(strings.TrimPrefix(h, "\ufeff"))
		k := strings.ToLower(raw)
		if k == "" {
			continue
		}
		if m, ok := importColumns[k]; ok {
			if seen[m] {
				im.warnf(0, "열 %q는 같은 뜻의 앞 열과 겹쳐 읽지 않았습니다", raw)
				continue
			}
			seen[m] = true
			cols[i] = m
			continue
		}
		for _, p := range []string{"attr.", "속성.", "속성:"} {
			if strings.HasPrefix(k, p) && len(raw) > len(p) {
				cols[i] = "attr." + strings.TrimSpace(raw[len(p):])
			}
		}
		if cols[i] == "" {
			im.warnf(0, "열 %q는 무엇인지 몰라 읽지 않았습니다 (속성이라면 \"속성.%s\"로 적어 주세요)", raw, raw)
		}
	}
	if !seen["name"] && !seen["tag"] {
		im.errorf(1, "이름 열(이름, name)이나 태그 열(태그, tag)이 있어야 합니다")
		return
	}

	for n, rec := range table[1:] {
		r := &importRow{line: n + 2, v: map[string]string{}, attrs: map[string]string{}}
		for i, c := range cols {
			if c == "" || i >= len(rec) {
				continue
			}
			val := strings.TrimSpace(rec[i])
			if val == "" {
				continue
			}
			if k, ok := strings.CutPrefix(c, "attr."); ok {
				r.attrs[k] = val
				continue
			}
			r.v[c] = val
		}
		if len(r.v) > 0 || len(r.attrs) > 0 {
			im.rows = append(im.rows, r)
		}
	}
}

// check reads every row against what is there, writing nothing, so that every
// error in the sheet is answered at once.
func (im *importer) check() error {
	t := im.t
	for _, r := range im.rows {
		if tag := r.v["tag"]; tag != "" {
			if prev, ok := im.byTag[tag]; ok {
				im.errorf(r.line, "태그 %q가 %d행에도 있습니다", tag, prev.line)
				continue
			}
			im.byTag[tag] = r
			cur, err := t.db.Asset.Query().Where(asset.TenantId(t.tenant.Uuid()), asset.Tag(tag), asset.DateErasedIsNil()).Only(t.ctx)
			switch {
			case err == nil:
				r.cur = cur
			case !ent.IsNotFound(err):
				return err
			}
		}
		if r.cur == nil && r.v["name"] == "" {
			im.errorf(r.line, "새로 등록하는 자산에는 이름이 있어야 합니다")
		}

		r.kind = "item"
		if r.cur != nil {
			r.kind = r.cur.Kind
		} else if name := r.v["type"]; name != "" && r.v["kind"] == "" {
			ty, err := t.db.AssetType.Query().Where(assettype.TenantId(t.tenant.Uuid()), assettype.NameEqualFold(name), assettype.DateErasedIsNil()).First(t.ctx)
			if err == nil {
				r.kind = ty.Kind
			} else if !ent.IsNotFound(err) {
				return err
			}
		}
		if v := r.v["kind"]; v != "" {
			k := word(v, kindWords)
			switch {
			case !slices.Contains(Kinds, k):
				im.errorf(r.line, "종류 %q는 물품, 공간, 키트, 그룹 중 하나입니다", v)
			case r.cur != nil && k != r.cur.Kind:
				im.errorf(r.line, "%s의 종류는 바꿀 수 없습니다", r.cur.Tag)
			default:
				r.kind = k
			}
		}
		if v := r.v["status"]; v != "" {
			if w := word(v, statusWords); slices.Contains(Statuses, w) {
				r.v["status"] = w
			} else {
				im.errorf(r.line, "상태 %q는 사용중, 수리중, 분실, 불용, 폐기, 주문 중 하나입니다", v)
			}
		}
		if v := r.v["condition"]; v != "" {
			if w := word(v, conditionWords); slices.Contains(Conditions, w) {
				r.v["condition"] = w
			} else {
				im.errorf(r.line, "컨디션 %q는 양호, 손상, 고장 중 하나입니다", v)
			}
		}
		for _, k := range []string{"since", "acquired_at"} {
			v := r.v[k]
			if v == "" {
				continue
			}
			d, err := parseDate(v)
			if err != nil {
				im.errorf(r.line, "%v", err)
				continue
			}
			if d.After(t.now) {
				im.errorf(r.line, "%s는 미래일 수 없습니다", v)
				continue
			}
			if k == "since" {
				r.since = &d
			} else {
				r.acquired = &d
			}
		}
		if r.kind == "space" {
			im.waiting[r.v["name"]]++
		}
	}

	// Where each goes, once every tag in the sheet is known.
	for _, r := range im.rows {
		if tag := r.v["location_tag"]; tag != "" {
			ok, err := im.known(tag)
			if err != nil {
				return err
			}
			if !ok {
				im.errorf(r.line, "위치태그 %q인 자산이 없습니다", tag)
			}
			r.loc = tag
		} else if v := r.v["location"]; v != "" {
			ok, err := im.known(v)
			if err != nil {
				return err
			}
			if ok {
				r.loc = v
			} else {
				r.path = splitPath(v)
			}
		}
		if r.loc != "" && r.loc == r.v["tag"] {
			im.errorf(r.line, "자산이 자기 안에 있을 수는 없습니다")
		}
		if err := im.checkCustodian(r); err != nil {
			return err
		}
	}
	return nil
}

// known answers whether a tag names an asset, in the sheet or already there.
func (im *importer) known(tag string) (bool, error) {
	if _, ok := im.byTag[tag]; ok {
		return true, nil
	}
	return im.t.db.Asset.Query().Where(asset.TenantId(im.t.tenant.Uuid()), asset.Tag(tag), asset.DateErasedIsNil()).Exist(im.t.ctx)
}

func splitPath(v string) []string {
	parts := strings.FieldsFunc(v, func(r rune) bool { return r == '/' || r == '>' || r == '›' })
	out := []string{}
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func (im *importer) checkCustodian(r *importRow) error {
	name := r.v["custodian"]
	if name == "" {
		return nil
	}
	if r.kind == "space" || r.kind == "group" {
		im.warnf(r.line, "공간과 그룹은 사람에게 지급하지 않아 사용자를 비워 두었습니다")
		return nil
	}
	t := im.t
	ps, err := t.db.Party.Query().Where(
		party.TenantId(t.tenant.Uuid()),
		party.KindNEQ("vendor"),
		party.DateErasedIsNil(),
		party.Or(party.EmailEqualFold(name), party.Name(name)),
	).All(t.ctx)
	if err != nil {
		return err
	}
	switch len(ps) {
	case 0:
		im.warnf(r.line, "사용자 %q를 찾지 못해 비워 두었습니다 (먼저 사람을 등록하세요)", name)
		return nil
	case 1:
	default:
		im.warnf(r.line, "사용자 %q가 여럿이라 비워 두었습니다 (이메일로 적어 주세요)", name)
		return nil
	}
	p := ps[0]
	if r.cur != nil && r.cur.CustodianId != (uuid.UUID{}) {
		if r.cur.CustodianId != p.Id {
			im.warnf(r.line, "%s는 이미 다른 사람이 가지고 있어 사용자를 바꾸지 않았습니다 (반납 후 지급하세요)", r.cur.Tag)
		}
		return nil
	}
	r.custodian = p.Id
	return nil
}

// write does what the sheet says: what holds things before what goes in them,
// pass by pass, until a pass takes nothing.
func (im *importer) write() error {
	rank := map[string]int{"space": 0, "group": 1, "kit": 2, "item": 3}
	pending := slices.Clone(im.rows)
	slices.SortStableFunc(pending, func(a, b *importRow) int { return rank[a.kind] - rank[b.kind] })
	for len(pending) > 0 {
		left := []*importRow{}
		for _, r := range pending {
			if im.blocked(r) {
				left = append(left, r)
				continue
			}
			if err := im.one(r); err != nil {
				return rowError(r.line, err)
			}
		}
		if len(left) == len(pending) {
			// What is left waits on itself; take it in order, and let paths
			// make the spaces they name.
			for _, r := range left {
				if err := im.one(r); err != nil {
					return rowError(r.line, err)
				}
			}
			break
		}
		pending = left
	}

	for _, p := range im.order {
		lines := []*app.CustodyLineSpec{}
		for _, a := range im.issue[p] {
			lines = append(lines, app.CustodyLineSpec_builder{Asset: assetRef(a)}.Build())
		}
		if _, err := im.t.layer().Custody().Add(im.t.ctx, app.CustodyAddRequest_builder{
			Party: app.PartyRef_builder{Id: pdid.Id(p).Bytes()}.Build(),
			Kind:  "issue",
			Desc:  "가져오기",
			Lines: lines,
		}.Build()); err != nil {
			return err
		}
	}
	return nil
}

func rowError(line int, err error) error {
	st := status.Convert(err)
	return status.Errorf(st.Code(), "%d행: %s", line, st.Message())
}

// blocked answers whether a row waits on a space another row makes.
func (im *importer) blocked(r *importRow) bool {
	if r.loc != "" {
		p, ok := im.byTag[r.loc]
		return ok && p != r && !p.done
	}
	for _, seg := range r.path {
		n := im.waiting[seg]
		if r.kind == "space" && !r.done && r.v["name"] == seg {
			n--
		}
		if n > 0 {
			return true
		}
	}
	return false
}

func (im *importer) one(r *importRow) error {
	defer func() {
		r.done = true
		if r.kind == "space" {
			im.waiting[r.v["name"]]--
		}
	}()

	t := im.t
	layer := t.layer()
	at := t.now
	if r.since != nil {
		at = *r.since
	}

	parent, err := im.place(r, at)
	if err != nil {
		return err
	}
	ty, err := im.typeOf(r)
	if err != nil {
		return err
	}
	model, err := im.modelOf(r, ty)
	if err != nil {
		return err
	}

	if r.cur == nil {
		add := app.AssetAddRequest_builder{
			Tag:        r.v["tag"],
			Name:       r.v["name"],
			Kind:       r.kind,
			Serial:     r.v["serial"],
			Status:     r.v["status"],
			Condition:  r.v["condition"],
			Desc:       r.v["desc"],
			Attributes: r.attrs,
			Since:      ts(at),
			Reason:     z.Ptr("가져오기"),
		}.Build()
		if ty != (uuid.UUID{}) {
			add.SetType(app.AssetTypeRef_builder{Id: pdid.Id(ty).Bytes()}.Build())
		}
		if model != (uuid.UUID{}) {
			add.SetModel(app.ItemModelRef_builder{Id: pdid.Id(model).Bytes()}.Build())
		}
		if r.acquired != nil {
			add.SetAcquiredAt(ts(*r.acquired))
		}
		if parent != (uuid.UUID{}) {
			add.SetTo(assetRef(parent))
		}
		a, err := layer.Asset().Add(t.ctx, add)
		if err != nil {
			return err
		}
		im.out.SetCreated(im.out.GetCreated() + 1)
		im.handOver(r, uuidOf(a.GetId()))
		return nil
	}

	cur := r.cur
	changed := false
	set := map[string]string{}
	have := map[string]string{"name": cur.Name, "serial": cur.Serial, "status": cur.Status, "condition": cur.Condition, "desc": cur.Desc}
	for k, now := range have {
		if v, ok := r.v[k]; ok && v != now {
			set[k] = v
		}
	}
	if ty != (uuid.UUID{}) && ty != cur.TypeId {
		set["type"] = pdid.Id(ty).String()
	}
	if model != (uuid.UUID{}) && model != cur.ModelId {
		set["model"] = pdid.Id(model).String()
	}
	for k, v := range r.attrs {
		if cur.Attributes[k] != v {
			set["attr."+k] = v
		}
	}
	if len(set) > 0 {
		if _, err := layer.Asset().SetAttributes(t.ctx, app.AssetSetAttributesRequest_builder{
			Ref:    assetRef(cur.Id),
			Set:    set,
			At:     ts(at),
			Reason: "가져오기",
		}.Build()); err != nil {
			return err
		}
		changed = true
	}
	if parent != (uuid.UUID{}) && (cur.ParentId == nil || *cur.ParentId != parent) {
		if _, err := layer.Asset().Move(t.ctx, app.AssetMoveRequest_builder{
			Ref:    assetRef(cur.Id),
			To:     assetRef(parent),
			At:     ts(at),
			Reason: "가져오기",
		}.Build()); err != nil {
			return err
		}
		changed = true
	}
	if r.acquired != nil && (cur.AcquiredAt == nil || !cur.AcquiredAt.Equal(*r.acquired)) {
		if _, err := t.next.Asset().Patch(t.ctx, app.AssetPatchRequest_builder{
			Ref:              assetRef(cur.Id),
			AcquiredAt:       ts(*r.acquired),
			DateUpdatedForce: z.Ptr(true),
		}.Build()); err != nil {
			return err
		}
		changed = true
	}
	if r.custodian != (uuid.UUID{}) {
		im.handOver(r, cur.Id)
		changed = true
	}
	if changed {
		im.out.SetUpdated(im.out.GetUpdated() + 1)
	} else {
		im.out.SetSkipped(im.out.GetSkipped() + 1)
	}
	return nil
}

func (im *importer) handOver(r *importRow, id uuid.UUID) {
	p := r.custodian
	if p == (uuid.UUID{}) {
		return
	}
	if _, ok := im.issue[p]; !ok {
		im.order = append(im.order, p)
	}
	im.issue[p] = append(im.issue[p], id)
}

// place answers the asset a row goes into, making the spaces of a path that
// are not there yet.
func (im *importer) place(r *importRow, at time.Time) (uuid.UUID, error) {
	t := im.t
	if r.loc != "" {
		a, err := t.db.Asset.Query().Where(asset.TenantId(t.tenant.Uuid()), asset.Tag(r.loc), asset.DateErasedIsNil()).Only(t.ctx)
		if err != nil {
			if ent.IsNotFound(err) {
				return uuid.UUID{}, failed("위치 %q를 찾지 못했습니다", r.loc)
			}
			return uuid.UUID{}, err
		}
		return a.Id, nil
	}
	if len(r.path) == 0 {
		return uuid.UUID{}, nil
	}

	var parent *uuid.UUID
	for i, seg := range r.path {
		key := strings.Join(r.path[:i+1], "/")
		if id, ok := im.paths[key]; ok {
			parent = &id
			continue
		}
		q := t.db.Asset.Query().Where(asset.TenantId(t.tenant.Uuid()), asset.Kind("space"), asset.Name(seg), asset.DateErasedIsNil())
		if parent == nil {
			q = q.Where(asset.ParentIdIsNil())
		} else {
			q = q.Where(asset.ParentId(*parent))
		}
		ids, err := q.Ids(t.ctx)
		if err != nil {
			return uuid.UUID{}, err
		}
		var id uuid.UUID
		switch len(ids) {
		case 0:
			add := app.AssetAddRequest_builder{
				Name:   seg,
				Kind:   "space",
				Since:  ts(at),
				Reason: z.Ptr("가져오기: " + key),
			}.Build()
			if parent != nil {
				add.SetTo(assetRef(*parent))
			}
			a, err := t.layer().Asset().Add(t.ctx, add)
			if err != nil {
				return uuid.UUID{}, err
			}
			id = uuidOf(a.GetId())
			im.warnf(0, "공간 %q를 새로 만들었습니다", key)
		case 1:
			id = ids[0]
		default:
			return uuid.UUID{}, failed("위치 %q에 이름이 같은 공간이 여럿입니다; 위치태그로 적어 주세요", key)
		}
		im.paths[key] = id
		parent = &id
	}
	return *parent, nil
}

func (im *importer) typeOf(r *importRow) (uuid.UUID, error) {
	name := r.v["type"]
	if name == "" {
		return uuid.UUID{}, nil
	}
	key := strings.ToLower(name)
	if id, ok := im.types[key]; ok {
		return id, nil
	}
	t := im.t
	ty, err := t.db.AssetType.Query().Where(assettype.TenantId(t.tenant.Uuid()), assettype.NameEqualFold(name), assettype.DateErasedIsNil()).First(t.ctx)
	var id uuid.UUID
	switch {
	case err == nil:
		id = ty.Id
	case ent.IsNotFound(err):
		v, err := t.layer().AssetType().Add(t.ctx, app.AssetTypeAddRequest_builder{Name: name, Kind: r.kind}.Build())
		if err != nil {
			return uuid.UUID{}, err
		}
		id = uuidOf(v.GetId())
		im.warnf(0, "유형 %q를 새로 만들었습니다", name)
	default:
		return uuid.UUID{}, err
	}
	im.types[key] = id
	return id, nil
}

func (im *importer) modelOf(r *importRow, ty uuid.UUID) (uuid.UUID, error) {
	name := r.v["model"]
	if name == "" {
		return uuid.UUID{}, nil
	}
	maker := r.v["maker"]
	key := strings.ToLower(maker) + "\x00" + strings.ToLower(name)
	if id, ok := im.models[key]; ok {
		return id, nil
	}
	t := im.t
	q := t.db.ItemModel.Query().Where(itemmodel.TenantId(t.tenant.Uuid()), itemmodel.NameEqualFold(name), itemmodel.DateErasedIsNil())
	if maker != "" {
		q = q.Where(itemmodel.MakerEqualFold(maker))
	}
	m, err := q.First(t.ctx)
	var id uuid.UUID
	switch {
	case err == nil:
		id = m.Id
	case ent.IsNotFound(err):
		add := app.ItemModelAddRequest_builder{Tenant: t.tenantRef(), Name: name, Maker: maker}.Build()
		if ty != (uuid.UUID{}) {
			add.SetType(app.AssetTypeRef_builder{Id: pdid.Id(ty).Bytes()}.Build())
		}
		v, err := t.layer().ItemModel().Add(t.ctx, add)
		if err != nil {
			return uuid.UUID{}, err
		}
		id = uuidOf(v.GetId())
		im.warnf(0, "모델 %q를 새로 만들었습니다", strings.TrimSpace(maker+" "+name))
	default:
		return uuid.UUID{}, err
	}
	im.models[key] = id
	return id, nil
}

// Export writes the register as a sheet that Import reads back: the same
// columns, the same words, and the place as a path a person can read beside
// the tag a machine can.
func (s domainAsset) Export(ctx context.Context, req *app.AssetExportRequest) (*app.AssetExportResponse, error) {
	t, err := s.read(ctx)
	if err != nil {
		return nil, err
	}
	format := strings.ToLower(or(req.GetFormat(), "csv"))
	if format != "csv" && format != "xlsx" {
		return nil, invalid("format", "CSV 또는 엑셀(xlsx)만 됩니다")
	}

	as, err := t.db.Asset.Query().Where(asset.TenantId(t.tenant.Uuid()), asset.DateErasedIsNil()).All(ctx)
	if err != nil {
		return nil, err
	}
	types, err := t.db.AssetType.Query().Where(assettype.TenantId(t.tenant.Uuid())).All(ctx)
	if err != nil {
		return nil, err
	}
	models, err := t.db.ItemModel.Query().Where(itemmodel.TenantId(t.tenant.Uuid())).All(ctx)
	if err != nil {
		return nil, err
	}
	parties, err := t.db.Party.Query().Where(party.TenantId(t.tenant.Uuid())).All(ctx)
	if err != nil {
		return nil, err
	}

	byId := map[uuid.UUID]*ent.Asset{}
	for _, a := range as {
		byId[a.Id] = a
	}
	typeName := map[uuid.UUID]string{}
	for _, v := range types {
		typeName[v.Id] = v.Name
	}
	model := map[uuid.UUID]*ent.ItemModel{}
	for _, v := range models {
		model[v.Id] = v
	}
	partyName := map[uuid.UUID]string{}
	for _, v := range parties {
		partyName[v.Id] = or(v.Email, v.Name)
	}
	path := func(a *ent.Asset) string {
		names := []string{}
		for p, n := a.ParentId, 0; p != nil && n < 64; n++ {
			v, ok := byId[*p]
			if !ok {
				break
			}
			names = append([]string{v.Name}, names...)
			p = v.ParentId
		}
		return strings.Join(names, "/")
	}

	keys := map[string]bool{}
	for _, a := range as {
		for k := range a.Attributes {
			keys[k] = true
		}
	}
	attrs := sortedKeys(keys)

	rank := map[string]int{"space": 0, "group": 1, "kit": 2, "item": 3}
	slices.SortStableFunc(as, func(a, b *ent.Asset) int {
		if d := rank[a.Kind] - rank[b.Kind]; d != 0 {
			return d
		}
		if a.Kind == "space" {
			if d := strings.Compare(path(a)+"/"+a.Name, path(b)+"/"+b.Name); d != 0 {
				return d
			}
		}
		return strings.Compare(a.Tag, b.Tag)
	})

	header := []string{"태그", "이름", "종류", "유형", "모델", "제조사", "시리얼", "상태", "컨디션", "위치", "위치태그", "사용자", "취득일", "설명"}
	for _, k := range attrs {
		header = append(header, "속성."+k)
	}
	rows := [][]string{header}
	for _, a := range as {
		var mName, maker, loc, acquired string
		if m, ok := model[a.ModelId]; ok {
			mName, maker = m.Name, m.Maker
		}
		if a.ParentId != nil {
			if p, ok := byId[*a.ParentId]; ok {
				loc = p.Tag
			}
		}
		if a.AcquiredAt != nil {
			acquired = a.AcquiredAt.In(Zone).Format(time.DateOnly)
		}
		row := []string{
			a.Tag, a.Name, or(kindSay[a.Kind], a.Kind), typeName[a.TypeId], mName, maker, a.Serial,
			or(statusSay[a.Status], a.Status), or(conditionSay[a.Condition], a.Condition),
			path(a), loc, partyName[a.CustodianId], acquired, a.Desc,
		}
		for _, k := range attrs {
			row = append(row, a.Attributes[k])
		}
		rows = append(rows, row)
	}

	name := "rove-assets-" + t.now.In(Zone).Format("20060102")
	if format == "csv" {
		b := bytes.Buffer{}
		b.WriteString("\ufeff")
		w := csv.NewWriter(&b)
		if err := w.WriteAll(rows); err != nil {
			return nil, err
		}
		return app.AssetExportResponse_builder{
			Data:        b.Bytes(),
			ContentType: "text/csv; charset=utf-8",
			Name:        name + ".csv",
		}.Build(), nil
	}

	f := excelize.NewFile()
	defer f.Close()
	sheet := "자산"
	if err := f.SetSheetName("Sheet1", sheet); err != nil {
		return nil, err
	}
	for i, row := range rows {
		cell, err := excelize.CoordinatesToCellName(1, i+1)
		if err != nil {
			return nil, err
		}
		vs := make([]any, len(row))
		for j, v := range row {
			vs[j] = v
		}
		if err := f.SetSheetRow(sheet, cell, &vs); err != nil {
			return nil, err
		}
	}
	bold, err := f.NewStyle(&excelize.Style{Font: &excelize.Font{Bold: true}})
	if err == nil {
		last, _ := excelize.CoordinatesToCellName(len(header), 1)
		_ = f.SetCellStyle(sheet, "A1", last, bold)
	}
	_ = f.SetPanes(sheet, &excelize.Panes{Freeze: true, YSplit: 1, TopLeftCell: "A2", ActivePane: "bottomLeft"})
	b, err := f.WriteToBuffer()
	if err != nil {
		return nil, err
	}
	return app.AssetExportResponse_builder{
		Data:        b.Bytes(),
		ContentType: "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet",
		Name:        name + ".xlsx",
	}.Build(), nil
}
