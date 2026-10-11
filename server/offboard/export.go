package offboard

import (
	"archive/zip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"slices"
	"time"
	"uuid"

	"github.com/lesomnus/payday/pdid"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"

	"github.com/lesomnus/rove/internal/ent"
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
	"github.com/lesomnus/rove/internal/ent/invitation"
	"github.com/lesomnus/rove/internal/ent/inventorycount"
	"github.com/lesomnus/rove/internal/ent/itemmodel"
	"github.com/lesomnus/rove/internal/ent/label"
	"github.com/lesomnus/rove/internal/ent/link"
	"github.com/lesomnus/rove/internal/ent/notification"
	"github.com/lesomnus/rove/internal/ent/party"
	"github.com/lesomnus/rove/internal/ent/placement"
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
	// `trail/archive.jsonl`.
	Trail struct {
		Database int `json:"database"`
		Archive  int `json:"archive"`
	} `json:"trail"`

	// Withheld is what is left out, and why.
	Withheld map[string]string `json:"withheld"`
}

// reader is every row of one kind a tenant has, as the API answers them.
type reader func(ctx context.Context, db *ent.Client, t uuid.UUID) ([]proto.Message, error)

func each[T any, M proto.Message](f func(T) M) func([]T, error) ([]proto.Message, error) {
	return func(vs []T, err error) ([]proto.Message, error) {
		if err != nil {
			return nil, err
		}
		out := make([]proto.Message, 0, len(vs))
		for _, v := range vs {
			out = append(out, f(v))
		}
		return out, nil
	}
}

