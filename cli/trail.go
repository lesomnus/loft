package cli

import (
	"bufio"
	"context"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"os/exec"
	"slices"
	"strings"
	"time"

	"filippo.io/age"
	"filippo.io/age/plugin"
	"github.com/lesomnus/payday/trail"
	"github.com/lesomnus/xli"
	"github.com/lesomnus/xli/arg"
	"github.com/lesomnus/xli/flg"
	"google.golang.org/grpc"

	app "github.com/lesomnus/loft"
)

// trailWritten is `loft trail`: the trail's archive checked against the
// account the database keeps of it, the live rows checked against the trail,
// what an operator who looked accepts, what its erasures owe, and a
// workspace's trail taken out (design 9.15) -- the operator's, but for a
// bundle held to the keys `--trust` names. `verify` and `rows` change nothing
// and fail when they found something, so that a schedule can run them.
func trailWritten() []written {
	return group(newCmdTrail(), map[string][]string{
		"verify":      {app.OperatorTrailService_Verify_FullMethodName},
		"accept":      {app.OperatorTrailService_Accept_FullMethodName},
		"rows":        {app.OperatorTrailService_Rows_FullMethodName},
		"accept-rows": {app.OperatorTrailService_AcceptRows_FullMethodName},
		"owed":        {app.OperatorTrailService_Owed_FullMethodName},
		"keys":        {app.OperatorTrailService_Keys_FullMethodName},
		"export":      {app.OperatorTrailService_Export_FullMethodName},
	})
}

