package cmd_test

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/lesomnus/payday/auth/authsession"
	"github.com/lesomnus/payday/pdid"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	app "github.com/lesomnus/rove"
	entclilogin "github.com/lesomnus/rove/internal/ent/clilogin"
	"github.com/lesomnus/rove/server/pd"
)

// TestARowPutPastTheServersSignsNobodyIn: a session's key, a handoff's code
// and a terminal's are kept as an HMAC under a key the database does not hold
// (`session.KeyFrom`, of `app.signing_key`). So a row somebody who may only
// write the database puts there for a key of their own -- its SHA-256, as rows
// were kept before -- signs nobody in, and neither does a terminal's sign-in
// whose secret they rewrote before somebody approved it; a row the servers
// made does.
func TestARowPutPastTheServersSignsNobodyIn(t *testing.T) {
	w := newHosted(t)
	x := w.x
	ctx := context.Background()

	h := w.holder("kim", "member")
	account := w.accountOf(h)
	me := func(cookie string) codes.Code {
		_, err := app.NewPartyServiceClient(w.conn).Me(metadata.AppendToOutgoingContext(ctx, "cookie", cookie), &app.PartyMeRequest{})
		return status.Code(err)
	}
	row := func(key string, secret []byte) {
		now := time.Now()
		x.NoError(w.s.Ent.Session.Create().
			SetId(pdid.New(pd.SessionDomain).Uuid()).
			SetAccount(account.Uuid()).
			SetSecret(secret).
			SetGrant([]byte("whole")).
			SetDateIdle(now.Add(time.Hour)).
			SetDateExpires(now.Add(time.Hour)).
			SetDateCreated(now).
			SetDateUpdated(now).
			Exec(ctx))
	}

	// A session the server made.
	x.Equal(codes.OK, me(cookieOf(w.session(h))))
	_, c, err := w.s.Sessions.Mint(ctx, authsession.Session{Id: account.String()})
	x.NoError(err)
	sum := sha256.Sum256([]byte(c.Value))
	x.NotEqual(sum[:], w.rowOf(c).Secret, "kept as nothing a writer of the table could work out")

	// One put there past the servers, for a key its writer knows.
	known := "a key somebody who may write the database made up"
	known2 := sha256.Sum256([]byte(known))
	row(known, known2[:])
	x.Equal(codes.Unauthenticated, me(authsession.DefaultCookie+"="+known), "a row kept as the SHA-256 of a known key")

	// A handoff code put there the same way, for a round trip its writer
	// started on the domain -- and even with its state as the server keeps
	// it: the code alone is what it is taken by.
	w.domain(w.tenant, ownHost, "app", "active")
	parent := w.rowOf(cookieIn(w.session(h)))
	tr := w.start(ownHost, "/")
	tr.code = "a code somebody who may write the database made up"
	codeSum := sha256.Sum256([]byte(tr.code))
	tid, err := pdid.From(w.tenant.GetId())
	x.NoError(err)
	x.NoError(w.s.Ent.Handoff.Create().
		SetId(pdid.New(pd.HandoffDomain).Uuid()).
		SetSecret(codeSum[:]).
		SetAccount(account.Uuid()).
		SetWorkspace(tid.Uuid()).
		SetHost(ownHost).
		SetState(w.s.Handoff.Store.Kept(tr.value)).
		SetParent(parent.Id).
		SetDateExpires(time.Now().Add(time.Minute)).
		SetDateCreated(time.Now()).
		Exec(ctx))
	res := w.take(nil, ownHost, tr, tr.state)
	x.Equal("/?signin=failed", res.Header().Get("Location"), "a code kept as its SHA-256")
	x.Nil(setCookie(res, authsession.DefaultCookie))

	// A terminal's sign-in whose secret was rewritten before it was approved:
	// the session approving it makes is for no key the rewrite knew.
	ask := func(secret []byte) (id, code string) {
		body, err := json.Marshal(map[string]string{"secret": base64.RawURLEncoding.EncodeToString(secret)})
		x.NoError(err)
		r := httptest.NewRequest(http.MethodPost, "/cli/logins", strings.NewReader(string(body)))
		r.Host = serviceHost
		r.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		w.h.ServeHTTP(rec, r)
		x.Equal(http.StatusCreated, rec.Code, rec.Body.String())
		var v struct{ Id, Code string }
		x.NoError(json.Unmarshal(rec.Body.Bytes(), &v))
		return v.Id, v.Code
	}
	terminal := "a key a terminal made up"
	terminalSum := sha256.Sum256([]byte(terminal))
	_, code := ask(terminalSum[:])
	rewritten := sha256.Sum256([]byte(known))
	x.NoError(w.s.Ent.CliLogin.Update().Where(entclilogin.Code(code)).SetSecret(rewritten[:]).Exec(ctx))
	r := httptest.NewRequest(http.MethodPost, "/cli/logins/approve", strings.NewReader(`{"code":"`+code+`"}`))
	r.Host = serviceHost
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Cookie", cookieOf(w.session(h)))
	rec := httptest.NewRecorder()
	w.h.ServeHTTP(rec, r)
	x.Equal(http.StatusNoContent, rec.Code, rec.Body.String())
	x.Equal(codes.Unauthenticated, me(authsession.DefaultCookie+"="+known), "the key the rewrite named")
	x.Equal(codes.Unauthenticated, me(authsession.DefaultCookie+"="+terminal), "nor the terminal's, whose secret was rewritten")
}
