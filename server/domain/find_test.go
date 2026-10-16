package domain_test

import (
	"testing"

	app "github.com/lesomnus/rove"
)

// roster is the people and the models a search of them looks among: a team
// and two of its people, two more people, a vendor; a type and three models,
// two of them of the type.
func (e *env) roster() (team, notebook []byte) {
	e.t.Helper()
	x := e.x
	add := func(v *app.PartyAddRequest) []byte {
		p, err := e.app().Party().Add(e.owner, v)
		x.NoError(err)
		return p.GetId()
	}
	team = add(app.PartyAddRequest_builder{Name: "개발팀", Kind: "team", Code: "T-DEV"}.Build())
	add(app.PartyAddRequest_builder{Name: "김민수", Kind: "person", Code: "E-1001", ParentId: team}.Build())
	add(app.PartyAddRequest_builder{Name: "이지은", Kind: "person", ParentId: team}.Build())
	add(app.PartyAddRequest_builder{Name: "박민수", Kind: "person"}.Build())
	add(app.PartyAddRequest_builder{Name: "최서연", Kind: "person"}.Build())
	add(app.PartyAddRequest_builder{Name: "델코리아", Kind: "vendor", Code: "V-77"}.Build())

	ty, err := e.app().AssetType().Add(e.owner, app.AssetTypeAddRequest_builder{Name: "노트북", Kind: "item"}.Build())
	x.NoError(err)
	for _, v := range []*app.ItemModelAddRequest{
		app.ItemModelAddRequest_builder{Name: "ThinkPad X1", Maker: "Lenovo", Code: "21HM", Type: ref[app.AssetTypeRef](ty.GetId())}.Build(),
		app.ItemModelAddRequest_builder{Name: "갤럭시북3 프로", Maker: "삼성전자", Type: ref[app.AssetTypeRef](ty.GetId())}.Build(),
		app.ItemModelAddRequest_builder{Name: "PowerEdge R660", Maker: "Dell"}.Build(),
	} {
		_, err := e.app().ItemModel().Add(e.owner, v)
		x.NoError(err)
	}
	return team, ty.GetId()
}

// people is the names a search of people finds, best first.
func (e *env) people(q, kind string) []string {
	e.t.Helper()
	v, err := e.app().Party().Search(e.owner, app.PartySearchRequest_builder{Q: q, Kind: kind, Size: 100}.Build())
	e.x.NoError(err)
	out := []string{}
	for _, p := range v.GetItems() {
		out = append(out, p.GetName())
	}
	e.x.Equal(len(out), int(v.GetTotal()), "the total counts what was found")
	return out
}

// models is the names a search of models finds, best first.
func (e *env) models(q string, of []byte) []string {
	e.t.Helper()
	req := app.ItemModelSearchRequest_builder{Q: q, Size: 100}.Build()
	if of != nil {
		req.SetType(ref[app.AssetTypeRef](of))
	}
	v, err := e.app().ItemModel().Search(e.owner, req)
	e.x.NoError(err)
	out := []string{}
	for _, m := range v.GetItems() {
		out = append(out, m.GetName())
	}
	e.x.Equal(len(out), int(v.GetTotal()), "the total counts what was found")
	return out
}

// TestTheDatabaseFindsPeopleAndModels, in a deployment that names no search
// engine: a name in part, a code, one letter by the start of a name, a kind,
// a maker, a type -- and never another workspace's.
func TestTheDatabaseFindsPeopleAndModels(t *testing.T) {
	e := newEnv(t)
	x := e.x
	_, notebook := e.roster()
	_, beta := e.newTenant("beta")
	_, err := e.app().Party().Add(beta, app.PartyAddRequest_builder{Name: "정민수", Kind: "person"}.Build())
	x.NoError(err)

	x.ElementsMatch([]string{"김민수", "박민수"}, e.people("민수", ""), "a name in part, and not another workspace's")
	x.Equal([]string{"김민수"}, e.people("e-1001", ""), "a code, folded")
	x.Equal([]string{"김민수"}, e.people("김", ""), "one letter: the start of a name")
	x.Equal([]string{"개발팀"}, e.people("", "team"))
	x.Equal([]string{"델코리아"}, e.people("", "vendor"))
	x.Empty(e.people("민수", "vendor"))
	x.Len(e.people("", ""), 7, "and the owner's own")

	x.Equal([]string{"ThinkPad X1"}, e.models("lenovo", nil), "who makes it")
	x.Equal([]string{"ThinkPad X1"}, e.models("21hm", nil), "a code")
	x.Equal([]string{"갤럭시북3 프로"}, e.models("프로", nil))
	x.ElementsMatch([]string{"ThinkPad X1", "갤럭시북3 프로"}, e.models("", notebook), "of a type")
	x.Empty(e.models("dell", notebook))

	_, err = e.app().Party().Search(e.owner, app.PartySearchRequest_builder{Kind: "robot"}.Build())
	x.Error(err, "a kind there is not")
}

// TestTheEngineFindsPeopleAndModels the way it finds assets (D31): a given
// name by itself, a name typed whole first, a letter wrong, a team's people
// by its name after the team; a model by what it is, who makes it, and the
// start of a name or a code.
func TestTheEngineFindsPeopleAndModels(t *testing.T) {
	e := newSearchingEnv(t)
	x := e.x
	e.roster()
	_, beta := e.newTenant("beta")
	_, err := e.app().Party().Add(beta, app.PartyAddRequest_builder{Name: "정민수", Kind: "person"}.Build())
	x.NoError(err)
	e.indexed()

	x.ElementsMatch([]string{"김민수", "박민수"}, e.people("민수", ""), "a given name finds the whole of it, and not another workspace's")
	x.Equal("김민수", e.people("김민수", "")[0], "the name typed whole first")
	x.Equal([]string{"김민수"}, e.people("김민스", ""), "a letter wrong")
	x.Equal([]string{"김민수"}, e.people("e-1001", ""), "a code")
	team := e.people("개발팀", "")
	x.Equal("개발팀", team[0], "the team first: %v", team)
	x.ElementsMatch([]string{"김민수", "이지은"}, team[1:], "then the people in it: %v", team)
	x.Equal([]string{"개발팀"}, e.people("개발팀", "team"))
	x.Empty(e.people("민수", "vendor"))

	x.Equal([]string{"ThinkPad X1"}, e.models("thinkpad", nil))
	x.Equal([]string{"ThinkPad X1"}, e.models("lenovo", nil), "who makes it")
	x.Equal([]string{"ThinkPad X1"}, e.models("21h", nil), "the start of a code")
	x.Equal([]string{"갤럭시북3 프로"}, e.models("갤럭시", nil), "the start of a name")
	x.ElementsMatch([]string{"ThinkPad X1", "갤럭시북3 프로"}, e.models("노트북", nil), "what it is a model of")
	x.Empty(e.models("없는 모델", nil))
}
