// Package checkout is the page Paddle's default payment link is (design 7.7,
// D61): `/billing/checkout`.
//
// Paddle makes a link to a checkout of every transaction it is asked to
// collect -- the default payment link with `?_ptxn=` and the transaction's
// identifier -- and sends a subscriber the same link when they are to say
// again how they pay, from its own mails. The page is that link's: it loads
// Paddle.js, initializes it with this deployment's environment and
// client-side token, and Paddle.js opens the checkout of the transaction the
// address names by itself (Paddle's "Set your default payment link"). With no
// `_ptxn` there is nothing to pay, and the page says so.
//
// Nobody signs in to it. What it opens is Paddle's, the transaction's
// checkout, which a link Paddle made is enough to open; the page reads and
// writes nothing here.
//
// It is also where a checkout this deployment begins is opened (design 7.9):
// the account's page of what it pays with sends the payer here with Paddle's
// transaction and levyan's checkout in the address (`transaction`,
// `checkout`), and the page opens the transaction itself --
// `Paddle.Checkout.open({transactionId, settings: {successUrl}})` -- sending
// the payer back to that page once they finished (`/billing?checkout=`),
// which asks levyan whether the subscription was made. So Paddle's script
// runs on this page alone, whoever's checkout it is.
//
// What it is told is the deployment's, `app.billing.checkout` ([Config]): the
// environment and the client-side token, which are public by Paddle's design
// -- the token is what every browser opening a checkout is given. Unset, there
// is no page: the route answers 404.
//
// The page is the only one Paddle's script runs on, and so the only one whose
// Content Security Policy lets it ([Sources]): every other page is held to the
// policy that loads nothing from elsewhere (`server/csp`).
package checkout

import (
	"bytes"
	_ "embed"
	"errors"
	"fmt"
	"html/template"
	"net/http"
	"regexp"
	"strings"
)

// Path is where the page is: what the default payment link names in Paddle's
// dashboard (Paddle > Checkout > Checkout configuration), on this
// deployment's address.
const Path = "/billing/checkout"

// Script and Style are the page's own script and style, beside it.
const (
	Script = "/billing/checkout.js"
	Style  = "/billing/checkout.css"
)

// Paddle is Paddle.js, as Paddle says to include it -- from its CDN, and never
// a copy (Paddle's "Include and initialize Paddle.js",
// https://developer.paddle.com/paddle-js/about/include-paddlejs).
const Paddle = "https://cdn.paddle.com/paddle/v2/paddle.js"

// Environment is which of Paddle's environments the page opens checkouts of:
// `sandbox`, where test cards move no money, or `live`.
type Environment string

const (
	Sandbox Environment = "sandbox"
	Live    Environment = "live"
)

// Validate refuses an environment that is neither, where it is written.
func (e Environment) Validate() error {
	switch e {
	case "", Sandbox, Live:
		return nil
	}
	return fmt.Errorf("%q is neither sandbox nor live", string(e))
}

// prefix is what a client-side token of the environment begins with
// (Paddle's "Manage client-side tokens":
// https://developer.paddle.com/paddle-js/about/client-side-tokens).
func (e Environment) prefix() string {
	if e == Live {
		return "live_"
	}
	return "test_"
}

// Config is `app.billing.checkout`: which of Paddle's environments the page
// opens checkouts of, and the client-side token Paddle.js is initialized with
// there. Both are public, as Paddle made them: nothing here is a key.
type Config struct {
	// Environment is `sandbox` or `live`.
	Environment Environment `yaml:"environment"`

	// ClientToken is the client-side token of the environment (Paddle > My
	// account > Settings > Authentication, its client-side tokens):
	// `test_…` in the sandbox, `live_…` live. It goes to every browser that
	// opens the page, which Paddle made it for.
	ClientToken Token `yaml:"client_token"`
}

// On says whether c names a checkout: the page is served when it does.
func (c Config) On() bool { return c.Environment != "" || c.ClientToken.v != "" }

// Validate refuses a block that says one half of a checkout and not the
// other, and hands the environment to the token, which says itself whether it
// is the environment's ([Token.Validate]).
//
// It is asked before what the block holds is, as the configuration's loader
// asks a block and then its fields: so the token is held to the environment
// where the token is written, with the position of it in the file, rather
// than in the block, which a file has no line of.
func (c *Config) Validate() error {
	c.ClientToken.environment = c.Environment
	switch {
	case !c.On():
		return nil
	case c.Environment == "":
		return errors.New("environment: none, and a checkout is opened in one of Paddle's: sandbox or live")
	case c.ClientToken.v == "":
		return errors.New("client_token: none, and Paddle.js is initialized with the environment's client-side token")
	}
	return nil
}

// Opens says whether the page opens a checkout begun in `environment`, as
// levyan names Paddle's in what it answers a checkout with: `sandbox`, or
// `production` for live. Paddle.js initialized for one environment opens no
// transaction of the other's.
func (c Config) Opens(environment string) bool {
	switch environment {
	case "sandbox":
		return c.Environment == Sandbox
	case "production", "live":
		return c.Environment == Live
	}
	return false
}

// Check is everything [Config.Validate] and the fields' own say, of a block
// that was put together rather than loaded: what the page is built by.
func (c Config) Check() error {
	if err := c.Environment.Validate(); err != nil {
		return fmt.Errorf("app.billing.checkout.environment: %w", err)
	}
	if err := c.Validate(); err != nil {
		return fmt.Errorf("app.billing.checkout.%w", err)
	}
	if err := c.ClientToken.Validate(); err != nil {
		return fmt.Errorf("app.billing.checkout.client_token: %w", err)
	}
	return nil
}

