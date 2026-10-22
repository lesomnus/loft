package csp_test

import (
	"bytes"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/lesomnus/otx/log"
	"github.com/stretchr/testify/require"

	"github.com/lesomnus/rove/server/csp"
)

// TestThePolicyIsWhatThePageLoads: its own scripts, styles and worker,
// images of its own and the QR codes it draws, the API here, and forms that go
// here and to the issuer a sign-out lands on.
func TestThePolicyIsWhatThePageLoads(t *testing.T) {
	x := require.New(t)

	directives := func(p string) map[string]string {
		out := map[string]string{}
		for _, d := range strings.Split(p, "; ") {
			k, v, _ := strings.Cut(d, " ")
			out[k] = v
		}
		return out
	}

	d := directives(csp.Policy("https://id.example.com/oauth2/"))
	x.Equal("'self'", d["script-src"], "a script from anywhere but here runs")
	x.Equal("'self'", d["style-src"])
	x.Equal("'self' data: blob:", d["img-src"], "the QR codes are data: images")
	x.Equal("'self'", d["connect-src"])
	x.Equal("'self'", d["worker-src"], "the service worker")
	x.Equal("'none'", d["object-src"])
	x.Equal("'self'", d["base-uri"])
	x.Equal("'self' https://id.example.com", d["form-action"], "a sign-out through the issuer is a form that lands there")
	x.Equal("'none'", d["frame-ancestors"])
	x.Equal("/csp-report", d["report-uri"])
	x.Equal("csp", d["report-to"])

	x.Equal("'self'", directives(csp.Policy(""))["form-action"], "no issuer, no form goes anywhere else")
	x.Equal("'self' https://rove.example https://app.rove.example https://id.example.com",
		directives(csp.Policy("https://id.example.com/oauth2/", "https://rove.example", "https://app.rove.example/", "https://rove.example/"))["form-action"],
		"a sign-out on a workspace's own domain is a form that lands at the service's address, once each")

	for v, want := range map[string]csp.Mode{"": csp.ReportOnly, "report-only": csp.ReportOnly, "enforce": csp.Enforce, "off": csp.Off} {
		got, err := csp.Parse(v)
		x.NoError(err)
		x.Equal(want, got, v)
	}
	_, err := csp.Parse("strict")
	x.ErrorContains(err, "report-only, enforce or off")
}

// TestWhatABrowserReportsIsALineInTheLog: either shape a browser sends, with
// no query of any address in it -- an attachment's carries its signature -- and
// at most so many lines a minute, the rest counted.
func TestWhatABrowserReportsIsALineInTheLog(t *testing.T) {
	x := require.New(t)

	var said bytes.Buffer
	now := time.Date(2026, 10, 13, 9, 0, 0, 0, time.UTC)
	s := &csp.Reports{PerMinute: 2, Now: func() time.Time { return now }}
	post := func(kind, body string) int {
		r := httptest.NewRequest(http.MethodPost, csp.Path, strings.NewReader(body))
		r.Header.Set("Content-Type", kind)
		r = r.WithContext(log.Into(r.Context(), slog.New(slog.NewTextHandler(&said, nil))))
		rec := httptest.NewRecorder()
		s.ServeHTTP(rec, r)
		return rec.Code
	}

	x.Equal(http.StatusNoContent, post("application/csp-report", `{"csp-report": {
		"document-uri": "https://rove.example.com/@acme/assets?q=secret#x",
		"violated-directive": "img-src",
		"effective-directive": "img-src",
		"blocked-uri": "https://rove.example.com/files/a.png?sig=abc&exp=1",
		"source-file": "https://rove.example.com/static/index.js",
		"line-number": 12,
		"disposition": "report"
	}}`))
	line := said.String()
	x.Contains(line, "directive=img-src")
	x.Contains(line, "blocked=https://rove.example.com/files/a.png ")
	x.Contains(line, "page=https://rove.example.com/@acme/assets ")
	x.Contains(line, "line=12")
	x.NotContains(line, "sig=", "an attachment's signature in the log")
	x.NotContains(line, "secret")

	said.Reset()
	x.Equal(http.StatusNoContent, post("application/reports+json", `[
		{"type": "csp-violation", "url": "https://rove.example.com/", "body": {
			"documentURL": "https://rove.example.com/@acme/labels",
			"effectiveDirective": "img-src",
			"blockedURL": "data:image/png;base64,iVBORw0KGgo=",
			"disposition": "report"
		}},
		{"type": "deprecation", "body": {}}
	]`))
	line = said.String()
	x.Contains(line, "blocked=data:image/png ", "a data: address is its kind, not its content")
	x.NotContains(line, "iVBOR")
	x.Equal(1, strings.Count(line, "csp: a page did"), "a report of another kind is not a violation")

	t.Run("and a minute has so many lines, and then a count", func(t *testing.T) {
		x := require.New(t)

		said.Reset()
		report := `{"csp-report": {"violated-directive": "script-src", "blocked-uri": "inline"}}`
		for range 5 {
			x.Equal(http.StatusNoContent, post("application/csp-report", report))
		}
		x.Equal(0, strings.Count(said.String(), "csp: a page did"), "the minute's two were the two above")

		now = now.Add(time.Minute)
		x.Equal(http.StatusNoContent, post("application/csp-report", report))
		x.Contains(said.String(), "reports=5", "what the minute before left out is not said")
		x.Equal(1, strings.Count(said.String(), "csp: a page did"))
	})

	t.Run("and what is not a report is nothing", func(t *testing.T) {
		x := require.New(t)

		said.Reset()
		x.Equal(http.StatusNoContent, post("application/json", `not json`))
		x.Equal(http.StatusRequestEntityTooLarge, post("application/csp-report", strings.Repeat("x", csp.Limit+1)))
		x.Empty(said.String())
	})
}
