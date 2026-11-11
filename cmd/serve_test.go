package cmd_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"

	"github.com/lesomnus/payday/auth/authsession"
	"github.com/lesomnus/payday/frame"
	"github.com/lesomnus/payday/pdid"
	"github.com/lesomnus/payday/pdtest"
	"github.com/lesomnus/z"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	app "github.com/lesomnus/loft"
	"github.com/lesomnus/loft/cli"
	"github.com/lesomnus/loft/cmd"
	"github.com/lesomnus/loft/internal/ent/holder"
	"github.com/lesomnus/loft/internal/identity"
	"github.com/lesomnus/loft/server/account"
	"github.com/lesomnus/loft/server/session"
)

type world struct {
	x      *require.Assertions
	s      *cmd.Server
	c      cmd.Config
	conn   *grpc.ClientConn
	tenant *app.Tenant
	owner  context.Context
}

// signIn mints a session for a holder's account and answers a context that
// carries its cookie and names the world's workspace, the way a browser's call
// from `/@acme/...` arrives.
func (w *world) signIn(h *app.Holder) context.Context {
	return metadata.AppendToOutgoingContext(w.session(h), cmd.WorkspaceHeader, w.tenant.GetAlias())
}

// session is a context that carries a session for a holder's account, and
// names no workspace.
func (w *world) session(h *app.Holder) context.Context {
	_, c, err := w.s.Sessions.Mint(context.Background(), authsession.Session{Id: w.accountOf(h).String(), Grant: frame.Whole()})
	w.x.NoError(err)
	return metadata.AppendToOutgoingContext(context.Background(), "cookie", c.Name+"="+c.Value)
}

func (w *world) accountOf(h *app.Holder) pdid.Id {
	id, err := pdid.From(h.GetId())
	w.x.NoError(err)
	v, err := w.s.Ent.Holder.Get(context.Background(), id.Uuid())
	w.x.NoError(err)
	w.x.NotNil(v.Account)
	return pdid.Id(*v.Account)
}

// holder is an account in the directory at roster -- where it signs in and
// what a session is held to -- and its membership of the world's workspace,
// with a role.
func (w *world) holder(alias, role string) *app.Holder {
	p, err := w.s.Identity.AddPerson(context.Background(), alias, alias, pdid.Nil)
	w.x.NoError(err)
	return w.here(p, role)
}

// here is an account's membership of the world's workspace.
func (w *world) here(p identity.Person, role string) *app.Holder {
	return w.member(w.tenant, p, role)
}

func (w *world) member(t *app.Tenant, p identity.Person, role string) *app.Holder {
	h, err := w.s.Base.Holder().Add(context.Background(), app.HolderAddRequest_builder{
		Tenant:  app.TenantRef_builder{Id: t.GetId()}.Build(),
		Alias:   p.Alias,
		Role:    z.Ptr(role),
		Account: p.Id.Bytes(),
	}.Build())
	w.x.NoError(err)
	return h
}

func newWorld(t *testing.T) *world { return newWorldWith(t, nil) }

// newWorldWith is the world with one more say over its configuration, for the
// settings only one test is about.
func newWorldWith(t *testing.T, with func(*cmd.Config)) *world {
	x := require.New(t)
	ctx := context.Background()
	driver, dsn := pdtest.DB(t)
	c := cmd.Config{}
	c.Db.Driver = driver
	c.Db.Dsn = dsn
	c.Watch.Broker = "memory"
	c.App.Files = t.TempDir()
	c.Auth.Roster.Db.Driver, c.Auth.Roster.Db.Dsn = pdtest.DB(t)
	if with != nil {
		with(&c)
	}

	s, err := cmd.Build(ctx, c)
	x.NoError(err)
	t.Cleanup(func() { s.Close() })
	x.NoError(cli.Migrate(ctx, s))

	// The directory and its first account at roster, then the workspace
	// here, as `loft init` does.
	owner, _, err := s.Identity.MakeDirectory(ctx, "owner", "")
	x.NoError(err)
	tn, err := s.Base.Tenant().Add(ctx, app.TenantAddRequest_builder{Alias: "acme"}.Build())
	x.NoError(err)

	g, err := s.Grpc(ctx, c)
	x.NoError(err)
	w := &world{x: x, s: s, c: c, conn: pdtest.Serve(t, g), tenant: tn}
	w.owner = w.signIn(w.here(owner, "owner"))
	return w
}

