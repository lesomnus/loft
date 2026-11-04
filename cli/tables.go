package cli

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/protobuf-orm/ent/dialect"

	"github.com/lesomnus/loft/cmd"
	"github.com/lesomnus/loft/server/offboard"
)

// errBoth is a database that holds a renamed table under both its names,
// each with rows in it: nothing says which of them is the table.
var errBoth = errors.New("the table is there under both its names, with rows in each")

// moveTables moves each table an earlier build made under another name
// ([offboard.Renamed]) onto the name this build makes it under, with its rows
// and what ent named after it, before anything else looks for it by name: the
// migration would make the new table, empty, and the rows would stay in the
// old one, read by nothing (D72). A table that is not there under its old
// name -- moved already, or never made -- is left alone, so it is the same
// call on a database of any build, as often as it is made.
//
// It is payday's `rename.Tables`, which `pd.TableRenames` will hand the same
// table once this app links the payday that has both (`(payday.entity).
// formerly`): this build's payday has neither, so the step is written here
// until then.
//
// What moves with the table is what ent named after it: its indexes
// (`<table>_<name>`), its primary key on PostgreSQL (`<table>_pkey`), its
// foreign keys (`<table>_<other>_<edge>`), and those of other tables that
// point at it (`<other>_<table>_<edge>`). On PostgreSQL each is a change to
// the catalog, all in one transaction, and nothing is built or copied. SQLite
// renames the table in place and points what pointed at it at the new name,
// and renames no index and no constraint: the old indexes are dropped, for
// the migration to make again under their new names, and each table whose
// foreign key keeps the old name is made again by the migration, rows and
// all, as it makes any table whose constraints changed.
//
// A table made under the new name beside the old one, empty -- a migration
// that ran first -- is dropped, and the old one moved onto its name; an empty
// one under the old name beside the new -- a build before, run again after
// this one moved it -- is dropped and nothing moved; and one with rows under
// both names is refused, and nothing is changed.
func moveTables(ctx context.Context, s *cmd.Server) error {
	for _, from := range slices.Sorted(maps.Keys(offboard.Renamed)) {
		to := offboard.Renamed[from]
		if err := moveTable(ctx, s.Db, s.Dialect, from, to); err != nil {
			return fmt.Errorf("table %s, which this build calls %s: %w", from, to, err)
		}
	}

	return nil
}

