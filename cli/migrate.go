package cli

import (
	"context"

	"github.com/lesomnus/xli"

	"github.com/lesomnus/rove/cmd"
)

// NewCmdMigrate is `rove migrate`: the database brought into the shape this
// build's schema says -- with PostgreSQL's constraints beside it (`pg.sql`)
// -- and nothing served. It is the step a deployment takes before it serves a
// new build, with `db.migrate: false` on what serves, which then refuses a
// database whose shape is not its own rather than altering tables itself
// (`serve`). The roster in this process brings its own database along as it
// opens.
func NewCmdMigrate(c *cmd.Config) *xli.Command {
	return &xli.Command{
		Name:  "migrate",
		Brief: "bring the database into this build's shape, and serve nothing",
		Handler: xli.OnRun(func(ctx context.Context, self *xli.Command, next xli.Next) error {
			s, err := cmd.Build(ctx, *c)
			if err != nil {
				return err
			}
			defer s.Close()
			if err := Migrate(ctx, s); err != nil {
				return err
			}
			self.Printf("the database is in this build's shape\n")
			return nil
		}),
	}
}
