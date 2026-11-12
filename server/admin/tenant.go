package admin

import (
	"context"
	"strings"
	"time"
	"uuid"

	"github.com/lesomnus/payday/pdid"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"

	app "github.com/lesomnus/loft"
	"github.com/lesomnus/loft/cmd"
	"github.com/lesomnus/loft/internal/ent"
	"github.com/lesomnus/loft/internal/ent/holder"
	"github.com/lesomnus/loft/internal/ent/legalhold"
	enttenant "github.com/lesomnus/loft/internal/ent/tenant"
	"github.com/lesomnus/loft/internal/ent/tenantcontract"
	"github.com/lesomnus/loft/server/domain"
	"github.com/lesomnus/loft/server/pd"
	"github.com/lesomnus/loft/server/retention"
)

// named is a reference to a workspace, as the entities' and the operator's
// requests both make one.
type named interface {
	GetId() []byte
	GetAlias() string
}

// workspace is the workspace `ref` names: by its identifier, or by its
// address, `@` or not.
func (a Routes) workspace(ctx context.Context, ref named) (*ent.Tenant, error) {
	q := a.S.Ent.Tenant.Query()
	alias := strings.TrimPrefix(strings.TrimSpace(ref.GetAlias()), "@")
	switch {
	case len(ref.GetId()) > 0:
		id, err := pdid.From(ref.GetId())
		if err != nil {
			return nil, status.Errorf(codes.InvalidArgument, "ref: %v", err)
		}
		q = q.Where(enttenant.Id(id.Uuid()))
	case alias != "":
		q = q.Where(enttenant.Alias(alias))
	default:
		return nil, status.Error(codes.InvalidArgument, "which workspace: its identifier, or its address")
	}
	t, err := q.Only(ctx)
	if ent.IsNotFound(err) {
		if alias != "" {
			return nil, status.Errorf(codes.NotFound, "no workspace is @%s", alias)
		}
		return nil, status.Error(codes.NotFound, "no such workspace")
	}
	return t, err
}

// row is a workspace as the generated server answers with it.
func (a Routes) row(ctx context.Context, t uuid.UUID) (*app.Tenant, error) {
	return a.S.Base.Tenant().Get(ctx, app.TenantGetRequest_builder{Ref: app.TenantRef_builder{Id: t[:]}.Build()}.Build())
}

// tenants is `TenantService` as the operator calls it: every workspace there
// is to find, and the one a call names to close or open again -- as the
// deployment's own work, at the operator's word.
type tenants struct {
	app.UnimplementedTenantServiceServer
	a Routes
}

// Get is one workspace, of any.
func (s tenants) Get(ctx context.Context, req *app.TenantGetRequest) (*app.Tenant, error) {
	return s.a.S.Base.Tenant().Get(ctx, req)
}

// List is every workspace there is, a page at a time.
func (s tenants) List(ctx context.Context, req *app.TenantListRequest) (*app.TenantListResponse, error) {
	return s.a.S.Base.Tenant().List(ctx, req)
}

// Close closes a workspace as an operator -- unpaid, a breach of the terms,
// its owner's request (D29) -- with its address typed in `confirm`, which is
// how an operator knows a workspace (design 9.11). Nobody in it closes it, or
// gets into it once it is closed (D83): it is the operator who restores it
// while the window lasts, or takes its data out.
func (s tenants) Close(ctx context.Context, req *app.TenantCloseRequest) (*app.Tenant, error) {
	t, err := s.a.workspace(ctx, req.GetRef())
	if err != nil {
		return nil, err
	}
	if strings.TrimPrefix(strings.TrimSpace(req.GetConfirm()), "@") != t.Alias {
		return nil, status.Errorf(codes.InvalidArgument, "confirm: an operator closes a workspace by typing its address, @%s", t.Alias)
	}
	return s.a.S.Deps.CloseWorkspace(ctx, s.a.S.Base, s.a.S.Drv, pdid.Id(t.Id), domain.ClosedByOperator, strings.TrimSpace(req.GetReason()))
}

// Restore opens a closed workspace again, while its window lasts: at its
// owner's request, or once what it was closed for is settled.
func (s tenants) Restore(ctx context.Context, req *app.TenantRestoreRequest) (*app.Tenant, error) {
	t, err := s.a.workspace(ctx, req.GetRef())
	if err != nil {
		return nil, err
	}
	return s.a.S.Deps.RestoreWorkspace(ctx, s.a.S.Base, s.a.S.Drv, pdid.Id(t.Id))
}

// operatorTenants is `OperatorTenantService`: what an operator does to a
// workspace that is no RPC of the workspace's own.
type operatorTenants struct {
	app.UnimplementedOperatorTenantServiceServer
	a Routes
}

