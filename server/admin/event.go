package admin

import (
	"bytes"
	"context"
	"slices"
	"uuid"

	"github.com/lesomnus/payday/pdid"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"

	app "github.com/lesomnus/loft"
	"github.com/lesomnus/loft/internal/ent"
	entevent "github.com/lesomnus/loft/internal/ent/event"
	entnotification "github.com/lesomnus/loft/internal/ent/notification"
	"github.com/lesomnus/loft/server/domain"
	"github.com/lesomnus/loft/server/operator"
	"github.com/lesomnus/loft/server/pd"
)

// redactKind is the kind of the act that takes an event's reason out, and of
// each of its parts (`proto/loft/payday/past.lock`).
const redactKind = "event.redact"

// eventOf is the event an identifier names, refused when it names none.
func eventOf(v []byte) (pdid.Id, error) {
	id, err := pdid.From(v)
	if err != nil || id.Domain() != pd.EventDomain {
		return pdid.Nil, status.Error(codes.InvalidArgument, "event: no event's identifier")
	}
	return id, nil
}

// events is the entity's `EventService` at the entry point, of which it
// serves the one method an operator is given and no role of a workspace is
// yet (`server/policy`): Redact.
type events struct {
	app.UnimplementedEventServiceServer
	a Routes
}

// Redact takes the reason out of an event on the word of the workspace it is
// of, for a person whose request to erase what it says of them points at it
// (payday#151): `Deps.RedactEvent`, the deployment's own work in that
// workspace done as the operator. The act's event and the trail's lines name
// them, and the access record says what it read and wrote, by them, with
// neither reason in it -- the one taken out, nor the request's reference.
func (s events) Redact(ctx context.Context, req *app.EventRedactRequest) (*app.Event, error) {
	id, err := eventOf(req.GetRef().GetId())
	if err != nil {
		return nil, err
	}
	ev, err := s.a.S.Ent.Event.Query().
		Where(entevent.Id(id.Uuid())).
		Select(entevent.FieldId, entevent.FieldTenantId).
		Only(ctx)
	if ent.IsNotFound(err) {
		return nil, status.Errorf(codes.NotFound, "event: there is no event %s", id)
	}
	if err != nil {
		return nil, err
	}
	v, err := s.a.S.Deps.RedactEvent(ctx, s.a.S.Base, s.a.S.Drv, pdid.Id(ev.TenantId), id, req.GetReason())
	if err != nil {
		return nil, err
	}
	return v.Event, nil
}

// operatorEvents is `OperatorEventService`: a workspace's history as the
// operator acts on it.
type operatorEvents struct {
	app.UnimplementedOperatorEventServiceServer
	a Routes
}

// Redaction is what taking an event's reason out would take, said before it
// is asked to: the event, without what it says; whether it says anything; the
// other events of its operation that say the same (design D49), the notices
// that say it as their words (D51) and the rows that keep it as their own
// (D56), each by kind and found without reading what they say
// ([domain.Copies], [domain.Told], [domain.Kept]); and what took its reason
// out before. It changes nothing, and reads no one's words.
func (s operatorEvents) Redaction(ctx context.Context, req *app.OperatorEventRedactionRequest) (*app.OperatorEventRedactionResponse, error) {
	id, err := eventOf(req.GetEvent())
	if err != nil {
		return nil, err
	}
	q := s.a.S.Ent.Event.Query().Where(entevent.Id(id.Uuid()))
	var t *ent.Tenant
	if req.HasTenant() {
		if t, err = s.a.workspace(ctx, req.GetTenant()); err != nil {
			return nil, err
		}
		q = q.Where(entevent.TenantId(t.Id))
	}
	ev, err := q.Select(
		entevent.FieldId, entevent.FieldTenantId, entevent.FieldKind,
		entevent.FieldOccurredAt, entevent.FieldDateCreated, entevent.FieldSubjectId,
	).Only(ctx)
	switch {
	case ent.IsNotFound(err) && t != nil:
		return nil, status.Errorf(codes.NotFound, "event: @%s has no event %s", t.Alias, id)
	case ent.IsNotFound(err):
		return nil, status.Errorf(codes.NotFound, "event: there is no event %s", id)
	case err != nil:
		return nil, err
	}
	if t == nil {
		if t, err = s.a.S.Ent.Tenant.Get(ctx, ev.TenantId); err != nil {
			return nil, err
		}
	}
	tenant := pdid.Id(t.Id)

	// Whether there is a reason, and not the reason, which is somebody's
	// words: the database is asked.
	said, err := s.a.S.Ent.Event.Query().Where(entevent.Id(id.Uuid()), entevent.TenantId(t.Id), entevent.ReasonNEQ("")).Exist(ctx)
	if err != nil {
		return nil, err
	}
	out := app.OperatorEventRedactionResponse_builder{
		Event:    id.Bytes(),
		Tenant:   t.Alias,
		Kind:     ev.Kind,
		Occurred: timestamppb.New(ev.OccurredAt),
		Recorded: timestamppb.New(ev.DateCreated),
		Said:     said,
	}.Build()
	if ev.SubjectId != nil {
		out.SetSubject(pdid.Id(*ev.SubjectId).Bytes())
	}
	acts, err := s.acts(ctx, ev, id)
	if err != nil {
		return nil, err
	}
	out.SetActs(acts)
	if !said {
		return out, nil
	}

	copies, err := domain.Copies(ctx, s.a.S.Ent, tenant, id)
	if err != nil {
		return nil, err
	}
	kinds, err := tally(copies, func(us []uuid.UUID) ([]string, error) {
		return s.a.S.Ent.Event.Query().Where(entevent.IdIn(us...)).Select(entevent.FieldKind).Strings(ctx)
	})
	if err != nil {
		return nil, err
	}
	out.SetCopies(kinds)

	told, err := domain.Told(ctx, s.a.S.Ent, tenant, id, copies)
	if err != nil {
		return nil, err
	}
	kinds, err = tally(told, func(us []uuid.UUID) ([]string, error) {
		return s.a.S.Ent.Notification.Query().Where(entnotification.IdIn(us...)).Select(entnotification.FieldKind).Strings(ctx)
	})
	if err != nil {
		return nil, err
	}
	out.SetTold(kinds)
	// And how many of those went by mail, which is not taken back.
	for page := range slices.Chunk(told, 500) {
		us := make([]uuid.UUID, len(page))
		for i, v := range page {
			us[i] = v.Uuid()
		}
		n, err := s.a.S.Ent.Notification.Query().Where(entnotification.IdIn(us...), entnotification.DateMailedNotNil()).Count(ctx)
		if err != nil {
			return nil, err
		}
		out.SetMailed(out.GetMailed() + uint32(n))
	}

	kept, err := domain.Kept(ctx, s.a.S.Ent, tenant, id, copies)
	if err != nil {
		return nil, err
	}
	kinds, err = tally(kept, func(us []uuid.UUID) ([]string, error) {
		vs := make([]string, len(us))
		for i, u := range us {
			vs[i] = kindOf(pdid.Id(u).Domain())
		}
		return vs, nil
	})
	if err != nil {
		return nil, err
	}
	out.SetKept(kinds)
	return out, nil
}

