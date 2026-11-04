package admin_test

import (
	"context"
	"testing"

	"github.com/lesomnus/payday/pdtest"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	app "github.com/lesomnus/loft"
	"github.com/lesomnus/loft/server/admin"
	"github.com/lesomnus/loft/server/operator"
)

// TestAKeySaysWhoItIs: the entry point answers who the key a call carries
// is -- the operator `admin.keys` names for it, and the identifier the rows
// they write name them by -- and nothing to a call with no key, or with one
// nobody holds. Each operator's key is that operator.
func TestAKeySaysWhoItIs(t *testing.T) {
	e := open(t)
	x := e.x
	const bobs = "bobs-own-operator-key-of-a-good-length"
	ops, err := admin.Operators(map[string]string{"alice": key, "bob": bobs})
	x.NoError(err)
	g, err := admin.Routes{S: e.s, C: e.c, Operators: ops}.Grpc(t.Context())
	x.NoError(err)
	auth := app.NewOperatorAuthServiceClient(pdtest.Serve(t, g))

	_, err = auth.Whoami(context.Background(), &app.OperatorAuthWhoamiRequest{})
	x.Equal(codes.Unauthenticated, status.Code(err), "no key")
	_, err = auth.Whoami(as("somebody-elses-key-of-a-good-length-too"), &app.OperatorAuthWhoamiRequest{})
	x.Equal(codes.Unauthenticated, status.Code(err), "a key nobody holds")

	for name, k := range map[string]string{"alice": key, "bob": bobs} {
		v, err := auth.Whoami(as(k), &app.OperatorAuthWhoamiRequest{})
		x.NoError(err, name)
		x.Equal(name, v.GetName())
		x.Equal(operator.Id(name).Bytes(), v.GetId())
	}

	n, err := e.s.Ent.Access.Query().Count(context.Background())
	x.NoError(err)
	x.Zero(n, "who a key is carries nobody's data")
}
