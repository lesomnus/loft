// Package session keeps browser sessions in the database, and signs somebody
// in once roster has checked their password (design 9.10).
//
// A session row is bookkeeping, not a fact anybody audits: it is written
// through ent directly, outside the trail, the way payday's own stores are.
// The trail still sees who did what, because every call a session makes is
// recorded with its holder.
//
// The session is Loft's, so that it can be ended at once (report 3); the
// person is roster's, so every call a session makes is held to what roster
// says of them now: suspended, erased or signed out everywhere since it began
// is a session that is over.
//
// A session names a **member** (D85): a holder of the roster tenant this
// deployment serves, which is their membership of its workspace by the same
// identifier, made the first time they sign in (`tenancy.Members`). Every call
// is served as it (`cmd.Resolver`).
package session

import (
	"bytes"
	"context"
	"crypto/hkdf"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"mime"
	"net/http"
	"strings"
	"time"

	"github.com/lesomnus/otx/log"
	"github.com/lesomnus/payday/auth"
	"github.com/lesomnus/payday/auth/authsession"
	"github.com/lesomnus/payday/frame"
	"github.com/lesomnus/payday/pdid"
	"github.com/lesomnus/payday/pdpb"
	"google.golang.org/protobuf/proto"

	"github.com/lesomnus/loft/internal/ent"
	"github.com/lesomnus/loft/internal/ent/holder"
	"github.com/lesomnus/loft/internal/ent/party"
	"github.com/lesomnus/loft/internal/ent/session"
	"github.com/lesomnus/loft/internal/ent/signinfailure"
	"github.com/lesomnus/loft/internal/identity"
	"github.com/lesomnus/loft/server/clientaddr"
	"github.com/lesomnus/loft/server/pd"
	"github.com/lesomnus/loft/server/tenancy"
)

// Store is an [authsession.Store] over the Session table. The key is never
// written down: the row holds what it is kept as ([Store.Kept]) -- an HMAC of
// its SHA-256 under a key the database does not hold -- which is what a cookie
// is looked up by. So a copy of the table signs nobody in, and neither does a
// row put in it past the servers by whoever may write it: what a key of their
// own is kept as is nothing they can work out without that key. Sessions are
// no act on the trail, and this is what keeps the table from being a way in.
type Store struct {
	Db *ent.Client

	// Key is what keys are kept under ([KeyFrom]); every process of a
	// deployment holds the same, or a key one kept is none to the next.
	Key []byte

	// People is what a session's person is held to on every call; nil holds
	// them to nothing, which only a test of the table itself wants.
	People Held
}

// Held is roster's word on whether a session that began at `began` is still
// good: [identity.Store.Held].
type Held interface {
	Held(ctx context.Context, member pdid.Id, began time.Time) error
}

var _ authsession.Store = Store{}

// KeyFrom is the key sessions and terminals' sign-ins are kept under, made
// from `app.signing_key` (HKDF, with a label of its own): a key
// every process of a deployment holds, and no database does -- the one place
// the trail's adversary is not. The signing key signs download links as well,
// and the label is what keeps what one is used for from saying anything of the
// other. Rotating it signs everybody out.
func KeyFrom(signing []byte) []byte {
	k, err := hkdf.Key(sha256.New, signing, nil, "loft: what a session's key is kept as", sha256.Size)
	if err != nil {
		panic(err)
	}
	return k
}

// Kept is what a session's key is kept and looked up as: the HMAC, under
// [Store.Key], of its SHA-256.
func (s Store) Kept(key string) []byte {
	sum := sha256.Sum256([]byte(key))
	return s.KeptSum(sum[:])
}

// KeptSum is [Store.Kept] of a key whose SHA-256 is all that was sent: a
// terminal's, which keeps the key and sends only that (`server/clilogin`).
func (s Store) KeptSum(sum []byte) []byte {
	m := hmac.New(sha256.New, s.Key)
	m.Write(sum)
	return m.Sum(nil)
}

// whole is the one grant a password gives: whatever the person may do.
var whole = []byte("whole")

