package cli_test

import (
	"bytes"
	"context"
	"encoding/base64"
	"io"
	"net"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/lesomnus/payday/auth/authsession"
	"github.com/lesomnus/payday/config"
	"github.com/lesomnus/payday/frame"
	"github.com/lesomnus/payday/pdid"
	"github.com/lesomnus/payday/pdtest"
	rostercli "github.com/lesomnus/roster/cli"
	rostercmd "github.com/lesomnus/roster/cmd"
	"github.com/lesomnus/roster/rstr"
	"github.com/lesomnus/z"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	app "github.com/lesomnus/loft"
	"github.com/lesomnus/loft/cli"
	"github.com/lesomnus/loft/cmd"
	"github.com/lesomnus/loft/internal/ent"
	entaccess "github.com/lesomnus/loft/internal/ent/access"
	"github.com/lesomnus/loft/internal/ent/holder"
	"github.com/lesomnus/loft/internal/ent/levyannotice"
	"github.com/lesomnus/loft/internal/ent/predicate"
	entsession "github.com/lesomnus/loft/internal/ent/session"
	enttenant "github.com/lesomnus/loft/internal/ent/tenant"
	"github.com/lesomnus/loft/internal/identity"
	"github.com/lesomnus/loft/internal/levyantest"
	"github.com/lesomnus/loft/server/admin"
	"github.com/lesomnus/loft/server/operator"
)

// entry is the operator's entry point on the deployment `c` names, which
// alice holds a key to: one already there, migrated as `loft migrate` leaves
// it before anything serves.
func entry(t *testing.T, c cmd.Config) *deployment {
	t.Helper()
	x := require.New(t)
	s, err := cmd.Build(t.Context(), c)
	x.NoError(err)
	t.Cleanup(func() { s.Close() })
	x.NoError(cli.Migrate(t.Context(), s))
	ops, err := admin.Operators(map[string]string{"alice": operatorKey})
	x.NoError(err)
	h, err := admin.Routes{S: s, C: c, Operators: ops}.Handler(t.Context())
	x.NoError(err)
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return &deployment{c: c, s: s, url: srv.URL, key: keyRef(t)}
}

// unlinked is a deployment whose logins name no account, as a restore of an
// export from another deployment leaves its members: two workspaces, both
// with somebody called kim; the owner called `admin`, the name roster gives
// the first holder of every tenant it makes; and a login that ended.
type unlinked struct {
	acme, beta                     pdid.Id
	admin, kim, gone, kimElsewhere pdid.Id
}

