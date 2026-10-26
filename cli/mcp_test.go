package cli_test

import (
	"context"
	"encoding/json"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/lesomnus/payday/pdid"

	"github.com/lesomnus/rove/cli"
)

// assistant is an MCP client speaking to `rove mcp` the way Claude Code
// does: the command run with its stdin and stdout given to the client.
type assistant struct {
	s       *served
	session *mcp.ClientSession
}

func (s *served) assistant(args ...string) *assistant {
	s.t.Helper()
	in, toServer := io.Pipe()
	fromServer, out := io.Pipe()
	k := cli.NewCmdMcp()
	k.ReadCloser, k.Writer, k.ErrWriter = in, out, io.Discard

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- k.Run(ctx, args)
		out.Close()
	}()
	session, err := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "0"}, nil).
		Connect(ctx, &mcp.IOTransport{Reader: fromServer, Writer: toServer}, nil)
	s.x.NoError(err)
	s.t.Cleanup(func() {
		session.Close()
		toServer.Close()
		cancel()
		<-done
	})
	return &assistant{s: s, session: session}
}

// call is a tool's answer, read into `out`; a tool that refused fails the
// test with what it said.
func (a *assistant) call(tool string, args map[string]any, out any) {
	a.s.t.Helper()
	res, err := a.session.CallTool(context.Background(), &mcp.CallToolParams{Name: tool, Arguments: args})
	a.s.x.NoError(err)
	a.s.x.False(res.IsError, "%s: %s", tool, said(res))
	b, err := json.Marshal(res.StructuredContent)
	a.s.x.NoError(err)
	a.s.x.NoError(json.Unmarshal(b, out), string(b))
}

// refused is what a tool said when it would not answer.
func (a *assistant) refused(tool string, args map[string]any) string {
	a.s.t.Helper()
	res, err := a.session.CallTool(context.Background(), &mcp.CallToolParams{Name: tool, Arguments: args})
	a.s.x.NoError(err)
	a.s.x.True(res.IsError, "%s answered: %s", tool, said(res))
	return said(res)
}

func said(res *mcp.CallToolResult) string {
	b := strings.Builder{}
	for _, c := range res.Content {
		if t, ok := c.(*mcp.TextContent); ok {
			b.WriteString(t.Text)
		}
	}
	return b.String()
}

// made is the id `rove api ... -o json` answered with, which the tools take
// as it is.
func made(t *testing.T, out string) string {
	t.Helper()
	var v struct{ Id string }
	if err := json.Unmarshal([]byte(out), &v); err != nil {
		t.Fatalf("%s: %v", out, err)
	}
	if _, err := pdid.Parse(v.Id); err != nil {
		t.Fatalf("%s: %v", v.Id, err)
	}
	return v.Id
}

type ref struct {
	Id   string `json:"id"`
	Name string `json:"name"`
}

type answered struct {
	Id        string            `json:"id"`
	Tag       string            `json:"tag"`
	Name      string            `json:"name"`
	Kind      string            `json:"kind"`
	Where     []ref             `json:"where"`
	Custodian *ref              `json:"custodian"`
	Stewards  map[string]string `json:"stewards"`
	Inside    []ref             `json:"inside"`
}

// TestAnAssistantReadsTheWorkspaceAsItsPerson: every tool, over the
// terminal's sign-in, answering with the names a person reads.
func TestAnAssistantReadsTheWorkspaceAsItsPerson(t *testing.T) {
	s := serve(t)
	x := s.x
	s.signIn("owner", "owner1234")

	building := made(t, run(t, cli.NewCmdApi(), "asset", "add", "-o", "json", `{"name": "본관", "kind": "space", "tag": "B-1"}`))
	room := made(t, run(t, cli.NewCmdApi(), "asset", "add", "-o", "json", `{"name": "3층 창고", "kind": "space", "tag": "R-3", "to": {"id": "`+building+`"}}`))
	laptop := made(t, run(t, cli.NewCmdApi(), "asset", "add", "-o", "json", `{"name": "개발용 노트북", "kind": "item", "tag": "NB-1", "to": {"id": "`+room+`"}}`))
	made(t, run(t, cli.NewCmdApi(), "asset", "add", "-o", "json", `{"name": "회의실 의자", "kind": "item", "tag": "CH-1", "to": {"id": "`+room+`"}}`))
	team := made(t, run(t, cli.NewCmdApi(), "party", "add", "-o", "json", `{"name": "개발팀", "kind": "team"}`))
	notebook := made(t, run(t, cli.NewCmdApi(), "asset-type", "add", "-o", "json", `{"name": "노트북", "kind": "item"}`))
	run(t, cli.NewCmdApi(), "item-model", "add", "-o", "json", `{"name": "ThinkPad X1", "maker": "Lenovo", "type": {"id": "`+notebook+`"}}`)
	kim := made(t, run(t, cli.NewCmdApi(), "party", "add", "-o", "json", `{"name": "김민수", "kind": "person", "parentId": "`+team+`"}`))
	due := time.Now().Add(72 * time.Hour).UTC().Format(time.RFC3339)
	run(t, cli.NewCmdApi(), "custody", "add", "-o", "json", `{"party": {"id": "`+kim+`"}, "dueAt": "`+due+`", "lines": [{"asset": {"id": "`+laptop+`"}}]}`)

	a := s.assistant()

	// Who.
	{
		var v struct {
			Login     string `json:"login"`
			Workspace struct {
				Alias string `json:"alias"`
				Role  string `json:"role"`
			} `json:"workspace"`
			Workspaces []struct{ Alias string } `json:"workspaces"`
		}
		a.call("whoami", nil, &v)
		x.Equal("owner", v.Login)
		x.Equal("acme", v.Workspace.Alias)
		x.Equal("owner", v.Workspace.Role)
		x.Len(v.Workspaces, 1)
	}

	// A search, with where each is and who has it.
	{
		var v struct {
			Total int        `json:"total"`
			Items []answered `json:"items"`
		}
		a.call("search_assets", map[string]any{"query": "노트북"}, &v)
		x.Equal(1, v.Total)
		x.Len(v.Items, 1)
		got := v.Items[0]
		x.Equal(laptop, got.Id)
		x.Equal("NB-1", got.Tag)
		x.Equal([]ref{{building, "본관"}, {room, "3층 창고"}}, got.Where, "the outermost first")
		x.Equal(&ref{kim, "김민수"}, got.Custodian)

		a.call("search_assets", map[string]any{"within": "R-3"}, &v)
		x.Equal(2, v.Total, "what is in a space, by its tag")
		a.call("search_assets", map[string]any{"custodian": kim}, &v)
		x.Equal(1, v.Total)
		a.call("search_assets", map[string]any{"kind": "space"}, &v)
		x.Equal(2, v.Total)
	}

	// One asset, and what is in another.
	{
		var v answered
		a.call("get_asset", map[string]any{"asset": "NB-1"}, &v)
		x.Equal("개발용 노트북", v.Name)
		x.Equal("김민수", v.Stewards["custodian"])

		a.call("get_asset", map[string]any{"asset": room}, &v)
		names := []string{}
		for _, c := range v.Inside {
			names = append(names, c.Name)
		}
		x.ElementsMatch([]string{"개발용 노트북", "회의실 의자"}, names)
	}

	// What happened to it.
	{
		var v struct {
			Entries []struct {
				Kind      string `json:"kind"`
				What      string `json:"what"`
				Operation string `json:"operation"`
				Who       string `json:"who"`
			} `json:"entries"`
		}
		a.call("asset_history", map[string]any{"asset": laptop}, &v)
		ops := map[string]string{}
		for _, e := range v.Entries {
			if e.Kind == "event" {
				ops[e.Operation] = e.Who
			}
		}
		x.Contains(ops, "asset.add")
		x.NotEmpty(ops["asset.add"], "who did it, by name")
		lent := false
		for _, e := range v.Entries {
			lent = lent || (e.Kind == "stewardship" && strings.Contains(e.What, "김민수"))
		}
		x.True(lent, "who it was handed to: %+v", v.Entries)
	}

	// As it was.
	{
		var v struct {
			Items []struct {
				Tag     string            `json:"tag"`
				Depth   int               `json:"depth"`
				Facts   map[string]string `json:"facts"`
				Existed bool              `json:"existed"`
			} `json:"items"`
		}
		a.call("asset_at", map[string]any{"asset": "B-1", "at": time.Now().Format(time.RFC3339Nano)}, &v)
		tags := map[string]int{}
		for _, st := range v.Items {
			tags[st.Tag] = st.Depth
		}
		x.Equal(map[string]int{"B-1": 0, "R-3": 1, "NB-1": 2, "CH-1": 2}, tags)

		a.call("asset_at", map[string]any{"asset": "B-1", "at": "2000-01-01"}, &v)
		for _, st := range v.Items {
			x.False(st.Existed, "%s, before anything was registered", st.Tag)
		}
	}

	// Lately.
	{
		var v struct {
			Items []struct {
				Operation string `json:"operation"`
				What      string `json:"what"`
				Who       string `json:"who"`
			} `json:"items"`
		}
		a.call("recent_events", map[string]any{"kind": "custody."}, &v)
		x.Len(v.Items, 1)
		x.Equal("custody.issue", v.Items[0].Operation)
		x.Contains(v.Items[0].What, "김민수", "the sentence names who it was about")
		x.NotEmpty(v.Items[0].Who)

		// A page at a time, each from where the last one ended.
		type page struct {
			Items []struct {
				Recorded  string `json:"recorded"`
				Operation string `json:"operation"`
				What      string `json:"what"`
			} `json:"items"`
			Next string `json:"next"`
		}
		var all page
		a.call("recent_events", map[string]any{"limit": 200}, &all)
		x.Empty(all.Next, "all of it in one answer")
		x.Greater(len(all.Items), 4)
		got, args := page{}, map[string]any{"limit": 2}
		for range len(all.Items) {
			var p page
			a.call("recent_events", args, &p)
			got.Items = append(got.Items, p.Items...)
			if p.Next == "" {
				break
			}
			args = map[string]any{"limit": 2, "cursor": p.Next}
		}
		x.Equal(all.Items, got.Items, "read two at a time, it is what one read is")
	}

	// Hand-overs.
	{
		var v struct {
			Items []struct {
				Party   ref    `json:"party"`
				Kind    string `json:"kind"`
				Status  string `json:"status"`
				Due     string `json:"due"`
				Overdue bool   `json:"overdue"`
				Lines   []struct {
					Asset    *ref  `json:"asset"`
					Quantity int64 `json:"quantity"`
				} `json:"lines"`
			} `json:"items"`
		}
		a.call("list_custody", nil, &v)
		x.Len(v.Items, 1)
		got := v.Items[0]
		x.Equal(ref{kim, "김민수"}, got.Party)
		x.Equal("loan", got.Kind)
		x.Equal("open", got.Status)
		x.NotEmpty(got.Due)
		x.False(got.Overdue)
		x.Len(got.Lines, 1)
		x.Equal("NB-1 개발용 노트북", got.Lines[0].Asset.Name)

		a.call("list_custody", map[string]any{"status": "returned"}, &v)
		x.Empty(v.Items)
	}

	// People, and models.
	{
		var v struct {
			Total int `json:"total"`
			Items []struct {
				Id   string `json:"id"`
				Name string `json:"name"`
				Kind string `json:"kind"`
				In   *ref   `json:"in"`
			} `json:"items"`
		}
		a.call("find_people", map[string]any{"query": "민수"}, &v)
		x.Len(v.Items, 1)
		x.Equal(kim, v.Items[0].Id)
		x.Equal(&ref{team, "개발팀"}, v.Items[0].In)

		a.call("find_people", map[string]any{"kind": "team"}, &v)
		x.Len(v.Items, 1)
		x.Equal("개발팀", v.Items[0].Name)

		var m struct {
			Items []struct {
				Name  string `json:"name"`
				Maker string `json:"maker"`
				Type  *ref   `json:"type"`
			} `json:"items"`
		}
		a.call("find_models", map[string]any{"query": "lenovo"}, &m)
		x.Len(m.Items, 1)
		x.Equal("ThinkPad X1", m.Items[0].Name)
		x.Equal(&ref{notebook, "노트북"}, m.Items[0].Type)
	}

	// Reservations.
	{
		var v struct {
			Items []any `json:"items"`
		}
		a.call("reservations", map[string]any{"assets": []string{"R-3"}}, &v)
		x.Empty(v.Items)
	}

	// What it will not do.
	{
		x.Contains(a.refused("get_asset", map[string]any{"asset": "NO-SUCH"}), "NotFound")
		x.Contains(a.refused("asset_at", map[string]any{"asset": "B-1", "at": "yesterday"}), "RFC 3339")
		x.Contains(a.refused("search_assets", map[string]any{"custodian": "김민수"}), "not an id")
	}
}

