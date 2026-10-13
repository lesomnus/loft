package tenancy

import (
	"context"
	"fmt"

	app "github.com/lesomnus/rove"
)

type attr struct {
	key, label, kind, unit string
	options                []string
}

func spec(as ...attr) *app.TypeSpec {
	out := &app.TypeSpec{}
	for _, a := range as {
		out.SetAttributes(append(out.GetAttributes(), app.AttributeDef_builder{
			Key:     a.key,
			Label:   a.label,
			Type:    a.kind,
			Unit:    a.unit,
			Options: a.options,
		}.Build()))
	}
	return out
}

// rack is a type whose assets hold others at rack positions, which is what
// the rack view plugin draws.
func rack(as ...attr) *app.TypeSpec {
	v := spec(as...)
	v.SetCapabilities([]string{"rack"})
	return v
}

// seedTypes is what a register of an office starts with; a tenant changes
// them as it likes.
func seedTypes(ctx context.Context, s app.Server) error {
	warranty := attr{"warranty_until", "보증 만료", "date", "", nil}
	computer := []attr{
		{"cpu", "CPU", "text", "", nil},
		{"ram", "메모리", "number", "GB", nil},
		{"disk", "저장장치", "number", "GB", nil},
		{"os", "운영체제", "enum", "", []string{"Windows", "macOS", "Linux"}},
		warranty,
	}
	types := []struct {
		name, kind string
		spec       *app.TypeSpec
	}{
		{"노트북", "item", spec(computer...)},
		{"데스크톱", "item", spec(computer...)},
		{"모니터", "item", spec(attr{"size", "크기", "number", "inch", nil}, attr{"resolution", "해상도", "text", "", nil}, warranty)},
		{"휴대폰", "item", spec(attr{"os", "운영체제", "enum", "", []string{"iOS", "Android"}}, attr{"number", "전화번호", "text", "", nil})},
		{"태블릿", "item", nil},
		{"주변기기", "item", nil},
		{"네트워크 장비", "item", spec(attr{"ip", "IP", "text", "", nil}, attr{"mac", "MAC", "text", "", nil})},
		{"서버", "item", spec(attr{"ip", "IP", "text", "", nil}, attr{"cpu", "CPU", "text", "", nil}, attr{"ram", "메모리", "number", "GB", nil}, warranty)},
		{"랙", "item", rack(attr{"units", "높이", "number", "U", nil})},
		{"촬영 장비", "item", nil},
		{"가구", "item", nil},
		{"소모품", "item", nil},
		{"키트", "kit", nil},
		{"건물", "space", nil},
		{"층", "space", nil},
		{"사무실", "space", spec(attr{"seats", "좌석", "number", "석", nil})},
		{"회의실", "space", spec(attr{"capacity", "정원", "number", "명", nil}, attr{"display", "디스플레이", "bool", "", nil})},
		{"창고", "space", nil},
		{"서버실", "space", nil},
	}
	for _, ty := range types {
		req := app.AssetTypeAddRequest_builder{Name: ty.name, Kind: ty.kind, Spec: ty.spec}.Build()
		if _, err := s.AssetType().Add(ctx, req); err != nil {
			return fmt.Errorf("type %s: %w", ty.name, err)
		}
	}
	return nil
}
