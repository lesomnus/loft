package cmd_test

import (
	"context"
	"testing"
	"time"

	"github.com/lesomnus/payday/pdid"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	app "github.com/lesomnus/rove"
	entsession "github.com/lesomnus/rove/internal/ent/session"
	"github.com/lesomnus/rove/internal/identity"
)

// What roster says of an account is handed to [cmd.Server.Lifecycle] here as
// the stream from a roster of its own hands it (`identity.Store.Follow`, whose
// own test is the stream): the roster in this process has none.

// TestRostersWordEndsTheSessionsHere: an account roster signed out everywhere,
// suspended or erased has its sessions here ended then, rather than at each
// one's next call.
func TestRostersWordEndsTheSessionsHere(t *testing.T) {
	w := newWorld(t)
	x := w.x
	ctx := context.Background()
	me := app.NewPartyServiceClient(w.conn)
	kim := w.holder("kim", "member")
	account := w.accountOf(kim)
	sessions := func() int {
		n, err := w.s.Ent.Session.Query().Where(entsession.Account(account.Uuid())).Count(ctx)
		x.NoError(err)
		return n
	}

	before := w.signIn(kim)
	time.Sleep(20 * time.Millisecond)
	cut := time.Now()
	time.Sleep(20 * time.Millisecond)
	after := w.signIn(kim)
	x.Equal(2, sessions())

	// Signed out everywhere: what began before it, and nothing after.
	x.NoError(w.s.Lifecycle.Apply(ctx, identity.Standing{Holder: account, Invalidated: cut}))
	x.Equal(1, sessions(), "the row went, not only the next call")
	_, err := me.Me(before, &app.PartyMeRequest{})
	x.Equal(codes.Unauthenticated, status.Code(err))
	_, err = me.Me(after, &app.PartyMeRequest{})
	x.NoError(err)

	// Suspended: all of them.
	x.NoError(w.s.Lifecycle.Apply(ctx, identity.Standing{Holder: account, Invalidated: cut, Disabled: time.Now()}))
	x.Zero(sessions())
	_, err = me.Me(after, &app.PartyMeRequest{})
	x.Equal(codes.Unauthenticated, status.Code(err))

	// And an event says the state, so the same one again is no different.
	x.NoError(w.s.Lifecycle.Apply(ctx, identity.Standing{Holder: account, Invalidated: cut, Disabled: time.Now()}))
	x.Zero(sessions())

	_, err = me.Me(w.owner, &app.PartyMeRequest{})
	x.NoError(err, "nobody else's")
}

// TestAnErasedAccountBeginsToLeave: an account roster erased leaves every
// workspace it is a member of, from when roster erased it -- and one roster
// restored stops leaving, which is what roster's grace is for.
func TestAnErasedAccountBeginsToLeave(t *testing.T) {
	w := newWorld(t)
	x := w.x
	ctx := context.Background()
	beta, err := w.s.Base.Tenant().Add(ctx, app.TenantAddRequest_builder{Alias: "beta"}.Build())
	x.NoError(err)
	kim, err := w.s.Identity.AddPerson(ctx, "kim", "Kim", pdid.Nil)
	x.NoError(err)
	here := w.here(kim, "member")
	there := w.member(beta, kim, "admin")
	lee := w.holder("lee", "member")
	leaving := func(h *app.Holder) *time.Time {
		id, err := pdid.From(h.GetId())
		x.NoError(err)
		v, err := w.s.Ent.Holder.Get(ctx, id.Uuid())
		x.NoError(err)
		return v.DateLeaving
	}

	session := w.signIn(here)
	erased := time.Now().Add(-time.Hour).Truncate(time.Second)
	x.NoError(w.s.Lifecycle.Apply(ctx, identity.Standing{Holder: kim.Id, Erased: erased}))
	for _, h := range []*app.Holder{here, there} {
		require.NotNil(t, leaving(h), "in every workspace it is in")
		x.True(leaving(h).Equal(erased), "from when roster erased it, and not from when this heard")
	}
	x.Nil(leaving(lee), "and nobody else")
	_, err = app.NewPartyServiceClient(w.conn).Me(session, &app.PartyMeRequest{})
	x.Equal(codes.Unauthenticated, status.Code(err), "signed in nowhere")

	// Heard again, later: it began to leave when it began.
	x.NoError(w.s.Lifecycle.Apply(ctx, identity.Standing{Holder: kim.Id, Erased: time.Now()}))
	x.True(leaving(here).Equal(erased))

	// What a membership ended here already is not the account's to change.
	_, err = w.s.Base.Holder().Erase(ctx, app.HolderRef_builder{Id: there.GetId()}.Build())
	x.NoError(err)

	// Restored.
	x.NoError(w.s.Lifecycle.Apply(ctx, identity.Standing{Holder: kim.Id}))
	x.Nil(leaving(here))
	x.NotNil(leaving(there), "an ended membership is left as it ended")

	// What the people screen reads is the same.
	v, err := app.NewHolderServiceClient(w.conn).Get(w.owner, app.HolderGetRequest_builder{
		Ref: app.HolderRef_builder{Id: here.GetId()}.Build(),
	}.Build())
	x.NoError(err)
	x.False(v.HasDateLeaving())
}