// Grant is what a session's row keeps of what it is narrowed to: [whole] for
// none, which is what a password and a terminal that asked for nothing get,
// or payday's Grant listing the methods it may call (`loft auth login --allow`,
// D33). Every tenant and every set: a terminal is narrowed by what it may do,
// and where is its member's.
func Grant(actions []string) ([]byte, error) {
	if len(actions) == 0 {
		return whole, nil
	}
	v, err := auth.Introspection(auth.Identity{Grant: frame.Whole().To(actions...)})
	if err != nil {
		return nil, err
	}
	return proto.Marshal(v.GetGrant())
}

// Actions is the methods a row's grant is narrowed to, as somebody reads
// them: nil for one that is narrowed to none.
func Actions(b []byte) ([]string, error) {
	g, err := grantOf(b)
	if err != nil || g.AnyAction() {
		return nil, err
	}
	return g.Actions(), nil
}

// grantOf is what a row's grant allows, read back as payday reads one off a
// token: an attenuation of whatever the member may do, which the gRPC chain
// holds every call to. A row says [whole] for none: every sign-in writes
// one or the other.
func grantOf(b []byte) (frame.Grant, error) {
	if bytes.Equal(b, whole) {
		return frame.Whole(), nil
	}
	g := &pdpb.Grant{}
	if err := proto.Unmarshal(b, g); err != nil {
		return frame.Grant{}, err
	}
	id, err := auth.IdentityFrom(pdpb.TokenIntrospectResponse_builder{Grant: g}.Build())
	if err != nil {
		return frame.Grant{}, err
	}
	return id.Grant, nil
}

func (s Store) Put(ctx context.Context, v authsession.Session) error {
	member, err := pdid.Parse(v.Id)
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
		Where(session.Secret(s.Kept(v.Key)), session.DateErasedIsNil()).
		SetNillableDateIdle(nonzero(v.Idle)).
		SetNillableDateExpires(nonzero(v.Expires)).
		SetDateUpdated(now).
		Save(ctx)
	if err != nil || n > 0 {
		return err
	}

	create := s.Db.Session.Create().
		SetId(pdid.New(pd.SessionDomain).Uuid()).
		SetAccount(member.Uuid()).
		SetSecret(s.Kept(v.Key)).
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
	v, err := s.Db.Session.Query().Where(session.Secret(s.Kept(key)), session.DateErasedIsNil()).Only(ctx)
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
				log.From(ctx).WarnContext(ctx, "session: ending one roster refused", slog.Any("err", err))
			}
			return authsession.Session{}, authsession.ErrNoSession
		case err != nil:
			// Not known, which is not the same as over: the cookie stays,
			// and the call is refused until roster can be asked.
			return authsession.Session{}, fmt.Errorf("%w: %w", auth.ErrUnavailable, err)
		}
	}
	// No tenant: the member's row says it, and the resolver reads that.
	g, err := grantOf(v.Grant)
	if err != nil {
		return authsession.Session{}, fmt.Errorf("session: what it allows: %w", err)
	}
	out := authsession.Session{
		Key:   key,
		Id:    pdid.Id(v.Account).String(),
		Grant: g,
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
	_, err := s.Db.Session.Delete().Where(session.Secret(s.Kept(key))).Exec(ctx)
	return err
}

// Adopt makes a session whose key somebody else holds: a terminal that made
// one up and sent only its SHA-256 (`server/clilogin`), which is kept as
// `kept` ([Store.KeptSum]). It is the row [Store.Put] writes, for a member,
// beginning now and good until `idle` and `expires`, narrowed to `grant`
// ([Grant]; empty is [whole]), and the key is never seen here.
func (s Store) Adopt(ctx context.Context, member pdid.Id, kept, grant []byte, idle, expires time.Time) error {
	if len(kept) != sha256.Size {
		return errors.New("session: a key is kept as an HMAC-SHA-256")
	}
	if len(grant) == 0 {
		grant = whole
	}
	if _, err := grantOf(grant); err != nil {
		return fmt.Errorf("session: what it allows: %w", err)
	}
	now := time.Now()
	return s.Db.Session.Create().
		SetId(pdid.New(pd.SessionDomain).Uuid()).
		SetAccount(member.Uuid()).
		SetSecret(kept).
		SetGrant(grant).
		SetNillableDateIdle(nonzero(idle)).
		SetNillableDateExpires(nonzero(expires)).
		SetDateCreated(now).
		SetDateUpdated(now).
		Exec(ctx)
}

