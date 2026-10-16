package opensearch

import (
	"context"
	"strings"
	"time"
	"uuid"

	"github.com/lesomnus/payday/pdid"

	"github.com/lesomnus/rove/internal/ent"
	"github.com/lesomnus/rove/server/bare"
	"github.com/lesomnus/rove/server/pd"
)

// What a [ent.SearchStale] says changed.
const (
	KindAsset     = "asset"
	KindAssetType = "asset_type"
	KindItemModel = "item_model"
	KindParty     = "party"
	// KindTenant is a workspace taken out of the deployment, every document
	// of which goes.
	KindTenant = "tenant"
)

// watched is the services whose writes make documents stale, by the kind of
// what they write: assets, people and models, and what their documents take
// a name from ([DocOf], [PartyDocOf], [ModelDocOf]).
var watched = map[string]string{
	"rove.AssetService":     KindAsset,
	"rove.AssetTypeService": KindAssetType,
	"rove.ItemModelService": KindItemModel,
	"rove.PartyService":     KindParty,
}

// own is the [Index] whose document of a row is the row's own: what a call
// that wrote it writes before it answers ([Indexer.Interceptor]).
var own = map[string]*Index{
	KindAsset:     Assets,
	KindItemModel: Models,
	KindParty:     Parties,
}

// Recorder writes down every write that makes documents of the index stale,
// inside the transaction that makes it, for the [Indexer] to make them again.
//
// Being in that transaction is the point. A change that commits is one the
// index hears of, whatever becomes of the process after; one that is rolled
// back takes its row with it. A deployment that names no cluster installs
// none, and writes nothing down.
//
// It reads [bare.Change.By] -- the write this server made, an asset's Patch
// even when the call was a move -- since what it is about is the row.
func Recorder() bare.Recorder { return recorder{} }

type recorder struct{}

var _ bare.Recorder = recorder{}

func (recorder) Record(ctx context.Context, s bare.Server, c bare.Change) error {
	svc, _, ok := strings.Cut(strings.TrimPrefix(c.By, "/"), "/")
	if !ok {
		return nil
	}
	kind, ok := watched[svc]
	if !ok {
		return nil
	}
	k, ok := c.Key.(uuid.UUID)
	if !ok {
		return nil
	}

	now := time.Now()
	if s.Now != nil {
		now = s.Now()
	}
	if w, ok := ctx.Value(writtenKey{}).(*written); ok {
		if ix, ok := own[kind]; ok {
			w.add(ix, k)
		}
	}
	return stale(ctx, s.Db, kind, k, now)
}

// Forget writes down, in the transaction that takes a workspace out of the
// deployment, that every document of it goes: the purge's rows go past the
// recorder (`offboard.Deployment.Unindex`).
func Forget(ctx context.Context, db *ent.Client, tenant uuid.UUID) error {
	return stale(ctx, db, KindTenant, tenant, time.Now())
}

func stale(ctx context.Context, db *ent.Client, kind string, key uuid.UUID, now time.Time) error {
	return db.SearchStale.Create().
		SetId(pdid.New(pd.SearchStaleDomain).Uuid()).
		SetKind(kind).
		SetKey(key).
		SetDateCreated(now).
		Exec(ctx)
}
