package admin

import (
	"context"
	"crypto/x509"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/lesomnus/flob"
	"github.com/lesomnus/otx/log"
	"github.com/lesomnus/payday/audit"
	"github.com/lesomnus/payday/pdid"
	"github.com/lesomnus/payday/trail"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"

	app "github.com/lesomnus/loft"
	"github.com/lesomnus/loft/server/operator"
	"github.com/lesomnus/loft/server/pd"
)

// operatorTrail is `OperatorTrailService`: the trail, as the deployment's
// operator keeps it -- what the shell's `loft trail` did on the box.
type operatorTrail struct {
	app.UnimplementedOperatorTrailServiceServer
	a Routes
}

// AcceptedRows is the act an acceptance of the rows is on the trail as.
const AcceptedRows = "loft.trail.rows.accept"

// Keys is the public keys the deployment signs with and trusts -- what
// `offboard.TrustedBy` holds an export to, and the trail's bundle -- for the
// operator to hold a file to where the file is.
func (s operatorTrail) Keys(ctx context.Context, _ *app.OperatorTrailKeysRequest) (*app.OperatorTrailKeysResponse, error) {
	out := &app.OperatorTrailKeysResponse{}
	if p := s.a.S.Trail; p.Signer != nil {
		b, err := x509.MarshalPKIXPublicKey(p.Signer.Public())
		if err != nil {
			return nil, fmt.Errorf("the deployment's key: %w", err)
		}
		out.SetSigner(b)
	}
	for _, k := range s.a.S.Trail.Trust {
		b, err := x509.MarshalPKIXPublicKey(k)
		if err != nil {
			return nil, fmt.Errorf("a key the deployment trusts: %w", err)
		}
		out.SetTrusted(append(out.GetTrusted(), b))
	}
	return out, nil
}

// archived is the trail's policy, refused when it names no archive to check.
func (s operatorTrail) archived() (trail.Policy, error) {
	if s.a.S.Trail.Archive == nil {
		return trail.Policy{}, status.Error(codes.FailedPrecondition, "audit.archive names no archive: the trail is all in the database, and there is nothing to verify")
	}
	return s.a.S.Trail, nil
}

// Verify is the archive compared with its manifest and its checkpoints, as
// every pass of the trail does lightly at its start -- and, `full`, every
// chunk read whole. It changes nothing.
func (s operatorTrail) Verify(req *app.OperatorTrailVerifyRequest, out grpc.ServerStreamingServer[app.OperatorTrailVerifyResponse]) error {
	ctx := out.Context()
	p, err := s.archived()
	if err != nil {
		return err
	}
	var v trail.Verified
	say := func(m string) error { return out.Send(app.OperatorTrailVerifyResponse_builder{Progress: m}.Build()) }
	if err := working(ctx, "verifying the trail's archive", say, func(ctx context.Context, _ progress) error {
		var err error
		v, err = p.Verify(ctx, pd.TrailStore(s.a.S.Ent), req.GetFull())
		return err
	}); err != nil {
		return err
	}

	w := app.OperatorTrailVerified_builder{
		Full:       req.GetFull(),
		Rows:       uint32(v.Rows),
		Blobs:      uint32(v.Blobs),
		Hashed:     uint32(v.Hashed),
		Checkpoint: uint32(v.Checkpoint),
		Digest:     string(v.Digest),
		Heard:      heard(v.Heard),
		Findings:   findings(v.Findings),
	}.Build()
	for _, a := range v.Accepted {
		w.SetAccepted(append(w.GetAccepted(), acceptance(a)))
	}
	return out.Send(app.OperatorTrailVerifyResponse_builder{Verified: w}.Build())
}

func findings(fs []trail.Finding) []*app.OperatorTrailFinding {
	out := make([]*app.OperatorTrailFinding, 0, len(fs))
	for _, f := range fs {
		out = append(out, app.OperatorTrailFinding_builder{Kind: f.Kind, Said: f.String()}.Build())
	}
	return out
}

