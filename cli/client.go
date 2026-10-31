package cli

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/lesomnus/xli"
	"github.com/lesomnus/xli/arg"
	"github.com/lesomnus/xli/flg"
	"google.golang.org/grpc/metadata"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"
	"google.golang.org/protobuf/types/dynamicpb"

	"github.com/lesomnus/payday/access"
	"github.com/lesomnus/payday/pdcmd"
	"github.com/lesomnus/z"

	app "github.com/lesomnus/loft"
	"github.com/lesomnus/loft/cmd"
	"github.com/lesomnus/loft/server/clilogin"
)

// The terminal's half of this app: everything it can do, asked of a running
// deployment over the address a browser reaches it at, as an account signed in
// by approving the terminal there (`loft login`, `server/clilogin`).
//
// It is not the operator's half. `loft init`, `loft tenant purge`, `loft
// identity ...` run on the box with the database; these run anywhere, as a
// person, through the same policy and the same wall as the page.

// clientConfig is what a terminal keeps of its sign-in: where the deployment
// is, the key of the session it signs in with, the cookie that carries it, and
// the workspace it speaks to unless told otherwise.
type clientConfig struct {
	Url       string `json:"url"`
	Cookie    string `json:"cookie"`
	Key       string `json:"key"`
	Workspace string `json:"workspace,omitempty"`
}

// clientConfigPath is where it is kept: `LOFT_CLIENT_CONFIG`, or the user's
// configuration directory.
func clientConfigPath() (string, error) {
	if v := os.Getenv("LOFT_CLIENT_CONFIG"); v != "" {
		return v, nil
	}
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "loft", "client.json"), nil
}

func loadClient() (clientConfig, error) {
	p, err := clientConfigPath()
	if err != nil {
		return clientConfig{}, err
	}
	b, err := os.ReadFile(p)
	if errors.Is(err, os.ErrNotExist) {
		return clientConfig{}, nil
	}
	if err != nil {
		return clientConfig{}, err
	}
	v := clientConfig{}
	if err := json.Unmarshal(b, &v); err != nil {
		return clientConfig{}, fmt.Errorf("%s: %w", p, err)
	}
	return v, nil
}

// saveClient writes it readable by its owner alone: it holds a session key.
func saveClient(v clientConfig) error {
	p, err := clientConfigPath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(p, append(b, '\n'), 0o600)
}

func (c clientConfig) signedIn() bool { return c.Url != "" && c.Key != "" }

// conn is the connection a command uses, to workspace `ws` (empty is none
// named, which an account in one workspace needs no more than).
func (c clientConfig) conn(ws string) *connectConn {
	h := http.Header{}
	h.Set("Cookie", c.Cookie+"="+c.Key)
	if ws = strings.TrimPrefix(strings.TrimSpace(ws), "@"); ws != "" {
		h.Set(cmd.WorkspaceHeader, ws)
	}
	h.Set("User-Agent", "loft-cli")
	return &connectConn{base: strings.TrimSuffix(c.Url, "/"), client: &http.Client{Timeout: 0}, header: h}
}

// do is one plain HTTP call beside the RPCs: `/account`, `/cli/...`.
func (c clientConfig) do(ctx context.Context, method, path string, body any, out any) (int, error) {
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return 0, err
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, strings.TrimSuffix(c.Url, "/")+path, rd)
	if err != nil {
		return 0, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.Key != "" {
		req.Header.Set("Cookie", c.Cookie+"="+c.Key)
	}
	req.Header.Set("User-Agent", "loft-cli")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return 0, err
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(res.Body, 4<<20))
	if res.StatusCode >= 300 {
		return res.StatusCode, fmt.Errorf("%s %s: %s", method, path, strings.TrimSpace(string(b)))
	}
	if out != nil && len(b) > 0 {
		if err := json.Unmarshal(b, out); err != nil {
			return res.StatusCode, err
		}
	}
	return res.StatusCode, nil
}

// fetch is one plain download beside the RPCs, written to `w` as it comes.
func (c clientConfig) fetch(ctx context.Context, path string, w io.Writer) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimSuffix(c.Url, "/")+path, nil)
	if err != nil {
		return err
	}
	if c.Key != "" {
		req.Header.Set("Cookie", c.Cookie+"="+c.Key)
	}
	req.Header.Set("User-Agent", "loft-cli")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode >= 300 {
		b, _ := io.ReadAll(io.LimitReader(res.Body, 4<<10))
		return fmt.Errorf("GET %s: %s", path, strings.TrimSpace(string(b)))
	}
	_, err = io.Copy(w, res.Body)
	return err
}

