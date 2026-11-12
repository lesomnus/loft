package cmd_test

import (
	"context"
	"testing"
	"time"

	"github.com/lesomnus/payday/pdid"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	app "github.com/lesomnus/loft"
	"github.com/lesomnus/loft/cmd"
	"github.com/lesomnus/loft/internal/ent"
	"github.com/lesomnus/loft/internal/ent/event"
	"github.com/lesomnus/loft/internal/ent/holder"
	"github.com/lesomnus/loft/internal/ent/levyannotice"
	enttenant "github.com/lesomnus/loft/internal/ent/tenant"
	"github.com/lesomnus/loft/internal/identity"
	"github.com/lesomnus/loft/internal/levyantest"
	"github.com/lesomnus/loft/server/closing"
	"github.com/lesomnus/loft/server/domain"
	"github.com/lesomnus/loft/server/levyan"
)

// What became of a workspace -- closed, restored, destroyed -- as levyan is
// told it (design 7.8, levyan's `docs/apps.md` 5). Who pays for it is what
// the portal tells levyan, and nothing here does (D84): a test says it of
// the fake ([levyantest.Levyan.Arrived]).

// notices is every notice for levyan, in the order the teller tells them:
// not the first pass's mark, which is no notice ([levyan.Past]).
func (w *world) notices() []*ent.LevyanNotice {
	vs, err := w.s.Ent.LevyanNotice.Query().
		Where(levyannotice.KindNEQ(levyan.Past)).
		Order(ent.Asc(levyannotice.FieldDateOccurred), ent.Asc(levyannotice.FieldId)).
		All(context.Background())
	w.x.NoError(err)
	return vs
}

// waiting is the notices levyan has not answered.
func (w *world) waiting() int {
	n, err := w.s.Ent.LevyanNotice.Query().Where(levyannotice.DateToldIsNil()).Count(context.Background())
	w.x.NoError(err)
	return n
}

// madeAt is when a workspace was made, as its row says.
func (w *world) madeAt(id pdid.Id) time.Time {
	v, err := w.s.Ent.Tenant.Get(context.Background(), id.Uuid())
	w.x.NoError(err)
	return v.DateCreated.UTC().Truncate(time.Microsecond)
}

// workspace is one more workspace, put up as an operator puts one up, with an
// account of its own its one owner.
func (w *world) workspace(alias string) (pdid.Id, identity.Person) {
	ctx := context.Background()
	p, err := w.s.Identity.AddPerson(ctx, alias, alias, pdid.Nil)
	w.x.NoError(err)
	t, err := w.s.Base.Tenant().Add(ctx, app.TenantAddRequest_builder{Alias: alias, Name: alias}.Build())
	w.x.NoError(err)
	w.member(t, p, "owner")
	return tenantId(w.x, t), p
}

// arrived has the fake told what the portal tells levyan: that the workspace
// was made, by `owner`, when its row says it was.
func (w *world) arrived(fake *levyantest.Levyan, ws, owner pdid.Id) {
	fake.Arrived(ws, owner, w.madeAt(ws))
}

// kinds is what levyan took of `ws`, in the order it came.
func kinds(fake *levyantest.Levyan, ws pdid.Id) []string {
	out := []string{}
	for _, v := range fake.Heard() {
		if v.Workspace == ws.String() {
			out = append(out, v.Kind)
		}
	}
	return out
}

// moments is the moments levyan took what became of `ws` at, in the order it
// came.
func moments(fake *levyantest.Levyan, ws pdid.Id) []time.Time {
	out := []time.Time{}
	for _, v := range fake.Heard() {
		if v.Workspace == ws.String() && !v.At.IsZero() {
			out = append(out, v.At.UTC())
		}
	}
	return out
}

// ends is the notices of what became of `ws`, in the order they are told.
func (w *world) ends(ws pdid.Id) []*ent.LevyanNotice {
	vs, err := w.s.Ent.LevyanNotice.Query().
		Where(levyannotice.Workspace(ws.Uuid()), levyannotice.KindIn(levyan.Closed, levyan.Restored, levyan.Destroyed)).
		Order(ent.Asc(levyannotice.FieldDateOccurred), ent.Asc(levyannotice.FieldId)).
		All(context.Background())
	w.x.NoError(err)
	return vs
}

