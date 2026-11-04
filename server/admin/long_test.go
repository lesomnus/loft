package admin_test

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/base64"
	"sync"
	"testing"
	"time"

	"github.com/lesomnus/payday/pdtest"
	"github.com/protobuf-orm/ent/dialect"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	app "github.com/lesomnus/loft"
	"github.com/lesomnus/loft/cli"
	"github.com/lesomnus/loft/cmd"
	"github.com/lesomnus/loft/internal/ent"
	"github.com/lesomnus/loft/internal/opensearchtest"
	"github.com/lesomnus/loft/server/admin"
)

// slow is a database whose every query takes a while -- as a check of every
// row takes on a deployment of any size -- and that says when one was ended
// by its call going away rather than finished.
type slow struct {
	dialect.Driver
	wait time.Duration

	// began says a query began, once for each, while somebody is listening.
	began chan struct{}
	once  sync.Once
	ended chan struct{}
}

func lag(drv dialect.Driver, wait time.Duration) *slow {
	return &slow{Driver: drv, wait: wait, began: make(chan struct{}, 1), ended: make(chan struct{})}
}

func (d *slow) Query(ctx context.Context, q string, args, v any) error {
	select {
	case d.began <- struct{}{}:
	default:
	}
	select {
	case <-time.After(d.wait):
	case <-ctx.Done():
		d.once.Do(func() { close(d.ended) })
		return ctx.Err()
	}
	return d.Driver.Query(ctx, q, args, v)
}

// BeginTx is the database's, as the driver it stands in for begins one.
func (d *slow) BeginTx(ctx context.Context, o *sql.TxOptions) (dialect.Tx, error) {
	return d.Driver.(interface {
		BeginTx(context.Context, *sql.TxOptions) (dialect.Tx, error)
	}).BeginTx(ctx, o)
}

// running waits until a query of `db` is running, so that what is cancelled
// next is a query and not a call to somewhere else.
func running(t *testing.T, db *slow) {
	t.Helper()
	select {
	case <-db.began:
	default:
	}
	select {
	case <-db.began:
	case <-time.After(10 * time.Second):
		require.Fail(t, "no query began")
	}
}

// TestALongCheckSaysItIsAtItAndStopsWithItsCall: a check of the rows reads
// every one of them, and on a real deployment a single query of it runs for
// minutes. While it does the call says, every heartbeat, that it is still at
// it -- the work saying nothing -- and a call that goes away ends the query,
// handed its context down to the database, rather than leave it running.
func TestALongCheckSaysItIsAtItAndStopsWithItsCall(t *testing.T) {
	x := require.New(t)
	was := admin.Heartbeat
	t.Cleanup(func() { admin.Heartbeat = was })
	admin.Heartbeat = 50 * time.Millisecond

	c := cmd.Config{}
	c.Db.Driver, c.Db.Dsn = pdtest.DB(t)
	c.Watch.Broker = "memory"
	c.App.Files = t.TempDir()
	c.Auth.Roster.Db.Driver, c.Auth.Roster.Db.Dsn = pdtest.DB(t)
	c.Audit.Digest.Key = "base64:" + base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{7}, 32))
	x.NoError(run(t, cli.NewCmdInit(&c), "--tenant", "acme", "--login", "owner", "--password", "owner1234"))
	s, err := cmd.Build(t.Context(), c)
	x.NoError(err)
	t.Cleanup(func() { s.Close() })

	// The same deployment, on a database whose queries each take longer than
	// a heartbeat.
	db := lag(s.Drv, 60*time.Millisecond)
	lagging := *s
	lagging.Ent = ent.NewClient(ent.Driver(db))
	ops, err := admin.Operators(map[string]string{"alice": key})
	x.NoError(err)
	g, err := admin.Routes{S: &lagging, C: c, Operators: ops}.Grpc(t.Context())
	x.NoError(err)
	conn := pdtest.Serve(t, g)

	// To the end, saying it is at it on the way.
	stream, err := app.NewOperatorTrailServiceClient(conn).Rows(as(key), &app.OperatorTrailRowsRequest{})
	x.NoError(err)
	beats := 0
	var checked *app.OperatorTrailRowsChecked
	for {
		m, err := stream.Recv()
		if err != nil {
			x.ErrorContains(err, "EOF")
			break
		}
		if m.GetProgress() != "" {
			beats++
		}
		if m.HasChecked() {
			checked = m.GetChecked()
		}
	}
	x.NotNil(checked, "the check ended without saying what it found")
	x.Positive(beats, "a check that took longer than a heartbeat said nothing while it ran")

	// And gone away from while a query runs.
	ctx, cancel := context.WithCancel(as(key))
	stream, err = app.NewOperatorTrailServiceClient(conn).Rows(ctx, &app.OperatorTrailRowsRequest{})
	x.NoError(err)
	m, err := stream.Recv()
	x.NoError(err)
	x.NotEmpty(m.GetProgress(), "the first word of a long check is that it is at it")
	running(t, db)
	cancel()
	select {
	case <-db.ended:
	case <-time.After(10 * time.Second):
		x.Fail("the query ran on after its call went away")
	}
	x.Equal(codes.Canceled, status.Code(drained(stream)))
}

