package domain_test

import (
	"context"
	"fmt"
	"os"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"

	app "github.com/lesomnus/rove"
	"github.com/lesomnus/rove/cmd"
)

// perConn counts the statements pgx is asked, by the connection that sends
// them -- so that what an import's transaction sends is told apart from
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

// most is what the busiest connection sent, which is the import's: it is one
// transaction, and so one connection.
func (c *perConn) most() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	m := 0
	for _, n := range c.n {
		m = max(m, n)
	}
	return m
}

// TestAnImportedRowIsAFewStatements holds what #103 bought. A row of a sheet
// sent 149 statements, one round trip each, so a sheet of 5,000 took two
// minutes; it sends 14. Nearly all of what went was the layers under this one
// asking again what they had already asked, or saying one row at a time what
// could be said together: the generator loading every edge to answer a key
// the row holds (protoc-gen-orm-ent#9); the gate's read through the wall and
// the sink's of a tenant, for every reference of every write, asked once in
// the transaction [Deps.run] begins (payday#85, #89); the trail reading again
// the row its write had just read (payday#87); the trail written a row a
// statement, where it is written a few hundred to one (payday#88); and the
// facts an asset is made with, a statement each, where they are one AddMany
// (payday#84). Any of them coming back would be a sheet taking half as long
// again, or twice as long, and nothing else would say so.
//
// It is a ceiling with room in it rather than the number, so that a statement
// a row really needs is a decision to raise it and not a test to fix.
//
// PostgreSQL only: it counts what pgx is asked, and that is the round trip.
func TestAnImportedRowIsAFewStatements(t *testing.T) {
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

	// A room to put things in, and one thing of each type: what the first of
	// a kind costs -- a type made -- is not what every row costs.
	_, err := e.app().Asset().Import(e.owner, app.AssetImportRequest_builder{Format: "csv",
		Data: []byte("종류,이름,태그,위치태그\n공간,본사,HQ,\n공간,101호,HQ-101,HQ\n물품,노트북 0,LT-0,HQ-101\n물품,모니터 0,MN-0,HQ-101\n"),
	}.Build())
	x.NoError(err)
	_, err = e.app().Asset().Import(e.owner, app.AssetImportRequest_builder{Format: "csv",
		Data: []byte("종류,이름,태그,유형,위치태그\n물품,노트북 1,LT-1,노트북,HQ-101\n물품,모니터 1,MN-1,모니터,HQ-101\n"),
	}.Build())
	x.NoError(err)

	const rows = 20
	sheet := "종류,이름,태그,유형,시리얼,위치태그,기준일\n"
	for i := range rows {
		kind := []string{"노트북", "모니터"}[i%2]
		sheet += fmt.Sprintf("물품,%s %02d,X-%02d,%s,SN%02d,HQ-101,2026-01-%02d\n", kind, i, i, kind, i, 1+i)
	}

	c.count(true)
	v, err := e.app().Asset().Import(e.owner, app.AssetImportRequest_builder{Format: "csv", Data: []byte(sheet)}.Build())
	c.count(false)
	x.NoError(err)
	x.Empty(v.GetErrors())
	x.EqualValues(rows, v.GetCreated())

	per := float64(c.most()) / rows
	t.Logf("%.1f statements a row", per)
	x.LessOrEqual(per, 16.0, "an imported row is %.1f statements; it was 13.8 when this was written, 18.8 before facts were added many at a time, 37 before payday#87 and #88, and 149 before #103", per)
}