// Accept is an acceptance of what a verification found, and of nothing else:
// `of` is the digest it said (payday#115). Who accepts is the operator whose
// key the call carries -- not a name typed beside it -- and why is kept in the
// checkpoint the acceptance ends in. What it is of, verified again, is said
// whether or not it is accepted.
func (s operatorTrail) Accept(req *app.OperatorTrailAcceptRequest, out grpc.ServerStreamingServer[app.OperatorTrailAcceptResponse]) error {
	ctx := out.Context()
	if strings.TrimSpace(req.GetOf()) == "" {
		return status.Error(codes.InvalidArgument, "of: the digest `trail verify` said of the findings looked at")
	}
	if strings.TrimSpace(req.GetWhy()) == "" {
		return status.Error(codes.InvalidArgument, "why: what was looked at and decided")
	}
	o, _ := operator.From(ctx)
	a := trail.Acceptance{Of: flob.Digest(strings.TrimSpace(req.GetOf())), By: o.Name, Why: req.GetWhy(), Full: req.GetFull()}
	if req.HasRestored() {
		k := int(req.GetRestored())
		a.Restored = &k
	}
	p, err := s.archived()
	if err != nil {
		return err
	}

	var v trail.Verified
	say := func(m string) error { return out.Send(app.OperatorTrailAcceptResponse_builder{Progress: m}.Build()) }
	aerr := working(ctx, "verifying the trail's archive again, to accept it", say, func(ctx context.Context, _ progress) error {
		var err error
		v, err = p.Accept(ctx, pd.TrailStore(s.a.S.Ent), a)
		return err
	})
	if len(v.Findings) > 0 || aerr == nil {
		if err := out.Send(app.OperatorTrailAcceptResponse_builder{Accepted: app.OperatorTrailAccepted_builder{
			By:       a.By,
			Findings: findings(v.Findings),
		}.Build()}.Build()); err != nil {
			return err
		}
	}
	if errors.Is(aerr, trail.ErrUnreviewed) || errors.Is(aerr, trail.ErrRestored) {
		again := "trail verify"
		if a.Full {
			again += " --full"
		}
		return status.Errorf(codes.FailedPrecondition, "%v -- run `%s` again, look at what it finds, and accept its digest", aerr, again)
	}
	return aerr
}

// Rows is the database's rows against the trail, with `audit.digest.key`:
// every live row digested again and compared with the newest line of the
// trail about it -- in the database, or in the archive once it has left
// (payday#102, #108). It changes nothing.
func (s operatorTrail) Rows(_ *app.OperatorTrailRowsRequest, out grpc.ServerStreamingServer[app.OperatorTrailRowsResponse]) error {
	ctx := out.Context()
	say := func(m string) error { return out.Send(app.OperatorTrailRowsResponse_builder{Progress: m}.Build()) }
	v, err := s.digests(ctx, "checking the rows against the trail", say)
	if err != nil {
		return err
	}
	return out.Send(app.OperatorTrailRowsResponse_builder{Checked: checked(v)}.Build())
}

// digests is a check of the rows, with what the trail keeps of each kind's
// writes and the digest's key.
func (s operatorTrail) digests(ctx context.Context, what string, say func(string) error) (audit.Rows, error) {
	cs, err := s.a.S.Contents()
	if err != nil {
		return audit.Rows{}, err
	}
	if cs.Digest == nil {
		return audit.Rows{}, status.Error(codes.FailedPrecondition, "audit.digest.key names no key, so the trail keeps no digest to check a row against")
	}
	var v audit.Rows
	err = working(ctx, what, say, func(ctx context.Context, _ progress) error {
		var err error
		v, err = pd.Digests(ctx, s.a.S.Ent, cs.Digest, s.a.S.Trail)
		return err
	})
	return v, err
}