// End deletes a member's sessions that began before `before`, or all of them
// when it is zero: what roster said of them voids them, and a row left would
// be a cookie answered with no until it ran out.
func (s Store) End(ctx context.Context, member pdid.Id, before time.Time) (int, error) {
	q := s.Db.Session.Delete().Where(session.Account(member.Uuid()))
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
// `login` is somebody's alias in the tenant at roster, or the address they
// are reached at: on the roster in this process the one on a person record
// here, and on a roster of its own one it verified.
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

// Members is who somebody roster vouched for is here: their membership, made
// the first time they sign in ([tenancy.Members.Of]) -- [tenancy.ErrEnded]
// for one ended here, [tenancy.ErrNoWorkspace] before `loft init`.
type Members interface {
	Of(ctx context.Context, p identity.Person) (*ent.Holder, error)
}

// SignIn is `POST /session` and `DELETE /session`: a password roster checked,
// the membership it is here, then Loft's session cookie for it.
//
// A refusal is one answer whatever was wrong -- nobody by that name, the wrong
// password, an address that keeps getting it wrong, somebody roster has
// locked -- and a roster that cannot be asked is another, since telling
// somebody their password is wrong when nobody could check it is the answer
// that sends them to reset it.
func SignIn(sessions *authsession.Sessions, people People, db *ent.Client, members Members) http.Handler {
	failures := Failures{Db: db}
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

		// Asked as JSON, which a page of another site cannot do without this
		// one agreeing to it first (CORS). A form can post anything else --
		// `text/plain` whose body happens to be JSON -- and a sign-in it
		// could post is somebody's browser signed in as a member of the
		// form's choosing, who then keeps whatever they put in (loft#40).
		if ct, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type")); ct != "application/json" {
			http.Error(w, "asked as JSON", http.StatusUnsupportedMediaType)
			return
		}

		ctx := r.Context()
		var in Login
		if err := json.NewDecoder(http.MaxBytesReader(nil, r.Body, 4<<10)).Decode(&in); err != nil {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		in.Login = strings.TrimSpace(in.Login)
		if in.Login == "" || in.Password == "" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		who := Subject(clientaddr.Of(r), in.Login)
		switch ok, err := failures.Ok(ctx, who); {
		case err != nil:
			log.From(ctx).WarnContext(ctx, "session: a sign-in whose failures could not be counted", slog.Any("err", err))
			http.Error(w, "cannot sign in just now", http.StatusServiceUnavailable)
			return
		case !ok:
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}

		p, err := verify(ctx, people, db, in)
		switch {
		case errors.Is(err, errNo):
			if err := failures.Fail(ctx, who); err != nil {
				log.From(ctx).WarnContext(ctx, "session: a failure that could not be counted", slog.Any("err", err))
			}
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		case err != nil:
			log.From(ctx).WarnContext(ctx, "session: a sign-in nobody could check", slog.Any("err", err))
			http.Error(w, "cannot sign in just now", http.StatusServiceUnavailable)
			return
		}
		if err := failures.Forget(ctx, who); err != nil {
			log.From(ctx).WarnContext(ctx, "session: failures that could not be forgotten", slog.Any("err", err))
		}

		h, err := members.Of(ctx, p)
		switch {
		case errors.Is(err, tenancy.ErrEnded):
			http.Error(w, "this login was ended here", http.StatusForbidden)
			return
		case err != nil:
			log.From(ctx).ErrorContext(ctx, "session: somebody roster vouched for is no member here", slog.Any("err", err))
			http.Error(w, "cannot sign in just now", http.StatusServiceUnavailable)
			return
		}

		_, c, err := sessions.Mint(ctx, authsession.Session{Id: pdid.Id(h.Id).String(), Grant: frame.Whole()})
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
		member, err := memberAt(ctx, db, login)
		if err != nil {
			return identity.Person{}, err
		}
		if member != pdid.Nil {
			p, err := people.ById(ctx, member.String())
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

// memberAt is the one member whose person record here has this address, and
// nil for nobody or for more than one.
func memberAt(ctx context.Context, db *ent.Client, address string) (pdid.Id, error) {
	vs, err := db.Party.Query().
		Where(
			party.EmailEqualFold(address),
			party.DateErasedIsNil(),
			party.HasHolderWith(holder.DateErasedIsNil()),
		).
		WithHolder().
		All(ctx)
	if err != nil {
		return pdid.Nil, err
	}
	var member pdid.Id
	for _, v := range vs {
		h := pdid.Id(v.Edges.Holder.Id)
		if member != pdid.Nil && member != h {
			return pdid.Nil, nil
		}
		member = h
	}

	return member, nil
}

// Failures is how many wrong answers a pair -- the address a sign-in came from
// and the name it was for -- gave lately: [MaxFailures] in [Window], and the
// pair is refused without roster being asked, until the oldest of them is
// older than that. Signing in forgets them.
//
// They are rows (`SignInFailure`) and not a count in memory, so that every
// process of a deployment holds a pair to the same ones: counted per process,
// a deployment of N processes was N times as easy to guess at, by whoever's
// requests the balancer spread. A pair is kept as its SHA-256 ([Subject]), so
// the table says how often and not who.
//
// The count is read before roster is asked and written after it answers, so
// requests that arrive together may each be the tenth. That is a limit held
// to within the requests in flight, which is what it was in one process too.
//
// Roster's own lockout is the person's -- ten wrong in a row, whatever they
// came from -- and this is the address's, which only Loft sees: roster is
// asked by Loft, and every request reaches it from here.
type Failures struct {
	Db *ent.Client

	// Now is the clock; tests move it.
	Now func() time.Time
}

const (
	// MaxFailures is how many wrong answers a pair may give in [Window].
	MaxFailures = 10
	// Window is how long one is held against it.
	Window = 15 * time.Minute
)

// Subject is what a failure is counted under: the address and the name the
// form was given, folded, since a name is not told apart by its case, and
// hashed.
func Subject(address, login string) []byte {
	v := sha256.Sum256([]byte(address + "\x00" + strings.ToLower(login)))
	return v[:]
}

func (f Failures) now() time.Time {
	if f.Now != nil {
		return f.Now()
	}
	return time.Now()
}

// Ok answers whether a pair may be asked about: fewer than [MaxFailures] in
// the last [Window].
func (f Failures) Ok(ctx context.Context, subject []byte) (bool, error) {
	n, err := f.Db.SignInFailure.Query().
		Where(signinfailure.Subject(subject), signinfailure.DateCreatedGT(f.now().Add(-Window))).
		Count(ctx)
	if err != nil {
		return false, err
	}
	return n < MaxFailures, nil
}

// Fail counts one wrong answer against a pair.
func (f Failures) Fail(ctx context.Context, subject []byte) error {
	return f.Db.SignInFailure.Create().
		SetId(pdid.New(pd.SignInFailureDomain).Uuid()).
		SetSubject(subject).
		SetDateCreated(f.now()).
		Exec(ctx)
}

// Forget is a pair signing in: what it got wrong before is not held against
// it any more.
func (f Failures) Forget(ctx context.Context, subject []byte) error {
	_, err := f.Db.SignInFailure.Delete().Where(signinfailure.Subject(subject)).Exec(ctx)
	return err
}

// Sweep deletes the failures nothing counts any more: those older than
// [Window].
func (f Failures) Sweep(ctx context.Context) error {
	_, err := f.Db.SignInFailure.Delete().Where(signinfailure.DateCreatedLTE(f.now().Add(-Window))).Exec(ctx)
	return err
}
