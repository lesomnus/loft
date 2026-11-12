package lifecycle

import (
	"context"
	"errors"
	"fmt"

	"github.com/lesomnus/otx/log"
	"github.com/lesomnus/payday/pdid"
	"github.com/lesomnus/roster/standing"

	"github.com/lesomnus/loft/internal/ent"
	"github.com/lesomnus/loft/internal/ent/holder"
	"github.com/lesomnus/loft/internal/identity"
)

// Reconciled is what roster says of every member here, against what this
// deployment holds.
type Reconciled struct {
	// Members is every member here, in the order they joined; roster has
	// Found of them.
	Members []*ent.Holder
	Found   int

	// Gone is the members roster has nobody by who are not leaving here, and
	// Back the ones leaving here whom roster has again.
	Gone []pdid.Id
	Back []identity.Standing

	// Marked says the ones gone were marked as leaving, and the ones back as
	// not.
	Marked bool
}

// Member is one of [Reconciled.Members] by their identifier, or nil.
func (r *Reconciled) Member(id pdid.Id) *ent.Holder {
	for _, h := range r.Members {
		if pdid.Id(h.Id) == id {
			return h
		}
	}
	return nil
}

// NobodyError is a reconciling that would have marked every member here as
// leaving, and was refused: nobody at all is far more often the wrong
// tenant, or a key that sees none of it, than everybody having left.
type NobodyError struct{ Members int }

func (e *NobodyError) Error() string {
	return fmt.Sprintf("roster has none of the %d member(s) here, which is more likely `auth.roster.tenant` or the key than all of them leaving; nothing was marked", e.Members)
}

// Reconcile asks roster about every member here, and -- with `apply` -- marks
// the ones it has nobody by as leaving, from now, so that they wait the whole
// grace like anybody else, and unmarks the ones it has again.
//
// It is for what roster's stream missed: the stream replays nothing (roster's
// `standing`), so a member erased while this deployment was not listening was
// never marked and would never leave. `loft identity reconcile` is an
// operator asking for it, and the deployment does it itself every
// `app.members.reconcile` ([Lifecycle.Reconciling]). A member a restore put
// back whom roster does not have is one of the ones gone, until `loft
// identity link` makes them there -- which brings them back.
//
// The asking is `standing.Accounts.Reconcile`, which decides nothing: every
// member is asked about before anything is answered, so that a roster which
// stops answering half way decides nothing -- then this answers nil, and the
// error -- and what to mark is this deployment's, here. Without `apply` it
// says what it found, all of it, whatever roster answered.
func (l Lifecycle) Reconcile(ctx context.Context, apply bool) (*Reconciled, error) {
	hs, err := l.Db.Holder.Query().
		Where(holder.DateErasedIsNil()).
		Order(ent.Asc(holder.FieldDateCreated), ent.Asc(holder.FieldId)).
		All(ctx)
	if err != nil {
		return nil, err
	}
	out := &Reconciled{Members: hs}
	ids := make([]pdid.Id, 0, len(hs))
	leaving := map[pdid.Id]bool{}
	for _, h := range hs {
		id := pdid.Id(h.Id)
		ids = append(ids, id)
		leaving[id] = h.DateLeaving != nil
	}

	v, err := l.People.Accounts().Reconcile(ctx, ids, func(id pdid.Id) bool { return leaving[id] })
	// Nobody at all is not believed (standing's NobodyError): said, with
	// where to look, by the deployment that would have marked on it.
	var nobody *standing.NobodyError
	if err != nil && !errors.As(err, &nobody) {
		return nil, err
	}
	out.Found, out.Gone, out.Back = v.Found, v.Gone, v.Back
	if !apply || (len(out.Gone) == 0 && len(out.Back) == 0) {
		return out, nil
	}
	if nobody != nil {
		return out, &NobodyError{Members: len(out.Members)}
	}

	now := l.now()
	for _, id := range out.Gone {
		if err := l.Apply(ctx, identity.Standing{Holder: id, Erased: now}); err != nil {
			return out, fmt.Errorf("%s: %w", id, err)
		}
	}
	for _, st := range out.Back {
		if err := l.Apply(ctx, st); err != nil {
			return out, fmt.Errorf("%s: %w", st.Holder, err)
		}
	}
	out.Marked = true
	return out, nil
}

// Reconciling is [Lifecycle.Reconcile] done by the deployment itself, every
// `app.members.reconcile` (design 9.10): what it marks, and what it would
// not, go in the log.
func (l Lifecycle) Reconciling(ctx context.Context) error {
	v, err := l.Reconcile(ctx, true)
	var nobody *NobodyError
	if errors.As(err, &nobody) {
		log.From(ctx).WarnContext(ctx, "lifecycle: "+nobody.Error())
		return nil
	}
	if v == nil || !v.Marked {
		return err
	}
	for _, id := range v.Gone {
		log.From(ctx).InfoContext(ctx, "lifecycle: roster has nobody by a member here; their membership begins to leave",
			"member", id.String())
	}
	for _, st := range v.Back {
		log.From(ctx).InfoContext(ctx, "lifecycle: roster has a member who was leaving here again; their membership stops leaving",
			"member", st.Holder.String())
	}
	return nil
}
