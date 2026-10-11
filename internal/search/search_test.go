package search_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/lesomnus/rove/internal/search"
)

// TestTextIsFoldedAsTheDatabaseFoldsIt: NFKC, then lower case, which is what
// `lower(normalize(x, NFKC))` does to what is kept.
func TestTextIsFoldedAsTheDatabaseFoldsIt(t *testing.T) {
	x := require.New(t)
	x.Equal("abc 카메라", search.Fold("ＡＢＣ 카메라"), "full-width Latin")
	x.Equal("カメラ", search.Fold("ｶﾒﾗ"), "half-width katakana")
	x.Equal("100%", search.Fold("１００%"))
	x.Equal("ch-001", search.Fold("CH-001"))
	x.Equal("의자", search.Fold("의자"), "Hangul as it is")
}

// TestWhatIsShortGoesByPrefix: one letter for pg_bigm and for no index, one or
// two for pg_trgm -- counted in letters, not bytes.
func TestWhatIsShortGoesByPrefix(t *testing.T) {
	x := require.New(t)
	for _, m := range []search.Mode{search.Bigram, search.Plain} {
		x.True(m.ByPrefix("의"), m)
		x.False(m.ByPrefix("의자"), m)
	}
	x.True(search.Trigram.ByPrefix("의자"))
	x.False(search.Trigram.ByPrefix("노트북"))
	x.True(search.Trigram.ByPrefix("m"))
}

// TestALikePatternMatchesWhatWasTyped: `%`, `_` and the escape itself are
// letters a person typed.
func TestALikePatternMatchesWhatWasTyped(t *testing.T) {
	x := require.New(t)
	x.Equal(`100\%`, search.Like("100%"))
	x.Equal(`a\_b`, search.Like("a_b"))
	x.Equal(`c:\\tmp`, search.Like(`c:\tmp`))
}
