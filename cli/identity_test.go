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
	"github.com/lesomnus/loft/internal/ent/predicate"
	entsession "github.com/lesomnus/loft/internal/ent/session"
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

// restored is a deployment whose members its roster does not have, as a
// restore of an export from another deployment leaves them: the workspace it
// serves, acme -- whose tenant the roster in this process does not have yet --
// with its owner called `admin`, the name roster gives the first holder of
// every tenant it makes, kim, and a login that ended; and another workspace,
// beta, with somebody called kim too.
type restored struct {
	acme, beta                     pdid.Id
	admin, kim, gone, kimElsewhere pdid.Id
}

func membersRosterDoesNotHave(t *testing.T, c cmd.Config) restored {
	t.Helper()
	x := require.New(t)
	ctx := context.Background()
	s, err := cmd.Build(ctx, c)
	x.NoError(err)
	defer s.Close()
	x.NoError(cli.Migrate(ctx, s))

	out := restored{}
	workspace := func(alias string) (pdid.Id, func(alias, role string) pdid.Id) {
		v, err := s.Base.Tenant().Add(ctx, app.TenantAddRequest_builder{Alias: alias, Name: strings.ToUpper(alias)}.Build())
		x.NoError(err)
		id, _ := pdid.From(v.GetId())
		tref := app.TenantRef_builder{Id: v.GetId()}.Build()
		return id, func(alias, role string) pdid.Id {
			h, err := s.Base.Holder().Add(ctx, app.HolderAddRequest_builder{Tenant: tref, Alias: alias, Name: alias}.Build())
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

var passwordLine = regexp.MustCompile(`(?m)^(\S+)  login (\S+)  password (\S+)$`)

type adopted struct{ login, password string }

func passwordsOf(out string) map[string]adopted {
	v := map[string]adopted{}
	for _, m := range passwordLine.FindAllStringSubmatch(out, -1) {
		v[m[1]] = adopted{login: m[2], password: m[3]}
	}
	return v
}

// TestMembersRosterDoesNotHaveAreMadeThere: every member of the workspace
// this deployment serves whom roster does not have is made a holder of the
// tenant at the roster in this process -- made by the workspace's identifier
// (D85), at the address it serves (D86) -- with the identifier they have
// here, and given a new password said once; and run again it makes nothing.
// Another workspace's are nobody's to make, and a login that ended nobody's.
func TestMembersRosterDoesNotHaveAreMadeThere(t *testing.T) {
	x := require.New(t)
	ctx := context.Background()
	c := embeddedConfig(t)
	c.Auth.Roster.Tenant = "acme"
	was := membersRosterDoesNotHave(t, c)
	d := entry(t, c)

	out, err := d.loft(t, "identity", "link")
	x.NoError(err, out)
	got := passwordsOf(out)
	x.Len(got, 2, out)
	x.Equal("admin", got["admin"].login, "roster's own admin kept the name from the person who had it")
	x.Equal("kim", got["kim"].login)
	rt, err := d.s.Identity.Tenant(ctx)
	x.NoError(err)
	x.Equal(was.acme, rt.Id, "the tenant at roster is the workspace")
	x.Equal("acme", rt.Alias)

	for at, id := range map[string]pdid.Id{"admin": was.admin, "kim": was.kim} {
		p, err := d.s.Identity.Verify(ctx, got[at].login, got[at].password)
		x.NoError(err, at)
		x.Equal(id, p.Id, "%s is somebody new at roster, by their identifier here", at)
	}
	for why, id := range map[string]pdid.Id{"a login that ended": was.gone, "a workspace this deployment does not serve": was.kimElsewhere} {
		_, err := d.s.Identity.ById(ctx, id.String())
		x.ErrorIs(err, identity.ErrNoPerson, why)
	}
	n, err := d.s.Ent.Access.Query().Where(entaccess.Kind("read"), entaccess.ActorId(operator.Id("alice").Uuid())).Count(ctx)
	x.NoError(err)
	x.Positive(n, "the operator saw who they are")

	again, err := d.loft(t, "identity", "link")
	x.NoError(err)
	x.Contains(again, "nobody to make: roster has every member here (2) in @acme")
	x.NotContains(again, "password ")
}

// TestAMemberRosterErasedIsSaidAndMadeNobody: somebody roster erased -- and
// forgot -- keeps their identifier there, which roster makes nobody else
// under: a restore that brought them back here is told so, and the others
// are made all the same.
func TestAMemberRosterErasedIsSaidAndMadeNobody(t *testing.T) {
	x := require.New(t)
	ctx := context.Background()
	c := embeddedConfig(t)
	c.Auth.Roster.Tenant = "acme"
	was := membersRosterDoesNotHave(t, c)
	d := entry(t, c)

	out, err := d.loft(t, "identity", "link", "--password", "demo1234")
	x.NoError(err, out)
	x.NoError(d.s.Identity.ForgetPerson(ctx, was.kim))
	_, err = d.s.Identity.ById(ctx, was.kim.String())
	x.ErrorIs(err, identity.ErrNoPerson, "forgotten at roster")

	out, err = d.loft(t, "identity", "link")
	x.NoError(err, out)
	x.Contains(out, "kim  erased at roster, which makes nobody under their identifier again")
	x.Contains(out, "1 erased at roster: they leave here once reconciled")
	_, err = d.s.Identity.Verify(ctx, "admin", "demo1234")
	x.NoError(err, "the others as they were")
}

// TestMembersToTryThingsOnAreGivenOnePassword: one password roster would not
// take leaves nobody half made; one it takes is everybody's.
func TestMembersToTryThingsOnAreGivenOnePassword(t *testing.T) {
	x := require.New(t)
	ctx := context.Background()
	c := embeddedConfig(t)
	c.Auth.Roster.Tenant = "acme"
	membersRosterDoesNotHave(t, c)
	d := entry(t, c)

	_, err := d.loft(t, "identity", "link", "--password", "short")
	x.ErrorContains(err, "admin")
	out, err := d.loft(t, "identity", "link", "--password", "demo1234")
	x.NoError(err, out)
	x.Contains(out, "2 made at roster, every one with the password given")
	for _, login := range []string{"admin", "kim"} {
		_, err := d.s.Identity.Verify(ctx, login, "demo1234")
		x.NoError(err, login)
	}
}

// external is a roster of its own on a TCP listener, with the tenant acme --
// by the identifier `acme`, the workspace's -- Loft installed into it, and the
// tenant's own key for it (roster's docs/apps.md, shape C): what a roster
// operator stands up.
func external(t *testing.T, acme pdid.Id) (addr, key string, s *rostercmd.Server) {
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
	req := rstr.TenantAddRequest_builder{Alias: "acme"}
	if acme != pdid.Nil {
		req.Id = acme.Bytes()
	}
	_, err = s.Ungated.Tenant().Add(ctx, req.Build())
	x.NoError(err)
	x.NoError(s.Close())

	x.NoError(rostercli.NewCmdApp(&c).Run(ctx, []string{"install", "--tenant", "acme", "--role", strings.Join(identity.AgentMethods, ","), identity.Agent}))
	out, err := ran(t, rostercli.NewCmdKey(&c), "add", "--tenant", "acme", "--holder", identity.Agent, "--name", "loft", "--allow", strings.Join(identity.AgentMethods, ","))
	x.NoError(err, out)
	key = strings.TrimSpace(out)

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
// its operator's, and is told the identifiers to make the members with.
// What the operator made is theirs when this runs again.
func TestARosterOfItsOwnIsToldWhatToMake(t *testing.T) {
	x := require.New(t)
	ctx := context.Background()
	c := embeddedConfig(t)
	was := membersRosterDoesNotHave(t, c)
	addr, rkey, roster := external(t, was.acme)
	c.Auth.Roster = cmd.Config{}.Auth.Roster
	c.Auth.Roster.Addr = addr
	c.Auth.Roster.Insecure = true
	c.Auth.Roster.Key = rkey
	d := entry(t, c)

	out, err := d.loft(t, "identity", "link")
	x.NoError(err, out)
	x.Contains(out, `roster holder add @acme/kim '{"id":"`+was.kim.String()+`","name":"kim"}'`)
	x.NotContains(out, was.kimElsewhere.String(), "a workspace this deployment does not serve")
	x.NotContains(out, was.gone.String(), "a login that ended")

	_, err = roster.Ungated.Holder().Add(ctx, rstr.HolderAddRequest_builder{
		Id:     was.kim.Bytes(),
		Tenant: rstr.TenantRef_builder{Alias: z.Ptr("acme")}.Build(),
		Alias:  "kim",
	}.Build())
	x.NoError(err)
	out, err = d.loft(t, "identity", "link")
	x.NoError(err, out)
	x.NotContains(out, `"id":"`+was.kim.String(), "roster has kim now, by their identifier here")
	x.Contains(out, `"id":"`+was.admin.String())

	// And somebody is made at that roster, and given their password there,
	// not here: refused as the shell refused it, before anything is asked of
	// roster.
	for _, args := range [][]string{
		{"holder", "add", "--login", "lee"},
		{"holder", "add", "--login", "lee", "--password", "lee-password"},
		{"holder", "password", "--login", "kim"},
		{"holder", "password", "--login", "kim", "--password", "kim-password"},
	} {
		_, err := d.loft(t, args...)
		x.Equal(codes.FailedPrecondition, status.Code(err), "%v: %v", args, err)
		x.ErrorContains(err, "auth.roster names a roster of its own: people are made and given passwords there", "%v", args)
	}
	_, err = roster.Ungated.Holder().Get(ctx, rstr.HolderGetRequest_builder{Ref: rstr.HolderRef_builder{Slug: rstr.HolderRefBySlug_builder{
		Tenant: rstr.TenantRef_builder{Alias: z.Ptr("acme")}.Build(),
		Alias:  z.Ptr("lee"),
	}.Build()}.Build()}.Build())
	x.Equal(codes.NotFound, status.Code(err), "lee was made at roster: %v", err)
}

// aDeployment is the roster in this process with acme's owner, and kim and
// lee, put there by the operator.
func aDeployment(t *testing.T, opts ...func(*cmd.Config)) *deployment {
	t.Helper()
	x := require.New(t)
	c := embeddedConfig(t)
	for _, opt := range opts {
		opt(&c)
	}
	c.Auth.Roster.Tenant = "acme"
	_, err := ran(t, cli.NewCmdInit(&c), "--login", "owner", "--password", "owner1234")
	x.NoError(err)
	d := entry(t, c)
	for _, login := range []string{"kim", "lee"} {
		out, err := d.loft(t, "holder", "add", "--login", login, "--password", login+"-password")
		x.NoError(err, out)
		x.Contains(out, "member   "+login+" (")
	}
	return d
}

// memberIdOf is the member who signs in as `login`: their identifier, which
// is their holder's at roster too (D85).
func memberIdOf(t *testing.T, s *cmd.Server, login string) pdid.Id {
	t.Helper()
	h, err := s.Ent.Holder.Query().Where(holder.Alias(login), holder.DateErasedIsNil()).First(context.Background())
	require.NoError(t, err)
	return pdid.Id(h.Id)
}

// row is a member's row as it is, ended or not.
func row(t *testing.T, s *cmd.Server, member pdid.Id) *ent.Holder {
	t.Helper()
	h, err := s.Ent.Holder.Get(context.Background(), member.Uuid())
	require.NoError(t, err)
	return h
}

// TestAnOperatorPutsAMemberInAndGivesThemAPassword: `holder add` makes
// somebody at roster with a password said once when they are new there, and
// their membership and person record as the operator's writes; `holder
// password` gives them a new one and ends everywhere they are signed in.
func TestAnOperatorPutsAMemberInAndGivesThemAPassword(t *testing.T) {
	x := require.New(t)
	ctx := context.Background()
	d := aDeployment(t)

	out, err := d.loft(t, "holder", "add", "--login", "park", "--name", "박", "--role", "manager")
	x.NoError(err, out)
	m := regexp.MustCompile(`(?m)^password (\S+)$`).FindStringSubmatch(out)
	x.NotNil(m, "a new member's password is said once: %s", out)
	_, err = d.s.Identity.Verify(ctx, "park", m[1])
	x.NoError(err)
	_, err = d.loft(t, "holder", "add", "--login", "park")
	x.ErrorContains(err, "is a member here already")
	_, err = d.loft(t, "holder", "add", "--login", "kim", "--password", "another")
	x.ErrorContains(err, "at roster already")
	_, err = d.loft(t, "holder", "add", "--login", "x", "--role", "king")
	x.ErrorContains(err, "role")
	park := memberIdOf(t, d.s, "park")
	n, err := d.s.Ent.Access.Query().Where(entaccess.Kind("write"), entaccess.ActorId(operator.Id("alice").Uuid())).Count(ctx)
	x.NoError(err)
	x.Positive(n, "the membership and its person record are the operator's writes")

	_, _, err = d.s.Sessions.Mint(ctx, authsession.Session{Id: park.String(), Grant: frame.Whole()})
	x.NoError(err)
	out, err = d.loft(t, "holder", "password", "--login", "park", "--password", "park-new-password")
	x.NoError(err, out)
	x.Contains(out, "member   park ("+park.String()+")")
	_, err = d.s.Identity.Verify(ctx, "park", "park-new-password")
	x.NoError(err)
	sessions, err := d.s.Ent.Session.Query().Where(entsession.Holder(park.Uuid())).Count(ctx)
	x.NoError(err)
	x.Zero(sessions, "where they were signed in")
	_, err = d.loft(t, "holder", "password", "--login", "nobody")
	x.ErrorContains(err, "nobody at roster signs in as nobody")
}

// TestAnOperatorDepartsAMemberAtOnce: somebody who asked to be forgotten now
// leaves the workspace, is told what became of it, is signed out where they
// were, and -- on the roster in this process -- leaves it too. Asked about
// first, nothing happens.
func TestAnOperatorDepartsAMemberAtOnce(t *testing.T) {
	x := require.New(t)
	ctx := context.Background()
	d := aDeployment(t)
	kim := memberIdOf(t, d.s, "kim")
	_, _, err := d.s.Sessions.Mint(ctx, authsession.Session{Id: kim.String(), Grant: frame.Whole()})
	x.NoError(err)

	for _, args := range [][]string{
		{"identity", "depart"},
		{"identity", "depart", "--member", "nobody"},
		{"identity", "depart", "--member", "acme/kim"},
	} {
		_, err := d.loft(t, args...)
		x.Error(err, "%v", args)
	}

	out, err := d.loft(t, "identity", "depart", "--member", "kim")
	x.NoError(err, out)
	x.Contains(out, "kim ("+kim.String()+")\n")
	x.Contains(out, "nothing was done")
	_, err = d.s.Identity.Verify(ctx, "kim", "kim-password")
	x.NoError(err, "only asked about")

	out, err = d.loft(t, "identity", "depart", "--member", kim.String(), "--yes")
	x.NoError(err, out)
	x.Contains(out, "left: kim ("+kim.String()+")\n")
	x.Contains(out, "forgot "+kim.String())
	_, err = d.s.Identity.Verify(ctx, "kim", "kim-password")
	x.ErrorIs(err, identity.ErrRefused)
	_, err = d.s.Identity.Verify(ctx, "owner", "owner1234")
	x.NoError(err, "nobody else")
	h := row(t, d.s, kim)
	x.NotNil(h.DateErased)
	x.Empty(h.Alias)
	n, err := d.s.Ent.Session.Query().Where(entsession.Holder(kim.Uuid())).Count(ctx)
	x.NoError(err)
	x.Zero(n, "where they were signed in")
}

// TestADepartureAskedAboutChangesNothing: without `--yes` a departure says,
// by the rules it then follows, what it would do, and changes nothing: no
// table of the deployment's or of the roster in this process, levyan told
// nothing, nobody signed out; only the access record says the operator saw
// who they are. Done, it does what it said -- and hands nothing on, whoever
// they were: who may run the workspace is roster's, by the roles its tenant
// binds there (D89).
func TestADepartureAskedAboutChangesNothing(t *testing.T) {
	x := require.New(t)
	ctx := context.Background()
	d := aDeployment(t, func(c *cmd.Config) { c.App.Billing.Levyan = levyantest.Start(t).Config() })
	kim := memberIdOf(t, d.s, "kim")
	x.NoError(d.s.Identity.Bind(ctx, "loft-owner", kim))
	_, _, err := d.s.Sessions.Mint(ctx, authsession.Session{Id: kim.String(), Grant: frame.Whole()})
	x.NoError(err)

	deployment := func() map[string]string { return tablesOf(t, d.c.Db.Driver, d.c.Db.Dsn) }
	roster := func() map[string]string { return tablesOf(t, d.c.Auth.Roster.Db.Driver, d.c.Auth.Roster.Db.Dsn) }
	notices := func(ps ...predicate.LevyanNotice) int {
		n, err := d.s.Ent.LevyanNotice.Query().Where(ps...).Count(ctx)
		x.NoError(err)
		return n
	}
	wasDeployment, wasRoster, wasNotices := deployment(), roster(), notices()

	out, err := d.loft(t, "identity", "depart", "--member", "kim")
	x.NoError(err, out)
	x.Contains(out, "kim ("+kim.String()+")\n")
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
	x.True(saw[kim], "the operator saw kim")
	x.False(saw[memberIdOf(t, d.s, "owner")], "and nobody else")

	out, err = d.loft(t, "identity", "depart", "--member", "kim", "--yes")
	x.NoError(err, out)
	x.Contains(out, "left: kim ("+kim.String()+")\n")
	x.Equal(wasNotices, notices(), "levyan is told nothing: who pays is the portal's to tell it (D84)")
	x.Error(d.s.Identity.Reach().May(ctx, memberIdOf(t, d.s, "lee"), app.TenantService_Configure_FullMethodName), "nobody was made the owner")
	n, err := d.s.Ent.Session.Query().Where(entsession.Holder(kim.Uuid())).Count(ctx)
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
	_, err = d.loft(t, "identity", "depart", "--member", "kim", "--yes")
	x.NoError(err)
	x.Nil(row(t, d.s, memberIdOf(t, d.s, "owner")).DateErased, "somebody else left")
	out, err = d.loft(t, "trail", "rows")
	x.NoError(err, "a departure left rows a check finds")
	x.Regexp(`; [1-9][0-9]* forgotten;`, out)
}

// TestReconcilingFindsWhatTheStreamMissed: every member asked about at
// roster, the ones it has nobody by marked from now, the ones it has again
// unmarked -- and none marked when roster has nobody at all, which is a wrong
// tenant far more often than everybody leaving.
func TestReconcilingFindsWhatTheStreamMissed(t *testing.T) {
	x := require.New(t)
	ctx := context.Background()
	d := aDeployment(t)
	kim, lee, owner := memberIdOf(t, d.s, "kim"), memberIdOf(t, d.s, "lee"), memberIdOf(t, d.s, "owner")
	x.NoError(d.s.Identity.ForgetPerson(ctx, kim))
	x.NoError(d.s.Lifecycle.Apply(ctx, identity.Standing{Holder: lee, Erased: time.Now()}))
	leaving := func(member pdid.Id) bool { return row(t, d.s, member).DateLeaving != nil }

	was := tablesOf(t, d.c.Db.Driver, d.c.Db.Dsn)
	out, err := d.loft(t, "identity", "reconcile")
	x.NoError(err, out)
	unchanged(t, was, tablesOf(t, d.c.Db.Driver, d.c.Db.Dsn), entaccess.Table)
	x.Contains(out, "3 member(s) here; roster has 2 of them")
	x.Contains(out, "gone at roster, not yet leaving here: kim ("+kim.String()+")")
	x.Contains(out, "leaving here, and roster has them again: lee ("+lee.String()+")")
	x.Contains(out, "nothing was done")
	x.False(leaving(kim))
	x.True(leaving(lee))

	since := time.Now()
	out, err = d.loft(t, "identity", "reconcile", "--yes")
	x.NoError(err, out)
	x.Contains(out, "marked 1 as leaving, and 1 as not")
	x.True(leaving(kim))
	x.False(leaving(lee))
	x.False(row(t, d.s, kim).DateLeaving.Before(since.Add(-time.Second)), "from now, to wait the whole grace")
	out, err = d.loft(t, "identity", "reconcile")
	x.NoError(err)
	x.Contains(out, "nothing to do")

	x.NoError(d.s.Identity.ForgetPerson(ctx, lee))
	x.NoError(d.s.Identity.ForgetPerson(ctx, owner))
	_, err = d.loft(t, "identity", "reconcile", "--yes")
	x.ErrorContains(err, "auth.roster.tenant")
	x.False(leaving(lee))
	x.False(leaving(owner))
}
