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
//	POST /e2e/reset                                 who pays, how, what was given and the readings a
//	                                                test had it keep forgotten, and every grant in
//	                                                force ended: a workspace it has just met
//	POST /e2e/payerless?workspace=ID&from=RFC3339[&readonly=RFC3339]
//	                                                nobody pays for it since now, restricted from
//	                                                `from`, and read-only from `readonly`: sixty
//	                                                days from now, unsaid
//	POST /e2e/restrict?workspace=ID                 `loft.restricted` granted it, on, for a week
//	POST /e2e/readonly?workspace=ID                 `loft.readonly` granted it, on, for a week
//	POST /e2e/dunning?workspace=ID&kind=K&restricted=RFC3339[&readonly=RFC3339]
//	                                                an invoice billing it whose charge failed, of
//	                                                the kind K declined (temporary, replace,
//	                                                fraud), of the billing account it is paid for
//	                                                with, restricting it from `restricted` and
//	                                                making it read-only from `readonly` (sixty
//	                                                days after it failed, unsaid)
//	POST /e2e/arrive?workspace=ID&account=ID         the workspace made by the roster account ID,
//	                                                who pays for it from now: what the portal tells
//	                                                levyan, and nothing this deployment says (D84)
//	POST /e2e/account?account=ID&name=N             a billing account of the roster account ID's,
//	                                                called N, on the merchant of record: what a
//	                                                workspace it makes is paid for with, opened as
//	                                                its payer opens one where a payer pays
//	POST /e2e/collection?workspace=ID&state=S       the billing account it is paid for with
//	                                                collected as S says from now (collectable,
//	                                                past_due, paused, canceled, needs_action,
//	                                                no_subscription)
//	POST /e2e/accept?workspace=ID                   what is offered for it accepted, as the account
//	                                                offered to accepts it where a payer pays: it
//	                                                pays from the end of the period
//	POST /e2e/reading?workspace=ID&meter=M&value=N  a reading of its meter M, as though the
//	                                                deployment had handed it over now: a workspace
//	                                                a test made is measured at midnight
//	POST /e2e/allowance?workspace=ID&meter=M&quantity=N[&from=RFC3339][&until=RFC3339]
//	                                                N more of M each period, from `from` (now,
//	                                                unsaid) until `until` (until it is ended,
//	                                                unsaid): given as levyan's operator gives one
//	POST /e2e/pool?workspace=ID&meter=M&quantity=N[&from=RFC3339][&expires=RFC3339]
//	                                                a pool of N of M, in its unit for a period,
//	                                                from `from` (now) until `expires` (a year
//	                                                after)
//	POST /e2e/credit?workspace=ID&amount=N[&expires=RFC3339]
//	                                                N of credit, in the minor unit, granted the
//	                                                workspace, expiring at `expires` (a year from
//	                                                now)
//	POST /e2e/close?workspace=ID                    the period before the one now open closed for
//	                                                it: its statement, and what it took of the
//	                                                pools taken off them
package main