// TestAnAssistantReadsWhatAnEventHeld: an asset's history answers what each
// entry held (`detail`): of an event, its message field by field -- a thing
// and a person by the name a person reads them by, a moment on the
// workspace's wall -- and of a time row, its own fields. Nothing of a
// person's but their name: never their address or their number, which would
// go to the assistant's provider.
func TestAnAssistantReadsWhatAnEventHeld(t *testing.T) {
	s := serve(t)
	x := s.x
	s.signIn("owner", "owner1234")

	room := made(t, run(t, cli.NewCmdApi(), "asset", "add", "-o", "json", `{"name": "3층 창고", "kind": "space", "tag": "R-3"}`))
	laptop := made(t, run(t, cli.NewCmdApi(), "asset", "add", "-o", "json", `{"name": "개발용 노트북", "kind": "item", "tag": "NB-1", "to": {"id": "`+room+`"}}`))
	kim := made(t, run(t, cli.NewCmdApi(), "party", "add", "-o", "json", `{"name": "김민수", "kind": "person", "email": "minsu@example.com", "phone": "010-1234-5678"}`))
	due := time.Now().Add(72 * time.Hour).UTC().Truncate(time.Second)
	loan := made(t, run(t, cli.NewCmdApi(), "custody", "add", "-o", "json", `{"party": {"id": "`+kim+`"}, "dueAt": "`+due.Format(time.RFC3339)+`", "lines": [{"asset": {"id": "`+laptop+`"}}]}`))

	a := s.assistant()
	var v struct {
		Entries []struct {
			Kind      string            `json:"kind"`
			Operation string            `json:"operation"`
			Detail    map[string]string `json:"detail"`
		} `json:"entries"`
	}
	a.call("asset_history", map[string]any{"asset": "NB-1"}, &v)
	held := map[string]map[string]string{}
	rows := map[string]map[string]string{}
	for _, e := range v.Entries {
		if e.Kind == "event" {
			held[e.Operation] = e.Detail
		} else {
			rows[e.Kind] = e.Detail
		}
	}

	x.Equal(map[string]string{"asset": "NB-1 개발용 노트북", "tag": "NB-1", "name": "개발용 노트북", "kind": "item"}, held["asset.add"])
	lent := held["custody.issue"]
	x.Equal("김민수", lent["party"], "whom it was handed to, by name")
	x.Equal("loan", lent["kind"])
	x.Equal("NB-1 개발용 노트북", lent["asset"])
	// A hand-over has no name of its own: its id, which the tools take.
	x.Equal(pid(t, loan), pid(t, lent["custody"]))
	at, err := time.Parse(time.RFC3339, lent["due"])
	x.NoError(err, lent["due"])
	x.True(due.Equal(at), "%s, the moment it is due", lent["due"])
	x.Equal("located", rows["placement"]["mode"])
	x.Equal("custodian", rows["stewardship"]["role"])

	b, err := json.Marshal(v)
	x.NoError(err)
	x.NotContains(string(b), "minsu@example.com")
	x.NotContains(string(b), "010-1234-5678")
}

// pid is an id a tool or the terminal said, read.
func pid(t *testing.T, v string) pdid.Id {
	t.Helper()
	id, err := pdid.Parse(v)
	if err != nil {
		t.Fatalf("%q: %v", v, err)
	}
	return id
}

