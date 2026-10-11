package cli_test

import (
	"context"
	"fmt"
	"slices"
	"sort"
	"strings"
	"testing"
	"time"
	"uuid"

	"github.com/protobuf-orm/ent/dialect/sql/schema"

	"github.com/lesomnus/payday/auth/authsession"
	"github.com/lesomnus/payday/frame"
	"github.com/lesomnus/payday/pdid"
	"github.com/lesomnus/payday/pdtest"
	"github.com/lesomnus/z"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"

	app "github.com/lesomnus/rove"
	"github.com/lesomnus/rove/cli"
	"github.com/lesomnus/rove/cmd"
	"github.com/lesomnus/rove/internal/ent/asset"
	"github.com/lesomnus/rove/internal/ent/holder"
	"github.com/lesomnus/rove/internal/ent/migrate"
	"github.com/lesomnus/rove/internal/ent/tenant"
	"github.com/lesomnus/rove/server/pd"
	"github.com/lesomnus/rove/server/policy"
)

// victim is a workspace full of rows, seen from the database: its identifiers
// by domain, and as the strings an answer would carry them in.
type victim struct {
	tenant  uuid.UUID
	byKind  map[pdid.Domain][]uuid.UUID
	byTable map[string][]uuid.UUID
	bytes   map[string]bool
	strings []string
}

// of is one of the victim's rows of an entity: by the domain its identifiers
// carry, or by its table for one whose identifiers carry none (the trail's).
func (v victim) of(entity string) ([]byte, bool) {
	ids := v.byKind[pd.Domains["rove."+entity]]
	if len(ids) == 0 {
		ids = v.byTable[strings.ToLower(entity)]
	}
	if len(ids) == 0 {
		return nil, false
	}
	return ids[0][:], true
}

// tenanted is every table with a tenant_id, by name.
func tenanted() []string {
	out := []string{}
	for _, t := range migrate.Tables {
		if slices.ContainsFunc(t.Columns, func(c *schema.Column) bool { return c.Name == "tenant_id" }) {
			out = append(out, t.Name)
		}
	}
	sort.Strings(out)
	return out
}

func placeholder(s *cmd.Server) string {
	if s.Dialect == "postgres" {
		return "$1"
	}
	return "?"
}

func victimOf(t *testing.T, s *cmd.Server, alias string) victim {
	t.Helper()
	x := require.New(t)
	ctx := context.Background()
	id, err := s.Ent.Tenant.Query().Where(tenant.Alias(alias)).OnlyId(ctx)
	x.NoError(err)

	v := victim{tenant: id, byKind: map[pdid.Domain][]uuid.UUID{pd.TenantDomain: {id}}, byTable: map[string][]uuid.UUID{}, bytes: map[string]bool{string(id[:]): true}}
	for _, table := range tenanted() {
		rows, err := s.Db.QueryContext(ctx, fmt.Sprintf("SELECT id FROM %s WHERE tenant_id = %s", table, placeholder(s)), id)
		x.NoError(err, table)
		for rows.Next() {
			var u uuid.UUID
			x.NoError(rows.Scan(&u), table)
			d := pdid.Id(u).Domain()
			v.byKind[d] = append(v.byKind[d], u)
			v.byTable[table] = append(v.byTable[table], u)
			v.bytes[string(u[:])] = true
			v.strings = append(v.strings, pdid.Id(u).String(), u.String())
		}
		x.NoError(rows.Close())
	}
	return v
}

// snapshot is what a workspace holds, table by table: how many rows, and the
// latest change to any -- enough to say whether anything was written to it.
func snapshot(t *testing.T, s *cmd.Server, id uuid.UUID) map[string]string {
	t.Helper()
	ctx := context.Background()
	out := map[string]string{}
	for _, table := range tenanted() {
		var n int
		require.NoError(t, s.Db.QueryRowContext(ctx, fmt.Sprintf("SELECT count(*) FROM %s WHERE tenant_id = %s", table, placeholder(s)), id).Scan(&n))
		out[table] = fmt.Sprint(n)
		if hasColumn(table, "date_updated") {
			var at any
			require.NoError(t, s.Db.QueryRowContext(ctx, fmt.Sprintf("SELECT max(date_updated) FROM %s WHERE tenant_id = %s", table, placeholder(s)), id).Scan(&at))
			out[table] += fmt.Sprintf(" %v", at)
		}
	}
	return out
}

