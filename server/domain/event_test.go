package domain_test

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	"google.golang.org/protobuf/types/known/timestamppb"

	app "github.com/lesomnus/rove"
	"github.com/lesomnus/rove/internal/ent"
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
		return r.Kind == "reservation.add" && strings.Contains(r.Payload, "for="+idOf(kim.GetId()).String())
	}), "a reservation keeps who it is for by identifier")
	for _, r := range rows {
		for _, said := range []string{name, "김 대리", alias, lee.GetName()} {
			x.NotContains(r.Desc, said, "an event row named them: %s", r.Kind)
			x.NotContains(r.Payload, said, "an event row named them: %s", r.Kind)
		}
	}
}

// TestAnEventWrittenWithNamesReadsAsWritten.
//
// An event from before events kept identifiers named people in its words; it
// is read as it was written, and so is a sentence that only looks like a mark.
func TestAnEventWrittenWithNamesReadsAsWritten(t *testing.T) {
	e := newEnv(t)
	x := e.x

	laptop := e.item("노트북", "NB-1", nil, 24*time.Hour)
	for _, v := range []struct{ kind, desc, payload string }{
		{"party.role", "역할 변경: Kim → admin", ""},
		{"party.update", "수정: {party}", "party=Kim"},
	} {
		_, err := e.s.Base.Event().Add(context.Background(), app.EventAddRequest_builder{
			Tenant:     app.TenantRef_builder{Id: e.tenant.Bytes()}.Build(),
			Kind:       v.kind,
			Desc:       v.desc,
			Payload:    v.payload,
			OccurredAt: timestamppb.New(e.now),
		}.Build())
		x.NoError(err)
	}
	_, err := e.s.Base.Event().Add(context.Background(), app.EventAddRequest_builder{
		Tenant:     app.TenantRef_builder{Id: e.tenant.Bytes()}.Build(),
		Kind:       "asset.assign",
		Desc:       "NB-1 owner: Kim",
		Payload:    "party=Kim\nrole=owner",
		OccurredAt: timestamppb.New(e.now),
		SubjectId:  laptop.GetId(),
	}.Build())
	x.NoError(err)

	x.Equal(map[string]string{
		"party.role":   "역할 변경: Kim → admin",
		"party.update": "수정: {party}",
		"asset.assign": "NB-1 owner: Kim",
	}, pick(e.events(), "party.role", "party.update", "asset.assign"))
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
