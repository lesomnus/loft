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

// TestASingleLetterGoesByPrefix -- counted in letters, not bytes.
func TestASingleLetterGoesByPrefix(t *testing.T) {
	x := require.New(t)
	x.True(search.ByPrefix("의"))
	x.True(search.ByPrefix("m"))
	x.False(search.ByPrefix("의자"))
	x.False(search.ByPrefix("노트북"))
}

// TestALikePatternMatchesWhatWasTyped: `%`, `_` and the escape itself are
// letters a person typed.
func TestALikePatternMatchesWhatWasTyped(t *testing.T) {
	x := require.New(t)
	x.Equal(`100\%`, search.Like("100%"))
	x.Equal(`a\_b`, search.Like("a_b"))
	x.Equal(`c:\\tmp`, search.Like(`c:\tmp`))
}
