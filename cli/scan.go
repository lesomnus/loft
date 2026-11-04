package cli

import (
	"context"

	"github.com/lesomnus/xli"
	"github.com/lesomnus/xli/arg"
	"github.com/lesomnus/xli/flg"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/encoding/protojson"

	app "github.com/lesomnus/loft"
)

// NewCmdScan is `loft scan`: what a code names -- a label by its code or its
// URL, or an asset by its tag -- in the workspace in use, or, for a label, in
// whichever of the account's own workspaces it is in (D28). It is the scan
// page's question, asked the way the page asks it.
func NewCmdScan() *xli.Command {
	return &xli.Command{
		Name:  "scan",
		Brief: "what a label's code or URL, or an asset's tag, names",
		Flags: flg.Flags{
			&flg.String{Name: "workspace", Brief: "the workspace to look in first, by alias; `loft use` sets the default"},
		},
		Args: arg.Args{&arg.String{Name: "CODE", Brief: "7K3M-Q9TX, https://<host>/l/7K3MQ9TX, or a tag"}},
		Handler: xli.OnRun(func(ctx context.Context, self *xli.Command, next xli.Next) error {
			code, _ := arg.Get[string](self, "CODE")
			if ws := flag(self, "workspace"); ws != "" {
				ctx = context.WithValue(ctx, workspaceKey{}, ws)
			}
			conn, _, err := remote{}.Connect(ctx)
			if err != nil {
				return err
			}
			req := app.LabelResolveRequest_builder{Code: code}.Build()
			out := &app.LabelResolveResponse{}
			err = conn.Invoke(ctx, app.LabelService_Resolve_FullMethodName, req, out)
			if status.Code(err) == codes.NotFound {
				// Not here: perhaps a label in another workspace of the
				// account's, which is asked of the account.
				_, c, lerr := signedInFor(ctx, "`loft scan`")
				if lerr != nil {
					return lerr
				}
				in, lerr := app.NewLabelServiceClient(c.person("")).Locate(ctx, app.LabelLocateRequest_builder{Code: code}.Build())
				if lerr == nil && in.GetAlias() != "" {
					self.Printf("# in @%s\n", in.GetAlias())
					err = c.person(in.GetAlias()).Invoke(ctx, app.LabelService_Resolve_FullMethodName, req, out)
				}
			}
			if err != nil {
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
