package domain

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"sort"
	"strings"
	"time"
	"uuid"

	"github.com/lesomnus/payday/pdid"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	app "github.com/lesomnus/rove"
	"github.com/lesomnus/rove/internal/ent"
	"github.com/lesomnus/rove/internal/ent/allocation"
	"github.com/lesomnus/rove/internal/ent/asset"
	"github.com/lesomnus/rove/internal/ent/assettype"
	"github.com/lesomnus/rove/internal/ent/custody"
	"github.com/lesomnus/rove/internal/ent/event"
	"github.com/lesomnus/rove/internal/ent/fact"
	"github.com/lesomnus/rove/internal/ent/holder"
	"github.com/lesomnus/rove/internal/ent/itemmodel"
	"github.com/lesomnus/rove/internal/ent/link"
	"github.com/lesomnus/rove/internal/ent/party"
	"github.com/lesomnus/rove/internal/ent/placement"
	"github.com/lesomnus/rove/internal/ent/predicate"
	"github.com/lesomnus/rove/internal/ent/stewardship"
	"github.com/lesomnus/rove/internal/ent/stock"
	"github.com/lesomnus/rove/internal/ent/workorder"
	"github.com/lesomnus/rove/server/opensearch"
)

// visible narrows time rows to what was recorded by `known` and not yet
// superseded then, or -- with no `known` -- to the current knowledge, unless
// `all` asks for every row ever written.
type visible struct {
	known *time.Time
	all   bool
}

func (v visible) placement() []predicate.Placement {
	switch {
	case v.known != nil:
		return []predicate.Placement{placement.DateCreatedLTE(*v.known), placement.Or(placement.SupersededAtIsNil(), placement.SupersededAtGT(*v.known))}
	case v.all:
		return nil
	default:
		return []predicate.Placement{placement.SupersededAtIsNil()}
	}
}

func (v visible) stewardship() []predicate.Stewardship {
	switch {
	case v.known != nil:
		return []predicate.Stewardship{stewardship.DateCreatedLTE(*v.known), stewardship.Or(stewardship.SupersededAtIsNil(), stewardship.SupersededAtGT(*v.known))}
	case v.all:
		return nil
	default:
		return []predicate.Stewardship{stewardship.SupersededAtIsNil()}
	}
}

func (v visible) link() []predicate.Link {
	switch {
	case v.known != nil:
		return []predicate.Link{link.DateCreatedLTE(*v.known), link.Or(link.SupersededAtIsNil(), link.SupersededAtGT(*v.known))}
	case v.all:
		return nil
	default:
		return []predicate.Link{link.SupersededAtIsNil()}
	}
}

func (v visible) fact() []predicate.Fact {
	switch {
	case v.known != nil:
		return []predicate.Fact{fact.DateCreatedLTE(*v.known), fact.Or(fact.SupersededAtIsNil(), fact.SupersededAtGT(*v.known))}
	case v.all:
		return nil
	default:
		return []predicate.Fact{fact.SupersededAtIsNil()}
	}
}

