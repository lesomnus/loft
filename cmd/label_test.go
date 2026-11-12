package cmd_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	app "github.com/lesomnus/loft"
)

// TestALabelOpensTheScanPage: a label is printed with the deployment's own
// address, its QR opens the scan page, which reads the code in the workspace
// -- and another workspace in the database, as an import puts one there,
// reads it as no label at all, the same as a code nobody printed.
func TestALabelOpensTheScanPage(t *testing.T) {
	w := newWorld(t)
	x := w.x
	ctx := context.Background()

	labels := app.NewLabelServiceClient(w.conn)
	printed, err := labels.Print(w.owner, app.LabelPrintRequest_builder{Count: 1}.Build())
	x.NoError(err)
	code := printed.GetLabels()[0].GetCode()
	x.Equal("http://localhost:8080/l/"+code, printed.GetUrls()[0], "the deployment's own address")
	for _, v := range []string{code, strings.ToLower(code[:4] + "-" + code[4:]), printed.GetUrls()[0]} {
		got, err := labels.Resolve(w.owner, app.LabelResolveRequest_builder{Code: v}.Build())
		x.NoError(err, v)
		x.Equal(printed.GetLabels()[0].GetId(), got.GetLabel().GetId(), v)
	}

	// What a label's QR opens: the scan page, which asks.
	res := w.page(w.owner, http.MethodGet, "/l/"+code)
	x.Equal(http.StatusFound, res.Code)
	x.Equal("http://localhost:8080/scan/"+code, res.Header().Get("Location"))

	beta, err := w.s.Base.Tenant().Add(ctx, app.TenantAddRequest_builder{Alias: "beta"}.Build())
	x.NoError(err)
	theirs, err := w.s.Base.Holder().Add(ctx, app.HolderAddRequest_builder{
		Tenant: app.TenantRef_builder{Id: beta.GetId()}.Build(),
		Alias:  "owner",
	}.Build())
	x.NoError(err)
	_, err = w.s.Walled.Label().Resolve(frameOf(x, theirs), app.LabelResolveRequest_builder{Code: code}.Build())
	x.Equal(codes.NotFound, status.Code(err), "beta has no such label")
	_, err = labels.Resolve(context.Background(), app.LabelResolveRequest_builder{Code: code}.Build())
	x.Equal(codes.Unauthenticated, status.Code(err), "nobody is told without a session")
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
