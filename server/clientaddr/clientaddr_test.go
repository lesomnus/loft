package clientaddr_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/lesomnus/loft/server/clientaddr"
)

// TestTheProxiesWordIsTakenAsFarAsTheProxiesGo: X-Forwarded-For is read only
// from a proxy this deployment trusts, and only past the hops trusted proxies
// wrote -- what a caller writes to the left of them is an address it chose.
func TestTheProxiesWordIsTakenAsFarAsTheProxiesGo(t *testing.T) {
	trusted, err := clientaddr.Parse([]string{"10.0.0.0/8", "192.0.2.7", " "})
	require.NoError(t, err)

	for _, v := range []struct {
		name    string
		proxies clientaddr.Proxies
		peer    string
		xff     []string
		want    string
	}{
		{name: "no proxy is trusted, so the header is a caller's", peer: "203.0.113.5:4100", xff: []string{"198.51.100.1"}, want: "203.0.113.5"},
		{name: "a caller that is not a trusted proxy", proxies: trusted, peer: "203.0.113.5:4100", xff: []string{"198.51.100.1"}, want: "203.0.113.5"},
		{name: "a trusted proxy says whom it passes on", proxies: trusted, peer: "10.0.0.2:4100", xff: []string{"198.51.100.1"}, want: "198.51.100.1"},
		{name: "what the caller wrote left of it is not believed", proxies: trusted, peer: "10.0.0.2:4100", xff: []string{"6.6.6.6, 198.51.100.1"}, want: "198.51.100.1"},
		{name: "two trusted proxies, read past both", proxies: trusted, peer: "10.0.0.2:4100", xff: []string{"6.6.6.6, 198.51.100.1, 192.0.2.7"}, want: "198.51.100.1"},
		{name: "the header on two lines is one list", proxies: trusted, peer: "10.0.0.2:4100", xff: []string{"6.6.6.6", "198.51.100.1"}, want: "198.51.100.1"},
		{name: "a trusted proxy that said nothing", proxies: trusted, peer: "10.0.0.2:4100", want: "10.0.0.2"},
		{name: "proxies all the way down", proxies: trusted, peer: "10.0.0.2:4100", xff: []string{"10.1.1.1"}, want: "10.1.1.1"},
		{name: "not an address where a proxy should have written one", proxies: trusted, peer: "10.0.0.2:4100", xff: []string{"198.51.100.1, garbage"}, want: "10.0.0.2"},
		{name: "IPv6", proxies: trusted, peer: "[2001:db8::1]:4100", xff: []string{"198.51.100.1"}, want: "2001:db8::1"},
		{name: "an IPv4 proxy seen as IPv6", proxies: trusted, peer: "[::ffff:10.0.0.2]:4100", xff: []string{"198.51.100.1"}, want: "198.51.100.1"},
	} {
		t.Run(v.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodPost, "/session", nil)
			r.RemoteAddr = v.peer
			for _, h := range v.xff {
				r.Header.Add("X-Forwarded-For", h)
			}

			require.Equal(t, v.want, v.proxies.Resolve(r))

			var got string
			v.proxies.Handler(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
				got = clientaddr.Of(r)
			})).ServeHTTP(httptest.NewRecorder(), r)
			require.Equal(t, v.want, got, "what the handler put on the request")
		})
	}
}

// TestWithoutTheHandlerTheHeaderIsNotRead: a request nothing resolved is the
// connection's, whatever it says about itself.
func TestWithoutTheHandlerTheHeaderIsNotRead(t *testing.T) {
	r := httptest.NewRequest(http.MethodPost, "/cli/logins", nil)
	r.RemoteAddr = "203.0.113.5:4100"
	r.Header.Set("X-Forwarded-For", "198.51.100.1")

	require.Equal(t, "203.0.113.5", clientaddr.Of(r))
}

func TestAProxyThatIsNotAnAddressIsRefused(t *testing.T) {
	_, err := clientaddr.Parse([]string{"10.0.0.0/8", "proxy.internal"})
	require.ErrorContains(t, err, `"proxy.internal"`)
}
