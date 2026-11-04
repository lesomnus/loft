package domain

import (
	"context"
	"encoding/binary"
	"fmt"
	"maps"
	"slices"
	"strings"
	"uuid"

	"github.com/lesomnus/payday/past"
	"github.com/lesomnus/payday/pdid"
	"github.com/lesomnus/z"
	"github.com/protobuf-orm/ent/dialect"
	"github.com/protobuf-orm/ent/dialect/sql"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/reflect/protoreflect"

	app "github.com/lesomnus/loft"
	"github.com/lesomnus/loft/internal/ent"
	"github.com/lesomnus/loft/internal/ent/accessreview"
	"github.com/lesomnus/loft/internal/ent/countfinding"
	"github.com/lesomnus/loft/internal/ent/custody"
	"github.com/lesomnus/loft/internal/ent/event"
	"github.com/lesomnus/loft/internal/ent/notification"
	"github.com/lesomnus/loft/internal/ent/predicate"
	enttenant "github.com/lesomnus/loft/internal/ent/tenant"
	"github.com/lesomnus/loft/server/pd"
)

// An event's reason is the one free text its history keeps, in the words of
// whoever asked, and nothing can tell whether somebody is named in it
// (payday#151). So when a person asks for what it says of them to be erased
// and the request points at an event, the reason is taken out:
// `past.Redact`, beneath the servers, on the trail as the act it is. The rest
// of the event stays, and says what was done as it did.
//
// The words are not in that event alone. An operation over many things copies
// its reason into each of its parts -- what a booking did to each thing it
// booked, each time a series takes place, each thing handed over or
// registered at once, what an operation it called did as part of it -- so
// that each thing's history says why (design D34, D49). So they are taken out
// of every event of the operation the event is part of that says them: the
// one they were copied from, and every part that copied them. Which those are
// is asked of the causes the events name and of the database, which compares
// two reasons where they are kept, so that no one's words are read to find
// them ([Copies]).
//
// Nor are they in events alone. A notice may say them as its own words --
// whoever asked for a reservation is told why it was decided -- and so they
// are taken out of every notice that says what one of those events said,
// which nothing links to it but what it is about and the kind it was told in
// ([ToldWhy]): found as the copies are, the database comparing its words
// with the event's ([Told]), and written through the servers, so that the
// trail and the access record say the act wrote it. A notice that went by
// mail before was sent where nothing Loft does reaches, and says it there
// still (design D51).
//
// Nor in notices alone. Some rows keep the reason of the event that wrote
// them as their own words -- what a hand-over was for is its memo, a scan's
// note is the finding it left, what a reviewer found is their review, why a
// workspace was closed is the workspace's -- each declared personal, as the
// reason is, so that the trail never held them. They are taken out of the
// row the event wrote ([KeptWhy]), found as the notices are, the database
// comparing the row's words with the event's ([Kept]), and written through
// the servers the same way (design D56).
//
// It is an operation like any other here, with an event of its own --
// `event.redact`, its subject the redacted event's, so that the redaction is
// in the history it was taken out of -- whose reason is the request's
// reference: the workspace's record of what it was asked and did. Each other
// event the words are taken out of is a child of it, about that one's subject
// (D34), copying the reference as every part does. Who may ask for it over
// the API is the policy's to say, and no role is given it yet; the
// deployment's operator does it with `loft event redact`, on the word of the
// workspace that keeps the event ([Deps.RedactEvent]).

// Redacted is what taking a reason out did: the event it was asked of, as it
// is now; the act's own event; the other events of the operation the first
// is part of that it took the same words out of, each the subject of a child
// of the act's; the notices it took them out of; and the rows that kept them
// as their own.
type Redacted struct {
	Event   *app.Event
	Act     pdid.Id
	Copies  []pdid.Id
	Notices []pdid.Id
	Rows    []pdid.Id
}

// Redact takes the reason out of the event `ref` names, and out of every
// event of its operation and every notice and row that says the same, for
// the request `reason` refers to, and answers the event as it is now.
func (s domainEvent) Redact(ctx context.Context, req *app.EventRedactRequest) (*app.Event, error) {
	var out Redacted
	err := s.tx(ctx, func(t *Tx) error {
		var err error
		out, err = t.redact(req.GetRef(), req.GetReason())
		return err
	})
	return out.Event, err
}

