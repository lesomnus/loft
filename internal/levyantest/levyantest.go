// Package levyantest is a levyan for a test to bill to, served in this
// process on the loopback, which keeps what it is handed and answers as a test
// tells it:
//
//   - its MeteringService, every reading kept by default;
//   - its `payday.GrantService`, the grants a test gives, ends and changes,
//     streamed to whoever watches and listed to whoever asks, as levyan's own
//     does (levyan's design 13);
//   - its WorkspaceService's Arrive and Leave, every call kept in the order it
//     came, taken unless a test says levyan is down;
//   - and its Close, Restore, Destroy and GetLifecycle: what became of each
//     workspace, read in the order of its moments and answered and refused as
//     levyan's own are (levyan's `docs/apps.md`, 5) -- a workspace it heard
//     nothing of is NotFound, a moment before it arrived or ahead of now is
//     InvalidArgument, and another thing said of a moment, or a close or a
//     restore of a workspace destroyed, is FailedPrecondition. A reading
//     measured once a workspace was destroyed is refused.
//
// It holds a call to what levyan holds one to: the product key it was given,
// as `authorization: Bearer lvk_…`, or Unauthenticated.
package levyantest

import (
	"context"
	"crypto/rand"
	"net"
	"slices"
	"strconv"
	"sync"
	"testing"
	"time"

	levyanv1 "github.com/lesomnus/levyan/api/levyan/v1"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/lesomnus/payday/pdid"
	"github.com/lesomnus/payday/pdpb"

	"github.com/lesomnus/loft/server/levyan"
)

// Key is the product key the fake is called with.
const Key = "lvk_levyantest-not-a-key-anybody-issued"

// Levyan is the fake.
type Levyan struct {
	levyanv1.UnimplementedMeteringServiceServer
	levyanv1.UnimplementedWorkspaceServiceServer
	pdpb.UnimplementedGrantServiceServer

	addr string

	mu      sync.Mutex
	calls   [][]*levyanv1.Reading
	answer  func(ctx context.Context, req *levyanv1.RecordRequest) (*levyanv1.RecordResponse, error)
	refused map[string]levyanv1.Refusal

	// The grants, in the order they were first given, and who watches them:
	// each stream's channel, and what ends it.
	grants   []*pdpb.EntitlementGrant
	watchers map[chan *pdpb.EntitlementGrant]chan struct{}
	lists    int

	// What it was told of who pays, and whether it takes it: from nobody
	// while it is down, and of a workspace it fails.
	told    []Told
	down    error
	failing map[string]error

	// What it knows of each workspace's life (lifecycle.go).
	lives lives
}

// Told is one call of WorkspaceService that came with the key: `arrive` or
// `leave`, of a workspace and an account, and when an arrival says it was
// made; or `close`, `restore` or `destroy`, of a workspace, and the moment it
// says. A call levyan was down for, or refused, is not kept.
type Told struct {
	Kind      string
	Workspace string
	Account   string
	Created   time.Time
	At        time.Time
}

// Start serves one for the length of the test.
func Start(t testing.TB) *Levyan {
	t.Helper()

	l, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)

	v := &Levyan{
		addr:     l.Addr().String(),
		refused:  map[string]levyanv1.Refusal{},
		watchers: map[chan *pdpb.EntitlementGrant]chan struct{}{},
		failing:  map[string]error{},
	}
	g := grpc.NewServer()
	levyanv1.RegisterMeteringServiceServer(g, v)
	levyanv1.RegisterWorkspaceServiceServer(g, v)
	pdpb.RegisterGrantServiceServer(g, v)
	go g.Serve(l)
	t.Cleanup(g.Stop)

	return v
}

// Gone is the configuration of a levyan that is not there: an address on the
// loopback that nothing listens on, which refuses every call.
func Gone(t testing.TB) levyan.Config {
	t.Helper()

	l, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	addr := l.Addr().String()
	require.NoError(t, l.Close())

	return levyan.Config{Addr: addr, Key: Key, Insecure: true}
}

// Config is this deployment's configuration of the fake: on the loopback, in
// plaintext, with its key.
func (v *Levyan) Config() levyan.Config {
	return levyan.Config{Addr: v.addr, Key: Key, Insecure: true}
}

// Answer has every call answered by f from now on; nil is the fake's own
// answer again.
func (v *Levyan) Answer(f func(ctx context.Context, req *levyanv1.RecordRequest) (*levyanv1.RecordResponse, error)) {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.answer = f
}

// Refuse has every reading of `meter` refused, as levyan refuses one whose
// meter its price book does not have.
func (v *Levyan) Refuse(meter string, why levyanv1.Refusal) {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.refused[meter] = why
}

// Calls is the readings of every call that came with the key, one call to an
// element, answered or not.
func (v *Levyan) Calls() [][]*levyanv1.Reading {
	v.mu.Lock()
	defer v.mu.Unlock()
	return slices.Clone(v.calls)
}

// Readings is every reading of every call, in the order they came.
func (v *Levyan) Readings() []*levyanv1.Reading {
	out := []*levyanv1.Reading{}
	for _, c := range v.Calls() {
		out = append(out, c...)
	}
	return out
}

