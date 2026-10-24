package cli

import (
	"context"
	"database/sql"
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/lesomnus/payday/pdid"
	"github.com/protobuf-orm/ent/dialect"
	entsql "github.com/protobuf-orm/ent/dialect/sql"

	"github.com/lesomnus/rove/cmd"
	"github.com/lesomnus/rove/internal/ent"
	"github.com/lesomnus/rove/server/domain"
	"github.com/lesomnus/rove/server/pd"
)

// An asset's facts were Rove's own table, a row for each key it wrote --
// `status`, `attr.cpu` -- holding the value as the text it was said in, and
// are payday's past's since (payday#151): a row for each field of the asset,
// by its number, holding the value's canonical JSON. The rows are the same
// rows -- each keeps its identifier, its tenant, its moments and the events it
// names -- and each is written again in the new shape, once, by the migration
// that makes the new table: what [stashFacts] sets aside, [moveFacts] moves
// back, rewritten.

// factsAside is where [stashFacts] sets an asset's facts aside, as Rove kept
// them, while the schema's migration makes payday's Fact in their place; and
// factsMoved what [moveFacts] makes each of them, as payday's past keeps it.
const (
	factsAside = "rove_fact_aside"
	factsMoved = "rove_fact_moved"
)

// MovedFacts is the act a fact moved into payday's past is on the trail as.
const MovedFacts = "rove.migrate.facts"

// stashFacts sets Rove's facts aside and takes their table out, before the
// schema is migrated, for [moveFacts] to move back once payday's Fact is made
// in its place.
//
// Before, and the table taken out, because a migration only changes a table
// in place: on SQLite it makes a changed table again and copies the columns
// the schema declares into it, which a fact's subject -- a column with no
// default -- is not; and on PostgreSQL it adds a column with no default to a
// table with rows, which it cannot. Payday's Fact is made empty instead, and
// the rows go into it once, rewritten. Nothing names a fact by a foreign key,
// and what pg.sql made on the table -- its index of the values known now, its
// trigger of the asset's tenant -- goes with it, to be made again on the new
// one.
//
// It is one transaction, which sets aside and takes out together or not at
// all; and it is done once, while the table is Rove's.
func stashFacts(ctx context.Context, s *cmd.Server) error {
	has, err := hasColumn(ctx, s, "fact", "asset_id")
	if err != nil || !has {
		return err
	}
	if aside, err := hasTable(ctx, s, factsAside); err != nil {
		return err
	} else if aside {
		// Set aside once already, and a table of the old shape made since:
		// a build before this one migrated in between. Neither is this
		// one's to drop.
		return fmt.Errorf("the assets' facts: %s holds them as they were set aside, and a table of the shape before is beside it, which a build before this one made since; move what it holds into %s by hand, take it out, and migrate again", factsAside, factsAside)
	}

	tx, err := s.Db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, q := range []string{
		"CREATE TABLE " + factsAside + " AS SELECT id, tenant_id, asset_id, key, value, cleared, date_updated, date_created, valid_from, event_id, superseded_at, superseded_by FROM fact",
		"DROP TABLE fact",
	} {
		if _, err := tx.ExecContext(ctx, q); err != nil {
			return fmt.Errorf("the assets' facts, set aside: %w", err)
		}
	}

	return tx.Commit()
}

// movePage is how many facts [moveFacts] reads and rewrites at a time.
const movePage = 1000

