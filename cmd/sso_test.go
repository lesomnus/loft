package cmd_test

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lesomnus/payday/pdid"
	"github.com/stretchr/testify/require"

	"github.com/lesomnus/rove/cmd"
	"github.com/lesomnus/rove/internal/oidctest"
	"github.com/lesomnus/rove/server/sso"
)

// The sign-in through roster's issuer, against an issuer that holds this
// deployment to what a relying party has to send (`internal/oidctest`), and
// through the routes the HTTP listener serves (`cmd.Server.Routes`).

// ssoWorld is a deployment that signs people in through an issuer, served
// over HTTP at the address the issuer sends browsers back to.
type ssoWorld struct {
	*world
	is  *oidctest.Issuer
	app *httptest.Server
}

func newSsoWorld(t *testing.T, with func(*cmd.Config)) *ssoWorld {
	t.Helper()
	var h http.Handler
	app := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { h.ServeHTTP(w, r) }))
	t.Cleanup(app.Close)

	is := oidctest.New(t, "rove", "the-client-secret")
	is.Redirect = app.URL + "/sso/callback"
	is.PostLogout = app.URL + "/"
	t.Setenv("ROVE_TEST_SSO_SECRET", "the-client-secret")

	// The built UI as well, which is `GET /` on the same mux: the routes
	// have to sit beside it, and once did not.
	web := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(web, "index.html"), []byte("<!doctype html><title>Rove</title>"), 0o644))

	w := newWorldWith(t, func(c *cmd.Config) {
		c.App.PublicUrl = app.URL
		c.App.AppUrl = app.URL
		c.App.Web = web
		c.Auth.Sso = sso.Config{
			Issuer:       is.URL,
			ClientId:     "rove",
			ClientSecret: "env:ROVE_TEST_SSO_SECRET",
			Providers: []sso.Provider{
				{Connection: "google", Label: "Google", Mark: "google"},
				{Connection: "microsoft", Label: "Microsoft", Mark: "microsoft"},
				{Connection: "github", Label: "GitHub", Mark: "github"},
			},
		}
		if with != nil {
			with(c)
		}
	})
	mux := http.NewServeMux()
	w.s.Routes(mux, w.c)
	h = mux

	return &ssoWorld{world: w, is: is, app: app}
}

