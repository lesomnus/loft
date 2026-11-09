package domain

import (
	"testing"

	"github.com/lesomnus/payday/past"
	"github.com/lesomnus/payday/pdid"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/timestamppb"

	app "github.com/lesomnus/loft"
	"github.com/lesomnus/loft/server/pd"
)

// A type's identifier and a model's, as they are written down: the domain is
// the ninth byte (pdid), 8 for a type and 9 for a model.
const (
	aType  = "0199f2c4-7b1e-8a3c-8008-0123456789ab"
	aModel = "0199f2c4-7b1e-8a3c-8009-0123456789ab"
)

// TestAFactsKeyIsItsFieldAndBack: a key is a field of an asset and the field
// the key, with the numbers the past keeps them by.
func TestAFactsKeyIsItsFieldAndBack(t *testing.T) {
	for k, want := range map[string]past.Slot{
		"name":      {Field: 5},
		"desc":      {Field: 6},
		"type":      {Field: 9},
		"model":     {Field: 10},
		"tag":       {Field: 11},
		"serial":    {Field: 12},
		"status":    {Field: 16},
		"condition": {Field: 17},
		"attr.cpu":  {Field: 22, Key: "cpu"},
		"attr.보증":   {Field: 22, Key: "보증"},
		"attr.a.b":  {Field: 22, Key: "a.b"},
		"attr.":     {Field: 22, Key: ""},
	} {
		s, err := SlotOf(k)
		require.NoError(t, err, k)
		require.Equal(t, want, s, k)

		back, ok := KeyOf(s)
		require.True(t, ok, k)
		require.Equal(t, k, back)
	}

	// Slots of no fact: the identifier, the labels, the kind, a field no
	// asset has, and a key on a field that is no map.
	for _, s := range []past.Slot{{Field: 1}, {Field: 7, Key: "x"}, {Field: 8}, {Field: 18}, {Field: 99}, {Field: 5, Key: "x"}} {
		_, ok := KeyOf(s)
		require.False(t, ok, "%+v", s)
	}
}

// TestTheFactsAnAssetStartsWithAreItsFactFields: what payday's past writes of
// an asset made -- the changes from nothing to its fact fields -- is each fact
// Add says of it, read back in the text Loft says it in ([textOf]): an edge
// by the identifier of the row it names, an attribute by its key. Nothing
// else of the row is a fact of it.
func TestTheFactsAnAssetStartsWithAreItsFactFields(t *testing.T) {
	tenant := pdid.New(pd.TenantDomain)
	whole := app.Asset_builder{
		Id:     pdid.New(pd.AssetDomain).Bytes(),
		Tenant: app.Tenant_builder{Id: tenant.Bytes()}.Build(),

		Name:       `노트북 "XPS"`,
		Desc:       "C:\\ 드라이브\n교체",
		Type:       app.AssetType_builder{Id: pdid.MustParse(aType).Bytes(), Name: "노트북"}.Build(),
		Model:      app.ItemModel_builder{Id: pdid.MustParse(aModel).Bytes(), Name: "XPS 13"}.Build(),
		Tag:        "A-00001",
		Serial:     "SN-1",
		Status:     "active",
		Condition:  "good",
		Attributes: map[string]string{"cpu": "i7", "보증": "2027-01-01", "memo": ""},

		// What is not a fact of it.
		Labels:         map[string]string{"team": "infra"},
		Kind:           "item",
		ParentId:       pdid.New(pd.AssetDomain).Bytes(),
		PlacementMode:  "located",
		Slot:           "U3",
		Custodian:      app.Party_builder{Id: pdid.New(pd.PartyDomain).Bytes()}.Build(),
		DateAcquired:   timestamppb.Now(),
		ContentVersion: 4,
		DateCreated:    timestamppb.Now(),
		DateUpdated:    timestamppb.Now(),
	}.Build()
	least := app.Asset_builder{
		Id:        pdid.New(pd.AssetDomain).Bytes(),
		Name:      "회의실",
		Tag:       "S-00001",
		Status:    "active",
		Condition: "good",
		Kind:      "space",
	}.Build()

	for _, c := range []struct {
		a *app.Asset
		// What Add writes for it.
		facts map[string]string
	}{
		{whole, map[string]string{
			"name": `노트북 "XPS"`, "desc": "C:\\ 드라이브\n교체", "type": aType, "model": aModel,
			"tag": "A-00001", "serial": "SN-1", "status": "active", "condition": "good",
			"attr.cpu": "i7", "attr.보증": "2027-01-01", "attr.memo": "",
		}},
		{least, map[string]string{"name": "회의실", "tag": "S-00001", "status": "active", "condition": "good"}},
	} {
		got := map[string]string{}
		for _, v := range past.Changes(nil, assetFacts(c.a)) {
			fd, s := v.Field, past.Slot{Field: v.Field.Number()}
			if fd.IsMap() {
				fd, s.Key = fd.MapValue(), v.Key.String()
			}
			json, err := past.Encode(fd, v.Value)
			require.NoError(t, err, "%+v", s)
			k, ok := KeyOf(s)
			require.True(t, ok, "%+v is no fact of an asset", s)
			text, err := textOf(s, json)
			require.NoError(t, err, k)
			got[k] = text
		}
		require.Equal(t, c.facts, got, c.a.GetTag())
	}
}
