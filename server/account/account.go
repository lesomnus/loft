// Package account is what a signed-in session is told of itself beside
// `/session`: the account it is, what it owes and agreed to, and where it is
// asking from (design 9.11, D28) -- the page's first question, asked before
// any RPC can be.
//
// It is HTTP rather than an RPC for two reasons that hold of nothing else an
// account asks. An account that owes an agreement is refused every RPC
// (`cmd.Resolver`) but what it has (`cmd.WhileOwing`), so what it owes, and
// agreeing to it, are the way out and cannot be behind the refusal. And a
// session on a workspace's own domain is served as its membership there and
// never as the account (payday#184), so what the page there is told of the
// one workspace it serves is said here too.
//
// The rest of what an account has outside any one workspace is the account's
// own RPCs (D73): the workspaces it is in and a new one
// (`TenantService.Mine`, `Create`), which of them a label is in
// (`LabelService.Locate`).
package account

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"mime"
	"net/http"
	"time"

	"github.com/lesomnus/otx/log"
	"github.com/lesomnus/payday/access"
	"github.com/lesomnus/payday/auth"
	"github.com/lesomnus/payday/auth/authsession"
	"github.com/lesomnus/payday/frame"
	"github.com/lesomnus/payday/pdid"
	"github.com/lesomnus/payday/pdpb"

	"github.com/lesomnus/loft/internal/identity"
	"github.com/lesomnus/loft/server/apphost"
	"github.com/lesomnus/loft/server/clientaddr"
	"github.com/lesomnus/loft/server/consent"
	"github.com/lesomnus/loft/server/tenancy"
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
	// Owes is what it has to agree to before it uses anything here, at the
	// versions in force; absent for nothing (design 9.11).
	Owes []consent.Document `json:"owes,omitempty"`
	// Allow is the methods the session asking is narrowed to (`loft auth login
	// --allow`, D33); absent is all that the account may do.
	Allow []string `json:"allow,omitempty"`
	// Pinned is the workspace a workspace's own domain serves alone, by its
	// alias, when that is where the account was asked about (loft#155): the
	// page there offers nothing of the account's beside it -- making a
	// workspace, the invitations -- which is the service's. Here is that
	// workspace as the account is told it, which nothing of the account's
	// own answers there (`TenantService.Mine` is the service's); absent
	// anywhere else, and for an account that is no member of it.
	Pinned string     `json:"pinned,omitempty"`
	Here   *Workspace `json:"here,omitempty"`
	// Service is the service's address, said where Pinned is: where the
	// account's other workspaces are opened from (`/?workspaces` there).
	Service string `json:"service,omitempty"`
	// Billing says this deployment's workspaces are billed, by levyan: the
	// page offers the account's payments (`/billing`) and a workspace's
	// (design 7.9). Absent for one that bills nobody.
	Billing bool `json:"billing,omitempty"`
	// Timezone is the deployment's zone (`app.locale.timezone`, Asia/Seoul
	// unset), as an IANA name: where a page about no workspace -- the
	// account's payments, its invitations -- draws its dates, as a
	// workspace's pages draw theirs on its own wall (`PartyService.Me`) and
	// never on the browser's (design 7.9).
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

// Workspace is one an account is in, as `TenantService.Mine` answers each
// (`TenantMembership`).
type Workspace struct {
	Id    string `json:"id"`
	Alias string `json:"alias"`
	// Name is what it is called: its own name, or, for one that has none,
	// after its owner (D57).
	Name string `json:"name"`
	Role string `json:"role"`
	// Closed is when it was closed and Destroy when it goes, for one that is
	// closed (D29), and AliasHoldDays how many days its address is then kept
	// from anybody else (`app.workspaces.alias_hold`).
	Closed        *time.Time `json:"closed,omitempty"`
	Destroy       *time.Time `json:"destroy,omitempty"`
	AliasHoldDays uint32     `json:"alias_hold_days,omitempty"`
	// Home is where its app is, when that is a domain of its own (loft#155):
	// the address a page of it begins with there, which the page goes to to
	// open it. Absent while it is the service's.
	Home string `json:"home,omitempty"`
}

// accountAction is what the account is called in the access record: the
// route, since it is no RPC.
const accountAction = "GET /account"

// saw puts on the access record whose personal data an answer showed in what
// it called the workspaces `ms` -- the owner one with no name of its own is
// called after (D57) -- by the account's membership of each, from where the
// request came. A read, so queued, as an RPC's read is; with no record wired,
// nothing.
func (a Routes) saw(r *http.Request, action string, ms ...tenancy.Membership) {
	if a.Access == nil {
		return
	}

	ctx := r.Context()
	d := pdpb.AccessDetail_builder{Address: clientaddr.Of(r)}.Build()
	now := time.Now()
	for _, m := range ms {
		if len(m.Saw) == 0 {
			continue
		}
		as := frame.Into(ctx, frame.New(m.Holder, m.Workspace, frame.Whole()))
		a.Access.Queue(access.Records(as, action, access.Read, "ok", m.Saw, d, now)...)
	}
}

