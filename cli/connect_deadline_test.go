package cli

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	app "github.com/lesomnus/loft"
)

// TestACallSaysItsDeadline: a terminal's deadline goes with its call, as the
// Connect protocol's own header, which is what a server keeps a call to rather
// than its own default.
func TestACallSaysItsDeadline(t *testing.T) {
	x := require.New(t)
	said := make(chan string, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		said <- r.Header.Get("Connect-Timeout-Ms")
		w.Header().Set("Content-Type", "application/proto")
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)
	c := &connectConn{base: srv.URL, client: srv.Client(), header: http.Header{}}

	ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
	defer cancel()
	x.NoError(c.Invoke(ctx, app.AssetService_Get_FullMethodName, &app.AssetGetRequest{}, &app.Asset{}))
	ms, err := strconv.Atoi(<-said)
	x.NoError(err)
	x.InDelta(time.Minute.Milliseconds(), ms, 5000)

	x.NoError(c.Invoke(t.Context(), app.AssetService_Get_FullMethodName, &app.AssetGetRequest{}, &app.Asset{}))
	x.Empty(<-said, "a call that names no deadline says none")
}
