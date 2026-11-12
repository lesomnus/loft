package cli

import (
	"bufio"
	"context"
	"crypto"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/lesomnus/payday/pdid"
	"github.com/lesomnus/xli"
	"github.com/lesomnus/xli/arg"
	"github.com/lesomnus/xli/flg"
	"github.com/lesomnus/z"
	"google.golang.org/grpc"
	"google.golang.org/protobuf/types/known/timestamppb"

	app "github.com/lesomnus/loft"
	"github.com/lesomnus/loft/server/offboard"
)

// A workspace, as the operator asks of it (design 8.2, 9.11): finding one,
// closing and restoring it, its export and its return from one, an owner
// named, its contract and its legal holds, and its end by the operator's
// hand. Of these a person's connection asks only for the list:
// what becomes of a workspace is the operator's (D83).

// tenantWritten is the workspace's commands written here, at their schema
// paths: `tenant`'s, `tenant-contract add` and `legal-hold add|patch`.
func tenantWritten() []written {
	return []written{
		{path: "tenant/list", replace: true, cmd: newCmdTenantList(), calls: []string{app.TenantService_List_FullMethodName}},
		{path: "tenant/show", cmd: newCmdTenantShow(), calls: []string{app.OperatorTenantService_Show_FullMethodName}},
		{path: "tenant/close", replace: true, cmd: newCmdTenantClose(), calls: []string{app.TenantService_Close_FullMethodName}},
		{path: "tenant/restore", replace: true, cmd: newCmdTenantRestore(), calls: []string{app.TenantService_Restore_FullMethodName}},
		{path: "tenant/export", cmd: newCmdTenantExport(), calls: []string{app.TenantService_Export_FullMethodName}},
		// A file held to the deployment's keys asks nobody with --trust.
		{path: "tenant/verify", cmd: newCmdTenantVerify()},
		{path: "tenant/import", cmd: newCmdTenantImport(), calls: []string{app.OperatorTenantService_Import_FullMethodName}},
		{path: "tenant/retired", cmd: newCmdTenantRetired(), calls: []string{app.OperatorTenantService_Retired_FullMethodName}},
		{path: "tenant/retire", cmd: destroying("retire", "take a workspace out of the deployment, keeping what the deployment's floors are on of its trail until they pass",
			app.OperatorTenantServiceClient.Retire), calls: []string{app.OperatorTenantService_Retire_FullMethodName}},
		{path: "tenant/purge", cmd: destroying("purge", "take everything of a workspace out of the deployment, its whole trail with it, the workspace last",
			app.OperatorTenantServiceClient.Purge), calls: []string{app.OperatorTenantService_Purge_FullMethodName}},

		{path: "tenant-contract/add", replace: true, cmd: newCmdContractAdd(), calls: []string{app.TenantContractService_Add_FullMethodName}},
		{path: "legal-hold/add", replace: true, cmd: newCmdHoldAdd(), calls: []string{app.LegalHoldService_Add_FullMethodName}},
		{path: "legal-hold/patch", replace: true, cmd: newCmdHoldPatch(), calls: []string{app.LegalHoldService_Patch_FullMethodName}},
	}
}

// stdio is what `--out` and `--file` take, in place of a file, for the
// command's own stdout or stdin: a file leaves the operator's machine through
// a pipe as well as from a disk (loft#126).
const stdio = "-"

// named is what a message calls the file `path` names.
func named(path string) string {
	if path == stdio {
		return "stdin"
	}
	return path
}

// errNoKeys is a deployment that signs with no key and trusts none: what it
// writes is vouched for by nothing.
var errNoKeys = errors.New("the deployment signs with no key and trusts none (audit.checkpoints.key, audit.checkpoints.trust): nothing to hold a file to")

// trusted is the keys the deployment signs with and trusts, as the entry
// point says them: what a file it wrote is held to here, beside the file --
// [errNoKeys] when it has none.
func trusted(ctx context.Context, conn grpc.ClientConnInterface) ([]crypto.PublicKey, error) {
	v, err := app.NewOperatorTrailServiceClient(conn).Keys(ctx, &app.OperatorTrailKeysRequest{})
	if err != nil {
		return nil, err
	}
	out := []crypto.PublicKey{}
	for _, b := range append([][]byte{v.GetSigner()}, v.GetTrusted()...) {
		if len(b) == 0 {
			continue
		}
		k, err := x509.ParsePKIXPublicKey(b)
		if err != nil {
			return nil, fmt.Errorf("a key the entry point says the deployment trusts: %w", err)
		}
		out = append(out, k)
	}
	if len(out) == 0 {
		return nil, errNoKeys
	}
	return out, nil
}

