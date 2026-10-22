package cmd_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/lesomnus/otx/log"
	"github.com/lesomnus/payday/auth/authsession"
	"github.com/lesomnus/payday/pdid"
	"google.golang.org/grpc/metadata"

	app "github.com/lesomnus/rove"
	"github.com/lesomnus/rove/cmd"
	"github.com/lesomnus/rove/internal/ent"
	"github.com/lesomnus/rove/internal/ent/handoff"
	"github.com/lesomnus/rove/internal/ent/hook"
	entsession "github.com/lesomnus/rove/internal/ent/session"
	"github.com/lesomnus/rove/server/account"
	"github.com/lesomnus/rove/server/session"
	"github.com/lesomnus/rove/server/sso"
)

// The hosts of the handoff's tests: a workspace's own domain, and the
// service's address ([newHosted]).
const (
	ownHost     = "assets.acme.example"
	serviceHost = "rove.example"
)

// send is a request to `host` as a browser makes it: with the cookies given
// and no others, and what its handlers log written to `logs` when it is not
// nil.
func (w *hosted) send(logs *bytes.Buffer, method, host, path string, cookies ...*http.Cookie) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, path, nil)
	r.Host = host
	for _, c := range cookies {
		if c != nil {
			r.AddCookie(&http.Cookie{Name: c.Name, Value: c.Value})
		}
	}
	if logs != nil {
		r = r.WithContext(log.Into(r.Context(), slog.New(slog.NewTextHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug}))))
	}
	rec := httptest.NewRecorder()
	w.h.ServeHTTP(rec, r)
	return rec
}

// trip is one browser's way from a workspace's domain to the service's
// address and back: the cookie the domain kept its state in, the state it
// sent, and the code the service gave for it.
type trip struct {
	state *http.Cookie
	value string
	code  string
}

// start is a domain sending a browser off to be signed in, to land on
// `next` once it is.
func (w *hosted) start(host, next string) trip {
	res := w.send(nil, http.MethodGet, host, "/handoff/start?next="+url.QueryEscape(next))
	w.x.Equal(http.StatusSeeOther, res.Code, res.Body.String())
	to, err := url.Parse(res.Header().Get("Location"))
	w.x.NoError(err)
	w.x.Equal("https://"+serviceHost+"/handoff", to.Scheme+"://"+to.Host+to.Path)
	w.x.Equal(host, to.Query().Get("to"))
	c := setCookie(res, "__Host-rove_handoff")
	w.x.NotNil(c, "the state, kept by the domain")
	return trip{state: c, value: to.Query().Get("state")}
}

// give is the service's address asked for a code for `tr`'s round trip, by a
// browser signed in there as `as`; the code is `tr`'s when one is given.
func (w *hosted) give(as context.Context, host string, tr *trip) *httptest.ResponseRecorder {
	res := w.send(nil, http.MethodGet, serviceHost, "/handoff?"+url.Values{"to": {host}, "state": {tr.value}}.Encode(), cookieIn(as))
	if to, err := url.Parse(res.Header().Get("Location")); err == nil && to.Path == "/handoff/callback" {
		tr.code = to.Query().Get("code")
	}
	return res
}

// take is a domain brought `tr`'s code and state, by a browser that kept
// `state` there.
func (w *hosted) take(logs *bytes.Buffer, host string, tr trip, state *http.Cookie) *httptest.ResponseRecorder {
	return w.send(logs, http.MethodGet, host, "/handoff/callback?"+url.Values{"code": {tr.code}, "state": {tr.value}}.Encode(), state)
}

// handOff is the whole round trip, for a browser signed in at the service's
// address as `as`: the session the domain hands it.
func (w *hosted) handOff(as context.Context, host string) *http.Cookie {
	tr := w.start(host, "/")
	res := w.give(as, host, &tr)
	w.x.Equal(http.StatusSeeOther, res.Code, res.Body.String())
	w.x.NotEmpty(tr.code)
	res = w.take(nil, host, tr, tr.state)
	w.x.Equal(http.StatusSeeOther, res.Code)
	w.x.Equal("/", res.Header().Get("Location"))
	c := setCookie(res, authsession.DefaultCookie)
	w.x.NotNil(c, "a session of the domain's")
	return c
}

