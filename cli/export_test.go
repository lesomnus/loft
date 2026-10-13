package cli_test

import (
	"archive/zip"
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/lesomnus/rove/cli"
)

// TestAWorkspaceIsTakenOutFromATerminal: `rove workspace export` is what the
// page's download is -- everything a workspace its owner owns has, as one zip
// -- and somebody who does not own it is refused, with nothing left behind.
func TestAWorkspaceIsTakenOutFromATerminal(t *testing.T) {
	s := serve(t)
	x := s.x
	dir := t.TempDir()

	s.signIn("owner", "owner1234")
	out := filepath.Join(dir, "acme.zip")
	x.Contains(run(t, cli.NewCmdWorkspace(), "export", "--out", out, "acme"), out)
	z, err := zip.OpenReader(out)
	x.NoError(err, "a whole zip")
	names := map[string]bool{}
	for _, f := range z.File {
		names[f.Name] = true
	}
	x.NoError(z.Close())
	x.True(names["manifest.json"], "what it holds and what it left out, and why")

	s.signIn("kim", "kim-password")
	err = cli.NewCmdWorkspace().Run(context.Background(), []string{"export", "--out", filepath.Join(dir, "kim.zip"), "acme"})
	x.ErrorContains(err, "owner")
	entries, err := os.ReadDir(dir)
	x.NoError(err)
	x.Len(entries, 1, "a refused export leaves no file, not even half of one")
}
