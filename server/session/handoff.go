package session

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"
	"uuid"

	"github.com/lesomnus/otx/log"
	"github.com/lesomnus/payday/auth/authsession"
	"github.com/lesomnus/payday/frame"
	"github.com/lesomnus/payday/pdid"

	"github.com/lesomnus/rove/internal/ent"
	"github.com/lesomnus/rove/internal/ent/handoff"
	"github.com/lesomnus/rove/internal/ent/holder"
	"github.com/lesomnus/rove/internal/ent/predicate"
	"github.com/lesomnus/rove/internal/ent/session"
	"github.com/lesomnus/rove/internal/ent/tenantdomain"
	"github.com/lesomnus/rove/server/apphost"
	"github.com/lesomnus/rove/server/landing"
	"github.com/lesomnus/rove/server/pd"
)

// Handoff is signing in on a workspace's own domain (rove#155, design 9.9),
// which is never done there: the domain sends the browser to the service's
// address, where it is signed in -- or signs in -- and the browser comes back
// with a code it exchanges for a session of the domain's own.
//
//  1. `GET /handoff/start?next=<path>` on the domain makes up a state, keeps
//     it in a cookie of that host's alone, and sends the browser to the
//     service's `GET /handoff?to=<host>&state=<state>`.
//  2. The service makes a code for an active `app` domain alone -- any other
//     host is answered no, and nothing is sent anywhere -- and for a browser
//     signed in there as an account that may do all it may (no `rove login
//     --allow`), that has agreed to what the deployment asks, and that is a
//     member of the domain's workspace. Anybody else is signed in first, or
//     told no. The code goes to that domain's `GET /handoff/callback`.
//  3. The domain holds the state the browser came back with to its cookie's,
//     takes the code -- once: of two requests that bring one together, one
//     is signed in -- and holds it to the account, the workspace, the host
//     and the state it was made for, and to the minute it was good for. The
//     session is a cookie of that host's alone, bound to that workspace, and
//     handed from the sign-in at the service's ([Store]).
//
// Signing out on the domain is `POST /handoff/logout`. It ends that session
// and the sign-in at the service's it was handed from -- and so every other
// session that one handed out, which is good while it is -- and sends the
// browser where signing out there would: to the issuer's end of its own
// session for a sign-in made through it, and the service's front page
// otherwise.
//
// A code is never written down, nor logged: its row holds what it is kept as
// ([Store.Kept]), and a refusal says no more than that the sign-in did not
// finish. A terminal
// is signed in at the service's address alone (`server/clilogin`).
type Handoff struct {
	Db       *ent.Client
	Sessions *authsession.Sessions
	Store    Store

	// Agreed is whether an account has agreed to what the deployment asks
	// ([consent.Consents]): one that has not is sent to agree first, at the
	// service's, rather than handed a session every call refuses.
	Agreed Agreed

	// Issuer is where signing out ends a sign-in made through roster's
	// issuer ([sso.Rp]); nil for none.
	Issuer Issuer

	// Links is where the service's address is -- which a domain sends a
	// browser to, to sign in, and which signing out on one ends at -- and how
	// a workspace's domain is reached.
	Links apphost.Links

	// Now is the clock; tests move it.
	Now func() time.Time
}

// Agreed is [consent.Consents.Settled].
type Agreed interface {
	Settled(ctx context.Context, account pdid.Id) (bool, error)
}

// Issuer is where a browser goes to end its session at roster's issuer too,
// for a sign-in that kept the issuer's token ([sso.Rp.EndSession]): empty for
// one that kept none.
type Issuer interface {
	EndSession(ctx context.Context, held map[string]string) string
}

const (
	// CodeTtl is how long a code is good for: a redirect's worth, and a slow
	// one's.
	CodeTtl = time.Minute

	// roundTrip is how long a domain waits for a browser it sent to the
	// service's to come back: signing in there on the way, included.
	roundTrip = 10 * time.Minute
)

// Mount serves the service's `GET /handoff`, and a domain's
// `GET /handoff/start`, `GET /handoff/callback` and `POST /handoff/logout`.
// Each answers where it is the one asked and is nothing anywhere else.
func (h Handoff) Mount(mux *http.ServeMux) {
	mux.HandleFunc("GET /handoff", h.give)
	mux.HandleFunc("GET /handoff/start", h.start)
	mux.HandleFunc("GET /handoff/callback", h.take)
	mux.HandleFunc("POST /handoff/logout", h.leave)
}

