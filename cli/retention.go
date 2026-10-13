package cli

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/lesomnus/payday/pdid"
	"github.com/lesomnus/xli"
	"github.com/lesomnus/xli/flg"

	"github.com/lesomnus/rove/cmd"
	"github.com/lesomnus/rove/internal/ent"
	enttenant "github.com/lesomnus/rove/internal/ent/tenant"
	"github.com/lesomnus/rove/server/domain"
	"github.com/lesomnus/rove/server/pd"
	"github.com/lesomnus/rove/server/retention"
)

// NewCmdRetention is `rove retention`: what the keep windows take out of the
// history (design 8.2, 8.3).
//
// `plan` is the dry run design 8.3 asks for before anything: it does all of
// the work in a transaction it then undoes, so what it says is what a run
// would take, to the row -- and, beside it, what the trail's pass would take
// of the trail. `run` takes it, and only when `app.retention.apply` says the
// deployment may; it is what `serve` does every `app.retention.every`.
func NewCmdRetention(c *cmd.Config) *xli.Command {
	tenant := func() flg.Flags {
		return flg.Flags{&flg.String{Name: "tenant", Brief: "one tenant, by alias; every tenant by default"}}
	}

	return &xli.Command{
		Name:  "retention",
		Brief: "what the keep windows take out of the history",

		Commands: xli.Commands{
			{
				Name:  "plan",
				Brief: "what a pass would take now, changing nothing",
				Flags: tenant(),
				Handler: xli.OnRun(func(ctx context.Context, self *xli.Command, next xli.Next) error {
					return expire(ctx, self, c, true)
				}),
			},
			{
				Name:  "run",
				Brief: "take it, when app.retention.apply says the deployment may",
				Flags: tenant(),
				Handler: xli.OnRun(func(ctx context.Context, self *xli.Command, next xli.Next) error {
					if !c.App.Retention.Apply {
						return errors.New("app.retention.apply is off: nothing may be destroyed. `rove retention plan` says what would be")
					}
					return expire(ctx, self, c, false)
				}),
			},
		},
	}
}

func expire(ctx context.Context, self *xli.Command, c *cmd.Config, dry bool) error {
	s, err := cmd.Build(ctx, *c)
	if err != nil {
		return err
	}
	defer s.Close()

	ts, err := tenantsNamed(ctx, s.Ent, self)
	if err != nil {
		return err
	}

	// The trail's policy as `serve` would run it with the windows on.
	p, err := c.Audit.Policy()
	if err != nil {
		return err
	}
	p.Tenants = retention.Trail(s.Ent, p, c.App.Retention.Defaults())
	store := pd.TrailStore(s.Ent)

	for _, t := range ts {
		x, err := s.Deps.Expire(ctx, s.Base, s.Drv, pdid.Id(t.Id), dry)
		if err != nil {
			return fmt.Errorf("%s: %w", t.Alias, err)
		}

		switch {
		case x.Before.IsZero():
			self.Printf("%s: kept forever\n", t.Alias)
			continue
		case len(x.Held) > 0:
			self.Printf("%s: held (%s), nothing goes\n", t.Alias, strings.Join(x.Held, "; "))
			continue
		}

		verb := "took"
		if dry {
			verb = "would take"
		}
		self.Printf("%s: what was over before %s\n", t.Alias, x.Before.UTC().Format(time.DateOnly))
		self.Printf("  history %s %d: %s\n", verb, x.Total(), counts(x))
		if len(x.Lost) > 0 {
			self.Printf("  files that could not be removed: %s\n", strings.Join(x.Lost, ", "))
		}

		if !dry {
			continue
		}
		answer, err := p.Tenants(ctx, pdid.Id(t.Id))
		if err != nil {
			return fmt.Errorf("%s: trail: %w", t.Alias, err)
		}
		rs, err := p.Preview(ctx, store, pdid.Id(t.Id), answer)
		if err != nil {
			return fmt.Errorf("%s: trail: %w", t.Alias, err)
		}
		parts := []string{}
		for _, k := range slices.Sorted(maps.Keys(rs)) {
			r := rs[k]
			parts = append(parts, fmt.Sprintf("%s %d archived, %d discarded, %d destroyed", k, r.Archived, r.Discarded, r.Destroyed))
		}
		if len(parts) == 0 {
			parts = append(parts, "nothing")
		}
		self.Printf("  trail would take: %s\n", strings.Join(parts, "; "))
	}

	if !dry && p.On() {
		// The trail goes on the same windows, in the pass `serve` runs.
		p.Pass(ctx, store)
	}
	if dry && !c.App.Retention.Apply {
		self.Printf("(app.retention.apply is off: `serve` takes none of this, and `run` refuses to)\n")
	}

	return nil
}

func counts(x domain.Expired) string {
	return fmt.Sprintf("%d placements, %d links, %d stewardships, %d facts, %d events, "+
		"%d reservations, %d loans, %d counts, %d work orders, %d stock movements, %d attachments",
		x.Placements, x.Links, x.Stewardships, x.Facts, x.Events,
		x.Reservations, x.Custodies, x.Counts, x.WorkOrders, x.StockMovements, x.Attachments)
}

// tenantsNamed is the tenant `--tenant` names, or every tenant.
func tenantsNamed(ctx context.Context, db *ent.Client, self *xli.Command) ([]*ent.Tenant, error) {
	if v, _ := flg.Find[string](self, "tenant"); strings.TrimSpace(v) != "" {
		t, err := tenantNamed(ctx, db, self)
		if err != nil {
			return nil, err
		}
		return []*ent.Tenant{t}, nil
	}

	return db.Tenant.Query().Order(ent.Asc(enttenant.FieldAlias)).All(ctx)
}
