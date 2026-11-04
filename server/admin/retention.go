package admin

import (
	"context"
	"fmt"
	"maps"
	"slices"

	"github.com/lesomnus/payday/pdid"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"

	app "github.com/lesomnus/loft"
	"github.com/lesomnus/loft/internal/ent"
	enttenant "github.com/lesomnus/loft/internal/ent/tenant"
	"github.com/lesomnus/loft/server/domain"
	"github.com/lesomnus/loft/server/pd"
)

// operatorRetention is `OperatorRetentionService`: what the keep windows take
// out of the history (design 8.2, 8.3) -- what the shell's `loft retention`
// did on the box.
type operatorRetention struct {
	app.UnimplementedOperatorRetentionServiceServer
	a Routes
}

// Plan is what a pass would take now, changing nothing: each workspace's in a
// transaction it then undoes, and what the trail's pass would take of its
// trail beside it.
func (s operatorRetention) Plan(req *app.OperatorRetentionRequest, out grpc.ServerStreamingServer[app.OperatorRetentionResponse]) error {
	return s.pass(req, out, true)
}

// Run takes it, when `app.retention.apply` says the deployment may, and then
// the trail's pass on the same windows, as `serve` does every
// `app.retention.every`.
func (s operatorRetention) Run(req *app.OperatorRetentionRequest, out grpc.ServerStreamingServer[app.OperatorRetentionResponse]) error {
	if !s.a.C.App.Retention.Apply {
		return status.Error(codes.FailedPrecondition, "app.retention.apply is off: nothing may be destroyed. `retention plan` says what would be")
	}
	return s.pass(req, out, false)
}

func (s operatorRetention) pass(req *app.OperatorRetentionRequest, out grpc.ServerStreamingServer[app.OperatorRetentionResponse], dry bool) error {
	ctx := out.Context()
	ts, err := s.tenants(ctx, req)
	if err != nil {
		return err
	}
	p := s.a.S.Trail
	store := pd.TrailStore(s.a.S.Ent)
	say := func(m string) error { return out.Send(app.OperatorRetentionResponse_builder{Progress: m}.Build()) }

	for _, t := range ts {
		id := pdid.Id(t.Id)
		var x domain.Expired
		if err := working(ctx, "the windows of @"+t.Alias, say, func(ctx context.Context, _ progress) error {
			var err error
			x, err = s.a.S.Deps.Expire(ctx, s.a.S.Base, s.a.S.Drv, id, dry)
			return err
		}); err != nil {
			return fmt.Errorf("@%s: %w", t.Alias, err)
		}

		v := app.OperatorRetentionTaken_builder{
			Tenant: id.Bytes(),
			Alias:  t.Alias,
			DryRun: dry,
			Held:   x.Held,
			Lost:   x.Lost,
			Taken: map[string]uint32{
				"placements":      uint32(x.Placements),
				"links":           uint32(x.Links),
				"stewardships":    uint32(x.Stewardships),
				"facts":           uint32(x.Facts),
				"events":          uint32(x.Events),
				"reservations":    uint32(x.Reservations),
				"loans":           uint32(x.Custodies),
				"counts":          uint32(x.Counts),
				"work orders":     uint32(x.WorkOrders),
				"stock movements": uint32(x.StockMovements),
				"attachments":     uint32(x.Attachments),
			},
		}.Build()
		if !x.Before.IsZero() {
			v.SetBefore(timestamppb.New(x.Before))
		}
		if dry && !x.Before.IsZero() && len(x.Held) == 0 && p.Tenants != nil {
			answer, err := p.Tenants(ctx, id)
			if err != nil {
				return fmt.Errorf("@%s: trail: %w", t.Alias, err)
			}
			rs, err := p.Preview(ctx, store, id, answer)
			if err != nil {
				return fmt.Errorf("@%s: trail: %w", t.Alias, err)
			}
			for _, k := range slices.Sorted(maps.Keys(rs)) {
				r := rs[k]
				v.SetTrail(append(v.GetTrail(), app.OperatorRetentionTrail_builder{
					Kind:      k,
					Archived:  uint32(r.Archived),
					Discarded: uint32(r.Discarded),
					Destroyed: uint32(r.Destroyed),
				}.Build()))
			}
		}
		if err := out.Send(app.OperatorRetentionResponse_builder{Taken: v}.Build()); err != nil {
			return err
		}
	}

	if !dry && p.On() {
		// The trail goes on the same windows, in the pass `serve` runs.
		if err := working(ctx, "the trail's pass", say, func(ctx context.Context, _ progress) error {
			p.Pass(ctx, store)
			return nil
		}); err != nil {
			return err
		}
	}
	return out.Send(app.OperatorRetentionResponse_builder{Apply: s.a.C.App.Retention.Apply, Last: true}.Build())
}

// tenants is the workspace `req` names, or every workspace, by address.
func (s operatorRetention) tenants(ctx context.Context, req *app.OperatorRetentionRequest) ([]*ent.Tenant, error) {
	if req.HasRef() {
		t, err := s.a.workspace(ctx, req.GetRef())
		if err != nil {
			return nil, err
		}
		return []*ent.Tenant{t}, nil
	}
	return s.a.S.Ent.Tenant.Query().Order(ent.Asc(enttenant.FieldAlias)).All(ctx)
}
