// Package clilogin signs a terminal in as whoever approves it in a browser:
// `rove login`.
//
// A terminal cannot do what the sign-in page does -- SaaS signs people in
// through an issuer and the IdP's own pages (D28), which want a browser -- and
// it should not be handed a password either. So it asks, and a person already
// signed in at the app says yes:
//
//  1. the terminal makes up the session key it will use, keeps it, and posts
//     only its SHA-256 (`POST /cli/logins`); it is shown a code and a link;
//  2. the person opens the link signed in, sees the code and where the asking
//     came from, and approves it (`POST /cli/logins/approve`) -- which makes
//     the session for **their** account, under the hash the terminal sent;
//  3. the terminal, asking all the while (`POST /cli/logins/{id}/poll`), hears
//     it was approved and uses the key it kept.
//
// No key crosses the wire after it was made, nothing written here could be
// signed in with, and the session is a session like a browser's: held to
// roster on every call, ended by signing out or by roster, idle and lifetime
// as configured.
//
// # What it cannot tell
//
// Whether the terminal is the person's. A code somebody else's terminal shows,
// read out to the person ("approve this for me"), signs that terminal in as
// them -- which is the shape of every flow like this one, and why the page
// says where the asking came from and asks them to approve only a terminal of
// their own that they just ran `rove login` on.
package clilogin

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"mime"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/lesomnus/otx/log"
	"github.com/lesomnus/payday/auth/authsession"
	"github.com/lesomnus/payday/pdid"

	"github.com/lesomnus/rove/internal/ent"
	entclilogin "github.com/lesomnus/rove/internal/ent/clilogin"
	entsession "github.com/lesomnus/rove/internal/ent/session"
	"github.com/lesomnus/rove/server/account"
	"github.com/lesomnus/rove/server/pd"
	"github.com/lesomnus/rove/server/session"
)

// The states a login is in. One still pending past its expiry is expired,
// which is said where it is read.
const (
	pending  = "pending"
	approved = "approved"
	denied   = "denied"
	expired  = "expired"
)

// Ttl is how long a terminal waits for somebody to approve it.
const Ttl = 10 * time.Minute

// Interval is how often a terminal asks, in seconds.
const Interval = 2

// Logins is the three halves of a terminal signing in.
type Logins struct {
	Db       *ent.Client
	Sessions *authsession.Sessions
	Store    session.Store
	People   account.People

	// Cookie is the name the session cookie goes by, which a terminal sends
	// its key under; Idle and Lifetime are what the session it gets is held
	// to, as a browser's is.
	Cookie         string
	Idle, Lifetime time.Duration

	// App is where the page that approves is: the app's public address.
	App string

	// Now is the clock; tests move it.
	Now func() time.Time

	limit *limiter
}

// Mount puts the routes on a mux.
func (l *Logins) Mount(mux *http.ServeMux) {
	if l.limit == nil {
		l.limit = &limiter{per: 10 * time.Minute, max: 20, seen: map[string][]time.Time{}}
	}
	mux.HandleFunc("POST /cli/logins", l.start)
	mux.HandleFunc("POST /cli/logins/{id}/poll", l.poll)
	mux.HandleFunc("GET /cli/logins", l.show)
	mux.HandleFunc("POST /cli/logins/approve", l.answer(approved))
	mux.HandleFunc("POST /cli/logins/deny", l.answer(denied))
}

// Started is what a terminal is told when it asks.
type Started struct {
	Id       string    `json:"id"`
	Code     string    `json:"code"`
	Approve  string    `json:"approve"`
	Expires  time.Time `json:"expires"`
	Interval int       `json:"interval"`
	Cookie   string    `json:"cookie"`
}

// Asking is what the page is shown of a terminal waiting.
type Asking struct {
	Code    string    `json:"code"`
	Client  string    `json:"client"`
	Origin  string    `json:"origin"`
	Created time.Time `json:"created"`
	Expires time.Time `json:"expires"`
}

func (l *Logins) now() time.Time {
	if l.Now != nil {
		return l.Now()
	}
	return time.Now()
}

