package cli

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/lesomnus/payday/access"
	"github.com/lesomnus/payday/pdcmd"
	"github.com/lesomnus/xli"
	"github.com/lesomnus/xli/arg"
	"google.golang.org/grpc/metadata"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"

	app "github.com/lesomnus/loft"
)

// The terminal's commands are the schema's (design 9.16, D75): `loft <schema
// name> <verb>`, at the root beside the utilities. An entity's group is
// payday's tree of its verbs (pdcmd), and every other method of its service
// is one more verb beside them, called what the RPC is called (`asset move`,
// `tenant mine`); the one service that is no entity's has a group of its
// own (`payment`). Who a command is, is its connection's: the same command
// is a person's through the app's policy and wall, or the operator's at the
// entry point.

// hidden are the rows other commands write, which somebody reading `loft
// --help` did not come for (pdcmd's Hide): the time rows the history reads,
// and the rows that are part of another thing. They run by name, `loft call`
// reaches their methods, and who may is the policy's to say, as it is of
// every other.
var hidden = []string{
	"placement", "link", "stewardship", "fact",
	"reservation-item", "allocation", "custody-line", "stock-movement", "purchase-line",
}

// internal is what the server keeps for itself and serves to nobody, which is
// no command at all: a person's session, a terminal waiting, the tree's lock,
// an address held.
var internal = []string{"session", "cli-login", "tree-lock", "alias-hold"}

// mount puts the schema's commands on `root`, beside what it holds already,
// and refuses a word that would answer two (pdcmd's Mount).
func mount(root *xli.Command) error {
	t := pdcmd.NewIn(remote{}, "loft")
	// What each command calls, by its path: what says whether it is the
	// operator's alone ([operatorOnly]).
	calls := map[string][]string{}
	for _, e := range pdcmd.Entities("loft") {
		ms := e.Service.Methods()
		for i := range ms.Len() {
			md := ms.Get(i)
			method := "/" + string(e.Service.FullName()) + "/" + string(md.Name())
			switch md.Name() {
			case "Get", "List", "Watch", "Add", "Patch", "Erase":
				// The verbs the tree builds itself.
				if path := e.Name + "/" + strings.ToLower(string(md.Name())); t.Command(path) != nil {
					calls[path] = []string{method}
				}
				continue
			case "Apply":
				// payday's general write, which no role is given: a command
				// that could only be refused, and pdcmd builds none.
				continue
			}
			if md.IsStreamingClient() || md.IsStreamingServer() {
				continue
			}
			c, err := t.Unary(string(md.FullName()))
			if err != nil {
				return err
			}
			if t.Command(e.Name) == nil {
				// An entity whose service has no verb of its own, only
				// methods: a group for them.
				if err := t.Add(e.Name, &xli.Command{Brief: fmt.Sprintf("%s's operations", e.Message.Name()), Handler: xli.RequireSubcommand()}); err != nil {
					return err
				}
			}
			path := e.Name + "/" + kebab(string(md.Name()))
			if err := t.Add(path, c); err != nil {
				return err
			}
			calls[path] = []string{method}
		}
	}

	// And the one service that is no entity's: how a workspace is paid for,
	// relayed to levyan (design 7.9) -- `loft payment get-payer`, the
	// workspace's, and `loft payment list-billing-accounts`, the account's
	// own.
	pay := app.File_loft_payment_proto.Services().ByName("PaymentService")
	if err := t.Add("payment", &xli.Command{Brief: "how a workspace is paid for, as levyan says it", Handler: xli.RequireSubcommand()}); err != nil {
		return err
	}
	for i := range pay.Methods().Len() {
		md := pay.Methods().Get(i)
		c, err := t.Unary(string(md.FullName()))
		if err != nil {
			return err
		}
		path := "payment/" + kebab(string(md.Name()))
		if err := t.Add(path, c); err != nil {
			return err
		}
		calls[path] = []string{"/" + string(pay.FullName()) + "/" + string(md.Name())}
	}

	// What a generated command cannot be, written here at its path: a file
	// written or read, a sheet shown before it is registered, what says what
	// it would do until `--yes`, a stream -- and the operator's own services,
	// which are no entity's.
	for _, w := range handwritten() {
		var err error
		if w.replace {
			err = t.Replace(w.path, w.cmd)
		} else {
			err = t.Add(w.path, w.cmd)
		}
		if err != nil {
			return err
		}
		calls[w.path] = w.calls
	}

	for _, name := range internal {
		if t.Command(name) != nil {
			if err := t.Drop(name); err != nil {
				return err
			}
		}
	}
	for _, name := range hidden {
		if err := t.Hide(name); err != nil {
			return err
		}
	}

	// The words beside the schema's: a workspace is what a person calls a
	// tenant, and a verb said shorter (D75).
	if err := t.Alias("tenant", "workspace", "ws"); err != nil {
		return err
	}
	if err := t.Alias("tenant/configure", "conf", "config"); err != nil {
		return err
	}

	mark(t.Commands(), "", calls)
	return t.Mount(root)
}

