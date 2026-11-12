package domain_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/lesomnus/payday/pdid"

	app "github.com/lesomnus/loft"
	"github.com/lesomnus/loft/internal/ent"
	"github.com/lesomnus/loft/internal/ent/event"
	"github.com/lesomnus/loft/internal/ent/holder"
	"github.com/lesomnus/loft/server/domain"
)

// depart is `member` leaving this env's workspace, as the lifecycle does it
// once roster's grace is over.
func (e *env) depart(member []byte) domain.Departure {
	e.t.Helper()
	id, err := pdid.From(member)
	e.x.NoError(err)
	v, err := e.s.Deps.Depart(context.Background(), e.s.Base, e.s.Drv, e.tenant, id)
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

// TestAMemberLeavingIsForgottenThere.
//
// What a member leaving takes from the workspace is everything that said who
// they were: the person record keeps its identifier and nothing else, the
// membership ends and its name goes too, and the trail's copies of all of it
// are forgotten. What they did stays, said by the pseudonym.
func TestAMemberLeavingIsForgottenThere(t *testing.T) {
	e := newEnv(t)
	x := e.x
	ctx := context.Background()
	kimAt, kim := e.person("manager")
	member := kim.GetHolder().GetId()

	// Something kim did, for the history to say who did it.
	room := e.space("회의실", nil)
	laptop := e.item("노트북", "NB-1", nil, time.Hour)
	_, err := e.app().Asset().Move(kimAt, app.AssetMoveRequest_builder{Ref: assetRef(laptop), To: assetRef(room)}.Build())
	x.NoError(err)

	full, _ := e.copies(kim.GetId(), member)
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

	full, _ = e.copies(kim.GetId(), member)
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
// membership's name goes too.
func TestAWorkspaceIsForgottenOfAPersonTheSameWayByItsAdministrators(t *testing.T) {
	e := newEnv(t)
	x := e.x
	_, kim := e.person("member")
	member := kim.GetHolder().GetId()

	_, err := e.app().Party().Pseudonymize(e.owner, app.PartyPseudonymizeRequest_builder{Ref: ref[app.PartyRef](kim.GetId())}.Build())
	x.NoError(err)

	h := e.holderRow(member)
	x.Empty(h.Alias)
	x.True(strings.HasPrefix(h.Name, "익명 "))
	full, _ := e.copies(member)
	x.Zero(full)
}

// TestTheLastOwnerLeavingHandsNothingOn: who may run the workspace is
// roster's, by the roles its tenant binds there (D89). Its last owner leaving
// is pseudonymized as anybody leaving is, and nothing more: nobody here is
// made anything, and what the others may call is what roster says, as it was
// -- the tenant's administrators at roster bind somebody the owner's role.
func TestTheLastOwnerLeavingHandsNothingOn(t *testing.T) {
	e := newEnv(t)
	x := e.x
	ctx := context.Background()
	_, admin := e.person("admin")
	them := idOf(admin.GetHolder().GetId())
	may := func(method string) bool { return e.s.Identity.Reach().May(ctx, them, method) == nil }
	x.True(may(app.PartyService_Pseudonymize_FullMethodName))
	x.False(may(app.TenantService_Configure_FullMethodName))

	e.depart(e.holderOf("owner").Bytes())
	x.True(may(app.PartyService_Pseudonymize_FullMethodName))
	x.False(may(app.TenantService_Configure_FullMethodName), "an administrator is no owner for the owner having left")
	n, err := e.s.Ent.Event.Query().Where(event.Kind("account.depart")).Count(ctx)
	x.NoError(err)
	x.Equal(1, n, "and the act is the leaving alone")
}

func idOf(b []byte) pdid.Id {
	v, _ := pdid.From(b)
	return v
}
