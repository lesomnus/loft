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
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"
	"google.golang.org/protobuf/types/known/anypb"
	"google.golang.org/protobuf/types/known/timestamppb"

	app "github.com/lesomnus/rove"
	"github.com/lesomnus/rove/internal/ent"
	"github.com/lesomnus/rove/internal/ent/aliashold"
	"github.com/lesomnus/rove/internal/ent/attachment"
	"github.com/lesomnus/rove/internal/ent/migrate"
	enttenant "github.com/lesomnus/rove/internal/ent/tenant"
	"github.com/lesomnus/rove/server/pd"
)

// ImportOptions is what an operator says of a restore beyond the export.
type ImportOptions struct {
	// Alias is the address to restore the workspace at instead of the
	// export's.
	Alias string

	// Reuse takes the address though another workspace's hold is on it, as
	// `rove tenant alias --reuse` does. A hold put on it when this workspace
	// left is taken back without being asked: it was held for this one.
	Reuse bool

	// Dry does all of it and keeps none of it: what a restore would put back.
	Dry bool
}

// Restored is what a restore put back, or would have.
type Restored struct {
	Tenant pdid.Id

	// Alias is the address the workspace is at, and Asked the export's when
	// that is another: another workspace has it here, or it is not one this
	// deployment gives.
	Alias, Asked string

	// Released is the holds on the address that were taken back, by why
	// they were put.
	Released []string

	// Reopened says it was closed when it was exported, and is open: a
	// restore is to have it back, and one closed past its window would be
	// destroyed again by the next sweep.
	Reopened bool

	// Retired says the workspace retired from this deployment (its address
	// is held `retired`), and Returned how many records of that the trail
	// lifted taking it back before anything of it was written
	// (`trail.Policy.Return`, payday#141): none on a dry run, which takes
	// nothing back, and none once its floors had all passed and the trail
	// had purged it.
	Retired  bool
	Returned int

	Rows map[string]int

	// Left is the rows of the export's that were not put back, by a table this
	// build no longer has ([Dropped]): an export from before the table went.
	Left map[string]int

	// Files is the stored files put back, and Missing the ones its rows name
	// that the export did not have -- missing from the store it came from
	// when it was made, as its manifest says.
	Files   int
	Missing []string

	// Unlinked is the members, by alias, whose accounts this deployment's
	// roster does not have: an export from another deployment, or from this
	// one after its roster forgot them. They are restored naming no account,
	// which is what `rove identity migrate` gives one to.
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

// Import restores a workspace from its export into a deployment that does not
// have it: every row as it was exported, under the identifier it had, so that
// what pointed at it -- a printed label, a link -- still does (design D34). It
// is the one the workspace was purged from, or another.
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
// trail and its key once the last floor has passed. So a workspace whose
// address this deployment holds as `retired` is taken back by the trail first
// (`trail.Policy.Return`, payday#141), before anything of it is written --
// after which its windows are its contract's again, and what the retirement
// kept is its own. A dry run takes nothing back, and says it would
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
	// and a restore that left them out would say it was all of it -- but for
	// a table this build dropped, whose rows it leaves behind and says so.
	for name, n := range m.Rows {
		if n == 0 || slices.ContainsFunc(plans, func(p *plan) bool { return p.table == name }) {
			continue
		}
		if _, ok := Dropped[name]; !ok {
			return out, fmt.Errorf("%w: it has %d rows of %s, which this build does not have", ErrFormat, n, name)
		}
		if out.Left == nil {
			out.Left = map[string]int{}
		}
		out.Left[name] = n
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
	if out.Retired, err = d.Ent.AliasHold.Query().Where(aliashold.Tenant(tid), aliashold.Reason(ReasonRetired)).Exist(ctx); err != nil {
		return out, err
	}
	if out.Retired && !o.Dry {
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

	var releasedHolds []pdid.Id
	out.Alias, out.Released, releasedHolds, err = d.address(ctx, db, out.Tenant, t.GetAlias(), o)
	if err != nil {
		return out, err
	}
	if out.Alias != t.GetAlias() {
		out.Asked = t.GetAlias()
		t.SetAlias(out.Alias)
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

	// Every row put back, for the trail to say so -- and the holds on its
	// address the restore took back, erased past the servers here.
	restored := append([]pdid.Id{out.Tenant}, releasedHolds...)
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
	left := map[string]uint32{}
	for k, n := range out.Left {
		left[k] = uint32(n)
	}
	value, err := anypb.New(app.WorkspaceImported_builder{
		ExportedAt: timestamppb.New(m.ExportedAt),
		Manifest:   out.Manifest,
		SignedBy:   out.Key,
		Rows:       uint32(out.Total()),
		Files:      uint32(m.Files),
		Asked:      out.Asked,
		Released:   out.Released,
		Unlinked:   uint32(len(out.Unlinked)),
		Reopened:   out.Reopened,
		Left:       left,
	}.Build())
	if err != nil {
		return out, fmt.Errorf("the restore's event: %w", err)
	}
	event := pdid.New(pd.EventDomain)
	if err := db.Event.Create().
		SetId(event.Uuid()).
		SetTenantId(tid).
		SetKind("workspace.import").
		SetValue(value).
		SetDesc("").
		SetReason("").
		SetPayload("").
		SetOccurredAt(now).
		SetDateCreated(now).
		SetDateUpdated(now).
		Exec(ctx); err != nil {
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

// address is where a restored workspace is: the export's address when it may
// be had here, and the one asked for or one made up when it may not -- and the
// holds on it that were taken back, by why each was placed and by identifier.
func (d Deployment) address(ctx context.Context, db *ent.Client, tenant pdid.Id, exported string, o ImportOptions) (string, []string, []pdid.Id, error) {
	want := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(o.Alias), "@"))
	asked := want != ""
	if !asked {
		want = exported
	}

	ok := true
	if d.Aliases != nil {
		if err := d.Aliases.Valid(want); err != nil {
			if asked {
				return "", nil, nil, err
			}
			// Not one this deployment gives: reserved here, say.
			ok = false
		}
	}
	if ok {
		taken, err := db.Tenant.Query().Where(enttenant.Alias(want)).Exist(ctx)
		if err != nil {
			return "", nil, nil, err
		}
		if taken {
			if asked {
				return "", nil, nil, fmt.Errorf("@%s is another workspace's", want)
			}
			ok = false
		}
	}
	if !ok {
		if d.Aliases == nil {
			return "", nil, nil, fmt.Errorf("@%s is another workspace's here; restore it at another (--alias)", want)
		}
		v, err := d.Aliases.Make(ctx)
		return v, nil, nil, err
	}

	now := d.now()
	holds, err := db.AliasHold.Query().
		Where(aliashold.Held(want), aliashold.DateUntilGT(now), aliashold.DateErasedIsNil()).
		All(ctx)
	if err != nil {
		return "", nil, nil, err
	}
	for _, h := range holds {
		if pdid.Id(h.Tenant) != tenant && !o.Reuse {
			return "", nil, nil, fmt.Errorf("@%s is held until %s (%s); --reuse takes it anyway, and --alias restores it at another", want, h.DateUntil.Format(time.DateOnly), h.Reason)
		}
	}
	released := []string{}
	ids := []pdid.Id{}
	for _, h := range holds {
		if err := db.AliasHold.UpdateOneId(h.Id).SetDateErased(now).SetDateUpdated(now).Exec(ctx); err != nil {
			return "", nil, nil, err
		}
		released = append(released, h.Reason)
		ids = append(ids, pdid.Id(h.Id))
	}
	return want, released, ids, nil
}

// restore writes back every row of one table the export has, as it was.
// Imported is the act a restore is on the trail as.
const Imported = "rove.workspace.import"

func (d Deployment) restore(ctx context.Context, db *ent.Client, z *zip.Reader, p *plan, tenant uuid.UUID, out *Restored, restored *[]pdid.Id) (int, error) {
	f, err := z.Open("rows/" + p.table + ".jsonl")
	if errors.Is(err, fs.ErrNotExist) {
		// A table the deployment that wrote it did not have yet, which had
		// no rows there.
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
			return n, fmt.Errorf("%w: rows/%s.jsonl, line %d: %v", ErrFormat, p.table, n, err)
		}
		if k, err := keyOf(v.Get(p.tenant).Message().Get(p.key).Bytes()); err != nil || k != tenant {
			return n, fmt.Errorf("%w: rows/%s.jsonl, line %d: a row of another tenant", ErrFormat, p.table, n)
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
		return n, fmt.Errorf("rows/%s.jsonl: %w", p.table, err)
	}
	return n, flush()
}

// adjusts is what a restore makes of a row of a table, beside writing it as
// it was.
var adjusts = map[string]func(ctx context.Context, d Deployment, v protoreflect.Message, out *Restored) error{
	// A member whose account is not this roster's names none: an account by
	// an identifier this directory does not have is nobody here, and the
	// member is somebody to give one to (`rove identity migrate`).
	"holder": func(ctx context.Context, d Deployment, v protoreflect.Message, out *Restored) error {
		h := v.Interface().(*app.Holder)
		if len(h.GetAccount()) == 0 || d.Known == nil {
			return nil
		}
		a, err := keyOf(h.GetAccount())
		if err != nil {
			return fmt.Errorf("%w: its account: %v", ErrFormat, err)
		}
		known, err := d.Known(ctx, pdid.Id(a))
		if err != nil {
			return fmt.Errorf("roster: %w", err)
		}
		if !known {
			h.SetAccount(nil)
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
		if d.ParentFile().Package() == "rove" && d.Parent() == d.ParentFile() && strings.ToLower(string(d.Name())) == table {
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
