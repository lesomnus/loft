package cli

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/lesomnus/payday/pdid"
	"github.com/lesomnus/xli"
	"github.com/lesomnus/xli/flg"

	"github.com/lesomnus/rove/cmd"
	"github.com/lesomnus/rove/internal/ent"
	"github.com/lesomnus/rove/internal/ent/aliashold"
	"github.com/lesomnus/rove/server/closing"
	"github.com/lesomnus/rove/server/offboard"
	"github.com/lesomnus/rove/server/pd"
	"github.com/lesomnus/rove/server/retention"
)

// NewCmdTenant is `rove tenant`: what leaves with a tenant (design 8.2).
//
// The order is export, the grace an offboarding gives, then a retirement or a
// purge, and it is the operator's to keep. They are commands and not RPCs: no
// role in a tenant may take everything out of it.
func NewCmdTenant(c *cmd.Config) *xli.Command {
	return &xli.Command{
		Name:  "tenant",
		Brief: "a tenant's end: closing, restoring, its address, its export, and its retirement or purge",

		Commands: xli.Commands{
			newCmdTenantClose(c),
			newCmdTenantRestore(c),
			newCmdTenantAlias(c),
			{
				Name:  "export",
				Brief: "everything a tenant has, as one zip archive",
				Flags: flg.Flags{
					&flg.String{Name: "tenant", Brief: "the tenant, by alias"},
					&flg.String{Name: "out", Brief: "where to write it; <alias>-<date>.zip by default, and - is stdout"},
				},
				Handler: xli.OnRun(func(ctx context.Context, self *xli.Command, next xli.Next) error {
					s, err := cmd.Build(ctx, *c)
					if err != nil {
						return err
					}
					defer s.Close()

					t, err := tenantNamed(ctx, s.Ent, self)
					if err != nil {
						return err
					}
					out, _ := flg.Find[string](self, "out")
					if strings.TrimSpace(out) == "" {
						out = fmt.Sprintf("%s-%s.zip", t.Alias, time.Now().Format(time.DateOnly))
					}

					// What this says goes where the archive does not: on stdout
					// beside a file, on stderr when the archive is stdout.
					say := self.Writer
					var m offboard.Manifest
					if out == stdio {
						// Half an archive is what a failure leaves in whatever
						// caught it, as no file is there to take back; the exit
						// status says so, and `tenant verify` refuses it.
						say, out = self.ErrWriter, "stdout"
						w := bufio.NewWriter(self.Writer)
						m, err = s.Offboard(*c).Export(ctx, pdid.Id(t.Id), w)
						if err == nil {
							err = w.Flush()
						}
						if err != nil {
							return err
						}
					} else {
						f, err := os.OpenFile(out, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
						if err != nil {
							return err
						}
						m, err = s.Offboard(*c).Export(ctx, pdid.Id(t.Id), f)
						if cerr := f.Close(); err == nil {
							err = cerr
						}
						if err != nil {
							os.Remove(out)
							return err
						}
					}

					rows := 0
					for _, n := range m.Rows {
						rows += n
					}
					fmt.Fprintf(say, "%s: %d rows of %d kinds, %d files, the trail's %d rows and %d archived\n",
						out, rows, len(m.Rows), m.Files, m.Trail.Database, m.Trail.Archive)
					if len(m.Missing) > 0 {
						fmt.Fprintf(say, "  missing from the file store: %s\n", strings.Join(m.Missing, ", "))
					}
					return nil
				}),
			},
			{
				Name:  "verify",
				Brief: "whether an export is what this deployment, or one it trusts, wrote",
				Flags: flg.Flags{
					&flg.String{Name: "file", Brief: "the export, or - to read it from stdin"},
				},
				Handler: xli.OnRun(func(ctx context.Context, self *xli.Command, next xli.Next) error {
					path, _ := flg.Find[string](self, "file")
					if strings.TrimSpace(path) == "" {
						return fmt.Errorf("--file: the export to verify")
					}
					p, err := c.Audit.Policy()
					if err != nil {
						return err
					}
					f, size, done, err := spooled(self, path)
					if err != nil {
						return err
					}
					defer done()

					m, err := offboard.Verify(f, size, offboard.TrustedBy(p))
					if err != nil {
						return fmt.Errorf("%s: %w", named(path), err)
					}
					manifest, key, err := offboard.Fingerprints(f, size)
					if err != nil {
						return fmt.Errorf("%s: %w", named(path), err)
					}
					var t struct {
						Alias string `json:"alias"`
					}
					_ = json.Unmarshal(m.Tenant, &t)
					self.Printf("%s: %s, exported %s, signed by a trusted key; every one of its %d files is what was exported\n",
						named(path), or(t.Alias, "a tenant"), m.ExportedAt.Format(time.RFC3339), len(m.Digests))
					self.Printf("  manifest %s, key %s\n", manifest, key)
					return nil
				}),
			},
			newCmdTenantImport(c),
			newCmdTenantRetired(c),
			newCmdTenantLeave(c, "retire", "take a tenant out of the deployment, keeping what the deployment's floors are on of its trail until they pass", closing.Retire),
			newCmdTenantLeave(c, "purge", "take everything of a tenant out of the deployment, its whole trail with it, the tenant last", closing.Purge),
		},
	}
}

// newCmdTenantLeave is `rove tenant retire` and `rove tenant purge`: a
// workspace destroyed by an operator's hand, as the closing sweep destroys one
// whose restore window is over ([closing.Closing.Destroy]) -- its rows, its
// files, the accounts of the roster in this process that were here alone, and
// its address held -- with its trail as the command says (payday#115).
//
// `retire` keeps what the deployment's floors (`audit.min`,
// `audit.by.<kind>.min`) are still on until each has passed: the access record
// the law has the deployment keep whoever's data it was. `purge` takes that
// too, for a workspace under a law or an agreement that asks for it. The
// sweep does what the workspace's contract says (`purge_floors`); these do
// what they are called, and say so when the contract says the other.
func newCmdTenantLeave(c *cmd.Config, name, brief string, how closing.How) *xli.Command {
	return &xli.Command{
		Name:  name,
		Brief: brief,
		Flags: flg.Flags{
			&flg.String{Name: "tenant", Brief: "the tenant, by alias"},
			&flg.Switch{Name: "yes", Brief: "do it; without this it only says what it would take"},
		},
		Handler: xli.OnRun(func(ctx context.Context, self *xli.Command, next xli.Next) error {
			yes, _ := flg.Find[bool](self, "yes")

			s, err := cmd.Build(ctx, *c)
			if err != nil {
				return err
			}
			defer s.Close()

			t, err := tenantNamed(ctx, s.Ent, self)
			if err != nil {
				return err
			}
			w, err := retention.Of(ctx, s.Ent, pdid.Id(t.Id), time.Now(), cmd.RetentionDefaults(*c, s.Trail))
			if err != nil {
				return err
			}

			x, err := s.Closing.Destroy(ctx, t, how, !yes)
			if err != nil {
				return fmt.Errorf("%s: %w", t.Alias, err)
			}

			verb := "took"
			if !yes {
				verb = "would take"
			}
			parts := []string{}
			for _, k := range slices.Sorted(maps.Keys(x.Rows)) {
				parts = append(parts, fmt.Sprintf("%s %d", k, x.Rows[k]))
			}
			self.Printf("%s: %s %d rows (%s), %d files\n", t.Alias, verb, x.Total(), strings.Join(parts, ", "), x.Files)
			self.Printf("  trail: %d rows removed, %d blanked, %d archived chunks\n", x.Trail.Removed, x.Trail.Blanked, x.Trail.Chunks)
			if x.Retired {
				if len(x.Trail.Kept) == 0 {
					self.Printf("  kept: nothing, since no floor of the deployment's is on any of it\n")
				} else {
					kept := []string{}
					for _, k := range slices.Sorted(maps.Keys(x.Trail.Kept)) {
						kept = append(kept, fmt.Sprintf("%s %d", k, x.Trail.Kept[k]))
					}
					self.Printf("  kept until %s, for the deployment's floors: %s\n", x.Trail.Until.Format(time.DateOnly), strings.Join(kept, ", "))
				}
			}
			if s.Identity.Embedded() {
				verb := "forgot"
				if !yes {
					verb = "would forget"
				}
				self.Printf("  roster in this process: %s %d account(s) in no other workspace, and what its trail said of them\n", verb, x.Forgotten)
			}
			if len(x.Lost) > 0 {
				self.Printf("  files that could not be removed: %s\n", strings.Join(x.Lost, ", "))
			}
			if !yes {
				switch {
				case how == closing.Purge && !w.Purge:
					self.Printf("(its contract retires it, keeping what the deployment's floors are on: `rove tenant retire` does what it says)\n")
				case how == closing.Retire && w.Purge:
					self.Printf("(its contract purges it, what the deployment's floors are on too: `rove tenant purge` does what it says)\n")
				}
				self.Printf("(nothing was taken: `--yes` takes it. Export first -- `rove tenant export --tenant %s`)\n", t.Alias)
			}
			return nil
		}),
	}
}

// newCmdTenantRetired is `rove tenant retired`: the workspaces that retired
// from this deployment and what is left of each -- when it retired, and until
// when the deployment's floors keep what it kept (`trail.Policy.Retired`).
// What an operator asked to preserve a workspace's records reads first:
// whether they are still there (docs/runbook/preserve.md).
func newCmdTenantRetired(c *cmd.Config) *xli.Command {
	return &xli.Command{
		Name:  "retired",
		Brief: "the workspaces that retired, when, and until when what they kept is kept",
		Handler: xli.OnRun(func(ctx context.Context, self *xli.Command, next xli.Next) error {
			s, err := cmd.Build(ctx, *c)
			if err != nil {
				return err
			}
			defer s.Close()

			vs, err := s.Trail.Retired(ctx, pd.TrailStore(s.Ent))
			if err != nil {
				return err
			}
			ids := slices.SortedFunc(maps.Keys(vs), func(a, b pdid.Id) int { return vs[a].At.Compare(vs[b].At) })
			now := time.Now()
			for _, id := range ids {
				v := vs[id]
				alias := "-"
				if h, err := s.Ent.AliasHold.Query().
					Where(aliashold.Tenant(id.Uuid()), aliashold.Reason(offboard.ReasonRetired)).
					Order(ent.Desc(aliashold.FieldDateCreated)).
					First(ctx); err == nil {
					alias = "@" + h.Held
				} else if !ent.IsNotFound(err) {
					return err
				}
				left := "kept until " + v.Until.Format(time.DateOnly)
				if !now.Before(v.Until) {
					left = "past its floors since " + v.Until.Format(time.DateOnly) + ": the next pass takes what is left"
				}
				self.Printf("%s\t%s\tretired %s\t%s\n", alias, id, v.At.Format(time.DateOnly), left)
			}
			if len(ids) == 0 {
				self.Printf("no workspace has retired, or what each kept is gone\n")
			}
			return next(ctx)
		}),
	}
}

// newCmdTenantImport is `rove tenant import`: a workspace back from its signed
// export, into a deployment that does not have it -- the one it was purged
// from, or another (design D34). Like a purge, it says what it would do until
// it is told `--yes`.
func newCmdTenantImport(c *cmd.Config) *xli.Command {
	return &xli.Command{
		Name:  "import",
		Brief: "restore a workspace from its signed export, into a deployment that does not have it",
		Flags: flg.Flags{
			&flg.String{Name: "file", Brief: "the export, or - to read it from stdin"},
			&flg.String{Name: "alias", Brief: "the address to restore it at, instead of the export's"},
			&flg.Switch{Name: "reuse", Brief: "take the address though another workspace's hold is on it"},
			&flg.Switch{Name: "yes", Brief: "do it; without this it only says what it would put back"},
		},
		Handler: xli.OnRun(func(ctx context.Context, self *xli.Command, next xli.Next) error {
			path, _ := flg.Find[string](self, "file")
			if strings.TrimSpace(path) == "" {
				return fmt.Errorf("--file: the export to restore")
			}
			alias, _ := flg.Find[string](self, "alias")
			reuse, _ := flg.Find[bool](self, "reuse")
			yes, _ := flg.Find[bool](self, "yes")

			s, err := cmd.Build(ctx, *c)
			if err != nil {
				return err
			}
			defer s.Close()

			f, size, done, err := spooled(self, path)
			if err != nil {
				return err
			}
			defer done()

			x, err := s.Offboard(*c).Import(ctx, f, size, offboard.ImportOptions{Alias: alias, Reuse: reuse, Dry: !yes})
			if err != nil {
				return fmt.Errorf("%s: %w", named(path), err)
			}

			verb := "put back"
			if !yes {
				verb = "would put back"
			}
			parts := []string{}
			for _, k := range slices.Sorted(maps.Keys(x.Rows)) {
				parts = append(parts, fmt.Sprintf("%s %d", k, x.Rows[k]))
			}
			self.Printf("@%s: %s %d rows (%s), %d files\n", x.Alias, verb, x.Total(), strings.Join(parts, ", "), x.Files)
			self.Printf("  from the export whose manifest is %s, signed by %s: keep it, for the trail it holds is not put back\n", x.Manifest, x.Key)
			if x.Asked != "" {
				self.Printf("  at @%s: the export's @%s is another workspace's here, or not one this deployment gives\n", x.Alias, x.Asked)
			}
			for _, r := range x.Released {
				self.Printf("  the address was held (%s), and is taken back\n", r)
			}
			if x.Reopened {
				self.Printf("  it was closed when it was exported, and is open\n")
			}
			if x.Retired {
				if yes {
					self.Printf("  it retired from here, and the trail took it back first: %d records of the retirement lifted\n", x.Returned)
				} else {
					self.Printf("  it retired from here: the trail takes it back first, before anything of it is written\n")
				}
			}
			if len(x.Missing) > 0 {
				self.Printf("  files the export did not have, as the store it came from did not: %s\n", strings.Join(x.Missing, ", "))
			}
			if len(x.Unlinked) > 0 {
				self.Printf("  %d member(s) whose accounts this roster does not have, restored with none: %s\n", len(x.Unlinked), strings.Join(x.Unlinked, ", "))
				self.Printf("  (nobody of them signs in until `rove identity migrate` gives each an account)\n")
			}
			if !yes {
				self.Printf("(nothing was put back: `--yes` does it)\n")
			}
			return nil
		}),
	}
}

// stdio is what `--out` and `--file` take, in place of a file, for the
// command's own stdout or stdin. A deployment's image has nothing to copy a
// file out of a container with, or into one, so an export leaves through
// `kubectl exec` and comes back the same way (rove#126; `deploy/k8s`).
const stdio = "-"

// spooled is the export `path` names, opened, with its size and what to call
// when done with it. For [stdio] it is what the command reads, written to a
// temporary file first -- one only this user can read, as an export is the
// whole of a workspace -- because an archive is read out of order and a pipe
// only once. Done takes that file away again.
func spooled(self *xli.Command, path string) (*os.File, int64, func(), error) {
	if path != stdio {
		f, err := os.Open(path)
		if err != nil {
			return nil, 0, nil, err
		}
		st, err := f.Stat()
		if err != nil {
			f.Close()
			return nil, 0, nil, err
		}
		return f, st.Size(), func() { f.Close() }, nil
	}

	f, err := os.CreateTemp("", "rove-export-*.zip")
	if err != nil {
		return nil, 0, nil, err
	}
	done := func() {
		f.Close()
		os.Remove(f.Name())
	}
	n, err := io.Copy(f, self.ReadCloser)
	if err != nil {
		done()
		return nil, 0, nil, fmt.Errorf("stdin: %w", err)
	}
	return f, n, done, nil
}

// named is what a message calls the export `path` names.
func named(path string) string {
	if path == stdio {
		return "stdin"
	}
	return path
}
