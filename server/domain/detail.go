package domain

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"
	"uuid"

	"github.com/lesomnus/payday/access"
	"github.com/lesomnus/payday/past"
	"github.com/lesomnus/payday/pdid"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/lesomnus/loft/internal/ent"
	"github.com/lesomnus/loft/internal/ent/attachment"
	"github.com/lesomnus/loft/internal/ent/inventorycount"
	"github.com/lesomnus/loft/internal/ent/purchase"
	"github.com/lesomnus/loft/internal/ent/reservation"
	"github.com/lesomnus/loft/internal/ent/stock"
	"github.com/lesomnus/loft/internal/ent/workorder"
	"github.com/lesomnus/loft/server/pd"
)

// details is what the events of one answer held, as its entries' `detail`
// says it (`TimelineEntry.detail`): each field of an event's message its kind
// wrote, by the name its domain's table gives it (`loft/past.proto`), in the
// words a person reads -- what a row from before kept in its `payload`, for
// an event since, which keeps a message instead (payday#151).
//
// A field that names a row says what the row is called now, read by the
// answer's namer: a person as they are called, on the access record of the
// call that shows it, as a sentence names them ([who]); an asset by its tag
// and name, a type and a model by theirs. Any other row with a name of its
// own -- a reservation, a work order, a count -- is called by it, read off a
// row of the caller's tenant as the namer reads one; an event of the answer
// by what it says, as the operation an event was part of is
// (`TimelineEntry.cause`); and a row nothing names -- gone, or of a kind with
// no name, a hand-over -- is said as its identifier. A message holds people
// by identifier alone (design 8.2), so nothing of theirs but the name they
// are called by is ever read into one: not an address, not a number.
type details struct {
	n *namer

	// events is the answer's events, by identifier, and say what it says of
	// one: what an event that names another -- a reason taken out of it,
	// `event.redact` -- calls it.
	events map[uuid.UUID]past.EventRow
	say    func(past.EventRow) string

	// held is each event's message, read once.
	held map[pdid.Id]proto.Message

	// titles is what each row with a name of its own is called: read a kind
	// at a time for the events of the answer ([details.read]) -- a room's
	// history names a reservation for every booking of it -- and a row at a
	// time for a kind [titled] does not know.
	titles map[uuid.UUID]called
}

// titled is how the rows a message can name that have a name of their own,
// and that the namer does not read, are read a page at a time: their names,
// by identifier, of the tenant's rows. One that is not here is read as
// payday's past names one (`pd.PastNames`), a row at a time.
var titled = map[pdid.Domain]func(ctx context.Context, db *ent.Client, tenant uuid.UUID, ids []uuid.UUID) (map[uuid.UUID]string, error){
	pd.ReservationDomain: func(ctx context.Context, db *ent.Client, tenant uuid.UUID, ids []uuid.UUID) (map[uuid.UUID]string, error) {
		vs, err := db.Reservation.Query().Where(reservation.IdIn(ids...), reservation.TenantId(tenant)).All(ctx)
		return namesOf(vs, err, func(v *ent.Reservation) (uuid.UUID, string) { return v.Id, v.Name })
	},
	pd.WorkOrderDomain: func(ctx context.Context, db *ent.Client, tenant uuid.UUID, ids []uuid.UUID) (map[uuid.UUID]string, error) {
		vs, err := db.WorkOrder.Query().Where(workorder.IdIn(ids...), workorder.TenantId(tenant)).All(ctx)
		return namesOf(vs, err, func(v *ent.WorkOrder) (uuid.UUID, string) { return v.Id, v.Name })
	},
	pd.InventoryCountDomain: func(ctx context.Context, db *ent.Client, tenant uuid.UUID, ids []uuid.UUID) (map[uuid.UUID]string, error) {
		vs, err := db.InventoryCount.Query().Where(inventorycount.IdIn(ids...), inventorycount.TenantId(tenant)).All(ctx)
		return namesOf(vs, err, func(v *ent.InventoryCount) (uuid.UUID, string) { return v.Id, v.Name })
	},
	pd.AttachmentDomain: func(ctx context.Context, db *ent.Client, tenant uuid.UUID, ids []uuid.UUID) (map[uuid.UUID]string, error) {
		vs, err := db.Attachment.Query().Where(attachment.IdIn(ids...), attachment.TenantId(tenant)).All(ctx)
		return namesOf(vs, err, func(v *ent.Attachment) (uuid.UUID, string) { return v.Id, v.Name })
	},
	pd.StockDomain: func(ctx context.Context, db *ent.Client, tenant uuid.UUID, ids []uuid.UUID) (map[uuid.UUID]string, error) {
		vs, err := db.Stock.Query().Where(stock.IdIn(ids...), stock.TenantId(tenant)).All(ctx)
		return namesOf(vs, err, func(v *ent.Stock) (uuid.UUID, string) { return v.Id, v.Name })
	},
	pd.PurchaseDomain: func(ctx context.Context, db *ent.Client, tenant uuid.UUID, ids []uuid.UUID) (map[uuid.UUID]string, error) {
		vs, err := db.Purchase.Query().Where(purchase.IdIn(ids...), purchase.TenantId(tenant)).All(ctx)
		return namesOf(vs, err, func(v *ent.Purchase) (uuid.UUID, string) { return v.Id, v.Name })
	},
}