// RedactEvent is [domainEvent.Redact] as the deployment's own work in a
// tenant: an operator's, on the instruction of the workspace the event is
// its. `server` is the ungated stack.
func (d *Deps) RedactEvent(ctx context.Context, server app.Server, drv dialect.Driver, tenant, event pdid.Id, reason string) (Redacted, error) {
	var out Redacted
	err := d.System(ctx, server, drv, tenant, func(t *Tx) error {
		var err error
		out, err = t.redact(app.EventRef_builder{Id: event.Bytes()}.Build(), reason)
		return err
	})
	return out, err
}

// redact is the act: the event read through the wall -- which puts it on the
// access record of the call, as every read of a reason is -- and every other
// event of its operation that says the same, read the same way; its own
// event recorded, with a child for each of the others; and the reason taken
// out of all of them, and out of every notice and row that says it.
func (t *Tx) redact(ref *app.EventRef, reason string) (Redacted, error) {
	why := strings.TrimSpace(reason)
	if why == "" {
		return Redacted{}, invalid("reason", "사유를 적어 주세요: 이 삭제가 답하는 요청을 가리키는 것")
	}
	id := idOf(ref.GetId())
	if id.IsZero() || id.Domain() != pd.EventDomain {
		return Redacted{}, invalid("ref", "사유를 지울 작업 기록을 지정하세요")
	}

	// One taking at a time of what an operation said, before any of it is
	// read: two at once -- of one event, or of two that say the same -- would
	// each find the words there, and each record that it took them out. They
	// take turns at the operation the event is part of, which each of its
	// events reaches by the same causes; on PostgreSQL the second waits here,
	// and is recorded after the first, which it then finds has left nothing.
	// SQLite has one writer anyway.
	if t.postgres() {
		root, err := operationOf(t.ctx, t.db, t.tenant, id)
		if err != nil {
			return Redacted{}, err
		}
		u := root.Uuid()
		if err := t.drv.Exec(t.ctx, "SELECT pg_advisory_xact_lock($1)", []any{int64(binary.BigEndian.Uint64(u[8:]))}, nil); err != nil {
			return Redacted{}, err
		}
		t.holds()
	}

	vs, err := past.Events(t.ctx, t.past, past.EventQuery{Ids: []pdid.Id{id}})
	if err != nil {
		return Redacted{}, err
	}
	if len(vs) == 0 {
		return Redacted{}, status.Error(codes.NotFound, "작업 기록을 찾을 수 없습니다")
	}
	ev := vs[0]
	if ev.Reason == "" {
		return Redacted{}, failed("사유가 없는 작업 기록입니다")
	}

	// The rest of the operation that says it, read as the first was.
	same, err := Copies(t.ctx, t.db, t.tenant, id)
	if err != nil {
		return Redacted{}, err
	}
	var others []past.EventRow
	if len(same) > 0 {
		rs, err := past.Events(t.ctx, t.past, past.EventQuery{Ids: same})
		if err != nil {
			return Redacted{}, err
		}
		for _, v := range rs {
			if v.Id != id && v.Reason == ev.Reason {
				others = append(others, v)
			}
		}
	}
	copies := make([]pdid.Id, len(others))
	for i, v := range others {
		copies[i] = v.Id
	}
	// And the notices and the rows that say it, while the events still do:
	// their words are compared with the event's.
	notices, err := Told(t.ctx, t.db, t.tenant, id, copies)
	if err != nil {
		return Redacted{}, err
	}
	rows, err := Kept(t.ctx, t.db, t.tenant, id, copies)
	if err != nil {
		return Redacted{}, err
	}

	if err := t.begin(nil, "event.redact", ev.Subject, t.now, why, app.EventRedacted_builder{Event: id.Bytes()}.Build()); err != nil {
		return Redacted{}, err
	}
	if len(others) > 0 {
		kids := make([]past.Child, len(others))
		for i, v := range others {
			kids[i] = past.Child{Event: past.Event{Kind: "event.redact", Subject: v.Subject, Reason: why, Value: app.EventRedacted_builder{Event: v.Id.Bytes()}.Build()}}
		}
		if _, err := t.op.Batch(t.ctx, kids); err != nil {
			return Redacted{}, err
		}
	}
	for _, v := range append([]pdid.Id{id}, copies...) {
		if err := past.Redact(t.ctx, t.past, v); err != nil {
			return Redacted{}, err
		}
	}
	// Each notice is the act's write, through the servers: on the trail as
	// the act wrote it, and on the access record as written.
	for _, n := range notices {
		if _, err := t.next.Notification().Patch(t.ctx, app.NotificationPatchRequest_builder{
			Ref:              app.NotificationRef_builder{Id: n.Bytes()}.Build(),
			Desc:             z.Ptr(""),
			DateUpdatedForce: z.Ptr(true),
		}.Build()); err != nil {
			return Redacted{}, err
		}
	}
	// And each row, the same way.
	for _, r := range rows {
		if err := t.unsay(r); err != nil {
			return Redacted{}, err
		}
	}

	out, err := t.next.Event().Get(t.ctx, app.EventGetRequest_builder{Ref: app.EventRef_builder{Id: id.Bytes()}.Build()}.Build())
	if err != nil {
		return Redacted{}, err
	}
	return Redacted{Event: out, Act: t.ev(), Copies: copies, Notices: notices, Rows: rows}, nil
}

