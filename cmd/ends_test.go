package cmd_test

import (
	"context"
	"testing"
	"time"

	levyanv1 "github.com/lesomnus/levyan/api/levyan/v1"
	"github.com/lesomnus/payday/pdid"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	app "github.com/lesomnus/loft"
	"github.com/lesomnus/loft/internal/ent"
	"github.com/lesomnus/loft/internal/ent/aliashold"
	"github.com/lesomnus/loft/internal/ent/event"
	"github.com/lesomnus/loft/internal/ent/levyannotice"
	"github.com/lesomnus/loft/internal/identity"
	"github.com/lesomnus/loft/internal/levyantest"
	"github.com/lesomnus/loft/server/closing"
	"github.com/lesomnus/loft/server/domain"
	"github.com/lesomnus/loft/server/levyan"
	"github.com/lesomnus/loft/server/pd"
)

// What became of a workspace -- closed, restored, destroyed -- as levyan is
// told it (design 7.8, levyan's `docs/apps.md` 5).

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
		Order(ent.Asc(levyannotice.FieldAt), ent.Asc(levyannotice.FieldId)).
		All(context.Background())
	w.x.NoError(err)
	return vs
}

// occurred is when the events say `ws` was closed and restored, in order.
func (w *world) occurred(ws pdid.Id) []time.Time {
	vs, err := w.s.Ent.Event.Query().
		Where(event.TenantId(ws.Uuid()), event.KindIn("workspace.close", "workspace.restore")).
		Order(ent.Asc(event.FieldOccurredAt), ent.Asc(event.FieldId)).
		All(context.Background())
	w.x.NoError(err)
	out := []time.Time{}
	for _, v := range vs {
		out = append(out, v.OccurredAt.UTC().Truncate(time.Microsecond))
	}
	return out
}

