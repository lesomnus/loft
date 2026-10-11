// Package admincli is `rove-admin`, the operator's tool (design 6장, decision
// 17). `serve` stands the operator's entry point up beside a deployment, on a
// listener of its own (`server/admin`); every other command asks it, from
// wherever the operator is, with their key -- the entry point's address in
// `--url` or `ROVE_ADMIN_URL`, the key in `--key` or `ROVE_ADMIN_KEY`.
//
// It is a binary of its own so that `rove`, the one the public reaches, links
// none of it.
package admincli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/lesomnus/otx/log"
	"github.com/lesomnus/payday/pdcmd"
	"github.com/lesomnus/xli"
	"github.com/lesomnus/xli/arg"
	"github.com/lesomnus/xli/cfg"
	"github.com/lesomnus/xli/flg"

	"github.com/lesomnus/rove/cmd"
	"github.com/lesomnus/rove/internal/identity"
	"github.com/lesomnus/rove/server/admin"
)

// Cmd is the root of `rove-admin`.
func Cmd(c *cmd.Config) *xli.Command {
	l := cfg.New(cmd.Name, c)
	version := pdcmd.NewCmdVersion()
	ws, contract, hold := newCmdWorkspace(), newCmdContract(), newCmdHold()

	return &xli.Command{
		Name:  "rove-admin",
		Brief: "the operator's entry point, and what an operator asks of it",

		Flags: flg.Flags{
			cfg.ConfigFlag(),
			&flg.String{Name: "url", Brief: "the entry point, as `rove-admin serve` listens on it; ROVE_ADMIN_URL, or http://127.0.0.1:8081"},
			&flg.String{Name: "key", Brief: "your key, as env:NAME, file:PATH or itself; ROVE_ADMIN_KEY"},
		},

		Commands: []*xli.Command{
			version,
			cfg.NewCmdConfig(l),
			newCmdServe(c),
			ws,
			contract,
			hold,
		},

		// What asks the entry point reads none of a deployment's
		// configuration: an operator runs it from their own machine.
		Handler: xli.Chain(cfg.Load(l, version, ws, contract, hold), xli.RequireSubcommand()),
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
			s, err := cmd.Build(ctx, *c)
			if err != nil {
				return err
			}
			defer s.Close()

			l, err := net.Listen("tcp", c.Admin.ListenAddr())
			if err != nil {
				return err
			}
			log.From(ctx).InfoContext(ctx, "admin", slog.String("addr", l.Addr().String()), slog.Int("operators", len(ops)))
			srv := &http.Server{
				Handler:           admin.Routes{S: s, C: *c, Operators: ops}.Handler(),
				ReadHeaderTimeout: 10 * time.Second,
				BaseContext:       func(net.Listener) context.Context { return ctx },
			}
			go func() {
				<-ctx.Done()
				shut, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				_ = srv.Shutdown(shut)
			}()
			if err := srv.Serve(l); err != nil && !errors.Is(err, http.ErrServerClosed) {
				return err
			}
			return next(ctx)
		}),
	}
}

// client is what a command asks the entry point with.
type client struct {
	base string
	key  string
}

func clientOf(self *xli.Command) (client, error) {
	base, _ := flg.Find[string](self, "url")
	if base = strings.TrimSpace(base); base == "" {
		base = os.Getenv("ROVE_ADMIN_URL")
	}
	if base == "" {
		base = "http://127.0.0.1:8081"
	}
	ref, _ := flg.Find[string](self, "key")
	if ref = strings.TrimSpace(ref); ref == "" {
		ref = os.Getenv("ROVE_ADMIN_KEY")
	}
	if ref == "" {
		return client{}, errors.New("--key or ROVE_ADMIN_KEY: the entry point answers an operator's key")
	}
	key, err := identity.ResolveRef(ref)
	if err != nil {
		return client{}, fmt.Errorf("--key: %w", err)
	}
	return client{base: strings.TrimSuffix(base, "/"), key: key}, nil
}

func (c client) do(ctx context.Context, method, path string, body, out any) error {
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, rd)
	if err != nil {
		return err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Authorization", "Bearer "+c.key)
	req.Header.Set("User-Agent", "rove-admin")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(res.Body, 8<<20))
	if res.StatusCode >= 300 {
		return fmt.Errorf("%s: %s", res.Status, strings.TrimSpace(string(b)))
	}
	if out != nil {
		return json.Unmarshal(b, out)
	}
	return nil
}

// yes refuses a change nobody said yes to.
func yes(self *xli.Command, what string) error {
	if ok, _ := flg.Find[bool](self, "yes"); !ok {
		return fmt.Errorf("%s: say --yes", what)
	}
	return nil
}

func at(t *time.Time) string {
	if t == nil {
		return "-"
	}
	return t.Local().Format(time.DateOnly)
}

