package offboard

import (
	"maps"
	"slices"
	"testing"

	"github.com/protobuf-orm/ent/dialect/sql/schema"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/reflect/protoreflect"

	"github.com/lesomnus/loft/internal/ent/migrate"
	"github.com/lesomnus/loft/server/pd"
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

// TestAnExportBeforeARenameIsReadUnderTheNamesNow (D76): of every exported
// table, each column renamed since is a field of its rows, which a restore of
// an export of a format before this build's reads by the name the field had
// then, as JSON wrote it and as the schema said it -- and a row that names a
// field by both is refused.
func TestAnExportBeforeARenameIsReadUnderTheNamesNow(t *testing.T) {
	x := require.New(t)
	for _, c := range slices.Concat(pd.ColumnRenames, RenamedColumns) {
		if _, ok := Exported[c.Table]; !ok && c.Table != "tenant" {
			continue
		}
		p, err := planOf(c.Table)
		x.NoError(err)
		as := renamed(c.Table, p.msg.Descriptor())
		f := p.msg.Descriptor().Fields().ByName(protoreflect.Name(c.To))
		x.NotNil(f, "%s.%s is no field of its rows", c.Table, c.To)
		x.Equal(f.JSONName(), as[jsonName(c.From)], "%s.%s, as JSON wrote it", c.Table, c.From)
		x.Equal(f.JSONName(), as[c.From], "%s.%s, as the schema said it", c.Table, c.From)
	}

	x.Equal("validFrom", jsonName("valid_from"))
	x.Equal("checkedInAt", jsonName("checked_in_at"))
	x.Equal("at", jsonName("at"))

	as := map[string]string{"validFrom": "dateStart", "valid_from": "dateStart"}
	got, err := renames([]byte(`{"id":"AQ==","validFrom":"2026-11-08T00:00:00Z"}`), as)
	x.NoError(err)
	x.JSONEq(`{"id":"AQ==","dateStart":"2026-11-08T00:00:00Z"}`, string(got))
	same := []byte(`{"id":"AQ==","dateStart":"2026-11-08T00:00:00Z"}`)
	got, err = renames(same, as)
	x.NoError(err)
	x.Equal(same, got, "a row that names nothing renamed is read as it is")
	_, err = renames([]byte(`{"validFrom":"2026-11-08T00:00:00Z","dateStart":"2026-11-09T00:00:00Z"}`), as)
	x.ErrorContains(err, "said twice")
}
