package domain_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/lesomnus/payday/pdid"
	"google.golang.org/protobuf/encoding/protojson"

	app "github.com/lesomnus/rove"
	"github.com/lesomnus/rove/internal/ent"
	"github.com/lesomnus/rove/internal/ent/event"
	"github.com/lesomnus/rove/internal/ent/holder"
	"github.com/lesomnus/rove/server/domain"
)

// depart is the account of `member` leaving this env's workspace, as the
// lifecycle does it once roster's grace is over.
func (e *env) depart(member []byte) domain.Departure {
	e.t.Helper()
	id, err := pdid.From(member)
	e.x.NoError(err)
	v, err := e.s.Deps.Depart(context.Background(), e.s.Base, e.s.Drv, e.tenant, id)
	e.x.NoError(err)
	return v
}

// invited is an invitation in this env's workspace, as the invitation verbs
// leave one: from the owner, to `email`.
func (e *env) invited(email, state string, edit func(*app.InvitationAddRequest)) *app.Invitation {
	e.t.Helper()
	req := app.InvitationAddRequest_builder{
		Tenant:      app.TenantRef_builder{Id: e.tenant.Bytes()}.Build(),
		Inviter:     app.HolderRef_builder{Id: e.holderOf("owner").Bytes()}.Build(),
		Email:       email,
		Role:        "member",
		State:       state,
		DateExpires: e.in(30 * 24 * time.Hour),
	}.Build()
	if edit != nil {
		edit(req)
	}
	v, err := e.s.Base.Invitation().Add(context.Background(), req)
	e.x.NoError(err)
	return v
}

func (e *env) holderOf(alias string) pdid.Id {
	e.t.Helper()
	h, err := e.s.Ent.Holder.Query().Where(holder.Alias(alias), holder.TenantId(e.tenant.Uuid()), holder.DateErasedIsNil()).Only(context.Background())
	e.x.NoError(err)
	return pdid.Id(h.Id)
}

func (e *env) holderRow(id []byte) *ent.Holder {
	e.t.Helper()
	h, err := e.s.Ent.Holder.Get(context.Background(), idUUID(id))
	e.x.NoError(err)
	return h
}

func (e *env) roleOf(id pdid.Id) string {
	e.t.Helper()
	h, err := e.s.Ent.Holder.Get(context.Background(), id.Uuid())
	e.x.NoError(err)
	return h.Role
}

