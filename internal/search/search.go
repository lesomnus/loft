// Package search is how the database compares what a person types with what
// is kept, in a deployment that names no search engine: a checkout, or one
// small enough not to need it (D31). Every row of the tenant is read. A
// deployment that wants an index names OpenSearch (`server/opensearch`); the
// database's search is not made any faster.
//
// # Folded
//
// NFKC, then lower case -- `lower(normalize(x, NFKC))` on PostgreSQL, [Fold]
// here -- so that a full-width `ＡＢＣ` and a half-width `ｶﾒﾗ` are found by
// `abc` and `カメラ`. Hiragana and katakana are not one another, and neither
// are simplified and traditional Chinese: that takes a dictionary, not a
// normal form. The search engine is handed text folded the same way.
//
// # Short
//
// A single letter goes by prefix, on the tag and the name: one found in the
// middle of every name is not a search anybody meant.
package search

import (
	"strings"
	"unicode/utf8"

	"golang.org/x/text/unicode/norm"
)

// Fold is text as it is compared.
func Fold(s string) string { return strings.ToLower(norm.NFKC.String(s)) }

// ByPrefix says whether a folded query is a single letter, and so goes by
// prefix -- counted in letters, not bytes.
func ByPrefix(q string) bool { return utf8.RuneCountInString(q) < 2 }

// Like is a folded query as a LIKE pattern matches it literally, under
// `ESCAPE '\'`: a person who types `100%` means the percent sign.
func Like(q string) string {
	return strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(q)
}
