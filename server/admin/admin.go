// Package admin is the operator's entry point (design 6장, decision 17): what
// an operator does to a deployment's workspaces from outside the box -- finds
// them, closes and restores them, moves one's address, names an owner, writes a
// contract and a legal hold -- over HTTP on a listener of its own.
//
// It is served by `rove-admin serve`, and `rove serve` links none of it: the
// public binary has no path that sees every workspace. Every request carries an
// operator's key (`admin.keys`) and is logged with the operator's name; what a
// change does to a workspace is written as the deployment's own work, as the
// shell's operator commands write it.
//
// The one path that asks for no key is `/internal/tls-ask`, which the reverse
// proxy in front of the public entry point asks before it fetches a
// certificate: whether a host is some workspace's verified domain -- one its
// app is on, or one its labels print with (design 9.9, rove#155).
package admin

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"log/slog"
	"maps"
	"mime"
	"net/http"
	"slices"
	"strings"
	"time"
	"uuid"

	"github.com/lesomnus/otx/log"
	"github.com/lesomnus/payday/pdid"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"

	app "github.com/lesomnus/rove"
	"github.com/lesomnus/rove/cmd"
	"github.com/lesomnus/rove/internal/ent"
	"github.com/lesomnus/rove/internal/ent/holder"
	"github.com/lesomnus/rove/internal/ent/legalhold"
	enttenant "github.com/lesomnus/rove/internal/ent/tenant"
	"github.com/lesomnus/rove/internal/ent/tenantcontract"
	"github.com/lesomnus/rove/internal/ent/tenantdomain"
	"github.com/lesomnus/rove/internal/identity"
	"github.com/lesomnus/rove/server/domain"
)

// Operator is somebody who may use this entry point: a name for the log, and
// the digest of the key they hold.
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

// Routes is the operator's entry point on a deployment.
type Routes struct {
	S         *cmd.Server
	C         cmd.Config
	Operators []Operator
}

type operatorKey struct{}

// Who is the operator a request was made by.
func Who(ctx context.Context) string {
	v, _ := ctx.Value(operatorKey{}).(string)
	return v
}

// Handler serves the entry point.
func (a Routes) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /internal/tls-ask", a.tlsAsk)

	api := http.NewServeMux()
	api.HandleFunc("GET /admin/workspaces", a.list)
	api.HandleFunc("GET /admin/workspaces/{alias}", a.show)
	api.HandleFunc("POST /admin/workspaces/{alias}/close", a.close)
	api.HandleFunc("POST /admin/workspaces/{alias}/restore", a.restore)
	api.HandleFunc("POST /admin/workspaces/{alias}/alias", a.move)
	api.HandleFunc("POST /admin/workspaces/{alias}/owner", a.owner)
	api.HandleFunc("POST /admin/workspaces/{alias}/contracts", a.contract)
	api.HandleFunc("POST /admin/workspaces/{alias}/holds", a.hold)
	api.HandleFunc("POST /admin/holds/{id}/lift", a.lift)
	mux.Handle("/admin/", a.authorized(api))

	return mux
}

// tlsAsk answers a reverse proxy issuing certificates on demand: only a host
// that is some workspace's domain, verified and not retired, gets one --
// whatever it is for, since a label printed with a domain opens on it.
func (a Routes) tlsAsk(w http.ResponseWriter, r *http.Request) {
	host := strings.ToLower(r.URL.Query().Get("domain"))
	ok, err := a.S.Ent.TenantDomain.Query().Where(
		tenantdomain.Host(host),
		tenantdomain.StateIn("ready", "active", "legacy"),
		tenantdomain.DateErasedIsNil(),
	).Exist(r.Context())
	if err != nil || !ok {
		http.Error(w, "not a workspace's domain here", http.StatusNotFound)
		return
	}
	w.WriteHeader(http.StatusOK)
}

type recorder struct {
	http.ResponseWriter
	code int
}

func (r *recorder) WriteHeader(code int) {
	r.code = code
	r.ResponseWriter.WriteHeader(code)
}

// authorized lets through a request that carries an operator's key, and says
// in the log who made it and what came of it.
func (a Routes) authorized(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		who := ""
		if tok, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer "); ok {
			sum := sha256.Sum256([]byte(strings.TrimSpace(tok)))
			for _, o := range a.Operators {
				if subtle.ConstantTimeCompare(sum[:], o.sum[:]) == 1 {
					who = o.Name
				}
			}
		}
		if who == "" {
			log.From(ctx).WarnContext(ctx, "admin: a request without an operator's key", "addr", r.RemoteAddr, "method", r.Method, "path", r.URL.Path)
			http.Error(w, "who is asking?", http.StatusUnauthorized)
			return
		}
		rec := &recorder{ResponseWriter: w, code: http.StatusOK}
		next.ServeHTTP(rec, r.WithContext(context.WithValue(ctx, operatorKey{}, who)))
		log.From(ctx).InfoContext(ctx, "admin", "operator", who, "method", r.Method, "path", r.URL.Path, "status", rec.code)
	})
}