func TestTheRoleTable(t *testing.T) {
	w := newWorld(t)
	x := w.x
	assets := app.NewAssetServiceClient(w.conn)
	member := w.signIn(w.holder("kim", "member"))
	auditor := w.signIn(w.holder("lee", "auditor"))

	room, err := assets.Add(w.owner, app.AssetAddRequest_builder{Name: "회의실", Kind: "space"}.Build())
	x.NoError(err)
	laptop, err := assets.Add(w.owner, app.AssetAddRequest_builder{Name: "노트북"}.Build())
	x.NoError(err)

	code := func(err error) codes.Code { return status.Code(err) }

	_, err = assets.Search(context.Background(), app.AssetSearchRequest_builder{}.Build())
	x.Equal(codes.Unauthenticated, code(err), "nobody is signed in")

	_, err = assets.Search(member, app.AssetSearchRequest_builder{}.Build())
	x.NoError(err, "a member reads the register")
	_, err = assets.Move(member, app.AssetMoveRequest_builder{Ref: app.AssetRef_builder{Id: laptop.GetId()}.Build(), To: app.AssetRef_builder{Id: room.GetId()}.Build()}.Build())
	x.Equal(codes.PermissionDenied, code(err), "and does not move things")
	_, err = assets.Search(auditor, app.AssetSearchRequest_builder{}.Build())
	x.NoError(err)
	_, err = assets.Add(auditor, app.AssetAddRequest_builder{Name: "x"}.Build())
	x.Equal(codes.PermissionDenied, code(err), "an auditor changes nothing")

	// What only the domain layer writes is sealed for everybody, the owner
	// included, and so is a generated write that would skip the history.
	_, err = app.NewPlacementServiceClient(w.conn).Add(w.owner, app.PlacementAddRequest_builder{}.Build())
	x.Equal(codes.PermissionDenied, code(err))
	_, err = assets.Patch(w.owner, app.AssetPatchRequest_builder{Ref: app.AssetRef_builder{Id: laptop.GetId()}.Build(), Name: z.Ptr("x")}.Build())
	x.Equal(codes.PermissionDenied, code(err))
	_, err = app.NewEventServiceClient(w.conn).Add(w.owner, app.EventAddRequest_builder{Kind: "forged"}.Build())
	x.Equal(codes.PermissionDenied, code(err))
	_, err = app.NewPlacementServiceClient(w.conn).List(w.owner, app.PlacementListRequest_builder{}.Build())
	x.Equal(codes.PermissionDenied, code(err), "the time rows are read through the history, which tells them as what they were")

	_, err = app.NewAuditServiceClient(w.conn).Recent(auditor, app.AuditRecentRequest_builder{}.Build())
	x.NoError(err, "the trail is what an auditor is for")
	_, err = app.NewAuditServiceClient(w.conn).Recent(member, app.AuditRecentRequest_builder{}.Build())
	x.Equal(codes.PermissionDenied, code(err))

	_, err = assets.Move(w.owner, app.AssetMoveRequest_builder{Ref: app.AssetRef_builder{Id: laptop.GetId()}.Build(), To: app.AssetRef_builder{Id: room.GetId()}.Build()}.Build())
	x.NoError(err, "the owner moves things")
	got, err := assets.Get(w.owner, app.AssetGetRequest_builder{Ref: app.AssetRef_builder{Id: laptop.GetId()}.Build()}.Build())
	x.NoError(err)
	x.Equal(room.GetId(), got.GetParentId())
}

