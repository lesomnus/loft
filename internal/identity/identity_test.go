package identity

import (
	"crypto/rand"
	"encoding/base32"
	"encoding/base64"
	"io"
	"log/slog"
	"net"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/lesomnus/xli"
	"github.com/lesomnus/z"
	"github.com/stretchr/testify/require"

	"github.com/lesomnus/payday/config"
	"github.com/lesomnus/payday/pdid"
	"github.com/lesomnus/payday/pdtest"
	rostercli "github.com/lesomnus/roster/cli"
	rostercmd "github.com/lesomnus/roster/cmd"
	"github.com/lesomnus/roster/rstr"
	"github.com/lesomnus/roster/server/vouch"
)

const pw = "correct horse battery staple"

// embeddedStore is roster in this process, on a database of its own.
func embeddedStore(t *testing.T) *Store {
	t.Helper()
	drv, dsn := pdtest.DB(t)
	s, err := Open(t.Context(), Config{Db: config.DbConfig{Driver: drv, Dsn: dsn}}, t.TempDir(), slog.Default())
	require.NoError(t, err)
	t.Cleanup(func() { s.Close() })

	return s
}

// TestTheRosterInThisProcessIsRovesOwn is the embedded roster: its directory
// is made here, its accounts are made and given passwords here, and what it
// says of them is what a session is held to.
func TestTheRosterInThisProcessIsRovesOwn(t *testing.T) {
	x := require.New(t)
	ctx := t.Context()
	s := embeddedStore(t)
	x.True(s.Embedded())

	_, err := s.Verify(ctx, "kim", "anything")
	x.ErrorIs(err, ErrNoDirectory, "a sign-in before there is anywhere to sign in to")

	kim, secret, err := s.MakeDirectory(ctx, "kim")
	x.NoError(err)
	x.NotEmpty(secret, "the first account's password is shown once")
	x.Equal(DefaultDirectory, kim.TenantAlias)
	_, _, err = s.MakeDirectory(ctx, "lee")
	x.Error(err, "a second directory")

	t.Run("a password is checked there", func(t *testing.T) {
		x := require.New(t)

		p, err := s.Verify(ctx, "kim", secret)
		x.NoError(err)
		x.Equal(kim, p)

		_, err = s.Verify(ctx, "kim", "not it")
		x.ErrorIs(err, ErrRefused)
		_, err = s.Verify(ctx, "nobody", secret)
		x.ErrorIs(err, ErrRefused, "nobody by that name is the same answer as a wrong password")
	})

	t.Run("as Rove's own holder there, with what it asks and no more", func(t *testing.T) {
		x := require.New(t)

		own := s.em.rs.Ungated
		tref := rstr.TenantRef_builder{Alias: z.Ptr(DefaultDirectory)}.Build()
		h, err := own.Holder().Get(ctx, rstr.HolderGetRequest_builder{
			Ref: rstr.HolderRef_builder{Slug: rstr.HolderRefBySlug_builder{Alias: z.Ptr(Agent), Tenant: tref}.Build()}.Build(),
		}.Build())
		x.NoError(err)
		r, err := own.Role().Get(ctx, rstr.RoleGetRequest_builder{
			Ref: rstr.RoleRef_builder{Slug: rstr.RoleRefBySlug_builder{Alias: z.Ptr(Agent), Tenant: tref}.Build()}.Build(),
		}.Build())
		x.NoError(err)
		x.ElementsMatch(AgentMethods, r.GetMethods())
		vs, err := own.Binding().List(ctx, rstr.BindingListRequest_builder{
			Filters: []*rstr.BindingFilter{rstr.BindingFilter_builder{Holder: rstr.HolderRef_builder{Id: h.GetId()}.Build()}.Build()},
		}.Build())
		x.NoError(err)
		x.Len(vs.GetItems(), 1)
	})

	t.Run("the directory is where every account is", func(t *testing.T) {
		x := require.New(t)

		d, err := s.Directory(ctx)
		x.NoError(err)
		x.Equal(Directory{Id: kim.Tenant, Alias: DefaultDirectory, Name: d.Name}, d)
		again, err := s.EnsureDirectory(ctx)
		x.NoError(err)
		x.Equal(d, again, "a directory there already is made again")

		p, err := s.Lookup(ctx, "kim")
		x.NoError(err)
		x.Equal(kim, p)
		p, err = s.ById(ctx, kim.Id.String())
		x.NoError(err)
		x.Equal(kim, p)
		_, err = s.ById(ctx, pdid.New(pdid.Domain(1)).String())
		x.ErrorIs(err, ErrNoPerson)
	})

	t.Run("an account is made, given a password and given another", func(t *testing.T) {
		x := require.New(t)

		lee, err := s.AddPerson(ctx, "lee", "Lee", pdid.Nil)
		x.NoError(err)
		x.Equal(kim.Tenant, lee.Tenant)
		_, err = s.Verify(ctx, "lee", pw)
		x.ErrorIs(err, ErrRefused, "an account nobody gave a password signs in with none")

		x.NoError(s.SetPassword(ctx, lee.Id, pw))
		p, err := s.Verify(ctx, "lee", pw)
		x.NoError(err)
		x.Equal(lee, p)

		fresh, err := s.IssuePassword(ctx, lee.Id)
		x.NoError(err)
		_, err = s.Verify(ctx, "lee", pw)
		x.ErrorIs(err, ErrRefused, "the password before the one issued")
		_, err = s.Verify(ctx, "lee", fresh)
		x.NoError(err)
	})

	t.Run("and roster's word on them is what a session is held to", func(t *testing.T) {
		x := require.New(t)

		began := time.Now()
		st, err := s.StandingOf(ctx, kim.Id)
		x.NoError(err)
		x.True(st.Good(began))

		x.NoError(s.Invalidate(ctx, kim.Id))
		st, err = s.StandingOf(ctx, kim.Id)
		x.NoError(err)
		x.False(st.Good(began), "signed out everywhere, and a session from before is still good")
		x.True(st.Good(time.Now().Add(time.Second)), "and one that began after it is not")

		x.NoError(s.Disable(ctx, kim.Id))
		st, err = s.StandingOf(ctx, kim.Id)
		x.NoError(err)
		x.False(st.Good(time.Now().Add(time.Second)), "suspended, and good")
		_, err = s.Verify(ctx, "kim", secret)
		x.ErrorIs(err, ErrRefused, "a suspended account signed in")

		x.NoError(s.Enable(ctx, kim.Id))
		_, err = s.Verify(ctx, "kim", secret)
		x.NoError(err)

		_, err = s.StandingOf(ctx, pdid.New(pdid.Domain(1)))
		x.ErrorIs(err, ErrNoPerson)
	})

	t.Run("an account forgotten is nobody there, and its login is free", func(t *testing.T) {
		x := require.New(t)

		park, err := s.AddPerson(ctx, "park", "Park", pdid.Nil)
		x.NoError(err)
		x.NoError(s.SetPassword(ctx, park.Id, pw))
		// Erased the way roster erases -- softly, keeping who they were --
		// which is what a deployment from before forgetting has.
		choi, err := s.AddPerson(ctx, "choi", "Choi", pdid.Nil)
		x.NoError(err)
		_, err = s.em.rs.Ungated.Holder().Erase(ctx, rstr.HolderRef_builder{Id: choi.Id.Bytes()}.Build())
		x.NoError(err)
		erased, err := s.em.erased(ctx)
		x.NoError(err)
		x.Contains(erased, choi.Id, "somebody erased and not forgotten is still somebody there")

		x.NoError(s.ForgetPerson(ctx, park.Id))
		_, err = s.Verify(ctx, "park", pw)
		x.ErrorIs(err, ErrRefused)
		_, err = s.Lookup(ctx, "park")
		x.ErrorIs(err, ErrNoPerson)

		x.NoError(s.ForgetPerson(ctx, choi.Id))
		erased, err = s.em.erased(ctx)
		x.NoError(err)
		x.NotContains(erased, choi.Id, "somebody erased is out of reach of a forgetting")
		_, err = s.AddPerson(ctx, "choi", "Choi again", pdid.Nil)
		x.NoError(err)

		x.NoError(s.ForgetPerson(ctx, park.Id), "forgetting twice")
		x.NoError(s.ForgetPerson(ctx, pdid.New(pdid.Domain(1))), "forgetting nobody")

		_, err = s.AddPerson(ctx, "park", "Park again", pdid.Nil)
		x.NoError(err, "the login of somebody forgotten is not free")
	})
}