// Workspace is a workspace as an operator finds it.
type Workspace struct {
	Id       string     `json:"id"`
	Alias    string     `json:"alias"`
	Name     string     `json:"name"`
	Created  time.Time  `json:"created"`
	Closed   *time.Time `json:"closed,omitempty"`
	ClosedBy string     `json:"closed_by,omitempty"`
	Destroy  *time.Time `json:"destroy,omitempty"`
	// Owners is its owners' memberships, by alias.
	Owners  []string `json:"owners"`
	Members int      `json:"members"`
}

// Detail is one workspace, with who is in it and what it is held to.
type Detail struct {
	Workspace
	People    []Member   `json:"people"`
	Contracts []Contract `json:"contracts"`
	Holds     []Hold     `json:"holds"`
}

// Member is one membership of a workspace.
type Member struct {
	Alias   string     `json:"alias"`
	Name    string     `json:"name"`
	Role    string     `json:"role"`
	Leaving *time.Time `json:"leaving,omitempty"`
}

// Contract is what a workspace's history is held to from a moment on: every
// term of a `TenantContract` an operator writes.
type Contract struct {
	Name      string `json:"name"`
	Desc      string `json:"desc,omitempty"`
	ViewDays  uint32 `json:"view_days"`
	KeepDays  uint32 `json:"keep_days"`
	GraceDays uint32 `json:"grace_days"`
	// AccessKeepDays and RestoreDays are the deployment's when 0 (#144).
	AccessKeepDays uint32 `json:"access_keep_days"`
	RestoreDays    uint32 `json:"restore_days"`
	// PurgeFloors is a workspace destroyed purged, what the deployment's
	// floors are on with it, and not retired (payday#115).
	PurgeFloors bool      `json:"purge_floors"`
	Effective   time.Time `json:"effective"`
}

// Hold is a legal hold on a workspace's history.
type Hold struct {
	Id     string     `json:"id"`
	Why    string     `json:"why"`
	Desc   string     `json:"desc,omitempty"`
	Placed time.Time  `json:"placed"`
	Lifted *time.Time `json:"lifted,omitempty"`
}

func (a Routes) workspaceOf(ctx context.Context, t *ent.Tenant) (Workspace, error) {
	hs, err := a.S.Ent.Holder.Query().Where(holder.TenantId(t.Id), holder.DateErasedIsNil()).All(ctx)
	if err != nil {
		return Workspace{}, err
	}
	v := Workspace{
		Id: pdid.Id(t.Id).String(), Alias: t.Alias, Name: t.Name, Created: t.DateCreated,
		Closed: t.DateClosed, ClosedBy: t.ClosedBy, Destroy: t.DateDestroy,
		Owners: []string{}, Members: len(hs),
	}
	for _, h := range hs {
		if h.Role == "owner" {
			v.Owners = append(v.Owners, h.Alias)
		}
	}
	slices.Sort(v.Owners)
	return v, nil
}

// list finds workspaces: by a piece of their address or name, closed ones
// alone with `closed`.
func (a Routes) list(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	q := a.S.Ent.Tenant.Query().Order(ent.Asc(enttenant.FieldAlias)).Limit(500)
	if v := strings.TrimSpace(r.URL.Query().Get("q")); v != "" {
		q = q.Where(enttenant.Or(enttenant.AliasContainsFold(v), enttenant.NameContainsFold(v)))
	}
	if r.URL.Query().Get("closed") != "" {
		q = q.Where(enttenant.DateClosedNotNil())
	}
	ts, err := q.All(ctx)
	if err != nil {
		fail(w, r, err)
		return
	}
	out := []Workspace{}
	for _, t := range ts {
		v, err := a.workspaceOf(ctx, t)
		if err != nil {
			fail(w, r, err)
			return
		}
		out = append(out, v)
	}
	answer(w, http.StatusOK, out)
}

func (a Routes) tenant(w http.ResponseWriter, r *http.Request) (*ent.Tenant, bool) {
	t, err := a.S.Ent.Tenant.Query().Where(enttenant.Alias(r.PathValue("alias"))).Only(r.Context())
	if ent.IsNotFound(err) {
		http.Error(w, "no workspace is @"+r.PathValue("alias"), http.StatusNotFound)
		return nil, false
	}
	if err != nil {
		fail(w, r, err)
		return nil, false
	}
	return t, true
}

