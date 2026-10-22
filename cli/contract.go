package cli

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/lesomnus/xli"
	"github.com/lesomnus/xli/arg"
	"github.com/lesomnus/xli/flg"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/lesomnus/payday/pdid"

	app "github.com/lesomnus/rove"
	"github.com/lesomnus/rove/cmd"
	"github.com/lesomnus/rove/internal/ent"
	"github.com/lesomnus/rove/internal/ent/legalhold"
	enttenant "github.com/lesomnus/rove/internal/ent/tenant"
	"github.com/lesomnus/rove/internal/ent/tenantcontract"
	"github.com/lesomnus/rove/server/pd"
	"github.com/lesomnus/rove/server/retention"
)

// NewCmdContract is `rove contract`: what the operator's agreement with a
// workspace says of what the deployment owes beyond the workspace's own
// settings -- whether its trail goes whole when it is destroyed -- and what
// those settings and its holds come to now (design 8). How long a workspace
// keeps its history is its own (D36), its owner's to say in its settings.
//
// A command and not an RPC, because the contract is the operator's and no role
// in a tenant may write it. It writes through [cmd.Server.Base], which a
// request cannot reach.
func NewCmdContract(c *cmd.Config) *xli.Command {
	return &xli.Command{
		Name:  "contract",
		Brief: "what the operator's agreement with a workspace says beyond its own settings",

		Commands: xli.Commands{
			newCmdContractSet(c),
			newCmdContractShow(c),
		},
	}
}

func newCmdContractSet(c *cmd.Config) *xli.Command {
	return &xli.Command{
		Name:  "set",
		Brief: "write a tenant's next contract, in force from --effective",

		Flags: flg.Flags{
			&flg.String{Name: "tenant", Brief: "the tenant, by alias"},
			&flg.String{Name: "name", Brief: "the agreement, as people call it: its number, standard"},
			&flg.String{Name: "desc", Brief: "more about it: an agreement's terms, a ticket"},
			&flg.Switch{Name: "purge-floors", Brief: "when it is destroyed, take its whole trail, what the deployment's floors are on too; without this it retires, keeping that until the floors pass"},
			&flg.String{Name: "effective", Brief: "from when, as 2006-01-02 or RFC 3339; now by default"},
		},

		Handler: xli.OnRun(func(ctx context.Context, self *xli.Command, next xli.Next) error {
			at := time.Now()
			if v, _ := flg.Find[string](self, "effective"); strings.TrimSpace(v) != "" {
				t, err := instant(v)
				if err != nil {
					return fmt.Errorf("--effective: %w", err)
				}

				at = t
			}

			name, _ := flg.Find[string](self, "name")
			desc, _ := flg.Find[string](self, "desc")
			purge, _ := flg.Find[bool](self, "purge-floors")

			s, err := cmd.Build(ctx, *c)
			if err != nil {
				return err
			}
			defer s.Close()

			t, err := tenantNamed(ctx, s.Ent, self)
			if err != nil {
				return err
			}

			v, err := s.Base.TenantContract().Add(ctx, app.TenantContractAddRequest_builder{
				Tenant:        app.TenantRef_builder{Id: t.Id[:]}.Build(),
				Name:          strings.TrimSpace(name),
				Desc:          strings.TrimSpace(desc),
				PurgeFloors:   &purge,
				DateEffective: timestamppb.New(at),
			}.Build())
			if err != nil {
				return err
			}

			self.Printf("contract %s for %s, from %s\n", must(pdid.From(v.GetId())), t.Alias, at.UTC().Format(time.RFC3339))

			return showWindow(ctx, self, s.Ent, pdid.Id(t.Id), *c)
		}),
	}
}

