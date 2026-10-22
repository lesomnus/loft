// Package sso is this deployment as a relying party of roster's issuer: the
// sign-in that goes to Google, Microsoft or GitHub and comes back as an account
// (design 9.10, D28).
//
// # Why roster's issuer, and not Google's
//
// Because who somebody is, is roster's (D26). Google, Microsoft and GitHub are
// connections of the deployment's directory at roster, and what comes back here
// is roster's token for an account in that directory: `sub` is the account,
// the same one a password names where a deployment has passwords. So the
// session this mints is the one `POST /session` mints, and nothing after the
// callback knows which way somebody came in -- every call is still held to
// what roster says of the account now.
//
// # What it is, in roster's words
//
// The `itself` shape of roster's `docs/relying-party.md`: discovery; an
// authorization request whose state is a nonce in a cookie, with an OpenID
// nonce and PKCE; the code exchanged with the client's secret in the header --
// said, not probed for, since `x/oauth2` caches what a probe found -- and the
// ID token verified for its audience. The token is kept, beside the session,
// for one thing: signing out hands it back to the issuer as `id_token_hint`,
// which is what the issuer wants before it sends a browser back here.
//
// The sign-in page is this deployment's (`ts/src`), and draws the buttons
// [Config.Providers] names. Each names its connection in the authorization
// request (`connection=`), and roster's Login App sends the browser straight
// there rather than drawing a page that asks again.
package sso

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"github.com/lesomnus/otx/log"
	"github.com/lesomnus/payday/auth/authsession"
	"github.com/lesomnus/payday/frame"
	"golang.org/x/oauth2"

	"github.com/lesomnus/rove/internal/identity"
)

// Config is the issuer this deployment signs people in through.
type Config struct {
	// Issuer is roster's issuer, where discovery is done: the public URL of
	// its Hydra. Empty is no single sign-on.
	Issuer string `yaml:"issuer"`

	// ClientId is what the issuer knows this deployment as, and ClientSecret
	// its secret there: `env:NAME`, `file:PATH`, or the secret itself. The
	// client is registered with `client_secret_basic`, this deployment's
	// `/sso/callback` as its redirect and its `/` as where a sign-out may
	// come back to.
	ClientId     string `yaml:"client_id"`
	ClientSecret string `yaml:"client_secret"`

	// Providers are the buttons the sign-in page draws, in order. None is one
	// button, and roster's page asks which.
	Providers []Provider `yaml:"providers"`
}

// On says whether people sign in through an issuer here.
func (c Config) On() bool { return c.Issuer != "" }

// Provider is one way in the sign-in page offers.
type Provider struct {
	// Connection is the directory's connection at roster (`Connection.name`),
	// which the authorization request names.
	Connection string `yaml:"connection" json:"connection"`

	// Label is what the button says, and Mark the vendor's mark it carries:
	// `google`, `microsoft`, `github`, or none.
	Label string `yaml:"label" json:"label"`
	Mark  string `yaml:"mark" json:"mark,omitempty"`
}

// Ways is what `GET /sso` answers: how somebody signs in here, for the page to
// draw it.
type Ways struct {
	// Sso says whether there is an issuer at all, and Providers which of its
	// connections to draw a button for.
	Sso       bool       `json:"sso"`
	Providers []Provider `json:"providers"`
	// Password says whether the form is offered beside them.
	Password bool `json:"password"`
}

// People is who an issuer's `sub` is, at roster.
type People interface {
	ById(ctx context.Context, sub string) (identity.Person, error)
}

// Arrive is what an account signing in is given: a workspace of its own when it
// is in none ([tenancy.Workspaces.Arrive]).
type Arrive func(ctx context.Context, p identity.Person) error

// The cookie that binds a browser to the round trip it started, and the one
// thing a session keeps from it.
const (
	stateCookie = "rove_sso"
	hint        = "id_token"
)

// flowTtl is how long a round trip to the issuer may take.
const flowTtl = 10 * time.Minute

// Rp is the relying party.
type Rp struct {
	c        Config
	secret   string
	base     string
	secure   bool
	password bool

	sessions *authsession.Sessions
	people   People
	arrive   Arrive

	// found is held by its own lock, which is held while the issuer is
	// asked; flows by another, which is never held across a call.
	dmu   sync.Mutex
	found *found
	mu    sync.Mutex
	flows map[string]flow
}

