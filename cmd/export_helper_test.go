package cmd_test

import (
	"bytes"
	"context"
	"errors"
	"io"

	"github.com/lesomnus/payday/access"
	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"

	app "github.com/lesomnus/loft"
)

// exported is a workspace's export over the RPC (`TenantService.Export`), as
// the operator takes it out at the entry point (D83) -- or as anybody else
// asks for it on `conn`: every part, put together, and what the first said
// the file is called. `why` is what it is for, said as a download says it
// (`payday-reason-bin`); empty says nothing.
func exported(ctx context.Context, conn grpc.ClientConnInterface, ref *app.TenantRef, why string) ([]byte, string, error) {
	if why != "" {
		ctx = metadata.AppendToOutgoingContext(ctx, access.ReasonKey, why)
	}
	s, err := app.NewTenantServiceClient(conn).Export(ctx, app.TenantExportRequest_builder{Ref: ref}.Build())
	if err != nil {
		return nil, "", err
	}
	var (
		b    bytes.Buffer
		name string
	)
	for first := true; ; first = false {
		v, err := s.Recv()
		if errors.Is(err, io.EOF) {
			return b.Bytes(), name, nil
		}
		if err != nil {
			return nil, "", err
		}
		if first {
			name = v.GetName()
		}
		b.Write(v.GetData())
	}
}
