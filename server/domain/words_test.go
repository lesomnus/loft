package domain_test

import (
	"testing"

	app "github.com/lesomnus/loft"
)

// TestTheHistorySaysLoftsValuesInThePagesWords: the history says a value of
// Loft's own in the word the page says it in, and never as its code -- a
// stewardship's role and a relation, in the sentence of the event that wrote
// the row and in the row's own; and an attribute that was corrected by the
// label the asset's type gives it, or the one the type it inherits from
// does, read when the history is.
func TestTheHistorySaysLoftsValuesInThePagesWords(t *testing.T) {
	e := newEnv(t)
	x := e.x

	computer, err := e.app().AssetType().Add(e.owner, app.AssetTypeAddRequest_builder{
		Name: "컴퓨터",
		Kind: "item",
		Spec: app.TypeSpec_builder{Attributes: []*app.AttributeDef{
			app.AttributeDef_builder{Key: "ram", Label: "메모리", Type: "text"}.Build(),
		}}.Build(),
	}.Build())
	x.NoError(err)
	notebook, err := e.app().AssetType().Add(e.owner, app.AssetTypeAddRequest_builder{
		Name:     "노트북",
		Kind:     "item",
		ParentId: computer.GetId(),
		Spec: app.TypeSpec_builder{Attributes: []*app.AttributeDef{
			app.AttributeDef_builder{Key: "cpu", Label: "CPU", Type: "text"}.Build(),
		}}.Build(),
	}.Build())
	x.NoError(err)
	laptop, err := e.app().Asset().Add(e.owner, app.AssetAddRequest_builder{
		Name:       "개발용 노트북",
		Tag:        "NB-1",
		Kind:       "item",
		Type:       ref[app.AssetTypeRef](notebook.GetId()),
		Since:      e.ago(10 * day),
		Attributes: map[string]string{"cpu": "i5", "ram": "8GB", "color": "검정"},
	}.Build())
	x.NoError(err)
	kit, err := e.app().Asset().Add(e.owner, app.AssetAddRequest_builder{Name: "출장 키트", Tag: "K-1", Kind: "kit"}.Build())
	x.NoError(err)
	_, kim := e.person("member")

	_, err = e.app().Asset().Assign(e.owner, app.AssetAssignRequest_builder{Ref: assetRef(laptop), Role: "manager", Party: ref[app.PartyRef](kim.GetId())}.Build())
	x.NoError(err)
	_, err = e.app().Asset().Relate(e.owner, app.AssetRelateRequest_builder{Ref: assetRef(laptop), Target: assetRef(kit), Kind: "member_of", Required: true}.Build())
	x.NoError(err)
	_, err = e.app().Asset().SetAttributes(e.owner, app.AssetSetAttributesRequest_builder{
		Ref: assetRef(laptop),
		Set: map[string]string{"cpu": "i7", "ram": "16GB", "color": "은색"},
	}.Build())
	x.NoError(err)

	// Each of the three facts SetAttributes wrote, taken back: one the type
	// labels, one its parent does, and one nothing labels, which is said by
	// the workspace's own key for it.
	for _, en := range e.timeline(laptop, false) {
		if en.GetKind() != "fact" {
			continue
		}
		switch en.GetDetail()["key"] + "=" + en.GetDetail()["value"] {
		case "attr.cpu=i7", "attr.ram=16GB", "attr.color=은색":
			_, err := e.app().Asset().Correct(e.owner, app.AssetCorrectRequest_builder{Ref: assetRef(laptop), RowId: en.GetRowId(), Retract: true, Reason: "잘못 입력"}.Build())
			x.NoError(err)
		}
	}
	x.ElementsMatch([]string{"NB-1 CPU 정정", "NB-1 메모리 정정", "NB-1 color 정정"}, e.said("asset.correct"))

	tl := e.timeline(laptop, false)
	x.Equal("NB-1 관리 담당: "+kim.GetName(), summaryOf(tl, "asset.assign"))
	x.Equal("NB-1 연결 K-1 (구성품 / 소속)", summaryOf(tl, "asset.relate"))
	rows := map[string]string{}
	for _, en := range tl {
		if en.GetKind() == "stewardship" || en.GetKind() == "link" {
			rows[en.GetKind()] = en.GetSummary()
		}
	}
	x.Equal(map[string]string{
		"stewardship": "관리 담당: " + kim.GetName(),
		"link":        "→ K-1 출장 키트 (구성품 / 소속)",
	}, rows)
}