// TestAnAssistantSeesWhatItsPersonMay: the wall, as the page has it -- one
// workspace's things are not another's -- whoever the terminal is signed in
// as at the moment of the call, and nothing without a sign-in.
func TestAnAssistantSeesWhatItsPersonMay(t *testing.T) {
	s := serve(t)
	x := s.x
	s.signIn("owner", "owner1234")
	run(t, cli.NewCmdApi(), "--workspace", "acme", "asset", "add", "-o", "json", `{"name": "공용 프린터", "kind": "item", "tag": "PR-1"}`)
	lab := strings.TrimPrefix(strings.Fields(run(t, cli.NewCmdWorkspace(), "new", "연구실"))[0], "@")
	run(t, cli.NewCmdApi(), "--workspace", lab, "asset", "add", "-o", "json", `{"name": "연구용 프린터", "kind": "item", "tag": "PR-1"}`)

	type found struct {
		Total int        `json:"total"`
		Items []answered `json:"items"`
	}
	names := func(v found) []string {
		out := []string{}
		for _, a := range v.Items {
			out = append(out, a.Name)
		}
		return out
	}
	var v found
	s.assistant("--workspace", "acme").call("search_assets", map[string]any{"query": "프린터"}, &v)
	x.Equal([]string{"공용 프린터"}, names(v))
	inLab := s.assistant("--workspace", lab)
	inLab.call("search_assets", map[string]any{"query": "프린터"}, &v)
	x.Equal([]string{"연구용 프린터"}, names(v), "the same tag, in a workspace of its own")
	var me struct {
		Workspace  struct{ Alias string }   `json:"workspace"`
		Workspaces []struct{ Alias string } `json:"workspaces"`
	}
	inLab.call("whoami", nil, &me)
	x.Equal(lab, me.Workspace.Alias)
	x.Len(me.Workspaces, 2)

	// Signed in again, as somebody in one of them and narrowed to reading:
	// the next call is theirs, and says so.
	a := s.assistant()
	s.signIn("kim", "kim-password", "read")
	a.call("search_assets", map[string]any{"query": "프린터"}, &v)
	x.Equal([]string{"공용 프린터"}, names(v), "a member reads the register of the workspace they are in")
	var narrowed struct {
		Allow []string `json:"allow"`
	}
	a.call("whoami", nil, &narrowed)
	x.Contains(narrowed.Allow, "/rove.*/Search")

	run(t, cli.NewCmdLogout())
	x.Contains(a.refused("search_assets", map[string]any{"query": "프린터"}), "rove login")
}

type planned struct {
	Token   string `json:"token"`
	Summary string `json:"summary"`
}

// plan is a plan_* tool's answer.
func (a *assistant) plan(tool string, args map[string]any) planned {
	a.s.t.Helper()
	var v planned
	a.call(tool, args, &v)
	a.s.x.NotEmpty(v.Token)
	return v
}

// apply carries a plan out, as the person said yes to it.
func (a *assistant) apply(p planned) {
	a.s.t.Helper()
	var v struct {
		Done string `json:"done"`
	}
	a.call("apply", map[string]any{"token": p.Token, "summary": p.Summary}, &v)
	a.s.x.Equal(p.Summary, v.Done)
}

// where is the names of what an asset is in now, the outermost first.
func (a *assistant) where(tag string) []string {
	a.s.t.Helper()
	var v answered
	a.call("get_asset", map[string]any{"asset": tag}, &v)
	out := []string{}
	for _, r := range v.Where {
		out = append(out, r.Name)
	}
	return out
}

