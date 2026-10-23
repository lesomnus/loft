package domain

import (
	"bufio"
	"fmt"
	"maps"
	"os"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"

	"github.com/lesomnus/rove/server/pd"
)

// What `rove/past.proto` says beside its messages, which the schema has no
// place for (payday#151), and which this holds to the schema and to what is
// written:
//
// Each domain's table of fields, at the head of the file -- every message of
// the domain takes what it holds from it, under the same name and number, of
// the same type:
//
//	// asset.* holds, by number:
//	//
//	//    1  asset          bytes                      -- the asset
//	//    2  tag            string                     -- what the asset was tagged then
//
// And, beside a message that holds several kinds, the fields each of them
// uses, a kind and its fields on a line of their own, indented, a list that
// ends in a comma going on to the next line:
//
//	//   stock.receive  stock, name, delta
//	//   stock.consume  stock, name, delta
//
// A message of one kind lists nothing: its kind uses all of it.

// field is a field of a domain's table.
type field struct {
	number int
	name   string
	typ    string
}

// notes is what `rove/past.proto` says beside its messages: each domain's
// table, by domain, and what each kind of a message of several uses of it,
// by kind. Read once.
type notes struct {
	tables map[string][]field
	uses   map[string][]string
}

var readNotes = sync.OnceValues(func() (notes, error) {
	out := notes{tables: map[string][]field{}, uses: map[string][]string{}}

	f, err := os.Open("../../proto/rove/past.proto")
	if err != nil {
		return out, err
	}
	defer f.Close()

	var twice []string
	// The table being read, and the kind whose list is.
	var domain, kind string
	var list []string
	flush := func() {
		if kind != "" {
			if _, ok := out.uses[kind]; ok {
				twice = append(twice, kind)
			}
			out.uses[kind] = list
		}
		kind, list = "", nil
	}
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := sc.Text()
		if !strings.HasPrefix(line, "//") {
			domain = ""
		}
		if m := tableOf.FindStringSubmatch(line); m != nil {
			if _, ok := out.tables[m[1]]; ok {
				twice = append(twice, m[1]+".*")
			}
			domain = m[1]
			out.tables[domain] = []field{}
			continue
		}
		if domain != "" {
			if m := tableEntry.FindStringSubmatch(line); m != nil {
				n, _ := strconv.Atoi(m[1])
				out.tables[domain] = append(out.tables[domain], field{n, m[2], m[3]})
			}
			continue
		}

		if kind != "" {
			// A list that ended in a comma goes on here.
			if m := more.FindStringSubmatch(line); m != nil {
				list = append(list, fieldsOf(m[1])...)
				if !strings.HasSuffix(m[1], ",") {
					flush()
				}
				continue
			}
			flush()
		}
		if m := listed.FindStringSubmatch(line); m != nil {
			kind, list = m[1], fieldsOf(m[2])
			if !strings.HasSuffix(m[2], ",") {
				flush()
			}
		}
	}
	flush()
	if len(twice) > 0 {
		return out, fmt.Errorf("said twice: %s", strings.Join(twice, ", "))
	}
	return out, sc.Err()
})

var (
	// tableOf is the line a domain's table begins with.
	tableOf = regexp.MustCompile(`^// ([a-z_]+)\.\* holds, by number:$`)
	// tableEntry is a field of a table: its number, its name, its type, and
	// what it is after `--`.
	tableEntry = regexp.MustCompile(`^//\s+(\d+)\s+([a-z_]+)\s+(\S.*?)(?:\s+--\s.*)?\s*$`)
	// listed is a kind's line: a comment indented by more than a space, the
	// kind, and the fields it uses.
	listed = regexp.MustCompile(`^//\s{2,}([a-z_]+\.[a-z_]+)\s+([a-z_]+(?:,\s*[a-z_]+)*,?)\s*$`)
	// more is the rest of a list that ended in a comma.
	more = regexp.MustCompile(`^//\s{2,}([a-z_]+(?:,\s*[a-z_]+)*,?)\s*$`)
)

func fieldsOf(v string) []string {
	var out []string
	for f := range strings.SplitSeq(v, ",") {
		if f = strings.TrimSpace(f); f != "" {
			out = append(out, f)
		}
	}
	return out
}

// typeOf is a field's type as a table writes it: `bytes`,
// `google.protobuf.Timestamp`, `repeated string`, `map<string, uint32>`.
func typeOf(fd protoreflect.FieldDescriptor) string {
	if fd.IsMap() {
		return "map<" + typeOf(fd.MapKey()) + ", " + typeOf(fd.MapValue()) + ">"
	}
	var t string
	switch fd.Kind() {
	case protoreflect.MessageKind, protoreflect.GroupKind:
		t = string(fd.Message().FullName())
	case protoreflect.EnumKind:
		t = string(fd.Enum().FullName())
	default:
		t = fd.Kind().String()
	}
	if fd.IsList() {
		return "repeated " + t
	}
	return t
}