func printWorkspace(self *xli.Command, v admin.Workspace) {
	state := "open"
	if v.Closed != nil {
		state = fmt.Sprintf("closed %s by %s, destroyed %s", at(v.Closed), v.ClosedBy, at(v.Destroy))
	}
	owners := strings.Join(v.Owners, ",")
	if owners == "" {
		owners = "(none)"
	}
	self.Printf("@%s\t%s\t%d people\towners %s\t%s\n", v.Alias, v.Name, v.Members, owners, state)
}

func printDetail(self *xli.Command, v admin.Detail) {
	printWorkspace(self, v.Workspace)
	for _, m := range v.People {
		leaving := ""
		if m.Leaving != nil {
			leaving = "\tleaving since " + at(m.Leaving)
		}
		self.Printf("  member\t@%s\t%s\t%s%s\n", m.Alias, m.Role, m.Name, leaving)
	}
	for _, c := range v.Contracts {
		self.Printf("  contract\t%s\t%s\tview %d keep %d grace %d (days; 0 is all)\n", c.Effective.Local().Format(time.DateOnly), c.Name, c.ViewDays, c.KeepDays, c.GraceDays)
	}
	for _, h := range v.Holds {
		lifted := "held"
		if h.Lifted != nil {
			lifted = "lifted " + at(h.Lifted)
		}
		self.Printf("  hold\t%s\t%s\t%s\n", h.Id, h.Why, lifted)
	}
}

func alias(self *xli.Command) string {
	v, _ := arg.Get[string](self, "WORKSPACE")
	return url.PathEscape(strings.TrimPrefix(strings.TrimSpace(v), "@"))
}

var workspaceArg = &arg.String{Name: "WORKSPACE", Brief: "by its address, @abc1234"}

func newCmdWorkspace() *xli.Command {
	change := func(name, brief, path string, flags flg.Flags, body func(self *xli.Command) any) *xli.Command {
		return &xli.Command{
			Name:  name,
			Brief: brief,
			Flags: append(flags, &flg.Switch{Name: "yes", Brief: "do it"}),
			Args:  arg.Args{workspaceArg},
			Handler: xli.OnRun(func(ctx context.Context, self *xli.Command, next xli.Next) error {
				if err := yes(self, name); err != nil {
					return err
				}
				c, err := clientOf(self)
				if err != nil {
					return err
				}
				var v admin.Detail
				if err := c.do(ctx, http.MethodPost, "/admin/workspaces/"+alias(self)+"/"+path, body(self), &v); err != nil {
					return err
				}
				printDetail(self, v)
				return next(ctx)
			}),
		}
	}
	str := func(self *xli.Command, name string) string {
		v, _ := flg.Find[string](self, name)
		return strings.TrimSpace(v)
	}

	return &xli.Command{
		Name:  "workspace",
		Brief: "find workspaces, close and restore them, move one's address, name an owner",
		Commands: xli.Commands{
			{
				Name:  "ls",
				Brief: "the workspaces, by a piece of their address or name",
				Flags: flg.Flags{
					&flg.String{Name: "q", Brief: "a piece of the address or the name"},
					&flg.Switch{Name: "closed", Brief: "only the closed ones"},
				},
				Handler: xli.OnRun(func(ctx context.Context, self *xli.Command, next xli.Next) error {
					c, err := clientOf(self)
					if err != nil {
						return err
					}
					q := url.Values{}
					if v := str(self, "q"); v != "" {
						q.Set("q", v)
					}
					if closed, _ := flg.Find[bool](self, "closed"); closed {
						q.Set("closed", "1")
					}
					var vs []admin.Workspace
					if err := c.do(ctx, http.MethodGet, "/admin/workspaces?"+q.Encode(), nil, &vs); err != nil {
						return err
					}
					for _, v := range vs {
						printWorkspace(self, v)
					}
					return next(ctx)
				}),
			},
			{
				Name:  "show",
				Brief: "one workspace: who is in it, its contracts and its holds",
				Args:  arg.Args{workspaceArg},
				Handler: xli.OnRun(func(ctx context.Context, self *xli.Command, next xli.Next) error {
					c, err := clientOf(self)
					if err != nil {
						return err
					}
					var v admin.Detail
					if err := c.do(ctx, http.MethodGet, "/admin/workspaces/"+alias(self), nil, &v); err != nil {
						return err
					}
					printDetail(self, v)
					return next(ctx)
				}),
			},
			change("close", "close a workspace as the operator; its owner may restore it within the window", "close",
				flg.Flags{&flg.String{Name: "reason", Brief: "why, for the history"}},
				func(self *xli.Command) any { return map[string]string{"reason": str(self, "reason")} }),
			change("restore", "open a closed workspace again", "restore", nil,
				func(self *xli.Command) any { return map[string]string{} }),
			change("alias", "move a workspace to another address; the old one is held and sends people on", "alias",
				flg.Flags{
					&flg.String{Name: "to", Brief: "the new address"},
					&flg.Switch{Name: "reuse", Brief: "take an address that is held anyway"},
				},
				func(self *xli.Command) any {
					reuse, _ := flg.Find[bool](self, "reuse")
					return map[string]any{"to": str(self, "to"), "reuse": reuse}
				}),
			change("owner", "make a member one of the workspace's owners", "owner",
				flg.Flags{&flg.String{Name: "member", Brief: "the membership, by its @alias in the workspace"}},
				func(self *xli.Command) any { return map[string]string{"member": str(self, "member")} }),
		},
		Handler: xli.RequireSubcommand(),
	}
}

