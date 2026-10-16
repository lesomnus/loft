package opensearch

import (
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"net/url"
	"sort"
	"strings"
	"time"
	"uuid"

	"github.com/lesomnus/rove/internal/ent"
)

// An Index is one kind of thing a deployment keeps documents of -- its
// assets, its people, its models -- each in indexes of its own: the fields
// its documents hold and how each is analyzed, its own [Index.Version], its
// own alias, and how its documents are made from the database.
//
// They are apart rather than one index of everything because what a search
// of people asks is not what a search of things asks, and because what a
// score counts is a field's words across its index: names of people beside
// names of things would make each other's words commoner.
type Index struct {
	// Name is what its alias ends with: `<index>-<Name>`.
	Name string
	// Version is the shape this build makes it in: its analyzers and fields.
	// One made by a build with another answers with what that build put in
	// it, which is what `rove search status` points out and the loop, or
	// `rove search reindex`, cures.
	Version int

	// shape is what it is made with; see the file.
	shape []byte
	// read is the documents of keys as the database has them now, and none
	// for a key it does not have.
	read func(ctx context.Context, db *ent.Client, keys []uuid.UUID) ([]made, error)
	// page is up to n of what is not erased, past `after` (nil: from the
	// start), in key order.
	page func(ctx context.Context, db *ent.Client, after *uuid.UUID, n int) ([]made, error)
}

// made is one document as the database has it: what it is of, the document,
// its version ([DocOf]), and when its row was erased, for one that was.
type made struct {
	key     uuid.UUID
	doc     any
	version int64
	erased  *time.Time
}

// The shapes, each strict, so that a document with a field it does not name
// is refused rather than given one OpenSearch guessed at. Each `_meta.version`
// is its Index's Version, and changes with it.
var (
	//go:embed assets.json
	assetsShape []byte
	//go:embed parties.json
	partiesShape []byte
	//go:embed models.json
	modelsShape []byte
)

var (
	// Assets is what `AssetService.Search` asks: the name, the tag, the
	// serial, and what the asset takes a name from ([Doc]).
	Assets = &Index{Name: "assets", Version: 2, shape: assetsShape, read: readAssets, page: pageAssets}
	// Parties is what `PartyService.Search` asks: people, teams,
	// organizations and vendors ([PartyDoc]).
	Parties = &Index{Name: "parties", Version: 1, shape: partiesShape, read: readParties, page: pageParties}
	// Models is what `ItemModelService.Search` asks ([ModelDoc]).
	Models = &Index{Name: "models", Version: 1, shape: modelsShape, read: readModels, page: pageModels}

	// Indexes is every one of them, in the order the loop makes them.
	Indexes = []*Index{Assets, Parties, Models}
)

// Alias is the name ix is searched and written by. It points at one index at
// a time, which a reindex replaces in one step.
func (c *Client) Alias(ix *Index) string { return c.prefix + "-" + ix.Name }

// fresh is the name of a new index of ix: the alias's, and when it was made,
// to the millisecond.
func (c *Client) fresh(ix *Index, now time.Time) string {
	return c.Alias(ix) + "-" + now.UTC().Format("20060102-150405.000")
}

// create makes an index of ix called name.
func (c *Client) create(ctx context.Context, ix *Index, name string) error {
	return c.call(ctx, "PUT", "/"+url.PathEscape(name), json.RawMessage(ix.shape), nil)
}

// current is the index ix's alias points at, or "" when it points at none: a
// cluster this deployment never made one on.
func (c *Client) current(ctx context.Context, ix *Index) (string, error) {
	names, err := c.pointed(ctx, ix)
	if err != nil || len(names) == 0 {
		return "", err
	}
	if len(names) > 1 {
		return "", fmt.Errorf("opensearch: %s points at %d indexes (%s); a reindex points it at one again", c.Alias(ix), len(names), strings.Join(names, ", "))
	}
	return names[0], nil
}

// pointed is every index ix's alias points at.
//
// It asks among this deployment's own indexes: asked of every index, the
// cluster would want the role to be allowed to read the aliases of all of
// them, and a role this deployment signs in with is allowed its own.
func (c *Client) pointed(ctx context.Context, ix *Index) ([]string, error) {
	alias := c.Alias(ix)
	v := map[string]struct {
		Aliases map[string]json.RawMessage `json:"aliases"`
	}{}
	if err := c.call(ctx, "GET", "/"+alias+"-*/_alias/"+url.PathEscape(alias), nil, &v); err != nil {
		if IsNotFound(err) {
			return nil, nil
		}
		return nil, err
	}
	names := make([]string, 0, len(v))
	for k, x := range v {
		if _, ok := x.Aliases[alias]; ok {
			names = append(names, k)
		}
	}
	sort.Strings(names)
	return names, nil
}

// point moves ix's alias onto name, off every index it pointed at, in one
// step: a search answers from the old index or from the new one, and never
// from neither.
func (c *Client) point(ctx context.Context, ix *Index, name string) error {
	from, err := c.pointed(ctx, ix)
	if err != nil {
		return err
	}
	alias := c.Alias(ix)
	actions := []map[string]map[string]string{}
	for _, v := range from {
		if v != name {
			actions = append(actions, map[string]map[string]string{"remove": {"index": v, "alias": alias}})
		}
	}
	actions = append(actions, map[string]map[string]string{"add": {"index": name, "alias": alias}})
	return c.call(ctx, "POST", "/_aliases", map[string]any{"actions": actions}, nil)
}

