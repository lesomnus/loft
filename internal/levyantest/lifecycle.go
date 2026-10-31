package levyantest

import (
	"context"
	"slices"
	"time"

	levyanv1 "github.com/lesomnus/levyan/api/levyan/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/lesomnus/payday/pdid"
)

// What became of the workspaces, as the fake was told it: levyan's
// `WorkspaceService` `Close`, `Restore`, `Destroy` and `GetLifecycle`
// (levyan#30), each of a moment, kept once of it, and read in the order of
// the moments and not of their telling.

// Ended is one thing the fake was told became of a workspace: `close`,
// `restore` or `destroy`, at a moment.
type Ended struct {
	Kind string
	At   time.Time
}

// lives is what the fake knows of each workspace's life, under the fake's
// lock: when it arrived, by the first arrival it was told or the first
// reading it was handed, which nothing may be said of a moment before; and
// what became of it.
type lives struct {
	arrived  map[string]time.Time
	measured map[string]time.Time
	ended    map[string][]Ended
	forgot   map[string]bool
}

func (l *lives) init() {
	if l.arrived == nil {
		l.arrived = map[string]time.Time{}
		l.measured = map[string]time.Time{}
		l.ended = map[string][]Ended{}
		l.forgot = map[string]bool{}
	}
}

// since is when `workspace` arrived, as levyan reads it: from its first
// arrival, or else its first reading; and whether it heard of it at all.
func (l *lives) since(workspace string) (time.Time, bool) {
	l.init()
	if l.forgot[workspace] {
		return time.Time{}, false
	}
	if at, ok := l.arrived[workspace]; ok {
		return at, true
	}
	at, ok := l.measured[workspace]
	return at, ok
}

// destroyed is when `workspace` was destroyed, and whether it was.
func (l *lives) destroyed(workspace string) (time.Time, bool) {
	l.init()
	var out time.Time
	for _, e := range l.ended[workspace] {
		if e.Kind == "destroy" && (out.IsZero() || e.At.Before(out)) {
			out = e.At
		}
	}
	return out, !out.IsZero()
}

// read keeps when each workspace of `rs` was first measured, and answers
// which of them were measured once it was destroyed: what levyan refuses.
func (l *lives) read(rs []*levyanv1.Reading) map[int]bool {
	l.init()
	out := map[int]bool{}
	for i, r := range rs {
		ws, at := r.GetWorkspace(), moment(r.GetMeasuredAt().AsTime())
		if d, ok := l.destroyed(ws); ok && !at.Before(d) {
			out[i] = true
			continue
		}
		if was, ok := l.measured[ws]; !ok || at.Before(was) {
			l.measured[ws] = at
		}
		delete(l.forgot, ws)
	}
	return out
}

func moment(v time.Time) time.Time { return v.UTC().Truncate(time.Microsecond) }

// arrived keeps that the workspace an arrival is of arrived, from when it was
// made -- or now, unset -- unless it was told so before: levyan keeps the
// first.
func (v *Levyan) arrived(t Told) {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.lives.init()
	delete(v.lives.forgot, t.Workspace)
	if _, ok := v.lives.arrived[t.Workspace]; ok {
		return
	}
	at := t.Created
	if at.IsZero() {
		at = time.Now()
	}
	v.lives.arrived[t.Workspace] = moment(at)
}

// Forget has the fake know nothing of `workspace` -- as a levyan made again,
// or one another product's key is for -- until it is told it arrived, or is
// handed a reading of it: what it is told became of it until then is
// NotFound.
func (v *Levyan) Forget(workspace pdid.Id) {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.lives.init()
	ws := workspace.String()
	delete(v.lives.arrived, ws)
	delete(v.lives.measured, ws)
	delete(v.lives.ended, ws)
	v.lives.forgot[ws] = true
}

// Ends is what the fake was told became of `workspace`, in the order of the
// moments.
func (v *Levyan) Ends(workspace pdid.Id) []Ended {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.lives.init()
	return slices.Clone(v.lives.ended[workspace.String()])
}

// life is what became of `workspace`, read in the order of its moments, as
// levyan's `Lifecycle` says it: since when it is closed, and when it was
// destroyed. A workspace destroyed while closed says the closure it was
// destroyed in.
func (l *lives) life(workspace string) *levyanv1.Lifecycle {
	out := levyanv1.Lifecycle_builder{Workspace: workspace}.Build()
	var closed time.Time
	for _, e := range l.ended[workspace] {
		switch e.Kind {
		case "close":
			if closed.IsZero() {
				closed = e.At
			}
		case "restore":
			closed = time.Time{}
		case "destroy":
			if !closed.IsZero() {
				out.SetClosedSince(timestamppb.New(closed))
			}
			out.SetDestroyed(timestamppb.New(e.At))
			return out
		}
	}
	if !closed.IsZero() {
		out.SetClosedSince(timestamppb.New(closed))
	}
	return out
}