// unsay takes the words out of the row `id`, which kept an event's reason as
// its own ([KeptWhy]): the one field, through the servers, and the rest of
// the row as it was. A review is written nowhere else (`AccessReview.desc`).
func (t *Tx) unsay(id pdid.Id) error {
	ref := id.Bytes()
	var err error
	switch id.Domain() {
	case pd.CustodyDomain:
		_, err = t.next.Custody().Patch(t.ctx, app.CustodyPatchRequest_builder{
			Ref:              app.CustodyRef_builder{Id: ref}.Build(),
			Desc:             z.Ptr(""),
			DateUpdatedForce: z.Ptr(true),
		}.Build())
	case pd.CountFindingDomain:
		_, err = t.next.CountFinding().Patch(t.ctx, app.CountFindingPatchRequest_builder{
			Ref:              app.CountFindingRef_builder{Id: ref}.Build(),
			Desc:             z.Ptr(""),
			DateUpdatedForce: z.Ptr(true),
		}.Build())
	case pd.AccessReviewDomain:
		_, err = t.next.AccessReview().Patch(t.ctx, app.AccessReviewPatchRequest_builder{
			Ref:  app.AccessReviewRef_builder{Id: ref}.Build(),
			Desc: z.Ptr(""),
		}.Build())
	case pd.TenantDomain:
		_, err = t.next.Tenant().Patch(t.ctx, app.TenantPatchRequest_builder{
			Ref:              app.TenantRef_builder{Id: ref}.Build(),
			CloseReason:      z.Ptr(""),
			DateUpdatedForce: z.Ptr(true),
		}.Build())
	default:
		err = fmt.Errorf("%s is of no kind that keeps a reason", id)
	}
	return err
}

// Copies is every other event of the operation the event `id` is part of that
// says what it does: the one it was copied from, and every part that copied
// it -- what taking its reason out takes it out of as well. They are found by
// the causes the events name, all the way up and down, and by whether two
// reasons are one, which the database asks of them where they are kept: no
// one's words are read to find them, so an operator may be told before
// anything is done (`loft event redact`). Of the tenant `tenant`, and none for
// an event with no reason, or none there.
func Copies(ctx context.Context, db *ent.Client, tenant, id pdid.Id) ([]pdid.Id, error) {
	root, err := operationOf(ctx, db, tenant, id)
	if err != nil {
		return nil, err
	}

	var out []pdid.Id
	seen := map[uuid.UUID]bool{root.Uuid(): true}
	for level := []uuid.UUID{root.Uuid()}; len(level) > 0; {
		var next []uuid.UUID
		for page := range slices.Chunk(level, 500) {
			said, err := db.Event.Query().
				Where(event.TenantId(tenant.Uuid()), event.IdIn(page...), event.IdNEQ(id.Uuid()), saying(tenant, id)).
				Order(event.ByDateCreated(), event.ById()).
				Ids(ctx)
			if err != nil {
				return nil, err
			}
			for _, v := range said {
				out = append(out, pdid.Id(v))
			}
			kids, err := db.Event.Query().Where(event.TenantId(tenant.Uuid()), event.CauseIdIn(page...)).Ids(ctx)
			if err != nil {
				return nil, err
			}
			for _, v := range kids {
				if !seen[v] {
					seen[v] = true
					next = append(next, v)
				}
			}
		}
		level = next
	}
	return out, nil
}