// keyed refuses a call that did not come with the key.
func keyed(ctx context.Context) error {
	md, _ := metadata.FromIncomingContext(ctx)
	if got := md.Get("authorization"); len(got) != 1 || got[0] != "Bearer "+Key {
		return status.Error(codes.Unauthenticated, "levyantest: not the key")
	}
	return nil
}

func (v *Levyan) Record(ctx context.Context, req *levyanv1.RecordRequest) (*levyanv1.RecordResponse, error) {
	if err := keyed(ctx); err != nil {
		return nil, err
	}

	v.mu.Lock()
	v.calls = append(v.calls, req.GetReadings())
	answer := v.answer
	refused := map[string]levyanv1.Refusal{}
	for k, r := range v.refused {
		refused[k] = r
	}
	// What levyan keeps of a reading, unless a test answers for it.
	var destroyed map[int]bool
	if answer == nil {
		destroyed = v.lives.read(req.GetReadings())
	}
	v.mu.Unlock()

	if answer != nil {
		return answer(ctx, req)
	}

	out := []*levyanv1.Recorded{}
	for i, r := range req.GetReadings() {
		if destroyed[i] {
			out = append(out, levyanv1.Recorded_builder{
				Outcome: levyanv1.Outcome_OUTCOME_REFUSED,
				Refusal: levyanv1.Refusal_REFUSAL_DESTROYED,
				Why:     "levyantest: measured once the workspace was destroyed",
			}.Build())
			continue
		}
		if why, ok := refused[r.GetMeter()]; ok {
			out = append(out, levyanv1.Recorded_builder{
				Outcome: levyanv1.Outcome_OUTCOME_REFUSED,
				Refusal: why,
				Why:     "levyantest: " + r.GetMeter() + " is refused",
			}.Build())
			continue
		}
		out = append(out, levyanv1.Recorded_builder{Outcome: levyanv1.Outcome_OUTCOME_KEPT}.Build())
	}

	return levyanv1.RecordResponse_builder{Results: out}.Build(), nil
}

// Grant gives `tenant` a switch of `key`, on, until `until`: a grant of its
// own, at version 1, from always. It answers the grant's identifier.
func (v *Levyan) Grant(tenant pdid.Id, key string, until time.Time) []byte {
	id := make([]byte, 16)
	rand.Read(id)
	v.Put(pdpb.EntitlementGrant_builder{
		Id:         id,
		TenantId:   tenant.Bytes(),
		Key:        key,
		Version:    1,
		On:         proto.Bool(true),
		ValidUntil: timestamppb.New(until),
	}.Build())
	return id
}

// Restrict gives `tenant` `loft.restricted`, on, for a week: what levyan gives
// a workspace nobody has paid for since its grace ran out.
func (v *Levyan) Restrict(tenant pdid.Id) []byte {
	return v.Grant(tenant, "loft.restricted", time.Now().Add(7*24*time.Hour))
}

// End ends grant `id` now, at its next version, as levyan takes one away.
func (v *Levyan) End(id []byte) {
	g := v.Of(id)
	if g == nil {
		return
	}
	g.SetVersion(g.GetVersion() + 1)
	g.SetValidUntil(timestamppb.Now())
	v.Put(g)
}

// Of is grant `id` as it is now, or nil.
func (v *Levyan) Of(id []byte) *pdpb.EntitlementGrant {
	v.mu.Lock()
	defer v.mu.Unlock()
	for _, g := range v.grants {
		if string(g.GetId()) == string(id) {
			return proto.CloneOf(g)
		}
	}
	return nil
}

// Put has `g` be the grant of its identifier, whatever version it says --
// a test of one heard out of order says an older one -- and tells everybody
// watching.
func (v *Levyan) Put(g *pdpb.EntitlementGrant) {
	g = proto.CloneOf(g)

	v.mu.Lock()
	i := slices.IndexFunc(v.grants, func(o *pdpb.EntitlementGrant) bool { return string(o.GetId()) == string(g.GetId()) })
	if i < 0 {
		v.grants = append(v.grants, g)
	} else {
		v.grants[i] = g
	}
	ws := make([]chan *pdpb.EntitlementGrant, 0, len(v.watchers))
	for ch := range v.watchers {
		ws = append(ws, ch)
	}
	v.mu.Unlock()

	for _, ch := range ws {
		select {
		case ch <- proto.CloneOf(g):
		default:
			// A watcher this far behind is one levyan would cut.
		}
	}
}

// Quietly has `g` be the grant of its identifier and tells nobody: a change
// a stream did not carry, which only a list finds.
func (v *Levyan) Quietly(g *pdpb.EntitlementGrant) {
	g = proto.CloneOf(g)

	v.mu.Lock()
	defer v.mu.Unlock()
	i := slices.IndexFunc(v.grants, func(o *pdpb.EntitlementGrant) bool { return string(o.GetId()) == string(g.GetId()) })
	if i < 0 {
		v.grants = append(v.grants, g)
	} else {
		v.grants[i] = g
	}
}

