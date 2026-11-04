// Package admincli is `loft-admin`, the operator's tool (design 6장, decision
// 17, D74). `serve` stands the operator's entry point up beside a deployment,
// on a listener of its own (`server/admin`); every other command calls its
// RPCs, from wherever the operator is, with their key -- the entry point's
// address in `--url` or `LOFT_ADMIN_URL`, the key in `--key` or
// `LOFT_ADMIN_KEY`. It speaks Connect, as the terminal speaks to the public
// server (`cli.Connect`), and the connection is what makes the call an
// operator's.
//
// It is a binary of its own so that `loft`, the one the public reaches, links
// none of it.
package admincli

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/lesomnus/otx/log"
	"github.com/lesomnus/payday/pdcmd"
	"github.com/lesomnus/payday/pdid"
	"github.com/lesomnus/xli"
	"github.com/lesomnus/xli/arg"
	"github.com/lesomnus/xli/cfg"
	"github.com/lesomnus/xli/flg"
	"github.com/lesomnus/z"
	"google.golang.org/grpc"
	"google.golang.org/protobuf/types/known/timestamppb"

	app "github.com/lesomnus/loft"
	"github.com/lesomnus/loft/cli"
	"github.com/lesomnus/loft/cmd"
	"github.com/lesomnus/loft/internal/identity"
	"github.com/lesomnus/loft/server/admin"
)

// Cmd is the root of `loft-admin`.
func Cmd(c *cmd.Config) *xli.Command {
	l := cfg.New(cmd.Name, c)
	version := pdcmd.NewCmdVersion()
	ws, contract, hold, trail := newCmdTenant(), newCmdContract(), newCmdHold(), newCmdTrail()
	search, retention := newCmdSearch(), newCmdRetention()
	identity, holder := newCmdIdentity(), newCmdHolder()

	return &xli.Command{
		Name:  "loft-admin",
		Brief: "the operator's entry point, and what an operator asks of it",

		Flags: flg.Flags{
			cfg.ConfigFlag(),
			&flg.String{Name: "url", Brief: "the entry point, as `loft-admin serve` listens on it; LOFT_ADMIN_URL, or http://127.0.0.1:8081"},
			&flg.String{Name: "key", Brief: "your key, as env:NAME, file:PATH or itself; LOFT_ADMIN_KEY"},
		},

		Commands: []*xli.Command{
			version,
			cfg.NewCmdConfig(l),
			newCmdServe(c),
			ws,
			contract,
			hold,
			trail,
			search,
			retention,
			identity,
			holder,
		},

		// What asks the entry point reads none of a deployment's
		// configuration: an operator runs it from their own machine.
		Handler: xli.Chain(cfg.Load(l, version, ws, contract, hold, trail, search, retention, identity, holder), xli.RequireSubcommand()),
	}
}

func newCmdServe(c *cmd.Config) *xli.Command {
	return &xli.Command{
		Name:  "serve",
		Brief: "serve the operator's entry point beside this deployment, on admin.addr",
		Handler: xli.OnRun(func(ctx context.Context, self *xli.Command, next xli.Next) error {
			ops, err := admin.Operators(c.Admin.Keys)
			if err != nil {
				return err
			}
			if len(ops) == 0 {
				return errors.New("admin.keys: an entry point nobody holds a key to is one nobody can use")
			}
			// Before the server, as `loft serve` does: what it builds logs,
			// traces and measures into the providers it finds on ctx, and
			// without these into ones that discard -- an entry point whose
			// every call went unlogged.
			ctx, done, err := cmd.Telemetry(ctx, c)
			if err != nil {
				return err
			}
			defer done()

			s, err := cmd.Build(ctx, *c)
			if err != nil {
				return err
			}
			defer s.Close()

			routes := admin.Routes{S: s, C: *c, Operators: ops, Ask: admin.NewIssuance(c.Admin.Ask)}
			h, err := routes.Handler(ctx)
			if err != nil {
				return err
			}
			l, err := net.Listen("tcp", c.Admin.ListenAddr())
			if err != nil {
				return err
			}
			log.From(ctx).InfoContext(ctx, "admin", slog.String("addr", l.Addr().String()), slog.Int("operators", len(ops)))
			srv := serving(ctx, h)
			// The ask alone, on a listener of its own, for a proxy that
			// faces the internet (`admin.ask.addr`).
			var ask *http.Server
			if addr := c.Admin.Ask.Addr; addr != "" {
				al, err := net.Listen("tcp", addr)
				if err != nil {
					l.Close()
					return err
				}
				log.From(ctx).InfoContext(ctx, "admin: tls-ask", slog.String("addr", al.Addr().String()))
				ask = serving(ctx, routes.AskHandler())
				go func() {
					if err := ask.Serve(al); err != nil && !errors.Is(err, http.ErrServerClosed) {
						log.From(ctx).ErrorContext(ctx, "admin: tls-ask", slog.Any("err", err))
					}
				}()
			}
			go func() {
				<-ctx.Done()
				shut, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				_ = srv.Shutdown(shut)
				if ask != nil {
					_ = ask.Shutdown(shut)
				}
			}()
			if err := srv.Serve(l); err != nil && !errors.Is(err, http.ErrServerClosed) {
				return err
			}
			return next(ctx)
		}),
	}
}

