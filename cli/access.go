package cli

import (
	"context"
	"fmt"
	"strings"
	"time"
	"uuid"

	"github.com/lesomnus/payday/pdid"
	"github.com/lesomnus/payday/pdpb"
	"github.com/lesomnus/xli"
	"github.com/lesomnus/xli/flg"
	"google.golang.org/protobuf/proto"

	"github.com/lesomnus/rove/cmd"
	"github.com/lesomnus/rove/internal/ent"
	entaccess "github.com/lesomnus/rove/internal/ent/access"
	"github.com/lesomnus/rove/internal/ent/accessreview"
	"github.com/lesomnus/rove/internal/ent/predicate"
	enttenant "github.com/lesomnus/rove/internal/ent/tenant"
)

// NewCmdAccess is the access record of the whole deployment, for an operator
// (rove#136): who read, wrote or took out whose personal data, from where --
// in a workspace, by a member, of one person, of one kind -- and the reviews
// the workspaces' administrators recorded. A person asking what was done with
// their data is answered with `ls --subject`.
//
// It reads the database the configuration names, prints identifiers and no
// names, and changes nothing. A workspace's own administrators read theirs at
// the page, or with `rove api access review`.
func NewCmdAccess(c *cmd.Config) *xli.Command {
	return &xli.Command{
		Name:  "access",
		Brief: "the access record: who read, wrote or took out whose personal data, and its reviews",

		Commands: xli.Commands{
			{
				Name:  "ls",
				Brief: "the calls that carried somebody's personal data, newest first",
				Flags: flg.Flags{
					&flg.String{Name: "tenant", Brief: "only this workspace's record, by alias"},
					&flg.String{Name: "from", Brief: "from this date or instant (RFC 3339); thirty days ago if left out"},
					&flg.String{Name: "to", Brief: "before this date or instant; now if left out"},
					&flg.String{Name: "actor", Brief: "only the calls of this membership, by id"},
					&flg.String{Name: "subject", Brief: "only the calls that carried this person's data, by the id of their person record or membership"},
					&flg.String{Name: "kind", Brief: "only read, write or download"},
					&flg.Uint32{Name: "limit", Brief: "at most this many; 100 if left out"},
				},
				Handler: xli.OnRun(func(ctx context.Context, self *xli.Command, next xli.Next) error {
					return accessList(ctx, self, c)
				}),
			},
			{
				Name:  "reviews",
				Brief: "the reviews of the record a workspace's administrators recorded, newest first",
				Flags: flg.Flags{
					&flg.String{Name: "tenant", Brief: "only this workspace's, by alias"},
				},
				Handler: xli.OnRun(func(ctx context.Context, self *xli.Command, next xli.Next) error {
					return accessReviews(ctx, self, c)
				}),
			},
		},
	}
}