// NewCmdLogin is `loft login`: this terminal signed in by approving it in a
// browser where the person is signed in already.
func NewCmdLogin() *xli.Command {
	return &xli.Command{
		Name:  "login",
		Brief: "sign this terminal in, by approving it in a browser",
		Flags: flg.Flags{
			&flg.String{Name: "url", Brief: "the app's address, e.g. https://loft.example; the last one used when left out"},
			&flg.Switch{Name: "no-browser", Brief: "print the address to open rather than opening it"},
			&flg.Strings{Name: "allow", Brief: "only this method or pattern (/loft.ReservationService/*, /loft.*/Get), again for more; `read` is every method that only reads"},
		},
		Handler: xli.OnRun(func(ctx context.Context, self *xli.Command, next xli.Next) error {
			was, err := loadClient()
			if err != nil {
				return err
			}
			base := strings.TrimSpace(flag(self, "url"))
			if base == "" {
				base = was.Url
			}
			if base == "" {
				return errors.New("which deployment: --url https://loft.example")
			}
			if _, err := url.Parse(base); err != nil {
				return fmt.Errorf("--url: %w", err)
			}
			quiet, _ := flg.Find[bool](self, "no-browser")
			allow, _ := flg.Find[[]string](self, "allow")
			v, err := login(ctx, self, strings.TrimSuffix(base, "/"), !quiet, expand(allow))
			if err != nil {
				return err
			}
			v.Workspace = was.Workspace
			if was.Url != v.Url {
				v.Workspace = ""
			}
			if err := saveClient(v); err != nil {
				return err
			}
			var me struct {
				Login      string `json:"login"`
				Workspaces []struct {
					Alias string `json:"alias"`
				} `json:"workspaces"`
			}
			if _, err := v.do(ctx, http.MethodGet, "/account", nil, &me); err == nil {
				self.Printf("signed in as %s, in %d workspace(s)\n", me.Login, len(me.Workspaces))
			} else {
				self.Printf("signed in\n")
			}
			return next(ctx)
		}),
	}
}

// reads is what `loft login --allow read` asks for: the methods that only
// read -- every entity's Get, List and Watch, and the searches, histories and
// answers beside them -- for a terminal handed to an assistant that may look
// and not touch. Named by pattern, so that a read added later is one too, and
// never a method that writes: SetPassword and MarkRead are not here.
var reads = []string{
	"/loft.*/Get", "/loft.*/List", "/loft.*/Watch", "/loft.*/Search", "/loft.*/Recent",
	"/loft.AssetService/Timeline", "/loft.AssetService/QueryAt", "/loft.AssetService/Diff", "/loft.AssetService/Report",
	"/loft.ReservationService/Calendar", "/loft.ReservationService/Timeline", "/loft.BookableService/Availability",
	"/loft.PartyService/Me", "/loft.LabelService/Resolve", "/loft.TenantDomainService/Status",
	"/loft.AttachmentService/Url", "/loft.NotificationService/Inbox",
}

// expand is what `--allow` named, with `read` as [reads].
func expand(vs []string) []string {
	out := []string{}
	for _, v := range vs {
		for _, w := range strings.Split(v, ",") {
			switch w = strings.TrimSpace(w); w {
			case "":
			case "read":
				out = append(out, reads...)
			default:
				out = append(out, w)
			}
		}
	}
	return out
}

func login(ctx context.Context, self *xli.Command, base string, browser bool, allow []string) (clientConfig, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return clientConfig{}, err
	}
	key := base64.RawURLEncoding.EncodeToString(raw)
	sum := sha256.Sum256([]byte(key))
	secret := base64.RawURLEncoding.EncodeToString(sum[:])

	host, _ := os.Hostname()
	c := clientConfig{Url: base}
	var started clilogin.Started
	if _, err := c.do(ctx, http.MethodPost, "/cli/logins", map[string]any{
		"secret": secret,
		"client": fmt.Sprintf("loft CLI on %s (%s/%s)", host, runtime.GOOS, runtime.GOARCH),
		"allow":  allow,
	}, &started); err != nil {
		return clientConfig{}, err
	}
	if len(allow) > 0 {
		self.Printf("Asking to be allowed only:\n  %s\n\n", strings.Join(allow, "\n  "))
	}
	self.Printf("Open this address signed in, and approve it if it shows %s:\n\n  %s\n\n", started.Code, started.Approve)
	if browser {
		openBrowser(started.Approve)
	}

	every := time.Duration(max(started.Interval, 1)) * time.Second
	for {
		select {
		case <-ctx.Done():
			return clientConfig{}, ctx.Err()
		case <-time.After(every):
		}
		var got struct {
			State string `json:"state"`
		}
		if _, err := c.do(ctx, http.MethodPost, "/cli/logins/"+started.Id+"/poll", map[string]string{"secret": secret}, &got); err != nil {
			return clientConfig{}, err
		}
		switch got.State {
		case "pending":
			continue
		case "approved":
			return clientConfig{Url: base, Cookie: started.Cookie, Key: key}, nil
		case "denied":
			return clientConfig{}, errors.New("turned away in the browser")
		default:
			return clientConfig{}, errors.New("nobody approved it in time; run `loft login` again")
		}
	}
}

