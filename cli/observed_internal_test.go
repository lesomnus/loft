package cli

import (
	"context"
	"testing"

	"github.com/lesomnus/otx"
	"github.com/lesomnus/xli"
	"github.com/stretchr/testify/require"

	"github.com/lesomnus/rove/cmd"
)

// TestAnOperatorCommandRunsObserved: a command `observed` names, and every
// command below it, runs on a context that carries the deployment's telemetry,
// so the server it stands up is not a silent one. Anything else runs on the
// context it was given.
func TestAnOperatorCommandRunsObserved(t *testing.T) {
	x := require.New(t)

	carried := map[string]bool{}
	leaf := func(name string) *xli.Command {
		return &xli.Command{
			Name: name,
			Handler: xli.OnRun(func(ctx context.Context, self *xli.Command, next xli.Next) error {
				_, carried[name] = otx.FromOK(ctx)
				return next(ctx)
			}),
		}
	}

	tenant := &xli.Command{Name: "tenant", Commands: []*xli.Command{leaf("purge")}}
	whoami := leaf("whoami")
	c := cmd.Config{}
	root := &xli.Command{
		Name:     "rove",
		Commands: []*xli.Command{tenant, whoami},
		Handler:  xli.Chain(observed(&c, tenant), xli.RequireSubcommand()),
	}

	x.NoError(root.Run(context.Background(), []string{"tenant", "purge"}))
	x.NoError(root.Run(context.Background(), []string{"whoami"}))
	x.True(carried["purge"], "a command below one it names")
	x.False(carried["whoami"], "a command it does not name")
}
