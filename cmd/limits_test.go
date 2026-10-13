package cmd_test

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/lesomnus/payday/pdid"
	"github.com/stretchr/testify/require"

	"github.com/lesomnus/rove/cmd"
	"github.com/lesomnus/rove/internal/ent/signinfailure"
	"github.com/lesomnus/rove/internal/identity"
	"github.com/lesomnus/rove/server/clilogin"
	"github.com/lesomnus/rove/server/session"
)

// asked is roster, counting how often the sign-in form asks it: a sign-in the
// limit refuses is one roster is never asked about.
type asked struct {
	session.People

	mu     sync.Mutex
	n      int
	locked bool
}

func (a *asked) Verify(ctx context.Context, login, password string) (identity.Person, error) {
	p, err := a.People.Verify(ctx, login, password)
	a.mu.Lock()
	defer a.mu.Unlock()
	a.n++
	a.locked = errors.As(err, &identity.Locked{})
	return p, err
}

func (a *asked) count() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.n
}

// another is a second process of the world's deployment: its own server, on
// the same database and the same roster's.
func (w *world) another(t *testing.T) *cmd.Server {
	s, err := cmd.Build(context.Background(), w.c)
	w.x.NoError(err)
	t.Cleanup(func() { s.Close() })
	return s
}

// signInAt posts a sign-in to `h` as a browser at `addr` would.
func signInAt(h http.Handler, addr, login, password string) int {
	b, _ := json.Marshal(session.Login{Login: login, Password: password})
	r := httptest.NewRequest(http.MethodPost, "/session", bytes.NewReader(b))
	r.Header.Set("Content-Type", "application/json")
	r.RemoteAddr = addr + ":1234"
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, r)
	return rec.Code
}

// TestEveryProcessCountsTheSameSignInFailures: the failures a pair -- an
// address and the name it signs in as -- gives are rows, so a deployment of
// two processes holds the pair to ten whichever process each guess reached.
// Counted in each one's memory it was ten per process.
func TestEveryProcessCountsTheSameSignInFailures(t *testing.T) {
	w := newWorld(t)
	x := w.x
	ctx := context.Background()
	other := w.another(t)

	ra, rb := &asked{People: w.s.Identity}, &asked{People: other.Identity}
	a := session.SignIn(w.s.Sessions, ra, w.s.Ent, nil)
	b := session.SignIn(other.Sessions, rb, other.Ent, nil)
	both := func() int { return ra.count() + rb.count() }

	// Nobody by this name, so that roster has no account to lock and what
	// refuses is the limit alone.
	for i := range session.MaxFailures {
		h := a
		if i%2 == 1 {
			h = b
		}
		x.Equal(http.StatusUnauthorized, signInAt(h, "10.0.0.9", "ghost", "a guess"))
	}
	x.Equal(session.MaxFailures, both())

	x.Equal(http.StatusUnauthorized, signInAt(a, "10.0.0.9", "ghost", "a guess"))
	x.Equal(http.StatusUnauthorized, signInAt(b, "10.0.0.9", "GHOST", "a guess"))
	x.Equal(session.MaxFailures, both(), "roster was asked past the limit, by one process or the other")

	t.Run("and another address, or another name, is its own pair", func(t *testing.T) {
		x := require.New(t)

		n := both()
		x.Equal(http.StatusUnauthorized, signInAt(b, "10.0.0.10", "ghost", "a guess"))
		x.Equal(http.StatusUnauthorized, signInAt(a, "10.0.0.9", "phantom", "a guess"))
		x.Equal(n+2, both())
	})

	t.Run("and signing in forgets them, in every process", func(t *testing.T) {
		x := require.New(t)

		park, err := w.s.Identity.AddPerson(ctx, "park", "박", pdid.Nil)
		x.NoError(err)
		x.NoError(w.s.Identity.SetPassword(ctx, park.Id, "correct horse"))

		// One short of the limit, and one short of roster's lockout.
		for i := range session.MaxFailures - 1 {
			h := a
			if i%2 == 1 {
				h = b
			}
			x.Equal(http.StatusUnauthorized, signInAt(h, "10.0.0.11", "park", "a guess"))
		}
		x.Equal(http.StatusNoContent, signInAt(b, "10.0.0.11", "park", "correct horse"))

		n, err := w.s.Ent.SignInFailure.Query().
			Where(signinfailure.Subject(session.Subject("10.0.0.11", "park"))).
			Count(ctx)
		x.NoError(err)
		x.Zero(n, "what the pair got wrong before it signed in is still held against it")

		asked := both()
		x.Equal(http.StatusUnauthorized, signInAt(a, "10.0.0.11", "park", "a guess"))
		x.Equal(asked+1, both())
	})
}