func newCmdContractShow(c *cmd.Config) *xli.Command {
	return &xli.Command{
		Name:  "show",
		Brief: "a workspace's contracts, its holds, and the windows its settings come to now",

		Flags: flg.Flags{
			&flg.String{Name: "tenant", Brief: "the tenant, by alias"},
		},

		Handler: xli.OnRun(func(ctx context.Context, self *xli.Command, next xli.Next) error {
			s, err := cmd.Build(ctx, *c)
			if err != nil {
				return err
			}
			defer s.Close()

			t, err := tenantNamed(ctx, s.Ent, self)
			if err != nil {
				return err
			}

			vs, err := s.Ent.TenantContract.Query().
				Where(tenantcontract.TenantIdEQ(t.Id), tenantcontract.DateErasedIsNil()).
				Order(ent.Desc(tenantcontract.FieldDateEffective, tenantcontract.FieldId)).
				All(ctx)
			if err != nil {
				return err
			}
			for _, v := range vs {
				desc := ""
				if v.Desc != "" {
					desc = "  " + v.Desc
				}
				self.Printf("%s  %-12s then %s%s\n",
					v.DateEffective.UTC().Format(time.DateOnly), or(v.Name, "-"), leaving(v.PurgeFloors), desc)
			}
			if len(vs) == 0 {
				self.Printf("no contract: a destroyed workspace retires\n")
			}

			return showWindow(ctx, self, s.Ent, pdid.Id(t.Id), *c)
		}),
	}
}

// NewCmdHold is `rove hold`: a legal hold on a tenant's history, which keeps
// all of it -- rove's history and the trail -- until it is lifted.
func NewCmdHold(c *cmd.Config) *xli.Command {
	return &xli.Command{
		Name:  "hold",
		Brief: "a legal hold on a tenant's history",

		Commands: xli.Commands{
			{
				Name:  "place",
				Brief: "keep everything of a tenant's history until the hold is lifted",

				Flags: flg.Flags{
					&flg.String{Name: "tenant", Brief: "the tenant, by alias"},
					&flg.String{Name: "why", Brief: "what it is for: a case, a demand, an agreement"},
					&flg.String{Name: "desc", Brief: "more about it"},
				},

				Handler: xli.OnRun(func(ctx context.Context, self *xli.Command, next xli.Next) error {
					why, _ := flg.Find[string](self, "why")
					if strings.TrimSpace(why) == "" {
						return errors.New("--why: a hold says what it is for")
					}
					desc, _ := flg.Find[string](self, "desc")

					s, err := cmd.Build(ctx, *c)
					if err != nil {
						return err
					}
					defer s.Close()

					t, err := tenantNamed(ctx, s.Ent, self)
					if err != nil {
						return err
					}

					v, err := s.Base.LegalHold().Add(ctx, app.LegalHoldAddRequest_builder{
						Tenant: app.TenantRef_builder{Id: t.Id[:]}.Build(),
						Name:   strings.TrimSpace(why),
						Desc:   strings.TrimSpace(desc),
					}.Build())
					if err != nil {
						return err
					}

					self.Printf("hold %s on %s\n", must(pdid.From(v.GetId())), t.Alias)

					return nil
				}),
			},
			{
				Name:  "lift",
				Brief: "end a hold; the windows apply again from the next pass",

				Args: arg.Args{
					&arg.String{Name: "ID", Brief: "the hold, as `rove contract show` lists it"},
				},

				Handler: xli.OnRun(func(ctx context.Context, self *xli.Command, next xli.Next) error {
					raw, _ := arg.Get[string](self, "ID")
					id, err := pdid.Parse(strings.TrimSpace(raw))
					if err != nil {
						return fmt.Errorf("ID: %w", err)
					}

					s, err := cmd.Build(ctx, *c)
					if err != nil {
						return err
					}
					defer s.Close()

					if _, err := s.Base.LegalHold().Patch(ctx, app.LegalHoldPatchRequest_builder{
						Ref:        app.LegalHoldRef_builder{Id: id.Bytes()}.Build(),
						DateLifted: timestamppb.Now(),
					}.Build()); err != nil {
						return err
					}

					self.Printf("hold %s lifted\n", id)

					return nil
				}),
			},
		},
	}
}

