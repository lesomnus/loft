package cli_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// TestAWorkspacesEndIsTheOperatorsFromATerminal (D83): closing a workspace,
// restoring it and taking all of it out are the operator's commands, and a
// person's connection is told so before anything is asked -- its owner's
// too. A workspace an operator takes out that is not there leaves no file,
// not even half of one; and the workspaces a person is in are listed on their
// connection as they were.
func TestAWorkspacesEndIsTheOperatorsFromATerminal(t *testing.T) {
	s := serve(t)
	x := s.x
	dir := t.TempDir()

	s.signIn("owner", "owner1234")
	x.Contains(run(t, root(), "tenant", "ls"), "@acme\t", "the workspaces an owner sees")
	for _, args := range [][]string{
		{"tenant", "close", "--yes", "acme"},
		{"tenant", "restore", "--yes", "acme"},
		{"--reason", "폐업 전 보관", "tenant", "export", "--out", filepath.Join(dir, "acme.zip"), "acme"},
	} {
		x.ErrorContains(fails(t, root(), args...), "is the operator's, and this is a person's connection", args)
	}

	t.Setenv("LOFT_ADMIN_URL", s.entryPoint())
	t.Setenv("LOFT_ADMIN_KEY", "file:"+keyFile(t))
	err := root().Run(context.Background(), []string{"--reason", "x", "tenant", "export", "--out", filepath.Join(dir, "nobody.zip"), "nobody-here"})
	x.ErrorContains(err, "NotFound")
	entries, err := os.ReadDir(dir)
	x.NoError(err)
	x.Empty(entries, "a refused export leaves no file, not even half of one")
}
