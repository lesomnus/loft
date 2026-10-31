package main

import (
	"os/exec"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestThePublicBinaryLinksNoOperatorCode: `loft` is what the public reaches,
// and the operator's entry point is a binary of its own (`loft-admin`, design
// 6장) so that no path in this one sees every workspace. That holds only as
// long as nothing here imports what that one is made of -- which a single
// import anywhere under `cli` or `server` would undo without a word. So it is
// asked of the toolchain, as `go list -deps ./cmd/loft` (CLAUDE.md), every run.
func TestThePublicBinaryLinksNoOperatorCode(t *testing.T) {
	out, err := exec.Command("go", "list", "-deps", ".").Output()
	require.NoError(t, err)

	deps := strings.Fields(string(out))
	require.Contains(t, deps, "github.com/lesomnus/loft/cli", "the list is this binary's")
	for _, pkg := range []string{"github.com/lesomnus/loft/server/admin", "github.com/lesomnus/loft/cli/admin"} {
		require.NotContains(t, deps, pkg, "loft links %s", pkg)
	}
}
