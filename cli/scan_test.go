package cli_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/lesomnus/loft/cli"
)

// TestAScanReadsALabelInTheWorkspaceInUse: `loft scan` asks what the scan
// page asks -- the workspace in use -- by the code, in any case, or the
// label's address; and a label of another workspace is nothing it is told of
// (D84).
func TestAScanReadsALabelInTheWorkspaceInUse(t *testing.T) {
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

	for _, v := range []string{strings.ToLower(code[:4] + "-" + code[4:]), printed.Urls[0]} {
		out = run(t, cli.NewCmdScan(), "--workspace", made.Alias, v)
		x.Contains(out, printed.Labels[0].Id, v)
	}
	err := cli.NewCmdScan().Run(context.Background(), []string{"--workspace", "acme", code})
	x.ErrorContains(err, "NotFound", "a label of another workspace")
}
