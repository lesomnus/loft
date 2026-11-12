package domain_test

import (
	"context"
	"testing"

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

// TestAPersonForgottenHereEndsTheMembership, and not their holder at roster:
// who is a member, and how they sign in, is roster's (D87), and that is the
// tenant's administrator's there to end.
func TestAPersonForgottenHereEndsTheMembership(t *testing.T) {
	e := newEnv(t)
	x := e.x
	_, kim := e.person("member")
	who := idOf(kim.GetHolder().GetId())

	_, err := e.app().Party().Pseudonymize(e.owner, app.PartyPseudonymizeRequest_builder{Ref: ref[app.PartyRef](kim.GetId())}.Build())
	x.NoError(err)

	live, err := e.s.Ent.Holder.Query().Where(holder.Id(who.Uuid()), holder.DateErasedIsNil()).Exist(context.Background())
	x.NoError(err)
	x.False(live, "the membership outlived the person")
	x.True(e.signsIn("p1", "password1234"), "the holder at roster went with the membership")
}
