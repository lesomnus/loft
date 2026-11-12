package cmd_test

import (
	"bytes"
	"context"
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

// signIn mints a session for a member and answers a context that carries its
// cookie, the way a browser's call arrives.
func (w *world) signIn(h *app.Holder) context.Context { return w.session(h) }

// session is a context that carries a session for a member.
func (w *world) session(h *app.Holder) context.Context {
	_, c, err := w.s.Sessions.Mint(context.Background(), authsession.Session{Id: w.accountOf(h).String(), Grant: frame.Whole()})
	w.x.NoError(err)
	return metadata.AppendToOutgoingContext(context.Background(), "cookie", c.Name+"="+c.Value)
}

// accountOf is the holder at roster a member is, which is their identifier
// here too (D85).
func (w *world) accountOf(h *app.Holder) pdid.Id {
	id, err := pdid.From(h.GetId())
	w.x.NoError(err)
	v, err := w.s.Ent.Holder.Get(context.Background(), id.Uuid())
	w.x.NoError(err)
	w.x.NotNil(v.Account)
	w.x.Equal(id, pdid.Id(*v.Account))
	return pdid.Id(*v.Account)
}

// holder is somebody in the tenant at roster -- where they sign in and what a
// session is held to -- and their membership of the world's workspace, by the
// same identifier, with a role.
func (w *world) holder(alias, role string) *app.Holder {
	p, err := w.s.Identity.AddPerson(context.Background(), alias, alias, pdid.Nil)
	w.x.NoError(err)
	return w.here(p, role)
}

// here is somebody at roster's membership of the world's workspace.
func (w *world) here(p identity.Person, role string) *app.Holder {
	return w.member(w.tenant, p, role)
}

// member is a membership of `t` by the identifier `p` has at roster: of the
// world's workspace, which is the tenant at roster; of another, a membership
// nobody at roster is.
func (w *world) member(t *app.Tenant, p identity.Person, role string) *app.Holder {
	h, err := w.s.Base.Holder().Add(context.Background(), app.HolderAddRequest_builder{
		Id:      p.Id.Bytes(),
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
	c.Auth.Roster.Tenant = "acme"
	if with != nil {
		with(&c)
	}

	s, err := cmd.Build(ctx, c)
	x.NoError(err)
	t.Cleanup(func() { s.Close() })
	x.NoError(cli.Migrate(ctx, s))

	// The tenant and its first person at roster, then the workspace here by
	// the same identifier, as `loft init` does (D85).
	rt, owner, _, err := s.Identity.MakeTenant(ctx, "", "owner", "")
	x.NoError(err)
	tn, err := s.Base.Tenant().Add(ctx, app.TenantAddRequest_builder{Id: rt.Id.Bytes(), Alias: rt.Alias}.Build())
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

	login := session.SignIn(w.s.Sessions, w.s.Identity, w.s.Ent, w.s.Members)
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

// TestSignIn is a password roster checked, and a session for the member it is
// here (D85): somebody in the tenant at roster is a member of its workspace
// from the first time they sign in, by roster's identifier for them, with the
// person record this workspace keeps already at an address roster holds as
// checked for them.
func TestSignIn(t *testing.T) {
	w := newWorld(t)
	x := w.x
	ctx := context.Background()
	me := app.NewPartyServiceClient(w.conn)

	// Somebody at roster nobody here has met, and their person record here,
	// at an address roster holds as checked for them.
	park, err := w.s.Identity.AddPerson(ctx, "park", "박", pdid.Nil)
	x.NoError(err)
	x.NoError(w.s.Identity.SetPassword(ctx, park.Id, "correct horse"))
	x.NoError(w.s.Identity.AddAddress(ctx, park.Id, "park@example.com", true))
	record, err := w.s.Base.Party().Add(ctx, app.PartyAddRequest_builder{
		Tenant: app.TenantRef_builder{Id: w.tenant.GetId()}.Build(),
		Name:   "박지성",
		Kind:   "person",
		Email:  "Park@Example.com",
	}.Build())
	x.NoError(err)

	login := session.SignIn(w.s.Sessions, w.s.Identity, w.s.Ent, w.s.Members)
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
		x.Equal(http.StatusNoContent, res.Code, res.Body.String())
		cookie := res.Result().Cookies()[0]
		return metadata.AppendToOutgoingContext(ctx, "cookie", cookie.Name+"="+cookie.Value)
	}
	members := func(id pdid.Id) int {
		n, err := w.s.Ent.Holder.Query().Where(holder.Id(id.Uuid())).Count(ctx)
		x.NoError(err)
		return n
	}

	x.Equal(http.StatusUnauthorized, post(`{"login":"park","password":"wrong one"}`).Code)
	x.Equal(http.StatusUnauthorized, post(`{"login":"nobody","password":"correct horse"}`).Code)
	x.Zero(members(park.Id), "a sign-in refused made a member")

	md := signedIn(`{"login":"park","password":"correct horse"}`)
	v, err := me.Me(md, &app.PartyMeRequest{})
	x.NoError(err, "a member from the first sign-in")
	x.Equal(park.Id.Bytes(), v.GetHolder().GetId(), "by roster's identifier for them")
	x.Equal("park", v.GetHolder().GetAlias())
	x.Equal("박", v.GetHolder().GetName())
	x.Equal("member", v.GetRole())
	x.Equal(record.GetId(), v.GetParty().GetId(), "the person record at the address roster holds as checked")
	signedIn(`{"login":"park","password":"correct horse"}`)
	x.Equal(1, members(park.Id), "a second sign-in made a second member")

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

	t.Run("a name is no membership", func(t *testing.T) {
		x := require.New(t)

		// A member nobody at roster is -- as a restore puts back one from
		// another deployment -- and somebody at roster by the same name.
		_, err := w.s.Base.Holder().Add(ctx, app.HolderAddRequest_builder{
			Tenant: app.TenantRef_builder{Id: w.tenant.GetId()}.Build(),
			Alias:  "choi",
			Name:   "최",
			Role:   z.Ptr("admin"),
		}.Build())
		x.NoError(err)
		choi, err := w.s.Identity.AddPerson(ctx, "choi", "최", pdid.Nil)
		x.NoError(err)
		x.NoError(w.s.Identity.SetPassword(ctx, choi.Id, "correct horse"))

		v, err := me.Me(signedIn(`{"login":"choi","password":"correct horse"}`), &app.PartyMeRequest{})
		x.NoError(err)
		x.Equal(choi.Id.Bytes(), v.GetHolder().GetId(), "a member of their own")
		x.Equal("choi-2", v.GetHolder().GetAlias(), "called by their name and a number, while somebody here goes by it")
		x.Equal("member", v.GetRole(), "and nothing of the other's")
	})

	t.Run("a login ended here stays ended, and the holder at roster with it", func(t *testing.T) {
		x := require.New(t)

		_, err := w.s.Base.Holder().Erase(ctx, app.HolderRef_builder{Id: park.Id.Bytes()}.Build())
		x.NoError(err)
		_, err = me.Me(md, &app.PartyMeRequest{})
		x.Equal(codes.Unauthenticated, status.Code(err), "the session of a membership ended")
		x.Equal(http.StatusForbidden, post(`{"login":"park","password":"correct horse"}`).Code, "a sign-in made them a member again")
		x.Equal(1, members(park.Id))
		_, err = w.s.Identity.Verify(ctx, "park", "correct horse")
		x.NoError(err, "the holder at roster went with the membership")
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

// TestASignInBeforeInitIsNobodysYet: roster vouching for somebody before
// `loft init` put the workspace up is a sign-in with nothing to sign in to --
// not a wrong password, and no member of anything.
func TestASignInBeforeInitIsNobodysYet(t *testing.T) {
	x := require.New(t)
	ctx := context.Background()
	c := cmd.Config{}
	c.Db.Driver, c.Db.Dsn = pdtest.DB(t)
	c.Watch.Broker = "memory"
	c.App.Files = t.TempDir()
	c.Auth.Roster.Db.Driver, c.Auth.Roster.Db.Dsn = pdtest.DB(t)
	s, err := cmd.Build(ctx, c)
	x.NoError(err)
	t.Cleanup(func() { s.Close() })
	x.NoError(cli.Migrate(ctx, s))
	_, _, secret, err := s.Identity.MakeTenant(ctx, "", "owner", "")
	x.NoError(err)

	r := httptest.NewRequest(http.MethodPost, "/session", bytes.NewBufferString(`{"login":"owner","password":"`+secret+`"}`))
	r.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	session.SignIn(s.Sessions, s.Identity, s.Ent, s.Members).ServeHTTP(rec, r)
	x.Equal(http.StatusServiceUnavailable, rec.Code)
	x.Empty(rec.Result().Cookies())
	n, err := s.Ent.Holder.Query().Count(ctx)
	x.NoError(err)
	x.Zero(n)
}

// TestAMemberSeesTheirWorkspaceAlone is the wall, which keeps a workspace's
// rows to it whatever else the database holds -- a workspace restored from
// another deployment's export, whose members nobody at roster is -- and a
// session of such a member is nobody's.
func TestAMemberSeesTheirWorkspaceAlone(t *testing.T) {
	w := newWorld(t)
	x := w.x
	ctx := context.Background()
	assets := app.NewAssetServiceClient(w.conn)

	beta, err := w.s.Base.Tenant().Add(ctx, app.TenantAddRequest_builder{Alias: "beta"}.Build())
	x.NoError(err)
	theirs, err := w.s.Base.Holder().Add(ctx, app.HolderAddRequest_builder{
		Tenant: app.TenantRef_builder{Id: beta.GetId()}.Build(),
		Alias:  "owner",
		Role:   z.Ptr("owner"),
	}.Build())
	x.NoError(err)
	box, err := w.s.Ungated.Asset().Add(frameOf(x, theirs), app.AssetAddRequest_builder{Name: "beta의 노트북"}.Build())
	x.NoError(err)

	got, err := assets.Search(w.owner, app.AssetSearchRequest_builder{}.Build())
	x.NoError(err)
	x.Empty(got.GetItems(), "acme's register shows beta's asset")
	_, err = assets.Get(w.owner, app.AssetGetRequest_builder{Ref: app.AssetRef_builder{Id: box.GetId()}.Build()}.Build())
	x.Equal(codes.NotFound, status.Code(err))

	tid, err := pdid.From(theirs.GetId())
	x.NoError(err)
	_, c, err := w.s.Sessions.Mint(ctx, authsession.Session{Id: tid.String(), Grant: frame.Whole()})
	x.NoError(err)
	_, err = assets.Search(metadata.AppendToOutgoingContext(ctx, "cookie", c.Name+"="+c.Value), app.AssetSearchRequest_builder{}.Build())
	x.Equal(codes.Unauthenticated, status.Code(err), "a member nobody at roster is")
}

// frameOf is a call made as `h`, past the gRPC chain: what the deployment's
// own work in a workspace is.
func frameOf(x *require.Assertions, h *app.Holder) context.Context {
	hid, err := pdid.From(h.GetId())
	x.NoError(err)
	tid, err := pdid.From(h.GetTenant().GetId())
	x.NoError(err)
	return frame.Into(context.Background(), frame.New(hid, tid, frame.Whole()).WithScope(frame.Only(tid)).WithRow(h))
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

	login := session.SignIn(w.s.Sessions, w.s.Identity, w.s.Ent, w.s.Members)
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
