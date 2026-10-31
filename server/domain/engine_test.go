package domain_test

import (
	"context"
	"fmt"
	"net"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	app "github.com/lesomnus/loft"
	"github.com/lesomnus/loft/cmd"
	"github.com/lesomnus/loft/internal/opensearchtest"
	"github.com/lesomnus/loft/server/opensearch"
)

// newSearchingEnv is [newEnv] searching on the cluster PDTEST_OPENSEARCH
// names (D31), with its index made: a test of what the engine finds.
func newSearchingEnv(t *testing.T) *env {
	t.Helper()
	e := newEnvWith(t, func(c *cmd.Config) { c.App.Search.Opensearch = opensearchtest.Config(t) })
	opensearchtest.Made(t, e.s.Search, e.s.Ent)
	return e
}

// indexed works off what was written down since, as the loop does, and makes
// it searchable at once rather than at the index's next refresh.
func (e *env) indexed() {
	e.t.Helper()
	ctx := context.Background()
	for {
		n, err := opensearch.Indexer{Client: e.s.Search, Db: e.s.Ent}.Pass(ctx)
		e.x.NoError(err)
		if n == 0 {
			break
		}
	}
	e.x.NoError(e.s.Search.Refresh(ctx))
}

// ranked is the names of what a search finds, best first.
func (e *env) ranked(req *app.AssetSearchRequest) []string {
	e.t.Helper()
	v, err := e.app().Asset().Search(e.owner, req)
	e.x.NoError(err)
	out := []string{}
	for _, a := range v.GetItems() {
		out = append(out, a.GetName())
	}
	return out
}

func q(text string) *app.AssetSearchRequest {
	return app.AssetSearchRequest_builder{Q: text, Size: 500}.Build()
}

// TestTheEngineFindsWhatWasTyped: what the database's search finds, the
// engine finds too -- a name by its words, a tag by its start or in part, a
// serial in part, what a thing is, who made it and who has it -- folded as
// the database folds it, on SQLite as well.
func TestTheEngineFindsWhatWasTyped(t *testing.T) {
	e := newSearchingEnv(t)
	x := e.x
	e.shelf()
	e.indexed()

	x.Equal([]string{"사무용 의자"}, e.searched("사무용"), "a name, by its words")
	x.Equal([]string{"Mac 미니"}, e.searched("pc-77"), "a tag, by its start")
	x.Equal([]string{"Mac 미니"}, e.searched("c-77"), "a tag, in part")
	x.Equal([]string{"개발용 노트북"}, e.searched("55-zx"), "a serial, in part")
	x.Equal([]string{"회의실 빔"}, e.searched("프로젝터"), "what it is")
	x.Equal([]string{"개발용 노트북"}, e.searched("thinkpad"), "its model")
	x.Equal([]string{"개발용 노트북"}, e.searched("lenovo"), "who made it")
	x.Equal([]string{"공용 키보드"}, e.searched("김민수"), "who has it")
	x.Empty(e.searched("없는 물건"))
	x.Equal([]string{"ＡＢＣ 카메라"}, e.searched("abc"), "full width, folded")
	x.Equal([]string{"Mac 미니"}, e.searched("m"), "one letter: the start of a word or a tag")
}

// TestTheEngineFindsTheWordsKoreanIsWrittenIn: a compound by the word at its
// end, a word with a particle on it, a letter wrong -- and the order somebody
// typing two words means: both of them first, spaced or not, then the rarer
// one alone, then the other.
func TestTheEngineFindsTheWordsKoreanIsWrittenIn(t *testing.T) {
	e := newSearchingEnv(t)
	x := e.x
	room := e.space("창고", nil)
	for i, n := range []string{"웹서버", "DB서버", "GPU 서버", "GPU서버", "고성능GPU서버", "NVIDIA GPU", "회의실의자", "의자", "27인치 모니터", "웹 서버 2"} {
		e.item(n, fmt.Sprintf("T-%02d", i), room, day)
	}
	e.indexed()

	x.ElementsMatch([]string{"웹서버", "DB서버", "GPU 서버", "GPU서버", "고성능GPU서버", "웹 서버 2"}, e.searched("서버"), "a compound, by its last word")
	x.ElementsMatch([]string{"회의실의자", "의자"}, e.searched("의자를"), "a particle is not asked for")
	x.Equal([]string{"27인치 모니터"}, e.searched("모니떠"), "a letter wrong")
	x.Equal("회의실의자", e.ranked(q("회의실의자"))[0], "a word typed whole is every piece of it")
	x.Empty(e.searched("E2E-ABC12"), "a tag nobody has finds nothing, though a piece of it -- 2 -- is a word of 웹 서버 2")

	got := e.ranked(q("GPU 서버"))
	x.Len(got, 7, "%v", got)
	x.ElementsMatch([]string{"GPU 서버", "GPU서버", "고성능GPU서버"}, got[:3], "both words first: %v", got)
	x.Equal("NVIDIA GPU", got[3], "then the rarer one: %v", got)
	x.ElementsMatch([]string{"웹서버", "DB서버", "웹 서버 2"}, got[4:], "then the other: %v", got)
}

