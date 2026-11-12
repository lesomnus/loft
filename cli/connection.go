package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/lesomnus/xli/cfg"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/lesomnus/loft/cmd"
	"github.com/lesomnus/loft/server/operator"
	"github.com/lesomnus/loft/server/policy"
)

// Where the terminal's commands go, and who they go as (design 9.16, D75): a
// connection, which the command line, the environment or the client's own
// file names. The connection decides the actor -- the same command, on a
// person's connection, is what that person may do through the app's policy
// and wall, and on an operator's, the operator's work at the entry point.

// Kind is what a connection is.
type Kind string

const (
	// Person is a person's: the app's public address, and the session a
	// person signed in there approved for this terminal (`loft auth login`).
	Person Kind = "person"

	// Operator is an operator's: the entry point's address (`loft-admin
	// serve`, D74), and a reference to the key it is opened with.
	Operator Kind = "operator"
)

// Connection is one, as the client's file keeps it.
type Connection struct {
	Kind Kind   `json:"kind"`
	Url  string `json:"url"`

	// Cookie and Key are a person's session: the cookie it is carried in,
	// and the key this terminal made up, which is the session. An
	// operator's Key is where their key is -- `env:NAME` or `file:PATH` --
	// and never the key: that is in no file this keeps, no shell's history
	// and no process list.
	Cookie string `json:"cookie,omitempty"`
	Key    string `json:"key,omitempty"`

	// Workspace is a person's, the one their commands speak to unless they
	// name another (`loft use`).
	Workspace string `json:"workspace,omitempty"`
}

// connections is the client's file: every connection by its name, and the
// one a command uses when it is told none.
type connections struct {
	Current     string                 `json:"current,omitempty"`
	Connections map[string]*Connection `json:"connections,omitempty"`
}

// defaultEntryPoint is where `loft-admin serve` listens when nothing says
// otherwise (`admin.addr`).
const defaultEntryPoint = "http://127.0.0.1:8081"

// TerminalReads is what the terminal reads of the environment itself, under
// the deployment's prefix and beside its configuration: which connection, the
// entry point's, and the client's own file and workspace. A loader told of
// them takes none of them for a typo of its own.
var TerminalReads = cfg.Reads("CLIENT_CONFIG", "WORKSPACE", "CONNECTION", "ADMIN_URL", "ADMIN_KEY")

var (
	errNoConnection = errors.New("no connection: `loft auth login --url <the app's address>`, or the entry point's with --url and --key (LOFT_ADMIN_URL, LOFT_ADMIN_KEY)")
	errLiteralKey   = errors.New("a key is named by reference -- env:NAME or file:PATH -- and never written out, so that it is in no file, shell history or process list")
)

// clientConfigPath is where the connections are kept: `LOFT_CLIENT_CONFIG`,
// or the user's configuration directory.
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

