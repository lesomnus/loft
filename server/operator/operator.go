// Package operator is who an operator of the deployment is, as the rows the
// deployment's own work writes at their word name them (D74, design 9.15).
//
// An operator is a name in `admin.keys`, found by the key they hold at the
// operator's entry point (`server/admin`). They are no row: the identifier a
// row names them by is made from the name ([Id]), so that the same name is the
// same person in every process and every release, whatever key they hold.
//
// What an operator asks is the deployment's own work, through the servers no
// wall and no gate were put in front of, and decided as the deployment's. So
// the operator is carried in the context ([Into]) and not in payday's frame,
// which the layers that decide what a call may do read as the caller
// (payday#206). What only names who did a write reads it where the
// deployment's work is framed in a workspace ([Frame], `domain.Deps.System`):
// the past, the trail and the access record write the operator where they
// wrote nobody.
package operator

import (
	"context"
	"crypto/sha256"

	"github.com/lesomnus/payday/frame"
	"github.com/lesomnus/payday/pdid"
)

// Domain is the one an operator's identifier says it is of. No entity has it,
// or ever had: an identifier of it is an operator's and nothing else's, which
// a test of this package holds every entity of the schema to.
const Domain pdid.Domain = 11

// Operator is one operator: their name in `admin.keys`, and the identifier the
// rows name them by.
type Operator struct {
	Name string
	Id   pdid.Id
}

// Named is the operator `admin.keys` calls `name`.
func Named(name string) Operator { return Operator{Name: name, Id: Id(name)} }

// Id is the identifier of the operator called `name`: of [Domain], and the
// rest the SHA-256 of the name, laid out as every identifier of the
// deployment is (a UUIDv8, pdid's).
func Id(name string) pdid.Id {
	sum := sha256.Sum256([]byte("loft.operator\x00" + name))
	var v pdid.Id
	copy(v[:], sum[:16])
	// The version and the variant, as pdid reads them, and the domain.
	v[6] = 0x80 | (v[6] & 0x0F)
	v[8] = 0x80 | (v[8] & 0x3F)
	v[9] = byte(Domain)
	return v
}

// Is whether `id` is an operator's.
func Is(id pdid.Id) bool { return !id.IsZero() && id.Domain() == Domain }

type key struct{}

// Into is `ctx` carrying the operator at whose word what follows is done.
func Into(ctx context.Context, o Operator) context.Context {
	return context.WithValue(ctx, key{}, o)
}

// From is the operator `ctx` carries, if any.
func From(ctx context.Context) (Operator, bool) {
	o, ok := ctx.Value(key{}).(Operator)
	return o, ok
}

// Actor is who the deployment's own work in `ctx` is done by, as a row names
// them: the operator it carries, or nobody.
func Actor(ctx context.Context) pdid.Id {
	if o, ok := From(ctx); ok {
		return o.Id
	}
	return pdid.Nil
}

// Frame is `ctx` framed as the deployment's own work in `tenant`, of that
// tenant alone -- or of none, for Nil: the operator `ctx` carries as its actor,
// or nobody.
//
// `domain.Deps.System` frames the deployment's work in a workspace so, which
// the past needs to say where an operation happened. A write the entry point
// makes past the domain layer is framed in none: the operator is held by no
// workspace, a workspace's rows are filed under their own workspace whatever
// the frame says, and a row behind no wall -- an address held -- is filed
// under the frame's tenant, where it is the deployment's and outlives any
// workspace it was about.
//
// Nothing that decides what a call may do reads it: the servers it is handed
// to have no wall and no gate. What reads it is what names who wrote -- the
// past, the trail and the access record.
func Frame(ctx context.Context, tenant pdid.Id) context.Context {
	f := frame.New(Actor(ctx), tenant, frame.Grant{})
	if !tenant.IsZero() {
		f = f.WithScope(frame.Only(tenant))
	}
	return frame.Into(ctx, f)
}