// Show is a workspace with who is in it, what it is held to, and what that
// comes to now. Its members' rows are the answer's, so that the access record
// says whose data the operator read, filed under the workspace.
func (s operatorTenants) Show(ctx context.Context, req *app.OperatorTenantShowRequest) (*app.OperatorTenantShowResponse, error) {
	t, err := s.a.workspace(ctx, req.GetRef())
	if err != nil {
		return nil, err
	}
	row, err := s.a.row(ctx, t.Id)
	if err != nil {
		return nil, err
	}
	out := app.OperatorTenantShowResponse_builder{Tenant: row}.Build()
	tref := app.Tenant_builder{Id: t.Id[:]}.Build()

	hs, err := s.a.S.Ent.Holder.Query().
		Where(holder.TenantId(t.Id), holder.DateErasedIsNil()).
		Order(ent.Asc(holder.FieldAlias)).
		All(ctx)
	if err != nil {
		return nil, err
	}
	members := make([]*app.Holder, 0, len(hs))
	for _, h := range hs {
		v := app.Holder_builder{
			Id:          h.Id[:],
			Tenant:      tref,
			Alias:       h.Alias,
			Name:        h.Name,
			DateCreated: timestamppb.New(h.DateCreated),
		}.Build()
		if h.DateLeaving != nil {
			v.SetDateLeaving(timestamppb.New(*h.DateLeaving))
		}
		members = append(members, v)
	}
	out.SetMembers(members)

	cs, err := s.a.S.Ent.TenantContract.Query().
		Where(tenantcontract.TenantIdEQ(t.Id), tenantcontract.DateErasedIsNil()).
		Order(ent.Desc(tenantcontract.FieldDateEffective, tenantcontract.FieldId)).
		All(ctx)
	if err != nil {
		return nil, err
	}
	contracts := make([]*app.TenantContract, 0, len(cs))
	for _, c := range cs {
		contracts = append(contracts, app.TenantContract_builder{
			Id:            c.Id[:],
			Tenant:        tref,
			Name:          c.Name,
			Desc:          c.Desc,
			PurgeFloors:   c.PurgeFloors,
			DateEffective: timestamppb.New(c.DateEffective),
			DateCreated:   timestamppb.New(c.DateCreated),
		}.Build())
	}
	out.SetContracts(contracts)

	ls, err := s.a.S.Ent.LegalHold.Query().
		Where(legalhold.TenantIdEQ(t.Id)).
		Order(ent.Desc(legalhold.FieldDateCreated)).
		All(ctx)
	if err != nil {
		return nil, err
	}
	holds := make([]*app.LegalHold, 0, len(ls))
	for _, l := range ls {
		v := app.LegalHold_builder{
			Id:          l.Id[:],
			Tenant:      tref,
			Name:        l.Name,
			Desc:        l.Desc,
			DateCreated: timestamppb.New(l.DateCreated),
		}.Build()
		if l.DateLifted != nil {
			v.SetDateLifted(timestamppb.New(*l.DateLifted))
		}
		holds = append(holds, v)
	}
	out.SetHolds(holds)

	w, err := s.a.window(ctx, t)
	if err != nil {
		return nil, err
	}
	out.SetWindow(w)
	return out, nil
}

// window is what a workspace's own settings, its contracts and its holds come
// to now: how long its history is kept, what the trail keeps of its access
// record -- the floor raising a shorter one -- and how long it may be restored
// once closed; and a window made shorter, until when the longer one holds.
func (a Routes) window(ctx context.Context, t *ent.Tenant) (*app.OperatorTenantWindow, error) {
	d := cmd.RetentionDefaults(a.C, a.S.Trail)
	now := time.Now()
	id := pdid.Id(t.Id)
	w, err := retention.Of(ctx, a.S.Ent, id, now, d)
	if err != nil {
		return nil, err
	}

	out := app.OperatorTenantWindow_builder{
		KeepDays: days(w.Keep),
		Purge:    w.Purge,
		Apply:    a.C.App.Retention.Apply,
	}.Build()
	if u := retention.KeepOf(t).Until(now, d.Wait(), retention.Days); !u.IsZero() {
		out.SetDateKeepUntil(timestamppb.New(u))
		out.SetKeepThenDays(t.KeepDays)
	}
	if u := retention.AccessOf(t).Until(now, d.Wait(), d.AccessDays); !u.IsZero() {
		out.SetDateAccessUntil(timestamppb.New(u))
		out.SetAccessThenDays(days(d.AccessDays(t.AccessKeepDays)))
	}
	if a.S.Trail.Tenants != nil {
		answer, err := a.S.Trail.Tenants(ctx, id)
		if err != nil {
			return nil, err
		}
		out.SetAccessDays(days(cmd.LifeOf(a.S.Trail.ForTenant(answer, pd.AccessDomain))))
	} else {
		out.SetAccessDays(days(cmd.LifeOf(a.S.Trail.For(pd.AccessDomain))))
	}
	restore := w.Restore
	if restore <= 0 {
		restore = a.C.App.Workspaces.Restore()
	}
	out.SetRestoreDays(days(restore))
	return out, nil
}

// days is a window as a count of days; 0 is forever.
func days(d time.Duration) uint32 {
	if d <= 0 {
		return 0
	}
	return uint32(d / (24 * time.Hour))
}

// refused is what was asked refused, as the status it is: one said in words,
// by a part of the deployment that answers no status, is the request's to
// change.
func refused(err error) error {
	if status.Code(err) != codes.Unknown {
		return err
	}
	return status.Error(codes.FailedPrecondition, err.Error())
}