func hasColumn(table, column string) bool {
	for _, t := range migrate.Tables {
		if t.Name == table {
			return slices.ContainsFunc(t.Columns, func(c *schema.Column) bool { return c.Name == column })
		}
	}
	return false
}

// referent is the entity a field named this refers to, by its stem.
var referent = map[string]string{
	"parent": "Asset", "child": "Asset", "resource": "Asset", "scope": "Asset", "space": "Asset",
	"source": "Asset", "target": "Asset", "subject": "Asset", "item": "Asset", "root": "Asset",
	"vendor": "Party", "custodian": "Party", "person": "Party", "inviter": "Holder", "actor": "Holder",
	"type": "AssetType", "model": "ItemModel", "count": "InventoryCount", "domain": "TenantDomain",
	"row": "Placement", "line": "CustodyLine",
}

func entityOf(stem string) string {
	if name := referent[stem]; name != "" {
		return name
	}
	parts := strings.Split(stem, "_")
	for i, p := range parts {
		parts[i] = strings.ToUpper(p[:1]) + p[1:]
	}
	return strings.Join(parts, "")
}

// refTo names the victim's row in a reference, when the victim has one of
// that entity.
func refTo(ref protoreflect.Message, v victim) bool {
	entity, ok := strings.CutSuffix(string(ref.Descriptor().FullName()), "Ref")
	if !ok {
		return false
	}
	id, has := v.of(strings.TrimPrefix(entity, "rove."))
	idf := ref.Descriptor().Fields().ByName("id")
	if !has || idf == nil {
		return false
	}
	ref.Set(idf, protoreflect.ValueOfBytes(id))
	return true
}

// fill names the victim's rows wherever a request can name a row: every
// reference, every field that holds an identifier of something, all the way
// down a request's own messages. What it cannot know -- a kind, a role -- it
// leaves for the server to refuse, and a reason or a name it gives.
func fill(m protoreflect.Message, v victim, depth int) {
	fields := m.Descriptor().Fields()
	for i := range fields.Len() {
		f := fields.Get(i)
		name := string(f.Name())
		if o := f.ContainingOneof(); o != nil && !o.IsSynthetic() && m.WhichOneof(o) != nil {
			continue
		}
		switch f.Kind() {
		case protoreflect.MessageKind:
			if f.IsMap() {
				continue
			}
			full := string(f.Message().FullName())
			if strings.HasPrefix(full, "google.protobuf.") || strings.HasSuffix(full, "Select") {
				continue
			}
			if strings.HasSuffix(full, "Ref") {
				if ref := newOf(m, f); refTo(ref, v) {
					put(m, f, ref)
				}
				continue
			}
			if depth < 3 {
				sub := newOf(m, f)
				fill(sub, v, depth+1)
				put(m, f, sub)
			}

		case protoreflect.BytesKind:
			if name == "data" && !f.IsList() {
				m.Set(f, protoreflect.ValueOfBytes([]byte("벽 너머")))
				continue
			}
			stem, ok := strings.CutSuffix(name, "_id")
			if name == "op" || !ok {
				continue
			}
			if id, has := v.of(entityOf(stem)); has {
				if f.IsList() {
					m.Mutable(f).List().Append(protoreflect.ValueOfBytes(id))
				} else {
					m.Set(f, protoreflect.ValueOfBytes(id))
				}
			}

		case protoreflect.StringKind:
			if f.IsList() {
				continue
			}
			switch name {
			case "reason", "name", "desc", "note", "title", "summary":
				m.Set(f, protoreflect.ValueOfString("벽 너머"))
			case "role":
				m.Set(f, protoreflect.ValueOfString("manager"))
			}

		case protoreflect.BoolKind:
			if name == "retract" {
				m.Set(f, protoreflect.ValueOfBool(true))
			}

		case protoreflect.Int32Kind, protoreflect.Int64Kind, protoreflect.Uint32Kind:
			if !f.IsList() && (name == "qty" || name == "quantity" || name == "units") {
				switch f.Kind() {
				case protoreflect.Int32Kind:
					m.Set(f, protoreflect.ValueOfInt32(1))
				case protoreflect.Int64Kind:
					m.Set(f, protoreflect.ValueOfInt64(1))
				default:
					m.Set(f, protoreflect.ValueOfUint32(1))
				}
			}
		}
	}
}

