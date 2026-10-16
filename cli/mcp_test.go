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

	// A terminal narrowed to reading is told so before anything is put to
	// the person.
	s.signIn("owner", "owner1234", "read")
	x.Contains(a.refused("plan_asset", map[string]any{"action": "move", "asset": "NB-1", "to": "R-4"}), "may not call /rove.AssetService/Move")
}
