package domain

import (
	"context"

	"github.com/protobuf-orm/ent/dialect/sql"

	app "github.com/lesomnus/rove"
	"github.com/lesomnus/rove/internal/ent/audit"
	"github.com/lesomnus/rove/internal/ent/event"
	"github.com/lesomnus/rove/internal/ent/predicate"
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
	// Each said as it is read (payday#151): a row from before by the words
	// it was written with, read here with it.
	out := &app.EventRecentResponse{}
	names := namer{t: t}
	for _, v := range vs {
		if v.Value == nil {
			names.knows(v.Id, v.Desc, v.Payload)
		}
	}
	r := t.reading(&names)
	for _, v := range vs {
		e := v.Proto()
		e.SetDesc(r.Say(said(v)))
		out.SetItems(append(out.GetItems(), e))
	}
	if err := r.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

type domainAudit struct {
	Domain
	app.AuditServiceServer
}

func (s Domain) Audit() app.AuditServiceServer { return domainAudit{s, s.Next().Audit()} }

// Recent answers the newest trail rows a person in this tenant may read: what
// happened to its rows, and what its people did.
func (s domainAudit) Recent(ctx context.Context, req *app.AuditRecentRequest) (*app.AuditRecentResponse, error) {
	t, err := s.read(ctx)
	if err != nil {
		return nil, err
	}
	tn := t.tenant.Uuid()
	ps := []predicate.Audit{audit.Or(audit.TenantId(tn), audit.ActorTenantId(tn))}
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