// operationOf is the operation the event `id` is part of, all the way up --
// itself when it is part of none, or is none of the tenant's -- by the cause
// each event names.
func operationOf(ctx context.Context, db *ent.Client, tenant, id pdid.Id) (pdid.Id, error) {
	seen := map[pdid.Id]bool{}
	for !seen[id] {
		seen[id] = true
		v, err := db.Event.Query().Where(event.Id(id.Uuid()), event.TenantId(tenant.Uuid())).Select(event.FieldCauseId).Only(ctx)
		if ent.IsNotFound(err) {
			return id, nil
		}
		if err != nil {
			return pdid.Nil, err
		}
		if v.CauseId == nil {
			return id, nil
		}
		id = pdid.Id(*v.CauseId)
	}
	return id, nil
}

// Told is every notice of the tenant `tenant` whose words are what the event
// `id` says as its reason -- told of it, or of one of `copies`, the other
// events of its operation that say the same ([Copies]): what taking the
// reason out takes it out of as well. Nothing links a notice to the event it
// tells of but what they are about and the kind it was told in ([ToldWhy]),
// so that is how they are found, and by whether the notice's words are the
// event's reason, which the database asks of them where both are kept: no
// one's words are read to find them, and an operator may be told how many
// before anything is done (`loft event redact`).
func Told(ctx context.Context, db *ent.Client, tenant, id pdid.Id, copies []pdid.Id) ([]pdid.Id, error) {
	// What each of them is about, by the kind of notice told of it: all
	// that is read of them.
	about := map[string]map[uuid.UUID]bool{}
	for page := range slices.Chunk(append([]pdid.Id{id}, copies...), 500) {
		us := make([]uuid.UUID, len(page))
		for i, v := range page {
			us[i] = v.Uuid()
		}
		vs, err := db.Event.Query().
			Where(event.TenantId(tenant.Uuid()), event.IdIn(us...), event.KindIn(slices.Collect(maps.Keys(ToldWhy))...), event.SubjectIdNotNil()).
			Select(event.FieldKind, event.FieldSubjectId).
			All(ctx)
		if err != nil {
			return nil, err
		}
		for _, v := range vs {
			k := ToldWhy[v.Kind]
			if about[k] == nil {
				about[k] = map[uuid.UUID]bool{}
			}
			about[k][*v.SubjectId] = true
		}
	}

	var out []pdid.Id
	for _, k := range slices.Sorted(maps.Keys(about)) {
		for page := range slices.Chunk(slices.Collect(maps.Keys(about[k])), 500) {
			vs, err := db.Notification.Query().
				Where(notification.TenantId(tenant.Uuid()), notification.Kind(k), notification.SubjectIdIn(page...), predicate.Notification(says(tenant, id, notification.FieldDesc))).
				Order(notification.ByDateCreated(), notification.ById()).
				Ids(ctx)
			if err != nil {
				return nil, err
			}
			for _, v := range vs {
				out = append(out, pdid.Id(v))
			}
		}
	}
	return out, nil
}

