// Command levyantest serves the levyan of package levyantest at an address of
// its own, for the browser tests (ts/e2e, design 7.9): the levyan a Go test
// stands up with [levyantest.Start], where a deployment the tests start can
// reach it. Its product key is [levyantest.Key].
//
//	levyantest -addr 127.0.0.1:18560 -control 127.0.0.1:18561
//
// It is a test's and nothing else's. What a test has it say of a workspace, it
// says on the control address, as a Go test calls the fake's methods:
//
//	GET  /healthz                                   up
//	POST /e2e/payerless?workspace=ID&from=RFC3339   nobody pays for it since now, restricted from `from`
//	POST /e2e/restrict?workspace=ID                 `loft.restricted` granted it, on, for a week
//	POST /e2e/turn?workspace=ID                     its period over: who was to pay from then pays now
//	POST /e2e/dunning?workspace=ID&kind=K&restricted=RFC3339[&billing_account=BA]
//	                                                an invoice billing it whose charge failed, of
//	                                                the kind K declined (temporary, replace,
//	                                                fraud), restricting it from `restricted`: of
//	                                                the billing account BA, or the one that pays
//	                                                for it; without a workspace, of BA alone
package main

import (
	"flag"
	"log"
	"net"
	"net/http"
	"time"

	levyanv1 "github.com/lesomnus/levyan/api/levyan/v1"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/lesomnus/payday/pdid"

	"github.com/lesomnus/loft/internal/levyantest"
)

func main() {
	addr := flag.String("addr", "127.0.0.1:0", "where its gRPC listens")
	control := flag.String("control", "127.0.0.1:0", "where a test tells it what to say")
	flag.Parse()

	l, err := net.Listen("tcp", *addr)
	if err != nil {
		log.Fatal(err)
	}
	v, g := levyantest.Serve(l)
	defer g.Stop()

	m := http.NewServeMux()
	m.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("ok")) })
	m.HandleFunc("POST /e2e/payerless", func(w http.ResponseWriter, r *http.Request) {
		ws, err := pdid.Parse(r.URL.Query().Get("workspace"))
		if err != nil {
			http.Error(w, "workspace: "+err.Error(), http.StatusBadRequest)
			return
		}
		from, err := time.Parse(time.RFC3339, r.URL.Query().Get("from"))
		if err != nil {
			http.Error(w, "from: "+err.Error(), http.StatusBadRequest)
			return
		}
		v.Payerless(ws.String(), from)
		w.WriteHeader(http.StatusNoContent)
	})
	m.HandleFunc("POST /e2e/restrict", func(w http.ResponseWriter, r *http.Request) {
		ws, err := pdid.Parse(r.URL.Query().Get("workspace"))
		if err != nil {
			http.Error(w, "workspace: "+err.Error(), http.StatusBadRequest)
			return
		}
		v.Restrict(ws)
		w.WriteHeader(http.StatusNoContent)
	})
	m.HandleFunc("POST /e2e/turn", func(w http.ResponseWriter, r *http.Request) {
		ws, err := pdid.Parse(r.URL.Query().Get("workspace"))
		if err != nil {
			http.Error(w, "workspace: "+err.Error(), http.StatusBadRequest)
			return
		}
		v.Turn(ws.String())
		w.WriteHeader(http.StatusNoContent)
	})
	m.HandleFunc("POST /e2e/dunning", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		var bills []string
		ba := q.Get("billing_account")
		if at := q.Get("workspace"); at != "" {
			ws, err := pdid.Parse(at)
			if err != nil {
				http.Error(w, "workspace: "+err.Error(), http.StatusBadRequest)
				return
			}
			bills = append(bills, ws.String())
			if ba == "" {
				ba = v.Payer(ws.String()).GetCurrent().GetBillingAccount()
			}
		}
		from, err := time.Parse(time.RFC3339, q.Get("restricted"))
		if err != nil {
			http.Error(w, "restricted: "+err.Error(), http.StatusBadRequest)
			return
		}
		kind, ok := map[string]levyanv1.DeclineKind{
			"temporary": levyanv1.DeclineKind_DECLINE_KIND_TEMPORARY,
			"replace":   levyanv1.DeclineKind_DECLINE_KIND_REPLACE,
			"fraud":     levyanv1.DeclineKind_DECLINE_KIND_FRAUD,
		}[q.Get("kind")]
		if !ok {
			http.Error(w, "kind: temporary, replace or fraud", http.StatusBadRequest)
			return
		}
		now := time.Now()
		d := levyanv1.Dunning_builder{
			Invoice:        "inv-" + pdid.New(23).String(),
			BillingAccount: ba,
			Kind:           kind,
			Failed:         timestamppb.New(now.Add(-time.Hour)),
			GraceEnds:      timestamppb.New(from),
			RestrictedFrom: timestamppb.New(from),
		}.Build()
		if kind == levyanv1.DeclineKind_DECLINE_KIND_TEMPORARY {
			d.SetNextRetry(timestamppb.New(now.Add(24 * time.Hour)))
		}
		v.Dun(d, bills...)
		w.WriteHeader(http.StatusNoContent)
	})

	log.Printf("levyantest: levyan at %s, told what to say at %s", l.Addr(), *control)
	log.Fatal((&http.Server{Addr: *control, Handler: m, ReadHeaderTimeout: 10 * time.Second}).ListenAndServe())
}