// trustFlag is a file of the deployment's public keys, for a file to be held
// to outside it -- by whoever keeps an export or a trail's bundle and holds
// no operator's key -- in place of asking the entry point. A flag of its own
// for each command, as a flag keeps what it was given.
func trustFlag() *flg.String {
	return &flg.String{Name: "trust", Brief: "the deployment's public keys, as `trail keys` printed them, to check against without asking the entry point"}
}

// heldTo is the keys a file is checked against: the ones `--trust` names, or
// the ones the entry point says, asked as the operator.
func heldTo(ctx context.Context, self *xli.Command) ([]crypto.PublicKey, error) {
	if path := str(self, "trust"); path != "" {
		return keysIn(path)
	}
	conn, err := operatorFor(ctx, self)
	if err != nil {
		return nil, fmt.Errorf("%w (or --trust, the keys `trail keys` printed)", err)
	}
	return trusted(ctx, conn)
}

// keysIn is every public key a PEM file holds -- what `trail keys` printed.
func keysIn(path string) ([]crypto.PublicKey, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("--trust: %w", err)
	}
	out := []crypto.PublicKey{}
	for {
		var block *pem.Block
		block, b = pem.Decode(b)
		if block == nil {
			break
		}
		if block.Type != "PUBLIC KEY" {
			continue
		}
		k, err := x509.ParsePKIXPublicKey(block.Bytes)
		if err != nil {
			return nil, fmt.Errorf("--trust: %w", err)
		}
		out = append(out, k)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("--trust: %s holds no public key", path)
	}
	return out, nil
}

// spooled is the file `path` names, opened, with its size and what to call
// when done with it. For [stdio] it is what the command reads, written to a
// temporary file first -- one only this user can read, as an export is the
// whole of a workspace -- because an archive is read out of order and a pipe
// only once.
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
	f, err := os.CreateTemp("", "loft-export-*.zip")
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

// printManifest is what an export holds, as its manifest says it.
func printManifest(w io.Writer, where string, m offboard.Manifest) {
	rows := 0
	for _, n := range m.Rows {
		rows += n
	}
	fmt.Fprintf(w, "%s: %d rows of %d kinds, %d files, the trail's %d rows and %d archived\n",
		where, rows, len(m.Rows), m.Files, m.Trail.Database, m.Trail.Archive)
	if len(m.Missing) > 0 {
		fmt.Fprintf(w, "  missing from the file store: %s\n", strings.Join(m.Missing, ", "))
	}
}

// state is whether a workspace is open, as its line says.
func state(t *app.Tenant) string {
	if !t.HasDateClosed() {
		return "open"
	}
	return fmt.Sprintf("closed %s by %s, destroyed %s", dayOf(t.GetDateClosed()), t.GetClosedBy(), dayOf(t.GetDateDestroy()))
}

func printTenant(self *xli.Command, t *app.Tenant) {
	self.Printf("@%s\t%s\t%s\n", t.GetAlias(), t.GetName(), state(t))
}

// printShow is a workspace as `tenant show` says it: its line and how many
// are in it, what its own settings and the deployment's come to now -- and a
// window made shorter that is still waiting out the grace -- its members, its
// contracts and its holds. Who holds which role is roster's (D89), and said
// there.
func printShow(self *xli.Command, v *app.OperatorTenantShowResponse) {
	t := v.GetTenant()
	self.Printf("@%s\t%s\t%d people\t%s\n", t.GetAlias(), t.GetName(), len(v.GetMembers()), state(t))
	self.Printf("  settings\thistory %s, access record %s, restore %s\n",
		days(t.GetKeepDays(), "forever"), days(t.GetAccessKeepDays(), "the deployment's"), days(t.GetRestoreDays(), "the deployment's"))
	w := v.GetWindow()
	self.Printf("  keeps\thistory %s, access record %s, restore %s once closed, then %s\n",
		days(w.GetKeepDays(), "forever"), days(w.GetAccessDays(), "forever"), days(w.GetRestoreDays(), "forever"), leaving(w.GetPurge()))
	if w.HasDateKeepUntil() {
		self.Printf("  history\tuntil %s, then %s: made shorter, and waiting out the grace\n", dayOf(w.GetDateKeepUntil()), days(w.GetKeepThenDays(), "forever"))
	}
	if w.HasDateAccessUntil() {
		self.Printf("  access record\tuntil %s, then %s: made shorter, and waiting out the grace\n", dayOf(w.GetDateAccessUntil()), days(w.GetAccessThenDays(), "forever"))
	}
	for _, m := range v.GetMembers() {
		leaving := ""
		if m.HasDateLeaving() {
			leaving = "\tleaving since " + dayOf(m.GetDateLeaving())
		}
		self.Printf("  member\t@%s\t%s%s\n", m.GetAlias(), m.GetName(), leaving)
	}
	for _, c := range v.GetContracts() {
		desc := ""
		if c.GetDesc() != "" {
			desc = "\t" + c.GetDesc()
		}
		name := c.GetName()
		if name == "" {
			name = "-"
		}
		self.Printf("  contract\t%s\t%s\tthen %s%s\n", dayOf(c.GetDateEffective()), name, leaving(c.GetPurgeFloors()), desc)
	}
	if len(v.GetContracts()) == 0 {
		self.Printf("  contract\tnone: destroyed, it retires\n")
	}
	for _, h := range v.GetHolds() {
		held := "held since " + dayOf(h.GetDateCreated())
		if h.HasDateLifted() {
			held = "lifted " + dayOf(h.GetDateLifted())
		}
		self.Printf("  hold\t%s\t%s\t%s\n", idSaid(h.GetId()), h.GetName(), held)
	}
	if !w.GetApply() && (w.GetKeepDays() > 0 || w.GetAccessDays() > 0) {
		self.Printf("  (app.retention.apply is off, so nothing is destroyed yet)\n")
	}
}

