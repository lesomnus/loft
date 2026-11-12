package cli_test

import (
	"context"
	"testing"

	"github.com/lesomnus/loft/cmd"
	"github.com/lesomnus/loft/internal/levyantest"
)

// TestHowAWorkspaceIsPaidForIsCommandsToo (design 7.9): every method of
// `loft.PaymentService` is a command of `loft payment`, as every RPC is
// one -- the workspace's, of the workspace the terminal names -- relayed to
// levyan as the page's are. What a payer does with what it pays with is no
// command here: it is none of this deployment's (D81).
func TestHowAWorkspaceIsPaidForIsCommandsToo(t *testing.T) {
	fake := levyantest.Start(t)
	s := serveWith(t, func(c *cmd.Config) {
		c.App.Billing.Levyan = fake.Config()
	})
	x := s.x
	s.signIn("owner", "owner1234")

	var verbs []string
	for _, c := range root().Commands.Get("payment").Commands {
		verbs = append(verbs, c.Name)
	}
	x.ElementsMatch([]string{"get-payer", "get-usage", "list-statements", "offer"}, verbs)

	out := run(t, root(), "--workspace", "acme", "payment", "get-payer", "-o", "json")
	x.Contains(out, `"workspace"`)
	out = run(t, root(), "--workspace", "acme", "payment", "get-usage", "-o", "json")
	x.Contains(out, `"usage"`)

	// The workspace's is its owner's and administrators': a member is told
	// no, as the page is.
	s.signIn("kim", "kim-password")
	err := root().Run(context.Background(), []string{"--workspace", "acme", "payment", "get-payer"})
	x.ErrorContains(err, "PermissionDenied")
}
