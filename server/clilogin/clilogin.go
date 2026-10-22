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
// A terminal may ask for less than its account may do: the methods it lists
// (`rove login --allow`, D33), which the page shows and the session is
// narrowed to -- an assistant handed that terminal can do that much and no
// more, whatever it does with the key.
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
	"log/slog"
	"mime"
	"net/http"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/lesomnus/otx/log"
	"github.com/lesomnus/payday/audit"
	"github.com/lesomnus/payday/auth/authsession"
	"github.com/lesomnus/payday/frame"
	"github.com/lesomnus/payday/pdid"

	"github.com/lesomnus/rove/internal/ent"
	entclilogin "github.com/lesomnus/rove/internal/ent/clilogin"
	"github.com/lesomnus/rove/internal/ent/predicate"
	entsession "github.com/lesomnus/rove/internal/ent/session"
	"github.com/lesomnus/rove/server/account"
	"github.com/lesomnus/rove/server/clientaddr"
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

// The most sign-ins one address may start in a while: a table nothing signed
// in can write to is a table anybody can fill.
//
// Counted from the rows themselves (`CliLogin.address`), so every process of a
// deployment counts the same ones; a count in each one's memory was a limit
// per process. A sign-in that was answered and told so is gone and no longer
// counts, which is only ever a terminal of somebody's own: one nobody approves
// stays until it expires, and the window is as long as that.
const (
	PerAddress    = 20
	AddressWindow = Ttl
)

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

	// Audit is what the trail keeps, as the recorder is given it: a terminal
	// asking, and a person answering it, are each put on the trail as the act
	// they are (payday#108, `trail: {recorded: {}}` on CliLogin). The row goes
	// once the terminal has its answer, and the trail is what is left of who
	// let which terminal in.
	Audit audit.Contents
}

// The acts a terminal's sign-in is on the trail as.
const (
	Asked    = "rove.cli-login.ask"
	Approved = "rove.cli-login.approve"
	Denied   = "rove.cli-login.deny"

	// Told is the row gone once the terminal had its answer, and Swept one
	// gone because nobody will ask about it again.
	Told  = "rove.cli-login.told"
	Swept = "rove.cli-login.sweep"
)

