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
	"database/sql"
	_ "embed"
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/lesomnus/xli"
	"github.com/lesomnus/xli/cfg"
	"github.com/lesomnus/xli/flg"
	"github.com/lesomnus/xli/frm"
	"github.com/lesomnus/xli/mode"

	"github.com/lesomnus/payday/pdcmd"
	"github.com/lesomnus/payday/rename"

	"github.com/lesomnus/rove/cmd"
	entmigrate "github.com/lesomnus/rove/internal/ent/migrate"
	"github.com/lesomnus/rove/server/offboard"
	"github.com/lesomnus/rove/server/pd"
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
	login, logout, whoami, use, ws, api, scan, register, agree, assist := NewCmdLogin(), NewCmdLogout(), NewCmdWhoami(), NewCmdUse(), NewCmdWorkspace(), NewCmdApi(), NewCmdScan(), NewCmdRegister(), NewCmdConsent(), NewCmdMcp()

	// The operator's half: each stands the stack up, does one thing and exits,
	// and is observed as the deployment is (`observed`).
	first, migrate, contract, hold, retention, tenant, holder, identity, trail, search := NewCmdInit(c), NewCmdMigrate(c), NewCmdContract(c), NewCmdHold(c), NewCmdRetention(c), NewCmdTenant(c), NewCmdHolder(c), NewCmdIdentity(c), NewCmdTrail(c), NewCmdSearch(c)
	access := NewCmdAccess(c)

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
			access,
			search,
			login,
			logout,
			whoami,
			use,
			ws,
			api,
			scan,
			register,
			agree,
			assist,
		},

		Handler: xli.Chain(
			cfg.Load(l, version, login, logout, whoami, use, ws, api, scan, register, agree, assist),
			observed(c, first, migrate, contract, hold, retention, tenant, holder, identity, trail, access, search),
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
//
// The indexes an earlier build stored under the names of their columns are
// moved to the names the schema declares first (payday#132): a migration finds
// an index by its name, and one it does not find it makes again, from every
// row, beside the old one -- which it leaves, since it drops no index. On
// PostgreSQL that is a rename, which builds nothing; SQLite renames no index,
// so the old one is dropped and `Create` makes the new one. It replaces the list
// of replaced indexes this dropped by hand after #122, whose every deployment
// has migrated past that build.
func Migrate(ctx context.Context, s *cmd.Server) error {
	if err := dropOldSessions(ctx, s); err != nil {
		return err
	}
	if err := dropGone(ctx, s); err != nil {
		return err
	}
	if err := rename.Indexes(ctx, s.Db, s.Dialect, pd.IndexRenames); err != nil {
		return err
	}
	if err := entmigrate.NewSchema(s.Drv).Create(ctx); err != nil {
		return err
	}
	if err := moveTerms(ctx, s); err != nil {
		return err
	}
	if s.Dialect != "postgres" {
		return nil
	}
	if _, err := s.Db.ExecContext(ctx, pgExtras); err != nil {
		return fmt.Errorf("postgres extras: %w", err)
	}
	return nil
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

// dropGone takes out the tables an earlier build had and this one does not
// ([offboard.Dropped]), rows and all. A migration only adds, so the table
// would stay -- and its rows each point at their tenant, which a workspace's
// destruction could then not delete.
func dropGone(ctx context.Context, s *cmd.Server) error {
	for _, name := range slices.Sorted(maps.Keys(offboard.Dropped)) {
		if _, err := s.Db.ExecContext(ctx, "DROP TABLE IF EXISTS "+name); err != nil {
			return fmt.Errorf("%s, which this build does not have: %w", name, err)
		}
	}
	return nil
}

// moveTerms moves what the workspaces' contracts said of how long each keeps
// its history and its access record and may be restored once closed onto the
// workspaces, once: they are each workspace's own settings now (D36). Then the
// contracts' columns for them go, and the view window's and the grace's with
// them -- a migration only adds, and these say no default, so every contract
// written without them would be refused.
//
// The contract in force says what a workspace keeps. The one before it says
// what it kept before, from when the one in force took effect: a shorter window
// waits out the deployment's grace (`app.retention.grace`) from then, as it
// waited out the contract's own. What a contract dated ahead of the migration
// said of these is not carried; the rest of it is, and it still takes effect.
//
// A contract that said any of them is a row a migration rewrote, and the trail
// finds it so (`rove trail rows`) until an operator accepts it once.
func moveTerms(ctx context.Context, s *cmd.Server) error {
	q := "SELECT count(*) FROM pragma_table_info('tenantcontract') WHERE name = 'keep_days'"
	if s.Dialect == "postgres" {
		q = "SELECT count(*) FROM information_schema.columns WHERE table_schema = current_schema() AND table_name = 'tenantcontract' AND column_name = 'keep_days'"
	}
	n := 0
	if err := s.Db.QueryRowContext(ctx, q).Scan(&n); err != nil {
		return fmt.Errorf("the contracts' terms: %w", err)
	}
	if n == 0 {
		return nil
	}

	tx, err := s.Db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	type terms struct {
		tenant                any
		effective             sql.NullTime
		keep, access, restore sql.NullInt64
	}
	rows, err := tx.QueryContext(ctx, "SELECT tenant_id, date_effective, keep_days, access_keep_days, restore_days FROM tenantcontract "+
		"WHERE date_erased IS NULL ORDER BY tenant_id, date_effective DESC, id DESC")
	if err != nil {
		return fmt.Errorf("the contracts' terms: %w", err)
	}
	byTenant := [][]terms{}
	var last any
	for rows.Next() {
		var v terms
		if err := rows.Scan(&v.tenant, &v.effective, &v.keep, &v.access, &v.restore); err != nil {
			rows.Close()
			return fmt.Errorf("the contracts' terms: %w", err)
		}
		if len(byTenant) == 0 || fmt.Sprint(v.tenant) != fmt.Sprint(last) {
			byTenant = append(byTenant, nil)
			last = v.tenant
		}
		byTenant[len(byTenant)-1] = append(byTenant[len(byTenant)-1], v)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return fmt.Errorf("the contracts' terms: %w", err)
	}

	now := time.Now()
	update := placeheld(s.Dialect, "UPDATE tenant SET keep_days = ?, access_keep_days = ?, restore_days = ?, "+
		"keep_days_before = ?, date_keep_shortened = ?, access_keep_days_before = ?, date_access_keep_shortened = ? WHERE id = ?")
	for _, vs := range byTenant {
		i := slices.IndexFunc(vs, func(v terms) bool { return !v.effective.Valid || !v.effective.Time.After(now) })
		if i < 0 {
			continue
		}
		in := vs[i]
		var keepBefore, accessBefore int64
		var keepSince, accessSince any
		if i+1 < len(vs) {
			was := vs[i+1]
			if outlasts(was.keep.Int64, in.keep.Int64) {
				keepBefore, keepSince = was.keep.Int64, in.effective.Time
			}
			// Whether the deployment's access record outlasts a count of days is
			// the deployment's to say when it is read: which keeps longer is
			// read then.
			if was.access.Int64 != in.access.Int64 {
				accessBefore, accessSince = was.access.Int64, in.effective.Time
			}
		}
		if _, err := tx.ExecContext(ctx, update, in.keep.Int64, in.access.Int64, in.restore.Int64,
			keepBefore, keepSince, accessBefore, accessSince, in.tenant); err != nil {
			return fmt.Errorf("the contracts' terms, onto %v: %w", in.tenant, err)
		}
	}

	for _, c := range []string{"view_days", "keep_days", "grace_days", "access_keep_days", "restore_days"} {
		if _, err := tx.ExecContext(ctx, "ALTER TABLE tenantcontract DROP COLUMN "+c); err != nil {
			return fmt.Errorf("the contracts' %s: %w", c, err)
		}
	}

	return tx.Commit()
}

// outlasts answers whether a count of days keeps longer than another, where 0
// is forever.
func outlasts(a, b int64) bool {
	return b > 0 && (a == 0 || a > b)
}

// placeheld is a statement with each `?` as the dialect binds it: `$1`, `$2`
// for PostgreSQL.
func placeheld(dialect, q string) string {
	if dialect != "postgres" {
		return q
	}

	b := strings.Builder{}
	n := 0
	for _, r := range q {
		if r == '?' {
			n++
			fmt.Fprintf(&b, "$%d", n)
			continue
		}
		b.WriteRune(r)
	}

	return b.String()
}

// pgExtras is what PostgreSQL promises beyond the schema; see the file.
//
//go:embed pg.sql
var pgExtras string
