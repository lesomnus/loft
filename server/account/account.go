// Package account is what a signed-in account has outside any one workspace:
// the workspaces it is in, making another, which of them a label it scanned
// is in, and what it agreed to (design 9.11, D28).
//
// It is HTTP beside `/session` rather than an RPC, and that is the reason it
// is a package of its own. Every RPC is served as a membership of one
// workspace (`cmd.Resolver`), and these are the questions asked before there
// is one -- which workspace to open, and a new one to open.
package account

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/lesomnus/otx/log"
	"github.com/lesomnus/payday/auth"
	"github.com/lesomnus/payday/auth/authsession"
	"github.com/lesomnus/payday/pdid"

	"github.com/lesomnus/rove/internal/identity"
	"github.com/lesomnus/rove/server/consent"
	"github.com/lesomnus/rove/server/tenancy"
)

// People is the account a session names, at roster.
type People interface {
	ById(ctx context.Context, sub string) (identity.Person, error)
}

// Account is what `GET /account` answers.
type Account struct {
	Id         string      `json:"id"`
	Login      string      `json:"login"`
	Name       string      `json:"name"`
	Workspaces []Workspace `json:"workspaces"`
	// Owns is how many of them it owns, and May how many it may: another
	// past that is paid for. May is absent for no limit.
	Owns int  `json:"owns"`
	May  *int `json:"may,omitempty"`
	// Manage is where the account itself is managed at roster, and deleted
	// where its directory offers that; absent for no such page.
	Manage string `json:"manage,omitempty"`
	// Owes is what it has to agree to before it uses anything here, at the
	// versions in force; absent for nothing (design 9.11).
	Owes []consent.Document `json:"owes,omitempty"`
}

// Workspace is one an account is in.
type Workspace struct {
	Id    string `json:"id"`
	Alias string `json:"alias"`
	Name  string `json:"name"`
	Role  string `json:"role"`
	// Closed is when it was closed and Destroy when it goes, for one that is
	// closed (D29).
	Closed  *time.Time `json:"closed,omitempty"`
	Destroy *time.Time `json:"destroy,omitempty"`
}

// Exporter writes everything a workspace has, as `rove tenant export` does.
type Exporter func(ctx context.Context, workspace pdid.Id, w io.Writer) error

// LabelFinder is the workspace a scanned code is in, whichever workspace it
// was scanned from, or false for none (`domain.LabelWorkspace`).
type LabelFinder func(ctx context.Context, code string) (workspace pdid.Id, ok bool, err error)

// Routes is what is served for an account beside `/session`.
type Routes struct {
	Sessions   *authsession.Sessions
	People     People
	Workspaces tenancy.Workspaces
	Export     Exporter
	// Label answers a scan made in a workspace a label is not in; nil
	// answers none.
	Label LabelFinder
	// Manage is roster's account page (`auth.roster.account_url`).
	Manage string
	// Consent is what an account agrees to first; nil asks nothing.
	Consent *consent.Consents
	// Arrive is what an account that has just agreed is given: a workspace
	// of its own, as a sign-in gives one.
	Arrive func(ctx context.Context, p identity.Person) error
}

