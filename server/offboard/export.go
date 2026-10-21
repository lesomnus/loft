package offboard

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	stdsql "database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"hash"
	"io"
	"maps"
	"reflect"
	"slices"
	"time"
	"uuid"

	"github.com/lesomnus/payday/pdid"
	"github.com/protobuf-orm/ent/dialect"
	"github.com/protobuf-orm/ent/dialect/sql"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"

	"github.com/lesomnus/rove/internal/ent"
	entaccess "github.com/lesomnus/rove/internal/ent/access"
	"github.com/lesomnus/rove/internal/ent/allocation"
	"github.com/lesomnus/rove/internal/ent/asset"
	"github.com/lesomnus/rove/internal/ent/assettype"
	"github.com/lesomnus/rove/internal/ent/attachment"
	entaudit "github.com/lesomnus/rove/internal/ent/audit"
	"github.com/lesomnus/rove/internal/ent/bookable"
	"github.com/lesomnus/rove/internal/ent/countfinding"
	"github.com/lesomnus/rove/internal/ent/custody"
	"github.com/lesomnus/rove/internal/ent/custodyline"
	"github.com/lesomnus/rove/internal/ent/event"
	"github.com/lesomnus/rove/internal/ent/fact"
	"github.com/lesomnus/rove/internal/ent/holder"
	"github.com/lesomnus/rove/internal/ent/inventorycount"
	"github.com/lesomnus/rove/internal/ent/invitation"
	"github.com/lesomnus/rove/internal/ent/itemmodel"
	"github.com/lesomnus/rove/internal/ent/label"
	"github.com/lesomnus/rove/internal/ent/link"
	"github.com/lesomnus/rove/internal/ent/notification"
	"github.com/lesomnus/rove/internal/ent/party"
	"github.com/lesomnus/rove/internal/ent/placement"
	"github.com/lesomnus/rove/internal/ent/predicate"
	"github.com/lesomnus/rove/internal/ent/purchase"
	"github.com/lesomnus/rove/internal/ent/purchaseline"
	"github.com/lesomnus/rove/internal/ent/reservation"
	"github.com/lesomnus/rove/internal/ent/reservationitem"
	"github.com/lesomnus/rove/internal/ent/stewardship"
	"github.com/lesomnus/rove/internal/ent/stock"
	"github.com/lesomnus/rove/internal/ent/stockmovement"
	enttenant "github.com/lesomnus/rove/internal/ent/tenant"
	"github.com/lesomnus/rove/internal/ent/tenantcontract"
	"github.com/lesomnus/rove/internal/ent/tenantdomain"
	"github.com/lesomnus/rove/internal/ent/usagesnapshot"
	"github.com/lesomnus/rove/internal/ent/workorder"
	"github.com/lesomnus/rove/server/pd"
)

// Manifest is what an export says about itself, as its `manifest.json`.
type Manifest struct {
	// Format is what the export is, which a restore reads by ([Format]).
	Format string `json:"format"`

	// Tenant is the tenant's row, as the API answers it.
	Tenant json.RawMessage `json:"tenant"`

	ExportedAt time.Time `json:"exported_at"`

	// Rows is how many rows of each kind, each in `rows/<kind>.jsonl`.
	Rows map[string]int `json:"rows"`

	// Files is how many stored files, under `files/` by their key, and
	// Missing the ones an attachment named that were not there to copy.
	Files   int      `json:"files"`
	Missing []string `json:"missing,omitempty"`

	// Trail is how many rows of the trail as the tenant may read it: in the
	// database, `trail/database.jsonl`, and in the archive,
	// `trail/archive.jsonl`. Access is the access record's rows in the
	// database, `trail/access.jsonl` (rove#136); what of it is archived is
	// in the archive's file with the rest.
	Trail struct {
		Database int `json:"database"`
		Archive  int `json:"archive"`
		Access   int `json:"access"`
	} `json:"trail"`

	// Withheld is what is left out, and why.
	Withheld map[string]string `json:"withheld"`

	// Digests is the SHA-256 of every other file in the export, by its name,
	// hex: what the signature beside the manifest vouches for ([Verify]).
	Digests map[string]string `json:"digests"`
}

