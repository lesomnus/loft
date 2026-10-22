package cli

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"os"
	"os/user"
	"slices"
	"strings"
	"time"

	"github.com/lesomnus/flob"
	"github.com/lesomnus/otx/log"
	"github.com/lesomnus/payday/pdid"
	"github.com/lesomnus/payday/trail"
	"github.com/lesomnus/xli"
	"github.com/lesomnus/xli/flg"

	"github.com/lesomnus/rove/cmd"
	"github.com/lesomnus/rove/server/pd"
)

// NewCmdTrail is `rove trail`: the trail's archive, checked against the account
// the database keeps of it (payday's `trail.Policy.Verify`), and the live rows
// checked against the trail (`rows`, payday's `pd.Digests`).
//
// `verify` is what every pass of the trail does lightly at its start -- the
// archive's chunks by name and labels against the manifest, and the latest
// checkpoint against the rows it was taken over -- and `--full` also reads
// every chunk whole and checks its bytes. It changes nothing, and fails when it
// found something, so a schedule can run it. `accept` is an operator who has
// looked at what it found and decided the archive is right -- a pass a crash
// stopped, a chunk restored from a backup -- and says who and why. It is of
// what was looked at (payday#115): `verify` prints the digest of what it
// found, and `accept --of` takes it back, verifying again -- `--full` when the
// check was -- and refused when that finds anything else. A database restored
// from a backup is accepted as the restore it is, `--restored` naming the
// checkpoint the backup holds. Who and why are kept in the checkpoint the
// acceptance ends in, and `verify` lists the latest.
//
// `rows` is the database's rows against the trail: with `audit.digest.key`,
// each row of the trail keeps a keyed digest of the row its write left, and
// this digests every live row again and compares it with the newest row of the
// trail about it -- in the database, or in the archive once it has left
// (payday#102, #108). A row somebody changed in the database past the servers
// is one that differs, and a row somebody added there is one nothing on the
// trail is about. A line whose digest is not its own is forged, one with no
// digest or under a key it was not given is a finding too, and a row the trail
// says is there and is not is missing (payday#115). It changes nothing either,
// fails on any of them, and says which keys it was not given and which kinds it
// did not read. `rows --accept` is an operator who has looked at each and
// decided the rows are right -- a migration rewrote them, they were put back
// before a restore said so -- and puts them on the trail as that act, gone ones
// as gone. It is of what was looked at too: `--of` is the digest `rows`
// printed, and it is refused when the check now finds anything else. The
// digest and the why are logged, and not on the trail, as `pd.Record` keeps no
// reason. A forged line and a key not given are not accepted.
//
// A digest covers every field but the ones declared personal, which a line
// kept for years in the archive must not keep a MAC of; those are covered by
// the row's seal, kept beside the row in the database alone (payday#115). A
// row whose newest write sealed nothing -- every row of a kind with a personal
// field, after the upgrade that brought seals -- is counted as from before
// seals, no finding, and `rows --accept` seals those too, when they are all a
// check found: the one acceptance an operator runs after that upgrade
// (docs/runbook/trail-integrity.md). `verify` says what each witness that
// answers heard last of the checkpoints (`audit.checkpoints.witnesses`).
func NewCmdTrail(c *cmd.Config) *xli.Command {
	return &xli.Command{
		Name:  "trail",
		Brief: "the trail's archive, checked against the account the database keeps of it",

		Commands: xli.Commands{
			{
				Name:  "verify",
				Brief: "compare the archive with its manifest and its checkpoints, changing nothing",
				Flags: flg.Flags{
					&flg.Switch{Name: "full", Brief: "also read every chunk whole and check its bytes"},
				},
				Handler: xli.OnRun(func(ctx context.Context, self *xli.Command, next xli.Next) error {
					full, _ := flg.Find[bool](self, "full")
					return verify(ctx, self, c, full)
				}),
			},
			{
				Name:  "rows",
				Brief: "check the live rows against the digests the trail keeps of them, changing nothing",
				Flags: flg.Flags{
					&flg.Switch{Name: "accept", Brief: "put what the check found on the trail as accepted, once somebody has looked"},
					&flg.String{Name: "of", Brief: "with --accept, the digest a check printed of what was looked at"},
					&flg.String{Name: "why", Brief: "what was looked at and decided, with --accept; it is logged"},
				},
				Handler: xli.OnRun(func(ctx context.Context, self *xli.Command, next xli.Next) error {
					accept, _ := flg.Find[bool](self, "accept")
					of, _ := flg.Find[string](self, "of")
					why, _ := flg.Find[string](self, "why")
					return rows(ctx, self, c, accept, strings.TrimSpace(of), why)
				}),
			},
			newCmdTrailExport(c),
			newCmdTrailVerifyExport(c),
			{
				Name:  "accept",
				Brief: "make the manifest say what the archive holds, once somebody has looked",
				Flags: flg.Flags{
					&flg.String{Name: "of", Brief: "the digest `rove trail verify` printed of the findings looked at"},
					&flg.String{Name: "by", Brief: "who accepts; the operator's user and host by default"},
					&flg.String{Name: "why", Brief: "what was looked at and decided; it is kept"},
					&flg.Switch{Name: "full", Brief: "the check looked at was --full"},
					&flg.Int{Name: "restored", Brief: "for a database restored from a backup, the checkpoint the backup holds (0 for one from before the first)"},
				},
				Handler: xli.OnRun(func(ctx context.Context, self *xli.Command, next xli.Next) error {
					of, _ := flg.Find[string](self, "of")
					by, _ := flg.Find[string](self, "by")
					why, _ := flg.Find[string](self, "why")
					a := trail.Acceptance{Of: flob.Digest(strings.TrimSpace(of)), By: strings.TrimSpace(by), Why: why}
					a.Full, _ = flg.Find[bool](self, "full")
					if k, ok := flg.Find[int](self, "restored"); ok {
						a.Restored = &k
					}
					return accept(ctx, self, c, a)
				}),
			},
		},
	}
}

