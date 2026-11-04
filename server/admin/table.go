package admin

import (
	"context"
	"slices"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	app "github.com/lesomnus/loft"
)

// Kind is what a method of the entry point does with what it reads.
type Kind int

const (
	// Reads changes nothing.
	Reads Kind = iota + 1
	// Changes writes, as the deployment's own work at the operator's word.
	Changes
	// Downloads hands people's data out to be kept somewhere else: it says
	// what it is for (`payday-reason-bin`), and its row of the access record
	// is written before its answer leaves.
	Downloads_
)

// Table is every method the entry point serves an operator, by the name gRPC
// knows it by, and what it is (D74). What is not in it is refused before
// anything is read, whatever is registered: a method added to a service the
// entry point serves is closed here until it is added. There are no roles
// among operators -- whoever holds a key holds the table.
//
// An entity's own RPC is here where it is what an operator does, served as the
// operator: the same request, and the deployment's work in the workspace it
// names. The rest are the operator's own services, which no other server has.
var Table = map[string]Kind{
	// Finding a workspace, closing and opening it: what its owner does from
	// inside, done for them.
	app.TenantService_Get_FullMethodName:     Reads,
	app.TenantService_List_FullMethodName:    Reads,
	app.TenantService_Close_FullMethodName:   Changes,
	app.TenantService_Restore_FullMethodName: Changes,

	// What the deployment's agreement with a workspace says, and its legal
	// holds: the operator's, and nobody's in the workspace.
	app.TenantContractService_Add_FullMethodName:  Changes,
	app.TenantContractService_Get_FullMethodName:  Reads,
	app.TenantContractService_List_FullMethodName: Reads,
	app.LegalHoldService_Add_FullMethodName:       Changes,
	app.LegalHoldService_Patch_FullMethodName:     Changes,
	app.LegalHoldService_Get_FullMethodName:       Reads,
	app.LegalHoldService_List_FullMethodName:      Reads,

	// Who is in a workspace.
	app.HolderService_Get_FullMethodName:  Reads,
	app.HolderService_List_FullMethodName: Reads,

	app.OperatorTenantService_Show_FullMethodName:      Reads,
	app.OperatorTenantService_Move_FullMethodName:      Changes,
	app.OperatorTenantService_NameOwner_FullMethodName: Changes,
}

// Downloads is the methods of [Table] that are downloads.
func Downloads() []string {
	out := []string{}
	for m, k := range Table {
		if k == Downloads_ {
			out = append(out, m)
		}
	}
	slices.Sort(out)
	return out
}

// may refuses a method the table does not have.
func may(method string) error {
	if _, ok := Table[method]; !ok {
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
