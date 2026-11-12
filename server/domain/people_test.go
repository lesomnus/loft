package domain_test

import (
	"context"
	"strings"
	"testing"

	"github.com/lesomnus/payday/auth/authsession"
	"github.com/lesomnus/payday/frame"
	"github.com/lesomnus/payday/pdid"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	app "github.com/lesomnus/loft"
	"github.com/lesomnus/loft/internal/ent/audit"
	"github.com/lesomnus/loft/internal/ent/event"
	"github.com/lesomnus/loft/internal/ent/holder"
	"github.com/lesomnus/loft/internal/ent/party"
	"github.com/lesomnus/loft/internal/ent/session"
	"github.com/lesomnus/loft/internal/identity"
	"github.com/lesomnus/loft/server/pd"
	"github.com/lesomnus/loft/server/tenancy"
)

// signsIn is whether `login` gets in at the roster with `password`.
func (e *env) signsIn(login, password string) bool {
	_, err := e.s.Identity.Verify(context.Background(), login, password)
	if err != nil {
		e.x.ErrorIs(err, identity.ErrRefused, "not a refusal: %v", err)
	}
	return err == nil
}

// TestAPersonForgottenHereEndsTheMembership, and not their holder at roster:
// who is a member, and how they sign in, is roster's (D87), and that is the
// tenant's administrator's there to end.
func TestAPersonForgottenHereEndsTheMembership(t *testing.T) {
	e := newEnv(t)
	x := e.x
	_, kim := e.person("member")
	who := idOf(kim.GetHolder().GetId())

	_, err := e.app().Party().Pseudonymize(e.owner, app.PartyPseudonymizeRequest_builder{Ref: ref[app.PartyRef](kim.GetId())}.Build())
	x.NoError(err)

	live, err := e.s.Ent.Holder.Query().Where(holder.Id(who.Uuid()), holder.DateErasedIsNil()).Exist(context.Background())
	x.NoError(err)
	x.False(live, "the membership outlived the person")
	x.True(e.signsIn("p1", "password1234"), "the holder at roster went with the membership")
}

// TestAMemberEndedHereIsOneAgainWhileRosterHasThem (D88): who is a member is
// roster's, so somebody forgotten here whom roster still has is a member
// again the next time they sign in -- the same membership, called as roster
// calls them, with a person record found or made as for anybody new; the
// record forgotten stays so. What they may call is what roster binds them,
// which Loft neither gave nor takes away (D89). What they were signed in with
// before is gone, and the trail says the row was taken up again.
func TestAMemberEndedHereIsOneAgainWhileRosterHasThem(t *testing.T) {
	e := newEnv(t)
	x := e.x
	ctx := context.Background()
	_, kim := e.person("manager")
	who := idOf(kim.GetHolder().GetId())
	_, _, err := e.s.Sessions.Mint(ctx, authsession.Session{Id: who.String(), Grant: frame.Whole()})
	x.NoError(err)

	_, err = e.app().Party().Pseudonymize(e.owner, app.PartyPseudonymizeRequest_builder{Ref: ref[app.PartyRef](kim.GetId())}.Build())
	x.NoError(err)

	p, err := e.s.Identity.Verify(ctx, "p1", "password1234")
	x.NoError(err)
	h, err := e.s.Members.Of(ctx, p)
	x.NoError(err)
	x.Equal(who.Uuid(), h.Id, "a membership of its own")
	x.Nil(h.DateErased)
	x.Equal("p1", h.Alias)
	x.Equal("manager 1", h.Name)
	x.NoError(e.s.Identity.Reach().May(ctx, who, app.ReservationService_Approve_FullMethodName), "what they may call is what roster binds them, as it was (D89)")

	rec, err := e.s.Ent.Party.Query().Where(party.HolderId(who.Uuid()), party.DateErasedIsNil()).Only(ctx)
	x.NoError(err)
	x.NotEqual(idUUID(kim.GetId()), rec.Id, "the record forgotten is somebody's again")
	x.Equal("manager 1", rec.Name)
	old, err := e.s.Ent.Party.Get(ctx, idUUID(kim.GetId()))
	x.NoError(err)
	x.True(strings.HasPrefix(old.Name, "익명 "), old.Name)

	n, err := e.s.Ent.Session.Query().Where(session.Holder(who.Uuid())).Count(ctx)
	x.NoError(err)
	x.Zero(n, "a session from before the membership ended")
	n, err = e.s.Ent.Audit.Query().Where(audit.ObjectId(who.Uuid()), audit.Action(tenancy.Readmitted)).Count(ctx)
	x.NoError(err)
	x.Equal(1, n, "the trail says nothing of the row coming back")

	// And signing in again is the membership it is now.
	again, err := e.s.Members.Of(ctx, p)
	x.NoError(err)
	x.Equal(h.Id, again.Id)
}

