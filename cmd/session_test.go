package cmd_test

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/lesomnus/payday/pdid"
	"github.com/lesomnus/z"
	"google.golang.org/grpc/metadata"

	app "github.com/lesomnus/loft"
	"github.com/lesomnus/loft/cmd"
	"github.com/lesomnus/loft/internal/identity"
	"github.com/lesomnus/loft/server/session"
)

// paged is the roster in this process with an account page, which only a
// roster of its own has (`auth.roster.account_url`).
type paged struct {
	*identity.Store
	page string
}

func (p paged) AccountPage() string { return p.page }

// TestAMemberIsToldWhereTheyManageHowTheySignIn: with a roster of its own,
// the page where somebody changes how they sign in -- and deletes themselves,
// where the tenant offers that (roster#106) -- is roster's, and what the
// workspace shows of the person says where.
func TestAMemberIsToldWhereTheyManageHowTheySignIn(t *testing.T) {
	w := newWorld(t)
	x := w.x
	parties := app.NewPartyServiceClient(w.conn)

	me, err := parties.Me(w.owner, &app.PartyMeRequest{})
	x.NoError(err)
	x.Empty(me.GetAccountUrl(), "the roster in this process has no page of its own")

	w.s.Deps.People = paged{w.s.Identity, "https://id.example/"}
	me, err = parties.Me(w.owner, &app.PartyMeRequest{})
	x.NoError(err)
	x.Equal("https://id.example/", me.GetAccountUrl())
}

// TestASessionIsToldWhoItIsAndItsWorkspace (D85): `GET /session` says who a
// session is -- the member, by the identifier they have at roster too -- what
// it is narrowed to, and the workspace the deployment serves, in the zone its
// dates are drawn in: its own, or the deployment's while it says none. A
// closed workspace answers no RPC, and this says what the page draws of it.
func TestASessionIsToldWhoItIsAndItsWorkspace(t *testing.T) {
	w := newWorld(t)
	x := w.x
	read := func(ctx context.Context) session.Answer {
		t.Helper()
		rec := w.page(ctx, http.MethodGet, "/session")
		x.Equal(http.StatusOK, rec.Code, rec.Body.String())
		var got session.Answer
		x.NoError(json.Unmarshal(rec.Body.Bytes(), &got))
		return got
	}

	got := read(w.owner)
	owner := w.ownerHolder()
	x.Equal(idString(owner.GetId()), got.Id, "the member, which is the holder at roster")
	x.Equal("owner", got.Login)
	x.NotEmpty(got.Identity)
	x.Empty(got.Allow)
	x.False(got.Billing)
	x.Equal(idString(w.tenant.GetId()), got.Workspace.Id)
	x.Equal("acme", got.Workspace.Alias)
	x.Equal("acme", got.Workspace.Name, "called by its address while it keeps no name")
	x.Equal("Asia/Seoul", got.Workspace.Timezone, "the deployment's, while it says none of its own")
	x.Nil(got.Workspace.Closed)

	_, err := app.NewTenantServiceClient(w.conn).Configure(w.owner, app.TenantConfigureRequest_builder{
		Ref:      app.TenantRef_builder{Id: w.tenant.GetId()}.Build(),
		Timezone: z.Ptr("Europe/Paris"),
		Name:     z.Ptr("연구소"),
	}.Build())
	x.NoError(err)
	got = read(w.owner)
	x.Equal("Europe/Paris", got.Workspace.Timezone, "its own")
	x.Equal("연구소", got.Workspace.Name)

	// Closed, every call is refused, and this says what the page shows.
	closed := w.closeIt("")
	_, err = app.NewPartyServiceClient(w.conn).Me(w.owner, &app.PartyMeRequest{})
	w.refusedClosed(err, "a closed workspace answers no call")
	got = read(w.owner)
	x.NotNil(got.Workspace.Closed)
	x.WithinDuration(closed.GetDateClosed().AsTime(), *got.Workspace.Closed, time.Second)
	x.NotNil(got.Workspace.Destroy)

	t.Run("one the deployment names", func(t *testing.T) {
		w := newWorldWith(t, func(c *cmd.Config) { c.App.Locale.Timezone = "America/Los_Angeles" })
		rec := w.page(w.owner, http.MethodGet, "/session")
		w.x.Equal(http.StatusOK, rec.Code)
		var got session.Answer
		w.x.NoError(json.Unmarshal(rec.Body.Bytes(), &got))
		w.x.Equal("America/Los_Angeles", got.Workspace.Timezone)
	})
}

// TestASessionNarrowedIsToldWhatItMayCall: a terminal's sign-in narrowed to
// some methods (D33) is told what they are, and nobody is told anything
// without a session, or of a membership ended since.
func TestASessionNarrowedIsToldWhatItMayCall(t *testing.T) {
	w := newWorld(t)
	x := w.x
	kim := w.holder("kim", "member")
	// A terminal's session, which kept its key and sent its SHA-256, as
	// `loft auth login --allow` makes one.
	key := "a-terminal-key-of-a-good-length-0123456789"
	sum := sha256.Sum256([]byte(key))
	g, err := session.Grant([]string{"/loft.AssetService/Get"})
	x.NoError(err)
	l := w.s.Logins
	x.NoError(l.Store.Adopt(t.Context(), w.memberId(kim), l.Store.KeptSum(sum[:]), g, time.Now().Add(time.Hour), time.Now().Add(time.Hour)))
	narrowed := metadata.AppendToOutgoingContext(t.Context(), "cookie", l.Cookie+"="+key)
	rec := w.page(narrowed, http.MethodGet, "/session")
	x.Equal(http.StatusOK, rec.Code, rec.Body.String())
	var got session.Answer
	x.NoError(json.Unmarshal(rec.Body.Bytes(), &got))
	x.Equal([]string{"/loft.AssetService/Get"}, got.Allow)
	x.Equal("kim", got.Login)

	x.Equal(http.StatusUnauthorized, w.page(t.Context(), http.MethodGet, "/session").Code, "nobody signed in")

	id, err := pdid.From(kim.GetId())
	x.NoError(err)
	x.NoError(w.s.Ent.Holder.UpdateOneId(id.Uuid()).SetDateErased(time.Now()).Exec(t.Context()))
	x.Equal(http.StatusUnauthorized, w.page(narrowed, http.MethodGet, "/session").Code, "a membership ended here is nobody's session")
}
