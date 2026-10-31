// Command oidctest serves the issuer of package oidctest at an address of its
// own, for the browser tests (ts/e2e): the issuer a Go test stands up with
// [oidctest.New], where a browser can reach it.
//
//	oidctest -addr 127.0.0.1:18540 -client loft-e2e -secret ... \
//	  -redirect http://127.0.0.1:18530/sso/callback -post-logout http://127.0.0.1:18530/
//
// It is a test's and nothing else's, and holds a relying party to what it has
// to send as it does in one. Who signs in is what the test says before it
// signs in, as a Go test sets [oidctest.Issuer.Subject]:
//
//	POST /e2e/next  {"subject": "<roster's identifier for the account>"}
//	POST /e2e/next  {"refuse": "access_denied"}
package main

import (
	"encoding/json"
	"flag"
	"log"
	"net"
	"net/http"

	"github.com/lesomnus/loft/internal/oidctest"
)

func main() {
	addr := flag.String("addr", "127.0.0.1:0", "where it listens")
	client := flag.String("client", "loft", "the one client it knows")
	secret := flag.String("secret", "", "that client's secret")
	redirect := flag.String("redirect", "", "where that client is registered to be sent back")
	logout := flag.String("post-logout", "", "where that client may ask to be sent after signing out")
	flag.Parse()

	is, err := oidctest.Unstarted(*client, *secret)
	if err != nil {
		log.Fatal(err)
	}
	is.Redirect, is.PostLogout = *redirect, *logout

	l, err := net.Listen("tcp", *addr)
	if err != nil {
		log.Fatal(err)
	}
	is.Listener.Close()
	is.Listener = l

	m := http.NewServeMux()
	m.HandleFunc("POST /e2e/next", func(w http.ResponseWriter, r *http.Request) {
		var v struct{ Subject, Refuse string }
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10)).Decode(&v); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		is.Next(v.Subject, v.Refuse)
		w.WriteHeader(http.StatusNoContent)
	})
	m.Handle("/", is.Config.Handler)
	is.Config.Handler = m

	is.Start()
	log.Printf("oidctest: an issuer at %s, for %s", is.URL, is.ClientId)
	select {}
}
