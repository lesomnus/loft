package cli_test

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/lesomnus/payday/pdtest"
	"github.com/stretchr/testify/require"

	"github.com/lesomnus/rove/cli"
	"github.com/lesomnus/rove/cmd"
)

// TestAScanFindsALabelInAnotherOfTheAccountsWorkspaces: `rove scan` asks what
// the scan page asks -- the workspace in use, then which of the account's own
// a label is in (D28) -- and a label in a workspace the account is not in is
// nothing it is told of.
func TestAScanFindsALabelInAnotherOfTheAccountsWorkspaces(t *testing.T) {
	s := serve(t)
	x := s.x

	cookie := s.browser("owner", "owner1234")
	n, body := s.http(http.MethodPost, "/account/workspaces", cookie, map[string]string{"name": "두 번째"})
	x.Equal(http.StatusCreated, n, body)
	var made struct{ Alias string }
	x.NoError(json.Unmarshal([]byte(body), &made))

	s.signIn("owner", "owner1234")
	out := run(t, cli.NewCmdApi(), "--workspace", made.Alias, "call", "rove.LabelService.Print", `{"count": 1}`)
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

// TestInitGivesNoLabelDomainWhereWorkspacesHaveNone: a suffix to make default
// subdomains under is nothing `rove init` gives a workspace while labels
// print with the deployment's own address (D28).
func TestInitGivesNoLabelDomainWhereWorkspacesHaveNone(t *testing.T) {
	x := require.New(t)
	c := cmd.Config{}
	c.Db.Driver, c.Db.Dsn = pdtest.DB(t)
	c.Watch.Broker = "memory"
	c.App.Files = t.TempDir()
	c.Auth.Roster.Db.Driver, c.Auth.Roster.Db.Dsn = pdtest.DB(t)
	c.App.Labels.Suffix = "l.test"
	run(t, cli.NewCmdInit(&c), "--tenant", "acme", "--login", "owner", "--password", "owner1234")

	s, err := cmd.Build(t.Context(), c)
	x.NoError(err)
	t.Cleanup(func() { s.Close() })
	n, err := s.Ent.TenantDomain.Query().Count(t.Context())
	x.NoError(err)
	x.Zero(n, "a label domain nothing prints with")
}
