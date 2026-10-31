package sso

import (
	"testing"

	"github.com/stretchr/testify/require"
	"golang.org/x/oauth2"

	"github.com/lesomnus/loft/internal/oidctest"
)

// How the secret is sent is said, and is what the client is registered with:
// left to `x/oauth2`, it is probed for and the first answer kept for as long as
// the process runs, which an issuer whose registration changed under it never
// gets past. And the request asks for who somebody is and nothing else.
func TestTheExchangeSaysHowItSendsTheSecret(t *testing.T) {
	x := require.New(t)
	is := oidctest.New(t, "loft", "secret")
	rp, err := New(Config{Issuer: is.URL, ClientId: "loft", ClientSecret: "secret"}, "https://loft.example", true, nil, nil, nil)
	x.NoError(err)

	f, err := rp.discovered(t.Context())
	x.NoError(err)
	x.Equal(oauth2.AuthStyleInHeader, f.cfg.Endpoint.AuthStyle)
	x.Equal([]string{"openid"}, f.cfg.Scopes)
	x.Equal("https://loft.example/sso/callback", f.cfg.RedirectURL)
	x.Equal(is.URL+"/oauth2/sessions/logout", f.endSession)
}

// A configuration that cannot sign anybody in says so when it is read.
func TestAConfigurationThatCannotWorkIsRefused(t *testing.T) {
	x := require.New(t)
	_, err := New(Config{Issuer: "https://id.example", ClientSecret: "s"}, "https://loft.example", true, nil, nil, nil)
	x.ErrorContains(err, "client_id")
	_, err = New(Config{Issuer: "https://id.example", ClientId: "loft", ClientSecret: "env:LOFT_NO_SUCH_VARIABLE"}, "https://loft.example", true, nil, nil, nil)
	x.ErrorContains(err, "client_secret")
	_, err = New(Config{Issuer: "https://id.example", ClientId: "loft", ClientSecret: "s"}, "not a url", true, nil, nil, nil)
	x.Error(err)
	_, err = New(Config{Issuer: "https://id.example", ClientId: "loft", ClientSecret: "s", Providers: []Provider{{Label: "Google"}}}, "https://loft.example", true, nil, nil, nil)
	x.ErrorContains(err, "providers")

	// And no issuer is a deployment with no single sign-on, not a mistake.
	_, err = New(Config{}, "", true, nil, nil, nil)
	x.NoError(err)
}
