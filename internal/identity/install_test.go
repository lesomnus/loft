package identity

import (
	"encoding/json"
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestTheInstallStepsGiveLoftWhatItAsks: the role an external roster
// installs Loft with, and the tenant's key, are written out where an
// operator reads them -- the identity guide and both deployments' READMEs --
// and run by CI's `bundle` step; each list there is [AgentMethods], no more
// and no less. One short of it, and `loft init` fails at roster writing the
// roles Loft ships (D89), or a call is refused for want of
// `HolderService/Reaches`; one more is a method Loft never asks for.
func TestTheInstallStepsGiveLoftWhatItAsks(t *testing.T) {
	for _, path := range []string{
		"../../docs/dev/identity.md",
		"../../deploy/compose/README.md",
		"../../deploy/k8s/README.md",
		"../../.github/workflows/ci.yaml",
	} {
		t.Run(strings.TrimPrefix(path, "../../"), func(t *testing.T) {
			x := require.New(t)
			b, err := os.ReadFile(path)
			x.NoError(err)
			lists := installLists(string(b))
			x.NotEmpty(lists, "no list of the methods Loft is installed with (`m=…`)")
			for _, l := range lists {
				x.ElementsMatch(AgentMethods, l)
			}
		})
	}
}

var (
	// listLine is a line of the shell variable the steps build the list in:
	// `m=/roster…` begins one, and `m=$m,…` goes on with it.
	listLine = regexp.MustCompile(`^\s*m=(\$\{?m\}?,)?(.*)$`)
	// patchLine is `roster role patch`, which is given the whole list.
	patchLine = regexp.MustCompile(`role patch @\S+ '(\{.*\})'`)
)

// installLists is every list of methods `doc` installs Loft with, or patches
// its role to.
func installLists(doc string) [][]string {
	out := [][]string{}
	unquote := strings.NewReplacer(`'`, "", `"`, "")
	for _, line := range strings.Split(doc, "\n") {
		if m := listLine.FindStringSubmatch(line); m != nil {
			if m[1] == "" || len(out) == 0 {
				out = append(out, []string{})
			}
			for _, v := range strings.Split(unquote.Replace(m[2]), ",") {
				if v = strings.TrimSpace(v); v != "" {
					out[len(out)-1] = append(out[len(out)-1], v)
				}
			}
			continue
		}
		if m := patchLine.FindStringSubmatch(line); m != nil {
			var v struct {
				Methods []string `json:"methods"`
			}
			if err := json.Unmarshal([]byte(m[1]), &v); err == nil {
				out = append(out, v.Methods)
			}
		}
	}
	return out
}
