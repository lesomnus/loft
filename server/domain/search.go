package domain

import (
	"uuid"

	"github.com/protobuf-orm/ent/dialect"
	"github.com/protobuf-orm/ent/dialect/sql"

	"github.com/lesomnus/rove/internal/ent/asset"
	"github.com/lesomnus/rove/internal/search"
)

// What `AssetService.Search` asks the database, in a deployment that names no
// search engine (design 9.5, `internal/search`).

// found is the assets of `tenant` that `q` finds, as a predicate on the asset
// table.
//
// It is a set of identifiers per column -- the tag, the name, the serial, and
// through the asset the name of its type, the name and maker of its model and
// the name of who has it -- joined with UNION, each column compared on its own
// rather than all of them in one OR across the joins.
//
// A single letter (`search.ByPrefix`) is the tag and the name by prefix, and
// nothing else.
func found(postgres bool, tenant uuid.UUID, q string) func(*sql.Selector) {
	f := search.Fold(q)
	prefix := search.ByPrefix(f)
	pattern := "%" + search.Like(f) + "%"
	if prefix {
		pattern = search.Like(f) + "%"
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
				b.S(" AND " + fold(postgres, v.text) + " LIKE ")
				b.Arg(pattern)
				b.S(` ESCAPE '\'`)
			}
			b.S(")")
		}))
	}
}

// like is the rows of a table one of whose columns has q in it, folded --
// begins with it, for a single letter, and then the first column alone: what
// the searches of people and models ask the database (`find.go`).
func like(postgres bool, cols []string, q string) func(*sql.Selector) {
	f := search.Fold(q)
	pattern := "%" + search.Like(f) + "%"
	if search.ByPrefix(f) {
		pattern, cols = search.Like(f)+"%", cols[:1]
	}
	return func(s *sql.Selector) {
		ps := make([]*sql.Predicate, 0, len(cols))
		for _, c := range cols {
			ps = append(ps, sql.P(func(b *sql.Builder) {
				b.S(fold(postgres, s.C(c)) + " LIKE ")
				b.Arg(pattern)
				b.S(` ESCAPE '\'`)
			}))
		}
		s.Where(sql.Or(ps...))
	}
}

// fold is a column as the database compares it with what was typed.
func fold(postgres bool, col string) string {
	if postgres {
		return "lower(normalize(" + col + ", NFKC))"
	}
	// SQLite has no normal forms; the query is folded, the rows are compared
	// as they are.
	return "lower(" + col + ")"
}

// postgres says whether this transaction is on PostgreSQL, where the text is
// compared by its normal form.
func (t *Tx) postgres() bool { return t.drv.Dialect() == dialect.Postgres }
