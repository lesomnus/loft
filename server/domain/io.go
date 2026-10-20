package domain

import (
	"bytes"
	"context"
	"encoding/csv"
	"errors"
	"slices"
	"strings"
	"time"
	"uuid"

	"github.com/xuri/excelize/v2"

	app "github.com/lesomnus/rove"
	"github.com/lesomnus/rove/internal/ent"
	"github.com/lesomnus/rove/internal/ent/asset"
	"github.com/lesomnus/rove/internal/ent/assettype"
	"github.com/lesomnus/rove/internal/ent/itemmodel"
	"github.com/lesomnus/rove/internal/ent/party"
)

// Zone is where a date is written without a zone: the pilot's, until a tenant
// says otherwise.
var Zone = time.FixedZone("KST", 9*60*60)

// The Korean words for rove's own values, which export writes and the pages
// and refusals say.
var (
	kindSay        = map[string]string{"item": "물품", "space": "공간", "kit": "키트", "group": "그룹"}
	reservationSay = map[string]string{"held": "임시 홀드", "requested": "승인 대기", "confirmed": "확정", "in_use": "이용 중", "completed": "완료", "cancelled": "취소", "rejected": "거절", "expired": "만료", "no_show": "노쇼"}
	custodySay     = map[string]string{"open": "진행 중", "returned": "반납 완료"}
	purchaseSay    = map[string]string{"ordered": "주문", "received": "입고 완료", "cancelled": "취소"}
	statusSay      = map[string]string{"ordered": "주문", "active": "사용중", "in_repair": "수리중", "lost": "분실", "retired": "불용", "disposed": "폐기"}
	conditionSay   = map[string]string{"good": "양호", "damaged": "손상", "broken": "고장"}
)

// errDry is how a dry run ends: everything was done, and is rolled back.
var errDry = errors.New("domain: dry run")

// Export writes the register as a sheet to read: a row an asset, in the words
// a person reads, and the place as a path beside the tag a machine can.
//
// Nothing reads one back. A sheet that edited the register by tag made a
// value people change into what an asset is, so that another workspace's
// sheet edited whatever shared a tag; a workspace is restored from its own
// export instead (design D34).
func (s domainAsset) Export(ctx context.Context, req *app.AssetExportRequest) (*app.AssetExportResponse, error) {
	t, err := s.read(ctx)
	if err != nil {
		return nil, err
	}
	format := strings.ToLower(or(req.GetFormat(), "csv"))
	if format != "csv" && format != "xlsx" {
		return nil, invalid("format", "CSV 또는 엑셀(xlsx)만 됩니다")
	}
	if req.GetTemplate() {
		if format != "csv" {
			return nil, invalid("format", "대량 등록 양식은 CSV입니다")
		}
		return registerForm()
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

	return writeSheet(format, "rove-assets-"+t.now.In(Zone).Format("20060102"), "자산", rows)
}

// writeSheet is `rows` as a file of `format`: a CSV with the mark Excel reads
// it as UTF-8 by, or a workbook whose header is bold and stays in view.
func writeSheet(format, name, sheet string, rows [][]string) (*app.AssetExportResponse, error) {
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
	if err := f.SetSheetName("Sheet1", sheet); err != nil {
		return nil, err
	}
	width := 0
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
		width = max(width, len(row))
	}
	bold, err := f.NewStyle(&excelize.Style{Font: &excelize.Font{Bold: true}})
	if err == nil && width > 0 {
		last, _ := excelize.CoordinatesToCellName(width, 1)
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