// TestAnAssistantChangesTheRegisterOnceThePersonSaysYes (D33): a plan says
// what would happen in names and changes nothing; apply, with its token and
// its sentence, does it, once. Who may is the person's role, and a terminal
// narrowed to reading is told so before anything is put to them.
func TestAnAssistantChangesTheRegisterOnceThePersonSaysYes(t *testing.T) {
	s := serve(t)
	x := s.x
	s.signIn("owner", "owner1234")
	building := made(t, run(t, cli.NewCmdApi(), "asset", "add", "-o", "json", `{"name": "본관", "kind": "space", "tag": "B-1"}`))
	made(t, run(t, cli.NewCmdApi(), "asset", "add", "-o", "json", `{"name": "3층 창고", "kind": "space", "tag": "R-3", "to": {"id": "`+building+`"}}`))
	room := made(t, run(t, cli.NewCmdApi(), "asset", "add", "-o", "json", `{"name": "4층 회의실", "kind": "space", "tag": "R-4", "to": {"id": "`+building+`"}}`))
	run(t, cli.NewCmdApi(), "bookable", "add", "-o", "json", `{"asset": {"id": "`+room+`"}, "enabled": true}`)
	kim := made(t, run(t, cli.NewCmdApi(), "party", "add", "-o", "json", `{"name": "김민수", "kind": "person"}`))
	a := s.assistant()

	// Added, where it is put.
	p := a.plan("plan_asset", map[string]any{"action": "add", "name": "개발용 노트북", "tag": "NB-1", "to": "R-3"})
	x.Equal("등록: NB-1 개발용 노트북 (물품), 본관 › 3층 창고에 놓임", p.Summary)
	var found struct {
		Total int `json:"total"`
	}
	a.call("search_assets", map[string]any{"query": "NB-1"}, &found)
	x.Zero(found.Total, "a plan changes nothing")
	a.apply(p)
	x.Equal([]string{"본관", "3층 창고"}, a.where("NB-1"))

	// Moved; and a plan is applied once, as it was read.
	p = a.plan("plan_asset", map[string]any{"action": "move", "asset": "NB-1", "to": "R-4", "reason": "회의 준비"})
	x.Equal("이동: NB-1 개발용 노트북, 본관 › 3층 창고 → 본관 › 4층 회의실 (놓임) — 회의 준비", p.Summary)
	x.Contains(a.refused("apply", map[string]any{"token": p.Token, "summary": "이동: NB-1 → 다른 곳"}), "not this plan's")
	x.Equal([]string{"본관", "3층 창고"}, a.where("NB-1"), "not until the person's yes")
	a.apply(p)
	x.Equal([]string{"본관", "4층 회의실"}, a.where("NB-1"))
	x.Contains(a.refused("apply", map[string]any{"token": p.Token, "summary": p.Summary}), "applied already")
	x.Contains(a.refused("apply", map[string]any{"token": "plan_nothing", "summary": p.Summary}), "no such plan")

	// What it is, and who manages it.
	p = a.plan("plan_asset", map[string]any{"action": "set", "asset": "NB-1", "set": map[string]any{"condition": "damaged"}})
	x.Equal("속성 변경: NB-1 개발용 노트북: 컨디션 → 손상", p.Summary)
	a.apply(p)
	p = a.plan("plan_asset", map[string]any{"action": "assign", "asset": "NB-1", "role": "manager", "party": kim})
	x.Equal("관리 담당자 지정: NB-1 개발용 노트북 → 김민수", p.Summary)
	a.apply(p)
	var full answered
	a.call("get_asset", map[string]any{"asset": "NB-1"}, &full)
	x.Equal("김민수", full.Stewards["manager"])

	// A row of its history that was wrong, by the row asset_history names;
	// and a relation to another.
	var hist struct {
		Entries []struct {
			Kind string `json:"kind"`
			Row  string `json:"row"`
			What string `json:"what"`
		} `json:"entries"`
	}
	a.call("asset_history", map[string]any{"asset": "NB-1"}, &hist)
	row, what := "", ""
	for _, e := range hist.Entries {
		if e.Kind == "fact" && strings.Contains(e.What, "손상") && row == "" {
			row, what = e.Row, e.What
		}
	}
	x.NotEmpty(row, "the condition, a fact with a row: %+v", hist.Entries)
	p = a.plan("plan_asset", map[string]any{"action": "correct", "asset": "NB-1", "row": row, "retract": true, "reason": "잘못 입력"})
	x.Equal("정정: NB-1 개발용 노트북의 기록 '"+what+"'을(를) 없었던 일로 — 잘못 입력", p.Summary)
	a.apply(p)
	made(t, run(t, cli.NewCmdApi(), "asset", "add", "-o", "json", `{"name": "도킹 스테이션", "kind": "item", "tag": "DK-1"}`))
	p = a.plan("plan_asset", map[string]any{"action": "relate", "asset": "NB-1", "target": "DK-1", "relation": "connected_to"})
	x.Equal("연결: NB-1 개발용 노트북 ↔ DK-1 도킹 스테이션", p.Summary)
	a.apply(p)

	// Lent, and given back.
	due := time.Now().Add(72 * time.Hour)
	p = a.plan("plan_custody", map[string]any{"action": "issue", "party": kim, "assets": []string{"NB-1"}, "due": due.Format(time.RFC3339)})
	x.Equal("대여: NB-1 개발용 노트북 → 김민수, 반납 기한 "+due.Local().Format("2006-01-02 15:04"), p.Summary)
	a.apply(p)
	var out struct {
		Items []struct {
			Id     string `json:"id"`
			Status string `json:"status"`
			Lines  []struct {
				Id string `json:"id"`
			} `json:"lines"`
		} `json:"items"`
	}
	a.call("list_custody", nil, &out)
	x.Len(out.Items, 1)
	x.NotEmpty(out.Items[0].Lines[0].Id)
	p = a.plan("plan_custody", map[string]any{"action": "return", "custody": out.Items[0].Id, "to": "R-3"})
	x.Equal("반납: 김민수에게 대여한 것: 아직 나가 있는 것 전부 → 본관 › 3층 창고", p.Summary)
	a.apply(p)
	a.call("list_custody", nil, &out)
	x.Empty(out.Items, "nothing out")
	a.call("list_custody", map[string]any{"status": "any"}, &out)
	x.Len(out.Items, 1, "every state, which is a list of no filter")
	x.Equal("returned", out.Items[0].Status)
	x.Equal([]string{"본관", "3층 창고"}, a.where("NB-1"), "back where it was put")

	// Reserved, and cancelled.
	now := time.Now()
	begins := time.Date(now.Year(), now.Month(), now.Day()+1, 14, 0, 0, 0, time.Local)
	ends := begins.Add(time.Hour)
	p = a.plan("plan_reservation", map[string]any{"action": "reserve", "assets": []string{"R-4"}, "begins": begins.Format(time.RFC3339), "ends": ends.Format(time.RFC3339), "name": "주간 회의"})
	x.Equal("예약: R-4 4층 회의실, "+begins.Local().Format("2006-01-02 15:04")+"–"+ends.Local().Format("15:04")+", 주간 회의", p.Summary)
	a.apply(p)
	var cal struct {
		Items []struct {
			Id     string `json:"id"`
			Status string `json:"status"`
		} `json:"items"`
	}
	a.call("reservations", map[string]any{"assets": []string{"R-4"}, "from": begins.Add(-time.Minute).Format(time.RFC3339), "to": ends.Format(time.RFC3339)}, &cal)
	x.Len(cal.Items, 1)
	p = a.plan("plan_reservation", map[string]any{"action": "cancel", "reservation": cal.Items[0].Id, "reason": "일정 변경"})
	x.Contains(p.Summary, "예약 취소: R-4 4층 회의실, ")
	a.apply(p)
	a.call("reservations", map[string]any{"assets": []string{"R-4"}, "from": begins.Add(-time.Minute).Format(time.RFC3339), "to": ends.Format(time.RFC3339)}, &cal)
	x.Equal("cancelled", cal.Items[0].Status)
	// And what happened to it, newest first, by whom and why.
	var happened struct {
		Reservation ref `json:"reservation"`
		Entries     []struct {
			What      string `json:"what"`
			Operation string `json:"operation"`
			Who       string `json:"who"`
			Why       string `json:"why"`
		} `json:"entries"`
	}
	a.call("reservation_history", map[string]any{"reservation": cal.Items[0].Id}, &happened)
	x.Equal(ref{cal.Items[0].Id, "주간 회의"}, happened.Reservation)
	x.Len(happened.Entries, 2)
	x.Equal("reservation.cancel", happened.Entries[0].Operation)
	x.Equal("예약 취소: 주간 회의", happened.Entries[0].What)
	x.Equal("일정 변경", happened.Entries[0].Why)
	x.Equal("관리자", happened.Entries[0].Who, "the person, by name")
	x.Equal("reservation.add", happened.Entries[1].Operation)
	x.True(strings.HasPrefix(happened.Entries[1].What, "예약: 4층 회의실 "), happened.Entries[1].What)

	// Two applies of one plan at once: one does it, and the other is told.
	p = a.plan("plan_asset", map[string]any{"action": "add", "name": "동시에 넣은 모니터", "tag": "MN-2"})
	results := make(chan *mcp.CallToolResult, 2)
	for range 2 {
		go func() {
			res, err := a.session.CallTool(context.Background(), &mcp.CallToolParams{Name: "apply", Arguments: map[string]any{"token": p.Token, "summary": p.Summary}})
			x.NoError(err)
			results <- res
		}()
	}
	ok, refused := 0, 0
	for range 2 {
		res := <-results
		if res.IsError {
			refused++
			x.Regexp("applied already|being applied already", said(res))
		} else {
			ok++
		}
	}
	x.Equal(1, ok)
	x.Equal(1, refused)
	a.call("search_assets", map[string]any{"query": "MN-2"}, &found)
	x.Equal(1, found.Total, "added once")

	// Added by mistake, and taken back.
	p = a.plan("plan_asset", map[string]any{"action": "add", "name": "잘못 넣은 모니터", "tag": "MN-9"})
	a.apply(p)
	p = a.plan("plan_asset", map[string]any{"action": "erase", "asset": "MN-9"})
	x.Equal("등록 취소: MN-9 잘못 넣은 모니터 (잘못 등록한 것으로 지웁니다)", p.Summary)
	a.apply(p)
	a.call("search_assets", map[string]any{"query": "MN-9"}, &found)
	x.Zero(found.Total)

	// A member may plan what they may not do; the server says no when it
	// is applied.
	s.signIn("kim", "kim-password")
	p = a.plan("plan_asset", map[string]any{"action": "move", "asset": "NB-1", "to": "R-4"})
	x.Contains(a.refused("apply", map[string]any{"token": p.Token, "summary": p.Summary}), "PermissionDenied")
	// Nor read the history of somebody else's reservation.
	x.Contains(a.refused("reservation_history", map[string]any{"reservation": cal.Items[0].Id}), "PermissionDenied")

	// A terminal narrowed to reading is told so before anything is put to
	// the person.
	s.signIn("owner", "owner1234", "read")
	x.Contains(a.refused("plan_asset", map[string]any{"action": "move", "asset": "NB-1", "to": "R-4"}), "may not call /rove.AssetService/Move")
}

// TestAnAssistantTakesWhatGoesOutOfUseOutOfItsKits: a plan that puts an
// asset out of use says what it stays a member of, so that the person is told
// before they say yes; and asked to, it takes the asset out of each, named --
// as it is when a count's finding is settled as lost.
func TestAnAssistantTakesWhatGoesOutOfUseOutOfItsKits(t *testing.T) {
	s := serve(t)
	x := s.x
	s.signIn("owner", "owner1234")
	room := made(t, run(t, cli.NewCmdApi(), "asset", "add", "-o", "json", `{"name": "장비실", "kind": "space", "tag": "S-1"}`))
	kit := made(t, run(t, cli.NewCmdApi(), "asset", "add", "-o", "json", `{"name": "촬영 키트", "kind": "kit", "tag": "K-1"}`))
	for _, v := range []string{`{"name": "카메라", "kind": "item", "tag": "CM-1"}`, `{"name": "렌즈", "kind": "item", "tag": "LN-1", "to": {"id": "` + room + `"}}`} {
		member := made(t, run(t, cli.NewCmdApi(), "asset", "add", "-o", "json", v))
		run(t, cli.NewCmdApi(), "asset", "relate", member, `{"target": {"id": "`+kit+`"}, "kind": "member_of", "required": true}`)
	}
	a := s.assistant()
	inKit := func(tag string) bool {
		var hist struct {
			Entries []struct {
				Kind   string            `json:"kind"`
				Until  string            `json:"until"`
				Detail map[string]string `json:"detail"`
			} `json:"entries"`
		}
		a.call("asset_history", map[string]any{"asset": tag}, &hist)
		for _, e := range hist.Entries {
			if e.Kind == "link" && e.Until == "" && e.Detail["side"] == "source" {
				return true
			}
		}
		return false
	}

	p := a.plan("plan_asset", map[string]any{"action": "set", "asset": "CM-1", "set": map[string]any{"status": "disposed"}})
	x.Equal("속성 변경: CM-1 카메라: 상태 → 폐기 (K-1 촬영 키트의 구성품으로 남음)", p.Summary)
	p = a.plan("plan_asset", map[string]any{"action": "set", "asset": "CM-1", "set": map[string]any{"status": "disposed"}, "leave_kits": true, "reason": "파손"})
	x.Equal("속성 변경: CM-1 카메라: 상태 → 폐기, K-1 촬영 키트에서 빼기 — 파손", p.Summary)
	x.True(inKit("CM-1"), "a plan changes nothing")
	a.apply(p)
	x.False(inKit("CM-1"))
	x.Contains(a.refused("plan_asset", map[string]any{"action": "set", "asset": "LN-1", "set": map[string]any{"condition": "damaged"}, "leave_kits": true}), "leave_kits")

	// A count that never saw the lens, settled as lost, and out of the kit.
	p = a.plan("plan_count", map[string]any{"action": "start", "name": "10월 실사", "scope": "S-1"})
	a.apply(p)
	var counts struct {
		Items []struct {
			Id       string `json:"id"`
			Findings []struct {
				Id   string `json:"id"`
				Kind string `json:"kind"`
			} `json:"findings"`
		} `json:"items"`
	}
	a.call("list_counts", map[string]any{"status": "open"}, &counts)
	x.Len(counts.Items, 1)
	a.applied(a.plan("plan_count", map[string]any{"action": "reconcile", "count": counts.Items[0].Id}))
	a.call("list_counts", map[string]any{"count": counts.Items[0].Id}, &counts)
	x.Len(counts.Items[0].Findings, 1)
	missing := counts.Items[0].Findings[0].Id
	p = a.plan("plan_count", map[string]any{"action": "resolve", "finding": missing, "resolution": "lost"})
	x.Equal("실사 처리: LN-1 렌즈: 분실 처리 (K-1 촬영 키트의 구성품으로 남음)", p.Summary)
	x.Contains(a.refused("plan_count", map[string]any{"action": "resolve", "finding": missing, "resolution": "ignored", "leave_kits": true}), "leave_kits")
	p = a.plan("plan_count", map[string]any{"action": "resolve", "finding": missing, "resolution": "lost", "leave_kits": true})
	x.Equal("실사 처리: LN-1 렌즈: 분실 처리, K-1 촬영 키트에서 빼기", p.Summary)
	a.apply(p)
	x.False(inKit("LN-1"))
}