func checked(v audit.Rows) *app.OperatorTrailRowsChecked {
	out := app.OperatorTrailRowsChecked_builder{
		Read:          uint32(v.Read),
		Matches:       uint32(v.Matches),
		BeforeDigests: uint32(v.BeforeDigests),
		Retired:       uint32(v.Retired),
		Forgotten:     uint32(v.Forgotten),
		Archived:      uint32(v.Archived),
		Unsealed:      uint32(len(v.Unsealed)),
		Digest:        string(v.Digest()),
	}.Build()
	for _, f := range []struct {
		what string
		ids  []pdid.Id
	}{
		{"differs", v.Differs},
		{"forged", v.Forged},
		{"no-digest", v.NoDigest},
		{"not-on-trail", v.NotOnTrail},
		{"key-not-held", v.KeyNotHeld},
		{"missing", v.Missing},
	} {
		for _, id := range f.ids {
			out.SetFindings(append(out.GetFindings(), app.OperatorTrailRowFinding_builder{What: f.what, Kind: kindOf(id.Domain()), Id: id.Bytes()}.Build()))
		}
	}
	for _, k := range v.Unheld {
		out.SetUnheld(append(out.GetUnheld(), fmt.Sprint(k)))
	}
	for _, k := range v.Skipped {
		out.SetSkipped(append(out.GetSkipped(), app.OperatorTrailSkipped_builder{Kind: fmt.Sprint(k.Kind), Why: k.Why}.Build()))
	}
	return out
}

// AcceptRows is an operator who has looked at what a check of the rows found
// and decided the rows are right -- a migration rewrote them, they were put
// back before a restore said so -- putting them on the trail as that act,
// gone ones as gone, as theirs: of what was looked at, `of` being the digest
// the check said, and refused when a check now finds anything else. The rows
// from before seals are sealed with it. The digest and the why are logged,
// and not on the trail, which keeps no reason (`pd.Record`). A forged line
// and a key not given are not accepted.
func (s operatorTrail) AcceptRows(req *app.OperatorTrailAcceptRowsRequest, out grpc.ServerStreamingServer[app.OperatorTrailAcceptRowsResponse]) error {
	ctx := out.Context()
	of := strings.TrimSpace(req.GetOf())
	if of == "" {
		return status.Error(codes.InvalidArgument, "of: the digest `trail rows` said of what was looked at")
	}
	if strings.TrimSpace(req.GetWhy()) == "" {
		return status.Error(codes.InvalidArgument, "why: what was looked at and decided")
	}
	say := func(m string) error {
		return out.Send(app.OperatorTrailAcceptRowsResponse_builder{Progress: m}.Build())
	}
	v, err := s.digests(ctx, "checking the rows again, to accept them", say)
	if err != nil {
		return err
	}
	if v.Ok() && len(v.Unsealed) == 0 {
		return status.Error(codes.FailedPrecondition, "the check finds nothing, and nothing is to be sealed: there is nothing to accept")
	}
	// What was looked at, and nothing found since: an acceptance of rows
	// somebody changed after the check that was read is of rows nobody
	// looked at (payday#115).
	if digest := string(v.Digest()); of != digest {
		return status.Errorf(codes.FailedPrecondition, "of: the check looked at was %s, and this one found %s: run `trail rows` again, and look at what it finds", of, digest)
	}
	if n := len(v.Forged) + len(v.KeyNotHeld); n > 0 {
		return status.Errorf(codes.FailedPrecondition, "%d lines whose digest is not their own or is under a key not given: those are not accepted -- "+
			"look at the line of the trail, or give the key in audit.digest.previous or retired", n)
	}

	cs, err := s.a.S.Contents()
	if err != nil {
		return err
	}
	// The operator's act, of no workspace: rows of every workspace there are.
	as := operator.Frame(ctx, pdid.Nil)
	there, err := pd.Record(as, s.a.S.Ent, cs, AcceptedRows, slices.Concat(v.Differs, v.NoDigest, v.NotOnTrail))
	if err != nil {
		return fmt.Errorf("accept: %w", err)
	}
	gone, err := pd.RecordErased(as, s.a.S.Ent, cs, AcceptedRows, v.Missing)
	if err != nil {
		return fmt.Errorf("accept: %w", err)
	}
	sealed, err := pd.Record(as, s.a.S.Ent, cs, AcceptedRows, v.Unsealed)
	if err != nil {
		return fmt.Errorf("accept: %w", err)
	}
	log.From(ctx).InfoContext(ctx, "trail: rows accepted as they are", "operator", Who(ctx), "of", of, "why", req.GetWhy(), "rows", there, "gone", gone, "sealed", sealed)
	return out.Send(app.OperatorTrailAcceptRowsResponse_builder{Accepted: app.OperatorTrailRowsAccepted_builder{
		There: uint32(there), Gone: uint32(gone), Sealed: uint32(sealed),
	}.Build()}.Build())
}

