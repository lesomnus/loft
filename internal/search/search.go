// Package search is how what a person types is compared with what is kept:
// folded the same way on both sides, and answered by whichever index the
// database has for it (design 9.5).
//
// # Folded
//
// NFKC, then lower case -- `lower(normalize(x, NFKC))` on PostgreSQL, [Fold]
// here -- so that a full-width `ＡＢＣ` and a half-width `ｶﾒﾗ` are found by
// `abc` and `カメラ`. Hiragana and katakana are not one another, and neither
// are simplified and traditional Chinese: that takes a dictionary, not a
// normal form.
//
// # Answered
//
// pg_bigm cuts the text into pairs of letters, which is what makes `의자` --
// two syllables, and the name of half the furniture there is -- a question an
// index answers. pg_trgm, which every PostgreSQL has, cuts it into threes and
// so answers nothing shorter. Below what the index can answer, a search goes
// by prefix, on the tag and the name, which a B-tree answers instead: a single
// letter found in the middle of every name is not a search anybody meant.
package search

import (
	"context"
	"database/sql"
	"strings"
	"unicode/utf8"

	"golang.org/x/text/unicode/norm"
)

// Mode is what the database has to answer a search with.
type Mode string

const (
	// Bigram is pg_bigm: from two letters by its index, one by prefix.
	Bigram Mode = "pg_bigm"

	// Trigram is pg_trgm, where pg_bigm is not installed (Azure, Naver Cloud
	// and NHN Cloud offer none): from three letters by its index, one or two
	// by prefix.
	Trigram Mode = "pg_trgm"

	// Plain is no index for it -- SQLite, or a PostgreSQL with neither -- and
	// every row read. It goes by prefix where a bigram index would.
	Plain Mode = ""
)

// Fold is text as it is compared.
func Fold(s string) string { return strings.ToLower(norm.NFKC.String(s)) }

// ByPrefix says whether a folded query is shorter than what the mode's index
// answers, and so goes by prefix.
func (m Mode) ByPrefix(q string) bool {
	n := utf8.RuneCountInString(q)
	if m == Trigram {
		return n < 3
	}

	return n < 2
}

// Like is a folded query as a LIKE pattern matches it literally, under
// `ESCAPE '\'`: a person who types `100%` means the percent sign.
func Like(q string) string {
	return strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(q)
}

// Detect is which of the two this PostgreSQL has: what the migration made
// (`cli/pg.sql`), read back.
func Detect(ctx context.Context, db *sql.DB) (Mode, error) {
	rows, err := db.QueryContext(ctx, "SELECT extname FROM pg_extension WHERE extname IN ('pg_bigm', 'pg_trgm')")
	if err != nil {
		return Plain, err
	}
	defer rows.Close()

	has := map[string]bool{}
	for rows.Next() {
		var v string
		if err := rows.Scan(&v); err != nil {
			return Plain, err
		}
		has[v] = true
	}
	if err := rows.Err(); err != nil {
		return Plain, err
	}

	switch {
	case has[string(Bigram)]:
		return Bigram, nil
	case has[string(Trigram)]:
		return Trigram, nil
	default:
		return Plain, nil
	}
}

// Says is the mode as the log line a deployment comes up with.
func (m Mode) Says(postgres bool) string {
	switch {
	case m == Bigram:
		return "pg_bigm answers from two letters, and one by prefix"
	case m == Trigram:
		return "pg_bigm is not installed: pg_trgm answers from three letters, and one or two by prefix"
	case postgres:
		return "neither pg_bigm nor pg_trgm is installed: every search reads every row"
	default:
		return "SQLite: every search reads every row"
	}
}
