package cmd_test

import (
	"context"
	"net/http"
	"testing"

	app "github.com/lesomnus/rove"
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
