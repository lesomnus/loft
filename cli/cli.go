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
	"cmp"
	"context"
	"database/sql"
	_ "embed"
	"fmt"
	"maps"
	"slices"
	"time"
	"uuid"

	"github.com/lesomnus/xli"
	"github.com/lesomnus/xli/cfg"
	"github.com/lesomnus/xli/flg"
	"github.com/lesomnus/xli/frm"
	"github.com/lesomnus/xli/mode"
	"github.com/protobuf-orm/ent/dialect"
	entsql "github.com/protobuf-orm/ent/dialect/sql"

	"github.com/lesomnus/payday/pdcmd"
	"github.com/lesomnus/payday/pdid"
	"github.com/lesomnus/payday/rename"

	"github.com/lesomnus/rove/cmd"
	"github.com/lesomnus/rove/internal/ent"
	entmigrate "github.com/lesomnus/rove/internal/ent/migrate"
	"github.com/lesomnus/rove/internal/ent/tenantcontract"
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
	_, err := migrate(ctx, s)
	return err
}

// Migrated is what a migration did that is more than the schema's shape:
// rows it rewrote, and how many of them it put on the trail.
type Migrated struct {
	// Facts is the assets' facts moved into payday's past (payday#151), and
	// Recorded the lines the trail was given of them.
	Facts, Recorded int
}

