package opensearch

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"time"
	"uuid"

	"github.com/lesomnus/rove/internal/ent"
	"github.com/lesomnus/rove/internal/search"
)

// Doc is what the index holds of one asset: what a search matches, and what a
// search is narrowed by. It is what `AssetService.Search` looks at in the
// database (design 9.5) and nothing more -- the name, the tag, the serial, and
// through the asset the name of its type, the name and maker of its model and
// the name of who has it.
//
// The text is folded as the database's search folds it ([search.Fold]): a
// name written in full-width letters is found by the letters typed. The
// documents are searched and never shown, so what is folded away is not
// lost: the rows are read from the database.
type Doc struct {
	Tenant string `json:"tenant"`
	Kind   string `json:"kind,omitempty"`
	Status string `json:"status,omitempty"`

	Name   string `json:"name,omitempty"`
	Tag    string `json:"tag,omitempty"`
	Serial string `json:"serial,omitempty"`

	Type      string `json:"type,omitempty"`
	Model     string `json:"model,omitempty"`
	Maker     string `json:"maker,omitempty"`
	Custodian string `json:"custodian,omitempty"`

	TypeId      string `json:"type_id,omitempty"`
	ModelId     string `json:"model_id,omitempty"`
	CustodianId string `json:"custodian_id,omitempty"`
	ParentId    string `json:"parent_id,omitempty"`
}

// DocOf is the document of an asset read with its type, model and custodian
// (`WithType`, `WithModel`, `WithCustodian`), and its version: the newest
// change of any of the four rows it is made of, in microseconds.
//
// The version is what keeps an older document from replacing a newer one. A
// document is made from what a read saw, and two reads of one asset -- the
// loop and a reindex, or the loop in two processes for a moment around a
// failover -- may write in the other order from the one they read in. Each
// write carries the version and the index keeps the newer, so whichever comes
// last, what stays is what the later read saw.
func DocOf(a *ent.Asset) (Doc, int64) {
	d := Doc{
		Tenant: a.TenantId.String(),
		Kind:   a.Kind,
		Status: a.Status,
		Name:   search.Fold(a.Name),
		Tag:    search.Fold(a.Tag),
		Serial: search.Fold(a.Serial),
	}
	v := a.DateUpdated
	newer := func(t time.Time) {
		if t.After(v) {
			v = t
		}
	}
	if t := a.Edges.Type; t != nil {
		d.Type, d.TypeId = search.Fold(t.Name), t.Id.String()
		newer(t.DateUpdated)
	}
	if m := a.Edges.Model; m != nil {
		d.Model, d.Maker, d.ModelId = search.Fold(m.Name), search.Fold(m.Maker), m.Id.String()
		newer(m.DateUpdated)
	}
	if p := a.Edges.Custodian; p != nil {
		d.Custodian, d.CustodianId = search.Fold(p.Name), p.Id.String()
		newer(p.DateUpdated)
	}
	if a.ParentId != nil && *a.ParentId != uuid.Nil() {
		d.ParentId = a.ParentId.String()
	}
	return d, v.UnixMicro()
}

// Op is one write of a bulk request: an asset's document put into an index,
// or taken out of it when Doc is nil.
type Op struct {
	// Index is where; empty is the alias.
	Index string
	Id    string
	// Version is the document's ([DocOf]); zero writes it whatever the index
	// holds.
	Version int64
	Doc     *Doc
}

// Bulk writes ops in one request.
//
// It answers how many the index kept the version it had of: a document made
// from a read older than the one already there, which is not a failure (see
// [DocOf]). Taking out a document that is not there is not one either.
func (c *Client) Bulk(ctx context.Context, ops []Op) (stale int, err error) {
	if len(ops) == 0 {
		return 0, nil
	}

	var b bytes.Buffer
	e := json.NewEncoder(&b)
	e.SetEscapeHTML(false)
	for _, o := range ops {
		meta := map[string]any{"_index": o.Index, "_id": o.Id}
		if o.Index == "" {
			meta["_index"] = c.Alias()
		}
		if o.Version > 0 {
			meta["version"] = o.Version
			meta["version_type"] = "external_gte"
		}
		action := "index"
		if o.Doc == nil {
			action = "delete"
		}
		if err := e.Encode(map[string]any{action: meta}); err != nil {
			return 0, err
		}
		if o.Doc != nil {
			if err := e.Encode(o.Doc); err != nil {
				return 0, err
			}
		}
	}

	var v struct {
		Errors bool `json:"errors"`
		Items  []map[string]struct {
			Id     string `json:"_id"`
			Status int    `json:"status"`
			Error  *struct {
				Type   string `json:"type"`
				Reason string `json:"reason"`
			} `json:"error"`
		} `json:"items"`
	}
	if err := c.do(ctx, "POST", "/_bulk", b.Bytes(), "application/x-ndjson", &v); err != nil {
		return 0, err
	}
	if !v.Errors {
		return 0, nil
	}

	failed, first := 0, ""
	for _, item := range v.Items {
		for action, r := range item {
			switch {
			case r.Error == nil:
			case r.Status == 409 && r.Error.Type == "version_conflict_engine_exception":
				stale++
			case action == "delete" && r.Status == 404:
			default:
				failed++
				if first == "" {
					first = fmt.Sprintf("%s %s: %d %s: %s", action, r.Id, r.Status, r.Error.Type, r.Error.Reason)
				}
			}
		}
	}
	if failed > 0 {
		return stale, fmt.Errorf("opensearch: %d of %d writes refused, the first %s", failed, len(ops), first)
	}
	return stale, nil
}
