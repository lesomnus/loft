package domain

import (
	"context"
	"encoding/binary"
	"strings"

	"github.com/lesomnus/payday/past"
	"github.com/lesomnus/payday/pdid"
	"github.com/protobuf-orm/ent/dialect"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	app "github.com/lesomnus/rove"
	"github.com/lesomnus/rove/server/pd"
)

// An event's reason is the one free text its history keeps, in the words of
// whoever asked, and nothing can tell whether somebody is named in it
// (payday#151). So when a person asks for what it says of them to be erased
// and the request points at an event, the reason is taken out:
// `past.Redact`, beneath the servers, on the trail as the act it is. The rest
// of the event stays, and says what was done as it did.
//
// It is an operation like any other here, with an event of its own --
// `event.redact`, its subject the redacted event's, so that the redaction is
// in the history it was taken out of -- whose reason is the request's
// reference: the workspace's record of what it was asked and did. Who may ask
// for it over the API is the policy's to say, and no role is given it yet;
// the deployment's operator does it with `rove event redact`, on the word of
// the workspace that keeps the event ([Deps.RedactEvent]).

// Redact takes the reason out of the event `ref` names, for the request
// `reason` refers to, and answers the event as it is now.
func (s domainEvent) Redact(ctx context.Context, req *app.EventRedactRequest) (*app.Event, error) {
	var out *app.Event
	err := s.tx(ctx, func(t *Tx) error {
		var err error
		out, _, err = t.redact(req.GetRef(), req.GetReason())
		return err
	})
	return out, err
}

// RedactEvent is [domainEvent.Redact] as the deployment's own work in a
// tenant: an operator's, on the instruction of the workspace the event is
// its. `server` is the ungated stack. It answers the event as it is now, and
// the act's own event.
func (d *Deps) RedactEvent(ctx context.Context, server app.Server, drv dialect.Driver, tenant, event pdid.Id, reason string) (*app.Event, pdid.Id, error) {
	var (
		out *app.Event
		act pdid.Id
	)
	err := d.System(ctx, server, drv, tenant, func(t *Tx) error {
		var err error
		out, act, err = t.redact(app.EventRef_builder{Id: event.Bytes()}.Build(), reason)
		return err
	})
	return out, act, err
}

// redact is the act: the event read through the wall -- which puts it on the
// access record of the call, as every read of a reason is -- its own event
// recorded, and the reason taken out.
func (t *Tx) redact(ref *app.EventRef, reason string) (*app.Event, pdid.Id, error) {
	why := strings.TrimSpace(reason)
	if why == "" {
		return nil, pdid.Nil, invalid("reason", "사유를 적어 주세요: 이 삭제가 답하는 요청을 가리키는 것")
	}
	id := idOf(ref.GetId())
	if id.IsZero() || id.Domain() != pd.EventDomain {
		return nil, pdid.Nil, invalid("ref", "사유를 지울 작업 기록을 지정하세요")
	}

	// One taking at a time of an event, before it is read: two at once would
	// each find its reason there, and each record that it took it out. On
	// PostgreSQL the second waits here, and is recorded after the first, which
	// it then finds has left nothing; SQLite has one writer anyway.
	if t.postgres() {
		u := id.Uuid()
		if err := t.drv.Exec(t.ctx, "SELECT pg_advisory_xact_lock($1)", []any{int64(binary.BigEndian.Uint64(u[8:]))}, nil); err != nil {
			return nil, pdid.Nil, err
		}
		t.holds()
	}

	vs, err := past.Events(t.ctx, t.past, past.EventQuery{Ids: []pdid.Id{id}})
	if err != nil {
		return nil, pdid.Nil, err
	}
	if len(vs) == 0 {
		return nil, pdid.Nil, status.Error(codes.NotFound, "작업 기록을 찾을 수 없습니다")
	}
	ev := vs[0]
	if ev.Reason == "" {
		return nil, pdid.Nil, failed("사유가 없는 작업 기록입니다")
	}

	if err := t.begin(nil, "event.redact", ev.Subject, t.now, why, app.EventRedacted_builder{Event: id.Bytes()}.Build()); err != nil {
		return nil, pdid.Nil, err
	}
	if err := past.Redact(t.ctx, t.past, id); err != nil {
		return nil, pdid.Nil, err
	}

	out, err := t.next.Event().Get(t.ctx, app.EventGetRequest_builder{Ref: app.EventRef_builder{Id: id.Bytes()}.Build()}.Build())
	if err != nil {
		return nil, pdid.Nil, err
	}
	return out, t.ev(), nil
}