func (a Routes) show(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	t, ok := a.tenant(w, r)
	if !ok {
		return
	}
	v, err := a.workspaceOf(ctx, t)
	if err != nil {
		fail(w, r, err)
		return
	}
	out := Detail{Workspace: v, People: []Member{}, Contracts: []Contract{}, Holds: []Hold{}}
	hs, err := a.S.Ent.Holder.Query().Where(holder.TenantId(t.Id), holder.DateErasedIsNil()).Order(ent.Asc(holder.FieldAlias)).All(ctx)
	if err != nil {
		fail(w, r, err)
		return
	}
	for _, h := range hs {
		out.People = append(out.People, Member{Alias: h.Alias, Name: h.Name, Role: h.Role, Leaving: h.DateLeaving})
	}
	cs, err := a.S.Ent.TenantContract.Query().
		Where(tenantcontract.TenantIdEQ(t.Id), tenantcontract.DateErasedIsNil()).
		Order(ent.Desc(tenantcontract.FieldDateEffective, tenantcontract.FieldId)).
		All(ctx)
	if err != nil {
		fail(w, r, err)
		return
	}
	for _, c := range cs {
		out.Contracts = append(out.Contracts, Contract{
			Name: c.Name, Desc: c.Desc,
			ViewDays: c.ViewDays, KeepDays: c.KeepDays, GraceDays: c.GraceDays,
			AccessKeepDays: c.AccessKeepDays, RestoreDays: c.RestoreDays, PurgeFloors: c.PurgeFloors,
			Effective: c.DateEffective,
		})
	}
	ls, err := a.S.Ent.LegalHold.Query().Where(legalhold.TenantIdEQ(t.Id)).Order(ent.Desc(legalhold.FieldDateCreated)).All(ctx)
	if err != nil {
		fail(w, r, err)
		return
	}
	for _, l := range ls {
		out.Holds = append(out.Holds, Hold{Id: pdid.Id(l.Id).String(), Why: l.Name, Desc: l.Desc, Placed: l.DateCreated, Lifted: l.DateLifted})
	}
	answer(w, http.StatusOK, out)
}

// read decodes a body that is JSON, which is all a change is asked as.
func read(w http.ResponseWriter, r *http.Request, v any) bool {
	if ct, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type")); ct != "application/json" {
		http.Error(w, "a change is asked for as JSON", http.StatusUnsupportedMediaType)
		return false
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10)).Decode(v); err != nil {
		http.Error(w, "the body: "+err.Error(), http.StatusBadRequest)
		return false
	}
	return true
}

func (a Routes) close(w http.ResponseWriter, r *http.Request) {
	t, ok := a.tenant(w, r)
	if !ok {
		return
	}
	var in struct {
		Reason string `json:"reason"`
	}
	if !read(w, r, &in) {
		return
	}
	if _, err := a.S.Deps.CloseWorkspace(r.Context(), a.S.Base, a.S.Drv, pdid.Id(t.Id), domain.ClosedByOperator, strings.TrimSpace(in.Reason)); err != nil {
		fail(w, r, err)
		return
	}
	a.answerShow(w, r, t.Id)
}

func (a Routes) restore(w http.ResponseWriter, r *http.Request) {
	t, ok := a.tenant(w, r)
	if !ok {
		return
	}
	if _, err := a.S.Deps.RestoreWorkspace(r.Context(), a.S.Base, a.S.Drv, pdid.Id(t.Id)); err != nil {
		fail(w, r, err)
		return
	}
	a.answerShow(w, r, t.Id)
}

func (a Routes) move(w http.ResponseWriter, r *http.Request) {
	t, ok := a.tenant(w, r)
	if !ok {
		return
	}
	var in struct {
		To    string `json:"to"`
		Reuse bool   `json:"reuse"`
	}
	if !read(w, r, &in) {
		return
	}
	if err := a.S.Closing.Rename(r.Context(), t.Alias, strings.TrimSpace(in.To), in.Reuse); err != nil {
		// What Rename refuses is what was asked, said in words.
		if status.Code(err) == codes.Unknown {
			http.Error(w, err.Error(), http.StatusConflict)
			return
		}
		fail(w, r, err)
		return
	}
	a.answerShow(w, r, t.Id)
}

func (a Routes) owner(w http.ResponseWriter, r *http.Request) {
	t, ok := a.tenant(w, r)
	if !ok {
		return
	}
	var in struct {
		// Member is the membership, by its alias in the workspace.
		Member string `json:"member"`
	}
	if !read(w, r, &in) {
		return
	}
	h, err := a.S.Ent.Holder.Query().Where(holder.TenantId(t.Id), holder.Alias(strings.TrimPrefix(strings.TrimSpace(in.Member), "@")), holder.DateErasedIsNil()).Only(r.Context())
	if ent.IsNotFound(err) {
		http.Error(w, "@"+t.Alias+" has no member @"+in.Member, http.StatusNotFound)
		return
	}
	if err != nil {
		fail(w, r, err)
		return
	}
	if err := a.S.Deps.NameOwner(r.Context(), a.S.Base, a.S.Drv, pdid.Id(t.Id), pdid.Id(h.Id)); err != nil {
		fail(w, r, err)
		return
	}
	a.answerShow(w, r, t.Id)
}

