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
)

// assetsIndex is what an index of assets is made with: how each field is
// analyzed, and what a document may hold. See the file.
//
// The mapping is strict, so a document with a field it does not name is
// refused rather than given one OpenSearch guessed at. Its `_meta.version` is
// [Version], and changes with it.
//
//go:embed assets.json
var assetsIndex []byte

// Version is the shape this build makes an index of assets in: its analyzers
// and fields. An index made by a build with another one answers with what that
// build put in it, which is what `rove search status` points out and `rove
// search reindex` cures.
const Version = 1

// Alias is the name the index of assets is searched and written by. It points
// at one index at a time, which `rove search reindex` replaces in one step.
func (c *Client) Alias() string { return c.prefix + "-assets" }

// fresh is the name of a new index of assets: the alias's, and when it was
// made, to the millisecond.
func (c *Client) fresh(now time.Time) string {
	return c.Alias() + "-" + now.UTC().Format("20060102-150405.000")
}

// create makes an index of assets called name.
func (c *Client) create(ctx context.Context, name string) error {
	return c.call(ctx, "PUT", "/"+url.PathEscape(name), json.RawMessage(assetsIndex), nil)
}

// current is the index the alias points at, or "" when it points at none: a
// cluster this deployment never made an index on.
func (c *Client) current(ctx context.Context) (string, error) {
	names, err := c.pointed(ctx)
	if err != nil || len(names) == 0 {
		return "", err
	}
	if len(names) > 1 {
		return "", fmt.Errorf("opensearch: %s points at %d indexes (%s); a reindex points it at one again", c.Alias(), len(names), strings.Join(names, ", "))
	}
	return names[0], nil
}

// pointed is every index the alias points at.
//
// It asks among this deployment's own indexes: asked of every index, the
// cluster would want the role to be allowed to read the aliases of all of
// them, and a role this deployment signs in with is allowed its own.
func (c *Client) pointed(ctx context.Context) ([]string, error) {
	v := map[string]struct {
		Aliases map[string]json.RawMessage `json:"aliases"`
	}{}
	if err := c.call(ctx, "GET", "/"+c.Alias()+"-*/_alias/"+url.PathEscape(c.Alias()), nil, &v); err != nil {
		if IsNotFound(err) {
			return nil, nil
		}
		return nil, err
	}
	names := make([]string, 0, len(v))
	for k, x := range v {
		if _, ok := x.Aliases[c.Alias()]; ok {
			names = append(names, k)
		}
	}
	sort.Strings(names)
	return names, nil
}

// point moves the alias onto name, off every index it pointed at, in one
// step: a search answers from the old index or from the new one, and never
// from neither.
func (c *Client) point(ctx context.Context, name string) error {
	from, err := c.pointed(ctx)
	if err != nil {
		return err
	}
	actions := []map[string]map[string]string{}
	for _, v := range from {
		if v != name {
			actions = append(actions, map[string]map[string]string{"remove": {"index": v, "alias": c.Alias()}})
		}
	}
	actions = append(actions, map[string]map[string]string{"add": {"index": name, "alias": c.Alias()}})
	return c.call(ctx, "POST", "/_aliases", map[string]any{"actions": actions}, nil)
}

// indexes is every index of assets this deployment has made, pointed at or
// not, by name.
//
// It reads them off their settings rather than `_cat/indices`, which asks
// the cluster's state: this way the role a deployment signs in with needs
// nothing of the cluster's beyond writing in bulk (README).
func (c *Client) indexes(ctx context.Context) ([]string, error) {
	v := map[string]json.RawMessage{}
	if err := c.call(ctx, "GET", "/"+c.Alias()+"-*/_settings/index.creation_date", nil, &v); err != nil {
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

// Analyze is the words the index makes of text with one of its analyzers:
// `ko`, what a name is kept as, or `ko_search`, what a search for it asks.
// It is how an operator sees why something is or is not found (`rove search
// analyze`).
func (c *Client) Analyze(ctx context.Context, analyzer, text string) ([]string, error) {
	var v struct {
		Tokens []struct {
			Token string `json:"token"`
		} `json:"tokens"`
	}
	in := map[string]string{"analyzer": analyzer, "text": text}
	if err := c.call(ctx, "POST", "/"+c.Alias()+"/_analyze", in, &v); err != nil {
		return nil, err
	}
	out := make([]string, 0, len(v.Tokens))
	for _, t := range v.Tokens {
		out = append(out, t.Token)
	}
	return out, nil
}

// Remove deletes every index of assets this deployment made, and the alias
// with them: for a deployment that stops searching with OpenSearch, and for a
// test that is done with its cluster.
func (c *Client) Remove(ctx context.Context) error {
	names, err := c.indexes(ctx)
	if err != nil {
		return err
	}
	for _, v := range names {
		if err := c.drop(ctx, v); err != nil && !IsNotFound(err) {
			return err
		}
	}
	return nil
}
