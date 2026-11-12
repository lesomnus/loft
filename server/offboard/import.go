package offboard

import (
	"archive/zip"
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"reflect"
	"slices"
	"strings"
	"time"
	"unicode"
	"uuid"

	"github.com/lesomnus/payday/pdid"
	"github.com/protobuf-orm/ent/dialect/sql/schema"
	"github.com/protobuf-orm/protobuf-orm/ormpb"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"
	"google.golang.org/protobuf/types/known/anypb"
	"google.golang.org/protobuf/types/known/timestamppb"

	app "github.com/lesomnus/loft"
	"github.com/lesomnus/loft/internal/ent"
	"github.com/lesomnus/loft/internal/ent/attachment"
	"github.com/lesomnus/loft/internal/ent/migrate"
	enttenant "github.com/lesomnus/loft/internal/ent/tenant"
	"github.com/lesomnus/loft/server/operator"
	"github.com/lesomnus/loft/server/pd"
)

// ImportOptions is what an operator says of a restore beyond the export.
type ImportOptions struct {
	// Dry does all of it and keeps none of it: what a restore would put back.
	Dry bool
}

// Restored is what a restore put back, or would have.
type Restored struct {
	Tenant pdid.Id

	// Alias is the address the workspace is at -- the tenant's at roster
	// ([Deployment.Serves], D86) -- and Asked the export's when that is
	// another: the tenant was renamed at roster since.
	Alias, Asked string

	// Reopened says it was closed when it was exported, and is open: a
	// restore is to have it back, and one closed past its window would be
	// destroyed again by the next sweep.
	Reopened bool

	// Retired says the workspace retired from this deployment -- the trail
	// keeps the record that it left (`trail.Policy.Retired`) -- and Returned
	// how many records of that the trail lifted taking it back before
	// anything of it was written (`trail.Policy.Return`, payday#141): none on
	// a dry run, which takes nothing back.
	Retired  bool
	Returned int

	Rows map[string]int

	// Files is the stored files put back, and Missing the ones its rows name
	// that the export did not have -- missing from the store it came from
	// when it was made, as its manifest says.
	Files   int
	Missing []string

	// Unlinked is the members, by alias, whom this deployment's roster does
	// not have ([Deployment.Known]): an export from another deployment's, or
	// from this one's after its roster forgot them. They are restored as they
	// are, and nobody signs in as them until `loft identity link` makes them
	// there.
	Unlinked []string

	// Manifest and Key are the export's [Fingerprints], which the restore's
	// event records.
	Manifest, Key string
}

// Total is the rows put back.
func (x Restored) Total() int {
	n := 0
	for _, v := range x.Rows {
		n += v
	}
	return n
}

// ErrNotNew is a restore of a workspace this deployment has: a restore puts
// back what is not there and never writes over what is, which is also what
// keeps an old export from turning a workspace back.
var ErrNotNew = errors.New("the deployment has the workspace already")

// ErrNotServed is a restore of a workspace that is not the tenant at roster
// the deployment serves (D85): nobody there could sign in to it.
var ErrNotServed = errors.New("the workspace is not the tenant this deployment serves")

