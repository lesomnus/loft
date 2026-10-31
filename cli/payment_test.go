package cli_test

import (
	"context"
	"testing"

	"github.com/lesomnus/loft/cli"
	"github.com/lesomnus/loft/cmd"
	"github.com/lesomnus/loft/internal/levyantest"
	"github.com/lesomnus/loft/server/checkout"
)

// TestHowAWorkspaceIsPaidForIsCommandsToo (design 7.9): every method of
// `loft.PaymentService` is a command of `loft api payment`, as every RPC is
// one -- a payer's own, answered as the account, and a workspace's, of the
// workspace the terminal names -- relayed to levyan as the page's are.
func TestHowAWorkspaceIsPaidForIsCommandsToo(t *testing.T) {
	fake := levyantest.Start(t)
	s := serveWith(t, func(c *cmd.Config) {
		c.App.Billing.Levyan = fake.Config()
		c.App.Billing.Checkout = checkout.Config{Environment: checkout.Sandbox, ClientToken: checkout.NewToken("test_000000000000000000000000000")}
	})
	x := s.x
	s.signIn("owner", "owner1234")

	out := run(t, cli.NewCmdApi(), "payment", "create-billing-account", "-o", "json", `{"country": "KR"}`)
	x.Contains(out, `"currency": "KRW"`)
	x.Contains(out, `"channel": "BILLING_CHANNEL_MOR"`)
	out = run(t, cli.NewCmdApi(), "payment", "list-billing-accounts", "-o", "json")
	x.Contains(out, `"country": "KR"`)
	x.Len(fake.Relays(), 2, "each relayed as the account")

	out = run(t, cli.NewCmdApi(), "--workspace", "acme", "payment", "get-payer", "-o", "json")
	x.Contains(out, `"workspace"`)
	out = run(t, cli.NewCmdApi(), "--workspace", "acme", "payment", "get-usage", "-o", "json")
	x.Contains(out, `"usage"`)

	// The workspace's is its owner's and administrators': a member is told
	// no, as the page is.
	s.signIn("kim", "kim-password")
	err := cli.NewCmdApi().Run(context.Background(), []string{"--workspace", "acme", "payment", "get-payer"})
	x.ErrorContains(err, "PermissionDenied")
	out = run(t, cli.NewCmdApi(), "payment", "list-offers", "-o", "json")
	x.NotContains(out, "error", "and asks what is offered to it as itself")
}