func accessList(ctx context.Context, self *xli.Command, c *cmd.Config) error {
	now := time.Now()
	from, to := now.Add(-30*24*time.Hour), now
	if v, _ := flg.Find[string](self, "from"); strings.TrimSpace(v) != "" {
		t, err := instant(v)
		if err != nil {
			return fmt.Errorf("--from: %w", err)
		}
		from = t
	}
	if v, _ := flg.Find[string](self, "to"); strings.TrimSpace(v) != "" {
		t, err := instant(v)
		if err != nil {
			return fmt.Errorf("--to: %w", err)
		}
		to = t
	}
	if !to.After(from) {
		return fmt.Errorf("--to: before --from")
	}
	limit, _ := flg.Find[uint32](self, "limit")
	if limit == 0 {
		limit = 100
	}

	s, err := cmd.Build(ctx, *c)
	if err != nil {
		return err
	}
	defer s.Close()

	ps := []predicate.Access{entaccess.DateCreatedGTE(from), entaccess.DateCreatedLT(to)}
	if v, _ := flg.Find[string](self, "tenant"); strings.TrimSpace(v) != "" {
		t, err := tenantNamed(ctx, s.Ent, self)
		if err != nil {
			return err
		}
		ps = append(ps, entaccess.TenantId(t.Id))
	}
	if v, _ := flg.Find[string](self, "actor"); strings.TrimSpace(v) != "" {
		id, err := pdid.Parse(strings.TrimSpace(v))
		if err != nil {
			return fmt.Errorf("--actor: %w", err)
		}
		ps = append(ps, entaccess.ActorId(id.Uuid()))
	}
	switch v, _ := flg.Find[string](self, "kind"); v {
	case "":
	case "read", "write", "download":
		ps = append(ps, entaccess.Kind(v))
	default:
		return fmt.Errorf("--kind: read, write or download")
	}
	var subject []byte
	if v, _ := flg.Find[string](self, "subject"); strings.TrimSpace(v) != "" {
		id, err := pdid.Parse(strings.TrimSpace(v))
		if err != nil {
			return fmt.Errorf("--subject: %w", err)
		}
		subject = id.Bytes()
	}

	aliases := map[uuid.UUID]string{}
	alias := func(id uuid.UUID) string {
		if v, ok := aliases[id]; ok {
			return v
		}
		v := pdid.Id(id).String()
		if t, err := s.Ent.Tenant.Query().Where(enttenant.Id(id)).Only(ctx); err == nil {
			v = t.Alias
		}
		aliases[id] = v
		return v
	}

	// A batch at a time, newest first, until the limit: whose data a call
	// carried is a list, which the database is not asked about.
	shown := uint32(0)
	cursor := []predicate.Access{}
	for shown < limit {
		vs, err := s.Ent.Access.Query().
			Where(append(append([]predicate.Access{}, ps...), cursor...)...).
			Order(ent.Desc(entaccess.FieldDateCreated), ent.Desc(entaccess.FieldId)).
			Limit(500).
			All(ctx)
		if err != nil {
			return err
		}
		for _, v := range vs {
			if subject != nil && !carries(v, subject) {
				continue
			}
			d := &pdpb.AccessDetail{}
			_ = proto.Unmarshal(v.Value, d)
			self.Printf("%s  %s  %-8s %-18s %s  actor=%s  from=%s  subjects=%d%s\n",
				v.DateCreated.UTC().Format(time.RFC3339), alias(v.TenantId), v.Kind, v.Result, v.Action,
				pdid.Id(v.ActorId), or(d.GetAddress(), "-"), v.SubjectCount, clause("  reason=%q", d.GetReason()))
			if shown++; shown == limit {
				break
			}
		}
		if len(vs) < 500 {
			break
		}
		last := vs[len(vs)-1]
		cursor = []predicate.Access{entaccess.Or(
			entaccess.DateCreatedLT(last.DateCreated),
			entaccess.And(entaccess.DateCreatedEQ(last.DateCreated), entaccess.IdLT(last.Id)),
		)}
	}
	if shown == 0 {
		self.Printf("nothing in %s – %s\n", from.UTC().Format(time.RFC3339), to.UTC().Format(time.RFC3339))
	}
	return nil
}

// carries is whether a call carried the data of the person `id` names.
func carries(v *ent.Access, id []byte) bool {
	for _, s := range v.Subjects {
		if string(s) == string(id) {
			return true
		}
	}
	return false
}

func accessReviews(ctx context.Context, self *xli.Command, c *cmd.Config) error {
	s, err := cmd.Build(ctx, *c)
	if err != nil {
		return err
	}
	defer s.Close()

	q := s.Ent.AccessReview.Query().WithTenant().Order(ent.Desc(accessreview.FieldDateCreated))
	if v, _ := flg.Find[string](self, "tenant"); strings.TrimSpace(v) != "" {
		t, err := tenantNamed(ctx, s.Ent, self)
		if err != nil {
			return err
		}
		q = q.Where(accessreview.TenantId(t.Id))
	}
	vs, err := q.All(ctx)
	if err != nil {
		return err
	}
	for _, v := range vs {
		self.Printf("%s  %s  %s – %s  calls=%d downloads=%d refused=%d  by=%s%s\n",
			v.DateCreated.UTC().Format(time.RFC3339), v.Edges.Tenant.Alias,
			v.PeriodFrom.UTC().Format(time.RFC3339), v.PeriodTo.UTC().Format(time.RFC3339),
			v.Calls, v.Downloads, v.Refused, pdid.Id(v.ReviewerId), clause("  %q", v.Desc))
	}
	if len(vs) == 0 {
		self.Printf("no review recorded\n")
	}
	return nil
}