// leaving is what becomes of a workspace's trail when it is destroyed.
func leaving(purge bool) string {
	if purge {
		return "purge"
	}
	return "retire"
}

// alias is the workspace a command names, without its `@`.
func alias(self *xli.Command) string {
	v, _ := arg.Get[string](self, "WORKSPACE")
	return strings.TrimPrefix(strings.TrimSpace(v), "@")
}

var workspaceArg = &arg.String{Name: "WORKSPACE", Brief: "by its address, @abc1234"}

func show(ctx context.Context, conn grpc.ClientConnInterface, ref *app.OperatorTenantRef) (*app.OperatorTenantShowResponse, error) {
	return app.NewOperatorTenantServiceClient(conn).Show(ctx, app.OperatorTenantShowRequest_builder{Ref: ref}.Build())
}

func byAlias(v string) *app.OperatorTenantRef {
	return app.OperatorTenantRef_builder{Alias: z.Ptr(v)}.Build()
}

func byId(b []byte) *app.OperatorTenantRef {
	return app.OperatorTenantRef_builder{Id: b}.Build()
}

// change is an operator's command that changes a workspace: it says what it
// would do, and does it with `--yes`.
func change(brief string, flags flg.Flags, run func(ctx context.Context, self *xli.Command, conn grpc.ClientConnInterface, ws string) error) *xli.Command {
	return &xli.Command{
		Brief: brief,
		Flags: append(flags, yesFlag()),
		Args:  arg.Args{workspaceArg},
		Handler: xli.OnRun(func(ctx context.Context, self *xli.Command, next xli.Next) error {
			conn, err := operatorFor(ctx, self)
			if err != nil {
				return err
			}
			if err := run(ctx, self, conn, alias(self)); err != nil {
				return err
			}
			return next(ctx)
		}),
	}
}

// newCmdTenantList is `loft tenant list`: the workspaces the connection in
// use sees -- every one, the operator's -- by a piece of their address or
// name.
func newCmdTenantList() *xli.Command {
	return &xli.Command{
		Brief: "the workspaces, by a piece of their address or name",
		Flags: flg.Flags{
			&flg.String{Name: "q", Brief: "a piece of the address or the name"},
			&flg.Switch{Name: "closed", Brief: "only the closed ones"},
		},
		Handler: xli.OnRun(func(ctx context.Context, self *xli.Command, next xli.Next) error {
			conn, _, err := remote{}.Connect(ctx)
			if err != nil {
				return err
			}
			q := strings.ToLower(str(self, "q"))
			closed, _ := flg.Find[bool](self, "closed")
			// Every workspace, a page at a time, and the ones asked for
			// said: the generated list filters by reference alone.
			found := []*app.Tenant{}
			after := ""
			for {
				page, err := app.NewTenantServiceClient(conn).List(ctx, app.TenantListRequest_builder{Size: 500, After: after}.Build())
				if err != nil {
					return err
				}
				for _, t := range page.GetItems() {
					if q != "" && !strings.Contains(strings.ToLower(t.GetAlias()), q) && !strings.Contains(strings.ToLower(t.GetName()), q) {
						continue
					}
					if closed && !t.HasDateClosed() {
						continue
					}
					found = append(found, t)
				}
				if after = page.GetNext(); after == "" {
					break
				}
			}
			slices.SortFunc(found, func(a, b *app.Tenant) int { return strings.Compare(a.GetAlias(), b.GetAlias()) })
			for _, t := range found {
				printTenant(self, t)
			}
			return next(ctx)
		}),
	}
}