// TestAnAssistantSaysWhatABookingTakesIsOutOfUse: a booking says the items it
// takes that are out of use -- here a member kept in its kit as it went --
// which its pickup does not hand over, by name and status, and says it no
// more once the item is in use again (design 4, D42).
func TestAnAssistantSaysWhatABookingTakesIsOutOfUse(t *testing.T) {
	s := serve(t)
	x := s.x
	s.signIn("owner", "owner1234")
	kit := made(t, run(t, cli.NewCmdApi(), "asset", "add", "-o", "json", `{"name": "촬영 키트", "kind": "kit", "tag": "K-1"}`))
	run(t, cli.NewCmdApi(), "bookable", "add", "-o", "json", `{"asset": {"id": "`+kit+`"}, "enabled": true}`)
	battery := made(t, run(t, cli.NewCmdApi(), "asset", "add", "-o", "json", `{"name": "배터리", "kind": "item", "tag": "BT-1"}`))
	run(t, cli.NewCmdApi(), "asset", "relate", battery, `{"target": {"id": "`+kit+`"}, "kind": "member_of", "required": true}`)
	a := s.assistant()

	now := time.Now()
	begins := time.Date(now.Year(), now.Month(), now.Day()+1, 14, 0, 0, 0, time.Local)
	ends := begins.Add(time.Hour)
	a.applied(a.plan("plan_reservation", map[string]any{"action": "reserve", "assets": []string{"K-1"}, "begins": begins.Format(time.RFC3339), "ends": ends.Format(time.RFC3339), "name": "제품 촬영"}))
	a.applied(a.plan("plan_asset", map[string]any{"action": "set", "asset": "BT-1", "set": map[string]any{"status": "retired"}}))
	span := map[string]any{"assets": []string{"K-1"}, "from": begins.Add(-time.Minute).Format(time.RFC3339), "to": ends.Format(time.RFC3339)}
	type booking struct {
		Items []struct {
			Name     string `json:"name"`
			OutOfUse []ref  `json:"out_of_use"`
		} `json:"items"`
	}
	var cal booking
	a.call("reservations", span, &cal)
	x.Len(cal.Items, 1)
	x.Equal("제품 촬영", cal.Items[0].Name)
	x.Equal([]ref{{battery, "BT-1 배터리 (retired)"}}, cal.Items[0].OutOfUse)

	a.applied(a.plan("plan_asset", map[string]any{"action": "set", "asset": "BT-1", "set": map[string]any{"status": "active"}}))
	var again booking
	a.call("reservations", span, &again)
	x.Len(again.Items, 1)
	x.Empty(again.Items[0].OutOfUse)
}

// TestAnAssistantSaysWhatABookingTakesIsOut: a booking says what it takes
// that somebody has out over its time -- lent at the desk after it was booked
// -- by name, and until when, and says it no more once it is back (design 4,
// D53).
func TestAnAssistantSaysWhatABookingTakesIsOut(t *testing.T) {
	s := serve(t)
	x := s.x
	s.signIn("owner", "owner1234")
	camera := made(t, run(t, cli.NewCmdApi(), "asset", "add", "-o", "json", `{"name": "카메라", "kind": "item", "tag": "CM-1"}`))
	run(t, cli.NewCmdApi(), "bookable", "add", "-o", "json", `{"asset": {"id": "`+camera+`"}, "enabled": true}`)
	kim := made(t, run(t, cli.NewCmdApi(), "party", "add", "-o", "json", `{"name": "김민준", "kind": "person"}`))
	a := s.assistant()

	now := time.Now()
	begins := time.Date(now.Year(), now.Month(), now.Day()+1, 14, 0, 0, 0, time.Local)
	ends := begins.Add(2 * time.Hour)
	a.applied(a.plan("plan_reservation", map[string]any{"action": "reserve", "assets": []string{"CM-1"}, "begins": begins.Format(time.RFC3339), "ends": ends.Format(time.RFC3339), "name": "제품 촬영"}))
	due := begins.Add(time.Hour)
	a.applied(a.plan("plan_custody", map[string]any{"action": "issue", "party": kim, "assets": []string{"CM-1"}, "kind": "loan", "due": due.Format(time.RFC3339)}))
	span := map[string]any{"assets": []string{"CM-1"}, "from": begins.Add(-time.Minute).Format(time.RFC3339), "to": ends.Format(time.RFC3339)}
	type booking struct {
		Items []struct {
			Name string `json:"name"`
			Out  []ref  `json:"out"`
		} `json:"items"`
	}
	var cal booking
	a.call("reservations", span, &cal)
	x.Len(cal.Items, 1)
	x.Len(cal.Items[0].Out, 1)
	x.Equal(camera, cal.Items[0].Out[0].Id)
	x.Contains(cal.Items[0].Out[0].Name, "CM-1 카메라 (lent until ")
	x.NotContains(cal.Items[0].Out[0].Name, "김민준", "nobody is said who has it")

	var lent struct {
		Items []struct {
			Id string `json:"id"`
		} `json:"items"`
	}
	a.call("list_custody", nil, &lent)
	x.Len(lent.Items, 1)
	a.applied(a.plan("plan_custody", map[string]any{"action": "return", "custody": lent.Items[0].Id}))
	var again booking
	a.call("reservations", span, &again)
	x.Len(again.Items, 1)
	x.Empty(again.Items[0].Out)
}

