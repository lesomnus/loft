package cli_test

import (
	"bytes"
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/lesomnus/payday/config"
	"github.com/lesomnus/payday/pdid"
	"github.com/lesomnus/payday/pdtest"
	rostercli "github.com/lesomnus/roster/cli"
	rostercmd "github.com/lesomnus/roster/cmd"
	"github.com/lesomnus/roster/rstr"
	"github.com/lesomnus/xli"
	"github.com/lesomnus/z"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/metadata"

	app "github.com/lesomnus/rove"
	"github.com/lesomnus/rove/cli"
	"github.com/lesomnus/rove/cmd"
	"github.com/lesomnus/rove/internal/ent/holder"
	"github.com/lesomnus/rove/internal/identity"
	rovesession "github.com/lesomnus/rove/server/session"
)

// before is a deployment whose logins name no account: made before roster held
// people. Two workspaces, both with somebody called kim; the owner called
// `admin`, which is what `rove init` called one and the name roster gives the
// first holder of every tenant it makes; a login that ended; a table of
// sessions from before a session was an account's; and the table the
// passwords were kept in.
type before struct {
	acme, beta                     pdid.Id
	admin, kim, gone, kimElsewhere pdid.Id
}

func deploymentFromBefore(t *testing.T, c cmd.Config) before {
	t.Helper()
	x := require.New(t)
	ctx := context.Background()

	s, err := cmd.Build(ctx, c)
	x.NoError(err)
	defer s.Close()
	x.NoError(cli.Migrate(ctx, s))

	out := before{}
	workspace := func(alias string) (pdid.Id, func(alias, role string) pdid.Id) {
		v, err := s.Base.Tenant().Add(ctx, app.TenantAddRequest_builder{Alias: alias, Name: strings.ToUpper(alias)}.Build())
		x.NoError(err)
		id, _ := pdid.From(v.GetId())
		tref := app.TenantRef_builder{Id: v.GetId()}.Build()
		return id, func(alias, role string) pdid.Id {
			h, err := s.Base.Holder().Add(ctx, app.HolderAddRequest_builder{Tenant: tref, Alias: alias, Name: alias, Role: z.Ptr(role)}.Build())
			x.NoError(err)
			_, err = s.Base.Party().Add(ctx, app.PartyAddRequest_builder{Tenant: tref, Name: alias, Kind: "person", Holder: app.HolderRef_builder{Id: h.GetId()}.Build()}.Build())
			x.NoError(err)
			id, _ := pdid.From(h.GetId())
			return id
		}
	}
	var acme, beta func(alias, role string) pdid.Id
	out.acme, acme = workspace("acme")
	out.beta, beta = workspace("beta")
	out.admin = acme("admin", "owner")
	out.kim = acme("kim", "member")
	out.gone = acme("gone", "member")
	_, err = s.Base.Holder().Erase(ctx, app.HolderRef_builder{Id: out.gone.Bytes()}.Build())
	x.NoError(err)
	out.kimElsewhere = beta("kim", "owner")

	_, err = s.Db.ExecContext(ctx, "DROP TABLE session")
	x.NoError(err)
	_, err = s.Db.ExecContext(ctx, "CREATE TABLE session (id uuid PRIMARY KEY, tenant_id uuid NOT NULL, holder_id uuid NOT NULL, secret text)")
	x.NoError(err)
	_, err = s.Db.ExecContext(ctx, "INSERT INTO session (id, tenant_id, holder_id, secret) VALUES ($1, $2, $3, $4)", pdid.New(38).String(), out.acme.String(), out.kim.String(), "x")
	x.NoError(err)

	_, err = s.Db.ExecContext(ctx, "CREATE TABLE credential (id uuid PRIMARY KEY, secret text NOT NULL)")
	x.NoError(err)
	_, err = s.Db.ExecContext(ctx, "INSERT INTO credential (id, secret) VALUES ($1, $2)", pdid.New(37).String(), "$argon2id$v=19$m=65536,t=3,p=4$...")
	x.NoError(err)

	return out
}

var passwordLine = regexp.MustCompile(`(?m)^@(\S+)/(\S+)  login (\S+)  password (\S+)$`)

type adopted struct{ login, password string }

func passwordsOf(out string) map[string]adopted {
	v := map[string]adopted{}
	for _, m := range passwordLine.FindAllStringSubmatch(out, -1) {
		v[m[1]+"/"+m[2]] = adopted{login: m[3], password: m[4]}
	}
	return v
}

