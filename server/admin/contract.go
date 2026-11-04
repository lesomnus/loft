package admin

import (
	"context"
	"strings"
	"time"

	"github.com/lesomnus/payday/pdid"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"

	app "github.com/lesomnus/loft"
	"github.com/lesomnus/loft/internal/ent"
	"github.com/lesomnus/loft/server/operator"
)

// contracts is `TenantContractService` as the operator calls it: what the
// deployment's agreement with a workspace says beyond the workspace's own
// settings -- whether its trail goes whole when it is destroyed (payday#115)
// -- which no role in a workspace writes (`server/policy`). Written through
// the servers no wall was put in front of, as the operator's work in the
// workspace it is of.
type contracts struct {
	app.UnimplementedTenantContractServiceServer
	a Routes
}

// Add writes a workspace's next contract, in force from `date_effective` --
// now, when it says none. How long a workspace keeps its history is its own,
// its owner's to say (D36), and no term of a contract.
func (s contracts) Add(ctx context.Context, req *app.TenantContractAddRequest) (*app.TenantContract, error) {
	t, err := s.a.workspace(ctx, req.GetTenant())
	if err != nil {
		return nil, err
	}
	v := app.TenantContractAddRequest_builder{
		Tenant:        app.TenantRef_builder{Id: t.Id[:]}.Build(),
		Name:          strings.TrimSpace(req.GetName()),
		Desc:          strings.TrimSpace(req.GetDesc()),
		DateEffective: req.GetDateEffective(),
	}.Build()
	if req.HasPurgeFloors() {
		v.SetPurgeFloors(req.GetPurgeFloors())
	}
	if !v.HasDateEffective() {
		v.SetDateEffective(timestamppb.Now())
	}
	return s.a.S.Base.TenantContract().Add(operator.Frame(ctx, pdid.Nil), v)
}

// Get is one contract, of any workspace.
func (s contracts) Get(ctx context.Context, req *app.TenantContractGetRequest) (*app.TenantContract, error) {
	return s.a.S.Base.TenantContract().Get(ctx, req)
}

// List is every workspace's contracts, a page at a time.
func (s contracts) List(ctx context.Context, req *app.TenantContractListRequest) (*app.TenantContractListResponse, error) {
	return s.a.S.Base.TenantContract().List(ctx, req)
}

// holds is `LegalHoldService` as the operator calls it: a hold on a
// workspace's history, which keeps all of it -- Loft's history and the trail
// -- until it is lifted. The people who run the workspace read theirs; the
// operator places and lifts them.
type holds struct {
	app.UnimplementedLegalHoldServiceServer
	a Routes
}

// Add places a hold, which says what it is for.
func (s holds) Add(ctx context.Context, req *app.LegalHoldAddRequest) (*app.LegalHold, error) {
	why := strings.TrimSpace(req.GetName())
	if why == "" {
		return nil, status.Error(codes.InvalidArgument, "name: a hold says what it is for -- a case, a demand, an agreement")
	}
	if req.HasDateLifted() {
		return nil, status.Error(codes.InvalidArgument, "date_lifted: a hold is placed on, and lifted after")
	}
	t, err := s.a.workspace(ctx, req.GetTenant())
	if err != nil {
		return nil, err
	}
	return s.a.S.Base.LegalHold().Add(operator.Frame(ctx, pdid.Nil), app.LegalHoldAddRequest_builder{
		Tenant: app.TenantRef_builder{Id: t.Id[:]}.Build(),
		Name:   why,
		Desc:   strings.TrimSpace(req.GetDesc()),
	}.Build())
}

// Patch lifts a hold: `date_lifted` and nothing else, at a moment that has
// come. The windows apply again from the next pass. What a hold is for is what
// it was placed for, and is not written again; one lifted by mistake is placed
// again.
func (s holds) Patch(ctx context.Context, req *app.LegalHoldPatchRequest) (*app.LegalHold, error) {
	if req.HasName() || req.HasDesc() || req.GetDateLiftedNull() {
		return nil, status.Error(codes.InvalidArgument, "an operator lifts a hold, by date_lifted, and changes nothing else of it")
	}
	if !req.HasDateLifted() {
		return nil, status.Error(codes.InvalidArgument, "date_lifted: when it is lifted")
	}
	if req.GetDateLifted().AsTime().After(time.Now().Add(time.Minute)) {
		return nil, status.Error(codes.InvalidArgument, "date_lifted: a hold is lifted when it is, and not ahead of it")
	}
	id, err := pdid.From(req.GetRef().GetId())
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "ref: %v", err)
	}
	l, err := s.a.S.Ent.LegalHold.Get(ctx, id.Uuid())
	if ent.IsNotFound(err) {
		return nil, status.Error(codes.NotFound, "no such hold")
	}
	if err != nil {
		return nil, err
	}
	if l.DateLifted != nil {
		return nil, status.Errorf(codes.FailedPrecondition, "the hold was lifted already, %s", l.DateLifted.UTC().Format(time.RFC3339))
	}
	return s.a.S.Base.LegalHold().Patch(operator.Frame(ctx, pdid.Nil), req)
}

// Get is one hold, of any workspace.
func (s holds) Get(ctx context.Context, req *app.LegalHoldGetRequest) (*app.LegalHold, error) {
	return s.a.S.Base.LegalHold().Get(ctx, req)
}

// List is every workspace's holds, a page at a time.
func (s holds) List(ctx context.Context, req *app.LegalHoldListRequest) (*app.LegalHoldListResponse, error) {
	return s.a.S.Base.LegalHold().List(ctx, req)
}

// holders is `HolderService` as the operator reads it: who is in a workspace,
// of any. A membership is somebody, so every read of one is a row of the
// access record naming the operator.
type holders struct {
	app.UnimplementedHolderServiceServer
	a Routes
}

// Get is one membership.
func (s holders) Get(ctx context.Context, req *app.HolderGetRequest) (*app.Holder, error) {
	return s.a.S.Base.Holder().Get(ctx, req)
}

// List is the memberships a filter names -- a workspace's, by its `tenant` --
// a page at a time.
func (s holders) List(ctx context.Context, req *app.HolderListRequest) (*app.HolderListResponse, error) {
	return s.a.S.Base.Holder().List(ctx, req)
}
