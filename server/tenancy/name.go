package tenancy

import (
	"context"
	"strings"
	"unicode"
	"unicode/utf8"
	"uuid"

	"github.com/lesomnus/loft/internal/ent"
)

// What a workspace is called (D57).
//
// A workspace is called by its own name, which whoever put it up chose (`loft
// init --name`, or the tenant's at roster) or its owner gave it since, and
// which is nobody's: the name of a thing, which payday's Tenant declares about
// no one (payday#180) and the trail keeps as a value, as it keeps any thing's
// (design 8.2). One put up with neither has none, and is called by its
// address, which is the tenant's at roster (D86).
//
// It was called after its first owner, "<이름>의 워크스페이스", while Loft
// knew who that was. Who holds the owner's role is roster's now (D89), and a
// name said after somebody would be roster's binding read at every page and
// in every mail.

// MaxName is how long a workspace's own name may be, in characters.
const MaxName = 80

// Fits says whether `v` may be what a workspace is called: one line -- it is
// the subject of the mails that name it -- of up to [MaxName] characters.
// Empty fits, and is none of its own.
func Fits(v string) bool {
	return utf8.RuneCountInString(v) <= MaxName && !strings.ContainsFunc(v, unicode.IsControl)
}

// Name is what a workspace is called where it is read.
type Name struct {
	// Said is what it is called: its own name, or its address.
	Said string
}

// Names is what each of `ts` is called, by its identifier.
func Names(_ context.Context, _ *ent.Client, ts ...*ent.Tenant) (map[uuid.UUID]Name, error) {
	out := make(map[uuid.UUID]Name, len(ts))
	for _, t := range ts {
		if t == nil {
			continue
		}
		out[t.Id] = Name{Said: or(t.Name, t.Alias)}
	}
	return out, nil
}

// NameOf is what one workspace is called ([Names]).
func NameOf(ctx context.Context, db *ent.Client, t *ent.Tenant) (Name, error) {
	vs, err := Names(ctx, db, t)
	if err != nil {
		return Name{}, err
	}

	return vs[t.Id], nil
}
