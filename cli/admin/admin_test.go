package admincli_test

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/lesomnus/payday/pdid"
	"github.com/lesomnus/payday/pdtest"
	"github.com/lesomnus/xli"
	"github.com/stretchr/testify/require"

	app "github.com/lesomnus/loft"
	"github.com/lesomnus/loft/cli"
	admincli "github.com/lesomnus/loft/cli/admin"
	"github.com/lesomnus/loft/cmd"
	"github.com/lesomnus/loft/internal/ent/tenant"
	"github.com/lesomnus/loft/internal/loftest"
	"github.com/lesomnus/loft/server/admin"
	"github.com/lesomnus/loft/server/pd"
)

const key = "an-operators-key-long-enough-to-be-one"

// run runs a command and answers what it printed, read as it prints: a pipe
// holds only so much before the writer waits for a reader.
func run(t *testing.T, k *xli.Command, args ...string) (string, error) {
	t.Helper()
	r, w, err := os.Pipe()
	require.NoError(t, err)
	read := make(chan []byte)
	go func() {
		b, _ := io.ReadAll(r)
		read <- b
	}()
	was := os.Stdout
	os.Stdout = w
	err = k.Run(t.Context(), args)
	os.Stdout = was
	require.NoError(t, w.Close())
	return string(<-read), err
}

type deployment struct {
	c   cmd.Config
	s   *cmd.Server
	url string
}

func deploy(t *testing.T) *deployment { return deployWith(t, nil) }

// deployWith is a deployment, its configuration as `with` says, with acme --
// its owner and kim, a member -- and its entry point, which alice holds a key
// to.
func deployWith(t *testing.T, with func(*cmd.Config)) *deployment {
	t.Helper()
	x := require.New(t)
	c := cmd.Config{}
	c.Db.Driver, c.Db.Dsn = pdtest.DB(t)
	c.Watch.Broker = "memory"
	c.App.Files = t.TempDir()
	c.Auth.Roster.Db.Driver, c.Auth.Roster.Db.Dsn = pdtest.DB(t)
	if with != nil {
		with(&c)
	}
	_, err := run(t, cli.NewCmdInit(&c), "--tenant", "acme", "--login", "owner", "--password", "owner1234")
	x.NoError(err)

	s, err := cmd.Build(t.Context(), c)
	x.NoError(err)
	t.Cleanup(func() { s.Close() })
	loftest.Member(t, s, "acme", "kim", "member", "kim-password")
	ops, err := admin.Operators(map[string]string{"alice": key})
	x.NoError(err)
	h, err := admin.Routes{S: s, C: c, Operators: ops}.Handler(t.Context())
	x.NoError(err)
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return &deployment{c: c, s: s, url: srv.URL}
}

// TestLoftAdminServesAndAsksNothing: `loft-admin` is the entry point served,
// and what says its build and its configuration -- what an operator asks of
// the entry point is `loft`'s, on an operator's connection (design 9.16).
func TestLoftAdminServesAndAsksNothing(t *testing.T) {
	x := require.New(t)
	c := cmd.Config{}
	names := []string{}
	for _, k := range admincli.Cmd(&c).Commands {
		names = append(names, k.Name)
	}
	x.Equal([]string{"version", "config", "serve"}, names)
	_, err := run(t, admincli.Cmd(&c), "tenant", "ls")
	x.ErrorContains(err, "tenant")
}

// TestTheEntryPointAnswersOnlyAnOperatorsKey: a call without a key is
// refused, and one with an operator's is what curl asks with -- and the
// public binary's server has no such door at all. A key nobody holds is
// `loft`'s to be refused with (cli's TestAnOperatorsCommandWantsAnOperatorsConnection).
func TestTheEntryPointAnswersOnlyAnOperatorsKey(t *testing.T) {
	x := require.New(t)
	d := deploy(t)

	connect := func(url, auth string) int {
		req, err := http.NewRequest(http.MethodPost, url+app.TenantService_List_FullMethodName, bytes.NewReader([]byte(`{}`)))
		x.NoError(err)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Connect-Protocol-Version", "1")
		if auth != "" {
			req.Header.Set("Authorization", "Bearer "+auth)
		}
		res, err := http.DefaultClient.Do(req)
		x.NoError(err)
		b, _ := io.ReadAll(res.Body)
		res.Body.Close()
		if res.StatusCode == http.StatusOK {
			x.Contains(string(b), `"alias":"acme"`)
		}
		return res.StatusCode
	}
	x.Equal(http.StatusUnauthorized, connect(d.url, ""))
	x.Equal(http.StatusUnauthorized, connect(d.url, "somebody-elses-key-of-a-good-length-too"))
	x.Equal(http.StatusOK, connect(d.url, key), "what curl asks with a key, as the deployment's checks do")

	// What a reverse proxy asks asks for no key, and says no to a host that
	// is nobody's.
	res, err := http.Get(d.url + "/internal/tls-ask?domain=nobody.example")
	x.NoError(err)
	res.Body.Close()
	x.Equal(http.StatusNotFound, res.StatusCode)

	mux := http.NewServeMux()
	d.s.Routes(mux, d.c)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/internal/tls-ask?domain=nobody.example", nil))
	x.Equal(http.StatusNotFound, rec.Code)

	_, err = admin.Operators(map[string]string{"bob": "short"})
	x.ErrorContains(err, "admin.keys.bob")
	_, err = admin.Operators(map[string]string{"carol": "env:LOFT_NO_SUCH_KEY_HERE"})
	x.ErrorContains(err, "admin.keys.carol")
}