// page is how many rows an export reads at once, of a table or of the trail,
// which is what it holds of them: a workspace of any size is written in as
// little.
var page = 1000

// reader is a page of the rows of one kind a tenant has, as the API answers
// them -- at most n, from the first after `last`, which is nil for the first
// page -- and the mark of the last of them.
type reader func(ctx context.Context, db *ent.Client, t uuid.UUID, last *mark, n int) ([]proto.Message, *mark, error)

// mark is where a page of rows ended: when the last of them was made, and its
// key. Rows are read in that order, which is the order a list reads them in
// and the one their page index holds them in.
//
// Both are the row's own as ent read them, and not as the API answers them,
// so that they go back to the database as they came out of it: SQLite
// compares times as it keeps them, as text.
type mark struct {
	at time.Time
	id uuid.UUID
}

// after narrows a table to the rows after `last`, and to all of them for nil.
// The bound on the time alone is what lets an index begin at `last` rather
// than read from its first row every page.
func after(last *mark) func(*sql.Selector) {
	return func(s *sql.Selector) {
		if last == nil {
			return
		}
		at, id := s.C("date_created"), s.C("id")
		s.Where(sql.And(sql.GTE(at, last.at), sql.Or(sql.GT(at, last.at), sql.GT(id, last.id))))
	}
}

// markOf is the mark of one of ent's rows, which every one of the rows an
// export reads has, by these names.
func markOf(v any) (*mark, error) {
	r := reflect.Indirect(reflect.ValueOf(v))
	at, id := r.FieldByName("DateCreated"), r.FieldByName("Id")
	if !at.IsValid() || !id.IsValid() {
		return nil, fmt.Errorf("%s has no time it was made, or no key", r.Type())
	}
	m := &mark{}
	var ok bool
	if m.at, ok = at.Interface().(time.Time); !ok {
		return nil, fmt.Errorf("%s: the time it was made is a %s", r.Type(), at.Type())
	}
	if m.id, ok = id.Interface().(uuid.UUID); !ok {
		return nil, fmt.Errorf("%s: its key is a %s", r.Type(), id.Type())
	}
	return m, nil
}

// each is a page of ent's rows as the API answers them, and its mark.
func each[T any, M proto.Message](f func(T) M) func([]T, error) ([]proto.Message, *mark, error) {
	return func(vs []T, err error) ([]proto.Message, *mark, error) {
		if err != nil || len(vs) == 0 {
			return nil, nil, err
		}
		out := make([]proto.Message, 0, len(vs))
		for _, v := range vs {
			out = append(out, f(v))
		}
		m, err := markOf(vs[len(vs)-1])
		return out, m, err
	}
}

