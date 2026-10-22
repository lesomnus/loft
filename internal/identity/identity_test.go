package identity

import (
	"context"
	"crypto/rand"
	"encoding/base32"
	"encoding/base64"
	"io"
	"log/slog"
	"net"
	"os"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/lesomnus/xli"
	"github.com/lesomnus/z"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/lesomnus/payday/access"
	"github.com/lesomnus/payday/auth"
	"github.com/lesomnus/payday/config"
	"github.com/lesomnus/payday/pdid"
	"github.com/lesomnus/payday/pdtest"
	"github.com/lesomnus/payday/spin"
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

	kim, secret, err := s.MakeDirectory(ctx, "kim", "Kim")
	x.NoError(err)
	x.NotEmpty(secret, "the first account's password is shown once")
	x.Equal(DefaultDirectory, kim.TenantAlias)
	x.Equal("Kim", kim.Name, "the first account is called what it was made as")
	_, _, err = s.MakeDirectory(ctx, "lee", "")
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
		after := time.Now().Add(time.Second)
		x.ErrorIs(s.Held(ctx, kim.Id, after), ErrRefused, "a session of a suspended account")

		x.NoError(s.Enable(ctx, kim.Id))
		_, err = s.Verify(ctx, "kim", secret)
		x.NoError(err)
		x.NoError(s.Held(ctx, kim.Id, after), "enabled here, and good at once rather than once what was known of it ran out")

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

// TestTheRosterInThisProcessOutlivesItsHousekeeping is the embedded roster
// once its housekeeping has stopped. Under a lease that is whenever the lease
// is lost -- the database gone for a moment -- and it starts again when the
// lease is held again, while every session this process serves is checked at
// roster all along: the server is not the housekeeping's to stop.
func TestTheRosterInThisProcessOutlivesItsHousekeeping(t *testing.T) {
	x := require.New(t)
	ctx := t.Context()
	s := embeddedStore(t)
	kim, secret, err := s.MakeDirectory(ctx, "kim", "Kim")
	x.NoError(err)

	for i := range 2 {
		run, stop := context.WithCancel(ctx)
		done := make(chan error, 1)
		go func() { done <- spin.Run(run, slices.Values(s.Spinners())) }()
		stop()
		<-done

		p, err := s.Verify(ctx, "kim", secret)
		x.NoError(err, "a sign-in after the housekeeping stopped %d times", i+1)
		x.Equal(kim, p)
	}
}

// TestTheRosterInThisProcessWritesItsRecordInEveryProcess: its loops are
// handed over one by one, so the one roster runs in every process -- what
// writes the reads its access record queued -- runs in a process that holds
// no lease, and not only in the one that holds the deployment's.
func TestTheRosterInThisProcessWritesItsRecordInEveryProcess(t *testing.T) {
	x := require.New(t)
	ctx := t.Context()
	s := embeddedStore(t)
	_, _, err := s.MakeDirectory(ctx, "kim", "Kim")
	x.NoError(err)
	dir, err := s.em.rs.Ent.Tenant.Query().First(ctx)
	x.NoError(err)

	run, stop := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() { done <- spin.Run(run, slices.Values(s.Spinners()), spin.WithLease(spin.Nowhere())) }()
	t.Cleanup(func() {
		stop()
		<-done
	})

	w := s.em.rs.Access
	w.Queue(access.Record{
		Tenant:   pdid.Id(dir.Id),
		Action:   "/test.Reads/Somebody",
		Kind:     access.Read,
		Result:   "ok",
		At:       time.Now().UTC(),
		Subjects: []pdid.Id{pdid.New(1)},
		Count:    1,
	})
	x.Eventually(func() bool { return w.Pending() == 0 }, 5*time.Second, 50*time.Millisecond,
		"what the roster in this process queued waited for a process holding a lease")
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
	// Rove's holder there. operator is this roster's own operator, who
	// suspends and erases people over the wire -- which is the only way the
	// sync stream hears of it.
	key, tenantKey, operator string
	acme, beta               pdid.Id
	kim                      pdid.Id
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
	e.operator = stdoutOf(t, rostercli.NewCmdControl(&c), "key", "add", "--allow",
		"/roster.HolderService/Invalidate,/roster.HolderService/Disable,/roster.HolderService/Erase,"+
			"/roster.TenantService/Get,/roster.TenantService/Update,/roster.TenantService/Close,/roster.TenantService/Restore", "operator")

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

// role patches the role acme installed Rove with to `methods`, the way the
// README tells a roster operator to: the whole list, with `roster role patch`.
func (e *external) role(t *testing.T, methods []string) {
	t.Helper()
	x := require.New(t)
	var entity *xli.Command
	for _, k := range rostercli.NewCmdEntities(&e.c) {
		if k.Name == "role" {
			entity = k
		}
	}
	x.NotNil(entity)
	quoted := make([]string, len(methods))
	for i, m := range methods {
		quoted[i] = `"` + m + `"`
	}
	x.NoError(entity.Run(t.Context(), []string{"patch", "@acme/" + Agent + "-app",
		`{"methods": [` + strings.Join(quoted, ",") + `], "date_updated_force": true}`}))
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

// rosterUnder is one of the two places this deployment's accounts can be --
// the roster in this process, or one of its own over TCP with the key it gave
// -- with an account in it, and roster's own door to it: what its operator
// does there, which this deployment is not told of until it asks again.
type rosterUnder struct {
	name    string
	s       *Store
	account pdid.Id
	login   string
	pass    string
	// makes says whether accounts are made from here.
	makes bool
	// invalidate signs the account out everywhere, and suspend suspends it.
	invalidate, suspend func() error
}

func bothRosters(t *testing.T) []rosterUnder {
	t.Helper()
	ctx := t.Context()

	em := embeddedStore(t)
	kim, secret, err := em.MakeDirectory(ctx, "kim", "Kim")
	require.NoError(t, err)
	inProcess := func() rstr.HolderRef_builder { return rstr.HolderRef_builder{Id: kim.Id.Bytes()} }

	e := newExternal(t)
	op, as := e.overTheWire(t)
	theirs := rstr.HolderRef_builder{Id: e.kim.Bytes()}.Build()

	return []rosterUnder{
		{
			name: "in this process", s: em, account: kim.Id, login: "kim", pass: secret, makes: true,
			invalidate: func() error {
				_, err := em.em.rs.Ungated.Holder().Invalidate(ctx, rstr.HolderInvalidateRequest_builder{Ref: inProcess().Build()}.Build())
				return err
			},
			suspend: func() error {
				_, err := em.em.rs.Ungated.Holder().Disable(ctx, rstr.HolderDisableRequest_builder{Ref: inProcess().Build()}.Build())
				return err
			},
		},
		{
			name: "of its own", s: e.open(t, e.key, "acme"), account: e.kim, login: "kim", pass: pw, makes: false,
			invalidate: func() error {
				_, err := op.Invalidate(as, rstr.HolderInvalidateRequest_builder{Ref: theirs}.Build())
				return err
			},
			suspend: func() error {
				_, err := op.Disable(as, rstr.HolderDisableRequest_builder{Ref: theirs}.Build())
				return err
			},
		},
	}
}

// TestTheSameIdentityOnEitherRoster: what this deployment asks of roster, and
// holds a session to, is the same whichever roster its accounts are in (plan
// M2) -- a password checked, an account found by its identifier, signed out
// everywhere and suspended reaching a session -- and a suspension nobody told
// it of reaches a session already open no later than [HeldTtl] after roster
// was last asked.
func TestTheSameIdentityOnEitherRoster(t *testing.T) {
	for _, r := range bothRosters(t) {
		t.Run(r.name, func(t *testing.T) {
			x := require.New(t)
			ctx := t.Context()
			s := r.s
			now := time.Now()
			s.now = func() time.Time { return now }

			p, err := s.Verify(ctx, r.login, r.pass)
			x.NoError(err)
			x.Equal(r.account, p.Id)
			// A session, from the sign-in on.
			began := time.Now()
			_, err = s.Verify(ctx, r.login, "not it")
			x.ErrorIs(err, ErrRefused)
			_, err = s.Verify(ctx, "nobody", r.pass)
			x.ErrorIs(err, ErrRefused, "nobody by that name is the same answer as a wrong password")

			p, err = s.ById(ctx, r.account.String())
			x.NoError(err)
			x.Equal(r.login, p.Alias)
			_, err = s.ById(ctx, pdid.New(pdid.Domain(1)).String())
			x.ErrorIs(err, ErrNoPerson)

			x.NoError(s.Held(ctx, r.account, began))

			// Signed out everywhere, and asked again: the sessions from
			// before end, and one after is good.
			x.NoError(r.invalidate())
			s.Recheck(r.account)
			x.ErrorIs(s.Held(ctx, r.account, began), ErrRefused, "a session from before")
			after := time.Now().Add(time.Second)
			x.NoError(s.Held(ctx, r.account, after), "and one that began after it")

			// Suspended, and nobody told: believed as roster last said for
			// HeldTtl, and refused once that is over.
			x.NoError(r.suspend())
			now = now.Add(HeldTtl - time.Second)
			x.NoError(s.Held(ctx, r.account, after), "what roster said last, within HeldTtl")
			now = now.Add(2 * time.Second)
			x.ErrorIs(s.Held(ctx, r.account, after), ErrRefused, "suspended, and no later than HeldTtl")
			_, err = s.Verify(ctx, r.login, r.pass)
			x.ErrorIs(err, ErrRefused, "and no sign-in")

			// Accounts are made from here, or are roster's to make.
			_, err = s.AddPerson(ctx, "lee", "Lee", pdid.Nil)
			if r.makes {
				x.NoError(err)
			} else {
				x.ErrorIs(err, ErrExternal)
			}
		})
	}
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

	_, _, err = s.MakeDirectory(ctx, "lee", "Lee")
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

// TestOnlyAnAddressSomebodyCheckedIsAnAccounts: what an invitation is found
// by. An address on an account that nobody checked -- one a provider passed on
// from a directory whose administrator typed it -- is not the account's.
func TestOnlyAnAddressSomebodyCheckedIsAnAccounts(t *testing.T) {
	t.Run("on the roster in this process", func(t *testing.T) {
		x := require.New(t)
		ctx := t.Context()
		s := embeddedStore(t)
		_, _, err := s.MakeDirectory(ctx, "owner", "")
		x.NoError(err)

		kim, err := s.AddPerson(ctx, "kim", "Kim", pdid.Nil)
		x.NoError(err)
		x.NoError(s.AddAddress(ctx, kim.Id, " Kim@Acme.Example ", true))
		x.NoError(s.AddAddress(ctx, kim.Id, "boss@acme.example", false))

		got, err := s.Addresses(ctx, kim.Id)
		x.NoError(err)
		x.Equal([]string{"kim@acme.example"}, got)
	})

	t.Run("on a roster of its own", func(t *testing.T) {
		x := require.New(t)
		ctx := t.Context()
		e := newExternal(t)

		// Checked by somebody, the way roster stamps one; and one that came in
		// a provider's claims with nothing checked.
		row, err := e.s.Ungated.Email().Add(ctx, rstr.EmailAddRequest_builder{
			Holder: rstr.HolderRef_builder{Id: e.kim.Bytes()}.Build(), Address: "kim@acme.example",
		}.Build())
		x.NoError(err)
		_, err = e.s.Ungated.Email().Patch(ctx, rstr.EmailPatchRequest_builder{
			Ref:          rstr.EmailRef_builder{Id: row.GetId()}.Build(),
			DateVerified: timestamppb.Now(),
			DateUpdated:  row.GetDateUpdated(),
		}.Build())
		x.NoError(err)
		_, err = e.s.Ungated.Email().Add(ctx, rstr.EmailAddRequest_builder{
			Holder: rstr.HolderRef_builder{Id: e.kim.Bytes()}.Build(), Address: "ceo@acme.example",
		}.Build())
		x.NoError(err)

		s := e.open(t, e.key, "acme")
		got, err := s.Addresses(ctx, e.kim)
		x.NoError(err)
		x.Equal([]string{"kim@acme.example"}, got)
	})
}

// operator is roster's operator over the wire: what publishes to roster's
// stream is the interceptor on its served stack, and a call made through
// roster's server in this process writes the row and tells nobody.
func (e *external) overTheWire(t *testing.T) (rstr.HolderServiceClient, context.Context) {
	t.Helper()
	return rstr.NewHolderServiceClient(e.wire(t)), auth.BearerProvider(e.operator).Provide(t.Context())
}

func (e *external) wire(t *testing.T) *grpc.ClientConn {
	t.Helper()
	conn, err := grpc.NewClient(e.addr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	require.NoError(t, err)
	t.Cleanup(func() { conn.Close() })
	return conn
}

// heard writes until the stream says what was written: there is nothing to
// say it has subscribed, and a write made before it has is heard by nobody.
func heard(t *testing.T, got <-chan Standing, who pdid.Id, write func() error, ok func(Standing) bool) Standing {
	t.Helper()
	deadline := time.After(10 * time.Second)
	for {
		require.NoError(t, write())
		select {
		case v := <-got:
			if v.Holder == who && ok(v) {
				return v
			}
		case <-time.After(100 * time.Millisecond):
		case <-deadline:
			t.Fatal("nothing about them came down the stream")
		}
	}
}

// follow runs [Store.Follow] until the test ends, and is what it handed on.
func follow(t *testing.T, s *Store) <-chan Standing {
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	t.Cleanup(func() { cancel(); <-done })
	got := make(chan Standing, 64)
	go func() {
		defer close(done)
		s.Follow(ctx, func(_ context.Context, v Standing) error {
			select {
			case got <- v:
			default:
			}
			return nil
		})
	}()
	return got
}

// TestRostersWordArrivesAsItHappens: what roster says of an account comes down
// its stream as it is said -- signed out everywhere, suspended, erased -- to a
// deployment with a roster of its own.
func TestRostersWordArrivesAsItHappens(t *testing.T) {
	x := require.New(t)
	e := newExternal(t)
	got := follow(t, e.open(t, e.key, "acme"))
	op, as := e.overTheWire(t)
	ref := rstr.HolderRef_builder{Id: e.kim.Bytes()}.Build()

	v := heard(t, got, e.kim, func() error {
		_, err := op.Invalidate(as, rstr.HolderInvalidateRequest_builder{Ref: ref}.Build())
		return err
	}, func(v Standing) bool { return !v.Invalidated.IsZero() })
	x.Equal(e.acme, v.Tenant)
	x.True(v.Disabled.IsZero())
	x.True(v.Erased.IsZero())

	// Once it has been heard, once is enough.
	v = heard(t, got, e.kim, func() error {
		_, err := op.Disable(as, rstr.HolderDisableRequest_builder{Ref: ref}.Build())
		if status.Code(err) == codes.FailedPrecondition {
			return nil
		}
		return err
	}, func(v Standing) bool { return !v.Disabled.IsZero() })
	x.False(v.Good(time.Now()))

	v = heard(t, got, e.kim, func() error {
		_, err := op.Erase(as, ref)
		if status.Code(err) == codes.NotFound {
			return nil
		}
		return err
	}, func(v Standing) bool { return !v.Erased.IsZero() })
	x.Equal(e.acme, v.Tenant)
}

// TestAnErasureIsHeardOfSomebodyNothingElseHappenedTo: most people's erasure is
// the first thing roster's stream hears of them since this deployment
// connected -- nothing was written about them before -- and it is heard all the
// same, with when roster erased them (roster#104). Somebody erased in a tenant
// this deployment does not serve is not.
func TestAnErasureIsHeardOfSomebodyNothingElseHappenedTo(t *testing.T) {
	x := require.New(t)
	e := newExternal(t)

	// Made before the stream opens, through roster's server in this process,
	// which publishes nothing: the stream has never read them.
	add := func(tenant pdid.Id, alias string) pdid.Id {
		v, err := e.s.Ungated.Holder().Add(t.Context(), rstr.HolderAddRequest_builder{
			Tenant: rstr.TenantRef_builder{Id: tenant.Bytes()}.Build(), Alias: alias, Name: alias,
		}.Build())
		x.NoError(err)
		id, err := pdid.From(v.GetId())
		x.NoError(err)
		return id
	}
	quiet := add(e.acme, "quiet")
	elsewhere := add(e.beta, "elsewhere")

	got := follow(t, e.open(t, e.key, "acme"))
	op, as := e.overTheWire(t)

	// kim first, for the stream to be known to be open: a write before it is
	// is heard by nobody, and an erasure is not made twice.
	heard(t, got, e.kim, func() error {
		_, err := op.Invalidate(as, rstr.HolderInvalidateRequest_builder{Ref: rstr.HolderRef_builder{Id: e.kim.Bytes()}.Build()}.Build())
		return err
	}, func(v Standing) bool { return !v.Invalidated.IsZero() })

	// The one this deployment may not hear of first, so that the other
	// arriving makes its absence a fact rather than a race.
	_, err := op.Erase(as, rstr.HolderRef_builder{Id: elsewhere.Bytes()}.Build())
	x.NoError(err)
	_, err = op.Erase(as, rstr.HolderRef_builder{Id: quiet.Bytes()}.Build())
	x.NoError(err)

	deadline := time.After(10 * time.Second)
	for {
		select {
		case v := <-got:
			x.NotEqual(elsewhere, v.Holder, "an erasure in a tenant this deployment does not serve")
			if v.Holder != quiet {
				continue
			}
			x.False(v.Erased.IsZero(), "heard, and as an erasure")
			x.Equal(e.acme, v.Tenant)
			return
		case <-deadline:
			t.Fatal("the erasure of somebody nothing else happened to was not heard")
		}
	}
}

// TestADirectoryClosedShutsOutEveryoneInIt: roster closing the directory this
// deployment's accounts are in is heard as an event about each of them, with
// when -- and is no session for any of them -- until roster restores it, which
// is heard too (roster#105).
func TestADirectoryClosedShutsOutEveryoneInIt(t *testing.T) {
	x := require.New(t)
	e := newExternal(t)
	s := e.open(t, e.key, "acme")
	got := follow(t, s)
	op, as := e.overTheWire(t)
	tenants := rstr.NewTenantServiceClient(e.wire(t))
	ref := rstr.TenantRef_builder{Id: e.acme.Bytes()}.Build()
	kim := rstr.HolderRef_builder{Id: e.kim.Bytes()}.Build()
	invalidate := func() error {
		_, err := op.Invalidate(as, rstr.HolderInvalidateRequest_builder{Ref: kim}.Build())
		return err
	}

	// kim first, for the stream to be known to be open.
	heard(t, got, e.kim, invalidate, func(v Standing) bool { return !v.Invalidated.IsZero() })
	began := time.Now()
	x.NoError(s.Held(t.Context(), e.kim, began))

	// The stream reads a tenant when it moves, and a tenant it had seen
	// before closing is a closing it names for everybody in it.
	was, err := tenants.Get(as, rstr.TenantGetRequest_builder{Ref: ref, Select: rstr.TenantSelect_builder{DateUpdated: z.Ptr(true)}.Build()}.Build())
	x.NoError(err)
	_, err = tenants.Update(as, rstr.TenantUpdateRequest_builder{Ref: ref, DateUpdated: was.GetDateUpdated(), Desc: z.Ptr("seen")}.Build())
	x.NoError(err)
	closed := false
	v := heard(t, got, e.kim, func() error {
		if closed {
			return nil
		}
		_, err := tenants.Close(as, rstr.TenantCloseRequest_builder{Ref: ref}.Build())
		closed = err == nil
		return err
	}, func(v Standing) bool { return !v.Closed.IsZero() })
	x.Equal(e.acme, v.Tenant)
	x.True(v.Disabled.IsZero(), "a directory closing is not its people suspended")
	x.False(v.Good(time.Now()))
	x.Error(s.Held(t.Context(), e.kim, began), "a session in a closed directory")

	_, err = tenants.Restore(as, rstr.TenantRestoreRequest_builder{Ref: ref}.Build())
	x.NoError(err)
	v = heard(t, got, e.kim, invalidate, func(v Standing) bool { return v.Closed.IsZero() })
	x.True(v.Disabled.IsZero())
	x.NoError(s.Held(t.Context(), e.kim, time.Now()), "a session again, once it is restored")
}

// TestAnAccountPageIsAnExternalRostersOwn: the page where somebody manages
// their own account is roster's, at its front door; the roster in this process
// has none to name.
func TestAnAccountPageIsAnExternalRostersOwn(t *testing.T) {
	x := require.New(t)
	e := newExternal(t)
	s, err := Open(t.Context(), Config{Addr: e.addr, Insecure: true, Key: e.key, Directory: "acme", AccountUrl: "https://id.example/"}, "", slog.Default())
	x.NoError(err)
	t.Cleanup(func() { s.Close() })
	x.Equal("https://id.example/", s.AccountPage())

	drv, dsn := pdtest.DB(t)
	_, err = Open(t.Context(), Config{Db: config.DbConfig{Driver: drv, Dsn: dsn}, AccountUrl: "https://id.example/"}, t.TempDir(), slog.Default())
	x.ErrorContains(err, "account_url")
}

// logs is a log a test reads while it is written.
type logs struct {
	mu sync.Mutex
	b  strings.Builder
}

func (l *logs) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *logs) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

// TestAStreamRosterRefusesIsSaidAndThenHeld: a role installed before Rove held
// roster's stream -- as every one before this step was -- is refused it. That
// is said once, with what to do, and doing it is noticed without a restart.
// What it says to do is what the README says, and this runs it.
func TestAStreamRosterRefusesIsSaidAndThenHeld(t *testing.T) {
	x := require.New(t)
	e := newExternal(t)
	role := func(methods []string) { e.role(t, methods) }
	without := []string{}
	for _, m := range AgentMethods {
		if m != "/roster.SyncService/Watch" {
			without = append(without, m)
		}
	}
	role(without)

	l := &logs{}
	s, err := Open(t.Context(), Config{Addr: e.addr, Insecure: true, Key: e.key, Directory: "acme"}, "", slog.New(slog.NewTextHandler(l, nil)))
	x.NoError(err)
	t.Cleanup(func() { s.Close() })
	s.refused = 300 * time.Millisecond
	got := follow(t, s)

	x.Eventually(func() bool { return strings.Contains(l.String(), "roster role patch") }, 10*time.Second, 20*time.Millisecond)
	x.Contains(l.String(), "level=ERROR")
	x.NotContains(l.String(), "level=WARN", "a refusal is not a stream that dropped")

	role(AgentMethods)
	op, as := e.overTheWire(t)
	heard(t, got, e.kim, func() error {
		_, err := op.Invalidate(as, rstr.HolderInvalidateRequest_builder{Ref: rstr.HolderRef_builder{Id: e.kim.Bytes()}.Build()}.Build())
		return err
	}, func(v Standing) bool { return !v.Invalidated.IsZero() })
}

// TestTheRosterInThisProcessSaysWhereItIs: it believes its callers -- it has no
// control plane -- and nothing outside this process reaches it, so it is served
// with the in-process Plain. A roster left to fall back on Plain says so with
// a warning about a port anybody might dial, which read as a hole in Rove
// (rove#56).
func TestTheRosterInThisProcessSaysWhereItIs(t *testing.T) {
	x := require.New(t)
	s := embeddedStore(t)

	x.NotNil(s.em)
	x.NotNil(s.em.rs.Auth, "served with what a roster falls back on, and its warning")
}
