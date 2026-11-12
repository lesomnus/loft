package cli_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/lesomnus/payday/config"
	"github.com/lesomnus/payday/pdtest"
	"github.com/lesomnus/payday/web"
	"github.com/stretchr/testify/require"

	"github.com/lesomnus/loft/cli"
	"github.com/lesomnus/loft/cmd"
	"github.com/lesomnus/loft/internal/ent/clilogin"
	"github.com/lesomnus/loft/internal/loftest"
)

// served is a deployment a terminal reaches the way a browser does: over the
// HTTP listener, the transcoder in front of the same gRPC server.
type served struct {
	t   *testing.T
	x   *require.Assertions
	c   cmd.Config
	s   *cmd.Server
	url string
}

func serve(t *testing.T) *served { return serveWith(t, nil) }

// serveWith is a deployment with one more say over its configuration.
func serveWith(t *testing.T, with func(*cmd.Config)) *served {
	t.Helper()
	x := require.New(t)
	ctx := context.Background()

	c := cmd.Config{}
	c.Db.Driver, c.Db.Dsn = pdtest.DB(t)
	c.Watch.Broker = "memory"
	c.App.Files = t.TempDir()
	c.Auth.Roster.Db.Driver, c.Auth.Roster.Db.Dsn = pdtest.DB(t)
	if with != nil {
		with(&c)
	}
	run(t, cli.NewCmdInit(&c), "--tenant", "acme", "--login", "owner", "--password", "owner1234")

	s, err := cmd.Build(ctx, c)
	x.NoError(err)
	t.Cleanup(func() { s.Close() })
	loftest.Member(t, s, "acme", "kim", "member", "kim-password")
	g, err := s.Grpc(ctx, c)
	x.NoError(err)
	h, err := web.New(config.HttpConfig{AllowWeb: true}, g)
	x.NoError(err)
	s.Routes(h.ServeMux, c)
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)

	t.Setenv("LOFT_CLIENT_CONFIG", filepath.Join(t.TempDir(), "client.json"))
	t.Setenv("LOFT_WORKSPACE", "")
	return &served{t: t, x: x, c: c, s: s, url: srv.URL}
}

// http is one plain call, as the page makes it.
func (s *served) http(method, path, cookie string, body any) (int, string) {
	s.t.Helper()
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		s.x.NoError(err)
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, s.url+path, rd)
	s.x.NoError(err)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if cookie != "" {
		req.Header.Set("Cookie", cookie)
	}
	res, err := http.DefaultClient.Do(req)
	s.x.NoError(err)
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	return res.StatusCode, string(b)
}

// browser is a browser signed in as somebody: the cookie it holds.
func (s *served) browser(login, password string) string {
	s.t.Helper()
	req, err := http.NewRequest(http.MethodPost, s.url+"/session", strings.NewReader(`{"login":"`+login+`","password":"`+password+`"}`))
	s.x.NoError(err)
	req.Header.Set("Content-Type", "application/json")
	res, err := http.DefaultClient.Do(req)
	s.x.NoError(err)
	defer res.Body.Close()
	s.x.Equal(http.StatusNoContent, res.StatusCode)
	cs := res.Cookies()
	s.x.NotEmpty(cs)
	return cs[0].Name + "=" + cs[0].Value
}

// waiting is the code of the one terminal waiting to be approved.
func (s *served) waiting() string {
	s.t.Helper()
	var code string
	s.x.Eventually(func() bool {
		v, err := s.s.Ent.CliLogin.Query().Where(clilogin.State("pending")).First(context.Background())
		if err != nil {
			return false
		}
		code = v.Code
		return true
	}, 10*time.Second, 20*time.Millisecond)
	return code
}

// login runs `loft auth login` and answers it in a browser as somebody: what
// `answer` does with the code it was shown.
func (s *served) login(answer func(code string), args ...string) error {
	s.t.Helper()
	k := cli.NewCmdAuth()
	k.Writer = io.Discard
	done := make(chan error, 1)
	go func() {
		done <- k.Run(context.Background(), append([]string{"login", "--url", s.url, "--no-browser"}, args...))
	}()
	answer(s.waiting())
	select {
	case err := <-done:
		return err
	case <-time.After(20 * time.Second):
		s.t.Fatal("the terminal never heard")
		return nil
	}
}

