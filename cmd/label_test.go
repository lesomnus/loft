package cmd_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/lesomnus/payday/pdid"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	app "github.com/lesomnus/loft"
	"github.com/lesomnus/loft/cmd"
)

// TestALabelIsOpenedInTheWorkspaceItIsIn: a label's code is one in the whole
// deployment, so a scan made in one of an account's workspaces finds a label
// in another of its own -- and a label in a workspace it is not in reads as no
// label at all, the same as a code nobody printed (D28).
func TestALabelIsOpenedInTheWorkspaceItIsIn(t *testing.T) {
	w := newWorld(t)
	x := w.x
	ctx := context.Background()

	kim, err := w.s.Identity.AddPerson(ctx, "kim", "Kim", pdid.Nil)
	x.NoError(err)
	beta, err := w.s.Base.Tenant().Add(ctx, app.TenantAddRequest_builder{Alias: "beta"}.Build())
	x.NoError(err)
	inAcme := w.here(kim, "member")
	w.member(beta, kim, "owner")
	md := w.session(inAcme)
	at := func(ws string) context.Context { return metadata.AppendToOutgoingContext(md, cmd.WorkspaceHeader, ws) }

	labels := app.NewLabelServiceClient(w.conn)
	printed, err := labels.Print(at("beta"), app.LabelPrintRequest_builder{Count: 1}.Build())
	x.NoError(err)
	code := printed.GetLabels()[0].GetCode()
	x.Equal("http://localhost:8080/l/"+code, printed.GetUrls()[0], "the deployment's own address")
	_, err = labels.Resolve(at("acme"), app.LabelResolveRequest_builder{Code: printed.GetUrls()[0]}.Build())
	x.Equal(codes.NotFound, status.Code(err), "acme has no such label")

	// What a label's QR opens: the scan page, which asks.
	res := w.page(md, http.MethodGet, "/l/"+code)
	x.Equal(http.StatusFound, res.Code)
	x.Equal("http://localhost:8080/scan/"+code, res.Header().Get("Location"))

	// Which of the account's workspaces it is in, asked as the account
	// (`LabelService.Locate`, D73).
	ask := func(ctx context.Context, code string) (codes.Code, *app.TenantMembership) {
		v, err := labels.Locate(ctx, app.LabelLocateRequest_builder{Code: code}.Build())
		return status.Code(err), v
	}
	for _, v := range []string{code, strings.ToLower(code[:4] + "-" + code[4:]), printed.GetUrls()[0]} {
		n, got := ask(md, v)
		x.Equal(codes.OK, n, v)
		x.Equal("beta", got.GetAlias(), "the workspace of the account's own it is in")
		x.Equal("owner", got.GetRole())
	}

	lee := w.holder("lee", "member")
	n, _ := ask(w.session(lee), code)
	x.Equal(codes.NotFound, n, "a label in a workspace the account is not in reads as none")
	n, _ = ask(md, "ZZZZZZZZ")
	x.Equal(codes.NotFound, n, "as does a code nobody printed")
	n, _ = ask(md, "not a code")
	x.Equal(codes.NotFound, n)
	n, _ = ask(context.Background(), code)
	x.Equal(codes.Unauthenticated, n, "nobody is told without a session")
}

// post is a page's JSON call, as a browser signed in as `ctx`'s session makes
// it.
func (w *world) post(ctx context.Context, path, body string) *httptest.ResponseRecorder {
	mux := http.NewServeMux()
	w.s.Routes(mux, w.c)
	r := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	if c := cookieOf(ctx); c != "" {
		r.Header.Set("Cookie", c)
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, r)
	return rec
}