// openBrowser opens an address, if this machine has a way to. Not opening is
// not an error: the address was printed.
func openBrowser(addr string) {
	var c *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		c = exec.Command("open", addr)
	case "windows":
		c = exec.Command("rundll32", "url.dll,FileProtocolHandler", addr)
	default:
		c = exec.Command("xdg-open", addr)
	}
	_ = c.Start()
}

// NewCmdLogout is `loft logout`: the session ended where it lives, and
// forgotten here.
func NewCmdLogout() *xli.Command {
	return &xli.Command{
		Name:  "logout",
		Brief: "sign this terminal out",
		Handler: xli.OnRun(func(ctx context.Context, self *xli.Command, next xli.Next) error {
			c, err := loadClient()
			if err != nil {
				return err
			}
			if c.signedIn() {
				if _, err := c.do(ctx, http.MethodDelete, "/session", nil, nil); err != nil {
					self.Printf("the deployment did not answer (%v); forgotten here anyway\n", err)
				}
			}
			c.Key, c.Cookie = "", ""
			if err := saveClient(c); err != nil {
				return err
			}
			self.Printf("signed out\n")
			return next(ctx)
		}),
	}
}

// NewCmdWhoami is `loft whoami`: the account, and the workspaces it is in.
func NewCmdWhoami() *xli.Command {
	return &xli.Command{
		Name:  "whoami",
		Brief: "the account this terminal is signed in as, and its workspaces",
		Handler: xli.OnRun(func(ctx context.Context, self *xli.Command, next xli.Next) error {
			c, err := loadClient()
			if err != nil {
				return err
			}
			if !c.signedIn() {
				return errNoLogin
			}
			var me struct {
				Id         string `json:"id"`
				Login      string `json:"login"`
				Name       string `json:"name"`
				Workspaces []struct {
					Alias string `json:"alias"`
					Name  string `json:"name"`
					Role  string `json:"role"`
				} `json:"workspaces"`
				Allow []string `json:"allow"`
			}
			if _, err := c.do(ctx, http.MethodGet, "/account", nil, &me); err != nil {
				return err
			}
			self.Printf("%s (%s) at %s\n", me.Login, me.Name, c.Url)
			if len(me.Allow) > 0 {
				self.Printf("  allowed only: %s\n", strings.Join(me.Allow, ", "))
			}
			for _, w := range me.Workspaces {
				mark := " "
				if w.Alias == c.Workspace {
					mark = "*"
				}
				self.Printf("%s @%s\t%s\t%s\n", mark, w.Alias, w.Role, w.Name)
			}
			return next(ctx)
		}),
	}
}

// NewCmdUse is `loft use`: which workspace the terminal speaks to when a
// command names none.
func NewCmdUse() *xli.Command {
	return &xli.Command{
		Name:  "use",
		Brief: "the workspace commands speak to unless --workspace says otherwise",
		Flags: flg.Flags{&flg.Switch{Name: "none", Brief: "name none: an account in one workspace needs none"}},
		Args:  arg.Args{&arg.String{Name: "WORKSPACE", Brief: "by alias; none prints the one in use", Optional: true}},
		Handler: xli.OnRun(func(ctx context.Context, self *xli.Command, next xli.Next) error {
			c, err := loadClient()
			if err != nil {
				return err
			}
			if !c.signedIn() {
				return errNoLogin
			}
			none, _ := flg.Find[bool](self, "none")
			named, _ := arg.Get[string](self, "WORKSPACE")
			switch {
			case none:
				c.Workspace = ""
			case named == "":
				if c.Workspace == "" {
					self.Printf("none\n")
				} else {
					self.Printf("@%s\n", c.Workspace)
				}
				return next(ctx)
			default:
				ws := strings.TrimPrefix(named, "@")
				var me struct {
					Workspaces []struct {
						Alias string `json:"alias"`
					} `json:"workspaces"`
				}
				if _, err := c.do(ctx, http.MethodGet, "/account", nil, &me); err != nil {
					return err
				}
				found := false
				for _, w := range me.Workspaces {
					found = found || w.Alias == ws
				}
				if !found {
					return fmt.Errorf("@%s: not a workspace this account is in (`loft whoami`)", ws)
				}
				c.Workspace = ws
			}
			if err := saveClient(c); err != nil {
				return err
			}
			if c.Workspace == "" {
				self.Printf("speaking to no workspace in particular\n")
			} else {
				self.Printf("speaking to @%s\n", c.Workspace)
			}
			return next(ctx)
		}),
	}
}

