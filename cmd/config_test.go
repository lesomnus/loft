package cmd_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/lesomnus/rove/cmd"
)

func TestStateDirIsWhereTheSqliteFileIs(t *testing.T) {
	for dsn, want := range map[string]string{
		"file:data/rove.db?_pragma=foreign_keys(1)": "data",
		"file:/var/lib/rove/rove.db":                "/var/lib/rove",
		"data/rove.db":                              "data",
		"rove.db":                                   ".",
		"file::memory:?cache=shared":                "",
		":memory:":                                  "",
		"file:rove?mode=memory&cache=shared":        "",
		"":                                          "",
	} {
		c := cmd.Config{}
		c.Db.Driver, c.Db.Dsn = "sqlite3", dsn
		require.Equal(t, want, c.StateDir(), dsn)
	}

	c := cmd.Config{}
	c.Db.Driver, c.Db.Dsn = "pgx", "postgres://rove@localhost/rove"
	require.Empty(t, c.StateDir(), "a database that is not a file")
}

// TestSeveralProcessesSignWithOneKey: a deployment that names a lease or a
// broker crossing processes is several of them, and a download link one signed
// with a key it made up is refused by the next -- so it is told to name one.
func TestSeveralProcessesSignWithOneKey(t *testing.T) {
	for _, v := range []struct {
		lease, broker, key string
		says               []string
	}{
		{lease: "local", broker: "memory"},
		{lease: "local", broker: "none"},
		{lease: "postgres", broker: "memory", says: []string{"spin.lease: postgres"}},
		{lease: "none", broker: "memory", says: []string{"spin.lease: none"}},
		{lease: "local", broker: "postgres", says: []string{"watch.broker: postgres"}},
		{lease: "postgres", broker: "postgres", says: []string{"spin.lease: postgres", "watch.broker: postgres"}},
		{lease: "postgres", broker: "postgres", key: "one-key-everywhere"},
	} {
		c := cmd.Config{}
		c.Spin.Lease, c.Watch.Broker, c.App.SigningKey = v.lease, v.broker, v.key
		c.App.PublicUrl = "https://rove.example.com"

		err := c.Replicas()
		if len(v.says) == 0 {
			require.NoError(t, err, "%+v", v)
			continue
		}
		require.ErrorContains(t, err, "app.signing_key", "%+v", v)
		require.ErrorContains(t, err, "ROVE_APP_SIGNING_KEY", "%+v", v)
		for _, w := range v.says {
			require.ErrorContains(t, err, w, "%+v", v)
		}
	}
}

// TestADeploymentIsToldWhereItIs: several processes are a deployment, and one
// that names no `app.public_url` would answer as http://localhost:8080 --
// cookies without `Secure`, links and labels on an address nobody opens -- so
// it does not start. A checkout, one process on its own, is still that
// address by default.
func TestADeploymentIsToldWhereItIs(t *testing.T) {
	c := cmd.Config{}
	c.Spin.Lease, c.Watch.Broker, c.App.SigningKey = "local", "memory", ""
	require.NoError(t, c.Replicas(), "a checkout")

	c.Spin.Lease, c.Watch.Broker, c.App.SigningKey = "postgres", "postgres", "one-key-everywhere"
	err := c.Replicas()
	require.ErrorContains(t, err, "app.public_url")
	require.ErrorContains(t, err, "ROVE_APP_PUBLIC_URL")

	c.App.PublicUrl = "https://rove.example.com"
	require.NoError(t, c.Replicas())
}

// TestTheRosterInThisProcessIsSaidInProduction is the predicate `serve`
// warns by: roster in this process (`auth.roster.addr` empty) on the
// PostgreSQL driver, or behind an address that is not this machine's, is a
// deployment that looks like production, and says which it was. A checkout
// does not, and neither does anything with roster apart.
func TestTheRosterInThisProcessIsSaidInProduction(t *testing.T) {
	for _, v := range []struct {
		name   string
		driver string
		public string
		addr   string
		want   []string
	}{
		{name: "a checkout", driver: "sqlite3"},
		{name: "a checkout told its address", driver: "sqlite3", public: "http://localhost:8080"},
		{name: "a name under localhost", driver: "sqlite3", public: "https://rove.localhost"},
		{name: "a loopback address", driver: "sqlite3", public: "http://127.0.0.1:8080"},
		{name: "the loopback of v6", driver: "sqlite3", public: "http://[::1]:8080"},
		{name: "PostgreSQL", driver: "pgx", want: []string{"db.driver: pgx"}},
		{name: "a name people open", driver: "sqlite3", public: "https://assets.example.com",
			want: []string{"app.public_url: https://assets.example.com"}},
		{name: "both", driver: "pgx", public: "https://assets.example.com",
			want: []string{"db.driver: pgx", "app.public_url: https://assets.example.com"}},
		{name: "a roster apart", driver: "pgx", public: "https://assets.example.com", addr: "roster.example.com:443"},
	} {
		t.Run(v.name, func(t *testing.T) {
			c := cmd.Config{}
			c.Db.Driver = v.driver
			c.App.PublicUrl = v.public
			c.Auth.Roster.Addr = v.addr
			require.Equal(t, v.want, c.EmbeddedInProduction())
		})
	}
}
