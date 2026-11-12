package domain_test

import (
	"context"
	"testing"
	"time"

	"github.com/lesomnus/payday/pdid"

	app "github.com/lesomnus/loft"
	"github.com/lesomnus/loft/internal/ent/holder"
	"github.com/lesomnus/loft/internal/identity"
)

// signsIn is whether `login` gets in at the roster with `password`.
func (e *env) signsIn(login, password string) bool {
	_, err := e.s.Identity.Verify(context.Background(), login, password)
	if err != nil {
		e.x.ErrorIs(err, identity.ErrRefused, "not a refusal: %v", err)
	}
	return err == nil
}

// accountOf is who a party's login is at roster: the membership's own
// identifier, which is their holder there (D85).
func (e *env) accountOf(p *app.Party) pdid.Id {
	return pdid.Id(e.holderRow(p.GetHolder().GetId()).Id)
}

// TestAPasswordIsRosters.
//
// What the people screen does to a password it does at the roster in this
// process: one's own needs the current one, and a reset by somebody else ends
// where they were signed in.
func TestAPasswordIsRosters(t *testing.T) {
	e := newEnv(t)
	x := e.x
	kimCtx, kim := e.person("member")
	login := "p1"
	x.True(e.signsIn(login, "password1234"))

	_, err := e.app().Party().SetPassword(kimCtx, app.PartySetPasswordRequest_builder{Current: "wrong-password", Password: "another1234"}.Build())
	x.Error(err, "a password changed without the current one")
	_, err = e.app().Party().SetPassword(kimCtx, app.PartySetPasswordRequest_builder{Current: "password1234", Password: "another1234"}.Build())
	x.NoError(err)
	x.False(e.signsIn(login, "password1234"))
	x.True(e.signsIn(login, "another1234"))

	began := time.Now().Add(-time.Second)
	_, err = e.app().Party().SetPassword(e.owner, app.PartySetPasswordRequest_builder{Ref: ref[app.PartyRef](kim.GetId()), Password: "reset-by-owner"}.Build())
	x.NoError(err)
	x.True(e.signsIn(login, "reset-by-owner"))
	st, err := e.s.Identity.StandingOf(context.Background(), e.accountOf(kim))
	x.NoError(err)
	x.False(st.Good(began), "a reset that left them signed in at roster")
}

// TestALoginEndedHereIsTheMembership.
//
// Stopping a login or forgetting a person ends their membership of this
// workspace, and not their holder at roster: that is the tenant's
// administrator's there to end. Ended here, a sign-in by it is refused here
// all the same (`tenancy.ErrEnded`).
func TestALoginEndedHereIsTheMembership(t *testing.T) {
	for _, end := range []string{"deactivate", "pseudonymize"} {
		t.Run(end, func(t *testing.T) {
			e := newEnv(t)
			x := e.x
			_, kim := e.person("member")
			who := e.accountOf(kim)

			r := ref[app.PartyRef](kim.GetId())
			var err error
			switch end {
			case "deactivate":
				_, err = e.app().Party().Deactivate(e.owner, app.PartyDeactivateRequest_builder{Ref: r}.Build())
			case "pseudonymize":
				_, err = e.app().Party().Pseudonymize(e.owner, app.PartyPseudonymizeRequest_builder{Ref: r}.Build())
			}
			x.NoError(err)

			live, err := e.s.Ent.Holder.Query().Where(holder.Id(who.Uuid()), holder.DateErasedIsNil()).Exist(context.Background())
			x.NoError(err)
			x.False(live, "the membership outlived the login")
			x.True(e.signsIn("p1", "password1234"), "the holder at roster went with the membership")
		})
	}
}