// Exported is every kind an export writes, by its table.
var Exported = map[string]reader{
	"allocation": func(ctx context.Context, db *ent.Client, t uuid.UUID) ([]proto.Message, error) {
		return each((*ent.Allocation).Proto)(db.Allocation.Query().Where(allocation.TenantId(t)).All(ctx))
	},
	"asset": func(ctx context.Context, db *ent.Client, t uuid.UUID) ([]proto.Message, error) {
		return each((*ent.Asset).Proto)(db.Asset.Query().Where(asset.TenantId(t)).All(ctx))
	},
	"assettype": func(ctx context.Context, db *ent.Client, t uuid.UUID) ([]proto.Message, error) {
		return each((*ent.AssetType).Proto)(db.AssetType.Query().Where(assettype.TenantId(t)).All(ctx))
	},
	"attachment": func(ctx context.Context, db *ent.Client, t uuid.UUID) ([]proto.Message, error) {
		return each((*ent.Attachment).Proto)(db.Attachment.Query().Where(attachment.TenantId(t)).All(ctx))
	},
	"bookable": func(ctx context.Context, db *ent.Client, t uuid.UUID) ([]proto.Message, error) {
		return each((*ent.Bookable).Proto)(db.Bookable.Query().Where(bookable.TenantId(t)).All(ctx))
	},
	"countfinding": func(ctx context.Context, db *ent.Client, t uuid.UUID) ([]proto.Message, error) {
		return each((*ent.CountFinding).Proto)(db.CountFinding.Query().Where(countfinding.TenantId(t)).All(ctx))
	},
	"custody": func(ctx context.Context, db *ent.Client, t uuid.UUID) ([]proto.Message, error) {
		return each((*ent.Custody).Proto)(db.Custody.Query().Where(custody.TenantId(t)).All(ctx))
	},
	"custodyline": func(ctx context.Context, db *ent.Client, t uuid.UUID) ([]proto.Message, error) {
		return each((*ent.CustodyLine).Proto)(db.CustodyLine.Query().Where(custodyline.TenantId(t)).All(ctx))
	},
	"event": func(ctx context.Context, db *ent.Client, t uuid.UUID) ([]proto.Message, error) {
		return each((*ent.Event).Proto)(db.Event.Query().Where(event.TenantId(t)).All(ctx))
	},
	"fact": func(ctx context.Context, db *ent.Client, t uuid.UUID) ([]proto.Message, error) {
		return each((*ent.Fact).Proto)(db.Fact.Query().Where(fact.TenantId(t)).All(ctx))
	},
	"holder": func(ctx context.Context, db *ent.Client, t uuid.UUID) ([]proto.Message, error) {
		return each((*ent.Holder).Proto)(db.Holder.Query().Where(holder.TenantId(t)).All(ctx))
	},
	// Who was invited, as what, and what became of it -- without the code a
	// mail carried, which is a way in and nothing the tenant needs back.
	"invitation": func(ctx context.Context, db *ent.Client, t uuid.UUID) ([]proto.Message, error) {
		return each(func(v *ent.Invitation) proto.Message {
			m := v.Proto()
			m.SetClaim(nil)
			return m
		})(db.Invitation.Query().Where(invitation.TenantId(t)).All(ctx))
	},
	"inventorycount": func(ctx context.Context, db *ent.Client, t uuid.UUID) ([]proto.Message, error) {
		return each((*ent.InventoryCount).Proto)(db.InventoryCount.Query().Where(inventorycount.TenantId(t)).All(ctx))
	},
	"itemmodel": func(ctx context.Context, db *ent.Client, t uuid.UUID) ([]proto.Message, error) {
		return each((*ent.ItemModel).Proto)(db.ItemModel.Query().Where(itemmodel.TenantId(t)).All(ctx))
	},
	"label": func(ctx context.Context, db *ent.Client, t uuid.UUID) ([]proto.Message, error) {
		return each((*ent.Label).Proto)(db.Label.Query().Where(label.TenantId(t)).All(ctx))
	},
	"link": func(ctx context.Context, db *ent.Client, t uuid.UUID) ([]proto.Message, error) {
		return each((*ent.Link).Proto)(db.Link.Query().Where(link.TenantId(t)).All(ctx))
	},
	"notification": func(ctx context.Context, db *ent.Client, t uuid.UUID) ([]proto.Message, error) {
		return each((*ent.Notification).Proto)(db.Notification.Query().Where(notification.TenantId(t)).All(ctx))
	},
	"party": func(ctx context.Context, db *ent.Client, t uuid.UUID) ([]proto.Message, error) {
		return each((*ent.Party).Proto)(db.Party.Query().Where(party.TenantId(t)).All(ctx))
	},
	"placement": func(ctx context.Context, db *ent.Client, t uuid.UUID) ([]proto.Message, error) {
		return each((*ent.Placement).Proto)(db.Placement.Query().Where(placement.TenantId(t)).All(ctx))
	},
	"purchase": func(ctx context.Context, db *ent.Client, t uuid.UUID) ([]proto.Message, error) {
		return each((*ent.Purchase).Proto)(db.Purchase.Query().Where(purchase.TenantId(t)).All(ctx))
	},
	"purchaseline": func(ctx context.Context, db *ent.Client, t uuid.UUID) ([]proto.Message, error) {
		return each((*ent.PurchaseLine).Proto)(db.PurchaseLine.Query().Where(purchaseline.TenantId(t)).All(ctx))
	},
	"reservation": func(ctx context.Context, db *ent.Client, t uuid.UUID) ([]proto.Message, error) {
		return each((*ent.Reservation).Proto)(db.Reservation.Query().Where(reservation.TenantId(t)).All(ctx))
	},
	"reservationitem": func(ctx context.Context, db *ent.Client, t uuid.UUID) ([]proto.Message, error) {
		return each((*ent.ReservationItem).Proto)(db.ReservationItem.Query().Where(reservationitem.TenantId(t)).All(ctx))
	},
	"stewardship": func(ctx context.Context, db *ent.Client, t uuid.UUID) ([]proto.Message, error) {
		return each((*ent.Stewardship).Proto)(db.Stewardship.Query().Where(stewardship.TenantId(t)).All(ctx))
	},
	"stock": func(ctx context.Context, db *ent.Client, t uuid.UUID) ([]proto.Message, error) {
		return each((*ent.Stock).Proto)(db.Stock.Query().Where(stock.TenantId(t)).All(ctx))
	},
	"stockmovement": func(ctx context.Context, db *ent.Client, t uuid.UUID) ([]proto.Message, error) {
		return each((*ent.StockMovement).Proto)(db.StockMovement.Query().Where(stockmovement.TenantId(t)).All(ctx))
	},
	"tenantcontract": func(ctx context.Context, db *ent.Client, t uuid.UUID) ([]proto.Message, error) {
		return each((*ent.TenantContract).Proto)(db.TenantContract.Query().Where(tenantcontract.TenantId(t)).All(ctx))
	},
	"tenantdomain": func(ctx context.Context, db *ent.Client, t uuid.UUID) ([]proto.Message, error) {
		return each((*ent.TenantDomain).Proto)(db.TenantDomain.Query().Where(tenantdomain.TenantId(t)).All(ctx))
	},
	"usagesnapshot": func(ctx context.Context, db *ent.Client, t uuid.UUID) ([]proto.Message, error) {
		return each((*ent.UsageSnapshot).Proto)(db.UsageSnapshot.Query().Where(usagesnapshot.TenantId(t)).All(ctx))
	},
	"workorder": func(ctx context.Context, db *ent.Client, t uuid.UUID) ([]proto.Message, error) {
		return each((*ent.WorkOrder).Proto)(db.WorkOrder.Query().Where(workorder.TenantId(t)).All(ctx))
	},
}