// browser is a browser that keeps cookies and follows nothing, so each hop
// can be looked at.
func (w *ssoWorld) browser() *http.Client {
	jar, err := cookiejar.New(nil)
	w.x.NoError(err)
	return &http.Client{
		Jar:           jar,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
}

func get(x *require.Assertions, b *http.Client, at string) *http.Response {
	res, err := b.Get(at)
	x.NoError(err)
	res.Body.Close()
	return res
}

// through is the whole round trip from the button: this deployment, the
// issuer, and back, answering where the callback sent the browser.
func (w *ssoWorld) through(b *http.Client, query string) *http.Response {
	res := get(w.x, b, w.app.URL+"/sso/login?"+query)
	w.x.Equal(http.StatusFound, res.StatusCode, "the button did not leave for the issuer")
	res = get(w.x, b, res.Header.Get("Location"))
	w.x.Equal(http.StatusFound, res.StatusCode, "the issuer did not send the browser back")
	return get(w.x, b, res.Header.Get("Location"))
}

// account is what `GET /account` answers this browser.
func (w *ssoWorld) account(b *http.Client) (int, map[string]any) {
	res, err := b.Get(w.app.URL + "/account")
	w.x.NoError(err)
	defer res.Body.Close()
	var v map[string]any
	if res.StatusCode == http.StatusOK {
		w.x.NoError(json.NewDecoder(res.Body).Decode(&v))
	}
	return res.StatusCode, v
}

func TestSomebodySignsInThroughTheIssuer(t *testing.T) {
	w := newSsoWorld(t, nil)
	x := w.x
	ctx := context.Background()

	park, err := w.s.Identity.AddPerson(ctx, "park", "박", pdid.Nil)
	x.NoError(err)
	w.is.Subject = park.Id.String()
	b := w.browser()

	// What the page draws: the three buttons, and the form beside them.
	res, err := b.Get(w.app.URL + "/sso")
	x.NoError(err)
	var ways sso.Ways
	x.NoError(json.NewDecoder(res.Body).Decode(&ways))
	res.Body.Close()
	x.True(ways.Sso)
	x.True(ways.Password)
	x.Len(ways.Providers, 3)
	x.Equal("github", ways.Providers[2].Connection)

	// The button: to the issuer, naming the connection, with PKCE and a
	// nonce, and the state bound to this browser.
	res = get(x, b, w.app.URL+"/sso/login?with=github&next="+url.QueryEscape("/@acme/assets?q=rack"))
	x.Equal(http.StatusFound, res.StatusCode)
	to, err := url.Parse(res.Header.Get("Location"))
	x.NoError(err)
	x.True(strings.HasPrefix(to.String(), w.is.URL+"/oauth2/auth"), to.String())
	q := to.Query()
	x.Equal("rove", q.Get("client_id"))
	x.Equal(w.app.URL+"/sso/callback", q.Get("redirect_uri"))
	x.Equal("openid", q.Get("scope"), "this deployment asks for who somebody is and nothing about them")
	x.Equal("github", q.Get("connection"))
	x.Equal("S256", q.Get("code_challenge_method"))
	x.NotEmpty(q.Get("nonce"))
	var state *http.Cookie
	for _, c := range res.Cookies() {
		if c.Name == "rove_sso" {
			state = c
		}
	}
	x.NotNil(state, "no cookie binds the browser to the round trip")
	x.Equal(q.Get("state"), state.Value)
	x.True(state.HttpOnly)
	x.Equal("/sso", state.Path)

	// The issuer, and back: the code exchanged, and the browser where it was
	// going, signed in.
	res = get(x, b, to.String())
	x.Equal(http.StatusFound, res.StatusCode)
	res = get(x, b, res.Header.Get("Location"))
	x.Equal(http.StatusSeeOther, res.StatusCode)
	x.Equal("/@acme/assets?q=rack", res.Header.Get("Location"))

	// The secret went in the header, as the client is registered, and the
	// verifier for the challenge went with the code.
	form, basic := w.is.Sent()
	x.Equal("Basic "+base64.StdEncoding.EncodeToString([]byte("rove:the-client-secret")), basic)
	x.Empty(form.Get("client_secret"), "the secret was sent in the body as well")
	x.NotEmpty(form.Get("code_verifier"))

	// Signed in as the account `sub` names, and given a workspace of its
	// own for having none.
	code, a := w.account(b)
	x.Equal(http.StatusOK, code)
	x.Equal("park", a["login"])
	x.Len(a["workspaces"], 1)
	x.Equal("박의 워크스페이스", a["workspaces"].([]any)[0].(map[string]any)["name"])

	// Signing out ends the session here, then sends the browser to the
	// issuer with the token it signed in with, which brings it back to `/`.
	res, err = b.Post(w.app.URL+"/sso/logout", "application/x-www-form-urlencoded", nil)
	x.NoError(err)
	res.Body.Close()
	x.Equal(http.StatusSeeOther, res.StatusCode)
	end, err := url.Parse(res.Header.Get("Location"))
	x.NoError(err)
	x.True(strings.HasPrefix(end.String(), w.is.URL+"/oauth2/sessions/logout"), end.String())
	x.Equal("rove", end.Query().Get("client_id"))
	x.Equal(w.is.Issued()[0], end.Query().Get("id_token_hint"))
	x.Equal(w.app.URL+"/", end.Query().Get("post_logout_redirect_uri"))

	code, _ = w.account(b)
	x.Equal(http.StatusUnauthorized, code, "the session outlived signing out")
	res = get(x, b, end.String())
	x.Equal(http.StatusFound, res.StatusCode, "the issuer would not send the browser back")
	x.Equal(w.app.URL+"/", res.Header.Get("Location"))

	// And the same account again is the same account, with the workspace it
	// already has.
	res = w.through(b, "with=google")
	x.Equal(http.StatusSeeOther, res.StatusCode)
	x.Equal("/", res.Header.Get("Location"))
	_, a = w.account(b)
	x.Len(a["workspaces"], 1)
}

func TestASignInThroughTheIssuerIsHeldToWhatItStarted(t *testing.T) {
	w := newSsoWorld(t, nil)
	ctx := context.Background()

	park, err := w.s.Identity.AddPerson(ctx, "park", "박", pdid.Nil)
	w.x.NoError(err)
	w.is.Subject = park.Id.String()

	// signedIn is whether a browser ended the round trip with a session --
	// and a session that answers nothing is still one that should not have
	// been written, so the table is asked too.
	sessions := func() int {
		n, err := w.s.Ent.Session.Query().Count(ctx)
		w.x.NoError(err)
		return n
	}
	signedIn := func(b *http.Client) bool {
		code, _ := w.account(b)
		return code == http.StatusOK
	}

	t.Run("a callback this browser did not start", func(t *testing.T) {
		x := require.New(t)
		b := w.browser()
		res := get(x, b, w.app.URL+"/sso/login?with=google")
		at := get(x, b, res.Header.Get("Location")).Header.Get("Location")

		// Somebody else's browser, with no cookie.
		res = get(x, w.browser(), at)
		x.Equal(http.StatusBadRequest, res.StatusCode)

		// The browser that started it, but a state of its own making.
		u, _ := url.Parse(at)
		q := u.Query()
		q.Set("state", "made-up")
		u.RawQuery = q.Encode()
		res = get(x, b, u.String())
		x.Equal(http.StatusBadRequest, res.StatusCode)
		x.False(signedIn(b))
	})

	t.Run("a state is spent once", func(t *testing.T) {
		x := require.New(t)
		b := w.browser()
		res := get(x, b, w.app.URL+"/sso/login?with=google")
		state := ""
		for _, c := range res.Cookies() {
			if c.Name == "rove_sso" {
				state = c.Value
			}
		}
		at := get(x, b, res.Header.Get("Location")).Header.Get("Location")
		x.Equal(http.StatusSeeOther, get(x, b, at).StatusCode)

		// The same callback again, with the cookie put back.
		u, _ := url.Parse(w.app.URL)
		b.Jar.SetCookies(u, []*http.Cookie{{Name: "rove_sso", Value: state, Path: "/sso"}})
		x.Equal(http.StatusBadRequest, get(x, b, at).StatusCode)
	})

	for _, c := range []struct {
		name   string
		claims map[string]any
	}{
		{"a token for another sign-in", map[string]any{"nonce": "somebody-else's"}},
		{"a token for another client", map[string]any{"aud": "another-app"}},
		{"a token from another issuer", map[string]any{"iss": "https://elsewhere.example"}},
		{"a token that has run out", map[string]any{"exp": 1700000000}},
		{"a token naming nobody in the directory", map[string]any{"sub": pdid.New(1).String()}},
	} {
		t.Run(c.name, func(t *testing.T) {
			x := require.New(t)
			w.is.Claims = c.claims
			t.Cleanup(func() { w.is.Claims = nil })

			b := w.browser()
			before := sessions()
			res := w.through(b, "with=google")
			x.Equal(http.StatusSeeOther, res.StatusCode)
			x.Equal("/?signin=failed", res.Header.Get("Location"))
			x.False(signedIn(b), "a browser was signed in by a token that should not have")
			x.Equal(before, sessions(), "a session was written for a token that should not have")
		})
	}

	t.Run("somebody who said no at the issuer", func(t *testing.T) {
		x := require.New(t)
		w.is.Refuse = "access_denied"
		t.Cleanup(func() { w.is.Refuse = "" })

		b := w.browser()
		res := w.through(b, "with=google")
		x.Equal("/?signin=refused", res.Header.Get("Location"))
		x.False(signedIn(b))
	})

	t.Run("a connection this deployment does not offer", func(t *testing.T) {
		x := require.New(t)
		x.Equal(http.StatusBadRequest, get(x, w.browser(), w.app.URL+"/sso/login?with=okta").StatusCode)
	})

	// A sign-in is a redirect, and one to wherever a link said would land
	// somebody who just signed in on a page made to look like this one.
	t.Run("it lands on this deployment and nowhere else", func(t *testing.T) {
		for _, next := range []string{"https://evil.example/", "//evil.example/x", "/\\evil.example", "evil", "javascript:alert(1)", "/%2Fevil.example/ x", "/%2Fevil.example/\""} {
			x := require.New(t)
			b := w.browser()
			res := w.through(b, "with=google&next="+url.QueryEscape(next))
			x.Equal("/", res.Header.Get("Location"), next)
		}
	})
}

// TestAPasswordIsNoWayInWhereTheIssuerIsTheOnlyOne is `auth.sso_only` (D28).
func TestAPasswordIsNoWayInWhereTheIssuerIsTheOnlyOne(t *testing.T) {
	w := newSsoWorld(t, func(c *cmd.Config) { c.Auth.SsoOnly = true })
	x := w.x
	ctx := context.Background()

	park, err := w.s.Identity.AddPerson(ctx, "park", "박", pdid.Nil)
	x.NoError(err)
	x.NoError(w.s.Identity.SetPassword(ctx, park.Id, "correct horse"))

	res, err := w.browser().Post(w.app.URL+"/session", "application/json", bytes.NewBufferString(`{"login":"park","password":"correct horse"}`))
	x.NoError(err)
	res.Body.Close()
	x.Equal(http.StatusForbidden, res.StatusCode)

	res, err = w.browser().Get(w.app.URL + "/sso")
	x.NoError(err)
	defer res.Body.Close()
	var ways sso.Ways
	x.NoError(json.NewDecoder(res.Body).Decode(&ways))
	x.False(ways.Password, "the page is told to draw a form that answers no")

	// And the issuer still lets them in.
	w.is.Subject = park.Id.String()
	b := w.browser()
	x.Equal(http.StatusSeeOther, w.through(b, "with=google").StatusCode)
	code, _ := w.account(b)
	x.Equal(http.StatusOK, code)

	t.Run("and with no issuer it is nobody's", func(t *testing.T) {
		x := require.New(t)
		c := w.c
		c.Auth.Sso = sso.Config{}
		_, err := cmd.Build(ctx, c)
		x.ErrorContains(err, "auth.sso_only")
	})
}

// TestASessionAPasswordMadeEndsHereAlone: beside an issuer, a session a
// password made has nothing at the issuer to end, and signing out does not
// send the browser there.
func TestASessionAPasswordMadeEndsHereAlone(t *testing.T) {
	w := newSsoWorld(t, nil)
	x := w.x
	ctx := context.Background()

	park, err := w.s.Identity.AddPerson(ctx, "park", "박", pdid.Nil)
	x.NoError(err)
	x.NoError(w.s.Identity.SetPassword(ctx, park.Id, "correct horse"))

	b := w.browser()
	res, err := b.Post(w.app.URL+"/session", "application/json", bytes.NewBufferString(`{"login":"park","password":"correct horse"}`))
	x.NoError(err)
	res.Body.Close()
	x.Equal(http.StatusNoContent, res.StatusCode)

	res, err = b.Post(w.app.URL+"/sso/logout", "application/x-www-form-urlencoded", nil)
	x.NoError(err)
	body, _ := io.ReadAll(res.Body)
	res.Body.Close()
	x.Equal(http.StatusSeeOther, res.StatusCode, string(body))
	x.Equal("/", res.Header.Get("Location"))
	x.Nil(w.is.Ended(), "the issuer was asked to end a session it never had")
	code, _ := w.account(b)
	x.Equal(http.StatusUnauthorized, code)
}