// Import restores a workspace from its export into a deployment that does not
// have it: every row as it was exported, under the identifier it had, so that
// what pointed at it -- a printed label, a link -- still does (design D34). It
// is the one the workspace was purged from, or another serving the same
// tenant at roster ([Deployment.Serves]); and it is at that tenant's address,
// whatever the export says (D86).
//
// Only an export [Verify] answers for is read, and the whole of it is one
// transaction: a restore is all of the workspace or none of it. The files its
// rows name are put back before that commits, and taken out again when it
// does not.
//
// The trail is not put back, nor kept. It is the history of another chain,
// which this deployment's checkpoints do not vouch for and cannot take in
// without breaking; and kept beside it as it was signed, it is a copy no
// erasure of a person reaches without breaking the signature. The export is
// that history, verified by its own signature where it is kept, and the event
// a restore records names it by its [Fingerprints].
//
// What this deployment's trail says instead is the restore itself: a row of it
// for every row put back, as the act [Imported], keeping the digest of the row
// as it is and nothing of what it holds (payday#108). A check of the rows then
// finds each as the restore left it, rather than as a row nothing on the trail
// is about -- which is what a row somebody added past the servers looks like.
// It seals each too (payday#115), since an export carries no seal.
//
// # A workspace that retired from here
//
// A retirement leaves the trail what the deployment's floors are on and a
// record that the tenant left, which every pass goes by whatever the app
// answers: what such a tenant writes goes at the next pass, and its whole
// trail and its key once the last floor has passed. So a workspace the trail
// keeps that record of (`trail.Policy.Retired`) is taken back by the trail
// first (`trail.Policy.Return`, payday#141), before anything of it is written
// -- after which its windows are its contract's again, and what the
// retirement kept is its own. A dry run takes nothing back, and says it would
// ([Restored.Retired]). A purge leaves the trail no such record, and a purged
// workspace needs nothing taken back.
//
// Nobody in the workspace does this, and the event it records says so by
// naming nobody.
func (d Deployment) Import(ctx context.Context, r io.ReaderAt, size int64, o ImportOptions) (Restored, error) {
	out := Restored{Rows: map[string]int{}}
	m, err := Verify(r, size, d.Trusted())
	if err != nil {
		return out, err
	}

	t := &app.Tenant{}
	if err := protojson.Unmarshal(m.Tenant, t); err != nil {
		return out, fmt.Errorf("%w: its tenant: %v", ErrFormat, err)
	}
	tid, err := keyOf(t.GetId())
	if err != nil {
		return out, fmt.Errorf("%w: its tenant: %v", ErrFormat, err)
	}
	out.Tenant = pdid.Id(tid)
	if out.Manifest, out.Key, err = Fingerprints(r, size); err != nil {
		return out, err
	}

	// The tenant at roster this deployment serves, and the address it goes
	// by there, which is the workspace's (D86). A roster with no tenant yet
	// -- the one in this process, before a restore it is made for -- takes
	// any, at the export's address, and `loft identity link` makes the
	// tenant by the workspace's identifier.
	out.Alias = t.GetAlias()
	if d.Serves != nil {
		served, alias, err := d.Serves(ctx)
		if err != nil {
			return out, fmt.Errorf("roster: %w", err)
		}
		if !served.IsZero() {
			if served != out.Tenant {
				return out, fmt.Errorf("%w: it is %s (@%s), and the tenant at roster is %s (@%s)", ErrNotServed, out.Tenant, t.GetAlias(), served, alias)
			}
			out.Alias = alias
		}
	}
	if out.Alias != t.GetAlias() {
		out.Asked = t.GetAlias()
		t.SetAlias(out.Alias)
	}

	z, err := zip.NewReader(r, size)
	if err != nil {
		return out, err
	}

	self, err := planOf("tenant")
	if err != nil {
		return out, err
	}
	tables, err := Tables()
	if err != nil {
		return out, err
	}
	// Each after every table it points at: the order a purge deletes in,
	// backwards.
	slices.Reverse(tables)
	plans := []*plan{}
	for _, name := range tables {
		if _, ok := Exported[name]; !ok {
			continue
		}
		p, err := planOf(name)
		if err != nil {
			return out, err
		}
		plans = append(plans, p)
	}
	// Rows of a table this build does not have are rows it cannot put back,
	// and a restore that left them out would say it was all of it.
	for name, n := range m.Rows {
		if n == 0 || slices.ContainsFunc(plans, func(p *plan) bool { return p.table == name }) {
			continue
		}
		return out, fmt.Errorf("%w: it has %d rows of %s, which this build does not have", ErrFormat, n, name)
	}

	// Taken back by the trail first, if it retired from here: outside the
	// restore's transaction, since it is an act of the trail's that writes
	// the archive's account and a checkpoint, and before anything of the
	// workspace is written, which a pass between the two would take.
	if here, err := d.Ent.Tenant.Query().Where(enttenant.Id(tid)).Exist(ctx); err != nil {
		return out, err
	} else if here {
		return out, fmt.Errorf("%w: %s (@%s) is here; a restore is into a deployment that does not have it", ErrNotNew, out.Tenant, t.GetAlias())
	}
	retired, err := d.Trail.Retired(ctx, pd.TrailStore(d.Ent))
	if err != nil {
		return out, fmt.Errorf("the trail, whether the workspace retired from here: %w", err)
	}
	if _, out.Retired = retired[out.Tenant]; out.Retired && !o.Dry {
		back, err := d.Trail.Return(ctx, pd.TrailStore(d.Ent), out.Tenant)
		if err != nil {
			return out, fmt.Errorf("the trail, taking back a workspace that retired from here: %w", err)
		}
		out.Returned = back.Records
	}

	tx, err := d.Ent.Tx(ctx)
	if err != nil {
		return out, err
	}
	defer tx.Rollback()
	db := tx.Client()

	if here, err := db.Tenant.Query().Where(enttenant.Id(tid)).Exist(ctx); err != nil {
		return out, err
	} else if here {
		return out, fmt.Errorf("%w: %s (@%s) is here; a restore is into a deployment that does not have it", ErrNotNew, out.Tenant, t.GetAlias())
	}

	if t.HasDateClosed() {
		out.Reopened = true
		t.ClearDateClosed()
		t.SetClosedBy("")
		t.SetCloseReason("")
		t.ClearDateDestroy()
	}
	if err := self.write(ctx, db, []protoreflect.Message{t.ProtoReflect()}); err != nil {
		return out, fmt.Errorf("tenant: %w", err)
	}

	// Every row put back, for the trail to say so.
	restored := []pdid.Id{out.Tenant}
	for _, p := range plans {
		n, err := d.restore(ctx, db, z, p, tid, &out, &restored)
		if err != nil {
			return out, err
		}
		if n > 0 {
			out.Rows[p.table] = n
		}
	}

	// The restore's event, beneath the servers as the rows it put back are,
	// and on the trail with them: what the past's own `Begin` would have
	// written, and cannot, in a workspace with nobody in it to have done it
	// (payday#151). Its message is what it holds; it keeps no sentence.
	now := d.now().UTC().Truncate(time.Microsecond)
	value, err := anypb.New(app.WorkspaceImported_builder{
		DateExported: timestamppb.New(m.DateExported),
		Manifest:     out.Manifest,
		SignedBy:     out.Key,
		Rows:         uint32(out.Total()),
		Files:        uint32(m.Files),
		Asked:        out.Asked,
		Unlinked:     uint32(len(out.Unlinked)),
		Reopened:     out.Reopened,
	}.Build())
	if err != nil {
		return out, fmt.Errorf("the restore's event: %w", err)
	}
	event := pdid.New(pd.EventDomain)
	q := db.Event.Create().
		SetId(event.Uuid()).
		SetTenantId(tid).
		SetKind("workspace.import").
		SetValue(value).
		SetReason("").
		SetDateOccurred(now).
		SetDateCreated(now).
		SetDateUpdated(now)
	// Nobody's in the workspace -- but done at an operator's word, theirs
	// (D74), as the trail's record of it is.
	if a := operator.Actor(ctx); !a.IsZero() {
		q.SetActorId(a.Uuid())
	}
	if err := q.Exec(ctx); err != nil {
		return out, fmt.Errorf("the restore's event: %w", err)
	}
	if _, err := pd.Record(ctx, db, d.Audit, Imported, append(restored, event)); err != nil {
		return out, fmt.Errorf("the restore's trail: %w", err)
	}

	if d.Reindex != nil {
		if err := d.Reindex(ctx, db, tid); err != nil {
			return out, err
		}
	}

	// The files its attachments kept, before the rows that name them hold: a
	// restore that stops between leaves files that nothing names, which is
	// nothing anybody reaches, and never rows that name nothing.
	keys, err := db.Attachment.Query().Where(attachment.TenantId(tid)).Select(attachment.FieldObjectKey).Strings(ctx)
	if err != nil {
		return out, err
	}
	slices.Sort(keys)
	put := []string{}
	undo := func() {
		for _, k := range put {
			_ = d.Files.Delete(context.WithoutCancel(ctx), k)
		}
	}
	for _, k := range keys {
		f, err := z.Open("files/" + k)
		if err != nil {
			out.Missing = append(out.Missing, k)
			continue
		}
		if o.Dry {
			f.Close()
			out.Files++
			continue
		}
		if d.Files == nil {
			f.Close()
			return out, errors.New("the export has files and this deployment keeps none (app.files)")
		}
		_, err = d.Files.Put(ctx, k, f)
		f.Close()
		if err != nil {
			undo()
			return out, fmt.Errorf("files/%s: %w", k, err)
		}
		put = append(put, k)
		out.Files++
	}

	if o.Dry {
		return out, nil
	}
	if err := tx.Commit(); err != nil {
		undo()
		return out, err
	}
	return out, nil
}

