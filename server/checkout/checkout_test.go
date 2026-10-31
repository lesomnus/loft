package checkout_test

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/lesomnus/loft/server/checkout"
)

// Made up, of the shape Paddle documents: never a token anybody issued.
const (
	sandboxToken = "test_000000000000000000000000000"
	liveToken    = "live_000000000000000000000000000"
)

// apiKey is of the shape of a sandbox API key of Paddle's, made up and made
// as the test runs: Paddle looks for its keys in public repositories, and a
// file with one in it is a key somebody reports.
var apiKey = "pdl_" + "sdbx_" + "apikey_" + strings.Repeat("0", 39)

func get(t *testing.T, mux *http.ServeMux, path string) (*http.Response, string) {
	t.Helper()
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
	res := rec.Result()
	b, err := io.ReadAll(res.Body)
	require.NoError(t, err)
	return res, string(b)
}

// TestThePageOpensTheCheckoutTheLinkNames: with `_ptxn`, the page loads
// Paddle.js from Paddle's CDN and its own script, which is handed the
// environment and the token the configuration says, and says the checkout is
// opening; without, it says there is nothing to pay here and links home. It
// reads nobody's session, and never what the transaction is.
func TestThePageOpensTheCheckoutTheLinkNames(t *testing.T) {
	x := require.New(t)
	mux := http.NewServeMux()
	checkout.Config{Environment: checkout.Sandbox, ClientToken: checkout.NewToken(sandboxToken)}.Mount(mux)

	res, body := get(t, mux, "/billing/checkout?_ptxn=txn_01h2b0qpjc0xt8k5aw6nsdec4p")
	x.Equal(http.StatusOK, res.StatusCode)
	x.Equal("text/html; charset=utf-8", res.Header.Get("Content-Type"))
	x.Contains(body, `<script src="https://cdn.paddle.com/paddle/v2/paddle.js"></script>`)
	x.Contains(body, `<script src="/billing/checkout.js" defer></script>`)
	x.Contains(body, `data-environment="sandbox"`)
	x.Contains(body, `data-token="`+sandboxToken+`"`)
	x.Contains(body, "결제창을 여는 중입니다")
	x.NotContains(body, "txn_01h2b0qpjc0xt8k5aw6nsdec4p", "Paddle.js reads the transaction from the address itself")
	x.NotContains(body, "결제할 것이 없습니다")

	res, body = get(t, mux, "/billing/checkout")
	x.Equal(http.StatusOK, res.StatusCode)
	x.Contains(body, "여기서 결제할 것이 없습니다")
	x.Contains(body, `<a href="/">처음으로</a>`)
	x.Contains(body, `data-token="`+sandboxToken+`"`, "Paddle.js is there all the same, as a default payment link's page has it")
	x.NotContains(body, "결제창을 여는 중")

	_, body = get(t, mux, "/billing/checkout?_ptxn=")
	x.Contains(body, "여기서 결제할 것이 없습니다", "an empty transaction is none")

	// The script is the page's own: it sets the sandbox before it
	// initializes Paddle.js with the token the page carries.
	res, js := get(t, mux, "/billing/checkout.js")
	x.Equal(http.StatusOK, res.StatusCode)
	x.Equal("text/javascript; charset=utf-8", res.Header.Get("Content-Type"))
	x.Contains(js, "at.dataset.environment === 'sandbox'")
	x.Contains(js, "paddle.Environment.set('sandbox')")
	x.Contains(js, "token: at.dataset.token")
	x.Less(strings.Index(js, "Environment.set"), strings.Index(js, "paddle.Initialize("), "the environment first")

	res, _ = get(t, mux, "/billing/checkout.css")
	x.Equal(http.StatusOK, res.StatusCode)
	x.Equal("text/css; charset=utf-8", res.Header.Get("Content-Type"))

	t.Run("live", func(t *testing.T) {
		x := require.New(t)
		mux := http.NewServeMux()
		checkout.Config{Environment: checkout.Live, ClientToken: checkout.NewToken(liveToken)}.Mount(mux)
		_, body := get(t, mux, "/billing/checkout?_ptxn=txn_01h2b0qpjc0xt8k5aw6nsdec4p")
		x.Contains(body, `data-environment="live"`)
		x.Contains(body, `data-token="`+liveToken+`"`)
	})
}

