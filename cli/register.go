package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/lesomnus/xli"
	"github.com/lesomnus/xli/arg"
	"github.com/lesomnus/xli/flg"

	app "github.com/lesomnus/rove"
)

// columnOf is the form's column for what an AddMany calls a field, so that
// what it says of an item is said of a sheet's line where a person put it.
var columnOf = map[string]string{
	"name":        "이름",
	"kind":        "종류",
	"type":        "유형",
	"type_name":   "유형",
	"model":       "모델",
	"model_name":  "모델",
	"maker":       "제조사",
	"serial":      "시리얼",
	"tag":         "태그",
	"to":          "위치태그",
	"acquired_at": "취득일",
	"since":       "기준일",
	"desc":        "설명",
	"status":      "상태",
	"condition":   "컨디션",
	"attributes":  "속성",
}

// NewCmdRegister is `rove register FILE`: new assets from a CSV in rove's
// form, as one intake (design 9.13). The sheet is read by the deployment --
// the form is read in one place, and the page reads it there too -- and what
// would be registered is shown, every issue by its line, until `--yes`
// registers it. With `--form` it writes the empty form to FILE instead: a CSV
// of its header, which Excel opens.
func NewCmdRegister() *xli.Command {
	return &xli.Command{
		Name:  "register",
		Brief: "register new assets from a sheet in rove's form (대량 등록), shown first until --yes",
		Flags: flg.Flags{
			&flg.String{Name: "workspace", Brief: "the workspace to register in, by alias; `rove use` sets the default"},
			&flg.String{Name: "reason", Brief: "what the intake is, for the history: 10월 입고"},
			&flg.Switch{Name: "yes", Brief: "register them; without this it only says what it would"},
			&flg.Switch{Name: "form", Brief: "write the empty form to FILE instead"},
		},
		Args: arg.Args{&arg.String{Name: "FILE", Brief: "a .csv in the form; a workbook is refused"}},
		Handler: xli.OnRun(func(ctx context.Context, self *xli.Command, next xli.Next) error {
			path, _ := arg.Get[string](self, "FILE")
			yes, _ := flg.Find[bool](self, "yes")
			if ws := flag(self, "workspace"); ws != "" {
				ctx = context.WithValue(ctx, workspaceKey{}, ws)
			}

			form, _ := flg.Find[bool](self, "form")
			if form && !strings.EqualFold(filepath.Ext(path), ".csv") {
				return errors.New("양식은 CSV입니다: 파일 이름을 양식.csv처럼 적으세요")
			}
			conn, _, err := remote{}.Connect(ctx)
			if err != nil {
				return err
			}

			if form {
				out := &app.AssetExportResponse{}
				if err := conn.Invoke(ctx, app.AssetService_Form_FullMethodName, &app.AssetFormRequest{}, out); err != nil {
					return err
				}
				f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
				if err != nil {
					return err
				}
				if _, err := f.Write(out.GetData()); err != nil {
					f.Close()
					return err
				}
				if err := f.Close(); err != nil {
					return err
				}
				self.Printf("%s: 대량 등록 양식 (엑셀에서 적었으면 'CSV UTF-8(쉼표로 분리)'로 저장하세요)\n", path)
				return next(ctx)
			}

			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			sheet := &app.AssetReadSheetResponse{}
			if err := conn.Invoke(ctx, app.AssetService_ReadSheet_FullMethodName, app.AssetReadSheetRequest_builder{Data: data}.Build(), sheet); err != nil {
				return err
			}
			for _, v := range sheet.GetIssues() {
				where := "시트"
				if v.GetLine() > 0 {
					where = fmt.Sprintf("%d행", v.GetLine())
				}
				if v.GetColumn() != "" {
					where += " " + v.GetColumn()
				}
				self.Printf("! %s: %s\n", where, v.GetMessage())
			}
			items := sheet.GetItems()
			if len(items) == 0 {
				return errors.New("등록할 행이 없습니다")
			}
			line := func(i uint32) uint32 {
				if int(i) < len(sheet.GetLines()) {
					return sheet.GetLines()[i]
				}
				return 0
			}

			dry := &app.AssetAddManyResponse{}
			if err := conn.Invoke(ctx, app.AssetService_AddMany_FullMethodName, app.AssetAddManyRequest_builder{Items: items, DryRun: true}.Build(), dry); err != nil {
				return err
			}
			for i, item := range items {
				tag := ""
				if i < len(dry.GetTags()) {
					tag = dry.GetTags()[i]
				}
				self.Printf("%5d행  %-12s  %s\n", line(uint32(i)), tag, item.GetName())
			}
			for _, v := range dry.GetNewTypes() {
				self.Printf("  새로 만들 유형: %s\n", v)
			}
			for _, v := range dry.GetNewModels() {
				self.Printf("  새로 만들 모델: %s\n", v)
			}
			for _, v := range dry.GetIssues() {
				self.Printf("! %d행 %s: %s\n", line(v.GetItem()), or(columnOf[v.GetField()], v.GetField()), v.GetMessage())
			}
			if n := len(dry.GetIssues()) + len(sheet.GetIssues()); n > 0 {
				return fmt.Errorf("문제가 %d건 있어 등록하지 않았습니다. 시트를 고쳐 다시 실행하세요", n)
			}
			if !yes {
				self.Printf("(%d개를 등록하려면 `--yes`를 붙이세요)\n", len(items))
				return next(ctx)
			}

			done := &app.AssetAddManyResponse{}
			req := app.AssetAddManyRequest_builder{Items: items, Op: op(), Reason: flag(self, "reason")}.Build()
			if err := conn.Invoke(ctx, app.AssetService_AddMany_FullMethodName, req, done); err != nil {
				return err
			}
			self.Printf("%d개를 등록했습니다\n", len(done.GetItems()))
			return next(ctx)
		}),
	}
}