// NewCmdWorkspace is `loft workspace`: what an account has outside any one
// workspace -- the ones it is in, a new one, and everything one it owns has --
// which is HTTP beside the RPCs (`server/account`), asked before there is a
// workspace to be a member of. And what one it owns is called, which is the
// workspace's own RPC (D58), here beside making one.
func NewCmdWorkspace() *xli.Command {
	return &xli.Command{
		Name:  "workspace",
		Brief: "the workspaces this account is in, a new one, what one is called, and one's data",
		Commands: xli.Commands{
			{
				Name:  "ls",
				Brief: "the workspaces this account is in",
				Handler: xli.OnRun(func(ctx context.Context, self *xli.Command, next xli.Next) error {
					return NewCmdWhoami().Handler.Handle(ctx, self, next)
				}),
			},
			{
				Name:  "new",
				Brief: "make a workspace, owned by this account",
				Args:  arg.Args{&arg.String{Name: "NAME", Brief: "what it is called"}},
				Handler: xli.OnRun(func(ctx context.Context, self *xli.Command, next xli.Next) error {
					c, err := loadClient()
					if err != nil {
						return err
					}
					if !c.signedIn() {
						return errNoLogin
					}
					name, _ := arg.Get[string](self, "NAME")
					var made struct {
						Alias string `json:"alias"`
						Name  string `json:"name"`
					}
					if _, err := c.do(ctx, http.MethodPost, "/account/workspaces", map[string]string{"name": name}, &made); err != nil {
						return err
					}
					self.Printf("@%s\t%s\n", made.Alias, made.Name)
					return next(ctx)
				}),
			},
			{
				Name:  "rename",
				Brief: "what a workspace this account owns is called, or none of its own and it is called after its owner",
				Flags: flg.Flags{&flg.Switch{Name: "clear", Brief: "no name of its own: it is called after its owner"}},
				Args: arg.Args{
					&arg.String{Name: "WORKSPACE", Brief: "by alias"},
					&arg.String{Name: "NAME", Brief: "what it is called; its history keeps it as it is, so no person's name", Optional: true},
				},
				Handler: xli.OnRun(func(ctx context.Context, self *xli.Command, next xli.Next) error {
					c, err := loadClient()
					if err != nil {
						return err
					}
					if !c.signedIn() {
						return errNoLogin
					}
					ws, _ := arg.Get[string](self, "WORKSPACE")
					ws = strings.TrimPrefix(strings.TrimSpace(ws), "@")
					name, _ := arg.Get[string](self, "NAME")
					name = strings.TrimSpace(name)
					none, _ := flg.Find[bool](self, "clear")
					switch {
					case ws == "":
						return errors.New("WORKSPACE: which one (`loft whoami` lists them)")
					case none && name != "":
						return errors.New("NAME or --clear, not both")
					case !none && name == "":
						return errors.New("NAME: what it is called; --clear for none of its own")
					}
					// Said as the page says it: its owner's Configure, with its
					// event (D58).
					if _, err := app.NewTenantServiceClient(c.conn(ws)).Configure(ctx, app.TenantConfigureRequest_builder{
						Ref:  app.TenantRef_builder{Alias: z.Ptr(ws)}.Build(),
						Name: z.Ptr(name),
					}.Build()); err != nil {
						return err
					}
					// And what it is called now, as the account is told it:
					// after its owner, for none of its own.
					var me struct {
						Workspaces []struct {
							Alias string `json:"alias"`
							Name  string `json:"name"`
						} `json:"workspaces"`
					}
					if _, err := c.do(ctx, http.MethodGet, "/account", nil, &me); err != nil {
						return err
					}
					for _, w := range me.Workspaces {
						if w.Alias == ws {
							self.Printf("@%s\t%s\n", w.Alias, w.Name)
						}
					}
					return next(ctx)
				}),
			},
			{
				Name:  "export",
				Brief: "everything a workspace this account owns has, as one zip: what it takes out before it leaves",
				Flags: flg.Flags{
					&flg.String{Name: "out", Brief: "where to write it; <workspace>-<date>.zip by default"},
					&flg.String{Name: "reason", Brief: "what it is for: it hands everybody's data out, so it is required and recorded"},
				},
				Args: arg.Args{&arg.String{Name: "WORKSPACE", Brief: "by alias; the one `loft use` names by default", Optional: true}},
				Handler: xli.OnRun(func(ctx context.Context, self *xli.Command, next xli.Next) error {
					c, err := loadClient()
					if err != nil {
						return err
					}
					if !c.signedIn() {
						return errNoLogin
					}
					ws, _ := arg.Get[string](self, "WORKSPACE")
					if ws = strings.TrimPrefix(strings.TrimSpace(ws), "@"); ws == "" {
						ws = c.Workspace
					}
					if ws == "" {
						return errors.New("WORKSPACE: which one (`loft whoami` lists them)")
					}
					why := strings.TrimSpace(flag(self, "reason"))
					if why == "" {
						return errors.New("--reason: what the export is for; it hands everybody's data out, and is recorded with why")
					}
					out := flag(self, "out")
					if out == "" {
						out = fmt.Sprintf("%s-%s.zip", ws, time.Now().Format("20060102"))
					}
					// Written beside where it goes and moved there once whole,
					// so that a download cut off is no file at all.
					tmp, err := os.CreateTemp(filepath.Dir(out), ".loft-export-*")
					if err != nil {
						return err
					}
					defer os.Remove(tmp.Name())
					if err := c.fetch(ctx, "/account/workspaces/"+url.PathEscape(ws)+"/export?reason="+url.QueryEscape(why), tmp); err != nil {
						tmp.Close()
						return err
					}
					if err := tmp.Close(); err != nil {
						return err
					}
					if err := os.Rename(tmp.Name(), out); err != nil {
						return err
					}
					self.Printf("%s\n", out)
					return next(ctx)
				}),
			},
		},
		Handler: xli.RequireSubcommand(),
	}
}