// TestALockedAccountIsRefusedAsAWrongAnswerIs: roster's lockout is the
// account's -- ten wrong in a row, from wherever -- and the form says no to it
// the way it says no to anything, rather than telling a guesser the account
// exists, or answering that nobody could check.
func TestALockedAccountIsRefusedAsAWrongAnswerIs(t *testing.T) {
	w := newWorld(t)
	x := w.x
	ctx := context.Background()

	lee, err := w.s.Identity.AddPerson(ctx, "lee", "이", pdid.Nil)
	x.NoError(err)
	x.NoError(w.s.Identity.SetPassword(ctx, lee.Id, "correct horse"))

	r := &asked{People: w.s.Identity}
	h := session.SignIn(w.s.Sessions, r, w.s.Ent, nil)

	// From a new address every time, so that Rove's limit on a pair never
	// comes into it and what locks is roster.
	for i := range 2 * session.MaxFailures {
		if r.locked {
			break
		}
		x.Equal(http.StatusUnauthorized, signInAt(h, "10.1.0."+strconv.Itoa(i), "lee", "a guess"))
	}
	x.True(r.locked, "roster never locked the account")

	x.Equal(http.StatusUnauthorized, signInAt(h, "10.2.0.1", "lee", "correct horse"))
	x.True(r.locked, "the right password was let past roster's lock")
}

// TestTheFailuresSweepTakesOnlyOldOnes: a failure older than the window is
// counted by nothing and swept, and one inside it is counted and kept.
func TestTheFailuresSweepTakesOnlyOldOnes(t *testing.T) {
	w := newWorld(t)
	x := w.x
	ctx := context.Background()

	now := time.Now()
	at := now
	f := session.Failures{Db: w.s.Ent, Now: func() time.Time { return at }}
	who := session.Subject("10.0.0.12", "kim")

	for _, ago := range []time.Duration{
		session.Window + 5*time.Minute,
		session.Window + time.Second,
		session.Window,
		session.Window - time.Second,
		time.Minute,
	} {
		at = now.Add(-ago)
		x.NoError(f.Fail(ctx, who))
	}
	other := session.Subject("10.0.0.13", "kim")
	at = now.Add(-session.Window - time.Minute)
	x.NoError(f.Fail(ctx, other))

	at = now
	count := func(s []byte) int {
		n, err := w.s.Ent.SignInFailure.Query().Where(signinfailure.Subject(s)).Count(ctx)
		x.NoError(err)
		return n
	}
	x.Equal(5, count(who))

	x.NoError(f.Sweep(ctx))
	x.Equal(2, count(who), "the sweep took one the window still holds, or left one it does not")
	x.Zero(count(other))

	t.Run("and the limit counts the window and nothing older", func(t *testing.T) {
		x := require.New(t)

		old := session.Subject("10.0.0.14", "kim")
		at = now.Add(-session.Window - time.Minute)
		for range session.MaxFailures {
			x.NoError(f.Fail(ctx, old))
		}
		at = now
		ok, err := f.Ok(ctx, old)
		x.NoError(err)
		x.True(ok, "failures older than the window held a pair off")

		for range session.MaxFailures - 1 {
			x.NoError(f.Fail(ctx, old))
		}
		ok, err = f.Ok(ctx, old)
		x.NoError(err)
		x.True(ok, "refused short of the limit")

		x.NoError(f.Fail(ctx, old))
		ok, err = f.Ok(ctx, old)
		x.NoError(err)
		x.False(ok, "the limit in the window let one more past")
	})
}

// TestEveryProcessCountsTheSameCliLogins: how many terminal sign-ins one
// address may start is counted from the rows, so a deployment of two
// processes holds an address to twenty in the window whichever one each
// request reached.
func TestEveryProcessCountsTheSameCliLogins(t *testing.T) {
	w := newWorld(t)
	x := w.x
	other := w.another(t)

	a, b := http.NewServeMux(), http.NewServeMux()
	w.s.Logins.Mount(a)
	other.Logins.Mount(b)
	start := func(h http.Handler, addr string) int {
		key := make([]byte, 32)
		_, _ = rand.Read(key)
		body, _ := json.Marshal(map[string]string{"secret": base64.RawURLEncoding.EncodeToString(key), "client": "a test"})
		r := httptest.NewRequest(http.MethodPost, "/cli/logins", bytes.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		r.RemoteAddr = addr + ":1234"
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, r)
		return rec.Code
	}

	for i := range clilogin.PerAddress {
		h := a
		if i%2 == 1 {
			h = b
		}
		x.Equal(http.StatusCreated, start(h, "10.0.0.20"))
	}
	x.Equal(http.StatusTooManyRequests, start(a, "10.0.0.20"))
	x.Equal(http.StatusTooManyRequests, start(b, "10.0.0.20"), "the other process let the address start more")
	x.Equal(http.StatusCreated, start(b, "10.0.0.21"), "another address was held to this one's")

	t.Run("and what started before the window is not counted", func(t *testing.T) {
		x := require.New(t)

		later := time.Now().Add(clilogin.AddressWindow + time.Minute)
		w.s.Logins.Now = func() time.Time { return later }
		t.Cleanup(func() { w.s.Logins.Now = nil })
		x.Equal(http.StatusCreated, start(a, "10.0.0.20"))
	})
}
