package domain

import (
	"context"

	"github.com/lesomnus/payday/past"
	"github.com/protobuf-orm/ent/dialect"

	app "github.com/lesomnus/loft"
)

// NamesFor is what an answer of the layer over `next` names rows by, for the
// caller in `ctx`, as payday's past asks for names (`past.Names`): for the
// tests outside the package.
func NamesFor(ctx context.Context, next app.Server, drv dialect.Driver, deps *Deps) (past.Names, error) {
	t, err := New(next, drv, deps).read(ctx)
	if err != nil {
		return nil, err
	}
	return &namer{t: t}, nil
}
