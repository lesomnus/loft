// Package account is what a signed-in account has outside any one workspace:
// the workspaces it is in, and making another (design 9.11).
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
	"mime"
	"net/http"
	"strings"
	"unicode/utf8"

	"github.com/lesomnus/otx/log"
	"github.com/lesomnus/payday/auth"
	"github.com/lesomnus/payday/auth/authsession"
	"github.com/lesomnus/payday/pdid"

	"github.com/lesomnus/rove/internal/identity"
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
}

// Workspace is one an account is in.
type Workspace struct {
	Id    string `json:"id"`
	Alias string `json:"alias"`
	Name  string `json:"name"`
	Role  string `json:"role"`
}

// Handler serves `GET /account` and `POST /account/workspaces`.
func Handler(sessions *authsession.Sessions, people People, ws tenancy.Workspaces) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /account", func(w http.ResponseWriter, r *http.Request) {
		p, ok := signedIn(w, r, sessions, people)
		if !ok {
			return
		}
		v, err := accountOf(r.Context(), ws, p)
		if err != nil {
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
		p, ok := signedIn(w, r, sessions, people)
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

	return mux
}

// signedIn is the account the request's session names, or false with the
// answer written.
func signedIn(w http.ResponseWriter, r *http.Request, sessions *authsession.Sessions, people People) (identity.Person, bool) {
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
	return Workspace{Id: pdid.Id(m.Workspace).String(), Alias: m.Alias, Name: m.Name, Role: m.Role}
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
