package domain

import (
	"context"

	"github.com/protobuf-orm/ent/dialect/sql"

	app "github.com/lesomnus/rove"
	"github.com/lesomnus/rove/internal/ent/audit"
	"github.com/lesomnus/rove/internal/ent/event"
	"github.com/lesomnus/rove/internal/ent/predicate"
	"github.com/lesomnus/rove/server/retention"
)

type domainEvent struct {
	Domain
	app.EventServiceServer
}

func (s Domain) Event() app.EventServiceServer { return domainEvent{s, s.Next().Event()} }

func pageSize(n uint32) int {
	if n == 0 || n > 500 {
		return 100
	}
	return int(n)
}

// Recent answers the tenant's newest events first.
func (s domainEvent) Recent(ctx context.Context, req *app.EventRecentRequest) (*app.EventRecentResponse, error) {
	t, err := s.read(ctx)
	if err != nil {
		return nil, err
	}
	ps := []predicate.Event{event.TenantId(t.tenant.Uuid())}
	since, err := t.since()
	if err != nil {
		return nil, err
	}
	if !since.IsZero() {
		ps = append(ps, retention.EventIn(since))
	}
	if k := req.GetKind(); k != "" {
		ps = append(ps, event.KindHasPrefix(k))
	}
	if req.HasBefore() {
		ps = append(ps, event.DateCreatedLT(req.GetBefore().AsTime()))
	}
	// An operation over many things once, rather than once for each of them
	// as well: what it did to each is that thing's history (design D34).
	if !req.GetChildren() {
		ps = append(ps, event.CauseIdIsNil())
	}
	vs, err := t.db.Event.Query().Where(ps...).Order(event.ByDateCreated(sql.OrderDesc()), event.ById(sql.OrderDesc())).Limit(pageSize(req.GetSize())).All(ctx)
	if err != nil {
		return nil, err
	}
	out := &app.EventRecentResponse{}
	names := namer{t: t}
	for _, v := range vs {
		e := v.Proto()
		e.SetDesc(names.say(v.Desc, v.Payload))
		out.SetItems(append(out.GetItems(), e))
	}
	return out, nil
}

type domainAudit struct {
	Domain
	app.AuditServiceServer
}

func (s Domain) Audit() app.AuditServiceServer { return domainAudit{s, s.Next().Audit()} }

// Recent answers the newest trail rows a person in this tenant may read: what
// happened to its rows, and what its people did -- the history's within the
// tenant's view window, since the trail of a write holds what it wrote.
func (s domainAudit) Recent(ctx context.Context, req *app.AuditRecentRequest) (*app.AuditRecentResponse, error) {
	t, err := s.read(ctx)
	if err != nil {
		return nil, err
	}
	tn := t.tenant.Uuid()
	ps := []predicate.Audit{audit.Or(audit.TenantId(tn), audit.ActorTenantId(tn))}
	since, err := t.since()
	if err != nil {
		return nil, err
	}
	if !since.IsZero() {
		ps = append(ps, audit.Or(audit.TenantIdNEQ(tn), retention.AuditIn(since)))
	}
	if req.HasBefore() {
		ps = append(ps, audit.DateCreatedLT(req.GetBefore().AsTime()))
	}
	vs, err := t.db.Audit.Query().Where(ps...).Order(audit.ByDateCreated(sql.OrderDesc())).Limit(pageSize(req.GetSize())).All(ctx)
	if err != nil {
		return nil, err
	}
	out := &app.AuditRecentResponse{}
	for _, v := range vs {
		out.SetItems(append(out.GetItems(), v.Proto()))
	}
	return out, nil
}
