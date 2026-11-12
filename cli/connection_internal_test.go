package cli

import (
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	app "github.com/lesomnus/loft"
)

func envOf(vs map[string]string) func(string) string {
	return func(k string) string { return vs[k] }
}

// TestAConnectionIsChosenByTheCommandLineThenTheEnvironmentThenTheFile: the
// command line's `--connection` or `--key`, else the environment's
// LOFT_CONNECTION or LOFT_ADMIN_KEY, else the file's current one -- and two
// at one level refused, since which was meant is nobody's to guess.
func TestAConnectionIsChosenByTheCommandLineThenTheEnvironmentThenTheFile(t *testing.T) {
	x := require.New(t)
	cs := &connections{
		Current: "app",
		Connections: map[string]*Connection{
			"app": {Kind: Person, Url: "https://loft.example", Cookie: "c", Key: "session"},
			"ops": {Kind: Operator, Url: "http://loft-admin:8081", Key: "file:/run/secrets/k"},
		},
	}
	none := envOf(nil)

	c, err := cs.choose(Choice{}, none)
	x.NoError(err)
	x.Equal("app", c.Name, "the file's current one")

	c, err = cs.choose(Choice{Name: "ops"}, envOf(map[string]string{"LOFT_ADMIN_KEY": "env:K", "LOFT_CONNECTION": "app"}))
	x.NoError(err)
	x.Equal("ops", c.Name, "the command line over the environment")

	c, err = cs.choose(Choice{Key: "env:K"}, none)
	x.NoError(err)
	x.Empty(c.Name, "a connection for the command alone")
	x.Equal(Operator, c.Kind)
	x.Equal(defaultEntryPoint, c.Url)
	x.Equal("env:K", c.Key)
	c, err = cs.choose(Choice{Key: "file:/k", Url: "http://admin.internal:8081/"}, envOf(map[string]string{"LOFT_ADMIN_URL": "http://other:1"}))
	x.NoError(err)
	x.Equal("http://admin.internal:8081", c.Url, "--url over LOFT_ADMIN_URL")
	c, err = cs.choose(Choice{Key: "file:/k"}, envOf(map[string]string{"LOFT_ADMIN_URL": "http://other:1"}))
	x.NoError(err)
	x.Equal("http://other:1", c.Url)

	c, err = cs.choose(Choice{}, envOf(map[string]string{"LOFT_ADMIN_KEY": "env:K", "LOFT_ADMIN_URL": "http://e:8081"}))
	x.NoError(err)
	x.Equal(Operator, c.Kind, "the environment over the file")
	x.Equal("http://e:8081", c.Url)
	c, err = cs.choose(Choice{}, envOf(map[string]string{"LOFT_CONNECTION": "ops"}))
	x.NoError(err)
	x.Equal("ops", c.Name)

	for _, v := range []struct {
		flags Choice
		env   map[string]string
		says  string
	}{
		{Choice{Name: "ops", Key: "env:K"}, nil, "not both"},
		{Choice{}, map[string]string{"LOFT_CONNECTION": "ops", "LOFT_ADMIN_KEY": "env:K"}, "not both"},
		{Choice{Url: "http://e:8081"}, nil, "give --key too"},
		{Choice{Name: "nowhere"}, nil, `no connection "nowhere"`},
		{Choice{}, map[string]string{"LOFT_CONNECTION": "nowhere"}, `no connection "nowhere"`},
		{Choice{Key: "the-key-itself-of-a-good-length"}, nil, "by reference"},
		{Choice{}, map[string]string{"LOFT_ADMIN_KEY": "the-key-itself-of-a-good-length"}, "by reference"},
		{Choice{Key: "env:"}, nil, "by reference"},
	} {
		_, err := cs.choose(v.flags, envOf(v.env))
		x.ErrorContains(err, v.says, "%+v %v", v.flags, v.env)
	}

	_, err = (&connections{Connections: map[string]*Connection{}}).choose(Choice{}, none)
	x.ErrorIs(err, errNoConnection)
	_, err = (&connections{Current: "gone", Connections: map[string]*Connection{}}).choose(Choice{}, none)
	x.ErrorContains(err, `no connection "gone"`)
}

// TestAKeyIsNamedAndNeverKept: an operator's key is read where its reference
// says, and one written out is refused -- in the file as on the command
// line.
func TestAKeyIsNamedAndNeverKept(t *testing.T) {
	x := require.New(t)
	dir := t.TempDir()
	file := filepath.Join(dir, "operator.key")
	x.NoError(os.WriteFile(file, []byte(" a-key-of-a-good-length-from-a-file \n"), 0o600))
	t.Setenv("LOFT_TEST_OPERATOR_KEY", "a-key-of-a-good-length-from-the-env")

	k, err := keyOf("file:" + file)
	x.NoError(err)
	x.Equal("a-key-of-a-good-length-from-a-file", k)
	k, err = keyOf("env:LOFT_TEST_OPERATOR_KEY")
	x.NoError(err)
	x.Equal("a-key-of-a-good-length-from-the-env", k)
	_, err = keyOf("env:LOFT_TEST_NOT_SET")
	x.ErrorContains(err, "not set")
	_, err = keyOf("file:" + filepath.Join(dir, "nothing"))
	x.Error(err)
	x.NoError(os.WriteFile(filepath.Join(dir, "empty"), nil, 0o600))
	_, err = keyOf("file:" + filepath.Join(dir, "empty"))
	x.ErrorContains(err, "empty")
	_, err = keyOf("a-key-itself")
	x.ErrorIs(err, errLiteralKey)

	path := filepath.Join(dir, "client.json")
	t.Setenv("LOFT_CLIENT_CONFIG", path)
	for body, says := range map[string]string{
		`{"connections": {"ops": {"kind": "operator", "url": "http://e:8081", "key": "the-key-itself"}}}`: "by reference",
		`{"connections": {"x": {"kind": "robot", "url": "http://e:8081"}}}`:                               "a person's or an operator's",
	} {
		x.NoError(os.WriteFile(path, []byte(body), 0o600))
		_, err := loadConnections()
		x.ErrorContains(err, says, body)
	}

	cs := &connections{Current: "ops", Connections: map[string]*Connection{"ops": {Kind: Operator, Url: "http://e:8081", Key: "file:" + file}}}
	x.NoError(cs.save())
	b, err := os.ReadFile(path)
	x.NoError(err)
	x.NotContains(string(b), "a-key-of-a-good-length", "the key is in no file this keeps")
	st, err := os.Stat(path)
	x.NoError(err)
	x.Equal(os.FileMode(0o600), st.Mode().Perm())
	got, err := loadConnections()
	x.NoError(err)
	x.Equal(cs, got)
}

