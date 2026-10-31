package offboard

import (
	"maps"
	"slices"
	"strings"
	"testing"
	"time"
	"uuid"

	"github.com/protobuf-orm/ent/dialect"
	"github.com/protobuf-orm/ent/dialect/sql"
	"github.com/protobuf-orm/ent/dialect/sql/schema"
	"github.com/stretchr/testify/require"

	"github.com/lesomnus/loft/internal/ent/migrate"
)

// SetPage makes an export read n rows at a time until the test is over.
func SetPage(t *testing.T, n int) {
	was := page
	page = n
	t.Cleanup(func() { page = was })
}

// TestAPageStartsAfterTheLastRowAsOnePair: what an export reads after a page
// is the pair compared once, `(date_created, id) > (a, b)` -- a row value,
// which an index starts its read at -- and not the OR that means, which a
// database reads as a filter (payday#154).
func TestAPageStartsAfterTheLastRowAsOnePair(t *testing.T) {
	for engine, want := range map[string]string{
		dialect.Postgres: ` WHERE ("label"."date_created", "label"."id") > ($1, $2)`,
		dialect.SQLite:   " WHERE (`label`.`date_created`, `label`.`id`) > (?, ?)",
	} {
		s := sql.Dialect(engine).Select("*").From(sql.Table("label"))
		after(&mark{at: time.Now().UTC(), id: uuid.NewV4()})(s)
		q, args := s.Query()
		require.True(t, strings.HasSuffix(q, want), "%s: %s", engine, q)
		require.Len(t, args, 2, engine)
	}
}

// TestEveryExportedTableHasTheColumnsOfItsOrder: an export reads a table a
// page at a time by when its rows were made and their keys, so every table it
// reads has both (loft#120).
func TestEveryExportedTableHasTheColumnsOfItsOrder(t *testing.T) {
	for _, name := range slices.Sorted(maps.Keys(Exported)) {
		i := slices.IndexFunc(migrate.Tables, func(t *schema.Table) bool { return t.Name == name })
		require.GreaterOrEqual(t, i, 0, name)
		for _, c := range []string{"date_created", "id"} {
			require.True(t, slices.ContainsFunc(migrate.Tables[i].Columns, func(v *schema.Column) bool { return v.Name == c }), "%s has no %s", name, c)
		}
	}
}