func (h Handoff) now() time.Time {
	if h.Now != nil {
		return h.Now()
	}
	return time.Now()
}

func (h Handoff) secure() bool { return h.Links.Secure() }

// cookie is what the state a domain sent a browser off with is kept in, on
// that host alone.
func (h Handoff) cookie() string {
	if h.secure() {
		return "__Host-rove_handoff"
	}
	return "rove_handoff"
}

// start is a domain sending a browser to the service's address to be signed
// in, with a state to hold what comes back to; see [Handoff].
func (h Handoff) start(w http.ResponseWriter, r *http.Request) {
	if _, ok := apphost.From(r.Context()); !ok {
		http.NotFound(w, r)
		return
	}
	state, next := secret(), landing.Path(r.URL.Query().Get("next"))
	http.SetCookie(w, &http.Cookie{
		Name:     h.cookie(),
		Value:    state + "." + base64.RawURLEncoding.EncodeToString([]byte(next)),
		Path:     "/",
		MaxAge:   int(roundTrip / time.Second),
		HttpOnly: true,
		Secure:   h.secure(),
		SameSite: http.SameSiteLaxMode,
	})
	w.Header().Set("Cache-Control", "no-store")
	q := url.Values{"to": {apphost.Host(r)}, "state": {state}}
	http.Redirect(w, r, h.Links.Home(apphost.Page)+"/handoff?"+q.Encode(), http.StatusSeeOther)
}

