package offboard

import (
	"maps"
	"slices"
	"testing"

	"github.com/protobuf-orm/ent/dialect/sql/schema"
	"github.com/stretchr/testify/require"

	"github.com/lesomnus/rove/internal/ent/migrate"
)

// SetPage makes an export read n rows at a time until the test is over.
func SetPage(t *testing.T, n int) {
	was := page
	page = n
	t.Cleanup(func() { page = was })
}

// TestEveryExportedTableHasTheColumnsOfItsOrder: an export reads a table a
// page at a time by when its rows were made and their keys, so every table it
// reads has both (rove#120).
func TestEveryExportedTableHasTheColumnsOfItsOrder(t *testing.T) {
	for _, name := range slices.Sorted(maps.Keys(Exported)) {
		i := slices.IndexFunc(migrate.Tables, func(t *schema.Table) bool { return t.Name == name })
		require.GreaterOrEqual(t, i, 0, name)
		for _, c := range []string{"date_created", "id"} {
			require.True(t, slices.ContainsFunc(migrate.Tables[i].Columns, func(v *schema.Column) bool { return v.Name == c }), "%s has no %s", name, c)
		}
	}
}