// signIn is a terminal signed in as somebody, approved in their browser --
// narrowed to what `allow` names, for `loft auth login --allow`.
func (s *served) signIn(login, password string, allow ...string) {
	s.t.Helper()
	cookie := s.browser(login, password)
	args := []string{}
	for _, v := range allow {
		args = append(args, "--allow", v)
	}
	s.x.NoError(s.login(func(code string) {
		status, body := s.http(http.MethodGet, "/cli/logins?code="+code, cookie, nil)
		s.x.Equal(http.StatusOK, status, body)
		s.x.Contains(body, "loft CLI on", "what the page shows of the terminal asking")
		status, body = s.http(http.MethodPost, "/cli/logins/approve", cookie, map[string]string{"code": code})
		s.x.Equal(http.StatusNoContent, status, body)
	}, args...))
}

// terminal is the cookie the connection in use holds, as the terminal's
// commands send it.
func (s *served) terminal() string {
	s.t.Helper()
	b, err := os.ReadFile(os.Getenv("LOFT_CLIENT_CONFIG"))
	s.x.NoError(err)
	var v struct {
		Current     string
		Connections map[string]struct{ Cookie, Key string }
	}
	s.x.NoError(json.Unmarshal(b, &v))
	c := v.Connections[v.Current]
	return c.Cookie + "=" + c.Key
}

// TestATerminalSignsInByBeingApproved.
//
// `loft auth login` shows a code; a person signed in at the app approves it; the
// terminal is then that person, through every operation the API has -- the
// entities' verbs, the domain's operations, any method by name -- and signing
// out ends it where it lives.
func TestATerminalSignsInByBeingApproved(t *testing.T) {
	s := serve(t)
	x := s.x
	s.signIn("owner", "owner1234")

	out := run(t, cli.NewCmdAuth(), "whoami")
	x.Contains(out, "owner")
	x.Contains(out, "@acme\towner")

	out = run(t, root(), "asset", "add", "-o", "json", `{"name": "노트북", "tag": "NB-1"}`)
	x.Contains(out, "노트북")
	out = run(t, root(), "asset", "search", "-o", "json", `{"q": "노트"}`)
	x.Contains(out, "NB-1", "a domain operation, beside the verbs")
	out = run(t, root(), "call", "loft.AssetService.Search", `{"q": "NB"}`)
	x.Contains(out, "노트북", "any method by name")
	out = run(t, root(), "--workspace", "acme", "party", "me", "-o", "json")
	x.Contains(out, "owner", "naming the workspace")

	out = run(t, root(), "tenant", "create", `{"name": "연구실"}`)
	x.Contains(out, "연구실", "what an account has outside any one workspace")
	out = run(t, root(), "workspace", "mine")
	x.Contains(out, "연구실", "a tenant is what a person calls a workspace")
	x.Error(cli.NewCmdUse().Run(context.Background(), []string{"nowhere"}), "a workspace it is not in")
	run(t, cli.NewCmdUse(), "acme")
	out = run(t, cli.NewCmdAuth(), "whoami")
	x.Contains(out, "* @acme")

	run(t, cli.NewCmdAuth(), "logout")
	err := cli.NewCmdAuth().Run(context.Background(), []string{"whoami"})
	x.ErrorContains(err, "no connection", "signed out, and forgotten")
}

// TestATerminalNamesAWorkspaceItsAccountOwns (D58): what a workspace is called,
// said from the terminal as its settings say it (`tenant configure`, its
// owner's one door) -- and none of its own, and it is called after its owner
// (D57). Its owner's alone.
func TestATerminalNamesAWorkspaceItsAccountOwns(t *testing.T) {
	s := serve(t)
	x := s.x
	ctx := context.Background()
	s.signIn("owner", "owner1234")
	called := func() string {
		var mine struct {
			Workspaces []struct{ Alias, Name string }
		}
		out := run(t, root(), "tenant", "mine", "-o", "json")
		x.NoError(json.Unmarshal([]byte(out), &mine), out)
		x.Len(mine.Workspaces, 1)
		x.Equal("acme", mine.Workspaces[0].Alias)
		return mine.Workspaces[0].Name
	}

	x.Contains(run(t, root(), "tenant", "configure", "@acme", `{"name": "연구실 장비"}`), "연구실 장비")
	x.Equal("연구실 장비", called())
	run(t, root(), "ws", "conf", "@acme", `{"name": ""}`)
	x.Equal("관리자의 워크스페이스", called(), "called after its owner, whom `loft init` calls so")

	s.signIn("kim", "kim-password")
	x.ErrorContains(root().Run(ctx, []string{"tenant", "configure", "@acme", `{"name": "김의 장비"}`}), "PermissionDenied")
}