// serving is an HTTP server for the entry point, on `ctx`: a client that
// takes its time over its headers holds no connection for nothing, and one
// that keeps a connection open between calls has it closed after a while. A
// call, once made, may take as long as it takes.
func serving(ctx context.Context, h http.Handler) *http.Server {
	return &http.Server{
		Handler:           h,
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       2 * time.Minute,
		BaseContext:       func(net.Listener) context.Context { return ctx },
	}
}

// connOf is the connection a command calls the entry point over: its address,
// and the operator's key in every call.
func connOf(self *xli.Command) (grpc.ClientConnInterface, error) {
	base, _ := flg.Find[string](self, "url")
	if base = strings.TrimSpace(base); base == "" {
		base = os.Getenv("LOFT_ADMIN_URL")
	}
	if base == "" {
		base = "http://127.0.0.1:8081"
	}
	ref, _ := flg.Find[string](self, "key")
	if ref = strings.TrimSpace(ref); ref == "" {
		ref = os.Getenv("LOFT_ADMIN_KEY")
	}
	if ref == "" {
		return nil, errors.New("--key or LOFT_ADMIN_KEY: the entry point answers an operator's key")
	}
	key, err := identity.ResolveRef(ref)
	if err != nil {
		return nil, fmt.Errorf("--key: %w", err)
	}
	h := http.Header{}
	h.Set("Authorization", "Bearer "+strings.TrimSpace(key))
	h.Set("User-Agent", "loft-admin")
	return cli.Connect(base, h), nil
}

// yes is whether the command was told to do it: without it, a command says
// what it would do and does nothing.
func yes(self *xli.Command) bool {
	v, _ := flg.Find[bool](self, "yes")
	return v
}

// nothing says that nothing was done, and how to do it.
func nothing(self *xli.Command) {
	self.Printf("(nothing was done: `--yes` does it)\n")
}

func str(self *xli.Command, name string) string {
	v, _ := flg.Find[string](self, name)
	return strings.TrimSpace(v)
}

// at is the day of a moment as the operator's commands say it: in UTC, as
// the shell's do, whatever zone the operator's own machine is in -- and so
// the day a date given to one (`--effective 2026-01-01`) was given as.
func at(t *timestamppb.Timestamp) string {
	if t == nil {
		return "-"
	}
	return t.AsTime().UTC().Format(time.DateOnly)
}

// days is a count of days as a command says it, and what 0 means in words.
func days(n uint32, zero string) string {
	if n == 0 {
		return zero
	}
	return fmt.Sprintf("%dd", n)
}

// state is whether a workspace is open, as its line says.
func state(t *app.Tenant) string {
	if !t.HasDateClosed() {
		return "open"
	}
	return fmt.Sprintf("closed %s by %s, destroyed %s", at(t.GetDateClosed()), t.GetClosedBy(), at(t.GetDateDestroy()))
}

func printTenant(self *xli.Command, t *app.Tenant) {
	self.Printf("@%s\t%s\t%s\n", t.GetAlias(), t.GetName(), state(t))
}