// Exported is every kind an export writes, by its table.
var Exported = map[string]reader{
	"allocation": func(ctx context.Context, db *ent.Client, t uuid.UUID, last *mark, n int) ([]proto.Message, *mark, error) {
		return each((*ent.Allocation).Proto)(db.Allocation.Query().Where(allocation.TenantId(t), after(last)).Order(ent.Asc(allocation.FieldDateCreated, allocation.FieldId)).Limit(n).All(ctx))
	},
	"asset": func(ctx context.Context, db *ent.Client, t uuid.UUID, last *mark, n int) ([]proto.Message, *mark, error) {
		return each((*ent.Asset).Proto)(db.Asset.Query().Where(asset.TenantId(t), after(last)).Order(ent.Asc(asset.FieldDateCreated, asset.FieldId)).Limit(n).All(ctx))
	},
	"assettype": func(ctx context.Context, db *ent.Client, t uuid.UUID, last *mark, n int) ([]proto.Message, *mark, error) {
		return each((*ent.AssetType).Proto)(db.AssetType.Query().Where(assettype.TenantId(t), after(last)).Order(ent.Asc(assettype.FieldDateCreated, assettype.FieldId)).Limit(n).All(ctx))
	},
	"attachment": func(ctx context.Context, db *ent.Client, t uuid.UUID, last *mark, n int) ([]proto.Message, *mark, error) {
		return each((*ent.Attachment).Proto)(db.Attachment.Query().Where(attachment.TenantId(t), after(last)).Order(ent.Asc(attachment.FieldDateCreated, attachment.FieldId)).Limit(n).All(ctx))
	},
	"bookable": func(ctx context.Context, db *ent.Client, t uuid.UUID, last *mark, n int) ([]proto.Message, *mark, error) {
		return each((*ent.Bookable).Proto)(db.Bookable.Query().Where(bookable.TenantId(t), after(last)).Order(ent.Asc(bookable.FieldDateCreated, bookable.FieldId)).Limit(n).All(ctx))
	},
	"countfinding": func(ctx context.Context, db *ent.Client, t uuid.UUID, last *mark, n int) ([]proto.Message, *mark, error) {
		return each((*ent.CountFinding).Proto)(db.CountFinding.Query().Where(countfinding.TenantId(t), after(last)).Order(ent.Asc(countfinding.FieldDateCreated, countfinding.FieldId)).Limit(n).All(ctx))
	},
	"custody": func(ctx context.Context, db *ent.Client, t uuid.UUID, last *mark, n int) ([]proto.Message, *mark, error) {
		return each((*ent.Custody).Proto)(db.Custody.Query().Where(custody.TenantId(t), after(last)).Order(ent.Asc(custody.FieldDateCreated, custody.FieldId)).Limit(n).All(ctx))
	},
	"custodyline": func(ctx context.Context, db *ent.Client, t uuid.UUID, last *mark, n int) ([]proto.Message, *mark, error) {
		return each((*ent.CustodyLine).Proto)(db.CustodyLine.Query().Where(custodyline.TenantId(t), after(last)).Order(ent.Asc(custodyline.FieldDateCreated, custodyline.FieldId)).Limit(n).All(ctx))
	},
	"event": func(ctx context.Context, db *ent.Client, t uuid.UUID, last *mark, n int) ([]proto.Message, *mark, error) {
		return each((*ent.Event).Proto)(db.Event.Query().Where(event.TenantId(t), after(last)).Order(ent.Asc(event.FieldDateCreated, event.FieldId)).Limit(n).All(ctx))
	},
	"fact": func(ctx context.Context, db *ent.Client, t uuid.UUID, last *mark, n int) ([]proto.Message, *mark, error) {
		return each((*ent.Fact).Proto)(db.Fact.Query().Where(fact.TenantId(t), after(last)).Order(ent.Asc(fact.FieldDateCreated, fact.FieldId)).Limit(n).All(ctx))
	},
	"holder": func(ctx context.Context, db *ent.Client, t uuid.UUID, last *mark, n int) ([]proto.Message, *mark, error) {
		return each((*ent.Holder).Proto)(db.Holder.Query().Where(holder.TenantId(t), after(last)).Order(ent.Asc(holder.FieldDateCreated, holder.FieldId)).Limit(n).All(ctx))
	},
	// Who was invited, as what, and what became of it -- without the code a
	// mail carried, which is a way in and nothing the tenant needs back.
	"invitation": func(ctx context.Context, db *ent.Client, t uuid.UUID, last *mark, n int) ([]proto.Message, *mark, error) {
		return each(func(v *ent.Invitation) proto.Message {
			m := v.Proto()
			m.SetClaim(nil)
			return m
		})(db.Invitation.Query().Where(invitation.TenantId(t), after(last)).Order(ent.Asc(invitation.FieldDateCreated, invitation.FieldId)).Limit(n).All(ctx))
	},
	"inventorycount": func(ctx context.Context, db *ent.Client, t uuid.UUID, last *mark, n int) ([]proto.Message, *mark, error) {
		return each((*ent.InventoryCount).Proto)(db.InventoryCount.Query().Where(inventorycount.TenantId(t), after(last)).Order(ent.Asc(inventorycount.FieldDateCreated, inventorycount.FieldId)).Limit(n).All(ctx))
	},
	"itemmodel": func(ctx context.Context, db *ent.Client, t uuid.UUID, last *mark, n int) ([]proto.Message, *mark, error) {
		return each((*ent.ItemModel).Proto)(db.ItemModel.Query().Where(itemmodel.TenantId(t), after(last)).Order(ent.Asc(itemmodel.FieldDateCreated, itemmodel.FieldId)).Limit(n).All(ctx))
	},
	"label": func(ctx context.Context, db *ent.Client, t uuid.UUID, last *mark, n int) ([]proto.Message, *mark, error) {
		return each((*ent.Label).Proto)(db.Label.Query().Where(label.TenantId(t), after(last)).Order(ent.Asc(label.FieldDateCreated, label.FieldId)).Limit(n).All(ctx))
	},
	"link": func(ctx context.Context, db *ent.Client, t uuid.UUID, last *mark, n int) ([]proto.Message, *mark, error) {
		return each((*ent.Link).Proto)(db.Link.Query().Where(link.TenantId(t), after(last)).Order(ent.Asc(link.FieldDateCreated, link.FieldId)).Limit(n).All(ctx))
	},
	"notification": func(ctx context.Context, db *ent.Client, t uuid.UUID, last *mark, n int) ([]proto.Message, *mark, error) {
		return each((*ent.Notification).Proto)(db.Notification.Query().Where(notification.TenantId(t), after(last)).Order(ent.Asc(notification.FieldDateCreated, notification.FieldId)).Limit(n).All(ctx))
	},
	"party": func(ctx context.Context, db *ent.Client, t uuid.UUID, last *mark, n int) ([]proto.Message, *mark, error) {
		return each((*ent.Party).Proto)(db.Party.Query().Where(party.TenantId(t), after(last)).Order(ent.Asc(party.FieldDateCreated, party.FieldId)).Limit(n).All(ctx))
	},
	"placement": func(ctx context.Context, db *ent.Client, t uuid.UUID, last *mark, n int) ([]proto.Message, *mark, error) {
		return each((*ent.Placement).Proto)(db.Placement.Query().Where(placement.TenantId(t), after(last)).Order(ent.Asc(placement.FieldDateCreated, placement.FieldId)).Limit(n).All(ctx))
	},
	"purchase": func(ctx context.Context, db *ent.Client, t uuid.UUID, last *mark, n int) ([]proto.Message, *mark, error) {
		return each((*ent.Purchase).Proto)(db.Purchase.Query().Where(purchase.TenantId(t), after(last)).Order(ent.Asc(purchase.FieldDateCreated, purchase.FieldId)).Limit(n).All(ctx))
	},
	"purchaseline": func(ctx context.Context, db *ent.Client, t uuid.UUID, last *mark, n int) ([]proto.Message, *mark, error) {
		return each((*ent.PurchaseLine).Proto)(db.PurchaseLine.Query().Where(purchaseline.TenantId(t), after(last)).Order(ent.Asc(purchaseline.FieldDateCreated, purchaseline.FieldId)).Limit(n).All(ctx))
	},
	"reservation": func(ctx context.Context, db *ent.Client, t uuid.UUID, last *mark, n int) ([]proto.Message, *mark, error) {
		return each((*ent.Reservation).Proto)(db.Reservation.Query().Where(reservation.TenantId(t), after(last)).Order(ent.Asc(reservation.FieldDateCreated, reservation.FieldId)).Limit(n).All(ctx))
	},
	"reservationitem": func(ctx context.Context, db *ent.Client, t uuid.UUID, last *mark, n int) ([]proto.Message, *mark, error) {
		return each((*ent.ReservationItem).Proto)(db.ReservationItem.Query().Where(reservationitem.TenantId(t), after(last)).Order(ent.Asc(reservationitem.FieldDateCreated, reservationitem.FieldId)).Limit(n).All(ctx))
	},
	"stewardship": func(ctx context.Context, db *ent.Client, t uuid.UUID, last *mark, n int) ([]proto.Message, *mark, error) {
		return each((*ent.Stewardship).Proto)(db.Stewardship.Query().Where(stewardship.TenantId(t), after(last)).Order(ent.Asc(stewardship.FieldDateCreated, stewardship.FieldId)).Limit(n).All(ctx))
	},
	"stock": func(ctx context.Context, db *ent.Client, t uuid.UUID, last *mark, n int) ([]proto.Message, *mark, error) {
		return each((*ent.Stock).Proto)(db.Stock.Query().Where(stock.TenantId(t), after(last)).Order(ent.Asc(stock.FieldDateCreated, stock.FieldId)).Limit(n).All(ctx))
	},
	"stockmovement": func(ctx context.Context, db *ent.Client, t uuid.UUID, last *mark, n int) ([]proto.Message, *mark, error) {
		return each((*ent.StockMovement).Proto)(db.StockMovement.Query().Where(stockmovement.TenantId(t), after(last)).Order(ent.Asc(stockmovement.FieldDateCreated, stockmovement.FieldId)).Limit(n).All(ctx))
	},
	"tenantcontract": func(ctx context.Context, db *ent.Client, t uuid.UUID, last *mark, n int) ([]proto.Message, *mark, error) {
		return each((*ent.TenantContract).Proto)(db.TenantContract.Query().Where(tenantcontract.TenantId(t), after(last)).Order(ent.Asc(tenantcontract.FieldDateCreated, tenantcontract.FieldId)).Limit(n).All(ctx))
	},
	"tenantdomain": func(ctx context.Context, db *ent.Client, t uuid.UUID, last *mark, n int) ([]proto.Message, *mark, error) {
		return each((*ent.TenantDomain).Proto)(db.TenantDomain.Query().Where(tenantdomain.TenantId(t), after(last)).Order(ent.Asc(tenantdomain.FieldDateCreated, tenantdomain.FieldId)).Limit(n).All(ctx))
	},
	"usagesnapshot": func(ctx context.Context, db *ent.Client, t uuid.UUID, last *mark, n int) ([]proto.Message, *mark, error) {
		return each((*ent.UsageSnapshot).Proto)(db.UsageSnapshot.Query().Where(usagesnapshot.TenantId(t), after(last)).Order(ent.Asc(usagesnapshot.FieldDateCreated, usagesnapshot.FieldId)).Limit(n).All(ctx))
	},
	"workorder": func(ctx context.Context, db *ent.Client, t uuid.UUID, last *mark, n int) ([]proto.Message, *mark, error) {
		return each((*ent.WorkOrder).Proto)(db.WorkOrder.Query().Where(workorder.TenantId(t), after(last)).Order(ent.Asc(workorder.FieldDateCreated, workorder.FieldId)).Limit(n).All(ctx))
	},
}

