package domain_test

import (
	"context"
	"testing"
	"time"

	"github.com/lesomnus/payday/pdid"
	"github.com/lesomnus/z"

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

// accountOf is the account a party's login is a membership of.
func (e *env) accountOf(p *app.Party) pdid.Id {
	h, err := e.s.Ent.Holder.Get(context.Background(), idUUID(p.GetHolder().GetId()))
	e.x.NoError(err)
	e.x.NotNil(h.Account, "a login that is no account's")
	return pdid.Id(*h.Account)
}

// TestAPasswordIsTheAccounts.
//
// What the people screen does to a password it does to the account, at the
// roster in this process: one's own needs the current one, and a reset by
// somebody else ends where they were signed in. A reset is refused for an
// account that is in another workspace too, which is not this workspace's to
// lock out.
func TestAPasswordIsTheAccounts(t *testing.T) {
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
	account := e.accountOf(kim)
	st, err := e.s.Identity.StandingOf(context.Background(), account)
	x.NoError(err)
	x.False(st.Good(began), "a reset that left them signed in at roster")

	t.Run("and an account in another workspace is not this one's to reset", func(t *testing.T) {
		beta, _ := e.newTenant("beta")
		_, err := e.s.Base.Holder().Add(context.Background(), app.HolderAddRequest_builder{
			Tenant:  app.TenantRef_builder{Id: beta.Bytes()}.Build(),
			Alias:   login,
			Name:    "Kim",
			Role:    z.Ptr("member"),
			Account: account.Bytes(),
		}.Build())
		x.NoError(err)

		_, err = e.app().Party().SetPassword(e.owner, app.PartySetPasswordRequest_builder{Ref: ref[app.PartyRef](kim.GetId()), Password: "reset-again"}.Build())
		x.ErrorContains(err, "다른 워크스페이스")
		x.True(e.signsIn(login, "reset-by-owner"))
	})
}

// TestALoginEndedHereIsTheMembership.
//
// Stopping a login or forgetting a person ends their membership of this
// workspace, and not their account: that is theirs, and may be somebody else's
// member too (D27).
func TestALoginEndedHereIsTheMembership(t *testing.T) {
	for _, end := range []string{"deactivate", "pseudonymize"} {
		t.Run(end, func(t *testing.T) {
			e := newEnv(t)
			x := e.x
			_, kim := e.person("member")
			account := e.accountOf(kim)

			r := ref[app.PartyRef](kim.GetId())
			var err error
			switch end {
			case "deactivate":
				_, err = e.app().Party().Deactivate(e.owner, app.PartyDeactivateRequest_builder{Ref: r}.Build())
			case "pseudonymize":
				_, err = e.app().Party().Pseudonymize(e.owner, app.PartyPseudonymizeRequest_builder{Ref: r}.Build())
			}
			x.NoError(err)

			live, err := e.s.Ent.Holder.Query().Where(holder.Account(account.Uuid()), holder.DateErasedIsNil()).Exist(context.Background())
			x.NoError(err)
			x.False(live, "the membership outlived the login")
			x.True(e.signsIn("p1", "password1234"), "the account went with the membership")
		})
	}
}