type workspaceKey struct{}

// remote is the connector of the API's commands: this terminal's sign-in, to
// the workspace the command line, `LOFT_WORKSPACE` or `loft use` names.
type remote struct{}

func (remote) Connect(ctx context.Context) (pdcmd.Conn, func(), error) {
	c, err := loadClient()
	if err != nil {
		return nil, nil, err
	}
	if !c.signedIn() {
		return nil, nil, errNoLogin
	}
	ws, _ := ctx.Value(workspaceKey{}).(string)
	if ws == "" {
		ws = os.Getenv("LOFT_WORKSPACE")
	}
	if ws == "" {
		ws = c.Workspace
	}
	return c.conn(ws), nil, nil
}

// NewCmdApi is `loft api`: every RPC of this app, as a command.
//
// The entities' own verbs -- `get`, `ls`, `watch`, `add`, `patch`, `erase` --
// are payday's tree, built from the descriptors; every other method of an
// entity's service is one more command beside them (`loft api asset move`),
// and `loft api call` reaches any method by name. Whether a call is allowed is
// the server's to say, through the same policy the page is held to.
func NewCmdApi() *xli.Command {
	t := pdcmd.NewIn(remote{}, "loft")
	for _, e := range pdcmd.Entities("loft") {
		ms := e.Service.Methods()
		for i := range ms.Len() {
			md := ms.Get(i)
			switch md.Name() {
			case "Get", "List", "Watch", "Add", "Patch", "Erase":
				continue
			}
			if md.IsStreamingClient() || md.IsStreamingServer() {
				continue
			}
			c, err := t.Unary(string(md.FullName()))
			if err != nil {
				panic(err)
			}
			path := e.Name + "/" + kebab(string(md.Name()))
			if t.Command(e.Name) == nil {
				// An entity whose service has no verb of its own, only
				// methods: a group for them.
				if err := t.Add(e.Name, &xli.Command{Brief: fmt.Sprintf("%s's operations", e.Message.Name()), Handler: xli.RequireSubcommand()}); err != nil {
					panic(err)
				}
			}
			if err := t.Add(path, c); err != nil {
				panic(err)
			}
		}
	}

	// What the server keeps for itself and serves to nobody: a person's
	// session, a terminal waiting, the tree's lock, an address held.
	for _, internal := range []string{"session", "cli-login", "tree-lock", "alias-hold"} {
		if t.Command(internal) != nil {
			if err := t.Drop(internal); err != nil {
				panic(err)
			}
		}
	}

	return &xli.Command{
		Name:  "api",
		Brief: "every operation of a running deployment, as this terminal's account (`loft login` first)",
		Flags: flg.Flags{
			&flg.String{Name: "workspace", Brief: "the workspace to speak to, by alias; `loft use` sets the default"},
			&flg.String{Name: "reason", Brief: "what a download is for -- `asset export` hands people's data out and is refused without one; it is recorded"},
		},
		Commands: append(t.Commands(), newCmdCall()),
		Handler: xli.Chain(
			xli.OnRunPass(func(ctx context.Context, self *xli.Command, next xli.Next) error {
				if ws := flag(self, "workspace"); ws != "" {
					ctx = context.WithValue(ctx, workspaceKey{}, ws)
				}
				// Said to every call, and read by the downloads: what the
				// access record keeps beside who took what (loft#136).
				if why := strings.TrimSpace(flag(self, "reason")); why != "" {
					ctx = metadata.AppendToOutgoingContext(ctx, access.ReasonKey, why)
				}
				return next(ctx)
			}),
			xli.RequireSubcommand(),
		),
	}
}