// showWindow prints what a workspace's settings, contracts and holds come to
// now: how long its history is kept, what the trail keeps of its access record
// -- the floor raising a shorter one -- and how long it may be restored once
// closed; and a window made shorter, until when the longer one holds.
func showWindow(ctx context.Context, self *xli.Command, db *ent.Client, tenant pdid.Id, all cmd.Config) error {
	c := all.App.Retention
	p, err := all.Audit.Policy()
	if err != nil {
		return err
	}
	d := cmd.RetentionDefaults(all, p)

	now := time.Now()
	w, err := retention.Of(ctx, db, tenant, now, d)
	if err != nil {
		return err
	}

	self.Printf("history: kept %s\n", span(w.Keep, "forever"))
	if t, err := db.Tenant.Get(ctx, tenant.Uuid()); err == nil {
		if u := retention.KeepOf(t).Until(now, d.Wait(), retention.Days); !u.IsZero() {
			self.Printf("  until %s, then %s: made shorter, and waiting out the grace\n", u.UTC().Format(time.DateOnly), days(t.KeepDays, "forever"))
		}
		if u := retention.AccessOf(t).Until(now, d.Wait(), d.AccessDays); !u.IsZero() {
			self.Printf("  the access record's until %s, then %s: made shorter, and waiting out the grace\n", u.UTC().Format(time.DateOnly), span(d.AccessDays(t.AccessKeepDays), "forever"))
		}
	}

	answer, err := retention.Trail(db, p, d)(ctx, tenant)
	if err != nil {
		return err
	}
	self.Printf("access record: kept %s\n", span(cmd.LifeOf(p.ForTenant(answer, pd.AccessDomain)), "forever"))
	restore := w.Restore
	if restore <= 0 {
		restore = all.App.Workspaces.Restore()
	}
	self.Printf("restore: %s once closed\n", span(restore, "forever"))
	self.Printf("destroyed: %s\n", map[bool]string{
		false: "retired -- what the deployment's floors are on of its trail stays until they pass",
		true:  "purged -- its whole trail, what the deployment's floors are on too",
	}[w.Purge])

	hs, err := db.LegalHold.Query().
		Where(legalhold.TenantIdEQ(tenant.Uuid()), legalhold.DateLiftedIsNil()).
		Order(ent.Asc(legalhold.FieldDateCreated)).
		All(ctx)
	if err != nil {
		return err
	}
	for _, h := range hs {
		self.Printf("held:  %s since %s  %s\n", pdid.Id(h.Id), h.DateCreated.UTC().Format(time.DateOnly), h.Name)
	}
	if !c.Apply && (w.Keep > 0 || shorter(w.Access, d.Access)) {
		self.Printf("(app.retention.apply is off, so nothing is destroyed yet)\n")
	}

	return nil
}

// tenantNamed is the tenant `--tenant` names, by its alias.
func tenantNamed(ctx context.Context, db *ent.Client, self *xli.Command) (*ent.Tenant, error) {
	alias, _ := flg.Find[string](self, "tenant")
	alias = strings.TrimSpace(alias)
	if alias == "" {
		return nil, errors.New("--tenant: which tenant, by alias")
	}

	t, err := db.Tenant.Query().Where(enttenant.AliasEQ(alias)).Only(ctx)
	if ent.IsNotFound(err) {
		return nil, fmt.Errorf("--tenant: no tenant is called %q", alias)
	}

	return t, err
}

// instant reads a date, or an instant in RFC 3339.
func instant(v string) (time.Time, error) {
	v = strings.TrimSpace(v)
	if t, err := time.Parse(time.DateOnly, v); err == nil {
		return t, nil
	}

	return time.Parse(time.RFC3339, v)
}

// leaving is what a contract says of the trail when the workspace is
// destroyed.
func leaving(purge bool) string {
	if purge {
		return "purge"
	}

	return "retire"
}

func days(n uint32, zero string) string {
	if n == 0 {
		return zero
	}

	return fmt.Sprintf("%dd", n)
}

func span(d time.Duration, zero string) string {
	if d <= 0 {
		return zero
	}

	return fmt.Sprintf("%dd", int(d/(24*time.Hour)))
}

// shorter is whether window a ends before b, where zero is forever.
func shorter(a, b time.Duration) bool {
	return a > 0 && (b <= 0 || a < b)
}

func or(v, otherwise string) string {
	if v == "" {
		return otherwise
	}

	return v
}

func must[T any](v T, err error) T {
	if err != nil {
		panic(err)
	}

	return v
}
