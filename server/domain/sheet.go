package domain

import (
	"bytes"
	"context"
	"encoding/csv"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/lesomnus/z"
	"github.com/xuri/excelize/v2"
	"golang.org/x/text/encoding/korean"
	"golang.org/x/text/transform"
	"google.golang.org/protobuf/types/known/timestamppb"

	app "github.com/lesomnus/rove"
)

// The form bulk registration reads (design 9.13): its columns, in the order
// the template writes them, what each is, and what else a header may say for
// it -- the export's words, and a few a register kept elsewhere uses.
var registerColumns = []struct {
	key   string
	label string
	about string
	also  []string
}{
	{"name", "이름", "필수", []string{"name", "자산명", "품명"}},
	{"kind", "종류", "물품, 공간, 키트, 그룹. 비우면 유형의 것, 그것도 없으면 물품", []string{"kind", "구분"}},
	{"type", "유형", "이름으로. 없는 이름이면 새로 만듭니다(관리자만)", []string{"type", "분류"}},
	{"model", "모델", "이름으로. 없는 이름이면 새로 만듭니다", []string{"model", "모델명"}},
	{"maker", "제조사", "모델을 찾고 만들 때 함께 봅니다", []string{"maker"}},
	{"serial", "시리얼", "", []string{"serial", "일련번호", "s/n"}},
	{"tag", "태그", "비우면 정해 줍니다. 이미 있는 태그는 등록하지 않습니다", []string{"tag", "자산번호", "관리번호"}},
	{"location_tag", "위치태그", "이미 있는 공간의 태그. 공간을 새로 만들지 않습니다", []string{"location_tag", "위치 태그"}},
	{"acquired_at", "취득일", "2024-03-01처럼", []string{"acquired_at", "구매일"}},
	{"since", "기준일", "입고일. 이력이 이날부터입니다. 비우면 오늘", []string{"since", "입고일"}},
	{"desc", "설명", "", []string{"desc", "비고", "메모"}},
	{"status", "상태", "사용중, 주문, 수리중, 분실, 불용, 폐기. 비우면 사용중", []string{"status"}},
	{"condition", "컨디션", "양호, 손상, 고장. 비우면 양호", []string{"condition"}},
}

// What a header may say that the form does not take, and why.
var refusedColumns = map[string]string{
	"위치":        "위치는 경로가 아니라 위치태그로 적습니다",
	"location":  "위치는 경로가 아니라 위치태그로 적습니다",
	"장소":        "위치는 경로가 아니라 위치태그로 적습니다",
	"사용자":       "사용자는 받지 않습니다. 지급은 등록한 뒤에 합니다",
	"담당자":       "사용자는 받지 않습니다. 지급은 등록한 뒤에 합니다",
	"custodian": "사용자는 받지 않습니다. 지급은 등록한 뒤에 합니다",
}

// attributePrefixes begin a header that is an attribute: `속성.색상`.
var attributePrefixes = []string{"속성.", "attr.", "attributes."}

// The Korean words for rove's own values, as a sheet writes them.
var (
	kindWords      = map[string]string{"물품": "item", "자산": "item", "공간": "space", "키트": "kit", "그룹": "group"}
	statusWords    = map[string]string{"주문": "ordered", "사용중": "active", "사용 중": "active", "운용": "active", "수리중": "in_repair", "수리 중": "in_repair", "분실": "lost", "불용": "retired", "폐기": "disposed"}
	conditionWords = map[string]string{"양호": "good", "보통": "fair", "손상": "damaged", "고장": "broken"}
)

// word is rove's value for what a sheet says: its Korean word, or the value
// itself.
func word(v string, words map[string]string) string {
	v = strings.TrimSpace(v)
	if w, ok := words[v]; ok {
		return w
	}
	return strings.ToLower(v)
}

