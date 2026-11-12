package admin_test

import (
	"context"
	"strings"
	"testing"
	"time"
	"uuid"

	"github.com/lesomnus/payday/auth/authsession"
	"github.com/lesomnus/payday/frame"
	"github.com/lesomnus/payday/pdid"
	"github.com/lesomnus/payday/pdtest"
	"github.com/lesomnus/z"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"

	app "github.com/lesomnus/loft"
	entaccess "github.com/lesomnus/loft/internal/ent/access"
	"github.com/lesomnus/loft/internal/ent/holder"
	"github.com/lesomnus/loft/server/operator"
)

// TestTheDeploymentsAccessRecordIsTheOperatorsToRead: the access record of
// every workspace, for an operator -- who made each call, by identifier and an
// operator's by name too, where from, and how many people's data it carried --
// narrowed by workspace, actor, person, kind and stretch, the newest first.
// Reading it is on the record itself, of the actors whose addresses it
// showed and not of the operator's own; and the reviews the workspaces'
// administrators recorded are read through the entity's own List, every
// workspace's.
func TestTheDeploymentsAccessRecordIsTheOperatorsToRead(t *testing.T) {
	e := open(t)
	x := e.x
	ctx := as(key)
	bg := context.Background()
	alice := operator.Id("alice")
	acme := uuid.UUID(e.acme.GetId())
	owner, err := e.s.Ent.Holder.Query().Where(holder.TenantId(acme), holder.Alias("owner")).Only(bg)
	x.NoError(err)
	kim, err := e.s.Ent.Holder.Query().Where(holder.TenantId(acme), holder.Alias("kim")).Only(bg)
	x.NoError(err)

	// Who is in acme, read by its owner from inside and by the operator at
	// the entry point.
	_, cookie, err := e.s.Sessions.Mint(bg, authsession.Session{Id: pdid.Id(owner.Id).String(), Grant: frame.Whole()})
	x.NoError(err)
	g, err := e.s.Grpc(t.Context(), e.c)
	x.NoError(err)
	public := pdtest.Serve(t, g)
	asOwner := metadata.AppendToOutgoingContext(bg, "cookie", cookie.Name+"="+cookie.Value)
	_, err = app.NewHolderServiceClient(public).List(asOwner, app.HolderListRequest_builder{}.Build())
	x.NoError(err)
	x.NoError(e.s.Access.Flush(bg), "the public server writes its rows a batch at a time")
	_, err = app.NewOperatorTenantServiceClient(e.conn).Show(ctx, app.OperatorTenantShowRequest_builder{
		Ref: app.OperatorTenantRef_builder{Alias: z.Ptr("acme")}.Build(),
	}.Build())
	x.NoError(err)

	records := app.NewOperatorAccessServiceClient(e.conn)
	list := func(req *app.OperatorAccessListRequest) []*app.OperatorAccessRow {
		t.Helper()
		v, err := records.List(ctx, req)
		x.NoError(err)
		return v.GetItems()
	}
	rows := list(app.OperatorAccessListRequest_builder{}.Build())
	byOwner, byAlice := 0, 0
	for i, r := range rows {
		if i > 0 {
			x.False(r.GetDateCreated().AsTime().After(rows[i-1].GetDateCreated().AsTime()), "the newest first")
		}
		switch pdid.Id(uuid.UUID(r.GetActor())) {
		case pdid.Id(owner.Id):
			x.Empty(r.GetOperator())
			x.Equal("acme", r.GetTenant())
			byOwner++
		case alice:
			x.Equal("alice", r.GetOperator(), "an operator's row, by the name the entry point knows")
			x.True(strings.HasPrefix(r.GetAddress(), "admin:"), r.GetAddress())
			byAlice++
		}
	}
	x.Positive(byOwner, "the owner's read")
	x.Positive(byAlice, "the operator's read")

	for _, r := range list(app.OperatorAccessListRequest_builder{Actor: alice.Bytes()}.Build()) {
		x.Equal(alice.Bytes(), r.GetActor())
	}
	for _, r := range list(app.OperatorAccessListRequest_builder{Kind: "read"}.Build()) {
		x.Equal("read", r.GetKind())
	}
	x.Empty(list(app.OperatorAccessListRequest_builder{Kind: "download"}.Build()))
	x.NotEmpty(list(app.OperatorAccessListRequest_builder{Subject: pdid.Id(kim.Id).Bytes()}.Build()), "the reads of who is in acme carried kim")
	x.NotEmpty(list(app.OperatorAccessListRequest_builder{Tenant: app.OperatorTenantRef_builder{Alias: z.Ptr("acme")}.Build()}.Build()))
	x.Len(list(app.OperatorAccessListRequest_builder{Size: 1}.Build()), 1)
	later := time.Now().Add(time.Hour)
	x.Empty(list(app.OperatorAccessListRequest_builder{DateFrom: timestamppb.New(later), DateTo: timestamppb.New(later.Add(time.Hour))}.Build()))
	for _, req := range []*app.OperatorAccessListRequest{
		app.OperatorAccessListRequest_builder{Kind: "glance"}.Build(),
		app.OperatorAccessListRequest_builder{DateFrom: timestamppb.New(later), DateTo: timestamppb.New(time.Now())}.Build(),
		app.OperatorAccessListRequest_builder{Actor: []byte{1, 2, 3}}.Build(),
	} {
		_, err := records.List(ctx, req)
		x.Equal(codes.InvalidArgument, status.Code(err), "%v", req)
	}
	_, err = records.List(ctx, app.OperatorAccessListRequest_builder{Tenant: app.OperatorTenantRef_builder{Alias: z.Ptr("nowhere")}.Build()}.Build())
	x.Equal(codes.NotFound, status.Code(err))

	// The reading is on the record, of whose address it showed.
	read, err := e.s.Ent.Access.Query().Where(entaccess.Action(app.OperatorAccessService_List_FullMethodName)).All(bg)
	x.NoError(err)
	x.NotEmpty(read)
	saw := map[pdid.Id]bool{}
	for _, r := range read {
		x.Equal(alice.Uuid(), r.ActorId)
		x.Equal("read", r.Kind)
		for _, v := range r.Subjects {
			saw[pdid.Id(v)] = true
		}
	}
	x.True(saw[pdid.Id(owner.Id)], "the owner, whose address it showed")
	x.False(saw[alice], "the operator's own")

	// A review acme's owner recorded, read at the entry point -- on the
	// record too, as every read of what a review says is.
	asOwnerHere := frame.Into(bg, frame.New(pdid.Id(owner.Id), pdid.Id(acme), frame.Whole()).WithScope(frame.Only(pdid.Id(acme))))
	now := time.Now()
	_, err = e.s.Walled.AccessReview().Add(asOwnerHere, app.AccessReviewAddRequest_builder{
		DateStart: timestamppb.New(now.Add(-24 * time.Hour)),
		DateEnd:   timestamppb.New(now),
		Desc:      "이상 없음",
	}.Build())
	x.NoError(err)
	reviewed, err := app.NewAccessReviewServiceClient(e.conn).List(ctx, app.AccessReviewListRequest_builder{}.Build())
	x.NoError(err)
	x.Len(reviewed.GetItems(), 1)
	x.Equal(e.acme.GetId(), reviewed.GetItems()[0].GetTenant().GetId())
	x.Equal("이상 없음", reviewed.GetItems()[0].GetDesc())
	n, err := e.s.Ent.Access.Query().Where(
		entaccess.Action(app.AccessReviewService_List_FullMethodName),
		entaccess.ActorId(alice.Uuid()),
		entaccess.TenantId(acme),
	).Count(bg)
	x.NoError(err)
	x.Positive(n, "the review's words, read by the operator")
}