import (
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"net/url"
	"strconv"
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
	m.HandleFunc("POST /e2e/reset", func(w http.ResponseWriter, r *http.Request) {
		v.Reset()
		w.WriteHeader(http.StatusNoContent)
	})
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
		readonly := time.Now().Add(levyantest.ReadOnlyAfter)
		if at := r.URL.Query().Get("readonly"); at != "" {
			if readonly, err = time.Parse(time.RFC3339, at); err != nil {
				http.Error(w, "readonly: "+err.Error(), http.StatusBadRequest)
				return
			}
		}
		v.PayerlessUntil(ws.String(), from, readonly)
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
	m.HandleFunc("POST /e2e/readonly", func(w http.ResponseWriter, r *http.Request) {
		ws, err := pdid.Parse(r.URL.Query().Get("workspace"))
		if err != nil {
			http.Error(w, "workspace: "+err.Error(), http.StatusBadRequest)
			return
		}
		v.ReadOnly(ws)
		w.WriteHeader(http.StatusNoContent)
	})
	m.HandleFunc("POST /e2e/dunning", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		ws, err := pdid.Parse(q.Get("workspace"))
		if err != nil {
			http.Error(w, "workspace: "+err.Error(), http.StatusBadRequest)
			return
		}
		ba := v.Payer(ws.String()).GetCurrent().GetBillingAccount()
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
		failed := now.Add(-time.Hour)
		readonly := failed.Add(levyantest.ReadOnlyAfter)
		if at := q.Get("readonly"); at != "" {
			if readonly, err = time.Parse(time.RFC3339, at); err != nil {
				http.Error(w, "readonly: "+err.Error(), http.StatusBadRequest)
				return
			}
		}
		d := levyanv1.Dunning_builder{
			Invoice:        "inv-" + pdid.New(23).String(),
			BillingAccount: ba,
			Kind:           kind,
			DateFailed:     timestamppb.New(failed),
			DateGraceEnds:  timestamppb.New(from),
			DateRestricted: timestamppb.New(from),
			DateReadonly:   timestamppb.New(readonly),
		}.Build()
		if kind == levyanv1.DeclineKind_DECLINE_KIND_TEMPORARY {
			d.SetDateNextRetry(timestamppb.New(now.Add(24 * time.Hour)))
		}
		v.Dun(d, ws.String())
		w.WriteHeader(http.StatusNoContent)
	})
	m.HandleFunc("POST /e2e/arrive", said(func(q url.Values) error {
		ws, err := pdid.Parse(q.Get("workspace"))
		if err != nil {
			return fmt.Errorf("workspace: %w", err)
		}
		account, err := pdid.Parse(q.Get("account"))
		if err != nil {
			return fmt.Errorf("account: %w", err)
		}
		v.Arrived(ws, account, time.Now())
		return nil
	}))
	m.HandleFunc("POST /e2e/account", said(func(q url.Values) error {
		account, err := pdid.Parse(q.Get("account"))
		if err != nil {
			return fmt.Errorf("account: %w", err)
		}
		v.OpenBillingAccount(account.String(), q.Get("name"))
		return nil
	}))
	m.HandleFunc("POST /e2e/collection", said(func(q url.Values) error {
		state, ok := map[string]levyanv1.CollectionState{
			"collectable":     levyanv1.CollectionState_COLLECTION_STATE_COLLECTABLE,
			"past_due":        levyanv1.CollectionState_COLLECTION_STATE_PAST_DUE,
			"paused":          levyanv1.CollectionState_COLLECTION_STATE_PAUSED,
			"canceled":        levyanv1.CollectionState_COLLECTION_STATE_CANCELED,
			"needs_action":    levyanv1.CollectionState_COLLECTION_STATE_NEEDS_ACTION,
			"no_subscription": levyanv1.CollectionState_COLLECTION_STATE_NO_SUBSCRIPTION,
		}[q.Get("state")]
		if !ok {
			return fmt.Errorf("state: collectable, past_due, paused, canceled, needs_action or no_subscription")
		}
		ws, err := workspace(q)
		if err != nil {
			return err
		}
		ba := v.Payer(ws).GetCurrent().GetBillingAccount()
		if ba == "" {
			return fmt.Errorf("workspace: paid for with no billing account")
		}
		v.Collect(ba, state)
		return nil
	}))
	m.HandleFunc("POST /e2e/accept", said(func(q url.Values) error {
		ws, err := workspace(q)
		if err != nil {
			return err
		}
		if v.Accept(ws) == nil {
			return fmt.Errorf("workspace: nothing is offered for it")
		}
		return nil
	}))

	m.HandleFunc("POST /e2e/reading", said(func(q url.Values) error {
		ws, err := workspace(q)
		if err != nil {
			return err
		}
		meter := q.Get("meter")
		unit, ok := units[meter]
		if !ok {
			return fmt.Errorf("meter: one of Loft's, not %q", meter)
		}
		n, err := number(q, "value")
		if err != nil {
			return err
		}
		v.Handed(levyanv1.Reading_builder{Workspace: ws, Meter: meter, Unit: unit, Value: n}.Build())
		return nil
	}))
	m.HandleFunc("POST /e2e/allowance", said(func(q url.Values) error {
		ws, err := workspace(q)
		if err != nil {
			return err
		}
		n, err := number(q, "quantity")
		if err != nil {
			return err
		}
		a := levyanv1.Allowance_builder{Meter: q.Get("meter"), Quantity: n}.Build()
		if err := moment(q, "from", a.SetDateEffective); err != nil {
			return err
		}
		if err := moment(q, "until", a.SetDateExpires); err != nil {
			return err
		}
		v.Allow(ws, a)
		return nil
	}))
	m.HandleFunc("POST /e2e/pool", said(func(q url.Values) error {
		ws, err := workspace(q)
		if err != nil {
			return err
		}
		n, err := number(q, "quantity")
		if err != nil {
			return err
		}
		p := levyanv1.Pool_builder{Meter: q.Get("meter"), Granted: n}.Build()
		if err := moment(q, "from", p.SetDateEffective); err != nil {
			return err
		}
		if err := moment(q, "expires", p.SetDateExpires); err != nil {
			return err
		}
		v.Pool(ws, p)
		return nil
	}))
	m.HandleFunc("POST /e2e/credit", said(func(q url.Values) error {
		ws, err := workspace(q)
		if err != nil {
			return err
		}
		n, err := number(q, "amount")
		if err != nil {
			return err
		}
		c := levyanv1.Credit_builder{Workspace: ws, Granted: n}.Build()
		if err := moment(q, "expires", c.SetDateExpires); err != nil {
			return err
		}
		v.Credit(c)
		return nil
	}))
	m.HandleFunc("POST /e2e/close", said(func(q url.Values) error {
		ws, err := workspace(q)
		if err != nil {
			return err
		}
		v.ClosePeriod(ws)
		return nil
	}))

	log.Printf("levyantest: levyan at %s, told what to say at %s", l.Addr(), *control)
	log.Fatal((&http.Server{Addr: *control, Handler: m, ReadHeaderTimeout: 10 * time.Second}).ListenAndServe())
}

// units is what each of Loft's meters reads in (proto/loft/meter.proto, and
// `retention.Meter`).
var units = map[string]string{
	"loft.assets.active":          "{asset}",
	"loft.attachments.bytes":      "By",
	"loft.members":                "{member}",
	"loft.history.retention_days": "d",
}

// said is a handler of what a test has the fake say, from what it asks: a
// question that does not hold together is 400, with why.
func said(f func(url.Values) error) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if err := f(r.URL.Query()); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

// workspace is the workspace a question names, as levyan names one.
func workspace(q url.Values) (string, error) {
	ws, err := pdid.Parse(q.Get("workspace"))
	if err != nil {
		return "", fmt.Errorf("workspace: %w", err)
	}
	return ws.String(), nil
}

// number is the whole number a question says under `name`.
func number(q url.Values, name string) (int64, error) {
	n, err := strconv.ParseInt(q.Get(name), 10, 64)
	if err != nil {
		return 0, fmt.Errorf("%s: %w", name, err)
	}
	return n, nil
}

// moment sets the moment a question says under `name`, if it says one.
func moment(q url.Values, name string, set func(*timestamppb.Timestamp)) error {
	v := q.Get(name)
	if v == "" {
		return nil
	}
	t, err := time.Parse(time.RFC3339, v)
	if err != nil {
		return fmt.Errorf("%s: %w", name, err)
	}
	set(timestamppb.New(t))
	return nil
}