// newCmdTenantShow is `loft tenant show`: one workspace as the operator reads
// it.
func newCmdTenantShow() *xli.Command {
	return &xli.Command{
		Brief: "one workspace: who is in it, what it keeps and until when, its contracts and its holds",
		Args:  arg.Args{workspaceArg},
		Handler: xli.OnRun(func(ctx context.Context, self *xli.Command, next xli.Next) error {
			conn, err := operatorFor(ctx, self)
			if err != nil {
				return err
			}
			v, err := show(ctx, conn, byAlias(alias(self)))
			if err != nil {
				return err
			}
			printShow(self, v)
			return next(ctx)
		}),
	}
}

// newCmdTenantClose is `loft tenant close`: a workspace closed by the
// operator, who names it by its address -- unpaid, a breach of the terms, its
// owner's request -- and destroyed once its restore window is over unless it
// is restored. Nobody in it closes it (D83).
func newCmdTenantClose() *xli.Command {
	return change("close a workspace; it may be restored within its window, and is destroyed after it",
		flg.Flags{&flg.String{Name: "reason", Brief: "why, in words; the workspace's history keeps it"}},
		func(ctx context.Context, self *xli.Command, conn grpc.ClientConnInterface, ws string) error {
			v, err := show(ctx, conn, byAlias(ws))
			if err != nil {
				return err
			}
			t := v.GetTenant()
			if t.HasDateClosed() {
				self.Printf("@%s is closed already, since %s; destroyed after %s\n", t.GetAlias(), dayOf(t.GetDateClosed()), dayOf(t.GetDateDestroy()))
				return nil
			}
			if !yes(self) {
				until := time.Now().Add(time.Duration(v.GetWindow().GetRestoreDays()) * 24 * time.Hour)
				self.Printf("would close @%s; it is destroyed after %s unless restored\n", t.GetAlias(), dayOf(timestamppb.New(until)))
				nothing(self)
				return nil
			}
			closed, err := app.NewTenantServiceClient(conn).Close(ctx, app.TenantCloseRequest_builder{
				Ref:     app.TenantRef_builder{Alias: z.Ptr(ws)}.Build(),
				Confirm: ws,
				Reason:  str(self, "reason"),
			}.Build())
			if err != nil {
				return err
			}
			printTenant(self, closed)
			return nil
		})
}

// newCmdTenantRestore is `loft tenant restore`: a closed workspace open
// again, while its restore window lasts -- by the operator, at its owner's
// request or once what it was closed for is settled (D83).
func newCmdTenantRestore() *xli.Command {
	return change("open a closed workspace again, while its restore window lasts", nil,
		func(ctx context.Context, self *xli.Command, conn grpc.ClientConnInterface, ws string) error {
			if !yes(self) {
				v, err := show(ctx, conn, byAlias(ws))
				if err != nil {
					return err
				}
				self.Printf("would restore @%s (%s)\n", ws, state(v.GetTenant()))
				nothing(self)
				return nil
			}
			t, err := app.NewTenantServiceClient(conn).Restore(ctx, app.TenantRestoreRequest_builder{
				Ref: app.TenantRef_builder{Alias: z.Ptr(ws)}.Build(),
			}.Build())
			if err != nil {
				return err
			}
			printTenant(self, t)
			return nil
		})
}