// Imported is the act a restore is on the trail as.
const Imported = "loft.workspace.import"

// restore writes back every row of one table the export has, as it was. A
// table with no file in the export has no rows in it.
func (d Deployment) restore(ctx context.Context, db *ent.Client, z *zip.Reader, p *plan, tenant uuid.UUID, out *Restored, restored *[]pdid.Id) (int, error) {
	name := p.table
	f, err := z.Open("rows/" + name + ".jsonl")
	if errors.Is(err, fs.ErrNotExist) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	defer f.Close()

	n := 0
	batch := make([]protoreflect.Message, 0, p.chunk)
	flush := func() error {
		if len(batch) == 0 {
			return nil
		}
		if err := p.write(ctx, db, batch); err != nil {
			return fmt.Errorf("%s: %w", p.table, err)
		}
		batch = batch[:0]
		return nil
	}

	s := bufio.NewScanner(f)
	s.Buffer(make([]byte, 0, 64<<10), 64<<20)
	for s.Scan() {
		line := bytes.TrimSpace(s.Bytes())
		if len(line) == 0 {
			continue
		}
		n++
		v := p.msg.New()
		if err := protojson.Unmarshal(line, v.Interface()); err != nil {
			return n, fmt.Errorf("%w: rows/%s.jsonl, line %d: %v", ErrFormat, name, n, err)
		}
		if k, err := keyOf(v.Get(p.tenant).Message().Get(p.key).Bytes()); err != nil || k != tenant {
			return n, fmt.Errorf("%w: rows/%s.jsonl, line %d: a row of another tenant", ErrFormat, name, n)
		}
		if adjust, ok := adjusts[p.table]; ok {
			if err := adjust(ctx, d, v, out); err != nil {
				return n, fmt.Errorf("%s, line %d: %w", p.table, n, err)
			}
		}
		batch = append(batch, v)
		if id, err := pdid.From(v.Get(v.Descriptor().Fields().ByName("id")).Bytes()); err == nil {
			*restored = append(*restored, id)
		}
		if len(batch) == p.chunk {
			if err := flush(); err != nil {
				return n, err
			}
		}
	}
	if err := s.Err(); err != nil {
		return n, fmt.Errorf("rows/%s.jsonl: %w", name, err)
	}
	return n, flush()
}