// archived is the trail's policy as `serve` builds it, refused when it names no
// archive to check.
func archived(ctx context.Context, c *cmd.Config) (*cmd.Server, trail.Policy, error) {
	s, err := cmd.Build(ctx, *c)
	if err != nil {
		return nil, trail.Policy{}, err
	}
	if s.Trail.Archive == nil {
		s.Close()
		return nil, trail.Policy{}, errors.New("audit.archive names no archive: the trail is all in the database, and there is nothing to verify")
	}

	return s, s.Trail, nil
}

func verify(ctx context.Context, self *xli.Command, c *cmd.Config, full bool) error {
	s, p, err := archived(ctx, c)
	if err != nil {
		return err
	}
	defer s.Close()

	v, err := p.Verify(ctx, pd.TrailStore(s.Ent), full)
	if err != nil {
		return err
	}

	read := fmt.Sprintf("%d rows in the manifest, %d blobs in the archive", v.Rows, v.Blobs)
	if full {
		read += fmt.Sprintf(", %d read whole", v.Hashed)
	}
	if v.Checkpoint > 0 {
		read += fmt.Sprintf("; checkpoint %d compared", v.Checkpoint)
	}
	self.Printf("%s\n", read)
	// What an acceptance of these findings names, and what was accepted
	// before: who, why, and of what (payday#115).
	self.Printf("digest %s\n", v.Digest)
	for _, a := range v.Accepted {
		self.Printf("accepted %s\n", acceptance(a))
	}
	// And what each witness that answers heard last, which is what the chain
	// was just held to (payday#115): a clean run says it too, which is how far
	// outside the database the newest checkpoint is known.
	for _, h := range heard(v.Heard) {
		self.Printf("%s\n", h)
	}

	if v.Ok() {
		self.Printf("the archive is what the database says it is\n")
		return nil
	}
	for _, f := range v.Findings {
		self.Printf("  %s\n", f.String())
	}

	how := "--of " + string(v.Digest)
	if full {
		how += " --full"
	}
	if slices.ContainsFunc(v.Findings, func(f trail.Finding) bool { return f.Kind == "behind" }) {
		how += " --restored <the checkpoint the backup holds>"
	}

	return fmt.Errorf("%d findings: look at each, and `rove trail accept %s --why ...` once the archive is right", len(v.Findings), how)
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
// heard as two statements -- each of which is also a `rewritten` finding until
// an acceptance takes it in.
//
// A witness that only tells (`log:`) answers nothing and is not here -- what
// it heard is the monitoring's to say -- and neither is one that did not
// answer this time. `unheard` is the finding when nothing that answers heard
// the newest.
func heard(hs map[string]trail.Heard) []string {
	out := make([]string, 0, len(hs))
	for _, name := range slices.Sorted(maps.Keys(hs)) {
		h := hs[name]
		// One that answered and has heard nothing yet -- named before the
		// deployment's first checkpoint -- answers the zero statement,
		// numbered nothing, which the chain is not held to.
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

func rows(ctx context.Context, self *xli.Command, c *cmd.Config, accept bool, of, why string) error {
	if accept && of == "" {
		return errors.New("--of: the digest `rove trail rows` printed of what was looked at")
	}
	if accept && strings.TrimSpace(why) == "" {
		return errors.New("--why says what was looked at and decided")
	}

	cs, err := c.Audit.Contents()
	if err != nil {
		return fmt.Errorf("audit: %w", err)
	}
	if cs.Digest == nil {
		return errors.New("audit.digest.key names no key, so the trail keeps no digest to check a row against")
	}

	s, err := cmd.Build(ctx, *c)
	if err != nil {
		return err
	}
	defer s.Close()

	v, err := pd.Digests(ctx, s.Ent, cs.Digest, s.Trail)
	if err != nil {
		return err
	}

	self.Printf("%d rows read: %d match, %d differ, %d forged, %d with no digest, %d not on the trail, %d under a key not given, %d missing; "+
		"%d from before digests began, %d under a key let go; %d forgotten; %d checked against the archive\n",
		v.Read, v.Matches, len(v.Differs), len(v.Forged), len(v.NoDigest), len(v.NotOnTrail), len(v.KeyNotHeld), len(v.Missing),
		v.BeforeDigests, v.Retired, v.Forgotten, v.Archived)
	// What an acceptance of these findings names (payday#115).
	digest := v.Digest()
	self.Printf("digest %s\n", digest)
	// The rows whose newest write sealed nothing, whose personal fields a
	// check cannot vouch for (payday#115): no finding, since nothing can be
	// said of what they were, and counted every run rather than listed --
	// after the upgrade that brought seals it is every row of a kind with a
	// personal field. The acceptance that seals them is the one below, of
	// this digest, which names them.
	if n := len(v.Unsealed); n > 0 {
		self.Printf("%d rows from before seals: what is personal in them is vouched for by nothing until "+
			"`rove trail rows --accept --of %s --why ...` seals them\n", n, digest)
	}
	// Each finding a line, by what it is, then its kind and the row: what a
	// schedule greps and an operator looks up (payday#115).
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
		if len(f.ids) == 0 {
			continue
		}
		self.Printf("  %s by kind: %s\n", f.what, byKind(f.ids))
		for _, id := range f.ids {
			self.Printf("  %s %s %s\n", f.what, kindOf(id.Domain()), id)
		}
	}
	// And the keys those under a key not given were made under, for
	// `audit.digest.previous` or, once somebody has decided they are gone,
	// `retired`.
	for _, k := range v.Unheld {
		self.Printf("  unheld-key %s\n", k)
	}
	// What was not looked at, every time, so that a check that found nothing
	// says what it did not read.
	for _, k := range v.Skipped {
		self.Printf("  not checked: %s, kept off the trail: %s\n", k.Kind, k.Why)
	}
	// Nothing found and nothing to seal is nothing to accept. Rows from
	// before seals are no finding, so a check that found only them passes --
	// and is still something `--accept` puts on the trail, which is how they
	// are sealed.
	if v.Ok() && (!accept || len(v.Unsealed) == 0) {
		return nil
	}

	findings := len(v.Differs) + len(v.Forged) + len(v.NoDigest) + len(v.NotOnTrail) + len(v.KeyNotHeld) + len(v.Missing)
	if !accept {
		return fmt.Errorf("%d findings: look at each, and `rove trail rows --accept --of %s --why ...` once the rows are right", findings, digest)
	}
	// What was looked at, and nothing found since: an acceptance of rows
	// somebody changed after the check that was read is of rows nobody looked
	// at (payday#115).
	if of != digest {
		return fmt.Errorf("--of: the check looked at was %s, and this one found %s: "+
			"run `rove trail rows` again, and look at what it finds", of, digest)
	}
	// A line whose digest is not its own, and one under a key not given, are
	// nothing an acceptance can make right: the one is somebody's writing,
	// and the other is a check that could not read it.
	if n := len(v.Forged) + len(v.KeyNotHeld); n > 0 {
		return fmt.Errorf("%d lines whose digest is not their own or is under a key not given: those are not accepted -- "+
			"look at the line of the trail, or give the key in audit.digest.previous or retired", n)
	}

	there, err := pd.Record(ctx, s.Ent, cs, AcceptedRows, slices.Concat(v.Differs, v.NoDigest, v.NotOnTrail))
	if err != nil {
		return fmt.Errorf("accept: %w", err)
	}
	gone, err := pd.RecordErased(ctx, s.Ent, cs, AcceptedRows, v.Missing)
	if err != nil {
		return fmt.Errorf("accept: %w", err)
	}
	// And the rows from before seals, which `pd.Record` seals as it records
	// them: what the operator looked at was the count and the digest that
	// names each, and what they are now is what the seal vouches for from
	// here on.
	sealed, err := pd.Record(ctx, s.Ent, cs, AcceptedRows, v.Unsealed)
	if err != nil {
		return fmt.Errorf("accept: %w", err)
	}
	log.From(ctx).InfoContext(ctx, "trail: rows accepted as they are", "of", digest, "why", why, "rows", there, "gone", gone, "sealed", sealed)
	self.Printf("accepted %d rows and %d gone, and sealed %d from before seals; the trail says they are what they are now\n", there, gone, sealed)

	return nil
}

// AcceptedRows is the act `rove trail rows --accept` is on the trail as.
const AcceptedRows = "rove.trail.rows.accept"

// kinds is the name this app's schema gives each of its domains. Not pdid's:
// the roster in this process registers its own kinds under the same numbers,
// and pdid names a number two apps claim by the number alone.
var kinds = func() map[pdid.Domain]string {
	out := map[pdid.Domain]string{}
	for full, d := range pd.Domains {
		out[d] = strings.TrimPrefix(full, "rove.")
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

// byKind is how many of `ids` are of each kind, the most first.
func byKind(ids []pdid.Id) string {
	n := map[string]int{}
	for _, id := range ids {
		n[kindOf(id.Domain())]++
	}
	names := slices.Collect(maps.Keys(n))
	slices.SortFunc(names, func(a, b string) int {
		if c := n[b] - n[a]; c != 0 {
			return c
		}
		return strings.Compare(a, b)
	})
	vs := make([]string, len(names))
	for i, v := range names {
		vs[i] = fmt.Sprintf("%s %d", v, n[v])
	}
	return strings.Join(vs, ", ")
}

// accept is an acceptance of what a verification found, and of nothing else:
// `a.Of` is the digest that verification printed (payday#115). Who accepts is
// `a.By`, or the user and host of the shell it is run from.
func accept(ctx context.Context, self *xli.Command, c *cmd.Config, a trail.Acceptance) error {
	if a.Of == "" {
		return errors.New("--of: the digest `rove trail verify` printed of the findings looked at")
	}
	if strings.TrimSpace(a.Why) == "" {
		return errors.New("--why says what was looked at and decided")
	}
	if a.By == "" {
		a.By = operator()
	}
	if a.By == "" {
		return errors.New("--by: who accepts, since this shell does not say")
	}

	s, p, err := archived(ctx, c)
	if err != nil {
		return err
	}
	defer s.Close()

	v, err := p.Accept(ctx, pd.TrailStore(s.Ent), a)
	for _, f := range v.Findings {
		self.Printf("  %s\n", f.String())
	}
	if errors.Is(err, trail.ErrUnreviewed) || errors.Is(err, trail.ErrRestored) {
		again := "rove trail verify"
		if a.Full {
			again += " --full"
		}
		return fmt.Errorf("%w\n\nrun `%s` again, look at what it finds, and accept its digest", err, again)
	}
	if err != nil {
		return err
	}

	self.Printf("accepted %d findings, by %s; the manifest says what the archive holds\n", len(v.Findings), a.By)
	return nil
}

// operator is who accepts when `--by` does not say: the user and the host of
// the shell it is run from, and empty when neither can be told.
func operator() string {
	name := ""
	if u, err := user.Current(); err == nil {
		name = u.Username
	}
	host, _ := os.Hostname()

	switch {
	case name != "" && host != "":
		return name + "@" + host
	case name != "":
		return name
	default:
		return host
	}
}
