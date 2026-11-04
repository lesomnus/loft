package admincli

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

	"github.com/lesomnus/payday/access"
	"github.com/lesomnus/xli"
	"github.com/lesomnus/xli/arg"
	"github.com/lesomnus/xli/flg"
	"github.com/lesomnus/z"
	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"

	app "github.com/lesomnus/loft"
	"github.com/lesomnus/loft/server/offboard"
)

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
// the ones the entry point says.
func heldTo(ctx context.Context, self *xli.Command) ([]crypto.PublicKey, error) {
	if path := str(self, "trust"); path != "" {
		return keysIn(path)
	}
	conn, err := connOf(self)
	if err != nil {
		return nil, err
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

// tenantEnd is a workspace's end, asked of the entry point (design 8.2): its
// export, its retirement or purge, and its return from an export.
func tenantEnd() xli.Commands {
	return xli.Commands{
		{
			Name:  "export",
			Brief: "everything a workspace has, as one zip archive: everybody in it, handed out",
			Flags: flg.Flags{
				&flg.String{Name: "reason", Brief: "what it is for -- a workspace leaving, a request; it is on the access record"},
				&flg.String{Name: "out", Brief: "where to write it, which must not be there; <alias>-<date>.zip by default, and - is stdout"},
			},
			Args: arg.Args{workspaceArg},
			Handler: xli.OnRun(func(ctx context.Context, self *xli.Command, next xli.Next) error {
				why := str(self, "reason")
				if why == "" {
					return errors.New("--reason: what it is for -- it hands everybody in the workspace out, and is recorded")
				}
				conn, err := connOf(self)
				if err != nil {
					return err
				}
				ctx = metadata.AppendToOutgoingContext(ctx, access.ReasonKey, why)
				stream, err := app.NewTenantServiceClient(conn).Export(ctx, app.TenantExportRequest_builder{
					Ref: app.TenantRef_builder{Alias: z.Ptr(alias(self))}.Build(),
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
						// Half an archive is no export: taken back when it is
						// a file, and said so when it is stdout.
						done(false)
						return err
					}
				}
				if err := done(true); err != nil {
					return err
				}
				if out == stdio {
					fmt.Fprintf(say, "stdout: %d bytes; `tenant verify --file -` says what they hold\n", n)
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
		},
		{
			Name:  "verify",
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
					named(path), t.Alias, m.ExportedAt.Format(time.RFC3339), len(m.Digests))
				self.Printf("  manifest %s, key %s\n", manifest, key)
				return next(ctx)
			}),
		},
		{
			Name:  "import",
			Brief: "restore a workspace from its signed export, into a deployment that does not have it",
			Flags: flg.Flags{
				&flg.String{Name: "file", Brief: "the export, or - to read it from stdin"},
				&flg.String{Name: "alias", Brief: "the address to restore it at, instead of the export's"},
				&flg.Switch{Name: "reuse", Brief: "take the address though another workspace's hold is on it"},
				&flg.Switch{Name: "yes", Brief: "do it; without this it only says what it would put back"},
			},
			Handler: xli.OnRun(func(ctx context.Context, self *xli.Command, next xli.Next) error {
				path := str(self, "file")
				if path == "" {
					return errors.New("--file: the export to restore")
				}
				reuse, _ := flg.Find[bool](self, "reuse")
				conn, err := connOf(self)
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
					Options: app.OperatorTenantImportOptions_builder{Alias: str(self, "alias"), Reuse: reuse, DryRun: !yes(self)}.Build(),
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
		},
		{
			Name:  "retired",
			Brief: "the workspaces that retired, when, and until when what they kept is kept",
			Handler: xli.OnRun(func(ctx context.Context, self *xli.Command, next xli.Next) error {
				conn, err := connOf(self)
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
					left := "kept until " + at(r.GetUntil())
					if !now.Before(r.GetUntil().AsTime()) {
						left = "past its floors since " + at(r.GetUntil()) + ": the next pass takes what is left"
					}
					self.Printf("%s\t%s\tretired %s\t%s\n", alias, idOf(r.GetTenant()), at(r.GetRetired()), left)
				}
				if len(v.GetItems()) == 0 {
					self.Printf("no workspace has retired, or what each kept is gone\n")
				}
				return next(ctx)
			}),
		},
		destroying("retire", "take a workspace out of the deployment, keeping what the deployment's floors are on of its trail until they pass",
			app.OperatorTenantServiceClient.Retire),
		destroying("purge", "take everything of a workspace out of the deployment, its whole trail with it, the workspace last",
			app.OperatorTenantServiceClient.Purge),
	}
}

// destroying is `tenant retire` and `tenant purge`: a workspace destroyed by
// the operator's hand, as the closing sweep destroys one whose restore window
// is over -- and what it would take, without `--yes`. What it is at while it
// works goes to stderr, and what it took to stdout.
func destroying(name, brief string, call func(app.OperatorTenantServiceClient, context.Context, *app.OperatorTenantDestroyRequest, ...grpc.CallOption) (grpc.ServerStreamingClient[app.OperatorTenantDestroyResponse], error)) *xli.Command {
	return change(name, brief, nil, func(ctx context.Context, self *xli.Command, conn grpc.ClientConnInterface, ws string) error {
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
		printDestroyed(self, name, x)
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
			self.Printf("  kept until %s, for the deployment's floors: %s\n", at(x.GetKeptUntil()), strings.Join(kept, ", "))
		}
	}
	if x.GetEmbedded() {
		verb := "forgot"
		if dry {
			verb = "would forget"
		}
		self.Printf("  roster in this process: %s %d account(s) in no other workspace, and what its trail said of them\n", verb, x.GetForgotten())
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
		self.Printf("(its contract retires it, keeping what the deployment's floors are on: `tenant retire` does what it says)\n")
	case how == "retire" && x.GetContractPurges():
		self.Printf("(its contract purges it, what the deployment's floors are on too: `tenant purge` does what it says)\n")
	}
	self.Printf("(nothing was taken: `--yes` takes it. Export first -- `loft-admin tenant export --reason ... %s`)\n", x.GetAlias())
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
		self.Printf("  at @%s: the export's @%s is another workspace's here, or not one this deployment gives\n", x.GetAlias(), x.GetAsked())
	}
	for _, r := range x.GetReleased() {
		self.Printf("  the address was held (%s), and is taken back\n", r)
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
	for _, k := range slices.Sorted(maps.Keys(x.GetLeft())) {
		self.Printf("  %d rows of %s left behind: %s\n", x.GetLeft()[k], k, x.GetLeftWhy()[k])
	}
	if len(x.GetMissing()) > 0 {
		self.Printf("  files the export did not have, as the store it came from did not: %s\n", strings.Join(x.GetMissing(), ", "))
	}
	if len(x.GetUnlinked()) > 0 {
		self.Printf("  %d member(s) whose accounts this roster does not have, restored with none: %s\n", len(x.GetUnlinked()), strings.Join(x.GetUnlinked(), ", "))
		self.Printf("  (nobody of them signs in until `identity migrate` gives each an account)\n")
	}
	if dry {
		self.Printf("(nothing was put back: `--yes` does it)\n")
		return
	}
	if x.GetHeld() > 0 {
		self.Printf("  what it has out holds its time: %d things, held from when they were handed over\n", x.GetHeld())
	}
	if g := x.GetGrants(); g != "" {
		self.Printf("  what levyan grants it is %s\n", g)
	}
}
