package identity

import (
	"context"
	"fmt"
	"time"

	"github.com/lesomnus/z"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/lesomnus/payday/pdid"
	"github.com/lesomnus/roster/rstr"
)

// What an account agreed to is roster's (D37): its consent ledger,
// `ConsentService`, keeps every act of it as a row nothing changes, and keeps
// them past the account for as long as its `consent.keep` says -- this
// deployment's [Config.ConsentKeep] on the roster in this process. Which
// documents are in force stays this deployment's (`app.consent`), and is what
// each call names.
//
// Every call is made as Rove's holder in the directory, which roster writes
// onto each act and narrows each read to: what Rove reads of the ledger is
// what Rove recorded and nothing another app did.

// consentScope is where Rove's documents hold in roster's ledger: the whole
// deployment, which is the empty scope. A workspace's own policy, were there
// one, would be the same rows with the workspace as their scope.
const consentScope = ""

// Document is one of the documents Rove asks an account to agree to, as
// roster's ledger names it: its key -- `terms`, `privacy` -- and a version.
type Document struct {
	Name    string
	Version string
}

// Agreement is one agreement roster's ledger keeps of an account's, to one
// of Rove's documents at a version.
type Agreement struct {
	Document string
	Version  string
	// Client is what it was given through: `web` or `cli`.
	Client string
	At     time.Time
}

// Owed is which of `docs` an account has not agreed to, each at its version
// (`ConsentService.Settled`): none for an account that owes nothing.
// [ErrNoPerson] is somebody roster does not have, and [ErrUnreachable] a
// roster that could not be asked or would not answer.
func (s *Store) Owed(ctx context.Context, account pdid.Id, docs []Document) ([]Document, error) {
	as, _, err := s.inDirectory(ctx)
	if err != nil {
		return nil, unreachable(err)
	}
	res, err := rstr.NewConsentServiceClient(s.conn).Settled(as, rstr.ConsentSettledRequest_builder{
		Holder:    rstr.HolderRef_builder{Id: account.Bytes()}.Build(),
		Scope:     consentScope,
		Documents: papers(docs),
	}.Build())
	if err != nil {
		return nil, ledgerFailed(err)
	}

	out := make([]Document, 0, len(res.GetOwed()))
	for _, v := range res.GetOwed() {
		out = append(out, Document{Name: v.GetDocument(), Version: v.GetVersion()})
	}

	return out, nil
}

// Agree records an account agreeing to `docs`, each at the version it read,
// through `client`: all of them, or none (`ConsentService.Agree`). One it
// agreed to already at that version is left as it is, and nothing is
// written for it.
func (s *Store) Agree(ctx context.Context, account pdid.Id, docs []Document, client string) error {
	as, _, err := s.inDirectory(ctx)
	if err != nil {
		return unreachable(err)
	}
	if _, err := rstr.NewConsentServiceClient(s.conn).Agree(as, rstr.ConsentAgreeRequest_builder{
		Holder:    rstr.HolderRef_builder{Id: account.Bytes()}.Build(),
		Scope:     consentScope,
		Documents: papers(docs),
		Client:    client,
	}.Build()); err != nil {
		return ledgerFailed(err)
	}

	return nil
}

// Agreements is what an account agreed to of Rove's documents, newest first:
// every version of each, since none is ever changed.
func (s *Store) Agreements(ctx context.Context, account pdid.Id) ([]Agreement, error) {
	as, _, err := s.inDirectory(ctx)
	if err != nil {
		return nil, unreachable(err)
	}
	c := rstr.NewConsentServiceClient(s.conn)
	filter := rstr.ConsentFilter_builder{
		Holder: rstr.HolderRef_builder{Id: account.Bytes()}.Build(),
		Scope:  z.Ptr(consentScope),
	}.Build()

	out := []Agreement{}
	after := ""
	for {
		res, err := c.List(as, rstr.ConsentListRequest_builder{
			Filters: []*rstr.ConsentFilter{filter},
			Size:    100,
			After:   after,
			Reverse: true,
		}.Build())
		if err != nil {
			return nil, ledgerFailed(err)
		}
		for _, v := range res.GetItems() {
			// Rove withdraws nothing -- what it asks for is required -- so
			// an act that is not an agreement is not one of its own.
			if v.GetAct() != rstr.ConsentAct_CONSENT_ACT_AGREE {
				continue
			}
			out = append(out, Agreement{
				Document: v.GetDocument(),
				Version:  v.GetVersion(),
				Client:   v.GetClient(),
				At:       v.GetDateCreated().AsTime(),
			})
		}
		if after = res.GetNext(); after == "" || len(res.GetItems()) == 0 {
			return out, nil
		}
	}
}

// papers is the documents as roster's ledger is told them: each required,
// since there is nothing Rove asks for that an account may decline and stay.
func papers(docs []Document) []*rstr.ConsentDocument {
	out := make([]*rstr.ConsentDocument, 0, len(docs))
	for _, d := range docs {
		out = append(out, rstr.ConsentDocument_builder{Document: d.Name, Version: d.Version}.Build())
	}

	return out
}

// ledgerFailed is a call to the ledger that did not answer, as this package
// says it: nobody by that account, or a roster that could not be asked or
// would not answer -- a role without the method among them, which is said in
// so many words, since no retry fixes it.
func ledgerFailed(err error) error {
	switch status.Code(err) {
	case codes.NotFound:
		return fmt.Errorf("%w: roster has no such account", ErrNoPerson)
	case codes.PermissionDenied:
		return fmt.Errorf("%w: Rove's role at roster does not hold its consent ledger, "+
			"which `roster role patch` adds (README): %v", ErrUnreachable, err)
	}

	return unreachable(err)
}

// unreachable is a roster that could not be asked. What it said is kept as
// words and not as the error: a status in the chain is what a gRPC caller of
// Rove would be answered with, and roster's refusal of Rove is not the
// caller's to read.
func unreachable(err error) error { return fmt.Errorf("%w: %v", ErrUnreachable, err) }