// loadConnections reads the client's file: none there is none kept.
func loadConnections() (*connections, error) {
	cs := &connections{Connections: map[string]*Connection{}}
	p, err := clientConfigPath()
	if err != nil {
		return nil, err
	}
	b, err := os.ReadFile(p)
	if errors.Is(err, os.ErrNotExist) {
		return cs, nil
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(b, cs); err != nil {
		return nil, fmt.Errorf("%s: %w", p, err)
	}
	if cs.Connections == nil {
		cs.Connections = map[string]*Connection{}
	}
	for name, c := range cs.Connections {
		if err := c.valid(); err != nil {
			return nil, fmt.Errorf("%s: connection %q: %w", p, name, err)
		}
	}
	return cs, nil
}

// save writes the file readable by its owner alone: a person's connection
// holds a session.
func (cs *connections) save() error {
	p, err := clientConfigPath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(cs, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(p, append(b, '\n'), 0o600)
}

// names is the connections' names, in order.
func (cs *connections) names() []string {
	out := make([]string, 0, len(cs.Connections))
	for name := range cs.Connections {
		out = append(out, name)
	}
	slices.Sort(out)
	return out
}

// valid refuses a connection of no kind, or an operator's that holds a key.
func (c *Connection) valid() error {
	switch c.Kind {
	case Person:
	case Operator:
		if !isKeyRef(c.Key) {
			return errLiteralKey
		}
	default:
		return fmt.Errorf("kind %q: a person's or an operator's", c.Kind)
	}
	if _, err := url.Parse(c.Url); err != nil || c.Url == "" {
		return fmt.Errorf("url %q: %v", c.Url, err)
	}
	return nil
}

// isKeyRef is whether `v` names where a key is rather than being one.
func isKeyRef(v string) bool {
	name, ok := strings.CutPrefix(v, "env:")
	if !ok {
		name, ok = strings.CutPrefix(v, "file:")
	}
	return ok && strings.TrimSpace(name) != ""
}

// keyOf is the key a reference names.
func keyOf(ref string) (string, error) {
	switch {
	case strings.HasPrefix(ref, "env:"):
		v := strings.TrimSpace(os.Getenv(ref[len("env:"):]))
		if v == "" {
			return "", fmt.Errorf("%s: not set", ref)
		}
		return v, nil
	case strings.HasPrefix(ref, "file:"):
		b, err := os.ReadFile(ref[len("file:"):])
		if err != nil {
			return "", err
		}
		v := strings.TrimSpace(string(b))
		if v == "" {
			return "", fmt.Errorf("%s: empty", ref)
		}
		return v, nil
	}
	return "", errLiteralKey
}

// Choice is what the command line said of the connection to use.
type Choice struct {
	// Name is `--connection`: one the file keeps.
	Name string
	// Url and Key are `--url` and `--key`: the operator's entry point, for
	// this command alone.
	Url, Key string
}

type choiceKey struct{}

// withChoice is `ctx` carrying what the command line said.
func withChoice(ctx context.Context, c Choice) context.Context {
	return context.WithValue(ctx, choiceKey{}, c)
}

func choiceOf(ctx context.Context) Choice {
	c, _ := ctx.Value(choiceKey{}).(Choice)
	return c
}

// chosen is the connection a command uses, and the name it is kept by --
// empty for one the command line or the environment made for the call.
type chosen struct {
	Name string
	*Connection
}

// choose is the connection a command uses: what the command line names, or
// else what the environment does, or else the file's current one --
// `--connection` and `--key`, then LOFT_CONNECTION and LOFT_ADMIN_KEY, then
// `current`. Two named at one level are refused: which was meant is not
// this to guess.
func (cs *connections) choose(flags Choice, env func(string) string) (chosen, error) {
	named := func(name, by string) (chosen, error) {
		c, ok := cs.Connections[name]
		if !ok {
			return chosen{}, fmt.Errorf("%s: no connection %q (`loft auth list`)", by, name)
		}
		return chosen{Name: name, Connection: c}, nil
	}
	adhoc := func(u, key, by string) (chosen, error) {
		if !isKeyRef(key) {
			return chosen{}, fmt.Errorf("%s: %w", by, errLiteralKey)
		}
		if u == "" {
			u = env("LOFT_ADMIN_URL")
		}
		if u == "" {
			u = defaultEntryPoint
		}
		if _, err := url.Parse(u); err != nil {
			return chosen{}, fmt.Errorf("the entry point %q: %w", u, err)
		}
		return chosen{Connection: &Connection{Kind: Operator, Url: strings.TrimSuffix(u, "/"), Key: key}}, nil
	}

	switch {
	case flags.Name != "" && flags.Key != "":
		return chosen{}, errors.New("--connection or --key, not both")
	case flags.Key != "":
		return adhoc(flags.Url, flags.Key, "--key")
	case flags.Url != "":
		return chosen{}, errors.New("--url names the operator's entry point: give --key too, or --connection")
	case flags.Name != "":
		return named(flags.Name, "--connection")
	}

	name, key := env("LOFT_CONNECTION"), env("LOFT_ADMIN_KEY")
	switch {
	case name != "" && key != "":
		return chosen{}, errors.New("LOFT_CONNECTION or LOFT_ADMIN_KEY, not both")
	case key != "":
		return adhoc("", key, "LOFT_ADMIN_KEY")
	case name != "":
		return named(name, "LOFT_CONNECTION")
	}

	if cs.Current != "" {
		return named(cs.Current, "current")
	}
	return chosen{}, errNoConnection
}

// chooseFor is the connection the command running on `ctx` uses, with the
// file it was chosen from. A connection for the command alone is no file's:
// a script that names the entry point and where its key is reads none, and a
// file that does not read is no reason to refuse it.
func chooseFor(ctx context.Context) (*connections, chosen, error) {
	cs, err := loadConnections()
	if err != nil {
		none := &connections{Connections: map[string]*Connection{}}
		if c, aerr := none.choose(choiceOf(ctx), os.Getenv); aerr == nil && c.Name == "" {
			return none, c, nil
		}
		return nil, chosen{}, err
	}
	c, err := cs.choose(choiceOf(ctx), os.Getenv)
	return cs, c, err
}

// personFor is the connection the command running on `ctx` uses, which has
// to be a person's: a command that is an account's -- its session, its
// workspaces, an assistant speaking as it -- has no operator's to be.
func personFor(ctx context.Context, what string) (*connections, chosen, error) {
	cs, c, err := chooseFor(ctx)
	if err != nil {
		return nil, chosen{}, err
	}
	if c.Kind != Person {
		return nil, chosen{}, fmt.Errorf("%s is a person's, and this is an operator's connection: use a person's (`loft auth switch`, --connection)", what)
	}
	return cs, c, nil
}

// dial is a connection to where `c` goes, as who `c` is: a person's with
// their session and the workspace `ws` (empty is none named), an operator's
// with their key and no workspace, which an operator names in each command.
func (c *Connection) dial(ws string) (*connectConn, error) {
	h := http.Header{}
	h.Set("User-Agent", "loft-cli")
	ws = strings.TrimPrefix(strings.TrimSpace(ws), "@")
	switch c.Kind {
	case Person:
		return c.person(ws), nil
	case Operator:
		if ws != "" {
			return nil, fmt.Errorf("@%s: an operator names the workspace in each command; --workspace, LOFT_WORKSPACE and `loft use` are a person's", ws)
		}
		key, err := keyOf(c.Key)
		if err != nil {
			return nil, fmt.Errorf("the operator's key: %w", err)
		}
		h.Set("Authorization", "Bearer "+key)
	default:
		return nil, fmt.Errorf("a connection of kind %q", c.Kind)
	}
	return &connectConn{base: strings.TrimSuffix(c.Url, "/"), client: &http.Client{Timeout: 0}, header: h, kind: Operator}, nil
}

// signedIn is whether a person's connection holds a session.
func (c *Connection) signedIn() bool { return c.Kind == Person && c.Url != "" && c.Key != "" }

// person is a connection to the app as the person whose connection `c` is,
// to the workspace `ws` (empty is none named, which an account in one
// workspace needs no more than). Only of a person's.
func (c *Connection) person(ws string) *connectConn {
	h := http.Header{}
	h.Set("User-Agent", "loft-cli")
	h.Set("Cookie", c.Cookie+"="+c.Key)
	if ws = strings.TrimPrefix(strings.TrimSpace(ws), "@"); ws != "" {
		h.Set(cmd.WorkspaceHeader, ws)
	}
	return &connectConn{base: strings.TrimSuffix(c.Url, "/"), client: &http.Client{Timeout: 0}, header: h, kind: Person}
}

// operatorOnly is whether `method` is the operator's alone: what the entry
// point serves ([operator.Table]) and no role of a workspace is given
// ([policy.Given]) -- the operator's own services, and an entity's methods
// only an operator calls, as a workspace's contracts and legal holds are
// written. A command that calls nothing else says it is the operator's.
func operatorOnly(method string) bool {
	_, served := operator.Table[method]
	return served && !policy.Given(method)
}

// refusal is what a refusal of `method` says on a connection of `kind` when
// the call went to the other kind (design 9.16): which one it wants, in place
// of what the server that was asked could say of a method it does not serve,
// or serves nobody. What is the operator's alone is served at the entry point
// alone ([operatorOnly]), and the entry point serves its table and nothing
// else ([operator.Table]): an entity's method that both serve is either
// kind's, and its refusal is the server's own.
func refusal(kind Kind, method string, err error) error {
	st, ok := status.FromError(err)
	if err == nil || !ok {
		return err
	}
	switch st.Code() {
	case codes.Unimplemented, codes.PermissionDenied:
	default:
		return err
	}
	var says string
	_, served := operator.Table[method]
	switch {
	case kind == Person && operatorOnly(method):
		says = fmt.Sprintf("%s is the operator's, and this is a person's connection: use an operator's (--key, or `loft auth login --operator`)", method)
	case kind == Operator && !served:
		says = fmt.Sprintf("%s is a person's, and this is an operator's connection: use a person's (--connection, or `loft auth switch`)", method)
	default:
		return err
	}
	p := st.Proto()
	p.Message = says
	return status.FromProto(p).Err()
}

// signedInFor is [personFor], refused when the connection holds no session.
func signedInFor(ctx context.Context, what string) (*connections, chosen, error) {
	cs, c, err := personFor(ctx, what)
	if err != nil {
		return nil, chosen{}, err
	}
	if !c.signedIn() {
		return nil, chosen{}, errNoLogin
	}
	return cs, c, nil
}

// hostOf is what a connection to `u` is called when it is given no name.
func hostOf(u string) string {
	if p, err := url.Parse(u); err == nil && p.Host != "" {
		return p.Host
	}
	return u
}
