package cli

import (
	"context"

	"github.com/lesomnus/xli"
	"github.com/lesomnus/xli/arg"
	"google.golang.org/protobuf/encoding/protojson"

	app "github.com/lesomnus/loft"
)

// NewCmdScan is `loft scan`: what a code names -- a label by its code or its
// URL, or an asset by its tag -- in the deployment's workspace. It is the scan
// page's question, asked the way the page asks it.
func NewCmdScan() *xli.Command {
	return &xli.Command{
		Name:  "scan",
		Brief: "what a label's code or URL, or an asset's tag, names",
		Args:  arg.Args{&arg.String{Name: "CODE", Brief: "7K3M-Q9TX, https://<host>/l/7K3MQ9TX, or a tag"}},
		Handler: xli.OnRun(func(ctx context.Context, self *xli.Command, next xli.Next) error {
			code, _ := arg.Get[string](self, "CODE")
			conn, _, err := remote{}.Connect(ctx)
			if err != nil {
				return err
			}
			req := app.LabelResolveRequest_builder{Code: code}.Build()
			out := &app.LabelResolveResponse{}
			if err := conn.Invoke(ctx, app.LabelService_Resolve_FullMethodName, req, out); err != nil {
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