// newCmdTenantExport is `loft tenant export`: everything a workspace has, as
// one zip (`TenantService.Export`), open or closed -- the operator's, at its
// owner's request or for one that is leaving (design 8.2, D83). A download,
// so it says what it is for (`--reason`, before the command). It is read back
// and held to the keys the deployment signs with.
func newCmdTenantExport() *xli.Command {
	return &xli.Command{
		Brief: "everything a workspace has, as one zip archive: everybody in it, handed out (--reason first)",
		Flags: flg.Flags{
			&flg.String{Name: "out", Brief: "where to write it, which must not be there; what the deployment calls it by default, and - is stdout"},
		},
		Args: arg.Args{workspaceArg},
		Handler: xli.OnRun(func(ctx context.Context, self *xli.Command, next xli.Next) error {
			conn, err := operatorFor(ctx, self)
			if err != nil {
				return err
			}
			ws := alias(self)
			if reasonOf(ctx) == "" {
				return errors.New("--reason, before the command: what the export is for -- it hands everybody in the workspace out, and is recorded with why")
			}
			stream, err := app.NewTenantServiceClient(conn).Export(ctx, app.TenantExportRequest_builder{
				Ref: app.TenantRef_builder{Alias: z.Ptr(ws)}.Build(),
			}.Build())
			if err != nil {
				return err
			}
			first, err := stream.Recv()
			if err != nil {
				return err
			}
			out := str(self, "out")
			if out == "" {
				out = first.GetName()
			}

			// What this says goes where the archive does not: on stdout
			// beside a file, on stderr when the archive is stdout.
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
			n := 0
			for {
				v, err := stream.Recv()
				if errors.Is(err, io.EOF) {
					break
				}
				if err == nil {
					var k int
					k, err = w.Write(v.GetData())
					n += k
				}
				if err != nil {
					// Half an archive is no export: taken back when it is a
					// file, and said so when it is stdout.
					done(false)
					return err
				}
			}
			if err := done(true); err != nil {
				return err
			}
			if out == stdio {
				fmt.Fprintf(say, "stdout: %d bytes; `loft tenant verify --file -` says what they hold\n", n)
				return next(ctx)
			}

			// What it holds, read back from the file, and held to what the
			// deployment signs with -- the export it wrote.
			f, err := os.Open(out)
			if err != nil {
				return err
			}
			defer f.Close()
			st, err := f.Stat()
			if err != nil {
				return err
			}
			keys, err := trusted(ctx, conn)
			if errors.Is(err, errNoKeys) {
				fmt.Fprintf(say, "%s: %d bytes, unsigned: %v\n", out, n, err)
				return next(ctx)
			}
			if err != nil {
				return err
			}
			m, err := offboard.Verify(f, st.Size(), keys)
			if err != nil {
				return fmt.Errorf("%s: %w", out, err)
			}
			printManifest(say, out, m)
			return next(ctx)
		}),
	}
}

// newCmdTenantVerify is `loft tenant verify`: whether an export is what this
// deployment, or one it trusts, wrote -- held to the keys `--trust` names, by
// whoever keeps it, or to the ones the entry point says.
func newCmdTenantVerify() *xli.Command {
	return &xli.Command{
		Brief: "whether an export is what this deployment, or one it trusts, wrote",
		Flags: flg.Flags{
			&flg.String{Name: "file", Brief: "the export, or - to read it from stdin"},
			trustFlag(),
		},
		Handler: xli.OnRun(func(ctx context.Context, self *xli.Command, next xli.Next) error {
			path := str(self, "file")
			if path == "" {
				return errors.New("--file: the export to verify")
			}
			keys, err := heldTo(ctx, self)
			if err != nil {
				return err
			}
			f, size, done, err := spooled(self, path)
			if err != nil {
				return err
			}
			defer done()
			m, err := offboard.Verify(f, size, keys)
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
			if t.Alias == "" {
				t.Alias = "a tenant"
			}
			self.Printf("%s: %s, exported %s, signed by a trusted key; every one of its %d files is what was exported\n",
				named(path), t.Alias, m.DateExported.Format(time.RFC3339), len(m.Digests))
			self.Printf("  manifest %s, key %s\n", manifest, key)
			return next(ctx)
		}),
	}
}

// newCmdTenantImport is `loft tenant import`: a workspace restored from its
// signed export, into a deployment that does not have it (D34).
func newCmdTenantImport() *xli.Command {
	return &xli.Command{
		Brief: "restore a workspace from its signed export, into a deployment that does not have it",
		Flags: flg.Flags{
			&flg.String{Name: "file", Brief: "the export, or - to read it from stdin"},
			&flg.Switch{Name: "yes", Brief: "do it; without this it only says what it would put back"},
		},
		Handler: xli.OnRun(func(ctx context.Context, self *xli.Command, next xli.Next) error {
			path := str(self, "file")
			if path == "" {
				return errors.New("--file: the export to restore")
			}
			conn, err := operatorFor(ctx, self)
			if err != nil {
				return err
			}
			var r io.Reader = self.ReadCloser
			if path != stdio {
				f, err := os.Open(path)
				if err != nil {
					return err
				}
				defer f.Close()
				r = f
			}
			stream, err := app.NewOperatorTenantServiceClient(conn).Import(ctx)
			if err != nil {
				return err
			}
			if err := stream.Send(app.OperatorTenantImportRequest_builder{
				Options: app.OperatorTenantImportOptions_builder{DryRun: !yes(self)}.Build(),
			}.Build()); err != nil {
				return err
			}
			buf := make([]byte, 512<<10)
			for {
				n, err := io.ReadFull(r, buf)
				if n > 0 {
					if serr := stream.Send(app.OperatorTenantImportRequest_builder{Data: slices.Clone(buf[:n])}.Build()); serr != nil {
						return serr
					}
				}
				if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
					break
				}
				if err != nil {
					return fmt.Errorf("%s: %w", named(path), err)
				}
			}
			x, err := stream.CloseAndRecv()
			if err != nil {
				return fmt.Errorf("%s: %w", named(path), err)
			}
			printImported(self, x)
			return next(ctx)
		}),
	}
}