// Routes is what is served for an account beside `/session`.
type Routes struct {
	Sessions   *authsession.Sessions
	People     People
	Workspaces tenancy.Workspaces
	// Access is where what an answer showed of somebody is recorded
	// (loft#136): the owner a workspace with no name of its own is called
	// after (D57).
	Access *access.Writer
	// Manage is roster's account page (`auth.roster.account_url`).
	Manage string
	// AliasHold is how long a destroyed workspace's address is kept from
	// anybody else, which an account in a closed one is told.
	AliasHold time.Duration
	// Consent is what an account agrees to first; nil asks nothing.
	Consent *consent.Consents
	// Arrive is what an account that has just agreed is given: a workspace
	// of its own, as a sign-in gives one.
	Arrive func(ctx context.Context, p identity.Person) error
	// Home is where a workspace's app is when that is a domain of its own,
	// and empty while it is the service's (`apphost.Links`); nil says none.
	// Service is where the service's page is, which a page on a workspace's
	// domain sends somebody to for the account's other workspaces.
	Home    func(ctx context.Context, workspace pdid.Id) (string, error)
	Service string

	// Billing says this deployment's workspaces are billed (`app.billing.
	// levyan`): what the page draws the account's payments and a workspace's
	// for (design 7.9).
	Billing bool

	// Zone is the deployment's zone, by its IANA name (`domain.Locale.Zone`):
	// what the account is told its pages outside every workspace draw their
	// dates in. Empty tells it none, and the page keeps the browser's.
	Zone string
}

