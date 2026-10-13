package retention

import (
	"time"

	"github.com/lesomnus/rove/internal/ent/audit"
	"github.com/lesomnus/rove/internal/ent/event"
	"github.com/lesomnus/rove/internal/ent/predicate"
)

// EventIn is an event in a view window that begins at `since`: it happened
// in it, or it was recorded in it.
func EventIn(since time.Time) predicate.Event {
	return event.Or(event.OccurredAtGTE(since), event.DateCreatedGTE(since))
}

// AuditIn is a trail row in a view window that begins at `since`: a row of the
// history written in it, or a row of anything else. The trail of who signed in
// and who changed whose role is not the history, and the window is not its.
func AuditIn(since time.Time) predicate.Audit {
	ds := make([]uint32, 0, len(Kinds))
	for _, k := range Kinds {
		ds = append(ds, uint32(k))
	}

	return audit.Or(audit.DomainNotIn(ds...), audit.DateCreatedGTE(since))
}
