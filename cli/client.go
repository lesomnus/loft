package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os/exec"
	"runtime"
	"strings"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"
	"google.golang.org/protobuf/types/dynamicpb"

	"github.com/lesomnus/payday/pdcmd"
)

// The terminal's half of this app: everything it can do, asked of a running
// deployment as the connection in use says (design 9.16) -- a person's, over
// the address a browser reaches it at, as a member signed in by approving
// the terminal there (`loft auth login`, `server/clilogin`), through the same
// policy and the same wall as the page; or an operator's, of the entry point
// with their key (D74).
//
// It is not the box's, which runs with the database where there is no server
// to ask (`loft init`, `loft migrate`); these run anywhere.

// do is one plain HTTP call beside the RPCs, as a person: `/session`,
// `/cli/...`.
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
	"/loft.PartyService/Me", "/loft.LabelService/Resolve",
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

// remote is the connector of the API's commands: the connection the command
// line, the environment or the client's file names (design 9.16), to the
// workspace the deployment serves (D85).
type remote struct{}

func (remote) Connect(ctx context.Context) (pdcmd.Conn, func(), error) {
	_, c, err := chooseFor(ctx)
	if err != nil {
		return nil, nil, err
	}
	if c.Kind == Person && !c.signedIn() {
		return nil, nil, errNoLogin
	}
	conn, err := c.dial()
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
