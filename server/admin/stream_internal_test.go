package admin

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// TestALongCallSaysItIsStillAtIt: what the work says goes out as it says it,
// a heartbeat goes out while it says nothing, and a call that cannot be
// answered any more ends the work rather than leave it running.
func TestALongCallSaysItIsStillAtIt(t *testing.T) {
	x := require.New(t)
	was := Heartbeat
	t.Cleanup(func() { Heartbeat = was })
	Heartbeat = 10 * time.Millisecond

	said := []string{}
	say := func(v string) error { said = append(said, v); return nil }
	err := working(context.Background(), "purging @acme", say, func(ctx context.Context, p progress) error {
		p.say("%d rows", 100)
		time.Sleep(80 * time.Millisecond)
		return nil
	})
	x.NoError(err)
	x.Contains(said, "100 rows")
	beats := 0
	for _, v := range said {
		if strings.HasPrefix(v, "purging @acme: ") {
			beats++
		}
	}
	x.GreaterOrEqual(beats, 2, "%v", said)

	gone := errors.New("the caller is gone")
	ended := make(chan error, 1)
	err = working(context.Background(), "reindexing", func(string) error { return gone }, func(ctx context.Context, p progress) error {
		<-ctx.Done()
		ended <- ctx.Err()
		return ctx.Err()
	})
	x.ErrorIs(err, gone)
	x.ErrorIs(<-ended, context.Canceled, "the work was ended, and not left running")
}
