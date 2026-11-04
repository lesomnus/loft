// Command loft-admin is the operator's entry point (design 6장, decision 17,
// D74): `loft-admin serve` beside a deployment, on a listener only the
// operators' network reaches. What an operator asks of it is `loft`'s, on an
// operator's connection, from wherever the operator is (design 9.16, D75).
// `loft`, the binary the public reaches, links none of the entry point.
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