// TestTheWorkLogIsReadByWhoeverRunsTheWorkspaceOrAuditsIt (D79): the
// workspace's work log -- every operation, and why, in the words of whoever
// did it -- is read as the trail is, by the people who run the workspace and
// by its auditors, which the owner decided. A member and a manager are
// refused it by the table, in their role's words: by Recent, which says each
// event, and by Get and List, which are its rows. What happened to one asset
// is not the log: its history is everybody's still, the reason in it too.
func TestTheWorkLogIsReadByWhoeverRunsTheWorkspaceOrAuditsIt(t *testing.T) {
	w := newWorld(t)
	x := w.x
	const why = "3층 공사로 옮김"
	id := w.moved(why)
	moved, err := w.s.Ent.Event.Get(context.Background(), id.Uuid())
	x.NoError(err)
	x.NotNil(moved.SubjectId)
	laptop := app.AssetById(pdid.Id(*moved.SubjectId).Bytes())

	events := app.NewEventServiceClient(w.conn)
	assets := app.NewAssetServiceClient(w.conn)
	refused := map[string]string{"member": "구성원", "manager": "매니저"}
	for _, role := range []string{"owner", "admin", "auditor", "manager", "member"} {
		as := w.owner
		if role != "owner" {
			as = w.signIn(w.holder("as-"+role, role))
		}
		feed, recent := events.Recent(as, app.EventRecentRequest_builder{Kind: "asset.move"}.Build())
		rows, list := events.List(as, app.EventListRequest_builder{}.Build())
		one, get := events.Get(as, app.EventGetRequest_builder{Ref: app.EventRef_builder{Id: id.Bytes()}.Build()}.Build())
		if word, ok := refused[role]; ok {
			for m, err := range map[string]error{
				app.EventService_Recent_FullMethodName: recent,
				app.EventService_List_FullMethodName:   list,
				app.EventService_Get_FullMethodName:    get,
			} {
				x.True(refusedByTable(err, m), "%s %s: %v", role, m, err)
				x.Contains(status.Convert(err).Message(), word+" 역할로는 할 수 없는 작업입니다", "%s %s", role, m)
			}
		} else {
			x.NoError(recent, role)
			x.Len(feed.GetItems(), 1, role)
			x.Equal(why, feed.GetItems()[0].GetReason(), role)
			x.NotEmpty(feed.GetItems()[0].GetDesc(), "%s: said as it is read", role)
			x.NoError(list, role)
			x.NotEmpty(rows.GetItems(), role)
			x.NoError(get, role)
			x.Equal(why, one.GetReason(), role)
		}

		tl, err := assets.Timeline(as, app.AssetTimelineRequest_builder{Ref: laptop}.Build())
		x.NoError(err, "%s reads what happened to one asset", role)
		x.True(slices.ContainsFunc(tl.GetEntries(), func(e *app.TimelineEntry) bool {
			return e.GetEventKind() == "asset.move" && e.GetReason() == why
		}), "%s reads why it was moved, on its own history", role)
	}
}

// TestSignIn is a password roster checked, and a session for the account: the
// workspaces it reaches are the ones it is a member of.
// TestASignInAFormOfAnotherSiteCouldPostIsRefused: a page on another site
// can post a form at `/session` -- `text/plain` whose body is JSON, or the
// form encoding -- and nothing stops a browser from following it. Answered,
// it is somebody signed in as an account of that page's choosing, where
// whatever they do next is kept (login CSRF, loft#40). Only JSON is answered,
// which no other site can send here without this one agreeing to it first.
func TestASignInAFormOfAnotherSiteCouldPostIsRefused(t *testing.T) {
	w := newWorld(t)
	x := w.x
	ctx := context.Background()

	park, err := w.s.Identity.AddPerson(ctx, "park", "박", pdid.Nil)
	x.NoError(err)
	x.NoError(w.s.Identity.SetPassword(ctx, park.Id, "correct horse"))

	login := session.SignIn(w.s.Sessions, w.s.Identity, w.s.Ent, nil)
	post := func(ct, body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodPost, "/session", bytes.NewBufferString(body))
		if ct != "" {
			r.Header.Set("Content-Type", ct)
		}
		r.RemoteAddr = "10.0.0.1:1234"
		rec := httptest.NewRecorder()
		login.ServeHTTP(rec, r)
		return rec
	}

	for _, ct := range []string{"text/plain", "application/x-www-form-urlencoded", "multipart/form-data; boundary=x", ""} {
		res := post(ct, `{"login":"park","password":"correct horse","x":"="}`)
		x.Equal(http.StatusUnsupportedMediaType, res.Code, "%q", ct)
		x.Empty(res.Result().Cookies(), "%q signed somebody in", ct)
	}

	res := post("application/json; charset=utf-8", `{"login":"park","password":"correct horse"}`)
	x.Equal(http.StatusNoContent, res.Code)
	x.NotEmpty(res.Result().Cookies())
}

