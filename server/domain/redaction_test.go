package domain

import (
	"context"
	"fmt"

	"github.com/lesomnus/payday/past"
	"github.com/lesomnus/payday/pdid"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	app "github.com/lesomnus/loft"
)

// RedactionAsTheActWill records an `event.redact` of the event `event` as the
// act that takes a reason out will record one (payday#151's Redact): its
// subject that event's, its reason the request's reference `reason`, and its
// message the event -- and nothing else, so the event keeps its own reason.
// Nothing in this build writes one, since a kind ships a release before
// anything writes it; this is for the tests outside the package, of what a
// build that declares it reads of a row of it. `s` is the stack a caller
// reaches, whose outside is this layer.
func RedactionAsTheActWill(ctx context.Context, s app.Server, event pdid.Id, reason string) (pdid.Id, error) {
	d, ok := s.(Domain)
	if !ok {
		return pdid.Nil, fmt.Errorf("domain: %T is not this layer", s)
	}

	var out pdid.Id
	err := d.tx(ctx, func(t *Tx) error {
		vs, err := past.Events(t.ctx, t.past, past.EventQuery{Ids: []pdid.Id{event}})
		if err != nil {
			return err
		}
		if len(vs) == 0 {
			return status.Errorf(codes.NotFound, "domain: no event %s", event)
		}
		if err := t.begin(nil, "event.redact", vs[0].Subject, t.now, reason, app.EventRedacted_builder{Event: event.Bytes()}.Build()); err != nil {
			return err
		}
		out = t.ev()
		return nil
	})
	return out, err
}