// Timeline answers an asset's history: the time rows about it and the events
// that wrote them, newest first.
//
// It answers within the tenant's view window (design 8.1): a row that ended or
// was superseded before the window began is left out, and so is a value an
// attribute stopped having before then. What the window begins inside of is
// the state it begins in, so a placement from years ago that still holds is
// here, and so is the event that made it.
func (s domainAsset) Timeline(ctx context.Context, req *app.AssetTimelineRequest) (*app.AssetTimelineResponse, error) {
	t, err := s.read(ctx)
	if err != nil {
		return nil, err
	}
	cur, id, err := t.get(req.GetRef(), "ref")
	if err != nil {
		return nil, err
	}
	// What the type calls each attribute, for a person reading the history.
	labels := map[string]string{}
	if spec, err := t.specOfAsset(cur); err == nil {
		for _, d := range spec.GetAttributes() {
			if d.GetLabel() != "" {
				labels["attr."+d.GetKey()] = d.GetLabel()
			}
		}
	}
	vis := visible{all: req.GetSuperseded()}
	if req.HasKnown() {
		k := req.GetKnown().AsTime()
		if err := t.inView("known", k); err != nil {
			return nil, err
		}
		vis.known = &k
	}
	since, err := t.since()
	if err != nil {
		return nil, err
	}
	tid := t.tenant.Uuid()
	a := id.Uuid()

	names := namer{t: t}
	entries := []*app.TimelineEntry{}

	ps, err := t.db.Placement.Query().Where(append(vis.placement(), placement.TenantId(tid), placement.ChildId(a))...).All(ctx)
	if err != nil {
		return nil, err
	}
	for _, r := range ps {
		if gone(since, r.ValidTo, r.SupersededAt) {
			continue
		}
		e := entry("placement", r.Id, r.ValidFrom, r.ValidTo, r.DateCreated, r.SupersededAt, r.EventId)
		e.SetOtherId(pdid.Id(r.ParentId).Bytes())
		e.SetOtherName(names.asset(r.ParentId))
		e.SetSummary(fmt.Sprintf("%s 안 (%s)", names.asset(r.ParentId), modeName(r.Mode)))
		d := map[string]string{"mode": r.Mode}
		if r.Slot != "" {
			d["slot"] = r.Slot
		}
		if r.UFrom > 0 {
			d["u"] = fmt.Sprintf("%d-%d", r.UFrom, r.UTo)
		}
		e.SetDetail(d)
		entries = append(entries, e)
	}

	ss, err := t.db.Stewardship.Query().Where(append(vis.stewardship(), stewardship.TenantId(tid), stewardship.AssetId(a))...).All(ctx)
	if err != nil {
		return nil, err
	}
	for _, r := range ss {
		if gone(since, r.ValidTo, r.SupersededAt) {
			continue
		}
		e := entry("stewardship", r.Id, r.ValidFrom, r.ValidTo, r.DateCreated, r.SupersededAt, r.EventId)
		e.SetOtherId(pdid.Id(r.PartyId).Bytes())
		e.SetOtherName(names.party(r.PartyId))
		e.SetSummary(fmt.Sprintf("%s: %s", roleName(r.Role), names.party(r.PartyId)))
		e.SetDetail(map[string]string{"role": r.Role})
		entries = append(entries, e)
	}

	ls, err := t.db.Link.Query().Where(append(vis.link(), link.TenantId(tid), link.Or(link.SourceId(a), link.TargetId(a)))...).All(ctx)
	if err != nil {
		return nil, err
	}
	for _, r := range ls {
		if gone(since, r.ValidTo, r.SupersededAt) {
			continue
		}
		other := r.TargetId
		dir := "→"
		if r.TargetId == a {
			other = r.SourceId
			dir = "←"
		}
		e := entry("link", r.Id, r.ValidFrom, r.ValidTo, r.DateCreated, r.SupersededAt, r.EventId)
		e.SetOtherId(pdid.Id(other).Bytes())
		e.SetOtherName(names.asset(other))
		e.SetSummary(fmt.Sprintf("%s %s (%s)", dir, names.asset(other), r.Kind))
		e.SetDetail(map[string]string{"kind": r.Kind, "required": fmt.Sprint(r.Required)})
		entries = append(entries, e)
	}

	fs, err := t.db.Fact.Query().Where(append(vis.fact(), fact.TenantId(tid), fact.AssetId(a))...).All(ctx)
	if err != nil {
		return nil, err
	}
	// When each attribute took the value it had as the window began. A fact
	// has no end of its own -- the next one of its key is its end -- so a value
	// older than that is one the attribute had stopped having by then.
	began := map[string]time.Time{}
	if !since.IsZero() {
		for _, r := range fs {
			if r.ValidFrom.After(since) || (vis.all && r.SupersededAt != nil) {
				continue
			}
			if b, ok := began[r.Key]; !ok || r.ValidFrom.After(b) {
				began[r.Key] = r.ValidFrom
			}
		}
	}
	for _, r := range fs {
		if gone(since, nil, r.SupersededAt) || r.ValidFrom.Before(began[r.Key]) {
			continue
		}
		e := entry("fact", r.Id, r.ValidFrom, nil, r.DateCreated, r.SupersededAt, r.EventId)
		v := r.Value
		switch {
		case r.Cleared:
			v = "(지움)"
		case r.Key == "type":
			v = names.kind(uuidOf(idFromString(r.Value).Bytes()))
		case r.Key == "model":
			v = names.model(uuidOf(idFromString(r.Value).Bytes()))
		case r.Key == "status":
			v = or(statusSay[v], v)
		case r.Key == "condition":
			v = or(conditionSay[v], v)
		}
		name := factName(r.Key)
		if l, ok := labels[r.Key]; ok {
			name = l
		}
		e.SetSummary(fmt.Sprintf("%s = %s", name, v))
		e.SetDetail(map[string]string{"key": r.Key, "value": r.Value})
		entries = append(entries, e)
	}

	// The events: what was done, by whom and why, including what wrote no
	// time row -- an acknowledgement, a label bound, a count.
	evs, err := t.db.Event.Query().Where(event.TenantId(tid), event.SubjectId(a)).All(ctx)
	if err != nil {
		return nil, err
	}
	byId := map[uuid.UUID]*ent.Event{}
	for _, v := range evs {
		byId[v.Id] = v
	}
	// Events time rows point at but that name another subject.
	for _, e := range entries {
		k := uuidOf(e.GetEventId())
		if _, ok := byId[k]; ok || k == (uuid.UUID{}) {
			continue
		}
		v, err := t.db.Event.Query().Where(event.Id(k), event.TenantId(tid)).Only(ctx)
		if err == nil {
			byId[k] = v
		}
	}
	// What an event was part of, when it was what an operation over many
	// things did to this one: the operation, in its own words (design D34).
	causes := map[uuid.UUID]string{}
	cause := func(v *ent.Event) string {
		if v.CauseId == nil {
			return ""
		}
		k := *v.CauseId
		if s, ok := causes[k]; ok {
			return s
		}
		s := ""
		if c, err := t.db.Event.Query().Where(event.Id(k), event.TenantId(tid)).Only(ctx); err == nil {
			s = names.say(c.Desc, c.Payload)
		}
		causes[k] = s
		return s
	}
	for _, e := range entries {
		if v, ok := byId[uuidOf(e.GetEventId())]; ok {
			e.SetEventKind(v.Kind)
			e.SetReason(v.Reason)
			e.SetActor(names.actor(v.ActorId))
			e.SetCause(cause(v))
		}
	}
	// An event outside the window is still here when a row in it says it is
	// what wrote the row.
	wrote := map[uuid.UUID]bool{}
	for _, e := range entries {
		wrote[uuidOf(e.GetEventId())] = true
	}
	for _, v := range evs {
		if !since.IsZero() && v.OccurredAt.Before(since) && v.DateCreated.Before(since) && !wrote[v.Id] {
			continue
		}
		e := entry("event", v.Id, v.OccurredAt, nil, v.DateCreated, nil, v.Id)
		e.SetSummary(names.say(v.Desc, v.Payload))
		e.SetEventKind(v.Kind)
		e.SetReason(v.Reason)
		e.SetActor(names.actor(v.ActorId))
		// Not when it says what the operation did: a hand-over's child says
		// "지급" of the asset as the hand-over says it of the person.
		if c := cause(v); c != e.GetSummary() {
			e.SetCause(c)
		}
		if v.Payload != "" {
			e.SetDetail(decode(v.Payload))
		}
		entries = append(entries, e)
	}

	sort.SliceStable(entries, func(i, j int) bool {
		a, b := entries[i], entries[j]
		if !a.GetValidFrom().AsTime().Equal(b.GetValidFrom().AsTime()) {
			return a.GetValidFrom().AsTime().After(b.GetValidFrom().AsTime())
		}
		return a.GetRecordedAt().AsTime().After(b.GetRecordedAt().AsTime())
	})
	if n := int(req.GetLimit()); n > 0 && len(entries) > n {
		entries = entries[:n]
	}

	return app.AssetTimelineResponse_builder{Entries: entries}.Build(), nil
}