func TestSignIn(t *testing.T) {
	w := newWorld(t)
	x := w.x
	ctx := context.Background()
	me := app.NewPartyServiceClient(w.conn)

	// An account at roster that is in no workspace yet.
	park, err := w.s.Identity.AddPerson(ctx, "park", "박", pdid.Nil)
	x.NoError(err)
	x.NoError(w.s.Identity.SetPassword(ctx, park.Id, "correct horse"))

	login := session.SignIn(w.s.Sessions, w.s.Identity, w.s.Ent, nil)
	post := func(body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodPost, "/session", bytes.NewBufferString(body))
		r.Header.Set("Content-Type", "application/json")
		r.RemoteAddr = "10.0.0.1:1234"
		rec := httptest.NewRecorder()
		login.ServeHTTP(rec, r)
		return rec
	}
	signedIn := func(body string) context.Context {
		res := post(body)
		x.Equal(http.StatusNoContent, res.Code)
		cookie := res.Result().Cookies()[0]
		return metadata.AppendToOutgoingContext(ctx, "cookie", cookie.Name+"="+cookie.Value)
	}

	x.Equal(http.StatusUnauthorized, post(`{"login":"park","password":"wrong one"}`).Code)
	x.Equal(http.StatusUnauthorized, post(`{"login":"nobody","password":"correct horse"}`).Code)

	md := signedIn(`{"login":"park","password":"correct horse"}`)
	_, err = me.Me(md, &app.PartyMeRequest{})
	x.Equal(codes.FailedPrecondition, status.Code(err), "an account in no workspace is served as nobody in particular")
	n, err := w.s.Ent.Holder.Query().Where(holder.Account(park.Id.Uuid())).Count(ctx)
	x.NoError(err)
	x.Zero(n, "a sign-in made a membership")

	// In the workspace, with the person record beside the membership.
	h := w.here(park, "member")
	_, err = w.s.Base.Party().Add(ctx, app.PartyAddRequest_builder{
		Tenant: app.TenantRef_builder{Id: w.tenant.GetId()}.Build(),
		Name:   "박",
		Kind:   "person",
		Email:  "park@example.com",
		Holder: app.HolderRef_builder{Id: h.GetId()}.Build(),
	}.Build())
	x.NoError(err)
	v, err := me.Me(md, &app.PartyMeRequest{})
	x.NoError(err, "the one workspace, when the call names none")
	x.Equal("park", v.GetHolder().GetAlias())
	x.Equal("member", v.GetRole())

	t.Run("by the address on their person record, on the roster in this process", func(t *testing.T) {
		x := require.New(t)

		x.Equal(http.StatusNoContent, post(`{"login":"Park@Example.com","password":"correct horse"}`).Code)
		x.Equal(http.StatusUnauthorized, post(`{"login":"park@example.com","password":"wrong one"}`).Code)
		x.Equal(http.StatusUnauthorized, post(`{"login":"nobody@example.com","password":"correct horse"}`).Code)
	})

	t.Run("roster signing them out everywhere ends the session open here", func(t *testing.T) {
		x := require.New(t)

		x.NoError(w.s.Identity.Invalidate(ctx, park.Id))
		_, err := me.Me(md, &app.PartyMeRequest{})
		x.Equal(codes.Unauthenticated, status.Code(err))

		md = signedIn(`{"login":"park","password":"correct horse"}`)
		_, err = me.Me(md, &app.PartyMeRequest{})
		x.NoError(err, "and a sign-in after it is a session that is good")
	})

	t.Run("a login ended here ends the workspace, and not the account", func(t *testing.T) {
		x := require.New(t)

		_, err := w.s.Base.Holder().Erase(ctx, app.HolderRef_builder{Id: h.GetId()}.Build())
		x.NoError(err)
		_, err = me.Me(metadata.AppendToOutgoingContext(md, cmd.WorkspaceHeader, "acme"), &app.PartyMeRequest{})
		x.Equal(codes.PermissionDenied, status.Code(err), "the session reached a workspace it is no member of any more")
		x.Equal(http.StatusNoContent, post(`{"login":"park","password":"correct horse"}`).Code, "the account went with the membership")
	})

	t.Run("a holder that names no account is nobody's membership", func(t *testing.T) {
		x := require.New(t)

		// A login that names no account -- as a restore puts back a member
		// this roster does not have -- and an account of the same name at
		// roster.
		_, err := w.s.Base.Holder().Add(ctx, app.HolderAddRequest_builder{
			Tenant: app.TenantRef_builder{Id: w.tenant.GetId()}.Build(),
			Alias:  "choi",
			Name:   "최",
			Role:   z.Ptr("member"),
		}.Build())
		x.NoError(err)
		choi, err := w.s.Identity.AddPerson(ctx, "choi", "최", pdid.Nil)
		x.NoError(err)
		x.NoError(w.s.Identity.SetPassword(ctx, choi.Id, "correct horse"))

		_, err = me.Me(signedIn(`{"login":"choi","password":"correct horse"}`), &app.PartyMeRequest{})
		x.Equal(codes.FailedPrecondition, status.Code(err), "a name is not a membership")
	})

	t.Run("and an address that keeps getting it wrong is refused for a while", func(t *testing.T) {
		x := require.New(t)

		lee, err := w.s.Identity.AddPerson(ctx, "lee", "이", pdid.Nil)
		x.NoError(err)
		x.NoError(w.s.Identity.SetPassword(ctx, lee.Id, "correct horse"))
		for range 10 {
			post(`{"login":"lee","password":"a guess at it"}`)
		}
		x.Equal(http.StatusUnauthorized, post(`{"login":"lee","password":"correct horse"}`).Code)
	})
}

