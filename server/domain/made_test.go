package domain_test

import (
	"testing"

	"github.com/lesomnus/payday/pdid"

	app "github.com/lesomnus/rove"
	"github.com/lesomnus/rove/server/pd"
)

// TestWhatTheCallerNamesKeepsItsName: a type, a model and a bookable are made
// as their caller asked, under the identifier it named among the rest. One the
// caller named nothing is named by the domain layer, before the operation's
// event is recorded, in the domain of what it is.
func TestWhatTheCallerNamesKeepsItsName(t *testing.T) {
	e := newEnv(t)
	x := e.x
	room := e.space("회의실", nil)

	ty := pdid.New(pd.AssetTypeDomain)
	v, err := e.app().AssetType().Add(e.owner, app.AssetTypeAddRequest_builder{Id: ty.Bytes(), Name: "노트북"}.Build())
	x.NoError(err)
	x.Equal(ty.Bytes(), v.GetId())

	m := pdid.New(pd.ItemModelDomain)
	w, err := e.app().ItemModel().Add(e.owner, app.ItemModelAddRequest_builder{Id: m.Bytes(), Name: "XPS 13"}.Build())
	x.NoError(err)
	x.Equal(m.Bytes(), w.GetId())

	b := pdid.New(pd.BookableDomain)
	bk := e.bookable(room, func(r *app.BookableAddRequest) { r.SetId(b.Bytes()) })
	x.Equal(b.Bytes(), bk.GetId())

	for d, id := range map[pdid.Domain][]byte{
		pd.AssetTypeDomain: func() []byte {
			v, err := e.app().AssetType().Add(e.owner, app.AssetTypeAddRequest_builder{Name: "모니터"}.Build())
			x.NoError(err)
			return v.GetId()
		}(),
		pd.ItemModelDomain: func() []byte {
			v, err := e.app().ItemModel().Add(e.owner, app.ItemModelAddRequest_builder{Name: "U2723"}.Build())
			x.NoError(err)
			return v.GetId()
		}(),
		pd.BookableDomain: e.bookable(e.space("강당", nil), nil).GetId(),
	} {
		v, err := pdid.From(id)
		x.NoError(err)
		x.Equal(d, v.Domain())
	}
}