// TestAMemberIsLinkedToTheirRecordByHand (D88): a person record that no
// membership is takes one up by hand -- from the record it was, which keeps
// its history and is nobody's login from then -- and lets it go again, the
// membership then no record's until one is linked to it, which signing in
// does not do. Each is an event, said of the record and the membership.
func TestAMemberIsLinkedToTheirRecordByHand(t *testing.T) {
	e := newEnv(t)
	x := e.x
	ctx := context.Background()
	_, kim := e.person("member")
	member := kim.GetHolder().GetId()
	called := "@" + e.holderRow(member).Alias
	right, err := e.app().Party().Add(e.owner, app.PartyAddRequest_builder{Name: "김철수", Kind: "person", Email: "kim@example.com"}.Build())
	x.NoError(err)
	hold := app.HolderRef_builder{Id: member}.Build()

	v, err := e.app().Party().LinkMember(e.owner, app.PartyLinkMemberRequest_builder{Ref: ref[app.PartyRef](right.GetId()), Holder: hold}.Build())
	x.NoError(err)
	x.Equal(member, v.GetHolder().GetId())
	was, err := e.app().Party().Get(e.owner, app.PartyGetRequest_builder{Ref: ref[app.PartyRef](kim.GetId())}.Build())
	x.NoError(err)
	x.False(was.HasHolder(), "a membership is one record at a time")
	x.Equal("구성원 연결: 김철수 ("+called+", "+kim.GetName()+"에서 옮김)", e.events()["party.link"])
	ev, err := e.s.Ent.Event.Query().Where(event.Kind("party.link")).Only(ctx)
	x.NoError(err)
	x.Equal(kim.GetId(), value[*app.PartyLinked](e, ev).GetWas())

	t.Run("and not to what it cannot be", func(t *testing.T) {
		x := require.New(t)
		team, err := e.app().Party().Add(e.owner, app.PartyAddRequest_builder{Name: "연구팀", Kind: "team"}.Build())
		x.NoError(err)
		_, lee := e.person("member")
		forgotten, err := e.app().Party().Add(e.owner, app.PartyAddRequest_builder{Name: "박지민", Kind: "person"}.Build())
		x.NoError(err)
		_, err = e.app().Party().Pseudonymize(e.owner, app.PartyPseudonymizeRequest_builder{Ref: ref[app.PartyRef](forgotten.GetId())}.Build())
		x.NoError(err)
		for name, req := range map[string]*app.PartyLinkMemberRequest{
			"a team":              app.PartyLinkMemberRequest_builder{Ref: ref[app.PartyRef](team.GetId()), Holder: hold}.Build(),
			"a member's record":   app.PartyLinkMemberRequest_builder{Ref: ref[app.PartyRef](lee.GetId()), Holder: hold}.Build(),
			"a record forgotten":  app.PartyLinkMemberRequest_builder{Ref: ref[app.PartyRef](forgotten.GetId()), Holder: hold}.Build(),
			"nobody's membership": app.PartyLinkMemberRequest_builder{Ref: ref[app.PartyRef](kim.GetId()), Holder: app.HolderRef_builder{Id: pdid.New(pd.HolderDomain).Bytes()}.Build()}.Build(),
		} {
			_, err := e.app().Party().LinkMember(e.owner, req)
			x.Error(err, name)
			x.Contains([]codes.Code{codes.InvalidArgument, codes.FailedPrecondition}, status.Code(err), "%s: %v", name, err)
		}
	})

	v, err = e.app().Party().UnlinkMember(e.owner, app.PartyUnlinkMemberRequest_builder{Ref: ref[app.PartyRef](right.GetId())}.Build())
	x.NoError(err)
	x.False(v.HasHolder())
	x.Equal("구성원 연결 끊기: 김철수 ("+called+")", e.events()["party.unlink"])
	_, err = e.app().Party().UnlinkMember(e.owner, app.PartyUnlinkMemberRequest_builder{Ref: ref[app.PartyRef](right.GetId())}.Build())
	x.Equal(codes.FailedPrecondition, status.Code(err), "%v", err)

	p, err := e.s.Identity.Verify(ctx, "p1", "password1234")
	x.NoError(err)
	_, err = e.s.Members.Of(ctx, p)
	x.NoError(err)
	any, err := e.s.Ent.Party.Query().Where(party.HolderId(idUUID(member)), party.DateErasedIsNil()).Exist(ctx)
	x.NoError(err)
	x.False(any, "signing in found the membership a record")
}