// TestOneAccountInTwoWorkspaces is the wall between workspaces, for one
// account that is in both: each call is served as its membership of the
// workspace the call names, and sees that workspace alone.
func TestOneAccountInTwoWorkspaces(t *testing.T) {
	w := newWorld(t)
	x := w.x
	ctx := context.Background()
	assets := app.NewAssetServiceClient(w.conn)

	kim, err := w.s.Identity.AddPerson(ctx, "kim", "Kim", pdid.Nil)
	x.NoError(err)
	beta, err := w.s.Base.Tenant().Add(ctx, app.TenantAddRequest_builder{Alias: "beta"}.Build())
	x.NoError(err)
	inAcme := w.here(kim, "member")
	w.member(beta, kim, "owner")
	md := w.session(inAcme)
	at := func(ws string) context.Context { return metadata.AppendToOutgoingContext(md, cmd.WorkspaceHeader, ws) }

	_, err = assets.Search(md, app.AssetSearchRequest_builder{}.Build())
	x.Equal(codes.InvalidArgument, status.Code(err), "two workspaces, and a call that names neither")

	mine, err := assets.Add(at("beta"), app.AssetAddRequest_builder{Name: "beta의 노트북"}.Build())
	x.NoError(err, "an owner of beta")
	_, err = assets.Add(at("@acme"), app.AssetAddRequest_builder{Name: "x"}.Build())
	x.Equal(codes.PermissionDenied, status.Code(err), "and a member of acme, who adds nothing there")

	got, err := assets.Search(at("acme"), app.AssetSearchRequest_builder{}.Build())
	x.NoError(err)
	x.Empty(got.GetItems(), "acme's register shows beta's asset")
	_, err = assets.Get(at("acme"), app.AssetGetRequest_builder{Ref: app.AssetRef_builder{Id: mine.GetId()}.Build()}.Build())
	x.Equal(codes.NotFound, status.Code(err))
	got, err = assets.Search(at(idString(beta.GetId())), app.AssetSearchRequest_builder{}.Build())
	x.NoError(err, "a workspace named by its identifier")
	x.Len(got.GetItems(), 1)

	_, err = assets.Search(at("gamma"), app.AssetSearchRequest_builder{}.Build())
	x.Equal(codes.PermissionDenied, status.Code(err), "a workspace there is no such of")
	_, err = assets.Search(at("acme"), app.AssetSearchRequest_builder{}.Build())
	x.NoError(err)
}

func idString(b []byte) string {
	id, _ := pdid.From(b)
	return id.String()
}

// TestARosterThatCannotBeAskedServesNoSession: not knowing what roster would
// say of somebody is not their session being over -- the cookie stays -- and
// it is not their session being good either.
func TestARosterThatCannotBeAskedServesNoSession(t *testing.T) {
	w := newWorld(t)
	x := w.x

	me := app.NewPartyServiceClient(w.conn)
	v, err := me.Me(w.owner, &app.PartyMeRequest{})
	x.NoError(err)

	x.NoError(w.s.Identity.Close())
	w.s.Identity.Recheck(w.accountOf(v.GetHolder()))

	_, err = me.Me(w.owner, &app.PartyMeRequest{})
	x.Equal(codes.Unavailable, status.Code(err))

	login := session.SignIn(w.s.Sessions, w.s.Identity, w.s.Ent, nil)
	r := httptest.NewRequest(http.MethodPost, "/session", bytes.NewBufferString(`{"login":"owner","password":"anything at all"}`))
	r.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	login.ServeHTTP(rec, r)
	x.Equal(http.StatusServiceUnavailable, rec.Code, "a password nobody could check is not a wrong one")
}

