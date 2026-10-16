package opensearch

import (
	"context"
	"strings"
	"uuid"

	"github.com/lesomnus/rove/internal/search"
)

// Query is a search of the index of assets: what a person typed, in one
// tenant, narrowed as `AssetService.Search` narrows it, and the page wanted.
type Query struct {
	Tenant uuid.UUID
	Q      string

	Kind   string
	Status string
	// Type and Custodian are what the assets are of, and who has them; the
	// zero value is any.
	Type      uuid.UUID
	Custodian uuid.UUID
	// Within is the assets to look among, by key; nil is anywhere, and an
	// empty list is nowhere.
	Within []uuid.UUID

	Offset int
	Size   int
}

// Window is how far into what it finds a search can page: OpenSearch's
// `index.max_result_window`, which is left as it is.
const Window = 10000

// fields is what a word is looked for in, and what each is worth: the name
// most, then what the thing is and what it is a model of.
var fields = []string{"name^6", "type^4", "model^4", "maker^2", "custodian^2"}

// Search answers what q finds: the keys of the assets, best first and by tag
// among equals, and how many it finds in all.
//
// Best first is, in order: a tag or a serial typed whole; every word typed,
// in whichever of the fields they are; then some of the words, the rarer
// counting for more -- `GPU 서버` finds `GPU서버` first, then `NVIDIA GPU`,
// then `웹서버`. A word is all of its pieces, so a tag nobody has finds
// nothing rather than whatever shares a letter with it. Beside those it
// finds the start of a word as it is being typed, a word with a letter wrong
// in a name, and a tag or a serial in part.
func (c *Client) Search(ctx context.Context, q Query) ([]uuid.UUID, int64, error) {
	body := map[string]any{
		"query":            query(q),
		"from":             q.Offset,
		"size":             q.Size,
		"sort":             []any{"_score", map[string]any{"tag": "asc"}},
		"track_total_hits": true,
		"_source":          false,
	}
	var v struct {
		Hits struct {
			Total struct {
				Value int64 `json:"value"`
			} `json:"total"`
			Hits []struct {
				Id string `json:"_id"`
			} `json:"hits"`
		} `json:"hits"`
	}
	if err := c.call(ctx, "POST", "/"+c.Alias()+"/_search", body, &v); err != nil {
		return nil, 0, err
	}
	out := make([]uuid.UUID, 0, len(v.Hits.Hits))
	for _, h := range v.Hits.Hits {
		k, err := uuid.Parse(h.Id)
		if err != nil {
			continue
		}
		out = append(out, k)
	}
	return out, v.Hits.Total.Value, nil
}

// query is q as OpenSearch's query DSL: the tenant always, what the request
// narrows by, and the ways what was typed can match, of which one has to.
func query(q Query) map[string]any {
	text := search.Fold(q.Q)
	term := func(field string, v any, boost float64) map[string]any {
		t := map[string]any{"value": v}
		if boost > 0 {
			t["boost"] = boost
		}
		return map[string]any{"term": map[string]any{field: t}}
	}
	match := func(field string, boost float64) map[string]any {
		return map[string]any{"match": map[string]any{field: map[string]any{"query": text, "boost": boost}}}
	}

	// A word typed is every piece nori makes of it -- 회의실의자 is 회의실
	// and 의자, and E2E-ABC12 is e, 2, abc and 12, none of which alone is
	// what was meant -- and of the words typed, any will do.
	each := []any{}
	for _, w := range strings.Fields(text) {
		each = append(each, map[string]any{"multi_match": map[string]any{"query": w, "fields": fields, "type": "cross_fields", "operator": "and"}})
	}

	should := []any{
		term("tag", text, 10),
		term("serial", text, 10),
		// Every word, wherever it is: counted once more than some of them.
		map[string]any{"multi_match": map[string]any{"query": text, "fields": fields, "type": "cross_fields", "operator": "and", "boost": 2}},
		map[string]any{"bool": map[string]any{"should": each, "minimum_should_match": 1}},
		// The start of a word, as it is typed.
		match("name.prefix", 1),
		match("model.prefix", 2),
		match("tag.prefix", 3),
		// A letter wrong in a name: nori cannot split what is misspelled
		// into words, so the name as plain words takes it.
		map[string]any{"match": map[string]any{"name.plain": map[string]any{"query": text, "fuzziness": "AUTO", "operator": "and", "boost": 0.5}}},
		// A tag or a serial in part, from three letters.
		map[string]any{"match_phrase": map[string]any{"tag.part": map[string]any{"query": text, "boost": 2}}},
		map[string]any{"match_phrase": map[string]any{"serial.part": map[string]any{"query": text, "boost": 3}}},
	}

	filter := []any{term("tenant", q.Tenant.String(), 0)}
	if q.Kind != "" {
		filter = append(filter, term("kind", q.Kind, 0))
	}
	if q.Status != "" {
		filter = append(filter, term("status", q.Status, 0))
	}
	if q.Type != uuid.Nil() {
		filter = append(filter, term("type_id", q.Type.String(), 0))
	}
	if q.Custodian != uuid.Nil() {
		filter = append(filter, term("custodian_id", q.Custodian.String(), 0))
	}
	if q.Within != nil {
		ids := make([]string, len(q.Within))
		for i, k := range q.Within {
			ids[i] = k.String()
		}
		filter = append(filter, map[string]any{"ids": map[string]any{"values": ids}})
	}

	return map[string]any{"bool": map[string]any{
		"filter":               filter,
		"should":               should,
		"minimum_should_match": 1,
	}}
}