// Withheld is every table an export leaves out, and why. A table in neither
// this nor [Exported] is one nobody decided about, which a test refuses.
var Withheld = map[string]string{
	"audit":         "the trail, which is written apart as the tenant may read it",
	"access":        "the access record, the trail's: written apart with it, as the tenant may read it",
	"archived":      "the deployment's account of the trail's archive, every tenant's chunks at once",
	"session":       "who is signed in",
	"clilogin":      "a terminal waiting to be signed in, which is nobody's yet",
	"aliashold":     "the deployment's: addresses nobody may take for a while",
	"consent":       "an account's, which agreed once for every workspace it is in",
	"signinfailure": "the deployment's: how often an address got a sign-in wrong lately",
	"searchstale":   "the deployment's queue of what the search index has yet to hear of",
	"outbox":        "the deployment's queue of what changed",
	"treelock":      "the deployment's bookkeeping",
	"legalhold":     "the operator's",
	"tenant":        "the manifest's",
}

// Export writes everything a tenant has as one zip archive: a JSON line per
// row of each kind, the trail as the tenant may read it -- the rows that name
// it, in the database and in the archive -- and the files its attachments
// kept. What it leaves out is in the manifest, with why.
//
// It reads, and changes nothing; a tenant under a legal hold is exported like
// any other. It reads one moment of the workspace, whatever is written to it
// meanwhile: read a table at a time outside one, an export could have a fact
// and not the asset it is of, which a restore refuses. And it reads a [page]
// at a time and writes it before the next, so that what it holds is not the
// workspace's size.
//
// The manifest lists every file's SHA-256 and is signed with the trail's key
// (`audit.checkpoints.key`), in `signature.json` beside it, so that a restore
// can tell the export is what this deployment wrote ([Verify], design D34). A
// deployment with no key writes an export without one, which is the tenant's
// data all the same and is not one a restore takes.
func (d Deployment) Export(ctx context.Context, tenant pdid.Id, w io.Writer) (Manifest, error) {
	out := Manifest{Format: Format, ExportedAt: d.now().UTC(), Rows: map[string]int{}, Withheld: Withheld, Digests: map[string]string{}}
	tid := tenant.Uuid()

	tx, err := d.Ent.BeginTx(ctx, snapshot(d.Ent.Dialect()))
	if err != nil {
		return out, err
	}
	// It writes nothing, so there is nothing to keep.
	defer tx.Rollback()
	db := tx.Client()

	t, err := db.Tenant.Query().Where(enttenant.Id(tid)).Only(ctx)
	if err != nil {
		return out, fmt.Errorf("tenant %s: %w", tenant, err)
	}
	out.Tenant, err = protojson.Marshal(t.Proto())
	if err != nil {
		return out, err
	}

	z := zip.NewWriter(w)
	// Every file goes through its digest on the way in.
	hashes := map[string]hash.Hash{}
	create := func(name string) (io.Writer, error) {
		f, err := z.Create(name)
		if err != nil {
			return nil, err
		}
		h := sha256.New()
		hashes[name] = h
		return io.MultiWriter(f, h), nil
	}
	line := func(f io.Writer, v proto.Message) error {
		b, err := protojson.Marshal(v)
		if err != nil {
			return err
		}
		_, err = f.Write(append(b, '\n'))
		return err
	}

	for _, k := range slices.Sorted(maps.Keys(Exported)) {
		f, err := create("rows/" + k + ".jsonl")
		if err != nil {
			return out, err
		}
		out.Rows[k] = 0
		var last *mark
		for {
			vs, m, err := Exported[k](ctx, db, tid, last, page)
			if err != nil {
				return out, fmt.Errorf("%s: %w", k, err)
			}
			for _, v := range vs {
				if err := line(f, v); err != nil {
					return out, err
				}
			}
			out.Rows[k] += len(vs)
			if len(vs) < page {
				break
			}
			last = m
		}
	}

	// The trail, by the wall's rule: the rows that name the tenant.
	tf, err := create("trail/database.jsonl")
	if err != nil {
		return out, err
	}
	if err := trailOf(ctx, db, tid, func(a *ent.Audit) error {
		out.Trail.Database++
		return line(tf, a.Proto())
	}); err != nil {
		return out, fmt.Errorf("trail: %w", err)
	}

	// And the access record, by the same rule (rove#136): who read or wrote
	// whose data in the workspace, and what its own people did in another.
	// The workspace is who answers for its people's data, and this is the
	// record of what was done with it.
	af, err := create("trail/access.jsonl")
	if err != nil {
		return out, err
	}
	if err := accessOf(ctx, db, tid, func(a *ent.Access) error {
		out.Trail.Access++
		return line(af, a.Proto())
	}); err != nil {
		return out, fmt.Errorf("access record: %w", err)
	}

	if d.Trail.Archive != nil {
		f, err := create("trail/archive.jsonl")
		if err != nil {
			return out, err
		}
		// Through its manifest: only what the deployment wrote there, and
		// only while its bytes are what was written. The manifest of the
		// moment the rows were read: a chunk archived since is rows the
		// database part has.
		if err := d.Trail.ReadTenant(ctx, pd.TrailStore(db), tenant, func(doc []byte) error {
			out.Trail.Archive++
			_, err := f.Write(append(doc, '\n'))
			return err
		}); err != nil {
			return out, fmt.Errorf("trail archive: %w", err)
		}
	}

	// The files the attachments kept.
	keys, err := db.Attachment.Query().Where(attachment.TenantId(tid)).Select(attachment.FieldObjectKey).Strings(ctx)
	if err != nil {
		return out, err
	}
	slices.Sort(keys)
	for _, k := range keys {
		if d.Files == nil {
			out.Missing = append(out.Missing, k)
			continue
		}
		r, err := d.Files.Open(ctx, k)
		if err != nil {
			out.Missing = append(out.Missing, k)
			continue
		}
		f, err := create("files/" + k)
		if err == nil {
			_, err = io.Copy(f, r)
		}
		r.Close()
		if err != nil {
			return out, fmt.Errorf("files/%s: %w", k, err)
		}
		out.Files++
	}

	for name, h := range hashes {
		out.Digests[name] = hex.EncodeToString(h.Sum(nil))
	}
	mb, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		return out, err
	}
	mb = append(mb, '\n')
	f, err := z.Create(manifestName)
	if err != nil {
		return out, err
	}
	if _, err := f.Write(mb); err != nil {
		return out, err
	}

	if d.Trail.Signer != nil {
		s, err := sign(d.Trail.Signer, statement(mb))
		if err != nil {
			return out, fmt.Errorf("sign the manifest: %w", err)
		}
		sb, err := json.MarshalIndent(s, "", "  ")
		if err != nil {
			return out, err
		}
		f, err := z.Create(signatureName)
		if err != nil {
			return out, err
		}
		if _, err := f.Write(append(sb, '\n')); err != nil {
			return out, err
		}
	}

	return out, z.Close()
}