func entry(kind string, id uuid.UUID, from time.Time, to *time.Time, recorded time.Time, superseded *time.Time, ev uuid.UUID) *app.TimelineEntry {
	e := app.TimelineEntry_builder{
		Kind:       kind,
		RowId:      pdid.Id(id).Bytes(),
		ValidFrom:  ts(from),
		RecordedAt: ts(recorded),
		EventId:    pdid.Id(ev).Bytes(),
	}.Build()
	if to != nil {
		e.SetValidTo(ts(*to))
	}
	if superseded != nil {
		e.SetSupersededAt(ts(*superseded))
	}
	return e
}

func decode(v string) map[string]string {
	out := map[string]string{}
	for _, line := range strings.Split(v, "\n") {
		k, val, ok := strings.Cut(line, "=")
		if ok {
			out[k] = val
		}
	}
	return out
}

// namer reads the names a timeline shows, once each.
type namer struct {
	t       *Tx
	assets  map[uuid.UUID]string
	partys  map[uuid.UUID]string
	actors  map[uuid.UUID]string
	members map[uuid.UUID]string
	kinds   map[uuid.UUID]string
	models  map[uuid.UUID]string
}

func (n *namer) asset(id uuid.UUID) string {
	if n.assets == nil {
		n.assets = map[uuid.UUID]string{}
	}
	if v, ok := n.assets[id]; ok {
		return v
	}
	v := "?"
	if a, err := n.t.db.Asset.Query().Where(asset.Id(id), asset.TenantId(n.t.tenant.Uuid())).Only(n.t.ctx); err == nil {
		v = fmt.Sprintf("%s %s", a.Tag, a.Name)
	}
	n.assets[id] = v
	return v
}