func (a Routes) contract(w http.ResponseWriter, r *http.Request) {
	t, ok := a.tenant(w, r)
	if !ok {
		return
	}
	var in struct {
		Name           string     `json:"name"`
		Desc           string     `json:"desc"`
		ViewDays       uint32     `json:"view_days"`
		KeepDays       uint32     `json:"keep_days"`
		GraceDays      uint32     `json:"grace_days"`
		AccessKeepDays uint32     `json:"access_keep_days"`
		RestoreDays    uint32     `json:"restore_days"`
		PurgeFloors    bool       `json:"purge_floors"`
		Effective      *time.Time `json:"effective"`
	}
	if !read(w, r, &in) {
		return
	}
	at := time.Now()
	if in.Effective != nil {
		at = *in.Effective
	}
	if _, err := a.S.Base.TenantContract().Add(r.Context(), app.TenantContractAddRequest_builder{
		Tenant:         app.TenantRef_builder{Id: t.Id[:]}.Build(),
		Name:           strings.TrimSpace(in.Name),
		Desc:           strings.TrimSpace(in.Desc),
		ViewDays:       in.ViewDays,
		KeepDays:       in.KeepDays,
		GraceDays:      in.GraceDays,
		AccessKeepDays: &in.AccessKeepDays,
		RestoreDays:    &in.RestoreDays,
		PurgeFloors:    &in.PurgeFloors,
		DateEffective:  timestamppb.New(at),
	}.Build()); err != nil {
		fail(w, r, err)
		return
	}
	a.answerShow(w, r, t.Id)
}

func (a Routes) hold(w http.ResponseWriter, r *http.Request) {
	t, ok := a.tenant(w, r)
	if !ok {
		return
	}
	var in struct {
		Why  string `json:"why"`
		Desc string `json:"desc"`
	}
	if !read(w, r, &in) {
		return
	}
	if strings.TrimSpace(in.Why) == "" {
		http.Error(w, "a hold says what it is for", http.StatusBadRequest)
		return
	}
	if _, err := a.S.Base.LegalHold().Add(r.Context(), app.LegalHoldAddRequest_builder{
		Tenant: app.TenantRef_builder{Id: t.Id[:]}.Build(),
		Name:   strings.TrimSpace(in.Why),
		Desc:   strings.TrimSpace(in.Desc),
	}.Build()); err != nil {
		fail(w, r, err)
		return
	}
	a.answerShow(w, r, t.Id)
}

func (a Routes) lift(w http.ResponseWriter, r *http.Request) {
	id, err := pdid.Parse(r.PathValue("id"))
	if err != nil {
		http.Error(w, "a hold, by the identifier `show` lists", http.StatusBadRequest)
		return
	}
	l, err := a.S.Ent.LegalHold.Get(r.Context(), id.Uuid())
	if ent.IsNotFound(err) {
		http.Error(w, "no such hold", http.StatusNotFound)
		return
	}
	if err != nil {
		fail(w, r, err)
		return
	}
	if _, err := a.S.Base.LegalHold().Patch(r.Context(), app.LegalHoldPatchRequest_builder{
		Ref:        app.LegalHoldRef_builder{Id: id.Bytes()}.Build(),
		DateLifted: timestamppb.Now(),
	}.Build()); err != nil {
		fail(w, r, err)
		return
	}
	a.answerShow(w, r, l.TenantId)
}

// answerShow answers a change with the workspace as it is now.
func (a Routes) answerShow(w http.ResponseWriter, r *http.Request, tenant uuid.UUID) {
	t, err := a.S.Ent.Tenant.Get(r.Context(), tenant)
	if err != nil {
		fail(w, r, err)
		return
	}
	r.SetPathValue("alias", t.Alias)
	a.show(w, r)
}

func answer(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

// fail says what the domain refused in its words, and anything else is this
// server's.
func fail(w http.ResponseWriter, r *http.Request, err error) {
	if s, ok := status.FromError(err); ok {
		switch s.Code() {
		case codes.InvalidArgument:
			http.Error(w, s.Message(), http.StatusBadRequest)
			return
		case codes.FailedPrecondition, codes.AlreadyExists, codes.Aborted:
			http.Error(w, s.Message(), http.StatusConflict)
			return
		case codes.NotFound:
			http.Error(w, s.Message(), http.StatusNotFound)
			return
		}
	}
	log.From(r.Context()).ErrorContext(r.Context(), "admin", "operator", Who(r.Context()), slog.Any("err", err))
	http.Error(w, "something went wrong", http.StatusInternalServerError)
}