// rowOf is the session row a cookie's key is.
func (w *hosted) rowOf(c *http.Cookie) *ent.Session {
	sum := sha256.Sum256([]byte(c.Value))
	return w.s.Ent.Session.Query().Where(entsession.Secret(sum[:])).OnlyX(context.Background())
}

// setCookie is the cookie a response set by `name`, or nil.
func setCookie(res *httptest.ResponseRecorder, name string) *http.Cookie {
	for _, c := range res.Result().Cookies() {
		if c.Name == name {
			return c
		}
	}
	return nil
}

// cookieIn is the session cookie a signed-in context carries, as a cookie.
func cookieIn(ctx context.Context) *http.Cookie {
	name, value, ok := strings.Cut(cookieOf(ctx), "=")
	if !ok {
		return nil
	}
	return &http.Cookie{Name: name, Value: value}
}

// withCookie is a context that carries `c`, as [world.session]'s carries a
// session.
func withCookie(c *http.Cookie) context.Context {
	return metadata.AppendToOutgoingContext(context.Background(), "cookie", c.Name+"="+c.Value)
}

// TestASignInIsHandedToAWorkspacesDomain: nobody signs in on a workspace's
// own domain (rove#155). The domain sends a browser to the service's address
// with a state it keeps in a cookie of its own; the service, where the
// browser is signed in, sends it back to that domain with a code; and the
// domain holds the state to its cookie, takes the code, and hands out a
// session: a cookie of that host's alone, bound to that workspace -- another
// one named there is refused, and at the service's address it is nobody. A
// code is good once, is written down as what it hashes to, and is never
// logged.
func TestASignInIsHandedToAWorkspacesDomain(t *testing.T) {
	w := newHosted(t)
	x := w.x
	ctx := context.Background()

	kim, err := w.s.Identity.AddPerson(ctx, "kim", "Kim", pdid.Nil)
	x.NoError(err)
	beta, err := w.s.Base.Tenant().Add(ctx, app.TenantAddRequest_builder{Alias: "beta"}.Build())
	x.NoError(err)
	as := w.session(w.here(kim, "member"))
	w.member(beta, kim, "owner")
	w.domain(w.tenant, ownHost, "app", "active")

	// Off to the service's address, with a state the domain keeps.
	tr := w.start(ownHost, "/@acme/assets?q=x")
	x.True(tr.state.Secure)
	x.True(tr.state.HttpOnly)
	x.Equal("/", tr.state.Path)
	x.Empty(tr.state.Domain)
	x.Equal(http.SameSiteLaxMode, tr.state.SameSite)

	// The service: a code, to the domain as it is written down.
	res := w.give(as, ownHost, &tr)
	x.Equal(http.StatusSeeOther, res.Code, res.Body.String())
	to, err := url.Parse(res.Header().Get("Location"))
	x.NoError(err)
	x.Equal("https://"+ownHost+"/handoff/callback", to.Scheme+"://"+to.Host+to.Path)
	x.Equal(tr.value, to.Query().Get("state"))
	x.Equal("no-referrer", res.Header().Get("Referrer-Policy"))
	rows, err := w.s.Ent.Handoff.Query().All(ctx)
	x.NoError(err)
	x.Len(rows, 1)
	sum := sha256.Sum256([]byte(tr.code))
	x.Equal(sum[:], rows[0].Secret, "written down as what it hashes to")
	x.Equal(ownHost, rows[0].Host)
	x.Equal(idOf(t, w.tenant), pdid.Id(rows[0].Workspace).String())
	x.WithinDuration(time.Now().Add(session.CodeTtl), rows[0].DateExpires, 5*time.Second)

	// The domain: the state held to its cookie, the code taken, a session.
	var logs bytes.Buffer
	res = w.take(&logs, ownHost, tr, tr.state)
	x.Equal(http.StatusSeeOther, res.Code)
	x.Equal("/@acme/assets?q=x", res.Header().Get("Location"), "where the browser was")
	x.Equal("no-store", res.Header().Get("Cache-Control"))
	c := setCookie(res, authsession.DefaultCookie)
	x.NotNil(c)
	x.True(c.Secure)
	x.True(c.HttpOnly)
	x.Equal("/", c.Path)
	x.Empty(c.Domain, "this host's alone")
	x.Equal(-1, setCookie(res, "__Host-rove_handoff").MaxAge, "the state is spent")
	x.Zero(w.s.Ent.Handoff.Query().CountX(ctx), "and so is the code")

	handed := withCookie(c)
	res = w.me(handed, ownHost, "")
	x.Equal(http.StatusOK, res.Code, res.Body.String())
	x.Equal(http.StatusForbidden, w.me(handed, ownHost, "beta").Code, "the account's other workspace, on acme's domain")
	x.Equal(http.StatusUnauthorized, w.me(handed, serviceHost, "acme").Code, "a session handed to a domain is nobody at the service's")
	x.Equal(http.StatusUnauthorized, w.at(handed, serviceHost, http.MethodGet, "/account", "").Code)
	acct := w.at(handed, ownHost, http.MethodGet, "/account", "")
	x.Equal(http.StatusOK, acct.Code, acct.Body.String())
	var a account.Account
	x.NoError(json.Unmarshal(acct.Body.Bytes(), &a))
	x.Len(a.Workspaces, 1)
	x.Equal("acme", a.Workspaces[0].Alias)
	agree := w.at(handed, ownHost, http.MethodPost, "/account/consents", `{"agree":{}}`, "Content-Type", "application/json")
	x.Equal(http.StatusForbidden, agree.Code, "what the account says of itself, it says at the service's")

	// Once: the same code again is no session.
	again := w.take(&logs, ownHost, tr, tr.state)
	x.Equal(http.StatusSeeOther, again.Code)
	x.Equal("/?signin=failed", again.Header().Get("Location"))
	x.Nil(setCookie(again, authsession.DefaultCookie))

	x.Contains(logs.String(), "already taken", "why a sign-in did not finish is logged")
	x.NotContains(logs.String(), tr.code, "and the code is not")
}

