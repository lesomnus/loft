package domain

import (
	"uuid"

	"github.com/protobuf-orm/ent/dialect"
	"github.com/protobuf-orm/ent/dialect/sql"

	"github.com/lesomnus/rove/internal/ent/asset"
	"github.com/lesomnus/rove/internal/search"
)

// What `AssetService.Search` asks the database (design 9.5, `internal/search`).

// found is the assets of `tenant` that `q` finds, as a predicate on the asset
// table.
//
// It is a set of identifiers per column -- the tag, the name, the serial, and
// through the asset the name of its type, the name and maker of its model and
// the name of who has it -- joined with UNION, rather than one OR over all of
// them. An OR that reaches into other tables is a scan of the asset table
// whatever indexes there are; a UNION is one index scan a column, which is
// what the indexes `cli/pg.sql` makes were made for.
//
// Below what the database's index answers (`Mode.ByPrefix`) it is the tag and
// the name by prefix, and nothing else.
func found(postgres bool, m search.Mode, tenant uuid.UUID, q string) func(*sql.Selector) {
	f := search.Fold(q)
	prefix := m.ByPrefix(f)
	pattern := "%" + search.Like(f) + "%"
	if prefix {
		pattern = search.Like(f) + "%"
	}
	fold := func(col string) string {
		if postgres {
			return "lower(normalize(" + col + ", NFKC))"
		}
		// SQLite has no normal forms; the query is folded, the rows are
		// compared as they are.
		return "lower(" + col + ")"
	}

	type set struct{ from, text string }
	sets := []set{
		{`asset a`, `a."tag"`},
		{`asset a`, `a."name"`},
	}
	if !prefix {
		sets = append(sets,
			set{`asset a`, `a."serial"`},
			set{`asset a JOIN assettype y ON y."id" = a."type_id"`, `y."name"`},
			set{`asset a JOIN itemmodel m ON m."id" = a."model_id"`, `m."name"`},
			set{`asset a JOIN itemmodel m ON m."id" = a."model_id"`, `m."maker"`},
			set{`asset a JOIN party p ON p."id" = a."custodian_id"`, `p."name"`},
		)
	}

	return func(s *sql.Selector) {
		s.Where(sql.P(func(b *sql.Builder) {
			b.S(s.C(asset.FieldId)).S(" IN (")
			for i, v := range sets {
				if i > 0 {
					b.S(" UNION ")
				}
				b.S(`SELECT a."id" FROM ` + v.from + ` WHERE a."tenant_id" = `)
				b.Arg(tenant)
				b.S(" AND " + fold(v.text) + " LIKE ")
				b.Arg(pattern)
				b.S(` ESCAPE '\'`)
			}
			b.S(")")
		}))
	}
}

// SearchQuery is what a search for `q` in `tenant` asks the database, as SQL
// and its arguments: the identifiers of what it finds, before the filters and
// the order a request adds. It is what an EXPLAIN is run on to see which index
// answers it -- a test does, and so can an operator wondering whether pg_bigm
// is in use.
func SearchQuery(d string, m search.Mode, tenant uuid.UUID, q string) (string, []any) {
	s := sql.Dialect(d).Select(asset.FieldId).From(sql.Table(asset.Table))
	found(d == dialect.Postgres, m, tenant, q)(s)

	return s.Query()
}

// postgres says whether this transaction is on PostgreSQL, where the text is
// compared by its normal form and an index answers.
func (t *Tx) postgres() bool { return t.drv.Dialect() == dialect.Postgres }

// searchMode is the mode a deployment's database was found to have, and no
// index at all for a layer built without one.
func (d *Deps) searchMode() search.Mode {
	if d == nil {
		return search.Plain
	}

	return d.Search
}