// KeptWhy is the fields of the rows an operation writes that keep its
// event's reason as their own words, by the kind of that event, each by its
// full name: what a hand-over was for, which is its memo; what a scan noted,
// which is the finding it left; what a reviewer found; why a workspace was
// closed. Each is declared personal, as the reason is, so that the trail
// holds neither (design D56); and this is how taking the reason out finds
// the row ([Kept]), since nothing links the two but what the row is to the
// event. A row whose words are somebody's reason is of a kind here, and the
// domain's tests hold every row they write to it.
var KeptWhy = map[string]protoreflect.FullName{
	"access.review":   "loft.AccessReview.desc",
	"count.scan":      "loft.CountFinding.desc",
	"custody.issue":   "loft.Custody.desc",
	"workspace.close": "loft.Tenant.close_reason",
}

// Kept is every row of the tenant `tenant` whose words are what the event
// `id` says as its reason -- written by it, or by one of `copies`, the other
// events of its operation that say the same ([Copies]): what taking the
// reason out takes it out of as well. Which row an event wrote is what the
// row is to it ([keeping]), and whether the row's words are the event's reason
// the database asks of them where both are kept: no one's words are read to
// find them, and an operator may be told how many before anything is done
// (`loft event redact`).
func Kept(ctx context.Context, db *ent.Client, tenant, id pdid.Id, copies []pdid.Id) ([]pdid.Id, error) {
	var out []pdid.Id
	for page := range slices.Chunk(append([]pdid.Id{id}, copies...), 500) {
		us := make([]uuid.UUID, len(page))
		for i, v := range page {
			us[i] = v.Uuid()
		}
		// What each of them is, and what it is about and holds: all that is
		// read of them. A message names people and things by identifier
		// only, and holds nobody's words (payday#151).
		vs, err := db.Event.Query().
			Where(event.TenantId(tenant.Uuid()), event.IdIn(us...), event.KindIn(slices.Collect(maps.Keys(KeptWhy))...)).
			Select(event.FieldId, event.FieldKind, event.FieldSubjectId, event.FieldActorId, event.FieldOccurredAt, event.FieldValue).
			Order(event.ByDateCreated(), event.ById()).
			All(ctx)
		if err != nil {
			return nil, err
		}
		for _, v := range vs {
			rows, err := keeping(ctx, db, tenant, v)
			if err != nil {
				return nil, err
			}
			out = append(out, rows...)
		}
	}
	return out, nil
}