func embeddedConfig(t *testing.T) cmd.Config {
	c := cmd.Config{}
	c.Db.Driver, c.Db.Dsn = pdtest.DB(t)
	c.Watch.Broker = "memory"
	c.App.Files = t.TempDir()
	c.Auth.Roster.Db.Driver, c.Auth.Roster.Db.Dsn = pdtest.DB(t)
	return c
}

// TestADeploymentFromBeforeRosterIsBroughtIntoIt.
//
// Every login is made an account in the directory at the roster in this
// process with the identifier it has here, so that what it did is still its
// own; each is given a new password, shown once, and signs in with it as who
// it was. A name two workspaces had becomes two logins.
func TestADeploymentFromBeforeRosterIsBroughtIntoIt(t *testing.T) {
	x := require.New(t)
	ctx := context.Background()

	c := embeddedConfig(t)
	was := deploymentFromBefore(t, c)

	// What somebody tries first, and is told no before roster is written to.
	err := cli.NewCmdInit(&c).Run(ctx, []string{"--tenant", "acme", "--login", "admin", "--password", "admin1234"})
	x.ErrorContains(err, "here already")

	out := run(t, cli.NewCmdIdentity(&c), "migrate")
	got := passwordsOf(out)
	x.Len(got, 3, out)
	x.Equal("admin", got["acme/admin"].login, "roster's own admin kept the name from the person who had it")
	x.Equal("kim", got["acme/kim"].login)
	x.Equal("kim-beta", got["beta/kim"].login, "two kims and one directory")
	x.Contains(out, "dropped `credential`")

	s, err := cmd.Build(ctx, c)
	x.NoError(err)
	defer s.Close()

	for at, id := range map[string]pdid.Id{"acme/admin": was.admin, "acme/kim": was.kim, "beta/kim": was.kimElsewhere} {
		p, err := s.Identity.Verify(ctx, got[at].login, got[at].password)
		x.NoError(err, at)
		x.Equal(id, p.Id, "%s is somebody new at roster", at)
		h, err := s.Ent.Holder.Get(ctx, id.Uuid())
		x.NoError(err)
		x.Equal(id.Uuid(), *h.Account, "%s is the membership of an account other than its own", at)
	}
	h, err := s.Ent.Holder.Query().Where(holder.Id(was.gone.Uuid())).Only(ctx)
	x.NoError(err)
	x.Nil(h.Account, "a login that ended was brought in")

	login := rovesession.SignIn(s.Sessions, s.Identity, s.Ent, nil)
	r := httptest.NewRequest(http.MethodPost, "/session", bytes.NewBufferString(`{"login":"admin","password":"`+got["acme/admin"].password+`"}`))
	rec := httptest.NewRecorder()
	login.ServeHTTP(rec, r)
	x.Equal(http.StatusNoContent, rec.Code)

	g, err := s.Grpc(ctx, c)
	x.NoError(err)
	conn := pdtest.Serve(t, g)
	cookie := rec.Result().Cookies()[0]
	me, err := app.NewPartyServiceClient(conn).Me(metadata.AppendToOutgoingContext(ctx, "cookie", cookie.Name+"="+cookie.Value), &app.PartyMeRequest{})
	x.NoError(err)
	x.Equal("owner", me.GetRole(), "the owner came back as somebody else")

	n := 0
	x.NoError(s.Db.QueryRowContext(ctx, "SELECT count(*) FROM session").Scan(&n))
	x.Equal(1, n, "the sessions from before outlived the upgrade, or the new one was not kept")

	again := run(t, cli.NewCmdIdentity(&c), "migrate")
	x.Contains(again, "nobody to bring in")
	x.NotContains(again, "password ")
	x.NotContains(again, "dropped")
}

// TestADeploymentToTryThingsOnIsBroughtInWithOnePassword.
func TestADeploymentToTryThingsOnIsBroughtInWithOnePassword(t *testing.T) {
	x := require.New(t)
	ctx := context.Background()

	c := embeddedConfig(t)
	deploymentFromBefore(t, c)

	// One roster would not take leaves nobody half brought in.
	err := cli.NewCmdIdentity(&c).Run(ctx, []string{"migrate", "--password", "short"})
	x.ErrorContains(err, "admin")

	out := run(t, cli.NewCmdIdentity(&c), "migrate", "--password", "demo1234")
	x.Contains(out, "3 account(s), every one with the password given")

	s, err := cmd.Build(ctx, c)
	x.NoError(err)
	defer s.Close()
	for _, login := range []string{"admin", "kim", "kim-beta"} {
		_, err := s.Identity.Verify(ctx, login, "demo1234")
		x.NoError(err, login)
	}
}