// Mount serves `GET /account`, `POST /account/workspaces`, a workspace's
// data for its owner at `GET /account/workspaces/{alias}/export`, which of the
// account's workspaces a label is in at `GET /account/labels/{code}`, and what
// it agreed to and agreeing at `GET` and `POST /account/consents`.
//
// An account that owes an agreement is refused a new workspace as every RPC
// refuses it (`cmd.Resolver`). Its data it takes out all the same: what it
// has is its own whether or not it agrees to what comes next.
func (a Routes) Mount(mux *http.ServeMux) {
	sessions, people, ws := a.Sessions, a.People, a.Workspaces
	mux.HandleFunc("GET /account/workspaces/{alias}/export", func(w http.ResponseWriter, r *http.Request) {
		p, ok := SignedIn(w, r, sessions, people)
		if !ok {
			return
		}
		ms, err := ws.Of(r.Context(), p.Id)
		if err != nil {
			fail(w, r, err)
			return
		}
		alias := r.PathValue("alias")
		for _, m := range ms {
			if m.Alias != alias {
				continue
			}
			if m.Role != "owner" {
				break
			}
			// Everything it has, open or closed: what an owner takes out
			// before a workspace goes (D29).
			w.Header().Set("Content-Type", "application/zip")
			w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s-%s.zip"`, alias, time.Now().Format("20060102")))
			w.Header().Set("Cache-Control", "no-store")
			if err := a.Export(r.Context(), m.Workspace, w); err != nil {
				log.From(r.Context()).ErrorContext(r.Context(), "account: an export failed part way", "workspace", alias, "err", err.Error())
			}
			return
		}
		http.Error(w, "only the workspace's owner takes its data out", http.StatusForbidden)
	})
	mux.HandleFunc("GET /account", func(w http.ResponseWriter, r *http.Request) {
		p, ok := SignedIn(w, r, sessions, people)
		if !ok {
			return
		}
		v, err := accountOf(r.Context(), ws, p)
		if err != nil {
			fail(w, r, err)
			return
		}
		v.Manage = a.Manage
		if v.Owes, err = a.Consent.Owed(r.Context(), p.Id); err != nil {
			fail(w, r, err)
			return
		}
		answer(w, http.StatusOK, v)
	})
	mux.HandleFunc("POST /account/workspaces", func(w http.ResponseWriter, r *http.Request) {
		// A body that is JSON, which a form on another site cannot send.
		if ct, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type")); ct != "application/json" {
			http.Error(w, "a workspace is asked for as JSON", http.StatusUnsupportedMediaType)
			return
		}
		p, ok := SignedIn(w, r, sessions, people)
		if !ok {
			return
		}
		var in struct {
			Name string `json:"name"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10)).Decode(&in); err != nil {
			http.Error(w, "a workspace is asked for as {\"name\": ...}", http.StatusBadRequest)
			return
		}
		name := strings.TrimSpace(in.Name)
		if name == "" || utf8.RuneCountInString(name) > 80 {
			http.Error(w, "이름은 1~80자로 씁니다", http.StatusBadRequest)
			return
		}
		if ok, err := a.Consent.Settled(r.Context(), p.Id); err != nil {
			fail(w, r, err)
			return
		} else if !ok {
			http.Error(w, "이용약관과 개인정보 처리방침에 먼저 동의하세요", http.StatusForbidden)
			return
		}
		m, err := ws.Create(r.Context(), p, name)
		if errors.Is(err, tenancy.ErrOwnsEnough) {
			http.Error(w, "워크스페이스를 더 만들려면 결제가 필요합니다", http.StatusPaymentRequired)
			return
		}
		if err != nil {
			fail(w, r, err)
			return
		}
		answer(w, http.StatusCreated, workspaceOf(m))
	})
	mux.HandleFunc("GET /account/labels/{code}", func(w http.ResponseWriter, r *http.Request) {
		p, ok := SignedIn(w, r, sessions, people)
		if !ok {
			return
		}
		// A label in a workspace the account is not in reads as no label at
		// all: which codes are somebody's is not for anybody else to learn.
		// The memberships are asked first either way, so that the two take
		// the same asking.
		ms, err := ws.Of(r.Context(), p.Id)
		if err != nil {
			fail(w, r, err)
			return
		}
		var (
			in pdid.Id
			at bool
		)
		if a.Label != nil {
			if in, at, err = a.Label(r.Context(), r.PathValue("code")); err != nil {
				fail(w, r, err)
				return
			}
		}
		for _, m := range ms {
			if at && m.Workspace == in {
				answer(w, http.StatusOK, workspaceOf(m))
				return
			}
		}
		http.Error(w, "no workspace of yours has this label", http.StatusNotFound)
	})
	mux.HandleFunc("GET /account/consents", func(w http.ResponseWriter, r *http.Request) {
		p, ok := SignedIn(w, r, sessions, people)
		if !ok {
			return
		}
		owed, err := a.Consent.Owed(r.Context(), p.Id)
		if err != nil {
			fail(w, r, err)
			return
		}
		agreed := []consent.Agreement{}
		if a.Consent != nil {
			if agreed, err = a.Consent.Of(r.Context(), p.Id); err != nil {
				fail(w, r, err)
				return
			}
		}
		answer(w, http.StatusOK, map[string]any{"owed": owed, "agreed": agreed})
	})
	mux.HandleFunc("POST /account/consents", func(w http.ResponseWriter, r *http.Request) {
		// A body that is JSON, which a form on another site cannot send: an
		// agreement is the account's own doing.
		if ct, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type")); ct != "application/json" {
			http.Error(w, "an agreement is sent as JSON", http.StatusUnsupportedMediaType)
			return
		}
		p, ok := SignedIn(w, r, sessions, people)
		if !ok {
			return
		}
		var in struct {
			// Agree is each document agreed to, by name, with the version
			// read.
			Agree map[string]string `json:"agree"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10)).Decode(&in); err != nil {
			http.Error(w, "an agreement is sent as {\"agree\": {\"terms\": <version>, ...}}", http.StatusBadRequest)
			return
		}
		client := "web"
		if r.Header.Get("User-Agent") == "rove-cli" {
			client = "cli"
		}
		err := a.Consent.Agree(r.Context(), p.Id, in.Agree, client)
		switch {
		case errors.Is(err, consent.ErrOwed):
			http.Error(w, "모든 문서에 동의해야 합니다", http.StatusConflict)
			return
		case errors.Is(err, consent.ErrStale):
			http.Error(w, "문서가 바뀌었습니다. 새로 읽고 다시 동의하세요", http.StatusConflict)
			return
		case err != nil:
			fail(w, r, err)
			return
		}
		// What a sign-in would have given it, had it agreed then.
		if a.Arrive != nil {
			if err := a.Arrive(r.Context(), p); err != nil {
				fail(w, r, err)
				return
			}
		}
		w.WriteHeader(http.StatusNoContent)
	})
}

