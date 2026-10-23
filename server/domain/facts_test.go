package domain

import (
	"testing"

	"github.com/lesomnus/payday/past"
	"github.com/lesomnus/payday/pdid"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/known/timestamppb"

	app "github.com/lesomnus/rove"
	"github.com/lesomnus/rove/server/pd"
)

// A type's identifier and a model's, as they are written down: the domain is
// the ninth byte (pdid), 8 for a type and 9 for a model.
const (
	aType  = "0199f2c4-7b1e-8a3c-8008-0123456789ab"
	aModel = "0199f2c4-7b1e-8a3c-8009-0123456789ab"
)

// TestAFactIsWrittenAsThePastWritesItsField: a fact Rove wrote under a key is,
// as payday's past writes it, the asset's field -- by its number, which is
// what every row of it will be read by for as long as the history is kept --
// and the value's canonical JSON: the fewest escapes, Korean as itself, an
// identifier in one form whatever form the request wrote it in, and `null`
// for one cleared (payday#151).
func TestAFactIsWrittenAsThePastWritesItsField(t *testing.T) {
	for _, c := range []struct {
		key, value string
		cleared    bool

		field protoreflect.FieldNumber
		entry string
		json  string
	}{
		{key: "name", value: `Dell "XPS" 13`, field: 5, json: `"Dell \"XPS\" 13"`},
		{key: "desc", value: `C:\temp\new`, field: 6, json: `"C:\\temp\\new"`},
		{key: "desc", value: "a\tb\nc\r\x01\x1f\x7f/<", field: 6, json: `"a\tb\nc\r\u0001\u001f` + "\x7f" + `/<"`},
		{key: "desc", value: "", field: 6, json: `""`},
		{key: "desc", cleared: true, field: 6, json: `null`},
		{key: "tag", value: "회의실-3층", field: 11, json: `"회의실-3층"`},
		{key: "serial", value: "SN 0001", field: 12, json: `"SN 0001"`},
		{key: "status", value: "in_repair", field: 16, json: `"in_repair"`},
		{key: "condition", value: "good", field: 17, json: `"good"`},

		// An edge, by the identifier of the row: one canonical string.
		{key: "type", value: aType, field: 9, json: `"` + aType + `"`},
		{key: "type", value: "0199F2C4-7B1E-8A3C-8008-0123456789AB", field: 9, json: `"` + aType + `"`},
		{key: "type", value: "{" + aType + "}", field: 9, json: `"` + aType + `"`},
		{key: "type", cleared: true, field: 9, json: `null`},
		{key: "model", value: "urn:uuid:" + aModel, field: 10, json: `"` + aModel + `"`},
		{key: "model", value: "0199f2c47b1e8a3c80090123456789ab", field: 10, json: `"` + aModel + `"`},

		// An attribute is an entry of the one map, by its key as it is.
		{key: "attr.cpu", value: `Intel "vPro"`, field: 22, entry: "cpu", json: `"Intel \"vPro\""`},
		{key: "attr.보증 만료", value: "2027-01-01", field: 22, entry: "보증 만료", json: `"2027-01-01"`},
		{key: "attr.a.b", value: "x", field: 22, entry: "a.b", json: `"x"`},
		{key: "attr.memo", value: "", field: 22, entry: "memo", json: `""`},
		{key: "attr.cpu", cleared: true, field: 22, entry: "cpu", json: `null`},
		// What Add writes for an attribute whose key is empty.
		{key: "attr.", value: "x", field: 22, entry: "", json: `"x"`},
	} {
		field, entry, json, err := LegacyFact(c.key, c.value, c.cleared)
		require.NoError(t, err, "%s=%q", c.key, c.value)
		require.Equal(t, c.field, field, "%s=%q", c.key, c.value)
		require.Equal(t, c.entry, entry, "%s=%q", c.key, c.value)
		require.Equal(t, c.json, json, "%s=%q", c.key, c.value)
	}
}

// TestAFactThePastCannotWriteIsRefused: a key that is no field of an asset,
// and a value its field cannot hold, are said rather than written as
// something else -- a migration that meets one stops, naming it.
func TestAFactThePastCannotWriteIsRefused(t *testing.T) {
	for _, c := range []struct{ key, value string }{
		{"location", "x"},
		{"", "x"},
		{"Name", "x"},
		{"attr", "x"},
		{"kind", "item"},
		{"labels", "x"},
		{"type", "not an identifier"},
		{"type", ""},
		// A uuid, and not one of payday's: no row is named by it.
		{"model", "f81d4fae-7dec-11d0-a765-00a0c91e6bf6"},
		{"name", "\xff"},
		{"attr.\xff", "x"},
	} {
		_, _, _, err := LegacyFact(c.key, c.value, false)
		require.Error(t, err, "%q=%q", c.key, c.value)
	}

	// Cleared is no value, but an unknown key is unknown all the same.
	_, _, _, err := LegacyFact("location", "", true)
	require.Error(t, err)
}

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

// TestTheFactsAnAssetStartsWithAreThePastsOfIt: what payday's past writes of
// an asset made -- the changes from nothing to its fact fields -- is, field
// for field and to the byte, what a fact Add writes today is once moved: so
// rows written before the past and after it read alike, and say the same
// value in the same text. Nothing else of the row is a fact of it.
func TestTheFactsAnAssetStartsWithAreThePastsOfIt(t *testing.T) {
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
		AcquiredAt:     timestamppb.Now(),
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
		want := map[past.Slot]string{}
		for k, v := range c.facts {
			field, entry, json, err := LegacyFact(k, v, false)
			require.NoError(t, err, k)
			want[past.Slot{Field: field, Key: entry}] = json
		}

		got := map[past.Slot]string{}
		for _, v := range past.Changes(nil, assetFacts(c.a)) {
			fd, s := v.Field, past.Slot{Field: v.Field.Number()}
			if fd.IsMap() {
				fd, s.Key = fd.MapValue(), v.Key.String()
			}
			json, err := past.Encode(fd, v.Value)
			require.NoError(t, err, "%+v", s)
			got[s] = json
		}
		require.Equal(t, want, got, c.a.GetTag())
	}
}