// give is the service's: a code for the domain a browser came from, for the
// account it is signed in as here; see [Handoff].
func (h Handoff) give(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	if _, ok := apphost.From(ctx); ok {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Cache-Control", "no-store")

	q := r.URL.Query()
	state := q.Get("state")
	if !madeHere(state) {
		http.Error(w, "no", http.StatusBadRequest)
		return
	}
	// The domain first, and nothing sent anywhere for a host that is not an
	// active app domain: a code goes nowhere it could be read by whoever
	// holds a name that is not one.
	d, err := h.domain(ctx, q.Get("to"))
	if err != nil {
		h.fail(w, r, "which domain it is could not be told", err)
		return
	}
	if d == nil {
		http.Error(w, "no workspace's own domain is at that address", http.StatusBadRequest)
		return
	}

	// Whoever is signed in here, which a sign-in handed to a domain is not:
	// nobody is signed in first, by the page, which comes back here once
	// they are.
	again := "/?next=" + url.QueryEscape(r.URL.RequestURI())
	key := h.Sessions.KeyOf(r.Header.Values("Cookie"))
	if key == "" {
		http.Redirect(w, r, again, http.StatusSeeOther)
		return
	}
	v, err := h.Sessions.Read(ctx, key)
	switch {
	case errors.Is(err, authsession.ErrNoSession), err == nil && v.TenantId != "":
		http.Redirect(w, r, again, http.StatusSeeOther)
		return
	case err != nil:
		h.fail(w, r, "who is signed in could not be told", err)
		return
	}
	if !v.Grant.IsWhole() {
		http.Error(w, "this sign-in may only do what `rove login --allow` named, and hands nothing on: sign in without it", http.StatusForbidden)
		return
	}
	account, err := pdid.Parse(v.Id)
	if err != nil {
		h.fail(w, r, "a session that names no account", err)
		return
	}
	if h.Agreed != nil {
		ok, err := h.Agreed.Settled(ctx, account)
		if err != nil {
			h.fail(w, r, "what the account agreed to could not be told", err)
			return
		}
		if !ok {
			// The page asks for it, and comes back here once it is given.
			http.Redirect(w, r, again, http.StatusSeeOther)
			return
		}
	}
	ws := pdid.Id(d.TenantId)
	in, err := h.Db.Holder.Query().Where(
		holder.Account(account.Uuid()),
		holder.TenantId(ws.Uuid()),
		holder.DateErasedIsNil(),
	).Exist(ctx)
	if err != nil {
		h.fail(w, r, "the account's membership could not be told", err)
		return
	}
	if !in {
		alias := ""
		if d.Edges.Tenant != nil {
			alias = d.Edges.Tenant.Alias
		}
		http.Error(w, "this account is not in @"+alias+": sign in as one that is", http.StatusForbidden)
		return
	}
	parent, err := h.Db.Session.Query().Where(session.Secret(h.Store.Kept(key)), session.DateErasedIsNil()).OnlyId(ctx)
	if err != nil {
		h.fail(w, r, "the sign-in here could not be read again", err)
		return
	}

	code := secret()
	now := h.now()
	if err := h.Db.Handoff.Create().
		SetId(pdid.New(pd.HandoffDomain).Uuid()).
		SetSecret(h.Store.Kept(code)).
		SetAccount(account.Uuid()).
		SetWorkspace(ws.Uuid()).
		SetHost(d.Host).
		SetState(h.Store.Kept(state)).
		SetParent(parent).
		SetDateExpires(now.Add(CodeTtl)).
		SetDateCreated(now).
		Exec(ctx); err != nil {
		h.fail(w, r, "no code", err)
		return
	}

	// To the domain as it is written down, and never as the request named
	// it.
	to := h.Links.On(d.Host) + "/handoff/callback?" + url.Values{"code": {code}, "state": {state}}.Encode()
	w.Header().Set("Referrer-Policy", "no-referrer")
	http.Redirect(w, r, to, http.StatusSeeOther)
}

// take is a domain exchanging the code a browser came back with for a
// session of its own; see [Handoff].
//
// Whatever was wrong is one answer, to the page with `?signin=failed`, which
// draws a way to try again and does not start one by itself: which of them
// it was would tell whoever sent the browser how far they got. The log says
// which, and never the code.
func (h Handoff) take(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	pin, ok := apphost.From(ctx)
	if !ok {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
	http.SetCookie(w, &http.Cookie{Name: h.cookie(), Path: "/", MaxAge: -1, HttpOnly: true, Secure: h.secure(), SameSite: http.SameSiteLaxMode})
	refused := func(why string, err error) {
		log.From(ctx).WarnContext(ctx, "handoff: a sign-in on a workspace's domain did not finish", "workspace", pin.Alias, "why", why, slog.Any("err", err))
		http.Redirect(w, r, "/?signin=failed", http.StatusSeeOther)
	}

	q := r.URL.Query()
	code, state := q.Get("code"), q.Get("state")

	// Taken first, whatever comes of the rest: a code that reached a domain
	// is spent, a good one and one somebody else sent alike.
	v, err := h.claim(ctx, code)
	if err != nil {
		refused("the code could not be taken", err)
		return
	}

	kept, next := "", "/"
	if c, err := r.Cookie(h.cookie()); err == nil {
		var tail string
		kept, tail, _ = strings.Cut(c.Value, ".")
		// Held to a path here again, though this host wrote it: what a
		// browser is sent to at the end is never another site.
		if b, err := base64.RawURLEncoding.DecodeString(tail); err == nil {
			next = landing.Path(string(b))
		}
	}
	now := h.now()
	switch {
	case kept == "" || state == "" || subtle.ConstantTimeCompare([]byte(kept), []byte(state)) != 1:
		refused("not the round trip this browser started here", nil)
		return
	case v == nil:
		refused("no such code, or one already taken", nil)
		return
	case !now.Before(v.DateExpires):
		refused("a code past its minute", nil)
		return
	case v.Host != apphost.Host(r):
		refused("a code made for another host", nil)
		return
	case pdid.Id(v.Workspace) != pin.Workspace:
		refused("a code made for another workspace", nil)
		return
	case subtle.ConstantTimeCompare(v.State, h.Store.Kept(state)) != 1:
		refused("a code made for another round trip", nil)
		return
	}

	// Still a member, and still signed in at the service's: a code is a
	// minute old at most, and either may have ended in it.
	in, err := h.Db.Holder.Query().Where(
		holder.Account(v.Account),
		holder.TenantId(v.Workspace),
		holder.DateErasedIsNil(),
	).Exist(ctx)
	if err != nil {
		refused("the account's membership could not be told", err)
		return
	}
	if !in {
		refused("the account left the workspace", nil)
		return
	}
	parent, err := h.Db.Session.Query().Where(append(live(now), session.Id(v.Parent))...).Only(ctx)
	if err != nil {
		refused("the sign-in at the service's is over", err)
		return
	}

	// Good until the sign-in it was handed from is over, and no longer.
	s := authsession.Session{Id: pdid.Id(v.Account).String(), TenantId: pdid.Id(v.Workspace).String(), Grant: frame.Whole()}
	if parent.DateExpires != nil {
		s.Expires = *parent.DateExpires
	}
	_, c, err := h.Sessions.Mint(handedFrom(ctx, v.Parent), s)
	if err != nil {
		refused("no session", err)
		return
	}
	http.SetCookie(w, c)
	http.Redirect(w, r, next, http.StatusSeeOther)
}

// claim takes the code `code` is -- reads its row, and deletes it -- or
// answers nil for none, or one another request took first: of requests that
// bring one code together, one deletes its row.
func (h Handoff) claim(ctx context.Context, code string) (*ent.Handoff, error) {
	if code == "" {
		return nil, nil
	}
	v, err := h.Db.Handoff.Query().Where(handoff.Secret(h.Store.Kept(code))).Only(ctx)
	switch {
	case ent.IsNotFound(err):
		return nil, nil
	case err != nil:
		return nil, err
	}
	n, err := h.Db.Handoff.Delete().Where(handoff.Id(v.Id)).Exec(ctx)
	if err != nil || n != 1 {
		return nil, err
	}
	return v, nil
}

// leave is signing out on a domain; see [Handoff].
//
// A form's POST, as `/sso/logout` is: another site's carries no cookie of
// this host's -- it is SameSite -- and ends nothing.
func (h Handoff) leave(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	if _, ok := apphost.From(ctx); !ok {
		http.NotFound(w, r)
		return
	}
	held, err := h.Store.Leave(ctx, h.Sessions.KeyOf(r.Header.Values("Cookie")))
	if err != nil {
		log.From(ctx).ErrorContext(ctx, "handoff: a sign-out that ended nothing", slog.Any("err", err))
		http.Error(w, "cannot sign out just now", http.StatusServiceUnavailable)
		return
	}
	http.SetCookie(w, h.Sessions.End(ctx, ""))
	to := h.Links.Home(apphost.Page) + "/"
	if h.Issuer != nil {
		if u := h.Issuer.EndSession(ctx, held); u != "" {
			to = u
		}
	}
	http.Redirect(w, r, to, http.StatusSeeOther)
}

// domain is the active `app` domain at `to`, or nil for none.
func (h Handoff) domain(ctx context.Context, to string) (*ent.TenantDomain, error) {
	host := strings.TrimSuffix(strings.ToLower(strings.TrimSpace(to)), ".")
	if host == "" {
		return nil, nil
	}
	d, err := h.Db.TenantDomain.Query().Where(
		tenantdomain.Host(host),
		tenantdomain.Purpose("app"),
		tenantdomain.State("active"),
		tenantdomain.DateErasedIsNil(),
	).WithTenant().Only(ctx)
	if ent.IsNotFound(err) {
		return nil, nil
	}
	return d, err
}

func (h Handoff) fail(w http.ResponseWriter, r *http.Request, why string, err error) {
	log.From(r.Context()).ErrorContext(r.Context(), "handoff: "+why, slog.Any("err", err))
	http.Error(w, "cannot sign in just now", http.StatusServiceUnavailable)
}

// live is a session that is not over at `now`.
func live(now time.Time) []predicate.Session {
	return []predicate.Session{
		session.DateErasedIsNil(),
		session.Or(session.DateExpiresIsNil(), session.DateExpiresGT(now)),
		session.Or(session.DateIdleIsNil(), session.DateIdleGT(now)),
	}
}

type handedKey struct{}

// handedFrom is `ctx` with the session being minted on it handed from the
// sign-in `parent`, which [Store.Put] writes beside it.
func handedFrom(ctx context.Context, parent uuid.UUID) context.Context {
	return context.WithValue(ctx, handedKey{}, parent)
}

// secret is 32 random bytes, for a code or a state.
func secret() string {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return base64.RawURLEncoding.EncodeToString(b)
}

// madeHere answers whether `state` is one a domain makes ([secret]).
func madeHere(state string) bool {
	b, err := base64.RawURLEncoding.DecodeString(state)
	return err == nil && len(b) == 32
}