// indexes is every index of ix this deployment has made, pointed at or not,
// by name.
//
// It reads them off their settings rather than `_cat/indices`, which asks
// the cluster's state: this way the role a deployment signs in with needs
// nothing of the cluster's beyond writing in bulk (README).
func (c *Client) indexes(ctx context.Context, ix *Index) ([]string, error) {
	v := map[string]json.RawMessage{}
	if err := c.call(ctx, "GET", "/"+c.Alias(ix)+"-*/_settings/index.creation_date", nil, &v); err != nil {
		if IsNotFound(err) {
			return nil, nil
		}
		return nil, err
	}
	names := make([]string, 0, len(v))
	for k := range v {
		names = append(names, k)
	}
	sort.Strings(names)
	return names, nil
}

// drop deletes an index.
func (c *Client) drop(ctx context.Context, name string) error {
	return c.call(ctx, "DELETE", "/"+url.PathEscape(name), nil, nil)
}

// refresh makes what was written to an index searchable now, rather than at
// its next refresh.
func (c *Client) refresh(ctx context.Context, name string) error {
	return c.call(ctx, "POST", "/"+url.PathEscape(name)+"/_refresh", nil, nil)
}

// count is how many documents an index holds.
func (c *Client) count(ctx context.Context, name string) (int64, error) {
	var v struct {
		Count int64 `json:"count"`
	}
	if err := c.call(ctx, "GET", "/"+url.PathEscape(name)+"/_count", nil, &v); err != nil {
		return 0, err
	}
	return v.Count, nil
}

// version is the shape an index was made in: its mapping's `_meta.version`,
// and 0 for one that says none.
func (c *Client) version(ctx context.Context, name string) (int, error) {
	v := map[string]struct {
		Mappings struct {
			Meta struct {
				Version int `json:"version"`
			} `json:"_meta"`
		} `json:"mappings"`
	}{}
	if err := c.call(ctx, "GET", "/"+url.PathEscape(name)+"/_mapping", nil, &v); err != nil {
		return 0, err
	}
	return v[name].Mappings.Meta.Version, nil
}

// Analyze is the words the index of assets makes of text with one of its
// analyzers: `ko`, what a name is kept as, or `ko_search`, what a search for
// it asks -- which every index has the same of. It is how an operator sees
// why something is or is not found (`rove search analyze`).
func (c *Client) Analyze(ctx context.Context, analyzer, text string) ([]string, error) {
	var v struct {
		Tokens []struct {
			Token string `json:"token"`
		} `json:"tokens"`
	}
	in := map[string]string{"analyzer": analyzer, "text": text}
	if err := c.call(ctx, "POST", "/"+c.Alias(Assets)+"/_analyze", in, &v); err != nil {
		return nil, err
	}
	out := make([]string, 0, len(v.Tokens))
	for _, t := range v.Tokens {
		out = append(out, t.Token)
	}
	return out, nil
}

// Remove deletes every index this deployment made, of every [Index], and the
// aliases with them: for a deployment that stops searching with OpenSearch,
// and for a test that is done with its cluster.
func (c *Client) Remove(ctx context.Context) error {
	for _, ix := range Indexes {
		names, err := c.indexes(ctx, ix)
		if err != nil {
			return err
		}
		for _, v := range names {
			if err := c.drop(ctx, v); err != nil && !IsNotFound(err) {
				return err
			}
		}
	}
	return nil
}

// deleteTenant takes every document of one workspace out of an index.
//
// They are found a thousand at a time and deleted in bulk, rather than with
// `_delete_by_query`: that scrolls, and the cluster asks a role to be allowed
// to scroll on the cluster as a whole. The deletes carry no version, since
// nothing of a workspace that went comes back to be kept against.
func (c *Client) deleteTenant(ctx context.Context, name, tenant string) error {
	q := map[string]any{
		"size":    1000,
		"_source": false,
		"query":   map[string]any{"term": map[string]any{"tenant": tenant}},
	}
	for {
		var v struct {
			Hits struct {
				Hits []struct {
					Id string `json:"_id"`
				} `json:"hits"`
			} `json:"hits"`
		}
		if err := c.call(ctx, "POST", "/"+url.PathEscape(name)+"/_search", q, &v); err != nil {
			return err
		}
		if len(v.Hits.Hits) == 0 {
			return nil
		}
		ops := make([]Op, len(v.Hits.Hits))
		for i, h := range v.Hits.Hits {
			ops[i] = Op{Index: name, Id: h.Id}
		}
		if _, err := c.Bulk(ctx, ops); err != nil {
			return err
		}
		// So that the next search does not find what was just deleted.
		if err := c.refresh(ctx, name); err != nil {
			return err
		}
	}
}

// doc is the document an index holds of id, as it was written, and its
// version; nil when it holds none.
func (c *Client) doc(ctx context.Context, name, id string) (json.RawMessage, int64, error) {
	var v struct {
		Found   bool            `json:"found"`
		Version int64           `json:"_version"`
		Source  json.RawMessage `json:"_source"`
	}
	if err := c.call(ctx, "GET", "/"+url.PathEscape(name)+"/_doc/"+url.PathEscape(id), nil, &v); err != nil {
		if IsNotFound(err) {
			return nil, 0, nil
		}
		return nil, 0, err
	}
	if !v.Found {
		return nil, 0, nil
	}
	return v.Source, v.Version, nil
}

// Refresh makes what was written to every index searchable now, rather than
// at its next refresh, a second at most: what a test that writes and then
// searches waits for. An index not made yet is nothing to refresh.
func (c *Client) Refresh(ctx context.Context) error {
	for _, ix := range Indexes {
		if err := c.refresh(ctx, c.Alias(ix)); err != nil && !IsNotFound(err) {
			return err
		}
	}
	return nil
}