// TestAnAccountLeavingIsForgottenThere.
//
// What an account leaving takes from a workspace is everything that said who
// it was: the person record keeps its identifier and nothing else, the
// membership ends and its name goes too, the invitations sent to them lose
// the address, and the trail's copies of all of it are forgotten. What they
// did stays, said by the pseudonym.
func TestAnAccountLeavingIsForgottenThere(t *testing.T) {
	e := newEnv(t)
	x := e.x
	ctx := context.Background()
	kimAt, kim := e.person("manager")
	member := kim.GetHolder().GetId()
	account := e.accountOf(kim)

	// Something kim did, for the history to say who did it.
	room := e.space("회의실", nil)
	laptop := e.item("노트북", "NB-1", nil, time.Hour)
	_, err := e.app().Asset().Move(kimAt, app.AssetMoveRequest_builder{Ref: assetRef(laptop), To: assetRef(room)}.Build())
	x.NoError(err)

	// What the workspace sent them: the invitation they took, one their
	// account was sent again and has not answered, and somebody else's.
	took := e.invited("kim@example.com", "accepted", func(r *app.InvitationAddRequest) {
		r.SetHolder(app.HolderRef_builder{Id: member}.Build())
		r.SetAccount(account.Bytes())
	})
	waiting := e.invited("kim@example.com", "pending", func(r *app.InvitationAddRequest) { r.SetAccount(account.Bytes()) })
	other := e.invited("lee@example.com", "pending", nil)

	full, _ := e.copies(kim.GetId(), member, took.GetId(), waiting.GetId())
	x.Positive(full)

	v := e.depart(member)
	x.Equal(idOf(kim.GetId()), v.Party)
	x.Zero(v.Held)

	p, err := e.s.Base.Party().Get(ctx, app.PartyGetRequest_builder{Ref: ref[app.PartyRef](kim.GetId())}.Build())
	x.NoError(err)
	x.True(strings.HasPrefix(p.GetName(), "익명 "))
	x.Empty(p.GetEmail())
	x.False(p.HasHolder())

	h := e.holderRow(member)
	x.NotNil(h.DateErased, "the membership ended")
	x.Empty(h.Alias, "and its name went with it")
	x.Equal(p.GetName(), h.Name, "it goes by its person's pseudonym")

	for _, inv := range []*app.Invitation{took, waiting} {
		got, err := e.s.Ent.Invitation.Get(ctx, idUUID(inv.GetId()))
		x.NoError(err)
		x.Empty(got.Email)
	}
	got, err := e.s.Ent.Invitation.Get(ctx, idUUID(waiting.GetId()))
	x.NoError(err)
	x.Equal("revoked", got.State, "one still waiting is taken back")
	got, err = e.s.Ent.Invitation.Get(ctx, idUUID(other.GetId()))
	x.NoError(err)
	x.Equal("lee@example.com", got.Email, "and nobody else's is touched")
	x.Equal("pending", got.State)

	full, _ = e.copies(kim.GetId(), member, took.GetId(), waiting.GetId())
	x.Zero(full, "a copy of them in the trail")

	// What they did is still said, by the pseudonym.
	said := false
	for _, en := range e.timeline(laptop, false) {
		x.NotContains(en.GetActor(), "@p", "the membership's old login")
		x.NotContains(en.GetActor(), "manager", "the person's old name")
		said = said || en.GetActor() == p.GetName()
	}
	x.True(said, "the move, by whom")

	n, err := e.s.Ent.Event.Query().Where(event.Kind("account.depart"), event.TenantId(e.tenant.Uuid())).Count(ctx)
	x.NoError(err)
	x.Equal(1, n)

	// Asked again, it is done already.
	again := e.depart(member)
	x.Zero(again.Party)
	n, err = e.s.Ent.Event.Query().Where(event.Kind("account.depart")).Count(ctx)
	x.NoError(err)
	x.Equal(1, n)
}

// TestAWorkspaceIsForgottenOfAPersonTheSameWayByItsAdministrators.
//
// 개인정보 삭제 from the people screen is the same forgetting: the
// membership's name and the invitations sent to them go too, which they did
// not before.
func TestAWorkspaceIsForgottenOfAPersonTheSameWayByItsAdministrators(t *testing.T) {
	e := newEnv(t)
	x := e.x
	ctx := context.Background()
	_, kim := e.person("member")
	member := kim.GetHolder().GetId()
	took := e.invited("kim@example.com", "accepted", func(r *app.InvitationAddRequest) {
		r.SetHolder(app.HolderRef_builder{Id: member}.Build())
	})
	named := e.invited("kim@work.example", "pending", func(r *app.InvitationAddRequest) {
		r.SetParty(app.PartyRef_builder{Id: kim.GetId()}.Build())
	})

	_, err := e.app().Party().Pseudonymize(e.owner, app.PartyPseudonymizeRequest_builder{Ref: ref[app.PartyRef](kim.GetId())}.Build())
	x.NoError(err)

	h := e.holderRow(member)
	x.Empty(h.Alias)
	x.True(strings.HasPrefix(h.Name, "익명 "))
	for _, inv := range []*app.Invitation{took, named} {
		got, err := e.s.Ent.Invitation.Get(ctx, idUUID(inv.GetId()))
		x.NoError(err)
		x.Empty(got.Email)
	}
	full, _ := e.copies(took.GetId(), named.GetId())
	x.Zero(full)
}

