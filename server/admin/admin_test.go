package admin_test

import (
	"context"
	"io"
	"os"
	"slices"
	"strings"
	"testing"
	"time"
	"uuid"

	"github.com/lesomnus/payday/auth/authsession"
	"github.com/lesomnus/payday/frame"
	"github.com/lesomnus/payday/pdid"
	"github.com/lesomnus/payday/pdpb"
	"github.com/lesomnus/payday/pdtest"
	"github.com/lesomnus/xli"
	"github.com/lesomnus/z"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"
	"google.golang.org/protobuf/types/dynamicpb"
	"google.golang.org/protobuf/types/known/timestamppb"

	app "github.com/lesomnus/loft"
	"github.com/lesomnus/loft/cli"
	"github.com/lesomnus/loft/cmd"
	entaccess "github.com/lesomnus/loft/internal/ent/access"
	"github.com/lesomnus/loft/internal/ent/aliashold"
	entaudit "github.com/lesomnus/loft/internal/ent/audit"
	"github.com/lesomnus/loft/internal/ent/event"
	"github.com/lesomnus/loft/internal/ent/holder"
	"github.com/lesomnus/loft/internal/ent/tenant"
	"github.com/lesomnus/loft/server/admin"
	"github.com/lesomnus/loft/server/operator"
)

const key = "an-operators-key-long-enough-to-be-one"

// run runs a command, keeping what it prints out of the test's output.
func run(t *testing.T, k *xli.Command, args ...string) error {
	t.Helper()
	r, w, err := os.Pipe()
	require.NoError(t, err)
	was := os.Stdout
	os.Stdout = w
	err = k.Run(t.Context(), args)
	os.Stdout = was
	require.NoError(t, w.Close())
	_, _ = io.ReadAll(r)
	return err
}

type entry struct {
	x    *require.Assertions
	c    cmd.Config
	s    *cmd.Server
	conn *grpc.ClientConn
	acme *app.Tenant
}

// open is a deployment with a workspace, its owner and a member, and its
// entry point, which alice holds a key to.
func open(t *testing.T) *entry {
	t.Helper()
	x := require.New(t)
	c := cmd.Config{}
	c.Db.Driver, c.Db.Dsn = pdtest.DB(t)
	c.Watch.Broker = "memory"
	c.App.Files = t.TempDir()
	c.Auth.Roster.Db.Driver, c.Auth.Roster.Db.Dsn = pdtest.DB(t)
	x.NoError(run(t, cli.NewCmdInit(&c), "--tenant", "acme", "--login", "owner", "--password", "owner1234"))
	x.NoError(run(t, cli.NewCmdHolder(&c), "add", "--tenant", "acme", "--login", "kim", "--role", "member", "--password", "kim-password"))

	s, err := cmd.Build(t.Context(), c)
	x.NoError(err)
	t.Cleanup(func() { s.Close() })
	ops, err := admin.Operators(map[string]string{"alice": key})
	x.NoError(err)
	g, err := admin.Routes{S: s, C: c, Operators: ops}.Grpc(t.Context())
	x.NoError(err)
	acme, err := s.Base.Tenant().Get(t.Context(), app.TenantGetRequest_builder{Ref: app.TenantRef_builder{Alias: z.Ptr("acme")}.Build()}.Build())
	x.NoError(err)
	return &entry{x: x, c: c, s: s, conn: pdtest.Serve(t, g), acme: acme}
}

// as is a call carrying `key` as an operator's.
func as(key string) context.Context {
	return metadata.AppendToOutgoingContext(context.Background(), "authorization", "Bearer "+key)
}

// call makes a call of `method` with an empty request, whatever its shape,
// and answers how it ended.
func call(ctx context.Context, conn *grpc.ClientConn, method string) error {
	name := protoreflect.FullName(strings.ReplaceAll(strings.TrimPrefix(method, "/"), "/", "."))
	d, err := protoregistry.GlobalFiles.FindDescriptorByName(name)
	if err != nil {
		return err
	}
	md := d.(protoreflect.MethodDescriptor)
	in := dynamicpb.NewMessage(md.Input())
	out := dynamicpb.NewMessage(md.Output())
	if !md.IsStreamingClient() && !md.IsStreamingServer() {
		return conn.Invoke(ctx, method, in, out)
	}
	ss, err := conn.NewStream(ctx, &grpc.StreamDesc{ClientStreams: md.IsStreamingClient(), ServerStreams: md.IsStreamingServer()}, method)
	if err != nil {
		return err
	}
	if err := ss.SendMsg(in); err != nil {
		return err
	}
	if err := ss.CloseSend(); err != nil {
		return err
	}
	for {
		if err := ss.RecvMsg(out); err != nil {
			if err == io.EOF {
				return nil
			}
			return err
		}
	}
}

