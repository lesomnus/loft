package domain

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// TestATagOfTheShapeAMadeUpOneHasIsKnown: what a counter could make up, and
// nothing else, holds a counter when somebody writes it (D52).
func TestATagOfTheShapeAMadeUpOneHasIsKnown(t *testing.T) {
	x := require.New(t)
	for tag, want := range map[string]string{
		"A-00007":  "A",
		"S-00001":  "S",
		"K-12345":  "K",
		"G-99999":  "G",
		"A-100000": "A",
		"A-7":      "",
		"A-000007": "",
		"A-00000":  "",
		"A-0000x":  "",
		"A-+0007":  "",
		"a-00007":  "",
		"NB-00007": "",
		"A00007":   "",
		"A-":       "",
		"":         "",
	} {
		got, ok := madeUpPrefix(tag)
		x.Equal(want, got, "%q", tag)
		x.Equal(want != "", ok, "%q", tag)
	}
	for kind, want := range map[string]string{"item": "A-00042", "space": "S-00042", "kit": "K-00042", "group": "G-00042", "boat": "A-00042"} {
		x.Equal(want, madeUpTag(tagPrefix(kind), 42), kind)
	}
}
