// Package cli is this app's command line, and what only a process does.
//
// `cmd` is the wiring both entry points share -- `Build` is there, and an app's
// sandbox calls it to stand up the same server the process stands up. This
// package is everything on the other side of that: parsing arguments, the
// database engine a process opens, and what to do about the shape of a database
// that was already there.
//
// # Why a package and not a build tag
//
// Because the linker follows imports. A blank-imported driver or a migration
// engine named in `cmd` is linked into the sandbox whether the page can use it
// or not, and neither is small: measured on payday's own app, the wazero SQLite
// engine was 15 MB of the sandbox module and Atlas -- which `Schema.Create`
// reaches -- another 10.5 MB, out of 72 MB the browser had to download.
//
// A `//go:build !js` on each would also have done it, and is worse: a tag
// refuses to compile what would compile, and says nothing about why. What is
// true is that a process and a page need different things, and a package
// boundary is how Go says that. Nothing here is excluded from a build; the
// sandbox simply does not import it.
package cli

import (
	"context"
	_ "embed"
	"fmt"
	"slices"
	"strings"

	"github.com/lesomnus/xli"
	"github.com/lesomnus/xli/cfg"
	"github.com/lesomnus/xli/flg"
	"github.com/lesomnus/xli/frm"
	"github.com/lesomnus/xli/mode"
	"google.golang.org/grpc/metadata"

	"github.com/lesomnus/payday/access"
	"github.com/lesomnus/payday/pdcmd"
	"github.com/lesomnus/payday/rename"
	"github.com/lesomnus/payday/valid"

	"github.com/lesomnus/loft/cmd"
	entmigrate "github.com/lesomnus/loft/internal/ent/migrate"
	"github.com/lesomnus/loft/server/pd"
)

// Cmd is this app's own command line: what payday and xli supply, plus
// whatever the app has of its own.
//
// `config`, `config env` and `version` are the commands that run against a
// **deployment** rather than against a checkout, and every one of them needs
// something only the app can hand over. `config env` is the clearest: listing
// the variables a deployment can set means walking this struct, and the struct
// is the app's. `version` is payday's; the two `config` are xli's, beside what
// reads the configuration.
//
// `serve` is not among them and will not be. It is the one command whose body
// is the stack -- which layers, in which order, with the wall on which server
// -- and that is the most important thing a reader of an app can see.
//
// The configuration is read on the **root**, so it has happened whichever of
// the box's commands runs -- `config` prints what came out, `serve` listens on
// what it says. A command that loaded it for itself would be one more place
// for the order to be wrong. The terminal's half beside them, the schema's
// commands and the utilities, reads none of it (design 9.16). What `c` holds when the loader is made is the defaults:
// every load starts from it, and the file, the environment and the flags go
// over the top.
func Cmd(c *cmd.Config) *xli.Command {
	l := cfg.New(cmd.Name, c, TerminalReads)

	// `version` needs no configuration, and is what somebody runs to ask a
	// deployment whose configuration is wrong what build it is.
	version := pdcmd.NewCmdVersion()
	config := cfg.NewCmdConfig(l)

	// The deployment's own life, which runs where there is no server to ask
	// (design 9.15): its first workspace, and its database brought to this
	// build's shape before anything serves. Each stands the stack up, does
	// one thing and exits, and is observed as the deployment is
	// (`observed`). Everything else an operator does is asked of the entry
	// point (`loft-admin serve`), on an operator's connection.
	first, migrate, serve := NewCmdInit(c), NewCmdMigrate(c), NewCmdServe(c)

	root := &xli.Command{
		Name:  cmd.Name,
		Brief: "loft",

		Flags: flg.Flags{
			cfg.ConfigFlag(),
			&flg.String{Name: "connection", Brief: "the connection to use, by name (`loft auth list`); LOFT_CONNECTION, or the one `loft auth switch` chose"},
			&flg.String{Name: "url", Brief: "with --key, the operator's entry point for this command alone; LOFT_ADMIN_URL, or http://127.0.0.1:8081"},
			&flg.String{Name: "key", Brief: "the operator's key, as env:NAME or file:PATH -- never the key -- for this command alone; LOFT_ADMIN_KEY"},
			&flg.String{Name: "workspace", Brief: "a person's: the workspace to speak to, by alias; LOFT_WORKSPACE, or the one `loft use` chose"},
			&flg.String{Name: "reason", Brief: "what a download is for -- `tenant export`, `asset export` hand people's data out and are refused without one; it is recorded"},
		},

		// The terminal's half, which runs anywhere, as the person or the
		// operator its connection is (design 9.16): the utilities, and the
		// schema's commands mounted beside them below.
		Commands: []*xli.Command{
			version,
			config,
			first,
			migrate,
			serve,
			NewCmdAuth(),
			NewCmdUse(),
			newCmdCall(),
			NewCmdScan(),
			NewCmdConsent(),
			NewCmdMcp(),
		},
	}
	if err := mount(root); err != nil {
		panic(err)
	}

	// Only the box's commands read a deployment's configuration. The
	// terminal's half reads none of it, and keeps its own
	// (`LOFT_CLIENT_CONFIG`): a terminal runs where no deployment is.
	box := []*xli.Command{config, first, migrate, serve}
	terminal := slices.DeleteFunc(slices.Clone(root.Commands), func(v *xli.Command) bool { return slices.Contains(box, v) })

	root.Handler = xli.Chain(
		cfg.Load(l, terminal...),
		// Who the terminal's commands are, and to which workspace and why,
		// as the command line says it (design 9.16): read where they
		// connect.
		xli.OnRunPass(func(ctx context.Context, self *xli.Command, next xli.Next) error {
			ctx = withChoice(ctx, Choice{Name: flag(self, "connection"), Url: flag(self, "url"), Key: flag(self, "key")})
			if ws := flag(self, "workspace"); ws != "" {
				ctx = context.WithValue(ctx, workspaceKey{}, ws)
			}
			// Said to every call, and read by the downloads: what the access
			// record keeps beside who took what (loft#136).
			if why := strings.TrimSpace(flag(self, "reason")); why != "" {
				ctx = metadata.AppendToOutgoingContext(ctx, access.ReasonKey, why)
			}
			return next(ctx)
		}),
		observed(c, first, migrate),
		xli.RequireSubcommand(),
	)

	return root
}

