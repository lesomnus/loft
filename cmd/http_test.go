package cmd

import (
	"bytes"
	"context"
	"log/slog"
	"net"
	"net/http"
	"testing"

	"github.com/lesomnus/otx/log"
	"github.com/stretchr/testify/require"
)

// TestWhatARouteSaysIsSaidWhereTheProcessSaysIt: a request runs on the
// context the listener was opened with, so a route that logs through it is
// heard. On the bare context a server gives a request, `log.From` falls back
// to the OpenTelemetry globals, which discard.
func TestWhatARouteSaysIsSaidWhereTheProcessSaysIt(t *testing.T) {
	x := require.New(t)

	var said bytes.Buffer
	ctx := log.Into(context.Background(), slog.New(slog.NewTextHandler(&said, nil)))
	srv := httpServer(ctx, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		log.From(r.Context()).WarnContext(r.Context(), "a route says something")
		w.WriteHeader(http.StatusNoContent)
	}))

	l, err := net.Listen("tcp", "127.0.0.1:0")
	x.NoError(err)
	go srv.Serve(l)
	t.Cleanup(func() { srv.Close() })

	res, err := http.Get("http://" + l.Addr().String() + "/")
	x.NoError(err)
	res.Body.Close()
	x.Equal(http.StatusNoContent, res.StatusCode)
	x.Contains(said.String(), "a route says something", "what the route said went nowhere")
}
