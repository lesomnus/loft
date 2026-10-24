package domain_test

import (
	"context"
	"os"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"

	app "github.com/lesomnus/rove"
	"github.com/lesomnus/rove/cmd"
)

// perConn counts the statements pgx is asked, by the connection that sends
// them -- so that what an operation's transaction sends is told apart from
// anything else the process does at the same time on another connection.
type perConn struct {
	mu sync.Mutex
	on bool
	n  map[*pgx.Conn]int
}

func (c *perConn) TraceQueryStart(ctx context.Context, conn *pgx.Conn, _ pgx.TraceQueryStartData) context.Context {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.on {
		c.n[conn]++
	}
	return ctx
}

func (c *perConn) TraceQueryEnd(context.Context, *pgx.Conn, pgx.TraceQueryEndData) {}

func (c *perConn) count(on bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.on = on
}

// most is what the busiest connection sent, which is the operation's: it is
// one transaction, and so one connection.
func (c *perConn) most() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	m := 0
	for _, n := range c.n {
		m = max(m, n)
	}
	return m
}

// TestAnAssetMadeInBulkIsAFewStatements holds what #103 bought, on the
// operation that makes assets many at a time: a stock's units turned into
// assets, each one the registration an Add makes. An imported row sent 149
// statements, one round trip each, so a sheet of 5,000 took two minutes;
// nearly all of what went was the layers under this one asking again what
// they had already asked, or saying one row at a time what could be said
// together: the generator loading every edge to answer a key the row holds
// (protoc-gen-orm-ent#9); the gate's read through the wall and the sink's of
// a tenant, for every reference of every write, asked once in the
// transaction [Deps.run] begins (payday#85, #89); the trail reading again the
// row its write had just read (payday#87); the trail written a row a
// statement, where it is written a few hundred to one (payday#88); and the
// facts an asset is made with, a statement each, where they are one AddMany
// (payday#84). Any of them coming back would be a registration taking half as
// long again, or twice as long, and nothing else would say so.
//
// It was measured on the sheet import until that went (design D34), and is
// measured here on the same path without the sheet's own lookups.
//
// It is a ceiling with room in it rather than the number, so that a statement
// an asset really needs is a decision to raise it and not a test to fix. It
// was raised once so, from 13, by payday's past (payday#151): an operation's
// event checks that its subject is of the operation's tenant, and its facts
// check theirs again and read what stands at their moment before they write
// -- three reads a registration of one, 11.2 statements an asset made 14.2.
// A conversion registers each asset as an Add of its own; registering them
// as bulk registration does, the children of one operation in a statement a
// page (`past.Op.Batch`), would make those reads one a page.
//
// PostgreSQL only: it counts what pgx is asked, and that is the round trip.
func TestAnAssetMadeInBulkIsAFewStatements(t *testing.T) {
	if os.Getenv("PDTEST_POSTGRES") == "" {
		t.Skip("counts what pgx is asked: set PDTEST_POSTGRES")
	}

	c := &perConn{n: map[*pgx.Conn]int{}}
	e := newEnvWith(t, func(cfg *cmd.Config) {
		v, err := pgx.ParseConfig(cfg.Db.Dsn)
		if err != nil {
			t.Fatal(err)
		}
		v.Tracer = c

		name := stdlib.RegisterConnConfig(v)
		t.Cleanup(func() { stdlib.UnregisterConnConfig(name) })
		cfg.Db.Dsn = name
	})
	x := e.x

	room := e.space("창고", nil)
	st := e.stock(e.model("모니터"), room, 200, 0)

	// One first: what the first of a kind costs is not what every one costs.
	_, err := e.app().Stock().Convert(e.owner, app.StockConvertRequest_builder{Ref: ref[app.StockRef](st.GetId()), Quantity: 1}.Build())
	x.NoError(err)

	const n = 100
	c.count(true)
	v, err := e.app().Stock().Convert(e.owner, app.StockConvertRequest_builder{Ref: ref[app.StockRef](st.GetId()), Quantity: n}.Build())
	c.count(false)
	x.NoError(err)
	x.Len(v.GetAssets(), n)

	per := float64(c.most()) / n
	t.Logf("%.1f statements an asset", per)
	x.LessOrEqual(per, 16.0, "an asset made is %.1f statements; it was 14.2 once payday kept the past, 11.2 when this was written (an imported row was 13.8, and 149 before #103)", per)
}
