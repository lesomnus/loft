package consent_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/lesomnus/payday/pdid"
	"github.com/stretchr/testify/require"

	"github.com/lesomnus/rove/internal/identity"
	"github.com/lesomnus/rove/server/consent"
)

// ledger is roster's, as these tests have it: what each account agreed to,
// how often it was asked, and an error to answer everything with when set.
type ledger struct {
	mu     sync.Mutex
	agreed map[pdid.Id]map[identity.Document]string
	asked  int
	err    error
	sent   [][]identity.Document
}

func (l *ledger) Owed(_ context.Context, account pdid.Id, docs []identity.Document) ([]identity.Document, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.asked++
	if l.err != nil {
		return nil, l.err
	}
	out := []identity.Document{}
	for _, d := range docs {
		if _, ok := l.agreed[account][d]; !ok {
			out = append(out, d)
		}
	}
	return out, nil
}

func (l *ledger) Agree(_ context.Context, account pdid.Id, docs []identity.Document, client string) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.err != nil {
		return l.err
	}
	l.sent = append(l.sent, docs)
	if l.agreed == nil {
		l.agreed = map[pdid.Id]map[identity.Document]string{}
	}
	if l.agreed[account] == nil {
		l.agreed[account] = map[identity.Document]string{}
	}
	for _, d := range docs {
		l.agreed[account][d] = client
	}
	return nil
}

func (l *ledger) Agreements(_ context.Context, account pdid.Id) ([]identity.Agreement, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.err != nil {
		return nil, l.err
	}
	out := []identity.Agreement{}
	for d, client := range l.agreed[account] {
		out = append(out, identity.Agreement{Document: d.Name, Version: d.Version, Client: client})
	}
	return out, nil
}

func (l *ledger) times() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.asked
}

var docs = []consent.Document{
	{Name: "terms", Title: "이용약관", Version: "2026-10-01", Url: "https://rove.example/terms"},
	{Name: "privacy", Title: "개인정보 처리방침", Version: "2026-10-01", Url: "https://rove.example/privacy"},
}

var agreed = map[string]string{"terms": "2026-10-01", "privacy": "2026-10-01"}

// asking is Consents over `l`, on a clock the test moves.
func asking(l *ledger) (*consent.Consents, *time.Time) {
	now := time.Now()
	return &consent.Consents{Ledger: l, Documents: docs, Now: func() time.Time { return now }}, &now
}

// TestOwingNothingIsBelievedForAsLongAsWhatRosterSaysOfAnAccount: an account
// heard to owe nothing is not asked about again for [identity.HeldTtl], which
// is how late roster's word on the account itself reaches a session -- and is
// asked about again after it.
func TestOwingNothingIsBelievedForAsLongAsWhatRosterSaysOfAnAccount(t *testing.T) {
	x := require.New(t)
	ctx := t.Context()
	l := &ledger{}
	c, now := asking(l)
	kim := pdid.New(2)
	x.NoError(c.Agree(ctx, kim, agreed, "web"))

	asked := l.times()
	for range 3 {
		ok, err := c.Settled(ctx, kim)
		x.NoError(err)
		x.True(ok)
	}
	x.Equal(asked, l.times(), "asked again within HeldTtl")

	*now = now.Add(identity.HeldTtl)
	ok, err := c.Settled(ctx, kim)
	x.NoError(err)
	x.True(ok)
	x.Equal(asked+1, l.times(), "believed past HeldTtl")
}

// TestOwingIsAskedEveryTime: owing ends wherever the account agrees -- in
// another process of this deployment, too -- so an account that owed is asked
// about again at its next call, and is served at the one after it agreed.
func TestOwingIsAskedEveryTime(t *testing.T) {
	x := require.New(t)
	ctx := t.Context()
	l := &ledger{}
	c, _ := asking(l)
	kim := pdid.New(2)

	ok, err := c.Settled(ctx, kim)
	x.NoError(err)
	x.False(ok)

	// Agreed through another process, which this one was not told of.
	elsewhere, _ := asking(l)
	x.NoError(elsewhere.Agree(ctx, kim, agreed, "web"))

	ok, err = c.Settled(ctx, kim)
	x.NoError(err)
	x.True(ok, "an account that owed was believed to owe still")
}