// TestEveryMethodAnswersAnOperatorsKeyAndNothingElse: every method the entry
// point serves refuses a call with no key, or with a key nobody holds, before
// anything is read; with one, it is served -- whatever it then says of a
// request that asks for nothing. And a method the table does not have is
// refused to a key too, whatever is registered.
func TestEveryMethodAnswersAnOperatorsKeyAndNothingElse(t *testing.T) {
	e := open(t)
	x := e.x

	methods := slices.Sorted(func(yield func(string) bool) {
		for m := range admin.Table {
			if !yield(m) {
				return
			}
		}
	})
	x.NotEmpty(methods)
	for _, m := range methods {
		x.Equal(codes.Unauthenticated, status.Code(call(context.Background(), e.conn, m)), "%s with no key", m)
		x.Equal(codes.Unauthenticated, status.Code(call(as("somebody-elses-key-of-a-good-length-too"), e.conn, m)), "%s with a key nobody holds", m)
		code := status.Code(call(as(key), e.conn, m))
		x.NotEqual(codes.Unauthenticated, code, "%s with alice's key", m)
		x.NotEqual(codes.PermissionDenied, code, "%s with alice's key", m)
		x.NotEqual(codes.Unimplemented, code, "%s is in the table and served", m)
	}

	for _, m := range []string{
		app.TenantService_Configure_FullMethodName,
		app.TenantService_Patch_FullMethodName,
		app.TenantService_Erase_FullMethodName,
		app.TenantService_Mine_FullMethodName,
		app.HolderService_Patch_FullMethodName,
	} {
		x.Equal(codes.PermissionDenied, status.Code(call(as(key), e.conn, m)), "%s is no operator's", m)
		x.Equal(codes.Unauthenticated, status.Code(call(context.Background(), e.conn, m)), "%s with no key", m)
	}
}

// TestTheTableIsWhatIsServed: every method of the operator's own services is
// in the table, and every method in the table is one the entry point
// registers -- a method the table names that nothing serves would be a door
// that answers Unimplemented, and one of the operator's own the table leaves
// out a method nobody can call.
func TestTheTableIsWhatIsServed(t *testing.T) {
	e := open(t)
	x := e.x
	g, err := admin.Routes{S: e.s, C: e.c}.Grpc(t.Context())
	x.NoError(err)

	served := map[string]bool{}
	for name, info := range g.GetServiceInfo() {
		for _, m := range info.Methods {
			method := "/" + name + "/" + m.Name
			served[method] = true
			if strings.HasPrefix(name, "loft.Operator") {
				x.Contains(admin.Table, method, "%s is the operator's and not in the table", method)
			}
		}
	}
	for m := range admin.Table {
		x.True(served[m], "%s is in the table and not served", m)
	}
}

// TestThePublicServerServesNoneOfTheOperators: the public server registers no
// service of the operator's, and an operator's key there is nobody's.
func TestThePublicServerServesNoneOfTheOperators(t *testing.T) {
	e := open(t)
	x := e.x

	g, err := e.s.Grpc(t.Context(), e.c)
	x.NoError(err)
	for name := range g.GetServiceInfo() {
		x.False(strings.HasPrefix(name, "loft.Operator"), "%s is served by the public server", name)
	}
	public := pdtest.Serve(t, g)
	for m, k := range admin.Table {
		if !strings.HasPrefix(m, "/loft.Operator") {
			continue
		}
		code := status.Code(call(as(key), public, m))
		x.Contains([]codes.Code{codes.Unimplemented, codes.Unauthenticated}, code, "%s (%v) on the public server", m, k)
	}
	_, err = app.NewTenantServiceClient(public).List(as(key), app.TenantListRequest_builder{}.Build())
	x.Equal(codes.Unauthenticated, status.Code(err), "an operator's key is nobody's on the public server")
}