// TestACertificateIsAskedForADomainInUse: a reverse proxy is told to take a
// certificate for a workspace's domain that is verified and in use -- ready,
// active, or legacy, whose labels and pages still open -- and for no other:
// not one waiting for its TXT record, not one that lapsed when its DNS
// stopped saying it is here (loft#155), not one retired.
func TestACertificateIsAskedForADomainInUse(t *testing.T) {
	x := require.New(t)
	d := deploy(t)
	ctx := context.Background()

	acme, err := d.s.Ent.Tenant.Query().Where(tenant.Alias("acme")).Only(ctx)
	x.NoError(err)
	for state, want := range map[string]int{
		"ready":   http.StatusOK,
		"active":  http.StatusOK,
		"legacy":  http.StatusOK,
		"pending": http.StatusNotFound,
		"lapsed":  http.StatusNotFound,
		"retired": http.StatusNotFound,
	} {
		host := state + ".acme.example"
		x.NoError(d.s.Ent.Domain.Create().
			SetId(pdid.New(pd.DomainDomain).Uuid()).
			SetTenantId(acme.Id).
			SetHost(host).
			SetPurpose("app").
			SetState(state).
			SetToken("loft-test").
			SetSource("custom").
			SetDateUpdated(time.Now()).
			Exec(ctx))
		res, err := http.Get(d.url + "/internal/tls-ask?domain=" + host)
		x.NoError(err)
		res.Body.Close()
		x.Equal(want, res.StatusCode, state)
	}
}

// TestTheAskIsServedAloneAndHeldToItsRate: `admin.ask.addr` serves the ask
// and no operator's door, with or without a key; a host nobody verified is
// told no without spending a yes; and a host asked for more often than
// `admin.ask` allows is told to ask again later.
func TestTheAskIsServedAloneAndHeldToItsRate(t *testing.T) {
	x := require.New(t)
	d := deploy(t)
	ctx := context.Background()

	acme, err := d.s.Ent.Tenant.Query().Where(tenant.Alias("acme")).Only(ctx)
	x.NoError(err)
	for _, host := range []string{"a.acme.example", "b.acme.example"} {
		x.NoError(d.s.Ent.Domain.Create().
			SetId(pdid.New(pd.DomainDomain).Uuid()).
			SetTenantId(acme.Id).
			SetHost(host).
			SetPurpose("app").
			SetState("active").
			SetToken("loft-test").
			SetSource("custom").
			SetDateUpdated(time.Now()).
			Exec(ctx))
	}
	ops, err := admin.Operators(map[string]string{"alice": key})
	x.NoError(err)
	routes := admin.Routes{S: d.s, C: d.c, Operators: ops, Ask: admin.NewIssuance(cmd.AskConfig{HostBurst: 1, HostEvery: time.Hour, Burst: 2, Every: time.Hour})}
	srv := httptest.NewServer(routes.AskHandler())
	t.Cleanup(srv.Close)
	ask := func(host string) int {
		res, err := http.Get(srv.URL + "/internal/tls-ask?domain=" + host)
		x.NoError(err)
		res.Body.Close()
		return res.StatusCode
	}

	for _, n := range []int{1, 2, 3} {
		x.Equal(http.StatusNotFound, ask("nobody.example"), "nobody's, the %d. time", n)
	}
	x.Equal(http.StatusOK, ask("a.acme.example"), "nobody's spent no yes")
	x.Equal(http.StatusTooManyRequests, ask("a.acme.example"), "a's own rate")
	x.Equal(http.StatusOK, ask("B.acme.example."), "as a proxy may spell it")
	x.Equal(http.StatusTooManyRequests, ask("b.acme.example"))

	for _, path := range []string{app.TenantService_List_FullMethodName, "/"} {
		req, err := http.NewRequest(http.MethodPost, srv.URL+path, bytes.NewReader([]byte(`{}`)))
		x.NoError(err)
		req.Header.Set("Authorization", "Bearer "+key)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Connect-Protocol-Version", "1")
		res, err := http.DefaultClient.Do(req)
		x.NoError(err)
		res.Body.Close()
		x.Contains([]int{http.StatusNotFound, http.StatusMethodNotAllowed}, res.StatusCode, "%s: no operator's door on the ask's listener", path)
	}
}
