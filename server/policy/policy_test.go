package policy

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

var update = flag.Bool("update", false, "write testdata/table.txt from the table as it is")

var levelWord = map[Level]string{
	Nobody:  "nobody",
	Account: "account",
	Read:    "read",
	Member:  "member",
	Manager: "manager",
	Admin:   "admin",
	Owner:   "owner",
}

// TestTheTableIsWhatItWasReviewedAs is the whole role × method table written
// out (`testdata/table.txt`): every method somebody may call, and the least
// role that may -- `read` is everybody signed in, auditors included, and
// `+auditor` an auditor let in below it. A change to who may call what is a
// change to that file, which a review reads line by line; `go test
// ./server/policy -update` writes it again. That the served stack does what it
// says, for every method and every role, is `cmd`'s
// TestEveryMethodIsAsTheTableSaysForEveryRole.
func TestTheTableIsWhatItWasReviewedAs(t *testing.T) {
	lines := []string{}
	for m, r := range Table {
		who := levelWord[r.min]
		if r.auditor {
			who += " +auditor"
		}
		lines = append(lines, fmt.Sprintf("%s\t%s", m, who))
	}
	sort.Strings(lines)
	got := strings.Join(lines, "\n") + "\n"

	path := filepath.Join("testdata", "table.txt")
	if *update {
		require.NoError(t, os.WriteFile(path, []byte(got), 0o644))
		return
	}
	want, err := os.ReadFile(path)
	require.NoError(t, err, "go test ./server/policy -update")
	require.Equal(t, string(want), got, "the table moved; read the diff, then -update")
}
