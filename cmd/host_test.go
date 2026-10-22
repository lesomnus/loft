package cmd_test

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lesomnus/payday/config"
	"github.com/lesomnus/payday/pdid"
	"github.com/lesomnus/payday/web"
	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"

	app "github.com/lesomnus/rove"
	"github.com/lesomnus/rove/cmd"
	"github.com/lesomnus/rove/server/account"
	"github.com/lesomnus/rove/server/pd"
)

// hosted is the world as the HTTP listener serves it, behind the decision
// the host a request came to makes ([cmd.Hosts]): the page, the RPCs it calls
// and the HTTP beside them.
type hosted struct {
	*world
	h http.Handler
}

func newHosted(t *testing.T) *hosted { return newHostedWith(t, nil) }

// newHostedWith is [newHosted] with one more say over its configuration.
func newHostedWith(t *testing.T, with func(*cmd.Config)) *hosted {
	web := t.TempDir()
	if err := os.WriteFile(filepath.Join(web, "index.html"), []byte("<!doctype html>the app"), 0o644); err != nil {
		t.Fatal(err)
	}
	w := newWorldWith(t, func(c *cmd.Config) {
		c.App.PublicUrl = "https://rove.example"
		c.App.Web = web
		if with != nil {
			with(c)
		}
	})

	g, err := w.s.Grpc(context.Background(), w.c)
	w.x.NoError(err)
	mux, err := webNew(g)
	w.x.NoError(err)
	w.s.Routes(mux.ServeMux, w.c)

	return &hosted{world: w, h: w.s.Hosts(w.c).Handler(mux)}
}

// domain is a domain of `t`'s in a state, as the domain layer leaves one.
func (w *hosted) domain(t *app.Tenant, host, purpose, state string) {
	id, err := pdid.From(t.GetId())
	w.x.NoError(err)
	w.x.NoError(w.s.Ent.TenantDomain.Create().
		SetId(pdid.New(pd.TenantDomainDomain).Uuid()).
		SetTenantId(id.Uuid()).
		SetHost(host).
		SetPurpose(purpose).
		SetState(state).
		SetToken("rove-test").
		SetSource("custom").
		SetDateUpdated(time.Now()).
		Exec(context.Background()))
}

// at is a request to `host`, signed in as `ctx`'s session.
func (w *hosted) at(ctx context.Context, host, method, path, body string, header ...string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	r.Host = host
	if c := cookieOf(ctx); c != "" {
		r.Header.Set("Cookie", c)
	}
	for i := 0; i+1 < len(header); i += 2 {
		r.Header.Set(header[i], header[i+1])
	}
	rec := httptest.NewRecorder()
	w.h.ServeHTTP(rec, r)
	return rec
}

// me is `PartyService.Me` as a page calls it, naming the workspace in `ws`
// or none.
func (w *hosted) me(ctx context.Context, host, ws string) *httptest.ResponseRecorder {
	h := []string{"Content-Type", "application/json", "Connect-Protocol-Version", "1"}
	if ws != "" {
		h = append(h, cmd.WorkspaceHeader, ws)
	}
	return w.at(ctx, host, http.MethodPost, "/rove.PartyService/Me", "{}", h...)
}

