package domain

import (
	"context"
	"encoding/base64"
	"fmt"
	"strconv"
	"strings"
	"time"
	"uuid"

	"github.com/protobuf-orm/ent/dialect/sql"

	app "github.com/lesomnus/loft"
	"github.com/lesomnus/loft/internal/ent/audit"
	"github.com/lesomnus/loft/internal/ent/event"
	"github.com/lesomnus/loft/internal/ent/predicate"
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

// before is the rows a list newest first reads after the row at `at`, `id` --
// of the access record, the trail or the events, each made at `date_created`
// and keyed `id`: `(date_created, id) < (a, b)`, the pair compared once, which
// an index starts its read at. The OR it means is a filter, behind which every
// page was read from the newest row down to where the one before had ended
// (payday#154).
//
// The time alone is not a row: an operation records all of its events at one
// instant -- a bulk registration one for every asset it made -- and the
// trail's lines, stamped to the microsecond, can share one. A page that ended
// inside an instant went on past the rest of it.
func before(at time.Time, id uuid.UUID) func(*sql.Selector) {
	return func(s *sql.Selector) {
		s.Where(sql.CompositeLT([]string{s.C("date_created"), s.C("id")}, at, id))
	}
}

// afterFor is where a page that ends at the row made at `at` keyed `id` ends,
// for the next to begin after: its time and its identifier, which together
// are one row.
func afterFor(at time.Time, id uuid.UUID) string {
	return base64.RawURLEncoding.EncodeToString(fmt.Appendf(nil, "%d~%s", at.UnixNano(), id))
}

func afterOf(v string) (time.Time, uuid.UUID, error) {
	b, err := base64.RawURLEncoding.DecodeString(v)
	if err != nil {
		return time.Time{}, uuid.UUID{}, err
	}
	at, id, ok := strings.Cut(string(b), "~")
	if !ok {
		return time.Time{}, uuid.UUID{}, fmt.Errorf("no row")
	}
	ns, err := strconv.ParseInt(at, 10, 64)
	if err != nil {
		return time.Time{}, uuid.UUID{}, err
	}
	u, err := uuid.Parse(id)
	if err != nil {
		return time.Time{}, uuid.UUID{}, err
	}
	return time.Unix(0, ns), u, nil
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
	// Where the page begins: after the row the one before ended at.
	if v := req.GetAfter(); v != "" {
		at, id, err := afterOf(v)
		if err != nil {
			return nil, invalid("after", "페이지 표시가 올바르지 않습니다")
		}
		ps = append(ps, before(at, id))
	}
	// An operation over many things once, rather than once for each of them
	// as well: what it did to each is that thing's history (design D34).
	if !req.GetChildren() {
		ps = append(ps, event.CauseIdIsNil())
	}
	// One more than a page, which is whether there is a page after it.
	size := pageSize(req.GetSize())
	vs, err := t.db.Event.Query().Where(ps...).Order(event.ByDateCreated(sql.OrderDesc()), event.ById(sql.OrderDesc())).Limit(size + 1).All(ctx)
	if err != nil {
		return nil, err
	}
	out := &app.EventRecentResponse{}
	if len(vs) > size {
		vs = vs[:size]
		out.SetNext(afterFor(vs[size-1].DateCreated, vs[size-1].Id))
	}
	// Each said as it is read (payday#151): a row from before by the words
	// it was written with, read here with it.
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
	if v := req.GetAfter(); v != "" {
		at, id, err := afterOf(v)
		if err != nil {
			return nil, invalid("after", "페이지 표시가 올바르지 않습니다")
		}
		ps = append(ps, before(at, id))
	}
	size := pageSize(req.GetSize())
	vs, err := t.db.Audit.Query().Where(ps...).Order(audit.ByDateCreated(sql.OrderDesc()), audit.ById(sql.OrderDesc())).Limit(size + 1).All(ctx)
	if err != nil {
		return nil, err
	}
	out := &app.AuditRecentResponse{}
	if len(vs) > size {
		vs = vs[:size]
		out.SetNext(afterFor(vs[size-1].DateCreated, vs[size-1].Id))
	}
	for _, v := range vs {
		out.SetItems(append(out.GetItems(), v.Proto()))
	}
	return out, nil
}
