// Package offboard is what leaves with a tenant: everything it has, written
// out, and then everything it has, gone (design 8.2, "테넌트 탈퇴").
//
// The order is the design's -- export, a grace, then the purge -- and it is the
// operator's to keep: `rove tenant export`, and `rove tenant purge` once the
// grace an offboarding gives is over. A tenant under a legal hold is refused
// both the purge and nothing else; its export is exactly what a hold is for.
//
// Both work on the database directly, through the deployment's own client and
// never a request: no role in a tenant may take everything out of it, and a
// purge goes through rows no server would let anybody erase.
package offboard

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"time"

	"github.com/lesomnus/payday/pdid"
	"github.com/lesomnus/payday/trail"
	"github.com/protobuf-orm/ent/dialect"
	"github.com/protobuf-orm/ent/dialect/sql/schema"

	"github.com/lesomnus/rove/internal/ent"
	"github.com/lesomnus/rove/internal/ent/migrate"
	"github.com/lesomnus/rove/server/retention"
	"github.com/lesomnus/rove/server/storage"
)

// Deployment is what an offboarding works on.
type Deployment struct {
	Ent   *ent.Client
	Drv   dialect.Driver
	Files storage.Store

	// Trail is the trail's policy with every tenant's holds answered, as
	// `retention.Trail` answers them; its archive is read for an export and
	// erased for a purge.
	Trail trail.Policy

	// Retention is what a tenant with no contract gets, for its holds and
	// windows.
	Retention retention.Defaults

	Now func() time.Time
}

func (d Deployment) now() time.Time {
	if d.Now == nil {
		return time.Now()
	}
	return d.Now()
}

// held refuses a tenant a legal hold is on.
func (d Deployment) held(ctx context.Context, tenant pdid.Id) error {
	w, err := retention.Of(ctx, d.Ent, tenant, d.now(), d.Retention)
	if err != nil {
		return err
	}
	if w.Held() {
		return fmt.Errorf("%w: %v", trail.ErrHeld, w.Holds)
	}
	return nil
}

// Trail is the trail's tables: its rows, and the manifest of its archive.
var Trail = []string{"audit", "archived"}

// Shared is the deployment's own tables, which no tenant has rows in: sessions
// and agreements are an account's, and an account is in no workspace (D27);
// a failed sign-in is an address's, before anybody is anybody.
var Shared = []string{"session", "clilogin", "aliashold", "consent", "signinfailure"}

// Tables is every table a tenant has rows in, each before every table it
// points at, so that deleting in this order leaves no reference dangling. The
// tenant's own row is not one of them, and is last; nor are the trail's, which
// are the trail's to purge (`trail.Policy.PurgeTenant`): its rows, and the
// account the database keeps of its archive.
//
// It is read off the schema rather than written out, so that an entity added
// later is purged without anybody remembering to.
func Tables() ([]string, error) {
	refs := map[string][]string{}
	for _, t := range migrate.Tables {
		if t.Name == "tenant" || slices.Contains(Trail, t.Name) || slices.Contains(Shared, t.Name) {
			continue
		}
		if !slices.ContainsFunc(t.Columns, func(c *schema.Column) bool { return c.Name == "tenant_id" }) {
			return nil, fmt.Errorf("%s has no tenant_id: a purge cannot tell whose its rows are", t.Name)
		}
		refs[t.Name] = nil
		for _, fk := range t.ForeignKeys {
			if r := fk.RefTable.Name; r != t.Name && r != "tenant" {
				refs[t.Name] = append(refs[t.Name], r)
			}
		}
	}

	out := []string{}
	left := slices.Sorted(maps.Keys(refs))
	for len(left) > 0 {
		next := []string{}
		for _, t := range left {
			// Anything still to go that points at this goes first.
			pointed := slices.ContainsFunc(left, func(o string) bool { return o != t && slices.Contains(refs[o], t) })
			if pointed {
				next = append(next, t)
				continue
			}
			out = append(out, t)
		}
		if len(next) == len(left) {
			return nil, fmt.Errorf("the tables %v point at each other", next)
		}
		left = next
	}

	return out, nil
}
