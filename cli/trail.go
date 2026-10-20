package cli

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"

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
// stopped, a chunk restored from a backup -- and says why.
//
// `rows` is the database's rows against the trail: with `audit.digest.key`,
// each row of the trail keeps a keyed digest of the row its write left, and
// this digests every live row again and compares it with the newest row of the
// trail about it -- in the database, or in the archive once it has left
// (payday#102, #108). A row somebody changed in the database past the servers
// is one that differs, and a row somebody added there is one nothing on the
// trail is about. It changes nothing either, and fails on either. `rows
// --accept` is an operator who has looked at each and decided the rows are
// right -- a migration rewrote them, they were put back before a restore said
// so -- and puts them on the trail as that act, with the why logged.
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
					&flg.String{Name: "why", Brief: "what was looked at and decided, with --accept; it is logged"},
				},
				Handler: xli.OnRun(func(ctx context.Context, self *xli.Command, next xli.Next) error {
					accept, _ := flg.Find[bool](self, "accept")
					why, _ := flg.Find[string](self, "why")
					return rows(ctx, self, c, accept, why)
				}),
			},
			{
				Name:  "accept",
				Brief: "make the manifest say what the archive holds, once somebody has looked",
				Flags: flg.Flags{
					&flg.String{Name: "why", Brief: "what was looked at and decided; it is logged"},
				},
				Handler: xli.OnRun(func(ctx context.Context, self *xli.Command, next xli.Next) error {
					why, _ := flg.Find[string](self, "why")
					return accept(ctx, self, c, why)
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

	if v.Ok() {
		self.Printf("the archive is what the database says it is\n")
		return nil
	}
	for _, f := range v.Findings {
		self.Printf("  %s\n", f.String())
	}

	return fmt.Errorf("%d findings: look at each, and `rove trail accept --why ...` once the archive is right", len(v.Findings))
}

func rows(ctx context.Context, self *xli.Command, c *cmd.Config, accept bool, why string) error {
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

	self.Printf("%d rows read: %d match, %d differ, %d not on the trail, %d with no digest, %d under a key not given; %d checked against the archive\n",
		v.Read, v.Matches, len(v.Differs), len(v.NotOnTrail), v.NoDigest, v.KeyNotHeld, v.Archived)
	for _, f := range []struct {
		what string
		ids  []pdid.Id
	}{
		{"differs", v.Differs},
		{"not-on-trail", v.NotOnTrail},
	} {
		if len(f.ids) == 0 {
			continue
		}
		self.Printf("  %s by kind: %s\n", f.what, byKind(f.ids))
		for _, id := range f.ids {
			self.Printf("  %s %s %s\n", f.what, kindOf(id.Domain()), id)
		}
	}
	if v.Ok() {
		return nil
	}
	if !accept {
		return fmt.Errorf("%d rows differ from what the trail recorded and %d are not on it: look at each, and `rove trail rows --accept --why ...` once they are right", len(v.Differs), len(v.NotOnTrail))
	}

	n, err := pd.Record(ctx, s.Ent, cs, AcceptedRows, slices.Concat(v.Differs, v.NotOnTrail))
	if err != nil {
		return fmt.Errorf("accept: %w", err)
	}
	log.From(ctx).InfoContext(ctx, "trail: rows accepted as they are", "why", why, "rows", n)
	self.Printf("accepted %d rows; the trail says they are what they are now\n", n)

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

func accept(ctx context.Context, self *xli.Command, c *cmd.Config, why string) error {
	if strings.TrimSpace(why) == "" {
		return errors.New("--why says what was looked at and decided")
	}

	s, p, err := archived(ctx, c)
	if err != nil {
		return err
	}
	defer s.Close()

	v, err := p.Accept(ctx, pd.TrailStore(s.Ent), why)
	for _, f := range v.Findings {
		self.Printf("  %s\n", f.String())
	}
	if err != nil {
		return err
	}

	self.Printf("accepted %d findings; the manifest says what the archive holds\n", len(v.Findings))
	return nil
}
