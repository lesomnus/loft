package cmd_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lesomnus/xli/cfg"
	"github.com/stretchr/testify/require"

	"github.com/lesomnus/loft/cmd"
	"github.com/lesomnus/loft/server/checkout"
)

// Made up, of the shape Paddle documents (`^(test|live)_[a-zA-Z0-9]{27}$`):
// never a token anybody issued.
const (
	sandboxToken = "test_000000000000000000000000000"
	liveToken    = "live_000000000000000000000000000"
)

// served is what `serve` answers a GET of `path` with, signed in as nobody.
func served(t *testing.T, w *world, path string) *httptest.ResponseRecorder {
	t.Helper()
	mux := http.NewServeMux()
	w.s.Routes(mux, w.c)
	h, err := cmd.Front(w.c, mux)
	w.x.NoError(err)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
	return rec
}

// TestTheDefaultPaymentLinkIsAPageOfItsOwn: a deployment with a checkout
// serves Paddle's default payment link at /billing/checkout to anybody --
// nobody signs in to it -- and that page alone is let load Paddle.js, frame
// the environment's checkout and take its styles: no other answer's policy
// names Paddle. With none configured there is no page, and nothing names
// Paddle anywhere.
func TestTheDefaultPaymentLinkIsAPageOfItsOwn(t *testing.T) {
	w := newWorldWith(t, func(c *cmd.Config) {
		c.App.Billing.Checkout = checkout.Config{Environment: checkout.Sandbox, ClientToken: checkout.NewToken(sandboxToken)}
	})
	x := w.x

	res := served(t, w, "/billing/checkout?_ptxn=txn_01h2b0qpjc0xt8k5aw6nsdec4p")
	x.Equal(http.StatusOK, res.Code)
	x.Contains(res.Body.String(), `data-token="`+sandboxToken+`"`)
	policy := res.Header().Get("Content-Security-Policy-Report-Only")
	x.Contains(policy, "script-src 'self' https://cdn.paddle.com;")
	x.Contains(policy, "style-src 'self' 'unsafe-inline' https://sandbox-cdn.paddle.com;")
	x.Contains(policy, "frame-src https://sandbox-buy.paddle.com;")
	x.Contains(policy, "frame-ancestors 'none'", "and is framed by nobody, as every page")
	x.NotContains(policy, "https://buy.paddle.com", "the live checkout is none of the sandbox's")
	x.Equal("frame-ancestors 'none'", res.Header().Get("Content-Security-Policy"))

	// The page alone: the app's own, an API path and what the page loads
	// of its own are held to the policy every page is.
	for _, p := range []string{"/healthz", "/billing/checkout.js", "/billing", "/@acme/"} {
		res := served(t, w, p)
		v := res.Header().Get("Content-Security-Policy-Report-Only")
		x.NotEmpty(v, p)
		x.NotContains(v, "paddle", p)
		x.Contains(v, "style-src 'self';", p)
	}

	t.Run("enforced", func(t *testing.T) {
		w.c.App.Csp = "enforce"
		res := served(t, w, "/billing/checkout")
		require.Contains(t, res.Header().Get("Content-Security-Policy"), "frame-src https://sandbox-buy.paddle.com;")
		require.Empty(t, res.Header().Get("Content-Security-Policy-Report-Only"))
		w.c.App.Csp = ""
	})

	t.Run("none configured", func(t *testing.T) {
		x := require.New(t)
		w := newWorld(t)
		for _, p := range []string{"/billing/checkout", "/billing/checkout?_ptxn=txn_01h2b0qpjc0xt8k5aw6nsdec4p", "/billing/checkout.js"} {
			res := served(t, w, p)
			x.Equal(http.StatusNotFound, res.Code, p)
			x.NotContains(res.Header().Get("Content-Security-Policy-Report-Only"), "paddle", p)
		}
		x.NotContains(served(t, w, "/healthz").Header().Get("Content-Security-Policy-Report-Only"), "paddle")
	})
}

// TestATokenOfTheOtherEnvironmentIsRefusedWhereItIsWritten: the checkout is
// read with the configuration, and a client-side token that is not the
// environment's -- or is no token, or is a secret -- is refused as the
// configuration is loaded, at the line and column it is written on; and so
// is a block that says one half of a checkout. A block put together by hand
// is refused by `Build` before it opens anything.
func TestATokenOfTheOtherEnvironmentIsRefusedWhereItIsWritten(t *testing.T) {
	x := require.New(t)

	load := func(text string, environ ...string) error {
		path := filepath.Join(t.TempDir(), "loft.yaml")
		x.NoError(os.WriteFile(path, []byte(text), 0o600))
		_, err := cfg.New(cmd.Name, &cmd.Config{}).Read(path, environ)
		return err
	}

	x.NoError(load("app:\n  billing:\n    checkout:\n      environment: sandbox\n      client_token: " + sandboxToken + "\n"))
	x.NoError(load("app:\n  billing:\n    checkout:\n      environment: live\n      client_token: " + liveToken + "\n"))
	x.NoError(load("app: {}\n"), "no checkout")

	err := load("app:\n  billing:\n    checkout:\n      environment: live\n      client_token: " + sandboxToken + "\n")
	x.ErrorContains(err, "loft.yaml:5:21: app.billing.checkout.client_token: is not the live's: a client-side token there begins live_")
	x.NotContains(err.Error(), sandboxToken)

	err = load("app:\n  billing:\n    checkout:\n      environment: sandbox\n      client_token: " + liveToken + "\n")
	x.ErrorContains(err, "loft.yaml:5:21: app.billing.checkout.client_token: is not the sandbox's")

	// Of a sandbox API key's shape, made up as the test runs: Paddle looks
	// for its keys in public repositories.
	key := "pdl_" + "sdbx_" + "apikey_" + strings.Repeat("0", 39)
	err = load("app:\n  billing:\n    checkout:\n      environment: sandbox\n      client_token: " + key + "\n")
	x.ErrorContains(err, "loft.yaml:5:21: app.billing.checkout.client_token: is one of Paddle's secrets")
	x.NotContains(err.Error(), key)

	err = load("app:\n  billing:\n    checkout:\n      environment: production\n      client_token: " + sandboxToken + "\n")
	x.ErrorContains(err, `loft.yaml:4:20: app.billing.checkout.environment: "production" is neither sandbox nor live`)

	err = load("app:\n  billing:\n    checkout:\n      environment: sandbox\n")
	x.ErrorContains(err, "app.billing.checkout: client_token: none")

	// From the environment, it is said to be the variable's.
	err = load("app:\n  billing:\n    checkout:\n      environment: live\n", "LOFT_APP_BILLING_CHECKOUT_CLIENT_TOKEN="+sandboxToken)
	x.ErrorContains(err, "LOFT_APP_BILLING_CHECKOUT_CLIENT_TOKEN: is not the live's")

	c := cmd.Config{}
	c.App.Billing.Checkout = checkout.Config{Environment: checkout.Live, ClientToken: checkout.NewToken(sandboxToken)}
	_, err = cmd.Build(context.Background(), c)
	x.ErrorContains(err, "app.billing.checkout.client_token: is not the live's")
}
