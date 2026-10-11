package domain_test

import (
	"context"
	"fmt"
	"slices"
	"sync"
	"testing"

	"github.com/lesomnus/payday/pdid"
	"google.golang.org/grpc/codes"
	"google.golang.org/protobuf/types/known/timestamppb"

	app "github.com/lesomnus/rove"
	"github.com/lesomnus/rove/internal/ent/event"
	"github.com/lesomnus/rove/internal/ent/placement"
	"github.com/lesomnus/rove/internal/ent/stockmovement"
	"github.com/lesomnus/rove/server/pd"
)

// What happens when people do things to one tree at once (design 3.2, D9).
//
// A change to where things are takes the tenant's one tree lock first, so
// changes land one after another and each sees the last. These ask PostgreSQL,
// where transactions really do run at once; SQLite runs one writer at a time
// and would pass them for the wrong reason.

// together runs `f` from `n` callers released at the same moment, and answers
// what each got back.
func together(n int, f func(i int) error) []error {
	errs := make([]error, n)
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := range n {
		wg.Go(func() {
			<-start
			errs[i] = f(i)
		})
	}
	close(start)
	wg.Wait()
	return errs
}

func (e *env) postgresOnly() {
	e.t.Helper()
	if e.s.Dialect != "postgres" {
		e.t.Skip("PostgreSQL only; set PDTEST_POSTGRES")
	}
}

// parentOf is where an asset is now, by its row.
func (e *env) parentOf(a *app.Asset) []byte { return e.get(a).GetParentId() }

// TestOneMoveSentManyTimesAtOnceIsMadeOnce: what a client sends again because
// it did not hear back -- the same operation, by its identifier -- is made
// once however many copies arrive together, and every copy answers as if it
// had been the one.
//
// A move sent again is a move to where the thing already is, which changes
// nothing whether or not anything remembers the first. A correction sent again
// names a row the first one superseded, and only the operation's identifier
// makes that copy a success rather than a refusal.
func TestOneMoveSentManyTimesAtOnceIsMadeOnce(t *testing.T) {
	e := newEnv(t)
	e.postgresOnly()
	x := e.x
	a := e.space("A", nil)
	b := e.space("B", nil)
	laptop := e.item("노트북", "NB-1", a, day)

	op := pdid.New(pd.EventDomain)
	errs := together(8, func(int) error {
		_, err := e.app().Asset().Move(e.owner, app.AssetMoveRequest_builder{Ref: assetRef(laptop), To: assetRef(b), Op: op.Bytes()}.Build())
		return err
	})
	for i, err := range errs {
		x.NoError(err, "copy %d", i)
	}

	ctx := context.Background()
	n, err := e.s.Ent.Event.Query().Where(event.Id(op.Uuid())).Count(ctx)
	x.NoError(err)
	x.Equal(1, n, "one operation")
	rows, err := e.s.Ent.Placement.Query().Where(placement.EventId(op.Uuid())).Count(ctx)
	x.NoError(err)
	x.Equal(2, rows, "written once: where it was until now, and where it is from now")
	x.Equal(b.GetId(), e.parentOf(laptop))

	// It went to B a day earlier than recorded, sent eight times at once.
	tl, err := e.app().Asset().Timeline(e.owner, app.AssetTimelineRequest_builder{Ref: assetRef(laptop)}.Build())
	x.NoError(err)
	var toB *app.TimelineEntry
	for _, en := range tl.GetEntries() {
		if en.GetKind() == "placement" && sameId(en.GetOtherId(), b.GetId()) {
			toB = en
		}
	}
	x.NotNil(toB)
	fix := pdid.New(pd.EventDomain)
	errs = together(8, func(int) error {
		_, err := e.app().Asset().Correct(e.owner, app.AssetCorrectRequest_builder{
			Ref: assetRef(laptop), RowId: toB.GetRowId(), ValidFrom: e.ago(day / 2), Reason: "영수증", Op: fix.Bytes(),
		}.Build())
		return err
	})
	for i, err := range errs {
		x.NoError(err, "copy %d of the correction", i)
	}
	n, err = e.s.Ent.Event.Query().Where(event.Id(fix.Uuid())).Count(ctx)
	x.NoError(err)
	x.Equal(1, n)
	x.Equal(b.GetId(), e.where(e.get(b), laptop, e.now.Add(-day/4), nil), "corrected once")
}

