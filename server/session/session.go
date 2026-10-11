// Package session keeps browser sessions in the database, and signs an account
// in once roster has checked its password (design 9.10).
//
// A session row is bookkeeping, not a fact anybody audits: it is written
// through ent directly, outside the trail, the way payday's own stores are.
// The trail still sees who did what, because every call a session makes is
// recorded with its holder.
//
// The session is Rove's, so that it can be ended at once (report 3); the
// account is roster's, so every call a session makes is held to what roster
// says of it now: suspended, erased or signed out everywhere since it began is
// a session that is over.
//
// A session names an **account** and not a membership (D27). Which workspace a
// call is about is the call's to say, and `cmd.Resolver` turns the account and
// that workspace into the holder the call is served as.
package session

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/lesomnus/otx/log"
	"github.com/lesomnus/payday/auth"
	"github.com/lesomnus/payday/auth/authsession"
	"github.com/lesomnus/payday/frame"
	"github.com/lesomnus/payday/pdid"

	"github.com/lesomnus/rove/internal/ent"
	"github.com/lesomnus/rove/internal/ent/holder"
	"github.com/lesomnus/rove/internal/ent/party"
	"github.com/lesomnus/rove/internal/ent/session"
	"github.com/lesomnus/rove/internal/identity"
	"github.com/lesomnus/rove/server/pd"
)

// Store is an [authsession.Store] over the Session table. The key is never
// written down: the row holds its SHA-256, which is what a cookie is looked up
// by, so a copy of the table signs nobody in.
type Store struct {
	Db *ent.Client

	// People is what a session's person is held to on every call; nil holds
	// them to nothing, which only a test of the table itself wants.
	People Held
}

// Held is roster's word on whether a session that began at `began` is still
// good: [identity.Store.Held].
type Held interface {
	Held(ctx context.Context, account pdid.Id, began time.Time) error
}

var _ authsession.Store = Store{}

func digest(key string) []byte {
	v := sha256.Sum256([]byte(key))
	return v[:]
}

// whole is the one grant a password gives: whatever the person may do.
var whole = []byte("whole")

func (s Store) Put(ctx context.Context, v authsession.Session) error {
	account, err := pdid.Parse(v.Id)
	if err != nil {
		return err
	}
	// What the sign-in kept beside the session: the issuer's ID token, for
	// signing out (`server/sso`). Written once, when the row is.
	var held []byte
	if len(v.Held) > 0 {
		if held, err = json.Marshal(v.Held); err != nil {
			return err
		}
	}

	// Put is also how the idle clock moves: the same key, again.
	now := time.Now()
	n, err := s.Db.Session.Update().
		Where(session.Secret(digest(v.Key)), session.DateErasedIsNil()).
		SetNillableDateIdle(nonzero(v.Idle)).
		SetNillableDateExpires(nonzero(v.Expires)).
		SetDateUpdated(now).
		Save(ctx)
	if err != nil || n > 0 {
		return err
	}

	create := s.Db.Session.Create().
		SetId(pdid.New(pd.SessionDomain).Uuid()).
		SetAccount(account.Uuid()).
		SetSecret(digest(v.Key)).
		SetGrant(whole).
		SetNillableDateIdle(nonzero(v.Idle)).
		SetNillableDateExpires(nonzero(v.Expires)).
		SetDateCreated(now).
		SetDateUpdated(now)
	if held != nil {
		create.SetHeld(held)
	}
	return create.Exec(ctx)
}

