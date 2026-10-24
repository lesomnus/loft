package admincli_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/lesomnus/payday/pdid"
	"github.com/lesomnus/payday/pdtest"
	"github.com/lesomnus/xli"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/encoding/protojson"

	app "github.com/lesomnus/rove"
	"github.com/lesomnus/rove/cli"
	admincli "github.com/lesomnus/rove/cli/admin"
	"github.com/lesomnus/rove/cmd"
	"github.com/lesomnus/rove/internal/ent/event"
	"github.com/lesomnus/rove/internal/ent/holder"
	"github.com/lesomnus/rove/internal/ent/tenant"
	"github.com/lesomnus/rove/internal/ent/tenantcontract"
	"github.com/lesomnus/rove/server/admin"
	"github.com/lesomnus/rove/server/pd"
)

const key = "an-operators-key-long-enough-to-be-one"

// run runs a command and answers what it printed.
func run(t *testing.T, k *xli.Command, args ...string) (string, error) {
	t.Helper()
	r, w, err := os.Pipe()
	require.NoError(t, err)
	was := os.Stdout
	os.Stdout = w
	err = k.Run(t.Context(), args)
	os.Stdout = was
	require.NoError(t, w.Close())
	b, _ := io.ReadAll(r)
	return string(b), err
}

type deployment struct {
	c   cmd.Config
	s   *cmd.Server
	url string
}

func deploy(t *testing.T) *deployment {
	t.Helper()
	x := require.New(t)
	c := cmd.Config{}
	c.Db.Driver, c.Db.Dsn = pdtest.DB(t)
	c.Watch.Broker = "memory"
	c.App.Files = t.TempDir()
	c.Auth.Roster.Db.Driver, c.Auth.Roster.Db.Dsn = pdtest.DB(t)
	_, err := run(t, cli.NewCmdInit(&c), "--tenant", "acme", "--login", "owner", "--password", "owner1234")
	x.NoError(err)
	_, err = run(t, cli.NewCmdHolder(&c), "add", "--tenant", "acme", "--login", "kim", "--role", "member", "--password", "kim-password")
	x.NoError(err)

	s, err := cmd.Build(t.Context(), c)
	x.NoError(err)
	t.Cleanup(func() { s.Close() })
	ops, err := admin.Operators(map[string]string{"alice": key})
	x.NoError(err)
	srv := httptest.NewServer(admin.Routes{S: s, C: c, Operators: ops}.Handler())
	t.Cleanup(srv.Close)
	return &deployment{c: c, s: s, url: srv.URL}
}

// admin runs `rove-admin` against the deployment's entry point.
func (d *deployment) admin(t *testing.T, args ...string) (string, error) {
	t.Helper()
	return run(t, admincli.Cmd(&d.c), append([]string{"--url", d.url, "--key", key}, args...)...)
}