// found is what the issuer published, once it was asked and answered.
type found struct {
	cfg        *oauth2.Config
	verifier   *oidc.IDTokenVerifier
	endSession string
}

// A flow is one round trip to the issuer, started and not yet finished.
type flow struct {
	nonce    string
	verifier string
	next     string
	expires  time.Time
}

// New is the relying party for a deployment whose browser-facing origin is
// `base` -- where the issuer sends a browser back, at `/sso/callback`, and
// where a sign-out ends -- and which offers a password beside it or not.
//
// Nothing is asked of the issuer here: discovery is done at the first sign-in
// and kept once it worked, so an issuer that is down when this starts is one
// that is asked again rather than a deployment that does not start.
func New(c Config, base string, password bool, sessions *authsession.Sessions, people People, arrive Arrive) (*Rp, error) {
	rp := &Rp{c: c, password: password, sessions: sessions, people: people, arrive: arrive, flows: map[string]flow{}}
	if !c.On() {
		return rp, nil
	}

	u, err := url.Parse(base)
	if err != nil || u.Host == "" || (u.Scheme != "https" && u.Scheme != "http") {
		return nil, fmt.Errorf("sso: %q is not where a browser reaches this deployment", base)
	}
	rp.base = strings.TrimSuffix(u.String(), "/")
	rp.secure = u.Scheme == "https"
	if c.ClientId == "" {
		return nil, errors.New("auth.sso.client_id: the issuer has to know this deployment as something")
	}
	if rp.secret, err = identity.ResolveRef(c.ClientSecret); err != nil {
		return nil, fmt.Errorf("auth.sso.client_secret: %w", err)
	}
	for _, p := range c.Providers {
		if p.Connection == "" || p.Label == "" {
			return nil, errors.New("auth.sso.providers: each names its connection and what its button says")
		}
	}

	return rp, nil
}

// Mount serves `GET /sso`, and with an issuer `GET /sso/login`,
// `GET /sso/callback` and `POST /sso/logout`, on a mux of the caller's.
//
// Each by its method and its path, rather than a subtree handed to a mux of
// its own: the built UI is `GET /` on the same mux, and a pattern with no
// method beside it is one `net/http` refuses as neither more nor less
// specific than that one.
func (rp *Rp) Mount(m *http.ServeMux) {
	m.HandleFunc("GET /sso", rp.ways)
	if rp.c.On() {
		m.HandleFunc("GET /sso/login", rp.login)
		m.HandleFunc("GET /sso/callback", rp.callback)
		m.HandleFunc("POST /sso/logout", rp.logout)
	}
}