func (n *namer) party(id uuid.UUID) string {
	if n.partys == nil {
		n.partys = map[uuid.UUID]string{}
	}
	if v, ok := n.partys[id]; ok {
		return v
	}
	v := "?"
	if p, err := n.t.db.Party.Query().Where(party.Id(id), party.TenantId(n.t.tenant.Uuid())).Only(n.t.ctx); err == nil {
		v = p.Name
	}
	n.partys[id] = v
	return v
}

func (n *namer) actor(id *uuid.UUID) string {
	if id == nil {
		return "시스템"
	}
	if n.actors == nil {
		n.actors = map[uuid.UUID]string{}
	}
	if v, ok := n.actors[*id]; ok {
		return v
	}
	v := "?"
	if p, err := n.t.db.Party.Query().Where(party.TenantId(n.t.tenant.Uuid()), party.HolderId(*id)).First(n.t.ctx); err == nil {
		v = p.Name
	} else {
		v = n.member(*id)
	}
	n.actors[*id] = v
	return v
}

// member is a membership by its own name: the one it signs in with.
func (n *namer) member(id uuid.UUID) string {
	if n.members == nil {
		n.members = map[uuid.UUID]string{}
	}
	if v, ok := n.members[id]; ok {
		return v
	}
	v := "?"
	if h, err := n.t.db.Holder.Query().Where(holder.Id(id), holder.TenantId(n.t.tenant.Uuid())).Only(n.t.ctx); err == nil {
		v = "@" + h.Alias
		if h.Alias == "" {
			// The membership of somebody forgotten names nobody, and goes
			// by the pseudonym it was left with (`endMembership`).
			v = h.Name
		}
	}
	n.members[id] = v
	return v
}

// An event names the people it is about by identifier, in its payload, and
// its sentence by a mark for each; the words are read with the event, so that
// one about somebody forgotten reads with the pseudonym they were left with,
// and one about somebody renamed with the name they have now (design 8.2). An
// event written before it did names them in its words, and reads as written.
//
// What it says of a thing -- a tag, a type, a reservation -- is what the thing
// was called then, and stays.
var marks = []struct {
	mark, key string
	say       func(*namer, uuid.UUID) string
}{
	// The person record.
	{"{party}", "party", (*namer).party},
	// The membership, as the history names whoever did something.
	{"{holder}", "holder", func(n *namer, id uuid.UUID) string { return n.actor(&id) }},
	// The membership by the name it signs in with.
	{"{alias}", "holder", (*namer).member},
}

// say is an event's sentence with the people it marks named as they are now.
func (n *namer) say(desc, payload string) string {
	if !strings.Contains(desc, "{") {
		return desc
	}
	p := decode(payload)
	for _, m := range marks {
		if !strings.Contains(desc, m.mark) {
			continue
		}
		id, err := pdid.Parse(p[m.key])
		if err != nil {
			continue
		}
		desc = strings.ReplaceAll(desc, m.mark, m.say(n, id.Uuid()))
	}
	return desc
}

func (n *namer) kind(id uuid.UUID) string {
	if n.kinds == nil {
		n.kinds = map[uuid.UUID]string{}
	}
	if v, ok := n.kinds[id]; ok {
		return v
	}
	v := "?"
	if ty, err := n.t.db.AssetType.Query().Where(assettype.Id(id), assettype.TenantId(n.t.tenant.Uuid())).Only(n.t.ctx); err == nil {
		v = ty.Name
	}
	n.kinds[id] = v
	return v
}

func (n *namer) model(id uuid.UUID) string {
	if n.models == nil {
		n.models = map[uuid.UUID]string{}
	}
	if v, ok := n.models[id]; ok {
		return v
	}
	v := "?"
	if m, err := n.t.db.ItemModel.Query().Where(itemmodel.Id(id), itemmodel.TenantId(n.t.tenant.Uuid())).Only(n.t.ctx); err == nil {
		v = strings.TrimSpace(m.Maker + " " + m.Name)
	}
	n.models[id] = v
	return v
}

func modeName(v string) string {
	return map[string]string{"located": "놓임", "installed": "장착", "part": "구성품"}[v]
}

func roleName(v string) string {
	return map[string]string{"owner": "소유", "manager": "관리", "custodian": "보유"}[v]
}

func factName(k string) string {
	if v, ok := map[string]string{
		"name": "이름", "desc": "설명", "status": "상태", "condition": "컨디션",
		"serial": "시리얼", "tag": "자산 번호", "type": "유형", "model": "모델",
	}[k]; ok {
		return v
	}
	return strings.TrimPrefix(k, "attr.")
}

