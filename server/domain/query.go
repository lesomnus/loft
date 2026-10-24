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

	"github.com/lesomnus/payday/past"
	"github.com/lesomnus/payday/pdid"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	app "github.com/lesomnus/rove"
	"github.com/lesomnus/rove/internal/ent"
	"github.com/lesomnus/rove/internal/ent/allocation"
	"github.com/lesomnus/rove/internal/ent/asset"
	"github.com/lesomnus/rove/internal/ent/custody"
	"github.com/lesomnus/rove/internal/ent/link"
	"github.com/lesomnus/rove/internal/ent/placement"
	"github.com/lesomnus/rove/internal/ent/predicate"
	"github.com/lesomnus/rove/internal/ent/stewardship"
	"github.com/lesomnus/rove/internal/ent/stock"
	"github.com/lesomnus/rove/internal/ent/workorder"
	"github.com/lesomnus/rove/internal/money"
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

// past is which facts a read of payday's past takes, as [visible] takes the
// time rows: as known at a moment, every one ever written, or as known now.
func (v visible) past() past.Known {
	switch {
	case v.known != nil:
		return past.Known{At: v.known}
	case v.all:
		return past.Known{All: true}
	default:
		return past.Known{}
	}
}

// Timeline answers an asset's history: the time rows about it and the events
// that wrote them, newest first -- all of what is kept of it (design 8, D36).
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
		vis.known = &k
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

	// Its facts, from payday's past: each field it held, from when, by the
	// key Rove says it under (payday#151).
	st, err := t.store()
	if err != nil {
		return nil, err
	}
	sts, err := past.Facts(ctx, st, past.FactQuery{Subjects: []pdid.Id{id}, Known: vis.past()})
	if err != nil {
		return nil, err
	}
	for _, s := range sts {
		for _, r := range s.Rows {
			key, ok := KeyOf(r.Slot())
			if !ok {
				continue
			}
			text, err := textOf(r.Slot(), r.Value)
			if err != nil {
				return nil, fmt.Errorf("fact %s: %w", r.Id, err)
			}
			e := entry("fact", r.Id.Uuid(), r.From, nil, r.Recorded, r.Superseded, r.Event.Uuid())
			v := text
			switch {
			case r.Value == past.Null:
				v = "(지움)"
			case key == "type":
				v = names.kind(uuidOf(idFromString(text).Bytes()))
			case key == "model":
				v = names.model(uuidOf(idFromString(text).Bytes()))
			case key == "status":
				v = or(statusSay[v], v)
			case key == "condition":
				v = or(conditionSay[v], v)
			}
			name := factName(key)
			if l, ok := labels[key]; ok {
				name = l
			}
			e.SetSummary(fmt.Sprintf("%s = %s", name, v))
			e.SetDetail(map[string]string{"key": key, "value": text})
			entries = append(entries, e)
		}
	}

	// The events: what was done, by whom and why, including what wrote no
	// time row -- an acknowledgement, a label bound, a count. Read from the
	// past, which puts each read with a reason on the access record: why is
	// somebody's words (payday#151).
	evs, err := past.Events(ctx, st, past.EventQuery{Subjects: []pdid.Id{id}})
	if err != nil {
		return nil, err
	}
	byId := map[uuid.UUID]past.EventRow{}
	for _, v := range evs {
		byId[v.Id.Uuid()] = v
	}
	// Events time rows point at but that name another subject.
	var others []pdid.Id
	for _, e := range entries {
		k := uuidOf(e.GetEventId())
		if _, ok := byId[k]; ok || k == (uuid.UUID{}) {
			continue
		}
		others = append(others, pdid.Id(k))
	}
	// And what each was part of, when it was what an operation over many
	// things did to this one: the operation, in its own words (design D34).
	more, err := t.events(st, others)
	if err != nil {
		return nil, err
	}
	for _, v := range more {
		byId[v.Id.Uuid()] = v
	}
	var causes []pdid.Id
	for _, v := range byId {
		if _, ok := byId[v.Cause.Uuid()]; !ok && !v.Cause.IsZero() {
			causes = append(causes, v.Cause)
		}
	}
	more, err = t.events(st, causes)
	if err != nil {
		return nil, err
	}
	causeOf := map[uuid.UUID]past.EventRow{}
	for _, v := range more {
		causeOf[v.Id.Uuid()] = v
	}
	if err := names.before(byId, causeOf); err != nil {
		return nil, err
	}

	r := t.reading(&names)
	cause := func(v past.EventRow) string {
		if v.Cause.IsZero() {
			return ""
		}
		if c, ok := byId[v.Cause.Uuid()]; ok {
			return r.Say(c)
		}
		if c, ok := causeOf[v.Cause.Uuid()]; ok {
			return r.Say(c)
		}
		return ""
	}
	for _, e := range entries {
		if v, ok := byId[uuidOf(e.GetEventId())]; ok {
			e.SetEventKind(v.Kind)
			e.SetReason(v.Reason)
			e.SetActor(names.actorOf(v.Actor))
			e.SetCause(cause(v))
		}
	}
	for _, v := range evs {
		e := entry("event", v.Id.Uuid(), v.At, nil, v.Recorded, nil, v.Id.Uuid())
		e.SetSummary(r.Say(v))
		e.SetEventKind(v.Kind)
		e.SetReason(v.Reason)
		e.SetActor(names.actorOf(v.Actor))
		// Not when it says what the operation did: a hand-over's child says
		// "지급" of the asset as the hand-over says it of the person.
		if c := cause(v); c != e.GetSummary() {
			e.SetCause(c)
		}
		if l, ok := names.legacies[v.Id.Uuid()]; ok && l.payload != "" {
			e.SetDetail(decode(l.payload))
		}
		entries = append(entries, e)
	}
	if err := r.Err(); err != nil {
		return nil, err
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

// events reads the events `ids` names from the past, and nothing for none.
func (t *Tx) events(st past.Store, ids []pdid.Id) ([]past.EventRow, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	return past.Events(t.ctx, st, past.EventQuery{Ids: ids})
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

// QueryAt answers an asset and what was inside it at a moment.
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
	vis := visible{}
	if req.HasKnown() {
		k := req.GetKnown().AsTime()
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

// Diff answers what changed under an asset between two moments.
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
				row("overdue", pdid.Id(c.Id).String(), names.party(c.PartyId), t.now.Sub(*c.DueAt).Hours()/24, map[string]string{"due": c.DueAt.In(t.zone()).Format(time.DateOnly)})
			}
		}
		for k, n := range byParty {
			row("party", pdid.Id(k).String(), names.party(k), float64(n), nil)
		}

	case "utilization":
		from, to := t.now.AddDate(0, 0, -30), t.now
		if req.HasFrom() {
			from = req.GetFrom().AsTime()
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
		// The work still open, and what was done.
		ws, err := t.db.WorkOrder.Query().Where(workorder.TenantId(tid), workorder.DateErasedIsNil()).All(ctx)
		if err != nil {
			return nil, err
		}
		// What it cost, by the currency it was priced in (#144): a workspace
		// that began pricing in another has amounts that add up to nothing.
		byStatus, cost := map[string]int{}, map[string]int64{}
		for _, w := range ws {
			byStatus[w.Status]++
			if w.Cost != 0 {
				cost[w.Currency] += w.Cost
			}
		}
		for _, k := range sortedKeys(byStatus) {
			row("status", k, k, float64(byStatus[k]), nil)
		}
		for _, c := range sortedKeys(cost) {
			row("cost", c, "비용 합계", float64(cost[c]), map[string]string{"amount": money.String(cost[c], c)})
		}

	default:
		return nil, status.Error(codes.InvalidArgument, "보고서 종류가 올바르지 않습니다")
	}

	slices.SortStableFunc(rows, func(a, b *app.ReportRow) int { return strings.Compare(a.GetGroup(), b.GetGroup()) })
	return app.AssetReportResponse_builder{Rows: rows}.Build(), nil
}