// occurred is when the events say `ws` was closed and restored, in order.
func (w *world) occurred(ws pdid.Id) []time.Time {
	vs, err := w.s.Ent.Event.Query().
		Where(event.TenantId(ws.Uuid()), event.KindIn("workspace.close", "workspace.restore")).
		Order(ent.Asc(event.FieldDateOccurred), ent.Asc(event.FieldId)).
		All(context.Background())
	w.x.NoError(err)
	out := []time.Time{}
	for _, v := range vs {
		out = append(out, v.DateOccurred.UTC().Truncate(time.Microsecond))
	}
	return out
}

func ats(vs []*ent.LevyanNotice) []time.Time {
	out := []time.Time{}
	for _, v := range vs {
		out = append(out, v.DateOccurred.UTC())
	}
	return out
}

func kindsOf(vs []*ent.LevyanNotice) []string {
	out := []string{}
	for _, v := range vs {
		out = append(out, v.Kind)
	}
	return out
}

// TestEveryWayAWorkspaceEndsIsToldToLevyan (design 7.8): a workspace closed --
// by an operator, or the deployment when an account leaving left nobody in it
// -- restored, and destroyed, by the sweep or an operator's hand, is written
// in the transaction that does it, or before a destruction takes anything, of
// the moment it happened; and told to levyan once, in the order it happened.
// A membership that ends is no notice (D84), and a dry run tells nothing.
func TestEveryWayAWorkspaceEndsIsToldToLevyan(t *testing.T) {
	w, fake := billed(t)
	x := w.x
	ctx := context.Background()
	acme := tenantId(x, w.tenant)
	w.arrived(fake, acme, w.memberId(w.firstOwner()))

	// Open, with no history: the first pass has nothing to tell of it.
	x.NoError(w.s.Teller.Pass(ctx))
	x.Empty(kinds(fake, acme))

	// An operator closes it, and restores it; and does again.
	v := w.closeIt("잠시 닫습니다")
	closedAt := v.GetDateClosed().AsTime().UTC()
	_, err := w.restoreIt()
	x.NoError(err)
	_, err = w.s.Deps.CloseWorkspace(ctx, w.s.Base, w.s.Drv, acme, domain.ClosedByOperator, "미납")
	x.NoError(err)
	_, err = w.s.Deps.RestoreWorkspace(ctx, w.s.Base, w.s.Drv, acme)
	x.NoError(err)
	vs := w.ends(acme)
	x.Equal([]string{levyan.Closed, levyan.Restored, levyan.Closed, levyan.Restored}, kindsOf(vs))
	x.Equal(closedAt, vs[0].DateOccurred.UTC(), "of the moment the workspace said it was closed")
	x.Equal(w.occurred(acme), ats(vs), "each of the moment its event says")
	for _, v := range vs {
		x.Equal(levyan.ByChange, v.Origin)
		x.Nil(v.DateTold, "told when the teller tells it, and not before")
	}

	// A member leaving a workspace they were alone in: nobody is left, and
	// the deployment closes it. Its window over, the sweep destroys it.
	alone, kim := w.workspace("alone")
	w.arrived(fake, alone, kim.Id)
	_, err = w.s.Lifecycle.Depart(ctx, kim.Id)
	x.NoError(err)
	x.Equal([]string{levyan.Closed}, kindsOf(w.ends(alone)))
	x.Len(w.notices(), 5, "and nothing of the membership that ended")
	x.NoError(w.s.Ent.Tenant.UpdateOneId(alone.Uuid()).SetDateDestroy(time.Now().Add(-time.Minute)).Exec(ctx))
	x.NoError(w.s.Closing.Sweep(ctx))

	// And an operator's hand, on a workspace that is open: said first, and
	// then done.
	theirs, lee := w.workspace("lab")
	w.arrived(fake, theirs, lee.Id)
	row, err := w.s.Ent.Tenant.Get(ctx, theirs.Uuid())
	x.NoError(err)
	_, err = w.s.Closing.Destroy(ctx, row, closing.Purge, true)
	x.NoError(err)
	x.Empty(w.ends(theirs), "a dry run tells nothing")
	_, err = w.s.Closing.Destroy(ctx, row, closing.Purge, false)
	x.NoError(err)

	for _, ws := range []pdid.Id{alone, theirs} {
		vs := w.ends(ws)
		d := vs[len(vs)-1]
		x.Equal(levyan.Destroyed, d.Kind)
		alive, err := w.s.Ent.Tenant.Query().Where(enttenant.Id(ws.Uuid())).Exist(ctx)
		x.NoError(err)
		x.False(alive, "told before it went, and gone")
		read := false
		for _, r := range fake.Readings() {
			if r.GetWorkspace() == ws.String() {
				read = true
				x.True(r.GetDateMeasured().AsTime().Before(d.DateOccurred), "after its final reading")
			}
		}
		x.True(read, "its final reading, handed to levyan first")
	}

	told, err := w.s.Teller.Tell(ctx)
	x.NoError(err)
	x.Equal(levyan.Told{Told: 7}, told)
	x.Equal([]string{levyan.Closed, levyan.Restored, levyan.Closed, levyan.Restored}, kinds(fake, acme))
	x.Equal(w.occurred(acme), moments(fake, acme), "of the moments they happened")
	x.Equal([]string{levyan.Closed, levyan.Destroyed}, kinds(fake, alone))
	x.Equal([]string{levyan.Destroyed}, kinds(fake, theirs))
	x.Zero(w.waiting())

	life, err := w.s.Levyan.Lifecycle(ctx, acme)
	x.NoError(err)
	x.False(life.HasDateClosed(), "open, as it was last restored")
	x.False(life.HasDateDestroyed())
	life, err = w.s.Levyan.Lifecycle(ctx, alone)
	x.NoError(err)
	x.Equal(w.ends(alone)[0].DateOccurred.UTC(), life.GetDateClosed().AsTime().UTC(), "destroyed as it was closed")
	x.True(life.HasDateDestroyed())

	// Told once.
	told, err = w.s.Teller.Tell(ctx)
	x.NoError(err)
	x.Equal(levyan.Told{}, told)
	x.Len(fake.Heard(), 7)
}