// registerForm is the template: the form's header, alone, and in a workbook a
// sheet beside it saying what each column is, with a row filled in.
func registerForm(format string) (*app.AssetExportResponse, error) {
	header := []string{}
	for _, c := range registerColumns {
		header = append(header, c.label)
	}
	header = append(header, "속성.키")

	return writeSheet(format, "rove-대량등록-양식", "등록", [][]string{header}, func(f *excelize.File) error {
		const sheet = "읽어 주세요"
		if _, err := f.NewSheet(sheet); err != nil {
			return err
		}
		rows := [][]string{{"열", "적는 것"}}
		for _, c := range registerColumns {
			rows = append(rows, []string{c.label, c.about})
		}
		rows = append(rows,
			[]string{"속성.키", "유형의 속성. 열 이름을 '속성.' 다음에 속성 키로 적습니다 (예: 속성.색상)"},
			[]string{},
			[]string{"예", ""},
		)
		example := []string{"노트북", "물품", "노트북", "ThinkPad X1 Carbon", "Lenovo", "PF-12345", "", "SP-01", "2024-03-01", "", "개발팀 지급용"}
		for i, c := range registerColumns[:len(example)] {
			rows = append(rows, []string{c.label, example[i]})
		}
		rows = append(rows,
			[]string{},
			[]string{"'등록' 시트에 한 줄에 자산 하나씩 적습니다. 새 자산만 등록하고, 이미 있는 자산은 고치지 않습니다."},
		)
		return setRows(f, sheet, rows)
	})
}

// ReadSheet reads a sheet in the form into the items an AddMany takes. It
// writes nothing, and does not look at the tenant: whether a tag is taken or
// a place is there is the AddMany's to say, in its dry run.
func (s domainAsset) ReadSheet(ctx context.Context, req *app.AssetReadSheetRequest) (*app.AssetReadSheetResponse, error) {
	if _, err := s.read(ctx); err != nil {
		return nil, err
	}

	format := strings.ToLower(req.GetFormat())
	if format == "" {
		format = "csv"
		if bytes.HasPrefix(req.GetData(), []byte("PK\x03\x04")) {
			format = "xlsx"
		}
	}
	table, err := readTable(format, req.GetData())
	if err != nil {
		return nil, invalid("data", "시트를 읽을 수 없습니다: %v", err)
	}

	res := app.AssetReadSheetResponse_builder{}.Build()
	say := func(line int, column, format string, args ...any) {
		res.SetIssues(append(res.GetIssues(), app.AssetReadSheetIssue_builder{
			Line:    uint32(line),
			Column:  column,
			Message: fmt.Sprintf(format, args...),
		}.Build()))
	}
	if len(table) == 0 {
		say(0, "", "빈 시트입니다")
		return res, nil
	}

	// What each column is: a field of the form, an attribute, or nothing.
	byLabel := map[string]string{}
	for _, c := range registerColumns {
		byLabel[c.label] = c.key
		for _, v := range c.also {
			byLabel[strings.ToLower(v)] = c.key
		}
	}
	keys := make([]string, len(table[0]))
	attr := make([]string, len(table[0]))
	// The form's own `속성.키`, which says how an attribute column is named.
	// Left as it is, it is nothing; written in, it was meant to be renamed.
	placeholder := map[int]bool{}
	named := false
	for i, h := range table[0] {
		h = strings.TrimSpace(h)
		if h == "" {
			continue
		}
		if k, ok := byLabel[h]; ok {
			keys[i] = k
		} else if k, ok := byLabel[strings.ToLower(h)]; ok {
			keys[i] = k
		} else if why, ok := refusedColumns[strings.ToLower(h)]; ok {
			say(0, h, "%s", why)
			continue
		} else if a, ok := attributeOf(h); ok {
			if a == "키" {
				placeholder[i] = true
				continue
			}
			if a == "" || strings.ContainsAny(a, " .") {
				say(0, h, "속성 열은 '속성.' 다음에 띄어쓰기나 점 없는 키를 적습니다")
				continue
			}
			attr[i] = a
			continue
		} else {
			say(0, h, "알 수 없는 열이라 읽지 않았습니다")
			continue
		}
		named = named || keys[i] == "name"
	}
	if !named {
		say(0, "이름", "이름 열이 없습니다. 양식의 첫 줄을 그대로 두세요")
		return res, nil
	}

	for n, row := range table[1:] {
		line := n + 2
		if blank(row) {
			continue
		}
		if len(res.GetItems()) == MaxRegisterRows {
			say(line, "", "한 번에 %d행까지 등록합니다. 여기부터는 나눠서 등록하세요", MaxRegisterRows)
			break
		}

		item := app.AssetAddRequest_builder{}.Build()
		ok := true
		attrs := map[string]string{}
		for i, cell := range row {
			if i >= len(keys) {
				break
			}
			v := strings.TrimSpace(cell)
			if v == "" {
				continue
			}
			if placeholder[i] {
				say(line, strings.TrimSpace(table[0][i]), "열 이름 '속성.키'의 '키'를 속성의 키로 바꿔 적으세요 (예: 속성.색상)")
				delete(placeholder, i)
				continue
			}
			if attr[i] != "" {
				attrs[attr[i]] = v
				continue
			}
			column := strings.TrimSpace(table[0][i])
			switch keys[i] {
			case "name":
				item.SetName(v)
			case "kind":
				item.SetKind(word(v, kindWords))
			case "type":
				item.SetTypeName(v)
			case "model":
				item.SetModelName(v)
			case "maker":
				item.SetMaker(v)
			case "serial":
				item.SetSerial(v)
			case "tag":
				item.SetTag(v)
			case "location_tag":
				item.SetTo(app.AssetRef_builder{Tag: app.AssetRefByTag_builder{Tag: z.Ptr(v)}.Build()}.Build())
			case "acquired_at", "since":
				at, err := parseDate(v)
				if err != nil {
					say(line, column, "%s", err)
					ok = false
					continue
				}
				if keys[i] == "since" {
					item.SetSince(timestamppb.New(at))
				} else {
					item.SetAcquiredAt(timestamppb.New(at))
				}
			case "desc":
				item.SetDesc(v)
			case "status":
				item.SetStatus(word(v, statusWords))
			case "condition":
				item.SetCondition(word(v, conditionWords))
			}
		}
		if !ok {
			continue
		}
		if len(attrs) > 0 {
			item.SetAttributes(attrs)
		}
		res.SetItems(append(res.GetItems(), item))
		res.SetLines(append(res.GetLines(), uint32(line)))
	}
	return res, nil
}