// external is a roster deployment of its own -- data plane, control plane and
// a key ring for second factors -- served on a TCP listener, the way a roster
// operator stands one up: `init`, a tenant with a person in it, and a
// deployment key for Rove installed into it.
type external struct {
	c    rostercmd.Config
	s    *rostercmd.Server
	addr string

	// key is a deployment key acme nominated; tenantKey is acme's own key for
	// Rove's holder there.
	key, tenantKey string
	acme, beta     pdid.Id
	kim            pdid.Id
}

func newExternal(t *testing.T) *external {
	t.Helper()
	x := require.New(t)
	ctx := t.Context()

	drv, dsn := pdtest.DB(t)
	cdrv, cdsn := pdtest.DB(t)
	ring := make([]byte, 32)
	_, err := rand.Read(ring)
	x.NoError(err)
	c := rostercmd.Config{
		Db:      config.DbConfig{Driver: drv, Dsn: dsn},
		Watch:   config.WatchConfig{Broker: config.BrokerMemory},
		Control: rostercmd.ControlConfig{Db: config.DbConfig{Driver: cdrv, Dsn: cdsn}},
		Vouch:   rostercmd.VouchConfig{Keys: []string{"one:" + base64.StdEncoding.EncodeToString(ring)}},
	}
	k := rostercli.NewCmdInit(&c)
	k.Writer = io.Discard
	x.NoError(k.Run(ctx, nil))

	e := &external{c: c}
	s, err := rostercmd.Build(ctx, c)
	x.NoError(err)
	for alias, id := range map[string]*pdid.Id{"acme": &e.acme, "beta": &e.beta} {
		v, err := s.Ungated.Tenant().Add(ctx, rstr.TenantAddRequest_builder{Alias: alias, Name: strings.ToUpper(alias)}.Build())
		x.NoError(err)
		*id, err = pdid.From(v.GetId())
		x.NoError(err)
	}
	kim, err := s.Ungated.Holder().Add(ctx, rstr.HolderAddRequest_builder{
		Tenant: rstr.TenantRef_builder{Id: e.acme.Bytes()}.Build(), Alias: "kim", Name: "Kim",
	}.Build())
	x.NoError(err)
	e.kim, err = pdid.From(kim.GetId())
	x.NoError(err)
	_, err = s.Ungated.Credential().Set(ctx, rstr.CredentialSetRequest_builder{
		Ref: rstr.HolderRef_builder{Id: e.kim.Bytes()}.Build(), Kind: "password", Secret: []byte(pw),
	}.Build())
	x.NoError(err)
	x.NoError(s.Close())

	// What docs/apps.md says an app beside roster is given: a deployment key
	// allowed nothing but its own nominations, installed into the tenants it
	// serves -- acme, and not beta.
	e.key = stdoutOf(t, rostercli.NewCmdControl(&c), "key", "add", "--allow", "/roster.NominationService/List", Agent)
	x.NoError(rostercli.NewCmdApp(&c).Run(ctx, []string{"install", "--tenant", "acme", "--role", strings.Join(AgentMethods, ","), Agent}))
	e.tenantKey = stdoutOf(t, rostercli.NewCmdKey(&c), "add", "--tenant", "acme", "--holder", Agent, "--name", "rove", "--allow", strings.Join(AgentMethods, ","))

	e.s, err = rostercmd.Build(ctx, c)
	x.NoError(err)
	t.Cleanup(func() { e.s.Close() })
	g, err := e.s.Grpc(ctx, rostercmd.Config{})
	x.NoError(err)
	l, err := net.Listen("tcp", "127.0.0.1:0")
	x.NoError(err)
	go g.Serve(l)
	t.Cleanup(g.Stop)
	e.addr = l.Addr().String()

	return e
}

