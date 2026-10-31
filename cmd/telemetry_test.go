package cmd_test

import (
	"context"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/goccy/go-yaml"
	"github.com/lesomnus/otx/log"
	"github.com/stretchr/testify/require"
	collectorlogspb "go.opentelemetry.io/proto/otlp/collector/logs/v1"
	"google.golang.org/grpc"

	"github.com/lesomnus/loft/cmd"
)

// logSink is a collector's logs service, keeping the bodies it is sent and
// the service each came from.
type logSink struct {
	collectorlogspb.UnimplementedLogsServiceServer

	mu     sync.Mutex
	bodies map[string]string
}

func (s *logSink) Export(_ context.Context, req *collectorlogspb.ExportLogsServiceRequest) (*collectorlogspb.ExportLogsServiceResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, rl := range req.GetResourceLogs() {
		service := ""
		for _, a := range rl.GetResource().GetAttributes() {
			if a.GetKey() == "service.name" {
				service = a.GetValue().GetStringValue()
			}
		}
		for _, sl := range rl.GetScopeLogs() {
			for _, r := range sl.GetLogRecords() {
				s.bodies[r.GetBody().GetStringValue()] = service
			}
		}
	}

	return &collectorlogspb.ExportLogsServiceResponse{}, nil
}

// TestTelemetryReachesACollector is `otel:` naming the OTLP exporter, as a
// deployment with a collector writes it: it builds, and what is logged
// arrives there, said to be from Loft.
func TestTelemetryReachesACollector(t *testing.T) {
	x := require.New(t)

	lis, err := net.Listen("tcp", "127.0.0.1:0")
	x.NoError(err)
	sink := &logSink{bodies: map[string]string{}}
	srv := grpc.NewServer()
	collectorlogspb.RegisterLogsServiceServer(srv, sink)
	go srv.Serve(lis)
	t.Cleanup(srv.Stop)

	var c cmd.Config
	x.NoError(yaml.Unmarshal([]byte(`
exporters:
  otlp:
    endpoint: http://`+lis.Addr().String()+`
providers:
  logger:
    processors: [resource/loft]
    exporters: [otlp]
`), &c.Otel))

	ctx, done, err := cmd.Telemetry(t.Context(), &c)
	x.NoError(err)
	log.From(ctx).InfoContext(ctx, "telemetry: over OTLP")
	done()

	sink.mu.Lock()
	defer sink.mu.Unlock()
	x.Equal(map[string]string{"telemetry: over OTLP": "loft"}, sink.bodies)
}

// TestAnExitWaitsOnACollectorForAMomentOnly: with `otel:` naming a collector
// nothing answers at, the last batch is given up after a moment rather than
// waited on for the OTLP exporter's ten seconds, so a collector that is down
// does not hold an operator's command at its last line.
func TestAnExitWaitsOnACollectorForAMomentOnly(t *testing.T) {
	x := require.New(t)

	was := *cmd.FlushWithin
	*cmd.FlushWithin = 200 * time.Millisecond
	t.Cleanup(func() { *cmd.FlushWithin = was })

	// An address nothing listens at.
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	x.NoError(err)
	addr := lis.Addr().String()
	x.NoError(lis.Close())

	var c cmd.Config
	x.NoError(yaml.Unmarshal([]byte(`
exporters:
  otlp:
    endpoint: http://`+addr+`
providers:
  logger:
    processors: [resource/loft]
    exporters: [otlp]
`), &c.Otel))

	ctx, done, err := cmd.Telemetry(t.Context(), &c)
	x.NoError(err)
	log.From(ctx).InfoContext(ctx, "telemetry: nobody to send this to")

	began := time.Now()
	done()
	x.Less(time.Since(began), 3*time.Second, "the exit waited on the collector")
}