// TestWhatAnOperatorDoesIsRecordedAsTheirs: the operator's reads and writes
// are the deployment's own work at their word, and the past, the trail and the
// access record name the operator who did them -- by the identifier their
// name makes, held by no workspace for a read and in the workspace for a
// write -- and the workspace's own review calls them the operator, by no name.
func TestWhatAnOperatorDoesIsRecordedAsTheirs(t *testing.T) {
	e := open(t)
	x := e.x
	ctx := as(key)
	bg := context.Background()
	alice := operator.Id("alice")
	acme := uuid.UUID(e.acme.GetId())

	// A read of who is in it.
	shown, err := app.NewOperatorTenantServiceClient(e.conn).Show(ctx, app.OperatorTenantShowRequest_builder{
		Ref: app.OperatorTenantRef_builder{Alias: z.Ptr("acme")}.Build(),
	}.Build())
	x.NoError(err)
	x.Len(shown.GetMembers(), 2)
	read, err := e.s.Ent.Access.Query().Where(entaccess.Action(app.OperatorTenantService_Show_FullMethodName)).Only(bg)
	x.NoError(err, "a read is on the record before it is answered")
	x.Equal("read", read.Kind)
	x.Equal(alice.Uuid(), read.ActorId)
	x.Equal(uuid.UUID{}, read.ActorTenantId, "a read the operator made, held by no workspace")
	x.Equal(acme, read.TenantId, "filed under the workspace whose people it showed")
	x.Equal(uint32(2), read.SubjectCount)
	d := &pdpb.AccessDetail{}
	x.NoError(proto.Unmarshal(read.Value, d))
	x.True(strings.HasPrefix(d.GetAddress(), "admin:"), d.GetAddress())

	// And through the entity's own read: filed under the workspace too.
	listed, err := app.NewHolderServiceClient(e.conn).List(ctx, app.HolderListRequest_builder{
		Filters: []*app.HolderFilter{app.HolderFilter_builder{Tenant: app.TenantRef_builder{Alias: z.Ptr("acme")}.Build()}.Build()},
	}.Build())
	x.NoError(err)
	x.Len(listed.GetItems(), 2)
	read, err = e.s.Ent.Access.Query().Where(entaccess.Action(app.HolderService_List_FullMethodName)).Only(bg)
	x.NoError(err)
	x.Equal(alice.Uuid(), read.ActorId)
	x.Equal(acme, read.TenantId, "filed under the workspace whose people it listed")

	// A write of a membership: the event, the trail and the access record.
	kim, err := e.s.Ent.Holder.Query().Where(holder.TenantId(acme), holder.Alias("kim")).Only(bg)
	x.NoError(err)
	_, err = app.NewOperatorTenantServiceClient(e.conn).NameOwner(ctx, app.OperatorTenantNameOwnerRequest_builder{
		Ref:    app.OperatorTenantRef_builder{Alias: z.Ptr("acme")}.Build(),
		Member: "@kim",
	}.Build())
	x.NoError(err)
	ev, err := e.s.Ent.Event.Query().Where(event.TenantId(acme), event.Kind("party.role")).Only(bg)
	x.NoError(err)
	x.NotNil(ev.ActorId)
	x.Equal(alice.Uuid(), *ev.ActorId, "the operator did it")
	trail, err := e.s.Ent.Audit.Query().Where(entaudit.ObjectId(kim.Id)).Order(entaudit.ByDateCreated()).All(bg)
	x.NoError(err)
	x.NotEmpty(trail)
	last := trail[len(trail)-1]
	x.Equal(alice.Uuid(), last.ActorId)
	x.Equal(acme, last.ActorTenantId, "the deployment's work in the workspace")
	wrote, err := e.s.Ent.Access.Query().Where(entaccess.Kind("write"), entaccess.ActorId(alice.Uuid())).All(bg)
	x.NoError(err)
	x.NotEmpty(wrote, "the membership's write names the operator")

	// A contract and a hold, written past the domain layer.
	c, err := app.NewTenantContractServiceClient(e.conn).Add(ctx, app.TenantContractAddRequest_builder{
		Tenant: app.TenantRef_builder{Alias: z.Ptr("acme")}.Build(),
		Name:   "pilot",
	}.Build())
	x.NoError(err)
	x.True(c.HasDateEffective(), "now, when it says none")
	h, err := app.NewLegalHoldServiceClient(e.conn).Add(ctx, app.LegalHoldAddRequest_builder{
		Tenant: app.TenantRef_builder{Alias: z.Ptr("acme")}.Build(),
		Name:   "a case",
	}.Build())
	x.NoError(err)
	for _, id := range [][]byte{c.GetId(), h.GetId()} {
		row, err := e.s.Ent.Audit.Query().Where(entaudit.ObjectId(uuid.UUID(id))).Only(bg)
		x.NoError(err)
		x.Equal(alice.Uuid(), row.ActorId)
		x.Equal(uuid.UUID{}, row.ActorTenantId, "the operator, held by no workspace")
		x.Equal(acme, row.TenantId, "filed under the workspace it is of")
	}

	// Closed, by the operator who typed its address.
	_, err = app.NewTenantServiceClient(e.conn).Close(ctx, app.TenantCloseRequest_builder{
		Ref:     app.TenantRef_builder{Alias: z.Ptr("acme")}.Build(),
		Confirm: "ACME",
	}.Build())
	x.Equal(codes.InvalidArgument, status.Code(err), "its name is the owner's to type; the operator types its address")
	closed, err := app.NewTenantServiceClient(e.conn).Close(ctx, app.TenantCloseRequest_builder{
		Ref:     app.TenantRef_builder{Alias: z.Ptr("acme")}.Build(),
		Confirm: "@acme",
		Reason:  "asked",
	}.Build())
	x.NoError(err)
	x.Equal("operator", closed.GetClosedBy())
	ev, err = e.s.Ent.Event.Query().Where(event.TenantId(acme), event.Kind("workspace.close")).Only(bg)
	x.NoError(err)
	x.Equal(alice.Uuid(), *ev.ActorId)
	_, err = app.NewTenantServiceClient(e.conn).Restore(ctx, app.TenantRestoreRequest_builder{
		Ref: app.TenantRef_builder{Alias: z.Ptr("acme")}.Build(),
	}.Build())
	x.NoError(err)

	// The workspace's own review of its record calls them the operator.
	owner, err := e.s.Ent.Holder.Query().Where(holder.TenantId(acme), holder.Alias("owner")).Only(bg)
	x.NoError(err)
	_, cookie, err := e.s.Sessions.Mint(bg, authsession.Session{Id: pdid.Id(*owner.Account).String(), Grant: frame.Whole()})
	x.NoError(err)
	g, err := e.s.Grpc(t.Context(), e.c)
	x.NoError(err)
	public := pdtest.Serve(t, g)
	asOwner := metadata.AppendToOutgoingContext(bg, "cookie", cookie.Name+"="+cookie.Value, cmd.WorkspaceHeader, "acme")
	now := time.Now()
	review, err := app.NewAccessServiceClient(public).Review(asOwner, app.AccessReviewRequest_builder{
		From: timestamppb.New(now.Add(-time.Hour)),
		To:   timestamppb.New(now.Add(time.Hour)),
	}.Build())
	x.NoError(err)
	named := 0
	for _, v := range review.GetItems() {
		if pdid.Id(uuid.UUID(v.GetActor())) == alice {
			x.Equal("운영자", v.GetActorName())
			named++
		}
	}
	x.Positive(named, "the operator's rows are in the workspace's record")
}