func newCmdTrail() *xli.Command {
	return &xli.Command{
		Name:  "trail",
		Brief: "the trail's archive, checked against the account the database keeps of it",
		Commands: xli.Commands{
			{
				Name:  "verify",
				Brief: "compare the archive with its manifest and its checkpoints, changing nothing",
				Flags: flg.Flags{&flg.Switch{Name: "full", Brief: "also read every chunk whole and check its bytes"}},
				Handler: xli.OnRun(func(ctx context.Context, self *xli.Command, next xli.Next) error {
					full, _ := flg.Find[bool](self, "full")
					conn, err := operatorFor(ctx, self)
					if err != nil {
						return err
					}
					stream, err := app.NewOperatorTrailServiceClient(conn).Verify(ctx, app.OperatorTrailVerifyRequest_builder{Full: full}.Build())
					if err != nil {
						return err
					}
					var v *app.OperatorTrailVerified
					if err := each(stream, self, func(m *app.OperatorTrailVerifyResponse) string {
						if m.HasVerified() {
							v = m.GetVerified()
						}
						return m.GetProgress()
					}); err != nil {
						return err
					}
					if v == nil {
						return errors.New("the entry point said nothing of what it found")
					}
					read := fmt.Sprintf("%d rows in the manifest, %d blobs in the archive", v.GetRows(), v.GetBlobs())
					if full {
						read += fmt.Sprintf(", %d read whole", v.GetHashed())
					}
					if v.GetCheckpoint() > 0 {
						read += fmt.Sprintf("; checkpoint %d compared", v.GetCheckpoint())
					}
					self.Printf("%s\n", read)
					self.Printf("digest %s\n", v.GetDigest())
					for _, a := range v.GetAccepted() {
						self.Printf("accepted %s\n", a)
					}
					for _, h := range v.GetHeard() {
						self.Printf("%s\n", h)
					}
					if len(v.GetFindings()) == 0 {
						self.Printf("the archive is what the database says it is\n")
						return next(ctx)
					}
					behind := false
					for _, f := range v.GetFindings() {
						self.Printf("  %s\n", f.GetSaid())
						behind = behind || f.GetKind() == "behind"
					}
					how := "--of " + v.GetDigest()
					if full {
						how += " --full"
					}
					if behind {
						how += " --restored <the checkpoint the backup holds>"
					}
					return fmt.Errorf("%d findings: look at each, and `loft trail accept %s --why ...` once the archive is right", len(v.GetFindings()), how)
				}),
			},
			{
				Name:  "accept",
				Brief: "make the manifest say what the archive holds, once somebody has looked",
				Flags: flg.Flags{
					&flg.String{Name: "of", Brief: "the digest `trail verify` printed of the findings looked at"},
					&flg.String{Name: "why", Brief: "what was looked at and decided; it is kept, with you as who accepted"},
					&flg.Switch{Name: "full", Brief: "the check looked at was --full"},
					&flg.Int{Name: "restored", Brief: "for a database restored from a backup, the checkpoint the backup holds (0 for one from before the first)"},
				},
				Handler: xli.OnRun(func(ctx context.Context, self *xli.Command, next xli.Next) error {
					of, why := str(self, "of"), str(self, "why")
					if of == "" {
						return errors.New("--of: the digest `trail verify` printed of the findings looked at")
					}
					if why == "" {
						return errors.New("--why says what was looked at and decided")
					}
					full, _ := flg.Find[bool](self, "full")
					req := app.OperatorTrailAcceptRequest_builder{Of: of, Why: why, Full: full}.Build()
					if k, ok := flg.Find[int](self, "restored"); ok {
						req.SetRestored(int32(k))
					}
					conn, err := operatorFor(ctx, self)
					if err != nil {
						return err
					}
					stream, err := app.NewOperatorTrailServiceClient(conn).Accept(ctx, req)
					if err != nil {
						return err
					}
					var v *app.OperatorTrailAccepted
					err = each(stream, self, func(m *app.OperatorTrailAcceptResponse) string {
						if m.HasAccepted() {
							v = m.GetAccepted()
							for _, f := range v.GetFindings() {
								self.Printf("  %s\n", f.GetSaid())
							}
						}
						return m.GetProgress()
					})
					if err != nil {
						return err
					}
					self.Printf("accepted %d findings, by %s; the manifest says what the archive holds\n", len(v.GetFindings()), v.GetBy())
					return next(ctx)
				}),
			},
			{
				Name:  "rows",
				Brief: "check the live rows against the digests the trail keeps of them, changing nothing",
				Handler: xli.OnRun(func(ctx context.Context, self *xli.Command, next xli.Next) error {
					conn, err := operatorFor(ctx, self)
					if err != nil {
						return err
					}
					stream, err := app.NewOperatorTrailServiceClient(conn).Rows(ctx, &app.OperatorTrailRowsRequest{})
					if err != nil {
						return err
					}
					var v *app.OperatorTrailRowsChecked
					if err := each(stream, self, func(m *app.OperatorTrailRowsResponse) string {
						if m.HasChecked() {
							v = m.GetChecked()
						}
						return m.GetProgress()
					}); err != nil {
						return err
					}
					if v == nil {
						return errors.New("the entry point said nothing of what it found")
					}
					if n := printRows(self, v); n > 0 {
						return fmt.Errorf("%d findings: look at each, and `loft trail accept-rows --of %s --why ...` once the rows are right", n, v.GetDigest())
					}
					return next(ctx)
				}),
			},
			{
				Name:  "accept-rows",
				Brief: "put what a check of the rows found on the trail as accepted, once somebody has looked",
				Flags: flg.Flags{
					&flg.String{Name: "of", Brief: "the digest `trail rows` printed of what was looked at"},
					&flg.String{Name: "why", Brief: "what was looked at and decided; it is logged"},
				},
				Handler: xli.OnRun(func(ctx context.Context, self *xli.Command, next xli.Next) error {
					conn, err := operatorFor(ctx, self)
					if err != nil {
						return err
					}
					if err := acceptRows(ctx, self, conn, str(self, "of"), str(self, "why")); err != nil {
						return err
					}
					return next(ctx)
				}),
			},
			{
				Name:  "owed",
				Brief: "what is owed of the trail's erasures and not paid yet: how many, since when, and what a legal hold keeps",
				Handler: xli.OnRun(func(ctx context.Context, self *xli.Command, next xli.Next) error {
					conn, err := operatorFor(ctx, self)
					if err != nil {
						return err
					}
					o, err := app.NewOperatorTrailServiceClient(conn).Owed(ctx, &app.OperatorTrailOwedRequest{})
					if err != nil {
						return err
					}
					if o.GetObjects() == 0 {
						self.Printf("nothing of the trail's erasures is owed\n")
						return next(ctx)
					}
					self.Printf("%d erasure(s) owed, the oldest since %s\n", o.GetObjects(), o.GetDateOldest().AsTime().UTC().Format(time.RFC3339))
					for _, h := range o.GetHeld() {
						name := idSaid(h.GetTenant())
						if h.GetAlias() != "" {
							name = "@" + h.GetAlias() + " (" + name + ")"
						}
						self.Printf("  %d kept by a legal hold of %s, until it lifts\n", h.GetObjects(), name)
					}
					return next(ctx)
				}),
			},
			{
				Name:  "keys",
				Brief: "the public keys the deployment signs with and trusts, as PEM: what an export and a trail's bundle are held to, for whoever checks one outside it (--trust)",
				Handler: xli.OnRun(func(ctx context.Context, self *xli.Command, next xli.Next) error {
					conn, err := operatorFor(ctx, self)
					if err != nil {
						return err
					}
					keys, err := trusted(ctx, conn)
					if err != nil {
						return err
					}
					for _, k := range keys {
						b, err := x509.MarshalPKIXPublicKey(k)
						if err != nil {
							return err
						}
						if err := pem.Encode(self.Writer, &pem.Block{Type: "PUBLIC KEY", Bytes: b}); err != nil {
							return err
						}
					}
					return next(ctx)
				}),
			},
			newCmdTrailExport(),
			newCmdTrailVerifyExport(),
		},
		Handler: xli.RequireSubcommand(),
	}
}

