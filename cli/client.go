package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"runtime"
	"strings"

	"github.com/lesomnus/xli"
	"github.com/lesomnus/xli/arg"
	"github.com/lesomnus/xli/flg"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"
	"google.golang.org/protobuf/types/dynamicpb"

	"github.com/lesomnus/payday/pdcmd"

	app "github.com/lesomnus/loft"
)

// The terminal's half of this app: everything it can do, asked of a running
// deployment as the connection in use says (design 9.16) -- a person's, over
// the address a browser reaches it at, as an account signed in by approving
// the terminal there (`loft auth login`, `server/clilogin`), through the same
// policy and the same wall as the page; or an operator's, of the entry point
// with their key (D74).
//
// It is not the box's, which runs with the database where there is no server
// to ask (`loft init`, `loft migrate`); these run anywhere.

// do is one plain HTTP call beside the RPCs, as a person: `/account`,
// `/cli/...`, `/session`.
func (c *Connection) do(ctx context.Context, method, path string, body any, out any) (int, error) {
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
	if c.Kind == Person && c.Key != "" {
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

// reads is what `loft auth login --allow read` asks for: the methods that only
// read -- every entity's Get, List and Watch, and the searches, histories and
// answers beside them -- for a terminal handed to an assistant that may look
// and not touch. Named by pattern, so that a read added later is one too, and
// never a method that writes: SetPassword and MarkRead are not here.
var reads = []string{
	"/loft.*/Get", "/loft.*/List", "/loft.*/Watch", "/loft.*/Search", "/loft.*/Recent",
	"/loft.AssetService/Timeline", "/loft.AssetService/QueryAt", "/loft.AssetService/Diff", "/loft.AssetService/Report",
	"/loft.ReservationService/Calendar", "/loft.ReservationService/Timeline", "/loft.BookableService/Availability",
	"/loft.PartyService/Me", "/loft.LabelService/Resolve", "/loft.DomainService/Status",
	"/loft.AttachmentService/Url", "/loft.NotificationService/Inbox",
	// What the account is in, and which of them a label is in: its own reads
	// (D73), which were HTTP every session might ask.
	"/loft.TenantService/Mine", "/loft.LabelService/Locate",
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

// NewCmdUse is `loft use`: which workspace the terminal speaks to when a
// command names none.
func NewCmdUse() *xli.Command {
	return &xli.Command{
		Name:  "use",
		Brief: "the workspace commands speak to unless --workspace says otherwise",
		Flags: flg.Flags{&flg.Switch{Name: "none", Brief: "name none: an account in one workspace needs none"}},
		Args:  arg.Args{&arg.String{Name: "WORKSPACE", Brief: "by alias; none prints the one in use", Optional: true}},
		Handler: xli.OnRun(func(ctx context.Context, self *xli.Command, next xli.Next) error {
			cs, c, err := signedInFor(ctx, "`loft use`")
			if err != nil {
				return err
			}
			if c.Name == "" {
				return errors.New("`loft use` keeps the workspace in a connection the file keeps; this one is the command line's")
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
				mine, err := app.NewTenantServiceClient(c.person("")).Mine(ctx, &app.TenantMineRequest{})
				if err != nil {
					return err
				}
				found := false
				for _, w := range mine.GetWorkspaces() {
					found = found || w.GetAlias() == ws
				}
				if !found {
					return fmt.Errorf("@%s: not a workspace this account is in (`loft auth whoami`)", ws)
				}
				c.Workspace = ws
			}
			if err := cs.save(); err != nil {
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

type workspaceKey struct{}

// remote is the connector of the API's commands: the connection the
// command line, the environment or the client's file names (design 9.16),
// to the workspace the command line, `LOFT_WORKSPACE` or `loft use` names
// when it is a person's.
type remote struct{}

func (remote) Connect(ctx context.Context) (pdcmd.Conn, func(), error) {
	_, c, err := chooseFor(ctx)
	if err != nil {
		return nil, nil, err
	}
	if c.Kind == Person && !c.signedIn() {
		return nil, nil, errNoLogin
	}
	ws, _ := ctx.Value(workspaceKey{}).(string)
	if ws == "" {
		ws = os.Getenv("LOFT_WORKSPACE")
	}
	if ws == "" && c.Kind == Person {
		ws = c.Workspace
	}
	conn, err := c.dial(ws)
	if err != nil {
		return nil, nil, err
	}
	return conn, nil, nil
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
