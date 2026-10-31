package domain_test

import (
	"slices"
	"testing"

	app "github.com/lesomnus/loft"
)

// searched is the names of what a search for `q` finds, sorted.
func (e *env) searched(q string) []string {
	e.t.Helper()
	v, err := e.app().Asset().Search(e.owner, app.AssetSearchRequest_builder{Q: q, Size: 500}.Build())
	e.x.NoError(err)
	out := []string{}
	for _, a := range v.GetItems() {
		out = append(out, a.GetName())
	}
	slices.Sort(out)
	e.x.Equal(len(out), int(v.GetTotal()), "the total counts what was found")
	return out
}

// shelf is a few things to look for: by name, by tag, by serial, and by what
// they are, who made them and who has them.
func (e *env) shelf() {
	e.t.Helper()
	x := e.x
	room := e.space("회의실", nil)
	e.item("사무용 의자", "CH-001", room, day)
	e.item("Mac 미니", "PC-777", room, day)
	e.item("１００% 면 장갑", "GL-100", room, day)
	e.item("A_B 케이블", "CB-1", room, day)
	e.item("AxB 어댑터", "AD-1", room, day)
	e.item("ＡＢＣ 카메라", "CM-1", room, day)
	e.item("ｶﾒﾗ 가방", "BG-1", room, day)

	ty, err := e.app().AssetType().Add(e.owner, app.AssetTypeAddRequest_builder{Name: "프로젝터", Kind: "item"}.Build())
	x.NoError(err)
	model, err := e.app().ItemModel().Add(e.owner, app.ItemModelAddRequest_builder{Name: "ThinkPad X1", Maker: "Lenovo"}.Build())
	x.NoError(err)
	kim, err := e.app().Party().Add(e.owner, app.PartyAddRequest_builder{Name: "김민수", Kind: "person"}.Build())
	x.NoError(err)
	for _, v := range []*app.AssetAddRequest{
		app.AssetAddRequest_builder{Name: "회의실 빔", Kind: "item", Type: ref[app.AssetTypeRef](ty.GetId()), Since: e.ago(day)}.Build(),
		app.AssetAddRequest_builder{Name: "개발용 노트북", Kind: "item", Model: ref[app.ItemModelRef](model.GetId()), Serial: "SN-55-ZX", Since: e.ago(day)}.Build(),
	} {
		v.SetTo(assetRef(room))
		_, err := e.app().Asset().Add(e.owner, v)
		x.NoError(err)
	}

	// Who has it is who it was issued to.
	keyboard := e.item("공용 키보드", "KB-1", room, day)
	_, err = e.app().Custody().Add(e.owner, app.CustodyAddRequest_builder{
		Party: ref[app.PartyRef](kim.GetId()),
		Lines: []*app.CustodyLineSpec{app.CustodyLineSpec_builder{Asset: assetRef(keyboard)}.Build()},
	}.Build())
	x.NoError(err)
}

// TestASearchFindsWhatWasTyped: a name, a tag or a serial in part, and what a
// thing is, who made it and who has it -- whichever index answers, and on
// SQLite with none.
func TestASearchFindsWhatWasTyped(t *testing.T) {
	e := newEnv(t)
	x := e.x
	e.shelf()

	x.Equal([]string{"사무용 의자"}, e.searched("사무용"), "a name, in part")
	x.Equal([]string{"Mac 미니"}, e.searched("pc-77"), "a tag, in part, in either case")
	x.Equal([]string{"개발용 노트북"}, e.searched("55-zx"), "a serial")
	x.Equal([]string{"회의실 빔"}, e.searched("프로젝터"), "what it is")
	x.Equal([]string{"개발용 노트북"}, e.searched("thinkpad"), "its model")
	x.Equal([]string{"개발용 노트북"}, e.searched("lenovo"), "who made it")
	x.Equal([]string{"공용 키보드"}, e.searched("김민수"), "who has it")
	x.Empty(e.searched("없는 물건"))

	// What a LIKE pattern means is not what a person typed.
	x.Equal([]string{"A_B 케이블"}, e.searched("a_b"), "an underscore is an underscore, and not any letter")
	x.Empty(e.searched("0%_"), "nor a percent sign any letters")
	x.Empty(e.searched("%"), "and a percent sign alone is not everything")
}

// TestTwoLettersAreASearchAndOneIsAPrefix: `의자` is two syllables and a
// search; a single letter is not a search anybody meant, and goes by prefix on
// the tag and the name.
func TestTwoLettersAreASearchAndOneIsAPrefix(t *testing.T) {
	e := newEnv(t)
	x := e.x
	e.shelf()

	x.Equal([]string{"사무용 의자"}, e.searched("의자"), "two syllables in the middle of a name")

	x.Equal([]string{"Mac 미니"}, e.searched("m"), "one letter: the start of a name or a tag")
	x.ElementsMatch([]string{"사무용 의자", "ＡＢＣ 카메라", "A_B 케이블"}, e.searched("c"),
		"C as the start of CH-001, CM-1 and CB-1 -- and not the c in Mac")
}

// TestFullWidthAndHalfWidthAreTheSame: what is kept and what is typed are both
// folded by NFKC, so `ＡＢＣ` is found by `abc` and `ｶﾒﾗ` by `カメラ`. SQLite has
// no normal forms and folds only what is typed.
func TestFullWidthAndHalfWidthAreTheSame(t *testing.T) {
	e := newEnv(t)
	x := e.x
	if e.cfg.Db.Driver != "pgx" {
		t.Skip("PostgreSQL only; set PDTEST_POSTGRES")
	}
	e.shelf()

	x.Equal([]string{"ＡＢＣ 카메라"}, e.searched("abc"))
	x.Equal([]string{"ＡＢＣ 카메라"}, e.searched("ＡＢＣ"), "and the other way round")
	x.Equal([]string{"ｶﾒﾗ 가방"}, e.searched("カメラ"))
	x.Equal([]string{"１００% 면 장갑"}, e.searched("100%"))
}