func (rp *Rp) ways(w http.ResponseWriter, r *http.Request) {
	v := Ways{Sso: rp.c.On(), Providers: []Provider{}, Password: rp.password}
	if rp.c.On() {
		v.Providers = append(v.Providers, rp.c.Providers...)
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(v)
}

// login starts a sign-in: `?with=<connection>` is the button pressed, and
// `?next=<path>` where on this deployment to land afterwards.
func (rp *Rp) login(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	with := r.URL.Query().Get("with")
	if with != "" && !rp.offers(with) {
		http.Error(w, "no such way in", http.StatusBadRequest)
		return
	}
	f, err := rp.discovered(ctx)
	if err != nil {
		log.From(ctx).WarnContext(ctx, "sso: the issuer could not be asked", slog.Any("err", err))
		http.Error(w, "signing in is not possible just now", http.StatusServiceUnavailable)
		return
	}

	state, nonce := secret(), secret()
	verifier := oauth2.GenerateVerifier()
	rp.put(state, flow{nonce: nonce, verifier: verifier, next: local(r.URL.Query().Get("next"))})

	http.SetCookie(w, &http.Cookie{
		Name:     stateCookie,
		Value:    state,
		Path:     "/sso",
		HttpOnly: true,
		Secure:   rp.secure,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   int(flowTtl / time.Second),
	})
	opts := []oauth2.AuthCodeOption{oidc.Nonce(nonce), oauth2.S256ChallengeOption(verifier)}
	if with != "" {
		opts = append(opts, oauth2.SetAuthURLParam("connection", with))
	}
	http.Redirect(w, r, f.cfg.AuthCodeURL(state, opts...), http.StatusFound)
}

// callback finishes a sign-in: the code exchanged, the token checked, and this
// deployment's session for the account it names.
//
// What went wrong past the state is not the browser's to be told in detail: it
// lands on the sign-in page with `?signin=refused` when the person said no at
// the issuer, `unavailable` when roster could not be asked, and `failed` for
// the rest, which the log says.
func (rp *Rp) callback(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	http.SetCookie(w, &http.Cookie{Name: stateCookie, Path: "/sso", MaxAge: -1, HttpOnly: true, Secure: rp.secure, SameSite: http.SameSiteLaxMode})

	q := r.URL.Query()
	state := q.Get("state")
	c, err := r.Cookie(stateCookie)
	if err != nil || state == "" || c.Value != state {
		// One answer for a missing cookie, a missing state and somebody
		// else's: which of them would tell whoever sent this browser how far
		// they got.
		http.Error(w, "no", http.StatusBadRequest)
		return
	}
	fl, ok := rp.take(state)
	if !ok {
		http.Error(w, "no", http.StatusBadRequest)
		return
	}
	failed := func(why string, err error) {
		log.From(ctx).WarnContext(ctx, "sso: a sign-in did not finish", "why", why, "err", fmt.Sprint(err))
		http.Redirect(w, r, "/?signin=failed", http.StatusSeeOther)
	}

	if e := q.Get("error"); e != "" {
		if e == "access_denied" {
			http.Redirect(w, r, "/?signin=refused", http.StatusSeeOther)
			return
		}
		failed("the issuer refused", errors.New(e+": "+q.Get("error_description")))
		return
	}
	f, err := rp.discovered(ctx)
	if err != nil {
		failed("the issuer could not be asked", err)
		return
	}
	tok, err := f.cfg.Exchange(ctx, q.Get("code"), oauth2.VerifierOption(fl.verifier))
	if err != nil {
		failed("the code was not exchanged", err)
		return
	}
	raw, ok := tok.Extra("id_token").(string)
	if !ok {
		failed("no id_token", nil)
		return
	}
	id, err := f.verifier.Verify(ctx, raw)
	if err != nil {
		failed("the id_token", err)
		return
	}
	if id.Nonce != fl.nonce {
		failed("the id_token is for another sign-in", nil)
		return
	}

	// `sub` is the account, and nothing else in the token is who they are.
	p, err := rp.people.ById(ctx, id.Subject)
	switch {
	case errors.Is(err, identity.ErrNoPerson):
		// The issuer vouched for somebody the directory does not have: an
		// issuer in front of another directory than this deployment's.
		failed("the issuer named nobody in this directory", err)
		return
	case err != nil:
		log.From(ctx).WarnContext(ctx, "sso: a sign-in nobody could check", slog.Any("err", err))
		http.Redirect(w, r, "/?signin=unavailable", http.StatusSeeOther)
		return
	}

	// A workspace that could not be made is not a sign-in refused: the
	// account is who it is, and is told it is in no workspace.
	if rp.arrive != nil {
		if err := rp.arrive(ctx, p); err != nil {
			log.From(ctx).ErrorContext(ctx, "sso: an account arrived and was not given what it is given", slog.Any("err", err))
		}
	}

	_, cookie, err := rp.sessions.Mint(ctx, authsession.Session{
		Id:    p.Id.String(),
		Grant: frame.Whole(),
		// Kept, and only for signing out.
		Held: map[string]string{hint: raw},
	})
	if err != nil {
		log.From(ctx).ErrorContext(ctx, "sso: no session", slog.Any("err", err))
		http.Redirect(w, r, "/?signin=unavailable", http.StatusSeeOther)
		return
	}
	http.SetCookie(w, cookie)
	http.Redirect(w, r, fl.next, http.StatusSeeOther)
}

// logout ends this deployment's session and then the issuer's.
//
// # Why both
//
// A session ended here alone is a sign-out that does not stick: the next page
// starts a sign-in, the issuer still remembers the browser, and the person who
// clicked *sign out* is signed in again without being asked. So the browser
// goes to the issuer's end-session endpoint with the token it signed in with,
// which proves an app asked, and comes back to `/`.
//
// A session that holds no token -- one a password made, beside an issuer --
// has nothing at the issuer to end, and goes to `/`.
//
// It is a POST because it is a form: a cross-site GET would carry the cookie
// and sign somebody out for whoever sent it, and a POST from another site
// carries none.
func (rp *Rp) logout(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	key := rp.sessions.KeyOf(r.Header.Values("Cookie"))
	held := ""
	if v, err := rp.sessions.Read(ctx, key); err == nil {
		held = v.Held[hint]
	}
	http.SetCookie(w, rp.sessions.End(ctx, key))

	if held == "" {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	f, err := rp.discovered(ctx)
	if err != nil || f.endSession == "" {
		// An issuer that publishes no way to end its own session: this one
		// is ended, and that is what there is.
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	u, err := url.Parse(f.endSession)
	if err != nil {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	q := u.Query()
	q.Set("client_id", rp.c.ClientId)
	q.Set("id_token_hint", held)
	q.Set("post_logout_redirect_uri", rp.base+"/")
	u.RawQuery = q.Encode()
	http.Redirect(w, r, u.String(), http.StatusSeeOther)
}

// discovered is what the issuer published, asked for once it is needed and
// kept once it answered.
func (rp *Rp) discovered(ctx context.Context) (*found, error) {
	rp.dmu.Lock()
	defer rp.dmu.Unlock()
	if rp.found != nil {
		return rp.found, nil
	}

	// A deadline of its own rather than the request's: a browser that gives
	// up half way should not leave the next one to ask again.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	p, err := oidc.NewProvider(ctx, rp.c.Issuer)
	if err != nil {
		return nil, fmt.Errorf("discovery at %s: %w", rp.c.Issuer, err)
	}

	// `end_session_endpoint` is not in `oidc.Provider`, so it is read off the
	// document itself.
	var doc struct {
		EndSession string `json:"end_session_endpoint"`
	}
	_ = p.Claims(&doc)

	// How the secret is sent is said here, and is what the client is
	// registered with: `x/oauth2` would otherwise probe, and keep what the
	// first probe found for as long as this process runs.
	endpoint := p.Endpoint()
	endpoint.AuthStyle = oauth2.AuthStyleInHeader

	rp.found = &found{
		cfg: &oauth2.Config{
			ClientID:     rp.c.ClientId,
			ClientSecret: rp.secret,
			Endpoint:     endpoint,
			RedirectURL:  rp.base + "/sso/callback",
			// Who somebody is, and nothing about them: `sub` is the whole of
			// what this reads, and roster says the rest.
			Scopes: []string{oidc.ScopeOpenID},
		},
		verifier:   p.Verifier(&oidc.Config{ClientID: rp.c.ClientId}),
		endSession: doc.EndSession,
	}
	return rp.found, nil
}

func (rp *Rp) offers(connection string) bool {
	for _, p := range rp.c.Providers {
		if p.Connection == connection {
			return true
		}
	}
	return false
}

// put remembers one round trip, and forgets the ones that ran out.
func (rp *Rp) put(state string, f flow) {
	rp.mu.Lock()
	defer rp.mu.Unlock()
	now := time.Now()
	for k, v := range rp.flows {
		if now.After(v.expires) {
			delete(rp.flows, k)
		}
	}
	f.expires = now.Add(flowTtl)
	rp.flows[state] = f
}

// take answers one round trip once.
func (rp *Rp) take(state string) (flow, bool) {
	rp.mu.Lock()
	defer rp.mu.Unlock()
	f, ok := rp.flows[state]
	delete(rp.flows, state)
	if !ok || time.Now().After(f.expires) {
		return flow{}, false
	}
	return f, true
}

// local is a path on this deployment to land on, and `/` for anything else: a
// sign-in is a redirect, and one to wherever a link said would send somebody
// who just signed in to a page made to look like this one.
//
// A path is what starts with one slash. Two is another host to a browser, and
// so is a backslash where a slash would be, since a browser reads one as the
// other; a control character, which a browser drops and which could close up
// either, is refused by the parse.
func local(next string) string {
	u, err := url.Parse(next)
	if err != nil || !strings.HasPrefix(next, "/") || strings.HasPrefix(next, "//") || strings.Contains(next, "\\") {
		return "/"
	}
	return u.RequestURI()
}

// secret is 24 random bytes, for a state or a nonce.
func secret() string {
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return base64.RawURLEncoding.EncodeToString(b)
}
