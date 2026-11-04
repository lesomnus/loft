// Package admin is the operator's entry point (design 6장, decision 17, D74):
// what an operator does to a deployment from outside the box, served as RPCs
// on a listener of its own.
//
// It serves the services the public server serves, where an entity's RPC is
// what an operator does -- `TenantService.Close`, a contract's `Add` -- the
// same request, served as the operator; and the operator's own services
// (`Operator<X>Service`, `proto/loft/operator.proto`) for what is no
// entity's. gRPC, answered as Connect over HTTP by payday's transcoder, which
// is what the terminal speaks to the public server too. The connection is what
// says who is calling: an operator's key here, a person's session there.
//
// It is served by `loft-admin serve`, and `loft serve` links none of it: the
// public binary has no path that sees every workspace, registers none of the
// operator's services and reads no operator's key. Every call carries an
// operator's key (`admin.keys`) and is logged with the operator's name; what
// it does is the deployment's own work at the operator's word, which the past,
// the trail and the access record name them as having done
// (`server/operator`).
//
// The one path that asks for no key is `/internal/tls-ask`, which the reverse
// proxy in front of workspaces' own domains asks before it takes a
// certificate: whether a host is some workspace's verified domain -- one its
// app is on, or one its labels print with (design 9.9, loft#155) -- and that
// it is not asked for more often than `admin.ask` allows ([Issuance]). It is
// also served alone on a listener of its own (`admin.ask.addr`), so that a
// proxy facing the internet reaches no operator's door.
package admin

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"fmt"
	"maps"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/lesomnus/otx/log"
	"github.com/lesomnus/payday/web"
	"golang.org/x/time/rate"

	"github.com/lesomnus/loft/cmd"
	entdomain "github.com/lesomnus/loft/internal/ent/domain"
	"github.com/lesomnus/loft/internal/identity"
	"github.com/lesomnus/loft/server/operator"
)

// Operator is somebody who may use this entry point: their name in
// `admin.keys`, and the digest of the key they hold.
type Operator struct {
	Name string
	sum  [32]byte
}

// MinKey is the shortest key an operator may hold: this door opens every
// workspace there is.
const MinKey = 32

// Operators reads `admin.keys`: each operator's name, and a key as `env:NAME`,
// `file:PATH` or the key itself.
func Operators(keys map[string]string) ([]Operator, error) {
	out := []Operator{}
	for _, name := range slices.Sorted(maps.Keys(keys)) {
		if strings.TrimSpace(name) == "" {
			return nil, fmt.Errorf("admin.keys: an operator is somebody, by a name")
		}
		v, err := identity.ResolveRef(keys[name])
		if err != nil {
			return nil, fmt.Errorf("admin.keys.%s: %w", name, err)
		}
		if len(v) < MinKey {
			return nil, fmt.Errorf("admin.keys.%s: a key that opens every workspace is not %d characters long; give it %d or more", name, len(v), MinKey)
		}
		out = append(out, Operator{Name: name, sum: sha256.Sum256([]byte(v))})
	}
	return out, nil
}

// holding is the operator who holds `key`, if anybody does. Every key is
// compared, in time that does not say which matched.
func holding(ops []Operator, key string) (operator.Operator, bool) {
	sum := sha256.Sum256([]byte(strings.TrimSpace(key)))
	who := ""
	for _, o := range ops {
		if subtle.ConstantTimeCompare(sum[:], o.sum[:]) == 1 {
			who = o.Name
		}
	}
	if who == "" {
		return operator.Operator{}, false
	}
	return operator.Named(who), true
}

// Routes is the operator's entry point on a deployment.
type Routes struct {
	S         *cmd.Server
	C         cmd.Config
	Operators []Operator

	// Ask is how often the ask says yes; nil says it as often as it is asked.
	Ask *Issuance
}

// Issuance is how often `/internal/tls-ask` says yes: a rate for each host and
// one for all of them (`admin.ask`). It is this process's to keep, which is
// enough for the one entry point a deployment runs.
type Issuance struct {
	all *rate.Limiter

	burst int
	every time.Duration
	mu    sync.Mutex
	// hosts is the rate of each host the ask said yes to, which is a
	// workspace's verified domain: no more of them than there are.
	hosts map[string]*rate.Limiter
}

// NewIssuance is the rates `c` says.
func NewIssuance(c cmd.AskConfig) *Issuance {
	burst, every := c.Rate()
	hostBurst, hostEvery := c.HostRate()
	return &Issuance{
		all:   rate.NewLimiter(rate.Every(every), burst),
		burst: hostBurst,
		every: hostEvery,
		hosts: map[string]*rate.Limiter{},
	}
}

// allow answers whether one more certificate may be taken for `host` now:
// its own rate first, and then all hosts'. One refused by either spends
// neither.
func (i *Issuance) allow(host string, now time.Time) bool {
	i.mu.Lock()
	l, ok := i.hosts[host]
	if !ok {
		l = rate.NewLimiter(rate.Every(i.every), i.burst)
		i.hosts[host] = l
	}
	i.mu.Unlock()

	own := l.ReserveN(now, 1)
	if !own.OK() || own.DelayFrom(now) > 0 {
		own.CancelAt(now)
		return false
	}
	every := i.all.ReserveN(now, 1)
	if !every.OK() || every.DelayFrom(now) > 0 {
		every.CancelAt(now)
		own.CancelAt(now)
		return false
	}
	return true
}

// Who is the name of the operator a call was made by, and empty for none.
func Who(ctx context.Context) string {
	o, _ := operator.From(ctx)
	return o.Name
}

// AskHandler serves `/internal/tls-ask` and nothing else, for
// `admin.ask.addr`.
func (a Routes) AskHandler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /internal/tls-ask", a.tlsAsk)
	return mux
}

// Handler serves the entry point: the operator's RPCs, answered as Connect,
// gRPC-Web and gRPC over HTTP, and the ask beside them.
//
// A request is an RPC by what it says it is (`web.Rpc`), as on the public
// server; anything else is the ask, or nothing.
func (a Routes) Handler(ctx context.Context) (http.Handler, error) {
	g, err := a.Grpc(ctx)
	if err != nil {
		return nil, err
	}
	rpc, err := web.Transcode(g)
	if err != nil {
		return nil, err
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /internal/tls-ask", a.tlsAsk)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if web.Rpc(r) {
			rpc.ServeHTTP(w, r)
			return
		}
		mux.ServeHTTP(w, r)
	}), nil
}

// tlsAsk answers a reverse proxy issuing certificates on demand: only a host
// that is some workspace's domain, verified and in use, gets one -- whatever
// it is for, since a label printed with a domain opens on it -- and no more
// often than [Issuance] allows.
func (a Routes) tlsAsk(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	host := strings.TrimSuffix(strings.ToLower(r.URL.Query().Get("domain")), ".")
	ok, err := a.S.Ent.Domain.Query().Where(
		entdomain.Host(host),
		entdomain.StateIn("ready", "active", "legacy"),
		entdomain.DateErasedIsNil(),
	).Exist(ctx)
	if err != nil || !ok {
		http.Error(w, "not a workspace's domain here", http.StatusNotFound)
		return
	}
	if a.Ask != nil && !a.Ask.allow(host, time.Now()) {
		log.From(ctx).WarnContext(ctx, "tls-ask: a certificate asked for more often than admin.ask allows, refused for now", "host", host)
		http.Error(w, "asked for too often: ask again later", http.StatusTooManyRequests)
		return
	}
	w.WriteHeader(http.StatusOK)
}
