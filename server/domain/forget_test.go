package domain_test

import (
	"context"
	"slices"
	"strings"
	"testing"
	"uuid"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	app "github.com/lesomnus/rove"
	"github.com/lesomnus/rove/cmd"
	"github.com/lesomnus/rove/internal/ent/audit"
	"github.com/lesomnus/rove/internal/ent/event"
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
// go with the person; that the writes happened stays.
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

// TestAPersonWhoseLoginWasStoppedFirstIsForgottenToo (rove#25, decided in
// rove#35). Stopping a login takes the person record's link to its membership
// away, and 개인정보 삭제 found the membership through that link and nothing
// else: the membership's login name and name stayed on it after, and the
// trail's copies of it. The membership now keeps which record it was.
func TestAPersonWhoseLoginWasStoppedFirstIsForgottenToo(t *testing.T) {
	e := newEnv(t)
	x := e.x
	_, lee := e.person("member")
	h := lee.GetHolder().GetId()
	alias, name := e.holderRow(h).Alias, lee.GetName()
	x.NotEmpty(alias)

	_, err := e.app().Party().Deactivate(e.owner, app.PartyDeactivateRequest_builder{Ref: ref[app.PartyRef](lee.GetId())}.Build())
	x.NoError(err)
	row := e.holderRow(h)
	x.NotNil(row.Party, "the membership does not say which record it was")
	x.Equal(idUUID(lee.GetId()), *row.Party)
	full, _ := e.copies(h)
	x.Positive(full, "the trail kept nothing of the membership to begin with")

	_, err = e.app().Party().Pseudonymize(e.owner, app.PartyPseudonymizeRequest_builder{Ref: ref[app.PartyRef](lee.GetId())}.Build())
	x.NoError(err)

	row = e.holderRow(h)
	x.Empty(row.Alias, "the login name stayed on the membership")
	x.NotEqual(name, row.Name)
	x.True(strings.HasPrefix(row.Name, "익명 "), row.Name)
	full, _ = e.copies(lee.GetId(), h)
	x.Zero(full, "a copy of the person or their membership in the trail")

	t.Run("and one stopped and given again is two memberships, both forgotten", func(t *testing.T) {
		x := require.New(t)

		_, park := e.person("member")
		first := park.GetHolder().GetId()
		_, err := e.app().Party().Deactivate(e.owner, app.PartyDeactivateRequest_builder{Ref: ref[app.PartyRef](park.GetId())}.Build())
		x.NoError(err)
		v, err := e.app().Party().Invite(e.owner, app.PartyInviteRequest_builder{
			Ref:      ref[app.PartyRef](park.GetId()),
			Alias:    "park-again",
			Role:     "member",
			Password: "password1234",
		}.Build())
		x.NoError(err)
		second := v.GetHolder().GetId()
		x.NotEqual(first, second)

		_, err = e.app().Party().Pseudonymize(e.owner, app.PartyPseudonymizeRequest_builder{Ref: ref[app.PartyRef](park.GetId())}.Build())
		x.NoError(err)
		for _, h := range [][]byte{first, second} {
			row := e.holderRow(h)
			x.Empty(row.Alias)
			x.True(strings.HasPrefix(row.Name, "익명 "), row.Name)
			x.NotNil(row.DateErased)
		}
		full, _ := e.copies(park.GetId(), first, second)
		x.Zero(full)
	})

	t.Run("and no request says which record a membership is", func(t *testing.T) {
		x := require.New(t)

		_, err := e.app().Holder().Add(e.owner, app.HolderAddRequest_builder{
			Tenant: app.TenantRef_builder{Id: e.tenant.Bytes()}.Build(),
			Alias:  "made-up",
			Name:   "Made up",
			Party:  lee.GetId(),
		}.Build())
		x.Equal(codes.InvalidArgument, status.Code(err), "%v", err)
	})
}