// Token is a client-side token of Paddle's: a leaf of the configuration that
// reads itself, so that what is wrong with it is said where it is written --
// and never what it was, since a key put here by mistake would be in the log.
type Token struct {
	v string

	// environment is the block's, as [Config.Validate] hands it over.
	environment Environment
}

// NewToken is a token, as a test or a tool makes one.
func NewToken(v string) Token { return Token{v: v} }

func (t Token) String() string { return t.v }

func (t *Token) UnmarshalText(b []byte) error {
	t.v = strings.TrimSpace(string(b))
	return nil
}

func (t Token) MarshalText() ([]byte, error) { return []byte(t.v), nil }

// Validate refuses what is no client-side token, and one of the other
// environment than the block's: Paddle.js initialized with it opens no
// checkout of the environment the page sets, and the link the transaction
// came with is one of that environment's.
func (t *Token) Validate() error {
	switch v := t.v; {
	case v == "":
		return nil
	case strings.HasPrefix(v, "pdl_"):
		// It goes to a page: a key there is a key published.
		return errors.New("is one of Paddle's secrets -- an API key, or a notification's -- which never goes to a page; the page's is a client-side token, test_… or live_…")
	case !tokenShape.MatchString(v):
		return errors.New("is no client-side token of Paddle's, each of which is test_ in the sandbox or live_ live, and 27 letters and digits")
	case t.environment != "" && !strings.HasPrefix(v, t.environment.prefix()):
		return fmt.Errorf("is not the %s's: a client-side token there begins %s", t.environment, t.environment.prefix())
	}
	return nil
}

// tokenShape is a client-side token as Paddle writes one: "Always start with
// test_ or live_ ... Contains a random string of 27 characters in length after
// the environment prefix" (Paddle's "Manage client-side tokens").
var tokenShape = regexp.MustCompile(`^(test|live)_[a-zA-Z0-9]{27}$`)

// Sources is what the page loads from Paddle, by directive of its Content
// Security Policy, for the environment c says: what Paddle Checkout needs on
// the page that opens it, and nothing of the rest of Paddle.
//
// Paddle publishes no list of the hosts a policy is to let a checkout use, so
// this is what its documentation says and what Paddle.js itself does, read
// on 2026-10-31 (design 7.7, D61):
//
//   - script-src: Paddle.js, from https://cdn.paddle.com -- "Always load
//     Paddle.js directly from https://cdn.paddle.com/" (Paddle's "Include and
//     initialize Paddle.js"). It is loaded from there in the sandbox too.
//   - frame-src: the checkout, which Paddle.js opens in a frame of the
//     environment's checkout host -- https://sandbox-buy.paddle.com in the
//     sandbox, https://buy.paddle.com live (its `checkoutFrontEndBase`,
//     Paddle.js 2.10.2).
//   - style-src: the stylesheet Paddle.js adds as it is initialized, from the
//     environment's CDN -- https://sandbox-cdn.paddle.com in the sandbox,
//     https://cdn.paddle.com live (`paddle/v2/assets/css/paddle.css`) -- and
//     'unsafe-inline': it places its frame and the loader before it with
//     `style` attributes and a `<style>` it writes, and a policy that refused
//     them would leave the checkout a frame of 300 by 150 in the page's
//     corner. Nothing on this page is anybody's words.
//
// Left out: Paddle's API (connect-src), which Paddle.js asks only for a price
// preview, and Retain, which a live Paddle.js loads from
// https://public.profitwell.com and which is no part of a checkout. No
// wildcard: Paddle documents none.
func Sources(e Environment) map[string][]string {
	frame, style := "https://sandbox-buy.paddle.com", "https://sandbox-cdn.paddle.com"
	if e == Live {
		frame, style = "https://buy.paddle.com", "https://cdn.paddle.com"
	}

	return map[string][]string{
		"script-src": {"https://cdn.paddle.com"},
		"style-src":  {"'unsafe-inline'", style},
		"frame-src":  {frame},
	}
}

//go:embed page.html
var pageText string

//go:embed checkout.js
var script []byte

//go:embed checkout.css
var style []byte

var page = template.Must(template.New("checkout").Parse(pageText))

// Mount serves the page and what it loads of its own on `mux` -- or, with no
// checkout configured, answers 404 there, so that nothing else answers for
// them: the built UI answers every path it does not have with its page.
func (c Config) Mount(mux *http.ServeMux) {
	if !c.On() {
		for _, p := range []string{Path, Script, Style} {
			mux.HandleFunc("GET "+p, http.NotFound)
		}
		return
	}

	mux.HandleFunc("GET "+Path, func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		var b bytes.Buffer
		if err := page.Execute(&b, struct {
			Paddle      string
			Script      string
			Style       string
			Environment Environment
			Token       string
			Transaction bool
			Ours        bool
		}{Paddle, Script, Style, c.Environment, c.ClientToken.v, q.Get("_ptxn") != "" || q.Get("transaction") != "", q.Get("transaction") != ""}); err != nil {
			http.Error(w, "the page could not be drawn", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-cache")
		w.Write(b.Bytes())
	})
	mux.HandleFunc("GET "+Script, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
		w.Header().Set("Cache-Control", "no-cache")
		w.Write(script)
	})
	mux.HandleFunc("GET "+Style, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/css; charset=utf-8")
		w.Header().Set("Cache-Control", "no-cache")
		w.Write(style)
	})
}