// external is a roster of its own on a TCP listener, with a directory and a
// deployment key installed into it: what a roster operator stands up.
func external(t *testing.T) (addr, key string, s *rostercmd.Server) {
	t.Helper()
	x := require.New(t)
	ctx := t.Context()

	drv, dsn := pdtest.DB(t)
	cdrv, cdsn := pdtest.DB(t)
	c := rostercmd.Config{
		Db:      config.DbConfig{Driver: drv, Dsn: dsn},
		Watch:   config.WatchConfig{Broker: config.BrokerMemory},
		Control: rostercmd.ControlConfig{Db: config.DbConfig{Driver: cdrv, Dsn: cdsn}},
	}
	k := rostercli.NewCmdInit(&c)
	k.Writer = io.Discard
	x.NoError(k.Run(ctx, nil))
	s, err := rostercmd.Build(ctx, c)
	x.NoError(err)
	_, err = s.Ungated.Tenant().Add(ctx, rstr.TenantAddRequest_builder{Alias: "accounts"}.Build())
	x.NoError(err)
	x.NoError(s.Close())

	key = strings.TrimSpace(stdout(t, rostercli.NewCmdControl(&c), "key", "add", "--allow", "/roster.NominationService/List", identity.Agent))
	x.NoError(rostercli.NewCmdApp(&c).Run(ctx, []string{"install", "--tenant", "accounts", "--role", strings.Join(identity.AgentMethods, ","), identity.Agent}))

	s, err = rostercmd.Build(ctx, c)
	x.NoError(err)
	t.Cleanup(func() { s.Close() })
	g, err := s.Grpc(ctx, rostercmd.Config{})
	x.NoError(err)
	l, err := net.Listen("tcp", "127.0.0.1:0")
	x.NoError(err)
	go g.Serve(l)
	t.Cleanup(g.Stop)

	return l.Addr().String(), key, s
}

func stdout(t *testing.T, k *xli.Command, args ...string) string {
	t.Helper()
	x := require.New(t)
	r, w, err := os.Pipe()
	x.NoError(err)
	was := os.Stdout
	os.Stdout = w
	err = k.Run(t.Context(), args)
	os.Stdout = was
	x.NoError(w.Close())
	b, e := io.ReadAll(r)
	x.NoError(e)
	x.NoError(err, "%s: %s", strings.Join(args, " "), b)
	return string(b)
}

// TestARosterOfItsOwnIsToldWhatToMake: one this process cannot write to is
// its operator's, and is told the identifiers to make the accounts with. What
// the operator made is linked when this runs again.
func TestARosterOfItsOwnIsToldWhatToMake(t *testing.T) {
	x := require.New(t)
	ctx := context.Background()

	c := embeddedConfig(t)
	was := deploymentFromBefore(t, c)

	addr, key, roster := external(t)
	c.Auth.Roster = cmd.Config{}.Auth.Roster
	c.Auth.Roster.Addr = addr
	c.Auth.Roster.Insecure = true
	c.Auth.Roster.Key = key
	c.Auth.Roster.Directory = "accounts"

	out := run(t, cli.NewCmdIdentity(&c), "migrate")
	x.Contains(out, `roster holder add @accounts/kim '{"id":"`+was.kim.String()+`","name":"kim"}'`)
	x.Contains(out, `roster holder add @accounts/kim '{"id":"`+was.kimElsewhere.String()+`","name":"kim"}'`, "a second kim the operator has to name")
	x.NotContains(out, was.gone.String(), "a login that ended")
	x.Contains(out, "dropped `credential`")

	// What the operator does with it.
	_, err := roster.Ungated.Holder().Add(ctx, rstr.HolderAddRequest_builder{
		Id:     was.kim.Bytes(),
		Tenant: rstr.TenantRef_builder{Alias: z.Ptr("accounts")}.Build(),
		Alias:  "kim",
	}.Build())
	x.NoError(err)

	out = run(t, cli.NewCmdIdentity(&c), "migrate")
	x.Contains(out, "@acme/kim  linked")
	x.NotContains(out, `"id":"`+was.kim.String())
}