// TestAWorkspacesEndWaitsForALevyanThatIsDown (design 7.8): nothing of
// closing, restoring or destroying waits for levyan or fails for it; what it
// is to be told waits, and is told once it answers, in the order it happened.
func TestAWorkspacesEndWaitsForALevyanThatIsDown(t *testing.T) {
	w, fake := billed(t)
	x := w.x
	ctx := context.Background()
	acme := tenantId(x, w.tenant)
	w.arrived(fake, acme, w.memberId(w.firstOwner()))
	theirs, lee := w.workspace("lab")
	w.arrived(fake, theirs, lee.Id)
	x.NoError(w.s.Teller.Pass(ctx))

	fake.Down(status.Error(codes.Unavailable, "levyantest: down"))
	w.closeIt("")
	_, err := w.restoreIt()
	x.NoError(err, "restored")
	row, err := w.s.Ent.Tenant.Get(ctx, theirs.Uuid())
	x.NoError(err)
	_, err = w.s.Closing.Destroy(ctx, row, closing.Purge, false)
	x.NoError(err, "destroyed")

	told, err := w.s.Teller.Tell(ctx)
	x.Equal(codes.Unavailable, status.Code(err))
	x.Equal(levyan.Told{Waiting: 3}, told)
	x.NoError(w.s.Teller.Pass(ctx), "a pass that could not tell levyan stops nothing")
	x.Equal(3, w.waiting())

	fake.Down(nil)
	told, err = w.s.Teller.Tell(ctx)
	x.NoError(err)
	x.Equal(levyan.Told{Told: 3}, told)
	x.Equal([]string{levyan.Closed, levyan.Restored}, kinds(fake, acme))
	x.Equal(w.occurred(acme), moments(fake, acme), "of the moments they happened, and not of their telling")
	x.Equal([]string{levyan.Destroyed}, kinds(fake, theirs))
	x.Zero(w.waiting())

	// Nobody there at all: the same.
	gone := newWorldWith(t, func(c *cmd.Config) { c.App.Billing.Levyan = levyantest.Gone(t) })
	gone.closeIt("")
	_, err = gone.s.Teller.Tell(ctx)
	gone.x.Equal(codes.Unavailable, status.Code(err))
	gone.x.Equal(1, gone.waiting())
}