// adjusts is what a restore makes of a row of a table, beside writing it as
// it was.
var adjusts = map[string]func(ctx context.Context, d Deployment, v protoreflect.Message, out *Restored) error{
	// A member is the holder at roster by their own identifier (D85): one
	// this deployment's roster does not have is restored as they are, and
	// said, for `loft identity link` to make there. An ended membership is
	// nobody to make.
	"holder": func(ctx context.Context, d Deployment, v protoreflect.Message, out *Restored) error {
		h := v.Interface().(*app.Holder)
		if d.Known == nil || h.HasDateErased() {
			return nil
		}
		id, err := keyOf(h.GetId())
		if err != nil {
			return fmt.Errorf("%w: a member: %v", ErrFormat, err)
		}
		known, err := d.Known(ctx, pdid.Id(id))
		if err != nil {
			return fmt.Errorf("roster: %w", err)
		}
		if !known {
			out.Unlinked = append(out.Unlinked, h.GetAlias())
		}
		return nil
	},
}

func keyOf(b []byte) (uuid.UUID, error) {
	if len(b) != len(uuid.UUID{}) {
		return uuid.UUID{}, fmt.Errorf("a key of %d bytes", len(b))
	}
	return uuid.UUID(b), nil
}

// plan is how the rows of one table are written back: the message each line
// of its file is, and for each field of the message, the setter of ent's
// builder that takes it.
//
// It is read off the schema and the builders ent generated, by the names the
// two share, so that a value is stored the way ent stores it -- a key, a time,
// a map -- and so that an entity, or a field, added later is restored without
// anybody remembering to.
type plan struct {
	table  string
	entity string // as `ent.Client` names its client
	msg    protoreflect.MessageType
	fields []setter

	// The edge to the tenant, and the key of the row it names; nil on the
	// tenant's own table.
	tenant, key protoreflect.FieldDescriptor

	// chunk is the rows written in one statement: as many as stay well
	// below what a statement may bind, on SQLite and on PostgreSQL.
	chunk int
}