// TestAnOperatorMovesAnAddressAndNamesNoOtherThing: a move says what it would
// do and does nothing until it is asked to, the old address held -- and its
// write is the operator's; a hold is lifted by when, and nothing else of it
// is written.
func TestAnOperatorMovesAnAddressAndNamesNoOtherThing(t *testing.T) {
	e := open(t)
	x := e.x
	ctx := as(key)
	bg := context.Background()
	ops := app.NewOperatorTenantServiceClient(e.conn)
	ref := app.OperatorTenantRef_builder{Alias: z.Ptr("acme")}.Build()

	trail, err := e.s.Ent.Audit.Query().Count(bg)
	x.NoError(err)
	v, err := ops.Move(ctx, app.OperatorTenantMoveRequest_builder{Ref: ref, To: "acme-two", DryRun: true}.Build())
	x.NoError(err)
	x.Equal("acme", v.GetFrom())
	x.Equal("acme", v.GetTenant().GetAlias(), "a dry run moves nothing")
	after, err := e.s.Ent.Audit.Query().Count(bg)
	x.NoError(err)
	x.Equal(trail, after, "and writes nothing")
	kept, err := e.s.Ent.AliasHold.Query().Count(bg)
	x.NoError(err)
	x.Zero(kept, "and holds nothing")
	_, err = ops.Move(ctx, app.OperatorTenantMoveRequest_builder{Ref: ref, To: "www", DryRun: true}.Build())
	x.Equal(codes.FailedPrecondition, status.Code(err), "refused as the move would be: %v", err)

	v, err = ops.Move(ctx, app.OperatorTenantMoveRequest_builder{Ref: ref, To: "@acme-two"}.Build())
	x.NoError(err)
	x.Equal("acme-two", v.GetTenant().GetAlias())
	x.True(v.GetHeldUntil().AsTime().After(time.Now()))
	moved, err := e.s.Ent.Tenant.Query().Where(tenant.Alias("acme-two")).Only(bg)
	x.NoError(err)
	rows, err := e.s.Ent.Audit.Query().Where(entaudit.ObjectId(moved.Id), entaudit.ActorId(operator.Id("alice").Uuid())).Count(bg)
	x.NoError(err)
	x.Positive(rows, "the move is on the trail as the operator's")
	// And the old address, held: a row behind no wall, the deployment's, which
	// outlives the workspace it sends people on to.
	address, err := e.s.Ent.AliasHold.Query().Where(aliashold.Held("acme")).Only(bg)
	x.NoError(err)
	held, err := e.s.Ent.Audit.Query().Where(entaudit.ObjectId(address.Id)).Only(bg)
	x.NoError(err)
	x.Equal(operator.Id("alice").Uuid(), held.ActorId)
	x.Equal(uuid.UUID{}, held.TenantId, "filed under no workspace, so that none takes it with it")
	_, err = ops.Move(ctx, app.OperatorTenantMoveRequest_builder{
		Ref: app.OperatorTenantRef_builder{Alias: z.Ptr("acme-two")}.Build(), To: "acme",
	}.Build())
	x.Equal(codes.FailedPrecondition, status.Code(err), "the old address is held, and not taken back by accident")

	holds := app.NewLegalHoldServiceClient(e.conn)
	h, err := holds.Add(ctx, app.LegalHoldAddRequest_builder{Tenant: app.TenantRef_builder{Alias: z.Ptr("acme-two")}.Build()}.Build())
	x.Equal(codes.InvalidArgument, status.Code(err), "a hold says what it is for")
	h, err = holds.Add(ctx, app.LegalHoldAddRequest_builder{Tenant: app.TenantRef_builder{Alias: z.Ptr("acme-two")}.Build(), Name: "a case"}.Build())
	x.NoError(err)
	hold := app.LegalHoldRef_builder{Id: h.GetId()}.Build()
	_, err = holds.Patch(ctx, app.LegalHoldPatchRequest_builder{Ref: hold, Name: z.Ptr("another"), DateLifted: timestamppb.Now()}.Build())
	x.Equal(codes.InvalidArgument, status.Code(err), "what it is for is not written again")
	_, err = holds.Patch(ctx, app.LegalHoldPatchRequest_builder{Ref: hold, DateLifted: timestamppb.New(time.Now().Add(time.Hour))}.Build())
	x.Equal(codes.InvalidArgument, status.Code(err), "lifted ahead of time")
	lifted, err := holds.Patch(ctx, app.LegalHoldPatchRequest_builder{Ref: hold, DateLifted: timestamppb.Now()}.Build())
	x.NoError(err)
	x.True(lifted.HasDateLifted())
	_, err = holds.Patch(ctx, app.LegalHoldPatchRequest_builder{Ref: hold, DateLifted: timestamppb.Now()}.Build())
	x.Equal(codes.FailedPrecondition, status.Code(err), "lifted already")
}