// snapshot is a transaction that reads one moment of the database and writes
// nothing: PostgreSQL's repeatable read, and on SQLite any transaction, which
// reads the moment of its first read until it ends.
func snapshot(d string) *stdsql.TxOptions {
	if d == dialect.Postgres {
		return &stdsql.TxOptions{Isolation: stdsql.LevelRepeatableRead, ReadOnly: true}
	}
	return &stdsql.TxOptions{ReadOnly: true}
}

// trailOf hands every row of the trail that names the tenant to f once, in
// the order they were written. They are three kinds of row, each read a page
// at a time by an index of its own -- the tenant's, then its people's in
// another tenant, then the rows of what was done to it from another -- and
// told apart so that none is in two.
func trailOf(ctx context.Context, db *ent.Client, t uuid.UUID, f func(*ent.Audit) error) error {
	type kind struct {
		of   []predicate.Audit
		rows []*ent.Audit // read and not yet handed on
		last *mark
		done bool
	}
	ks := []*kind{
		{of: []predicate.Audit{entaudit.TenantId(t)}},
		{of: []predicate.Audit{entaudit.ActorTenantId(t), entaudit.TenantIdNEQ(t)}},
		{of: []predicate.Audit{entaudit.CounterpartTenantId(t), entaudit.TenantIdNEQ(t), entaudit.ActorTenantIdNEQ(t)}},
	}
	fill := func(k *kind) error {
		if len(k.rows) > 0 || k.done {
			return nil
		}
		vs, err := db.Audit.Query().
			Where(k.of...).
			Where(after(k.last)).
			Order(ent.Asc(entaudit.FieldDateCreated, entaudit.FieldId)).
			Limit(page).
			All(ctx)
		if err != nil {
			return err
		}
		k.rows, k.done = vs, len(vs) < page
		if len(vs) > 0 {
			v := vs[len(vs)-1]
			k.last = &mark{at: v.DateCreated, id: v.Id}
		}
		return nil
	}
	before := func(a, b *ent.Audit) bool {
		if !a.DateCreated.Equal(b.DateCreated) {
			return a.DateCreated.Before(b.DateCreated)
		}
		return bytes.Compare(a.Id[:], b.Id[:]) < 0
	}

	for {
		var next *kind
		for _, k := range ks {
			if err := fill(k); err != nil {
				return err
			}
			if len(k.rows) > 0 && (next == nil || before(k.rows[0], next.rows[0])) {
				next = k
			}
		}
		if next == nil {
			return nil
		}
		v := next.rows[0]
		next.rows = next.rows[1:]
		if err := f(v); err != nil {
			return err
		}
	}
}

// accessOf hands `f` the access record's rows as the tenant may read them,
// oldest first: those filed under it, and those of what its own people did in
// another -- the wall's rule, as [trailOf] reads the trail by.
func accessOf(ctx context.Context, db *ent.Client, t uuid.UUID, f func(*ent.Access) error) error {
	for _, of := range [][]predicate.Access{
		{entaccess.TenantId(t)},
		{entaccess.ActorTenantId(t), entaccess.TenantIdNEQ(t)},
	} {
		var last *mark
		for {
			vs, err := db.Access.Query().
				Where(of...).
				Where(after(last)).
				Order(ent.Asc(entaccess.FieldDateCreated, entaccess.FieldId)).
				Limit(page).
				All(ctx)
			if err != nil {
				return err
			}
			for _, v := range vs {
				if err := f(v); err != nil {
					return err
				}
			}
			if len(vs) < page {
				break
			}
			v := vs[len(vs)-1]
			last = &mark{at: v.DateCreated, id: v.Id}
		}
	}

	return nil
}