func namesOf[V any](vs []V, err error, of func(V) (uuid.UUID, string)) (map[uuid.UUID]string, error) {
	if err != nil {
		return nil, err
	}
	out := make(map[uuid.UUID]string, len(vs))
	for _, v := range vs {
		id, name := of(v)
		out[id] = name
	}
	return out, nil
}

// read reads what each of `evs` held, and what the rows they name with a name
// of their own are called, a kind at a time.
func (d *details) read(evs []past.EventRow) error {
	if d.held == nil {
		d.held = map[pdid.Id]proto.Message{}
	}
	if d.titles == nil {
		d.titles = map[uuid.UUID]called{}
	}

	want := map[pdid.Domain][]uuid.UUID{}
	for _, ev := range evs {
		m := heldBy(ev)
		if m == nil {
			continue
		}
		d.held[ev.Id] = m
		ids(m.ProtoReflect(), func(id pdid.Id) {
			if _, ok := titled[id.Domain()]; !ok {
				return
			}
			if _, ok := d.titles[id.Uuid()]; ok {
				return
			}
			d.titles[id.Uuid()] = called{}
			want[id.Domain()] = append(want[id.Domain()], id.Uuid())
		})
	}

	for dom, us := range want {
		for page := range slices.Chunk(us, 500) {
			vs, err := titled[dom](d.n.t.ctx, d.n.t.db, d.n.t.tenant.Uuid(), page)
			if err != nil {
				return err
			}
			for id, name := range vs {
				d.titles[id] = called{found: true, name: name}
			}
		}
	}
	return nil
}

// ids calls `f` with each identifier the message `m` holds.
func ids(m protoreflect.Message, f func(pdid.Id)) {
	m.Range(func(fd protoreflect.FieldDescriptor, v protoreflect.Value) bool {
		if fd.Kind() != protoreflect.BytesKind || fd.IsMap() {
			return true
		}
		each := []protoreflect.Value{v}
		if fd.IsList() {
			each = each[:0]
			for i := range v.List().Len() {
				each = append(each, v.List().Get(i))
			}
		}
		for _, v := range each {
			if id, err := pdid.From(v.Bytes()); err == nil && !id.IsZero() {
				f(id)
			}
		}
		return true
	})
}

