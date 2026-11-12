package admin

import (
	"context"

	"github.com/lesomnus/payday/pdid"

	app "github.com/lesomnus/loft"
	"github.com/lesomnus/loft/server/operator"
)

// operatorRole is `OperatorRoleService`: the roles Loft ships, as the tenant
// at roster has them (D90).
type operatorRole struct {
	app.UnimplementedOperatorRoleServiceServer
	a Routes
}

// Widen is `tenancy.Roles.Widen` for the workspace this deployment serves, as
// the operator's work in it: what `serve` does as it starts, asked for now and
// told first.
func (s operatorRole) Widen(ctx context.Context, req *app.OperatorRoleWidenRequest) (*app.OperatorRoleWidenResponse, error) {
	t, err := s.a.served(ctx)
	if err != nil {
		return nil, err
	}
	tid := pdid.Id(t.Id)
	v, err := s.a.S.Roles.Widen(operator.Frame(ctx, tid), tid, req.GetFrom(), req.GetDryRun())
	out := app.OperatorRoleWidenResponse_builder{
		DryRun:     req.GetDryRun(),
		From:       v.From,
		To:         v.To,
		Unrecorded: v.Unrecorded,
	}.Build()
	for _, r := range v.Roles {
		out.SetRoles(append(out.GetRoles(), app.OperatorRoleWidened_builder{
			Role: r.Role, Methods: r.Methods, Missing: r.Missing,
		}.Build()))
	}
	return out, err
}
