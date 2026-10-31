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
// It is for what roster's stream missed: the stream replays nothing (roster's
// `standing`), so an account erased while this deployment was not listening
// was never marked and would never leave. `loft identity reconcile` is an
// operator asking for it, and the deployment does it itself every
// `app.accounts.reconcile` ([Lifecycle.Reconciling]).
//
// The asking is `standing.Accounts.Reconcile`, which decides nothing: every
// account is asked about before anything is answered, so that a roster which
// stops answering half way decides nothing -- then this answers nil, and the
// error -- and what to mark is this deployment's, here. Without `apply` it
// says what it found, all of it, whatever roster answered.
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

	v, err := l.People.Accounts().Reconcile(ctx, out.Accounts, func(a pdid.Id) bool { return leaving(out.Members[a]) })
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
