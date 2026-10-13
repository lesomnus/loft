package domain_test

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"

	app "github.com/lesomnus/rove"
)

// TestAnOperationThatWaitedIsRecordedAfterTheOneItWaitedFor: an operation's
// moment is taken once it holds the tree lock (rove#21, decided in rove#35).
//
// Two moves of one laptop. A began first and read the clock; B took the lock
// and committed before A reached it. A is applied over B, so it is recorded
// after B: known at any instant, the laptop went from where it was to where B
// put it, and then to where A put it. With A's moment from before the lock it
// was recorded before B, superseding B's row at a moment before B had written
// it -- and known at any instant, B's move was never there.
//
// PostgreSQL only: payday opens every SQLite database with `_txlock=immediate`,
// so a transaction there begins holding the write lock, and its moment already
// follows every write before it.
func TestAnOperationThatWaitedIsRecordedAfterTheOneItWaitedFor(t *testing.T) {
	e := newEnv(t)
	if e.cfg.Db.Driver == "sqlite3" {
		t.Skip("SQLite begins every transaction holding the write lock, so nothing gets in between; PDTEST_POSTGRES runs this")
	}
	x := e.x
	hq := e.space("본사", nil)
	from := e.space("창고", hq)
	byB := e.space("B의 자리", hq)
	byA := e.space("A의 자리", hq)
	laptop := e.item("노트북", "NB-1", from, day)

	// A clock that moves on every reading, as a real one does. The first
	// reading is A's, as its transaction begins, and A stops there until B
	// has committed.
	e.now = e.now.Add(time.Minute)
	start := e.now
	var mu sync.Mutex
	ticks := e.now
	tick := func() time.Time {
		mu.Lock()
		defer mu.Unlock()
		ticks = ticks.Add(time.Millisecond)
		return ticks
	}
	began, committed := make(chan struct{}), make(chan struct{})
	var reads atomic.Int64
	e.s.Deps.Now = func() time.Time {
		v := tick()
		if reads.Add(1) == 1 {
			close(began)
			<-committed
		}
		return v
	}
	t.Cleanup(func() { e.s.Deps.Now = func() time.Time { return e.now } })

	moved := make(chan error, 1)
	go func() {
		_, err := e.app().Asset().Move(e.owner, app.AssetMoveRequest_builder{Ref: assetRef(laptop), To: assetRef(byA)}.Build())
		moved <- err
	}()
	<-began
	_, err := e.app().Asset().Move(e.owner, app.AssetMoveRequest_builder{Ref: assetRef(laptop), To: assetRef(byB)}.Build())
	x.NoError(err)
	close(committed)
	x.NoError(<-moved)
	end := tick()
	e.s.Deps.Now = func() time.Time { return e.now }

	// Where it is, known at every millisecond of it: the places in the order
	// they were taken, each once.
	after := end.Add(time.Hour)
	seen := []string{}
	for k := start; !k.After(end); k = k.Add(time.Millisecond) {
		in := e.where(hq, laptop, after, &k)
		x.NotNil(in, "known at %s, the laptop was nowhere", k.Sub(start))
		name := "?"
		switch {
		case sameId(in, from.GetId()):
			name = "창고"
		case sameId(in, byB.GetId()):
			name = "B의 자리"
		case sameId(in, byA.GetId()):
			name = "A의 자리"
		}
		if len(seen) == 0 || seen[len(seen)-1] != name {
			seen = append(seen, name)
		}
	}
	x.Equal([]string{"창고", "B의 자리", "A의 자리"}, seen, "known at some moment, the history is not what was applied by then")

	// And where the asset says it is now is where its history says.
	now, err := e.app().Asset().Get(e.owner, app.AssetGetRequest_builder{Ref: assetRef(laptop)}.Build())
	x.NoError(err)
	x.True(sameId(now.GetParentId(), byA.GetId()), "the asset is not where the last move put it")
	x.True(sameId(e.where(hq, laptop, after, nil), byA.GetId()), "its history says it is somewhere else")
}