// TestAHandoffGoesToAnActiveAppDomainAlone: the service makes a code for an
// active app domain and sends it nowhere else -- a legacy one, a label
// domain, one not yet in use, the service's own host and a host that is
// nobody's are answered no, and nothing is sent anywhere -- and only for an
// account that is a member of the domain's workspace, signed in with a
// session that may do all it may. Nobody signed in is signed in first, and
// comes back. Each part is served where it is the one asked, and the domain
// is no place to sign in or out but by it.
func TestAHandoffGoesToAnActiveAppDomainAlone(t *testing.T) {
	w := newHosted(t)
	x := w.x
	ctx := context.Background()

	kim, err := w.s.Identity.AddPerson(ctx, "kim", "Kim", pdid.Nil)
	x.NoError(err)
	lee, err := w.s.Identity.AddPerson(ctx, "lee", "Lee", pdid.Nil)
	x.NoError(err)
	beta, err := w.s.Base.Tenant().Add(ctx, app.TenantAddRequest_builder{Alias: "beta"}.Build())
	x.NoError(err)
	inAcme := w.here(kim, "member")
	as := w.session(inAcme)
	outsider := w.session(w.member(beta, lee, "owner"))
	w.domain(w.tenant, ownHost, "app", "active")
	w.domain(w.tenant, "old.acme.example", "app", "legacy")
	w.domain(w.tenant, "acme.l.test", "label", "active")
	w.domain(beta, "next.beta.example", "app", "ready")

	tr := w.start(ownHost, "/")
	for _, host := range []string{"old.acme.example", "acme.l.test", "next.beta.example", serviceHost, "elsewhere.example", ""} {
		res := w.give(as, host, &tr)
		x.Equal(http.StatusBadRequest, res.Code, host)
		x.Empty(res.Header().Get("Location"), "%q: sent nowhere", host)
	}
	res := w.give(outsider, ownHost, &tr)
	x.Equal(http.StatusForbidden, res.Code, "an account that is not in acme")
	x.Contains(res.Body.String(), "not in @acme")
	grant, err := session.Grant([]string{app.PartyService_Me_FullMethodName})
	x.NoError(err)
	key := "a terminal's own key"
	sum := sha256.Sum256([]byte(key))
	x.NoError(w.s.Handoff.Store.Adopt(ctx, w.accountOf(inAcme), sum[:], grant, time.Now().Add(time.Hour), time.Now().Add(time.Hour)))
	narrowed := withCookie(&http.Cookie{Name: authsession.DefaultCookie, Value: key})
	x.Equal(http.StatusForbidden, w.give(narrowed, ownHost, &tr).Code, "a sign-in narrowed by `rove login --allow`")
	bad := tr
	bad.value = "made-up"
	x.Equal(http.StatusBadRequest, w.give(as, ownHost, &bad).Code, "a state no domain made")
	x.Zero(w.s.Ent.Handoff.Query().CountX(ctx), "no code for any of them")

	// Nobody signed in: the page signs them in, and comes back here.
	res = w.give(context.Background(), ownHost, &tr)
	x.Equal(http.StatusSeeOther, res.Code)
	back, err := url.Parse(res.Header().Get("Location"))
	x.NoError(err)
	x.Equal("/", back.Path)
	x.True(strings.HasPrefix(back.Query().Get("next"), "/handoff?"), back.String())

	// Each part where it is the one asked.
	x.Equal(http.StatusNotFound, w.send(nil, http.MethodGet, ownHost, "/handoff?"+url.Values{"to": {ownHost}, "state": {tr.value}}.Encode(), cookieIn(as)).Code, "a code is the service's to give")
	x.Equal(http.StatusNotFound, w.send(nil, http.MethodGet, serviceHost, "/handoff/start").Code)
	x.Equal(http.StatusNotFound, w.send(nil, http.MethodGet, serviceHost, "/handoff/callback?code=x&state=y").Code)
	x.Equal(http.StatusNotFound, w.send(nil, http.MethodPost, serviceHost, "/handoff/logout").Code)
	x.Equal(http.StatusNotFound, w.send(nil, http.MethodGet, "old.acme.example", "/handoff/start").Code, "a legacy domain hands nothing")

	// And the domain is no place to sign in or out but by it: what its page
	// is told is the handoff.
	x.Equal(http.StatusNotFound, w.send(nil, http.MethodDelete, ownHost, "/session", cookieIn(as)).Code)
	x.Equal(http.StatusNotFound, w.send(nil, http.MethodGet, ownHost, "/sso/login").Code)
	x.Equal(http.StatusNotFound, w.send(nil, http.MethodGet, ownHost, "/cli/logins?code=x", cookieIn(as)).Code, "a terminal is signed in at the service's")
	waysAt := func(host string) sso.Ways {
		res := w.send(nil, http.MethodGet, host, "/sso")
		x.Equal(http.StatusOK, res.Code, host)
		var v sso.Ways
		x.NoError(json.Unmarshal(res.Body.Bytes(), &v))
		return v
	}
	x.Equal(sso.Ways{Providers: []sso.Provider{}, Handoff: true}, waysAt(ownHost))
	x.Equal(sso.Ways{Providers: []sso.Provider{}, Password: true}, waysAt(serviceHost))

	// And the one it is for, however the request spells it: the code goes to
	// the domain as it is written down.
	res = w.give(as, "Assets.ACME.example.", &tr)
	x.Equal(http.StatusSeeOther, res.Code)
	to, err := url.Parse(res.Header().Get("Location"))
	x.NoError(err)
	x.Equal(ownHost, to.Host)
	x.NotEmpty(tr.code)
}

