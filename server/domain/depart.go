package domain

import (
	"context"
	"fmt"
	"uuid"

	"github.com/lesomnus/payday/pdid"
	"github.com/lesomnus/z"
	"github.com/protobuf-orm/ent/dialect"

	app "github.com/lesomnus/rove"
	"github.com/lesomnus/rove/internal/ent"
	"github.com/lesomnus/rove/internal/ent/holder"
	"github.com/lesomnus/rove/internal/ent/invitation"
	"github.com/lesomnus/rove/internal/ent/party"
	"github.com/lesomnus/rove/internal/ent/predicate"
	"github.com/lesomnus/rove/server/pd"
)

// An account leaving the deployment (design 9.10, M2 step 5).
//
// roster erases an account and its memberships here begin to leave
// (`server/lifecycle`). Once roster's grace is over and roster has not
// restored it, the account leaves every workspace it was in, one membership
// at a time and each as the deployment's own work there ([Deps.Depart]).
//
// Leaving a workspace is what an administrator's 개인정보 삭제 does to the
// person ([Tx.pseudonymize]), done by the deployment -- and one thing more: a
// workspace whose last owner this was is handed on ([Tx.succeed]), since
// nobody would be left who may.

// Departure is what an account leaving a workspace did there.
type Departure struct {
	// Party is the person record forgotten, when the workspace kept one.
	Party pdid.Id
	// Owner is who the workspace was handed on to, when this was its last
	// owner and there was somebody to hand it to; Ownerless is a workspace
	// whose last owner this was with nobody to hand it to.
	Owner     pdid.Id
	Ownerless bool
	// Held is how many of the trail's rows a legal hold kept as they were.
	Held int
}

// Depart is the account a membership is of leaving that membership's
// workspace. `server` is the ungated stack. A membership already ended is
// nothing to do, so asking twice is asking once.
func (d *Deps) Depart(ctx context.Context, server app.Server, drv dialect.Driver, tenant, member pdid.Id) (Departure, error) {
	var out Departure
	err := d.System(ctx, server, drv, tenant, func(t *Tx) error {
		v, err := t.depart(member)
		out = v
		return err
	})
	return out, err
}

// Unaddress takes an account's address off what a workspace it was never a
// member of sent it: an invitation it claimed or answered there, and nothing
// else, since nothing else there is about it. What is left of an account
// leaving, beside its memberships.
func (d *Deps) Unaddress(ctx context.Context, server app.Server, drv dialect.Driver, tenant, account pdid.Id) error {
	return d.System(ctx, server, drv, tenant, func(t *Tx) error {
		a := account.Uuid()
		ids, err := t.forgetInvitations(pdid.Nil, pdid.Nil, &a)
		if err != nil || len(ids) == 0 {
			return err
		}
		_, err = t.forget(ids)
		return err
	})
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

	if out.Owner, out.Ownerless, err = t.succeed(h); err != nil {
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

	payload := map[string]string{}
	if out.Held > 0 {
		payload["held"] = fmt.Sprint(out.Held)
	}
	if out.Ownerless {
		payload["ownerless"] = "true"
	}
	return out, t.begin(nil, "account.depart", pdid.Nil, t.now, "계정 탈퇴: 개인정보 삭제", "", payload)
}

// pseudonymize forgets a person here: the record keeps its identifier, so the
// history that names it still reads, and loses everything that said who it
// was; their membership ends and its name goes with it; the invitations sent
// to them lose the address; and the trail's copies of all of it are
// forgotten (design 8.2, D13). It answers the record as it is now, and how
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

	h := idOf(p.GetHolder().GetId())
	held, err := t.endMembership(h, id, objects)
	if err != nil {
		return out, held, err
	}

	// And the memberships it was before a login was stopped, which the
	// record no longer points at: each keeps which record it was
	// (`Holder.party`, rove#25). One stopped and then given again is two.
	was, err := t.db.Holder.Query().
		Where(holder.TenantId(t.tenant.Uuid()), holder.Party(id.Uuid())).
		Ids(t.ctx)
	if err != nil {
		return out, held, err
	}
	for _, v := range was {
		if pdid.Id(v) == h {
			continue
		}
		n, err := t.endMembership(pdid.Id(v), id, nil)
		if err != nil {
			return out, held, err
		}
		held += n
	}
	return out, held, nil
}

// Pseudonymized is the act a membership's own name going is on the trail as.
const Pseudonymized = "rove.holder.pseudonymize"

