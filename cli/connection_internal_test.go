package cli

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/lesomnus/loft/cmd"
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
// line -- and a file from before connections is not read as one.
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
		`{"url": "https://loft.example", "cookie": "c", "key": "k"}`:                                      "before connections",
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

// TestAConnectionSaysWhoItIs: a person's carries their session and the
// workspace they speak to; an operator's carries their key, and refuses a
// workspace, which an operator names in each command.
func TestAConnectionSaysWhoItIs(t *testing.T) {
	x := require.New(t)
	t.Setenv("LOFT_TEST_OPERATOR_KEY", "a-key-of-a-good-length")

	p := &Connection{Kind: Person, Url: "https://loft.example/", Cookie: "loft_session", Key: "session-key"}
	c, err := p.dial("@acme")
	x.NoError(err)
	x.Equal("https://loft.example", c.base)
	x.Equal("loft_session=session-key", c.header.Get("Cookie"))
	x.Equal("acme", c.header.Get(cmd.WorkspaceHeader))
	x.Empty(c.header.Get("Authorization"))

	o := &Connection{Kind: Operator, Url: "http://127.0.0.1:8081", Key: "env:LOFT_TEST_OPERATOR_KEY"}
	c, err = o.dial("")
	x.NoError(err)
	x.Equal("Bearer a-key-of-a-good-length", c.header.Get("Authorization"))
	x.Empty(c.header.Get("Cookie"))
	_, err = o.dial("acme")
	x.ErrorContains(err, "an operator names the workspace in each command")
}