// recorded is a write of a sign-in and the act on the trail that says so, in
// one transaction.
func (l *Logins) recorded(ctx context.Context, act string, write func(*ent.Client) (*ent.CliLogin, error)) (*ent.CliLogin, error) {
	tx, err := l.Db.Tx(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	v, err := write(tx.Client())
	if err != nil {
		return nil, err
	}
	if _, err := pd.Record(ctx, tx.Client(), l.Audit, act, []pdid.Id{pdid.Id(v.Id)}); err != nil {
		return nil, err
	}
	return v, tx.Commit()
}

// removed deletes the sign-ins `where` reads and puts each on the trail as
// gone, under `act`, in one transaction (payday#115): a row removed past the
// servers is otherwise a thing the trail says is a row and is not.
func (l *Logins) removed(ctx context.Context, act string, where ...predicate.CliLogin) (int, error) {
	tx, err := l.Db.Tx(ctx)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()

	ids, err := tx.CliLogin.Query().Where(where...).Ids(ctx)
	if err != nil || len(ids) == 0 {
		return 0, err
	}
	n, err := tx.CliLogin.Delete().Where(entclilogin.IdIn(ids...)).Exec(ctx)
	if err != nil {
		return 0, err
	}
	gone := make([]pdid.Id, len(ids))
	for i, v := range ids {
		gone[i] = pdid.Id(v)
	}
	if _, err := pd.RecordErased(ctx, tx.Client(), l.Audit, act, gone); err != nil {
		return 0, err
	}
	return n, tx.Commit()
}

// Mount puts the routes on a mux.
func (l *Logins) Mount(mux *http.ServeMux) {
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
	// Allow is the methods it asked to be narrowed to; absent is all that
	// the account may do.
	Allow []string `json:"allow,omitempty"`
}

// The most methods one terminal may list, and how long one may be: a grant
// is read on every call.
const (
	MaxAllow    = 64
	MaxAllowLen = 200
)

// method is what payday reads as a method or a pattern of them
// (`frame.Covers`): `/package.Service/Method`, any part of which may be `*`.
var method = regexp.MustCompile(`^/(\*|[A-Za-z_][A-Za-z0-9_]*(\.[A-Za-z_][A-Za-z0-9_]*)*)\.(\*|[A-Za-z_][A-Za-z0-9_]*)/(\*|[A-Za-z_][A-Za-z0-9_]*)$`)

// allowed is what a terminal asked to be narrowed to, as a session's grant
// keeps it, or an error saying what about it is not one.
func allowed(vs []string) ([]byte, error) {
	if len(vs) > MaxAllow {
		return nil, errors.New("allow: at most 64 methods")
	}
	out := make([]string, 0, len(vs))
	for _, v := range vs {
		v = strings.TrimSpace(v)
		if len(v) > MaxAllowLen || !method.MatchString(v) {
			return nil, errors.New("allow: " + v + " is not a method, nor a pattern of them: /rove.AssetService/Search, /rove.ReservationService/*, /rove.*/Get")
		}
		out = append(out, v)
	}
	return session.Grant(out)
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
	origin := clientaddr.Of(r)
	switch ok, err := l.allow(r.Context(), origin); {
	case err != nil:
		fail(w, r, err)
		return
	case !ok:
		http.Error(w, "too many sign-ins asked from here; wait a little", http.StatusTooManyRequests)
		return
	}
	var in struct {
		Secret string   `json:"secret"`
		Client string   `json:"client"`
		Allow  []string `json:"allow"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 32<<10)).Decode(&in); err != nil {
		http.Error(w, `a terminal asks as {"secret": ..., "client": ..., "allow": [...]}`, http.StatusBadRequest)
		return
	}
	grant, err := allowed(in.Allow)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
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
	// Asked by nobody signed in: the terminal is who the row says it is.
	v, err := l.recorded(ctx, Asked, func(db *ent.Client) (*ent.CliLogin, error) {
		return db.CliLogin.Create().
			SetId(pdid.New(pd.CliLoginDomain).Uuid()).
			SetSecret(secret).
			SetCode(code).
			SetClient(client).
			SetOrigin(origin + agentOf(r)).
			SetAddress(origin).
			SetGrant(grant).
			SetState(pending).
			SetDateExpires(now.Add(Ttl)).
			SetDateCreated(now).
			SetDateUpdated(now).
			Save(ctx)
	})
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
		if _, err := l.removed(ctx, Told, entclilogin.Id(v.Id)); err != nil {
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
	allow, err := session.Actions(v.Grant)
	if err != nil {
		fail(w, r, err)
		return
	}
	reply(w, http.StatusOK, Asking{
		Code: v.Code, Client: v.Client, Origin: v.Origin,
		Created: v.DateCreated, Expires: v.DateExpires,
		Allow: allow,
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
		act := Denied
		if to == approved {
			act = Approved
		}
		// Answered by the account signed in at the page, which is who the
		// trail says answered.
		as := frame.Into(ctx, frame.New(p.Id, pdid.Nil, frame.Grant{}))
		_, err := l.recorded(as, act, func(db *ent.Client) (*ent.CliLogin, error) {
			u := db.CliLogin.UpdateOneId(v.Id).
				Where(entclilogin.State(pending)).
				SetState(to).
				SetDateUpdated(now)
			if to == approved {
				u = u.SetAccount(p.Id.Uuid())
			}
			return u.Save(ctx)
		})
		if err != nil {
			if ent.IsNotFound(err) {
				http.Error(w, "that sign-in was answered already", http.StatusConflict)
				return
			}
			fail(w, r, err)
			return
		}
		if to == approved {
			if err := l.Store.Adopt(ctx, p.Id, v.Secret, v.Grant, now.Add(l.Idle), now.Add(l.Lifetime)); err != nil {
				fail(w, r, err)
				return
			}
			allow, _ := session.Actions(v.Grant)
			log.From(ctx).InfoContext(ctx, "cli: a terminal was signed in", "account", p.Id.String(), "client", v.Client, "origin", v.Origin, "allow", strings.Join(allow, " "))
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
// a while past it -- each on the trail as gone.
func (l *Logins) Sweep(ctx context.Context) error {
	_, err := l.removed(ctx, Swept, entclilogin.DateExpiresLT(l.now().Add(-time.Hour)))
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
	log.From(r.Context()).ErrorContext(r.Context(), "cli: a sign-in failed", slog.Any("err", err))
	http.Error(w, "cannot just now", http.StatusServiceUnavailable)
}

// allow answers whether an address may start another sign-in: fewer than
// [PerAddress] started from it in the last [AddressWindow].
func (l *Logins) allow(ctx context.Context, address string) (bool, error) {
	n, err := l.Db.CliLogin.Query().
		Where(entclilogin.Address(address), entclilogin.DateCreatedGT(l.now().Add(-AddressWindow))).
		Count(ctx)
	if err != nil {
		return false, err
	}
	return n < PerAddress, nil
}
