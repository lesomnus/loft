package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/lesomnus/payday/access"
	"github.com/lesomnus/payday/pdcmd"
	"github.com/lesomnus/xli"
	"github.com/lesomnus/xli/arg"
	"github.com/lesomnus/xli/flg"
	"github.com/lesomnus/z"
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
	for _, e := range pdcmd.Entities("loft") {
		ms := e.Service.Methods()
		for i := range ms.Len() {
			md := ms.Get(i)
			switch md.Name() {
			case "Get", "List", "Watch", "Add", "Patch", "Erase":
				// The verbs the tree builds itself.
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
			if err := t.Add(e.Name+"/"+kebab(string(md.Name())), c); err != nil {
				return err
			}
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
		if err := t.Add("payment/"+kebab(string(md.Name())), c); err != nil {
			return err
		}
	}

	// What a generated command cannot be: a file written or read, and a
	// sheet shown before it is registered.
	if err := t.Add("tenant/export", newCmdTenantExport()); err != nil {
		return err
	}
	if err := t.Replace("asset/add-many", newCmdAssetAddMany()); err != nil {
		return err
	}
	if err := t.Replace("asset/form", newCmdAssetForm()); err != nil {
		return err
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

	return t.Mount(root)
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

// newCmdTenantExport is `loft tenant export`: everything a workspace has, as
// one zip (`TenantService.Export`) -- its owner's on a person's connection,
// as their membership of it, and the operator's on theirs, for one that is
// leaving (design 8.2). A download, so it says what it is for.
func newCmdTenantExport() *xli.Command {
	return &xli.Command{
		Brief: "everything a workspace has, as one zip: what it takes out before it leaves (--reason first)",
		Flags: flg.Flags{
			&flg.String{Name: "out", Brief: "where to write it; <workspace>-<date>.zip by default"},
		},
		Args: arg.Args{&arg.String{Name: "WORKSPACE", Brief: "by alias; a person's default is the one `loft use` names", Optional: true}},
		Handler: xli.OnRun(func(ctx context.Context, self *xli.Command, next xli.Next) error {
			named, _ := arg.Get[string](self, "WORKSPACE")
			conn, ws, err := workspaceConn(ctx, named)
			if err != nil {
				return err
			}
			if reasonOf(ctx) == "" {
				return errors.New("--reason, before the command: what the export is for; it hands everybody's data out, and is recorded with why")
			}
			out := flag(self, "out")
			if out == "" {
				out = fmt.Sprintf("%s-%s.zip", ws, time.Now().Format("20060102"))
			}
			// Written beside where it goes and moved there once whole, so
			// that a download cut off is no file at all.
			tmp, err := os.CreateTemp(filepath.Dir(out), ".loft-export-*")
			if err != nil {
				return err
			}
			defer os.Remove(tmp.Name())
			stream, err := app.NewTenantServiceClient(conn).Export(ctx, app.TenantExportRequest_builder{
				Ref: app.TenantRef_builder{Alias: z.Ptr(ws)}.Build(),
			}.Build())
			if err != nil {
				tmp.Close()
				return err
			}
			for {
				part, err := stream.Recv()
				if errors.Is(err, io.EOF) {
					break
				}
				if err != nil {
					tmp.Close()
					return err
				}
				if _, err := tmp.Write(part.GetData()); err != nil {
					tmp.Close()
					return err
				}
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
	}
}

// workspaceConn is a connection for a command about the workspace `named`
// names, by alias, and the alias: a person's speaks to it as their membership
// of it -- the one in use when none is named -- and an operator's names it in
// the request alone.
func workspaceConn(ctx context.Context, named string) (*connectConn, string, error) {
	_, c, err := chooseFor(ctx)
	if err != nil {
		return nil, "", err
	}
	ws := strings.TrimPrefix(strings.TrimSpace(named), "@")
	switch c.Kind {
	case Person:
		if !c.signedIn() {
			return nil, "", errNoLogin
		}
		if ws == "" {
			ws, _ = ctx.Value(workspaceKey{}).(string)
		}
		if ws == "" {
			ws = os.Getenv("LOFT_WORKSPACE")
		}
		if ws == "" {
			ws = c.Workspace
		}
		ws = strings.TrimPrefix(ws, "@")
		if ws == "" {
			return nil, "", errors.New("WORKSPACE: which one (`loft tenant mine` lists them)")
		}
		return c.person(ws), ws, nil
	case Operator:
		// What names a person's workspace names none here.
		said, _ := ctx.Value(workspaceKey{}).(string)
		if said == "" {
			said = os.Getenv("LOFT_WORKSPACE")
		}
		conn, err := c.dial(said)
		if err != nil {
			return nil, "", err
		}
		if ws == "" {
			return nil, "", errors.New("WORKSPACE: which one (`loft tenant list`)")
		}
		return conn, ws, nil
	}
	return nil, "", fmt.Errorf("a connection of kind %q", c.Kind)
}