// TestAHandoffCodeIsGoodForItsRoundTripAlone: a code signs in the round trip
// it was made for -- the state the domain kept, the host, the workspace, the
// minute -- and nothing else: brought with another state, past its minute, or
// to another host or workspace than its own, it is spent, and signs nobody
// in. Of requests that bring one code together, one is signed in.
func TestAHandoffCodeIsGoodForItsRoundTripAlone(t *testing.T) {
	w := newHosted(t)
	x := w.x
	ctx := context.Background()

	kim, err := w.s.Identity.AddPerson(ctx, "kim", "Kim", pdid.Nil)
	x.NoError(err)
	beta, err := w.s.Base.Tenant().Add(ctx, app.TenantAddRequest_builder{Alias: "beta"}.Build())
	x.NoError(err)
	as := w.session(w.here(kim, "member"))
	w.member(beta, kim, "member")
	w.domain(w.tenant, ownHost, "app", "active")

	refused := func(res *httptest.ResponseRecorder, why string) {
		t.Helper()
		x.Equal(http.StatusSeeOther, res.Code, why)
		x.Equal("/?signin=failed", res.Header().Get("Location"), why)
		x.Nil(setCookie(res, authsession.DefaultCookie), why)
	}
	code := func() trip {
		tr := w.start(ownHost, "/")
		x.Equal(http.StatusSeeOther, w.give(as, ownHost, &tr).Code)
		return tr
	}

	// Another round trip's: its state in the cookie and in the request
	// alike, which agree with each other and not with the code.
	a, b := code(), w.start(ownHost, "/")
	refused(w.take(nil, ownHost, trip{value: b.value, code: a.code}, b.state), "a code made for another round trip")
	refused(w.take(nil, ownHost, a, a.state), "the code, spent though it was refused")

	// A state this browser did not keep here, or none.
	a, b = code(), w.start(ownHost, "/")
	refused(w.take(nil, ownHost, a, b.state), "a state this browser did not start here")
	refused(w.take(nil, ownHost, a, a.state), "spent")
	a = code()
	refused(w.take(nil, ownHost, a, nil), "no state kept here")

	// Past its minute, or made for another host or workspace.
	inBeta, err := pdid.From(beta.GetId())
	x.NoError(err)
	for why, change := range map[string]func(*ent.HandoffUpdate){
		"past its minute":            func(u *ent.HandoffUpdate) { u.SetDateExpires(time.Now().Add(-time.Second)) },
		"made for another host":      func(u *ent.HandoffUpdate) { u.SetHost("other.acme.example") },
		"made for another workspace": func(u *ent.HandoffUpdate) { u.SetWorkspace(inBeta.Uuid()) },
	} {
		a := code()
		sum := sha256.Sum256([]byte(a.code))
		u := w.s.Ent.Handoff.Update().Where(handoff.Secret(sum[:]))
		change(u)
		x.Equal(1, u.SaveX(ctx))
		refused(w.take(nil, ownHost, a, a.state), why)
	}
	x.Zero(w.s.Ent.Session.Query().Where(entsession.WorkspaceNotNil()).CountX(ctx), "nobody signed in by any of them")

	// Taken between this one's reading it and deleting it -- by another
	// request that brought it at the same time -- it is that one's, and this
	// one signs nobody in.
	a = code()
	first := true
	w.s.Ent.Handoff.Use(func(next ent.Mutator) ent.Mutator {
		return hook.HandoffFunc(func(ctx context.Context, m *ent.HandoffMutation) (ent.Value, error) {
			if first && m.Op().Is(ent.OpDelete) {
				first = false
				sum := sha256.Sum256([]byte(a.code))
				w.s.Ent.Handoff.Delete().Where(handoff.Secret(sum[:])).ExecX(ctx)
			}
			return next.Mutate(ctx, m)
		})
	})
	refused(w.take(nil, ownHost, a, a.state), "a code another request took first")

	// Together: one of them.
	a = code()
	var (
		mu  sync.Mutex
		got int
		wg  sync.WaitGroup
	)
	for range 8 {
		wg.Go(func() {
			if setCookie(w.take(nil, ownHost, a, a.state), authsession.DefaultCookie) != nil {
				mu.Lock()
				got++
				mu.Unlock()
			}
		})
	}
	wg.Wait()
	x.Equal(1, got, "one code, one session")
	x.Equal(1, w.s.Ent.Session.Query().Where(entsession.WorkspaceNotNil()).CountX(ctx))
}

