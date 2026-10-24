package domain_test

import (
	"bytes"
	"encoding/csv"
	"fmt"
	"slices"
	"testing"

	"github.com/lesomnus/payday/pderr"
	"google.golang.org/grpc/codes"
	"google.golang.org/protobuf/types/known/timestamppb"

	app "github.com/lesomnus/rove"
	"github.com/lesomnus/rove/server/domain"
)

// An attribute of an asset is keyed by something, with no space in it and no
// dot -- what a type's spec defines one by -- however the asset is given it:
// by SetAttributes, by Add, or by bulk registration. Each refuses another key
// on the field it came in, in the words SetAttributes has always said it in.

// TestAnAttributeIsKeyedBySomethingWhereverAnAssetIsGivenOne: Add and bulk
// registration refuse an attribute keyed by nothing, or by what reads as two,
// as SetAttributes does, and make nothing.
func TestAnAttributeIsKeyedBySomethingWhereverAnAssetIsGivenOne(t *testing.T) {
	e := newEnv(t)
	x := e.x
	before := e.assets()

	for _, k := range []string{"", "two words", "x.y"} {
		_, err := e.app().Asset().Add(e.owner, app.AssetAddRequest_builder{
			Name:       "모니터",
			Attributes: map[string]string{"size": "27", k: "보증서 없음"},
		}.Build())
		x.Equal(codes.InvalidArgument, codeOf(err), "%q", k)
		x.Equal([]pderr.Violation{{Field: "attributes", Why: fmt.Sprintf("%q는 자산의 항목이 아닙니다", k)}}, pderr.Violations(err), "%q", k)
	}
	x.Equal(before, e.assets(), "nothing was made")

	// The words SetAttributes says of the same key.
	a := e.item("모니터", "MN-1", nil, day)
	_, err := e.app().Asset().SetAttributes(e.owner, app.AssetSetAttributesRequest_builder{
		Ref: assetRef(a),
		Set: map[string]string{"": "보증서 없음"},
	}.Build())
	x.Equal(codes.InvalidArgument, codeOf(err))
	x.Equal([]pderr.Violation{{Field: "set", Why: `""는 자산의 항목이 아닙니다`}}, pderr.Violations(err))

	// Bulk registration says it of the row, in a dry run and in a refusal.
	items := []*app.AssetAddRequest{
		app.AssetAddRequest_builder{Name: "a", Attributes: map[string]string{"size": "27"}}.Build(),
		app.AssetAddRequest_builder{Name: "b", Attributes: map[string]string{"": "보증서 없음"}}.Build(),
		app.AssetAddRequest_builder{Name: "c", Attributes: map[string]string{"x.y": "1"}}.Build(),
	}
	dry, err := e.app().Asset().AddMany(e.owner, app.AssetAddManyRequest_builder{DryRun: true, Items: items}.Build())
	x.NoError(err)
	issues := []string{}
	for _, v := range dry.GetIssues() {
		issues = append(issues, fmt.Sprintf("%d %s %s", v.GetItem(), v.GetField(), v.GetMessage()))
	}
	x.Equal([]string{`1 attributes ""는 자산의 항목이 아닙니다`, `2 attributes "x.y"는 자산의 항목이 아닙니다`}, issues)

	_, err = e.app().Asset().AddMany(e.owner, app.AssetAddManyRequest_builder{Items: items}.Build())
	x.Equal(codes.InvalidArgument, codeOf(err))
	x.Equal([]pderr.Violation{
		{Field: "items[1].attributes", Why: `""는 자산의 항목이 아닙니다`},
		{Field: "items[2].attributes", Why: `"x.y"는 자산의 항목이 아닙니다`},
	}, pderr.Violations(err))
	x.Equal(before+1, e.assets(), "nothing was made but MN-1")
	x.Empty(e.eventsOf("asset.add_many"))
}

// TestAnAttributeKeyedByNothingBeforeReadsAsItDid: an asset Add gave an
// attribute keyed by nothing, before it refused one, has a fact `attr.` of
// it -- and reads as it did, wherever it is read: the row, its history, what
// it was at a moment and what changed between two, a change to another of
// its attributes, and the register's file. (The terminal's tools ask the same
// calls.)
func TestAnAttributeKeyedByNothingBeforeReadsAsItDid(t *testing.T) {
	e := newEnv(t)
	x := e.x

	a, err := domain.AddedAsBefore(e.owner, e.app(), app.AssetAddRequest_builder{
		Name:       "모니터",
		Tag:        "MN-1",
		Attributes: map[string]string{"": "보증서 없음", "size": "27"},
	}.Build())
	x.NoError(err)
	x.Equal(map[string]string{"": "보증서 없음", "size": "27"}, e.get(a).GetAttributes())
	made := e.now

	// What it was at a moment.
	q, err := e.app().Asset().QueryAt(e.owner, app.AssetQueryAtRequest_builder{Root: assetRef(a), At: timestamppb.New(made)}.Build())
	x.NoError(err)
	x.NotEmpty(q.GetItems())
	x.Equal("보증서 없음", q.GetItems()[0].GetFacts()["attr."])
	x.Equal("27", q.GetItems()[0].GetFacts()["attr.size"])

	// Another of its attributes changed: the row is made again of its facts,
	// the one keyed by nothing among them.
	e.now = e.now.Add(day)
	_, err = e.app().Asset().SetAttributes(e.owner, app.AssetSetAttributesRequest_builder{
		Ref: assetRef(a),
		Set: map[string]string{"size": "32"},
	}.Build())
	x.NoError(err)
	x.Equal(map[string]string{"": "보증서 없음", "size": "32"}, e.get(a).GetAttributes())

	// What changed between the two: the size, and not what stayed.
	d, err := e.app().Asset().Diff(e.owner, app.AssetDiffRequest_builder{
		Root: assetRef(a),
		From: timestamppb.New(made),
		To:   timestamppb.New(e.now),
	}.Build())
	x.NoError(err)
	changed := []string{}
	for _, c := range d.GetChanges() {
		changed = append(changed, fmt.Sprintf("%s %s %s→%s", c.GetWhat(), c.GetField(), c.GetBefore(), c.GetAfter()))
	}
	x.Equal([]string{"changed attr.size 27→32"}, changed)

	// Its history.
	tl, err := e.app().Asset().Timeline(e.owner, app.AssetTimelineRequest_builder{Ref: assetRef(a)}.Build())
	x.NoError(err)
	keyed := []string{}
	for _, v := range tl.GetEntries() {
		if v.GetKind() == "fact" {
			keyed = append(keyed, v.GetDetail()["key"]+"="+v.GetDetail()["value"])
		}
	}
	x.Contains(keyed, "attr.=보증서 없음")

	// The register's file: a column for it, as for every attribute.
	f, err := e.app().Asset().Export(e.owner, app.AssetExportRequest_builder{}.Build())
	x.NoError(err)
	rows, err := csv.NewReader(bytes.NewReader(bytes.TrimPrefix(f.GetData(), []byte("\ufeff")))).ReadAll()
	x.NoError(err)
	x.NotEmpty(rows)
	col := slices.Index(rows[0], "속성.")
	x.GreaterOrEqual(col, 0, "%v", rows[0])
	i := slices.IndexFunc(rows, func(r []string) bool { return r[0] == "MN-1" })
	x.Positive(i)
	x.Equal("보증서 없음", rows[i][col])
}