// TestAReindexSaysItIsAtItAndStopsWithItsCall: a reindex reads every row of
// what it indexes and writes it, and runs for as long as there are rows. It
// says every heartbeat that it is still at it, and a call that goes away
// ends the query it is reading with -- as a check of the rows does.
func TestAReindexSaysItIsAtItAndStopsWithItsCall(t *testing.T) {
	x := require.New(t)
	was := admin.Heartbeat
	t.Cleanup(func() { admin.Heartbeat = was })
	admin.Heartbeat = 50 * time.Millisecond

	c := cmd.Config{}
	c.Db.Driver, c.Db.Dsn = pdtest.DB(t)
	c.Watch.Broker = "memory"
	c.App.Files = t.TempDir()
	c.Auth.Roster.Db.Driver, c.Auth.Roster.Db.Dsn = pdtest.DB(t)
	c.App.Search.Opensearch = opensearchtest.Config(t)
	x.NoError(run(t, cli.NewCmdInit(&c), "--tenant", "acme", "--login", "owner", "--password", "owner1234", "--demo"))
	s, err := cmd.Build(t.Context(), c)
	x.NoError(err)
	t.Cleanup(func() { s.Close() })

	db := lag(s.Drv, 60*time.Millisecond)
	lagging := *s
	lagging.Ent = ent.NewClient(ent.Driver(db))
	ops, err := admin.Operators(map[string]string{"alice": key})
	x.NoError(err)
	g, err := admin.Routes{S: &lagging, C: c, Operators: ops}.Grpc(t.Context())
	x.NoError(err)
	conn := pdtest.Serve(t, g)

	stream, err := app.NewOperatorSearchServiceClient(conn).Reindex(as(key), &app.OperatorSearchReindexRequest{})
	x.NoError(err)
	beats, made := 0, 0
	for {
		m, err := stream.Recv()
		if err != nil {
			x.ErrorContains(err, "EOF")
			break
		}
		if m.GetProgress() != "" {
			beats++
		}
		if m.HasMade() {
			made++
		}
	}
	x.Equal(3, made, "every index, made")
	x.Positive(beats, "a reindex that took longer than a heartbeat said nothing while it ran")

	ctx, cancel := context.WithCancel(as(key))
	stream, err = app.NewOperatorSearchServiceClient(conn).Reindex(ctx, &app.OperatorSearchReindexRequest{})
	x.NoError(err)
	m, err := stream.Recv()
	x.NoError(err)
	x.NotEmpty(m.GetProgress(), "the first word of a long reindex is that it is at it")
	running(t, db)
	cancel()
	select {
	case <-db.ended:
	case <-time.After(10 * time.Second):
		x.Fail("the reindex read on after its call went away")
	}
	x.Equal(codes.Canceled, status.Code(drained(stream)))
}

// drained is how a stream ended, past what it had said already.
func drained[T any](stream interface{ Recv() (*T, error) }) error {
	for {
		if _, err := stream.Recv(); err != nil {
			return err
		}
	}
}
