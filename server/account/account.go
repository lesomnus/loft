// Package account is what a signed-in session is told of itself beside
// `/session`: the account it is (design 9.11, D28) -- the page's first
// question, asked before it knows which workspace to call.
//
// The rest of what an account has outside any one workspace is the account's
// own RPCs (D73): the workspaces it is in and a new one
// (`TenantService.Mine`, `Create`).
package account

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"

	"github.com/lesomnus/otx/log"
	"github.com/lesomnus/payday/auth"
	"github.com/lesomnus/payday/auth/authsession"
	"github.com/lesomnus/payday/frame"

	"github.com/lesomnus/loft/internal/identity"
)

// People is the account a session names, at roster.
type People interface {
	ById(ctx context.Context, sub string) (identity.Person, error)
}

// Account is what `GET /account` answers.
type Account struct {
	Id    string `json:"id"`
	Login string `json:"login"`
	Name  string `json:"name"`
	// Manage is where the account itself is managed at roster, and deleted
	// where its directory offers that; absent for no such page.
	Manage string `json:"manage,omitempty"`
	// Allow is the methods the session asking is narrowed to (`loft auth login
	// --allow`, D33); absent is all that the account may do.
	Allow []string `json:"allow,omitempty"`
	// Billing says this deployment's workspaces are billed, by levyan: the
	// page offers a workspace's billing (design 7.9). Absent for one that
	// bills nobody.
	Billing bool `json:"billing,omitempty"`
	// Timezone is the deployment's zone (`app.locale.timezone`, Asia/Seoul
	// unset), as an IANA name: where a page about no workspace -- the
	// account's invitations -- draws its dates, as a workspace's pages draw
	// theirs on its own wall (`PartyService.Me`) and never on the browser's
	// (design 7.9).
	Timezone string `json:"timezone,omitempty"`
	// Identity is what the session asking lets its caller see, named as
	// `authsession.IdentityOf` names it: what the page keeps what it was
	// answered and what it has not sent yet for, and says on every call it
	// makes (`payday-identity`). A call saying it is served only as a
	// session of that name (`authsession.Sessions.Handler`), so a page left
	// behind by somebody else's sign-in in this browser is refused, rather
	// than served as them.
	Identity string `json:"identity"`
}

// Routes is what is served for an account beside `/session`.
type Routes struct {
	Sessions *authsession.Sessions
	People   People
	// Manage is roster's account page (`auth.roster.account_url`).
	Manage string

	// Billing says this deployment's workspaces are billed (`app.billing.
	// levyan`): what the page draws a workspace's billing for (design 7.9).
	Billing bool

	// Zone is the deployment's zone, by its IANA name (`domain.Locale.Zone`):
	// what the account is told its pages outside every workspace draw their
	// dates in. Empty tells it none, and the page keeps the browser's.
	Zone string
}

// Mount serves `GET /account`.
func (a Routes) Mount(mux *http.ServeMux) {
	sessions, people := a.Sessions, a.People
	mux.HandleFunc("GET /account", func(w http.ResponseWriter, r *http.Request) {
		p, s, ok := reading(w, r, sessions, people)
		if !ok {
			return
		}
		v := Account{Id: p.Id.String(), Login: p.Alias, Name: p.Name, Manage: a.Manage, Billing: a.Billing, Timezone: a.Zone}
		if !s.Grant.AnyAction() {
			v.Allow = s.Grant.Actions()
		}
		v.Identity = authsession.IdentityOf(s)
		answer(w, http.StatusOK, v)
	})
}

// SignedIn is the account the request's session names, or false with the
// answer written: what every HTTP route beside the RPCs asks first.
//
// A session narrowed to some methods (`loft auth login --allow`, D33) is refused.
// What is served beside the RPCs is no method a grant names, and what a route
// there does -- approve another terminal -- is not what a terminal that may do
// less than its account asked for: one that could approve a terminal for all
// of it would have been narrowed to nothing. The few that only read what an
// account is ask [Reading] instead.
func SignedIn(w http.ResponseWriter, r *http.Request, sessions *authsession.Sessions, people People) (identity.Person, bool) {
	p, g, ok := Reading(w, r, sessions, people)
	if !ok {
		return identity.Person{}, false
	}
	if !g.IsWhole() {
		http.Error(w, "this sign-in may only do what `loft auth login --allow` named, and this is not that: sign in without it", http.StatusForbidden)
		return identity.Person{}, false
	}
	return p, true
}

// Reading is [SignedIn] for a route that only reads what the account is --
// itself -- which any session may ask, however narrowed; and the grant it is
// narrowed by.
func Reading(w http.ResponseWriter, r *http.Request, sessions *authsession.Sessions, people People) (identity.Person, frame.Grant, bool) {
	p, v, ok := reading(w, r, sessions, people)
	return p, v.Grant, ok
}

func reading(w http.ResponseWriter, r *http.Request, sessions *authsession.Sessions, people People) (identity.Person, authsession.Session, bool) {
	ctx := r.Context()
	key := sessions.KeyOf(r.Header.Values("Cookie"))
	if key == "" {
		http.Error(w, "who is asking?", http.StatusUnauthorized)
		return identity.Person{}, authsession.Session{}, false
	}
	v, err := sessions.Read(ctx, key)
	switch {
	case errors.Is(err, authsession.ErrNoSession):
		http.Error(w, "who is asking?", http.StatusUnauthorized)
		return identity.Person{}, authsession.Session{}, false
	case err != nil:
		fail(w, r, err)
		return identity.Person{}, authsession.Session{}, false
	}
	p, err := people.ById(ctx, v.Id)
	switch {
	case errors.Is(err, identity.ErrNoPerson):
		http.Error(w, "who is asking?", http.StatusUnauthorized)
		return identity.Person{}, authsession.Session{}, false
	case err != nil:
		fail(w, r, err)
		return identity.Person{}, authsession.Session{}, false
	}

	return p, v, true
}

func answer(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

// fail is an error nobody asked for: roster not answering is one to come back
// after, and anything else is this server's.
func fail(w http.ResponseWriter, r *http.Request, err error) {
	if errors.Is(err, auth.ErrUnavailable) || errors.Is(err, identity.ErrUnreachable) {
		http.Error(w, "cannot say just now", http.StatusServiceUnavailable)
		return
	}
	log.From(r.Context()).ErrorContext(r.Context(), "account", slog.Any("err", err))
	http.Error(w, "something went wrong", http.StatusInternalServerError)
}
