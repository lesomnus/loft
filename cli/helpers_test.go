package cli_test

import (
	"context"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/lesomnus/payday/audit"
	"github.com/lesomnus/payday/pdid"
	"github.com/lesomnus/payday/pdtest"
	"github.com/lesomnus/xli"
	"github.com/stretchr/testify/require"

	"github.com/lesomnus/loft/cmd"
	"github.com/lesomnus/loft/server/pd"
)

// run is a command's output, run the way a shell runs it, read as it prints:
// a pipe holds only so much before the writer waits for a reader.
func run(t *testing.T, k *xli.Command, args ...string) string {
	t.Helper()
	x := require.New(t)

	r, w, err := os.Pipe()
	x.NoError(err)
	read := make(chan []byte)
	go func() {
		b, _ := io.ReadAll(r)
		read <- b
	}()
	was := os.Stdout
	os.Stdout = w
	err = k.Run(t.Context(), args)
	os.Stdout = was
	x.NoError(w.Close())

	b := <-read
	x.NoError(err, "%s: %s", strings.Join(args, " "), b)
	return string(b)
}

// embeddedConfig is a deployment of a test's own, with the roster in this
// process on a database of its own.
func embeddedConfig(t *testing.T) cmd.Config {
	c := cmd.Config{}
	c.Db.Driver, c.Db.Dsn = pdtest.DB(t)
	c.Watch.Broker = "memory"
	c.App.Files = t.TempDir()
	c.Auth.Roster.Db.Driver, c.Auth.Roster.Db.Dsn = pdtest.DB(t)
	return c
}

// on runs `f` against the deployment, the way a command does.
func on(t *testing.T, c cmd.Config, f func(s *cmd.Server)) {
	t.Helper()
	s, err := cmd.Build(context.Background(), c)
	require.NoError(t, err)
	defer s.Close()
	f(s)
}

// rowsChecked is every live row of the deployment `c` names checked against
// the digest the trail keeps of it: what `loft-admin trail rows` asks of the
// entry point, asked of the deployment here.
func rowsChecked(t *testing.T, c cmd.Config) audit.Rows {
	t.Helper()
	var v audit.Rows
	on(t, c, func(s *cmd.Server) {
		cs, err := s.Contents()
		require.NoError(t, err)
		require.NotNil(t, cs.Digest, "audit.digest.key names no key")
		v, err = pd.Digests(t.Context(), s.Ent, cs.Digest, s.Trail)
		require.NoError(t, err)
	})
	return v
}

// asTheTrailSays holds a check of the rows to having found nothing: every row
// is what the trail's digest of it says.
func asTheTrailSays(t *testing.T, v audit.Rows) {
	t.Helper()
	for what, ids := range map[string][]pdid.Id{
		"differ":                v.Differs,
		"forged":                v.Forged,
		"with no digest":        v.NoDigest,
		"not on the trail":      v.NotOnTrail,
		"under a key not given": v.KeyNotHeld,
		"missing":               v.Missing,
	} {
		require.Empty(t, ids, "rows %s", what)
	}
}
