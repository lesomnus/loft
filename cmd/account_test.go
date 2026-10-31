package cmd_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/lesomnus/z"

	app "github.com/lesomnus/loft"
	"github.com/lesomnus/loft/cmd"
	"github.com/lesomnus/loft/internal/identity"
	"github.com/lesomnus/loft/server/account"
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

// TestAnAccountIsToldTheDeploymentsZone (design 7.9): what is answered of the
// account says the deployment's zone -- Asia/Seoul while `app.locale` says
// none, and the one it says otherwise -- which a page about no workspace, the
// account's payments first, draws its dates in rather than the browser's. A
// workspace's own zone is the workspace's pages', and moves none of it.
func TestAnAccountIsToldTheDeploymentsZone(t *testing.T) {
	read := func(w *world) account.Account {
		rec := w.page(w.owner, http.MethodGet, "/account")
		w.x.Equal(http.StatusOK, rec.Code, rec.Body.String())
		var got account.Account
		w.x.NoError(json.Unmarshal(rec.Body.Bytes(), &got))
		return got
	}

	w := newWorld(t)
	x := w.x
	x.Equal("Asia/Seoul", read(w).Timezone, "the pilot's, while the deployment names none")

	_, err := app.NewTenantServiceClient(w.conn).Configure(w.owner, app.TenantConfigureRequest_builder{
		Ref:      app.TenantRef_builder{Id: w.tenant.GetId()}.Build(),
		Timezone: z.Ptr("Europe/Paris"),
	}.Build())
	x.NoError(err)
	me, err := app.NewPartyServiceClient(w.conn).Me(w.owner, &app.PartyMeRequest{})
	x.NoError(err)
	x.Equal("Europe/Paris", me.GetTimezone(), "the workspace's pages are on its own wall")
	x.Equal("Asia/Seoul", read(w).Timezone, "and the account's on the deployment's")

	t.Run("one the deployment names", func(t *testing.T) {
		w := newWorldWith(t, func(c *cmd.Config) { c.App.Locale.Timezone = "America/Los_Angeles" })
		w.x.Equal("America/Los_Angeles", read(w).Timezone)
		me, err := app.NewPartyServiceClient(w.conn).Me(w.owner, &app.PartyMeRequest{})
		w.x.NoError(err)
		w.x.Equal("America/Los_Angeles", me.GetTimezone(), "the zone a workspace that says none of its own is told in")
	})
}
