package levyan_test

import (
	"bytes"
	"context"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	levyanv1 "github.com/lesomnus/levyan/api/levyan/v1"
	"github.com/lesomnus/otx/log"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/lesomnus/payday/config"
	"github.com/lesomnus/payday/meter"
	"github.com/lesomnus/payday/pdid"

	"github.com/lesomnus/loft/internal/identity"
	"github.com/lesomnus/loft/internal/levyantest"
	"github.com/lesomnus/loft/server/levyan"
)

// logged is a context whose log is kept, for a test to read what was said.
func logged(ctx context.Context) (context.Context, *lines) {
	l := &lines{}
	return log.Into(ctx, slog.New(slog.NewTextHandler(l, &slog.HandlerOptions{Level: slog.LevelDebug}))), l
}

type lines struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *lines) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *lines) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

func dial(t *testing.T, c levyan.Config) *levyan.Client {
	t.Helper()
	v, err := levyan.Dial(c, identity.ResolveRef)
	require.NoError(t, err)
	t.Cleanup(func() { v.Close() })
	return v
}

// readings are one workspace's, from one measuring, as the reader makes them.
func readings(tenant pdid.Id, at time.Time) []meter.Reading {
	return []meter.Reading{
		{Tenant: tenant, Meter: "loft.assets.active", MeasuredAt: at, Value: 120, Unit: "{asset}"},
		{Tenant: tenant, Meter: "loft.history.retention_days", MeasuredAt: at, Value: 0, Unit: "d"},
	}
}

// TestAReadingIsHandedToLevyanAsItWasMeasured: the workspace as the canonical
// text of its tenant's identifier, the meter by its key, the moment to the
// microsecond, the value and the unit -- with the product key.
func TestAReadingIsHandedToLevyanAsItWasMeasured(t *testing.T) {
	x := require.New(t)
	fake := levyantest.Start(t)
	c := dial(t, fake.Config())

	tenant := pdid.New(1)
	at := time.Date(2027, 1, 15, 0, 0, 0, 123456000, time.UTC)
	x.NoError(c.Send(t.Context(), readings(tenant, at)))

	calls := fake.Calls()
	x.Len(calls, 1, "one workspace's readings are one call")
	x.Len(calls[0], 2)
	r := calls[0][0]
	x.Equal(tenant.String(), r.GetWorkspace())
	x.Equal("loft.assets.active", r.GetMeter())
	x.Equal(at, r.GetMeasuredAt().AsTime())
	x.EqualValues(120, r.GetValue())
	x.Equal("{asset}", r.GetUnit())
	x.Equal("d", calls[0][1].GetUnit())

	// And nothing is no call.
	x.NoError(c.Send(t.Context(), nil))
	x.Len(fake.Calls(), 1)
}

// TestARefusedReadingIsLoggedAndNotHandedOverAgain: a reading levyan refused
// is refused for what it is -- a meter its price book has not got -- and would
// be again, so it is said in the log and the call is not an error: the reader
// does not hand it over again, and the readings beside it are kept.
func TestARefusedReadingIsLoggedAndNotHandedOverAgain(t *testing.T) {
	x := require.New(t)
	fake := levyantest.Start(t)
	fake.Refuse("loft.history.retention_days", levyanv1.Refusal_REFUSAL_NO_METER)
	c := dial(t, fake.Config())
	ctx, said := logged(t.Context())

	tenant := pdid.New(1)
	x.NoError(c.Send(ctx, readings(tenant, time.Now().UTC())))
	x.Contains(said.String(), "a reading was refused")
	x.Contains(said.String(), "loft.history.retention_days")
	x.Contains(said.String(), "REFUSAL_NO_METER")
	x.Contains(said.String(), tenant.String())
	x.NotContains(said.String(), "loft.assets.active", "the reading beside it was kept, and is not said")

	// Through the reader, as a workspace is read before it is destroyed:
	// handed over once, and the reading done.
	r := meter.Reader{
		Sink: c,
		Meters: []meter.Meter{
			{Key: "loft.history.retention_days", Unit: "d", Measure: func(context.Context, pdid.Id) (int64, error) { return 365, nil }},
		},
	}
	x.NoError(r.Read(ctx, tenant))
	x.Len(fake.Calls(), 2)

	// And an outcome a later levyan says that this build cannot: levyan
	// answered, and would answer the same again.
	fake.Answer(func(_ context.Context, req *levyanv1.RecordRequest) (*levyanv1.RecordResponse, error) {
		out := []*levyanv1.Recorded{}
		for range req.GetReadings() {
			out = append(out, levyanv1.Recorded_builder{Outcome: levyanv1.Outcome(9)}.Build())
		}
		return levyanv1.RecordResponse_builder{Results: out}.Build(), nil
	})
	x.NoError(c.Send(ctx, readings(tenant, time.Now().UTC())))
	x.Contains(said.String(), "came to what this build cannot say")
	x.NotContains(said.String(), levyantest.Key)
}

