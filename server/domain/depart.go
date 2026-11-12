package domain

import (
	"context"

	"github.com/lesomnus/payday/pdid"
	"github.com/lesomnus/z"
	"github.com/protobuf-orm/ent/dialect"

	app "github.com/lesomnus/loft"
	"github.com/lesomnus/loft/internal/ent"
	"github.com/lesomnus/loft/internal/ent/holder"
	"github.com/lesomnus/loft/internal/ent/party"
	"github.com/lesomnus/loft/server/pd"
)

// A member leaving the workspace (design 9.10, M2 step 5).
//
// roster erases the holder a member is -- the holder of the tenant this
// deployment serves, by the member's own identifier (D85) -- and the
// membership here begins to leave (`server/lifecycle`). Once roster's grace is
// over and roster has not restored them, the member leaves, as the
// deployment's own work in the workspace ([Deps.Depart]).
//
// Leaving is what an administrator's 개인정보 삭제 does to the person
// ([Tx.pseudonymize]), done by the deployment. Who may run the workspace once
// they have gone is roster's, by the roles the tenant binds there (D89): it
// used to be handed on to whoever was next, when this was its last owner, and
// nothing here knows who that is now.

// Departure is what a member leaving did.
type Departure struct {
	// Party is the person record forgotten, when the workspace kept one.
	Party pdid.Id
	// Held is how many of the trail's rows in the database a legal hold kept
	// as they were. What it keeps in the archive is learnt when the archive's
	// half is paid, after the commit, and stays owed (`loft trail owed`).
	Held int
}

// Depart is a member leaving the workspace. `server` is the ungated stack. A
// membership already ended is nothing to do, so asking twice is asking once.
func (d *Deps) Depart(ctx context.Context, server app.Server, drv dialect.Driver, tenant, member pdid.Id) (Departure, error) {
	var out Departure
	err := d.System(ctx, server, drv, tenant, func(t *Tx) error {
		v, err := t.depart(member)
		out = v
		return err
	})
	return out, err
}

func (t *Tx) depart(member pdid.Id) (Departure, error) {
	out := Departure{}
	h, err := t.db.Holder.Query().Where(holder.Id(member.Uuid()), holder.TenantId(t.tenant.Uuid()), holder.DateErasedIsNil()).Only(t.ctx)
	if ent.IsNotFound(err) {
		return out, nil
	}
	if err != nil {
		return out, err
	}

	// The person record this membership is, when the workspace keeps one;
	// a membership with none is forgotten as itself.
	p, err := t.db.Party.Query().Where(party.HolderId(h.Id), party.TenantId(t.tenant.Uuid()), party.DateErasedIsNil()).First(t.ctx)
	switch {
	case err == nil:
		v, err := t.next.Party().Get(t.ctx, app.PartyGetRequest_builder{Ref: app.PartyRef_builder{Id: pdid.Id(p.Id).Bytes()}.Build()}.Build())
		if err != nil {
			return out, err
		}
		if _, out.Held, err = t.pseudonymize(v); err != nil {
			return out, err
		}
		out.Party = pdid.Id(p.Id)
	case ent.IsNotFound(err):
		if out.Held, err = t.endMembership(member, pdid.Nil, nil); err != nil {
			return out, err
		}
	default:
		return out, err
	}

	return out, t.begin(nil, "account.depart", pdid.Nil, t.now, "", app.AccountDeparted_builder{
		Held: uint32(out.Held),
	}.Build())
}

// pseudonymize forgets a person here: the record keeps its identifier, so the
// history that names it still reads, and loses everything that said who it
// was; their membership ends and its name goes with it; and the trail's
// copies of all of it are forgotten (design 8.2, D13). It answers the record as it is now, and how
// many of the trail's rows a legal hold kept as they were.
func (t *Tx) pseudonymize(p *app.Party) (*app.Party, int, error) {
	id := idOf(p.GetId())
	name := "익명 " + id.String()[:8]
	objects := []pdid.Id{id}

	out, err := t.next.Party().Patch(t.ctx, app.PartyPatchRequest_builder{
		Ref:              app.PartyRef_builder{Id: id.Bytes()}.Build(),
		Name:             z.Ptr(name),
		Desc:             z.Ptr(""),
		Email:            z.Ptr(""),
		Phone:            z.Ptr(""),
		Code:             z.Ptr(""),
		Labels:           map[string]string{},
		HolderNull:       z.Ptr(true),
		DateUpdatedForce: z.Ptr(true),
	}.Build())
	if err != nil {
		return nil, 0, err
	}

	held, err := t.endMembership(idOf(p.GetHolder().GetId()), id, objects)
	return out, held, err
}

// Pseudonymized is the act a membership's own name going is on the trail as.
const Pseudonymized = "loft.holder.pseudonymize"

// endMembership ends a membership of somebody being forgotten, and forgets
// what the trail kept of it and of `objects` beside it: the person record
// it was.
//
// Its own name goes too, through ent rather than a Patch, because a record of
// what was blanked would be the thing blanked. What the history calls the
// membership from then is the pseudonym its person record took, or one of its
// own.
func (t *Tx) endMembership(member, person pdid.Id, objects []pdid.Id) (int, error) {
	if !member.IsZero() {
		h, err := t.db.Holder.Get(t.ctx, member.Uuid())
		if err != nil {
			return 0, err
		}
		if h.DateErased == nil {
			if _, err := t.next.Holder().Erase(t.ctx, app.HolderRef_builder{Id: member.Bytes()}.Build()); err != nil {
				return 0, err
			}
		}
		pseudonym := "익명 " + member.String()[:8]
		if !person.IsZero() {
			pseudonym = "익명 " + person.String()[:8]
		}
		if err := t.db.Holder.UpdateOneId(member.Uuid()).
			SetAlias("").
			SetName(pseudonym).
			SetDesc("").
			SetLabels(map[string]string{}).
			Exec(t.ctx); err != nil {
			return 0, err
		}
		// And on the trail as the act it is: a digest of the row it leaves,
		// and nothing of what it held. An erased row is checked as a live one
		// is (payday#115), and one written past the servers that nothing
		// records is a row the trail says is other than it is.
		if _, err := pd.Record(t.ctx, t.db, t.audit(), Pseudonymized, []pdid.Id{member}); err != nil {
			return 0, err
		}
		objects = append(objects, member)
	}

	// The trail kept the rows as they were after every write; those copies
	// are the person too.
	return t.forget(objects)
}