// QueryAt answers an asset and what was inside it at a moment, which is a
// moment in the tenant's view window (design 8.1).
func (s domainAsset) QueryAt(ctx context.Context, req *app.AssetQueryAtRequest) (*app.AssetQueryAtResponse, error) {
	t, err := s.read(ctx)
	if err != nil {
		return nil, err
	}
	_, id, err := t.get(req.GetRoot(), "root")
	if err != nil {
		return nil, err
	}
	at := t.now
	if req.HasAt() {
		at = req.GetAt().AsTime()
	}
	if err := t.inView("at", at); err != nil {
		return nil, err
	}
	vis := visible{}
	if req.HasKnown() {
		k := req.GetKnown().AsTime()
		if err := t.inView("known", k); err != nil {
			return nil, err
		}
		vis.known = &k
	}

	items, err := t.subtree(id.Uuid(), at, vis, int(req.GetDepth()))
	if err != nil {
		return nil, err
	}
	return app.AssetQueryAtResponse_builder{Items: items}.Build(), nil
}

// subtree answers `root` and everything inside it at `at`.
func (t *Tx) subtree(root uuid.UUID, at time.Time, vis visible, depth int) ([]*app.AssetState, error) {
	tid := t.tenant.Uuid()
	valid := func() []predicate.Placement {
		return append(vis.placement(),
			placement.TenantId(tid),
			placement.ValidFromLTE(at),
			placement.Or(placement.ValidToIsNil(), placement.ValidToGT(at)),
		)
	}

	states := map[uuid.UUID]*app.AssetState{}
	order := []uuid.UUID{root}
	states[root] = app.AssetState_builder{Id: pdid.Id(root).Bytes()}.Build()

	// Where the root itself was.
	if r, err := t.db.Placement.Query().Where(append(valid(), placement.ChildId(root))...).First(t.ctx); err == nil {
		placeState(states[root], r)
	}

	frontier := []uuid.UUID{root}
	for d := 1; len(frontier) > 0 && (depth == 0 || d <= depth) && d < 64; d++ {
		rs, err := t.db.Placement.Query().Where(append(valid(), placement.ParentIdIn(frontier...))...).All(t.ctx)
		if err != nil {
			return nil, err
		}
		frontier = nil
		for _, r := range rs {
			if _, seen := states[r.ChildId]; seen {
				continue
			}
			st := app.AssetState_builder{Id: pdid.Id(r.ChildId).Bytes(), Depth: uint32(d)}.Build()
			placeState(st, r)
			states[r.ChildId] = st
			order = append(order, r.ChildId)
			frontier = append(frontier, r.ChildId)
		}
	}

	facts, err := t.factsVisible(order, at, vis)
	if err != nil {
		return nil, err
	}
	ss, err := t.db.Stewardship.Query().Where(append(vis.stewardship(),
		stewardship.TenantId(tid),
		stewardship.AssetIdIn(order...),
		stewardship.ValidFromLTE(at),
		stewardship.Or(stewardship.ValidToIsNil(), stewardship.ValidToGT(at)),
	)...).All(t.ctx)
	if err != nil {
		return nil, err
	}
	rows, err := t.db.Asset.Query().Where(asset.TenantId(tid), asset.IdIn(order...)).All(t.ctx)
	if err != nil {
		return nil, err
	}
	kinds := map[uuid.UUID]*ent.Asset{}
	for _, r := range rows {
		kinds[r.Id] = r
	}

	names := namer{t: t}
	out := []*app.AssetState{}
	for _, k := range order {
		st := states[k]
		f := facts[k]
		if f == nil {
			f = map[string]string{}
		}
		st.SetFacts(f)
		st.SetExisted(len(f) > 0)
		if r, ok := kinds[k]; ok {
			st.SetKind(r.Kind)
			st.SetTag(or(f["tag"], r.Tag))
		}
		stw := map[string]string{}
		stn := map[string]string{}
		for _, r := range ss {
			if r.AssetId == k {
				stw[r.Role] = pdid.Id(r.PartyId).String()
				stn[r.Role] = names.party(r.PartyId)
			}
		}
		st.SetStewards(stw)
		st.SetStewardNames(stn)
		out = append(out, st)
	}
	return out, nil
}

func placeState(st *app.AssetState, r *ent.Placement) {
	st.SetParentId(pdid.Id(r.ParentId).Bytes())
	st.SetMode(r.Mode)
	st.SetSlot(r.Slot)
	st.SetUFrom(r.UFrom)
	st.SetUTo(r.UTo)
}

func (t *Tx) factsVisible(ids []uuid.UUID, at time.Time, vis visible) (map[uuid.UUID]map[string]string, error) {
	if vis.known == nil {
		return t.factsAt(ids, at, nil)
	}
	return t.factsAt(ids, at, vis.known)
}