// each reads a stream to its end: what each message says it is at goes to
// stderr, and `of` is handed every message.
func each[T any](stream grpc.ServerStreamingClient[T], self *xli.Command, of func(*T) string) error {
	for {
		m, err := stream.Recv()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		if p := of(m); p != "" {
			fmt.Fprintf(self.ErrWriter, "%s\n", p)
		}
	}
}

// printRows says what a check of the rows found, as the shell's did, and
// answers how many findings it had.
func printRows(self *xli.Command, v *app.OperatorTrailRowsChecked) int {
	by := map[string][]*app.OperatorTrailRowFinding{}
	for _, f := range v.GetFindings() {
		by[f.GetWhat()] = append(by[f.GetWhat()], f)
	}
	n := func(what string) int { return len(by[what]) }
	self.Printf("%d rows read: %d match, %d differ, %d forged, %d with no digest, %d not on the trail, %d under a key not given, %d missing; "+
		"%d from before digests began, %d under a key let go; %d forgotten; %d checked against the archive\n",
		v.GetRead(), v.GetMatches(), n("differs"), n("forged"), n("no-digest"), n("not-on-trail"), n("key-not-held"), n("missing"),
		v.GetBeforeDigests(), v.GetRetired(), v.GetForgotten(), v.GetArchived())
	self.Printf("digest %s\n", v.GetDigest())
	if u := v.GetUnsealed(); u > 0 {
		self.Printf("%d rows from before seals: what is personal in them is vouched for by nothing until "+
			"`loft trail accept-rows --of %s --why ...` seals them\n", u, v.GetDigest())
	}
	for _, what := range []string{"differs", "forged", "no-digest", "not-on-trail", "key-not-held", "missing"} {
		fs := by[what]
		if len(fs) == 0 {
			continue
		}
		count := map[string]int{}
		for _, f := range fs {
			count[f.GetKind()]++
		}
		kinds := slices.Collect(maps.Keys(count))
		slices.SortFunc(kinds, func(a, b string) int {
			if c := count[b] - count[a]; c != 0 {
				return c
			}
			return strings.Compare(a, b)
		})
		parts := make([]string, len(kinds))
		for i, k := range kinds {
			parts[i] = fmt.Sprintf("%s %d", k, count[k])
		}
		self.Printf("  %s by kind: %s\n", what, strings.Join(parts, ", "))
		for _, f := range fs {
			self.Printf("  %s %s %s\n", what, f.GetKind(), idSaid(f.GetId()))
		}
	}
	for _, k := range v.GetUnheld() {
		self.Printf("  unheld-key %s\n", k)
	}
	for _, k := range v.GetSkipped() {
		self.Printf("  not checked: %s, kept off the trail: %s\n", k.GetKind(), k.GetWhy())
	}
	return len(v.GetFindings())
}

// acceptRows is `loft trail accept-rows`.
func acceptRows(ctx context.Context, self *xli.Command, conn grpc.ClientConnInterface, of, why string) error {
	if of == "" {
		return errors.New("--of: the digest `trail rows` printed of what was looked at")
	}
	if why == "" {
		return errors.New("--why says what was looked at and decided")
	}
	stream, err := app.NewOperatorTrailServiceClient(conn).AcceptRows(ctx, app.OperatorTrailAcceptRowsRequest_builder{Of: of, Why: why}.Build())
	if err != nil {
		return err
	}
	var v *app.OperatorTrailRowsAccepted
	if err := each(stream, self, func(m *app.OperatorTrailAcceptRowsResponse) string {
		if m.HasAccepted() {
			v = m.GetAccepted()
		}
		return m.GetProgress()
	}); err != nil {
		return err
	}
	self.Printf("accepted %d rows and %d gone, and sealed %d from before seals; the trail says they are what they are now\n", v.GetThere(), v.GetGone(), v.GetSealed())
	return nil
}

