// Package oidctest is an issuer for a test to sign in through, in the shape
// roster's is: Hydra's discovery, keys, authorization, token and end-session
// endpoints (design 9.10).
//
// It holds a relying party to what it has to send -- the client's secret in the
// `Authorization` header, the redirect it was registered with, the PKCE
// verifier for the challenge the browser carried -- because an issuer that took
// anything would let a relying party that sent nothing pass. Everything else it
// agrees with: who signs in is [Issuer.Subject].
package oidctest

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"testing"
	"time"

	"github.com/go-jose/go-jose/v4"
	"github.com/stretchr/testify/require"
)

// Issuer is the fake, and its server.
type Issuer struct {
	*httptest.Server

	// ClientId and Secret are the one client it knows, Redirect where that
	// client is registered to be sent back, and PostLogout where it may ask
	// to be sent after signing out.
	ClientId   string
	Secret     string
	Redirect   string
	PostLogout string

	// Subject is who the next sign-in is, and Claims what else its ID token
	// says -- or says instead: a claim here wins over the one the issuer would
	// have written, `nonce` and `aud` included.
	Subject string
	Claims  map[string]any

	// Refuse is the `error` the next authorization answers with instead of a
	// code: `access_denied`, for somebody who would not.
	Refuse string

	key *rsa.PrivateKey

	mu     sync.Mutex
	grants map[string]grant
	asked  url.Values
	sent   url.Values
	basic  string
	ended  url.Values
	tokens []string
}

// A grant is what one authorization asked for, kept for its code.
type grant struct {
	nonce, challenge, redirect string
}

// New stands one up for a client, closed when the test ends.
func New(t *testing.T, clientId, secret string) *Issuer {
	t.Helper()
	is, err := Unstarted(clientId, secret)
	require.NoError(t, err)
	is.Start()
	t.Cleanup(is.Close)
	return is
}

// Unstarted is one for a client, on a server not yet started: for what serves
// it outside a test, as the browser tests' issuer does -- at an address of its
// own, which is a listener put in before it starts (cmd/oidctest).
func Unstarted(clientId, secret string) (*Issuer, error) {
	k, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return nil, err
	}

	is := &Issuer{ClientId: clientId, Secret: secret, key: k, grants: map[string]grant{}}
	m := http.NewServeMux()
	m.HandleFunc("GET /.well-known/openid-configuration", func(w http.ResponseWriter, r *http.Request) {
		answer(w, http.StatusOK, map[string]any{
			"issuer":                                is.URL,
			"authorization_endpoint":                is.URL + "/oauth2/auth",
			"token_endpoint":                        is.URL + "/oauth2/token",
			"jwks_uri":                              is.URL + "/.well-known/jwks.json",
			"end_session_endpoint":                  is.URL + "/oauth2/sessions/logout",
			"response_types_supported":              []string{"code"},
			"subject_types_supported":               []string{"public"},
			"id_token_signing_alg_values_supported": []string{"RS256"},
		})
	})
	m.HandleFunc("GET /.well-known/jwks.json", func(w http.ResponseWriter, r *http.Request) {
		answer(w, http.StatusOK, jose.JSONWebKeySet{Keys: []jose.JSONWebKey{
			{Key: &k.PublicKey, KeyID: "one", Algorithm: "RS256", Use: "sig"},
		}})
	})
	m.HandleFunc("GET /oauth2/auth", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		is.mu.Lock()
		is.asked = q
		refuse := is.Refuse
		is.mu.Unlock()

		// What Hydra refuses to redirect at all: a client it does not know, or
		// a redirect that client was not registered with.
		if q.Get("client_id") != is.ClientId || q.Get("redirect_uri") != is.Redirect {
			http.Error(w, "the client or its redirect is not one registered here", http.StatusBadRequest)
			return
		}
		to, _ := url.Parse(is.Redirect)
		back := url.Values{"state": {q.Get("state")}}
		switch {
		case refuse != "":
			back.Set("error", refuse)
		case q.Get("response_type") != "code" || q.Get("code_challenge_method") != "S256" || q.Get("code_challenge") == "":
			back.Set("error", "invalid_request")
		default:
			code := token()
			is.mu.Lock()
			is.grants[code] = grant{nonce: q.Get("nonce"), challenge: q.Get("code_challenge"), redirect: q.Get("redirect_uri")}
			is.mu.Unlock()
			back.Set("code", code)
		}
		to.RawQuery = back.Encode()
		http.Redirect(w, r, to.String(), http.StatusFound)
	})
	m.HandleFunc("POST /oauth2/token", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		is.mu.Lock()
		is.sent = r.PostForm
		is.basic = r.Header.Get("Authorization")
		g, ok := is.grants[r.PostForm.Get("code")]
		delete(is.grants, r.PostForm.Get("code"))
		is.mu.Unlock()

		id, secret, basic := r.BasicAuth()
		switch {
		case !basic || id != url.QueryEscape(is.ClientId) || secret != url.QueryEscape(is.Secret):
			answer(w, http.StatusUnauthorized, map[string]string{"error": "invalid_client"})
			return
		case r.PostForm.Get("grant_type") != "authorization_code" || !ok:
			answer(w, http.StatusBadRequest, map[string]string{"error": "invalid_grant"})
			return
		case r.PostForm.Get("redirect_uri") != g.redirect:
			answer(w, http.StatusBadRequest, map[string]string{"error": "invalid_grant", "error_description": "redirect_uri"})
			return
		case challengeOf(r.PostForm.Get("code_verifier")) != g.challenge:
			answer(w, http.StatusBadRequest, map[string]string{"error": "invalid_grant", "error_description": "code_verifier"})
			return
		}

		is.mu.Lock()
		claims := map[string]any{
			"iss":   is.URL,
			"aud":   is.ClientId,
			"sub":   is.Subject,
			"nonce": g.nonce,
			"iat":   time.Now().Unix(),
			"exp":   time.Now().Add(time.Hour).Unix(),
		}
		for k, v := range is.Claims {
			claims[k] = v
		}
		is.mu.Unlock()
		raw, err := is.sign(claims)
		if err != nil {
			answer(w, http.StatusInternalServerError, map[string]string{"error": "server_error", "error_description": err.Error()})
			return
		}
		is.mu.Lock()
		is.tokens = append(is.tokens, raw)
		is.mu.Unlock()
		answer(w, http.StatusOK, map[string]any{
			"access_token": token(),
			"token_type":   "bearer",
			"expires_in":   3600,
			"id_token":     raw,
		})
	})
	m.HandleFunc("GET /oauth2/sessions/logout", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		is.mu.Lock()
		is.ended = q
		is.mu.Unlock()

		// Hydra's two rules about the way back: only with a hint, and only to
		// a URL the client registered.
		to := q.Get("post_logout_redirect_uri")
		switch {
		case to == "":
			w.Write([]byte("signed out at the issuer"))
		case q.Get("id_token_hint") == "" || to != is.PostLogout:
			http.Error(w, "post_logout_redirect_uri is not one this client registered", http.StatusBadRequest)
		default:
			http.Redirect(w, r, to, http.StatusFound)
		}
	})

	is.Server = httptest.NewUnstartedServer(m)
	return is, nil
}