// Mount serves `GET /account`, and what the account agreed to and agreeing at
// `GET` and `POST /account/consents`.
func (a Routes) Mount(mux *http.ServeMux) {
	sessions, people, ws := a.Sessions, a.People, a.Workspaces
	mux.HandleFunc("GET /account", func(w http.ResponseWriter, r *http.Request) {
		p, s, ok := reading(w, r, sessions, people)
		if !ok {
			return
		}
		v, ms, err := a.accountOf(r, ws, p)
		if err != nil {
			fail(w, r, err)
			return
		}
		v.Manage = a.Manage
		if v.Owes, err = a.Consent.Owed(r.Context(), p.Id); err != nil {
			fail(w, r, err)
			return
		}
		if !s.Grant.AnyAction() {
			v.Allow = s.Grant.Actions()
		}
		v.Identity = authsession.IdentityOf(s)
		a.saw(r, accountAction, ms...)
		answer(w, http.StatusOK, v)
	})
	mux.HandleFunc("GET /account/consents", func(w http.ResponseWriter, r *http.Request) {
		p, _, ok := Reading(w, r, sessions, people)
		if !ok {
			return
		}
		owed, err := a.Consent.Owed(r.Context(), p.Id)
		if err != nil {
			fail(w, r, err)
			return
		}
		agreed := []consent.Agreement{}
		if a.Consent != nil {
			if agreed, err = a.Consent.Of(r.Context(), p.Id); err != nil {
				fail(w, r, err)
				return
			}
		}
		answer(w, http.StatusOK, map[string]any{"owed": owed, "agreed": agreed})
	})
	mux.HandleFunc("POST /account/consents", func(w http.ResponseWriter, r *http.Request) {
		// A body that is JSON, which a form on another site cannot send: an
		// agreement is the account's own doing.
		if ct, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type")); ct != "application/json" {
			http.Error(w, "an agreement is sent as JSON", http.StatusUnsupportedMediaType)
			return
		}
		p, ok := Own(w, r, sessions, people)
		if !ok {
			return
		}
		var in struct {
			// Agree is each document agreed to, by name, with the version
			// read.
			Agree map[string]string `json:"agree"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10)).Decode(&in); err != nil {
			http.Error(w, "an agreement is sent as {\"agree\": {\"terms\": <version>, ...}}", http.StatusBadRequest)
			return
		}
		client := "web"
		if r.Header.Get("User-Agent") == "loft-cli" {
			client = "cli"
		}
		err := a.Consent.Agree(r.Context(), p.Id, in.Agree, client)
		switch {
		case errors.Is(err, consent.ErrOwed):
			http.Error(w, "모든 문서에 동의해야 합니다", http.StatusConflict)
			return
		case errors.Is(err, consent.ErrStale):
			http.Error(w, "문서가 바뀌었습니다. 새로 읽고 다시 동의하세요", http.StatusConflict)
			return
		case err != nil:
			fail(w, r, err)
			return
		}
		// What a sign-in would have given it, had it agreed then.
		if a.Arrive != nil {
			if err := a.Arrive(r.Context(), p); err != nil {
				fail(w, r, err)
				return
			}
		}
		w.WriteHeader(http.StatusNoContent)
	})
}

// SignedIn is the account the request's session names, or false with the
// answer written: what every HTTP route beside the RPCs asks first.
//
// A session narrowed to some methods (`loft auth login --allow`, D33) is refused.
// What is served beside the RPCs is no method a grant names, and what a route
// there does -- approve another terminal, agree to terms -- is not what a
// terminal that may do less than its account asked for: one that could
// approve a terminal for all of it would have been narrowed to nothing. The
// few that only read what an account is ask [Reading] instead.
func SignedIn(w http.ResponseWriter, r *http.Request, sessions *authsession.Sessions, people People) (identity.Person, bool) {
	p, _, ok := signedIn(w, r, sessions, people)
	return p, ok
}

func signedIn(w http.ResponseWriter, r *http.Request, sessions *authsession.Sessions, people People) (identity.Person, authsession.Session, bool) {
	p, v, ok := reading(w, r, sessions, people)
	if !ok {
		return identity.Person{}, authsession.Session{}, false
	}
	if !v.Grant.IsWhole() {
		http.Error(w, "this sign-in may only do what `loft auth login --allow` named, and this is not that: sign in without it", http.StatusForbidden)
		return identity.Person{}, authsession.Session{}, false
	}
	return p, v, true
}

// Own is [SignedIn] for what an account does as itself -- agree to what the
// deployment asks -- which is done signed in at the service's address, and
// never with a session handed to a workspace's own domain (loft#155): that
// one is the workspace's, and what the account says of itself is not.
func Own(w http.ResponseWriter, r *http.Request, sessions *authsession.Sessions, people People) (identity.Person, bool) {
	p, v, ok := signedIn(w, r, sessions, people)
	if !ok {
		return identity.Person{}, false
	}
	if v.TenantId != "" {
		http.Error(w, "this sign-in is a workspace's, on its own address: what the account does as itself is done at the service's", http.StatusForbidden)
		return identity.Person{}, false
	}
	return p, true
}

// Reading is [SignedIn] for a route that only reads what the account is --
// itself, and what it agreed to -- which any session may ask, however
// narrowed; and the grant it is narrowed by.
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
	// A session handed to a workspace's own domain is that workspace's, on a
	// host that pins it, and nobody anywhere else (loft#155).
	if v.TenantId != "" {
		if pin, ok := apphost.From(ctx); !ok || pin.Workspace.String() != v.TenantId {
			http.Error(w, "who is asking?", http.StatusUnauthorized)
			return identity.Person{}, authsession.Session{}, false
		}
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

// accountOf is what `GET /account` answers of `p`, and the membership it
// says: on a workspace's own domain, the account's in that workspace, which
// is all of the account's the page there is told (loft#155); at the
// service's address none, which `TenantService.Mine` answers.
func (a Routes) accountOf(r *http.Request, ws tenancy.Workspaces, p identity.Person) (Account, []tenancy.Membership, error) {
	out := Account{Id: p.Id.String(), Login: p.Alias, Name: p.Name, Billing: a.Billing, Timezone: a.Zone}
	pin, ok := apphost.From(r.Context())
	if !ok {
		return out, nil, nil
	}
	out.Pinned, out.Service = pin.Alias, a.Service

	ms, err := ws.Of(r.Context(), p.Id)
	if err != nil {
		return Account{}, nil, err
	}
	for _, m := range ms {
		if m.Workspace != pin.Workspace {
			continue
		}
		v, err := a.workspaceOf(r.Context(), m)
		if err != nil {
			return Account{}, nil, err
		}
		out.Here = &v
		return out, []tenancy.Membership{m}, nil
	}

	return out, nil, nil
}

func (a Routes) workspaceOf(ctx context.Context, m tenancy.Membership) (Workspace, error) {
	w := Workspace{Id: pdid.Id(m.Workspace).String(), Alias: m.Alias, Name: m.Name, Role: m.Role, Closed: m.Closed, Destroy: m.Destroy}
	if m.Closed != nil {
		w.AliasHoldDays = uint32(a.AliasHold / (24 * time.Hour))
	}
	if a.Home != nil {
		home, err := a.Home(ctx, m.Workspace)
		if err != nil {
			return Workspace{}, err
		}
		w.Home = home
	}
	return w, nil
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
