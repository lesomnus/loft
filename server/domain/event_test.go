package domain_test

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	"google.golang.org/protobuf/encoding/protojson"

	app "github.com/lesomnus/loft"
	"github.com/lesomnus/loft/internal/ent"
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
		"asset.assign":  "NB-1 소유: " + name,
		"custody.issue": "지급: " + name,
	}, pick(e.events(), "party.add", "asset.assign", "custody.issue"))
	x.Equal("NB-1 소유: "+name, summaryOf(e.timeline(laptop, false), "asset.assign"))

	// Renamed, they are called what they are called now.
	_, err = e.app().Party().Update(e.owner, app.PartyUpdateRequest_builder{Ref: ref[app.PartyRef](kim.GetId()), Name: "김 대리"}.Build())
	x.NoError(err)
	x.Equal("NB-1 소유: 김 대리", summaryOf(e.timeline(laptop, false), "asset.assign"))

	_, lee := e.person("member")

	v, err := e.app().Party().Pseudonymize(e.owner, app.PartyPseudonymizeRequest_builder{Ref: ref[app.PartyRef](kim.GetId())}.Build())
	x.NoError(err)
	pseudonym := v.GetName()
	x.True(strings.HasPrefix(pseudonym, "익명 "))
	w, err := e.app().Party().Pseudonymize(e.owner, app.PartyPseudonymizeRequest_builder{Ref: ref[app.PartyRef](lee.GetId())}.Build())
	x.NoError(err)
	// A pseudonym is "익명" and the start of the record's identifier, which
	// is when it was made: two records made close together can go by one,
	// and the history then tells them apart as a list of people does (D80)
	// -- by identifier, neither being in a team or having a 사번 any more.
	called := pseudonym
	if w.GetName() == pseudonym {
		called += " · " + idOf(kim.GetId()).String()
	}

	got := e.events()
	x.Equal(map[string]string{
		"party.update":  "수정: " + called,
		"asset.assign":  "NB-1 소유: " + called,
		"custody.issue": "지급: " + called,
	}, pick(got, "party.update", "asset.assign", "custody.issue"))
	x.Equal("NB-1 소유: "+called, summaryOf(e.timeline(laptop, false), "asset.assign"))
	for _, said := range e.said("party.add") {
		x.NotContains(said, name)
	}

	rows, err := e.s.Ent.Event.Query().All(ctx)
	x.NoError(err)
	x.True(slices.ContainsFunc(rows, func(r *ent.Event) bool {
		return r.Kind == "reservation.add" && r.CauseId == nil && sameId(value[*app.ReservationAdded](e, r).GetParty(), kim.GetId())
	}), "a reservation keeps who it is for by identifier")
	for _, r := range rows {
		x.NotNil(r.Value, "%s keeps no message", r.Kind)
		held := protojson.Format(r.Value)
		for _, said := range []string{name, "김 대리", alias, lee.GetName()} {
			x.NotContains(held, said, "an event row named them: %s", r.Kind)
		}
	}
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