// newCmdTrailExport is `loft trail export`: a workspace's trail taken out,
// encrypted to whoever keeps it, for a demand to preserve it (loft#153). A
// download: what it is for is required (`--reason`, before the command, the
// request it answers: a case, a demand to preserve -- kept in the bundle),
// and the access record names everybody whose data it carries before its
// first byte.
func newCmdTrailExport() *xli.Command {
	return &xli.Command{
		Name:  "export",
		Brief: "take a workspace's trail out, encrypted to whoever keeps it, for a demand to preserve it (--reason first)",
		Flags: flg.Flags{
			&flg.String{Name: "to", Brief: "whoever keeps it: an age recipient (age1…, age1yubikey1…), or a file of them, read here"},
			&flg.String{Name: "out", Brief: "the file to write, which must not be there, or - for stdout"},
		},
		Args: arg.Args{&arg.String{Name: "WORKSPACE", Brief: "its identifier, or its address -- a destroyed one's, as the hold on it names it"}},
		Handler: xli.OnRun(func(ctx context.Context, self *xli.Command, next xli.Next) error {
			why := reasonOf(ctx)
			if why == "" {
				return errors.New("--reason, before the command: the request it answers -- it hands people's data out, and is recorded")
			}
			out := str(self, "out")
			if out == "" {
				return errors.New("--out: the file to write, or - for stdout")
			}
			to := str(self, "to")
			if to == "" {
				return errors.New("--to: whoever keeps it, an age recipient or a file of them")
			}
			doc, err := fileOr(to)
			if err != nil {
				return fmt.Errorf("--to: %w", err)
			}
			ws, _ := arg.Get[string](self, "WORKSPACE")
			conn, err := operatorFor(ctx, self)
			if err != nil {
				return err
			}
			stream, err := app.NewOperatorTrailServiceClient(conn).Export(ctx, app.OperatorTrailExportRequest_builder{
				Tenant: ws,
				To:     strings.Split(doc, "\n"),
			}.Build())
			if err != nil {
				return err
			}
			if _, err := stream.Recv(); err != nil {
				return err
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
			var x *app.OperatorTrailExported
			for {
				m, err := stream.Recv()
				if errors.Is(err, io.EOF) {
					break
				}
				if err == nil && len(m.GetData()) > 0 {
					_, err = w.Write(m.GetData())
				}
				if err != nil {
					done(false)
					return err
				}
				if m.HasExported() {
					x = m.GetExported()
				}
			}
			if x == nil {
				done(false)
				return errors.New("the entry point ended without saying what it held")
			}
			if err := done(true); err != nil {
				return err
			}

			whose := idSaid(x.GetTenant())
			if x.GetAlias() != "" {
				whose = "@" + x.GetAlias() + " (" + whose + ")"
			}
			where := out
			if out == stdio {
				where = "stdout"
			}
			fmt.Fprintf(say, "%s: the trail of %s, %d rows of the database and %d of the archive in %d chunks, %d files\n",
				where, whose, x.GetDatabase(), x.GetArchive(), x.GetChunks(), x.GetFiles())
			fmt.Fprintf(say, "  manifest %s, signed by the deployment\n", x.GetDigest())
			if x.GetCheckpoint() > 0 {
				witnesses := strings.Join(x.GetWitnesses(), ", ")
				if witnesses == "" {
					witnesses = "no witness"
				}
				fmt.Fprintf(say, "  held to checkpoint %d (%s), heard by %s\n", x.GetCheckpoint(), x.GetCheckpointDigest(), witnesses)
				if x.GetUncovered() {
					fmt.Fprintf(say, "  (a chunk the checkpoint does not list is in it: the manifest marks it, and `trail verify` says why)\n")
				}
			}
			if x.HasDateRetired() {
				fmt.Fprintf(say, "  retired %s; what it kept is kept until %s\n", dayOf(x.GetDateRetired()), dayOf(x.GetDateKeptUntil()))
			}
			fmt.Fprintf(say, "  %d people in the access record, as a download, for %q\n", x.GetPeople(), why)
			return next(ctx)
		}),
	}
}

// newCmdTrailVerifyExport is `loft trail verify-export`: whether a bundle is
// what the deployment wrote -- every file what its manifest says, and the
// manifest signed by a key the deployment trusts, as `--trust` or the entry
// point says them (`trail.OpenTenantExport`). The bundle and the identity it
// opens with stay here.
func newCmdTrailVerifyExport() *xli.Command {
	return &xli.Command{
		Name:  "verify-export",
		Brief: "whether a bundle `trail export` wrote is what the deployment wrote",
		Flags: flg.Flags{
			&flg.String{Name: "file", Brief: "the bundle, or - for stdin"},
			&flg.String{Name: "identity", Brief: "the identity it was encrypted to: a file of age-keygen's, or a plugin's (AGE-PLUGIN-…)"},
			trustFlag(),
		},
		Handler: xli.OnRun(func(ctx context.Context, self *xli.Command, next xli.Next) error {
			path := str(self, "file")
			if path == "" {
				return errors.New("--file: the bundle to verify")
			}
			ids, err := identitiesOf(str(self, "identity"))
			if err != nil {
				return fmt.Errorf("--identity: %w", err)
			}
			keys, err := heldTo(ctx, self)
			if err != nil {
				return err
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
			v, err := trail.OpenTenantExport(r, ids, keys, nil)
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
				self.Printf("  retired %s, kept until %s\n", r.At.UTC().Format(time.DateOnly), r.Until.UTC().Format(time.DateOnly))
			}
			return next(ctx)
		}),
	}
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