// Diff answers what changed under an asset between two moments, both in the
// tenant's view window.
func (s domainAsset) Diff(ctx context.Context, req *app.AssetDiffRequest) (*app.AssetDiffResponse, error) {
	t, err := s.read(ctx)
	if err != nil {
		return nil, err
	}
	_, id, err := t.get(req.GetRoot(), "root")
	if err != nil {
		return nil, err
	}
	if !req.HasFrom() || !req.HasTo() {
		return nil, invalid("from", "비교할 두 시점을 정하세요")
	}
	if err := t.inView("from", req.GetFrom().AsTime()); err != nil {
		return nil, err
	}
	if err := t.inView("to", req.GetTo().AsTime()); err != nil {
		return nil, err
	}
	a, err := t.subtree(id.Uuid(), req.GetFrom().AsTime(), visible{}, 0)
	if err != nil {
		return nil, err
	}
	b, err := t.subtree(id.Uuid(), req.GetTo().AsTime(), visible{}, 0)
	if err != nil {
		return nil, err
	}

	index := func(vs []*app.AssetState) map[uuid.UUID]*app.AssetState {
		m := map[uuid.UUID]*app.AssetState{}
		for _, v := range vs {
			m[uuidOf(v.GetId())] = v
		}
		return m
	}
	before, after := index(a), index(b)
	names := namer{t: t}

	changes := []*app.AssetChange{}
	add := func(k uuid.UUID, st *app.AssetState, what, field, x, y string) {
		changes = append(changes, app.AssetChange_builder{
			Id:     pdid.Id(k).Bytes(),
			Tag:    st.GetTag(),
			Name:   st.GetFacts()["name"],
			What:   what,
			Field:  field,
			Before: x,
			After:  y,
		}.Build())
	}
	for k, y := range after {
		x, ok := before[k]
		if !ok {
			add(k, y, "entered", "", "", names.asset(uuidOf(y.GetParentId())))
			continue
		}
		if string(x.GetParentId()) != string(y.GetParentId()) {
			add(k, y, "moved", "parent", names.asset(uuidOf(x.GetParentId())), names.asset(uuidOf(y.GetParentId())))
		}
		for _, f := range unionKeys(x.GetFacts(), y.GetFacts()) {
			if x.GetFacts()[f] != y.GetFacts()[f] {
				add(k, y, "changed", f, x.GetFacts()[f], y.GetFacts()[f])
			}
		}
		for _, f := range unionKeys(x.GetStewardNames(), y.GetStewardNames()) {
			if x.GetStewards()[f] != y.GetStewards()[f] {
				add(k, y, "changed", "steward."+f, x.GetStewardNames()[f], y.GetStewardNames()[f])
			}
		}
	}
	for k, x := range before {
		if _, ok := after[k]; !ok {
			add(k, x, "left", "", names.asset(uuidOf(x.GetParentId())), "")
		}
	}
	sort.SliceStable(changes, func(i, j int) bool { return changes[i].GetTag() < changes[j].GetTag() })

	return app.AssetDiffResponse_builder{Changes: changes}.Build(), nil
}

func unionKeys(a, b map[string]string) []string {
	m := maps.Clone(a)
	if m == nil {
		m = map[string]string{}
	}
	maps.Copy(m, b)
	return sortedKeys(m)
}

// Search finds assets by what a person types: a name, a tag, a serial, in
// part.
//
// A deployment that names a search engine asks it (D31), and what it finds
// is read through the wall like anything else, best first. One that names
// none asks the database, which reads every row of the tenant ([found]). A
// search with nothing typed is the filters alone, and the database answers it
// either way.
func (s domainAsset) Search(ctx context.Context, req *app.AssetSearchRequest) (*app.AssetSearchResponse, error) {
	t, err := s.read(ctx)
	if err != nil {
		return nil, err
	}
	if q := strings.TrimSpace(req.GetQ()); q != "" && t.deps.Finder != nil {
		return t.searchEngine(req, q)
	}
	tid := t.tenant.Uuid()
	ps := []predicate.Asset{asset.TenantId(tid), asset.DateErasedIsNil()}
	if q := strings.TrimSpace(req.GetQ()); q != "" {
		// What a person would type: a name, a tag, a serial, or what it is,
		// who made it and who has it.
		ps = append(ps, predicate.Asset(found(t.postgres(), tid, q)))
	}
	if v := req.GetKind(); v != "" {
		ps = append(ps, asset.Kind(v))
	}
	if v := req.GetStatus(); v != "" {
		ps = append(ps, asset.Status(v))
	}
	if req.HasType() {
		ps = append(ps, asset.TypeId(uuidOf(req.GetType().GetId())))
	}
	if req.HasCustodian() {
		ps = append(ps, asset.CustodianId(uuidOf(req.GetCustodian().GetId())))
	}
	if req.HasWithin() {
		_, root, err := t.get(req.GetWithin(), "within")
		if err != nil {
			return nil, err
		}
		ids, err := t.descendants(root.Uuid())
		if err != nil {
			return nil, err
		}
		ps = append(ps, asset.IdIn(ids...))
	}

	q := t.db.Asset.Query().Where(ps...)
	total, err := q.Clone().Count(ctx)
	if err != nil {
		return nil, err
	}
	size := int(req.GetSize())
	if size <= 0 || size > 500 {
		size = 50
	}
	rows, err := q.Order(asset.ByTag()).Offset(int(req.GetOffset())).Limit(size).Ids(ctx)
	if err != nil {
		return nil, err
	}

	return t.found(rows, total), nil
}