// TestEveryOperationSentManyTimesAtOnceIsMadeOnce: what holds for a move holds
// for every operation a client sends again -- a stock started, used, moved and
// turned into assets, a hand-over and its return, a scan, a booking. Copies of
// it arriving together make it once, and each answers what the first did.
// Most of these take no lock that copies of them would wait on; what they
// wait on is the operation's identifier (`Tx.claim`).
func TestEveryOperationSentManyTimesAtOnceIsMadeOnce(t *testing.T) {
	e := newEnv(t)
	e.postgresOnly()
	x := e.x
	store := e.space("창고", nil)
	other := e.space("3층", nil)
	room := e.space("회의실", nil)
	e.bookable(room, nil)
	laptop := e.item("노트북", "NB-1", store, day)
	_, kim := e.person("member")
	m := e.model("AA 건전지")

	// once sends eight copies of an operation together, and answers what they
	// answered, which is the same for every one.
	once := func(name string, f func(op []byte) (string, error)) string {
		t.Helper()
		op := newOp()
		got := make([]string, 8)
		errs := together(len(got), func(i int) error {
			var err error
			got[i], err = f(op)
			return err
		})
		for i, err := range errs {
			x.NoError(err, "%s: copy %d", name, i)
			x.Equal(got[0], got[i], "%s: copy %d answers as the first", name, i)
		}
		x.Equal(1, e.ops(op), "%s: one operation", name)
		return got[0]
	}
	idsOf := func(as []*app.Asset) string {
		v := ""
		for _, a := range as {
			v += string(a.GetId())
		}
		return v
	}

	s := app.Stock_builder{Id: []byte(once("stock.add", func(op []byte) (string, error) {
		v, err := e.app().Stock().Add(e.owner, app.StockAddRequest_builder{Model: ref[app.ItemModelRef](m.GetId()), Space: assetRef(store), Quantity: 20, Op: op}.Build())
		return string(v.GetId()), err
	}))}.Build()
	sref := ref[app.StockRef](s.GetId())
	once("stock.consume", func(op []byte) (string, error) {
		v, err := e.app().Stock().Consume(e.owner, app.StockChangeRequest_builder{Ref: sref, Quantity: 3, Op: op}.Build())
		return fmt.Sprint(v.GetQuantity()), err
	})
	x.EqualValues(17, e.quantity(s))
	once("stock.transfer", func(op []byte) (string, error) {
		v, err := e.app().Stock().Transfer(e.owner, app.StockTransferRequest_builder{Ref: sref, To: assetRef(other), Quantity: 2, Op: op}.Build())
		return fmt.Sprint(v.GetQuantity()), err
	})
	x.EqualValues(15, e.quantity(s))
	converted := once("stock.convert", func(op []byte) (string, error) {
		v, err := e.app().Stock().Convert(e.owner, app.StockConvertRequest_builder{Ref: sref, Quantity: 2, Op: op}.Build())
		return idsOf(v.GetAssets()), err
	})
	x.Len(converted, 2*16, "two assets")
	x.EqualValues(13, e.quantity(s))

	c := once("custody.add", func(op []byte) (string, error) {
		v, err := e.app().Custody().Add(e.owner, app.CustodyAddRequest_builder{
			Party: ref[app.PartyRef](kim.GetId()),
			Lines: []*app.CustodyLineSpec{
				app.CustodyLineSpec_builder{Asset: assetRef(laptop)}.Build(),
				app.CustodyLineSpec_builder{Stock: sref, Quantity: 1}.Build(),
			},
			Op: op,
		}.Build())
		return string(v.GetId()), err
	})
	x.EqualValues(12, e.quantity(s))
	x.True(sameId(kim.GetId(), e.get(laptop).GetCustodian().GetId()))
	once("custody.return", func(op []byte) (string, error) {
		v, err := e.app().Custody().Return(e.owner, app.CustodyReturnRequest_builder{Ref: ref[app.CustodyRef]([]byte(c)), Op: op}.Build())
		return v.GetStatus(), err
	})
	x.EqualValues(13, e.quantity(s))
	x.False(e.get(laptop).HasCustodian())

	count, err := e.app().InventoryCount().Add(e.owner, app.InventoryCountAddRequest_builder{Scope: assetRef(store)}.Build())
	x.NoError(err)
	for _, code := range []string{"NB-1", "no-such-thing"} {
		once("count.scan "+code, func(op []byte) (string, error) {
			v, err := e.app().InventoryCount().Scan(e.owner, app.InventoryCountScanRequest_builder{Ref: ref[app.InventoryCountRef](count.GetId()), Code: code, Op: op}.Build())
			return string(v.GetId()), err
		})
	}
	fs, err := e.app().CountFinding().List(e.owner, app.CountFindingListRequest_builder{
		Filters: []*app.CountFindingFilter{app.CountFindingFilter_builder{Count: ref[app.InventoryCountRef](count.GetId())}.Build()},
	}.Build())
	x.NoError(err)
	x.Len(fs.GetItems(), 2)

	r := once("reservation.add", func(op []byte) (string, error) {
		v, err := e.reserve(e.owner, room, e.tomorrow(10, 0), e.tomorrow(11, 0), func(r *app.ReservationAddRequest) { r.SetOp(op) })
		return string(v.GetId()), err
	})
	cal, err := e.app().Reservation().Calendar(e.owner, app.ReservationCalendarRequest_builder{
		Resources: []*app.AssetRef{assetRef(room)},
		From:      timestamppb.New(e.now),
		To:        timestamppb.New(e.now.AddDate(0, 0, 3)),
	}.Build())
	x.NoError(err)
	x.Len(cal.GetEntries(), 1, "booked once")
	x.Equal(r, string(cal.GetEntries()[0].GetReservation().GetId()))
}

