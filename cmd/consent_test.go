package cmd_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/lesomnus/payday/pdid"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	app "github.com/lesomnus/rove"
	"github.com/lesomnus/rove/cmd"
	"github.com/lesomnus/rove/internal/ent"
	entconsent "github.com/lesomnus/rove/internal/ent/consent"
	"github.com/lesomnus/rove/internal/ent/holder"
	"github.com/lesomnus/rove/internal/identity"
	"github.com/lesomnus/rove/server/account"
	"github.com/lesomnus/rove/server/consent"
)

var (
	terms   = cmd.DocumentConfig{Version: "2026-10-01", Url: "https://rove.example/terms"}
	privacy = cmd.DocumentConfig{Version: "2026-10-01", Url: "https://rove.example/privacy"}
)

func consentWorld(t *testing.T) *world {
	return newWorldWith(t, func(c *cmd.Config) {
		c.App.Consent.Terms = terms
		c.App.Consent.Privacy = privacy
	})
}

// TestNothingIsServedBeforeTheAccountAgrees: a deployment that asks for its
// terms and its privacy policy to be agreed to serves an account nothing until
// it has -- every call refused, a new workspace refused, though its own data
// it still takes out -- and then records each at its version, and asks again
// when one changes (design 9.11).
func TestNothingIsServedBeforeTheAccountAgrees(t *testing.T) {
	w := consentWorld(t)
	x := w.x
	ctx := context.Background()
	parties := app.NewPartyServiceClient(w.conn)
	tid, err := pdid.From(w.tenant.GetId())
	x.NoError(err)
	h, err := w.s.Ent.Holder.Query().Where(holder.TenantId(tid.Uuid()), holder.Role("owner")).Only(ctx)
	x.NoError(err)
	owner := pdid.Id(*h.Account)

	_, err = parties.Me(w.owner, &app.PartyMeRequest{})
	x.Equal(codes.FailedPrecondition, status.Code(err))
	x.Contains(status.Convert(err).Message(), "rove consent", "a terminal is told what to run")

	res := w.page(w.owner, http.MethodGet, "/account")
	x.Equal(http.StatusOK, res.Code)
	var acc account.Account
	x.NoError(json.Unmarshal(res.Body.Bytes(), &acc))
	x.Equal([]consent.Document{
		{Name: "terms", Title: "이용약관", Version: terms.Version, Url: terms.Url},
		{Name: "privacy", Title: "개인정보 처리방침", Version: privacy.Version, Url: privacy.Url},
	}, acc.Owes)
	x.Equal(http.StatusForbidden, w.post(w.owner, "/account/workspaces", `{"name":"하나 더"}`).Code)
	res = w.page(w.owner, http.MethodGet, "/account/workspaces/acme/export")
	x.Equal(http.StatusOK, res.Code, "what it has is its own, agreed or not")
	x.Equal("application/zip", res.Header().Get("Content-Type"))

	// One of the two, or another version than the one in force, is not an
	// agreement; nothing is recorded of either.
	x.Equal(http.StatusConflict, w.post(w.owner, "/account/consents", `{"agree":{"terms":"2026-10-01"}}`).Code)
	x.Equal(http.StatusConflict, w.post(w.owner, "/account/consents", `{"agree":{"terms":"2026-09-01","privacy":"2026-10-01"}}`).Code)
	n, err := w.s.Ent.Consent.Query().Count(ctx)
	x.NoError(err)
	x.Zero(n)
	_, err = parties.Me(w.owner, &app.PartyMeRequest{})
	x.Equal(codes.FailedPrecondition, status.Code(err))

	x.Equal(http.StatusNoContent, w.post(w.owner, "/account/consents", `{"agree":{"terms":"2026-10-01","privacy":"2026-10-01"}}`).Code)
	_, err = parties.Me(w.owner, &app.PartyMeRequest{})
	x.NoError(err, "served, once it agreed")
	x.Equal(http.StatusNoContent, w.post(w.owner, "/account/consents", `{"agree":{"terms":"2026-10-01","privacy":"2026-10-01"}}`).Code)
	rows, err := w.s.Ent.Consent.Query().Where(entconsent.Account(owner.Uuid())).All(ctx)
	x.NoError(err)
	x.Len(rows, 2, "agreed to once, each")
	for _, r := range rows {
		x.Equal("2026-10-01", r.Version)
		x.Equal("web", r.Client)
	}

	// A new version is agreed to again, and nothing is served until it is:
	// the documents change at a restart, which is a new [consent.Consents].
	*w.s.Consents = consent.Consents{Db: w.s.Ent, Documents: []consent.Document{
		{Name: "terms", Title: "이용약관", Version: "2027-01-01", Url: terms.Url},
		{Name: "privacy", Title: "개인정보 처리방침", Version: privacy.Version, Url: privacy.Url},
	}}
	_, err = parties.Me(w.owner, &app.PartyMeRequest{})
	x.Equal(codes.FailedPrecondition, status.Code(err))
	res = w.page(w.owner, http.MethodGet, "/account/consents")
	var got struct {
		Owed   []consent.Document
		Agreed []consent.Agreement
	}
	x.NoError(json.Unmarshal(res.Body.Bytes(), &got))
	x.Len(got.Owed, 1)
	x.Equal("2027-01-01", got.Owed[0].Version)
	x.Equal(http.StatusNoContent, w.post(w.owner, "/account/consents", `{"agree":{"terms":"2027-01-01"}}`).Code)
	_, err = parties.Me(w.owner, &app.PartyMeRequest{})
	x.NoError(err)
	res = w.page(w.owner, http.MethodGet, "/account/consents")
	x.NoError(json.Unmarshal(res.Body.Bytes(), &got))
	x.Empty(got.Owed)
	x.Len(got.Agreed, 3, "every version agreed to stays agreed to")
	x.Equal("2027-01-01", got.Agreed[0].Version, "newest first")
}

