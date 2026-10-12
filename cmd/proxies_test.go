package cmd_test

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/lesomnus/rove/cmd"
	"github.com/lesomnus/rove/internal/ent/signinfailure"
	"github.com/lesomnus/rove/server/clientaddr"
	"github.com/lesomnus/rove/server/clilogin"
	"github.com/lesomnus/rove/server/session"
)

// TestAnAddressACallerWritesDoesNotMoveTheLimits: X-Forwarded-For is a header
// anybody writes, so from a caller that is not a trusted proxy it moves
// neither limit. A terminal that wrote a new one each time used to start as
// many sign-ins as it liked. Behind a proxy this deployment trusts, the people
// it passes on are counted apart, rather than as the one address of the proxy
// that ten guesses by anybody lock out for all of them.
func TestAnAddressACallerWritesDoesNotMoveTheLimits(t *testing.T) {
	w := newWorld(t)
	x := w.x
	ctx := context.Background()

	trusted, err := clientaddr.Parse([]string{"10.9.0.0/16"})
	x.NoError(err)

	post := func(h http.Handler, peer, xff string) int {
		b, _ := json.Marshal(session.Login{Login: "ghost", Password: "a guess"})
		r := httptest.NewRequest(http.MethodPost, "/session", bytes.NewReader(b))
		r.RemoteAddr = peer + ":1234"
		if xff != "" {
			r.Header.Set("X-Forwarded-For", xff)
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, r)
		return rec.Code
	}
	failures := func(addr string) int {
		n, err := w.s.Ent.SignInFailure.Query().
			Where(signinfailure.Subject(session.Subject(addr, "ghost"))).
			Count(ctx)
		x.NoError(err)
		return n
	}

	t.Run("a caller's header is not its address", func(t *testing.T) {
		x := require.New(t)

		ra := &asked{People: w.s.Identity}
		h := clientaddr.Proxies{}.Handler(session.SignIn(w.s.Sessions, ra, w.s.Ent, nil))
		for i := range session.MaxFailures {
			x.Equal(http.StatusUnauthorized, post(h, "203.0.113.5", fmt.Sprintf("198.51.100.%d", i)))
		}
		x.Equal(session.MaxFailures, failures("203.0.113.5"), "the failures went under the addresses written")
		x.Equal(http.StatusUnauthorized, post(h, "203.0.113.5", "198.51.100.200"))
		x.Equal(session.MaxFailures, ra.count(), "a new address written got roster asked past the limit")
	})

	t.Run("behind a trusted proxy, each person behind it is counted apart", func(t *testing.T) {
		x := require.New(t)

		ra := &asked{People: w.s.Identity}
		h := trusted.Handler(session.SignIn(w.s.Sessions, ra, w.s.Ent, nil))
		for range session.MaxFailures {
			x.Equal(http.StatusUnauthorized, post(h, "10.9.0.1", "198.51.100.1"))
		}
		x.Equal(session.MaxFailures, failures("198.51.100.1"))
		x.Zero(failures("10.9.0.1"), "counted under the proxy")

		x.Equal(http.StatusUnauthorized, post(h, "10.9.0.1", "198.51.100.2"))
		x.Equal(session.MaxFailures+1, ra.count(), "a second person behind the proxy was refused for the first one's guesses")
	})

	t.Run("and a terminal's sign-ins are counted the same way", func(t *testing.T) {
		x := require.New(t)

		mux := http.NewServeMux()
		w.s.Logins.Mount(mux)
		h := clientaddr.Proxies{}.Handler(mux)
		start := func(xff string) int {
			key := make([]byte, 32)
			_, _ = rand.Read(key)
			body, _ := json.Marshal(map[string]string{"secret": base64.RawURLEncoding.EncodeToString(key), "client": "a test"})
			r := httptest.NewRequest(http.MethodPost, "/cli/logins", bytes.NewReader(body))
			r.Header.Set("Content-Type", "application/json")
			r.Header.Set("X-Forwarded-For", xff)
			r.RemoteAddr = "203.0.113.6:1234"
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, r)
			return rec.Code
		}

		for i := range clilogin.PerAddress {
			x.Equal(http.StatusCreated, start(fmt.Sprintf("198.51.100.%d", i)))
		}
		x.Equal(http.StatusTooManyRequests, start("198.51.100.250"), "a new address written started another sign-in")
	})
}

// TestEveryRequestIsToldWhereItCameFrom: what `serve` puts in front of the
// routes resolves the address with the proxies the configuration names, and
// refuses a proxy that is not an address.
func TestEveryRequestIsToldWhereItCameFrom(t *testing.T) {
	x := require.New(t)

	c := cmd.Config{}
	c.App.TrustedProxies = []string{"10.9.0.0/16"}

	var got string
	h, err := cmd.Front(c, http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		got = clientaddr.Of(r)
	}))
	x.NoError(err)

	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.RemoteAddr = "10.9.0.1:1234"
	r.Header.Set("X-Forwarded-For", "6.6.6.6, 198.51.100.1")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, r)
	x.Equal("198.51.100.1", got)
	x.Equal("nosniff", rec.Header().Get("X-Content-Type-Options"), "the headers every answer carries")

	c.App.TrustedProxies = []string{"proxy.internal"}
	_, err = cmd.Front(c, http.NotFoundHandler())
	x.ErrorContains(err, "app.trusted_proxies")
}