// TestManyMovesOfOneThingAtOnceLandOneAfterAnother: moves of one thing to
// different places at the same moment each land -- one after another, each
// replacing the last from that moment -- and the thing is never in two places
// at once, which PostgreSQL would refuse anyway (`placement_one_parent`).
func TestManyMovesOfOneThingAtOnceLandOneAfterAnother(t *testing.T) {
	e := newEnv(t)
	e.postgresOnly()
	x := e.x
	hq := e.space("본사", nil)
	from := e.space("창고", hq)
	rooms := []*app.Asset{}
	for i := range 8 {
		rooms = append(rooms, e.space(fmt.Sprintf("방 %d", i), hq))
	}
	laptop := e.item("노트북", "NB-1", from, day)

	at := timestamppb.New(e.now)
	errs := together(len(rooms), func(i int) error {
		_, err := e.app().Asset().Move(e.owner, app.AssetMoveRequest_builder{Ref: assetRef(laptop), To: assetRef(rooms[i]), At: at}.Build())
		return err
	})
	for i, err := range errs {
		x.NoError(err, "move %d", i)
	}

	ctx := context.Background()
	moves, err := e.s.Ent.Event.Query().Where(event.Kind("asset.move"), event.SubjectId(idUUID(laptop.GetId()))).Count(ctx)
	x.NoError(err)
	x.Equal(len(rooms), moves, "every one of them happened")

	cur, err := e.s.Ent.Placement.Query().Where(placement.ChildId(idUUID(laptop.GetId())), placement.SupersededAtIsNil()).All(ctx)
	x.NoError(err)
	x.Len(cur, 2, "where it was until then, and the last of the moves")
	now := e.where(hq, laptop, e.now, nil)
	x.Equal(e.parentOf(laptop), now, "the row and the history agree")
	x.NotEqual(from.GetId(), now)
}

// TestManyUsesOfOneStockAtOnceAllCount: eight different uses of one stock at
// the same moment each take what they took -- one after another, each from
// what the last left -- rather than two of them from the same quantity and the
// second writing over the first. Every movement says the balance it left.
func TestManyUsesOfOneStockAtOnceAllCount(t *testing.T) {
	e := newEnv(t)
	e.postgresOnly()
	x := e.x
	store := e.space("창고", nil)
	s := e.stock(e.model("AA 건전지"), store, 20, 0)

	errs := together(8, func(int) error {
		_, err := e.app().Stock().Consume(e.owner, app.StockChangeRequest_builder{Ref: ref[app.StockRef](s.GetId()), Quantity: 1, Op: newOp()}.Build())
		return err
	})
	for i, err := range errs {
		x.NoError(err, "use %d", i)
	}
	x.EqualValues(12, e.quantity(s), "every use counted")

	ms, err := e.s.Ent.StockMovement.Query().Where(stockmovement.StockId(idUUID(s.GetId())), stockmovement.Reason("consume")).All(context.Background())
	x.NoError(err)
	left := []int64{}
	for _, m := range ms {
		left = append(left, m.Balance)
	}
	slices.Sort(left)
	x.Equal([]int64{12, 13, 14, 15, 16, 17, 18, 19}, left, "each from what the last left")
}

