// Package csp is the Content Security Policy every page here is shown with,
// and where a browser reports what a page did that the policy does not allow
// (loft#30; loft#35 decided to begin with report-only).
//
// The policy is what the built page loads and nothing else: its own scripts,
// styles, fonts and service worker; images of its own, the QR codes it draws
// as `data:` and what it makes as `blob:`; the API at its own address; and
// forms that go nowhere but here, the service's own address, and the issuer
// people sign out through -- a sign-out is a form whose answer sends the
// browser on, from a workspace's own domain to the service's (loft#155) and
// from either to the issuer's page (`server/sso`), and a browser holds where
// a form's redirect goes to the policy too. A script somebody gets into a page
// is then one no browser runs, once the policy is enforced.
//
// One page loads more: the one Paddle's checkout opens on
// (`server/checkout`), whose policy is this one with Paddle's hosts in it
// ([With]). It is answered with that one alone, and only while a checkout is
// configured.
//
// Report-only first: a page held to a policy that stops something it needs is
// a page that stops working, and what it needs is found by watching what the
// browsers say -- the e2e suite fails on anything a page reports
// (`ts/e2e/loft.ts`), and a deployment logs what its browsers send.
package csp

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"maps"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/lesomnus/otx/log"
)

// Mode is how a page is held to the policy: reported, enforced, or neither.
type Mode string

const (
	ReportOnly Mode = "report-only"
	Enforce    Mode = "enforce"
	Off        Mode = "off"
)

// Parse reads `app.csp`; empty is report-only.
func Parse(v string) (Mode, error) {
	switch m := Mode(v); m {
	case "":
		return ReportOnly, nil
	case ReportOnly, Enforce, Off:
		return m, nil
	default:
		return "", fmt.Errorf("%q is not one of report-only, enforce or off", v)
	}
}

// Path is where the policy has browsers report to.
const Path = "/csp-report"

// Policy is the policy, for a deployment whose people sign in through
// `issuer` (empty is none) and whose own address is `service` -- where its
// page is, and where it answers. It frames nothing in -- `frame-ancestors`,
// which is enforced on its own whatever the mode, since that has been so
// since loft#30.
func Policy(issuer string, service ...string) string { return With(nil, issuer, service...) }

// With is [Policy] for a page that loads more than the others do -- the one
// Paddle's checkout opens on (`server/checkout`) -- with `more` of each
// directive it names: after what every page has of it, or, for a directive no
// page has, beside the rest before `object-src`. Only that page is answered
// with it: every other is held to [Policy] whatever is configured.
func With(more map[string][]string, issuer string, service ...string) string {
	forms := "'self'"
	seen := map[string]bool{}
	for _, v := range append(service, issuer) {
		u, err := url.Parse(v)
		if err != nil || u.Scheme == "" || u.Host == "" {
			continue
		}
		if o := u.Scheme + "://" + u.Host; !seen[o] {
			seen[o] = true
			forms += " " + o
		}
	}

	directives := [][2]string{
		{"default-src", "'self'"},
		{"script-src", "'self'"},
		{"style-src", "'self'"},
		{"img-src", "'self' data: blob:"},
		{"font-src", "'self'"},
		{"connect-src", "'self'"},
		{"media-src", "'self' blob:"},
		{"worker-src", "'self'"},
		{"manifest-src", "'self'"},
		{"object-src", "'none'"},
		{"base-uri", "'self'"},
		{"form-action", forms},
		{"frame-ancestors", "'none'"},
		{"report-uri", Path},
		{"report-to", "csp"},
	}
	out := make([]string, 0, len(directives)+len(more))
	for _, d := range directives {
		if d[0] == "object-src" {
			// What no page has, in an order a reader can follow.
			for _, name := range slices.Sorted(maps.Keys(more)) {
				if !slices.ContainsFunc(directives, func(v [2]string) bool { return v[0] == name }) {
					out = append(out, name+" "+strings.Join(more[name], " "))
				}
			}
		}
		v := d[1]
		if m := more[d[0]]; len(m) > 0 {
			v += " " + strings.Join(m, " ")
		}
		out = append(out, d[0]+" "+v)
	}

	return strings.Join(out, "; ")
}

// Headers says the policy on an answer, as `mode` holds a page to it.
func Headers(h http.Header, mode Mode, policy string) {
	switch mode {
	case ReportOnly:
		h.Set("Content-Security-Policy-Report-Only", policy)
	case Enforce:
		h.Set("Content-Security-Policy", policy)
	default:
		return
	}
	h.Set("Reporting-Endpoints", `csp="`+Path+`"`)
}

// Reports is the endpoint browsers post what a page broke to. Each report is a
// line in the log, without the query of any address in it -- an attachment's
// address carries its signature, a page's a code -- and at most PerMinute
// lines a minute in all: a page broken for everybody is a few lines and a
// count, and not one line per visit.
type Reports struct {
	// PerMinute is how many lines a minute it writes; zero is 30.
	PerMinute int
	// Now is the clock; nil is time.Now.
	Now func() time.Time

	mu      sync.Mutex
	minute  time.Time
	n       int
	dropped int
}