// SignedIn is the account the request's session names, or false with the
// answer written: what every HTTP route beside the RPCs asks first.
func SignedIn(w http.ResponseWriter, r *http.Request, sessions *authsession.Sessions, people People) (identity.Person, bool) {
	ctx := r.Context()
	key := sessions.KeyOf(r.Header.Values("Cookie"))
	if key == "" {
		http.Error(w, "who is asking?", http.StatusUnauthorized)
		return identity.Person{}, false
	}
	v, err := sessions.Read(ctx, key)
	switch {
	case errors.Is(err, authsession.ErrNoSession):
		http.Error(w, "who is asking?", http.StatusUnauthorized)
		return identity.Person{}, false
	case err != nil:
		fail(w, r, err)
		return identity.Person{}, false
	}
	p, err := people.ById(ctx, v.Id)
	switch {
	case errors.Is(err, identity.ErrNoPerson):
		http.Error(w, "who is asking?", http.StatusUnauthorized)
		return identity.Person{}, false
	case err != nil:
		fail(w, r, err)
		return identity.Person{}, false
	}

	return p, true
}

func accountOf(ctx context.Context, ws tenancy.Workspaces, p identity.Person) (Account, error) {
	ms, err := ws.Of(ctx, p.Id)
	if err != nil {
		return Account{}, err
	}
	out := Account{Id: p.Id.String(), Login: p.Alias, Name: p.Name, Workspaces: []Workspace{}}
	for _, m := range ms {
		out.Workspaces = append(out.Workspaces, workspaceOf(m))
		if m.Role == "owner" {
			out.Owns++
		}
	}
	if ws.Owned >= 0 {
		out.May = &ws.Owned
	}

	return out, nil
}

func workspaceOf(m tenancy.Membership) Workspace {
	return Workspace{Id: pdid.Id(m.Workspace).String(), Alias: m.Alias, Name: m.Name, Role: m.Role, Closed: m.Closed, Destroy: m.Destroy}
}

func answer(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

// fail is an error nobody asked for: roster not answering is one to come back
// after, and anything else is this server's.
func fail(w http.ResponseWriter, r *http.Request, err error) {
	if errors.Is(err, auth.ErrUnavailable) || errors.Is(err, identity.ErrUnreachable) {
		http.Error(w, "cannot say just now", http.StatusServiceUnavailable)
		return
	}
	log.From(r.Context()).ErrorContext(r.Context(), "account", "err", err.Error())
	http.Error(w, "something went wrong", http.StatusInternalServerError)
}