// TestATerminalIsHeldToWhatItsAccountMay: the policy the page is held to,
// and nothing wider.
func TestATerminalIsHeldToWhatItsAccountMay(t *testing.T) {
	s := serve(t)
	x := s.x
	s.signIn("kim", "kim-password")

	out := run(t, root(), "asset", "ls", "-o", "json")
	x.NotContains(out, "error")
	err := root().Run(context.Background(), []string{"asset", "add", `{"name": "x"}`})
	x.ErrorContains(err, "PermissionDenied")
}

// TestATerminalMayBeNarrowed to some methods (D33): what it asked for is on
// the page that approves it, and on every call after it the server holds it
// to them -- whatever its account may do, and whoever holds its key. Beside
// the RPCs it reads what its account is, and does nothing an account does:
// it approves no terminal, takes no data out, makes no workspace.
func TestATerminalMayBeNarrowed(t *testing.T) {
	s := serve(t)
	x := s.x
	browser := s.browser("owner", "owner1234")
	x.NoError(s.login(func(code string) {
		status, body := s.http(http.MethodGet, "/cli/logins?code="+code, browser, nil)
		x.Equal(http.StatusOK, status, body)
		x.Contains(body, `"allow":["/loft.*/Get","/loft.*/List"`, "the page shows what it asks for: %s", body)
		status, body = s.http(http.MethodPost, "/cli/logins/approve", browser, map[string]string{"code": code})
		x.Equal(http.StatusNoContent, status, body)
	}, "--allow", "read"))

	out := run(t, cli.NewCmdAuth(), "whoami")
	x.Contains(out, "allowed only: /loft.*/Get, /loft.*/List")
	out = run(t, root(), "asset", "ls", "-o", "json")
	x.NotContains(out, "error", "a read")
	err := root().Run(context.Background(), []string{"asset", "add", `{"name": "x"}`})
	x.ErrorContains(err, "PermissionDenied")
	x.ErrorContains(err, "this credential is not for that", "though the owner may")

	narrowed := s.terminal()
	status, body := s.http(http.MethodGet, "/account", narrowed, nil)
	x.Equal(http.StatusOK, status, body)
	x.Contains(body, `"allow":[`)
	out = run(t, root(), "tenant", "mine")
	x.Contains(out, "acme", "what it is in, which it reads")
	err = root().Run(context.Background(), []string{"--reason", "몰래", "asset", "export"})
	x.ErrorContains(err, "this credential is not for that", "no data out")
	err = root().Run(context.Background(), []string{"tenant", "create", `{"name": "몰래"}`})
	x.ErrorContains(err, "this credential is not for that", "no workspace made")

	// Nor another terminal approved, which would be one that may do all of it.
	other := s.login(func(code string) {
		status, _ := s.http(http.MethodPost, "/cli/logins/approve", narrowed, map[string]string{"code": code})
		x.Equal(http.StatusForbidden, status)
		status, _ = s.http(http.MethodPost, "/cli/logins/deny", browser, map[string]string{"code": code})
		x.Equal(http.StatusNoContent, status)
	})
	x.ErrorContains(other, "turned away")

	// A pattern of one service is that service.
	s.signIn("owner", "owner1234", "/loft.AssetService/*")
	run(t, root(), "asset", "add", "-o", "json", `{"name": "노트북", "tag": "NB-1"}`)
	err = root().Run(context.Background(), []string{"party", "add", `{"name": "김민수"}`})
	x.ErrorContains(err, "this credential is not for that")

	status, body = s.http(http.MethodPost, "/cli/logins", "", map[string]any{"secret": strings.Repeat("D", 43), "allow": []string{"loft.AssetService"}})
	x.Equal(http.StatusBadRequest, status, "a pattern that is not one: %s", body)
}

// TestAWatchComesThroughTheBrowsersAddress: a server stream, which is what
// `watch` holds open, over the same listener as everything else.
func TestAWatchComesThroughTheBrowsersAddress(t *testing.T) {
	s := serve(t)
	x := s.x
	s.signIn("owner", "owner1234")

	id := func(out string) string {
		var v struct{ Id string }
		x.NoError(json.Unmarshal([]byte(out), &v), out)
		return v.Id
	}
	room := id(run(t, root(), "asset", "add", "-o", "json", `{"name": "회의실", "kind": "space"}`))
	printer := id(run(t, root(), "asset", "add", "-o", "json", `{"name": "프린터"}`))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	buf := &syncBuffer{}
	k := root()
	k.Writer = buf
	done := make(chan error, 1)
	go func() { done <- k.Run(ctx, []string{"asset", "watch", "-o", "json", printer}) }()

	// Moved until the stream says so: nothing says when it has subscribed.
	deadline := time.After(15 * time.Second)
	for to := room; !strings.Contains(buf.String(), "프린터"); {
		select {
		case err := <-done:
			t.Fatalf("the stream ended: %v", err)
		case <-deadline:
			t.Fatalf("nothing came down the stream: %q", buf.String())
		case <-time.After(300 * time.Millisecond):
		}
		run(t, root(), "asset", "move", printer, `{"to": {"id": "`+to+`"}}`)
	}
	cancel()
	<-done
}

