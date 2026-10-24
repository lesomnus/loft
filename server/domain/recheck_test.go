package domain_test

import (
	"context"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/lesomnus/payday/pdid"
	"google.golang.org/grpc/codes"

	app "github.com/lesomnus/rove"
	"github.com/lesomnus/rove/internal/ent"
	"github.com/lesomnus/rove/internal/ent/event"
	"github.com/lesomnus/rove/internal/ent/holder"
	"github.com/lesomnus/rove/internal/ent/notification"
	entsession "github.com/lesomnus/rove/internal/ent/session"
	"github.com/lesomnus/rove/internal/ent/tenantdomain"
	"github.com/lesomnus/rove/server/apphost"
	"github.com/lesomnus/rove/server/domain"
	"github.com/lesomnus/rove/server/pd"
)

// dns is what DNS says, as a test says it: records by name, and whether it
// answers at all.
type dns struct {
	mu    sync.Mutex
	txt   map[string][]string
	cname map[string]string
	hosts map[string][]string
	down  bool
}

func (d *dns) asked(name string) error {
	if d.down {
		return &net.DNSError{Err: "i/o timeout", Name: name, IsTimeout: true, IsTemporary: true}
	}
	return nil
}

func missing(name string) error {
	return &net.DNSError{Err: "no such host", Name: name, IsNotFound: true}
}

func (d *dns) lookupTXT(_ context.Context, name string) ([]string, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if err := d.asked(name); err != nil {
		return nil, err
	}
	if v, ok := d.txt[name]; ok {
		return v, nil
	}
	return nil, missing(name)
}

func (d *dns) lookupCNAME(_ context.Context, host string) (string, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if err := d.asked(host); err != nil {
		return "", err
	}
	if v, ok := d.cname[host]; ok {
		return v + ".", nil
	}
	if _, ok := d.hosts[host]; ok {
		return host + ".", nil
	}
	return "", missing(host)
}

func (d *dns) lookupHost(_ context.Context, host string) ([]string, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if err := d.asked(host); err != nil {
		return nil, err
	}
	if c, ok := d.cname[host]; ok {
		host = c
	}
	if v, ok := d.hosts[host]; ok {
		return v, nil
	}
	return nil, missing(host)
}

func (d *dns) set(f func(d *dns)) {
	d.mu.Lock()
	defer d.mu.Unlock()
	f(d)
}

// edge is the host a custom domain's CNAME points at, and its addresses.
const edge = "edge.rove.example"

// reading is the env asking `d` about DNS, with this deployment's target.
func (e *env) reading(d *dns) {
	if d.hosts == nil {
		d.hosts = map[string][]string{}
	}
	d.hosts[edge] = []string{"203.0.113.10", "203.0.113.11"}
	e.s.Deps.LookupTXT, e.s.Deps.LookupCNAME, e.s.Deps.LookupHost = d.lookupTXT, d.lookupCNAME, d.lookupHost
	e.s.Deps.LabelTarget = edge
}

// pointed is a custom domain added, with its records as they are asked for.
func (e *env) pointed(d *dns, host string) *app.TenantDomain {
	v, err := e.app().TenantDomain().Add(e.owner, app.TenantDomainAddRequest_builder{Host: host}.Build())
	e.x.NoError(err)
	d.set(func(d *dns) {
		if d.txt == nil {
			d.txt = map[string][]string{}
		}
		if d.cname == nil {
			d.cname = map[string]string{}
		}
		d.txt["_rove-challenge."+host] = []string{v.GetToken()}
		d.cname[host] = edge
	})
	return v
}

func (e *env) domainRow(v *app.TenantDomain) *ent.TenantDomain {
	id, err := pdid.From(v.GetId())
	e.x.NoError(err)
	return e.s.Ent.TenantDomain.Query().Where(tenantdomain.Id(id.Uuid())).OnlyX(context.Background())
}

// later is the env's clock moved on, and a sweep at it.
func (e *env) later(d time.Duration) {
	e.now = e.now.Add(d)
	e.sweep()
}

