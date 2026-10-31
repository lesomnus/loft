package cmd

import (
	"context"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/lesomnus/otx/log"
	"github.com/lesomnus/payday/pdid"

	"github.com/lesomnus/loft/internal/ent"
	"github.com/lesomnus/loft/internal/ent/tenant"
	"github.com/lesomnus/loft/internal/ent/tenantdomain"
	"github.com/lesomnus/loft/server/apphost"
	"github.com/lesomnus/loft/server/tenancy"
)

// Hosts is what the host a request came to decides (loft#155), in front of
// everything this deployment serves over HTTP -- the RPCs a page calls, which
// it hands the decision to on the request's context ([apphost.From]), and the
// HTTP beside them.
//
//   - **A workspace's active `app` domain** pins the request to that workspace.
//     The app is served there, and every call is served as a membership of
//     that workspace and no other: a `loft-workspace` that names another is
//     refused, never re-pointed (`cmd.Resolver`), and so is a page at
//     `/@<alias>` of another. Signing in and out is not done there but by
//     the handoff (`/handoff/...`, `server/session`): the service signs a
//     browser in and hands the domain a session of its own, which is the
//     page's way in (`GET /sso` says so). Making a workspace is the
//     service's too, and a signed file link of another workspace's is not
//     served there either.
//   - **The deployment's own host** -- `app.public_url`'s, and `app.app_url`'s
//     when the page is served from another -- is the service, as it always
//     was: every workspace, by the path a page is at and the header a call
//     names; but a page of a workspace whose app is on its active `app`
//     domain is sent to the same page there ([Server.Routes]), which nothing
//     else is. A deployment told no address is a checkout, and every host
//     that is no workspace's domain is the service's there.
//   - **A workspace's domain that is not an active `app` one** -- a label
//     domain, a default subdomain, an app domain that is legacy since another
//     took its place, one that lapsed when its DNS stopped saying it is here,
//     or one verified and not yet in use -- opens the labels printed with it,
//     `/l/<code>`, and nothing else: an inactive or legacy domain pins
//     nothing. An app domain another took the place of sends its pages on to
//     where the workspace's app is now, for a while
//     (`app.workspaces.domain_redirect`).
//   - **Any other host** is answered nothing but `/healthz`, which a probe asks
//     by whatever address it reaches the pod at.
type Hosts struct {
	Db *ent.Client

	// Service is the deployment's own hosts; none is a checkout, where every
	// host that is no workspace's domain is the service's.
	Service []string

	// Aliases says where an address a workspace moved off went, for a page
	// at an alias a pinned workspace moved off, which sends it on (D29).
	Aliases tenancy.Aliases

	// Links is where a legacy app domain sends its pages: where the
	// workspace's app is now.
	Links apphost.Links

	// Now is the clock; tests move it.
	Now func() time.Time
}

// Hosts is what decides by the host a request came to, for this deployment.
func (s *Server) Hosts(c Config) Hosts {
	h := Hosts{Db: s.Ent, Aliases: s.Aliases(c), Links: c.App.Links()}
	if c.App.PublicUrl != "" {
		h.Service = c.App.ServiceHosts()
	}
	return h
}

// place is what a host is to this deployment.
type place int

const (
	// atService is the deployment's own host.
	atService place = iota
	// atWorkspace is a workspace's active app domain.
	atWorkspace
	// atDomain is a workspace's domain that pins nothing: labels only.
	atDomain
	// nowhere is no host of this deployment's.
	nowhere
)

// where answers what `host` is: the workspace it pins a request to when it is
// an active app domain, and the domain it is when it is any workspace's.
func (h Hosts) where(ctx context.Context, host string) (place, apphost.Pinned, *ent.TenantDomain, error) {
	d, err := h.Db.TenantDomain.Query().Where(
		tenantdomain.Host(host),
		tenantdomain.StateIn("ready", "active", "legacy", "lapsed"),
		tenantdomain.DateErasedIsNil(),
	).WithTenant().Only(ctx)
	switch {
	case ent.IsNotFound(err):
		if len(h.Service) == 0 || slices.Contains(h.Service, host) {
			return atService, apphost.Pinned{}, nil, nil
		}
		return nowhere, apphost.Pinned{}, nil, nil
	case err != nil:
		return nowhere, apphost.Pinned{}, nil, err
	}
	if d.Purpose != "app" || d.State != "active" || d.Edges.Tenant == nil {
		return atDomain, apphost.Pinned{}, d, nil
	}

	return atWorkspace, apphost.Pinned{Workspace: pdid.Id(d.TenantId), Alias: d.Edges.Tenant.Alias}, d, nil
}

// movedOn is where a page asked for on `d` is now, when `d` is an app domain
// another took the place of and still sends its pages on; empty otherwise.
func (h Hosts) movedOn(r *http.Request, d *ent.TenantDomain) (string, error) {
	if d == nil || d.Purpose != "app" || d.State != "legacy" || d.RedirectUntil == nil {
		return "", nil
	}
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		return "", nil
	}
	now := time.Now()
	if h.Now != nil {
		now = h.Now()
	}
	if !now.Before(*d.RedirectUntil) {
		return "", nil
	}
	at, err := h.Links.Of(r.Context(), h.Db, pdid.Id(d.TenantId), apphost.Page)
	if err != nil {
		return "", err
	}
	return at + r.URL.RequestURI(), nil
}

