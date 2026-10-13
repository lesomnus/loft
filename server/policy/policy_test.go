package policy

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/lesomnus/payday/pdpb"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"
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

// TestNothingBeyondTheWallIsInTheTable: an entity that says `global: {}` has
// no wall in front of it -- its table is every tenant's rows at once -- so a
// method of its service in the table would hand all of them to whoever the
// table lets in. None is, and none may be: what those entities are for, the
// deployment does through the ungated server (payday's permissions guide,
// §13: "grep for it and read the list"). Read off the schema rather than a
// list here, so that a global entity added later is held to it too.
func TestNothingBeyondTheWallIsInTheTable(t *testing.T) {
	global := []string{}
	protoregistry.GlobalFiles.RangeFiles(func(fd protoreflect.FileDescriptor) bool {
		if fd.Package() != "rove" {
			return true
		}
		for i := 0; i < fd.Messages().Len(); i++ {
			md := fd.Messages().Get(i)
			if e, _ := proto.GetExtension(md.Options(), pdpb.E_Entity).(*pdpb.Entity); e.GetGlobal() != nil {
				global = append(global, "/"+string(md.FullName())+"Service/")
			}
		}
		return true
	})
	sort.Strings(global)
	// Sessions, terminal sign-ins, held addresses, consents, sign-in failures,
	// and payday's outbox and archive manifest: if this is shorter, the
	// registry was read before the schema was linked, and the test says
	// nothing.
	require.GreaterOrEqual(t, len(global), 7, "the global entities: %v", global)

	for m := range Table {
		for _, g := range global {
			require.False(t, strings.HasPrefix(m, g), "%s is a global entity's, which the wall does not narrow", m)
		}
	}
}