func newOf(m protoreflect.Message, f protoreflect.FieldDescriptor) protoreflect.Message {
	if f.IsList() {
		return m.Mutable(f).List().NewElement().Message()
	}
	return m.NewField(f).Message()
}

func put(m protoreflect.Message, f protoreflect.FieldDescriptor, v protoreflect.Message) {
	if f.IsList() {
		m.Mutable(f).List().Append(protoreflect.ValueOfMessage(v))
		return
	}
	m.Set(f, protoreflect.ValueOfMessage(v))
}

// alone is `full` with only its field `keep` set, for a request to name the
// victim in one place at a time: a request that names them everywhere is often
// refused for something else first.
func alone(full protoreflect.Message, keep protoreflect.FieldDescriptor) protoreflect.Message {
	out := full.New()
	full.Range(func(f protoreflect.FieldDescriptor, val protoreflect.Value) bool {
		switch f.Kind() {
		case protoreflect.MessageKind, protoreflect.BytesKind:
			if f.Number() != keep.Number() {
				return true
			}
		}
		out.Set(f, val)
		return true
	})
	return out
}

// leaks is where in an answer one of the victim's identifiers is, or "".
func leaks(m protoreflect.Message, v victim, path string) string {
	found := ""
	m.Range(func(f protoreflect.FieldDescriptor, val protoreflect.Value) bool {
		here := path + "." + string(f.Name())
		check := func(val protoreflect.Value) {
			if found != "" {
				return
			}
			switch {
			case f.Kind() == protoreflect.BytesKind:
				if v.bytes[string(val.Bytes())] {
					found = here
				}
			case f.Kind() == protoreflect.StringKind:
				for _, s := range v.strings {
					if strings.Contains(val.String(), s) {
						found = here
						return
					}
				}
			case f.Kind() == protoreflect.MessageKind:
				found = leaks(val.Message(), v, here)
			}
		}
		switch {
		case f.IsList():
			l := val.List()
			for i := range l.Len() {
				check(l.Get(i))
			}
		case f.IsMap():
			val.Map().Range(func(k protoreflect.MapKey, mv protoreflect.Value) bool {
				if f.MapValue().Kind() == protoreflect.MessageKind {
					if found == "" {
						found = leaks(mv.Message(), v, here)
					}
				} else if f.MapValue().Kind() == protoreflect.StringKind {
					for _, s := range v.strings {
						if strings.Contains(mv.String(), s) {
							found = here
						}
					}
				}
				return found == ""
			})
		default:
			check(val)
		}
		return found == ""
	})
	return found
}

// furnish gives a demo workspace one of each kind of row the demo does not
// make, so that a call naming one names a real row of it: a label domain and a
// label, a count and what it found, an attachment, an invitation -- as its
// owner, over the wire -- and the operator's legal hold, contract and usage
// day.
func furnish(t *testing.T, s *cmd.Server, conn *grpc.ClientConn, alias, login string) {
	t.Helper()
	x := require.New(t)
	ctx := context.Background()
	tid, err := s.Ent.Tenant.Query().Where(tenant.Alias(alias)).OnlyId(ctx)
	x.NoError(err)
	owner, err := s.Ent.Holder.Query().Where(holder.Alias(login), holder.TenantId(tid)).Only(ctx)
	x.NoError(err)
	_, cookie, err := s.Sessions.Mint(ctx, authsession.Session{Id: pdid.Id(*owner.Account).String(), Grant: frame.Whole()})
	x.NoError(err)
	as := metadata.AppendToOutgoingContext(ctx, "cookie", cookie.Name+"="+cookie.Value, cmd.WorkspaceHeader, alias)

	space, err := s.Ent.Asset.Query().Where(asset.TenantId(tid), asset.Kind("space")).First(ctx)
	x.NoError(err)
	item, err := s.Ent.Asset.Query().Where(asset.TenantId(tid), asset.Kind("item")).First(ctx)
	x.NoError(err)

	// `init` gives a workspace its default label domain when there is a
	// suffix to give it under.
	if _, err = app.NewTenantDomainServiceClient(conn).Add(as, app.TenantDomainAddRequest_builder{Sub: z.Ptr(alias)}.Build()); status.Code(err) != codes.AlreadyExists {
		x.NoError(err)
	}
	_, err = app.NewLabelServiceClient(conn).Print(as, app.LabelPrintRequest_builder{Count: 1}.Build())
	x.NoError(err)
	count, err := app.NewInventoryCountServiceClient(conn).Add(as, app.InventoryCountAddRequest_builder{
		Name: "실사", Scope: app.AssetRef_builder{Id: pdid.Id(space.Id).Bytes()}.Build(),
	}.Build())
	x.NoError(err)
	_, err = app.NewInventoryCountServiceClient(conn).Scan(as, app.InventoryCountScanRequest_builder{
		Ref: app.InventoryCountRef_builder{Id: count.GetId()}.Build(), Asset: app.AssetRef_builder{Id: pdid.Id(item.Id).Bytes()}.Build(),
	}.Build())
	x.NoError(err)
	_, err = app.NewAttachmentServiceClient(conn).Upload(as, app.AttachmentUploadRequest_builder{
		SubjectId: pdid.Id(item.Id).Bytes(), Name: "영수증.txt", ContentType: "text/plain", Data: []byte("영수증"),
	}.Build())
	x.NoError(err)
	_, err = app.NewInvitationServiceClient(conn).Add(as, app.InvitationAddRequest_builder{Email: "someone@example.com", Role: "member"}.Build())
	x.NoError(err)

	tref := app.TenantRef_builder{Id: pdid.Id(tid).Bytes()}.Build()
	_, err = s.Base.LegalHold().Add(ctx, app.LegalHoldAddRequest_builder{Tenant: tref, Name: "사건 1"}.Build())
	x.NoError(err)
	_, err = s.Base.TenantContract().Add(ctx, app.TenantContractAddRequest_builder{Tenant: tref, Name: "계약", ViewDays: 3650, KeepDays: 3650}.Build())
	x.NoError(err)
	_, err = s.Base.UsageSnapshot().Add(ctx, app.UsageSnapshotAddRequest_builder{Tenant: tref, Day: "2026-10-01", Metrics: map[string]string{"active_assets": "1"}}.Build())
	x.NoError(err)
}

