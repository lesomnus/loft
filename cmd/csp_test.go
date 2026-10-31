package cmd_test

import (
	"bytes"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/lesomnus/otx/log"
	"github.com/stretchr/testify/require"

	"github.com/lesomnus/loft/cmd"
)

// TestABrowsersReportReachesTheLog: what `serve` puts in front of the routes
// takes a browser's report at /csp-report -- signed in or not, it is the
// browser's and not a person's -- answers 204, and logs it; and the answer
// carries the policy, as every answer does.
func TestABrowsersReportReachesTheLog(t *testing.T) {
	w := newWorld(t)
	x := w.x

	mux := http.NewServeMux()
	w.s.Routes(mux, w.c)
	h, err := cmd.Front(w.c, mux)
	x.NoError(err)

	var said bytes.Buffer
	r := httptest.NewRequest(http.MethodPost, "/csp-report", strings.NewReader(`{"csp-report": {"violated-directive": "img-src", "blocked-uri": "https://elsewhere.example/x.png", "document-uri": "https://loft.example/@acme/"}}`))
	r.Header.Set("Content-Type", "application/csp-report")
	r = r.WithContext(log.Into(r.Context(), slog.New(slog.NewTextHandler(&said, nil))))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, r)
	x.Equal(http.StatusNoContent, rec.Code)
	x.Contains(said.String(), "directive=img-src")
	x.Contains(said.String(), "blocked=https://elsewhere.example/x.png")
	x.Contains(rec.Header().Get("Content-Security-Policy-Report-Only"), "script-src 'self'")

	t.Run("and app.csp is one of what it can be", func(t *testing.T) {
		x := require.New(t)

		c := w.c
		c.App.Csp = "strict"
		_, err := cmd.Front(c, mux)
		x.ErrorContains(err, "app.csp")
	})
}
