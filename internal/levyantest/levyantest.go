// Package levyantest is a levyan for a test to bill to: its MeteringService,
// served in this process on the loopback, which keeps what it is handed and
// answers as a test tells it -- every reading kept, by default.
//
// It holds a call to what levyan holds one to: the product key it was given,
// as `authorization: Bearer lvk_…`, or Unauthenticated.
package levyantest

import (
	"context"
	"net"
	"slices"
	"sync"
	"testing"

	levyanv1 "github.com/lesomnus/levyan/api/levyan/v1"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	"github.com/lesomnus/loft/server/levyan"
)

// Key is the product key the fake is called with.
const Key = "lvk_levyantest-not-a-key-anybody-issued"

// Levyan is the fake.
type Levyan struct {
	levyanv1.UnimplementedMeteringServiceServer

	addr string

	mu      sync.Mutex
	calls   [][]*levyanv1.Reading
	answer  func(ctx context.Context, req *levyanv1.RecordRequest) (*levyanv1.RecordResponse, error)
	refused map[string]levyanv1.Refusal
}

// Start serves one for the length of the test.
func Start(t testing.TB) *Levyan {
	t.Helper()

	l, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)

	v := &Levyan{addr: l.Addr().String(), refused: map[string]levyanv1.Refusal{}}
	g := grpc.NewServer()
	levyanv1.RegisterMeteringServiceServer(g, v)
	go g.Serve(l)
	t.Cleanup(g.Stop)

	return v
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

func (v *Levyan) Record(ctx context.Context, req *levyanv1.RecordRequest) (*levyanv1.RecordResponse, error) {
	md, _ := metadata.FromIncomingContext(ctx)
	if got := md.Get("authorization"); len(got) != 1 || got[0] != "Bearer "+Key {
		return nil, status.Error(codes.Unauthenticated, "levyantest: not the key")
	}

	v.mu.Lock()
	v.calls = append(v.calls, req.GetReadings())
	answer := v.answer
	refused := map[string]levyanv1.Refusal{}
	for k, r := range v.refused {
		refused[k] = r
	}
	v.mu.Unlock()

	if answer != nil {
		return answer(ctx, req)
	}

	out := []*levyanv1.Recorded{}
	for _, r := range req.GetReadings() {
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
