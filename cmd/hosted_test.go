package cmd_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lesomnus/payday/config"
	"github.com/lesomnus/payday/web"
	"google.golang.org/grpc"

	"github.com/lesomnus/loft/cmd"
	"github.com/lesomnus/loft/internal/ent"
	entsession "github.com/lesomnus/loft/internal/ent/session"
)

// hosted is the world as the HTTP listener serves it: the page, the RPCs it
// calls and the HTTP beside them.
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
		c.App.PublicUrl = "https://loft.example"
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

	return &hosted{world: w, h: mux}
}

// at is a request signed in as `ctx`'s session.
func (w *hosted) at(ctx context.Context, method, path, body string, header ...string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, path, strings.NewReader(body))
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

// rowOf is the session row a cookie's key is.
func (w *hosted) rowOf(c *http.Cookie) *ent.Session {
	return w.s.Ent.Session.Query().Where(entsession.Secret(w.s.Logins.Store.Kept(c.Value))).OnlyX(context.Background())
}

// webNew is the HTTP listener's handler, as `serve` makes it: the page's
// calls transcoded to the gRPC server.
func webNew(g *grpc.Server) (*web.Mux, error) {
	return web.New(config.HttpConfig{AllowWeb: true}, g)
}
