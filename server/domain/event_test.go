package domain_test

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/lesomnus/payday/pdid"
	"google.golang.org/protobuf/encoding/protojson"

	app "github.com/lesomnus/rove"
	"github.com/lesomnus/rove/internal/ent"
	"github.com/lesomnus/rove/internal/ent/holder"
	"github.com/lesomnus/rove/internal/ent/party"
	"github.com/lesomnus/rove/server/pd"
)

func (e *env) events() map[string]string {
	v, err := e.app().Event().Recent(e.owner, app.EventRecentRequest_builder{Size: 500}.Build())
	e.x.NoError(err)
	out := map[string]string{}
	for _, ev := range v.GetItems() {
		// The newest of a kind.
		if _, ok := out[ev.GetKind()]; !ok {
			out[ev.GetKind()] = ev.GetDesc()
		}
	}
	return out
}

// said is what the history says of every event of these kinds.
func (e *env) said(kinds ...string) []string {
	v, err := e.app().Event().Recent(e.owner, app.EventRecentRequest_builder{Size: 500}.Build())
	e.x.NoError(err)
	out := []string{}
	for _, ev := range v.GetItems() {
		if slices.Contains(kinds, ev.GetKind()) {
			out = append(out, ev.GetDesc())
		}
	}
	return out
}

func summaryOf(es []*app.TimelineEntry, kind string) string {
	for _, en := range es {
		if en.GetKind() == "event" && en.GetEventKind() == kind {
			return en.GetSummary()
		}
	}
	return ""
}

// TestTheHistoryNamesPeopleAsTheyAreNow.
//
// An event keeps who it was about by identifier and names them when it is
// read (design 8.2): as they are called now while they are here, and by
// their pseudonym once they are forgotten -- so forgetting somebody reaches
// what the history says of them, and no event row ever held their name.
func TestTheHistoryNamesPeopleAsTheyAreNow(t *testing.T) {
	e := newEnv(t)
	x := e.x
	ctx := context.Background()

	store := e.space("창고", nil)
	laptop := e.item("노트북", "NB-1", store, 24*time.Hour)
	room := e.space("회의실", nil)
	e.bookable(room, nil)
	kimCtx, kim := e.person("member")
	_, err := e.reserve(kimCtx, room, e.tomorrow(10, 0), e.tomorrow(11, 0), nil)
	x.NoError(err)
	_, err = e.app().Party().SetRole(e.owner, app.PartySetRoleRequest_builder{Ref: ref[app.PartyRef](kim.GetId()), Role: "manager"}.Build())
	x.NoError(err)
	_, err = e.app().Asset().Assign(e.owner, app.AssetAssignRequest_builder{Ref: assetRef(laptop), Role: "owner", Party: ref[app.PartyRef](kim.GetId())}.Build())
	x.NoError(err)
	_, err = e.app().Custody().Add(e.owner, app.CustodyAddRequest_builder{
		Party: ref[app.PartyRef](kim.GetId()),
		Lines: []*app.CustodyLineSpec{app.CustodyLineSpec_builder{Asset: assetRef(laptop)}.Build()},
	}.Build())
	x.NoError(err)

	name := kim.GetName()
	alias := "@" + e.holderRow(kim.GetHolder().GetId()).Alias
	x.Equal(map[string]string{
		"party.add":     "추가: " + name,
		"party.invite":  "로그인 발급: " + name + " (" + alias + ")",
		"party.role":    "역할 변경: " + name + " → manager",
		"asset.assign":  "NB-1 owner: " + name,
		"custody.issue": "지급: " + name,
	}, pick(e.events(), "party.add", "party.invite", "party.role", "asset.assign", "custody.issue"))
	x.Equal("NB-1 owner: "+name, summaryOf(e.timeline(laptop, false), "asset.assign"))

	// Renamed, they are called what they are called now.
	_, err = e.app().Party().Update(e.owner, app.PartyUpdateRequest_builder{Ref: ref[app.PartyRef](kim.GetId()), Name: "김 대리"}.Build())
	x.NoError(err)
	x.Equal("역할 변경: 김 대리 → manager", e.events()["party.role"])

	// A login ended is still theirs to read about.
	_, lee := e.person("member")
	_, err = e.app().Party().Deactivate(e.owner, app.PartyDeactivateRequest_builder{Ref: ref[app.PartyRef](lee.GetId())}.Build())
	x.NoError(err)
	x.Equal("로그인 비활성화: "+lee.GetName(), e.events()["party.deactivate"])

	v, err := e.app().Party().Pseudonymize(e.owner, app.PartyPseudonymizeRequest_builder{Ref: ref[app.PartyRef](kim.GetId())}.Build())
	x.NoError(err)
	pseudonym := v.GetName()
	x.True(strings.HasPrefix(pseudonym, "익명 "))
	_, err = e.app().Party().Pseudonymize(e.owner, app.PartyPseudonymizeRequest_builder{Ref: ref[app.PartyRef](lee.GetId())}.Build())
	x.NoError(err)

	got := e.events()
	x.Equal(map[string]string{
		"party.update":  "수정: " + pseudonym,
		"party.role":    "역할 변경: " + pseudonym + " → manager",
		"asset.assign":  "NB-1 owner: " + pseudonym,
		"custody.issue": "지급: " + pseudonym,
	}, pick(got, "party.update", "party.role", "asset.assign", "custody.issue"))
	x.True(strings.HasPrefix(got["party.deactivate"], "로그인 비활성화: 익명 "), got["party.deactivate"])
	x.Equal("NB-1 owner: "+pseudonym, summaryOf(e.timeline(laptop, false), "asset.assign"))
	for _, said := range e.said("party.add", "party.invite") {
		x.NotContains(said, name)
		x.NotContains(said, alias)
	}
	x.Contains(e.said("party.invite"), "로그인 발급: "+pseudonym+" ("+pseudonym+")")

	rows, err := e.s.Ent.Event.Query().All(ctx)
	x.NoError(err)
	x.True(slices.ContainsFunc(rows, func(r *ent.Event) bool {
		return r.Kind == "reservation.add" && r.CauseId == nil && sameId(value[*app.ReservationAdded](e, r).GetParty(), kim.GetId())
	}), "a reservation keeps who it is for by identifier")
	for _, r := range rows {
		x.NotNil(r.Value, "%s keeps no message", r.Kind)
		x.Empty(r.Desc, "%s keeps a sentence", r.Kind)
		x.Empty(r.Payload, "%s keeps a payload", r.Kind)
		held := protojson.Format(r.Value)
		for _, said := range []string{name, "김 대리", alias, lee.GetName()} {
			x.NotContains(held, said, "an event row named them: %s", r.Kind)
		}
	}
}

