package cmd_test

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/lesomnus/z"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/lesomnus/payday/pdid"

	app "github.com/lesomnus/loft"
	"github.com/lesomnus/loft/cmd"
	"github.com/lesomnus/loft/internal/ent"
	"github.com/lesomnus/loft/internal/ent/holder"
	"github.com/lesomnus/loft/internal/ent/levyannotice"
	"github.com/lesomnus/loft/internal/identity"
	"github.com/lesomnus/loft/internal/levyantest"
	"github.com/lesomnus/loft/server/account"
	"github.com/lesomnus/loft/server/levyan"
	"github.com/lesomnus/loft/server/session"
)

// notices is every notice for levyan, in the order the teller tells them:
// not the first pass's mark, which is no notice ([levyan.Past]).
func (w *world) notices() []*ent.LevyanNotice {
	vs, err := w.s.Ent.LevyanNotice.Query().
		Where(levyannotice.KindNEQ(levyan.Past)).
		Order(ent.Asc(levyannotice.FieldAt), ent.Asc(levyannotice.FieldId)).
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

// heard is what levyan was told, as "kind workspace account".
func heard(fake *levyantest.Levyan) []string {
	out := []string{}
	for _, v := range fake.Heard() {
		out = append(out, v.Kind+" "+v.Workspace+" "+v.Account)
	}
	return out
}

func said(kind string, ws, account pdid.Id) string {
	return kind + " " + ws.String() + " " + account.String()
}

// madeAt is when a workspace was made, as its row says.
func (w *world) madeAt(id pdid.Id) time.Time {
	v, err := w.s.Ent.Tenant.Get(context.Background(), id.Uuid())
	w.x.NoError(err)
	return v.DateCreated.UTC().Truncate(time.Microsecond)
}

// TestAWorkspaceIsToldToLevyanByWhoMadeIt (design 7.6): every way a
// workspace is made writes, in the transaction that makes it, that levyan is
// to be told it arrived -- made by whom, and when, which is who pays for it --
// and the teller tells it: the one an account is given when it signs in, one
// it makes over HTTP, and one it makes otherwise.
func TestAWorkspaceIsToldToLevyanByWhoMadeIt(t *testing.T) {
	w, fake := billed(t)
	x := w.x
	ctx := context.Background()

	park, err := w.s.Identity.AddPerson(ctx, "park", "박", pdid.Nil)
	x.NoError(err)
	x.NoError(w.s.Identity.SetPassword(ctx, park.Id, "correct horse"))
	ws := w.s.Workspaces(w.c)
	x.NotNil(ws.Told)

	// Signing in, it is given one of its own.
	login := session.SignIn(w.s.Sessions, w.s.Identity, w.s.Ent, ws.Arrive)
	r := httptest.NewRequest(http.MethodPost, "/session", bytes.NewBufferString(`{"login":"park","password":"correct horse"}`))
	r.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	login.ServeHTTP(rec, r)
	x.Equal(http.StatusNoContent, rec.Code)
	cookie := rec.Result().Cookies()[0]

	// And makes another, at `POST /account/workspaces`.
	acc := http.NewServeMux()
	account.Routes{Sessions: w.s.Sessions, People: w.s.Identity, Workspaces: ws, Export: func(context.Context, pdid.Id, io.Writer) error { return nil }}.Mount(acc)
	r = httptest.NewRequest(http.MethodPost, "/account/workspaces", bytes.NewBufferString(`{"name":"연구실"}`))
	r.Header.Set("Cookie", cookie.Name+"="+cookie.Value)
	r.Header.Set("Content-Type", "application/json")
	rec = httptest.NewRecorder()
	acc.ServeHTTP(rec, r)
	x.Equal(http.StatusCreated, rec.Code, rec.Body.String())

	// And one more, as anything else that makes one does.
	lee, err := w.s.Identity.AddPerson(ctx, "lee", "이", pdid.Nil)
	x.NoError(err)
	made, err := ws.Create(ctx, lee, "")
	x.NoError(err)

	mine, err := ws.Of(ctx, park.Id)
	x.NoError(err)
	x.Len(mine, 2)
	want := []string{
		said(levyan.Arrived, mine[0].Workspace, park.Id),
		said(levyan.Arrived, mine[1].Workspace, park.Id),
		said(levyan.Arrived, made.Workspace, lee.Id),
	}
	got := []string{}
	for _, v := range w.notices() {
		got = append(got, said(v.Kind, pdid.Id(v.Workspace), pdid.Id(v.Account)))
		x.Equal(levyan.ByChange, v.Origin)
		x.Nil(v.DateTold, "told when the teller tells it, and not before")
		x.Equal(w.madeAt(pdid.Id(v.Workspace)), v.At.UTC(), "from when it was made")
	}
	x.Equal(want, got, "the world's own workspace was made past every way an account makes one: the backfill's")

	n, err := w.s.Teller.Backfill(ctx)
	x.NoError(err)
	x.Equal(1, n)
	x.Len(w.notices(), 4)
	told, err := w.s.Teller.Tell(ctx)
	x.NoError(err)
	x.Equal(levyan.Told{Told: 4}, told)
	x.Zero(w.waiting())
	for _, v := range fake.Heard() {
		id, err := pdid.Parse(v.Workspace)
		x.NoError(err)
		x.Equal(w.madeAt(id), v.Created.UTC(), "levyan is told when it was made")
	}
	x.Subset(heard(fake), want)
}

// TestTheWorkspacesMadeBeforeAreToldOnce (design 7.6): the teller's first
// pass tells levyan of every workspace it was never told of -- made before
// this deployment told it of any -- each by its first owner, from when it was
// made, and says so; one nobody owns is nobody's to pay for, and is not told.
// It is done once: told, a workspace is not told again, by this process or
// the next.
func TestTheWorkspacesMadeBeforeAreToldOnce(t *testing.T) {
	w, fake := billed(t)
	x := w.x
	ctx := context.Background()

	acme := tenantId(x, w.tenant)
	owner := w.firstOwner()
	time.Sleep(time.Millisecond)
	w.holder("lee", "owner")
	_, err := w.s.Base.Tenant().Add(ctx, app.TenantAddRequest_builder{Alias: "nobody"}.Build())
	x.NoError(err)

	x.NoError(w.s.Teller.Pass(ctx))
	x.Equal([]string{said(levyan.Arrived, acme, owner)}, heard(fake), "by its first owner, who has owned it longest; and nothing of the one nobody owns")
	vs := w.notices()
	x.Len(vs, 1)
	x.Equal(levyan.ByBackfill, vs[0].Origin)
	x.NotNil(vs[0].DateTold)
	x.Equal(w.madeAt(acme), fake.Heard()[0].Created.UTC())

	x.NoError(w.s.Teller.Pass(ctx))
	again := &levyan.Teller{Db: w.s.Ent, Levyan: w.s.Levyan}
	x.NoError(again.Pass(ctx), "the next process's first pass")
	x.Len(fake.Heard(), 1, "told once")
	x.Len(w.notices(), 1)
}

// TestEveryMembershipThatEndsIsToldToLevyan (design 7.6): an account no
// longer in a workspace -- 로그인 중지, 개인정보 삭제, an account leaving the
// deployment -- is written, with the write that ends its membership, as an
// account levyan is to be told left. roster erasing an account only marks it
// leaving, which is not that: it is told when the account does leave. And a
// workspace an account leaving left nobody in is closed, which levyan is
// told too (design 7.8).
func TestEveryMembershipThatEndsIsToldToLevyan(t *testing.T) {
	w, fake := billed(t)
	x := w.x
	ctx := context.Background()
	parties := app.NewPartyServiceClient(w.conn)
	acme := tenantId(x, w.tenant)

	kim := w.holder("kim", "member")
	lee := w.holder("lee", "member")
	park := w.holder("park", "member")
	beta, err := w.s.Base.Tenant().Add(ctx, app.TenantAddRequest_builder{Alias: "beta"}.Build())
	x.NoError(err)
	parkAccount := w.accountOf(park)
	parkPerson, err := w.s.Identity.ById(ctx, parkAccount.String())
	x.NoError(err)
	w.member(beta, parkPerson, "owner")
	choi := w.holder("choi", "member")
	nobody, err := w.s.Base.Holder().Add(ctx, app.HolderAddRequest_builder{
		Tenant: app.TenantRef_builder{Id: w.tenant.GetId()}.Build(),
		Alias:  "old", Role: z.Ptr("member"),
	}.Build())
	x.NoError(err)
	x.Empty(w.notices())

	_, err = parties.Deactivate(w.owner, app.PartyDeactivateRequest_builder{Ref: app.PartyRef_builder{Id: partyOf(w, kim).GetId()}.Build()}.Build())
	x.NoError(err)
	_, err = parties.Pseudonymize(w.owner, app.PartyPseudonymizeRequest_builder{Ref: app.PartyRef_builder{Id: partyOf(w, lee).GetId()}.Build()}.Build())
	x.NoError(err)
	_, err = w.s.Lifecycle.Depart(ctx, parkAccount)
	x.NoError(err)

	// Erased at roster: leaving, and not gone, until roster is asked again.
	choiAccount := w.accountOf(choi)
	x.NoError(w.s.Lifecycle.Apply(ctx, identity.Standing{Holder: choiAccount, Erased: time.Now()}))
	got := []string{}
	for _, v := range w.notices() {
		got = append(got, said(v.Kind, pdid.Id(v.Workspace), pdid.Id(v.Account)))
	}
	x.ElementsMatch([]string{
		said(levyan.Left, acme, w.accountOf(kim)),
		said(levyan.Left, acme, w.accountOf(lee)),
		said(levyan.Left, acme, parkAccount),
		said(levyan.Left, tenantId(x, beta), parkAccount),
		said(levyan.Closed, tenantId(x, beta), pdid.Nil),
	}, got, "nothing of an account roster only marked; and beta, nobody left in it, closed")

	// And then it leaves.
	_, err = w.s.Lifecycle.Depart(ctx, choiAccount)
	x.NoError(err)
	// A membership of no account is nobody levyan knows.
	_, err = w.s.Base.Holder().Erase(ctx, app.HolderRef_builder{Id: nobody.GetId()}.Build())
	x.NoError(err)
	vs := w.notices()
	x.Len(vs, 6)
	x.Equal(said(levyan.Left, acme, choiAccount), said(vs[5].Kind, pdid.Id(vs[5].Workspace), pdid.Id(vs[5].Account)))

	// beta was made past every way an account makes one, and so never told
	// arrived, and nobody owns it now to say it did: levyan bills nobody for
	// it, and is told nothing of it.
	told, err := w.s.Teller.Tell(ctx)
	x.NoError(err)
	x.Equal(levyan.Told{Told: 5, Refused: 1}, told)
	x.Len(fake.Heard(), 5)
	for _, v := range w.notices() {
		if v.Kind == levyan.Closed {
			x.Contains(v.Refused, "no account owns it")
		}
	}
}

// TestNoticesWaitForALevyanThatIsDown (design 7.6): a levyan that cannot be
// reached fails nothing of Loft's -- a workspace is made, a membership ends --
// and what it is to be told waits, and is told once it answers, each
// workspace's in the order it happened.
func TestNoticesWaitForALevyanThatIsDown(t *testing.T) {
	w, fake := billed(t)
	x := w.x
	ctx := context.Background()

	// What there was before, told.
	x.NoError(w.s.Teller.Pass(ctx))
	x.Len(fake.Heard(), 1)

	fake.Down(status.Error(codes.Unavailable, "levyantest: down"))
	lee, err := w.s.Identity.AddPerson(ctx, "lee", "이", pdid.Nil)
	x.NoError(err)
	made, err := w.s.Workspaces(w.c).Create(ctx, lee, "연구실")
	x.NoError(err, "made, with levyan down")
	kim, err := w.s.Identity.AddPerson(ctx, "kim", "김", pdid.Nil)
	x.NoError(err)
	h := w.member(app.Tenant_builder{Id: made.Workspace.Bytes()}.Build(), kim, "member")
	_, err = w.s.Base.Holder().Erase(ctx, app.HolderRef_builder{Id: h.GetId()}.Build())
	x.NoError(err, "a membership ended, with levyan down")

	told, err := w.s.Teller.Tell(ctx)
	x.Equal(codes.Unavailable, status.Code(err))
	x.Equal(levyan.Told{Waiting: 2}, told)
	x.NoError(w.s.Teller.Pass(ctx), "a pass that could not tell levyan is a pass to try again, and stops nothing")
	x.Equal(2, w.waiting())

	x.Len(fake.Heard(), 1)

	fake.Down(nil)
	told, err = w.s.Teller.Tell(ctx)
	x.NoError(err)
	x.Equal(levyan.Told{Told: 2}, told)
	x.Equal([]string{said(levyan.Arrived, made.Workspace, lee.Id), said(levyan.Left, made.Workspace, kim.Id)}, heard(fake)[1:])
	x.Zero(w.waiting())

	// Nobody there at all: the same.
	gone := newWorldWith(t, func(c *cmd.Config) { c.App.Billing.Levyan = levyantest.Gone(t) })
	choi, err := gone.s.Identity.AddPerson(ctx, "choi", "최", pdid.Nil)
	gone.x.NoError(err)
	_, err = gone.s.Workspaces(gone.c).Create(ctx, choi, "")
	gone.x.NoError(err)
	_, err = gone.s.Teller.Tell(ctx)
	gone.x.Equal(codes.Unavailable, status.Code(err))
	gone.x.Equal(1, gone.waiting())
}

// TestAWorkspacesNoticesAreToldInOrder (design 7.6): one workspace's notices
// are told in the order they happened, each once levyan took the one before
// it -- so that levyan never hears an account left a workspace it was not
// told arrived -- and another workspace's are told meanwhile. A notice levyan
// refuses is one it would refuse again: told, with what it said, and the
// workspace goes on.
func TestAWorkspacesNoticesAreToldInOrder(t *testing.T) {
	w, fake := billed(t)
	x := w.x
	ctx := context.Background()
	ws := w.s.Workspaces(w.c)

	madeOne := func(alias string) (pdid.Id, pdid.Id) {
		p, err := w.s.Identity.AddPerson(ctx, alias, alias, pdid.Nil)
		x.NoError(err)
		m, err := ws.Create(ctx, p, alias)
		x.NoError(err)
		kim, err := w.s.Identity.AddPerson(ctx, alias+"-kim", "kim", pdid.Nil)
		x.NoError(err)
		h := w.member(app.Tenant_builder{Id: m.Workspace.Bytes()}.Build(), kim, "member")
		_, err = w.s.Base.Holder().Erase(ctx, app.HolderRef_builder{Id: h.GetId()}.Build())
		x.NoError(err)
		return m.Workspace, kim.Id
	}
	a, aLeft := madeOne("alpha")
	b, bLeft := madeOne("beta")
	c, _ := madeOne("gamma")
	creator := func(ws pdid.Id) pdid.Id {
		for _, v := range w.notices() {
			if pdid.Id(v.Workspace) == ws && v.Kind == levyan.Arrived {
				return pdid.Id(v.Account)
			}
		}
		return pdid.Nil
	}

	fake.Fail(a, status.Error(codes.Internal, "levyantest: blinked"))
	fake.Fail(c, status.Error(codes.InvalidArgument, "levyantest: not one of ours"))
	told, err := w.s.Teller.Tell(ctx)
	x.NoError(err)
	x.Equal(levyan.Told{Told: 2, Refused: 2, Waiting: 2}, told)
	x.Equal([]string{said(levyan.Arrived, b, creator(b)), said(levyan.Left, b, bLeft)}, heard(fake),
		"alpha's arrival not taken, and so neither its leaving; gamma's refused, and so told")
	for _, v := range w.notices() {
		if pdid.Id(v.Workspace) == c {
			x.NotNil(v.DateTold)
			x.Contains(v.Refused, "not one of ours")
		}
	}

	fake.Fail(a, nil)
	told, err = w.s.Teller.Tell(ctx)
	x.NoError(err)
	x.Equal(levyan.Told{Told: 2}, told)
	x.Equal([]string{
		said(levyan.Arrived, b, creator(b)), said(levyan.Left, b, bLeft),
		said(levyan.Arrived, a, creator(a)), said(levyan.Left, a, aLeft),
	}, heard(fake), "alpha's in the order they happened")
}

// TestADeploymentThatBillsNobodyTellsNothing: with `app.billing.levyan` unset
// nothing is written for levyan and nothing tells it -- a workspace made and
// a membership ended are what they were.
func TestADeploymentThatBillsNobodyTellsNothing(t *testing.T) {
	w := newWorld(t)
	x := w.x
	ctx := context.Background()

	x.Nil(w.s.Notices)
	x.Nil(w.s.Teller)
	x.Nil(w.s.Workspaces(w.c).Told)
	x.NotContains(spun(w.s), levyan.NoticesName)

	lee, err := w.s.Identity.AddPerson(ctx, "lee", "이", pdid.Nil)
	x.NoError(err)
	_, err = w.s.Workspaces(w.c).Create(ctx, lee, "")
	x.NoError(err)
	kim := w.holder("kim", "member")
	_, err = w.s.Base.Holder().Erase(ctx, app.HolderRef_builder{Id: kim.GetId()}.Build())
	x.NoError(err)
	x.Empty(w.notices())
}

// firstOwner is the account of the world's workspace's first owner.
func (w *world) firstOwner() pdid.Id {
	h, err := w.s.Ent.Holder.Query().
		Where(holder.TenantId(tenantId(w.x, w.tenant).Uuid()), holder.Role("owner")).
		Order(ent.Asc(holder.FieldDateCreated)).
		First(context.Background())
	w.x.NoError(err)
	w.x.NotNil(h.Account)
	return pdid.Id(*h.Account)
}