// Cut ends every stream open now, as levyan going down ends them.
func (v *Levyan) Cut() {
	v.mu.Lock()
	defer v.mu.Unlock()
	for ch, end := range v.watchers {
		close(end)
		delete(v.watchers, ch)
	}
}

// Watching is how many streams are open.
func (v *Levyan) Watching() int {
	v.mu.Lock()
	defer v.mu.Unlock()
	return len(v.watchers)
}

// Lists is how many pages of the grants it was asked for.
func (v *Levyan) Lists() int {
	v.mu.Lock()
	defer v.mu.Unlock()
	return v.lists
}

func (v *Levyan) Watch(req *pdpb.GrantWatchRequest, stream grpc.ServerStreamingServer[pdpb.GrantWatchResponse]) error {
	if err := keyed(stream.Context()); err != nil {
		return err
	}

	ch := make(chan *pdpb.EntitlementGrant, 64)
	end := make(chan struct{})
	v.mu.Lock()
	v.watchers[ch] = end
	v.mu.Unlock()
	defer func() {
		v.mu.Lock()
		delete(v.watchers, ch)
		v.mu.Unlock()
	}()

	if err := stream.Send(pdpb.GrantWatchResponse_builder{Ready: &pdpb.GrantWatchReady{}}.Build()); err != nil {
		return err
	}
	for {
		select {
		case g := <-ch:
			if err := stream.Send(pdpb.GrantWatchResponse_builder{Grant: g}.Build()); err != nil {
				return err
			}
		case <-end:
			return status.Error(codes.Unavailable, "levyantest: the stream was cut")
		case <-stream.Context().Done():
			return nil
		}
	}
}

func (v *Levyan) List(ctx context.Context, req *pdpb.GrantListRequest) (*pdpb.GrantListResponse, error) {
	if err := keyed(ctx); err != nil {
		return nil, err
	}

	v.mu.Lock()
	defer v.mu.Unlock()
	v.lists++

	vs := []*pdpb.EntitlementGrant{}
	for _, g := range v.grants {
		if len(req.GetTenantId()) > 0 && string(g.GetTenantId()) != string(req.GetTenantId()) {
			continue
		}
		vs = append(vs, g)
	}
	from := 0
	if c := req.GetCursor(); c != "" {
		n, err := strconv.Atoi(c)
		if err != nil {
			return nil, status.Error(codes.InvalidArgument, "levyantest: not a cursor")
		}
		from = n
	}
	size := int(req.GetSize())
	if size <= 0 {
		size = 100
	}
	to := min(from+size, len(vs))
	out := pdpb.GrantListResponse_builder{}.Build()
	for _, g := range vs[min(from, len(vs)):to] {
		out.SetItems(append(out.GetItems(), proto.CloneOf(g)))
	}
	if to < len(vs) {
		out.SetNext(strconv.Itoa(to))
	}
	return out, nil
}

// Down has every WorkspaceService call answered with `err` from now on, as a
// levyan that is not there; nil takes them again.
func (v *Levyan) Down(err error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.down = err
}

// Fail has every WorkspaceService call about `workspace` answered with `err`
// from now on, and the others taken; nil takes them again.
func (v *Levyan) Fail(workspace pdid.Id, err error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	if err == nil {
		delete(v.failing, workspace.String())
		return
	}
	v.failing[workspace.String()] = err
}

// Heard is every WorkspaceService call it took, in the order they came.
func (v *Levyan) Heard() []Told {
	v.mu.Lock()
	defer v.mu.Unlock()
	return slices.Clone(v.told)
}

func (v *Levyan) hear(ctx context.Context, t Told) error {
	if err := keyed(ctx); err != nil {
		return err
	}

	v.mu.Lock()
	defer v.mu.Unlock()
	if v.down != nil {
		return v.down
	}
	if err := v.failing[t.Workspace]; err != nil {
		return err
	}
	if _, err := pdid.Parse(t.Workspace); err != nil {
		return status.Errorf(codes.InvalidArgument, "levyantest: %q is not a workspace", t.Workspace)
	}
	if _, err := pdid.Parse(t.Account); err != nil {
		return status.Errorf(codes.InvalidArgument, "levyantest: %q is not an account", t.Account)
	}
	v.told = append(v.told, t)
	return nil
}

func (v *Levyan) Arrive(ctx context.Context, req *levyanv1.ArriveRequest) (*levyanv1.ArriveResponse, error) {
	t := Told{Kind: "arrive", Workspace: req.GetWorkspace(), Account: req.GetCreator()}
	if req.HasCreated() {
		t.Created = req.GetCreated().AsTime()
	}
	if err := v.hear(ctx, t); err != nil {
		return nil, err
	}
	v.arrived(t)
	return &levyanv1.ArriveResponse{}, nil
}

func (v *Levyan) Leave(ctx context.Context, req *levyanv1.LeaveRequest) (*levyanv1.LeaveResponse, error) {
	if err := v.hear(ctx, Told{Kind: "leave", Workspace: req.GetWorkspace(), Account: req.GetAccount()}); err != nil {
		return nil, err
	}
	return &levyanv1.LeaveResponse{}, nil
}