// TestARosterThatCannotBeAskedIsARefusal: what roster would have said is not
// known, so it is neither owed nor settled but an error -- which the gate
// refuses with -- and it is not asked again for [identity.HeldRetry], so that
// a roster that is down is not asked once per call.
func TestARosterThatCannotBeAskedIsARefusal(t *testing.T) {
	x := require.New(t)
	ctx := t.Context()
	l := &ledger{err: identity.ErrUnreachable}
	c, now := asking(l)
	kim := pdid.New(2)

	ok, err := c.Settled(ctx, kim)
	x.ErrorIs(err, identity.ErrUnreachable)
	x.False(ok)
	ok, err = c.Settled(ctx, kim)
	x.ErrorIs(err, identity.ErrUnreachable, "refused, within HeldRetry, as it was")
	x.False(ok)
	x.Equal(1, l.times(), "asked again at once")

	l.mu.Lock()
	l.err = nil
	l.agreed = map[pdid.Id]map[identity.Document]string{kim: {{Name: "terms", Version: "2026-10-01"}: "web", {Name: "privacy", Version: "2026-10-01"}: "web"}}
	l.mu.Unlock()
	*now = now.Add(identity.HeldRetry)
	ok, err = c.Settled(ctx, kim)
	x.NoError(err)
	x.True(ok, "asked again once HeldRetry is over")
}

// TestAnAgreementIsToEveryDocumentOwedAtTheVersionInForce: every document an
// account owes is agreed to at once, each at the version in force, or nothing
// is recorded; what it agreed to before is not sent again; and an account
// that just agreed is served without roster being asked again.
func TestAnAgreementIsToEveryDocumentOwedAtTheVersionInForce(t *testing.T) {
	x := require.New(t)
	ctx := t.Context()
	l := &ledger{}
	c, _ := asking(l)
	kim := pdid.New(2)

	x.ErrorIs(c.Agree(ctx, kim, map[string]string{"terms": "2026-10-01"}, "web"), consent.ErrOwed)
	x.ErrorIs(c.Agree(ctx, kim, map[string]string{"terms": "2026-09-01", "privacy": "2026-10-01"}, "web"), consent.ErrStale)
	x.Empty(l.sent, "something was recorded of an agreement that was none")

	// Agreed to the terms before; the privacy policy is what is owed now.
	x.NoError(l.Agree(ctx, kim, []identity.Document{{Name: "terms", Version: "2026-10-01"}}, "web"))
	l.sent = nil
	x.NoError(c.Agree(ctx, kim, map[string]string{"privacy": "2026-10-01"}, "cli"))
	x.Equal([][]identity.Document{{{Name: "privacy", Version: "2026-10-01"}}}, l.sent, "only what was owed, at the version in force")

	asked := l.times()
	ok, err := c.Settled(ctx, kim)
	x.NoError(err)
	x.True(ok)
	x.Equal(asked, l.times(), "an account that just agreed was asked about again")

	vs, err := c.Of(ctx, kim)
	x.NoError(err)
	x.Len(vs, 2)
}

// TestADeploymentThatAsksNothingAsksRosterNothing: a deployment that names no
// document asks nothing of an account, and nothing of roster's ledger either,
// so a role without it serves it.
func TestADeploymentThatAsksNothingAsksRosterNothing(t *testing.T) {
	x := require.New(t)
	ctx := t.Context()
	l := &ledger{err: errors.New("roster's ledger was asked")}
	c := &consent.Consents{Ledger: l}
	kim := pdid.New(2)

	ok, err := c.Settled(ctx, kim)
	x.NoError(err)
	x.True(ok)
	owed, err := c.Owed(ctx, kim)
	x.NoError(err)
	x.Empty(owed)
	vs, err := c.Of(ctx, kim)
	x.NoError(err)
	x.Empty(vs)
	x.NoError(c.Agree(ctx, kim, nil, "web"))
	x.Zero(l.times())

	var none *consent.Consents
	ok, err = none.Settled(ctx, kim)
	x.NoError(err)
	x.True(ok, "and nil asks nothing")
}