// written is a command written here at its schema path: in place of the one
// the tree built there, or where the tree has none.
type written struct {
	path    string
	replace bool
	cmd     *xli.Command
	// calls is what it does: the methods that say whether it is the
	// operator's alone. None is a command that asks nobody what it is for,
	// as a file held to keys `--trust` names.
	calls []string
}

// handwritten is every command written here, by its path.
func handwritten() []written {
	out := []written{
		{path: "asset/add-many", replace: true, cmd: newCmdAssetAddMany(), calls: []string{app.AssetService_AddMany_FullMethodName}},
		{path: "asset/form", replace: true, cmd: newCmdAssetForm(), calls: []string{app.AssetService_Form_FullMethodName}},
	}
	out = append(out, tenantWritten()...)
	out = append(out, trailWritten()...)
	out = append(out, searchWritten()...)
	out = append(out, identityWritten()...)
	out = append(out, eventWritten()...)
	return out
}

// group is the written commands of a group the tree has no place for: the
// group, and each of its commands with what it calls.
func group(g *xli.Command, calls map[string][]string) []written {
	out := []written{{path: g.Name, cmd: &xli.Command{Brief: g.Brief, Handler: xli.RequireSubcommand()}}}
	for _, c := range g.Commands {
		out = append(out, written{path: g.Name + "/" + c.Name, cmd: c, calls: calls[c.Name]})
	}
	return out
}

// operatorMark is what the help says of a command that is the operator's
// alone (design 9.16): the help is the same on every connection.
const operatorMark = " (operator)"

// mark says in the help which commands are the operator's alone: a command
// whose every call is ([operatorOnly]), and a group whose every command is.
// It answers whether all of `level` was.
func mark(level xli.Commands, at string, calls map[string][]string) bool {
	all := len(level) > 0
	for _, c := range level {
		path := strings.TrimPrefix(at+"/"+c.Name, "/")
		alone := false
		if len(c.Commands) > 0 {
			alone = mark(c.Commands, path, calls)
		} else if ms := calls[path]; len(ms) > 0 {
			alone = !slices.ContainsFunc(ms, func(m string) bool { return !operatorOnly(m) })
		}
		if alone && !strings.HasSuffix(c.Brief, operatorMark) {
			c.Brief += operatorMark
		}
		all = all && alone
	}
	return all
}

// newCmdCall is `loft call METHOD [JSON]`: any method by its full name, with
// the request as protojson, answered as protojson -- a hidden row's too.
func newCmdCall() *xli.Command {
	return &xli.Command{
		Name:  "call",
		Brief: "call any method by name: loft call loft.AssetService.Move '{\"ref\": ...}'",
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
				return fmt.Errorf("%s: a stream; `loft <entity> watch` holds one open", name)
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

// reasonOf is what the command line said a download is for (`--reason`),
// as every call is told it.
func reasonOf(ctx context.Context) string {
	md, _ := metadata.FromOutgoingContext(ctx)
	vs := md.Get(access.ReasonKey)
	if len(vs) == 0 {
		return ""
	}
	return strings.TrimSpace(vs[len(vs)-1])
}