func (e *external) open(t *testing.T, key, directory string) *Store {
	t.Helper()
	s, err := Open(t.Context(), Config{Addr: e.addr, Insecure: true, Key: key, Directory: directory}, "", slog.Default())
	require.NoError(t, err)
	t.Cleanup(func() { s.Close() })

	return s
}

// stdoutOf runs a roster command and answers what it printed, which is how its
// keys are handed out.
func stdoutOf(t *testing.T, k *xli.Command, args ...string) string {
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

	return strings.TrimSpace(string(b))
}

// TestARosterOfItsOwnIsReachedWithTheKeyItGave is the external roster, the
// default: a deployment key serves the tenants that nominated it, and the
// directory is the one of them the configuration names; every call names it
// beside the key. Accounts are roster's to make there.
func TestARosterOfItsOwnIsReachedWithTheKeyItGave(t *testing.T) {
	x := require.New(t)
	ctx := t.Context()
	e := newExternal(t)
	s := e.open(t, e.key, "acme")
	x.False(s.Embedded())

	d, err := s.Directory(ctx)
	x.NoError(err)
	x.Equal(e.acme, d.Id)

	p, err := s.Verify(ctx, "kim", pw)
	x.NoError(err)
	x.Equal(e.kim, p.Id)
	x.Equal(e.acme, p.Tenant)
	x.Equal("acme", p.TenantAlias)
	_, err = s.Verify(ctx, "kim", "not it")
	x.ErrorIs(err, ErrRefused)

	p, err = s.ById(ctx, e.kim.String())
	x.NoError(err)
	x.Equal("kim", p.Alias)
	st, err := s.StandingOf(ctx, e.kim)
	x.NoError(err)
	x.True(st.Good(time.Now()))

	_, _, err = s.MakeDirectory(ctx, "lee")
	x.ErrorIs(err, ErrExternal)
	_, err = s.EnsureDirectory(ctx)
	x.ErrorIs(err, ErrExternal)
	_, err = s.AddPerson(ctx, "lee", "Lee", pdid.Nil)
	x.ErrorIs(err, ErrExternal)
	_, err = s.IssuePassword(ctx, e.kim)
	x.ErrorIs(err, ErrExternal)

	t.Run("a directory that did not nominate the key is not served", func(t *testing.T) {
		x := require.New(t)

		_, err := e.open(t, e.key, "beta").Verify(ctx, "kim", pw)
		x.ErrorIs(err, ErrNoTenant)
		_, err = e.open(t, e.key, "").Verify(ctx, "kim", pw)
		x.ErrorContains(err, "auth.roster.directory", "a deployment key, and nothing saying which tenant is the directory")
	})

	t.Run("and a tenant key's directory is its own tenant", func(t *testing.T) {
		x := require.New(t)

		s := e.open(t, e.tenantKey, "")
		p, err := s.Verify(ctx, "kim", pw)
		x.NoError(err)
		x.Equal(e.kim, p.Id)
		_, err = e.open(t, e.tenantKey, "beta").Verify(ctx, "kim", pw)
		x.ErrorContains(err, "auth.roster.directory")
	})

	t.Run("and no key is no way in", func(t *testing.T) {
		x := require.New(t)

		_, err := Open(ctx, Config{Addr: e.addr, Insecure: true}, "", slog.Default())
		x.ErrorContains(err, "auth.roster.key")
		_, err = Open(ctx, Config{Addr: e.addr, Insecure: true, Key: "something"}, "", slog.Default())
		x.ErrorContains(err, "auth.roster.key")
	})
}

