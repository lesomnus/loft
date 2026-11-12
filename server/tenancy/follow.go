package tenancy

import (
	"context"

	"github.com/lesomnus/payday/frame"
	"github.com/lesomnus/payday/pdid"
	"github.com/lesomnus/z"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	app "github.com/lesomnus/loft"
	"github.com/lesomnus/loft/internal/identity"
)

// Follow makes the workspace's address the tenant's at roster, `t` (D86): its
// alias is roster's, and a tenant renamed there is the workspace's address
// once this has seen it. What `serve` does as it starts, and `loft identity
// link` once it made the tenant. Written through `base` as the deployment's
// own work in the workspace, so that the trail says it. It answers the
// address the workspace was at when it moved, and nothing when there is no
// workspace of the tenant here or it is at that address already.
//
// Nobody else moves it: the domain layer refuses an alias in
// `TenantService.Patch`, and the operator has no command for it.
func Follow(ctx context.Context, base app.Server, t identity.Tenant) (string, error) {
	as := frame.Into(ctx, frame.New(pdid.Nil, t.Id, frame.Whole()).WithScope(frame.Only(t.Id)))
	ref := app.TenantRef_builder{Id: t.Id.Bytes()}.Build()
	ws, err := base.Tenant().Get(as, app.TenantGetRequest_builder{Ref: ref}.Build())
	if status.Code(err) == codes.NotFound {
		return "", nil
	}
	if err != nil || ws.GetAlias() == t.Alias {
		return "", err
	}
	if _, err := base.Tenant().Patch(as, app.TenantPatchRequest_builder{
		Ref:              ref,
		Alias:            z.Ptr(t.Alias),
		DateUpdatedForce: z.Ptr(true),
	}.Build()); err != nil {
		return "", err
	}
	return ws.GetAlias(), nil
}