// TestAnEventWrittenWithNamesReadsAsWritten.
//
// An event from before events kept identifiers named people in its words; it
// is read as it was written, and so is a sentence that only looks like a mark.
// A mark names nobody of another workspace's, whatever its payload says.
func TestAnEventWrittenWithNamesReadsAsWritten(t *testing.T) {
	e := newEnv(t)
	x := e.x
	ctx := context.Background()

	laptop := e.item("노트북", "NB-1", nil, 24*time.Hour)
	// As one from before Rove's events were payday's past was written: no
	// message, its sentence and what it named beside it. Beneath the
	// servers, which write the past's rows only as the past says them.
	add := func(kind, desc, payload string, subject []byte) {
		q := e.s.Ent.Event.Create().
			SetId(pdid.New(pd.EventDomain).Uuid()).
			SetTenantId(e.tenant.Uuid()).
			SetKind(kind).
			SetDesc(desc).
			SetPayload(payload).
			SetReason("").
			SetOccurredAt(e.now).
			SetDateCreated(e.now).
			SetDateUpdated(e.now)
		if subject != nil {
			q.SetSubjectId(idUUID(subject))
		}
		x.NoError(q.Exec(ctx))
	}
	add("party.role", "역할 변경: Kim → admin", "", nil)
	add("party.update", "수정: {party}", "party=Kim", nil)
	add("asset.assign", "NB-1 owner: Kim", "party=Kim\nrole=owner", laptop.GetId())

	beta, _ := e.newTenant("beta")
	h, err := e.s.Ent.Holder.Query().Where(holder.TenantId(beta.Uuid())).Only(ctx)
	x.NoError(err)
	p, err := e.s.Ent.Party.Query().Where(party.TenantId(beta.Uuid())).Only(ctx)
	x.NoError(err)
	add("party.invite", "로그인 발급: {party} ({alias}), {holder}", "holder="+pdid.Id(h.Id).String()+"\nparty="+pdid.Id(p.Id).String(), nil)

	x.Equal(map[string]string{
		"party.role":   "역할 변경: Kim → admin",
		"party.update": "수정: {party}",
		"asset.assign": "NB-1 owner: Kim",
		"party.invite": "로그인 발급: ? (?), ?",
	}, pick(e.events(), "party.role", "party.update", "asset.assign", "party.invite"))
	x.Equal("NB-1 owner: Kim", summaryOf(e.timeline(laptop, false), "asset.assign"))
}

func pick(m map[string]string, keys ...string) map[string]string {
	out := map[string]string{}
	for _, k := range keys {
		if v, ok := m[k]; ok {
			out[k] = v
		}
	}
	return out
}
