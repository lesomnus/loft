package cli_test

import (
	"fmt"
	"io"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lesomnus/xli"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/lesomnus/loft/cli"
	"github.com/lesomnus/loft/server/admin"
	"github.com/lesomnus/loft/server/operator"
)

// operatorKey is alice's, at the entry point [served.entryPoint] serves.
const operatorKey = "an-operators-key-long-enough-to-be-one"

// entryPoint is the deployment's entry point (`loft-admin serve`, D74), which
// alice holds a key to, over HTTP as `loft` reaches it.
func (s *served) entryPoint() string {
	s.t.Helper()
	ops, err := admin.Operators(map[string]string{"alice": operatorKey})
	s.x.NoError(err)
	h, err := admin.Routes{S: s.s, C: s.c, Operators: ops}.Handler(s.t.Context())
	s.x.NoError(err)
	srv := httptest.NewServer(h)
	s.t.Cleanup(srv.Close)
	return srv.URL
}

// keyFile is a file holding alice's key, as an operator keeps one.
func keyFile(t *testing.T) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "alice.key")
	require.NoError(t, os.WriteFile(p, []byte(operatorKey+"\n"), 0o600))
	return p
}

// fails is the error a command ends with, which it has to end with one.
func fails(t *testing.T, k *xli.Command, args ...string) error {
	t.Helper()
	k.Writer = io.Discard
	err := k.Run(t.Context(), args)
	require.Error(t, err, strings.Join(args, " "))
	return err
}

// TestAnOperatorKeepsWhereTheirKeyIs (D75): `loft auth login --operator` asks
// the entry point who the key is before it keeps anything, and keeps where the
// key is and never the key -- refused written out, and refused when the entry
// point does not know it. Kept beside a person's, either is the one in use as
// `loft auth switch` says, and says who it is; what is an account's is refused
// on the operator's.
func TestAnOperatorKeepsWhereTheirKeyIs(t *testing.T) {
	s := serve(t)
	x := s.x
	ep := s.entryPoint()
	file := keyFile(t)
	conf := os.Getenv("LOFT_CLIENT_CONFIG")
	t.Setenv("LOFT_TEST_NOBODYS_KEY", "nobody-holds-this-key-of-a-good-length")

	x.ErrorContains(fails(t, cli.NewCmdAuth(), "login", "--operator", "--url", ep, "--key", operatorKey), "by reference")
	x.ErrorContains(fails(t, cli.NewCmdAuth(), "login", "--operator", "--url", ep), "--key")
	x.ErrorContains(fails(t, cli.NewCmdAuth(), "login", "--operator", "--url", ep, "--key", "file:"+file, "--allow", "read"), "--allow")
	x.ErrorContains(fails(t, cli.NewCmdAuth(), "login", "--url", s.url, "--key", "file:"+file), "--operator")
	err := fails(t, cli.NewCmdAuth(), "login", "--operator", "--url", ep, "--key", "env:LOFT_TEST_NOBODYS_KEY")
	x.Equal(codes.Unauthenticated, status.Code(err), "a key the entry point does not know")
	_, err = os.Stat(conf)
	x.ErrorIs(err, os.ErrNotExist, "nothing was kept")

	out := run(t, cli.NewCmdAuth(), "login", "--operator", "--url", ep, "--key", "file:"+file, "--name", "ops")
	x.Equal("signed in as alice, an operator, at "+ep+"\n", out)
	b, err := os.ReadFile(conf)
	x.NoError(err)
	x.NotContains(string(b), operatorKey, "where the key is, and never the key")
	x.Equal(fmt.Sprintf("alice, an operator (%s), at %s\n", operator.Id("alice"), ep), run(t, cli.NewCmdAuth(), "whoami"))
	x.Contains(run(t, root(), "call", "loft.OperatorTrailService.Owed"), "{", "the operator's own methods, as the operator")

	// A person's beside it, the one in use once it is signed in.
	s.signIn("owner", "owner1234")
	person := strings.TrimPrefix(s.url, "http://")
	x.Equal(fmt.Sprintf("* %s\tperson\t%s\n  ops\toperator\t%s\n", person, s.url, ep), run(t, cli.NewCmdAuth(), "list"))
	x.Contains(run(t, cli.NewCmdAuth(), "whoami"), "owner")
	x.ErrorContains(fails(t, cli.NewCmdAuth(), "login", "--operator", "--url", ep, "--key", "file:"+file, "--name", person), "another kind")

	x.Equal("using ops, operator at "+ep+"\n", run(t, cli.NewCmdAuth(), "switch", "ops"))
	x.Contains(run(t, cli.NewCmdAuth(), "whoami"), "alice, an operator")
	x.ErrorContains(fails(t, cli.NewCmdAuth(), "switch", "nowhere"), `no connection "nowhere"`)

	// What is an account's has no operator's to be: its workspace, its
	// workspaces, its consents, and an assistant speaking as it.
	for _, v := range []struct {
		k    *xli.Command
		args []string
	}{
		{cli.NewCmdUse(), []string{"acme"}},
		{root(), []string{"tenant", "mine"}},
		{cli.NewCmdConsent(), nil},
	} {
		x.ErrorContains(fails(t, v.k, v.args...), "this is an operator's connection", v.k.Name)
	}
	x.Contains(s.assistant().refused("whoami", nil), "this is an operator's connection")
	x.ErrorContains(fails(t, root(), "--workspace", "acme", "asset", "ls"), "an operator names the workspace in each command")

	// The key is read where it is, each call: one moved is none.
	x.NoError(os.Rename(file, file+".moved"))
	x.ErrorContains(fails(t, cli.NewCmdAuth(), "whoami"), "the operator's key")
	x.NoError(os.Rename(file+".moved", file))

	// Signed in again, with no address: the connection in use's, by its name.
	x.Equal("signed in as alice, an operator, at "+ep+"\n", run(t, cli.NewCmdAuth(), "login", "--operator", "--key", "file:"+file))
	x.Equal(fmt.Sprintf("  %s\tperson\t%s\n* ops\toperator\t%s\n", person, s.url, ep), run(t, cli.NewCmdAuth(), "list"))

	// Forgotten: the key is the deployment's (`admin.keys`), and only the
	// connection goes.
	x.Equal("signed out of ops\n", run(t, cli.NewCmdAuth(), "logout"))
	x.Equal(fmt.Sprintf("  %s\tperson\t%s\n", person, s.url), run(t, cli.NewCmdAuth(), "list"))
	x.ErrorContains(fails(t, cli.NewCmdAuth(), "whoami"), "no connection")
	x.Contains(run(t, cli.NewCmdAuth(), "logout", person), "signed out of "+person)
	x.Equal("none: `loft auth login`\n", run(t, cli.NewCmdAuth(), "list"))
}