func (l *Logins) start(w http.ResponseWriter, r *http.Request) {
	if !jsonBody(w, r) {
		return
	}
	origin := originOf(r)
	if !l.limit.allow(origin, l.now()) {
		http.Error(w, "too many sign-ins asked from here; wait a little", http.StatusTooManyRequests)
		return
	}
	var in struct {
		Secret string `json:"secret"`
		Client string `json:"client"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10)).Decode(&in); err != nil {
		http.Error(w, `a terminal asks as {"secret": ..., "client": ...}`, http.StatusBadRequest)
		return
	}
	secret, err := base64.RawURLEncoding.DecodeString(in.Secret)
	if err != nil || len(secret) != 32 {
		http.Error(w, "secret: the SHA-256 of the key the terminal keeps, as base64url", http.StatusBadRequest)
		return
	}
	client := strings.TrimSpace(in.Client)
	if client == "" {
		client = "a terminal"
	}
	if utf8.RuneCountInString(client) > 100 {
		client = string([]rune(client)[:100])
	}

	ctx := r.Context()
	taken, err := l.Db.CliLogin.Query().Where(entclilogin.Secret(secret)).Exist(ctx)
	if err == nil && !taken {
		taken, err = l.Db.Session.Query().Where(entsession.Secret(secret)).Exist(ctx)
	}
	if err != nil {
		fail(w, r, err)
		return
	}
	if taken {
		// A key is made up once, and one that was used before is not one.
		http.Error(w, "secret: already used; make up another key", http.StatusConflict)
		return
	}

	now := l.now()
	code, err := l.freeCode(ctx, now)
	if err != nil {
		fail(w, r, err)
		return
	}
	v, err := l.Db.CliLogin.Create().
		SetId(pdid.New(pd.CliLoginDomain).Uuid()).
		SetSecret(secret).
		SetCode(code).
		SetClient(client).
		SetOrigin(origin + agentOf(r)).
		SetState(pending).
		SetDateExpires(now.Add(Ttl)).
		SetDateCreated(now).
		SetDateUpdated(now).
		Save(ctx)
	if err != nil {
		fail(w, r, err)
		return
	}

	reply(w, http.StatusCreated, Started{
		Id:       pdid.Id(v.Id).String(),
		Code:     code,
		Approve:  strings.TrimSuffix(l.App, "/") + "/cli?code=" + code,
		Expires:  v.DateExpires,
		Interval: Interval,
		Cookie:   l.Cookie,
	})
}

func (l *Logins) poll(w http.ResponseWriter, r *http.Request) {
	if !jsonBody(w, r) {
		return
	}
	var in struct {
		Secret string `json:"secret"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10)).Decode(&in); err != nil {
		http.Error(w, `a terminal asks as {"secret": ...}`, http.StatusBadRequest)
		return
	}
	secret, _ := base64.RawURLEncoding.DecodeString(in.Secret)

	ctx := r.Context()
	id, err := pdid.Parse(r.PathValue("id"))
	if err != nil {
		http.Error(w, "no such sign-in", http.StatusNotFound)
		return
	}
	v, err := l.Db.CliLogin.Get(ctx, id.Uuid())
	if ent.IsNotFound(err) || (err == nil && subtle.ConstantTimeCompare(v.Secret, secret) != 1) {
		// Nobody else's to ask about, and not told apart from nothing.
		http.Error(w, "no such sign-in", http.StatusNotFound)
		return
	}
	if err != nil {
		fail(w, r, err)
		return
	}

	state := v.State
	if state == pending && !l.now().Before(v.DateExpires) {
		state = expired
	}
	if state != pending {
		// Told once: the row is the question, and it has been answered.
		if err := l.Db.CliLogin.DeleteOneId(v.Id).Exec(ctx); err != nil && !ent.IsNotFound(err) {
			fail(w, r, err)
			return
		}
	}
	reply(w, http.StatusOK, map[string]string{"state": state})
}

func (l *Logins) show(w http.ResponseWriter, r *http.Request) {
	if _, ok := account.SignedIn(w, r, l.Sessions, l.People); !ok {
		return
	}
	v, ok := l.waiting(w, r, r.URL.Query().Get("code"))
	if !ok {
		return
	}
	reply(w, http.StatusOK, Asking{
		Code: v.Code, Client: v.Client, Origin: v.Origin,
		Created: v.DateCreated, Expires: v.DateExpires,
	})
}