// TestAConnectionSaysWhoItIs: a person's carries their session, and an
// operator's their key. Neither names a workspace: a deployment serves one
// (D85).
func TestAConnectionSaysWhoItIs(t *testing.T) {
	x := require.New(t)
	t.Setenv("LOFT_TEST_OPERATOR_KEY", "a-key-of-a-good-length")

	p := &Connection{Kind: Person, Url: "https://loft.example/", Cookie: "loft_session", Key: "session-key"}
	c, err := p.dial()
	x.NoError(err)
	x.Equal("https://loft.example", c.base)
	x.Equal("loft_session=session-key", c.header.Get("Cookie"))
	x.Empty(c.header.Get("Authorization"))

	o := &Connection{Kind: Operator, Url: "http://127.0.0.1:8081", Key: "env:LOFT_TEST_OPERATOR_KEY"}
	c, err = o.dial()
	x.NoError(err)
	x.Equal("Bearer a-key-of-a-good-length", c.header.Get("Authorization"))
	x.Empty(c.header.Get("Cookie"))
}

// TestOnlyACallOfTheOtherKindIsToldWhichConnection: what a refusal says is
// changed only where the call went to the other kind -- the operator's own
// methods on a person's connection, a method the entry point's table does not
// name on an operator's -- and only of what a server says of a method it does
// not serve. The code is the server's, and so are the details.
func TestOnlyACallOfTheOtherKindIsToldWhichConnection(t *testing.T) {
	x := require.New(t)
	owed := app.OperatorTrailService_Owed_FullMethodName
	members := app.HolderService_List_FullMethodName
	list := app.AssetService_List_FullMethodName

	for _, v := range []struct {
		kind   Kind
		method string
		code   codes.Code
		says   string
	}{
		{Person, owed, codes.Unimplemented, "is the operator's, and this is a person's connection"},
		{Person, owed, codes.PermissionDenied, "is the operator's, and this is a person's connection"},
		{Person, app.TenantContractService_Add_FullMethodName, codes.PermissionDenied, "is the operator's, and this is a person's connection"},
		{Person, app.TenantService_Close_FullMethodName, codes.PermissionDenied, "is the operator's, and this is a person's connection"},
		{Operator, list, codes.Unimplemented, "is a person's, and this is an operator's connection"},
		{Operator, list, codes.PermissionDenied, "is a person's, and this is an operator's connection"},
		{Person, owed, codes.Unavailable, "as it was"},
		{Operator, list, codes.NotFound, "as it was"},
		{Person, members, codes.PermissionDenied, "as it was"},
		{Operator, members, codes.PermissionDenied, "as it was"},
		{Person, list, codes.PermissionDenied, "as it was"},
		{Operator, owed, codes.Unimplemented, "as it was"},
		{"", owed, codes.Unimplemented, "as it was"},
	} {
		st, err := status.New(v.code, "as it was").WithDetails(&errdetails.ErrorInfo{Reason: "kept"})
		x.NoError(err)
		got := status.Convert(refusal(v.kind, v.method, st.Err()))
		x.Equal(v.code, got.Code(), "%s %s", v.kind, v.method)
		x.Contains(got.Message(), v.says, "%s %s %s", v.kind, v.method, v.code)
		x.Len(got.Details(), 1, "the details are the server's")
	}
	x.NoError(refusal(Person, owed, nil))
	x.Equal(io.EOF, refusal(Operator, list, io.EOF), "a stream's end is no refusal")
}

// TestWhatIsTheOperatorsAloneIsWhatNoRoleIsGiven: the operator's own
// services, and an entity's methods the entry point serves and no role of a
// workspace is given; an entity's method both serve is either kind's, and one
// the entry point does not serve is nobody's to say of.
func TestWhatIsTheOperatorsAloneIsWhatNoRoleIsGiven(t *testing.T) {
	x := require.New(t)
	for m, alone := range map[string]bool{
		app.OperatorTrailService_Verify_FullMethodName: true,
		app.OperatorAuthService_Whoami_FullMethodName:  true,
		app.TenantContractService_Add_FullMethodName:   true,
		app.LegalHoldService_Patch_FullMethodName:      true,
		app.EventService_Redact_FullMethodName:         true,
		app.TenantService_Close_FullMethodName:         true,
		app.TenantService_Restore_FullMethodName:       true,
		app.TenantService_Export_FullMethodName:        true,
		app.TenantService_List_FullMethodName:          false,
		app.HolderService_List_FullMethodName:          false,
		app.TenantContractService_List_FullMethodName:  false,
		app.AccessReviewService_List_FullMethodName:    false,
		app.AssetService_List_FullMethodName:           false,
		app.EventService_Add_FullMethodName:            false,
	} {
		x.Equal(alone, operatorOnly(m), m)
	}
}