// TestAWorkspacesNoticesAreToldInOrder (design 7.8): one workspace's notices
// are told in the order they happened, each once levyan took the one before
// it, and another workspace's are told meanwhile.
func TestAWorkspacesNoticesAreToldInOrder(t *testing.T) {
	w, fake := billed(t)
	x := w.x
	ctx := context.Background()

	cycled := func(alias string) pdid.Id {
		ws, p := w.workspace(alias)
		w.arrived(fake, ws, p.Id)
		_, err := w.s.Deps.CloseWorkspace(ctx, w.s.Base, w.s.Drv, ws, domain.ClosedByOperator, "")
		x.NoError(err)
		_, err = w.s.Deps.RestoreWorkspace(ctx, w.s.Base, w.s.Drv, ws)
		x.NoError(err)
		return ws
	}
	a := cycled("alpha")
	b := cycled("beta")

	fake.Fail(a, status.Error(codes.Internal, "levyantest: blinked"))
	told, err := w.s.Teller.Tell(ctx)
	x.NoError(err)
	x.Equal(levyan.Told{Told: 2, Waiting: 2}, told)
	x.Empty(kinds(fake, a), "alpha's close not taken, and so neither its restore")
	x.Equal([]string{levyan.Closed, levyan.Restored}, kinds(fake, b))

	fake.Fail(a, nil)
	told, err = w.s.Teller.Tell(ctx)
	x.NoError(err)
	x.Equal(levyan.Told{Told: 2}, told)
	x.Equal([]string{levyan.Closed, levyan.Restored}, kinds(fake, a), "alpha's in the order they happened")
	x.Equal(w.occurred(a), moments(fake, a))
}

// TestWhatBecameOfAWorkspaceLevyanHeardNothingOfIsNotToldAgain (design 7.8):
// levyan answering it heard nothing of a workspace (NotFound) is a workspace
// nobody told it arrived -- the portal's to tell it (D84) -- and so nobody it
// bills: what became of it is marked told with what levyan said, logged, and
// not told again, and another workspace's notices go on.
func TestWhatBecameOfAWorkspaceLevyanHeardNothingOfIsNotToldAgain(t *testing.T) {
	w, fake := billed(t)
	x := w.x
	ctx := context.Background()

	heard, p := w.workspace("heard")
	w.arrived(fake, heard, p.Id)
	unheard, _ := w.workspace("unheard")
	for _, ws := range []pdid.Id{heard, unheard} {
		_, err := w.s.Deps.CloseWorkspace(ctx, w.s.Base, w.s.Drv, ws, domain.ClosedByOperator, "")
		x.NoError(err)
	}

	ctx, said := logged(ctx)
	told, err := w.s.Teller.Tell(ctx)
	x.NoError(err)
	x.Equal(levyan.Told{Told: 1, Refused: 1}, told)
	x.Equal([]string{levyan.Closed}, kinds(fake, heard))
	x.Empty(kinds(fake, unheard))
	vs := w.ends(unheard)
	x.Len(vs, 1)
	x.NotNil(vs[0].DateTold)
	x.Contains(vs[0].Refused, "has not arrived")
	x.Contains(said.String(), "is not told again, since nobody is billed for it")
	x.NotContains(said.String(), "a fault of Loft's")

	told, err = w.s.Teller.Tell(ctx)
	x.NoError(err)
	x.Equal(levyan.Told{}, told, "not told again")
	x.Zero(w.waiting())
}

