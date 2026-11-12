package session

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/lesomnus/otx/log"
	"github.com/lesomnus/payday/auth"
	"github.com/lesomnus/payday/auth/authsession"
	"github.com/lesomnus/payday/frame"
	"github.com/lesomnus/payday/pdid"

	"github.com/lesomnus/loft/internal/ent"
	"github.com/lesomnus/loft/internal/ent/holder"
	"github.com/lesomnus/loft/internal/identity"
)

// Answer is what `GET /session` says of the session asking: who it is, what
// it is narrowed to, and the workspace -- the page's first question, which it
// asks before any RPC, since a closed workspace answers no RPC at all (D83).
type Answer struct {
	// Id is the member, which is their holder at roster too (D85); Login and
	// Name what the workspace calls them.
	Id    string `json:"id"`
	Login string `json:"login"`
	Name  string `json:"name"`
	// Allow is the methods the session asking is narrowed to (`loft auth
	// login --allow`, D33); absent is all that the member may do.
	Allow []string `json:"allow,omitempty"`
	// Billing says the deployment bills its workspace, by levyan: the page
	// offers its billing (design 7.9). Absent for one that bills nobody.
	Billing bool `json:"billing,omitempty"`
	// Identity is what the session asking lets its caller see, named as
	// `authsession.IdentityOf` names it: what the page keeps what it was
	// answered and what it has not sent yet for, and says on every call it
	// makes (`payday-identity`). A call saying it is served only as a session
	// of that name (`authsession.Sessions.Handler`), so a page left behind by
	// somebody else's sign-in in this browser is refused, rather than served
	// as them.
	Identity string `json:"identity"`

	Workspace Workspace `json:"workspace"`
}

// Workspace is the workspace a session is in, as the page is told it before
// it calls anything: enough to draw what a closed one shows.
type Workspace struct {
	Id    string `json:"id"`
	Alias string `json:"alias"`
	// Name is what it is called as it keeps it, or its address for one that
	// keeps none: what a closed one is called, which no RPC is there to say
	// otherwise. Open, the page reads `PartyService.Me`.
	Name string `json:"name"`
	// Timezone is where its dates are drawn: its own, or the deployment's
	// (`app.locale`) while it says none (design 7.9).
	Timezone string `json:"timezone"`
	// Closed is when it was closed and Destroy when it goes unless an
	// operator restores it, for one that is closed (D29, D83), and
	// AliasHoldDays how many days its address is then held from anybody else
	// (`app.workspaces.alias_hold`).
	Closed        *time.Time `json:"closed,omitempty"`
	Destroy       *time.Time `json:"destroy,omitempty"`
	AliasHoldDays uint32     `json:"alias_hold_days,omitempty"`
}

// Who is `GET /session`.
type Who struct {
	Sessions *authsession.Sessions
	Db       *ent.Client

	// Zone is the deployment's zone, what a workspace that says none of its
	// own is told in.
	Zone *time.Location
	// Hold is how long a destroyed workspace's address is held
	// (`app.workspaces.alias_hold`).
	Hold time.Duration
	// Billing says the deployment bills (`app.billing.levyan`).
	Billing bool
}

func (a Who) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	h, s, ok := reading(w, r, a.Sessions, a.Db)
	if !ok {
		return
	}
	t := h.Edges.Tenant
	v := Answer{
		Id: pdid.Id(h.Id).String(), Login: h.Alias, Name: h.Name,
		Billing:  a.Billing,
		Identity: authsession.IdentityOf(s),
		Workspace: Workspace{
			Id: pdid.Id(t.Id).String(), Alias: t.Alias, Name: or(t.Name, t.Alias),
			Timezone: t.Timezone,
		},
	}
	if v.Workspace.Timezone == "" && a.Zone != nil {
		v.Workspace.Timezone = a.Zone.String()
	}
	if !s.Grant.AnyAction() {
		v.Allow = s.Grant.Actions()
	}
	if t.DateClosed != nil {
		v.Workspace.Closed, v.Workspace.Destroy = t.DateClosed, t.DateDestroy
		v.Workspace.AliasHoldDays = uint32(a.Hold / (24 * time.Hour))
	}
	answer(w, http.StatusOK, v)
}

func or(v, otherwise string) string {
	if v == "" {
		return otherwise
	}
	return v
}

// SignedIn is the member the request's session names, with their workspace,
// or false with the answer written: what every HTTP route beside the RPCs
// asks first.
//
// A session narrowed to some methods (`loft auth login --allow`, D33) is
// refused. What is served beside the RPCs is no method a grant names, and what
// a route there does -- approve another terminal -- is not what a terminal
// that may do less than its member asked for: one that could approve a
// terminal for all of it would have been narrowed to nothing. The few that
// only read who the session is ask [Reading] instead.
func SignedIn(w http.ResponseWriter, r *http.Request, sessions *authsession.Sessions, db *ent.Client) (*ent.Holder, bool) {
	h, g, ok := Reading(w, r, sessions, db)
	if !ok {
		return nil, false
	}
	if !g.IsWhole() {
		http.Error(w, "this sign-in may only do what `loft auth login --allow` named, and this is not that: sign in without it", http.StatusForbidden)
		return nil, false
	}
	return h, true
}

// Reading is [SignedIn] for a route that only reads who the session is, which
// any session may ask, however narrowed; and the grant it is narrowed by.
func Reading(w http.ResponseWriter, r *http.Request, sessions *authsession.Sessions, db *ent.Client) (*ent.Holder, frame.Grant, bool) {
	h, v, ok := reading(w, r, sessions, db)
	return h, v.Grant, ok
}

func reading(w http.ResponseWriter, r *http.Request, sessions *authsession.Sessions, db *ent.Client) (*ent.Holder, authsession.Session, bool) {
	ctx := r.Context()
	key := sessions.KeyOf(r.Header.Values("Cookie"))
	if key == "" {
		http.Error(w, "who is asking?", http.StatusUnauthorized)
		return nil, authsession.Session{}, false
	}
	v, err := sessions.Read(ctx, key)
	switch {
	case errors.Is(err, authsession.ErrNoSession):
		http.Error(w, "who is asking?", http.StatusUnauthorized)
		return nil, authsession.Session{}, false
	case err != nil:
		fail(w, r, err)
		return nil, authsession.Session{}, false
	}
	id, err := pdid.Parse(v.Id)
	if err != nil {
		http.Error(w, "who is asking?", http.StatusUnauthorized)
		return nil, authsession.Session{}, false
	}
	h, err := db.Holder.Query().Where(holder.Id(id.Uuid()), holder.DateErasedIsNil()).WithTenant().Only(ctx)
	switch {
	case ent.IsNotFound(err):
		// A membership ended here since the session began is nobody's
		// session any more.
		http.Error(w, "who is asking?", http.StatusUnauthorized)
		return nil, authsession.Session{}, false
	case err != nil:
		fail(w, r, err)
		return nil, authsession.Session{}, false
	}

	return h, v, true
}

func answer(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
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
	log.From(r.Context()).ErrorContext(r.Context(), "session", slog.Any("err", err))
	http.Error(w, "something went wrong", http.StatusInternalServerError)
}
