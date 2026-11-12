package cli_test

import (
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/lesomnus/payday/pdcmd"
	"github.com/lesomnus/xli"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// words is every word a level of the tree answers to, and the command each
// runs: a word that answered two would run the first and never the second.
func words(t *testing.T, path string, level xli.Commands) {
	t.Helper()
	seen := map[string]string{}
	for _, c := range level {
		for _, w := range append([]string{c.Name}, c.Aliases...) {
			was, ok := seen[w]
			require.False(t, ok, "%s: %q answers %s and %s", path, w, was, c.Name)
			seen[w] = c.Name
		}
		words(t, path+" "+c.Name, c.Commands)
	}
}

// TestTheTreeIsTheSchemasWords (design 9.16, D75): `loft <schema name>
// <verb>` -- every entity of the schema with an RPC is a command at the root,
// beside the utilities and none of their words; the rows other commands write
// are hidden and run by name; and the words beside the schema's are the ones
// decided, and no more.
func TestTheTreeIsTheSchemasWords(t *testing.T) {
	x := require.New(t)
	root := root()
	words(t, "loft", root.Commands)

	utilities := []string{"version", "config", "init", "migrate", "serve", "auth", "call", "scan", "mcp"}
	x.Nil(root.Commands.Get("use"), "a deployment serves one workspace, and nobody chooses it (D85)")
	for _, name := range utilities {
		x.NotNil(root.Commands.Get(name), name)
	}
	hidden := []string{
		"placement", "link", "stewardship", "fact",
		"reservation-item", "allocation", "custody-line", "stock-movement", "purchase-line",
	}
	for _, e := range pdcmd.Entities("loft") {
		c := root.Commands.Get(e.Name)
		if e.Service.Methods().Len() == 0 {
			x.Nil(c, "%s has no RPC, and is no command", e.Name)
			continue
		}
		x.NotNil(c, "%s is the schema's, and a command", e.Name)
		x.Equal(slices.Contains(hidden, e.Name), c.Hidden, "%s is hidden as decided", e.Name)
		for _, v := range c.Commands {
			x.NotEqual("apply", v.Name, "%s: payday's general write, which no role is given", e.Name)
		}
	}
	x.Nil(root.Commands.Get("api"), "no layer above the schema's names")
	x.NotNil(root.Commands.Get("payment"), "the one service that is no entity's")

	aliases := map[string][]string{}
	var walk func(path string, level xli.Commands)
	walk = func(path string, level xli.Commands) {
		for _, c := range level {
			p := strings.TrimPrefix(path+"/"+c.Name, "/")
			if len(c.Aliases) > 0 && !(c.Name == "list" && slices.Equal(c.Aliases, []string{"ls"})) {
				aliases[p] = c.Aliases
			}
			walk(p, c.Commands)
		}
	}
	walk("", root.Commands)
	x.Equal(map[string][]string{
		"tenant":           {"workspace", "ws"},
		"tenant/configure": {"conf", "config"},
	}, aliases, "beside every list's ls, which is payday's")
	for _, e := range pdcmd.Entities("loft") {
		if c := root.Commands.Get(e.Name); c != nil && c.Commands.Get("list") != nil {
			x.Equal([]string{"ls"}, c.Commands.Get("list").Aliases, e.Name)
		}
	}
}

// TestAHiddenRowRunsByName: what is out of the help is still a command by its
// name, and its methods are `loft call`'s -- reaching the server, which says
// who may, as it says it of every other.
func TestAHiddenRowRunsByName(t *testing.T) {
	s := serve(t)
	x := s.x
	s.signIn("owner", "owner1234")
	reaches := func(args ...string) {
		t.Helper()
		k := root()
		k.Writer = io.Discard
		err := k.Run(t.Context(), args)
		if err == nil {
			return
		}
		_, ok := status.FromError(err)
		x.True(ok, "%v: refused, if at all, by the server: %v", args, err)
		x.NotEqual(codes.Unimplemented, status.Code(err), "%v", args)
	}
	for name, svc := range map[string]string{
		"placement": "Placement", "link": "Link", "stewardship": "Stewardship", "fact": "Fact",
		"reservation-item": "ReservationItem", "allocation": "Allocation", "custody-line": "CustodyLine",
		"stock-movement": "StockMovement", "purchase-line": "PurchaseLine",
	} {
		reaches(name, "list")
		reaches("call", "loft."+svc+"Service.List", "{}")
	}
}

// TestTheHelpIsTheSameOnEveryConnection (design 9.16): the tree is built
// before any configuration is read, so what `--help` says is the same whoever
// the connection in use is -- and a client file that does not read is no
// reason for it to say nothing. What is hidden is not in it.
func TestTheHelpIsTheSameOnEveryConnection(t *testing.T) {
	x := require.New(t)
	conf := filepath.Join(t.TempDir(), "client.json")
	t.Setenv("LOFT_CLIENT_CONFIG", conf)
	help := func(args ...string) string {
		t.Helper()
		return run(t, root(), append(args, "--help")...)
	}

	none := help()
	x.Contains(none, "tenant,workspace,ws")
	x.Contains(none, "asset")
	x.NotContains(none, "placement", "hidden")
	x.NotContains(none, " api ", "no layer above the schema's names")
	asset := help("asset")
	x.Contains(asset, "add-many")

	x.NoError(os.WriteFile(conf, []byte(`{"current": "ops", "connections": {"ops": {"kind": "operator", "url": "http://127.0.0.1:8081", "key": "env:NOWHERE"}}}`), 0o600))
	x.Equal(none, help(), "an operator's in use")
	x.Equal(asset, help("asset"))
	x.NoError(os.WriteFile(conf, []byte(`{"current": "app", "connections": {"app": {"kind": "person", "url": "http://127.0.0.1:8080", "cookie": "c", "key": "k"}}}`), 0o600))
	x.Equal(none, help(), "a person's in use")
	x.NoError(os.WriteFile(conf, []byte(`not a file this reads`), 0o600))
	x.Equal(none, help(), "a client file that does not read")
	t.Setenv("LOFT_ADMIN_KEY", "the-key-itself-of-a-good-length")
	x.Equal(none, help(), "an environment that would be refused")

	x.Contains(help("fact"), "list", "a hidden group's own help lists its verbs")
}

// TestTheOperatorsCommandsSaySo (design 9.16): the help is the same on every
// connection, and what is the operator's alone says so in it -- a command
// whose calls only the entry point serves and no role of a workspace is
// given, and a group of nothing else. What both kinds call says nothing, nor
// what checks a file against keys `--trust` names.
func TestTheOperatorsCommandsSaySo(t *testing.T) {
	x := require.New(t)
	marked := map[string]bool{}
	var walk func(path string, level xli.Commands)
	walk = func(path string, level xli.Commands) {
		for _, c := range level {
			p := strings.TrimPrefix(path+"/"+c.Name, "/")
			if strings.HasSuffix(c.Brief, " (operator)") {
				marked[p] = true
			}
			walk(p, c.Commands)
		}
	}
	walk("", root().Commands)
	x.Equal(map[string]bool{
		"tenant/show":  true,
		"tenant/close": true, "tenant/restore": true, "tenant/export": true,
		"tenant/import": true, "tenant/retired": true, "tenant/retire": true, "tenant/purge": true,
		"tenant-contract/add": true, "legal-hold/add": true, "legal-hold/patch": true,
		"trail/verify": true, "trail/accept": true, "trail/rows": true, "trail/accept-rows": true,
		"trail/owed": true, "trail/keys": true, "trail/export": true,
		"search": true, "search/status": true, "search/reindex": true, "search/remove": true, "search/analyze": true,
		"retention": true, "retention/plan": true, "retention/run": true,
		"identity": true, "identity/link": true, "identity/depart": true, "identity/reconcile": true,
		"holder/add": true, "holder/password": true,
		"role": true, "role/widen": true,
		"event/redact": true, "access/list": true,
	}, marked)

	help := run(t, root(), "--help")
	x.Contains(help, "search               the search index")
	x.Regexp(`(?m)^\s+search\s.*\(operator\)$`, help)
	x.Regexp(`(?m)^\s+trail\s`, help, "the trail's group holds a check anyone with the keys makes")
}