// printShow is a workspace as `show` says it: its line, who owns it and how
// many are in it, what its own settings and the deployment's come to, its
// members, contracts and holds.
func printShow(self *xli.Command, v *app.OperatorTenantShowResponse) {
	t := v.GetTenant()
	owners := []string{}
	for _, m := range v.GetMembers() {
		if m.GetRole() == "owner" {
			owners = append(owners, m.GetAlias())
		}
	}
	slices.Sort(owners)
	who := strings.Join(owners, ",")
	if who == "" {
		who = "(none)"
	}
	self.Printf("@%s\t%s\t%d people\towners %s\t%s\n", t.GetAlias(), t.GetName(), len(v.GetMembers()), who, state(t))
	self.Printf("  settings\thistory %s, access record %s, restore %s\n",
		days(t.GetKeepDays(), "forever"), days(t.GetAccessKeepDays(), "the deployment's"), days(t.GetRestoreDays(), "the deployment's"))
	w := v.GetWindow()
	self.Printf("  keeps\thistory %s, access record %s, restore %s once closed, then %s\n",
		days(w.GetKeepDays(), "forever"), days(w.GetAccessDays(), "forever"), days(w.GetRestoreDays(), "forever"), leaving(w.GetPurge()))
	for _, m := range v.GetMembers() {
		leaving := ""
		if m.HasDateLeaving() {
			leaving = "\tleaving since " + at(m.GetDateLeaving())
		}
		self.Printf("  member\t@%s\t%s\t%s%s\n", m.GetAlias(), m.GetRole(), m.GetName(), leaving)
	}
	for _, c := range v.GetContracts() {
		desc := ""
		if c.GetDesc() != "" {
			desc = "\t" + c.GetDesc()
		}
		self.Printf("  contract\t%s\t%s\tthen %s%s\n", at(c.GetDateEffective()), c.GetName(), leaving(c.GetPurgeFloors()), desc)
	}
	for _, h := range v.GetHolds() {
		lifted := "held"
		if h.HasDateLifted() {
			lifted = "lifted " + at(h.GetDateLifted())
		}
		self.Printf("  hold\t%s\t%s\t%s\n", idOf(h.GetId()), h.GetName(), lifted)
	}
}

// leaving is what becomes of a workspace's trail when it is destroyed.
func leaving(purge bool) string {
	if purge {
		return "purge"
	}
	return "retire"
}

