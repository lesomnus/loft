package domain

import (
	"context"
	"errors"

	"github.com/lesomnus/payday/pdid"
	"github.com/lesomnus/roster/reach"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	app "github.com/lesomnus/loft"
	"github.com/lesomnus/loft/internal/ent/holder"
	"github.com/lesomnus/loft/server/policy"
)

// Reaches is what the tenant's people may call, as roster says it (D89):
// roster's `reach.Holders`, kept for half a minute and dropped by its stream.
// The domain layer asks it of a permission finer than one method
// (`proto/loft/permission.proto`) -- the same act over somebody else's -- of
// who is told what is theirs to act on, and of what the page is told somebody
// may call. It never asks which role somebody holds: Loft knows no role's
// name, and a tenant may rename, split or narrow them at roster.
type Reaches interface {
	May(ctx context.Context, holder pdid.Id, method string) error
	Reach(ctx context.Context, holder pdid.Id) (reach.Reach, error)
}

// may refuses the caller `method` -- an RPC of Loft's, or a permission of
// [app.PermissionServiceServer]'s -- unless roster says they may call it:
// PermissionDenied saying `why` when it does not, and Unavailable when it
// could not be asked, which is no doing of theirs.
func (t *Tx) may(method, why string) error {
	ok, err := t.mayTo(method)
	switch {
	case err != nil:
		return err
	case !ok:
		return status.Error(codes.PermissionDenied, why)
	}
	return nil
}

// mayTo is [Tx.may] as a yes or no, for an operation that does less rather
// than refusing: false for a roster that answered no, and an error -- what the
// caller is told -- for one that could not be asked. The deployment's own
// work, which is nobody's call, may nothing finer than a method.
func (t *Tx) mayTo(method string) (bool, error) {
	if t.deps == nil || t.deps.Reaches == nil || t.actor.IsZero() {
		return false, nil
	}
	err := t.deps.Reaches.May(t.ctx, t.actor, method)
	switch {
	case err == nil:
		return true, nil
	case errors.Is(err, reach.ErrDenied):
		return false, nil
	}
	return false, policy.Said(err, method)
}

// whoMay is the members of the workspace roster says may call `method`, in the
// order they joined: who is told what is theirs to act on ([Tx.notifyWhoMay]).
// A member leaving is nobody's to tell. One roster could not be asked about is
// left out rather than failing what told them: a notice is not worth an
// operation, and roster's answer is asked again next time.
func (t *Tx) whoMay(method string) ([]pdid.Id, error) {
	if t.deps == nil || t.deps.Reaches == nil {
		return nil, nil
	}
	hs, err := t.db.Holder.Query().Where(
		holder.TenantId(t.tenant.Uuid()),
		holder.DateErasedIsNil(),
		holder.DateLeavingIsNil(),
	).Order(holder.ByDateCreated(), holder.ById()).Ids(t.ctx)
	if err != nil {
		return nil, err
	}
	out := []pdid.Id{}
	for _, h := range hs {
		if err := t.deps.Reaches.May(t.ctx, pdid.Id(h), method); err == nil {
			out = append(out, pdid.Id(h))
		} else if t.ctx.Err() != nil {
			return nil, t.ctx.Err()
		}
	}
	return out, nil
}

// methodsOf is every method a role may be given here that roster says
// `member` may call (`policy.Methods`), by its full name: what the page draws
// its menus and buttons from (`PartyService.Me`). None for a roster that could
// not be asked, which the page reads as nothing to do until it asks again.
func (t *Tx) methodsOf(member pdid.Id) ([]string, error) {
	if t.deps == nil || t.deps.Reaches == nil {
		return []string{}, nil
	}
	r, err := t.deps.Reaches.Reach(t.ctx, member)
	switch {
	case errors.Is(err, reach.ErrNoPerson):
		return []string{}, nil
	case err != nil:
		return nil, policy.Said(err, app.PartyService_Me_FullMethodName)
	}
	out := []string{}
	for _, m := range policy.Methods() {
		if r.Covers(m) {
			out = append(out, m)
		}
	}
	return out, nil
}