// TestWhatLevyanRefusesOfAWorkspacesEndIsNotToldAgain (design 7.8): levyan
// refusing what it is told became of a workspace -- a close and a restore of
// one moment, which is a fault of Loft's -- is levyan having answered: the
// notice is marked with what it said, logged, and not told again.
func TestWhatLevyanRefusesOfAWorkspacesEndIsNotToldAgain(t *testing.T) {
	w, fake := billed(t)
	x := w.x
	ctx := context.Background()
	acme := tenantId(x, w.tenant)
	w.arrived(fake, acme, w.memberId(w.firstOwner()))
	x.NoError(w.s.Teller.Pass(ctx))

	// One clock that stands still: closed and restored at one moment.
	at := time.Now().UTC().Truncate(time.Microsecond)
	w.s.Deps.Now = func() time.Time { return at }
	t.Cleanup(func() { w.s.Deps.Now = nil })
	w.closeIt("")
	_, err := w.restoreIt()
	x.NoError(err)
	w.s.Deps.Now = nil

	ctx, said := logged(ctx)
	told, err := w.s.Teller.Tell(ctx)
	x.NoError(err)
	x.Equal(levyan.Told{Told: 1, Refused: 1}, told, "the close, and not the restore of its moment")
	x.Contains(said.String(), "a fault of Loft's")
	vs := w.ends(acme)
	x.Len(vs, 2)
	x.Empty(vs[0].Refused)
	x.Contains(vs[1].Refused, "is not restore then")
	x.NotNil(vs[1].DateTold)

	// And one levyan refuses for a reason of its own.
	_, err = w.s.Deps.CloseWorkspace(ctx, w.s.Base, w.s.Drv, acme, domain.ClosedByOperator, "")
	x.NoError(err)
	fake.Fail(acme, status.Error(codes.InvalidArgument, "levyantest: not that"))
	told, err = w.s.Teller.Tell(ctx)
	x.NoError(err)
	x.Equal(levyan.Told{Refused: 1}, told)
	vs = w.ends(acme)
	x.Len(vs, 3)
	x.Contains(vs[2].Refused, "not that")
	x.NotNil(vs[2].DateTold)

	fake.Fail(acme, nil)
	told, err = w.s.Teller.Tell(ctx)
	x.NoError(err)
	x.Equal(levyan.Told{}, told, "not told again")
	x.Equal([]string{levyan.Closed}, kinds(fake, acme))
}

