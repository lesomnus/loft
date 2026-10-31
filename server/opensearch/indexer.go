package opensearch

import (
	"context"
	"fmt"
	"log/slog"
	"maps"
	"slices"
	"time"
	"uuid"

	"github.com/lesomnus/otx/log"

	"github.com/lesomnus/loft/internal/ent"
	"github.com/lesomnus/loft/internal/ent/asset"
	"github.com/lesomnus/loft/internal/ent/itemmodel"
	"github.com/lesomnus/loft/internal/ent/party"
	"github.com/lesomnus/loft/internal/ent/predicate"
	"github.com/lesomnus/loft/internal/ent/searchstale"
)

// batch is how many rows of the queue one pass takes, and chunk how many
// documents it reads and writes at a time.
var (
	batch = 256
	chunk = 500
)

// Indexer is the loop that keeps the index current (D31): what [Recorder]
// wrote down, made again from what the database says now, and the rows taken
// out once it is written.
//
// What it writes is the database now and not what the change said, so a row
// is never too late or too early, and two passes over one -- a lease changing
// hands -- write the same. Each document carries its version ([DocOf]), and
// the index keeps the newer of two.
//
// It writes to every index the deployment has of each [Index]: the one the
// alias points at, and one a reindex is filling. A pass reads the queue first
// and the indexes after, and a reindex makes its index before it reads the
// database; so a reindex either began in time to be written to, or reads
// every change the pass is about for itself.
type Indexer struct {
	Client *Client
	Db     *ent.Client
	// Every is how long it waits when there was nothing to do: about how long
	// a change takes to be found. Unset is a second.
	Every time.Duration
	// Now is the clock; unset is time.Now.
	Now func() time.Time
}

// SpinName is what the processes of a deployment call it, and agree on: one
// of them runs it (`spin.lease`).
func (x Indexer) SpinName() string { return "loft.search.index" }

func (x Indexer) now() time.Time {
	if x.Now != nil {
		return x.Now()
	}
	return time.Now()
}

// Spin runs passes until ctx ends.
//
// Before the first it makes each index, when there is none or it is not the
// shape this build makes ([Index.Version]): a deployment that has just named
// a cluster, or a build that changed an index or added one, searches it once
// it is whole, and until then from what there was.
//
// A pass that took a whole batch is followed by another at once. One that
// failed -- a cluster that does not answer -- is followed by a wait that
// doubles up to a minute, and the queue keeps what it had: the log says so
// when it begins, once a minute while it lasts, and when it is over.
func (x Indexer) Spin(ctx context.Context) error {
	every := x.Every
	if every <= 0 {
		every = time.Second
	}
	var down time.Duration
	made := false
	for {
		var n int
		var err error
		if !made {
			err = x.ensure(ctx)
			made = err == nil
		}
		if made {
			n, err = x.Pass(ctx)
		}
		wait := every
		switch {
		case ctx.Err() != nil:
			return nil
		case err != nil:
			if down == 0 || down == time.Minute {
				log.From(ctx).WarnContext(ctx, "search: the index is not kept current; what changed waits", slog.Any("err", err))
			}
			down = min(max(2*down, time.Second), time.Minute)
			wait = down
		default:
			if down > 0 {
				log.From(ctx).InfoContext(ctx, "search: the index is kept current again")
				down = 0
			}
			if n >= batch {
				wait = 0
			}
		}

		t := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			t.Stop()
			return nil
		case <-t.C:
		}
	}
}

// ensure makes each index from the database when there is none to keep
// current, or none of this build's shape.
func (x Indexer) ensure(ctx context.Context) error {
	for _, ix := range Indexes {
		st, err := x.Client.Status(ctx, ix)
		if err != nil {
			return err
		}
		if st.Index != "" && st.Version == ix.Version {
			continue
		}
		l := log.From(ctx)
		if st.Index == "" {
			l.InfoContext(ctx, "search: no index yet; making one from the database", "alias", st.Alias)
		} else {
			l.InfoContext(ctx, "search: the index is of another shape; making it again", "index", st.Index, "version", st.Version, "makes", ix.Version)
		}
		r, err := Reindex(ctx, x.Client, x.Db, ix, x.now(), nil)
		if err != nil {
			return err
		}
		l.InfoContext(ctx, "search: made the index", "index", r.Index, "docs", r.Docs)
	}
	return nil
}

// Pass makes again the documents of up to [batch] rows of the queue, and
// takes those rows out; it answers how many it took. A pass that fails takes
// none out, and the next one tries them again.
func (x Indexer) Pass(ctx context.Context) (int, error) {
	rows, err := x.Db.SearchStale.Query().
		Order(ent.Asc(searchstale.FieldDateCreated), ent.Asc(searchstale.FieldId)).
		Limit(batch).
		All(ctx)
	if err != nil || len(rows) == 0 {
		return 0, err
	}

	// After the rows; see [Indexer].
	indexes := map[*Index][]string{}
	for _, ix := range Indexes {
		names, err := x.Client.indexes(ctx, ix)
		if err != nil {
			return 0, err
		}
		indexes[ix] = names
	}
	if err := x.apply(ctx, indexes, rows); err != nil {
		return 0, err
	}

	ids := make([]uuid.UUID, len(rows))
	for i, r := range rows {
		ids[i] = r.Id
	}
	if _, err := x.Db.SearchStale.Delete().Where(searchstale.IdIn(ids...)).Exec(ctx); err != nil {
		return 0, err
	}
	return len(rows), nil
}

