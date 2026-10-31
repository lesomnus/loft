package domain_test

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"
	"uuid"

	"github.com/lesomnus/payday/frame"
	"github.com/lesomnus/payday/pdid"
	"github.com/lesomnus/payday/pdpb"
	"github.com/lesomnus/payday/pdtest"
	"github.com/lesomnus/z"
	"github.com/protobuf-orm/ent/dialect/sql"
	"github.com/protobuf-orm/ent/dialect/sql/schema"
	"github.com/protobuf-orm/ent/schema/field"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"
	"google.golang.org/protobuf/types/known/timestamppb"

	app "github.com/lesomnus/loft"
	"github.com/lesomnus/loft/cli"
	"github.com/lesomnus/loft/cmd"
	"github.com/lesomnus/loft/internal/ent"
	"github.com/lesomnus/loft/internal/ent/event"
	"github.com/lesomnus/loft/internal/ent/migrate"
	"github.com/lesomnus/loft/internal/ent/notification"
	"github.com/lesomnus/loft/internal/identity"
	"github.com/lesomnus/loft/server/domain"
	"github.com/lesomnus/loft/server/pd"
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
	// and of it only what its kind uses; and each notice and row it wrote
	// that says somebody's words is one taking them out finds.
	t.Cleanup(e.heldToKinds)
	t.Cleanup(e.toldAsDeclared)
	t.Cleanup(e.keptAsDeclared)

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

// toldAsDeclared holds every notice of the deployment whose words are what an
// event of its workspace says as its reason to [domain.ToldWhy]: told of such
// an event, of a kind it gives that event's, about what the event is about.
// That is how the words are found in it when they are taken out (design D51),
// and a notice of any other kind would keep them.
func (e *env) toldAsDeclared() {
	e.t.Helper()
	ctx := context.Background()
	ns, err := e.s.Ent.Notification.Query().Where(notification.DescNEQ("")).All(ctx)
	e.x.NoError(err)
	for _, n := range ns {
		vs, err := e.s.Ent.Event.Query().Where(event.TenantId(n.TenantId), event.Reason(n.Desc)).All(ctx)
		e.x.NoError(err)
		if len(vs) == 0 {
			continue
		}
		e.x.True(slices.ContainsFunc(vs, func(v *ent.Event) bool {
			return domain.ToldWhy[v.Kind] == n.Kind && v.SubjectId != nil && n.SubjectId != nil && *v.SubjectId == *n.SubjectId
		}), "a %s notice says what %s %s says as its reason, and domain.ToldWhy does not pair them: taking the words out would leave them in it", n.Kind, vs[0].Kind, pdid.Id(vs[0].Id))
	}
}