// newCmdTenantRetired is `loft tenant retired`: the workspaces that retired,
// and until when what they kept is kept.
func newCmdTenantRetired() *xli.Command {
	return &xli.Command{
		Brief: "the workspaces that retired, when, and until when what they kept is kept",
		Handler: xli.OnRun(func(ctx context.Context, self *xli.Command, next xli.Next) error {
			conn, err := operatorFor(ctx, self)
			if err != nil {
				return err
			}
			v, err := app.NewOperatorTenantServiceClient(conn).Retired(ctx, &app.OperatorTenantRetiredRequest{})
			if err != nil {
				return err
			}
			now := time.Now()
			for _, r := range v.GetItems() {
				alias := "-"
				if r.GetAlias() != "" {
					alias = "@" + r.GetAlias()
				}
				left := "kept until " + dayOf(r.GetDateKeptUntil())
				if !now.Before(r.GetDateKeptUntil().AsTime()) {
					left = "past its floors since " + dayOf(r.GetDateKeptUntil()) + ": the next pass takes what is left"
				}
				self.Printf("%s\t%s\tretired %s\t%s\n", alias, idSaid(r.GetTenant()), dayOf(r.GetDateRetired()), left)
			}
			if len(v.GetItems()) == 0 {
				self.Printf("no workspace has retired, or what each kept is gone\n")
			}
			return next(ctx)
		}),
	}
}

// destroying is `loft tenant retire` and `loft tenant purge`: a workspace
// destroyed by the operator's hand, as the closing sweep destroys one whose
// restore window is over -- and what it would take, without `--yes`. What it
// is at while it works goes to stderr, and what it took to stdout.
func destroying(how, brief string, call func(app.OperatorTenantServiceClient, context.Context, *app.OperatorTenantDestroyRequest, ...grpc.CallOption) (grpc.ServerStreamingClient[app.OperatorTenantDestroyResponse], error)) *xli.Command {
	return change(brief, nil, func(ctx context.Context, self *xli.Command, conn grpc.ClientConnInterface, ws string) error {
		stream, err := call(app.NewOperatorTenantServiceClient(conn), ctx, app.OperatorTenantDestroyRequest_builder{
			Ref:    byAlias(ws),
			DryRun: !yes(self),
		}.Build())
		if err != nil {
			return err
		}
		var x *app.OperatorTenantDestroyed
		for {
			v, err := stream.Recv()
			if errors.Is(err, io.EOF) {
				break
			}
			if err != nil {
				return fmt.Errorf("@%s: %w", ws, err)
			}
			if v.GetProgress() != "" {
				fmt.Fprintf(self.ErrWriter, "%s\n", v.GetProgress())
			}
			if v.HasDestroyed() {
				x = v.GetDestroyed()
			}
		}
		if x == nil {
			return fmt.Errorf("@%s: the entry point said nothing of what it took", ws)
		}
		printDestroyed(self, how, x)
		return nil
	})
}

