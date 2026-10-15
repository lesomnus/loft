package opensearch

import (
	"context"
	"fmt"
	"time"
	"uuid"

	"github.com/lesomnus/rove/internal/ent"
	"github.com/lesomnus/rove/internal/ent/asset"
)

// page is how many assets a reindex reads from the database, and writes to
// the index, at a time.
var page = 1000

// Reindexed is what a reindex did.
type Reindexed struct {
	// Index is the index it made and pointed the alias at.
	Index string
	// Docs is how many assets it wrote there.
	Docs int
	// Replaced is the indexes it deleted: the one the alias pointed at
	// before, and any a reindex that stopped halfway left behind.
	Replaced []string
}

// Reindex makes the index of assets again from db.
//
// It is a new index, every asset that is not erased written into it, and the
// alias moved onto it in one step, so that a search answers from the old
// index until the new one is whole and never from neither. The index the alias
// pointed at is deleted after, and so is any one a reindex that stopped
// halfway left. One reindex runs at a time: a second deletes what the first
// is still writing.
//
// It is how a deployment begins to search with OpenSearch, how a new [Version]
// of the index comes into use, and how an index that was lost or has gone
// wrong is made right; done says how far it is, after every page.
func Reindex(ctx context.Context, c *Client, db *ent.Client, now time.Time, done func(n int)) (Reindexed, error) {
	out := Reindexed{Index: c.fresh(now)}
	if err := c.create(ctx, out.Index); err != nil {
		return out, fmt.Errorf("make %s: %w", out.Index, err)
	}

	// Pages in key order, each one past the last key of the one before: a
	// page is the same query however far in it is.
	var last *uuid.UUID
	for {
		q := db.Asset.Query().
			Where(asset.DateErasedIsNil()).
			WithType().WithModel().WithCustodian().
			Order(ent.Asc(asset.FieldId)).
			Limit(page)
		if last != nil {
			q.Where(asset.IdGT(*last))
		}
		rows, err := q.All(ctx)
		if err != nil {
			return out, err
		}
		if len(rows) == 0 {
			break
		}

		ops := make([]Op, 0, len(rows))
		for _, a := range rows {
			d, v := DocOf(a)
			ops = append(ops, Op{Index: out.Index, Id: a.Id.String(), Version: v, Doc: &d})
		}
		if _, err := c.Bulk(ctx, ops); err != nil {
			return out, fmt.Errorf("write %s: %w", out.Index, err)
		}
		out.Docs += len(rows)
		if done != nil {
			done(out.Docs)
		}

		k := rows[len(rows)-1].Id
		last = &k
		if len(rows) < page {
			break
		}
	}

	if err := c.refresh(ctx, out.Index); err != nil {
		return out, err
	}
	if err := c.point(ctx, out.Index); err != nil {
		return out, fmt.Errorf("point %s at %s: %w", c.Alias(), out.Index, err)
	}

	names, err := c.indexes(ctx)
	if err != nil {
		return out, err
	}
	for _, v := range names {
		if v == out.Index {
			continue
		}
		if err := c.drop(ctx, v); err != nil && !IsNotFound(err) {
			return out, fmt.Errorf("delete %s: %w", v, err)
		}
		out.Replaced = append(out.Replaced, v)
	}
	return out, nil
}

// State is what the index of assets is now.
type State struct {
	Alias string
	// Index is what the alias points at; empty is nothing, which is a
	// deployment that has not reindexed yet.
	Index string
	// Version is the shape Index was made in; see [Version].
	Version int
	// Docs is how many documents it holds.
	Docs int64
	// Others is every other index of assets this deployment has: what a
	// reindex that stopped halfway left behind.
	Others []string
}

// Status is what the index of assets is now.
func (c *Client) Status(ctx context.Context) (State, error) {
	s := State{Alias: c.Alias()}
	idx, err := c.current(ctx)
	if err != nil {
		return s, err
	}
	s.Index = idx
	if idx != "" {
		if s.Version, err = c.version(ctx, idx); err != nil {
			return s, err
		}
		if s.Docs, err = c.count(ctx, idx); err != nil {
			return s, err
		}
	}
	names, err := c.indexes(ctx)
	if err != nil {
		return s, err
	}
	for _, v := range names {
		if v != idx {
			s.Others = append(s.Others, v)
		}
	}
	return s, nil
}