// TestASessionOnAWorkspacesDomainLivesWithItsSignIn: a session handed to a
// workspace's domain is good while the sign-in at the service's address it
// was handed from is, and no longer -- not past that one's lifetime, not once
// somebody signs out there, and the sweep takes one whose sign-in is gone --
// and it keeps that one from idling out while it is used. Signing out on the
// domain ends both, and goes on to the service's front page; another site's
// form ends nothing.
func TestASessionOnAWorkspacesDomainLivesWithItsSignIn(t *testing.T) {
	w := newHosted(t)
	x := w.x
	ctx := context.Background()

	h := w.holder("kim", "member")
	w.domain(w.tenant, ownHost, "app", "active")

	// No longer than the sign-in it was handed from.
	as := w.session(h)
	parent := w.rowOf(cookieIn(as))
	ends := time.Now().Add(time.Hour)
	w.s.Ent.Session.UpdateOneId(parent.Id).SetDateExpires(ends).ExecX(ctx)
	c := w.handOff(as, ownHost)
	child := w.rowOf(c)
	x.Equal(parent.Id, *child.Parent)
	x.Equal(idOf(t, w.tenant), pdid.Id(*child.Workspace).String())
	x.WithinDuration(ends, *child.DateExpires, time.Second, "good until the sign-in it was handed from is over")

	// Used, it keeps that one: its idle clock moves that one's.
	soon := time.Now().Add(30 * time.Minute)
	w.s.Ent.Session.UpdateOneId(parent.Id).SetDateIdle(soon).ExecX(ctx)
	w.s.Ent.Session.UpdateOneId(child.Id).SetDateIdle(soon).ExecX(ctx)
	x.Equal(http.StatusOK, w.at(withCookie(c), ownHost, http.MethodGet, "/account", "").Code)
	x.True(w.s.Ent.Session.GetX(ctx, parent.Id).DateIdle.After(soon), "the sign-in at the service's, kept while the domain is used")

	// Signing out at the service's address ends it.
	x.Equal(http.StatusNoContent, w.send(nil, http.MethodDelete, serviceHost, "/session", cookieIn(as)).Code)
	x.Equal(http.StatusUnauthorized, w.me(withCookie(c), ownHost, "").Code, "signed out there, signed out here")
	x.False(w.s.Ent.Session.Query().Where(entsession.Id(child.Id)).ExistX(ctx))

	// Signing out on the domain ends both, and goes on to the service's.
	as = w.session(h)
	c = w.handOff(as, ownHost)
	res := w.send(nil, http.MethodPost, ownHost, "/handoff/logout", c)
	x.Equal(http.StatusSeeOther, res.Code)
	x.Equal("https://"+serviceHost+"/", res.Header().Get("Location"))
	x.Equal(-1, setCookie(res, authsession.DefaultCookie).MaxAge)
	x.Equal(http.StatusUnauthorized, w.me(withCookie(c), ownHost, "").Code)
	x.Equal(http.StatusUnauthorized, w.me(as, serviceHost, "acme").Code, "and the sign-in at the service's with it")

	// Another site's form carries no cookie of the domain's, and ends nothing.
	as = w.session(h)
	c = w.handOff(as, ownHost)
	x.Equal(http.StatusSeeOther, w.send(nil, http.MethodPost, ownHost, "/handoff/logout").Code)
	x.Equal(http.StatusOK, w.me(withCookie(c), ownHost, "").Code)

	// The sweep takes one whose sign-in is gone.
	child = w.rowOf(c)
	w.s.Ent.Session.DeleteOneId(*child.Parent).ExecX(ctx)
	x.NoError(w.s.Handoff.Store.Sweep(ctx))
	x.False(w.s.Ent.Session.Query().Where(entsession.Id(child.Id)).ExistX(ctx))
}

