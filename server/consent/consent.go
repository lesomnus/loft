// Package consent is what an account agrees to before it uses this deployment
// -- its terms of use and its privacy policy, each at a version -- and asking
// roster whether it did (design 9.11, D37).
//
// Which documents are in force is this deployment's to say and stays here
// (`app.consent`). What each account did about them is roster's: its consent
// ledger (`ConsentService`, roster's `docs/apps.md`) keeps every agreement as
// a row of its own that nothing changes, past the account for as long as its
// `consent.keep` says, and destroys them then. Loft keeps none of it -- a copy
// here would be one more record of the same acts to disagree with roster's,
// and one more to destroy.
//
// It is the account's and not a workspace's: an account agrees once, for every
// workspace it is or will be in -- the ledger's scope for the whole deployment
// -- and agrees again when a document changes version.
//
// A deployment that names no document asks nothing: a team running its own
// Loft has no terms to put in front of itself.
package consent

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/lesomnus/payday/pdid"

	"github.com/lesomnus/loft/internal/identity"
)

// Document is one thing an account agrees to.
type Document struct {
	// Name is what an agreement records: `terms` or `privacy`.
	Name string `json:"name"`
	// Title is what a page calls it.
	Title string `json:"title"`
	// Version is the one in force: an agreement to any other is not one.
	Version string `json:"version"`
	// Url is where it is read.
	Url string `json:"url"`
}

// Agreement is one document an account agreed to, at a version, then.
type Agreement struct {
	Document string    `json:"document"`
	Version  string    `json:"version"`
	At       time.Time `json:"at"`
	// Client is what it was agreed on: `web` or `cli`.
	Client string `json:"client,omitempty"`
}

// Ledger is roster's record of what accounts agreed to: [identity.Store].
type Ledger interface {
	Owed(ctx context.Context, account pdid.Id, docs []identity.Document) ([]identity.Document, error)
	Agree(ctx context.Context, account pdid.Id, docs []identity.Document, client string) error
	Agreements(ctx context.Context, account pdid.Id) ([]identity.Agreement, error)
}

// Consents is the documents in force, and roster's ledger of the agreements
// to them.
type Consents struct {
	Ledger    Ledger
	Documents []Document

	// Now is the clock, for the tests.
	Now func() time.Time

	mu sync.Mutex
	// settled is when each account was last heard to owe nothing, believed
	// for [identity.HeldTtl] as what roster says of the account itself is.
	// Owing is not kept: it ends wherever the account agrees, in another
	// process of this deployment too, so it is asked again every time.
	settled map[pdid.Id]time.Time
	// failed is the last time roster could not be asked about an account,
	// and why: it is not asked again for [identity.HeldRetry], so that a
	// roster that is down is not asked once per call. A call that ended while
	// roster was being asked is not one: that was the caller's doing, and
	// the account's next call asks.
	failed map[pdid.Id]failure
}

type failure struct {
	err error
	at  time.Time
}

// ErrOwed is an agreement that leaves something owed: every document an
// account owes is agreed to at once, or none is.
var ErrOwed = errors.New("consent: not every document is agreed to")

// ErrStale is an agreement to a version that is not the one in force: the
// page it was read on is older than the document.
var ErrStale = errors.New("consent: not the version in force")

func (c *Consents) now() time.Time {
	if c.Now == nil {
		return time.Now()
	}
	return c.Now()
}

// Asks says whether this deployment asks anything of an account.
func (c *Consents) Asks() bool { return c != nil && len(c.Documents) > 0 }

// Owed is the documents an account has not agreed to at the version in force,
// as roster says now.
func (c *Consents) Owed(ctx context.Context, account pdid.Id) ([]Document, error) {
	out := []Document{}
	if !c.Asks() {
		return out, nil
	}
	asked := make([]identity.Document, 0, len(c.Documents))
	for _, d := range c.Documents {
		asked = append(asked, identity.Document{Name: d.Name, Version: d.Version})
	}
	owed, err := c.Ledger.Owed(ctx, account, asked)
	if err != nil {
		if ctx.Err() == nil {
			c.heard(account, false, err)
		}
		return nil, err
	}
	for _, d := range c.Documents {
		for _, v := range owed {
			if v.Name == d.Name && v.Version == d.Version {
				out = append(out, d)
				break
			}
		}
	}
	c.heard(account, len(out) == 0, nil)
	return out, nil
}

// heard keeps what roster said of an account: whether it owes nothing, or
// that it could not be asked.
func (c *Consents) heard(account pdid.Id, settled bool, err error) {
	now := c.now()
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.settled == nil {
		c.settled, c.failed = map[pdid.Id]time.Time{}, map[pdid.Id]failure{}
	}
	switch {
	case err != nil:
		c.failed[account] = failure{err: err, at: now}
	case settled:
		c.settled[account] = now
		delete(c.failed, account)
	default:
		delete(c.settled, account)
		delete(c.failed, account)
	}
}

// Settled says whether an account owes nothing: what every call a session
// makes is held to. A roster that could not be asked is an error, and so a
// refusal: what it would have said is not known.
func (c *Consents) Settled(ctx context.Context, account pdid.Id) (bool, error) {
	if !c.Asks() {
		return true, nil
	}
	now := c.now()
	c.mu.Lock()
	at, ok := c.settled[account]
	f, failed := c.failed[account]
	c.mu.Unlock()
	switch {
	case ok && now.Sub(at) < identity.HeldTtl:
		return true, nil
	case failed && now.Sub(f.at) < identity.HeldRetry:
		return false, f.err
	}

	owed, err := c.Owed(ctx, account)
	if err != nil {
		return false, err
	}
	return len(owed) == 0, nil
}

// Agree records an account agreeing to what it owes, each document named with
// the version it read: every one it owes, at the version in force, or nothing
// is recorded. What it agreed to before is not agreed to again.
func (c *Consents) Agree(ctx context.Context, account pdid.Id, agreed map[string]string, client string) error {
	owed, err := c.Owed(ctx, account)
	if err != nil {
		return err
	}
	for _, d := range owed {
		v, ok := agreed[d.Name]
		if !ok {
			return fmt.Errorf("%w: %s", ErrOwed, d.Name)
		}
		if v != d.Version {
			return fmt.Errorf("%w: %s is at %s, and %s was read", ErrStale, d.Name, d.Version, v)
		}
	}
	if len(owed) == 0 {
		return nil
	}

	docs := make([]identity.Document, 0, len(owed))
	for _, d := range owed {
		docs = append(docs, identity.Document{Name: d.Name, Version: d.Version})
	}
	if err := c.Ledger.Agree(ctx, account, docs, client); err != nil {
		return err
	}
	c.heard(account, true, nil)
	return nil
}

// Of is what an account agreed to, newest first.
//
// Nothing, on a deployment that asks nothing: one that names no document asks
// nothing of roster's ledger either, and is served by a role without it.
func (c *Consents) Of(ctx context.Context, account pdid.Id) ([]Agreement, error) {
	out := []Agreement{}
	if !c.Asks() {
		return out, nil
	}
	vs, err := c.Ledger.Agreements(ctx, account)
	if err != nil {
		return nil, err
	}
	for _, v := range vs {
		out = append(out, Agreement{Document: v.Document, Version: v.Version, At: v.At, Client: v.Client})
	}
	return out, nil
}