// TestADomainWhoseDNSLeftLapses: an active custom domain's DNS is asked about
// again every hour (rove#155), and the domain lapses once six answers in a
// row, over five hours, said its TXT record is gone or that it no longer
// points here. An answer that says nothing -- DNS that does not answer -- is
// not counted, nor is a pass sooner than the hour, and a yes starts the count
// again. Lapsed, it is no longer the workspace's: links to the workspace are
// at the service's address, the sessions handed to it are over, and whoever
// runs the workspace is told -- by an event, and a notification that goes by
// mail.
func TestADomainWhoseDNSLeftLapses(t *testing.T) {
	e := newEnv(t)
	x := e.x
	ctx := context.Background()
	d := &dns{}
	e.reading(d)

	v := e.pointed(d, "assets.acme.example")
	e.later(10 * time.Minute)
	x.Equal("active", e.domainRow(v).State, "verified by the sweep, and the workspace's only domain")
	x.Equal("https://assets.acme.example", on(e, "https://rove.example"), "links to the workspace are on it")

	// A session handed to the domain, and one at the service's.
	owner, err := e.s.Ent.Holder.Query().Where(holder.Alias("owner"), holder.TenantId(e.tenant.Uuid())).Only(ctx)
	x.NoError(err)
	parent := e.session(owner, pdid.Nil, nil)
	handed := e.session(owner, e.tenant, &parent)

	failures := func() uint32 { return e.domainRow(v).Failures }
	e.later(time.Hour)
	x.Zero(failures())

	// The TXT record gone: counted once an hour, and no sooner.
	d.set(func(d *dns) { delete(d.txt, "_rove-challenge.assets.acme.example") })
	e.later(time.Hour)
	x.Equal(uint32(1), failures())
	e.later(10 * time.Minute)
	x.Equal(uint32(1), failures(), "asked again within the hour")
	e.later(50 * time.Minute)
	x.Equal(uint32(2), failures())

	// DNS that does not answer says nothing either way.
	d.set(func(d *dns) { d.down = true })
	e.later(time.Hour)
	x.Equal(uint32(2), failures(), "no answer is no failure")
	d.set(func(d *dns) { d.down = false })

	// A yes starts the count again.
	token := v.GetToken()
	d.set(func(d *dns) { d.txt["_rove-challenge.assets.acme.example"] = []string{token} })
	e.later(time.Hour)
	x.Zero(failures())
	x.Nil(e.domainRow(v).FailingSince)

	// Six noes come an hour apart, and are held to it besides: six that came
	// quicker -- as a count left by checks sooner than this build's -- keep
	// it until five hours have passed since the first.
	row := e.domainRow(v)
	x.NoError(e.s.Ent.TenantDomain.UpdateOneId(row.Id).SetFailures(5).SetFailingSince(e.now.Add(-time.Hour)).Exec(ctx))
	d.set(func(d *dns) { delete(d.txt, "_rove-challenge.assets.acme.example") })
	e.later(time.Hour)
	x.Equal("active", e.domainRow(v).State, "six noes in two hours")
	x.Equal(uint32(6), failures())
	d.set(func(d *dns) { d.txt["_rove-challenge.assets.acme.example"] = []string{token} })
	e.later(time.Hour)
	x.Zero(failures())

	// Pointed elsewhere, six times in a row: five of them keep it.
	d.set(func(d *dns) {
		d.cname["assets.acme.example"] = "elsewhere.example"
		d.hosts["elsewhere.example"] = []string{"198.51.100.7"}
	})
	for i := range 5 {
		e.later(time.Hour)
		x.Equal("active", e.domainRow(v).State, "the %d. no", i+1)
	}
	x.Equal(uint32(5), failures())
	x.True(e.sessionLives(handed), "still the domain's")

	e.later(time.Hour)
	row = e.domainRow(v)
	x.Equal("lapsed", row.State, "the sixth no, five hours after the first")
	x.NotNil(row.DateLapsed)
	x.Equal(on(e, "https://rove.example"), "https://rove.example", "links to the workspace are the service's again")
	x.False(e.sessionLives(handed), "the session handed to it is over")
	x.True(e.sessionLives(parent), "and the one at the service's is not")

	lapses, err := e.s.Ent.Event.Query().Where(event.TenantId(e.tenant.Uuid()), event.Kind("domain.lapse")).All(ctx)
	x.NoError(err)
	x.Len(lapses, 1)
	x.Equal("assets.acme.example", value[*app.TenantDomainLapsed](e, lapses[0]).GetHost())
	x.Contains(e.say(lapses[0]), "assets.acme.example")
	x.Empty(lapses[0].Reason, "why it lapsed is the deployment's, which the event says, and no person's words")
	told, err := e.s.Ent.Notification.Query().Where(notification.HolderId(owner.Id), notification.Kind("domain.lapsed")).All(ctx)
	x.NoError(err)
	x.Len(told, 1, "the owner, told")
	x.Equal("/settings", told[0].Link)
	x.True(domain.Mailed["domain.lapsed"], "by mail as well, where the deployment sends any")

	// Not asked about as an active one any more.
	e.later(time.Hour)
	x.Equal("lapsed", e.domainRow(v).State)
}

