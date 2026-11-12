// Package admincli is `loft-admin`, the operator's entry point (design 6장,
// decision 17, D74): `serve` stands it up beside a deployment, on a listener
// of its own (`server/admin`). What an operator asks of it is `loft`'s, on an
// operator's connection (design 9.16, D75): the same commands a person runs,
// at their schema paths, and the operator's own beside them.
//
// It is a binary of its own so that `loft`, the one the public reaches, links
// none of the entry point.
package admincli

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"time"

	"github.com/lesomnus/otx/log"
	"github.com/lesomnus/payday/pdcmd"
	"github.com/lesomnus/xli"
	"github.com/lesomnus/xli/cfg"
	"github.com/lesomnus/xli/flg"

	"github.com/lesomnus/loft/cli"
	"github.com/lesomnus/loft/cmd"
	"github.com/lesomnus/loft/server/admin"
)

// Cmd is the root of `loft-admin`: the entry point, served, and what tells
// a deployment's build and configuration.
func Cmd(c *cmd.Config) *xli.Command {
	l := cfg.New(cmd.Name, c, cli.TerminalReads)
	version := pdcmd.NewCmdVersion()

	return &xli.Command{
		Name:  "loft-admin",
		Brief: "the operator's entry point; what an operator asks of it is `loft`'s, on an operator's connection",

		Flags: flg.Flags{cfg.ConfigFlag()},

		Commands: []*xli.Command{
			version,
			cfg.NewCmdConfig(l),
			newCmdServe(c),
		},

		Handler: xli.Chain(cfg.Load(l, version), xli.RequireSubcommand()),
	}
}

func newCmdServe(c *cmd.Config) *xli.Command {
	return &xli.Command{
		Name:  "serve",
		Brief: "serve the operator's entry point beside this deployment, on admin.addr",
		Handler: xli.OnRun(func(ctx context.Context, self *xli.Command, next xli.Next) error {
			ops, err := admin.Operators(c.Admin.Keys)
			if err != nil {
				return err
			}
			if len(ops) == 0 {
				return errors.New("admin.keys: an entry point nobody holds a key to is one nobody can use")
			}
			// Before the server, as `loft serve` does: what it builds logs,
			// traces and measures into the providers it finds on ctx, and
			// without these into ones that discard -- an entry point whose
			// every call went unlogged.
			ctx, done, err := cmd.Telemetry(ctx, c)
			if err != nil {
				return err
			}
			defer done()

			s, err := cmd.Build(ctx, *c)
			if err != nil {
				return err
			}
			defer s.Close()

			routes := admin.Routes{S: s, C: *c, Operators: ops}
			h, err := routes.Handler(ctx)
			if err != nil {
				return err
			}
			l, err := net.Listen("tcp", c.Admin.ListenAddr())
			if err != nil {
				return err
			}
			log.From(ctx).InfoContext(ctx, "admin", slog.String("addr", l.Addr().String()), slog.Int("operators", len(ops)))
			srv := serving(ctx, h)
			go func() {
				<-ctx.Done()
				shut, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				_ = srv.Shutdown(shut)
			}()
			if err := srv.Serve(l); err != nil && !errors.Is(err, http.ErrServerClosed) {
				return err
			}
			return next(ctx)
		}),
	}
}

// serving is an HTTP server for the entry point, on `ctx`: a client that
// takes its time over its headers holds no connection for nothing, and one
// that keeps a connection open between calls has it closed after a while. A
// call, once made, may take as long as it takes.
func serving(ctx context.Context, h http.Handler) *http.Server {
	return &http.Server{
		Handler:           h,
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       2 * time.Minute,
		BaseContext:       func(net.Listener) context.Context { return ctx },
	}
}