// TestNoCallReachesAnotherWorkspace: the M1 sweep, every method the policy
// table opens -- so a method added to it is in here without anybody writing a
// line -- called as the owner of one workspace with the identifiers of
// another's rows wherever the request can carry one. None answers with
// anything of the other workspace, none fails in a way that says it got past
// every check (Internal, Unknown), and the other workspace is as it was.
func TestNoCallReachesAnotherWorkspace(t *testing.T) {
	x := require.New(t)
	ctx := context.Background()

	c := cmd.Config{}
	c.Db.Driver, c.Db.Dsn = pdtest.DB(t)
	c.Watch.Broker = "memory"
	c.App.Files = t.TempDir()
	c.Auth.Roster.Db.Driver, c.Auth.Roster.Db.Dsn = pdtest.DB(t)
	c.App.Labels.Suffix = "l.test"
	run(t, cli.NewCmdInit(&c), "--tenant", "acme", "--login", "owner", "--password", "owner1234")
	run(t, cli.NewCmdInit(&c), "--tenant", "beta", "--login", "boss", "--password", "boss12345", "--demo")

	s, err := cmd.Build(ctx, c)
	x.NoError(err)
	t.Cleanup(func() { s.Close() })
	g, err := s.Grpc(ctx, c)
	x.NoError(err)
	conn := pdtest.Serve(t, g)

	owner, err := s.Ent.Holder.Query().Where(holder.Role("owner"), holder.HasTenantWith(tenant.Alias("acme"))).Only(ctx)
	x.NoError(err)
	_, cookie, err := s.Sessions.Mint(ctx, authsession.Session{Id: pdid.Id(*owner.Account).String(), Grant: frame.Whole()})
	x.NoError(err)
	as := metadata.AppendToOutgoingContext(ctx, "cookie", cookie.Name+"="+cookie.Value, cmd.WorkspaceHeader, "acme")

	furnish(t, s, conn, "beta", "boss")
	beta := victimOf(t, s, "beta")
	kinds := []string{}
	for d, ids := range beta.byKind {
		kinds = append(kinds, fmt.Sprintf("%d:%d", d, len(ids)))
	}
	sort.Strings(kinds)
	t.Logf("the other workspace's rows, by domain: %v", kinds)
	x.Greater(len(beta.byKind), 25, "a row of nearly every kind")
	before := snapshot(t, s, beta.tenant)

	methods := []string{}
	for m := range policy.Table {
		methods = append(methods, m)
	}
	sort.Strings(methods)

	codesOf := map[codes.Code][]string{}
	absent := []string{}
	call := (&sweep{t: t, conn: conn, as: as, beta: beta, codesOf: codesOf}).call
	for _, full := range methods {
		svc, name, _ := strings.Cut(strings.TrimPrefix(full, "/"), "/")
		d, err := protoregistry.GlobalFiles.FindDescriptorByName(protoreflect.FullName(svc))
		x.NoError(err, full)
		md := d.(protoreflect.ServiceDescriptor).Methods().ByName(protoreflect.Name(name))
		if md == nil {
			// The table opens every read of a service, and some serve no
			// Watch: a name nobody can call.
			absent = append(absent, full)
			continue
		}
		if svc == "payday.BatchService" {
			// Every operation in a batch is held to the same table and the
			// same layers; the operations themselves are swept here one by one.
			continue
		}

		in, err := protoregistry.GlobalTypes.FindMessageByName(md.Input().FullName())
		x.NoError(err, full)
		out, err := protoregistry.GlobalTypes.FindMessageByName(md.Output().FullName())
		x.NoError(err, full)
		req := in.New()
		reqs := []protoreflect.Message{req}
		if !refTo(req, beta) {
			fill(req, beta, 0)
			req.Range(func(f protoreflect.FieldDescriptor, _ protoreflect.Value) bool {
				if f.Kind() == protoreflect.MessageKind || f.Kind() == protoreflect.BytesKind {
					reqs = append(reqs, alone(req, f))
				}
				return true
			})
			if len(reqs) == 2 {
				reqs = reqs[:1]
			}
		}
		for _, req := range reqs {
			call(full, md, req, out)
		}
	}

	for _, k := range []codes.Code{codes.OK, codes.NotFound, codes.InvalidArgument, codes.PermissionDenied, codes.FailedPrecondition} {
		t.Logf("%v: %d", k, len(codesOf[k]))
	}
	for k, ms := range codesOf {
		switch k {
		case codes.OK, codes.NotFound, codes.InvalidArgument:
		case codes.PermissionDenied, codes.FailedPrecondition, codes.Unimplemented:
			// A refusal that names nothing of theirs; said, so that a new
			// one is looked at.
			t.Logf("%v: %v", k, ms)
		default:
			t.Errorf("%v is no answer to a call naming another workspace's rows: %v", k, ms)
		}
	}
	t.Logf("opened by the table, served by nobody: %v", absent)
	x.Greater(len(codesOf[codes.NotFound]), 100, "the wall is what answered most of them")
	x.Equal(before, snapshot(t, s, beta.tenant), "the other workspace is as it was")
}