// TestALapsedDomainIsActiveAgainOnceItsTXTVerifies: a lapsed domain is asked
// about every few minutes for a week, and is the workspace's active domain
// again once its TXT record holds its token and it points here -- the record
// alone or the CNAME alone is not enough. A person may ask at any time; the
// one thing they may not do is make it active without asking.
func TestALapsedDomainIsActiveAgainOnceItsTXTVerifies(t *testing.T) {
	e := newEnv(t)
	x := e.x
	d := &dns{}
	e.reading(d)

	v := e.pointed(d, "assets.acme.example")
	e.later(10 * time.Minute)
	x.Equal("active", e.domainRow(v).State)
	token := v.GetToken()
	d.set(func(d *dns) { delete(d.txt, "_rove-challenge.assets.acme.example") })
	for range 6 {
		e.later(time.Hour)
	}
	x.Equal("lapsed", e.domainRow(v).State)
	ref := app.TenantDomainRef_builder{Id: v.GetId()}.Build()

	_, err := e.app().TenantDomain().Activate(e.owner, app.TenantDomainActivateRequest_builder{Ref: ref}.Build())
	x.Equal(codes.FailedPrecondition, codeOf(err), "not made active without its DNS asked again")
	_, err = e.app().TenantDomain().Verify(e.owner, app.TenantDomainVerifyRequest_builder{Ref: ref}.Build())
	x.Equal(codes.FailedPrecondition, codeOf(err), "its TXT record is still gone")

	// It points here all along; the record back is what brings it back.
	e.later(10 * time.Minute)
	x.Equal("lapsed", e.domainRow(v).State, "pointed here, and no TXT record")
	d.set(func(d *dns) { d.txt["_rove-challenge.assets.acme.example"] = []string{token} })
	e.later(10 * time.Minute)
	row := e.domainRow(v)
	x.Equal("active", row.State, "verified again by the sweep, with no other domain active")
	x.Zero(row.Failures)
	x.Equal(on(e, "https://rove.example"), "https://assets.acme.example")

	// And by a person, at once.
	d.set(func(d *dns) { delete(d.txt, "_rove-challenge.assets.acme.example") })
	for range 6 {
		e.later(time.Hour)
	}
	x.Equal("lapsed", e.domainRow(v).State)
	d.set(func(d *dns) { d.txt["_rove-challenge.assets.acme.example"] = []string{token} })
	got, err := e.app().TenantDomain().Verify(e.owner, app.TenantDomainVerifyRequest_builder{Ref: ref}.Build())
	x.NoError(err)
	x.Equal("active", got.GetState())

	// A week lapsed, it is looked for no more: a person asks.
	d.set(func(d *dns) { delete(d.txt, "_rove-challenge.assets.acme.example") })
	for range 6 {
		e.later(time.Hour)
	}
	x.Equal("lapsed", e.domainRow(v).State)
	e.now = e.now.Add(8 * 24 * time.Hour)
	d.set(func(d *dns) { d.txt["_rove-challenge.assets.acme.example"] = []string{token} })
	e.later(10 * time.Minute)
	x.Equal("lapsed", e.domainRow(v).State, "a week on, the sweep does not ask")
}