func printDestroyed(self *xli.Command, how string, x *app.OperatorTenantDestroyed) {
	dry := x.GetDryRun()
	verb := "took"
	if dry {
		verb = "would take"
	}
	total := uint32(0)
	parts := []string{}
	for _, k := range slices.Sorted(maps.Keys(x.GetRows())) {
		total += x.GetRows()[k]
		parts = append(parts, fmt.Sprintf("%s %d", k, x.GetRows()[k]))
	}
	self.Printf("%s: %s %d rows (%s), %d files\n", x.GetAlias(), verb, total, strings.Join(parts, ", "), x.GetFiles())
	self.Printf("  trail: %d rows removed, %d blanked, %d archived chunks\n", x.GetTrailRemoved(), x.GetTrailBlanked(), x.GetTrailChunks())
	if x.GetRetired() {
		if len(x.GetKept()) == 0 {
			self.Printf("  kept: nothing, since no floor of the deployment's is on any of it\n")
		} else {
			kept := []string{}
			for _, k := range slices.Sorted(maps.Keys(x.GetKept())) {
				kept = append(kept, fmt.Sprintf("%s %d", k, x.GetKept()[k]))
			}
			self.Printf("  kept until %s, for the deployment's floors: %s\n", dayOf(x.GetDateKeptUntil()), strings.Join(kept, ", "))
		}
	}
	if len(x.GetLost()) > 0 {
		self.Printf("  files that could not be removed: %s\n", strings.Join(x.GetLost(), ", "))
	}
	switch {
	case !x.GetBills():
	case dry:
		self.Printf("  meters: its final reading is measured and handed to levyan first; if levyan cannot take it, it goes all the same\n")
	case x.GetUnmeasured() != "":
		self.Printf("  meters: its final reading could not be handed to levyan, and it went all the same; levyan bills it by its last daily reading: %s\n", x.GetUnmeasured())
	default:
		self.Printf("  meters: its final reading was measured and handed to levyan first\n")
	}
	switch {
	case !x.GetTells():
	case dry:
		self.Printf("  levyan: it is told the workspace was destroyed, at a moment after its final reading, before anything of it is taken\n")
	default:
		self.Printf("  levyan: it is to be told the workspace was destroyed, by the notices a running `loft serve` tells\n")
	}
	if !dry {
		return
	}
	switch {
	case how == "purge" && !x.GetContractPurges():
		self.Printf("(its contract retires it, keeping what the deployment's floors are on: `loft tenant retire` does what it says)\n")
	case how == "retire" && x.GetContractPurges():
		self.Printf("(its contract purges it, what the deployment's floors are on too: `loft tenant purge` does what it says)\n")
	}
	self.Printf("(nothing was taken: `--yes` takes it. Export first -- `loft --reason ... tenant export %s`)\n", x.GetAlias())
}

func printImported(self *xli.Command, x *app.OperatorTenantImportResponse) {
	dry := x.GetDryRun()
	verb := "put back"
	if dry {
		verb = "would put back"
	}
	total := uint32(0)
	parts := []string{}
	for _, k := range slices.Sorted(maps.Keys(x.GetRows())) {
		total += x.GetRows()[k]
		parts = append(parts, fmt.Sprintf("%s %d", k, x.GetRows()[k]))
	}
	self.Printf("@%s: %s %d rows (%s), %d files\n", x.GetAlias(), verb, total, strings.Join(parts, ", "), x.GetFiles())
	self.Printf("  from the export whose manifest is %s, signed by %s: keep it, for the trail it holds is not put back\n", x.GetManifest(), x.GetKey())
	if x.GetAsked() != "" {
		self.Printf("  at @%s, the tenant's address at roster: the export says @%s\n", x.GetAlias(), x.GetAsked())
	}
	if x.GetReopened() {
		self.Printf("  it was closed when it was exported, and is open\n")
	}
	if x.GetRetired() {
		if dry {
			self.Printf("  it retired from here: the trail takes it back first, before anything of it is written\n")
		} else {
			self.Printf("  it retired from here, and the trail took it back first: %d records of the retirement lifted\n", x.GetReturned())
		}
	}
	if len(x.GetMissing()) > 0 {
		self.Printf("  files the export did not have, as the store it came from did not: %s\n", strings.Join(x.GetMissing(), ", "))
	}
	if len(x.GetUnlinked()) > 0 {
		self.Printf("  %d member(s) this deployment's roster does not have: %s\n", len(x.GetUnlinked()), strings.Join(x.GetUnlinked(), ", "))
		self.Printf("  (nobody of them signs in until `loft identity link` makes them there)\n")
	}
	if dry {
		self.Printf("(nothing was put back: `--yes` does it)\n")
		return
	}
	if g := x.GetGrants(); g != "" {
		self.Printf("  what levyan grants it is %s\n", g)
	}
}

