package tenancy

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/lesomnus/otx/log"
	"github.com/lesomnus/payday/slug"

	"github.com/lesomnus/loft/internal/ent"
	"github.com/lesomnus/loft/internal/ent/aliashold"
	enttenant "github.com/lesomnus/loft/internal/ent/tenant"
)

// A workspace's address: `/@<alias>` (design 9.11, D28, D29).
//
// Nobody chooses one. A workspace is given seven characters at random, and
// keeps them: the owner cannot change them (the policy opens no write of it,
// `server/domain`), and an operator can (`loft tenant alias`), which holds the
// old one for a year and sends whoever arrives at it on to the new one.

// The characters an address is made up of: lower case letters and digits, less
// the ones read for each other -- i, l and o, 0 and 1 -- since an address is
// read aloud and typed back in. The first is a letter, which is payday's
// grammar for an alias and keeps one from looking like a number.
const (
	aliasFirst = "abcdefghjkmnpqrstuvwxyz"
	aliasRest  = aliasFirst + "23456789"
)

// Reserved is what no workspace may be called: the paths this app answers
// itself, and names that would read as the deployment's own. A deployment adds
// its own (`app.workspaces.reserved`).
var Reserved = []string{
	"account", "accounts", "admin", "api", "app", "billing", "cli", "docs",
	"files", "healthz", "help", "internal", "invitations", "invite", "l",
	"login", "logout", "new", "operator", "root", "loft", "scan", "session",
	"sessions", "settings", "sso", "static", "status", "support", "system",
	"www",
}

// ErrAlias is an address a workspace may not have.
var ErrAlias = errors.New("not an address a workspace may have")

// collisions is how many made-up addresses in a row may be taken before the
// next is made a character longer: when that many meet, the length is
// crowded, and longer is how it stops being.
const collisions = 10

// NewAlias is an address of n characters, at random.
func NewAlias(n int) string {
	pick := func(from string) byte {
		// Bytes past the last whole multiple of the alphabet are thrown
		// away, so that every character is as likely as every other.
		limit := 256 - 256%len(from)
		b := make([]byte, 1)
		for {
			if _, err := rand.Read(b); err != nil {
				panic(err)
			}
			if int(b[0]) < limit {
				return from[int(b[0])%len(from)]
			}
		}
	}
	out := make([]byte, n)
	out[0] = pick(aliasFirst)
	for i := 1; i < n; i++ {
		out[i] = pick(aliasRest)
	}
	return string(out)
}

// Aliases is what decides a workspace's address: which are reserved, which are
// held, how long a new one is.
type Aliases struct {
	Db *ent.Client
	// Length is how long a new one starts; Reserved the deployment's own
	// beside [Reserved].
	Length   int
	Reserved []string
	Now      func() time.Time
}

func (a Aliases) now() time.Time {
	if a.Now != nil {
		return a.Now()
	}
	return time.Now()
}

// Valid says whether an address may be a workspace's -- one an operator
// chose: payday's grammar for an alias, three to thirty characters, and none
// this app or the deployment keeps. Whether another workspace has it, or it is
// held, is [Aliases.Free]'s.
func (a Aliases) Valid(v string) error {
	if err := slug.Validate(v); err != nil {
		return fmt.Errorf("%w: %v", ErrAlias, err)
	}
	if n := len(v); n < 3 || n > 30 {
		return fmt.Errorf("%w: three to thirty characters", ErrAlias)
	}
	if slices.Contains(Reserved, v) || slices.Contains(a.Reserved, v) {
		return fmt.Errorf("%w: %s is kept for this app", ErrAlias, v)
	}
	return nil
}

// Free says whether nobody has an address and nobody may not take it: no
// workspace has it, nothing holds it, and it is not reserved.
func (a Aliases) Free(ctx context.Context, v string) (bool, error) {
	if slices.Contains(Reserved, v) || slices.Contains(a.Reserved, v) {
		return false, nil
	}
	taken, err := a.Db.Tenant.Query().Where(enttenant.Alias(v)).Exist(ctx)
	if err != nil || taken {
		return false, err
	}
	held, err := a.Held(ctx, v)
	return held == nil, err
}

// Held is what holds an address now, or nil.
func (a Aliases) Held(ctx context.Context, v string) (*ent.AliasHold, error) {
	h, err := a.Db.AliasHold.Query().
		Where(aliashold.Held(v), aliashold.DateUntilGT(a.now()), aliashold.DateErasedIsNil()).
		Order(ent.Desc(aliashold.FieldDateCreated)).
		First(ctx)
	if ent.IsNotFound(err) {
		return nil, nil
	}
	return h, err
}

// Make is a new address nobody has: made up at the length configured, and a
// character longer each time ten made up in a row were taken.
func (a Aliases) Make(ctx context.Context) (string, error) { return a.make(ctx, a.Free) }

func (a Aliases) make(ctx context.Context, free func(context.Context, string) (bool, error)) (string, error) {
	n := a.Length
	if n < 3 {
		n = 7
	}
	for ; n <= 30; n++ {
		for range collisions {
			v := NewAlias(n)
			free, err := free(ctx, v)
			if err != nil {
				return "", err
			}
			if free {
				if n > a.Length && a.Length >= 3 {
					log.From(ctx).WarnContext(ctx, "workspaces: addresses of the configured length are crowded; this one is longer",
						"length", n, "configured", a.Length)
				}
				return v, nil
			}
		}
	}
	return "", errors.New("workspaces: no address came free")
}

// Moved is where an address went, when a workspace was moved off it and the
// hold says where to: followed a few steps, for an address moved twice.
func (a Aliases) Moved(ctx context.Context, v string) (string, error) {
	seen := map[string]bool{v: true}
	to := ""
	for range 5 {
		h, err := a.Held(ctx, v)
		if err != nil || h == nil || h.MovedTo == "" || seen[h.MovedTo] {
			return to, err
		}
		to, v = h.MovedTo, h.MovedTo
		seen[v] = true
		live, err := a.Db.Tenant.Query().Where(enttenant.Alias(v)).Exist(ctx)
		if err != nil {
			return "", err
		}
		if live {
			return to, nil
		}
	}
	return to, nil
}

// fold is an address as it is compared.
func fold(v string) string {
	return strings.ToLower(strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(v), "@")))
}