func moveTable(ctx context.Context, db *sql.DB, d, from, to string) error {
	if d != dialect.Postgres && d != dialect.SQLite {
		return fmt.Errorf("nothing here renames the tables of a %q database", d)
	}
	pg := d == dialect.Postgres

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	exists := func(name string) (bool, error) {
		q := `SELECT count(*) FROM sqlite_master WHERE type = 'table' AND name = ?`
		if pg {
			q = `SELECT count(*) FROM pg_tables WHERE schemaname = current_schema() AND tablename = $1`
		}
		n := 0
		err := tx.QueryRowContext(ctx, q, name).Scan(&n)
		return n > 0, err
	}
	if ok, err := exists(from); err != nil || !ok {
		return err
	}
	count := func(name string) (int, error) {
		n := 0
		err := tx.QueryRowContext(ctx, "SELECT count(*) FROM "+ident(name)).Scan(&n)
		return n, err
	}
	drop := func(name string) error {
		q := "DROP TABLE " + ident(name)
		if pg {
			// What points at it is what a migration pointed at it, which the
			// next one makes again at the table that stays.
			q += " CASCADE"
		}
		_, err := tx.ExecContext(ctx, q)
		return err
	}
	if ok, err := exists(to); err != nil {
		return err
	} else if ok {
		// Under both names, which is a migration of one build made beside
		// what another left: the empty one goes. One of rows under both is
		// for somebody to decide.
		nto, err := count(to)
		if err != nil {
			return err
		}
		nfrom, err := count(from)
		if err != nil {
			return err
		}
		switch {
		case nto == 0:
			if err := drop(to); err != nil {
				return fmt.Errorf("the empty %s a migration made beside it: %w", to, err)
			}
		case nfrom == 0:
			if err := drop(from); err != nil {
				return fmt.Errorf("the empty %s a build before made again: %w", from, err)
			}
			return tx.Commit()
		default:
			return fmt.Errorf("%w: %s and %s; move what one of them holds into the other, drop it, and migrate again", errBoth, from, to)
		}
	}

	if _, err := tx.ExecContext(ctx, "ALTER TABLE "+ident(from)+" RENAME TO "+ident(to)); err != nil {
		return err
	}
	renamed := func(name string) (string, bool) {
		rest, ok := strings.CutPrefix(name, from+"_")
		// PostgreSQL keeps 63 bytes of a name and drops the rest without a
		// word; ent makes a longer one of a digest this cannot guess, so the
		// migration makes that one again.
		return to + "_" + rest, ok && len(to+"_"+rest) <= 63
	}

	if !pg {
		idx, err := column(ctx, tx, `SELECT name FROM sqlite_master WHERE type = 'index' AND tbl_name = ? AND sql IS NOT NULL`, to)
		if err != nil {
			return err
		}
		for _, n := range idx {
			if _, ok := strings.CutPrefix(n, from+"_"); !ok {
				continue
			}
			if _, err := tx.ExecContext(ctx, "DROP INDEX "+ident(n)); err != nil {
				return fmt.Errorf("index %s: %w", n, err)
			}
		}

		return tx.Commit()
	}

	// Its indexes, the primary key's among them: renaming the index of a
	// constraint renames the constraint.
	idx, err := column(ctx, tx, `SELECT indexname FROM pg_indexes WHERE schemaname = current_schema() AND tablename = $1`, to)
	if err != nil {
		return err
	}
	for _, n := range idx {
		if v, ok := renamed(n); ok {
			if _, err := tx.ExecContext(ctx, "ALTER INDEX "+ident(n)+" RENAME TO "+ident(v)); err != nil {
				return fmt.Errorf("index %s: %w", n, err)
			}
		}
	}
	// Its foreign keys, and a check.
	own, err := column(ctx, tx, `
		SELECT c.conname FROM pg_constraint c
		JOIN pg_class t ON t.oid = c.conrelid
		JOIN pg_namespace n ON n.oid = t.relnamespace
		WHERE n.nspname = current_schema() AND t.relname = $1 AND c.contype IN ('f', 'c')`, to)
	if err != nil {
		return err
	}
	for _, n := range own {
		if v, ok := renamed(n); ok {
			if _, err := tx.ExecContext(ctx, "ALTER TABLE "+ident(to)+" RENAME CONSTRAINT "+ident(n)+" TO "+ident(v)); err != nil {
				return fmt.Errorf("constraint %s: %w", n, err)
			}
		}
	}
	// And those of the tables that point at it, named after both.
	rows, err := tx.QueryContext(ctx, `
		SELECT t.relname, c.conname FROM pg_constraint c
		JOIN pg_class t ON t.oid = c.conrelid
		JOIN pg_class r ON r.oid = c.confrelid
		JOIN pg_namespace n ON n.oid = r.relnamespace
		WHERE n.nspname = current_schema() AND r.relname = $1 AND c.contype = 'f' AND c.conrelid <> c.confrelid`, to)
	if err != nil {
		return err
	}
	type fk struct{ table, name string }
	fks := []fk{}
	for rows.Next() {
		var v fk
		if err := rows.Scan(&v.table, &v.name); err != nil {
			rows.Close()
			return err
		}
		fks = append(fks, v)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	for _, f := range fks {
		rest, ok := strings.CutPrefix(f.name, f.table+"_"+from+"_")
		v := f.table + "_" + to + "_" + rest
		if !ok || len(v) > 63 {
			continue
		}
		if _, err := tx.ExecContext(ctx, "ALTER TABLE "+ident(f.table)+" RENAME CONSTRAINT "+ident(f.name)+" TO "+ident(v)); err != nil {
			return fmt.Errorf("constraint %s of %s: %w", f.name, f.table, err)
		}
	}

	return tx.Commit()
}

// column is the first column of what `q` answers.
func column(ctx context.Context, tx *sql.Tx, q string, args ...any) ([]string, error) {
	rows, err := tx.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []string{}
	for rows.Next() {
		var v string
		if err := rows.Scan(&v); err != nil {
			return nil, err
		}
		out = append(out, v)
	}

	return out, rows.Err()
}

// ident writes a name as an identifier, which both databases read the same.
func ident(name string) string {
	return `"` + strings.ReplaceAll(name, `"`, `""`) + `"`
}
