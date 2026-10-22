package cli

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"slices"
	"strings"
	"time"
	"uuid"

	"filippo.io/age"
	"filippo.io/age/plugin"
	"github.com/lesomnus/payday/access"
	"github.com/lesomnus/payday/frame"
	"github.com/lesomnus/payday/pdid"
	"github.com/lesomnus/payday/pdpb"
	"github.com/lesomnus/payday/trail"
	"github.com/lesomnus/xli"
	"github.com/lesomnus/xli/flg"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"

	"github.com/lesomnus/rove/cmd"
	"github.com/lesomnus/rove/internal/ent"
	"github.com/lesomnus/rove/internal/ent/aliashold"
	enttenant "github.com/lesomnus/rove/internal/ent/tenant"
	"github.com/lesomnus/rove/server/offboard"
	"github.com/lesomnus/rove/server/pd"
)

// exportAction is what `rove trail export` is called in the access record: the
// command, since it is no RPC.
const exportAction = "rove trail export"

// newCmdTrailExport is `rove trail export`: a workspace's trail taken out,
// encrypted to whoever keeps it, for a demand to preserve it (rove#153).
//
// Nothing holds what a workspace that retired left past the deployment's
// floors -- a legal hold is on a workspace there is, and one destroyed has no
// row to hold -- so a demand that comes while it is still there is answered by
// taking it out, for keeping apart from the deployment, and a live workspace's
// the same way. What goes is payday's to say (`trail.Policy.ExportTenant`):
// every row of the trail that names the workspace, in the database and in the
// archive, and a manifest the deployment's key signs, naming the newest
// checkpoint and what its witnesses answered. It is written encrypted to
// `--to` and never otherwise.
//
// It hands people's data out, so it is a download: it says why, which is
// required, and its row of the access record is written before its first byte
// -- naming everybody whose data it carries by the identifiers its rows carry,
// since a destroyed workspace's people are no rows any more -- and the trail
// keeps it as an act of its own, with who and why.
func newCmdTrailExport(c *cmd.Config) *xli.Command {
	return &xli.Command{
		Name:  "export",
		Brief: "take a workspace's trail out, encrypted to whoever keeps it, for a demand to preserve it",
		Flags: flg.Flags{
			&flg.String{Name: "tenant", Brief: "the workspace: its identifier, or its address -- a destroyed one's, as the hold on it names it"},
			&flg.String{Name: "why", Brief: "the request it answers: a case, a demand to preserve; it is recorded"},
			&flg.String{Name: "by", Brief: "who takes it out; the operator's user and host by default"},
			&flg.String{Name: "to", Brief: "whoever keeps it: an age recipient (age1…, age1yubikey1…), or a file of them"},
			&flg.String{Name: "out", Brief: "the file to write, which must not be there, or - for stdout"},
		},
		Handler: xli.OnRun(func(ctx context.Context, self *xli.Command, next xli.Next) error {
			why := strings.TrimSpace(flag(self, "why"))
			if why == "" {
				return errors.New("--why: what it answers -- it hands people's data out, and is recorded")
			}
			by := strings.TrimSpace(flag(self, "by"))
			if by == "" {
				by = operator()
			}
			if by == "" {
				return errors.New("--by: who takes it out, since this shell does not say")
			}
			out := strings.TrimSpace(flag(self, "out"))
			if out == "" {
				return errors.New("--out: the file to write, or - for stdout")
			}
			to, err := recipientsOf(strings.TrimSpace(flag(self, "to")))
			if err != nil {
				return fmt.Errorf("--to: %w", err)
			}

			s, err := cmd.Build(ctx, *c)
			if err != nil {
				return err
			}
			defer s.Close()

			tenant, alias, err := workspaceOf(ctx, s.Ent, flag(self, "tenant"))
			if err != nil {
				return fmt.Errorf("--tenant: %w", err)
			}

			// The access record first: a download is recorded before anything
			// of it leaves.
			people, err := carried(ctx, s, tenant)
			if err != nil {
				return err
			}
			as := frame.Into(ctx, frame.New(pdid.Nil, tenant, frame.Grant{}).WithScope(frame.Only(tenant)))
			d := pdpb.AccessDetail_builder{Address: by, Reason: why}.Build()
			if err := s.Access.Write(ctx, access.Records(as, exportAction, access.Download, "ok", people, d, time.Now())...); err != nil {
				return fmt.Errorf("the access record, which an export is written into before its first byte: %w", err)
			}

			say := self.Writer
			var w io.Writer
			var done func(ok bool) error
			if out == stdio {
				say = self.ErrWriter
				bw := bufio.NewWriter(self.Writer)
				w, done = bw, func(bool) error { return bw.Flush() }
			} else {
				f, err := os.OpenFile(out, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
				if err != nil {
					return err
				}
				w, done = f, func(ok bool) error {
					err := f.Close()
					if !ok {
						os.Remove(out)
					}
					return err
				}
			}

			v, err := s.Trail.ExportTenant(ctx, pd.TrailStore(s.Ent), tenant, trail.Export{By: by, Why: why, To: to}, w)
			if cerr := done(err == nil); err == nil {
				err = cerr
			}
			if err != nil {
				return err
			}

			whose := tenant.String()
			if alias != "" {
				whose = "@" + alias + " (" + whose + ")"
			}
			where := out
			if out == stdio {
				where = "stdout"
			}
			fmt.Fprintf(say, "%s: the trail of %s, %d rows of the database and %d of the archive in %d chunks, %d files\n",
				where, whose, v.Database, v.Archive, len(v.Chunks), len(v.Files))
			fmt.Fprintf(say, "  manifest %s, signed by the deployment\n", v.Digest)
			if cp := v.Checkpoint; cp != nil {
				witnesses := []string{}
				for _, w := range cp.Witnessed {
					witnesses = append(witnesses, w.Witness)
				}
				uncovered := slices.ContainsFunc(v.Chunks, func(c trail.ExportChunk) bool { return !c.Covered })
				fmt.Fprintf(say, "  held to checkpoint %d (%s), heard by %s\n", cp.K, cp.Digest, or(strings.Join(witnesses, ", "), "no witness"))
				if uncovered {
					fmt.Fprintf(say, "  (a chunk the checkpoint does not list is in it: the manifest marks it, and `rove trail verify` says why)\n")
				}
			}
			if r := v.Retired; r != nil {
				fmt.Fprintf(say, "  retired %s; what it kept is kept until %s\n", day(r.At), day(r.Until))
			}
			fmt.Fprintf(say, "  %d people in the access record, as a download, for %q\n", len(people), why)

			return next(ctx)
		}),
	}
}

// newCmdTrailVerifyExport is `rove trail verify-export`: whether a bundle is
// what the deployment wrote -- every file what its manifest says, and the
// manifest signed by a key the deployment trusts (`trail.OpenTenantExport`).
func newCmdTrailVerifyExport(c *cmd.Config) *xli.Command {
	return &xli.Command{
		Name:  "verify-export",
		Brief: "whether a bundle `trail export` wrote is what the deployment wrote",
		Flags: flg.Flags{
			&flg.String{Name: "file", Brief: "the bundle, or - for stdin"},
			&flg.String{Name: "identity", Brief: "the identity it was encrypted to: a file of age-keygen's, or a plugin's (AGE-PLUGIN-…)"},
		},
		Handler: xli.OnRun(func(ctx context.Context, self *xli.Command, next xli.Next) error {
			path := strings.TrimSpace(flag(self, "file"))
			if path == "" {
				return errors.New("--file: the bundle to verify")
			}
			ids, err := identitiesOf(strings.TrimSpace(flag(self, "identity")))
			if err != nil {
				return fmt.Errorf("--identity: %w", err)
			}
			p, err := c.Audit.Policy()
			if err != nil {
				return err
			}
			trusted := slices.Clone(p.Trust)
			if p.Signer != nil {
				trusted = append(trusted, p.Signer.Public())
			}
			if len(trusted) == 0 {
				return errors.New("no key to hold it to: audit.checkpoints.key, or the deployment's public key in audit.checkpoints.trust")
			}

			var r io.Reader
			if path == stdio {
				r = bufio.NewReader(self.ReadCloser)
			} else {
				f, err := os.Open(path)
				if err != nil {
					return err
				}
				defer f.Close()
				r = bufio.NewReader(f)
			}

			v, err := trail.OpenTenantExport(r, ids, trusted, nil)
			if err != nil {
				return fmt.Errorf("%s: %w", named(path), err)
			}

			self.Printf("%s: the trail of %s, taken out %s by %s for %q\n", named(path), v.Tenant, v.At.Format(time.RFC3339), v.By, v.Why)
			self.Printf("  every one of its %d files is what its manifest says, and a key the deployment trusts signed it\n", len(v.Files))
			self.Printf("  %d rows of the database, %d of the archive in %d chunks\n", v.Database, v.Archive, len(v.Chunks))
			self.Printf("  manifest %s\n", v.Digest)
			if cp := v.Checkpoint; cp != nil {
				covered := 0
				for _, c := range v.Chunks {
					if c.Covered {
						covered++
					}
				}
				self.Printf("  checkpoint %d (%s) of %s lists %d of its %d chunks; %d witness receipt(s)\n",
					cp.K, cp.Digest, cp.At.Format(time.RFC3339), covered, len(v.Chunks), len(cp.Witnessed))
			}
			if r := v.Retired; r != nil {
				self.Printf("  retired %s, kept until %s\n", day(r.At), day(r.Until))
			}

			return next(ctx)
		}),
	}
}

// workspaceOf is the workspace `v` names: by its identifier, or by its
// address -- a live workspace's, or a destroyed one's as the newest hold on it
// says, which is all that is left of what it was called.
func workspaceOf(ctx context.Context, db *ent.Client, v string) (pdid.Id, string, error) {
	v = strings.TrimPrefix(strings.TrimSpace(v), "@")
	if v == "" {
		return pdid.Nil, "", errors.New("which workspace: its identifier, or its address")
	}
	if id, err := pdid.Parse(v); err == nil {
		return id, "", nil
	}

	t, err := db.Tenant.Query().Where(enttenant.Alias(v)).Only(ctx)
	if err == nil {
		return pdid.Id(t.Id), t.Alias, nil
	}
	if !ent.IsNotFound(err) {
		return pdid.Nil, "", err
	}

	h, err := db.AliasHold.Query().
		Where(aliashold.Held(v), aliashold.ReasonIn(offboard.ReasonDestroyed, offboard.ReasonRetired)).
		Order(ent.Desc(aliashold.FieldDateCreated)).
		First(ctx)
	if ent.IsNotFound(err) {
		return pdid.Nil, "", fmt.Errorf("no workspace is @%s, nor was one destroyed under it", v)
	}
	if err != nil {
		return pdid.Nil, "", err
	}

	return pdid.Id(h.Tenant), v, nil
}

// carried is everybody a workspace's trail carries the data of, by the
// identifiers its rows carry: whoever acted, the people its rows are about --
// a row of a kind that holds a field declared personal -- and in the access
// record whose data each call carried. A destroyed workspace's people are no
// rows any more, so these are all there is to name them by.
func carried(ctx context.Context, s *cmd.Server, tenant pdid.Id) ([]access.Subject, error) {
	seen := map[pdid.Id]bool{}
	out := []access.Subject{}
	add := func(id pdid.Id) {
		if id.IsZero() || seen[id] {
			return
		}
		seen[id] = true
		out = append(out, access.Subject{Id: id, Tenant: tenant})
	}
	read := func(doc []byte) error {
		var v struct {
			Actor    []byte   `json:"actorId"`
			Object   []byte   `json:"objectId"`
			Domain   uint32   `json:"domain"`
			Subjects [][]byte `json:"subjects"`
		}
		if err := json.Unmarshal(doc, &v); err != nil {
			return err
		}
		add(rowId(v.Actor))
		if personal[pdid.Domain(v.Domain)] {
			add(rowId(v.Object))
		}
		for _, b := range v.Subjects {
			add(rowId(b))
		}
		return nil
	}

	// What is still in the database: the rows that name the workspace, its
	// alone and those it shares, as the export reads them.
	store := pd.TrailStore(s.Ent)
	until := time.Now().Add(time.Hour)
	for _, whose := range []trail.Whose{trail.Alone, trail.Together} {
		at := trail.Cursor{}
		for {
			vs, err := store.Older(ctx, trail.Scope{Whose: whose, Tenant: tenant}, until, at, trail.Batch)
			if err != nil {
				return nil, err
			}
			for _, v := range vs {
				if err := read(v.Doc); err != nil {
					return nil, err
				}
				at = trail.Cursor{Created: v.Created, Key: v.Key}
			}
			if len(vs) < trail.Batch {
				break
			}
		}
	}

	// And what the archive holds of it.
	if s.Trail.Archive != nil {
		if err := s.Trail.ReadTenant(ctx, store, tenant, read); err != nil {
			return nil, err
		}
	}

	return out, nil
}

// rowId is an identifier as protojson carries one, base64 of its bytes, and
// nil when it is none.
func rowId(b []byte) pdid.Id {
	if len(b) != len(uuid.UUID{}) {
		return pdid.Nil
	}
	return pdid.Id(uuid.UUID(b))
}

// personal is the kinds whose rows hold a person: a field of theirs declared
// personal (`payday.field`), read off the schema.
var personal = func() map[pdid.Domain]bool {
	out := map[pdid.Domain]bool{}
	for name, d := range pd.Domains {
		mt, err := protoregistry.GlobalTypes.FindMessageByName(protoreflect.FullName(name))
		if err != nil {
			continue
		}
		fs := mt.Descriptor().Fields()
		for i := range fs.Len() {
			o, _ := proto.GetExtension(fs.Get(i).Options(), pdpb.E_Field).(*pdpb.Field)
			if o.GetPersonal() {
				out[d] = true
				break
			}
		}
	}
	return out
}()

// recipientsOf is whoever `v` names, as age reads a recipient: one, or a file
// of them, each line age's own -- `age1…`, an SSH key -- or a plugin's, sealed
// to by running `age-plugin-<name>` without its device.
func recipientsOf(v string) ([]age.Recipient, error) {
	if v == "" {
		return nil, errors.New("whoever keeps it: an age recipient, or a file of them")
	}
	doc, err := fileOr(v)
	if err != nil {
		return nil, err
	}

	out := []age.Recipient{}
	err = ageLines(doc, func(line string) error {
		if rs, err := age.ParseRecipients(strings.NewReader(line)); err == nil {
			out = append(out, rs...)
			return nil
		} else if name, _, perr := plugin.ParseRecipient(line); perr == nil {
			if _, err := exec.LookPath("age-plugin-" + name); err != nil {
				return fmt.Errorf("age-plugin-%s is not on PATH, and it is what this key is used through", name)
			}
			r, err := plugin.NewRecipient(line, sealing)
			if err != nil {
				return err
			}
			out = append(out, r)
			return nil
		} else {
			return err
		}
	})
	if err != nil {
		return nil, err
	}
	if len(out) == 0 {
		return nil, errors.New("no recipient")
	}

	return out, nil
}

// identitiesOf is the identities `v` names: a file of them, each age's own or
// a plugin's, which opens with its device and asks this terminal for what it
// wants.
func identitiesOf(v string) ([]age.Identity, error) {
	if v == "" {
		return nil, errors.New("the identity it was encrypted to, a file of age-keygen's or a plugin's")
	}
	doc, err := fileOr(v)
	if err != nil {
		return nil, err
	}

	out := []age.Identity{}
	err = ageLines(doc, func(line string) error {
		if !strings.HasPrefix(line, "AGE-PLUGIN-") {
			ids, err := age.ParseIdentities(strings.NewReader(line))
			if err != nil {
				return err
			}
			out = append(out, ids...)
			return nil
		}
		name, _, err := plugin.ParseIdentity(line)
		if err != nil {
			return errors.New("an AGE-PLUGIN- identity that does not read as one")
		}
		if _, err := exec.LookPath("age-plugin-" + name); err != nil {
			return fmt.Errorf("age-plugin-%s is not on PATH, and it is what this key is used through", name)
		}
		id, err := plugin.NewIdentity(line, plugin.NewTerminalUI(
			func(format string, v ...any) { fmt.Fprintf(os.Stderr, format+"\n", v...) },
			func(format string, v ...any) { fmt.Fprintf(os.Stderr, "warning: "+format+"\n", v...) },
		))
		if err != nil {
			return err
		}
		out = append(out, id)
		return nil
	})
	if err != nil {
		return nil, err
	}
	if len(out) == 0 {
		return nil, errors.New("no identity")
	}

	return out, nil
}

// fileOr is the file `v` names when there is one, and `v` itself otherwise.
func fileOr(v string) (string, error) {
	if st, err := os.Stat(v); err == nil && !st.IsDir() {
		b, err := os.ReadFile(v)
		return string(b), err
	}
	return v, nil
}

// ageLines hands over each line of an age file that says something.
func ageLines(doc string, fn func(line string) error) error {
	sc := bufio.NewScanner(strings.NewReader(doc))
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if err := fn(line); err != nil {
			return err
		}
	}
	return sc.Err()
}

// sealing is what a plugin may ask of a command that only seals: nothing,
// since the device is for opening.
var sealing = &plugin.ClientUI{
	DisplayMessage: func(name, message string) error {
		fmt.Fprintf(os.Stderr, "age-plugin-%s: %s\n", name, message)
		return nil
	},
	RequestValue: func(name, prompt string, _ bool) (string, error) {
		return "", fmt.Errorf("age-plugin-%s asks %q, and sealing asks nothing", name, prompt)
	},
	Confirm: func(name, prompt, _, _ string) (bool, error) {
		return false, fmt.Errorf("age-plugin-%s asks %q, and sealing asks nothing", name, prompt)
	},
}
