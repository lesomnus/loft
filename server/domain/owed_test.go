package domain_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/lesomnus/payday/pdid"
	"github.com/lesomnus/payday/pdtest"

	app "github.com/lesomnus/rove"
	"github.com/lesomnus/rove/cmd"
	"github.com/lesomnus/rove/internal/ent"
	"github.com/lesomnus/rove/server/pd"
)

// archiving is a deployment whose trail keeps an archive, and whose rows
// leave the database for it at the first pass.
func archiving(t *testing.T) *env {
	return newEnvWith(t, func(c *cmd.Config) {
		c.Audit.Archive = t.TempDir()
		c.Audit.Checkpoints.Key = pdtest.Key(t)
		c.Audit.Retain = time.Nanosecond
	})
}

// archived is how many of the archive's rows about these objects still hold
// what they said.
func (e *env) archived(ids ...[]byte) int {
	want := map[string]bool{}
	for _, v := range ids {
		want[string(v)] = true
	}
	n := 0
	e.x.NoError(e.s.Trail.Read(context.Background(), pd.TrailStore(e.s.Ent), pd.TrailOf(func(v *app.Audit) error {
		if want[string(v.GetObjectId())] && (len(v.GetValue()) > 0 || len(v.GetPatch()) > 0) {
			n++
		}
		return nil
	})))
	return n
}

// owed is how many of the trail's erasures are owed and not paid.
func (e *env) owed() int {
	n, err := e.s.Ent.Owed.Query().Count(context.Background())
	e.x.NoError(err)
	return n
}

// TestAPersonForgottenLeavesTheArchiveOnceItCommits (payday#115).
//
// The archive takes part in no transaction. So 개인정보 삭제 blanks the
// trail's rows about the person in its own transaction and owes the
// archive's half there, and pays it once the transaction has committed --
// naming what it forgot, once: the archive's copies of the person go too,
// and nothing is left owed.
func TestAPersonForgottenLeavesTheArchiveOnceItCommits(t *testing.T) {
	e := archiving(t)
	x := e.x
	_, kim := e.person("member")
	h := kim.GetHolder().GetId()
	e.s.Trail.Pass(context.Background(), pd.TrailStore(e.s.Ent))
	x.Positive(e.archived(kim.GetId(), h), "the archive holds nothing of the person, so this proves nothing")

	paid := [][]pdid.Id{}
	pay := e.s.Deps.Pay
	e.s.Deps.Pay = func(ctx context.Context, objects []pdid.Id) error {
		paid = append(paid, objects)
		return pay(ctx, objects)
	}

	_, err := e.app().Party().Pseudonymize(e.owner, app.PartyPseudonymizeRequest_builder{Ref: ref[app.PartyRef](kim.GetId())}.Build())
	x.NoError(err)

	x.Zero(e.archived(kim.GetId(), h), "a copy of the person in the archive")
	x.Zero(e.owed(), "an erasure paid is still owed")
	x.Len(paid, 1, "what the operation forgot was paid other than once")
	x.Contains(paid[0], pdid.Id(idUUID(kim.GetId())), "the person was not among what was paid")
}

// TestAnArchiveNotPaidIsOwedToTheNextPass (payday#115): a payment that fails
// after the commit has undone nothing. The person is forgotten in the
// database, the debt is written, the operation answers as it would have --
// and the trail's next pass pays it.
func TestAnArchiveNotPaidIsOwedToTheNextPass(t *testing.T) {
	e := archiving(t)
	x := e.x
	ctx := context.Background()
	_, kim := e.person("member")
	h := kim.GetHolder().GetId()
	e.s.Trail.Pass(ctx, pd.TrailStore(e.s.Ent))
	x.Positive(e.archived(kim.GetId(), h), "the archive holds nothing of the person, so this proves nothing")

	e.s.Deps.Pay = func(context.Context, []pdid.Id) error { return errors.New("the archive is not there") }
	_, err := e.app().Party().Pseudonymize(e.owner, app.PartyPseudonymizeRequest_builder{Ref: ref[app.PartyRef](kim.GetId())}.Build())
	x.NoError(err, "a payment that failed failed the operation, which had committed")

	full, _ := e.copies(kim.GetId(), h)
	x.Zero(full, "the database's half was not done")
	x.Positive(e.archived(kim.GetId(), h), "the archive was paid by a payment that failed, so this proves nothing")
	x.Positive(e.owed(), "the archive's half is not owed")

	e.s.Trail.Pass(ctx, pd.TrailStore(e.s.Ent))
	x.Zero(e.archived(kim.GetId(), h), "the next pass did not pay what was owed")
	x.Zero(e.owed(), "a debt paid is still owed")
}

// TestAnOperationThatFailsForgetsNothing (payday#115): the trail's erasure is
// part of the operation's transaction, so an operation that fails after it
// takes it back with the rest. The trail says what it said, nothing is owed,
// and nothing is paid.
func TestAnOperationThatFailsForgetsNothing(t *testing.T) {
	e := archiving(t)
	x := e.x
	_, kim := e.person("member")
	h := kim.GetHolder().GetId()
	before, _ := e.copies(kim.GetId(), h)
	x.Positive(before, "the trail holds nothing of the person, so this proves nothing")

	forget := e.s.Deps.Forget
	e.s.Deps.Forget = func(ctx context.Context, db *ent.Client, objects []pdid.Id) (int, error) {
		if _, err := forget(ctx, db, objects); err != nil {
			return 0, err
		}
		return 0, errors.New("what came after the trail's erasure failed")
	}
	paid := 0
	e.s.Deps.Pay = func(context.Context, []pdid.Id) error { paid++; return nil }

	_, err := e.app().Party().Pseudonymize(e.owner, app.PartyPseudonymizeRequest_builder{Ref: ref[app.PartyRef](kim.GetId())}.Build())
	x.Error(err)

	full, _ := e.copies(kim.GetId(), h)
	x.Equal(before, full, "an operation that failed blanked the trail")
	x.Zero(e.owed(), "a debt outlived the transaction that owed it")
	x.Zero(paid, "an operation that failed paid an erasure")
}
