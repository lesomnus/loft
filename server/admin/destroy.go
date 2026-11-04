package admin

import (
	"context"
	"errors"
	"maps"
	"slices"
	"time"

	"github.com/lesomnus/payday/pdid"
	"github.com/lesomnus/payday/trail"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"

	app "github.com/lesomnus/loft"
	"github.com/lesomnus/loft/cmd"
	"github.com/lesomnus/loft/internal/ent"
	"github.com/lesomnus/loft/internal/ent/aliashold"
	"github.com/lesomnus/loft/server/closing"
	"github.com/lesomnus/loft/server/offboard"
	"github.com/lesomnus/loft/server/operator"
	"github.com/lesomnus/loft/server/pd"
	"github.com/lesomnus/loft/server/retention"
)

// Retired is the workspaces that retired from this deployment and what is
// left of each (`trail.Policy.Retired`), the address each was at as the hold
// on it names it.
func (s operatorTenants) Retired(ctx context.Context, _ *app.OperatorTenantRetiredRequest) (*app.OperatorTenantRetiredResponse, error) {
	vs, err := s.a.S.Trail.Retired(ctx, pd.TrailStore(s.a.S.Ent))
	if err != nil {
		return nil, err
	}
	ids := slices.SortedFunc(maps.Keys(vs), func(a, b pdid.Id) int { return vs[a].At.Compare(vs[b].At) })
	out := &app.OperatorTenantRetiredResponse{}
	for _, id := range ids {
		v := app.OperatorTenantRetirement_builder{
			Tenant:  id.Bytes(),
			Retired: timestamppb.New(vs[id].At),
			Until:   timestamppb.New(vs[id].Until),
		}.Build()
		h, err := s.a.S.Ent.AliasHold.Query().
			Where(aliashold.Tenant(id.Uuid()), aliashold.Reason(offboard.ReasonRetired)).
			Order(ent.Desc(aliashold.FieldDateCreated)).
			First(ctx)
		switch {
		case err == nil:
			v.SetAlias(h.Held)
		case !ent.IsNotFound(err):
			return nil, err
		}
		out.SetItems(append(out.GetItems(), v))
	}
	return out, nil
}

// Retire is a workspace destroyed by the operator's hand, keeping what the
// deployment's floors are on of its trail until they pass.
func (s operatorTenants) Retire(req *app.OperatorTenantDestroyRequest, out grpc.ServerStreamingServer[app.OperatorTenantDestroyResponse]) error {
	return s.destroy(req, out, closing.Retire)
}

// Purge is a workspace destroyed by the operator's hand, its whole trail with
// it.
func (s operatorTenants) Purge(req *app.OperatorTenantDestroyRequest, out grpc.ServerStreamingServer[app.OperatorTenantDestroyResponse]) error {
	return s.destroy(req, out, closing.Purge)
}

// destroy is [closing.Closing.Destroy] of the workspace `req` names, as `how`
// says, as the operator's work in it -- what the sweep does to one whose
// restore window is over, by hand, whatever its contract says (which the
// answer says beside it). A hold on it refuses, and so does a retirement the
// deployment cannot keep, before anything is taken.
func (s operatorTenants) destroy(req *app.OperatorTenantDestroyRequest, out grpc.ServerStreamingServer[app.OperatorTenantDestroyResponse], how closing.How) error {
	ctx := out.Context()
	t, err := s.a.workspace(ctx, req.GetRef())
	if err != nil {
		return err
	}
	id := pdid.Id(t.Id)
	w, err := retention.Of(ctx, s.a.S.Ent, id, time.Now(), cmd.RetentionDefaults(s.a.C, s.a.S.Trail))
	if err != nil {
		return err
	}

	var x closing.Destroyed
	verb := "destroying @" + t.Alias
	if req.GetDryRun() {
		verb = "counting what destroying @" + t.Alias + " takes"
	}
	say := func(v string) error {
		return out.Send(app.OperatorTenantDestroyResponse_builder{Progress: v}.Build())
	}
	err = working(ctx, verb, say, func(ctx context.Context, p progress) error {
		var err error
		x, err = s.a.S.Closing.Destroy(operator.Frame(ctx, pdid.Nil), t, how, req.GetDryRun())
		return err
	})
	switch {
	case errors.Is(err, trail.ErrHeld):
		return status.Errorf(codes.FailedPrecondition, "@%s is under a legal hold, and nothing of it is taken while it is: %v", t.Alias, err)
	case errors.Is(err, offboard.ErrNoArchive):
		return status.Errorf(codes.FailedPrecondition, "@%s: %v -- name one, or purge it", t.Alias, err)
	case err != nil:
		return err
	}

	v := app.OperatorTenantDestroyed_builder{
		Tenant:         id.Bytes(),
		Alias:          t.Alias,
		DryRun:         req.GetDryRun(),
		Rows:           counts(x.Rows),
		Files:          uint32(x.Files),
		Lost:           x.Lost,
		TrailRemoved:   uint32(x.Trail.Removed),
		TrailBlanked:   uint32(x.Trail.Blanked),
		TrailChunks:    uint32(x.Trail.Chunks),
		Retired:        x.Retired,
		Kept:           counts(x.Trail.Kept),
		Embedded:       s.a.S.Identity.Embedded(),
		Forgotten:      uint32(x.Forgotten),
		Bills:          s.a.S.Meters != nil,
		Tells:          s.a.S.Notices != nil,
		ContractPurges: w.Purge,
	}.Build()
	if !x.Trail.Until.IsZero() {
		v.SetKeptUntil(timestamppb.New(x.Trail.Until))
	}
	if x.Unmeasured != nil {
		v.SetUnmeasured(x.Unmeasured.Error())
	}
	return out.Send(app.OperatorTenantDestroyResponse_builder{Destroyed: v}.Build())
}

// counts is a count by name as a message holds one.
func counts(vs map[string]int) map[string]uint32 {
	out := make(map[string]uint32, len(vs))
	for k, n := range vs {
		out[k] = uint32(n)
	}
	return out
}