// Handler is `next` behind the decision the host makes; see [Hosts].
func (h Hosts) Handler(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/healthz" {
			next.ServeHTTP(w, r)
			return
		}

		at, pin, d, err := h.where(r.Context(), apphost.Host(r))
		if err != nil {
			log.From(r.Context()).ErrorContext(r.Context(), "hosts: which workspace a host is could not be told", "host", r.Host, "err", err)
			http.Error(w, "cannot say just now", http.StatusServiceUnavailable)
			return
		}
		switch at {
		case atService:
			next.ServeHTTP(w, r)
		case atDomain:
			if r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/l/") {
				next.ServeHTTP(w, r)
				return
			}
			to, err := h.movedOn(r, d)
			if err != nil {
				log.From(r.Context()).ErrorContext(r.Context(), "hosts: where a moved domain's workspace is could not be told", "host", r.Host, "err", err)
				http.Error(w, "cannot say just now", http.StatusServiceUnavailable)
				return
			}
			if to != "" {
				http.Redirect(w, r, to, http.StatusFound)
				return
			}
			http.Error(w, "this address opens the labels printed with it, and nothing else", http.StatusNotFound)
		case atWorkspace:
			if why := refuses(pin, r, h.Aliases); why != "" {
				http.Error(w, why, http.StatusNotFound)
				return
			}
			next.ServeHTTP(w, r.WithContext(apphost.Into(r.Context(), pin)))
		default:
			http.Error(w, "nothing is served at this address", http.StatusNotFound)
		}
	})
}

// scanning is where the page that resolves a label scanned on `r`'s host
// begins: nothing on a workspace's own domain, whose page it is; the app
// domain of the workspace whose domain it is, or the service's address while
// it has none, on one of a workspace's domains that pins nothing; the
// service's anywhere else.
func (s *Server) scanning(r *http.Request, links apphost.Links) (string, error) {
	if _, ok := apphost.From(r.Context()); ok {
		return "", nil
	}
	d, err := s.Ent.TenantDomain.Query().Where(
		tenantdomain.Host(apphost.Host(r)),
		tenantdomain.StateIn("ready", "active", "legacy", "lapsed"),
		tenantdomain.DateErasedIsNil(),
	).Only(r.Context())
	switch {
	case ent.IsNotFound(err):
		return links.Home(apphost.Page), nil
	case err != nil:
		return "", err
	}
	return links.Of(r.Context(), s.Ent, pdid.Id(d.TenantId), apphost.Page)
}

// appOf is where workspace `ws`'s app is when that is a domain of its own --
// its active `app` domain -- and empty while it is the service's.
func (s *Server) appOf(ctx context.Context, links apphost.Links, ws pdid.Id) (string, error) {
	at, err := links.Of(ctx, s.Ent, ws, apphost.Page)
	if err != nil || at == links.Home(apphost.Page) {
		return "", err
	}
	return at, nil
}

// domainOf is [Server.appOf] for the workspace at `alias`, and empty when
// there is none.
func (s *Server) domainOf(ctx context.Context, links apphost.Links, alias string) (string, error) {
	id, err := s.Ent.Tenant.Query().Where(tenant.Alias(alias)).OnlyId(ctx)
	switch {
	case ent.IsNotFound(err):
		return "", nil
	case err != nil:
		return "", err
	}
	return s.appOf(ctx, links, pdid.Id(id))
}

// refuses says why a request on a workspace's app domain is not served
// there, or nothing when it is. A page or a file of another workspace is not
// found here, rather than sent where it is: the domain serves its workspace
// alone, and telling which workspaces there are is the service's.
func refuses(p apphost.Pinned, r *http.Request, aliases tenancy.Aliases) string {
	path := r.URL.Path
	switch {
	case path == "/session",
		strings.HasPrefix(path, "/sso") && (path != "/sso" || r.Method != http.MethodGet),
		strings.HasPrefix(path, "/cli/"),
		path == "/handoff",
		path == "/account/workspaces" && r.Method == http.MethodPost:
		return "this is @" + p.Alias + "'s address: sign in, and make a workspace, at the service's"
	}
	if rest, ok := strings.CutPrefix(path, "/files/"); ok {
		// A file's key begins with the workspace it is of
		// (`<tenant>/<month>/<id>`).
		if t, _, _ := strings.Cut(rest, "/"); !p.Names(t) {
			return "no such file at this address"
		}
	}
	if rest, ok := strings.CutPrefix(path, "/@"); ok {
		alias, _, _ := strings.Cut(rest, "/")
		if !p.Names(alias) {
			// An alias this workspace moved off is its own still, and the
			// page sends it on to where it is (D29).
			if to, err := aliases.Moved(r.Context(), alias); err != nil || to != p.Alias {
				return "this is @" + p.Alias + "'s address, and no other workspace is served here"
			}
		}
	}
	return ""
}