func (s Store) Get(ctx context.Context, key string) (authsession.Session, error) {
	v, err := s.Db.Session.Query().Where(session.Secret(digest(key)), session.DateErasedIsNil()).Only(ctx)
	if err != nil {
		if ent.IsNotFound(err) {
			return authsession.Session{}, authsession.ErrNoSession
		}
		return authsession.Session{}, err
	}
	if s.People != nil {
		switch err := s.People.Held(ctx, pdid.Id(v.Account), v.DateCreated); {
		case errors.Is(err, identity.ErrRefused):
			// Over: what roster says of them since it began ends it here too.
			if _, err := s.Db.Session.Delete().Where(session.Id(v.Id)).Exec(ctx); err != nil {
				log.From(ctx).WarnContext(ctx, "session: ending one roster refused", "err", err.Error())
			}
			return authsession.Session{}, authsession.ErrNoSession
		case err != nil:
			// Not known, which is not the same as over: the cookie stays,
			// and the call is refused until roster can be asked.
			return authsession.Session{}, fmt.Errorf("%w: %w", auth.ErrUnavailable, err)
		}
	}
	// No tenant: an account is in none, and which workspace a call is about
	// is the call's to say.
	out := authsession.Session{
		Key:   key,
		Id:    pdid.Id(v.Account).String(),
		Grant: frame.Whole(),
	}
	if v.DateExpires != nil {
		out.Expires = *v.DateExpires
	}
	if v.DateIdle != nil {
		out.Idle = *v.DateIdle
	}
	if v.Held != nil && len(*v.Held) > 0 {
		if err := json.Unmarshal(*v.Held, &out.Held); err != nil {
			return authsession.Session{}, fmt.Errorf("session: what it held: %w", err)
		}
	}
	return out, nil
}

func (s Store) Del(ctx context.Context, key string) error {
	_, err := s.Db.Session.Delete().Where(session.Secret(digest(key))).Exec(ctx)
	return err
}

// End deletes an account's sessions that began before `before`, or all of
// them when it is zero: what roster said of the account voids them, and a row
// left would be a cookie answered with no until it ran out.
func (s Store) End(ctx context.Context, account pdid.Id, before time.Time) (int, error) {
	q := s.Db.Session.Delete().Where(session.Account(account.Uuid()))
	if !before.IsZero() {
		q = q.Where(session.DateCreatedLT(before))
	}
	return q.Exec(ctx)
}

// Sweep deletes sessions that are over, for the table not to keep every
// sign-in there ever was.
func (s Store) Sweep(ctx context.Context) error {
	now := time.Now()
	_, err := s.Db.Session.Delete().Where(session.Or(
		session.DateExpiresLT(now),
		session.DateIdleLT(now),
	)).Exec(ctx)
	return err
}

func nonzero(t time.Time) *time.Time {
	if t.IsZero() {
		return nil
	}
	return &t
}

// Login is what a sign-in form posts:
//
//	{"login": "kim", "password": "..."}
//
// `login` is an account's alias in the directory, or the address it is
// reached at: on the roster in this process the one on a person record here,
// and on a roster of its own one it verified. Which workspace is not said:
// a session is the account's, and every workspace it belongs to is open to it.
type Login struct {
	Login    string `json:"login"`
	Password string `json:"password"`
}

// People is what a sign-in is checked with: roster.
type People interface {
	Verify(ctx context.Context, login, password string) (identity.Person, error)
	ById(ctx context.Context, sub string) (identity.Person, error)
	Embedded() bool
}

// Arrive is what an account signing in is given, once roster vouched for it:
// a workspace of its own when it is in none ([tenancy.Workspaces.Arrive]).
type Arrive func(ctx context.Context, p identity.Person) error

