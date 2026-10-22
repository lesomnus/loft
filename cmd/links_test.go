package cmd_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/lesomnus/payday/pdid"

	app "github.com/lesomnus/rove"
	"github.com/lesomnus/rove/internal/ent/tenantdomain"
	"github.com/lesomnus/rove/server/account"
)

// TestALabelOpensWhereItsWorkspaceIs: a printed label sends whoever scans it
// to the page that resolves it, where the workspace of the host it was printed
// with is (rove#155): on the workspace's own domain, that domain; on one of
// its domains that pins nothing -- an address it moved off, a label domain --
// its app domain while it has one, and the service's address otherwise; at
// the service's, the service's. Which workspace the code is of never decides
// it, so that a label tells nobody more than what was printed on it.
func TestALabelOpensWhereItsWorkspaceIs(t *testing.T) {
	w := newHosted(t)
	x := w.x
	ctx := context.Background()

	beta, err := w.s.Base.Tenant().Add(ctx, app.TenantAddRequest_builder{Alias: "beta"}.Build())
	x.NoError(err)
	w.domain(w.tenant, ownHost, "app", "active")
	w.domain(w.tenant, "old.acme.example", "app", "legacy")
	w.domain(beta, "beta.l.test", "label", "active")

	opens := func(host, want string) {
		t.Helper()
		res := w.send(nil, http.MethodGet, host, "/l/7K3MQ9TX")
		x.Equal(http.StatusFound, res.Code, host)
		x.Equal(want, res.Header().Get("Location"), host)
	}
	opens(ownHost, "/scan/7K3MQ9TX")
	opens("old.acme.example", "https://"+ownHost+"/scan/7K3MQ9TX")
	opens("beta.l.test", "https://"+serviceHost+"/scan/7K3MQ9TX")
	opens(serviceHost, "https://"+serviceHost+"/scan/7K3MQ9TX")

	// Its app domain no longer active: the service's.
	_, err = w.s.Ent.TenantDomain.Update().SetState("legacy").Save(ctx)
	x.NoError(err)
	opens("old.acme.example", "https://"+serviceHost+"/scan/7K3MQ9TX")
}

// TestAWorkspacesDataIsTakenOutOnItsDomain: the link to a workspace's export
// is the page's, on whichever host the page is -- its own domain, with the
// session handed there, or the service's address.
func TestAWorkspacesDataIsTakenOutOnItsDomain(t *testing.T) {
	w := newHosted(t)
	x := w.x

	w.domain(w.tenant, ownHost, "app", "active")
	export := "/account/workspaces/acme/export?reason=keeping"
	handed := w.handOff(w.owner, ownHost)
	res := w.send(nil, http.MethodGet, ownHost, export, handed)
	x.Equal(http.StatusOK, res.Code, res.Body.String())
	x.Equal("application/zip", res.Header().Get("Content-Type"))
	res = w.send(nil, http.MethodGet, serviceHost, export, cookieIn(w.owner))
	x.Equal(http.StatusOK, res.Code, res.Body.String())
}