// TestACommandIsToldItsConnection (D75): the command line's, or else the
// environment's, or else the one in use -- and an operator's for the command
// alone, with no file at all, which is how a script or CI asks the entry point.
func TestACommandIsToldItsConnection(t *testing.T) {
	s := serve(t)
	x := s.x
	ep := s.entryPoint()
	file := keyFile(t)
	conf := os.Getenv("LOFT_CLIENT_CONFIG")

	x.Contains(run(t, root(), "--url", ep, "--key", "file:"+file, "auth", "whoami"), "alice, an operator")
	_, err := os.Stat(conf)
	x.ErrorIs(err, os.ErrNotExist, "a connection for the command alone is kept nowhere")
	x.ErrorContains(fails(t, root(), "--url", ep, "--key", "file:"+file, "auth", "logout"), "nothing to forget")
	x.ErrorContains(fails(t, root(), "--url", ep, "--key", operatorKey, "auth", "whoami"), "by reference")
	x.ErrorContains(fails(t, root(), "--url", ep, "auth", "whoami"), "give --key too")
	x.ErrorContains(fails(t, root(), "auth", "whoami"), "no connection")

	x.NoError(os.WriteFile(conf, []byte(`not a file this reads`), 0o600))
	x.Contains(run(t, root(), "--url", ep, "--key", "file:"+file, "auth", "whoami"), "alice, an operator", "a connection for the command alone reads no file")
	x.ErrorContains(fails(t, root(), "auth", "whoami"), conf, "and one that is the file's says it does not read")
	x.NoError(os.Remove(conf))

	t.Setenv("LOFT_ADMIN_URL", ep)
	t.Setenv("LOFT_ADMIN_KEY", "file:"+file)
	x.Contains(run(t, root(), "auth", "whoami"), "alice, an operator", "the environment's")

	s.signIn("owner", "owner1234")
	person := strings.TrimPrefix(s.url, "http://")
	x.Contains(run(t, root(), "auth", "whoami"), "alice, an operator", "the environment's over the one in use")
	t.Setenv("LOFT_ADMIN_KEY", "")
	x.Contains(run(t, root(), "auth", "whoami"), "owner", "the one in use")

	t.Setenv("LOFT_CONNECTION", person)
	x.Contains(run(t, root(), "--key", "file:"+file, "auth", "whoami"), "alice, an operator", "the command line's over the environment's")
	x.ErrorContains(fails(t, root(), "--connection", person, "--key", "file:"+file, "auth", "whoami"), "not both")
	t.Setenv("LOFT_ADMIN_KEY", "file:"+file)
	x.ErrorContains(fails(t, root(), "auth", "whoami"), "not both")
	t.Setenv("LOFT_ADMIN_KEY", "")
	t.Setenv("LOFT_CONNECTION", "nowhere")
	x.ErrorContains(fails(t, root(), "auth", "whoami"), `LOFT_CONNECTION: no connection "nowhere"`)
	x.Contains(run(t, root(), "--connection", person, "auth", "whoami"), "owner")
}