// end is `kind` said of `workspace` at `at`, kept as levyan keeps it, or
// refused as levyan refuses it.
func (v *Levyan) end(ctx context.Context, kind, workspace string, has bool, at time.Time) (*levyanv1.Lifecycle, error) {
	if err := keyed(ctx); err != nil {
		return nil, err
	}

	v.mu.Lock()
	defer v.mu.Unlock()
	v.lives.init()
	if v.down != nil {
		return nil, v.down
	}
	if err := v.failing[workspace]; err != nil {
		return nil, err
	}
	if _, err := pdid.Parse(workspace); err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "levyantest: %q is not a workspace", workspace)
	}
	if !has || at.Unix() <= 0 {
		return nil, status.Error(codes.InvalidArgument, "levyantest: at: when it happened, which the product's own record of it says")
	}
	at = moment(at)
	if at.After(time.Now().Add(5 * time.Minute)) {
		return nil, status.Errorf(codes.InvalidArgument, "levyantest: at: %s is ahead of now", at.Format(time.RFC3339Nano))
	}
	since, ok := v.lives.since(workspace)
	if !ok {
		return nil, status.Errorf(codes.NotFound, "levyantest: workspace %s has not arrived; say it arrived first (Arrive)", workspace)
	}
	if at.Before(since) {
		return nil, status.Errorf(codes.InvalidArgument, "levyantest: at: %s is before workspace %s arrived, at %s", at.Format(time.RFC3339Nano), workspace, since.Format(time.RFC3339Nano))
	}

	es := v.lives.ended[workspace]
	keep := true
	if d, ok := v.lives.destroyed(workspace); ok && !at.Before(d) {
		if kind != "destroy" {
			return nil, status.Errorf(codes.FailedPrecondition, "levyantest: workspace %s was destroyed at %s, for good", workspace, d.Format(time.RFC3339Nano))
		}
		keep = false
	}
	for _, e := range es {
		if !e.At.Equal(at) {
			continue
		}
		if e.Kind != kind {
			return nil, status.Errorf(codes.FailedPrecondition, "levyantest: workspace %s was %s at %s, and is not %s then", workspace, e.Kind, at.Format(time.RFC3339Nano), kind)
		}
		keep = false
	}
	if keep {
		es = append(es, Ended{Kind: kind, At: at})
		slices.SortStableFunc(es, func(a, b Ended) int { return a.At.Compare(b.At) })
		v.lives.ended[workspace] = es
	}
	v.told = append(v.told, Told{Kind: kind, Workspace: workspace, At: at})

	return v.lives.life(workspace), nil
}

func (v *Levyan) Close(ctx context.Context, req *levyanv1.CloseRequest) (*levyanv1.CloseResponse, error) {
	l, err := v.end(ctx, "close", req.GetWorkspace(), req.HasAt(), req.GetAt().AsTime())
	if err != nil {
		return nil, err
	}
	return levyanv1.CloseResponse_builder{Lifecycle: l}.Build(), nil
}

func (v *Levyan) Restore(ctx context.Context, req *levyanv1.RestoreRequest) (*levyanv1.RestoreResponse, error) {
	l, err := v.end(ctx, "restore", req.GetWorkspace(), req.HasAt(), req.GetAt().AsTime())
	if err != nil {
		return nil, err
	}
	return levyanv1.RestoreResponse_builder{Lifecycle: l}.Build(), nil
}

func (v *Levyan) Destroy(ctx context.Context, req *levyanv1.DestroyRequest) (*levyanv1.DestroyResponse, error) {
	l, err := v.end(ctx, "destroy", req.GetWorkspace(), req.HasAt(), req.GetAt().AsTime())
	if err != nil {
		return nil, err
	}
	return levyanv1.DestroyResponse_builder{Lifecycle: l}.Build(), nil
}

// GetLifecycle is what became of a workspace, as the fake was told it: one it
// heard nothing of is open.
func (v *Levyan) GetLifecycle(ctx context.Context, req *levyanv1.GetLifecycleRequest) (*levyanv1.GetLifecycleResponse, error) {
	if err := keyed(ctx); err != nil {
		return nil, err
	}

	v.mu.Lock()
	defer v.mu.Unlock()
	v.lives.init()
	if v.down != nil {
		return nil, v.down
	}
	if _, err := pdid.Parse(req.GetWorkspace()); err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "levyantest: %q is not a workspace", req.GetWorkspace())
	}
	return levyanv1.GetLifecycleResponse_builder{Lifecycle: v.lives.life(req.GetWorkspace())}.Build(), nil
}
