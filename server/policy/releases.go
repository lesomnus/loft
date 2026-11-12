package policy

import (
	"bufio"
	"embed"
	"fmt"
	"io/fs"
	"path"
	"slices"
	"strings"
)

// Release is what one release of Loft gave the roles it ships (#327's
// decision 33, D90): each method it added to them, and which of them it went
// to.
type Release struct {
	// Name is its place in the ledger, `0001` and on, and Desc what it was
	// for, as its file's first line says.
	Name, Desc string
	// Adds is what it gave each role, by the role's alias, in order.
	Adds map[string][]string
}

// Ledger is releases, in the order they shipped.
type Ledger []Release

// Releases is every release that gave the roles Loft ships anything, in
// order: `releases/*.txt`, a file each. The first is what they held when
// roster first held them; each after it, what a later one added. A tenant's
// roles have had release n when they were given what the first n gave
// (`Tenant.roles_release`): `loft init` writes them as [Roles] holds them and
// records the last, and they are brought from n to the last by what each
// release after n added ([Ledger.Since]) -- never by what [Roles] holds by
// then, which would give back what the tenant took out of a role at roster.
//
// A release that shipped is never written again, since a tenant recorded at
// it has had it: what a role is given later is a release of its own, which
// `go test ./server/policy -update` writes from what [Roles] holds and no
// release gave. Nothing here takes a method away from a role -- a tenant's
// roles are the tenant's -- so a role holding less than its releases gave
// fails that test, and is a decision to make rather than a line to write.
var Releases Ledger

//go:embed releases/*.txt
var releaseFiles embed.FS

func init() {
	names, err := fs.Glob(releaseFiles, "releases/*.txt")
	if err != nil {
		panic(err)
	}
	slices.Sort(names)
	for i, name := range names {
		want := fmt.Sprintf("releases/%04d.txt", i+1)
		if name != want {
			panic(fmt.Sprintf("policy: %s where %s was to be: the releases are numbered from 0001, each once", name, want))
		}
		b, err := releaseFiles.ReadFile(name)
		if err != nil {
			panic(err)
		}
		r, err := parseRelease(strings.TrimSuffix(path.Base(name), ".txt"), string(b))
		if err != nil {
			panic(fmt.Sprintf("policy: %s: %v", name, err))
		}
		Releases = append(Releases, r)
	}
}

// parseRelease reads a release written as `testdata/roles.txt` is -- a method
// and the roles it went to, by the word each alias is made of, a line each --
// under a first line that says what it was for.
func parseRelease(name, text string) (Release, error) {
	r := Release{Name: name, Adds: map[string][]string{}}
	sc := bufio.NewScanner(strings.NewReader(text))
	for n := 1; sc.Scan(); n++ {
		line := strings.TrimSpace(sc.Text())
		switch {
		case line == "":
			continue
		case strings.HasPrefix(line, "#"):
			if r.Desc == "" {
				r.Desc = strings.TrimSpace(strings.TrimPrefix(line, "#"))
			}
			continue
		}
		method, to, ok := strings.Cut(line, "\t")
		if !ok || !strings.HasPrefix(method, "/loft.") || strings.TrimSpace(to) == "" {
			return Release{}, fmt.Errorf("line %d: a method, a tab, and the roles it went to: %q", n, line)
		}
		for _, word := range strings.Fields(to) {
			alias := "loft-" + word
			if !slices.Contains(r.Adds[alias], method) {
				r.Adds[alias] = append(r.Adds[alias], method)
			}
		}
	}
	return r, sc.Err()
}

// Latest is how many releases there are: what `loft init` records of the
// roles it writes, and what every tenant's roles are brought to.
func (l Ledger) Latest() uint32 { return uint32(len(l)) }

// Since is what the releases after the first `n` gave each role, by its
// alias, each method once and in order -- of what may still be given
// ([Grantable]): a method gone from Loft is nobody's to be given.
func (l Ledger) Since(n uint32) map[string][]string {
	out := map[string][]string{}
	for i := int(n); i < len(l); i++ {
		for alias, ms := range l[i].Adds {
			for _, m := range ms {
				if Grantable(m) && !slices.Contains(out[alias], m) {
					out[alias] = append(out[alias], m)
				}
			}
		}
	}
	for alias := range out {
		slices.Sort(out[alias])
	}
	return out
}
