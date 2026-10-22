package cmd_test

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/lesomnus/payday/auth/authsession"
	"github.com/lesomnus/payday/pdid"

	"github.com/lesomnus/rove/cmd"
	"github.com/lesomnus/rove/server/account"
)

// TestACallSaysWhoseStoreItIsFor: `GET /account` names the session asking as
// the server reads it (`authsession.IdentityOf`), the page says that name on
// every call it makes, and a call saying it is served as that caller and
// nobody else. A page left behind by somebody else's sign-in in the same
// browser -- the first person's name, the second person's cookie -- is
// refused as `Unauthenticated`, where it would otherwise be served as the
// second person. A second sign-in of the same person is the same name, which
// is what lets what the page kept for them wait for it. On a workspace's own
// domain the same, and a call naming no store is served as before.
func TestACallSaysWhoseStoreItIsFor(t *testing.T) {
	w := newHosted(t)
	x := w.x
	ctx := context.Background()

	kim, err := w.s.Identity.AddPerson(ctx, "kim", "Kim", pdid.Nil)
	x.NoError(err)
	lee, err := w.s.Identity.AddPerson(ctx, "lee", "Lee", pdid.Nil)
	x.NoError(err)
	inKim, inLee := w.here(kim, "member"), w.here(lee, "member")
	const service, own = "rove.example", "assets.acme.example"
	w.domain(w.tenant, own, "app", "active")

	named := func(as context.Context, host string) string {
		res := w.at(as, host, http.MethodGet, "/account", "")
		x.Equal(http.StatusOK, res.Code, res.Body.String())
		var a account.Account
		x.NoError(json.Unmarshal(res.Body.Bytes(), &a))
		x.NotEmpty(a.Identity)
		return a.Identity
	}
	me := func(as context.Context, host, store string) int {
		h := []string{"Content-Type", "application/json", "Connect-Protocol-Version", "1", cmd.WorkspaceHeader, "acme"}
		if store != "" {
			h = append(h, authsession.IdentityHeader, store)
		}
		return w.at(as, host, http.MethodPost, "/rove.PartyService/Me", "{}", h...).Code
	}

	asKim, asLee := w.session(inKim), w.session(inLee)
	kims, lees := named(asKim, service), named(asLee, service)
	x.NotEqual(kims, lees)
	x.Equal(kims, named(w.session(inKim), service), "a sign-in again is the same store")

	x.Equal(http.StatusOK, me(asKim, service, kims), "a call for the caller's own store")
	x.Equal(http.StatusUnauthorized, me(asLee, service, kims), "one for somebody else's store, with this browser's cookie")
	x.Equal(http.StatusOK, me(asLee, service, ""), "and one naming no store, as before")

	// On a workspace's own domain, which hands the header on as it is.
	x.Equal(kims, named(asKim, own))
	x.Equal(http.StatusOK, me(asKim, own, kims))
	x.Equal(http.StatusUnauthorized, me(asLee, own, kims))
}