// keptAsDeclared holds every row of the deployment whose words are what an
// event of its workspace about it says as its reason -- an event of the row,
// naming it, or of something it points at -- to [domain.KeptWhy]: written by
// such an event, of a kind that gives the field those words are in. And the
// field is declared personal, as the reason it keeps is. That is how the
// words are found in it when they are taken out (design D56); a row of any
// other field would keep them, and a field nobody declared would have put
// them on the trail.
//
// It reads every field of free text of every kind a workspace has rows of,
// off the schema, so that a row a later change makes keep a reason is held to
// this as well. The notices are [env.toldAsDeclared]'s; the trail's and the
// past's own are no rows of this.
func (e *env) keptAsDeclared() {
	e.t.Helper()
	ctx := context.Background()
	// Nothing to keep when no event says a reason, as most tests' do not.
	said, err := e.s.Ent.Event.Query().Where(event.ReasonNEQ("")).Exist(ctx)
	e.x.NoError(err)
	if !said {
		return
	}
	drv := e.s.Ent.Driver()
	skip := map[pdid.Domain]bool{
		pd.EventDomain: true, pd.FactDomain: true, pd.NotificationDomain: true,
		pd.AuditDomain: true, pd.ArchivedDomain: true, pd.AccessDomain: true,
		pd.OutboxDomain: true, pd.OwedDomain: true, pd.RowSealDomain: true,
	}
	for name, d := range pd.Domains {
		if skip[d] {
			continue
		}
		found, err := protoregistry.GlobalFiles.FindDescriptorByName(protoreflect.FullName(name))
		e.x.NoError(err, name)
		md := found.(protoreflect.MessageDescriptor)
		i := slices.IndexFunc(migrate.Tables, func(t *schema.Table) bool { return t.Name == strings.ToLower(string(md.Name())) })
		e.x.GreaterOrEqual(i, 0, "%s has no table", name)
		table := migrate.Tables[i]
		of := func(name string, typ field.Type) bool {
			return slices.ContainsFunc(table.Columns, func(c *schema.Column) bool { return c.Name == name && c.Type == typ })
		}
		// Whose a row is: a workspace's own row is its, and a row of the
		// deployment's is nobody's here.
		whose := "tenant_id"
		if d == pd.TenantDomain {
			whose = "id"
		} else if !of(whose, field.TypeUuid) {
			continue
		}
		// What a row points at, besides whose it is.
		refs := []string{}
		for _, c := range table.Columns {
			if c.Type == field.TypeUuid && c.Name != "id" && c.Name != whose {
				refs = append(refs, c.Name)
			}
		}

		fs := md.Fields()
		for j := range fs.Len() {
			f := fs.Get(j)
			if f.Kind() != protoreflect.StringKind || f.IsList() || f.IsMap() || !of(string(f.Name()), field.TypeString) {
				continue
			}
			col := string(f.Name())
			b := sql.Dialect(drv.Dialect())
			said := b.Table("event").As("said")
			q, args := b.Select(append([]string{"id", whose, col}, refs...)...).From(sql.Table(table.Name)).
				Where(sql.And(
					sql.NEQ(col, ""),
					sql.In(col, b.Select(said.C("reason")).From(said).Where(sql.ColumnsEQ(said.C("tenant_id"), sql.Table(table.Name).C(whose)))),
				)).
				Query()
			rows := &sql.Rows{}
			e.x.NoError(drv.Query(ctx, q, args, rows), "%s.%s", table.Name, col)
			type kept struct {
				id, tenant uuid.UUID
				words      string
				points     []uuid.UUID
			}
			var vs []kept
			for rows.Next() {
				var v kept
				var words string
				ps := make([]sql.Null[uuid.UUID], len(refs))
				dst := []any{&v.id, &v.tenant, &words}
				for k := range ps {
					dst = append(dst, &ps[k])
				}
				e.x.NoError(rows.Scan(dst...), "%s.%s", table.Name, col)
				v.words = words
				for _, p := range ps {
					if p.Valid {
						v.points = append(v.points, p.V)
					}
				}
				vs = append(vs, v)
			}
			e.x.NoError(rows.Err())
			e.x.NoError(rows.Close())

			for _, v := range vs {
				evs, err := e.s.Ent.Event.Query().Where(event.TenantId(v.tenant), event.Reason(v.words)).All(ctx)
				e.x.NoError(err)
				about := slices.DeleteFunc(evs, func(ev *ent.Event) bool { return !aboutRow(ev, d, v.id, v.points) })
				if len(about) == 0 {
					continue
				}
				e.x.True(slices.ContainsFunc(about, func(ev *ent.Event) bool { return domain.KeptWhy[ev.Kind] == f.FullName() }),
					"%s %s says what %s %s says as its reason, and domain.KeptWhy does not pair them: taking the words out would leave them in it", f.FullName(), pdid.Id(v.id), about[0].Kind, pdid.Id(about[0].Id))
				opts, _ := proto.GetExtension(f.Options(), pdpb.E_Field).(*pdpb.Field)
				e.x.True(opts.GetPersonal(), "%s keeps what %s says as its reason, and is not declared personal: the trail keeps it", f.FullName(), about[0].Kind)
			}
		}
	}
}

// aboutRow is whether the event `ev` is about the row `id`, of the domain
// `d`, which points at `points`: the row is the workspace it is of, or its
// subject or something its message names is the row or what it points at.
func aboutRow(ev *ent.Event, d pdid.Domain, id uuid.UUID, points []uuid.UUID) bool {
	if d == pd.TenantDomain {
		return ev.TenantId == id
	}
	near := append([]uuid.UUID{id}, points...)
	if ev.SubjectId != nil && slices.Contains(near, *ev.SubjectId) {
		return true
	}
	if ev.Value == nil {
		return false
	}
	m, err := ev.Value.UnmarshalNew()
	if err != nil {
		return false
	}
	named := false
	var walk func(m protoreflect.Message)
	walk = func(m protoreflect.Message) {
		m.Range(func(f protoreflect.FieldDescriptor, v protoreflect.Value) bool {
			switch {
			case f.Kind() == protoreflect.BytesKind && f.IsList():
				for k := range v.List().Len() {
					named = named || slices.Contains(near, uuid.UUID(v.List().Get(k).Bytes()))
				}
			case f.Kind() == protoreflect.BytesKind && len(v.Bytes()) == 16:
				named = named || slices.Contains(near, uuid.UUID(v.Bytes()))
			case f.Kind() == protoreflect.MessageKind && !f.IsList() && !f.IsMap():
				walk(v.Message())
			}
			return !named
		})
	}
	walk(m.ProtoReflect())
	return named
}

// newTenant puts up a workspace and its owner the way `loft init` does -- the
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