// keeping is the row the event `v` of the tenant `tenant` wrote that keeps
// its reason as its own words, if one still does ([Kept]).
func keeping(ctx context.Context, db *ent.Client, tenant pdid.Id, v *ent.Event) ([]pdid.Id, error) {
	id := pdid.Id(v.Id)
	var us []uuid.UUID
	var err error
	switch v.Kind {
	case "custody.issue":
		// The hand-over it made is its subject. Each thing it handed over is
		// the subject of a part of it, which made no hand-over.
		if v.SubjectId == nil || pdid.Id(*v.SubjectId).Domain() != pd.CustodyDomain {
			return nil, nil
		}
		us, err = db.Custody.Query().
			Where(custody.TenantId(tenant.Uuid()), custody.Id(*v.SubjectId), predicate.Custody(says(tenant, id, custody.FieldDesc))).
			Ids(ctx)

	case "access.review":
		// The review its message names.
		m := &app.AccessReviewed{}
		if err := v.Value.UnmarshalTo(m); err != nil {
			return nil, err
		}
		us, err = db.AccessReview.Query().
			Where(accessreview.TenantId(tenant.Uuid()), accessreview.Id(uuidOf(m.GetReview())), predicate.AccessReview(says(tenant, id, accessreview.FieldDesc))).
			Ids(ctx)

	case "workspace.close":
		// The workspace, while this is what it is closed by: one restored
		// and closed again says why the last closing was, and one restored
		// says nothing.
		var last uuid.UUID
		if last, err = db.Event.Query().
			Where(event.TenantId(tenant.Uuid()), event.Kind(v.Kind)).
			Order(event.ByDateCreated(sql.OrderDesc()), event.ById(sql.OrderDesc())).
			FirstId(ctx); err != nil {
			return nil, err
		}
		if last != v.Id {
			return nil, nil
		}
		us, err = db.Tenant.Query().
			Where(enttenant.Id(tenant.Uuid()), predicate.Tenant(says(tenant, id, enttenant.FieldCloseReason))).
			Ids(ctx)

	case "count.scan":
		// The finding it left in the count it scanned in, in its words.
		m := &app.CountScanned{}
		if err := v.Value.UnmarshalTo(m); err != nil {
			return nil, err
		}
		count := uuidOf(m.GetCount())
		in := []predicate.CountFinding{
			countfinding.TenantId(tenant.Uuid()),
			countfinding.CountId(count),
			predicate.CountFinding(says(tenant, id, countfinding.FieldDesc)),
		}
		if v.SubjectId == nil {
			// What it found no thing for -- a label bound to none, a code of
			// nothing here -- is a finding of its own, which nothing scans
			// again: of the label it read or none, by whoever scanned, seen
			// when it says.
			if l := idOf(m.GetLabel()); l.IsZero() {
				in = append(in, countfinding.LabelIdIsNil())
			} else {
				in = append(in, countfinding.LabelId(l.Uuid()))
			}
			in = append(in, countfinding.AssetIdIsNil())
			if v.ActorId != nil {
				in = append(in, countfinding.RecordedBy(*v.ActorId))
			}
			fs, err := db.CountFinding.Query().Where(in...).Select(countfinding.FieldId, countfinding.FieldSeenAt).All(ctx)
			if err != nil {
				return nil, err
			}
			for _, f := range fs {
				if f.SeenAt.Equal(v.OccurredAt) {
					us = append(us, f.Id)
				}
			}
			break
		}
		// A thing has one finding in a count, which the scan that first
		// found it left in its words; another scan of it says where it was
		// seen last, and nothing else.
		var first uuid.UUID
		if first, err = firstScan(ctx, db, tenant, count, *v.SubjectId); err != nil {
			return nil, err
		}
		if first != v.Id {
			return nil, nil
		}
		us, err = db.CountFinding.Query().Where(append(in, countfinding.AssetId(*v.SubjectId))...).Ids(ctx)
	}
	if err != nil {
		return nil, err
	}

	out := make([]pdid.Id, len(us))
	for i, u := range us {
		out[i] = pdid.Id(u)
	}
	return out, nil
}

// firstScan is the scan that first found the thing `asset` in the count
// `count` of the tenant `tenant`: the earliest recorded of its scans there,
// which made its finding. None is the zero identifier.
func firstScan(ctx context.Context, db *ent.Client, tenant pdid.Id, count, asset uuid.UUID) (uuid.UUID, error) {
	vs, err := db.Event.Query().
		Where(event.TenantId(tenant.Uuid()), event.Kind("count.scan"), event.SubjectId(asset)).
		Select(event.FieldId, event.FieldValue).
		Order(event.ByDateCreated(), event.ById()).
		All(ctx)
	if err != nil {
		return uuid.UUID{}, err
	}
	for _, v := range vs {
		m := &app.CountScanned{}
		if err := v.Value.UnmarshalTo(m); err != nil {
			return uuid.UUID{}, err
		}
		if uuidOf(m.GetCount()) == count {
			return v.Id, nil
		}
	}
	return uuid.UUID{}, nil
}

// saying is an event whose reason is the one the event `id` of the tenant
// `tenant` has, and not none: compared by the database, where both are kept.
func saying(tenant, id pdid.Id) predicate.Event {
	return predicate.Event(says(tenant, id, event.FieldReason))
}

// says is a row whose `column` holds the reason the event `id` of the tenant
// `tenant` has, and not nothing: compared by the database, which reads both
// where they are kept and answers neither.
func says(tenant, id pdid.Id, column string) func(*sql.Selector) {
	return func(s *sql.Selector) {
		b := sql.Dialect(s.Dialect())
		said := b.Table(event.Table).As("said")
		of := b.Select(said.C(event.FieldReason)).From(said).Where(sql.And(
			sql.EQ(said.C(event.FieldId), id.Uuid()),
			sql.EQ(said.C(event.FieldTenantId), tenant.Uuid()),
		))
		s.Where(sql.And(sql.NEQ(s.C(column), ""), sql.In(s.C(column), of)))
	}
}
