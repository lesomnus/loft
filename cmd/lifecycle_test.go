package cmd_test

import (
	"context"
	"testing"
	"time"
	"uuid"

	"github.com/lesomnus/payday/pdid"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"

	app "github.com/lesomnus/rove"
	"github.com/lesomnus/rove/internal/ent"
	"github.com/lesomnus/rove/internal/ent/holder"
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

// TestAnAccountStillGoneAfterTheGraceLeaves: once the grace is over, roster is
// asked again about an account whose memberships are still leaving. One it has
// again was restored, and stops leaving; one whose grace is not over waits;
// and one roster still has nobody by leaves every workspace it is in -- its
// person record pseudonymized, its memberships ended, and a workspace it was
// the last owner of handed to whoever is next.
func TestAnAccountStillGoneAfterTheGraceLeaves(t *testing.T) {
	w := newWorld(t)
	x := w.x
	ctx := context.Background()
	beta, err := w.s.Base.Tenant().Add(ctx, app.TenantAddRequest_builder{Alias: "beta"}.Build())
	x.NoError(err)
	kim, err := w.s.Identity.AddPerson(ctx, "kim", "Kim", pdid.Nil)
	x.NoError(err)
	lee, err := w.s.Identity.AddPerson(ctx, "lee", "Lee", pdid.Nil)
	x.NoError(err)
	here := w.here(kim, "member")
	there := w.member(beta, kim, "owner")
	next := w.member(beta, lee, "admin")
	// And a workspace kim was asked into and never joined: kim opened the
	// link, which made the invitation the account's, and did not answer.
	gamma, err := w.s.Base.Tenant().Add(ctx, app.TenantAddRequest_builder{Alias: "gamma"}.Build())
	x.NoError(err)
	asked, err := w.s.Base.Invitation().Add(ctx, app.InvitationAddRequest_builder{
		Tenant:      app.TenantRef_builder{Id: gamma.GetId()}.Build(),
		Inviter:     app.HolderRef_builder{Id: w.member(gamma, lee, "owner").GetId()}.Build(),
		Email:       "kim@acme.example",
		Role:        "member",
		State:       "pending",
		Account:     kim.Id.Bytes(),
		DateExpires: timestamppb.New(time.Now().Add(24 * time.Hour)),
	}.Build())
	x.NoError(err)
	person, err := w.s.Base.Party().Add(ctx, app.PartyAddRequest_builder{
		Tenant: app.TenantRef_builder{Id: w.tenant.GetId()}.Build(),
		Name:   "김민수", Kind: "person", Email: "kim@acme.example",
		Holder: app.HolderRef_builder{Id: here.GetId()}.Build(),
	}.Build())
	x.NoError(err)

	row := func(h *app.Holder) *ent.Holder {
		id, err := pdid.From(h.GetId())
		x.NoError(err)
		v, err := w.s.Ent.Holder.Get(ctx, id.Uuid())
		x.NoError(err)
		return v
	}
	erased := func(at time.Time) {
		// Restored first, so that the mark is from `at`.
		x.NoError(w.s.Lifecycle.Apply(ctx, identity.Standing{Holder: kim.Id}))
		x.NoError(w.s.Lifecycle.Apply(ctx, identity.Standing{Holder: kim.Id, Erased: at}))
	}
	past := time.Now().Add(-w.s.Lifecycle.Grace - time.Hour)

	// roster has kim: restored, whatever the mark says.
	erased(past)
	x.NoError(w.s.Lifecycle.Sweep(ctx))
	x.Nil(row(here).DateLeaving, "restored at roster, and heard by asking")
	x.Nil(row(there).DateLeaving)

	// Gone at roster, and the grace not over.
	x.NoError(w.s.Identity.ForgetPerson(ctx, kim.Id))
	erased(time.Now().Add(-time.Hour))
	x.NoError(w.s.Lifecycle.Sweep(ctx))
	x.Nil(row(here).DateErased, "waits out the grace")
	x.NotNil(row(here).DateLeaving)

	// Gone, and the grace over -- but a roster that cannot be asked decides
	// nothing.
	erased(past)
	nowhere, err := identity.Open(ctx, identity.Config{Addr: "127.0.0.1:1", Insecure: true, Key: "rk_nobody", Directory: "nowhere"}, "", nil)
	x.NoError(err)
	t.Cleanup(func() { nowhere.Close() })
	unasked := w.s.Lifecycle
	unasked.People = nowhere
	x.NoError(unasked.Sweep(ctx))
	x.Nil(row(here).DateErased, "roster could not say kim is still gone")

	x.NoError(w.s.Lifecycle.Sweep(ctx))
	for _, h := range []*app.Holder{here, there} {
		v := row(h)
		x.NotNil(v.DateErased, "every workspace it was in")
		x.Empty(v.Alias)
	}
	p, err := w.s.Base.Party().Get(ctx, app.PartyGetRequest_builder{Ref: app.PartyRef_builder{Id: person.GetId()}.Build()}.Build())
	x.NoError(err)
	x.NotEqual("김민수", p.GetName())
	x.Empty(p.GetEmail())
	x.Equal("owner", row(next).Role, "beta's last owner left, and it went to who was next")
	acme, err := pdid.From(w.tenant.GetId())
	x.NoError(err)
	owners, err := w.s.Ent.Holder.Query().Where(holder.TenantId(acme.Uuid()), holder.Role("owner"), holder.DateErasedIsNil()).Count(ctx)
	x.NoError(err)
	x.Equal(1, owners, "and acme, which had an owner of its own, is as it was")

	inv, err := w.s.Ent.Invitation.Get(ctx, idUuid(asked.GetId()))
	x.NoError(err)
	x.Empty(inv.Email, "the address it was asked at, in a workspace it never joined")
	x.Equal("revoked", inv.State)

	// Nothing more to do the next time.
	x.NoError(w.s.Lifecycle.Sweep(ctx))
}

func idUuid(b []byte) uuid.UUID {
	v, _ := pdid.From(b)
	return v.Uuid()
}
