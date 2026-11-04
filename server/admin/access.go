package admin

import (
	"context"
	"time"
	"uuid"

	"github.com/lesomnus/payday/access"
	"github.com/lesomnus/payday/pdid"
	"github.com/lesomnus/payday/pdpb"
	"github.com/protobuf-orm/ent/dialect/sql"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"

	app "github.com/lesomnus/loft"
	"github.com/lesomnus/loft/internal/ent"
	entaccess "github.com/lesomnus/loft/internal/ent/access"
	"github.com/lesomnus/loft/internal/ent/predicate"
	"github.com/lesomnus/loft/server/operator"
)

// How many rows of the access record one List answers when it says nothing,
// and at most.
const (
	accessPage = 100
	accessMost = 1000
)

// operatorAccess is `OperatorAccessService`: the access record of the whole
// deployment, for an operator (loft#136) -- what the shell's `loft access
// ls` read off the database.
type operatorAccess struct {
	app.UnimplementedOperatorAccessServiceServer
	a Routes
}

// List is the calls that carried somebody's personal data, newest first: of
// one workspace, by one actor, of one person, of one kind, in a stretch --
// the thirty days to now when it names none. Each row says who made the call
// by identifier, an operator's by name too, where from, and what a download
// was for. Those are the actor's -- their address, their words -- so the
// actors of the rows it answers are on the access record of this call; whose
// data each call carried it counts, and does not name. It changes nothing
// else.
func (s operatorAccess) List(ctx context.Context, req *app.OperatorAccessListRequest) (*app.OperatorAccessListResponse, error) {
	now := time.Now()
	from, to := now.Add(-30*24*time.Hour), now
	if req.HasFrom() {
		from = req.GetFrom().AsTime()
	}
	if req.HasTo() {
		to = req.GetTo().AsTime()
	}
	if !to.After(from) {
		return nil, status.Error(codes.InvalidArgument, "to: before from")
	}
	size := int(req.GetSize())
	switch {
	case size == 0:
		size = accessPage
	case size > accessMost:
		size = accessMost
	}

	ps := []predicate.Access{entaccess.DateCreatedGTE(from), entaccess.DateCreatedLT(to)}
	if req.HasTenant() {
		t, err := s.a.workspace(ctx, req.GetTenant())
		if err != nil {
			return nil, err
		}
		ps = append(ps, entaccess.TenantId(t.Id))
	}
	if v := req.GetActor(); len(v) > 0 {
		id, err := pdid.From(v)
		if err != nil {
			return nil, status.Errorf(codes.InvalidArgument, "actor: %v", err)
		}
		ps = append(ps, entaccess.ActorId(id.Uuid()))
	}
	switch v := req.GetKind(); v {
	case "":
	case "read", "write", "download":
		ps = append(ps, entaccess.Kind(v))
	default:
		return nil, status.Error(codes.InvalidArgument, "kind: read, write or download")
	}
	var subject []byte
	if v := req.GetSubject(); len(v) > 0 {
		id, err := pdid.From(v)
		if err != nil {
			return nil, status.Errorf(codes.InvalidArgument, "subject: %v", err)
		}
		subject = id.Bytes()
	}

	aliases := map[uuid.UUID]string{}
	alias := func(id uuid.UUID) string {
		if v, ok := aliases[id]; ok {
			return v
		}
		v := pdid.Id(id).String()
		if t, err := s.a.S.Ent.Tenant.Get(ctx, id); err == nil {
			v = t.Alias
		}
		aliases[id] = v
		return v
	}

	// A batch at a time, newest first, until enough: whose data a call
	// carried is a list, which the database is not asked about.
	out := &app.OperatorAccessListResponse{}
	seen := []access.Subject{}
	cursor := []predicate.Access{}
	for len(out.GetItems()) < size {
		vs, err := s.a.S.Ent.Access.Query().
			Where(append(append([]predicate.Access{}, ps...), cursor...)...).
			Order(ent.Desc(entaccess.FieldDateCreated), ent.Desc(entaccess.FieldId)).
			Limit(500).
			All(ctx)
		if err != nil {
			return nil, err
		}
		for _, v := range vs {
			if subject != nil && !carries(v, subject) {
				continue
			}
			d := &pdpb.AccessDetail{}
			_ = proto.Unmarshal(v.Value, d)
			actor := pdid.Id(v.ActorId)
			out.SetItems(append(out.GetItems(), app.OperatorAccessRow_builder{
				At:       timestamppb.New(v.DateCreated),
				Tenant:   alias(v.TenantId),
				Kind:     v.Kind,
				Result:   v.Result,
				Action:   v.Action,
				Actor:    actor.Bytes(),
				Operator: s.a.operatorNamed(actor),
				Address:  d.GetAddress(),
				Subjects: v.SubjectCount,
				Reason:   d.GetReason(),
			}.Build()))
			if !operator.Is(actor) {
				seen = append(seen, access.Subject{Id: actor, Tenant: pdid.Id(v.ActorTenantId)})
			}
			if len(out.GetItems()) == size {
				break
			}
		}
		if len(vs) < 500 {
			break
		}
		// After the last row, as the pair compared once: an index starts
		// the read at it (payday#154).
		last := vs[len(vs)-1]
		cursor = []predicate.Access{func(s *sql.Selector) {
			s.Where(sql.CompositeLT([]string{s.C(entaccess.FieldDateCreated), s.C(entaccess.FieldId)}, last.DateCreated, last.Id))
		}}
	}
	access.Saw(ctx, seen...)
	return out, nil
}

// carries is whether a call carried the data of the person `id` names.
func carries(v *ent.Access, id []byte) bool {
	for _, s := range v.Subjects {
		if string(s) == string(id) {
			return true
		}
	}
	return false
}

// reviews is the entity's `AccessReviewService` at the entry point, of which
// it serves List: the reviews of their record a workspace's administrators
// recorded, of every workspace, as the wall would not let any one of them.
type reviews struct {
	app.UnimplementedAccessReviewServiceServer
	a Routes
}

func (s reviews) List(ctx context.Context, req *app.AccessReviewListRequest) (*app.AccessReviewListResponse, error) {
	return s.a.S.Base.AccessReview().List(ctx, req)
}