// Withheld is every table an export leaves out, and why. A table in neither
// this nor [Exported] is one nobody decided about, which a test refuses.
var Withheld = map[string]string{
	"audit":     "the trail, which is written apart as the tenant may read it",
	"archived":  "the deployment's account of the trail's archive, every tenant's chunks at once",
	"session":   "who is signed in",
	"outbox":    "the deployment's queue of what changed",
	"treelock":  "the deployment's bookkeeping",
	"legalhold": "the operator's",
	"tenant":    "the manifest's",
}

// Export writes everything a tenant has as one zip archive: a JSON line per
// row of each kind, the trail as the tenant may read it -- the rows that name
// it, in the database and in the archive -- and the files its attachments
// kept. What it leaves out is in the manifest, with why.
//
// It reads, and changes nothing; a tenant under a legal hold is exported like
// any other.
func (d Deployment) Export(ctx context.Context, tenant pdid.Id, w io.Writer) (Manifest, error) {
	out := Manifest{ExportedAt: d.now().UTC(), Rows: map[string]int{}, Withheld: Withheld}
	tid := tenant.Uuid()

	t, err := d.Ent.Tenant.Query().Where(enttenant.Id(tid)).Only(ctx)
	if err != nil {
		return out, fmt.Errorf("tenant %s: %w", tenant, err)
	}
	out.Tenant, err = protojson.Marshal(t.Proto())
	if err != nil {
		return out, err
	}

	z := zip.NewWriter(w)
	lines := func(name string, vs []proto.Message) error {
		f, err := z.Create(name)
		if err != nil {
			return err
		}
		for _, v := range vs {
			b, err := protojson.Marshal(v)
			if err != nil {
				return err
			}
			if _, err := f.Write(append(b, '\n')); err != nil {
				return err
			}
		}
		return nil
	}

	for _, k := range slices.Sorted(maps.Keys(Exported)) {
		vs, err := Exported[k](ctx, d.Ent, tid)
		if err != nil {
			return out, fmt.Errorf("%s: %w", k, err)
		}
		if err := lines("rows/"+k+".jsonl", vs); err != nil {
			return out, err
		}
		out.Rows[k] = len(vs)
	}

	// The trail, by the wall's rule: the rows that name the tenant.
	as, err := each((*ent.Audit).Proto)(d.Ent.Audit.Query().
		Where(entaudit.Or(entaudit.TenantId(tid), entaudit.ActorTenantId(tid), entaudit.CounterpartTenantId(tid))).
		Order(ent.Asc(entaudit.FieldDateCreated)).
		All(ctx))
	if err != nil {
		return out, fmt.Errorf("trail: %w", err)
	}
	if err := lines("trail/database.jsonl", as); err != nil {
		return out, err
	}
	out.Trail.Database = len(as)

	if d.Trail.Archive != nil {
		f, err := z.Create("trail/archive.jsonl")
		if err != nil {
			return out, err
		}
		// Through its manifest: only what the deployment wrote there, and
		// only while its bytes are what was written.
		if err := d.Trail.ReadTenant(ctx, pd.TrailStore(d.Ent), tenant, func(doc []byte) error {
			out.Trail.Archive++
			_, err := f.Write(append(doc, '\n'))
			return err
		}); err != nil {
			return out, fmt.Errorf("trail archive: %w", err)
		}
	}

	// The files the attachments kept.
	keys, err := d.Ent.Attachment.Query().Where(attachment.TenantId(tid)).Select(attachment.FieldObjectKey).Strings(ctx)
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
		f, err := z.Create("files/" + k)
		if err == nil {
			_, err = io.Copy(f, r)
		}
		r.Close()
		if err != nil {
			return out, fmt.Errorf("files/%s: %w", k, err)
		}
		out.Files++
	}

	f, err := z.Create("manifest.json")
	if err != nil {
		return out, err
	}
	e := json.NewEncoder(f)
	e.SetIndent("", "  ")
	if err := e.Encode(out); err != nil {
		return out, err
	}

	return out, z.Close()
}