func idOf(b []byte) string {
	id, err := pdid.From(b)
	if err != nil {
		return "-"
	}
	return id.String()
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

// change is a command that changes a workspace: it says what it would do, and
// does it with `--yes`.
func change(name, brief string, flags flg.Flags, run func(ctx context.Context, self *xli.Command, conn grpc.ClientConnInterface, ws string) error) *xli.Command {
	return &xli.Command{
		Name:  name,
		Brief: brief,
		Flags: append(flags, &flg.Switch{Name: "yes", Brief: "do it; without this it only says what it would do"}),
		Args:  arg.Args{workspaceArg},
		Handler: xli.OnRun(func(ctx context.Context, self *xli.Command, next xli.Next) error {
			conn, err := connOf(self)
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

func newCmdTenant() *xli.Command {
	return &xli.Command{
		Name:    "tenant",
		Aliases: []string{"workspace", "ws"},
		Brief:   "find workspaces, close and restore them, move one's address, name an owner, and their end",
		Commands: append(xli.Commands{
			{
				Name:    "list",
				Aliases: []string{"ls"},
				Brief:   "the workspaces, by a piece of their address or name",
				Flags: flg.Flags{
					&flg.String{Name: "q", Brief: "a piece of the address or the name"},
					&flg.Switch{Name: "closed", Brief: "only the closed ones"},
				},
				Handler: xli.OnRun(func(ctx context.Context, self *xli.Command, next xli.Next) error {
					conn, err := connOf(self)
					if err != nil {
						return err
					}
					q := strings.ToLower(str(self, "q"))
					closed, _ := flg.Find[bool](self, "closed")
					// Every workspace, a page at a time, and the ones asked
					// for said: the generated list filters by reference
					// alone.
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
			},
			{
				Name:  "show",
				Brief: "one workspace: who is in it, what it keeps, its contracts and its holds",
				Args:  arg.Args{workspaceArg},
				Handler: xli.OnRun(func(ctx context.Context, self *xli.Command, next xli.Next) error {
					conn, err := connOf(self)
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
			},
			change("close", "close a workspace as the operator; its owner may restore it within the window",
				flg.Flags{&flg.String{Name: "reason", Brief: "why, in words; the workspace's history keeps it"}},
				func(ctx context.Context, self *xli.Command, conn grpc.ClientConnInterface, ws string) error {
					v, err := show(ctx, conn, byAlias(ws))
					if err != nil {
						return err
					}
					t := v.GetTenant()
					if t.HasDateClosed() {
						self.Printf("@%s is closed already, since %s; destroyed after %s\n", t.GetAlias(), at(t.GetDateClosed()), at(t.GetDateDestroy()))
						return nil
					}
					if !yes(self) {
						until := time.Now().Add(time.Duration(v.GetWindow().GetRestoreDays()) * 24 * time.Hour)
						self.Printf("would close @%s; it is destroyed after %s unless restored\n", t.GetAlias(), at(timestamppb.New(until)))
						nothing(self)
						return nil
					}
					t, err = app.NewTenantServiceClient(conn).Close(ctx, app.TenantCloseRequest_builder{
						Ref:     app.TenantRef_builder{Alias: z.Ptr(t.GetAlias())}.Build(),
						Confirm: t.GetAlias(),
						Reason:  str(self, "reason"),
					}.Build())
					if err != nil {
						return err
					}
					printTenant(self, t)
					return nil
				}),
			change("restore", "open a closed workspace again, while its restore window lasts", nil,
				func(ctx context.Context, self *xli.Command, conn grpc.ClientConnInterface, ws string) error {
					if !yes(self) {
						v, err := show(ctx, conn, byAlias(ws))
						if err != nil {
							return err
						}
						t := v.GetTenant()
						self.Printf("would restore @%s (%s)\n", t.GetAlias(), state(t))
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
				}),
			change("alias", "move a workspace to another address; the old one is held and sends people on",
				flg.Flags{
					&flg.String{Name: "to", Brief: "the new address"},
					&flg.Switch{Name: "reuse", Brief: "take an address that is held anyway"},
				},
				func(ctx context.Context, self *xli.Command, conn grpc.ClientConnInterface, ws string) error {
					reuse, _ := flg.Find[bool](self, "reuse")
					v, err := app.NewOperatorTenantServiceClient(conn).Move(ctx, app.OperatorTenantMoveRequest_builder{
						Ref:    byAlias(ws),
						To:     str(self, "to"),
						Reuse:  reuse,
						DryRun: !yes(self),
					}.Build())
					if err != nil {
						return err
					}
					if !yes(self) {
						self.Printf("would move @%s to @%s, holding @%s until %s\n", v.GetFrom(), strings.TrimPrefix(str(self, "to"), "@"), v.GetFrom(), at(v.GetHeldUntil()))
						nothing(self)
						return nil
					}
					self.Printf("moved @%s to @%s; @%s sends people on until %s\n", v.GetFrom(), v.GetTenant().GetAlias(), v.GetFrom(), at(v.GetHeldUntil()))
					printTenant(self, v.GetTenant())
					return nil
				}),
			change("owner", "make a member one of the workspace's owners",
				flg.Flags{&flg.String{Name: "member", Brief: "the membership, by its @alias in the workspace"}},
				func(ctx context.Context, self *xli.Command, conn grpc.ClientConnInterface, ws string) error {
					member := strings.TrimPrefix(str(self, "member"), "@")
					if member == "" {
						return errors.New("--member: the membership, by its @alias in the workspace")
					}
					if !yes(self) {
						self.Printf("would make @%s one of @%s's owners; whoever owns it still does\n", member, ws)
						nothing(self)
						return nil
					}
					if _, err := app.NewOperatorTenantServiceClient(conn).NameOwner(ctx, app.OperatorTenantNameOwnerRequest_builder{
						Ref:    byAlias(ws),
						Member: member,
					}.Build()); err != nil {
						return err
					}
					v, err := show(ctx, conn, byAlias(ws))
					if err != nil {
						return err
					}
					printShow(self, v)
					return nil
				}),
		}, tenantEnd()...),
		Handler: xli.RequireSubcommand(),
	}
}

func newCmdContract() *xli.Command {
	return &xli.Command{
		Name:  "contract",
		Brief: "what the operator's agreement with a workspace says beyond its own settings",
		Commands: xli.Commands{
			change("set", "write a workspace's next contract, in force from --effective",
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
							str(self, "name"), ws, at(timestamppb.New(effective)), leaving(purge))
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
				}),
			{
				Name:  "show",
				Brief: "a workspace's contracts, its holds, and the windows its settings come to now",
				Args:  arg.Args{workspaceArg},
				Handler: xli.OnRun(func(ctx context.Context, self *xli.Command, next xli.Next) error {
					conn, err := connOf(self)
					if err != nil {
						return err
					}
					v, err := show(ctx, conn, byAlias(alias(self)))
					if err != nil {
						return err
					}
					printWindow(self, v)
					return next(ctx)
				}),
			},
		},
		Handler: xli.RequireSubcommand(),
	}
}

// printWindow is what `contract show` says: a workspace's contracts, and what
// its settings, contracts and holds come to now.
func printWindow(self *xli.Command, v *app.OperatorTenantShowResponse) {
	for _, c := range v.GetContracts() {
		desc := ""
		if c.GetDesc() != "" {
			desc = "  " + c.GetDesc()
		}
		name := c.GetName()
		if name == "" {
			name = "-"
		}
		self.Printf("%s  %-12s then %s%s\n", at(c.GetDateEffective()), name, leaving(c.GetPurgeFloors()), desc)
	}
	if len(v.GetContracts()) == 0 {
		self.Printf("no contract: a destroyed workspace retires\n")
	}
	w := v.GetWindow()
	self.Printf("history: kept %s\n", days(w.GetKeepDays(), "forever"))
	if w.HasKeepUntil() {
		self.Printf("  until %s, then %s: made shorter, and waiting out the grace\n", at(w.GetKeepUntil()), days(w.GetKeepThenDays(), "forever"))
	}
	if w.HasAccessUntil() {
		self.Printf("  the access record's until %s, then %s: made shorter, and waiting out the grace\n", at(w.GetAccessUntil()), days(w.GetAccessThenDays(), "forever"))
	}
	self.Printf("access record: kept %s\n", days(w.GetAccessDays(), "forever"))
	self.Printf("restore: %s once closed\n", days(w.GetRestoreDays(), "forever"))
	if w.GetPurge() {
		self.Printf("destroyed: purged -- its whole trail, what the deployment's floors are on too\n")
	} else {
		self.Printf("destroyed: retired -- what the deployment's floors are on of its trail stays until they pass\n")
	}
	for _, h := range v.GetHolds() {
		if h.HasDateLifted() {
			continue
		}
		self.Printf("held:  %s since %s  %s\n", idOf(h.GetId()), at(h.GetDateCreated()), h.GetName())
	}
	if !w.GetApply() && (w.GetKeepDays() > 0 || w.GetAccessDays() > 0) {
		self.Printf("(app.retention.apply is off, so nothing is destroyed yet)\n")
	}
}

func newCmdHold() *xli.Command {
	return &xli.Command{
		Name:  "hold",
		Brief: "a legal hold on a workspace's history",
		Commands: xli.Commands{
			change("place", "keep everything of a workspace's history until the hold is lifted",
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
				}),
			{
				Name:  "lift",
				Brief: "end a hold; the windows apply again from the next pass",
				Flags: flg.Flags{&flg.Switch{Name: "yes", Brief: "do it; without this it only says what it would do"}},
				Args:  arg.Args{&arg.String{Name: "ID", Brief: "the hold, as `tenant show` lists it"}},
				Handler: xli.OnRun(func(ctx context.Context, self *xli.Command, next xli.Next) error {
					raw, _ := arg.Get[string](self, "ID")
					id, err := pdid.Parse(strings.TrimSpace(raw))
					if err != nil {
						return fmt.Errorf("ID: %w", err)
					}
					conn, err := connOf(self)
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
			},
		},
		Handler: xli.RequireSubcommand(),
	}
}

// instant reads a moment as RFC 3339, or a date as the start of that day in
// UTC: the day an operator's command says it back as, whatever zone the
// operator's machine is in.
func instant(v string) (time.Time, error) {
	v = strings.TrimSpace(v)
	if t, err := time.Parse(time.DateOnly, v); err == nil {
		return t, nil
	}
	return time.Parse(time.RFC3339, v)
}