// TestAWorkspacesDomainServesItAlone: a request that came to a workspace's
// active app domain is pinned to that workspace (rove#155). A call naming no
// workspace is served there, though the account is in two; one naming the
// other is refused rather than re-pointed, as is a page at the other's
// `/@alias`, a file of the other's, signing in, and making a workspace; and
// what the account is answers that workspace alone. The service's address
// is as it was. A domain that is legacy, or a label domain, pins nothing and
// opens labels; any other host is answered nothing but `/healthz`.
func TestAWorkspacesDomainServesItAlone(t *testing.T) {
	w := newHosted(t)
	x := w.x
	ctx := context.Background()

	kim, err := w.s.Identity.AddPerson(ctx, "kim", "Kim", pdid.Nil)
	x.NoError(err)
	beta, err := w.s.Base.Tenant().Add(ctx, app.TenantAddRequest_builder{Alias: "beta"}.Build())
	x.NoError(err)
	inAcme := w.here(kim, "member")
	w.member(beta, kim, "owner")
	as := w.session(inAcme)

	w.domain(w.tenant, "assets.acme.example", "app", "active")
	w.domain(w.tenant, "old.acme.example", "app", "legacy")
	w.domain(w.tenant, "acme.l.test", "label", "active")
	const own, service = "assets.acme.example", "rove.example"

	// Pinned: the workspace is the domain's, whatever else the account is in.
	res := w.me(as, own, "")
	x.Equal(http.StatusOK, res.Code, res.Body.String())
	var me struct {
		Tenant struct {
			Alias string `json:"alias"`
		} `json:"tenant"`
	}
	x.NoError(json.Unmarshal(res.Body.Bytes(), &me))
	x.Equal("acme", me.Tenant.Alias)
	x.Equal(http.StatusOK, w.me(as, own, "acme").Code, "the domain's own, named")
	x.Equal(http.StatusOK, w.me(as, own, "@"+idOf(t, w.tenant)).Code, "the domain's own, by its identifier")
	res = w.me(as, own, "beta")
	x.Equal(http.StatusForbidden, res.Code, "another workspace, on this one's address")
	x.Contains(res.Body.String(), "serves no other workspace")

	// The service's address: every workspace of the account's, as it was.
	x.Equal(http.StatusBadRequest, w.me(as, service, "").Code, "two workspaces and none named")
	x.Equal(http.StatusOK, w.me(as, service, "beta").Code)

	// What else is refused on the domain, and what the account is there.
	page := w.at(as, own, http.MethodGet, "/@beta/assets", "")
	x.Equal(http.StatusNotFound, page.Code, "another workspace's page")
	x.Equal(http.StatusOK, w.at(as, own, http.MethodGet, "/@acme/assets", "").Code, "its own")
	x.Equal(http.StatusOK, w.at(as, service, http.MethodGet, "/@beta/assets", "").Code, "every workspace's, at the service's")
	file := w.at(as, own, http.MethodGet, "/files/"+idOf(t, beta)+"/2026/10/x", "")
	x.Equal(http.StatusNotFound, file.Code)
	x.Contains(file.Body.String(), "no such file at this address", "another workspace's file")
	x.NotContains(w.at(as, own, http.MethodGet, "/files/"+idOf(t, w.tenant)+"/2026/10/x", "").Body.String(), "no such file at this address", "its own goes to the link's check")
	signIn := w.at(context.Background(), own, http.MethodPost, "/session", `{"login":"kim","password":"x"}`, "Content-Type", "application/json")
	x.Equal(http.StatusNotFound, signIn.Code, "signing in is the service's")
	x.Equal(http.StatusNotFound, w.at(as, own, http.MethodPost, "/account/workspaces", `{"name":"x"}`, "Content-Type", "application/json").Code)
	acct := w.at(as, own, http.MethodGet, "/account", "")
	x.Equal(http.StatusOK, acct.Code, acct.Body.String())
	var a account.Account
	x.NoError(json.Unmarshal(acct.Body.Bytes(), &a))
	x.Len(a.Workspaces, 1, "the account's other workspaces are nothing this address tells")
	x.Equal("acme", a.Workspaces[0].Alias)
	x.Equal("acme", a.Pinned, "and it says whose address this is, for the page to offer nothing of the account's beside it")
	acct = w.at(as, service, http.MethodGet, "/account", "")
	a = account.Account{}
	x.NoError(json.Unmarshal(acct.Body.Bytes(), &a))
	x.Len(a.Workspaces, 2)
	x.Empty(a.Pinned)

	// What an account does across its workspaces is the service's: the
	// invitations addressed to it, from whichever workspace.
	mine := `{"filters":[{"account":"` + base64.StdEncoding.EncodeToString(w.accountOf(inAcme).Bytes()) + `"}]}`
	invitations := func(host string) int {
		return w.at(as, host, http.MethodPost, "/rove.InvitationService/List", mine, "Content-Type", "application/json", "Connect-Protocol-Version", "1").Code
	}
	x.Equal(http.StatusForbidden, invitations(own), "the account's invitations, on a workspace's address")
	x.Equal(http.StatusOK, invitations(service))

	// And the HTTP that names a workspace names this one alone: another's
	// export and another's label are nothing here.
	export := "/account/workspaces/beta/export?reason=" + strings.ReplaceAll("a test", " ", "+")
	x.Equal(http.StatusForbidden, w.at(as, own, http.MethodGet, export, "").Code, "beta's export, on acme's address")
	x.Equal(http.StatusOK, w.at(as, service, http.MethodGet, export, "").Code, "beta's owner takes it out at the service's")
	inBeta := metadata.AppendToOutgoingContext(as, cmd.WorkspaceHeader, "beta")
	label, err := app.NewLabelServiceClient(w.conn).Print(inBeta, app.LabelPrintRequest_builder{Count: 1}.Build())
	x.NoError(err)
	code := label.GetLabels()[0].GetCode()
	x.Equal(http.StatusNotFound, w.at(as, own, http.MethodGet, "/account/labels/"+code, "").Code, "beta's label, on acme's address")
	x.Equal(http.StatusOK, w.at(as, service, http.MethodGet, "/account/labels/"+code, "").Code)

	// A domain that pins nothing: labels, and nothing more.
	for _, host := range []string{"old.acme.example", "acme.l.test"} {
		x.Equal(http.StatusFound, w.at(as, host, http.MethodGet, "/l/7K3MQ9TX", "").Code, host)
		x.Equal(http.StatusNotFound, w.me(as, host, "acme").Code, "%s pins nothing, and serves no call", host)
		x.Equal(http.StatusNotFound, w.at(as, host, http.MethodGet, "/@acme/assets", "").Code, host)
	}

	// Any other host: a probe's `/healthz`, and nothing else.
	x.Equal(http.StatusOK, w.at(as, "10.42.0.7:8080", http.MethodGet, "/healthz", "").Code)
	for _, path := range []string{"/", "/l/7K3MQ9TX", "/account"} {
		x.Equal(http.StatusNotFound, w.at(as, "elsewhere.example", http.MethodGet, path, "").Code, path)
	}
	x.Equal(http.StatusNotFound, w.me(as, "elsewhere.example", "acme").Code)
}

