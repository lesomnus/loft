package domain

import (
	"bytes"
	"context"
	"encoding/csv"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/lesomnus/z"
	"golang.org/x/text/encoding/korean"
	"golang.org/x/text/transform"
	"google.golang.org/protobuf/types/known/timestamppb"

	app "github.com/lesomnus/loft"
)

// The form bulk registration reads (design 9.13): its columns, in the order
// the template writes them, and what else a header may say for each -- the
// export's words, and a few a register kept elsewhere uses. What each takes
// is said on the page that hands the form out (`ts/src/pages/register.tsx`).
var registerColumns = []struct {
	key   string
	label string
	also  []string
}{
	{"name", "이름", []string{"name", "자산명", "품명"}},
	{"kind", "종류", []string{"kind", "구분"}},
	{"type", "유형", []string{"type", "분류"}},
	{"model", "모델", []string{"model", "모델명"}},
	{"maker", "제조사", []string{"maker"}},
	{"serial", "시리얼", []string{"serial", "일련번호", "s/n"}},
	{"tag", "태그", []string{"tag", "자산번호", "관리번호"}},
	{"location_tag", "위치태그", []string{"location_tag", "위치 태그"}},
	{"acquired_at", "취득일", []string{"acquired_at", "구매일"}},
	{"since", "기준일", []string{"since", "입고일"}},
	{"desc", "설명", []string{"desc", "비고", "메모"}},
	{"status", "상태", []string{"status"}},
	{"condition", "컨디션", []string{"condition"}},
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

// The Korean words for loft's own values, as a sheet writes them.
var (
	kindWords      = map[string]string{"물품": "item", "자산": "item", "공간": "space", "키트": "kit", "그룹": "group"}
	statusWords    = map[string]string{"주문": "ordered", "사용중": "active", "사용 중": "active", "운용": "active", "수리중": "in_repair", "수리 중": "in_repair", "분실": "lost", "불용": "retired", "폐기": "disposed"}
	conditionWords = map[string]string{"양호": "good", "보통": "fair", "손상": "damaged", "고장": "broken"}
)

// word is loft's value for what a sheet says: its Korean word, or the value
// itself.
func word(v string, words map[string]string) string {
	v = strings.TrimSpace(v)
	if w, ok := words[v]; ok {
		return w
	}
	return strings.ToLower(v)
}

// registerForm is the template: the form's header, as a CSV.
func registerForm() (*app.AssetExportResponse, error) {
	header := []string{}
	for _, c := range registerColumns {
		header = append(header, c.label)
	}
	header = append(header, "속성.키")

	return writeCSV("loft-대량등록-양식", [][]string{header})
}

// What a workbook begins with: a zip (.xlsx), or the OLE container an older
// one (.xls) and an encrypted one are.
var workbookMarks = [][]byte{[]byte("PK\x03\x04"), []byte("\xd0\xcf\x11\xe0\xa1\xb1\x1a\xe1")}

// exponent is a number as Excel writes a long one into a CSV, when its cell
// is not text: 3.57951E+14, the digits after the sixth gone.
var exponent = regexp.MustCompile(`^[0-9]+(\.[0-9]+)?[Ee]\+[0-9]+$`)

// ReadSheet reads a CSV in the form into the items an AddMany takes. It
// writes nothing, and does not look at the tenant: whether a tag is taken or
// a place is there is the AddMany's to say, in its dry run.
func (s domainAsset) ReadSheet(ctx context.Context, req *app.AssetReadSheetRequest) (*app.AssetReadSheetResponse, error) {
	t, err := s.read(ctx)
	if err != nil {
		return nil, err
	}
	// A date in a cell is a day where the workspace is (#144).
	zone := t.zone()

	// A workbook is not opened to be read: what one person uploads is read in
	// the process every workspace is served by, reading a workbook takes a
	// library as large as Excel -- formulas, encryption, the container an old
	// one is in -- and a crafted workbook stops it. Loft has none (design
	// 9.13).
	for _, mark := range workbookMarks {
		if bytes.HasPrefix(req.GetData(), mark) {
			return nil, invalid("data", "엑셀 파일은 받지 않습니다. 엑셀에서 파일 형식을 'CSV UTF-8(쉼표로 분리)'로 골라 저장한 파일을 올리세요")
		}
	}
	table, err := readCSV(req.GetData())
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
			if !isAttributeKey(a) {
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
			column := strings.TrimSpace(table[0][i])
			// An identifier Excel took for a number and wrote short: the
			// digits it dropped are not in the file to be read back.
			if (attr[i] != "" || keys[i] == "serial" || keys[i] == "tag") && exponent.MatchString(v) {
				say(line, column, "%q는 엑셀이 긴 숫자를 줄여 쓴 것입니다. 그 열의 셀 서식을 텍스트로 바꾸고 다시 적으세요", v)
				ok = false
				continue
			}
			if attr[i] != "" {
				attrs[attr[i]] = v
				continue
			}
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
				at, err := parseDate(v, zone)
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

// readCSV reads a CSV in UTF-8, or in the CP949 that Korean Excel saves by
// default.
func readCSV(data []byte) ([][]string, error) {
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

var dateLayouts = []string{
	"2006-01-02", "2006.01.02", "2006/01/02", "20060102",
	"2006-1-2", "2006.1.2", "2006/1/2", "2006. 1. 2", "2006. 01. 02",
	"2006-01-02 15:04", "2006-01-02 15:04:05", "2006-01-02T15:04:05Z07:00",
	"1/2/06", "1/2/2006",
}

// parseDate reads a date as people write one, and as Excel writes one into a
// CSV in Korean or in English.
func parseDate(v string, zone *time.Location) (time.Time, error) {
	v = strings.TrimSuffix(strings.TrimSpace(v), ".")
	for _, l := range dateLayouts {
		if t, err := time.ParseInLocation(l, v, zone); err == nil {
			return t, nil
		}
	}
	return time.Time{}, fmt.Errorf("%q는 날짜가 아닙니다 (2024-03-01처럼 적어 주세요)", v)
}