// TestAContractIsTheOperators.
//
// What the deployment owes a workspace beyond its own settings is the
// operator's agreement with it, and a tenant that could write its own would be
// rewriting the agreement. So everybody in the tenant reads it, nobody writes
// it, and the holds on it are for the people who run the tenant -- and the
// wall keeps one tenant's out of another's sight.
func TestAContractIsTheOperators(t *testing.T) {
	w := newWorld(t)
	x := w.x
	code := func(err error) codes.Code { return status.Code(err) }

	member := w.signIn(w.holder("kim", "member"))
	contracts := app.NewTenantContractServiceClient(w.conn)
	holds := app.NewLegalHoldServiceClient(w.conn)

	ctx := context.Background()
	other, err := w.s.Base.Tenant().Add(ctx, app.TenantAddRequest_builder{Alias: "globex"}.Build())
	x.NoError(err)
	for _, tn := range []*app.Tenant{w.tenant, other} {
		_, err := w.s.Base.TenantContract().Add(ctx, app.TenantContractAddRequest_builder{
			Tenant: app.TenantRef_builder{Id: tn.GetId()}.Build(),
			Name:   tn.GetAlias(),
		}.Build())
		x.NoError(err)
	}

	vs, err := contracts.List(member, app.TenantContractListRequest_builder{}.Build())
	x.NoError(err, "a member cannot read their workspace's agreement")
	x.Len(vs.GetItems(), 1, "a tenant read another's contract")
	x.Equal("acme", vs.GetItems()[0].GetName())

	_, err = contracts.Add(w.owner, app.TenantContractAddRequest_builder{PurgeFloors: z.Ptr(true)}.Build())
	x.Equal(codes.PermissionDenied, code(err), "an owner wrote their own contract")

	_, err = holds.List(member, app.LegalHoldListRequest_builder{}.Build())
	x.Equal(codes.PermissionDenied, code(err), "a hold is for the people who run the tenant")
	_, err = holds.List(w.owner, app.LegalHoldListRequest_builder{}.Build())
	x.NoError(err)
	_, err = holds.Add(w.owner, app.LegalHoldAddRequest_builder{Name: "mine"}.Build())
	x.Equal(codes.PermissionDenied, code(err), "an owner placed a hold")
}

