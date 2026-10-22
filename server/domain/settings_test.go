package domain_test

import (
	"strings"
	"testing"
	"time"

	"github.com/lesomnus/z"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/protobuf/types/known/timestamppb"

	app "github.com/lesomnus/rove"
)

// TestAWorkspaceSaysWhereItIs (#144): the zone its dates are told in, the
// currency a new amount is priced in, and how long a room waits for whoever
// booked it are its own, its owner's to say -- and the deployment's while it
// says nothing.
func TestAWorkspaceSaysWhereItIs(t *testing.T) {
	e := newEnv(t)
	x := e.x
	ws := app.TenantRef_builder{Id: e.tenant.Bytes()}.Build()
	configure := func(v *app.TenantConfigureRequest) (*app.Tenant, error) {
		v.SetRef(ws)
		return e.app().Tenant().Configure(e.owner, v)
	}

	_, err := configure(app.TenantConfigureRequest_builder{Timezone: z.Ptr("Mars/Olympus")}.Build())
	x.Equal(codes.InvalidArgument, codeOf(err), "a zone nothing knows")
	_, err = configure(app.TenantConfigureRequest_builder{Currency: z.Ptr("won")}.Build())
	x.Equal(codes.InvalidArgument, codeOf(err), "a currency that is not ISO 4217's")
	_, err = configure(app.TenantConfigureRequest_builder{NoShowMinutes: z.Ptr(int32(24*60 + 1))}.Build())
	x.Equal(codes.InvalidArgument, codeOf(err), "a room that waits more than a day")
	_, err = e.app().Tenant().Patch(e.owner, app.TenantPatchRequest_builder{Ref: ws, Currency: z.Ptr("USD")}.Build())
	x.Equal(codes.InvalidArgument, codeOf(err), "said past Configure, and its event")

	v, err := configure(app.TenantConfigureRequest_builder{
		Timezone:      z.Ptr("America/New_York"),
		Currency:      z.Ptr("USD"),
		NoShowMinutes: z.Ptr(int32(5)),
	}.Build())
	x.NoError(err)
	x.Equal("America/New_York", v.GetTimezone())
	x.Equal("USD", v.GetCurrency())
	x.EqualValues(5, v.GetNoShowMinutes())

	t.Run("a file's dates are told in its zone", func(t *testing.T) {
		x := require.New(t)

		// 03:30 UTC on the 2nd is the 1st in New York and the 2nd in Seoul.
		at := time.Date(2026, 3, 2, 3, 30, 0, 0, time.UTC)
		_, err := e.app().Asset().Add(e.owner, app.AssetAddRequest_builder{
			Name: "모니터", Tag: "MN-1", Kind: "item", Since: e.ago(time.Hour), AcquiredAt: timestamppb.New(at),
		}.Build())
		x.NoError(err)

		f, err := e.app().Asset().Export(e.owner, &app.AssetExportRequest{})
		x.NoError(err)
		row := ""
		for _, l := range strings.Split(string(f.GetData()), "\n") {
			if strings.HasPrefix(l, "MN-1,") {
				row = l
			}
		}
		x.Contains(row, ",2026-03-01,", "the date was not New York's")
	})

	purchase := func(name string) (*app.Purchase, error) {
		m, err := e.app().ItemModel().Add(e.owner, app.ItemModelAddRequest_builder{Name: name + " 모델"}.Build())
		if err != nil {
			return nil, err
		}
		return e.app().Purchase().Add(e.owner, app.PurchaseAddRequest_builder{
			Name:  name,
			Lines: []*app.PurchaseLineSpec{app.PurchaseLineSpec_builder{Model: ref[app.ItemModelRef](m.GetId()), Quantity: 1, UnitCost: 100}.Build()},
		}.Build())
	}

	t.Run("a new amount is priced in its currency", func(t *testing.T) {
		x := require.New(t)

		p, err := purchase("모니터 구매")
		x.NoError(err)
		x.Equal("USD", p.GetCurrency())

		_, err = e.app().Purchase().Add(e.owner, app.PurchaseAddRequest_builder{
			Name:     "달러로",
			Currency: "dollar",
			Lines:    []*app.PurchaseLineSpec{app.PurchaseLineSpec_builder{Desc: "케이블", Quantity: 1, UnitCost: 100}.Build()},
		}.Build())
		x.Equal(codes.InvalidArgument, codeOf(err), "a currency an amount cannot be told in")
	})

	t.Run("what work cost is totalled by the currency it was priced in", func(t *testing.T) {
		x := require.New(t)
		desk := e.item("책상", "DK-1", nil, time.Hour)
		for _, v := range []struct {
			cost     int64
			currency string
		}{{1250, ""}, {250, "USD"}, {30000, "KRW"}} {
			_, err := e.app().WorkOrder().Add(e.owner, app.WorkOrderAddRequest_builder{
				Asset: ref[app.AssetRef](desk.GetId()), Name: "다리 수리", Cost: v.cost, Currency: v.currency,
			}.Build())
			x.NoError(err)
		}

		v, err := e.app().Asset().Report(e.owner, app.AssetReportRequest_builder{Kind: "work"}.Build())
		x.NoError(err)
		got := map[string]string{}
		for _, r := range v.GetRows() {
			if r.GetGroup() == "cost" {
				got[r.GetKey()] = r.GetDetail()["amount"]
			}
		}
		x.Equal(map[string]string{"USD": "15.00 USD", "KRW": "30,000 KRW"}, got)
	})

	t.Run("a cost moves to another currency only said again in it", func(t *testing.T) {
		x := require.New(t)
		desk := e.item("의자", "CH-1", nil, time.Hour)
		w, err := e.app().WorkOrder().Add(e.owner, app.WorkOrderAddRequest_builder{
			Asset: ref[app.AssetRef](desk.GetId()), Name: "바퀴 교체", Cost: 30000, Currency: "KRW",
		}.Build())
		x.NoError(err)
		update := func(cost int64, currency string) (*app.WorkOrder, error) {
			return e.app().WorkOrder().Update(e.owner, app.WorkOrderUpdateRequest_builder{
				Ref: ref[app.WorkOrderRef](w.GetId()), Cost: cost, Currency: currency,
			}.Build())
		}

		_, err = update(0, "USD")
		x.Equal(codes.InvalidArgument, codeOf(err), "30,000 won read as 300 dollars")
		v, err := update(2250, "USD")
		x.NoError(err)
		x.EqualValues(2250, v.GetCost())
		x.Equal("USD", v.GetCurrency())
		v, err = update(0, "USD")
		x.NoError(err, "the currency it is in already")
		x.EqualValues(2250, v.GetCost())

		// And one that cost nothing yet has nothing to be read again.
		free, err := e.app().WorkOrder().Add(e.owner, app.WorkOrderAddRequest_builder{
			Asset: ref[app.AssetRef](desk.GetId()), Name: "점검", Currency: "KRW",
		}.Build())
		x.NoError(err)
		v, err = e.app().WorkOrder().Update(e.owner, app.WorkOrderUpdateRequest_builder{
			Ref: ref[app.WorkOrderRef](free.GetId()), Currency: "USD",
		}.Build())
		x.NoError(err)
		x.Equal("USD", v.GetCurrency())
	})

	t.Run("and Me answers where it is", func(t *testing.T) {
		x := require.New(t)

		v, err := e.app().Party().Me(e.owner, &app.PartyMeRequest{})
		x.NoError(err)
		x.Equal("America/New_York", v.GetTimezone())
		x.Equal("USD", v.GetCurrency())
		x.EqualValues(365, v.GetAliasHoldDays(), "the deployment's, a year unset")
	})

	t.Run("a room waits as long as it says", func(t *testing.T) {
		x := require.New(t)
		room := e.space("회의실", nil)
		e.bookable(room, nil)
		kim, _ := e.person("member")

		// The deployment waits fifteen minutes; this workspace, five.
		start := e.now.Add(time.Hour).Truncate(time.Minute)
		r, err := e.reserve(kim, room, start, start.Add(time.Hour), nil)
		x.NoError(err)
		e.now = start.Add(7 * time.Minute)
		e.sweep()
		got, err := e.app().Reservation().Get(kim, app.ReservationGetRequest_builder{Ref: ref[app.ReservationRef](r.GetId())}.Build())
		x.NoError(err)
		x.Equal("no_show", got.GetStatus(), "freed on the deployment's clock")

		// And holds nobody to it once it says so.
		_, err = configure(app.TenantConfigureRequest_builder{NoShowMinutes: z.Ptr(int32(-1))}.Build())
		x.NoError(err)
		start = e.now.Add(time.Hour).Truncate(time.Minute)
		r, err = e.reserve(kim, room, start, start.Add(time.Hour), nil)
		x.NoError(err)
		e.now = start.Add(30 * time.Minute)
		e.sweep()
		got, err = e.app().Reservation().Get(kim, app.ReservationGetRequest_builder{Ref: ref[app.ReservationRef](r.GetId())}.Build())
		x.NoError(err)
		x.NotEqual("no_show", got.GetStatus(), "a room it does not hold anybody to was freed")
	})

	t.Run("and empty is the deployment's again", func(t *testing.T) {
		x := require.New(t)

		v, err := configure(app.TenantConfigureRequest_builder{Timezone: z.Ptr(""), Currency: z.Ptr("")}.Build())
		x.NoError(err)
		x.Empty(v.GetTimezone())
		p, err := purchase("다시")
		x.NoError(err)
		x.Equal("KRW", p.GetCurrency())
		me, err := e.app().Party().Me(e.owner, &app.PartyMeRequest{})
		x.NoError(err)
		x.Equal("Asia/Seoul", me.GetTimezone())
		x.Equal("KRW", me.GetCurrency())
	})
}