// kindsOf is the kinds each event message holds, by its full name.
func kindsOf() map[protoreflect.FullName][]string {
	out := map[protoreflect.FullName][]string{}
	for _, k := range slices.Sorted(maps.Keys(pd.EventKinds)) {
		name := pd.EventKinds[k].Descriptor().FullName()
		out[name] = append(out[name], k)
	}
	return out
}

// Beyond is what `v`, an event of `kind`, holds that its kind does not use:
// of a message of several kinds, the fields it sets that the kind's list in
// `rove/past.proto` does not name. A message of one kind is all its kind's.
// For the tests outside the package, which hold every event they record to
// it.
func Beyond(kind string, v proto.Message) []string {
	s, err := readNotes()
	if err != nil {
		return []string{err.Error()}
	}
	if len(kindsOf()[v.ProtoReflect().Descriptor().FullName()]) < 2 {
		return nil
	}
	uses, ok := s.uses[kind]
	if !ok {
		return []string{kind + " is listed nowhere"}
	}
	var out []string
	v.ProtoReflect().Range(func(fd protoreflect.FieldDescriptor, _ protoreflect.Value) bool {
		if !slices.Contains(uses, string(fd.Name())) {
			out = append(out, string(fd.Name()))
		}
		return true
	})
	return out
}

func beyond(kind string, v proto.Message) []string { return Beyond(kind, v) }

// TestEachDomainHoldsItsFieldsAsItsTableSays: every event message holds what
// it holds as its domain's table says -- each field the table's of that
// name, at its number, of its type -- and every field of a table is one a
// message holds. A table is a domain's that kinds are of, and says a name and
// a number once each.
func TestEachDomainHoldsItsFieldsAsItsTableSays(t *testing.T) {
	x := require.New(t)
	s, err := readNotes()
	x.NoError(err)

	domains := map[string]bool{}
	for k := range pd.EventKinds {
		domains[strings.SplitN(k, ".", 2)[0]] = true
	}
	x.ElementsMatch(slices.Collect(maps.Keys(domains)), slices.Collect(maps.Keys(s.tables)), "a table for each domain kinds are of, and none for another")

	for d, table := range s.tables {
		x.NotEmpty(table, "%s.*: its table", d)
		byNumber, byName := map[int]field{}, map[string]field{}
		for _, f := range table {
			x.NotContains(byNumber, f.number, "%s.*: %d is said twice", d, f.number)
			x.NotContains(byName, f.name, "%s.*: %s is said twice", d, f.name)
			byNumber[f.number], byName[f.name] = f, f
		}

		held := map[string]bool{}
		for name, kinds := range kindsOf() {
			if !slices.ContainsFunc(kinds, func(k string) bool { return strings.HasPrefix(k, d+".") }) {
				continue
			}
			fs := pd.EventKinds[kinds[0]].Descriptor().Fields()
			for i := range fs.Len() {
				fd := fs.Get(i)
				f, ok := byName[string(fd.Name())]
				x.True(ok, "%s.%s is in no table of %s.*", name, fd.Name(), d)
				x.Equal(f.number, int(fd.Number()), "%s.%s: its number, as %s.* says it", name, fd.Name(), d)
				x.Equal(f.typ, typeOf(fd), "%s.%s: its type, as %s.* says it", name, fd.Name(), d)
				held[f.name] = true
			}
		}
		for _, f := range table {
			x.True(held[f.name], "%s.*: %s is a field no message of the domain holds", d, f.name)
		}
	}
}

// TestEachKindOfASharedMessageSaysWhatItUses: a message that holds several
// kinds lists each of them beside it, once, with the fields it uses -- fields
// the message has, each once -- and every field of it is used by one of its
// kinds at least. A message of one kind lists nothing.
func TestEachKindOfASharedMessageSaysWhatItUses(t *testing.T) {
	x := require.New(t)
	s, err := readNotes()
	x.NoError(err)

	for name, kinds := range kindsOf() {
		md := pd.EventKinds[kinds[0]].Descriptor()
		if len(kinds) == 1 {
			_, ok := s.uses[kinds[0]]
			x.False(ok, "%s holds one kind, which uses all of it, and lists nothing", name)
			continue
		}

		used := map[protoreflect.Name]bool{}
		for _, k := range kinds {
			list, ok := s.uses[k]
			x.True(ok, "%s: %s is not listed beside it", name, k)
			x.Len(slices.Compact(slices.Sorted(slices.Values(list))), len(list), "%s lists a field twice", k)
			for _, f := range list {
				x.NotNil(md.Fields().ByName(protoreflect.Name(f)), "%s uses %s, which %s has no field of", k, f, name)
				used[protoreflect.Name(f)] = true
			}
		}
		for i := range md.Fields().Len() {
			f := md.Fields().Get(i).Name()
			x.True(used[f], "%s.%s is used by none of its kinds", name, f)
		}
	}
	for k := range s.uses {
		x.Contains(pd.EventKinds, k, "%s is listed, and no message holds it", k)
	}
}