// found is the assets keys name, read through the wall in that order, and
// the total a search counted. One the caller may not see, or that went since
// it was counted, is left out.
func (t *Tx) found(keys []uuid.UUID, total int) *app.AssetSearchResponse {
	items := make([]*app.Asset, 0, len(keys))
	for _, k := range keys {
		v, _, err := t.get(assetRef(k), "id")
		if err != nil {
			continue
		}
		items = append(items, v)
	}
	return app.AssetSearchResponse_builder{Items: items, Total: uint32(total)}.Build()
}

// searchEngine is a search the engine answers (D31): the same request, asked
// of the index, and what it finds read as [Tx.found] reads it.
func (t *Tx) searchEngine(req *app.AssetSearchRequest, q string) (*app.AssetSearchResponse, error) {
	f := opensearch.Query{
		Tenant: t.tenant.Uuid(),
		Q:      q,
		Kind:   req.GetKind(),
		Status: req.GetStatus(),
		Offset: int(req.GetOffset()),
		Size:   int(req.GetSize()),
	}
	if f.Size <= 0 || f.Size > 500 {
		f.Size = 50
	}
	if f.Offset+f.Size > opensearch.Window {
		return nil, invalid("offset", "검색은 앞의 %d건까지만 넘겨 볼 수 있습니다", opensearch.Window)
	}
	if req.HasType() {
		f.Type = uuidOf(req.GetType().GetId())
	}
	if req.HasCustodian() {
		f.Custodian = uuidOf(req.GetCustodian().GetId())
	}
	if req.HasWithin() {
		_, root, err := t.get(req.GetWithin(), "within")
		if err != nil {
			return nil, err
		}
		if f.Within, err = t.descendants(root.Uuid()); err != nil {
			return nil, err
		}
	}

	keys, total, err := t.deps.Finder.Search(t.ctx, f)
	if err != nil {
		return nil, t.unanswered(err)
	}
	return t.found(keys, int(total)), nil
}

// descendants answers what is inside an asset now, at any depth, by the
// current parent on each row.
func (t *Tx) descendants(root uuid.UUID) ([]uuid.UUID, error) {
	out := []uuid.UUID{}
	frontier := []uuid.UUID{root}
	seen := map[uuid.UUID]bool{root: true}
	for len(frontier) > 0 {
		ids, err := t.db.Asset.Query().Where(asset.TenantId(t.tenant.Uuid()), asset.ParentIdIn(frontier...), asset.DateErasedIsNil()).Ids(t.ctx)
		if err != nil {
			return nil, err
		}
		frontier = nil
		for _, k := range ids {
			if seen[k] {
				continue
			}
			seen[k] = true
			out = append(out, k)
			frontier = append(frontier, k)
		}
	}
	return out, nil
}

