package domain_test

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"
	"uuid"

	"github.com/lesomnus/payday/frame"
	"github.com/lesomnus/payday/pdid"
	"github.com/lesomnus/payday/pdtest"
	"github.com/lesomnus/z"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"

	app "github.com/lesomnus/rove"
	"github.com/lesomnus/rove/cli"
	"github.com/lesomnus/rove/cmd"
	"github.com/lesomnus/rove/internal/identity"
	"github.com/lesomnus/rove/server/domain"
	"github.com/lesomnus/rove/server/pd"
)

// env is a deployment with one tenant in it, and its owner signed in.
type env struct {
	t   *testing.T
	x   *require.Assertions
	s   *cmd.Server
	cfg cmd.Config

	// now is the clock the domain layer reads; a test moves it.
	now time.Time

	tenant pdid.Id
	owner  context.Context
	n      int
}

func newEnv(t *testing.T) *env { return newEnvWith(t, nil) }

// newEnvWith is [newEnv] with the configuration as with says, before the
// deployment is built.
func newEnvWith(t *testing.T, with func(c *cmd.Config)) *env {
	t.Helper()
	x := require.New(t)
	ctx := context.Background()

	driver, dsn := pdtest.DB(t)
	c := cmd.Config{}
	c.Db.Driver = driver
	c.Db.Dsn = dsn
	c.Watch.Broker = "memory"
	c.App.Files = t.TempDir()
	c.App.Labels.Suffix = "l.test"
	c.App.NoShowAfter = 15 * time.Minute
	// The roster in this process, on a database of its own beside.
	c.Auth.Roster.Db.Driver, c.Auth.Roster.Db.Dsn = pdtest.DB(t)
	if with != nil {
		with(&c)
	}

	s, err := cmd.Build(ctx, c)
	x.NoError(err)
	t.Cleanup(func() { s.Close() })
	x.NoError(cli.Migrate(ctx, s))

	e := &env{t: t, x: x, s: s, cfg: c, now: time.Now().Truncate(time.Second)}
	s.Deps.Now = func() time.Time { return e.now }
	s.Deps.LookupTXT = nil
	// Whatever a test did, each event it recorded holds its kind's message,
	// and of it only what its kind uses.
	t.Cleanup(e.heldToKinds)

	e.tenant, e.owner = e.newTenant("acme")
	return e
}

// heldToKinds holds every event of the deployment to what payday's past
// writes (payday#151): the message its kind declares, which reads as one,
// holding only what the kind uses of it (`domain.Beyond`) -- or, for a row a
// test wrote as one from before, none and the sentence it was written with.
func (e *env) heldToKinds() {
	e.t.Helper()
	vs, err := e.s.Ent.Event.Query().All(context.Background())
	e.x.NoError(err)
	for _, v := range vs {
		if v.Value == nil {
			e.x.NotEmpty(v.Desc, "%s %s holds no message, and is no row from before", v.Kind, pdid.Id(v.Id))
			continue
		}
		typ, ok := pd.EventKinds[v.Kind]
		e.x.True(ok, "%s is a kind nothing declares", v.Kind)
		e.x.Equal(typ.Descriptor().FullName(), v.Value.MessageName(), "%s holds another kind's message", v.Kind)
		m := typ.New().Interface()
		e.x.NoError(v.Value.UnmarshalTo(m), v.Kind)
		e.x.Empty(domain.Beyond(v.Kind, m), "%s %s holds what its kind does not use", v.Kind, pdid.Id(v.Id))
	}
}

// newTenant puts up a workspace and its owner the way `rove init` does -- the
// owner an account in the directory at roster, the workspace here and the
// owner's membership of it -- and answers with the owner signed in.
func (e *env) newTenant(alias string) (pdid.Id, context.Context) {
	ctx := context.Background()
	login := "owner-" + alias
	_, err := e.s.Identity.Directory(ctx)
	var p identity.Person
	if errors.Is(err, identity.ErrNoDirectory) {
		p, _, err = e.s.Identity.MakeDirectory(ctx, login, "Owner of "+alias)
	} else {
		e.x.NoError(err)
		p, err = e.s.Identity.AddPerson(ctx, login, "Owner of "+alias, pdid.Nil)
	}
	e.x.NoError(err)
	t, err := e.s.Base.Tenant().Add(ctx, app.TenantAddRequest_builder{Alias: alias, Name: alias}.Build())
	e.x.NoError(err)
	tenant := app.TenantRef_builder{Id: t.GetId()}.Build()
	h, err := e.s.Base.Holder().Add(ctx, app.HolderAddRequest_builder{
		Tenant:  tenant,
		Alias:   "owner",
		Name:    "Owner",
		Role:    z.Ptr("owner"),
		Account: p.Id.Bytes(),
	}.Build())
	e.x.NoError(err)
	_, err = e.s.Base.Party().Add(ctx, app.PartyAddRequest_builder{
		Tenant: tenant,
		Name:   "Owner of " + alias,
		Kind:   "person",
		Holder: app.HolderRef_builder{Id: h.GetId()}.Build(),
	}.Build())
	e.x.NoError(err)

	tid, _ := pdid.From(t.GetId())
	return tid, e.as(h)
}

