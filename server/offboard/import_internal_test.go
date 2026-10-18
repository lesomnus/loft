package offboard

import (
	"maps"
	"slices"
	"testing"

	"github.com/protobuf-orm/ent/dialect/sql/schema"
	"github.com/stretchr/testify/require"

	"github.com/lesomnus/rove/internal/ent/migrate"
)

// TestEveryExportedTableIsRestoredWhole: every field of every row an export
// writes has a column to go back to and a setter that takes it, and every
// column is one of them -- so that a restore puts back all of a row, and
// nothing in it is left to a default.
func TestEveryExportedTableIsRestoredWhole(t *testing.T) {
	for _, name := range append(slices.Sorted(maps.Keys(Exported)), "tenant") {
		t.Run(name, func(t *testing.T) {
			p, err := planOf(name)
			require.NoError(t, err)

			set := map[string]bool{}
			for _, s := range p.fields {
				c := string(s.f.Name())
				if s.key != nil {
					c += "_id"
				}
				set[c] = true
			}
			i := slices.IndexFunc(migrate.Tables, func(t *schema.Table) bool { return t.Name == name })
			for _, c := range migrate.Tables[i].Columns {
				require.True(t, set[c.Name], "the column %s.%s is no field of its rows", name, c.Name)
			}
		})
	}
}
