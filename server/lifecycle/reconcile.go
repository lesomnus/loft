package lifecycle

import (
	"context"
	"errors"
	"fmt"

	"github.com/lesomnus/otx/log"
	"github.com/lesomnus/payday/pdid"

	"github.com/lesomnus/rove/internal/ent"
	"github.com/lesomnus/rove/internal/ent/holder"
	"github.com/lesomnus/rove/internal/identity"
)

// Reconciled is what roster says of every account with a membership here,
// against what this deployment holds.
type Reconciled struct {
	// Accounts is every account with a membership here, in the order they
	// first joined, and Members each one's memberships; roster has Found of
	// them.
	Accounts []pdid.Id
	Members  map[pdid.Id][]*ent.Holder
	Found    int

	// Gone is the accounts roster has nobody by that are not leaving here,
	// and Back the ones leaving here that roster has again.
	Gone []pdid.Id
	Back []identity.Standing

	// Marked says the ones gone were marked as leaving, and the ones back as
	// not.
	Marked bool
}

// NobodyError is a reconciling that would have marked every account here as
// leaving, and was refused: nobody at all is far more often the wrong
// directory, or a key that sees none of it, than everybody having left.
type NobodyError struct{ Accounts int }

func (e *NobodyError) Error() string {
	return fmt.Sprintf("roster has none of the %d account(s) here, which is more likely `auth.roster.directory` or the key than all of them leaving; nothing was marked", e.Accounts)
}

// Reconcile asks roster about every account with a membership here, and --
// with `apply` -- marks the ones it has nobody by as leaving, from now, so
// that they wait the whole grace like anybody else, and unmarks the ones it
// has again.
//
// It is for what roster's stream missed: the stream replays nothing
// ([identity.Store.Follow]), so an account erased while this deployment was
// not listening was never marked and would never leave. `rove identity
// reconcile` is an operator asking for it, and the deployment does it itself
// every `app.accounts.reconcile` ([Lifecycle.Reconciling]).
//
// Everything is asked before anything is done, so that a roster which stops
// answering half way decides nothing: then it answers nil, and the error.
func (l Lifecycle) Reconcile(ctx context.Context, apply bool) (*Reconciled, error) {
	hs, err := l.Db.Holder.Query().
		Where(holder.DateErasedIsNil(), holder.AccountNotNil()).
		Order(ent.Asc(holder.FieldDateCreated), ent.Asc(holder.FieldId)).
		All(ctx)
	if err != nil {
		return nil, err
	}
	out := &Reconciled{Members: map[pdid.Id][]*ent.Holder{}}
	for _, h := range hs {
		a := pdid.Id(*h.Account)
		if _, ok := out.Members[a]; !ok {
			out.Accounts = append(out.Accounts, a)
		}
		out.Members[a] = append(out.Members[a], h)
	}

	for _, a := range out.Accounts {
		st, err := l.People.StandingOf(ctx, a)
		switch {
		case errors.Is(err, identity.ErrNoPerson):
			if !leaving(out.Members[a]) {
				out.Gone = append(out.Gone, a)
			}
		case err != nil:
			return nil, fmt.Errorf("roster could not be asked about %s, so nothing was decided: %w", a, err)
		default:
			out.Found++
			if st.Erased.IsZero() && leaving(out.Members[a]) {
				out.Back = append(out.Back, st)
			}
		}
	}
	if !apply || (len(out.Gone) == 0 && len(out.Back) == 0) {
		return out, nil
	}
	if out.Found == 0 && len(out.Gone) > 0 {
		return out, &NobodyError{Accounts: len(out.Accounts)}
	}

	now := l.now()
	for _, a := range out.Gone {
		if err := l.Apply(ctx, identity.Standing{Holder: a, Erased: now}); err != nil {
			return out, fmt.Errorf("%s: %w", a, err)
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
// `app.accounts.reconcile` (design 9.10): what it marks, and what it would
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
	for _, a := range v.Gone {
		log.From(ctx).InfoContext(ctx, "lifecycle: roster has nobody by an account with a membership here; its memberships begin to leave",
			"account", a.String(), "memberships", len(v.Members[a]))
	}
	for _, st := range v.Back {
		log.From(ctx).InfoContext(ctx, "lifecycle: roster has an account that was leaving here again; its memberships stop leaving",
			"account", st.Holder.String(), "memberships", len(v.Members[st.Holder]))
	}
	return nil
}

// leaving says whether any of these memberships is marked as leaving.
func leaving(hs []*ent.Holder) bool {
	for _, h := range hs {
		if h.DateLeaving != nil {
			return true
		}
	}
	return false
}