// apply writes to the indexes of each [Index] what rows say is stale. With no
// index of one there is nothing of it to keep current: the reindex that
// makes one reads all of it.
func (x Indexer) apply(ctx context.Context, indexes map[*Index][]string, rows []*ent.SearchStale) error {
	stale := map[*Index]map[uuid.UUID]bool{Assets: {}, Parties: {}, Models: {}}
	var types, models, parties, tenants []uuid.UUID
	for _, r := range rows {
		switch r.Kind {
		case KindAsset:
			stale[Assets][r.Key] = true
		case KindAssetType:
			types = append(types, r.Key)
		case KindItemModel:
			models = append(models, r.Key)
			stale[Models][r.Key] = true
		case KindParty:
			parties = append(parties, r.Key)
			stale[Parties][r.Key] = true
		case KindTenant:
			tenants = append(tenants, r.Key)
		}
	}

	// A workspace that went: every document of it, whatever else is said.
	for _, t := range tenants {
		for _, ix := range Indexes {
			for _, idx := range indexes[ix] {
				if err := x.Client.deleteTenant(ctx, idx, t.String()); err != nil {
					return fmt.Errorf("forget the workspace %s in %s: %w", t, idx, err)
				}
			}
		}
	}

	// What a document takes a name from: every one that takes it. An asset
	// takes its type's, its model's and its custodian's; a model its type's;
	// and a party the name of the team or organization it is in.
	var from []predicate.Asset
	if len(types) > 0 {
		from = append(from, asset.TypeIdIn(types...))
	}
	if len(models) > 0 {
		from = append(from, asset.ModelIdIn(models...))
	}
	if len(parties) > 0 {
		from = append(from, asset.CustodianIdIn(parties...))
	}
	if len(from) > 0 && len(indexes[Assets]) > 0 {
		ids, err := x.Db.Asset.Query().Where(asset.Or(from...), asset.DateErasedIsNil()).Ids(ctx)
		if err != nil {
			return err
		}
		for _, k := range ids {
			stale[Assets][k] = true
		}
	}
	if len(types) > 0 && len(indexes[Models]) > 0 {
		ids, err := x.Db.ItemModel.Query().Where(itemmodel.TypeIdIn(types...), itemmodel.DateErasedIsNil()).Ids(ctx)
		if err != nil {
			return err
		}
		for _, k := range ids {
			stale[Models][k] = true
		}
	}
	if len(parties) > 0 && len(indexes[Parties]) > 0 {
		ids, err := x.Db.Party.Query().Where(party.ParentIdIn(parties...), party.DateErasedIsNil()).Ids(ctx)
		if err != nil {
			return err
		}
		for _, k := range ids {
			stale[Parties][k] = true
		}
	}

	for _, ix := range Indexes {
		if len(indexes[ix]) == 0 {
			continue
		}
		keys := slices.SortedFunc(maps.Keys(stale[ix]), func(a, b uuid.UUID) int { return slices.Compare(a[:], b[:]) })
		for part := range slices.Chunk(keys, chunk) {
			if err := x.write(ctx, ix, indexes[ix], part); err != nil {
				return err
			}
		}
	}
	return nil
}

// write puts the documents of keys into every one of indexes, all of ix, as
// the database has them now: a document for each, and none for one that was
// erased or is gone.
func (x Indexer) write(ctx context.Context, ix *Index, indexes []string, keys []uuid.UUID) error {
	rows, err := ix.read(ctx, x.Db, keys)
	if err != nil {
		return err
	}

	// A delete is versioned past everything it might follow: the newest of
	// the rows the document was made of, and now.
	gone := x.now().UnixMicro()
	found := map[uuid.UUID]bool{}
	ops := make([]Op, 0, len(keys)*len(indexes))
	for _, r := range rows {
		found[r.key] = true
		if r.erased != nil {
			v := max(r.version, r.erased.UnixMicro(), gone) + 1
			for _, idx := range indexes {
				ops = append(ops, Op{Index: idx, Id: r.key.String(), Version: v})
			}
			continue
		}
		for _, idx := range indexes {
			ops = append(ops, Op{Index: idx, Id: r.key.String(), Version: r.version, Doc: r.doc})
		}
	}
	for _, k := range keys {
		if found[k] {
			continue
		}
		for _, idx := range indexes {
			ops = append(ops, Op{Index: idx, Id: k.String(), Version: gone + 1})
		}
	}
	_, err = x.Client.Bulk(ctx, ops)
	return err
}