// observed builds the deployment's telemetry (`cmd.Telemetry`) for the commands
// named and whatever runs below them, and runs them on the context that
// carries it.
//
// They are the operator's. Each stands the stack up with `cmd.Build`, and a
// server stood up on a context with no telemetry logs, traces and measures
// nothing -- which `grpcx` says on stderr, and every one of these printed it.
// With it, what an operator did is in the deployment's telemetry the way what
// the deployment does is, attributed to the same service, and its log goes
// where the deployment's goes: by default to stderr, never stdout, so a command
// whose output is data keeps its stdout to itself.
//
// `serve` is not among them. It builds its own in the open, because its order
// is the stack's (`cli/serve.go`). Neither is `config`, which only prints what
// was read, nor the terminal's half, which reads none of it.
func observed(c *cmd.Config, these ...*xli.Command) xli.Handler {
	return xli.On(mode.Run, func(ctx context.Context, self *xli.Command, next xli.Next) error {
		if !runs(ctx, self, these) {
			return next(ctx)
		}

		ctx, done, err := cmd.Telemetry(ctx, c)
		if err != nil {
			return err
		}
		defer done()

		return next(ctx)
	})
}

// runs says one of these is among the commands being run: self, the ones above
// it and the ones below, read as `cfg.Load` reads them.
func runs(ctx context.Context, self *xli.Command, these []*xli.Command) bool {
	path := self.Tree()
	for f := frm.From(ctx); f != nil; f = f.Next() {
		if c, ok := f.Cmd().(*xli.Command); ok && c != self {
			path = append(path, c)
		}
	}

	return slices.ContainsFunc(path, func(c *xli.Command) bool { return slices.Contains(these, c) })
}

// Migrate brings the database s runs on into the shape this app's schema says.
//
// It is here and not on `cmd.Server` because this is the call that links the
// migration engine, and `cmd` is what an app's sandbox imports. A sandbox does
// not migrate: there is no database there that outlives the page.
//
// What payday renames is moved onto its name first, in payday's order
// (`rename`): the tables of an entity the schema renamed and says it did
// (`formerly`), the columns of payday's own entities, and the indexes stored
// under the names of their columns, each list as `pd gen` writes it. A
// migration finds each of them by its name, and one it does not find it
// makes again beside the old -- an empty table, an empty column, or an index
// built from every row. The tables first, since the columns and the indexes
// are looked for on a table by the name it has now.
//
// And then an index this build declares that PostgreSQL left invalid is
// refused, before anything else is changed (payday#176). Found by its name,
// it would be taken for made: an operator who built an index ahead of the
// migration, `CONCURRENTLY`, and had the build cancelled, was told the
// database was in this build's shape while the index served no read. The
// refusal names it and says what to run to make it again
// (docs/dev/postgres.md).
//
// Until launch it brings nothing else along (the owner's decision,
// 2026-11-09): a change that breaks what an earlier build stored carries no
// step of its own here, and a deployment from before it is made again from
// nothing (`.github/compat-epoch`).
func Migrate(ctx context.Context, s *cmd.Server) error {
	if err := rename.Tables(ctx, s.Db, s.Dialect, pd.TableRenames); err != nil {
		return err
	}
	if err := rename.Columns(ctx, s.Db, s.Dialect, pd.ColumnRenames); err != nil {
		return err
	}
	if err := rename.Indexes(ctx, s.Db, s.Dialect, pd.IndexRenames); err != nil {
		return err
	}
	if err := valid.Indexes(ctx, s.Db, s.Dialect, entmigrate.Tables); err != nil {
		return err
	}
	if err := entmigrate.NewSchema(s.Drv).Create(ctx); err != nil {
		return err
	}
	if s.Dialect == "postgres" {
		if _, err := s.Db.ExecContext(ctx, pgExtras); err != nil {
			return fmt.Errorf("postgres extras: %w", err)
		}
	}
	return nil
}

// pgExtras is what PostgreSQL promises beyond the schema; see the file.
//
//go:embed pg.sql
var pgExtras string
