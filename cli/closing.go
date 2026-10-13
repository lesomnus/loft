package cli

import (
	"context"
	"strings"
	"time"

	"github.com/lesomnus/payday/pdid"
	"github.com/lesomnus/xli"
	"github.com/lesomnus/xli/flg"

	"github.com/lesomnus/rove/cmd"
	"github.com/lesomnus/rove/server/domain"
)

// The operator's half of a workspace closing (D29): what an owner can do from
// the app, done for them -- unpaid, a breach of the terms, a request by mail
// -- and the one thing nobody in a workspace may do, which is move it to
// another address.

func newCmdTenantClose(c *cmd.Config) *xli.Command {
	return &xli.Command{
		Name:  "close",
		Brief: "close a workspace: nobody but its owner gets in, and it is destroyed once the restore window is over",
		Flags: flg.Flags{
			&flg.String{Name: "tenant", Brief: "the workspace, by alias"},
			&flg.String{Name: "reason", Brief: "why, in words; the workspace's history keeps it"},
			&flg.Switch{Name: "yes", Brief: "do it; without this it only says what it would do"},
		},
		Handler: xli.OnRun(func(ctx context.Context, self *xli.Command, next xli.Next) error {
			yes, _ := flg.Find[bool](self, "yes")
			s, err := cmd.Build(ctx, *c)
			if err != nil {
				return err
			}
			defer s.Close()
			t, err := tenantNamed(ctx, s.Ent, self)
			if err != nil {
				return err
			}
			if t.DateClosed != nil {
				self.Printf("@%s is closed already, since %s; destroyed after %s\n", t.Alias, t.DateClosed.Format(time.DateOnly), when(t.DateDestroy))
				return next(ctx)
			}
			if !yes {
				self.Printf("would close @%s; it is destroyed after %s unless restored\n", t.Alias, time.Now().Add(c.App.Workspaces.Restore()).Format(time.DateOnly))
				self.Printf("(nothing was done: `--yes` does it)\n")
				return next(ctx)
			}
			v, err := s.Deps.CloseWorkspace(ctx, s.Base, s.Drv, pdid.Id(t.Id), domain.ClosedByOperator, strings.TrimSpace(flag(self, "reason")))
			if err != nil {
				return err
			}
			self.Printf("closed @%s; destroyed after %s unless restored\n", t.Alias, v.GetDateDestroy().AsTime().Format(time.DateOnly))
			return next(ctx)
		}),
	}
}

func newCmdTenantRestore(c *cmd.Config) *xli.Command {
	return &xli.Command{
		Name:  "restore",
		Brief: "open a closed workspace again, while its restore window lasts",
		Flags: flg.Flags{
			&flg.String{Name: "tenant", Brief: "the workspace, by alias"},
			&flg.Switch{Name: "yes", Brief: "do it; without this it only says what it would do"},
		},
		Handler: xli.OnRun(func(ctx context.Context, self *xli.Command, next xli.Next) error {
			yes, _ := flg.Find[bool](self, "yes")
			s, err := cmd.Build(ctx, *c)
			if err != nil {
				return err
			}
			defer s.Close()
			t, err := tenantNamed(ctx, s.Ent, self)
			if err != nil {
				return err
			}
			if !yes {
				self.Printf("would restore @%s (closed %s by %s)\n", t.Alias, when(t.DateClosed), or(t.ClosedBy, "nobody"))
				self.Printf("(nothing was done: `--yes` does it)\n")
				return next(ctx)
			}
			if _, err := s.Deps.RestoreWorkspace(ctx, s.Base, s.Drv, pdid.Id(t.Id)); err != nil {
				return err
			}
			self.Printf("restored @%s\n", t.Alias)
			return next(ctx)
		}),
	}
}

func newCmdTenantAlias(c *cmd.Config) *xli.Command {
	return &xli.Command{
		Name:  "alias",
		Brief: "move a workspace to another address; the old one is held and sends people on",
		Flags: flg.Flags{
			&flg.String{Name: "tenant", Brief: "the workspace, by alias"},
			&flg.String{Name: "to", Brief: "the new address"},
			&flg.Switch{Name: "reuse", Brief: "take an address that is held anyway"},
			&flg.Switch{Name: "yes", Brief: "do it; without this it only says what it would do"},
		},
		Handler: xli.OnRun(func(ctx context.Context, self *xli.Command, next xli.Next) error {
			yes, _ := flg.Find[bool](self, "yes")
			reuse, _ := flg.Find[bool](self, "reuse")
			s, err := cmd.Build(ctx, *c)
			if err != nil {
				return err
			}
			defer s.Close()
			t, err := tenantNamed(ctx, s.Ent, self)
			if err != nil {
				return err
			}
			to := strings.ToLower(strings.TrimSpace(strings.TrimPrefix(flag(self, "to"), "@")))
			if err := s.Aliases(*c).Valid(to); err != nil {
				return err
			}
			if !yes {
				self.Printf("would move @%s to @%s, holding @%s until %s\n", t.Alias, to, t.Alias, time.Now().Add(c.App.Workspaces.Hold()).Format(time.DateOnly))
				self.Printf("(nothing was done: `--yes` does it)\n")
				return next(ctx)
			}
			if err := s.Closing.Rename(ctx, t.Alias, to, reuse); err != nil {
				return err
			}
			self.Printf("moved @%s to @%s; @%s sends people on until %s\n", t.Alias, to, t.Alias, time.Now().Add(c.App.Workspaces.Hold()).Format(time.DateOnly))
			return next(ctx)
		}),
	}
}

func when(t *time.Time) string {
	if t == nil {
		return "never"
	}
	return t.Format(time.DateOnly)
}