// TestAnAccountIsGivenItsWorkspaceWhenItAgrees: signing up is agreeing first.
// An account in no workspace that signs in owing an agreement is given no
// workspace yet -- nothing of it is written here before it agreed -- and is
// given its own when it agrees.
func TestAnAccountIsGivenItsWorkspaceWhenItAgrees(t *testing.T) {
	w := consentWorld(t)
	x := w.x
	ctx := context.Background()

	park, err := w.s.Identity.AddPerson(ctx, "park", "박", pdid.Nil)
	x.NoError(err)
	x.NoError(w.s.Identity.SetPassword(ctx, park.Id, "correct horse"))
	mux := http.NewServeMux()
	w.s.Routes(mux, w.c)
	r := httptest.NewRequest(http.MethodPost, "/session", strings.NewReader(`{"login":"park","password":"correct horse"}`))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, r)
	x.Equal(http.StatusNoContent, rec.Code)
	c := rec.Result().Cookies()[0]
	as := metadata.AppendToOutgoingContext(ctx, "cookie", c.Name+"="+c.Value)

	in := func() int {
		n, err := w.s.Ent.Holder.Query().Where(holder.Account(park.Id.Uuid())).Count(ctx)
		x.NoError(err)
		return n
	}
	x.Zero(in(), "a workspace for somebody who agreed to nothing")

	x.Equal(http.StatusNoContent, w.post(as, "/account/consents", `{"agree":{"terms":"2026-10-01","privacy":"2026-10-01"}}`).Code)
	x.Equal(1, in(), "its own, once it agreed")
	me, err := app.NewPartyServiceClient(w.conn).Me(as, &app.PartyMeRequest{})
	x.NoError(err)
	x.Equal("owner", me.GetRole())
}

// TestADocumentWithNowhereToReadItIsNotAsked: a version with no address it is
// read at is a deployment nobody could agree to, and does not start.
func TestADocumentWithNowhereToReadItIsNotAsked(t *testing.T) {
	x := require.New(t)
	_, err := cmd.ConsentConfig{Terms: cmd.DocumentConfig{Version: "1"}}.Documents()
	x.ErrorContains(err, "app.consent.terms")
	_, err = cmd.ConsentConfig{Privacy: cmd.DocumentConfig{Version: "1", Url: "privacy.html"}}.Documents()
	x.ErrorContains(err, "app.consent.privacy")
	docs, err := cmd.ConsentConfig{}.Documents()
	x.NoError(err)
	x.Empty(docs, "asking nothing is the default")
}

// TestAnAccountThatLeftKeepsWhatItAgreedToForTheWindow (rove#35): leaving
// says so on what an account agreed to, which still counts; the agreements
// stay for good while `app.consent.keep` says nothing, and once it says how
// long, an account's go that long after it left -- and nobody else's.
func TestAnAccountThatLeftKeepsWhatItAgreedToForTheWindow(t *testing.T) {
	w := consentWorld(t)
	x := w.x
	ctx := context.Background()

	now := time.Now()
	w.s.Consents.Now = func() time.Time { return now }
	t.Cleanup(func() { w.s.Consents.Now = nil })

	agreed := map[string]string{"terms": terms.Version, "privacy": privacy.Version}
	person := func(login string) identity.Person {
		p, err := w.s.Identity.AddPerson(ctx, login, login, pdid.Nil)
		x.NoError(err)
		x.NoError(w.s.Consents.Agree(ctx, p.Id, agreed, "web"))
		w.here(p, "member")
		return p
	}
	rows := func(p identity.Person) []*ent.Consent {
		vs, err := w.s.Ent.Consent.Query().Where(entconsent.Account(p.Id.Uuid())).All(ctx)
		x.NoError(err)
		return vs
	}
	kim, lee, park := person("kim"), person("lee"), person("park")

	// kim leaves now, and lee eleven months on.
	_, err := w.s.Lifecycle.Depart(ctx, kim.Id)
	x.NoError(err)
	for _, v := range rows(kim) {
		x.NotNil(v.DateLeft, "leaving did not say so on what it agreed to")
	}
	ok, err := w.s.Consents.Settled(ctx, kim.Id)
	x.NoError(err)
	x.True(ok, "what it agreed to stopped counting as it left")

	n, err := w.s.Consents.Sweep(ctx, 0)
	x.NoError(err)
	x.Zero(n, "app.consent.keep says nothing, and agreements went")

	now = now.Add(335 * 24 * time.Hour)
	_, err = w.s.Lifecycle.Depart(ctx, lee.Id)
	x.NoError(err)

	// A year after kim left, kept for ninety days.
	now = now.Add(30 * 24 * time.Hour)
	n, err = w.s.Consents.Sweep(ctx, 90*24*time.Hour)
	x.NoError(err)
	x.Equal(2, n, "kim's two")
	x.Empty(rows(kim))
	x.Len(rows(lee), 2, "lee left thirty days ago")
	x.Len(rows(park), 2, "park is here")
	for _, v := range rows(park) {
		x.Nil(v.DateLeft)
	}

	// And kim, were it to come back, is asked again.
	owed, err := w.s.Consents.Owed(ctx, kim.Id)
	x.NoError(err)
	x.Len(owed, 2)
}