// TestWhatBecameOfTheWorkspacesBeforeIsToldOnce (design 7.8): the first pass
// of a deployment that comes to tell levyan writes what became of the
// workspaces before it did -- the closes and restores their events keep,
// oldest first; a workspace closed now whose events no longer say so, from
// when it says it was -- and once: nothing it told is told again, by this
// process or the next. A workspace destroyed meanwhile is told nothing:
// nothing of it is left here to say it was (D86).
func TestWhatBecameOfTheWorkspacesBeforeIsToldOnce(t *testing.T) {
	w := newWorld(t)
	x := w.x
	ctx := context.Background()
	acme := tenantId(x, w.tenant)
	destroy := func(id pdid.Id) {
		row, err := w.s.Ent.Tenant.Get(ctx, id.Uuid())
		x.NoError(err)
		_, err = w.s.Closing.Destroy(ctx, row, closing.Purge, false)
		x.NoError(err)
	}

	// What there was before this deployment told levyan of it, told nothing.
	// acme: closed, restored, and closed again.
	w.closeIt("")
	_, err := w.restoreIt()
	x.NoError(err)
	_, err = w.s.Deps.CloseWorkspace(ctx, w.s.Base, w.s.Drv, acme, domain.ClosedByOperator, "")
	x.NoError(err)
	// beta: closed, its history since taken by its window.
	beta, _ := w.workspace("beta")
	_, err = w.s.Deps.CloseWorkspace(ctx, w.s.Base, w.s.Drv, beta, domain.ClosedByOperator, "")
	x.NoError(err)
	_, err = w.s.Ent.Event.Delete().Where(event.TenantId(beta.Uuid())).Exec(ctx)
	x.NoError(err)
	// gamma: destroyed; levyan heard of it.
	gamma, park := w.workspace("gamma")
	gammaMade := w.madeAt(gamma)
	destroy(gamma)
	// delta: destroyed, and levyan heard nothing of it.
	delta, _ := w.workspace("delta")
	destroy(delta)
	// epsilon: closed and restored, open now.
	epsilon, _ := w.workspace("epsilon")
	_, err = w.s.Deps.CloseWorkspace(ctx, w.s.Base, w.s.Drv, epsilon, domain.ClosedByOperator, "")
	x.NoError(err)
	_, err = w.s.Deps.RestoreWorkspace(ctx, w.s.Base, w.s.Drv, epsilon)
	x.NoError(err)
	x.Empty(w.notices(), "nothing written for levyan while nothing bills")

	acmeAt := w.occurred(acme)
	x.Len(acmeAt, 3)
	betaClosed := *must(w.s.Ent.Tenant.Get(ctx, beta.Uuid())).DateClosed

	// What the portal told levyan meanwhile: which workspaces there are.
	fake := levyantest.Start(t)
	client, err := levyan.Dial(fake.Config(), identity.ResolveRef)
	x.NoError(err)
	t.Cleanup(func() { client.Close() })
	owner := w.memberId(w.firstOwner())
	for _, ws := range []pdid.Id{acme, beta, epsilon} {
		fake.Arrived(ws, owner, w.madeAt(ws))
	}
	fake.Arrived(gamma, park.Id, gammaMade)

	// And then it tells levyan.
	teller := &levyan.Teller{Db: w.s.Ent, Levyan: client}
	ctx, said := logged(ctx)
	x.NoError(teller.Pass(ctx))
	x.Contains(said.String(), "what became of the workspaces before it was told of it is to be told it")

	x.Equal([]string{levyan.Closed, levyan.Restored, levyan.Closed}, kinds(fake, acme), "oldest first")
	x.Equal(acmeAt, moments(fake, acme), "of the moments its events say")
	x.Equal([]string{levyan.Closed}, kinds(fake, beta), "closed now, as it says")
	x.Equal([]time.Time{betaClosed.UTC().Truncate(time.Microsecond)}, moments(fake, beta))
	x.Empty(kinds(fake, gamma), "destroyed while nothing billed, and nothing of it left to say so")
	x.Empty(w.ends(gamma))
	x.Empty(kinds(fake, delta))
	x.Empty(w.ends(delta))
	x.Equal([]string{levyan.Closed, levyan.Restored}, kinds(fake, epsilon))
	for _, id := range []pdid.Id{acme, beta, epsilon} {
		for _, v := range w.ends(id) {
			x.Equal(levyan.ByBackfill, v.Origin)
			x.NotNil(v.DateTold)
			x.Empty(v.Refused)
		}
	}
	marks, err := w.s.Ent.LevyanNotice.Query().Where(levyannotice.KindEQ(levyan.Past)).All(ctx)
	x.NoError(err)
	x.Len(marks, 1, "the events read, once")
	x.NotNil(marks[0].DateTold)

	life, err := client.Lifecycle(ctx, acme)
	x.NoError(err)
	x.Equal(acmeAt[2], life.GetDateClosed().AsTime().UTC(), "closed since it was closed last")

	// Once: by this process, and by the next.
	heard := len(fake.Heard())
	notices := len(w.notices())
	x.NoError(teller.Pass(ctx))
	x.NoError((&levyan.Teller{Db: w.s.Ent, Levyan: client}).Pass(ctx), "the next process's first pass")
	x.Len(fake.Heard(), heard, "told once")
	x.Len(w.notices(), notices, "and written once")
}

// TestADeploymentThatBillsNobodyTellsNothingOfAWorkspacesEnd: with
// `app.billing.levyan` unset, closing, restoring and destroying are what they
// were, and nothing is written for levyan.
func TestADeploymentThatBillsNobodyTellsNothingOfAWorkspacesEnd(t *testing.T) {
	w := newWorld(t)
	x := w.x
	ctx := context.Background()

	x.Nil(w.s.Notices)
	x.Nil(w.s.Teller)
	x.Nil(w.s.Deps.Closings)
	x.Nil(w.s.Closing.Notices)
	x.NotContains(spun(w.s), levyan.NoticesName)
	w.closeIt("")
	_, err := w.restoreIt()
	x.NoError(err)
	w.closed()
	x.NoError(w.s.Closing.Sweep(ctx))
	x.True(w.gone())
	x.Empty(w.notices())
}

// firstOwner is the world's workspace's first owner.
func (w *world) firstOwner() *app.Holder {
	h, err := w.s.Ent.Holder.Query().
		Where(holder.TenantId(tenantId(w.x, w.tenant).Uuid()), holder.Role("owner")).
		Order(ent.Asc(holder.FieldDateCreated)).
		First(context.Background())
	w.x.NoError(err)
	return app.Holder_builder{Id: pdid.Id(h.Id).Bytes()}.Build()
}

// must is v, which err says nothing went wrong in making.
func must[T any](v T, err error) T {
	if err != nil {
		panic(err)
	}
	return v
}