// TestAnAssistantIsToldWhatIsOutOfUseIsNotBooked: a plan to reserve what is
// out of use is refused, naming each of it with its status, before the
// person is asked -- the server would refuse it (D33, D43) -- and so is one
// to approve, confirm or check into a reservation of what went out of use
// since; one to reject it is planned. The reservation says what it booked
// is out of use, a room as well.
func TestAnAssistantIsToldWhatIsOutOfUseIsNotBooked(t *testing.T) {
	s := serve(t)
	x := s.x
	s.signIn("owner", "owner1234")
	room := made(t, run(t, cli.NewCmdApi(), "asset", "add", "-o", "json", `{"name": "회의실", "kind": "space", "tag": "R-1"}`))
	run(t, cli.NewCmdApi(), "bookable", "add", "-o", "json", `{"asset": {"id": "`+room+`"}, "enabled": true, "approval": true}`)
	battery := made(t, run(t, cli.NewCmdApi(), "asset", "add", "-o", "json", `{"name": "배터리", "kind": "item", "tag": "BT-1"}`))
	run(t, cli.NewCmdApi(), "bookable", "add", "-o", "json", `{"asset": {"id": "`+battery+`"}, "enabled": true}`)
	a := s.assistant()

	now := time.Now()
	begins := time.Date(now.Year(), now.Month(), now.Day()+1, 14, 0, 0, 0, time.Local)
	ends := begins.Add(time.Hour)
	reserve := func(assets ...string) map[string]any {
		return map[string]any{"action": "reserve", "assets": assets, "begins": begins.Format(time.RFC3339), "ends": ends.Format(time.RFC3339), "name": "주간 회의"}
	}
	a.applied(a.plan("plan_reservation", reserve("R-1")))
	a.applied(a.plan("plan_reservation", reserve("BT-1")))
	type booking struct {
		Items []struct {
			Id       string `json:"id"`
			Status   string `json:"status"`
			Assets   []ref  `json:"assets"`
			OutOfUse []ref  `json:"out_of_use"`
		} `json:"items"`
	}
	var cal booking
	a.call("reservations", map[string]any{"from": begins.Add(-time.Minute).Format(time.RFC3339), "to": ends.Format(time.RFC3339)}, &cal)
	x.Len(cal.Items, 2)
	of, was := map[string]string{}, map[string]string{}
	for _, v := range cal.Items {
		of[v.Assets[0].Id], was[v.Assets[0].Id] = v.Id, v.Status
	}
	x.Equal(map[string]string{room: "requested", battery: "confirmed"}, was)

	a.applied(a.plan("plan_asset", map[string]any{"action": "set", "asset": "R-1", "set": map[string]any{"status": "retired"}}))
	a.applied(a.plan("plan_asset", map[string]any{"action": "set", "asset": "BT-1", "set": map[string]any{"status": "lost"}}))

	x.Contains(a.refused("plan_reservation", reserve("BT-1", "R-1")), "not booked anew, and the server would refuse it: BT-1 배터리 (lost), R-1 회의실 (retired)")
	refused := a.refused("plan_reservation", map[string]any{"action": "approve", "reservation": of[room]})
	x.Contains(refused, "R-1 회의실 (retired)")
	x.Contains(refused, "would not approve it; reject or cancel it instead")
	x.Contains(a.refused("plan_reservation", map[string]any{"action": "check_in", "reservation": of[battery]}), "BT-1 배터리 (lost), and the server would not check it in")
	a.applied(a.plan("plan_reservation", map[string]any{"action": "reject", "reservation": of[room], "reason": "회의실 불용"}))

	var again booking
	a.call("reservations", map[string]any{"from": begins.Add(-time.Minute).Format(time.RFC3339), "to": ends.Format(time.RFC3339)}, &again)
	for _, v := range again.Items {
		if v.Id == of[battery] {
			x.Equal([]ref{{battery, "BT-1 배터리 (lost)"}}, v.OutOfUse)
		} else {
			x.Equal("rejected", v.Status)
			x.Empty(v.OutOfUse, "over, and said no more")
		}
	}
}

// TestAnAssistantIsToldWhatWasDeletedIsOutOfUse: a reservation of what was
// deleted as registered by mistake says so, as "voided", and a plan to
// approve it is refused, naming what it booked as the reservation says it --
// it can no longer be read -- before the person is asked (D33, D48); one to
// reject it is planned.
func TestAnAssistantIsToldWhatWasDeletedIsOutOfUse(t *testing.T) {
	s := serve(t)
	x := s.x
	s.signIn("owner", "owner1234")
	pool := made(t, run(t, cli.NewCmdApi(), "asset", "add", "-o", "json", `{"name": "카메라 풀", "kind": "group", "tag": "G-1"}`))
	camera := made(t, run(t, cli.NewCmdApi(), "asset", "add", "-o", "json", `{"name": "카메라", "kind": "item", "tag": "CM-1"}`))
	run(t, cli.NewCmdApi(), "asset", "relate", camera, `{"target": {"id": "`+pool+`"}, "kind": "member_of"}`)
	run(t, cli.NewCmdApi(), "bookable", "add", "-o", "json", `{"asset": {"id": "`+pool+`"}, "enabled": true, "approval": true}`)
	a := s.assistant()

	now := time.Now()
	begins := time.Date(now.Year(), now.Month(), now.Day()+1, 14, 0, 0, 0, time.Local)
	ends := begins.Add(time.Hour)
	a.applied(a.plan("plan_reservation", map[string]any{"action": "reserve", "assets": []string{"G-1"}, "begins": begins.Format(time.RFC3339), "ends": ends.Format(time.RFC3339), "name": "촬영"}))
	a.apply(a.plan("plan_asset", map[string]any{"action": "erase", "asset": "G-1"}))

	var cal struct {
		Items []struct {
			Id       string `json:"id"`
			OutOfUse []ref  `json:"out_of_use"`
		} `json:"items"`
	}
	a.call("reservations", map[string]any{"from": begins.Add(-time.Minute).Format(time.RFC3339), "to": ends.Format(time.RFC3339)}, &cal)
	x.Len(cal.Items, 1)
	x.Equal([]ref{{pool, "G-1 카메라 풀 (voided)"}}, cal.Items[0].OutOfUse)
	refused := a.refused("plan_reservation", map[string]any{"action": "approve", "reservation": cal.Items[0].Id})
	x.Contains(refused, "G-1 카메라 풀 (voided)")
	x.Contains(refused, "would not approve it; reject or cancel it instead")
	a.applied(a.plan("plan_reservation", map[string]any{"action": "reject", "reservation": cal.Items[0].Id, "reason": "등록 취소"}))
}

// TestAnAssistantSaysWhetherAKitTakesAMember: a plan that gives a kit a
// member says whether it is a required one, which every booking of the kit
// takes with it, or an optional one; and of a member the kit has already, that
// it is made the other -- which a plan that said nothing of it did, unsaid.
func TestAnAssistantSaysWhetherAKitTakesAMember(t *testing.T) {
	s := serve(t)
	x := s.x
	s.signIn("owner", "owner1234")
	for _, v := range []string{
		`{"name": "촬영 키트", "kind": "kit", "tag": "K-1"}`,
		`{"name": "카메라 풀", "kind": "group", "tag": "G-1"}`,
		`{"name": "카메라", "kind": "item", "tag": "CM-1"}`,
		`{"name": "렌즈", "kind": "item", "tag": "LN-1"}`,
	} {
		made(t, run(t, cli.NewCmdApi(), "asset", "add", "-o", "json", v))
	}
	a := s.assistant()
	// What the history says of the asset's membership now: whether it is
	// required, or "" when it is a member of nothing.
	required := func(tag string) string {
		var hist struct {
			Entries []struct {
				Kind   string            `json:"kind"`
				Until  string            `json:"until"`
				Detail map[string]string `json:"detail"`
			} `json:"entries"`
		}
		a.call("asset_history", map[string]any{"asset": tag}, &hist)
		for _, e := range hist.Entries {
			if e.Kind == "link" && e.Until == "" && e.Detail["side"] == "source" {
				return e.Detail["required"]
			}
		}
		return ""
	}

	p := a.plan("plan_asset", map[string]any{"action": "relate", "asset": "CM-1", "target": "K-1", "relation": "member_of", "required": true})
	x.Equal("구성에 넣기: CM-1 카메라 → K-1 촬영 키트 (필수 구성품)", p.Summary)
	x.Empty(required("CM-1"), "a plan changes nothing")
	a.apply(p)
	x.Equal("true", required("CM-1"))
	p = a.plan("plan_asset", map[string]any{"action": "relate", "asset": "LN-1", "target": "K-1", "relation": "member_of"})
	x.Equal("구성에 넣기: LN-1 렌즈 → K-1 촬영 키트 (선택 구성품)", p.Summary)
	a.apply(p)
	x.Equal("false", required("LN-1"))

	// A member it has already is made what the plan says, and the plan says
	// that is a change.
	p = a.plan("plan_asset", map[string]any{"action": "relate", "asset": "CM-1", "target": "K-1", "relation": "member_of", "reason": "예비가 있음"})
	x.Equal("선택 구성품으로 바꾸기: CM-1 카메라 → K-1 촬영 키트 — 예비가 있음", p.Summary)
	a.apply(p)
	x.Equal("false", required("CM-1"))
	p = a.plan("plan_asset", map[string]any{"action": "relate", "asset": "LN-1", "target": "K-1", "relation": "member_of", "required": true})
	x.Equal("필수 구성품으로 바꾸기: LN-1 렌즈 → K-1 촬영 키트", p.Summary)
	a.apply(p)
	x.Equal("true", required("LN-1"))

	// Nothing but a kit's member is either.
	for _, v := range []map[string]any{
		{"action": "relate", "asset": "CM-1", "target": "G-1", "relation": "member_of", "required": true},
		{"action": "relate", "asset": "CM-1", "target": "K-1", "relation": "member_of", "end": true, "required": true},
		{"action": "relate", "asset": "CM-1", "target": "LN-1", "relation": "connected_to", "required": true},
	} {
		x.Contains(a.refused("plan_asset", v), "required: only of a member given to a kit")
	}
}

