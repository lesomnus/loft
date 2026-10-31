package opensearch

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"
	"uuid"

	"github.com/lesomnus/loft/internal/ent"
	"github.com/lesomnus/loft/internal/ent/asset"
	"github.com/lesomnus/loft/internal/ent/itemmodel"
	"github.com/lesomnus/loft/internal/ent/party"
	"github.com/lesomnus/loft/internal/search"
)

// Doc is what the index of assets holds of one: what a search matches, and
// what a search is narrowed by. It is what `AssetService.Search` looks at in the
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

// Op is one write of a bulk request: a document put into an index, or taken
// out of it when Doc is nil.
type Op struct {
	Index string
	Id    string
	// Version is the document's ([DocOf]); zero writes it whatever the index
	// holds.
	Version int64
	// Doc is a [Doc], a [PartyDoc] or a [ModelDoc], as Index holds them.
	Doc any
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
		if o.Index == "" {
			return 0, errors.New("opensearch: a write names no index")
		}
		meta := map[string]any{"_index": o.Index, "_id": o.Id}
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

// PartyDoc is what the index of parties holds of one person, team,
// organization or vendor: its name and code, and the name of the team or
// organization it is in, which a search for a team finds its people by.
//
// Not its email nor its phone: nothing searches by them, and a copy holds no
// more of a person than it is asked for.
type PartyDoc struct {
	Tenant string `json:"tenant"`
	Kind   string `json:"kind,omitempty"`

	Name string `json:"name,omitempty"`
	Code string `json:"code,omitempty"`
	In   string `json:"in,omitempty"`

	InId string `json:"in_id,omitempty"`
}

// PartyDocOf is the document of a party and of what it is in (nil for
// nothing), and its version: the newer change of the two.
func PartyDocOf(p *ent.Party, in *ent.Party) (PartyDoc, int64) {
	d := PartyDoc{
		Tenant: p.TenantId.String(),
		Kind:   p.Kind,
		Name:   search.Fold(p.Name),
		Code:   search.Fold(p.Code),
	}
	v := p.DateUpdated
	if in != nil {
		d.In, d.InId = search.Fold(in.Name), in.Id.String()
		if in.DateUpdated.After(v) {
			v = in.DateUpdated
		}
	}
	return d, v.UnixMicro()
}

// ModelDoc is what the index of models holds of one: its name, who makes it,
// its code, and what it is a model of.
type ModelDoc struct {
	Tenant string `json:"tenant"`

	Name  string `json:"name,omitempty"`
	Maker string `json:"maker,omitempty"`
	Code  string `json:"code,omitempty"`
	Type  string `json:"type,omitempty"`

	TypeId string `json:"type_id,omitempty"`
}

// ModelDocOf is the document of a model read with its type (`WithType`), and
// its version: the newer change of the two.
func ModelDocOf(m *ent.ItemModel) (ModelDoc, int64) {
	d := ModelDoc{
		Tenant: m.TenantId.String(),
		Name:   search.Fold(m.Name),
		Maker:  search.Fold(m.Maker),
		Code:   search.Fold(m.Code),
	}
	v := m.DateUpdated
	if t := m.Edges.Type; t != nil {
		d.Type, d.TypeId = search.Fold(t.Name), t.Id.String()
		if t.DateUpdated.After(v) {
			v = t.DateUpdated
		}
	}
	return d, v.UnixMicro()
}

// How each [Index] reads its documents from the database: those of some
// keys, and a page of everything.

func assetsMade(rows []*ent.Asset) []made {
	out := make([]made, 0, len(rows))
	for _, a := range rows {
		d, v := DocOf(a)
		out = append(out, made{key: a.Id, doc: d, version: v, erased: a.DateErased})
	}
	return out
}

func readAssets(ctx context.Context, db *ent.Client, keys []uuid.UUID) ([]made, error) {
	rows, err := db.Asset.Query().Where(asset.IdIn(keys...)).WithType().WithModel().WithCustodian().All(ctx)
	return assetsMade(rows), err
}

func pageAssets(ctx context.Context, db *ent.Client, after *uuid.UUID, n int) ([]made, error) {
	q := db.Asset.Query().Where(asset.DateErasedIsNil()).WithType().WithModel().WithCustodian().Order(ent.Asc(asset.FieldId)).Limit(n)
	if after != nil {
		q.Where(asset.IdGT(*after))
	}
	rows, err := q.All(ctx)
	return assetsMade(rows), err
}

// partiesMade reads what each party is in -- a field and not an edge -- in
// one query, and makes the documents.
func partiesMade(ctx context.Context, db *ent.Client, rows []*ent.Party) ([]made, error) {
	ups := []uuid.UUID{}
	for _, p := range rows {
		if p.ParentId != nil && *p.ParentId != uuid.Nil() {
			ups = append(ups, *p.ParentId)
		}
	}
	in := map[uuid.UUID]*ent.Party{}
	if len(ups) > 0 {
		vs, err := db.Party.Query().Where(party.IdIn(ups...), party.DateErasedIsNil()).All(ctx)
		if err != nil {
			return nil, err
		}
		for _, v := range vs {
			in[v.Id] = v
		}
	}
	out := make([]made, 0, len(rows))
	for _, p := range rows {
		var up *ent.Party
		if p.ParentId != nil {
			up = in[*p.ParentId]
		}
		d, v := PartyDocOf(p, up)
		out = append(out, made{key: p.Id, doc: d, version: v, erased: p.DateErased})
	}
	return out, nil
}

func readParties(ctx context.Context, db *ent.Client, keys []uuid.UUID) ([]made, error) {
	rows, err := db.Party.Query().Where(party.IdIn(keys...)).All(ctx)
	if err != nil {
		return nil, err
	}
	return partiesMade(ctx, db, rows)
}

func pageParties(ctx context.Context, db *ent.Client, after *uuid.UUID, n int) ([]made, error) {
	q := db.Party.Query().Where(party.DateErasedIsNil()).Order(ent.Asc(party.FieldId)).Limit(n)
	if after != nil {
		q.Where(party.IdGT(*after))
	}
	rows, err := q.All(ctx)
	if err != nil {
		return nil, err
	}
	return partiesMade(ctx, db, rows)
}

func modelsMade(rows []*ent.ItemModel) []made {
	out := make([]made, 0, len(rows))
	for _, m := range rows {
		d, v := ModelDocOf(m)
		out = append(out, made{key: m.Id, doc: d, version: v, erased: m.DateErased})
	}
	return out
}

func readModels(ctx context.Context, db *ent.Client, keys []uuid.UUID) ([]made, error) {
	rows, err := db.ItemModel.Query().Where(itemmodel.IdIn(keys...)).WithType().All(ctx)
	return modelsMade(rows), err
}

func pageModels(ctx context.Context, db *ent.Client, after *uuid.UUID, n int) ([]made, error) {
	q := db.ItemModel.Query().Where(itemmodel.DateErasedIsNil()).WithType().Order(ent.Asc(itemmodel.FieldId)).Limit(n)
	if after != nil {
		q.Where(itemmodel.IdGT(*after))
	}
	rows, err := q.All(ctx)
	return modelsMade(rows), err
}