// TestACallOfTheOtherKindSaysWhichConnectionItWants (design 9.16): the
// operator's own methods asked on a person's connection -- and an entity's no
// role is given, as a workspace's closing is (D83) -- and a person's asked on
// an operator's, are refused with the code the server answered and the
// connection they want -- a call and a stream alike. An entity's method the
// entry point serves and a role is given is either kind's, and its refusal is
// the server's own.
func TestACallOfTheOtherKindSaysWhichConnectionItWants(t *testing.T) {
	s := serve(t)
	x := s.x
	ep := s.entryPoint()
	file := keyFile(t)

	s.signIn("kim", "kim-password")
	err := fails(t, root(), "call", "loft.OperatorTrailService.Owed")
	x.Equal(codes.Unimplemented, status.Code(err))
	x.ErrorContains(err, "/loft.OperatorTrailService/Owed is the operator's, and this is a person's connection: use an operator's (--key")
	err = fails(t, root(), "call", "loft.TenantService.Close", `{}`)
	x.Equal(codes.PermissionDenied, status.Code(err))
	x.ErrorContains(err, "/loft.TenantService/Close is the operator's, and this is a person's connection: use an operator's (--key")
	err = fails(t, root(), "call", "loft.HolderService.List", `{}`)
	x.Equal(codes.PermissionDenied, status.Code(err), "kim is a member, who reads nobody's membership: %v", err)
	x.NotContains(err.Error(), "connection", "either kind's, refused as kim")

	t.Setenv("LOFT_ADMIN_URL", ep)
	t.Setenv("LOFT_ADMIN_KEY", "file:"+file)
	err = fails(t, root(), "asset", "ls")
	x.Equal(codes.Unimplemented, status.Code(err), "a service the entry point does not have")
	x.ErrorContains(err, "/loft.AssetService/List is a person's, and this is an operator's connection: use a person's (--connection")
	err = fails(t, root(), "asset", "watch")
	x.Equal(codes.Unimplemented, status.Code(err), "a stream")
	x.ErrorContains(err, "/loft.AssetService/Watch is a person's, and this is an operator's connection")
	err = fails(t, root(), "call", "loft.TenantService.Configure", `{}`)
	x.Equal(codes.PermissionDenied, status.Code(err), "a method of a service it has, which its table does not name")
	x.ErrorContains(err, "/loft.TenantService/Configure is a person's, and this is an operator's connection")

	x.Contains(run(t, root(), "call", "loft.OperatorTrailService.Owed"), "{", "the operator's own")
	x.Contains(run(t, root(), "tenant", "ls"), "@acme\t", "an entity's the entry point serves, as the operator")
}
