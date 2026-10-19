package cmd

import (
	"context"

	// The exporter a collector is reached with. mkot knows an exporter by the
	// name a package registers, and without this one an `otel:` naming
	// `otlp` is refused at start: what is left is the pretty exporter, and so
	// stderr.
	_ "github.com/lesomnus/mkot/otlp"
	"github.com/lesomnus/payday/config"
)

// Telemetry builds what this app's `otel:` describes and puts it on the
// context, answering with that context and what shuts it down.
//
// It is written here rather than done by payday, and that is the same decision
// `serve.go` makes about the stack: the order is load-bearing and belongs
// where it can be read. `Build` answers with a **new context**, and everything
// that logs, traces or measures reads the providers off it -- so this has to
// happen before [Build], and a reader has to be able to see that it does.
// Both servers call it: `rove serve` and the operator's entry point,
// `rove-admin serve`. So does every operator command that stands the stack up
// (`rove tenant`, `rove trail`, ...), once, from the root (`cli.observed`).
//
// # What it costs to leave out
//
// Silence, and nothing else. `grpcx` puts a request logger and a tracer on
// every server it builds, both read from `otx.From(ctx)`, and `otx.From`
// answers a context carrying nothing with the OpenTelemetry globals -- which
// discard what they are given. The app serves, every call succeeds, and there
// is no log anywhere.
//
// It is worth knowing that this happened: an app shipped with `otel:` in its
// configuration and these three calls missing, and what found it was somebody
// wondering where the request log had gone. `grpcx` says so on stderr now,
// once, which is the only channel left when the log is the broken thing.
//
// # In the sandbox
//
// The same three calls, in `wasm/main.go`, with no configuration to read: the
// defaults give the pretty exporter, and on Wasm that writes to the browser's
// console rather than to a stderr nothing reads. The request logger is named
// there rather than through `grpcx`, since a message port has no
// `grpc.ServerOption`.
func Telemetry(ctx context.Context, c *Config) (context.Context, func(), error) {
	ctx, o, err := c.Otel.Build(ctx, config.Service{
		Name: Name,

		// The Go package instruments are created from. Without it an
		// instrument is attributed to whichever library happened to make it,
		// and a dashboard groups by the name of a dependency.
		Scope: "github.com/lesomnus/rove",
	})
	if err != nil {
		return nil, nil, err
	}

	if err := o.Start(ctx); err != nil {
		return nil, nil, err
	}

	// Shutdown and not ForceFlush: shutting the providers down is what flushes
	// the last batch, and a process that exits without it loses whatever that
	// batch held.
	return ctx, func() { o.Shutdown(ctx) }, nil
}