// newCmdCall is `loft api call METHOD [JSON]`: any method by its full name,
// with the request as protojson, answered as protojson.
func newCmdCall() *xli.Command {
	return &xli.Command{
		Name:  "call",
		Brief: "call any method by name: loft api call loft.AssetService.Move '{\"ref\": ...}'",
		Args: arg.Args{
			&arg.String{Name: "METHOD", Brief: "loft.AssetService.Search, or /loft.AssetService/Search"},
			&arg.RestStrings{Name: "REQ", Brief: "the request as protojson"},
		},
		Handler: xli.OnRun(func(ctx context.Context, self *xli.Command, next xli.Next) error {
			method, _ := arg.Get[string](self, "METHOD")
			req, _ := arg.Get[[]string](self, "REQ")
			name := strings.Trim(strings.ReplaceAll(method, "/", "."), ".")
			i := strings.LastIndexByte(name, '.')
			if i < 0 {
				return fmt.Errorf("%s: expected <package>.<Service>.<Method>", method)
			}
			d, err := protoregistry.GlobalFiles.FindDescriptorByName(protoreflect.FullName(name[:i]))
			if err != nil {
				return fmt.Errorf("%s: %w", name[:i], err)
			}
			sd, ok := d.(protoreflect.ServiceDescriptor)
			if !ok {
				return fmt.Errorf("%s: not a service", name[:i])
			}
			md := sd.Methods().ByName(protoreflect.Name(name[i+1:]))
			if md == nil {
				return fmt.Errorf("%s: no such method", name)
			}
			if md.IsStreamingClient() || md.IsStreamingServer() {
				return fmt.Errorf("%s: a stream; `loft api <entity> watch` holds one open", name)
			}
			in := messageOf(md.Input())
			if len(req) > 0 {
				if err := protojson.Unmarshal([]byte(strings.Join(req, " ")), in); err != nil {
					return fmt.Errorf("the request: %w", err)
				}
			}
			out := messageOf(md.Output())
			conn, _, err := remote{}.Connect(ctx)
			if err != nil {
				return err
			}
			if err := conn.Invoke(ctx, "/"+string(sd.FullName())+"/"+string(md.Name()), in, out); err != nil {
				return err
			}
			b, err := protojson.MarshalOptions{Multiline: true}.Marshal(out)
			if err != nil {
				return err
			}
			self.Printf("%s\n", b)
			return next(ctx)
		}),
	}
}

// messageOf is a fresh message of a type: the generated one when it is linked
// in, which every one of this app's is.
func messageOf(d protoreflect.MessageDescriptor) proto.Message {
	if t, err := protoregistry.GlobalTypes.FindMessageByName(d.FullName()); err == nil {
		return t.New().Interface()
	}
	return dynamicpb.NewMessage(d)
}

// kebab is a method's name as a command: `SetRole` is `set-role`.
func kebab(v string) string {
	var b strings.Builder
	for i, r := range v {
		if r >= 'A' && r <= 'Z' {
			if i > 0 {
				b.WriteByte('-')
			}
			b.WriteRune(r + ('a' - 'A'))
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}
