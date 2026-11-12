package cli_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/lesomnus/loft/cli"
)

// TestAScanFindsALabelInAnotherOfTheAccountsWorkspaces: `loft scan` asks what
// the scan page asks -- the workspace in use, then which of the account's own
// a label is in (D28) -- and a label in a workspace the account is not in is
// nothing it is told of.
func TestAScanFindsALabelInAnotherOfTheAccountsWorkspaces(t *testing.T) {
	s := serve(t)
	x := s.x

	s.signIn("owner", "owner1234")
	var made struct{ Alias string }
	out := run(t, root(), "tenant", "create", "-o", "json", `{"name": "두 번째"}`)
	x.NoError(json.Unmarshal([]byte(out), &made), out)
	x.NotEmpty(made.Alias, out)
	out = run(t, root(), "--workspace", made.Alias, "call", "loft.LabelService.Print", `{"count": 1}`)
	var printed struct {
		Labels []struct{ Id, Code string }
		Urls   []string
	}
	x.NoError(json.Unmarshal([]byte(out), &printed), out)
	x.Len(printed.Labels, 1)
	code := printed.Labels[0].Code
	x.Len(code, 8)

	out = run(t, cli.NewCmdScan(), "--workspace", "acme", strings.ToLower(code[:4]+"-"+code[4:]))
	x.Contains(out, "# in @"+made.Alias, "found in the other workspace")
	x.Contains(out, printed.Labels[0].Id)
	out = run(t, cli.NewCmdScan(), "--workspace", made.Alias, printed.Urls[0])
	x.NotContains(out, "# in @", "found where it was asked")
	x.Contains(out, printed.Labels[0].Id)

	s.signIn("kim", "kim-password")
	err := cli.NewCmdScan().Run(context.Background(), []string{code})
	x.ErrorContains(err, "NotFound", "a label in a workspace kim is not in")
}