// of is what `ev` held, field by field; nothing for a row from before, which
// keeps no message, and for a value this build cannot read -- the sentence
// says what it can of that one.
func (d *details) of(ev past.EventRow) (map[string]string, error) {
	m, ok := d.held[ev.Id]
	if !ok {
		m = heldBy(ev)
	}
	if m == nil {
		return nil, nil
	}

	out := map[string]string{}
	var err error
	m.ProtoReflect().Range(func(fd protoreflect.FieldDescriptor, v protoreflect.Value) bool {
		var s string
		if s, err = d.value(fd, v); err != nil {
			return false
		}
		if s != "" {
			out[string(fd.Name())] = s
		}
		return true
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// heldBy is the message `ev` holds, read as its kind declares it -- the
// message every kind of a shared one is read into (payday's past guide, §5)
// -- or, for a kind this build declares nothing for, as the message the row
// says it holds; nil when neither reads.
func heldBy(ev past.EventRow) proto.Message {
	if ev.Value == nil {
		return nil
	}
	if typ, ok := pd.EventKinds[ev.Kind]; ok {
		m := typ.New().Interface()
		if err := ev.Value.UnmarshalTo(m); err == nil {
			return m
		}
		return nil
	}
	m, err := ev.Value.UnmarshalNew()
	if err != nil {
		return nil
	}
	return m
}

// value is one field as a detail says it: a list's values and a map's
// entries -- `k=v`, in the order of their keys -- each said as one is, and
// joined.
func (d *details) value(fd protoreflect.FieldDescriptor, v protoreflect.Value) (string, error) {
	switch {
	case fd.IsMap():
		vs := []string{}
		var err error
		v.Map().Range(func(k protoreflect.MapKey, mv protoreflect.Value) bool {
			var s string
			if s, err = d.one(fd.MapValue(), mv); err != nil {
				return false
			}
			vs = append(vs, k.String()+"="+s)
			return true
		})
		if err != nil {
			return "", err
		}
		slices.Sort(vs)
		return strings.Join(vs, ", "), nil

	case fd.IsList():
		l := v.List()
		vs := make([]string, 0, l.Len())
		for i := range l.Len() {
			s, err := d.one(fd, l.Get(i))
			if err != nil {
				return "", err
			}
			vs = append(vs, s)
		}
		return strings.Join(vs, ", "), nil
	}

	return d.one(fd, v)
}

// one is a single value: a row an identifier names, as it is called now
// ([details.called]); a word as the message holds it; a number in decimal; a
// moment in the workspace's zone, as RFC 3339 writes one -- the wall the page
// draws on, with its offset, which an assistant reads back as the instant it
// is.
func (d *details) one(fd protoreflect.FieldDescriptor, v protoreflect.Value) (string, error) {
	switch fd.Kind() {
	case protoreflect.BytesKind:
		return d.called(v.Bytes())
	case protoreflect.StringKind:
		return v.String(), nil
	case protoreflect.EnumKind:
		if e := fd.Enum().Values().ByNumber(v.Enum()); e != nil {
			return string(e.Name()), nil
		}
		return fmt.Sprint(int32(v.Enum())), nil
	case protoreflect.MessageKind, protoreflect.GroupKind:
		if ts, ok := v.Message().Interface().(*timestamppb.Timestamp); ok {
			return ts.AsTime().In(d.n.t.zone()).Format(time.RFC3339), nil
		}
		b, err := protojson.Marshal(v.Message().Interface())
		if err != nil {
			return "", err
		}
		return string(b), nil
	}

	return fmt.Sprint(v.Interface()), nil
}

// called is the row the identifier `b` names, as a detail says it: what it is
// called now, each person whose data that is on the access record of the call
// that shows it, or the identifier itself where nothing names it.
func (d *details) called(b []byte) (string, error) {
	id, err := pdid.From(b)
	if err != nil {
		// Not an identifier, which no message holds: as the bytes it is.
		return fmt.Sprintf("%x", b), nil
	}
	if id.IsZero() {
		return "", nil
	}

	if ev, ok := d.events[id.Uuid()]; ok && d.say != nil && id.Domain() == pd.EventDomain {
		if v := d.say(ev); v != "" {
			return v, nil
		}
	}

	ctx := d.n.t.ctx
	name, saw, err := d.n.Name(ctx, id)
	if err != nil {
		return "", err
	}
	if name == "" {
		v := d.n.look(ctx, &d.titles, id.Uuid(), d.title)
		if v.err != nil {
			return "", v.err
		}
		name, saw = v.name, v.saw
	}
	if name == "" {
		return id.String(), nil
	}
	access.Saw(ctx, saw...)
	return name, nil
}

// title is what a row the namer does not read, of a kind [titled] does not
// know, is called: its name, else its alias, through the wall, as payday's
// past names one. Nothing for the rows the namer reads, which said what they
// are called already, and for a kind [titled] knows, read before.
func (d *details) title(ctx context.Context, u uuid.UUID) called {
	id := pdid.Id(u)
	switch id.Domain() {
	case pd.PartyDomain, pd.HolderDomain, pd.AssetDomain, pd.AssetTypeDomain, pd.ItemModelDomain:
		return called{}
	}
	if fetch, ok := titled[id.Domain()]; ok {
		vs, err := fetch(ctx, d.n.t.db, d.n.t.tenant.Uuid(), []uuid.UUID{u})
		if err != nil {
			return called{err: err}
		}
		name, ok := vs[u]
		return called{found: ok, name: name}
	}
	name, saw, err := pd.PastNames(d.n.t.drv).Name(ctx, id)
	if err != nil {
		return called{err: err}
	}
	return called{found: name != "", name: name, saw: saw}
}
