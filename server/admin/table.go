package admin

import (
	"context"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/lesomnus/loft/server/operator"
)

// may refuses a method the table does not have.
func may(method string) error {
	if _, ok := operator.Table[method]; !ok {
		return status.Errorf(codes.PermissionDenied, "the operator's entry point does not serve %s", method)
	}
	return nil
}

func mayUnary(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
	if err := may(info.FullMethod); err != nil {
		return nil, err
	}
	return handler(ctx, req)
}

func mayStream(srv any, ss grpc.ServerStream, info *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
	if err := may(info.FullMethod); err != nil {
		return err
	}
	return handler(srv, ss)
}