// TestASignInNobodyApprovedIsNotOne.
func TestASignInNobodyApprovedIsNotOne(t *testing.T) {
	s := serve(t)
	x := s.x
	cookie := s.browser("owner", "owner1234")

	t.Run("turned away", func(t *testing.T) {
		err := s.login(func(code string) {
			status, _ := s.http(http.MethodPost, "/cli/logins/deny", cookie, map[string]string{"code": code})
			x.Equal(http.StatusNoContent, status)
		})
		x.ErrorContains(err, "turned away")
		x.ErrorContains(cli.NewCmdAuth().Run(context.Background(), []string{"whoami"}), "no connection")
	})

	t.Run("approved by nobody signed in", func(t *testing.T) {
		err := s.login(func(code string) {
			status, _ := s.http(http.MethodPost, "/cli/logins/approve", "", map[string]string{"code": code})
			x.Equal(http.StatusUnauthorized, status)
			status, _ = s.http(http.MethodGet, "/cli/logins?code="+code, "", nil)
			x.Equal(http.StatusUnauthorized, status, "what the page shows is for somebody signed in")
			status, _ = s.http(http.MethodPost, "/cli/logins/deny", cookie, map[string]string{"code": code})
			x.Equal(http.StatusNoContent, status)
		})
		x.Error(err)
	})

	t.Run("a form on another site", func(t *testing.T) {
		req, err := http.NewRequest(http.MethodPost, s.url+"/cli/logins/approve", strings.NewReader("code=ABCD-EFGH"))
		x.NoError(err)
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.Header.Set("Cookie", cookie)
		res, err := http.DefaultClient.Do(req)
		x.NoError(err)
		res.Body.Close()
		x.Equal(http.StatusUnsupportedMediaType, res.StatusCode)
	})

	t.Run("too late", func(t *testing.T) {
		err := s.login(func(code string) {
			// Asked the length of its wait ago.
			x.NoError(s.s.Ent.CliLogin.Update().Where(clilogin.Code(code)).
				SetDateExpires(time.Now().Add(-time.Second)).Exec(context.Background()))
			status, _ := s.http(http.MethodPost, "/cli/logins/approve", cookie, map[string]string{"code": code})
			x.Equal(http.StatusNotFound, status, "an approval after it ran out")
		})
		x.ErrorContains(err, "nobody approved it in time")
	})

	t.Run("a secret made up twice", func(t *testing.T) {
		secret := strings.Repeat("A", 43)
		status, _ := s.http(http.MethodPost, "/cli/logins", "", map[string]string{"secret": secret})
		x.Equal(http.StatusCreated, status)
		status, _ = s.http(http.MethodPost, "/cli/logins", "", map[string]string{"secret": secret})
		x.Equal(http.StatusConflict, status)
	})

	t.Run("asking about somebody else's", func(t *testing.T) {
		status, body := s.http(http.MethodPost, "/cli/logins", "", map[string]string{"secret": strings.Repeat("B", 43)})
		x.Equal(http.StatusCreated, status)
		var v struct{ Id string }
		x.NoError(json.Unmarshal([]byte(body), &v))
		status, _ = s.http(http.MethodPost, "/cli/logins/"+v.Id+"/poll", "", map[string]string{"secret": strings.Repeat("C", 43)})
		x.Equal(http.StatusNotFound, status)
	})

	t.Run("too many from one place", func(t *testing.T) {
		seen := 0
		for i := range 30 {
			secret := strings.Repeat(string(rune('a'+i%26)), 42) + string(rune('A'+i/26))
			status, _ := s.http(http.MethodPost, "/cli/logins", "", map[string]string{"secret": secret})
			if status == http.StatusTooManyRequests {
				seen++
			}
		}
		x.Positive(seen)
	})
}

// syncBuffer is a buffer a command writes to while a test reads it.
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}