// TestAHandoffWaitsForTheAgreement: an account that has not agreed to what
// the deployment asks is handed nothing. It is sent to the page at the
// service's address, which asks it, and comes back once it has agreed.
func TestAHandoffWaitsForTheAgreement(t *testing.T) {
	w := newHostedWith(t, func(c *cmd.Config) {
		c.App.Consent.Terms = terms
		c.App.Consent.Privacy = privacy
	})
	x := w.x
	ctx := context.Background()

	as := w.session(w.holder("kim", "member"))
	w.domain(w.tenant, ownHost, "app", "active")

	tr := w.start(ownHost, "/")
	res := w.give(as, ownHost, &tr)
	x.Equal(http.StatusSeeOther, res.Code)
	back, err := url.Parse(res.Header().Get("Location"))
	x.NoError(err)
	x.Equal("/", back.Path, "to the page, which asks")
	x.True(strings.HasPrefix(back.Query().Get("next"), "/handoff?"), back.String())
	x.Zero(w.s.Ent.Handoff.Query().CountX(ctx))

	agreed := w.at(as, serviceHost, http.MethodPost, "/account/consents", `{"agree":{"terms":"`+terms.Version+`","privacy":"`+privacy.Version+`"}}`, "Content-Type", "application/json")
	x.Equal(http.StatusNoContent, agreed.Code, agreed.Body.String())
	x.Equal(http.StatusSeeOther, w.give(as, ownHost, &tr).Code)
	x.NotEmpty(tr.code, "and once it has agreed, a code")
}