// Next says who the next sign-in is, and what the next authorization answers
// with instead of a code -- empty for a code -- from somewhere other than the
// test that stood the issuer up: a request to the command serving it, which
// is answered while the issuer answers others.
func (is *Issuer) Next(subject, refuse string) {
	is.mu.Lock()
	defer is.mu.Unlock()
	is.Subject, is.Refuse = subject, refuse
}

// Sign is one ID token with whatever claims are given, for a test that wants
// to hand a relying party a bad one.
func (is *Issuer) Sign(t *testing.T, claims map[string]any) string {
	t.Helper()
	v, err := is.sign(claims)
	require.NoError(t, err)
	return v
}

func (is *Issuer) sign(claims map[string]any) (string, error) {
	b, err := json.Marshal(claims)
	if err != nil {
		return "", err
	}
	s, err := jose.NewSigner(jose.SigningKey{Algorithm: jose.RS256, Key: is.key}, (&jose.SignerOptions{}).WithHeader("kid", "one"))
	if err != nil {
		return "", err
	}
	o, err := s.Sign(b)
	if err != nil {
		return "", err
	}
	return o.CompactSerialize()
}

// Asked is what the last authorization request carried.
func (is *Issuer) Asked() url.Values {
	is.mu.Lock()
	defer is.mu.Unlock()
	return is.asked
}

// Sent is the form of the last token request, and Basic its `Authorization`.
func (is *Issuer) Sent() (url.Values, string) {
	is.mu.Lock()
	defer is.mu.Unlock()
	return is.sent, is.basic
}

// Ended is what the last end-session request carried, nil for none.
func (is *Issuer) Ended() url.Values {
	is.mu.Lock()
	defer is.mu.Unlock()
	return is.ended
}

// Issued is every ID token handed out, in order.
func (is *Issuer) Issued() []string {
	is.mu.Lock()
	defer is.mu.Unlock()
	return append([]string(nil), is.tokens...)
}

func challengeOf(verifier string) string {
	v := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(v[:])
}

// token is a value nobody can guess. crypto/rand's Read never fails: it ends
// the program rather than answer an error.
func token() string {
	b := make([]byte, 18)
	_, _ = rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}

func answer(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}