// SignIn is `POST /session` and `DELETE /session`: a password roster checked,
// what the account is given on arriving, then Rove's session cookie for it.
//
// A refusal is one answer whatever was wrong -- nobody by that name, the wrong
// password, an address that keeps getting it wrong -- and a roster that cannot
// be asked is another, since telling somebody their password is wrong when
// nobody could check it is the answer that sends them to reset it.
func SignIn(sessions *authsession.Sessions, people People, db *ent.Client, arrive Arrive) http.Handler {
	l := &limiter{seen: map[string][]time.Time{}}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodPost:
		case http.MethodDelete:
			http.SetCookie(w, sessions.End(r.Context(), sessions.KeyOf(r.Header.Values("Cookie"))))
			w.WriteHeader(http.StatusNoContent)
			return
		default:
			w.Header().Set("Allow", "POST, DELETE")
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}

		ctx := r.Context()
		var in Login
		if err := json.NewDecoder(http.MaxBytesReader(nil, r.Body, 4<<10)).Decode(&in); err != nil {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		in.Login = strings.TrimSpace(in.Login)
		who := addr(r) + "\x00" + strings.ToLower(in.Login)
		if in.Login == "" || in.Password == "" || !l.ok(who) {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}

		p, err := verify(ctx, people, db, in)
		switch {
		case errors.Is(err, errNo):
			l.fail(who)
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		case err != nil:
			log.From(ctx).WarnContext(ctx, "session: a sign-in nobody could check", "err", err.Error())
			http.Error(w, "cannot sign in just now", http.StatusServiceUnavailable)
			return
		}
		l.forget(who)

		// A workspace that could not be made is not a sign-in refused: the
		// account is who it is, and is told it is in no workspace.
		if arrive != nil {
			if err := arrive(ctx, p); err != nil {
				log.From(ctx).ErrorContext(ctx, "session: an account arrived and was not given what it is given", "err", err.Error())
			}
		}

		_, c, err := sessions.Mint(ctx, authsession.Session{Id: p.Id.String(), Grant: frame.Whole()})
		if err != nil {
			http.Error(w, "cannot sign in just now", http.StatusServiceUnavailable)
			return
		}
		http.SetCookie(w, c)
		w.WriteHeader(http.StatusNoContent)
	})
}

// errNo is a refusal: everything a sign-in can be told no for.
var errNo = errors.New("session: no")

func verify(ctx context.Context, people People, db *ent.Client, in Login) (identity.Person, error) {
	login := in.Login
	if strings.Contains(login, "@") && people.Embedded() {
		// The roster in this process keeps no addresses: the one a person is
		// reached at is on their person record here, as it was before roster
		// held them. An address nobody here has goes to roster as it is,
		// which refuses it the way it refuses anybody.
		account, err := accountAt(ctx, db, login)
		if err != nil {
			return identity.Person{}, err
		}
		if account != pdid.Nil {
			p, err := people.ById(ctx, account.String())
			switch {
			case err == nil:
				login = p.Alias
			case !errors.Is(err, identity.ErrNoPerson):
				return identity.Person{}, err
			}
		}
	}

	p, err := people.Verify(ctx, login, in.Password)
	switch {
	case errors.Is(err, identity.ErrRefused), errors.Is(err, identity.ErrSecondFactor),
		errors.Is(err, identity.ErrNoTenant), errors.Is(err, identity.ErrNoPerson):
		return identity.Person{}, errNo
	case err != nil:
		return identity.Person{}, err
	}

	return p, nil
}

// accountAt is the one account whose person records here have this address, in
// any workspace, and nil for nobody or for more than one.
func accountAt(ctx context.Context, db *ent.Client, address string) (pdid.Id, error) {
	vs, err := db.Party.Query().
		Where(
			party.EmailEqualFold(address),
			party.DateErasedIsNil(),
			party.HasHolderWith(holder.DateErasedIsNil(), holder.AccountNotNil()),
		).
		WithHolder().
		All(ctx)
	if err != nil {
		return pdid.Nil, err
	}
	var account pdid.Id
	for _, v := range vs {
		a := pdid.Id(*v.Edges.Holder.Account)
		if account != pdid.Nil && account != a {
			return pdid.Nil, nil
		}
		account = a
	}

	return account, nil
}

func addr(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// limiter refuses a login from an address after ten failures in fifteen
// minutes. It is per process, which is what one replica needs.
type limiter struct {
	mu   sync.Mutex
	seen map[string][]time.Time
}

const (
	maxFailures = 10
	window      = 15 * time.Minute
)

func (l *limiter) ok(k string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	cut := time.Now().Add(-window)
	vs := l.seen[k]
	for len(vs) > 0 && vs[0].Before(cut) {
		vs = vs[1:]
	}
	l.seen[k] = vs
	if len(vs) == 0 {
		delete(l.seen, k)
	}
	return len(vs) < maxFailures
}

func (l *limiter) fail(k string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.seen[k] = append(l.seen[k], time.Now())
}

func (l *limiter) forget(k string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.seen, k)
}