// TestTheLastOwnerLeavingHandsTheWorkspaceOn.
//
// Nobody could manage a workspace whose last owner left, so it goes to the
// most senior member who remains: the widest role first, then whoever joined
// first -- among those who can sign in and are not leaving themselves, and
// never an auditor, whose role is to change nothing.
func TestTheLastOwnerLeavingHandsTheWorkspaceOn(t *testing.T) {
	ctx := context.Background()

	t.Run("the widest role, then who joined first", func(t *testing.T) {
		e := newEnv(t)
		x := e.x
		_, auditor := e.person("auditor")
		_, member := e.person("member")
		e.now = e.now.Add(time.Second)
		_, early := e.person("manager")
		e.now = e.now.Add(time.Second)
		_, late := e.person("manager")

		v := e.depart(e.holderOf("owner").Bytes())
		x.Equal(idOf(early.GetHolder().GetId()), v.Owner)
		x.False(v.Ownerless)
		x.Equal("owner", e.roleOf(idOf(early.GetHolder().GetId())))
		x.Equal("manager", e.roleOf(idOf(late.GetHolder().GetId())))
		x.Equal("member", e.roleOf(idOf(member.GetHolder().GetId())))
		x.Equal("auditor", e.roleOf(idOf(auditor.GetHolder().GetId())))

		ev, err := e.s.Ent.Event.Query().Where(event.Kind("party.role")).Only(ctx)
		x.NoError(err)
		x.Contains(e.say(ev), "소유자 승계")
		x.Equal("succession", value[*app.PartyRoleSet](e, ev).GetBy())
		x.NotContains(protojson.Format(ev.Value), early.GetName(), "the event keeps who by identifier")
		x.Equal("소유자 승계: "+early.GetName()+" → owner (마지막 소유자의 계정 탈퇴)", e.events()["party.role"])
	})

	t.Run("an admin over a manager who joined before", func(t *testing.T) {
		e := newEnv(t)
		x := e.x
		e.person("manager")
		e.now = e.now.Add(time.Second)
		_, admin := e.person("admin")

		v := e.depart(e.holderOf("owner").Bytes())
		x.Equal(idOf(admin.GetHolder().GetId()), v.Owner)
	})

	t.Run("not to somebody leaving too", func(t *testing.T) {
		e := newEnv(t)
		x := e.x
		_, first := e.person("admin")
		e.now = e.now.Add(time.Second)
		_, second := e.person("admin")
		x.NoError(e.s.Ent.Holder.UpdateOneId(idUUID(first.GetHolder().GetId())).SetDateLeaving(e.now).Exec(ctx))

		v := e.depart(e.holderOf("owner").Bytes())
		x.Equal(idOf(second.GetHolder().GetId()), v.Owner)
	})

	t.Run("another owner is enough", func(t *testing.T) {
		e := newEnv(t)
		x := e.x
		_, other := e.person("owner")
		e.person("admin")

		v := e.depart(e.holderOf("owner").Bytes())
		x.Zero(v.Owner)
		x.False(v.Ownerless)
		x.Equal("owner", e.roleOf(idOf(other.GetHolder().GetId())))
		n, err := e.s.Ent.Event.Query().Where(event.Kind("party.role")).Count(ctx)
		x.NoError(err)
		x.Zero(n)
	})

	t.Run("nobody but an auditor, and it says so", func(t *testing.T) {
		e := newEnv(t)
		x := e.x
		_, auditor := e.person("auditor")

		v := e.depart(e.holderOf("owner").Bytes())
		x.Zero(v.Owner)
		x.True(v.Ownerless)
		x.Equal("auditor", e.roleOf(idOf(auditor.GetHolder().GetId())))
		ev, err := e.s.Ent.Event.Query().Where(event.Kind("account.depart")).Only(ctx)
		x.NoError(err)
		x.True(value[*app.AccountDeparted](e, ev).GetOwnerless())
	})

	t.Run("a member leaving is not an owner leaving", func(t *testing.T) {
		e := newEnv(t)
		x := e.x
		_, kim := e.person("member")
		_, admin := e.person("admin")

		v := e.depart(kim.GetHolder().GetId())
		x.Zero(v.Owner)
		x.False(v.Ownerless)
		x.Equal("admin", e.roleOf(idOf(admin.GetHolder().GetId())))
	})
}

func idOf(b []byte) pdid.Id {
	v, _ := pdid.From(b)
	return v
}
