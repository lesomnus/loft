// Command loft-admin is the operator's entry point and its tool (design 6장,
// decision 17): `loft-admin serve` beside a deployment, on a listener only the
// operators' network reaches, and every other command asking it from wherever
// the operator is. `loft`, the binary the public reaches, links none of it.
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"

	admincli "github.com/lesomnus/loft/cli/admin"
	"github.com/lesomnus/loft/cmd"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	var c cmd.Config
	if err := admincli.Cmd(&c).Run(ctx, os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "loft-admin:", err)
		os.Exit(1)
	}
}