// moveFacts moves the facts [stashFacts] set aside into payday's Fact, each
// as payday's past writes the same fact (`domain.LegacyFact`): the asset's
// field by its number, the entry of its attributes by its key, and the value
// as the field's canonical JSON -- a type and a model by the identifier they
// name, in one form whatever form it was written in -- and `null` for one
// cleared. Its identifier, its tenant, its moments, the event that wrote it
// and the one that superseded it are what they were, copied in the database
// and never read into Go, so that a moment is the text it was.
//
// A fact the past cannot write -- a key that is no field of an asset, an
// identifier that does not read as one, text that is not UTF-8 -- is refused
// with the others, each kind of them by how many, and nothing is moved: such
// a fact was never what Rove wrote, and it is a person's to say what it was.
//
// Each fact moved is a row rewritten beneath the servers, so it is put on the
// trail as the act it is ([MovedFacts], `pd.Record`), in the same
// transaction: by the deployment, filed under the row's tenant, its digest the
// row's as it is now. The key the trail's digests are made with is read then
// and not before (`cmd.Server.Contents`), so that a database with no facts to
// move opens none.
//
// It answers how many it moved and how many lines it put on the trail, and is
// done once: what it set aside goes with the move.
func moveFacts(ctx context.Context, s *cmd.Server) (int, int, error) {
	aside, err := hasTable(ctx, s, factsAside)
	if err != nil || !aside {
		return 0, 0, err
	}

	tdrv, tx, err := dialect.BeginTx(ctx, s.Drv)
	if err != nil {
		return 0, 0, err
	}
	defer tx.Rollback()

	for _, q := range []string{
		"DROP TABLE IF EXISTS " + factsMoved,
		"CREATE TABLE " + factsMoved + " AS SELECT id, 0 AS field_number, key, value FROM " + factsAside + " WHERE 1 = 0",
		"CREATE INDEX IF NOT EXISTS " + factsAside + "_id ON " + factsAside + " (id)",
	} {
		if err := tdrv.Exec(ctx, q, []any{}, nil); err != nil {
			return 0, 0, fmt.Errorf("the assets' facts: %w", err)
		}
	}

	pg := s.Dialect == dialect.Postgres
	arg := func(i int) string {
		if pg {
			return fmt.Sprintf("$%d", i)
		}
		return "?"
	}

	var ids []pdid.Id
	refused := map[string]int{}
	var first []string
	var last any
	for {
		q := "SELECT id, key, value, cleared FROM " + factsAside
		args := []any{}
		if last != nil {
			q += " WHERE id > " + arg(1)
			args = append(args, last)
		}
		q += fmt.Sprintf(" ORDER BY id LIMIT %d", movePage)

		type said struct {
			id      any
			key     string
			value   string
			cleared bool
		}
		var page []said
		rows := &entsql.Rows{}
		if err := tdrv.Query(ctx, q, args, rows); err != nil {
			return 0, 0, fmt.Errorf("the assets' facts: %w", err)
		}
		for rows.Next() {
			var v said
			var value sql.NullString
			if err := rows.Scan(&v.id, &v.key, &value, &v.cleared); err != nil {
				rows.Close()
				return 0, 0, fmt.Errorf("the assets' facts: %w", err)
			}
			v.value = value.String
			page = append(page, v)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return 0, 0, fmt.Errorf("the assets' facts: %w", err)
		}
		if len(page) == 0 {
			break
		}
		last = page[len(page)-1].id

		values := make([]string, 0, len(page))
		args = make([]any, 0, 4*len(page))
		for _, v := range page {
			id, err := idOfRow(v.id)
			if err != nil {
				return 0, 0, fmt.Errorf("the assets' facts: %w", err)
			}
			field, key, value, err := domain.LegacyFact(v.key, v.value, v.cleared)
			if err != nil {
				refused[refusedKey(v.key)]++
				if len(first) < 5 {
					first = append(first, fmt.Sprintf("%s: %v", id, err))
				}
				continue
			}
			n := len(args)
			values = append(values, fmt.Sprintf("(%s, %s, %s, %s)", arg(n+1), arg(n+2), arg(n+3), arg(n+4)))
			args = append(args, v.id, int64(field), key, value)
			ids = append(ids, id)
		}
		if len(values) > 0 {
			q := "INSERT INTO " + factsMoved + " (id, field_number, key, value) VALUES " + strings.Join(values, ", ")
			if err := tdrv.Exec(ctx, q, args, nil); err != nil {
				return 0, 0, fmt.Errorf("the assets' facts: %w", err)
			}
		}
		if len(page) < movePage {
			break
		}
	}
	if len(refused) > 0 {
		var vs []string
		for _, k := range slices.Sorted(maps.Keys(refused)) {
			vs = append(vs, fmt.Sprintf("%s: %d", k, refused[k]))
		}
		return 0, 0, fmt.Errorf("the assets' facts: %d payday's past cannot write, so none was moved (%s), such as %s",
			sum(refused), strings.Join(vs, ", "), strings.Join(first, "; "))
	}

	// Back, rewritten, beside what they were: every moment and every event
	// copied as the database holds it.
	var res sql.Result
	if err := tdrv.Exec(ctx, fmt.Sprintf(`INSERT INTO fact (id, tenant_id, subject_id, key, value, field_number, domain,
		date_updated, date_created, valid_from, event_id, superseded_at, superseded_by)
	SELECT a.id, a.tenant_id, a.asset_id, m.key, m.value, m.field_number, %d,
		a.date_updated, a.date_created, a.valid_from, a.event_id, a.superseded_at, a.superseded_by
	FROM %s a JOIN %s m ON m.id = a.id`, pd.AssetDomain, factsAside, factsMoved), []any{}, &res); err != nil {
		return 0, 0, fmt.Errorf("the assets' facts, moved: %w", err)
	}
	if n, err := res.RowsAffected(); err != nil {
		return 0, 0, fmt.Errorf("the assets' facts, moved: %w", err)
	} else if int(n) != len(ids) {
		return 0, 0, fmt.Errorf("the assets' facts: %d were rewritten and %d moved", len(ids), n)
	}

	recorded := 0
	if len(ids) > 0 {
		content, err := s.Contents()
		if err != nil {
			return 0, 0, fmt.Errorf("the assets' facts, on the trail: %w", err)
		}
		db := ent.NewClient(ent.Driver(tdrv))
		for page := range slices.Chunk(ids, 5000) {
			n, err := pd.Record(ctx, db, content, MovedFacts, page)
			if err != nil {
				return 0, 0, fmt.Errorf("the assets' facts, on the trail: %w", err)
			}
			recorded += n
		}
	}

	for _, t := range []string{factsMoved, factsAside} {
		if err := tdrv.Exec(ctx, "DROP TABLE "+t, []any{}, nil); err != nil {
			return 0, 0, fmt.Errorf("the assets' facts: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, 0, err
	}

	return len(ids), recorded, nil
}

// idOfRow is a row's identifier as the database answered it: text, or the
// sixteen bytes.
func idOfRow(v any) (pdid.Id, error) {
	switch v := v.(type) {
	case string:
		return pdid.Parse(v)
	case []byte:
		if len(v) == 16 {
			return pdid.From(v)
		}
		return pdid.Parse(string(v))
	case [16]byte:
		return pdid.From(v[:])
	}
	return pdid.Nil, fmt.Errorf("an identifier of %T", v)
}

// refusedKey is a key a fact the past cannot write was under, as a refusal
// counts them: an attribute's as `attr.*`, which are many and somebody's
// words.
func refusedKey(k string) string {
	if strings.HasPrefix(k, "attr.") {
		return "attr.*"
	}
	return fmt.Sprintf("%q", k)
}

func sum(m map[string]int) int {
	n := 0
	for _, v := range m {
		n += v
	}
	return n
}