// TestAPasswordAloneIsNoSignInForSomebodyWithASecondFactor: roster answers a
// right password for such a person with a continuation and never with ok, and
// Rove, which takes no second factor, refuses rather than signing them in on
// one -- they sign in through the issuer.
func TestAPasswordAloneIsNoSignInForSomebodyWithASecondFactor(t *testing.T) {
	x := require.New(t)
	ctx := t.Context()
	e := newExternal(t)

	res, err := e.s.Ungated.Credential().Enrol(ctx, rstr.CredentialEnrolRequest_builder{
		Ref:    rstr.HolderRef_builder{Id: e.kim.Bytes()}.Build(),
		Kind:   vouch.KindTotp,
		Issuer: "roster",
	}.Build())
	x.NoError(err)
	seed, err := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(res.GetSeed())
	x.NoError(err)
	// Confirmed the way an enrolment is: one code verified against it.
	ok, err := vouch.New(e.s.Ungated, e.s.Ungated, vouch.WithKeys(e.s.Keyring)).Verify(ctx, rstr.VouchVerifyRequest_builder{
		Who:    rstr.VouchWho_builder{Id: e.kim.Bytes()}.Build(),
		Kind:   vouch.KindTotp,
		Secret: []byte(vouch.CodeAt(seed, time.Now().Unix()/30)),
	}.Build())
	x.NoError(err)
	x.True(ok.GetOk())

	_, err = e.open(t, e.key, "acme").Verify(ctx, "kim", pw)
	x.ErrorIs(err, ErrSecondFactor)
}