type setter struct {
	f protoreflect.FieldDescriptor
	// key is the key of the row an edge names, and nil for a field that is
	// not an edge.
	key protoreflect.FieldDescriptor

	method   int // of the builder
	want     reflect.Type
	nullable bool
}

var (
	typeTime = reflect.TypeFor[time.Time]()
	typeUuid = reflect.TypeFor[uuid.UUID]()
)

// planOf is the [plan] of a table, or what in its schema has no place to go.
func planOf(table string) (*plan, error) {
	i := slices.IndexFunc(migrate.Tables, func(t *schema.Table) bool { return t.Name == table })
	if i < 0 {
		return nil, fmt.Errorf("no table %s", table)
	}
	columns := map[string]*schema.Column{}
	for _, c := range migrate.Tables[i].Columns {
		columns[c.Name] = c
	}

	// The message the table's rows are: ent names a table after its entity,
	// in lower case and without separators.
	var mt protoreflect.MessageType
	protoregistry.GlobalTypes.RangeMessages(func(v protoreflect.MessageType) bool {
		d := v.Descriptor()
		if d.ParentFile().Package() == "loft" && d.Parent() == d.ParentFile() && strings.ToLower(string(d.Name())) == table {
			mt = v
			return false
		}
		return true
	})
	if mt == nil {
		return nil, fmt.Errorf("%s: no message is its rows", table)
	}
	entity := string(mt.Descriptor().Name())

	client, ok := reflect.TypeFor[ent.Client]().FieldByName(entity)
	if !ok {
		return nil, fmt.Errorf("%s: ent has no client of %s", table, entity)
	}
	create, ok := client.Type.MethodByName("Create")
	if !ok {
		return nil, fmt.Errorf("%s: %s makes no rows", table, client.Type)
	}
	builder := create.Type.Out(0)

	p := &plan{table: table, entity: entity, msg: mt, chunk: max(1, 8000/len(columns))}
	fs := mt.Descriptor().Fields()
	for i := range fs.Len() {
		f := fs.Get(i)
		if !stored(f) {
			// No row holds it, so no export's row does either.
			continue
		}
		s := setter{f: f}

		name := string(f.Name())
		if f.Kind() == protoreflect.MessageKind && !f.IsMap() && columns[name] == nil {
			// An edge, which the row has by the key of the row it names.
			if f.IsList() {
				// The rows of an edge to many are the other table's.
				continue
			}
			s.key = f.Message().Fields().ByName("id")
			if s.key == nil {
				return nil, fmt.Errorf("%s.%s: an edge to a row with no key", table, name)
			}
			name += "_id"
		}
		c := columns[name]
		if c == nil {
			return nil, fmt.Errorf("%s.%s: no column is it", table, f.Name())
		}
		s.nullable = c.Nullable

		m, ok := builder.MethodByName("Set" + goName(name))
		if !ok {
			return nil, fmt.Errorf("%s.%s: %s has no setter of it", table, f.Name(), builder)
		}
		s.method = m.Index
		s.want = m.Type.In(1)
		if err := s.check(); err != nil {
			return nil, fmt.Errorf("%s.%s: %w", table, f.Name(), err)
		}

		if name == "tenant_id" && s.key != nil {
			p.tenant, p.key = f, s.key
		}
		p.fields = append(p.fields, s)
	}
	if p.tenant == nil && table != "tenant" {
		return nil, fmt.Errorf("%s: its rows name no tenant", table)
	}
	return p, nil
}

// stored says whether a table keeps f in a column. protobuf-orm makes none
// of a field or an edge the schema declares disabled: a message may carry
// what no row holds, as payday's `Event` carries `desc`, the sentence an
// event is said in when it is read (payday#224).
func stored(f protoreflect.FieldDescriptor) bool {
	of, _ := proto.GetExtension(f.Options(), ormpb.E_Field).(*ormpb.FieldOptions)
	oe, _ := proto.GetExtension(f.Options(), ormpb.E_Edge).(*ormpb.EdgeOptions)
	return !of.GetDisabled() && !oe.GetDisabled()
}