// TestTwoMovesThatWouldMakeALoopTogetherDoNotBothLand: V10. With b inside c
// and d inside a, putting a into b is fine and so is putting c into d -- but
// both would put each inside the other. Locking only the rows each move
// touches lets both through; the tenant's tree lock makes the second see the
// first and refuse.
func TestTwoMovesThatWouldMakeALoopTogetherDoNotBothLand(t *testing.T) {
	e := newEnv(t)
	e.postgresOnly()
	x := e.x
	hq := e.space("본사", nil)

	for round := range 10 {
		a := e.space(fmt.Sprintf("a%d", round), hq)
		c := e.space(fmt.Sprintf("c%d", round), hq)
		d := e.space(fmt.Sprintf("d%d", round), a)
		b := e.space(fmt.Sprintf("b%d", round), c)

		moves := [][2]*app.Asset{{a, b}, {c, d}}
		errs := together(2, func(i int) error {
			_, err := e.app().Asset().Move(e.owner, app.AssetMoveRequest_builder{Ref: assetRef(moves[i][0]), To: assetRef(moves[i][1])}.Build())
			return err
		})
		landed := 0
		for _, err := range errs {
			if err == nil {
				landed++
				continue
			}
			x.Equal(codes.FailedPrecondition, codeOf(err), "round %d: %v", round, err)
		}
		x.Equal(1, landed, "round %d: one of the two, and only one", round)

		// And nothing is inside itself: walking up from either reaches the
		// building.
		for _, start := range []*app.Asset{a, c} {
			seen := map[string]bool{}
			for cur := e.get(start); len(cur.GetParentId()) > 0; {
				x.False(seen[string(cur.GetId())], "round %d: a loop", round)
				seen[string(cur.GetId())] = true
				if sameId(cur.GetParentId(), hq.GetId()) {
					break
				}
				cur = e.get(app.Asset_builder{Id: cur.GetParentId()}.Build())
			}
		}
	}
}

// TestOneSlotTakesOneInstallAtOnce: parts installed into the same slot of one
// machine at the same moment -- or into overlapping positions of one rack --
// are one success and the rest refused, never two parts in one place.
func TestOneSlotTakesOneInstallAtOnce(t *testing.T) {
	e := newEnv(t)
	e.postgresOnly()
	x := e.x
	room := e.space("서버실", nil)
	server := e.item("서버", "SV-1", room, day)
	rack := e.item("랙", "RK-1", room, day)

	tries := []struct {
		name string
		into *app.Asset
		req  func(*app.AssetMoveRequest)
	}{
		{"slot", server, func(r *app.AssetMoveRequest) { r.SetMode("installed"); r.SetSlot("DIMM1") }},
		{"rack", rack, func(r *app.AssetMoveRequest) { r.SetMode("installed"); r.SetUFrom(10); r.SetUTo(11) }},
	}
	for _, try := range tries {
		parts := []*app.Asset{}
		for i := range 8 {
			parts = append(parts, e.item(fmt.Sprintf("%s 부품 %d", try.name, i), fmt.Sprintf("%s-%d", try.name, i), room, day))
		}
		errs := together(len(parts), func(i int) error {
			req := app.AssetMoveRequest_builder{Ref: assetRef(parts[i]), To: assetRef(try.into)}.Build()
			try.req(req)
			_, err := e.app().Asset().Move(e.owner, req)
			return err
		})
		landed := 0
		for i, err := range errs {
			if err == nil {
				landed++
				x.Equal(try.into.GetId(), e.parentOf(parts[i]))
				continue
			}
			x.Equal(codes.FailedPrecondition, codeOf(err), "%s: %v", try.name, err)
			x.Equal(room.GetId(), e.parentOf(parts[i]), "%s: a refused part stays where it was", try.name)
		}
		x.Equal(1, landed, "%s: one part in one place", try.name)
	}
}