// TestTheEngineNarrowsAsTheRequestDoes: what a thing is, who has it, where it
// is and what kind it is, a page of it and how many in all -- and never
// another workspace's things, nor one that was erased.
func TestTheEngineNarrowsAsTheRequestDoes(t *testing.T) {
	e := newSearchingEnv(t)
	x := e.x
	store := e.space("창고", nil)
	office := e.space("사무실 키보드 보관함", nil)

	kim, err := e.app().Party().Add(e.owner, app.PartyAddRequest_builder{Name: "김민수", Kind: "person"}.Build())
	x.NoError(err)
	ty, err := e.app().AssetType().Add(e.owner, app.AssetTypeAddRequest_builder{Name: "입력 장치", Kind: "item"}.Build())
	x.NoError(err)
	typed := app.AssetAddRequest_builder{Name: "무선 키보드", Tag: "KB-1", Kind: "item", Type: ref[app.AssetTypeRef](ty.GetId()), Since: e.ago(day)}.Build()
	typed.SetTo(assetRef(store))
	_, err = e.app().Asset().Add(e.owner, typed)
	x.NoError(err)
	held := e.item("공용 키보드", "KB-2", office, day)
	_, err = e.app().Custody().Add(e.owner, app.CustodyAddRequest_builder{
		Party: ref[app.PartyRef](kim.GetId()),
		Lines: []*app.CustodyLineSpec{app.CustodyLineSpec_builder{Asset: assetRef(held)}.Build()},
	}.Build())
	x.NoError(err)
	gone := e.item("고장난 키보드", "KB-3", store, day)

	_, beta := e.newTenant("beta")
	other := app.AssetAddRequest_builder{Name: "다른 곳의 키보드", Tag: "KB-1", Kind: "item", Since: e.ago(day)}.Build()
	_, err = e.app().Asset().Add(beta, other)
	x.NoError(err)

	_, err = e.app().Asset().Erase(e.owner, assetRef(gone))
	x.NoError(err)
	e.indexed()

	x.ElementsMatch([]string{"무선 키보드", "공용 키보드", "사무실 키보드 보관함"}, e.searched("키보드"), "not another workspace's, nor what was erased")
	x.Equal([]string{"무선 키보드"}, e.searched("kb-1"), "a tag is the workspace's own")

	only := func(set func(r *app.AssetSearchRequest)) []string {
		r := q("키보드")
		set(r)
		return e.ranked(r)
	}
	x.ElementsMatch([]string{"무선 키보드", "공용 키보드"}, only(func(r *app.AssetSearchRequest) { r.SetKind("item") }))
	x.Equal([]string{"무선 키보드"}, only(func(r *app.AssetSearchRequest) { r.SetType(ref[app.AssetTypeRef](ty.GetId())) }))
	x.Equal([]string{"공용 키보드"}, only(func(r *app.AssetSearchRequest) { r.SetCustodian(ref[app.PartyRef](kim.GetId())) }))
	x.Equal([]string{"무선 키보드"}, only(func(r *app.AssetSearchRequest) { r.SetWithin(assetRef(store)) }))

	page, err := e.app().Asset().Search(e.owner, app.AssetSearchRequest_builder{Q: "키보드", Size: 2, Offset: 1}.Build())
	x.NoError(err)
	x.Len(page.GetItems(), 2)
	x.EqualValues(3, page.GetTotal(), "the total counts all of it")
}

// TestASearchTheEngineCannotAnswerIsUnavailable, and says so, rather than
// asking the database behind its back. With nothing typed there is nothing to
// ask it, and the database answers.
func TestASearchTheEngineCannotAnswerIsUnavailable(t *testing.T) {
	e := newSearchingEnv(t)
	x := e.x
	e.shelf()

	l, err := net.Listen("tcp", "127.0.0.1:0")
	x.NoError(err)
	cfg := e.cfg.App.Search.Opensearch
	cfg.Addrs = []string{"http://" + l.Addr().String()}
	x.NoError(l.Close())
	nobody, err := opensearch.New(cfg, func(s string) (string, error) { return s, nil })
	x.NoError(err)
	e.s.Deps.Finder = nobody

	_, err = e.app().Asset().Search(e.owner, q("의자"))
	x.Equal(codes.Unavailable, status.Code(err))

	v, err := e.app().Asset().Search(e.owner, app.AssetSearchRequest_builder{Kind: "item"}.Build())
	x.NoError(err)
	x.NotEmpty(v.GetItems())
}