// Limit is the most of a report that is read.
const Limit = 64 << 10

func (s *Reports) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	b, err := io.ReadAll(http.MaxBytesReader(w, r.Body, Limit))
	if err != nil {
		w.WriteHeader(http.StatusRequestEntityTooLarge)
		return
	}
	w.WriteHeader(http.StatusNoContent)

	ctx := r.Context()
	for _, v := range parse(b) {
		dropped, ok := s.take()
		if !ok {
			continue
		}
		if dropped > 0 {
			log.From(ctx).WarnContext(ctx, "csp: reports left out of the log, a minute's worth past the limit", slog.Int("reports", dropped))
		}
		log.From(ctx).WarnContext(ctx, "csp: a page did what the policy does not allow",
			slog.String("disposition", v.Disposition),
			slog.String("directive", v.Directive),
			slog.String("blocked", v.Blocked),
			slog.String("page", v.Page),
			slog.String("source", v.Source),
			slog.Int("line", v.Line),
		)
	}
}

// take answers whether this report has a line, and how many the minute before
// went without one.
func (s *Reports) take() (int, bool) {
	now := time.Now()
	if s.Now != nil {
		now = s.Now()
	}
	limit := s.PerMinute
	if limit <= 0 {
		limit = 30
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	minute := now.Truncate(time.Minute)
	dropped := 0
	if !minute.Equal(s.minute) {
		dropped, s.dropped = s.dropped, 0
		s.minute, s.n = minute, 0
	}
	if s.n >= limit {
		s.dropped++
		return 0, false
	}
	s.n++
	return dropped, true
}

// Report is what a browser said, as much of it as is worth a line.
type Report struct {
	Disposition string
	Directive   string
	Blocked     string
	Page        string
	Source      string
	Line        int
}

// parse reads both shapes a browser sends: `report-uri`'s one object under
// "csp-report", and the Reporting API's list of them.
func parse(b []byte) []Report {
	b = bytes.TrimSpace(b)
	if len(b) == 0 {
		return nil
	}

	if b[0] == '[' {
		var vs []struct {
			Type string `json:"type"`
			Body struct {
				DocumentURL        string `json:"documentURL"`
				EffectiveDirective string `json:"effectiveDirective"`
				BlockedURL         string `json:"blockedURL"`
				SourceFile         string `json:"sourceFile"`
				LineNumber         int    `json:"lineNumber"`
				Disposition        string `json:"disposition"`
			} `json:"body"`
		}
		if json.Unmarshal(b, &vs) != nil {
			return nil
		}
		out := []Report{}
		for _, v := range vs {
			if v.Type != "csp-violation" {
				continue
			}
			out = append(out, clean(Report{
				Disposition: v.Body.Disposition,
				Directive:   v.Body.EffectiveDirective,
				Blocked:     v.Body.BlockedURL,
				Page:        v.Body.DocumentURL,
				Source:      v.Body.SourceFile,
				Line:        v.Body.LineNumber,
			}))
		}
		return out
	}

	var v struct {
		Report struct {
			DocumentURI        string `json:"document-uri"`
			ViolatedDirective  string `json:"violated-directive"`
			EffectiveDirective string `json:"effective-directive"`
			BlockedURI         string `json:"blocked-uri"`
			SourceFile         string `json:"source-file"`
			LineNumber         int    `json:"line-number"`
			Disposition        string `json:"disposition"`
		} `json:"csp-report"`
	}
	if json.Unmarshal(b, &v) != nil {
		return nil
	}
	d := v.Report.EffectiveDirective
	if d == "" {
		d = v.Report.ViolatedDirective
	}
	return []Report{clean(Report{
		Disposition: v.Report.Disposition,
		Directive:   d,
		Blocked:     v.Report.BlockedURI,
		Page:        v.Report.DocumentURI,
		Source:      v.Report.SourceFile,
		Line:        v.Report.LineNumber,
	})}
}

// clean takes the query and fragment off every address, and cuts what is
// left to a length worth a line.
func clean(v Report) Report {
	v.Directive = cut(v.Directive)
	v.Blocked = cut(bare(v.Blocked))
	v.Page = cut(bare(v.Page))
	v.Source = cut(bare(v.Source))
	v.Disposition = cut(v.Disposition)
	return v
}

func bare(v string) string {
	if i := strings.IndexAny(v, "?#"); i >= 0 {
		v = v[:i]
	}
	// A `data:` address is its content; what kind it was is enough.
	if strings.HasPrefix(v, "data:") {
		if i := strings.IndexAny(v, ";,"); i >= 0 {
			return v[:i]
		}
	}
	return v
}

func cut(v string) string {
	if len(v) <= 256 {
		return v
	}
	v = v[:256]
	for !utf8.ValidString(v) {
		v = v[:len(v)-1]
	}
	return v
}
