package domain_test

import (
	"context"
	"slices"
	"strings"
	"testing"
	"uuid"

	app "github.com/lesomnus/loft"
	"github.com/lesomnus/loft/cmd"
	"github.com/lesomnus/loft/internal/ent/audit"
	"github.com/lesomnus/loft/internal/ent/event"
)

// copies is how many of the trail's rows about these objects still hold what
// they said, and how many there are.
func (e *env) copies(ids ...[]byte) (full, all int) {
	us := []uuid.UUID{}
	for _, v := range ids {
		us = append(us, idUUID(v))
	}
	vs, err := e.s.Ent.Audit.Query().Where(audit.ObjectIdIn(us...)).All(context.Background())
	e.x.NoError(err)
	for _, v := range vs {
		if len(v.Value) > 0 || len(v.Patch) > 0 {
			full++
		}
	}
	return full, len(vs)
}

// TestAPersonForgottenIsForgottenInTheTrail.
//
// The trail kept the person's row, and their login's, after every write. They
// go with the person; that the writes happened stays. Their membership ends,
// and the name it signs in with and is called by goes with it.
func TestAPersonForgottenIsForgottenInTheTrail(t *testing.T) {
	e := newEnv(t)
	x := e.x
	_, kim := e.person("member")
	h := kim.GetHolder().GetId()

	full, all := e.copies(kim.GetId(), h)
	x.Positive(full, "the trail kept nothing of the person to begin with")

	v, err := e.app().Party().Pseudonymize(e.owner, app.PartyPseudonymizeRequest_builder{Ref: ref[app.PartyRef](kim.GetId())}.Build())
	x.NoError(err)
	x.True(strings.HasPrefix(v.GetName(), "익명 "))

	full, after := e.copies(kim.GetId(), h)
	x.Zero(full, "a copy of the person in the trail")
	x.GreaterOrEqual(after, all, "the record that the writes happened went with them")

	row := e.holderRow(h)
	x.NotNil(row.DateErased, "the membership did not end")
	x.Empty(row.Alias, "the login name stayed on the membership")
	x.True(strings.HasPrefix(row.Name, "익명 "), row.Name)
}

// TestTheTrailHoldsNoValueOfAPerson: what identifies or describes a person --
// what they are called, how they are reached, the number they go by -- is
// declared personal (payday#92), so the trail records that it was set and
// never what to, even where it keeps rows. The forgetting above blanks what is
// left; this is what was never there. (A membership's login is payday's own
// declaration, and payday's test.)
func TestTheTrailHoldsNoValueOfAPerson(t *testing.T) {
	e := newEnvWith(t, func(c *cmd.Config) { c.Audit.Content = "value" })
	x := e.x
	_, kim := e.person("member")

	_, err := e.app().Party().Update(e.owner, app.PartyUpdateRequest_builder{
		Ref:   ref[app.PartyRef](kim.GetId()),
		Name:  "김다시",
		Email: "kim.again@example.com",
		Phone: "010-9876-5432",
		Code:  "EMP-0042",
	}.Build())
	x.NoError(err)

	vs, err := e.s.Ent.Audit.Query().All(context.Background())
	x.NoError(err)
	rows := 0
	for _, v := range vs {
		for _, b := range [][]byte{v.Patch, v.Value} {
			for _, said := range []string{
				kim.GetName(), kim.GetEmail(), // what it was made with
				"김다시", "kim.again@example.com", "010-9876-5432", "EMP-0042",
			} {
				x.NotEmpty(said)
				x.NotContains(string(b), said, "the trail holds a value of the person")
			}
		}
		if len(v.Value) > 0 {
			rows++
		}
	}
	x.Positive(rows, "the trail kept no rows, so the checks above never looked at one")

	// And that they were set, by whom and when, is still the record.
	set := false
	for _, v := range vs {
		if string(v.ObjectId[:]) == string(kim.GetId()) && slices.Contains(v.Fields, 11) && slices.Contains(v.Fields, 16) {
			set = true
		}
	}
	x.True(set, "nothing says the address and the number were set")
}

// TestAHoldKeepsWhatTheTrailSaidOfAPerson.
//
// A legal claim outweighs an erasure request: the person's row is forgotten,
// the trail's copies a hold is on are not, and the event says so.
func TestAHoldKeepsWhatTheTrailSaidOfAPerson(t *testing.T) {
	e := newEnv(t)
	x := e.x
	_, kim := e.person("member")

	_, err := e.s.Base.LegalHold().Add(e.owner, app.LegalHoldAddRequest_builder{
		Tenant: app.TenantRef_builder{Id: e.tenant.Bytes()}.Build(),
		Name:   "사건 1",
	}.Build())
	x.NoError(err)

	before, _ := e.copies(kim.GetId())
	_, err = e.app().Party().Pseudonymize(e.owner, app.PartyPseudonymizeRequest_builder{Ref: ref[app.PartyRef](kim.GetId())}.Build())
	x.NoError(err)

	full, _ := e.copies(kim.GetId())
	x.GreaterOrEqual(full, before, "a hold is on what the trail said")
	ev, err := e.s.Ent.Event.Query().Where(event.Kind("party.pseudonymize")).Only(context.Background())
	x.NoError(err)
	x.Positive(value[*app.PartyPseudonymized](e, ev).GetHeld())
}