// endMembership ends a membership of somebody being forgotten, and forgets
// what the trail kept of it and of `objects` beside it: the person record
// it was, and the invitations sent to them.
//
// Its own name goes too, through ent rather than a Patch, because a record of
// what was blanked would be the thing blanked. What the history calls the
// membership from then is the pseudonym its person record took, or one of its
// own.
func (t *Tx) endMembership(member, person pdid.Id, objects []pdid.Id) (int, error) {
	var account *uuid.UUID
	if !member.IsZero() {
		h, err := t.db.Holder.Get(t.ctx, member.Uuid())
		if err != nil {
			return 0, err
		}
		account = h.Account
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

	sent, err := t.forgetInvitations(member, person, account)
	if err != nil {
		return 0, err
	}
	objects = append(objects, sent...)

	// The trail kept the rows as they were after every write; those copies
	// are the person too.
	return t.forget(objects)
}

// forgetInvitations takes the address off this workspace's invitations to
// somebody being forgotten -- the ones their account answered or claimed, the
// one that made their membership, the ones naming their person record -- and
// takes back any still waiting. It answers which they were, for the trail's
// copies of them to go too.
//
// One sent to an address nobody claimed is not theirs to anybody's knowledge
// here, and stays: what roster could have said of the address went with the
// account.
func (t *Tx) forgetInvitations(member, person pdid.Id, account *uuid.UUID) ([]pdid.Id, error) {
	whose := []predicate.Invitation{}
	if !member.IsZero() {
		whose = append(whose, invitation.HolderId(member.Uuid()))
	}
	if !person.IsZero() {
		whose = append(whose, invitation.PartyId(person.Uuid()))
	}
	if account != nil {
		whose = append(whose, invitation.Account(*account))
	}
	if len(whose) == 0 {
		return nil, nil
	}
	vs, err := t.db.Invitation.Query().Where(invitation.TenantId(t.tenant.Uuid()), invitation.Or(whose...)).All(t.ctx)
	if err != nil {
		return nil, err
	}

	ids := make([]pdid.Id, 0, len(vs))
	for _, v := range vs {
		req := app.InvitationPatchRequest_builder{
			Ref:              app.InvitationRef_builder{Id: pdid.Id(v.Id).Bytes()}.Build(),
			Email:            z.Ptr(""),
			DateUpdatedForce: z.Ptr(true),
		}.Build()
		if v.State == invitePending {
			req.SetState(inviteRevoked)
			req.SetDateSettled(ts(t.now))
			req.SetClaimNull(true)
		}
		if _, err := t.next.Invitation().Patch(t.ctx, req); err != nil {
			return nil, err
		}
		ids = append(ids, pdid.Id(v.Id))
	}
	return ids, nil
}

// succeed hands a workspace on when its last owner leaves: to the most senior
// member who remains -- the widest role, then whoever joined first -- among
// those who can sign in and are not leaving themselves, and never to an
// auditor, whose role is to change nothing. It answers who, or that there was
// nobody: a workspace with no owner is still everybody else's, and only an
// operator can give it one.
func (t *Tx) succeed(h *ent.Holder) (pdid.Id, bool, error) {
	if h.Role != "owner" {
		return pdid.Nil, false, nil
	}
	live := func(q *ent.HolderQuery) *ent.HolderQuery {
		return q.Where(
			holder.TenantId(t.tenant.Uuid()),
			holder.IdNEQ(h.Id),
			holder.DateErasedIsNil(),
			holder.DateLeavingIsNil(),
			holder.AccountNotNil(),
		)
	}
	others, err := live(t.db.Holder.Query()).Where(holder.Role("owner")).Exist(t.ctx)
	if err != nil || others {
		return pdid.Nil, false, err
	}

	for _, role := range []string{"admin", "manager", "member"} {
		next, err := live(t.db.Holder.Query()).Where(holder.Role(role)).
			Order(ent.Asc(holder.FieldDateCreated), ent.Asc(holder.FieldId)).
			First(t.ctx)
		if ent.IsNotFound(err) {
			continue
		}
		if err != nil {
			return pdid.Nil, false, err
		}

		id := pdid.Id(next.Id)
		if err := t.begin(nil, "party.role", pdid.Nil, t.now, "소유자 승계: {holder} → owner (마지막 소유자의 계정 탈퇴)", "", map[string]string{
			"role": "owner", "holder": id.String(),
		}); err != nil {
			return pdid.Nil, false, err
		}
		if _, err := t.next.Holder().Patch(t.ctx, app.HolderPatchRequest_builder{
			Ref:              app.HolderRef_builder{Id: id.Bytes()}.Build(),
			Role:             z.Ptr("owner"),
			DateUpdatedForce: z.Ptr(true),
		}.Build()); err != nil {
			return pdid.Nil, false, err
		}
		return id, false, nil
	}
	return pdid.Nil, true, nil
}
