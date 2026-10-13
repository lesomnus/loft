// Command rove-admin is the operator's entry point and its tool (design 6장,
// decision 17): `rove-admin serve` beside a deployment, on a listener only the
// operators' network reaches, and every other command asking it from wherever
// the operator is. `rove`, the binary the public reaches, links none of it.
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"

	admincli "github.com/lesomnus/rove/cli/admin"
	"github.com/lesomnus/rove/cmd"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	var c cmd.Config
	if err := admincli.Cmd(&c).Run(ctx, os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "rove-admin:", err)
		os.Exit(1)
	}
}