// TestALegacyDomainSendsItsPagesOn: an app domain another took the place of
// sends a page asked for on it to where the workspace's app is now, the same
// page, until its redirect is over (rove#155); a call is no page, and is not
// sent anywhere. One that lapsed when its DNS stopped saying it is here sends
// nothing on, and opens the labels printed with it -- at the service's, while
// the workspace has no app domain.
func TestALegacyDomainSendsItsPagesOn(t *testing.T) {
	w := newHosted(t)
	x := w.x
	ctx := context.Background()

	beta, err := w.s.Base.Tenant().Add(ctx, app.TenantAddRequest_builder{Alias: "beta"}.Build())
	x.NoError(err)
	w.domain(w.tenant, ownHost, "app", "active")
	w.domain(w.tenant, "old.acme.example", "app", "legacy")
	w.domain(beta, "beta.example", "app", "lapsed")
	until := func(host string, at time.Time) {
		x.NoError(w.s.Ent.TenantDomain.Update().Where(tenantdomain.Host(host)).SetRedirectUntil(at).Exec(ctx))
	}
	until("old.acme.example", time.Now().Add(time.Hour))

	for _, method := range []string{http.MethodGet, http.MethodHead} {
		res := w.send(nil, method, "old.acme.example", "/@acme/assets?q=rack")
		x.Equal(http.StatusFound, res.Code, method)
		x.Equal("https://"+ownHost+"/@acme/assets?q=rack", res.Header().Get("Location"), method)
	}
	x.Equal(http.StatusNotFound, w.me(w.owner, "old.acme.example", "acme").Code, "a call is sent nowhere")
	res := w.send(nil, http.MethodGet, "old.acme.example", "/l/7K3MQ9TX")
	x.Equal("https://"+ownHost+"/scan/7K3MQ9TX", res.Header().Get("Location"), "its labels open where the app is")

	// Its redirect over: labels, and nothing else.
	until("old.acme.example", time.Now().Add(-time.Second))
	x.Equal(http.StatusNotFound, w.send(nil, http.MethodGet, "old.acme.example", "/@acme/assets").Code)
	x.Equal(http.StatusFound, w.send(nil, http.MethodGet, "old.acme.example", "/l/7K3MQ9TX").Code)

	// Lapsed: nothing sent on, whatever it says, and its labels open at the
	// service's.
	until("beta.example", time.Now().Add(time.Hour))
	x.Equal(http.StatusNotFound, w.send(nil, http.MethodGet, "beta.example", "/@beta/assets").Code)
	res = w.send(nil, http.MethodGet, "beta.example", "/l/7K3MQ9TX")
	x.Equal(http.StatusFound, res.Code)
	x.Equal("https://"+serviceHost+"/scan/7K3MQ9TX", res.Header().Get("Location"))
	x.Equal(http.StatusNotFound, w.me(w.owner, "beta.example", "beta").Code, "and serves no call")
}

// TestAWorkspacesPageAtTheServiceGoesToItsDomain: a page of a workspace whose
// app is on a domain of its own, opened at the service's address, is sent to
// the same page there (rove#155), where the handoff signs the browser in; the
// account says where each workspace's app is, for the page to go there
// itself. What is the service's stays there: a call, the account, a label.
// A domain not in use -- lapsed, legacy -- sends nothing anywhere.
func TestAWorkspacesPageAtTheServiceGoesToItsDomain(t *testing.T) {
	w := newHosted(t)
	x := w.x
	ctx := context.Background()

	kim, err := w.s.Identity.AddPerson(ctx, "kim", "Kim", pdid.Nil)
	x.NoError(err)
	beta, err := w.s.Base.Tenant().Add(ctx, app.TenantAddRequest_builder{Alias: "beta"}.Build())
	x.NoError(err)
	as := w.session(w.here(kim, "member"))
	w.member(beta, kim, "member")
	w.domain(w.tenant, ownHost, "app", "active")
	w.domain(beta, "beta.example", "app", "lapsed")

	page := func(host, path string) *httptest.ResponseRecorder {
		return w.send(nil, http.MethodGet, host, path, cookieIn(as))
	}
	res := page(serviceHost, "/@acme/assets?q=rack")
	x.Equal(http.StatusFound, res.Code)
	x.Equal("https://"+ownHost+"/@acme/assets?q=rack", res.Header().Get("Location"), "the same page, on its domain")
	x.Equal(http.StatusOK, page(ownHost, "/@acme/assets").Code, "and served there")

	// What is the service's stays there.
	x.Equal(http.StatusOK, w.me(as, serviceHost, "acme").Code, "a call")
	res = page(serviceHost, "/account")
	x.Equal(http.StatusOK, res.Code)
	var a account.Account
	x.NoError(json.Unmarshal(res.Body.Bytes(), &a))
	homes := map[string]string{}
	for _, v := range a.Workspaces {
		homes[v.Alias] = v.Home
	}
	x.Equal(map[string]string{"acme": "https://" + ownHost, "beta": ""}, homes, "where each workspace's app is")
	x.Equal("https://"+serviceHost+"/scan/7K3MQ9TX", page(serviceHost, "/l/7K3MQ9TX").Header().Get("Location"), "a label scanned at the service's")

	// A domain not in use sends nothing on.
	x.Equal(http.StatusOK, page(serviceHost, "/@beta/assets").Code, "lapsed")
	x.NoError(w.s.Ent.TenantDomain.Update().Where(tenantdomain.Host(ownHost)).SetState("legacy").Exec(ctx))
	x.Equal(http.StatusOK, page(serviceHost, "/@acme/assets").Code, "legacy")
	var logs bytes.Buffer
	x.Equal(http.StatusOK, w.send(&logs, http.MethodGet, serviceHost, "/@nobody/assets", cookieIn(as)).Code, "and a workspace that is not there is the page's to say")
	x.Empty(logs.String(), "and nothing went wrong")
}