// TestAnAccountInNoWorkspaceIsGivenOne is signing up (design 9.11): an
// account that signs in and is in no workspace is given one of its own, and
// makes more until another one is paid for.
func TestAnAccountInNoWorkspaceIsGivenOne(t *testing.T) {
	w := newWorld(t)
	x := w.x
	ctx := context.Background()

	park, err := w.s.Identity.AddPerson(ctx, "park", "박", pdid.Nil)
	x.NoError(err)
	x.NoError(w.s.Identity.SetPassword(ctx, park.Id, "correct horse"))

	ws := w.s.Workspaces(w.c)
	login := session.SignIn(w.s.Sessions, w.s.Identity, w.s.Ent, ws.Arrive)
	signIn := func() *http.Cookie {
		r := httptest.NewRequest(http.MethodPost, "/session", bytes.NewBufferString(`{"login":"park","password":"correct horse"}`))
		r.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		login.ServeHTTP(rec, r)
		x.Equal(http.StatusNoContent, rec.Code)
		return rec.Result().Cookies()[0]
	}
	cookie := signIn()
	md := metadata.AppendToOutgoingContext(ctx, "cookie", cookie.Name+"="+cookie.Value)

	me, err := app.NewPartyServiceClient(w.conn).Me(md, &app.PartyMeRequest{})
	x.NoError(err, "the workspace it was given")
	x.Equal("owner", me.GetRole())
	x.Empty(me.GetTenant().GetName(), "a workspace named after its person (D57)")
	x.Equal("박의 워크스페이스", me.GetWorkspaceName())
	types, err := app.NewAssetTypeServiceClient(w.conn).List(md, app.AssetTypeListRequest_builder{}.Build())
	x.NoError(err)
	x.NotEmpty(types.GetItems(), "a workspace that does not start with what a register starts with")

	signIn()
	n, err := w.s.Ent.Holder.Query().Where(holder.Account(park.Id.Uuid())).Count(ctx)
	x.NoError(err)
	x.Equal(1, n, "a second sign-in gave it a second workspace")

	acc := http.NewServeMux()
	account.Routes{Sessions: w.s.Sessions, People: w.s.Identity, Workspaces: ws}.Mount(acc)
	var got account.Account
	r := httptest.NewRequest(http.MethodGet, "/account", nil)
	r.Header.Set("Cookie", cookie.Name+"="+cookie.Value)
	rec := httptest.NewRecorder()
	acc.ServeHTTP(rec, r)
	x.Equal(http.StatusOK, rec.Code)
	x.NoError(json.Unmarshal(rec.Body.Bytes(), &got))
	x.Equal("park", got.Login)
	x.Nil(got.Here, "at the service's address, what it is in is TenantService.Mine's")

	// What it is in, and more of its own, asked as the account (D73).
	tenants := app.NewTenantServiceClient(w.conn)
	mine, err := tenants.Mine(md, &app.TenantMineRequest{})
	x.NoError(err)
	x.Len(mine.GetWorkspaces(), 1)
	x.Equal(me.GetTenant().GetAlias(), mine.GetWorkspaces()[0].GetAlias())
	x.Equal("박의 워크스페이스", mine.GetWorkspaces()[0].GetName())
	x.Equal("owner", mine.GetWorkspaces()[0].GetRole())
	x.EqualValues(1, mine.GetOwns())
	x.EqualValues(cmd.DefaultOwned, mine.GetMay())

	for i := 1; i < cmd.DefaultOwned; i++ {
		made, err := tenants.Create(md, app.TenantCreateRequest_builder{Name: "팀 " + string(rune('A'+i))}.Build())
		x.NoError(err)
		x.Equal("owner", made.GetRole())
		x.Equal("팀 "+string(rune('A'+i)), made.GetName())
	}
	_, err = tenants.Create(md, app.TenantCreateRequest_builder{Name: "하나 더"}.Build())
	x.Equal(codes.FailedPrecondition, status.Code(err), "one more than an account owns for nothing")
	_, err = tenants.Create(md, app.TenantCreateRequest_builder{Name: ""}.Build())
	x.Equal(codes.InvalidArgument, status.Code(err))
	_, err = tenants.Create(md, app.TenantCreateRequest_builder{Name: "팀\nBcc: x@example.com"}.Build())
	x.Equal(codes.InvalidArgument, status.Code(err), "more than the one line a mail's subject is")

	mine, err = tenants.Mine(md, &app.TenantMineRequest{})
	x.NoError(err)
	x.Len(mine.GetWorkspaces(), cmd.DefaultOwned)
	_, err = app.NewPartyServiceClient(w.conn).Me(md, &app.PartyMeRequest{})
	x.Equal(codes.InvalidArgument, status.Code(err), "more than one workspace, and a call that names none")
	_, err = app.NewPartyServiceClient(w.conn).Me(metadata.AppendToOutgoingContext(md, cmd.WorkspaceHeader, mine.GetWorkspaces()[1].GetAlias()), &app.PartyMeRequest{})
	x.NoError(err)

	t.Run("nobody is asked about without a session", func(t *testing.T) {
		r := httptest.NewRequest(http.MethodGet, "/account", nil)
		rec := httptest.NewRecorder()
		acc.ServeHTTP(rec, r)
		require.Equal(t, http.StatusUnauthorized, rec.Code)
	})

	t.Run("and a deployment that gives none gives none", func(t *testing.T) {
		x := require.New(t)

		lee, err := w.s.Identity.AddPerson(ctx, "lee", "이", pdid.Nil)
		x.NoError(err)
		x.NoError(w.s.Identity.SetPassword(ctx, lee.Id, "correct horse"))
		ws := w.s.Workspaces(w.c)
		ws.Personal = false
		r := httptest.NewRequest(http.MethodPost, "/session", bytes.NewBufferString(`{"login":"lee","password":"correct horse"}`))
		r.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		session.SignIn(w.s.Sessions, w.s.Identity, w.s.Ent, ws.Arrive).ServeHTTP(rec, r)
		x.Equal(http.StatusNoContent, rec.Code)
		n, err := w.s.Ent.Holder.Query().Where(holder.Account(lee.Id.Uuid())).Count(ctx)
		x.NoError(err)
		x.Zero(n)
	})
}