// TestAnOperatorWorksFromOutsideTheBox: what an operator does to a workspace
// -- finds it, closes and restores it, names an owner, moves its address,
// writes its contract and a hold -- asked of the entry point with their key,
// from wherever they are.
func TestAnOperatorWorksFromOutsideTheBox(t *testing.T) {
	x := require.New(t)
	d := deploy(t)
	ctx := context.Background()
	must := func(args ...string) string {
		t.Helper()
		out, err := d.admin(t, args...)
		x.NoError(err, "%s: %s", strings.Join(args, " "), out)
		return out
	}

	out := must("workspace", "ls")
	x.Contains(out, "@acme\t")
	x.Contains(out, "owners owner")
	x.Contains(out, "2 people")
	x.Contains(must("workspace", "ls", "--q", "ACM"), "@acme", "found by a piece of its address")
	x.NotContains(must("workspace", "ls", "--q", "nothing-like-it"), "@acme")

	out = must("workspace", "show", "@acme")
	x.Contains(out, "member\t@kim\tmember")

	_, err := d.admin(t, "workspace", "close", "--reason", "asked", "acme")
	x.ErrorContains(err, "--yes", "a change nobody said yes to")
	out = must("workspace", "close", "--reason", "asked", "--yes", "acme")
	x.Contains(out, "closed")
	x.Contains(out, "by operator")
	x.Contains(must("workspace", "ls", "--closed"), "@acme")
	_, err = d.admin(t, "workspace", "close", "--yes", "acme")
	x.ErrorContains(err, "409", "closed already")
	x.NotContains(must("workspace", "restore", "--yes", "acme"), "closed")
	x.NotContains(must("workspace", "ls", "--closed"), "@acme", "open again")

	out = must("workspace", "owner", "--member", "@kim", "--yes", "acme")
	x.Contains(out, "owners kim,owner")
	_, err = d.admin(t, "workspace", "owner", "--member", "nobody", "--yes", "acme")
	x.ErrorContains(err, "404")

	out = must("workspace", "alias", "--to", "acme-two", "--yes", "acme")
	x.Contains(out, "@acme-two\t")
	tn, err := d.s.Ent.Tenant.Query().Where(tenant.Alias("acme-two")).Only(ctx)
	x.NoError(err)
	_, err = d.admin(t, "workspace", "alias", "--to", "acme", "--yes", "acme-two")
	x.ErrorContains(err, "held", "the old address is held, and not taken back by accident")

	out = must("contract", "set", "--name", "pilot", "--yes", "acme-two")
	x.Contains(out, "contract\t")
	x.Contains(out, "pilot\tthen retire")

	out = must("hold", "place", "--why", "a case", "--yes", "acme-two")
	x.Contains(out, "a case\theld")
	i := strings.Index(out, "hold\t")
	x.Positive(i)
	id := strings.Fields(out[i:])[1]
	x.Contains(must("hold", "lift", "--yes", id), "a case\tlifted")

	// What the history says of the owner named, and who did it, is the
	// deployment's: an operator's change is written as its own work.
	owners, err := d.s.Ent.Holder.Query().Where(holder.TenantId(tn.Id), holder.Role("owner")).Count(ctx)
	x.NoError(err)
	x.Equal(2, owners)
	kim, err := d.s.Ent.Holder.Query().Where(holder.TenantId(tn.Id), holder.Alias("kim")).Only(ctx)
	x.NoError(err)
	ev, err := d.s.Ent.Event.Query().Where(event.TenantId(tn.Id), event.Kind("party.role")).Only(ctx)
	x.NoError(err)
	x.Nil(ev.ActorId, "the deployment's own work, and nobody's in the workspace")
	// Who by identifier, and never by name: the sentence names them when it
	// is read (payday#151).
	x.NotNil(ev.Value)
	said := &app.PartyRoleSet{}
	x.NoError(ev.Value.UnmarshalTo(said))
	x.Equal("operator", said.GetBy())
	x.Equal("owner", said.GetRole())
	x.Equal(pdid.Id(kim.Id).Bytes(), said.GetHolder())
	x.NotContains(protojson.Format(ev.Value), kim.Name)
}

// TestAnOperatorWritesEveryTermOfAContract: `contract set` at the entry point
// writes every term a contract has -- whether a destroyed workspace's trail
// goes whole (payday#115), and from when -- and `show` says each, beside how
// long the workspace keeps its history by its own settings (D36), which an
// operator reads and does not write.
func TestAnOperatorWritesEveryTermOfAContract(t *testing.T) {
	x := require.New(t)
	d := deploy(t)
	ctx := context.Background()

	tn, err := d.s.Ent.Tenant.Query().Where(tenant.Alias("acme")).Only(ctx)
	x.NoError(err)
	_, err = d.s.Ent.Tenant.UpdateOneId(tn.Id).SetKeepDays(730).SetRestoreDays(90).Save(ctx)
	x.NoError(err)
	// An operator fourteen hours ahead of UTC writes it, and one twelve behind
	// reads it, for each of whom the first of January in UTC is another day:
	// each is said the day it was given as.
	was := time.Local
	t.Cleanup(func() { time.Local = was })
	time.Local = time.FixedZone("UTC+14", 14*60*60)

	out, err := d.admin(t, "contract", "set", "--name", "enterprise", "--desc", "agreement 2026-7", "--purge-floors",
		"--effective", "2026-01-01", "--yes", "acme")
	x.NoError(err, out)
	x.Contains(out, "enterprise\tthen purge\tagreement 2026-7")
	x.Contains(out, "keeps\thistory 730d, access record the deployment's, restore 90d", "the workspace's own settings")

	v, err := d.s.Ent.TenantContract.Query().Where(tenantcontract.TenantIdEQ(tn.Id)).Only(ctx)
	x.NoError(err)
	x.Equal("enterprise", v.Name)
	x.Equal("agreement 2026-7", v.Desc)
	x.True(v.PurgeFloors)
	x.Equal("2026-01-01", v.DateEffective.UTC().Format(time.DateOnly), "the day it was given as")
	x.Contains(out, "contract\t2026-01-01\tenterprise", "said as the day it was given as, whatever the operator's zone")
	time.Local = time.FixedZone("UTC-12", -12*60*60)
	shown, err := d.admin(t, "workspace", "show", "acme")
	x.NoError(err, shown)
	x.Contains(shown, "contract\t2026-01-01\tenterprise", "read as another day, by an operator in another zone")

	// One that says nothing more retires.
	out, err = d.admin(t, "contract", "set", "--name", "standard", "--yes", "acme")
	x.NoError(err, out)
	x.Contains(out, "standard\tthen retire")

	_, err = d.admin(t, "contract", "set", "--name", "pro", "--keep", "60", "--yes", "acme")
	x.Error(err, "a workspace's own window, written by an operator")
}

