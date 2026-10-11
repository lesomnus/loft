package domain_test

import (
	"context"
	"regexp"
	"slices"
	"strings"
	"testing"

	app "github.com/lesomnus/rove"
	"github.com/lesomnus/rove/internal/search"
	"github.com/lesomnus/rove/server/domain"
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
// the tag and the name. Where only pg_trgm answers, two letters go by prefix
// too.
func TestTwoLettersAreASearchAndOneIsAPrefix(t *testing.T) {
	e := newEnv(t)
	x := e.x
	e.shelf()

	if e.s.Deps.Search == search.Trigram {
		x.Empty(e.searched("의자"), "two letters, by prefix, where only pg_trgm answers")
		x.Equal([]string{"사무용 의자"}, e.searched("사무"))
	} else {
		x.Equal([]string{"사무용 의자"}, e.searched("의자"), "two syllables in the middle of a name")
	}

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

// TestAnIndexAnswersASearch: what `Search` asks PostgreSQL is answered by
// each column's index, and not by reading the tenant's things one by one --
// two syllables of Korean by pg_bigm's, three letters by pg_trgm's where that
// is all there is, and one by the prefix B-tree (plan M3: 2음절 한글 검색이
// 인덱스를 쓴다).
//
// On a tenant of a dozen things the planner rightly reads all of them, so this
// one has five thousand: copies of one, made by SQL rather than one call each.
// Sequential scans are turned off for the EXPLAIN besides, which leaves the
// planner choosing among indexes.
func TestAnIndexAnswersASearch(t *testing.T) {
	e := newEnv(t)
	x := e.x
	if e.cfg.Db.Driver != "pgx" {
		t.Skip("PostgreSQL only; set PDTEST_POSTGRES")
	}
	e.shelf()
	ctx := context.Background()
	db := e.s.Db

	// Five thousand more things in the tenant, none of which a search below
	// finds.
	cols := []string{}
	rows, err := db.QueryContext(ctx, "SELECT column_name FROM information_schema.columns WHERE table_schema = current_schema() AND table_name = 'asset' ORDER BY ordinal_position")
	x.NoError(err)
	for rows.Next() {
		var c string
		x.NoError(rows.Scan(&c))
		cols = append(cols, c)
	}
	x.NoError(rows.Err())
	rows.Close()
	from := make([]string, len(cols))
	for i, c := range cols {
		switch c {
		case "id":
			from[i] = "gen_random_uuid()"
		case "name":
			from[i] = "(ARRAY['책상', '모니터', '키보드', '마우스', '프린터', '캐비닛', '화이트보드', '스탠드'])[1 + g % 8] || ' ' || g"
		case "tag":
			from[i] = "'X-' || g"
		default:
			from[i] = `a."` + c + `"`
		}
	}
	quoted := make([]string, len(cols))
	for i, c := range cols {
		quoted[i] = `"` + c + `"`
	}
	_, err = db.ExecContext(ctx, "INSERT INTO asset ("+strings.Join(quoted, ", ")+") SELECT "+strings.Join(from, ", ")+
		" FROM (SELECT * FROM asset WHERE tenant_id = $1 AND tag = 'CH-001') a, generate_series(1, 5000) g", e.tenant.Uuid())
	x.NoError(err)
	_, err = db.ExecContext(ctx, "ANALYZE asset, assettype, itemmodel, party")
	x.NoError(err)

	plan := func(query string, args ...any) string {
		t.Helper()
		tx, err := db.BeginTx(ctx, nil)
		x.NoError(err)
		defer tx.Rollback()
		_, err = tx.ExecContext(ctx, "SET LOCAL enable_seqscan = off")
		x.NoError(err)
		rows, err := tx.QueryContext(ctx, "EXPLAIN (COSTS OFF) "+query, args...)
		x.NoError(err)
		defer rows.Close()
		b := strings.Builder{}
		for rows.Next() {
			var line string
			x.NoError(rows.Scan(&line))
			b.WriteString(line + "\n")
		}
		x.NoError(rows.Err())
		return b.String()
	}
	searching := func(q string) string {
		t.Helper()
		query, args := domain.SearchQuery("postgres", e.s.Deps.Search, e.tenant.Uuid(), q)
		return plan(query, args...)
	}
	read := regexp.MustCompile(`(?m)Seq Scan on asset( a(_[0-9]+)?)?$`)

	kind, q := "bigm", "의자"
	switch e.s.Deps.Search {
	case search.Bigram:
	case search.Trigram:
		kind, q = "trgm", "노트북"
	default:
		t.Fatalf("PostgreSQL with neither pg_bigm nor pg_trgm: %q", e.s.Deps.Search)
	}

	// The whole of what a search asks: nothing read row by row, and the
	// bigram index answering where the planner finds it cheapest -- which
	// for each column is its statistics' to decide.
	p := searching(q)
	t.Logf("%s:\n%s", q, p)
	x.Regexp(`Bitmap Index Scan on asset_[a-z]+_`+kind, p)
	x.NotRegexp(read, p)

	// And each column's index answers the question as the search puts it:
	// the same expression, with nothing else in the way.
	for _, v := range []struct{ table, col string }{
		{"asset", "name"}, {"asset", "tag"}, {"asset", "serial"},
		{"assettype", "name"}, {"itemmodel", "name"}, {"itemmodel", "maker"}, {"party", "name"},
	} {
		p := plan(`SELECT 1 FROM `+v.table+` WHERE lower(normalize("`+v.col+`", NFKC)) LIKE $1 ESCAPE '\'`, "%"+q+"%")
		x.Contains(p, "Bitmap Index Scan on "+v.table+"_"+v.col+"_"+kind, p)
	}

	p = searching("m")
	t.Logf("m:\n%s", p)
	x.Contains(p, "asset_tag_prefix", p)
	x.Contains(p, "asset_name_prefix", p)
	x.NotRegexp(read, p)
}

// TestTheModeIsWhatTheDatabaseHas: a database with pg_bigm installed answers
// with it, and one without with pg_trgm -- which every PostgreSQL has -- as
// the migration left it.
func TestTheModeIsWhatTheDatabaseHas(t *testing.T) {
	e := newEnv(t)
	x := e.x
	if e.cfg.Db.Driver != "pgx" {
		x.Equal(search.Plain, e.s.Deps.Search, "SQLite has neither")
		return
	}
	n := 0
	x.NoError(e.s.Db.QueryRowContext(context.Background(), "SELECT count(*) FROM pg_available_extensions WHERE name = 'pg_bigm'").Scan(&n))
	if n > 0 {
		x.Equal(search.Bigram, e.s.Deps.Search)
	} else {
		x.Equal(search.Trigram, e.s.Deps.Search)
	}
}
