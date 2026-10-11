package tenancy

import (
	"context"
	"strings"
	"testing"

	"github.com/lesomnus/payday/slug"
	"github.com/stretchr/testify/require"
)

// TestAnAddressIsReadableAndAnAlias: seven characters, a letter first, none
// read for another, and payday's grammar for an alias.
func TestAnAddressIsReadableAndAnAlias(t *testing.T) {
	x := require.New(t)
	for range 2000 {
		v := NewAlias(7)
		x.Len(v, 7)
		x.NoError(slug.Validate(v))
		x.Contains(aliasFirst, v[:1], "a letter first")
		x.False(strings.ContainsAny(v, "ilo01"), "%s has a character read for another", v)
	}
}

// TestACrowdedLengthGrows: ten made up in a row that are taken make the next
// one a character longer, by itself.
func TestACrowdedLengthGrows(t *testing.T) {
	x := require.New(t)
	tried := map[int]int{}
	a := Aliases{Length: 7}
	v, err := a.make(context.Background(), func(_ context.Context, v string) (bool, error) {
		tried[len(v)]++
		return len(v) > 7, nil
	})
	x.NoError(err)
	x.Len(v, 8)
	x.Equal(collisions, tried[7])
	x.Equal(1, tried[8])
}

// TestAnOperatorsAddressIsChecked: what an operator may move a workspace to.
func TestAnOperatorsAddressIsChecked(t *testing.T) {
	x := require.New(t)
	a := Aliases{Reserved: []string{"acme-corp"}}
	x.NoError(a.Valid("lab-2"))
	for _, v := range []string{"ab", "-lab", "lab-", "Lab", "2lab", "admin", "acme-corp", strings.Repeat("a", 31)} {
		x.ErrorIs(a.Valid(v), ErrAlias, v)
	}
}