// migrate is [Migrate], answering what it rewrote.
func migrate(ctx context.Context, s *cmd.Server) (Migrated, error) {
	out := Migrated{}
	err := func() error {
		if err := dropOldSessions(ctx, s); err != nil {
			return err
		}
		if err := dropGone(ctx, s); err != nil {
			return err
		}
		if err := rename.Indexes(ctx, s.Db, s.Dialect, pd.IndexRenames); err != nil {
			return err
		}
		if err := stashTerms(ctx, s); err != nil {
			return err
		}
		if err := stashFacts(ctx, s); err != nil {
			return err
		}
		if err := entmigrate.NewSchema(s.Drv).Create(ctx); err != nil {
			return err
		}
		if err := moveTerms(ctx, s); err != nil {
			return err
		}
		var err error
		if out.Facts, out.Recorded, err = moveFacts(ctx, s); err != nil {
			return err
		}
		if s.Dialect != "postgres" {
			return nil
		}
		if _, err := s.Db.ExecContext(ctx, pgExtras); err != nil {
			return fmt.Errorf("postgres extras: %w", err)
		}
		return nil
	}()
	return out, err
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

// The contracts' terms a workspace's own settings took over (D36), and the
// columns they were kept in, with the view window's and the grace's: a
// migration only adds, and these say no default, so every contract written
// without them would be refused.
var oldTerms = []string{"view_days", "keep_days", "grace_days", "access_keep_days", "restore_days"}

// termsAside is where [stashTerms] sets the contracts' terms aside, by
// contract, while the schema is migrated beneath them.
const termsAside = "rove_contract_terms"

// stashTerms sets aside what the workspaces' contracts said of how long each
// keeps its history and its access record and may be restored once closed,
// before the schema is migrated, for [moveTerms] to move onto the workspaces
// once it is (D36).
//
// Before, because the schema's migration may take the columns first. On
// SQLite a table is changed by making it again -- a new table, its rows copied
// in, the old one dropped -- and what is copied is the columns the schema
// declares: a contract table that has to change for anything else, as one
// from before `purge_floors` does, comes out of it without these. Set aside,
// they are a table of their own the migration does not touch, until the move
// has them.
//
// While the columns are there they are what the contracts say, and the terms
// are set aside again from them; once they are gone, what was set aside is
// all there is, and it is kept.
func stashTerms(ctx context.Context, s *cmd.Server) error {
	has, err := hasColumn(ctx, s, "tenantcontract", "keep_days")
	if err != nil || !has {
		return err
	}

	tx, err := s.Db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, q := range []string{
		"DROP TABLE IF EXISTS " + termsAside,
		"CREATE TABLE " + termsAside + " AS SELECT id AS contract_id, keep_days, access_keep_days, restore_days FROM tenantcontract",
	} {
		if _, err := tx.ExecContext(ctx, q); err != nil {
			return fmt.Errorf("the contracts' terms, set aside: %w", err)
		}
	}

	return tx.Commit()
}

// moveTerms moves the contracts' terms onto the workspaces, once: they are
// each workspace's own settings now (D36). Then the contracts' columns for them
// go, where the schema's migration left them, and what [stashTerms] set aside.
//
// The terms are the columns' while they are there -- what a contract written
// since they were set aside says too -- and what was set aside once they are
// not. A contract written after the columns had gone said none, and keeps
// none.
//
// The contract in force says what a workspace keeps. The one before it says
// what it kept before, from when the one in force took effect: a shorter window
// waits out the deployment's grace (`app.retention.grace`) from then, as it
// waited out the contract's own. What a contract dated ahead of the migration
// said of these is not carried; the rest of it is, and it still takes effect.
//
// The contracts are read as the app reads them, through ent: which of them is
// in force is their dates, in whatever form the database holds a time. A
// contract whose terms do not read, or do not go onto its workspace, is
// refused by name, and nothing is moved or dropped.
//
// A contract that said any of them is a row a migration rewrote, and the trail
// finds it so (`rove trail rows`) until an operator accepts it once.
func moveTerms(ctx context.Context, s *cmd.Server) error {
	columns, err := hasColumn(ctx, s, "tenantcontract", "keep_days")
	if err != nil {
		return err
	}
	aside, err := hasTable(ctx, s, termsAside)
	if err != nil {
		return err
	}
	if !columns && !aside {
		return nil
	}

	tdrv, tx, err := dialect.BeginTx(ctx, s.Drv)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	db := ent.NewClient(ent.Driver(tdrv))

	from := "SELECT contract_id, keep_days, access_keep_days, restore_days FROM " + termsAside
	if columns {
		from = "SELECT id, keep_days, access_keep_days, restore_days FROM tenantcontract"
	}
	type terms struct{ keep, access, restore int64 }
	said := map[uuid.UUID]terms{}
	rows := &entsql.Rows{}
	if err := tdrv.Query(ctx, from, []any{}, rows); err != nil {
		return fmt.Errorf("the contracts' terms: %w", err)
	}
	for rows.Next() {
		var id string
		var keep, access, restore sql.NullInt64
		if err := rows.Scan(&id, &keep, &access, &restore); err != nil {
			rows.Close()
			return fmt.Errorf("the contracts' terms: %w", err)
		}
		k, err := uuid.Parse(id)
		if err != nil {
			rows.Close()
			return fmt.Errorf("the contracts' terms: contract %q: %w", id, err)
		}
		said[k] = terms{keep.Int64, access.Int64, restore.Int64}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return fmt.Errorf("the contracts' terms: %w", err)
	}

	cs, err := db.TenantContract.Query().Where(tenantcontract.DateErasedIsNil()).All(ctx)
	if err != nil {
		return fmt.Errorf("the contracts, as they are read: %w", err)
	}
	byTenant := map[uuid.UUID][]*ent.TenantContract{}
	for _, c := range cs {
		byTenant[c.TenantId] = append(byTenant[c.TenantId], c)
	}

	now := time.Now()
	for _, t := range slices.SortedFunc(maps.Keys(byTenant), func(a, b uuid.UUID) int { return a.Compare(b) }) {
		vs := byTenant[t]
		// The latest first, as the contract in force is read.
		slices.SortFunc(vs, func(a, b *ent.TenantContract) int {
			return cmp.Or(b.DateEffective.Compare(a.DateEffective), b.Id.Compare(a.Id))
		})
		i := slices.IndexFunc(vs, func(v *ent.TenantContract) bool { return !v.DateEffective.After(now) })
		if i < 0 {
			continue
		}
		in, is := vs[i], said[vs[i].Id]
		up := db.Tenant.UpdateOneId(t).
			SetKeepDays(uint32(is.keep)).
			SetAccessKeepDays(uint32(is.access)).
			SetRestoreDays(uint32(is.restore))
		if i+1 < len(vs) {
			was := said[vs[i+1].Id]
			if outlasts(was.keep, is.keep) {
				up.SetKeepDaysBefore(uint32(was.keep)).SetDateKeepShortened(in.DateEffective)
			}
			// Whether the deployment's access record outlasts a count of days is
			// the deployment's to say when it is read: which keeps longer is
			// read then.
			if was.access != is.access {
				up.SetAccessKeepDaysBefore(uint32(was.access)).SetDateAccessKeepShortened(in.DateEffective)
			}
		}
		if err := up.Exec(ctx); err != nil {
			return fmt.Errorf("contract %s, onto tenant %s: %w", pdid.Id(in.Id), pdid.Id(t), err)
		}
	}

	if columns {
		for _, c := range oldTerms {
			has, err := hasColumnIn(ctx, tdrv, s.Dialect, "tenantcontract", c)
			if err != nil {
				return err
			}
			if !has {
				continue
			}
			if err := tdrv.Exec(ctx, "ALTER TABLE tenantcontract DROP COLUMN "+c, []any{}, nil); err != nil {
				return fmt.Errorf("the contracts' %s: %w", c, err)
			}
		}
	}
	if aside {
		if err := tdrv.Exec(ctx, "DROP TABLE "+termsAside, []any{}, nil); err != nil {
			return fmt.Errorf("the contracts' terms, set aside: %w", err)
		}
	}

	return tx.Commit()
}

// hasColumn answers whether a table of the database has a column.
func hasColumn(ctx context.Context, s *cmd.Server, table, column string) (bool, error) {
	return hasColumnIn(ctx, s.Drv, s.Dialect, table, column)
}

// hasColumnIn is [hasColumn] through `drv`, which may be a transaction's.
func hasColumnIn(ctx context.Context, drv dialect.Driver, d, table, column string) (bool, error) {
	q := "SELECT count(*) FROM pragma_table_info(?) WHERE name = ?"
	if d == "postgres" {
		q = "SELECT count(*) FROM information_schema.columns WHERE table_schema = current_schema() AND table_name = $1 AND column_name = $2"
	}

	return counted(ctx, drv, q, table, column)
}

// hasTable answers whether the database has a table.
func hasTable(ctx context.Context, s *cmd.Server, table string) (bool, error) {
	q := "SELECT count(*) FROM sqlite_master WHERE type = 'table' AND name = ?"
	if s.Dialect == "postgres" {
		q = "SELECT count(*) FROM information_schema.tables WHERE table_schema = current_schema() AND table_name = $1"
	}

	return counted(ctx, s.Drv, q, table)
}

// counted answers whether a count asked of the database is more than none.
func counted(ctx context.Context, drv dialect.Driver, q string, args ...any) (bool, error) {
	rows := &entsql.Rows{}
	if err := drv.Query(ctx, q, args, rows); err != nil {
		return false, err
	}
	defer rows.Close()

	n := 0
	if rows.Next() {
		if err := rows.Scan(&n); err != nil {
			return false, err
		}
	}

	return n > 0, rows.Err()
}

// outlasts answers whether a count of days keeps longer than another, where 0
// is forever.
func outlasts(a, b int64) bool {
	return b > 0 && (a == 0 || a > b)
}

// pgExtras is what PostgreSQL promises beyond the schema; see the file.
//
//go:embed pg.sql
var pgExtras string