// Report answers the numbers a dashboard shows.
func (s domainAsset) Report(ctx context.Context, req *app.AssetReportRequest) (*app.AssetReportResponse, error) {
	t, err := s.read(ctx)
	if err != nil {
		return nil, err
	}
	tid := t.tenant.Uuid()
	rows := []*app.ReportRow{}
	row := func(group, key, label string, v float64, detail map[string]string) {
		rows = append(rows, app.ReportRow_builder{Group: group, Key: key, Label: label, Value: v, Detail: detail}.Build())
	}

	switch req.GetKind() {
	case "", "summary":
		as, err := t.db.Asset.Query().Where(asset.TenantId(tid), asset.DateErasedIsNil()).All(ctx)
		if err != nil {
			return nil, err
		}
		byKind, byStatus, byType := map[string]int{}, map[string]int{}, map[uuid.UUID]int{}
		for _, a := range as {
			byKind[a.Kind]++
			byStatus[a.Status]++
			byType[a.TypeId]++
		}
		for _, k := range sortedKeys(byKind) {
			row("kind", k, k, float64(byKind[k]), nil)
		}
		for _, k := range sortedKeys(byStatus) {
			row("status", k, k, float64(byStatus[k]), nil)
		}
		names := namer{t: t}
		for k, n := range byType {
			label := "(유형 없음)"
			if k != (uuid.UUID{}) {
				label = names.kind(k)
			}
			row("type", pdid.Id(k).String(), label, float64(n), nil)
		}
		open, err := t.db.Custody.Query().Where(custody.TenantId(tid), custody.Status("open"), custody.DateErasedIsNil()).All(ctx)
		if err != nil {
			return nil, err
		}
		overdue := 0
		for _, c := range open {
			if c.DueAt != nil && c.DueAt.Before(t.now) {
				overdue++
			}
		}
		row("custody", "open", "지급·대여 중", float64(len(open)), nil)
		row("custody", "overdue", "반납 기한 지남", float64(overdue), nil)
		wo, err := t.db.WorkOrder.Query().Where(workorder.TenantId(tid), workorder.StatusIn("open", "scheduled", "in_progress"), workorder.DateErasedIsNil()).Count(ctx)
		if err != nil {
			return nil, err
		}
		row("work", "open", "진행 중 작업", float64(wo), nil)

	case "custody":
		open, err := t.db.Custody.Query().Where(custody.TenantId(tid), custody.Status("open"), custody.DateErasedIsNil()).All(ctx)
		if err != nil {
			return nil, err
		}
		names := namer{t: t}
		byParty := map[uuid.UUID]int{}
		for _, c := range open {
			byParty[c.PartyId]++
			if c.DueAt != nil && c.DueAt.Before(t.now) {
				row("overdue", pdid.Id(c.Id).String(), names.party(c.PartyId), t.now.Sub(*c.DueAt).Hours()/24, map[string]string{"due": c.DueAt.Format(time.DateOnly)})
			}
		}
		for k, n := range byParty {
			row("party", pdid.Id(k).String(), names.party(k), float64(n), nil)
		}

	case "utilization":
		since, err := t.since()
		if err != nil {
			return nil, err
		}
		from, to := t.now.AddDate(0, 0, -30), t.now
		if from.Before(since) {
			from = since
		}
		if req.HasFrom() {
			from = req.GetFrom().AsTime()
			if err := t.inView("from", from); err != nil {
				return nil, err
			}
		}
		if req.HasTo() {
			to = req.GetTo().AsTime()
		}
		as, err := t.db.Allocation.Query().Where(
			allocation.TenantId(tid),
			allocation.Kind("reservation"),
			allocation.Blocking(true),
			allocation.BeginsAtLT(to),
			allocation.EndsAtGT(from),
		).All(ctx)
		if err != nil {
			return nil, err
		}
		hours := map[uuid.UUID]float64{}
		for _, a := range as {
			b, e := a.BeginsAt, a.EndsAt
			if b.Before(from) {
				b = from
			}
			if e.After(to) {
				e = to
			}
			hours[a.ResourceId] += e.Sub(b).Hours()
		}
		names := namer{t: t}
		span := to.Sub(from).Hours()
		for k, h := range hours {
			row("resource", pdid.Id(k).String(), names.asset(k), h, map[string]string{"share": fmt.Sprintf("%.1f%%", 100*h/span)})
		}

	case "stock":
		ss, err := t.db.Stock.Query().Where(stock.TenantId(tid), stock.DateErasedIsNil()).All(ctx)
		if err != nil {
			return nil, err
		}
		for _, v := range ss {
			if v.Threshold > 0 && v.Quantity <= v.Threshold {
				row("low", pdid.Id(v.Id).String(), v.Name, float64(v.Quantity), map[string]string{"threshold": fmt.Sprint(v.Threshold)})
			}
		}

	case "work":
		// The work still open, and what was done in the view window.
		since, err := t.since()
		if err != nil {
			return nil, err
		}
		ps := []predicate.WorkOrder{workorder.TenantId(tid), workorder.DateErasedIsNil()}
		if !since.IsZero() {
			ps = append(ps, workorder.Not(workOver(since)))
		}
		ws, err := t.db.WorkOrder.Query().Where(ps...).All(ctx)
		if err != nil {
			return nil, err
		}
		byStatus := map[string]int{}
		var cost int64
		for _, w := range ws {
			byStatus[w.Status]++
			cost += w.Cost
		}
		for _, k := range sortedKeys(byStatus) {
			row("status", k, k, float64(byStatus[k]), nil)
		}
		row("cost", "total", "비용 합계", float64(cost), nil)

	default:
		return nil, status.Error(codes.InvalidArgument, "보고서 종류가 올바르지 않습니다")
	}

	slices.SortStableFunc(rows, func(a, b *app.ReportRow) int { return strings.Compare(a.GetGroup(), b.GetGroup()) })
	return app.AssetReportResponse_builder{Rows: rows}.Build(), nil
}