// applied carries a plan out and answers what else apply said.
func (a *assistant) applied(p planned) []string {
	a.s.t.Helper()
	var v struct {
		Done string   `json:"done"`
		More []string `json:"more"`
	}
	a.call("apply", map[string]any{"token": p.Token, "summary": p.Summary}, &v)
	a.s.x.Equal(p.Summary, v.Done)
	return v.More
}

// TestAnAssistantKeepsTheRestOfTheRegister (D33): stock, a repair, a count,
// a purchase, a model and labels, each planned in names and applied; and
// the lists that answer the ids the plans take.
func TestAnAssistantKeepsTheRestOfTheRegister(t *testing.T) {
	s := serve(t)
	x := s.x
	s.signIn("owner", "owner1234")
	building := made(t, run(t, cli.NewCmdApi(), "asset", "add", "-o", "json", `{"name": "본관", "kind": "space", "tag": "B-1"}`))
	store := made(t, run(t, cli.NewCmdApi(), "asset", "add", "-o", "json", `{"name": "창고", "kind": "space", "tag": "S-1", "to": {"id": "`+building+`"}}`))
	made(t, run(t, cli.NewCmdApi(), "asset", "add", "-o", "json", `{"name": "2층 창고", "kind": "space", "tag": "S-2", "to": {"id": "`+building+`"}}`))
	made(t, run(t, cli.NewCmdApi(), "asset", "add", "-o", "json", `{"name": "개발용 노트북", "kind": "item", "tag": "NB-1", "to": {"id": "`+store+`"}}`))
	made(t, run(t, cli.NewCmdApi(), "asset", "add", "-o", "json", `{"name": "사무용 의자", "kind": "item", "tag": "CH-1", "to": {"id": "`+store+`"}}`))
	vendor := made(t, run(t, cli.NewCmdApi(), "party", "add", "-o", "json", `{"name": "델코리아", "kind": "vendor"}`))
	a := s.assistant()

	// A model, and stock of it.
	p := a.plan("plan_model", map[string]any{"action": "add", "name": "A4 복사용지", "maker": "한국제지", "code": "A4-80"})
	x.Equal("모델 등록: A4 복사용지 (한국제지), 코드 A4-80", p.Summary)
	a.apply(p)
	var models struct {
		Items []struct {
			Id string `json:"id"`
		} `json:"items"`
	}
	a.call("find_models", map[string]any{"query": "a4-80"}, &models)
	x.Len(models.Items, 1)
	paper := models.Items[0].Id
	p = a.plan("plan_model", map[string]any{"action": "update", "model": paper, "maker": "한국제지(주)"})
	x.Equal("모델 변경: A4 복사용지: 제조사 → 한국제지(주)", p.Summary)
	a.apply(p)

	p = a.plan("plan_stock", map[string]any{"action": "add", "name": "A4 용지", "model": paper, "space": "S-1", "quantity": 10, "unit": "박스", "threshold": 3})
	x.Equal("재고 등록: A4 용지 10박스, 본관 › 창고 (모델 A4 복사용지, 3박스 이하면 부족)", p.Summary)
	a.apply(p)
	var stock struct {
		Items []struct {
			Id       string `json:"id"`
			Quantity int64  `json:"quantity"`
			Low      bool   `json:"low"`
		} `json:"items"`
	}
	a.call("list_stock", map[string]any{"name": "a4"}, &stock)
	x.Len(stock.Items, 1)
	sheets := stock.Items[0].Id
	p = a.plan("plan_stock", map[string]any{"action": "consume", "stock": sheets, "quantity": 8})
	x.Equal("사용: A4 용지 -8박스 (10 → 2)", p.Summary)
	a.apply(p)
	a.call("list_stock", map[string]any{"low": true}, &stock)
	x.Len(stock.Items, 1, "low, at or below its threshold")
	p = a.plan("plan_stock", map[string]any{"action": "receive", "stock": sheets, "quantity": 5})
	x.Equal("입고: A4 용지 +5박스 (2 → 7)", p.Summary)
	a.apply(p)
	p = a.plan("plan_stock", map[string]any{"action": "adjust", "stock": sheets, "quantity": -1, "reason": "실사 결과"})
	x.Equal("조정: A4 용지 -1박스 (7 → 6) — 실사 결과", p.Summary)
	a.apply(p)
	p = a.plan("plan_stock", map[string]any{"action": "transfer", "stock": sheets, "to": "S-2", "quantity": 2})
	x.Equal("재고 이동: A4 용지 2박스, 본관 › 창고 → 본관 › 2층 창고", p.Summary)
	a.apply(p)
	a.call("list_stock", map[string]any{"space": "S-1"}, &stock)
	x.EqualValues(4, stock.Items[0].Quantity)

	// A repair, from reported to done.
	p = a.plan("plan_work_order", map[string]any{"action": "report", "asset": "NB-1", "name": "화면 깜빡임"})
	x.Equal("작업 접수: NB-1 개발용 노트북: 화면 깜빡임 (수리)", p.Summary)
	a.apply(p)
	var work struct {
		Items []struct {
			Id     string `json:"id"`
			Status string `json:"status"`
		} `json:"items"`
	}
	a.call("list_work_orders", map[string]any{"asset": "NB-1"}, &work)
	x.Len(work.Items, 1)
	p = a.plan("plan_work_order", map[string]any{"action": "update", "work_order": work.Items[0].Id, "status": "in_progress"})
	x.Equal("작업 변경: 화면 깜빡임: 상태 → 진행 중", p.Summary)
	a.apply(p)
	p = a.plan("plan_work_order", map[string]any{"action": "update", "work_order": work.Items[0].Id, "cost": 450000})
	x.Equal("작업 변경: 화면 깜빡임: 비용 → 450,000 KRW", p.Summary)
	a.apply(p)
	// Another currency under the same number is another amount.
	x.Contains(a.refused("plan_work_order", map[string]any{"action": "update", "work_order": work.Items[0].Id, "currency": "USD", "name": "패널 교체"}), "cost:")
	p = a.plan("plan_work_order", map[string]any{"action": "complete", "work_order": work.Items[0].Id, "condition": "good", "reason": "패널 교체"})
	x.Equal("작업 완료: 화면 깜빡임, 컨디션 → 양호 — 패널 교체", p.Summary)
	a.apply(p)
	a.call("list_work_orders", map[string]any{"asset": "NB-1"}, &work)
	x.Equal("done", work.Items[0].Status)

	// A count of the store: one thing seen, one never, and that one settled.
	p = a.plan("plan_count", map[string]any{"action": "start", "name": "10월 실사", "scope": "S-1"})
	x.Equal("실사 시작: 10월 실사, 본관 › 창고 안의 모든 것", p.Summary)
	a.apply(p)
	var counts struct {
		Items []struct {
			Id       string `json:"id"`
			Status   string `json:"status"`
			Findings []struct {
				Id    string `json:"id"`
				Kind  string `json:"kind"`
				Asset *ref   `json:"asset"`
			} `json:"findings"`
		} `json:"items"`
	}
	a.call("list_counts", map[string]any{"status": "open"}, &counts)
	x.Len(counts.Items, 1)
	count := counts.Items[0].Id
	p = a.plan("plan_count", map[string]any{"action": "scan", "count": count, "asset": "NB-1"})
	x.Equal("실사 스캔: 10월 실사: NB-1 개발용 노트북 확인", p.Summary)
	a.apply(p)
	p = a.plan("plan_count", map[string]any{"action": "reconcile", "count": count})
	more := a.applied(p)
	x.Len(more, 1)
	x.Contains(more[0], "미발견 1")
	a.call("list_counts", map[string]any{"count": count}, &counts)
	missing := ""
	for _, f := range counts.Items[0].Findings {
		if f.Kind == "missing" {
			missing = f.Id
			x.Equal("CH-1 사무용 의자", f.Asset.Name)
		}
	}
	x.NotEmpty(missing)
	p = a.plan("plan_count", map[string]any{"action": "resolve", "finding": missing, "resolution": "ignored", "reason": "다른 층에 있음"})
	x.Equal("실사 처리: CH-1 사무용 의자: 무시 — 다른 층에 있음", p.Summary)
	a.apply(p)
	p = a.plan("plan_count", map[string]any{"action": "close", "count": count})
	a.apply(p)

	// Bought, and received as assets.
	p = a.plan("plan_purchase", map[string]any{"action": "add", "name": "노트북 2대", "vendor": vendor, "currency": "KRW",
		"lines": []map[string]any{{"model": paper, "quantity": 2, "unit_cost": 1500000, "receive_as": "asset"}}})
	x.Equal("구매 등록: 노트북 2대, 델코리아에서: A4 복사용지 ×2 (자산으로), 합계 3,000,000 KRW", p.Summary)
	a.apply(p)
	var bought struct {
		Items []struct {
			Id    string `json:"id"`
			Lines []struct {
				Quantity int64 `json:"quantity"`
			} `json:"lines"`
		} `json:"items"`
	}
	a.call("list_purchases", map[string]any{"status": "ordered"}, &bought)
	x.Len(bought.Items, 1)
	p = a.plan("plan_purchase", map[string]any{"action": "receive", "purchase": bought.Items[0].Id, "into": "S-2", "tag_prefix": "NB-2"})
	x.Equal("구매 입고: 노트북 2대 → 본관 › 2층 창고 (자산 번호 NB-2…)", p.Summary)
	x.Len(a.applied(p), 2, "the two assets it made")

	// Labels: made, stuck on, taken off.
	p = a.plan("plan_label", map[string]any{"action": "print", "count": 2})
	x.Equal("라벨 만들기: 새 라벨 2장", p.Summary)
	codes := a.applied(p)
	x.Len(codes, 2)
	code := strings.Fields(codes[0])[0]
	p = a.plan("plan_label", map[string]any{"action": "bind", "label": code, "asset": "NB-1"})
	x.Equal("라벨 연결: "+code+" → NB-1 개발용 노트북", p.Summary)
	a.apply(p)
	p = a.plan("plan_label", map[string]any{"action": "unbind", "label": code, "void": true})
	x.Equal("라벨 해제: "+code+" (폐기, 다시 쓰지 않음)", p.Summary)
	a.apply(p)

	// Priced as planned: in the workspace's currency when it was planned,
	// though the workspace prices in another by the time it is applied.
	p = a.plan("plan_purchase", map[string]any{"action": "add", "name": "케이블",
		"lines": []map[string]any{{"desc": "HDMI 케이블", "quantity": 2, "unit_cost": 12000}}})
	x.Equal("구매 등록: 케이블: HDMI 케이블 ×2, 합계 24,000 KRW", p.Summary)
	run(t, cli.NewCmdApi(), "tenant", "configure", "@acme", `{"currency": "USD"}`)
	a.apply(p)
	var priced struct {
		Items []struct {
			Name     string `json:"name"`
			Total    int64  `json:"total"`
			Currency string `json:"currency"`
		} `json:"items"`
	}
	a.call("list_purchases", map[string]any{"status": "ordered"}, &priced)
	x.Len(priced.Items, 1)
	x.Equal("KRW", priced.Items[0].Currency, "priced in what the workspace said after it was planned")
	x.EqualValues(24000, priced.Items[0].Total)
}