// TestNoCheckoutIsNoPage: a deployment that configures no checkout serves no
// page -- 404, for the page and what it loads, rather than the built UI's
// page that answers every path it does not have.
func TestNoCheckoutIsNoPage(t *testing.T) {
	x := require.New(t)
	mux := http.NewServeMux()
	mux.HandleFunc("GET /", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("the app")) })
	checkout.Config{}.Mount(mux)

	for _, p := range []string{"/billing/checkout", "/billing/checkout?_ptxn=txn_01h2b0qpjc0xt8k5aw6nsdec4p", "/billing/checkout.js", "/billing/checkout.css"} {
		res, body := get(t, mux, p)
		x.Equal(http.StatusNotFound, res.StatusCode, p)
		x.NotContains(body, "paddle", p)
	}
}

// TestWhatThePageLoadsFromPaddleIsTheEnvironments: the sources of the page's
// policy -- Paddle.js from its CDN either way, and the checkout's frame and
// Paddle.js's stylesheet from the environment's own hosts -- and nothing with
// a wildcard.
func TestWhatThePageLoadsFromPaddleIsTheEnvironments(t *testing.T) {
	x := require.New(t)

	x.Equal(map[string][]string{
		"script-src": {"https://cdn.paddle.com"},
		"style-src":  {"'unsafe-inline'", "https://sandbox-cdn.paddle.com"},
		"frame-src":  {"https://sandbox-buy.paddle.com"},
	}, checkout.Sources(checkout.Sandbox))
	x.Equal(map[string][]string{
		"script-src": {"https://cdn.paddle.com"},
		"style-src":  {"'unsafe-inline'", "https://cdn.paddle.com"},
		"frame-src":  {"https://buy.paddle.com"},
	}, checkout.Sources(checkout.Live))
	for _, e := range []checkout.Environment{checkout.Sandbox, checkout.Live} {
		for _, vs := range checkout.Sources(e) {
			for _, v := range vs {
				x.NotContains(v, "*")
			}
		}
	}
}

// TestATokenIsTheEnvironments: a client-side token of the shape Paddle
// documents, of the environment the block says -- and never what it was in
// what is said of one that is not, since what was put there by mistake may be
// a secret.
func TestATokenIsTheEnvironments(t *testing.T) {
	x := require.New(t)

	check := func(e checkout.Environment, token string) error {
		return checkout.Config{Environment: e, ClientToken: checkout.NewToken(token)}.Check()
	}

	x.NoError(check(checkout.Sandbox, sandboxToken))
	x.NoError(check(checkout.Live, liveToken))
	x.NoError(checkout.Config{}.Check(), "no checkout")

	for _, c := range []struct {
		env     checkout.Environment
		token   string
		says    string
		dropped string
	}{
		{checkout.Live, sandboxToken, "app.billing.checkout.client_token: is not the live's: a client-side token there begins live_", sandboxToken},
		{checkout.Sandbox, liveToken, "app.billing.checkout.client_token: is not the sandbox's: a client-side token there begins test_", liveToken},
		{checkout.Sandbox, apiKey, "never goes to a page", apiKey},
		{checkout.Sandbox, "test_short", "27 letters and digits", "test_short"},
		{checkout.Sandbox, "test_00000000000000000000000000-", "27 letters and digits", "test_00000000000000000000000000-"},
		{checkout.Sandbox, "pk_test_000000000000000000000000000", "is no client-side token", "pk_test_000000000000000000000000000"},
		{"production", sandboxToken, `app.billing.checkout.environment: "production" is neither sandbox nor live`, sandboxToken},
		{"", sandboxToken, "app.billing.checkout.environment: none", sandboxToken},
		{checkout.Live, "", "app.billing.checkout.client_token: none", ""},
	} {
		err := check(c.env, c.token)
		x.ErrorContains(err, c.says, "%s %s", c.env, c.token)
		if c.dropped != "" {
			x.NotContains(err.Error(), c.dropped, "said what it was")
		}
	}
}
