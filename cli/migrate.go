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
//
// It opens the databases and nothing else (`cmd.Open`): no files and no trail,
// and the key the trail's rows are digested with only when a step has rows to
// put on the trail (`cmd.Server.Contents`), which a database nothing has
// written to never has. So it runs where a server could not -- before a pod
// serves, with none of the volumes it will serve from mounted and that key
// alone beside it (deploy/k8s).
func NewCmdMigrate(c *cmd.Config) *xli.Command {
	return &xli.Command{
		Name:  "migrate",
		Brief: "bring the database into this build's shape, and serve nothing",
		Handler: xli.OnRun(func(ctx context.Context, self *xli.Command, next xli.Next) error {
			s, err := cmd.Open(ctx, *c)
			if err != nil {
				return err
			}
			defer s.Close()
			v, err := migrate(ctx, s)
			if err != nil {
				return err
			}
			if v.Facts > 0 {
				self.Printf("the assets' facts moved into payday's past: %d rewritten, %d put on the trail\n", v.Facts, v.Recorded)
			}
			self.Printf("the database is in this build's shape\n")
			return nil
		}),
	}
}