// TestTheEntryPointAnswersOnlyAnOperatorsKey: a request without a key, or
// with one nobody holds, is refused -- and the public binary's server has no
// such door at all.
func TestTheEntryPointAnswersOnlyAnOperatorsKey(t *testing.T) {
	x := require.New(t)
	d := deploy(t)

	_, err := run(t, admincli.Cmd(&d.c), "--url", d.url, "--key", "somebody-elses-key-of-a-good-length-too", "workspace", "ls")
	x.ErrorContains(err, "401")
	res, err := http.Get(d.url + "/admin/workspaces")
	x.NoError(err)
	res.Body.Close()
	x.Equal(http.StatusUnauthorized, res.StatusCode)

	// What a reverse proxy asks asks for no key, and says no to a host that
	// is nobody's.
	res, err = http.Get(d.url + "/internal/tls-ask?domain=nobody.example")
	x.NoError(err)
	res.Body.Close()
	x.Equal(http.StatusNotFound, res.StatusCode)

	mux := http.NewServeMux()
	d.s.Routes(mux, d.c)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/internal/tls-ask?domain=nobody.example", nil))
	x.Equal(http.StatusNotFound, rec.Code)
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/admin/workspaces", nil))
	x.Equal(http.StatusNotFound, rec.Code, "the public server serves no operator's door")

	_, err = admin.Operators(map[string]string{"bob": "short"})
	x.ErrorContains(err, "admin.keys.bob")
	_, err = admin.Operators(map[string]string{"carol": "env:ROVE_NO_SUCH_KEY_HERE"})
	x.ErrorContains(err, "admin.keys.carol")
}

// TestACertificateIsAskedForADomainInUse: a reverse proxy is told to take a
// certificate for a workspace's domain that is verified and in use -- ready,
// active, or legacy, whose labels and pages still open -- and for no other:
// not one waiting for its TXT record, not one that lapsed when its DNS
// stopped saying it is here (rove#155), not one retired.
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
		x.NoError(d.s.Ent.TenantDomain.Create().
			SetId(pdid.New(pd.TenantDomainDomain).Uuid()).
			SetTenantId(acme.Id).
			SetHost(host).
			SetPurpose("app").
			SetState(state).
			SetToken("rove-test").
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
		x.NoError(d.s.Ent.TenantDomain.Create().
			SetId(pdid.New(pd.TenantDomainDomain).Uuid()).
			SetTenantId(acme.Id).
			SetHost(host).
			SetPurpose("app").
			SetState("active").
			SetToken("rove-test").
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

	for _, path := range []string{"/admin/workspaces", "/"} {
		req, err := http.NewRequest(http.MethodGet, srv.URL+path, nil)
		x.NoError(err)
		req.Header.Set("Authorization", "Bearer "+key)
		res, err := http.DefaultClient.Do(req)
		x.NoError(err)
		res.Body.Close()
		x.Equal(http.StatusNotFound, res.StatusCode, "%s: no operator's door on the ask's listener", path)
	}
}
