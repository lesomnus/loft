// Package consent is what an account agrees to before it uses this deployment
// -- its terms of use and its privacy policy, each at a version -- and the
// record that it did (design 9.11).
//
// It is the account's and not a workspace's: an account agrees once, for every
// workspace it is or will be in, and agrees again when a document changes
// version. Every agreement is a row of its own and none is ever changed, so
// what an account agreed to, and when, is there to show afterwards.
//
// A deployment that names no document asks nothing: a team running its own
// Rove has no terms to put in front of itself.
package consent

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/lesomnus/payday/audit"
	"github.com/lesomnus/payday/frame"
	"github.com/lesomnus/payday/pdid"

	"github.com/lesomnus/rove/internal/ent"
	entconsent "github.com/lesomnus/rove/internal/ent/consent"
	"github.com/lesomnus/rove/server/pd"
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

// Consents is the documents in force, and the agreements to them.
type Consents struct {
	Db        *ent.Client
	Documents []Document
	Now       func() time.Time

	// Audit is what the trail keeps, as the recorder is given it: every
	// agreement, and every mark of one's account leaving, is put on the trail
	// as the act it is (payday#108, `trail: {recorded: {}}` on Consent), so a
	// check of the rows finds an agreement nobody made through here.
	Audit audit.Contents

	mu sync.Mutex
	// settled is the accounts known to owe nothing. Owing nothing lasts
	// until the documents change, which is a restart, so it is kept; owing
	// is asked again every time, since it ends wherever the account agrees.
	settled map[pdid.Id]bool
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

// Owed is the documents an account has not agreed to at the version in force.
func (c *Consents) Owed(ctx context.Context, account pdid.Id) ([]Document, error) {
	out := []Document{}
	if !c.Asks() {
		return out, nil
	}
	for _, d := range c.Documents {
		ok, err := c.Db.Consent.Query().Where(
			entconsent.Account(account.Uuid()),
			entconsent.Document(d.Name),
			entconsent.Version(d.Version),
			entconsent.DateErasedIsNil(),
		).Exist(ctx)
		if err != nil {
			return nil, err
		}
		if !ok {
			out = append(out, d)
		}
	}
	return out, nil
}

// Settled says whether an account owes nothing: what every call a session
// makes is held to.
func (c *Consents) Settled(ctx context.Context, account pdid.Id) (bool, error) {
	if !c.Asks() {
		return true, nil
	}
	c.mu.Lock()
	ok := c.settled[account]
	c.mu.Unlock()
	if ok {
		return true, nil
	}
	owed, err := c.Owed(ctx, account)
	if err != nil || len(owed) > 0 {
		return false, err
	}
	c.mu.Lock()
	if c.settled == nil {
		c.settled = map[pdid.Id]bool{}
	}
	c.settled[account] = true
	c.mu.Unlock()
	return true, nil
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

	now := c.now()
	tx, err := c.Db.Tx(ctx)
	if err != nil {
		return err
	}
	made := make([]pdid.Id, 0, len(owed))
	for _, d := range owed {
		id := pdid.New(pd.ConsentDomain)
		if err := tx.Consent.Create().
			SetId(id.Uuid()).
			SetAccount(account.Uuid()).
			SetDocument(d.Name).
			SetVersion(d.Version).
			SetClient(client).
			SetDateCreated(now).
			Exec(ctx); err != nil {
			_ = tx.Rollback()
			if ent.IsConstraintError(err) {
				// Agreed to at the same moment somewhere else: agreed.
				if owed, err := c.Owed(ctx, account); err == nil && len(owed) == 0 {
					return nil
				}
			}
			return err
		}
		made = append(made, id)
	}
	// The account's own act, by the account.
	as := frame.Into(ctx, frame.New(account, pdid.Nil, frame.Grant{}))
	if _, err := pd.Record(as, tx.Client(), c.Audit, Agreed, made); err != nil {
		_ = tx.Rollback()
		return err
	}
	return tx.Commit()
}

// The acts an agreement is on the trail as.
const (
	Agreed    = "rove.consent.agree"
	Gone      = "rove.consent.left"
	Destroyed = "rove.consent.destroy"
)

// Of is what an account agreed to, newest first.
func (c *Consents) Of(ctx context.Context, account pdid.Id) ([]Agreement, error) {
	vs, err := c.Db.Consent.Query().
		Where(entconsent.Account(account.Uuid()), entconsent.DateErasedIsNil()).
		Order(ent.Desc(entconsent.FieldDateCreated)).
		All(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]Agreement, 0, len(vs))
	for _, v := range vs {
		out = append(out, Agreement{Document: v.Document, Version: v.Version, At: v.DateCreated, Client: v.Client})
	}
	return out, nil
}

// Left records that an account left this deployment, now, on every agreement
// it made that does not say so already: what [Consents.Sweep] counts the
// window from. They still count -- an account that comes back agreed to what
// it agreed to -- and its next sign-in asks nothing it answered.
func (c *Consents) Left(ctx context.Context, account pdid.Id) error {
	tx, err := c.Db.Tx(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	ids, err := tx.Consent.Query().
		Where(entconsent.Account(account.Uuid()), entconsent.DateLeftIsNil(), entconsent.DateErasedIsNil()).
		Ids(ctx)
	if err != nil || len(ids) == 0 {
		return err
	}
	if _, err := tx.Consent.Update().
		Where(entconsent.IdIn(ids...)).
		SetDateLeft(c.now()).
		Save(ctx); err != nil {
		return err
	}

	// The deployment's mark, which is what it is: nobody asked.
	marked := make([]pdid.Id, len(ids))
	for i, v := range ids {
		marked[i] = pdid.Id(v)
	}
	if _, err := pd.Record(ctx, tx.Client(), c.Audit, Gone, marked); err != nil {
		return err
	}
	return tx.Commit()
}

// Sweep destroys the agreements of accounts that left more than `keep` ago,
// and answers how many. Zero keeps them for good, and destroys nothing.
//
// Destroyed rather than erased: an agreement is kept to show afterwards that
// it was made, and past the window there is nothing it is kept for. The trail
// keeps that each act happened -- the agreement, the leaving, and this, the
// deployment's destruction of it, in the same transaction -- as who, when, of
// which row and a keyed digest of it, for this one a digest that says it went,
// and nothing of what was agreed to. It keeps them for as long as its own
// window for the kind keeps any row (`audit.by.consent`, or the deployment's),
// as it keeps every act's actor.
func (c *Consents) Sweep(ctx context.Context, keep time.Duration) (int, error) {
	if keep <= 0 {
		return 0, nil
	}
	tx, err := c.Db.Tx(ctx)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()

	ids, err := tx.Consent.Query().
		Where(entconsent.DateLeftNotNil(), entconsent.DateLeftLT(c.now().Add(-keep))).
		Ids(ctx)
	if err != nil || len(ids) == 0 {
		return 0, err
	}
	n, err := tx.Consent.Delete().Where(entconsent.IdIn(ids...)).Exec(ctx)
	if err != nil {
		return 0, err
	}
	// Once they are rows no more: what the trail keeps is a digest that says
	// each went (payday#115). One that said it was there, kept while it was,
	// is a row the trail says is there and is not.
	gone := make([]pdid.Id, len(ids))
	for i, v := range ids {
		gone[i] = pdid.Id(v)
	}
	if _, err := pd.RecordErased(ctx, tx.Client(), c.Audit, Destroyed, gone); err != nil {
		return 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}

	// Whoever owed nothing because of them owes them again.
	c.mu.Lock()
	c.settled = nil
	c.mu.Unlock()
	return n, nil
}