func (e *env) as(h *app.Holder) context.Context {
	hid, _ := pdid.From(h.GetId())
	tid, _ := pdid.From(h.GetTenant().GetId())
	return frame.Into(context.Background(), frame.New(hid, tid, frame.Whole()).WithScope(frame.Only(tid)).WithRow(h))
}

func (e *env) app() app.Server { return e.s.Walled }

// person is somebody with a login and a role, signed in.
func (e *env) person(role string) (context.Context, *app.Party) {
	e.n++
	p, err := e.app().Party().Add(e.owner, app.PartyAddRequest_builder{
		Name:  fmt.Sprintf("%s %d", role, e.n),
		Kind:  "person",
		Email: fmt.Sprintf("p%d@example.com", e.n),
	}.Build())
	e.x.NoError(err)
	v, err := e.app().Party().Invite(e.owner, app.PartyInviteRequest_builder{
		Ref:      ref[app.PartyRef](p.GetId()),
		Alias:    fmt.Sprintf("p%d", e.n),
		Role:     role,
		Password: "password1234",
	}.Build())
	e.x.NoError(err)
	return e.as(v.GetHolder()), v.GetParty()
}

func (e *env) space(name string, in *app.Asset) *app.Asset {
	req := app.AssetAddRequest_builder{Name: name, Kind: "space", Since: e.ago(365 * 24 * time.Hour)}.Build()
	if in != nil {
		req.SetTo(assetRef(in))
	}
	v, err := e.app().Asset().Add(e.owner, req)
	e.x.NoError(err)
	return v
}

func (e *env) item(name, tag string, in *app.Asset, since time.Duration) *app.Asset {
	req := app.AssetAddRequest_builder{Name: name, Tag: tag, Kind: "item", Since: e.ago(since)}.Build()
	if in != nil {
		req.SetTo(assetRef(in))
	}
	v, err := e.app().Asset().Add(e.owner, req)
	e.x.NoError(err)
	return v
}

func (e *env) get(a *app.Asset) *app.Asset {
	v, err := e.app().Asset().Get(e.owner, app.AssetGetRequest_builder{Ref: assetRef(a)}.Build())
	e.x.NoError(err)
	return v
}

func (e *env) ago(d time.Duration) *timestamppb.Timestamp { return timestamppb.New(e.now.Add(-d)) }
func (e *env) in(d time.Duration) *timestamppb.Timestamp  { return timestamppb.New(e.now.Add(d)) }

// sweep runs the background work once, at the env's clock.
func (e *env) sweep() {
	e.x.NoError(domain.Sweeper{Server: e.s.Base, Drv: e.s.Drv, Deps: e.s.Deps}.Pass(context.Background()))
}

func assetRef(a *app.Asset) *app.AssetRef { return app.AssetRef_builder{Id: a.GetId()}.Build() }

// ref makes a reference by identifier of any of the generated `*Ref`s.
func ref[T any, P interface {
	*T
	SetId([]byte)
}](id []byte) P {
	var v P = new(T)
	v.SetId(id)
	return v
}

func codeOf(err error) codes.Code { return status.Code(err) }

func sameId(a, b []byte) bool { return string(a) == string(b) }

// newOp is a fresh operation identifier, what a client mints so that a retry
// is the same operation.
func newOp() []byte { return pdid.New(pd.EventDomain).Bytes() }

func newOpUUID() uuid.UUID { return pdid.New(pd.AllocationDomain).Uuid() }

func idUUID(b []byte) uuid.UUID {
	v, _ := pdid.From(b)
	return v.Uuid()
}