// TestADomainPointsHereByItsCNAMEOrItsAddresses: a custom domain is verified
// once its TXT record holds its token and it points here: its CNAME is this
// deployment's target, or -- an apex its DNS host flattens -- it has an
// address the target has. One pointed elsewhere is told so, one with no record
// is told that, and DNS that does not answer is told to be asked again.
func TestADomainPointsHereByItsCNAMEOrItsAddresses(t *testing.T) {
	e := newEnv(t)
	x := e.x
	d := &dns{}
	e.reading(d)

	verify := func(v *app.TenantDomain) (*app.TenantDomain, error) {
		return e.app().TenantDomain().Verify(e.owner, app.TenantDomainVerifyRequest_builder{Ref: app.TenantDomainRef_builder{Id: v.GetId()}.Build()}.Build())
	}

	byName := e.pointed(d, "assets.acme.example")
	got, err := verify(byName)
	x.NoError(err)
	x.Equal("ready", got.GetState(), "by its CNAME")

	flat := e.pointed(d, "acme.example")
	d.set(func(d *dns) {
		delete(d.cname, "acme.example")
		d.hosts["acme.example"] = []string{"203.0.113.11"}
	})
	got, err = verify(flat)
	x.NoError(err)
	x.Equal("ready", got.GetState(), "by an address the target has")

	away := e.pointed(d, "away.acme.example")
	d.set(func(d *dns) {
		d.cname["away.acme.example"] = "elsewhere.example"
		d.hosts["elsewhere.example"] = []string{"198.51.100.7"}
	})
	_, err = verify(away)
	x.Equal(codes.FailedPrecondition, codeOf(err))
	x.ErrorContains(err, "가리키지 않습니다")

	bare := e.pointed(d, "bare.acme.example")
	d.set(func(d *dns) { delete(d.txt, "_rove-challenge.bare.acme.example") })
	_, err = verify(bare)
	x.Equal(codes.FailedPrecondition, codeOf(err))
	x.ErrorContains(err, "TXT")

	quiet := e.pointed(d, "quiet.acme.example")
	d.set(func(d *dns) { d.down = true })
	_, err = verify(quiet)
	x.Equal(codes.Unavailable, codeOf(err), "DNS that does not answer, asked again later")
}

// TestAnAppDomainAnotherTookThePlaceOfSendsItsPagesOn: when a workspace makes
// another app domain its active one, the one before is legacy, and sends its
// pages on to the new one until `app.workspaces.domain_redirect` is over --
// 90 days unless said. Made active again, it sends nothing on.
func TestAnAppDomainAnotherTookThePlaceOfSendsItsPagesOn(t *testing.T) {
	e := newEnv(t)
	x := e.x
	d := &dns{}
	e.reading(d)

	old := e.pointed(d, "old.acme.example")
	e.later(10 * time.Minute)
	x.Equal("active", e.domainRow(old).State)
	next := e.pointed(d, "assets.acme.example")
	e.later(10 * time.Minute)
	x.Equal("ready", e.domainRow(next).State, "left for a person to switch to")

	_, err := e.app().TenantDomain().Activate(e.owner, app.TenantDomainActivateRequest_builder{Ref: app.TenantDomainRef_builder{Id: next.GetId()}.Build()}.Build())
	x.NoError(err)
	row := e.domainRow(old)
	x.Equal("legacy", row.State)
	x.NotNil(row.RedirectUntil)
	x.WithinDuration(e.now.Add(90*24*time.Hour), *row.RedirectUntil, time.Second)

	_, err = e.app().TenantDomain().Activate(e.owner, app.TenantDomainActivateRequest_builder{Ref: app.TenantDomainRef_builder{Id: old.GetId()}.Build()}.Build())
	x.NoError(err)
	x.Nil(e.domainRow(old).RedirectUntil, "in use again, it sends nothing on")
	x.NotNil(e.domainRow(next).RedirectUntil)
}

// on is where a link to a page of the env's workspace begins, with the
// service's page at `service`.
func on(e *env, service string) string {
	at, err := apphost.Links{App: service, Public: service}.Of(context.Background(), e.s.Ent, e.tenant, apphost.Page)
	e.x.NoError(err)
	return at
}

// session is a session row for an account's membership `h`: bound to
// `workspace` and handed from `parent`, or a sign-in at the service's.
func (e *env) session(h *ent.Holder, workspace pdid.Id, parent *pdid.Id) pdid.Id {
	id := pdid.New(pd.SessionDomain)
	now := time.Now()
	c := e.s.Ent.Session.Create().
		SetId(id.Uuid()).
		SetAccount(*h.Account).
		SetSecret([]byte(id.String())).
		SetGrant([]byte("whole")).
		SetDateIdle(now.Add(time.Hour)).
		SetDateExpires(now.Add(time.Hour)).
		SetDateCreated(now).
		SetDateUpdated(now)
	if !workspace.IsZero() {
		c.SetWorkspace(workspace.Uuid()).SetParent(parent.Uuid())
	}
	e.x.NoError(c.Exec(context.Background()))
	return id
}

func (e *env) sessionLives(id pdid.Id) bool {
	return e.s.Ent.Session.Query().Where(entsession.Id(id.Uuid())).ExistX(context.Background())
}
