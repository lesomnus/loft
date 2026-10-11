package cmd_test

import (
	"context"
	"testing"
	"time"

	"github.com/lesomnus/z"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/protobuf/types/known/timestamppb"

	app "github.com/lesomnus/rove"
)

// The screens that follow other people's changes as they happen -- an asset,
// the reservation calendar, a count in progress -- each watch one row, since a
// watch names the rows it follows and most of what those screens draw is rows
// nobody has named yet: a reservation just made, a finding just scanned. So
// whatever changes what a screen draws writes the row that stands for it
// (design 9.6), and that is what is pinned here. A screen whose row stopped
// being written would go on drawing the past, with nothing to say so.
func TestALivePageHearsOfChanges(t *testing.T) {
	w := newWorld(t)
	x := w.x
	assets := app.NewAssetServiceClient(w.conn)
	// Who watches is a member, which is who most screens are open for.
	kim := w.signIn(w.holder("kim", "member"))

	room, err := assets.Add(w.owner, app.AssetAddRequest_builder{Name: "회의실", Kind: "space"}.Build())
	x.NoError(err)
	store, err := assets.Add(w.owner, app.AssetAddRequest_builder{Name: "창고", Kind: "space"}.Build())
	x.NoError(err)
	in := func(name, tag string, at *app.Asset) *app.Asset {
		v, err := assets.Add(w.owner, app.AssetAddRequest_builder{Name: name, Tag: tag, Kind: "item", To: app.AssetRef_builder{Id: at.GetId()}.Build()}.Build())
		x.NoError(err)
		return v
	}
	laptop := in("노트북", "NB-1", room)
	in("마우스", "MS-1", room)

	t.Run("an asset, when it moves", func(t *testing.T) {
		x := require.New(t)
		s := opened(t, kim, func(ctx context.Context) (grpc.ServerStreamingClient[app.AssetWatchResponse], error) {
			return assets.Watch(ctx, app.AssetWatchRequest_builder{Filters: []*app.AssetFilter{
				app.AssetFilter_builder{Ref: app.AssetRef_builder{Id: laptop.GetId()}.Build()}.Build(),
			}}.Build())
		})

		_, err := assets.Move(w.owner, app.AssetMoveRequest_builder{
			Ref: app.AssetRef_builder{Id: laptop.GetId()}.Build(),
			To:  app.AssetRef_builder{Id: store.GetId()}.Build(),
		}.Build())
		x.NoError(err)
		v, err := s.Recv()
		x.NoError(err)
		x.Len(v.GetItems(), 1)
		x.Equal(store.GetId(), v.GetItems()[0].GetValue().GetParentId())
	})

	t.Run("a calendar, when a reservation is made on what it shows", func(t *testing.T) {
		x := require.New(t)
		b, err := app.NewBookableServiceClient(w.conn).Add(w.owner, app.BookableAddRequest_builder{Asset: app.AssetRef_builder{Id: room.GetId()}.Build()}.Build())
		x.NoError(err)
		s := opened(t, kim, func(ctx context.Context) (grpc.ServerStreamingClient[app.BookableWatchResponse], error) {
			return app.NewBookableServiceClient(w.conn).Watch(ctx, app.BookableWatchRequest_builder{Filters: []*app.BookableFilter{
				app.BookableFilter_builder{Ref: app.BookableRef_builder{Id: b.GetId()}.Build()}.Build(),
			}}.Build())
		})

		lee, err := app.NewPartyServiceClient(w.conn).Add(w.owner, app.PartyAddRequest_builder{Name: "이", Kind: "person"}.Build())
		x.NoError(err)
		at := time.Now().AddDate(0, 0, 1).Truncate(time.Hour)
		_, err = app.NewReservationServiceClient(w.conn).Add(w.owner, app.ReservationAddRequest_builder{
			Name:     "회의",
			Party:    app.PartyRef_builder{Id: lee.GetId()}.Build(),
			BeginsAt: timestamppb.New(at),
			EndsAt:   timestamppb.New(at.Add(time.Hour)),
			Items:    []*app.ReservationItemSpec{app.ReservationItemSpec_builder{Resource: app.AssetRef_builder{Id: room.GetId()}.Build()}.Build()},
		}.Build())
		x.NoError(err)
		v, err := s.Recv()
		x.NoError(err)
		x.Len(v.GetItems(), 1)
		x.Greater(v.GetItems()[0].GetValue().GetScheduleVersion(), b.GetScheduleVersion())
	})

	t.Run("a count, when its findings change", func(t *testing.T) {
		x := require.New(t)
		counts := app.NewInventoryCountServiceClient(w.conn)
		c, err := counts.Add(w.owner, app.InventoryCountAddRequest_builder{Scope: app.AssetRef_builder{Id: room.GetId()}.Build()}.Build())
		x.NoError(err)
		cref := app.InventoryCountRef_builder{Id: c.GetId()}.Build()
		s := opened(t, kim, func(ctx context.Context) (grpc.ServerStreamingClient[app.InventoryCountWatchResponse], error) {
			return counts.Watch(ctx, app.InventoryCountWatchRequest_builder{Filters: []*app.InventoryCountFilter{
				app.InventoryCountFilter_builder{Ref: cref}.Build(),
			}}.Build())
		})
		heard := func() uint64 {
			v, err := s.Recv()
			x.NoError(err)
			x.Len(v.GetItems(), 1)
			return v.GetItems()[0].GetValue().GetFindingsVersion()
		}

		// A scan is a finding nobody could have named before it was made.
		_, err = counts.Scan(w.owner, app.InventoryCountScanRequest_builder{Ref: cref, Code: "NB-1"}.Build())
		x.NoError(err)
		x.EqualValues(1, heard())

		// What was expected and not seen is findings too, and so is settling one.
		_, err = counts.Reconcile(w.owner, app.InventoryCountReconcileRequest_builder{Ref: cref}.Build())
		x.NoError(err)
		x.EqualValues(2, heard(), "the mouse was not seen")
		fs, err := app.NewCountFindingServiceClient(w.conn).List(w.owner, app.CountFindingListRequest_builder{
			Filters: []*app.CountFindingFilter{app.CountFindingFilter_builder{Count: cref, Kind: z.Ptr("missing")}.Build()},
		}.Build())
		x.NoError(err)
		x.Len(fs.GetItems(), 1)
		_, err = counts.Resolve(w.owner, app.InventoryCountResolveRequest_builder{
			Ref:        app.CountFindingRef_builder{Id: fs.GetItems()[0].GetId()}.Build(),
			Resolution: "ignored",
		}.Build())
		x.NoError(err)
		x.EqualValues(3, heard())
	})
}

// opened is a watch once it is open: past its first message, which is what is
// there now, so that a write after it is one the stream was there for.
func opened[T any](t *testing.T, as context.Context, open func(context.Context) (grpc.ServerStreamingClient[T], error)) grpc.ServerStreamingClient[T] {
	t.Helper()
	ctx, cancel := context.WithTimeout(as, 10*time.Second)
	t.Cleanup(cancel)
	s, err := open(ctx)
	require.NoError(t, err)
	_, err = s.Recv()
	require.NoError(t, err)
	return s
}
