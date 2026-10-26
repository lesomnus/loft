package domain_test

import (
	"context"
	"testing"

	"google.golang.org/grpc/codes"

	app "github.com/lesomnus/rove"
)

// Tags made up for what nobody gave one (D52). What happens when two are
// made up at once is in concurrency_test.go.

// tagOf adds an asset of `kind` nobody gave a tag, and answers the tag it was
// given.
func (e *env) tagOf(kind string) string {
	e.t.Helper()
	v, err := e.app().Asset().Add(e.owner, app.AssetAddRequest_builder{Name: "태그 없음", Kind: kind}.Build())
	e.x.NoError(err)
	return v.GetTag()
}

// TestATagNobodyGaveIsTheNextNumberOfItsKind: its kind's prefix and the next
// number of it the workspace has not used. A tag somebody wrote of that shape
// is passed over; the numbers an operation refused had made up are given
// again; an erased asset's tag is free again, and the numbers go on past it.
func TestATagNobodyGaveIsTheNextNumberOfItsKind(t *testing.T) {
	e := newEnv(t)
	x := e.x

	x.Equal("A-00001", e.tagOf("item"))
	x.Equal("S-00001", e.tagOf("space"))
	x.Equal("K-00001", e.tagOf("kit"))
	x.Equal("G-00001", e.tagOf("group"))
	x.Equal("A-00002", e.tagOf("item"), "a prefix is numbered on its own")

	written := e.item("노트북", "A-00004", nil, day)
	x.Equal("A-00003", e.tagOf("item"))
	x.Equal("A-00005", e.tagOf("item"), "A-00004 is somebody's")

	// Made up and then refused, for where it was put: nothing was made, and
	// the number is the next one's.
	group, err := e.app().Asset().Add(e.owner, app.AssetAddRequest_builder{Name: "카메라 풀", Kind: "group", Tag: "POOL-1"}.Build())
	x.NoError(err)
	_, err = e.app().Asset().Add(e.owner, app.AssetAddRequest_builder{Name: "카메라", To: assetRef(group)}.Build())
	x.Equal(codes.InvalidArgument, codeOf(err))
	x.Equal("A-00006", e.tagOf("item"))

	// Erased, its tag is anybody's to write again; the counter goes on.
	_, err = e.app().Asset().Erase(e.owner, assetRef(written))
	x.NoError(err)
	x.Equal("A-00007", e.tagOf("item"))
	again := e.item("노트북", "A-00004", nil, day)
	x.Equal("A-00004", again.GetTag())

	// A number somebody wrote ahead and that was erased since is given when
	// the counter comes to it: no live asset has it.
	ahead := e.item("모니터", "A-00009", nil, day)
	_, err = e.app().Asset().Erase(e.owner, assetRef(ahead))
	x.NoError(err)
	x.Equal("A-00008", e.tagOf("item"))
	x.Equal("A-00009", e.tagOf("item"))

	// Of another shape, it is nothing the counter would make up.
	e.item("의자", "A-10", nil, day)
	e.item("의자", "A-000011", nil, day)
	x.Equal("A-00010", e.tagOf("item"))
	x.Equal("A-00011", e.tagOf("item"))
}

// TestTheFirstTagMadeUpBeginsWhereNumberingDid: a workspace whose assets were
// registered before there were counters -- or restored from an export, which
// keeps none -- has its tags numbered on from as many tags of the prefix as it
// ever had, erased ones too, which is where they were numbered from then; a
// number somebody wrote ahead is passed over as before.
func TestTheFirstTagMadeUpBeginsWhereNumberingDid(t *testing.T) {
	e := newEnv(t)
	x := e.x
	ctx := context.Background()

	for _, tag := range []string{"A-00001", "A-00002", "A-00005"} {
		e.item("노트북", tag, nil, day)
	}
	erased := e.item("노트북", "A-00003", nil, day)
	_, err := e.app().Asset().Erase(e.owner, assetRef(erased))
	x.NoError(err)

	// No counter, as before there were any.
	_, err = e.s.Ent.TagCounter.Delete().Exec(ctx)
	x.NoError(err)

	x.Equal("A-00006", e.tagOf("item"), "four tags of A-, the erased one too, and A-00005 written ahead")
	x.Equal("A-00007", e.tagOf("item"))
	n, err := e.s.Ent.TagCounter.Query().Count(ctx)
	x.NoError(err)
	x.Equal(1, n, "made the first time it was asked for")
}

// TestADryRunSaysTheTagsTheRegistrationGives: what a dry run says it would
// make up is what the registration sent after it makes up, a prefix at a time,
// passing over what the rows give themselves and what is taken -- and saying
// it takes nothing.
func TestADryRunSaysTheTagsTheRegistrationGives(t *testing.T) {
	e := newEnv(t)
	x := e.x
	x.Equal("A-00001", e.tagOf("item"))
	e.item("노트북", "A-00003", nil, day)

	items := []*app.AssetAddRequest{
		app.AssetAddRequest_builder{Name: "모니터"}.Build(),
		app.AssetAddRequest_builder{Name: "회의실", Kind: "space"}.Build(),
		app.AssetAddRequest_builder{Name: "모니터", Tag: "A-00004"}.Build(),
		app.AssetAddRequest_builder{Name: "모니터"}.Build(),
		app.AssetAddRequest_builder{Name: "모니터"}.Build(),
	}
	want := []string{"A-00002", "S-00001", "A-00004", "A-00005", "A-00006"}

	dry, err := e.app().Asset().AddMany(e.owner, app.AssetAddManyRequest_builder{DryRun: true, Items: items}.Build())
	x.NoError(err)
	x.Equal(want, dry.GetTags())
	dry, err = e.app().Asset().AddMany(e.owner, app.AssetAddManyRequest_builder{DryRun: true, Items: items}.Build())
	x.NoError(err)
	x.Equal(want, dry.GetTags(), "a dry run takes nothing")

	res, err := e.app().Asset().AddMany(e.owner, app.AssetAddManyRequest_builder{Items: items}.Build())
	x.NoError(err)
	x.Equal(want, res.GetTags())
	for i, a := range res.GetItems() {
		x.Equal(want[i], a.GetTag())
	}
	x.Equal("A-00007", e.tagOf("item"))
	x.Equal("S-00002", e.tagOf("space"))
}