// acts is what took the reason of `ev` out before, the newest first: each
// event of the act's kind about what `ev` is about whose message names it --
// the act asked of it, or the part of one asked of another event of its
// operation. Read without anybody's words: an act's reason is the request's
// reference, and not asked for.
func (s operatorEvents) acts(ctx context.Context, ev *ent.Event, id pdid.Id) ([]*app.OperatorEventRedactionAct, error) {
	q := s.a.S.Ent.Event.Query().Where(entevent.TenantId(ev.TenantId), entevent.Kind(redactKind))
	if ev.SubjectId != nil {
		q = q.Where(entevent.SubjectId(*ev.SubjectId))
	} else {
		q = q.Where(entevent.SubjectIdIsNil())
	}
	vs, err := q.
		Select(entevent.FieldId, entevent.FieldValue, entevent.FieldDateCreated, entevent.FieldActorId).
		Order(ent.Desc(entevent.FieldDateCreated), ent.Desc(entevent.FieldId)).
		All(ctx)
	if err != nil {
		return nil, err
	}
	out := []*app.OperatorEventRedactionAct{}
	for _, v := range vs {
		m := &app.EventRedacted{}
		if v.Value == nil || v.Value.UnmarshalTo(m) != nil || !bytes.Equal(m.GetEvent(), id.Bytes()) {
			continue
		}
		a := app.OperatorEventRedactionAct_builder{Event: pdid.Id(v.Id).Bytes(), Recorded: timestamppb.New(v.DateCreated)}.Build()
		if v.ActorId != nil {
			who := pdid.Id(*v.ActorId)
			a.SetActor(who.Bytes())
			a.SetOperator(s.a.operatorNamed(who))
		}
		out = append(out, a)
	}
	return out, nil
}

// tally is how many of `ids` are of each kind, which `kinds` answers of each
// page of them.
func tally(ids []pdid.Id, kinds func([]uuid.UUID) ([]string, error)) (map[string]uint32, error) {
	out := map[string]uint32{}
	for page := range slices.Chunk(ids, 500) {
		us := make([]uuid.UUID, len(page))
		for i, v := range page {
			us[i] = v.Uuid()
		}
		vs, err := kinds(us)
		if err != nil {
			return nil, err
		}
		for _, v := range vs {
			out[v]++
		}
	}
	return out, nil
}

// operatorNamed is the name of the operator `id` is, of those this entry
// point holds a key for: empty for anybody else, and for an operator whose
// key it no longer holds.
func (a Routes) operatorNamed(id pdid.Id) string {
	if !operator.Is(id) {
		return ""
	}
	for _, o := range a.Operators {
		if operator.Id(o.Name) == id {
			return o.Name
		}
	}
	return ""
}