// Owed is what is owed of the trail's erasures and not paid yet (payday#115,
// `trail.Policy.Owing`): what a legal hold keeps waits for it to lift, by
// workspace, and what cannot be done -- an archive that cannot be written --
// is said by its age.
func (s operatorTrail) Owed(ctx context.Context, _ *app.OperatorTrailOwedRequest) (*app.OperatorTrailOwedResponse, error) {
	o, err := s.a.S.Trail.Owing(ctx, pd.TrailStore(s.a.S.Ent))
	if err != nil {
		return nil, err
	}
	out := app.OperatorTrailOwedResponse_builder{Objects: uint32(o.Objects)}.Build()
	if o.Objects > 0 {
		out.SetOldest(timestamppb.New(o.Oldest))
	}
	for _, id := range slices.SortedFunc(maps.Keys(o.Held), func(a, b pdid.Id) int { return strings.Compare(a.String(), b.String()) }) {
		v := app.OperatorTrailHeld_builder{Tenant: id.Bytes(), Objects: uint32(o.Held[id])}.Build()
		if t, err := s.a.S.Ent.Tenant.Get(ctx, id.Uuid()); err == nil {
			v.SetAlias(t.Alias)
		}
		out.SetHeld(append(out.GetHeld(), v))
	}
	return out, nil
}

// acceptance is one acceptance as `verify` lists it: the checkpoint it ended
// in and when, who said it and why, and what it was of.
func acceptance(a trail.Accepted) string {
	kinds := make([]string, 0, len(a.Kinds))
	for _, k := range slices.Sorted(maps.Keys(a.Kinds)) {
		kinds = append(kinds, fmt.Sprintf("%s %d", k, a.Kinds[k]))
	}
	if len(kinds) == 0 {
		kinds = append(kinds, "nothing")
	}
	twice := 0
	for _, ts := range a.Twice {
		twice += len(ts)
	}
	if twice > 0 {
		kinds = append(kinds, fmt.Sprintf("heard twice %d", twice))
	}

	v := fmt.Sprintf("at checkpoint %d, %s, by %s: %s (%s)", a.Checkpoint, a.At.UTC().Format(time.RFC3339), a.By, a.Why, strings.Join(kinds, ", "))
	if a.Restored != nil {
		v += fmt.Sprintf(", restoring checkpoint %d", *a.Restored)
	}

	return v
}

// heard is what each witness that answered heard last, as `verify` lists it,
// one line each in the order of their names: the checkpoint's number and
// digest, when the witness heard it by its own clock, and how many numbers it
// heard as two statements. A witness that only tells (`log:`) answers
// nothing and is not here.
func heard(hs map[string]trail.Heard) []string {
	out := make([]string, 0, len(hs))
	for _, name := range slices.Sorted(maps.Keys(hs)) {
		h := hs[name]
		// One that answered and has heard nothing yet -- named before the
		// deployment's first checkpoint -- answers the zero statement.
		if h.K == 0 {
			out = append(out, fmt.Sprintf("heard by %s: nothing yet", name))
			continue
		}

		v := fmt.Sprintf("heard by %s: checkpoint %d, %s, at %s", name, h.K, h.Digest, h.At.UTC().Format(time.RFC3339))
		if n := len(h.Twice); n > 0 {
			v += fmt.Sprintf("; %d number(s) heard as two statements", n)
		}
		out = append(out, v)
	}

	return out
}

// kinds is the name this app's schema gives each of its domains. Not pdid's:
// the roster in this process registers its own kinds under the same numbers,
// and pdid names a number two apps claim by the number alone.
var kinds = func() map[pdid.Domain]string {
	out := map[pdid.Domain]string{}
	for full, d := range pd.Domains {
		out[d] = strings.TrimPrefix(full, "loft.")
	}
	return out
}()

// kindOf is a domain by the name this app gives it.
func kindOf(d pdid.Domain) string {
	if v, ok := kinds[d]; ok {
		return v
	}
	return d.String()
}
