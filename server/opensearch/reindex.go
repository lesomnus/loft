package opensearch

import (
	"context"
	"fmt"
	"time"
	"uuid"

	"github.com/lesomnus/rove/internal/ent"
)

// page is how many rows a reindex reads from the database, and writes to the
// index, at a time.
var page = 1000

// Reindexed is what a reindex did.
type Reindexed struct {
	// Index is the index it made and pointed the alias at.
	Index string
	// Docs is how many documents it wrote there.
	Docs int
	// Replaced is the indexes it deleted: the one the alias pointed at
	// before, and any a reindex that stopped halfway left behind.
	Replaced []string
}

// Reindex makes the index of ix again from db.
//
// It is a new index, everything of its kind that is not erased written into
// it, and the alias moved onto it in one step, so that a search answers from
// the old index until the new one is whole and never from neither. The index
// the alias pointed at is deleted after, and so is any one a reindex that
// stopped halfway left. One reindex of an Index runs at a time: a second
// deletes what the first is still writing.
//
// It is how a deployment begins to search with OpenSearch, how a new
// [Index.Version] comes into use, and how an index that was lost or has gone
// wrong is made right; done says how far it is, after every page.
func Reindex(ctx context.Context, c *Client, db *ent.Client, ix *Index, now time.Time, done func(n int)) (Reindexed, error) {
	out := Reindexed{Index: c.fresh(ix, now)}
	if err := c.create(ctx, ix, out.Index); err != nil {
		return out, fmt.Errorf("make %s: %w", out.Index, err)
	}

	// Pages in key order, each one past the last key of the one before: a
	// page is the same query however far in it is.
	var last *uuid.UUID
	for {
		rows, err := ix.page(ctx, db, last, page)
		if err != nil {
			return out, err
		}
		if len(rows) == 0 {
			break
		}

		ops := make([]Op, 0, len(rows))
		for _, r := range rows {
			ops = append(ops, Op{Index: out.Index, Id: r.key.String(), Version: r.version, Doc: r.doc})
		}
		if _, err := c.Bulk(ctx, ops); err != nil {
			return out, fmt.Errorf("write %s: %w", out.Index, err)
		}
		out.Docs += len(rows)
		if done != nil {
			done(out.Docs)
		}

		k := rows[len(rows)-1].key
		last = &k
		if len(rows) < page {
			break
		}
	}

	if err := c.refresh(ctx, out.Index); err != nil {
		return out, err
	}
	if err := c.point(ctx, ix, out.Index); err != nil {
		return out, fmt.Errorf("point %s at %s: %w", c.Alias(ix), out.Index, err)
	}

	names, err := c.indexes(ctx, ix)
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

// State is what the index of an [Index] is now.
type State struct {
	Alias string
	// Index is what the alias points at; empty is nothing, which is a
	// deployment that has not made one yet.
	Index string
	// Version is the shape Index was made in; see [Index.Version].
	Version int
	// Docs is how many documents it holds.
	Docs int64
	// Others is every other index of it this deployment has: what a reindex
	// that stopped halfway left behind.
	Others []string
}

// Status is what the index of ix is now.
func (c *Client) Status(ctx context.Context, ix *Index) (State, error) {
	s := State{Alias: c.Alias(ix)}
	idx, err := c.current(ctx, ix)
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
	names, err := c.indexes(ctx, ix)
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