// TestAnAssistantReadsTheReports (design 9.14): each report, as the person
// signed in reads it -- who has what by name, and nobody's address -- and
// the period of one given in the workspace's days. A member is refused them,
// as the screen's calls are.
func TestAnAssistantReadsTheReports(t *testing.T) {
	s := serve(t)
	x := s.x
	s.signIn("owner", "owner1234")
	room := made(t, run(t, cli.NewCmdApi(), "asset", "add", "-o", "json", `{"name": "3층 창고", "kind": "space", "tag": "R-3"}`))
	laptop := made(t, run(t, cli.NewCmdApi(), "asset", "add", "-o", "json", `{"name": "개발용 노트북", "kind": "item", "tag": "NB-1", "to": {"id": "`+room+`"}}`))
	made(t, run(t, cli.NewCmdApi(), "asset", "add", "-o", "json", `{"name": "회의실 의자", "kind": "item", "tag": "CH-1", "to": {"id": "`+room+`"}}`))
	kim := made(t, run(t, cli.NewCmdApi(), "party", "add", "-o", "json", `{"name": "김민수", "kind": "person", "email": "kim@acme.example"}`))
	due := time.Now().Add(72 * time.Hour).UTC().Format(time.RFC3339)
	run(t, cli.NewCmdApi(), "custody", "add", "-o", "json", `{"party": {"id": "`+kim+`"}, "dueAt": "`+due+`", "lines": [{"asset": {"id": "`+laptop+`"}}]}`)
	model := made(t, run(t, cli.NewCmdApi(), "item-model", "add", "-o", "json", `{"name": "건전지"}`))
	run(t, cli.NewCmdApi(), "stock", "add", "-o", "json", `{"model": {"id": "`+model+`"}, "space": {"id": "`+room+`"}, "quantity": 12}`)

	a := s.assistant()
	var v struct {
		Assets *struct {
			Rows []struct {
				Name  string `json:"name"`
				Total int    `json:"total"`
			} `json:"rows"`
			Total struct{ Total int } `json:"total"`
		} `json:"assets"`
		Loans *struct {
			Custodies int `json:"custodies"`
			Rows      []struct {
				Who  ref    `json:"who"`
				What ref    `json:"what"`
				Kind string `json:"kind"`
			} `json:"rows"`
		} `json:"loans"`
		Stock []struct {
			Stock  ref   `json:"stock"`
			CameIn int64 `json:"came_in"`
		} `json:"stock"`
		Bookings []any  `json:"bookings"`
		Counts   []any  `json:"counts"`
		From     string `json:"from"`
		To       string `json:"to"`
	}
	a.call("report", map[string]any{"report": "assets", "by": "location"}, &v)
	x.Len(v.Assets.Rows, 1)
	x.Equal("3층 창고", v.Assets.Rows[0].Name)
	x.Equal(2, v.Assets.Rows[0].Total)
	x.Equal(2, v.Assets.Total.Total)

	res, err := a.session.CallTool(context.Background(), &mcp.CallToolParams{Name: "report", Arguments: map[string]any{"report": "loans"}})
	x.NoError(err)
	x.False(res.IsError, said(res))
	b, err := json.Marshal(res.StructuredContent)
	x.NoError(err)
	x.NotContains(string(b), "kim@acme.example", "nobody's address")
	x.NoError(json.Unmarshal(b, &v))
	x.Equal(1, v.Loans.Custodies)
	x.Len(v.Loans.Rows, 1)
	x.Equal("김민수", v.Loans.Rows[0].Who.Name)
	x.Equal("NB-1 개발용 노트북", v.Loans.Rows[0].What.Name)
	x.Equal("loan", v.Loans.Rows[0].Kind)

	// A period of the workspace's days: Seoul's, whatever this machine's is.
	today := time.Now().In(time.FixedZone("KST", 9*3600)).Format(time.DateOnly)
	a.call("report", map[string]any{"report": "stock", "from": today, "to": today}, &v)
	x.Len(v.Stock, 1)
	x.Equal("건전지", v.Stock[0].Stock.Name)
	x.EqualValues(12, v.Stock[0].CameIn)
	from, err := time.Parse(time.RFC3339, v.From)
	x.NoError(err)
	to, err := time.Parse(time.RFC3339, v.To)
	x.NoError(err)
	x.Equal(24*time.Hour, to.Sub(from), "the one day, which the period includes")
	x.Equal("00:00", from.In(time.FixedZone("KST", 9*3600)).Format("15:04"), "as Seoul's day begins")

	a.call("report", map[string]any{"report": "bookings"}, &v)
	a.call("report", map[string]any{"report": "counts"}, &v)
	x.Contains(a.refused("report", map[string]any{"report": "sales"}), "sales")

	s.signIn("kim", "kim-password")
	x.Contains(a.refused("report", map[string]any{"report": "loans"}), "PermissionDenied")
}