// attributeOf is the attribute key a header names, if it names one.
func attributeOf(header string) (string, bool) {
	for _, p := range attributePrefixes {
		if v, ok := strings.CutPrefix(header, p); ok {
			return strings.TrimSpace(v), true
		}
	}
	return "", false
}

func blank(row []string) bool {
	for _, v := range row {
		if strings.TrimSpace(v) != "" {
			return false
		}
	}
	return true
}

// readTable reads the first sheet of a workbook, or a CSV in UTF-8 or in the
// CP949 that Korean Excel saves by default.
func readTable(format string, data []byte) ([][]string, error) {
	switch format {
	case "xlsx":
		f, err := excelize.OpenReader(bytes.NewReader(data))
		if err != nil {
			return nil, fmt.Errorf("엑셀(xlsx) 파일이 아닙니다: %w", err)
		}
		defer f.Close()
		sheets := f.GetSheetList()
		if len(sheets) == 0 {
			return nil, errors.New("시트가 없습니다")
		}
		return f.GetRows(sheets[0])

	case "csv":
		data = bytes.TrimPrefix(data, []byte("\xef\xbb\xbf"))
		if !utf8.Valid(data) {
			v, _, err := transform.Bytes(korean.EUCKR.NewDecoder(), data)
			if err != nil {
				return nil, errors.New("UTF-8도 CP949도 아닙니다")
			}
			data = v
		}
		r := csv.NewReader(bytes.NewReader(data))
		r.FieldsPerRecord = -1
		r.LazyQuotes = true
		return r.ReadAll()
	}
	return nil, fmt.Errorf("형식 %q는 csv나 xlsx입니다", format)
}

var dateLayouts = []string{
	"2006-01-02", "2006.01.02", "2006/01/02", "20060102",
	"2006-1-2", "2006.1.2", "2006/1/2", "2006. 1. 2", "2006. 01. 02",
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