// answer approves a terminal or turns it away, for the account signed in.
func (l *Logins) answer(to string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// A body that is JSON, which a form on another site cannot send.
		if !jsonBody(w, r) {
			return
		}
		p, ok := account.SignedIn(w, r, l.Sessions, l.People)
		if !ok {
			return
		}
		var in struct {
			Code string `json:"code"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10)).Decode(&in); err != nil {
			http.Error(w, `answered as {"code": ...}`, http.StatusBadRequest)
			return
		}
		v, ok := l.waiting(w, r, in.Code)
		if !ok {
			return
		}

		ctx := r.Context()
		now := l.now()
		u := l.Db.CliLogin.UpdateOneId(v.Id).
			Where(entclilogin.State(pending)).
			SetState(to).
			SetDateUpdated(now)
		if to == approved {
			u = u.SetAccount(p.Id.Uuid())
		}
		if err := u.Exec(ctx); err != nil {
			if ent.IsNotFound(err) {
				http.Error(w, "that sign-in was answered already", http.StatusConflict)
				return
			}
			fail(w, r, err)
			return
		}
		if to == approved {
			if err := l.Store.Adopt(ctx, p.Id, v.Secret, now.Add(l.Idle), now.Add(l.Lifetime)); err != nil {
				fail(w, r, err)
				return
			}
			log.From(ctx).InfoContext(ctx, "cli: a terminal was signed in", "account", p.Id.String(), "client", v.Client, "origin", v.Origin)
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

// waiting is the pending, unexpired login a code names, or false with the
// answer written.
func (l *Logins) waiting(w http.ResponseWriter, r *http.Request, code string) (*ent.CliLogin, bool) {
	code = normalCode(code)
	if code == "" {
		http.Error(w, "no such sign-in", http.StatusNotFound)
		return nil, false
	}
	v, err := l.Db.CliLogin.Query().Where(entclilogin.Code(code), entclilogin.State(pending)).First(r.Context())
	if ent.IsNotFound(err) || (err == nil && !l.now().Before(v.DateExpires)) {
		http.Error(w, "no such sign-in, or it is too late: run `rove login` again", http.StatusNotFound)
		return nil, false
	}
	if err != nil {
		fail(w, r, err)
		return nil, false
	}
	return v, true
}

// Sweep deletes logins nobody will ask about again: answered or expired, and
// a while past it.
func (l *Logins) Sweep(ctx context.Context) error {
	_, err := l.Db.CliLogin.Delete().Where(entclilogin.DateExpiresLT(l.now().Add(-time.Hour))).Exec(ctx)
	return err
}

// The code a person reads: eight of Crockford's base32, which has no I, L, O
// or U to be mistaken, in two halves.
const alphabet = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"

func newCode() (string, error) {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	out := make([]byte, 0, 9)
	for i, v := range b {
		if i == 4 {
			out = append(out, '-')
		}
		out = append(out, alphabet[int(v)%len(alphabet)])
	}
	return string(out), nil
}

// normalCode is a code as a person typed it, as it is stored: upper case,
// the letters Crockford reads as digits read so, the halves joined by one dash.
func normalCode(v string) string {
	v = strings.ToUpper(strings.Map(func(r rune) rune {
		switch r {
		case '-', ' ':
			return -1
		}
		return r
	}, v))
	v = strings.NewReplacer("I", "1", "L", "1", "O", "0").Replace(v)
	if len(v) != 8 || strings.Trim(v, alphabet) != "" {
		return ""
	}
	return v[:4] + "-" + v[4:]
}

func (l *Logins) freeCode(ctx context.Context, now time.Time) (string, error) {
	for range 8 {
		c, err := newCode()
		if err != nil {
			return "", err
		}
		taken, err := l.Db.CliLogin.Query().Where(entclilogin.Code(c), entclilogin.State(pending), entclilogin.DateExpiresGT(now)).Exist(ctx)
		if err != nil {
			return "", err
		}
		if !taken {
			return c, nil
		}
	}
	return "", errors.New("cli: no free code")
}

// originOf is where a request came from, as a person would recognise it.
func originOf(r *http.Request) string {
	if v := strings.TrimSpace(strings.Split(r.Header.Get("X-Forwarded-For"), ",")[0]); v != "" {
		return v
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

func agentOf(r *http.Request) string {
	v := strings.TrimSpace(r.Header.Get("User-Agent"))
	if v == "" {
		return ""
	}
	if utf8.RuneCountInString(v) > 80 {
		v = string([]rune(v)[:80])
	}
	return " · " + v
}

func jsonBody(w http.ResponseWriter, r *http.Request) bool {
	if ct, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type")); ct != "application/json" {
		http.Error(w, "asked as JSON", http.StatusUnsupportedMediaType)
		return false
	}
	return true
}

func reply(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func fail(w http.ResponseWriter, r *http.Request, err error) {
	log.From(r.Context()).ErrorContext(r.Context(), "cli: a sign-in failed", "err", err.Error())
	http.Error(w, "cannot just now", http.StatusServiceUnavailable)
}

// limiter is how many sign-ins one address may start in a while: a table
// nothing signed in can write to is a table anybody can fill.
type limiter struct {
	mu   sync.Mutex
	per  time.Duration
	max  int
	seen map[string][]time.Time
}

func (l *limiter) allow(who string, now time.Time) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	ts := l.seen[who][:0]
	for _, t := range l.seen[who] {
		if now.Sub(t) < l.per {
			ts = append(ts, t)
		}
	}
	if len(ts) >= l.max {
		l.seen[who] = ts
		return false
	}
	l.seen[who] = append(ts, now)
	if len(l.seen) > 10000 {
		// Forget everybody rather than grow without end.
		clear(l.seen)
	}
	return true
}