// TestAWorkspacesDomainRefusesAnotherWorkspacesCredential: what pins a call
// is the host, whatever the credential names -- a session whose account is in
// the domain's workspace not at all is refused there, as a membership of
// nothing it may reach.
func TestAWorkspacesDomainRefusesAnotherWorkspacesCredential(t *testing.T) {
	w := newHosted(t)
	x := w.x
	ctx := context.Background()

	lee, err := w.s.Identity.AddPerson(ctx, "lee", "Lee", pdid.Nil)
	x.NoError(err)
	beta, err := w.s.Base.Tenant().Add(ctx, app.TenantAddRequest_builder{Alias: "beta"}.Build())
	x.NoError(err)
	as := w.session(w.member(beta, lee, "owner"))
	w.domain(w.tenant, "assets.acme.example", "app", "active")

	x.Equal(http.StatusOK, w.me(as, "rove.example", "").Code, "its only workspace, at the service's")
	x.Equal(http.StatusForbidden, w.me(as, "assets.acme.example", "").Code, "acme's address, and no membership of acme")
	x.Equal(http.StatusForbidden, w.me(as, "assets.acme.example", "beta").Code)
}

// TestACheckoutIsTheServiceOnEveryHost: a deployment told no address is a
// checkout, and every host that is no workspace's domain is the service's
// there -- a page on another port, a name `localhost` resolves.
func TestACheckoutIsTheServiceOnEveryHost(t *testing.T) {
	w := newWorld(t)
	x := w.x
	g, err := w.s.Grpc(context.Background(), w.c)
	x.NoError(err)
	mux, err := webNew(g)
	x.NoError(err)
	w.s.Routes(mux.ServeMux, w.c)
	h := &hosted{world: w, h: w.s.Hosts(w.c).Handler(mux)}

	as := w.session(w.holder("kim", "member"))
	for _, host := range []string{"localhost:8080", "127.0.0.1:5173", "acme.l.localhost"} {
		x.Equal(http.StatusOK, h.me(as, host, "").Code, host)
	}
}

// webNew is the HTTP listener's handler, as `serve` makes it: the page's
// calls transcoded to the gRPC server.
func webNew(g *grpc.Server) (*web.Mux, error) {
	return web.New(config.HttpConfig{AllowWeb: true}, g)
}

// idOf is a row's identifier, as text.
func idOf(t *testing.T, v interface{ GetId() []byte }) string {
	t.Helper()
	id, err := pdid.From(v.GetId())
	if err != nil {
		t.Fatal(err)
	}
	return id.String()
}