func newCmdContract() *xli.Command {
	return &xli.Command{
		Name:  "contract",
		Brief: "what a workspace's contract says about its history",
		Commands: xli.Commands{
			{
				Name:  "set",
				Brief: "write a workspace's next contract, in force from --effective",
				Flags: flg.Flags{
					&flg.String{Name: "name", Brief: "the plan, as people call it"},
					&flg.Uint32{Name: "view", Brief: "how far back it may look, in days; 0 is all of it"},
					&flg.Uint32{Name: "keep", Brief: "how long its history is kept, in days; 0 is forever"},
					&flg.Uint32{Name: "grace", Brief: "how long a shorter keep waits before it applies, in days"},
					&flg.String{Name: "effective", Brief: "from when, as 2006-01-02 or RFC 3339; now by default"},
					&flg.Switch{Name: "yes", Brief: "do it"},
				},
				Args: arg.Args{workspaceArg},
				Handler: xli.OnRun(func(ctx context.Context, self *xli.Command, next xli.Next) error {
					if err := yes(self, "contract set"); err != nil {
						return err
					}
					c, err := clientOf(self)
					if err != nil {
						return err
					}
					body := map[string]any{}
					if v, _ := flg.Find[string](self, "name"); v != "" {
						body["name"] = v
					}
					for _, k := range []string{"view", "keep", "grace"} {
						v, _ := flg.Find[uint32](self, k)
						body[k+"_days"] = v
					}
					if v, _ := flg.Find[string](self, "effective"); strings.TrimSpace(v) != "" {
						t, err := instant(v)
						if err != nil {
							return fmt.Errorf("--effective: %w", err)
						}
						body["effective"] = t
					}
					var v admin.Detail
					if err := c.do(ctx, http.MethodPost, "/admin/workspaces/"+alias(self)+"/contracts", body, &v); err != nil {
						return err
					}
					printDetail(self, v)
					return next(ctx)
				}),
			},
		},
		Handler: xli.RequireSubcommand(),
	}
}

func newCmdHold() *xli.Command {
	return &xli.Command{
		Name:  "hold",
		Brief: "a legal hold on a workspace's history",
		Commands: xli.Commands{
			{
				Name:  "place",
				Brief: "keep everything of a workspace's history until the hold is lifted",
				Flags: flg.Flags{
					&flg.String{Name: "why", Brief: "what it is for: a case, a demand, an agreement"},
					&flg.String{Name: "desc", Brief: "more about it"},
					&flg.Switch{Name: "yes", Brief: "do it"},
				},
				Args: arg.Args{workspaceArg},
				Handler: xli.OnRun(func(ctx context.Context, self *xli.Command, next xli.Next) error {
					if err := yes(self, "hold place"); err != nil {
						return err
					}
					c, err := clientOf(self)
					if err != nil {
						return err
					}
					why, _ := flg.Find[string](self, "why")
					desc, _ := flg.Find[string](self, "desc")
					var v admin.Detail
					if err := c.do(ctx, http.MethodPost, "/admin/workspaces/"+alias(self)+"/holds", map[string]string{"why": why, "desc": desc}, &v); err != nil {
						return err
					}
					printDetail(self, v)
					return next(ctx)
				}),
			},
			{
				Name:  "lift",
				Brief: "end a hold; the windows apply again from the next pass",
				Flags: flg.Flags{&flg.Switch{Name: "yes", Brief: "do it"}},
				Args:  arg.Args{&arg.String{Name: "ID", Brief: "the hold, as `workspace show` lists it"}},
				Handler: xli.OnRun(func(ctx context.Context, self *xli.Command, next xli.Next) error {
					if err := yes(self, "hold lift"); err != nil {
						return err
					}
					c, err := clientOf(self)
					if err != nil {
						return err
					}
					id, _ := arg.Get[string](self, "ID")
					var v admin.Detail
					if err := c.do(ctx, http.MethodPost, "/admin/holds/"+url.PathEscape(strings.TrimSpace(id))+"/lift", map[string]string{}, &v); err != nil {
						return err
					}
					printDetail(self, v)
					return next(ctx)
				}),
			},
		},
		Handler: xli.RequireSubcommand(),
	}
}

// instant reads a moment as a date or as RFC 3339.
func instant(v string) (time.Time, error) {
	v = strings.TrimSpace(v)
	if t, err := time.ParseInLocation(time.DateOnly, v, time.Local); err == nil {
		return t, nil
	}
	return time.Parse(time.RFC3339, v)
}
