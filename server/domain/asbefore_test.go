package domain

import (
	"context"
	"fmt"

	"github.com/lesomnus/payday/past"
	"github.com/lesomnus/payday/pdid"
	"google.golang.org/protobuf/proto"

	app "github.com/lesomnus/loft"
	"github.com/lesomnus/loft/server/pd"
)

// AddedAsBefore registers an asset as Add did before an attribute's key was
// held to the rule ([attributeKeys]): the row, its event and the facts it
// starts with, written as Add writes them -- through payday's past, and the
// stack beneath this layer -- with none of this layer's checks. It is for the
// tests outside the package, of what such an Add left behind; `s` is the
// stack a caller reaches, whose outside is this layer.
func AddedAsBefore(ctx context.Context, s app.Server, req *app.AssetAddRequest) (*app.Asset, error) {
	d, ok := s.(Domain)
	if !ok {
		return nil, fmt.Errorf("domain: %T is not this layer", s)
	}

	var out *app.Asset
	err := d.tx(ctx, func(t *Tx) error {
		id := pdid.New(pd.AssetDomain)
		row := proto.Clone(req).(*app.AssetAddRequest)
		row.SetTenant(t.tenantRef())
		row.SetId(id.Bytes())
		row.SetKind(or(row.GetKind(), "item"))
		row.SetStatus(or(row.GetStatus(), "active"))
		row.SetCondition(or(row.GetCondition(), "good"))
		if err := t.begin(nil, "asset.add", id, t.now, "", app.AssetAdded_builder{
			Asset: id.Bytes(),
			Tag:   row.GetTag(),
			Name:  row.GetName(),
			Kind:  row.GetKind(),
		}.Build()); err != nil {
			return err
		}
		a, err := t.next.Asset().Add(t.ctx, row)
		if err != nil {
			return err
		}
		if err := t.op.Set(t.ctx, past.Fact{Subject: id, From: t.now, Values: past.Changes(nil, assetFacts(a))}); err != nil {
			return err
		}
		out, _, err = t.get(assetRef(id.Uuid()), "id")
		return err
	})
	return out, err
}
