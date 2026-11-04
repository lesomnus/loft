package admin

import (
	"context"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	app "github.com/lesomnus/loft"
	"github.com/lesomnus/loft/server/operator"
)

// operatorAuth is `OperatorAuthService`: who the key a call carries is, for
// the terminal to say and to check before it keeps a connection (design
// 9.16).
type operatorAuth struct {
	app.UnimplementedOperatorAuthServiceServer
	a Routes
}

// Whoami is the operator the call's key names, as the chain put them in the
// context: a call with no key, or a key nobody holds, is refused before it
// gets here.
func (s operatorAuth) Whoami(ctx context.Context, _ *app.OperatorAuthWhoamiRequest) (*app.OperatorAuthWhoamiResponse, error) {
	o, ok := operator.From(ctx)
	if !ok {
		return nil, status.Error(codes.Unauthenticated, "no operator's key")
	}
	return app.OperatorAuthWhoamiResponse_builder{Name: o.Name, Id: o.Id.Bytes()}.Build(), nil
}