// sweep is one workspace's owner calling into another's rows.
type sweep struct {
	t       *testing.T
	conn    *grpc.ClientConn
	as      context.Context
	beta    victim
	codesOf map[codes.Code][]string
}

func (w *sweep) call(full string, md protoreflect.MethodDescriptor, req protoreflect.Message, out protoreflect.MessageType) {
	t, as, conn, beta, codesOf := w.t, w.as, w.conn, w.beta, w.codesOf
	{
		var got protoreflect.Message
		var err error
		if md.IsStreamingServer() {
			sctx, cancel := context.WithTimeout(as, 500*time.Millisecond)
			var stream grpc.ClientStream
			stream, err = conn.NewStream(sctx, &grpc.StreamDesc{ServerStreams: true}, full)
			if err == nil {
				err = stream.SendMsg(req.Interface())
			}
			if err == nil {
				err = stream.CloseSend()
			}
			if err == nil {
				res := out.New()
				if err = stream.RecvMsg(res.Interface()); err == nil {
					got = res
				}
			}
			cancel()
			if status.Code(err) == codes.DeadlineExceeded {
				// Opened and said nothing: nothing of theirs came down it.
				err = nil
			}
			codesOf[status.Code(err)] = append(codesOf[status.Code(err)], full)
		} else {
			res := out.New()
			err = conn.Invoke(as, full, req.Interface(), res.Interface())
			if err == nil {
				got = res
			}
			codesOf[status.Code(err)] = append(codesOf[status.Code(err)], full)
		}

		if testing.Verbose() && status.Code(err) != codes.NotFound {
			t.Logf("%-55s %v %s", full, status.Code(err), status.Convert(err).Message())
		}
		if got != nil {
			if where := leaks(got, beta, ""); where != "" {
				t.Errorf("%s answered with another workspace's row at %s", full, where)
			}
		}
	}
}