// newCmdContractAdd is `loft tenant-contract add`: a workspace's next
// contract with the deployment, in force from when it says -- the operator's,
// and nobody's in the workspace.
func newCmdContractAdd() *xli.Command {
	return change("write a workspace's next contract, in force from --effective",
		flg.Flags{
			&flg.String{Name: "name", Brief: "the agreement, as people call it: its number, standard"},
			&flg.String{Name: "desc", Brief: "more about it: an agreement's terms, a ticket"},
			&flg.Switch{Name: "purge-floors", Brief: "when it is destroyed, take its whole trail, what the deployment's floors are on too; without this it retires, keeping that until the floors pass"},
			&flg.String{Name: "effective", Brief: "from when, as 2006-01-02 or RFC 3339; now by default"},
		},
		func(ctx context.Context, self *xli.Command, conn grpc.ClientConnInterface, ws string) error {
			effective := time.Now()
			if v := str(self, "effective"); v != "" {
				t, err := instant(v)
				if err != nil {
					return fmt.Errorf("--effective: %w", err)
				}
				effective = t
			}
			purge, _ := flg.Find[bool](self, "purge-floors")
			if !yes(self) {
				if _, err := show(ctx, conn, byAlias(ws)); err != nil {
					return err
				}
				self.Printf("would write a contract %q for @%s, in force from %s; destroyed, it would %s\n",
					str(self, "name"), ws, dayOf(timestamppb.New(effective)), leaving(purge))
				nothing(self)
				return nil
			}
			if _, err := app.NewTenantContractServiceClient(conn).Add(ctx, app.TenantContractAddRequest_builder{
				Tenant:        app.TenantRef_builder{Alias: z.Ptr(ws)}.Build(),
				Name:          str(self, "name"),
				Desc:          str(self, "desc"),
				PurgeFloors:   &purge,
				DateEffective: timestamppb.New(effective),
			}.Build()); err != nil {
				return err
			}
			v, err := show(ctx, conn, byAlias(ws))
			if err != nil {
				return err
			}
			printShow(self, v)
			return nil
		})
}

// newCmdHoldAdd is `loft legal-hold add`: everything of a workspace's history
// kept until the hold is lifted.
func newCmdHoldAdd() *xli.Command {
	return change("keep everything of a workspace's history until the hold is lifted",
		flg.Flags{
			&flg.String{Name: "why", Brief: "what it is for: a case, a demand, an agreement"},
			&flg.String{Name: "desc", Brief: "more about it"},
		},
		func(ctx context.Context, self *xli.Command, conn grpc.ClientConnInterface, ws string) error {
			why := str(self, "why")
			if why == "" {
				return errors.New("--why: a hold says what it is for")
			}
			if !yes(self) {
				if _, err := show(ctx, conn, byAlias(ws)); err != nil {
					return err
				}
				self.Printf("would keep everything of @%s's history until the hold is lifted: %s\n", ws, why)
				nothing(self)
				return nil
			}
			if _, err := app.NewLegalHoldServiceClient(conn).Add(ctx, app.LegalHoldAddRequest_builder{
				Tenant: app.TenantRef_builder{Alias: z.Ptr(ws)}.Build(),
				Name:   why,
				Desc:   str(self, "desc"),
			}.Build()); err != nil {
				return err
			}
			v, err := show(ctx, conn, byAlias(ws))
			if err != nil {
				return err
			}
			printShow(self, v)
			return nil
		})
}

// newCmdHoldPatch is `loft legal-hold patch --lift`: a hold ended, the one
// change made to one; the windows apply again from the next pass.
func newCmdHoldPatch() *xli.Command {
	return &xli.Command{
		Brief: "end a hold (--lift); the windows apply again from the next pass",
		Flags: flg.Flags{
			&flg.Switch{Name: "lift", Brief: "end it: the one change a hold is made"},
			yesFlag(),
		},
		Args: arg.Args{&arg.String{Name: "ID", Brief: "the hold, as `tenant show` lists it"}},
		Handler: xli.OnRun(func(ctx context.Context, self *xli.Command, next xli.Next) error {
			if lift, _ := flg.Find[bool](self, "lift"); !lift {
				return errors.New("--lift: a hold is patched by being lifted, and in no other way")
			}
			raw, _ := arg.Get[string](self, "ID")
			id, err := pdid.Parse(strings.TrimSpace(raw))
			if err != nil {
				return fmt.Errorf("ID: %w", err)
			}
			conn, err := operatorFor(ctx, self)
			if err != nil {
				return err
			}
			ref := app.LegalHoldRef_builder{Id: id.Bytes()}.Build()
			h, err := app.NewLegalHoldServiceClient(conn).Get(ctx, app.LegalHoldGetRequest_builder{Ref: ref}.Build())
			if err != nil {
				return err
			}
			if !yes(self) {
				self.Printf("would lift hold %s (%s); the windows apply again from the next pass\n", id, h.GetName())
				nothing(self)
				return next(ctx)
			}
			if _, err := app.NewLegalHoldServiceClient(conn).Patch(ctx, app.LegalHoldPatchRequest_builder{
				Ref:        ref,
				DateLifted: timestamppb.Now(),
			}.Build()); err != nil {
				return err
			}
			v, err := show(ctx, conn, byId(h.GetTenant().GetId()))
			if err != nil {
				return err
			}
			printShow(self, v)
			return next(ctx)
		}),
	}
}