// TestACallThatFailedIsAnError: levyan not answering, or refusing the call,
// is it not having the readings -- an error the reader hands the same readings
// over again for. The key is in none of it.
func TestACallThatFailedIsAnError(t *testing.T) {
	x := require.New(t)
	fake := levyantest.Start(t)
	c := dial(t, fake.Config())
	ctx, said := logged(t.Context())
	tenant := pdid.New(1)

	fake.Answer(func(context.Context, *levyanv1.RecordRequest) (*levyanv1.RecordResponse, error) {
		return nil, status.Error(codes.Unavailable, "levyan is going down")
	})
	err := c.Send(ctx, readings(tenant, time.Now().UTC()))
	x.Error(err)
	x.Equal(codes.Unavailable, status.Code(err))
	x.NotContains(err.Error(), levyantest.Key)

	// A levyan that does not answer at all.
	l, err := net.Listen("tcp", "127.0.0.1:0")
	x.NoError(err)
	gone := l.Addr().String()
	x.NoError(l.Close())
	cfg := fake.Config()
	cfg.Addr = gone
	err = dial(t, cfg).Send(ctx, readings(tenant, time.Now().UTC()))
	x.Error(err)
	x.Equal(codes.Unavailable, status.Code(err))

	// A key levyan does not know.
	cfg = fake.Config()
	cfg.Key = levyan.KeyPrefix + "somebody-elses"
	fake.Answer(nil)
	err = dial(t, cfg).Send(ctx, readings(tenant, time.Now().UTC()))
	x.Equal(codes.Unauthenticated, status.Code(err))

	// An answer that does not say what became of every reading.
	fake.Answer(func(context.Context, *levyanv1.RecordRequest) (*levyanv1.RecordResponse, error) {
		return levyanv1.RecordResponse_builder{Results: []*levyanv1.Recorded{
			levyanv1.Recorded_builder{Outcome: levyanv1.Outcome_OUTCOME_KEPT}.Build(),
		}}.Build(), nil
	})
	x.ErrorContains(c.Send(ctx, readings(tenant, time.Now().UTC())), "answered 1 results for 2 readings")

	x.NotContains(said.String(), levyantest.Key)
}

// TestACallIsGivenADeadline: the reader's context ends only when the process
// stops, so a levyan that takes the call and never answers would hold the
// pass there for good. Each call has an end of its own, `timeout`, and ten
// seconds unset.
func TestACallIsGivenADeadline(t *testing.T) {
	x := require.New(t)
	fake := levyantest.Start(t)
	left := make(chan time.Duration, 1)
	fake.Answer(func(ctx context.Context, _ *levyanv1.RecordRequest) (*levyanv1.RecordResponse, error) {
		d, ok := ctx.Deadline()
		if !ok {
			left <- -1
			return nil, status.Error(codes.Internal, "no deadline")
		}
		left <- time.Until(d)
		<-ctx.Done()
		return nil, ctx.Err()
	})

	cfg := fake.Config()
	cfg.Timeout = 200 * time.Millisecond
	began := time.Now()
	err := dial(t, cfg).Send(t.Context(), readings(pdid.New(1), time.Now().UTC()))
	x.Equal(codes.DeadlineExceeded, status.Code(err))
	x.Less(time.Since(began), 5*time.Second, "given up at its deadline")
	d := <-left
	x.Greater(d, time.Duration(0))
	x.LessOrEqual(d, 200*time.Millisecond)

	// Unset, ten seconds.
	fake.Answer(func(ctx context.Context, _ *levyanv1.RecordRequest) (*levyanv1.RecordResponse, error) {
		d, ok := ctx.Deadline()
		if !ok {
			left <- -1
		} else {
			left <- time.Until(d)
		}
		return nil, status.Error(codes.Unavailable, "not now")
	})
	_ = dial(t, fake.Config()).Send(t.Context(), readings(pdid.New(1), time.Now().UTC()))
	d = <-left
	x.Greater(d, levyan.DefaultTimeout-2*time.Second)
	x.LessOrEqual(d, levyan.DefaultTimeout)
}

// TestLevyanIsDialledAsTheConfigurationSays: with the product key it issued,
// and over TLS unless `insecure` says otherwise in as many words.
func TestLevyanIsDialledAsTheConfigurationSays(t *testing.T) {
	x := require.New(t)
	fake := levyantest.Start(t)
	dir := t.TempDir()

	refused := func(c levyan.Config) string {
		t.Helper()
		_, err := levyan.Dial(c, identity.ResolveRef)
		x.Error(err)
		x.NotContains(err.Error(), levyantest.Key)
		return err.Error()
	}

	cfg := fake.Config()
	cfg.Key = ""
	x.Contains(refused(cfg), "app.billing.levyan.key")
	cfg.Key = "rk_a-roster-key-and-not-levyans"
	x.Contains(refused(cfg), "not a product key")
	cfg.Key = "file:" + filepath.Join(dir, "nothing")
	x.Contains(refused(cfg), "app.billing.levyan.key")
	cfg = fake.Config()
	cfg.Tls.CAFile = filepath.Join(dir, "ca.pem")
	x.Contains(refused(cfg), "insecure and tls both")
	cfg.Insecure = false
	x.Contains(refused(cfg), "app.billing.levyan.tls")
	_, err := levyan.Dial(levyan.Config{}, identity.ResolveRef)
	x.Error(err, "no levyan")

	// The key in a file, as a deployment keeps it.
	key := filepath.Join(dir, "key")
	x.NoError(os.WriteFile(key, []byte(levyantest.Key+"\n"), 0o600))
	cfg = fake.Config()
	cfg.Key = "file:" + key
	x.NoError(dial(t, cfg).Send(t.Context(), readings(pdid.New(1), time.Now().UTC())))

	// TLS when nothing says otherwise: a plaintext levyan is no levyan to it,
	// and the readings are not sent in the clear.
	cfg = fake.Config()
	cfg.Insecure = false
	calls := len(fake.Calls())
	err = dial(t, cfg).Send(t.Context(), readings(pdid.New(1), time.Now().UTC()))
	x.Equal(codes.Unavailable, status.Code(err))
	x.Len(fake.Calls(), calls, "nothing reached it")

	// And so whatever `enabled` says.
	cfg.Tls = config.DialConfig{Enabled: false, ServerName: "levyan"}
	err = dial(t, cfg).Send(t.Context(), readings(pdid.New(1), time.Now().UTC()))
	x.Equal(codes.Unavailable, status.Code(err))
	x.Len(fake.Calls(), calls)
}
