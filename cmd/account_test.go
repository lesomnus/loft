package cmd_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	app "github.com/lesomnus/rove"
	"github.com/lesomnus/rove/internal/identity"
	"github.com/lesomnus/rove/server/account"
)

// paged is the roster in this process with an account page, which only a
// roster of its own has (`auth.roster.account_url`).
type paged struct {
	*identity.Store
	page string
}

func (p paged) AccountPage() string { return p.page }

// TestAnAccountIsToldWhereItIsManaged: with a roster of its own, the page where
// an account changes how it signs in -- and deletes itself, where its
// directory offers that (roster#106) -- is roster's, and what a workspace
// shows of the person and what the app shows of the account both say where.
func TestAnAccountIsToldWhereItIsManaged(t *testing.T) {
	w := newWorld(t)
	x := w.x
	parties := app.NewPartyServiceClient(w.conn)

	me, err := parties.Me(w.owner, &app.PartyMeRequest{})
	x.NoError(err)
	x.Empty(me.GetAccountUrl(), "the roster in this process has no page of its own")

	w.s.Deps.People = paged{w.s.Identity, "https://id.example/"}
	me, err = parties.Me(w.owner, &app.PartyMeRequest{})
	x.NoError(err)
	x.Equal("https://id.example/", me.GetAccountUrl())

	mux := http.NewServeMux()
	account.Routes{Sessions: w.s.Sessions, People: w.s.Identity, Workspaces: w.s.Workspaces(w.c), Manage: "https://id.example/"}.Mount(mux)
	r := httptest.NewRequest(http.MethodGet, "/account", nil)
	r.Header.Set("Cookie", cookieOf(w.owner))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, r)
	x.Equal(http.StatusOK, rec.Code)
	var got account.Account
	x.NoError(json.Unmarshal(rec.Body.Bytes(), &got))
	x.Equal("https://id.example/", got.Manage)
}