// check refuses a setter that does not take what the field is.
func (s setter) check() error {
	f := s.f
	got := ""
	switch {
	case s.key != nil:
		if s.want == typeUuid {
			return nil
		}
		got = "a key"
	case f.IsMap():
		if f.MapKey().Kind() == protoreflect.StringKind && f.MapValue().Kind() == protoreflect.StringKind && s.want == reflect.TypeFor[map[string]string]() {
			return nil
		}
		got = "a map"
	case f.IsList():
		if f.Kind() == protoreflect.StringKind && s.want == reflect.TypeFor[[]string]() {
			return nil
		}
		got = "a list"
	case f.Kind() == protoreflect.MessageKind && f.Message().FullName() == "google.protobuf.Timestamp":
		if s.want == typeTime {
			return nil
		}
		got = "a time"
	case f.Kind() == protoreflect.MessageKind:
		mt, err := protoregistry.GlobalTypes.FindMessageByName(f.Message().FullName())
		if err == nil && reflect.TypeOf(mt.Zero().Interface()) == s.want {
			return nil
		}
		got = "a message"
	case f.Kind() == protoreflect.BytesKind:
		if s.want == typeUuid || s.want == reflect.TypeFor[[]byte]() {
			return nil
		}
		got = "bytes"
	case f.Kind() == protoreflect.EnumKind:
		got = "an enum"
	default:
		if reflect.TypeOf(f.Default().Interface()).ConvertibleTo(s.want) {
			return nil
		}
		got = f.Kind().String()
	}
	return fmt.Errorf("%s, which a setter of %s does not take", got, s.want)
}

// value is what the setter is given for the row, and false for a field the
// row is to have none of: NULL as it was, or what the column is when nothing
// is said.
func (s setter) value(m protoreflect.Message) (reflect.Value, bool, error) {
	f := s.f
	if !m.Has(f) && (s.nullable || f.HasPresence()) {
		return reflect.Value{}, false, nil
	}
	v := m.Get(f)
	switch {
	case s.key != nil:
		k, err := keyOf(v.Message().Get(s.key).Bytes())
		return reflect.ValueOf(k), err == nil, err
	case f.IsMap():
		out := map[string]string{}
		v.Map().Range(func(k protoreflect.MapKey, v protoreflect.Value) bool {
			out[k.String()] = v.String()
			return true
		})
		return reflect.ValueOf(out), true, nil
	case f.IsList():
		out := make([]string, v.List().Len())
		for i := range out {
			out[i] = v.List().Get(i).String()
		}
		return reflect.ValueOf(out), true, nil
	case s.want == typeTime:
		return reflect.ValueOf(v.Message().Interface().(*timestamppb.Timestamp).AsTime()), true, nil
	case f.Kind() == protoreflect.MessageKind:
		return reflect.ValueOf(v.Message().Interface()), true, nil
	case s.want == typeUuid:
		if len(v.Bytes()) == 0 {
			return reflect.ValueOf(uuid.UUID{}), true, nil
		}
		k, err := keyOf(v.Bytes())
		return reflect.ValueOf(k), err == nil, err
	}
	return reflect.ValueOf(v.Interface()).Convert(s.want), true, nil
}

// write saves rows of the plan's table, in one statement.
func (p *plan) write(ctx context.Context, db *ent.Client, rows []protoreflect.Message) error {
	client := reflect.ValueOf(db).Elem().FieldByName(p.entity)
	create := client.MethodByName("Create")
	builders := reflect.MakeSlice(reflect.SliceOf(create.Type().Out(0)), 0, len(rows))
	for _, m := range rows {
		b := create.Call(nil)[0]
		for _, s := range p.fields {
			v, ok, err := s.value(m)
			if err != nil {
				return fmt.Errorf("%s: %w", s.f.Name(), err)
			}
			if ok {
				b.Method(s.method).Call([]reflect.Value{v})
			}
		}
		builders = reflect.Append(builders, b)
	}

	bulk := client.MethodByName("CreateBulk").CallSlice([]reflect.Value{builders})[0]
	out := bulk.MethodByName("Exec").Call([]reflect.Value{reflect.ValueOf(ctx)})
	if err, _ := out[0].Interface().(error); err != nil {
		return err
	}
	return nil
}

// goName is a field's name as ent spells it in Go (`gen.Pascal`, with no
// acronyms): `date_created` is `DateCreated`, `tenant_id` is `TenantId`.
func goName(v string) string {
	words := strings.FieldsFunc(v, func(r rune) bool { return r == '_' || r == '-' || unicode.IsSpace(r) })
	for i, w := range words {
		words[i] = strings.ToUpper(w[:1]) + w[1:]
	}
	return strings.Join(words, "")
}