func membersWithNoAccount(t *testing.T, c cmd.Config) unlinked {
	t.Helper()
	x := require.New(t)
	ctx := context.Background()
	s, err := cmd.Build(ctx, c)
	x.NoError(err)
	defer s.Close()
	x.NoError(cli.Migrate(ctx, s))

	out := unlinked{}
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

// TestMembersWithNoAccountAreGivenOne: every login that names no account is
// made an account in the directory at the roster in this process with the
// identifier it has here, given a new password said once, its membership
// linked as the operator's write -- and run again it makes nothing.
func TestMembersWithNoAccountAreGivenOne(t *testing.T) {
	x := require.New(t)
	ctx := context.Background()
	c := embeddedConfig(t)
	was := membersWithNoAccount(t, c)
	d := entry(t, c)

	out, err := d.loft(t, "identity", "link")
	x.NoError(err, out)
	got := passwordsOf(out)
	x.Len(got, 3, out)
	x.Equal("admin", got["acme/admin"].login, "roster's own admin kept the name from the person who had it")
	x.Equal("kim", got["acme/kim"].login)
	x.Equal("kim-beta", got["beta/kim"].login, "two kims and one directory")

	for at, id := range map[string]pdid.Id{"acme/admin": was.admin, "acme/kim": was.kim, "beta/kim": was.kimElsewhere} {
		p, err := d.s.Identity.Verify(ctx, got[at].login, got[at].password)
		x.NoError(err, at)
		x.Equal(id, p.Id, "%s is somebody new at roster", at)
		h, err := d.s.Ent.Holder.Get(ctx, id.Uuid())
		x.NoError(err)
		x.Equal(id.Uuid(), *h.Account, "%s is the membership of an account other than its own", at)
	}
	h, err := d.s.Ent.Holder.Query().Where(holder.Id(was.gone.Uuid())).Only(ctx)
	x.NoError(err)
	x.Nil(h.Account, "a login that ended was brought in")
	n, err := d.s.Ent.Access.Query().Where(entaccess.Kind("write"), entaccess.ActorId(operator.Id("alice").Uuid())).Count(ctx)
	x.NoError(err)
	x.Positive(n, "the links are the operator's writes")

	again, err := d.loft(t, "identity", "link")
	x.NoError(err)
	x.Contains(again, "nobody to bring in")
	x.NotContains(again, "password ")
}

// TestMembersToTryThingsOnAreGivenOnePassword: one password roster would not
// take leaves nobody half brought in; one it takes is everybody's.
func TestMembersToTryThingsOnAreGivenOnePassword(t *testing.T) {
	x := require.New(t)
	ctx := context.Background()
	c := embeddedConfig(t)
	membersWithNoAccount(t, c)
	d := entry(t, c)

	_, err := d.loft(t, "identity", "link", "--password", "short")
	x.ErrorContains(err, "admin")
	out, err := d.loft(t, "identity", "link", "--password", "demo1234")
	x.NoError(err, out)
	x.Contains(out, "3 account(s), every one with the password given")
	for _, login := range []string{"admin", "kim", "kim-beta"} {
		_, err := d.s.Identity.Verify(ctx, login, "demo1234")
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

	out, err := ran(t, rostercli.NewCmdControl(&c), "key", "add", "--allow", "/roster.NominationService/List", identity.Agent)
	x.NoError(err, out)
	key = strings.TrimSpace(out)
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

// TestARosterOfItsOwnIsToldWhatToMake: one this process cannot write to is
// its operator's, and is told the identifiers to make the accounts with.
// What the operator made is linked when this runs again.
func TestARosterOfItsOwnIsToldWhatToMake(t *testing.T) {
	x := require.New(t)
	ctx := context.Background()
	c := embeddedConfig(t)
	was := membersWithNoAccount(t, c)
	addr, rkey, roster := external(t)
	c.Auth.Roster = cmd.Config{}.Auth.Roster
	c.Auth.Roster.Addr = addr
	c.Auth.Roster.Insecure = true
	c.Auth.Roster.Key = rkey
	c.Auth.Roster.Directory = "accounts"
	d := entry(t, c)

	out, err := d.loft(t, "identity", "link")
	x.NoError(err, out)
	x.Contains(out, `roster holder add @accounts/kim '{"id":"`+was.kim.String()+`","name":"kim"}'`)
	x.Contains(out, `roster holder add @accounts/kim '{"id":"`+was.kimElsewhere.String()+`","name":"kim"}'`)
	x.NotContains(out, was.gone.String(), "a login that ended")

	_, err = roster.Ungated.Holder().Add(ctx, rstr.HolderAddRequest_builder{
		Id:     was.kim.Bytes(),
		Tenant: rstr.TenantRef_builder{Alias: z.Ptr("accounts")}.Build(),
		Alias:  "kim",
	}.Build())
	x.NoError(err)
	out, err = d.loft(t, "identity", "link")
	x.NoError(err, out)
	x.Contains(out, "@acme/kim  linked")
	x.NotContains(out, `"id":"`+was.kim.String())

	// And an account is made at that roster, and given its password there,
	// not here: refused as the shell refused it, before anything is asked of
	// roster.
	for _, args := range [][]string{
		{"holder", "add", "--tenant", "acme", "--login", "lee"},
		{"holder", "add", "--tenant", "acme", "--login", "lee", "--password", "lee-password"},
		{"holder", "password", "--login", "kim"},
		{"holder", "password", "--login", "kim", "--password", "kim-password"},
	} {
		_, err := d.loft(t, args...)
		x.Equal(codes.FailedPrecondition, status.Code(err), "%v: %v", args, err)
		x.ErrorContains(err, "auth.roster names a roster of its own: accounts are made and given passwords there", "%v", args)
	}
	_, err = roster.Ungated.Holder().Get(ctx, rstr.HolderGetRequest_builder{Ref: rstr.HolderRef_builder{Slug: rstr.HolderRefBySlug_builder{
		Tenant: rstr.TenantRef_builder{Alias: z.Ptr("accounts")}.Build(),
		Alias:  z.Ptr("lee"),
	}.Build()}.Build()}.Build())
	x.Equal(codes.NotFound, status.Code(err), "lee was made at roster: %v", err)
}

// aDeployment is the roster in this process with acme's owner, kim and lee
// -- put there by the operator -- and kim's own workspace beta.
func aDeployment(t *testing.T, opts ...func(*cmd.Config)) *deployment {
	t.Helper()
	x := require.New(t)
	c := embeddedConfig(t)
	for _, opt := range opts {
		opt(&c)
	}
	_, err := ran(t, cli.NewCmdInit(&c), "--tenant", "acme", "--login", "owner", "--password", "owner1234")
	x.NoError(err)
	d := entry(t, c)
	for _, login := range []string{"kim", "lee"} {
		out, err := d.loft(t, "holder", "add", "--tenant", "acme", "--login", login, "--password", login+"-password")
		x.NoError(err, out)
		x.Contains(out, "holder   @acme/"+login)
	}
	_, err = ran(t, cli.NewCmdInit(&c), "--tenant", "beta", "--login", "kim")
	x.NoError(err)
	return d
}

func accountOf(t *testing.T, s *cmd.Server, login string) pdid.Id {
	t.Helper()
	h, err := s.Ent.Holder.Query().Where(holder.Alias(login), holder.DateErasedIsNil()).First(context.Background())
	require.NoError(t, err)
	require.NotNil(t, h.Account)
	return pdid.Id(*h.Account)
}

func membershipsOf(t *testing.T, s *cmd.Server, account pdid.Id) []*ent.Holder {
	t.Helper()
	hs, err := s.Ent.Holder.Query().Where(holder.Account(account.Uuid())).All(context.Background())
	require.NoError(t, err)
	return hs
}

// TestAnOperatorPutsAnAccountInAndGivesItAPassword: `holder add` makes an
// account with a password said once when it is new, and its membership and
// person record as the operator's writes; `holder password` gives it a new
// one and ends everywhere it is signed in.
func TestAnOperatorPutsAnAccountInAndGivesItAPassword(t *testing.T) {
	x := require.New(t)
	ctx := context.Background()
	d := aDeployment(t)

	out, err := d.loft(t, "holder", "add", "--tenant", "acme", "--login", "park", "--name", "박", "--role", "manager")
	x.NoError(err, out)
	m := regexp.MustCompile(`(?m)^password (\S+)$`).FindStringSubmatch(out)
	x.NotNil(m, "a new account's password is said once: %s", out)
	_, err = d.s.Identity.Verify(ctx, "park", m[1])
	x.NoError(err)
	_, err = d.loft(t, "holder", "add", "--tenant", "acme", "--login", "park")
	x.ErrorContains(err, "in @acme already")
	_, err = d.loft(t, "holder", "add", "--tenant", "acme", "--login", "kim", "--password", "another")
	x.ErrorContains(err, "an account already")
	_, err = d.loft(t, "holder", "add", "--tenant", "acme", "--login", "x", "--role", "king")
	x.ErrorContains(err, "role")
	park := accountOf(t, d.s, "park")
	n, err := d.s.Ent.Access.Query().Where(entaccess.Kind("write"), entaccess.ActorId(operator.Id("alice").Uuid())).Count(ctx)
	x.NoError(err)
	x.Positive(n, "the membership and its person record are the operator's writes")

	_, _, err = d.s.Sessions.Mint(ctx, authsession.Session{Id: park.String(), Grant: frame.Whole()})
	x.NoError(err)
	out, err = d.loft(t, "holder", "password", "--login", "park", "--password", "park-new-password")
	x.NoError(err, out)
	x.Contains(out, "account  park")
	_, err = d.s.Identity.Verify(ctx, "park", "park-new-password")
	x.NoError(err)
	sessions, err := d.s.Ent.Session.Query().Where(entsession.Account(park.Uuid())).Count(ctx)
	x.NoError(err)
	x.Zero(sessions, "where they were signed in")
	_, err = d.loft(t, "holder", "password", "--login", "nobody")
	x.ErrorContains(err, "no account nobody")
}

// TestAnOperatorDepartsAnAccountAtOnce: somebody who asked to be forgotten
// now leaves every workspace their account is in, says what became of each,
// is signed out where they were, and -- on the roster in this process --
// leaves it too. Asked about first, nothing happens.
func TestAnOperatorDepartsAnAccountAtOnce(t *testing.T) {
	x := require.New(t)
	ctx := context.Background()
	d := aDeployment(t)
	kim := accountOf(t, d.s, "kim")
	_, _, err := d.s.Sessions.Mint(ctx, authsession.Session{Id: kim.String(), Grant: frame.Whole()})
	x.NoError(err)

	for _, args := range [][]string{
		{"identity", "depart"},
		{"identity", "depart", "--member", "acme/kim"},
		{"identity", "depart", "--member", "@acme/nobody"},
		{"identity", "depart", "--member", "@acme/kim", "--account", kim.String()},
	} {
		_, err := d.loft(t, args...)
		x.Error(err, "%v", args)
	}

	out, err := d.loft(t, "identity", "depart", "--member", "@acme/kim")
	x.NoError(err, out)
	x.Contains(out, "@acme: @kim, member")
	x.Contains(out, "@beta: @kim, owner")
	x.Contains(out, "nothing was done")
	_, err = d.s.Identity.Verify(ctx, "kim", "kim-password")
	x.NoError(err, "only asked about")

	out, err = d.loft(t, "identity", "depart", "--member", "@acme/kim", "--yes")
	x.NoError(err, out)
	x.Contains(out, "left @acme\n")
	x.Contains(out, "left @beta; its last owner, and nobody was there to hand it to")
	x.Contains(out, "forgot "+kim.String())
	_, err = d.s.Identity.Verify(ctx, "kim", "kim-password")
	x.ErrorIs(err, identity.ErrRefused)
	_, err = d.s.Identity.Verify(ctx, "owner", "owner1234")
	x.NoError(err, "nobody else")
	for _, h := range membershipsOf(t, d.s, kim) {
		x.NotNil(h.DateErased)
		x.Empty(h.Alias)
	}
	n, err := d.s.Ent.Session.Query().Where(entsession.Account(kim.Uuid())).Count(ctx)
	x.NoError(err)
	x.Zero(n, "where they were signed in")
}

// TestADepartureAskedAboutChangesNothing: without `--yes` a departure says,
// by the rules it then follows, what it would do in each workspace -- a last
// owner's handed on, and to whom, or to nobody; one nobody would be left in
// closed -- and changes nothing: no table of the deployment's or of the
// roster in this process, levyan told nothing, nobody signed out; only the
// access record says the operator saw whose memberships they are. Done, it
// does what it said.
func TestADepartureAskedAboutChangesNothing(t *testing.T) {
	x := require.New(t)
	ctx := context.Background()
	d := aDeployment(t, func(c *cmd.Config) { c.App.Billing.Levyan = levyantest.Start(t).Config() })
	// kim is a member of acme, owns beta -- which lee manages -- and owns
	// gamma alone.
	out, err := d.loft(t, "holder", "add", "--tenant", "beta", "--login", "lee", "--role", "manager")
	x.NoError(err, out)
	_, err = ran(t, cli.NewCmdInit(&d.c), "--tenant", "gamma", "--login", "kim")
	x.NoError(err)
	kim, lee := accountOf(t, d.s, "kim"), accountOf(t, d.s, "lee")
	_, _, err = d.s.Sessions.Mint(ctx, authsession.Session{Id: kim.String(), Grant: frame.Whole()})
	x.NoError(err)
	gamma, err := d.s.Ent.Tenant.Query().Where(enttenant.Alias("gamma")).Only(ctx)
	x.NoError(err)

	deployment := func() map[string]string { return tablesOf(t, d.c.Db.Driver, d.c.Db.Dsn) }
	roster := func() map[string]string { return tablesOf(t, d.c.Auth.Roster.Db.Driver, d.c.Auth.Roster.Db.Dsn) }
	notices := func(ps ...predicate.LevyanNotice) int {
		n, err := d.s.Ent.LevyanNotice.Query().Where(ps...).Count(ctx)
		x.NoError(err)
		return n
	}
	wasDeployment, wasRoster, wasNotices := deployment(), roster(), notices()

	out, err = d.loft(t, "identity", "depart", "--member", "@acme/kim")
	x.NoError(err, out)
	x.Contains(out, "@acme: @kim, member\n")
	x.Contains(out, "@beta: @kim, owner; its last owner, so it would be @lee's\n")
	x.Contains(out, "@gamma: @kim, owner; its last owner, and nobody is there to hand it to; nobody would be left in it, so it would close\n")
	x.Contains(out, "(nothing was done: `--yes` does it. It cannot be undone)")

	now := deployment()
	x.NotEqual(wasDeployment[entaccess.Table], now[entaccess.Table], "the access record says nothing of what the operator saw")
	unchanged(t, wasDeployment, now, entaccess.Table)
	unchanged(t, wasRoster, roster())
	x.Equal(wasNotices, notices(), "levyan was told something")
	_, err = d.s.Identity.Verify(ctx, "kim", "kim-password")
	x.NoError(err, "only asked about")

	rows, err := d.s.Ent.Access.Query().Where(
		entaccess.ActorId(operator.Id("alice").Uuid()),
		entaccess.Action("/loft.OperatorIdentityService/Depart"),
	).All(ctx)
	x.NoError(err)
	x.NotEmpty(rows)
	saw := map[pdid.Id]bool{}
	for _, r := range rows {
		x.Equal("read", r.Kind)
		for _, v := range r.Subjects {
			saw[pdid.Id(v)] = true
		}
	}
	for _, h := range membershipsOf(t, d.s, kim) {
		x.True(saw[pdid.Id(h.Id)], "the operator saw kim in %s", pdid.Id(h.TenantId))
	}
	for _, h := range membershipsOf(t, d.s, lee) {
		x.Equal(h.Role == "manager", saw[pdid.Id(h.Id)], "the operator saw who beta would go to, and nobody else")
	}

	out, err = d.loft(t, "identity", "depart", "--member", "@acme/kim", "--yes")
	x.NoError(err, out)
	x.Contains(out, "left @acme\n")
	x.Contains(out, "left @beta; its last owner, so it is @lee's now\n")
	x.Contains(out, "left @gamma; its last owner, and nobody was there to hand it to; nobody was left in it, so it is closed\n")
	x.Equal(1, notices(levyannotice.Workspace(gamma.Id), levyannotice.KindEQ("close")), "levyan is told gamma is closed")
	x.Equal(wasNotices+1, notices(), "and nothing else: who pays is the portal's to tell it (D84)")
	for _, h := range membershipsOf(t, d.s, lee) {
		if h.Role != "member" {
			x.Equal("owner", h.Role, "beta is lee's")
		}
	}
	gamma, err = d.s.Ent.Tenant.Get(ctx, gamma.Id)
	x.NoError(err)
	x.NotNil(gamma.DateClosed)
	n, err := d.s.Ent.Session.Query().Where(entsession.Account(kim.Uuid())).Count(ctx)
	x.NoError(err)
	x.Zero(n, "where they were signed in")
}

// TestADepartureIsForgottenUnderTheDeploymentsDigestKey: a member departed
// at the entry point is forgotten under the policy that holds the digest
// key, and a check of the rows then finds nothing.
func TestADepartureIsForgottenUnderTheDeploymentsDigestKey(t *testing.T) {
	x := require.New(t)
	d := aDeployment(t, func(c *cmd.Config) {
		c.Audit.Digest.Key = "base64:" + base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{7}, 32))
	})
	out, err := d.loft(t, "trail", "rows")
	x.NoError(err, out)
	x.Contains(out, "; 0 forgotten;")
	_, err = d.loft(t, "identity", "depart", "--member", "@acme/kim", "--yes")
	x.NoError(err)
	for _, h := range membershipsOf(t, d.s, accountOf(t, d.s, "owner")) {
		x.Nil(h.DateErased, "somebody else left")
	}
	out, err = d.loft(t, "trail", "rows")
	x.NoError(err, "a departure left rows a check finds")
	x.Regexp(`; [1-9][0-9]* forgotten;`, out)
}

// TestReconcilingFindsWhatTheStreamMissed: every account with a membership
// here asked about at roster, the ones it has nobody by marked from now, the
// ones it has again unmarked -- and none marked when roster has nobody at
// all, which is a wrong directory far more often than everybody leaving.
func TestReconcilingFindsWhatTheStreamMissed(t *testing.T) {
	x := require.New(t)
	ctx := context.Background()
	d := aDeployment(t)
	kim, lee, owner := accountOf(t, d.s, "kim"), accountOf(t, d.s, "lee"), accountOf(t, d.s, "owner")
	x.NoError(d.s.Identity.ForgetPerson(ctx, kim))
	x.NoError(d.s.Lifecycle.Apply(ctx, identity.Standing{Holder: lee, Erased: time.Now()}))
	leaving := func(account pdid.Id) (n int) {
		for _, h := range membershipsOf(t, d.s, account) {
			if h.DateLeaving != nil {
				n++
			}
		}
		return n
	}

	was := tablesOf(t, d.c.Db.Driver, d.c.Db.Dsn)
	out, err := d.loft(t, "identity", "reconcile")
	x.NoError(err, out)
	unchanged(t, was, tablesOf(t, d.c.Db.Driver, d.c.Db.Dsn), entaccess.Table)
	x.Contains(out, "3 account(s) with a membership here; roster has 2 of them")
	x.Contains(out, "gone at roster, not yet leaving here: "+kim.String()+" (@acme/kim, @beta/kim)")
	x.Contains(out, "leaving here, and roster has it again: "+lee.String())
	x.Contains(out, "nothing was done")
	x.Zero(leaving(kim))
	x.Equal(1, leaving(lee))

	since := time.Now()
	out, err = d.loft(t, "identity", "reconcile", "--yes")
	x.NoError(err, out)
	x.Contains(out, "marked 1 as leaving, and 1 as not")
	x.Equal(2, leaving(kim), "in both its workspaces")
	x.Zero(leaving(lee))
	for _, h := range membershipsOf(t, d.s, kim) {
		x.False(h.DateLeaving.Before(since.Add(-time.Second)), "from now, to wait the whole grace")
	}
	out, err = d.loft(t, "identity", "reconcile")
	x.NoError(err)
	x.Contains(out, "nothing to do")

	x.NoError(d.s.Identity.ForgetPerson(ctx, lee))
	x.NoError(d.s.Identity.ForgetPerson(ctx, owner))
	_, err = d.loft(t, "identity", "reconcile", "--yes")
	x.ErrorContains(err, "auth.roster.directory")
	x.Zero(leaving(lee))
	x.Zero(leaving(owner))
}
