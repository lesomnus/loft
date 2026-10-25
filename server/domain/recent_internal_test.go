package domain

import (
	"strings"
	"testing"
	"time"
	"uuid"

	"github.com/protobuf-orm/ent/dialect"
	"github.com/protobuf-orm/ent/dialect/sql"
	"github.com/stretchr/testify/require"
)

// TestAPageGoesOnAfterTheLastRowAsOnePair: what a list newest first reads
// after a page is the pair compared once, `(date_created, id) < (a, b)` -- a
// row value, which an index starts its read at -- and not the OR that means,
// which a database reads as a filter (payday#154). Of the events, the trail
// and the access record alike.
func TestAPageGoesOnAfterTheLastRowAsOnePair(t *testing.T) {
	for _, table := range []string{"event", "audit", "access"} {
		for engine, want := range map[string]string{
			dialect.Postgres: ` WHERE ("` + table + `"."date_created", "` + table + `"."id") < ($1, $2)`,
			dialect.SQLite:   " WHERE (`" + table + "`.`date_created`, `" + table + "`.`id`) < (?, ?)",
		} {
			s := sql.Dialect(engine).Select("*").From(sql.Table(table))
			before(time.Now().UTC(), uuid.NewV4())(s)
			q, args := s.Query()
			require.True(t, strings.HasSuffix(q, want), "%s: %s", engine, q)
			require.Len(t, args, 2, engine)
		}
	}
}

// TestAPageEndsAtARow: where a page ends is the row it ended at, its time to
// the nanosecond and its key, whatever zone the time was read in.
func TestAPageEndsAtARow(t *testing.T) {
	x := require.New(t)
	seoul, err := time.LoadLocation("Asia/Seoul")
	x.NoError(err)
	at := time.Date(2026, 10, 25, 21, 30, 15, 123456789, seoul)
	id := uuid.NewV4()

	got, key, err := afterOf(afterFor(at, id))
	x.NoError(err)
	x.True(at.Equal(got), "%s, read as %s", at, got)
	x.Equal(id, key)

	for _, v := range []string{"", "어디", "MTIz", afterFor(at, id)[:8]} {
		_, _, err := afterOf(v)
		x.Error(err, "%q", v)
	}
}