func ats(vs []*ent.LevyanNotice) []time.Time {
	out := []time.Time{}
	for _, v := range vs {
		out = append(out, v.At.UTC())
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
// by its owner, an operator, or the deployment when an account leaving left
// nobody in it -- restored, and destroyed, by the sweep or an operator's hand,
// is written in the transaction that does it, or before a destruction takes
// anything, of the moment it happened; and told to levyan once, after the
// workspace's arrival, in the order it happened. A dry run tells nothing.
func TestEveryWayAWorkspaceEndsIsToldToLevyan(t *testing.T) {
	w, fake := billed(t)
	x := w.x
	ctx := context.Background()
	tenants := app.NewTenantServiceClient(w.conn)
	acme := tenantId(x, w.tenant)
	ref := app.TenantRef_builder{Id: w.tenant.GetId()}.Build()

	// The world's own workspace, made past every way an account makes one,
	// arrives by the first pass.
	x.NoError(w.s.Teller.Pass(ctx))
	x.Equal([]string{levyan.Arrived}, kinds(fake, acme))

	// Its owner closes it, and restores it; an operator does, too.
	v, err := tenants.Close(w.owner, app.TenantCloseRequest_builder{Ref: ref, Confirm: w.called(), Reason: "잠시 닫습니다"}.Build())
	x.NoError(err)
	closedAt := v.GetDateClosed().AsTime().UTC()
	_, err = tenants.Restore(w.owner, app.TenantRestoreRequest_builder{Ref: ref}.Build())
	x.NoError(err)
	_, err = w.s.Deps.CloseWorkspace(ctx, w.s.Base, w.s.Drv, acme, domain.ClosedByOperator, "미납")
	x.NoError(err)
	_, err = w.s.Deps.RestoreWorkspace(ctx, w.s.Base, w.s.Drv, acme)
	x.NoError(err)
	vs := w.ends(acme)
	x.Equal([]string{levyan.Closed, levyan.Restored, levyan.Closed, levyan.Restored}, kindsOf(vs))
	x.Equal(closedAt, vs[0].At.UTC(), "of the moment the workspace said it was closed")
	x.Equal(w.occurred(acme), ats(vs), "each of the moment its event says")
	for _, v := range vs {
		x.Equal(levyan.ByChange, v.Origin)
		x.Equal(pdid.Nil.Uuid(), v.Account, "of no account")
		x.Nil(v.DateTold, "told when the teller tells it, and not before")
	}

	// An account leaving the one workspace it was in: nobody is left, and the
	// deployment closes it. Its window over, the sweep destroys it.
	kim, err := w.s.Identity.AddPerson(ctx, "kim", "김", pdid.Nil)
	x.NoError(err)
	alone, err := w.s.Workspaces(w.c).Create(ctx, kim, "김의 워크스페이스")
	x.NoError(err)
	_, err = w.s.Lifecycle.Depart(ctx, kim.Id)
	x.NoError(err)
	x.Equal([]string{levyan.Closed}, kindsOf(w.ends(alone.Workspace)))
	x.NoError(w.s.Ent.Tenant.UpdateOneId(alone.Workspace.Uuid()).SetDateDestroy(time.Now().Add(-time.Minute)).Exec(ctx))
	x.NoError(w.s.Closing.Sweep(ctx))

	// And an operator's hand, on a workspace that is open: said first, and
	// then done.
	lee, err := w.s.Identity.AddPerson(ctx, "lee", "이", pdid.Nil)
	x.NoError(err)
	theirs, err := w.s.Workspaces(w.c).Create(ctx, lee, "연구실")
	x.NoError(err)
	row, err := w.s.Ent.Tenant.Get(ctx, theirs.Workspace.Uuid())
	x.NoError(err)
	_, err = w.s.Closing.Destroy(ctx, row, closing.Purge, true)
	x.NoError(err)
	x.Empty(w.ends(theirs.Workspace), "a dry run tells nothing")
	_, err = w.s.Closing.Destroy(ctx, row, closing.Purge, false)
	x.NoError(err)

	for _, ws := range []pdid.Id{alone.Workspace, theirs.Workspace} {
		vs := w.ends(ws)
		d := vs[len(vs)-1]
		x.Equal(levyan.Destroyed, d.Kind)
		hold, err := w.s.Ent.AliasHold.Query().Where(aliashold.Tenant(ws.Uuid())).Only(ctx)
		x.NoError(err)
		x.Equal(hold.DateCreated.UTC().Truncate(time.Microsecond), d.At.UTC(), "the moment its address is held from")
		read := false
		for _, r := range fake.Readings() {
			if r.GetWorkspace() == ws.String() {
				read = true
				x.True(r.GetMeasuredAt().AsTime().Before(d.At), "after its final reading")
			}
		}
		x.True(read, "its final reading, handed to levyan first")
	}

	told, err := w.s.Teller.Tell(ctx)
	x.NoError(err)
	x.Equal(levyan.Told{Told: 10}, told)
	x.Equal([]string{levyan.Arrived, levyan.Closed, levyan.Restored, levyan.Closed, levyan.Restored}, kinds(fake, acme))
	x.Equal(w.occurred(acme), moments(fake, acme), "of the moments they happened")
	x.Equal([]string{levyan.Arrived, levyan.Left, levyan.Closed, levyan.Destroyed}, kinds(fake, alone.Workspace), "after its arrival")
	x.Equal([]string{levyan.Arrived, levyan.Destroyed}, kinds(fake, theirs.Workspace))
	x.Zero(w.waiting())

	life, err := w.s.Levyan.Lifecycle(ctx, acme)
	x.NoError(err)
	x.False(life.HasClosedSince(), "open, as it was last restored")
	x.False(life.HasDestroyed())
	life, err = w.s.Levyan.Lifecycle(ctx, alone.Workspace)
	x.NoError(err)
	x.Equal(w.ends(alone.Workspace)[0].At.UTC(), life.GetClosedSince().AsTime().UTC(), "destroyed as it was closed")
	x.True(life.HasDestroyed())

	// Told once.
	told, err = w.s.Teller.Tell(ctx)
	x.NoError(err)
	x.Equal(levyan.Told{}, told)
	x.Len(fake.Heard(), 11)
}

// TestAWorkspacesEndWaitsForALevyanThatIsDown (design 7.8): nothing of
// closing, restoring or destroying waits for levyan or fails for it; what it
// is to be told waits, and is told once it answers, in the order it happened.
func TestAWorkspacesEndWaitsForALevyanThatIsDown(t *testing.T) {
	w, fake := billed(t)
	x := w.x
	ctx := context.Background()
	tenants := app.NewTenantServiceClient(w.conn)
	acme := tenantId(x, w.tenant)
	ref := app.TenantRef_builder{Id: w.tenant.GetId()}.Build()
	x.NoError(w.s.Teller.Pass(ctx))

	fake.Down(status.Error(codes.Unavailable, "levyantest: down"))
	_, err := tenants.Close(w.owner, app.TenantCloseRequest_builder{Ref: ref, Confirm: w.called()}.Build())
	x.NoError(err, "closed, with levyan down")
	_, err = tenants.Restore(w.owner, app.TenantRestoreRequest_builder{Ref: ref}.Build())
	x.NoError(err, "restored")
	lee, err := w.s.Identity.AddPerson(ctx, "lee", "이", pdid.Nil)
	x.NoError(err)
	theirs, err := w.s.Workspaces(w.c).Create(ctx, lee, "연구실")
	x.NoError(err)
	row, err := w.s.Ent.Tenant.Get(ctx, theirs.Workspace.Uuid())
	x.NoError(err)
	_, err = w.s.Closing.Destroy(ctx, row, closing.Purge, false)
	x.NoError(err, "destroyed")

	told, err := w.s.Teller.Tell(ctx)
	x.Equal(codes.Unavailable, status.Code(err))
	x.Equal(levyan.Told{Waiting: 4}, told)
	x.NoError(w.s.Teller.Pass(ctx), "a pass that could not tell levyan stops nothing")
	x.Equal(4, w.waiting())

	fake.Down(nil)
	told, err = w.s.Teller.Tell(ctx)
	x.NoError(err)
	x.Equal(levyan.Told{Told: 4}, told)
	x.Equal([]string{levyan.Arrived, levyan.Closed, levyan.Restored}, kinds(fake, acme))
	x.Equal(w.occurred(acme), moments(fake, acme), "of the moments they happened, and not of their telling")
	x.Equal([]string{levyan.Arrived, levyan.Destroyed}, kinds(fake, theirs.Workspace))
	x.Zero(w.waiting())
}

// TestALevyanThatHeardNothingOfAWorkspaceIsToldItArrivedFirst (design 7.8):
// what became of a workspace levyan answers it heard nothing of (NotFound) is
// told again once levyan is told the workspace arrived -- as it was told
// before, or by its first owner -- and one destroyed before levyan heard of
// it is nobody levyan bills, and is not told.
func TestALevyanThatHeardNothingOfAWorkspaceIsToldItArrivedFirst(t *testing.T) {
	w, fake := billed(t)
	x := w.x
	ctx := context.Background()
	ws := w.s.Workspaces(w.c)
	made := func(alias string) (pdid.Id, identity.Person) {
		p, err := w.s.Identity.AddPerson(ctx, alias, alias, pdid.Nil)
		x.NoError(err)
		m, err := ws.Create(ctx, p, alias)
		x.NoError(err)
		return m.Workspace, p
	}
	closed := func(id pdid.Id) {
		_, err := w.s.Deps.CloseWorkspace(ctx, w.s.Base, w.s.Drv, id, domain.ClosedByOperator, "")
		x.NoError(err)
	}
	arrivals := func(id pdid.Id) []*ent.LevyanNotice {
		vs, err := w.s.Ent.LevyanNotice.Query().
			Where(levyannotice.Workspace(id.Uuid()), levyannotice.KindEQ(levyan.Arrived)).
			Order(ent.Asc(levyannotice.FieldDateCreated), ent.Asc(levyannotice.FieldId)).
			All(ctx)
		x.NoError(err)
		return vs
	}

	// A levyan made again knows nothing of what it was told: it is told the
	// workspace arrived as it was told before, and then that it was closed.
	again, park := made("park")
	// Its arrival refused when it was told: by its first owner, now.
	refused, lee := made("lee")
	// And one destroyed before levyan heard of it: its arrival refused, and
	// its final reading never handed over.
	gone, _ := made("choi")
	fake.Fail(refused, status.Error(codes.InvalidArgument, "levyantest: not now"))
	fake.Fail(gone, status.Error(codes.InvalidArgument, "levyantest: not now"))
	told, err := w.s.Teller.Tell(ctx)
	x.NoError(err)
	x.Equal(levyan.Told{Told: 1, Refused: 2}, told)
	fake.Fail(refused, nil)
	fake.Fail(gone, nil)
	fake.Forget(again)

	closed(again)
	closed(refused)
	fake.Answer(func(context.Context, *levyanv1.RecordRequest) (*levyanv1.RecordResponse, error) {
		return nil, status.Error(codes.Unavailable, "levyantest: down for the reading")
	})
	row, err := w.s.Ent.Tenant.Get(ctx, gone.Uuid())
	x.NoError(err)
	d, err := w.s.Closing.Destroy(ctx, row, closing.Purge, false)
	x.NoError(err)
	x.Error(d.Unmeasured)
	fake.Answer(nil)

	ctx, said := logged(ctx)
	told, err = w.s.Teller.Tell(ctx)
	x.NoError(err)
	x.Equal(levyan.Told{Told: 4, Refused: 1}, told)

	x.Equal([]string{levyan.Arrived, levyan.Arrived, levyan.Closed}, kinds(fake, again), "its arrival again, then the close")
	vs := arrivals(again)
	x.Len(vs, 2)
	x.Equal(levyan.ByNotFound, vs[1].Origin)
	x.Equal(vs[0].Account, vs[1].Account, "as levyan was told it before")
	x.Equal(park.Id.Uuid(), vs[1].Account)
	x.Equal(vs[0].At, vs[1].At, "from when it was made")
	x.NotNil(vs[1].DateTold)

	x.Equal([]string{levyan.Arrived, levyan.Closed}, kinds(fake, refused), "its arrival by its first owner, then the close")
	vs = arrivals(refused)
	x.Len(vs, 2)
	x.NotEmpty(vs[0].Refused)
	x.Equal(levyan.ByNotFound, vs[1].Origin)
	x.Equal(lee.Id.Uuid(), vs[1].Account)
	x.Equal(w.madeAt(refused), vs[1].At.UTC())

	x.Empty(kinds(fake, gone), "nobody levyan bills")
	es := w.ends(gone)
	x.Len(es, 1)
	x.NotNil(es[0].DateTold)
	x.Contains(es[0].Refused, "it is gone, and levyan was never told it arrived")
	x.Contains(said.String(), "is not told, since nobody is billed for it")
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
	tenants := app.NewTenantServiceClient(w.conn)
	acme := tenantId(x, w.tenant)
	ref := app.TenantRef_builder{Id: w.tenant.GetId()}.Build()
	x.NoError(w.s.Teller.Pass(ctx))

	// One clock that stands still: closed and restored at one moment.
	at := time.Now().UTC().Truncate(time.Microsecond)
	w.s.Deps.Now = func() time.Time { return at }
	t.Cleanup(func() { w.s.Deps.Now = nil })
	_, err := tenants.Close(w.owner, app.TenantCloseRequest_builder{Ref: ref, Confirm: w.called()}.Build())
	x.NoError(err)
	_, err = tenants.Restore(w.owner, app.TenantRestoreRequest_builder{Ref: ref}.Build())
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
	x.Equal([]string{levyan.Arrived, levyan.Closed}, kinds(fake, acme))
}

// TestWhatBecameOfTheWorkspacesBeforeIsToldOnce (design 7.8): the first pass
// of a deployment that comes to tell levyan writes what became of the
// workspaces before it did -- the closes and restores their events keep,
// oldest first; a workspace closed now whose events no longer say so, from
// when it says it was; a workspace destroyed that levyan was told arrived,
// from when its address was held -- after each workspace's arrival, and
// once: nothing it told is told again, by this process or the next.
func TestWhatBecameOfTheWorkspacesBeforeIsToldOnce(t *testing.T) {
	w := newWorld(t)
	x := w.x
	ctx := context.Background()
	tenants := app.NewTenantServiceClient(w.conn)
	acme := tenantId(x, w.tenant)
	ref := app.TenantRef_builder{Id: w.tenant.GetId()}.Build()
	ws := w.s.Workspaces(w.c)
	made := func(alias string) (pdid.Id, identity.Person) {
		p, err := w.s.Identity.AddPerson(ctx, alias, alias, pdid.Nil)
		x.NoError(err)
		m, err := ws.Create(ctx, p, alias)
		x.NoError(err)
		return m.Workspace, p
	}
	destroy := func(id pdid.Id) {
		row, err := w.s.Ent.Tenant.Get(ctx, id.Uuid())
		x.NoError(err)
		_, err = w.s.Closing.Destroy(ctx, row, closing.Purge, false)
		x.NoError(err)
	}

	// What there was before this deployment told levyan of it, told nothing.
	// acme: closed, restored, and closed again.
	_, err := tenants.Close(w.owner, app.TenantCloseRequest_builder{Ref: ref, Confirm: w.called()}.Build())
	x.NoError(err)
	_, err = tenants.Restore(w.owner, app.TenantRestoreRequest_builder{Ref: ref}.Build())
	x.NoError(err)
	_, err = w.s.Deps.CloseWorkspace(ctx, w.s.Base, w.s.Drv, acme, domain.ClosedByOperator, "")
	x.NoError(err)
	// beta: closed, its history since taken by its window.
	beta, _ := made("beta")
	_, err = w.s.Deps.CloseWorkspace(ctx, w.s.Base, w.s.Drv, beta, domain.ClosedByOperator, "")
	x.NoError(err)
	_, err = w.s.Ent.Event.Delete().Where(event.TenantId(beta.Uuid())).Exec(ctx)
	x.NoError(err)
	// gamma: told arrived by the build before, and destroyed.
	fake := levyantest.Start(t)
	client, err := levyan.Dial(fake.Config(), identity.ResolveRef)
	x.NoError(err)
	t.Cleanup(func() { client.Close() })
	gamma, park := made("gamma")
	x.NoError(client.Arrive(ctx, gamma, park.Id, w.madeAt(gamma)))
	x.NoError((&levyan.Notices{}).Arrive(ctx, w.s.Ent, gamma, park.Id, w.madeAt(gamma)))
	x.NoError(w.s.Ent.LevyanNotice.Update().Where(levyannotice.Workspace(gamma.Uuid())).SetDateTold(time.Now()).Exec(ctx))
	destroy(gamma)
	// delta: destroyed, and levyan never told it arrived.
	delta, _ := made("delta")
	destroy(delta)
	// epsilon: closed and restored, open now; and its address held as a
	// destroyed one's is, as a workspace put back from its export is.
	epsilon, _ := made("epsilon")
	_, err = w.s.Deps.CloseWorkspace(ctx, w.s.Base, w.s.Drv, epsilon, domain.ClosedByOperator, "")
	x.NoError(err)
	_, err = w.s.Deps.RestoreWorkspace(ctx, w.s.Base, w.s.Drv, epsilon)
	x.NoError(err)
	x.NoError(w.s.Ent.AliasHold.Create().
		SetId(pdid.New(pd.AliasHoldDomain).Uuid()).SetHeld("epsilon-was").SetReason("destroyed").SetMovedTo("").SetTenant(epsilon.Uuid()).
		SetDateUntil(time.Now().Add(time.Hour)).SetDateCreated(time.Now()).SetDateUpdated(time.Now()).
		Exec(ctx))
	x.Len(w.notices(), 1, "nothing written for levyan while nothing bills, but what the build before told")

	acmeAt := w.occurred(acme)
	x.Len(acmeAt, 3)
	betaClosed := *must(w.s.Ent.Tenant.Get(ctx, beta.Uuid())).DateClosed
	gammaHeld := must(w.s.Ent.AliasHold.Query().Where(aliashold.Tenant(gamma.Uuid())).Only(ctx)).DateCreated

	// And then it tells levyan.
	teller := &levyan.Teller{Db: w.s.Ent, Levyan: client}
	ctx, said := logged(ctx)
	x.NoError(teller.Pass(ctx))
	x.Contains(said.String(), "what became of the workspaces before it was told of it is to be told it")

	x.Equal([]string{levyan.Arrived, levyan.Closed, levyan.Restored, levyan.Closed}, kinds(fake, acme), "after its arrival, oldest first")
	x.Equal(acmeAt, moments(fake, acme), "of the moments its events say")
	x.Equal([]string{levyan.Arrived, levyan.Closed}, kinds(fake, beta), "closed now, as it says")
	x.Equal([]time.Time{betaClosed.UTC().Truncate(time.Microsecond)}, moments(fake, beta))
	x.Equal([]string{levyan.Arrived, levyan.Destroyed}, kinds(fake, gamma), "levyan was told it arrived")
	x.Equal([]time.Time{gammaHeld.UTC().Truncate(time.Microsecond)}, moments(fake, gamma), "from when its address was held")
	x.Empty(kinds(fake, delta), "levyan never heard of it")
	x.Equal([]string{levyan.Arrived, levyan.Closed, levyan.Restored}, kinds(fake, epsilon), "not destroyed now, and not told it was")
	for _, id := range []pdid.Id{acme, beta, gamma, epsilon} {
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
	x.Equal(acmeAt[2], life.GetClosedSince().AsTime().UTC(), "closed since it was closed last")

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
	tenants := app.NewTenantServiceClient(w.conn)
	ref := app.TenantRef_builder{Id: w.tenant.GetId()}.Build()

	x.Nil(w.s.Deps.Closings)
	x.Nil(w.s.Closing.Notices)
	_, err := tenants.Close(w.owner, app.TenantCloseRequest_builder{Ref: ref, Confirm: w.called()}.Build())
	x.NoError(err)
	_, err = tenants.Restore(w.owner, app.TenantRestoreRequest_builder{Ref: ref}.Build())
	x.NoError(err)
	w.closed()
	x.NoError(w.s.Closing.Sweep(ctx))
	x.True(w.gone())
	x.Empty(w.notices())
}

// must is v, which err says nothing went wrong in making.
func must[T any](v T, err error) T {
	if err != nil {
		panic(err)
	}
	return v
}
