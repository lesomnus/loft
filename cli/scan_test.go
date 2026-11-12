package cli_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/lesomnus/loft/cli"
)

// TestAScanReadsALabelInTheWorkspace: `loft scan` asks what the scan page
// asks -- in the workspace the deployment serves -- by the code, in any case,
// or the label's address; and a code nobody printed is nothing it is told of.
func TestAScanReadsALabelInTheWorkspace(t *testing.T) {
	s := serve(t)
	x := s.x

	s.signIn("owner", "owner1234")
	out := run(t, root(), "call", "loft.LabelService.Print", `{"count": 1}`)
	var printed struct {
		Labels []struct{ Id, Code string }
		Urls   []string
	}
	x.NoError(json.Unmarshal([]byte(out), &printed), out)
	x.Len(printed.Labels, 1)
	code := printed.Labels[0].Code
	x.Len(code, 8)

	for _, v := range []string{code, strings.ToLower(code[:4] + "-" + code[4:]), printed.Urls[0]} {
		out = run(t, cli.NewCmdScan(), v)
		x.Contains(out, printed.Labels[0].Id, v)
	}
	err := cli.NewCmdScan().Run(context.Background(), []string{"nobody-printed-this"})
	x.ErrorContains(err, "NotFound", "a code nobody printed")
}
