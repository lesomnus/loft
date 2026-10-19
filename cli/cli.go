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

	"github.com/lesomnus/xli"
	"github.com/lesomnus/xli/cfg"
	"github.com/lesomnus/xli/flg"
	"github.com/lesomnus/xli/frm"
	"github.com/lesomnus/xli/mode"

	"github.com/lesomnus/payday/pdcmd"

	"github.com/lesomnus/rove/cmd"
	entmigrate "github.com/lesomnus/rove/internal/ent/migrate"
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
// The configuration is read on the **root**, so it has happened whichever
// subcommand runs -- `config` prints what came out, `serve` listens on what it
// says. A command that loaded it for itself would be one more place for the
// order to be wrong. What `c` holds when the loader is made is the defaults:
// every load starts from it, and the file, the environment and the flags go
// over the top.
func Cmd(c *cmd.Config) *xli.Command {
	l := cfg.New(cmd.Name, c)

	// `version` needs no configuration, and is what somebody runs to ask a
	// deployment whose configuration is wrong what build it is.
	version := pdcmd.NewCmdVersion()

	// The terminal's half, which runs anywhere as a person and reads none of
	// a deployment's configuration: it keeps its own (`ROVE_CLIENT_CONFIG`).
	login, logout, whoami, use, ws, api, scan, agree, assist := NewCmdLogin(), NewCmdLogout(), NewCmdWhoami(), NewCmdUse(), NewCmdWorkspace(), NewCmdApi(), NewCmdScan(), NewCmdConsent(), NewCmdMcp()

	// The operator's half: each stands the stack up, does one thing and exits,
	// and is observed as the deployment is (`observed`).
	first, migrate, contract, hold, retention, tenant, holder, identity, trail, search := NewCmdInit(c), NewCmdMigrate(c), NewCmdContract(c), NewCmdHold(c), NewCmdRetention(c), NewCmdTenant(c), NewCmdHolder(c), NewCmdIdentity(c), NewCmdTrail(c), NewCmdSearch(c)

	return &xli.Command{
		Name:  cmd.Name,
		Brief: "rove",

		Flags: flg.Flags{cfg.ConfigFlag()},

		Commands: []*xli.Command{
			version,
			cfg.NewCmdConfig(l),
			first,
			migrate,
			NewCmdServe(c),
			contract,
			hold,
			retention,
			tenant,
			holder,
			identity,
			trail,
			search,
			login,
			logout,
			whoami,
			use,
			ws,
			api,
			scan,
			agree,
			assist,
		},

		Handler: xli.Chain(
			cfg.Load(l, version, login, logout, whoami, use, ws, api, scan, agree, assist),
			observed(c, first, migrate, contract, hold, retention, tenant, holder, identity, trail, search),
			xli.RequireSubcommand(),
		),
	}
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
func Migrate(ctx context.Context, s *cmd.Server) error {
	if err := dropOldSessions(ctx, s); err != nil {
		return err
	}
	if err := entmigrate.NewSchema(s.Drv).Create(ctx); err != nil {
		return err
	}
	// After: the index that replaces each is there by now.
	for _, v := range Replaced {
		if _, err := s.Db.ExecContext(ctx, "DROP INDEX IF EXISTS "+v); err != nil {
			return fmt.Errorf("%s, an index this build replaced: %w", v, err)
		}
	}
	if s.Dialect != "postgres" {
		return nil
	}
	if _, err := s.Db.ExecContext(ctx, pgExtras); err != nil {
		return fmt.Errorf("postgres extras: %w", err)
	}
	return nil
}

// Replaced are the indexes an earlier build made and this one does not. The
// migration makes what the schema declares and drops nothing, so an index whose
// columns changed would stay beside the one that replaced it -- written on every
// insert and read by nothing. Each is dropped once its replacement is made.
//
// #122: every list index begins with the tenant now, and an index that declared
// an edge before a field is in that order rather than with the edge last.
var Replaced = []string{
	"allocation_begins_at_resource_id",
	"allocation_date_created_id",
	"asset_date_created_id",
	"assettype_date_created_id",
	"attachment_date_created_id",
	"bookable_date_created_id",
	"countfinding_date_created_id",
	"countfinding_kind_count_id",
	"custody_date_created_id",
	"custody_issued_at_party_id",
	"custodyline_date_created_id",
	"event_date_created_id",
	"fact_date_created_id",
	"fact_key_valid_from_asset_id",
	"inventorycount_date_created_id",
	"invitation_date_created_id",
	"itemmodel_date_created_id",
	"label_date_created_id",
	"link_date_created_id",
	"link_valid_from_source_id",
	"link_valid_from_target_id",
	"notification_date_created_holder_id",
	"notification_date_created_id",
	"party_date_created_id",
	"placement_date_created_id",
	"placement_valid_from_child_id",
	"placement_valid_from_parent_id",
	"purchase_date_created_id",
	"purchaseline_date_created_id",
	"reservation_begins_at_party_id",
	"reservation_date_created_id",
	"reservationitem_date_created_id",
	"stewardship_date_created_id",
	"stewardship_valid_from_asset_id",
	"stewardship_valid_from_party_id",
	"stock_date_created_id",
	"stockmovement_date_created_id",
	"stockmovement_occurred_at_stock_id",
	"tenantcontract_date_effective_tenant_id",
	"tenantdomain_date_created_id",
	"usagesnapshot_date_created_id",
	"usagesnapshot_day_tenant_id",
	"workorder_date_created_id",
}

// dropOldSessions takes out a session table from before a session was an
// account's (D27). Its rows each name a tenant and a holder the new ones have
// no use for, and a migration only adds, so the columns would stay and refuse
// every new row. Sessions are only who is signed in: everybody signs in again.
func dropOldSessions(ctx context.Context, s *cmd.Server) error {
	q := "SELECT count(*) FROM pragma_table_info('session') WHERE name = 'holder_id'"
	if s.Dialect == "postgres" {
		q = "SELECT count(*) FROM information_schema.columns WHERE table_schema = current_schema() AND table_name = 'session' AND column_name = 'holder_id'"
	}
	n := 0
	if err := s.Db.QueryRowContext(ctx, q).Scan(&n); err != nil {
		return fmt.Errorf("sessions from before accounts: %w", err)
	}
	if n == 0 {
		return nil
	}
	if _, err := s.Db.ExecContext(ctx, "DROP TABLE session"); err != nil {
		return fmt.Errorf("sessions from before accounts: %w", err)
	}
	return nil
}

// pgExtras is what PostgreSQL promises beyond the schema; see the file.
//
//go:embed pg.sql
var pgExtras string
