package cmd_test

import (
	"archive/zip"
	"bytes"
	"context"
	"io"
	"net/http"
	"net/url"
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"

	"github.com/lesomnus/payday/access"
	"github.com/lesomnus/payday/pdid"
	"github.com/lesomnus/payday/pdpb"

	app "github.com/lesomnus/rove"
	"github.com/lesomnus/rove/internal/ent"
	entaccess "github.com/lesomnus/rove/internal/ent/access"
)

// person is somebody of the world's workspace who signs in nowhere: a person
// record, which holds a name and an address.
func (w *world) person(name, email string) *app.Party {
	p, err := w.s.Base.Party().Add(context.Background(), app.PartyAddRequest_builder{
		Tenant: app.TenantRef_builder{Id: w.tenant.GetId()}.Build(),
		Name:   name, Kind: "person", Email: email,
	}.Build())
	w.x.NoError(err)
	return p
}

// accessed is the access record's rows of `action`, once what waits has been
// written.
func (w *world) accessed(t *testing.T, action string) []*ent.Access {
	t.Helper()
	ctx := context.Background()
	require.NoError(t, w.s.Access.Flush(ctx))

	vs, err := w.s.Ent.Access.Query().
		Where(entaccess.ActionEQ(action)).
		Order(ent.Asc(entaccess.FieldDateCreated, entaccess.FieldId)).
		All(ctx)
	require.NoError(t, err)

	return vs
}

func detailOf(t *testing.T, v *ent.Access) *pdpb.AccessDetail {
	t.Helper()
	d := &pdpb.AccessDetail{}
	require.NoError(t, proto.Unmarshal(v.Value, d))
	return d
}

// TestWhoReadWhoseDataIsRecorded is rove#136 through the chain a page calls:
// a list of people is a row naming each of them, filed under the workspace
// and naming the member who asked; and a history that reads somebody's name
// out in a sentence -- which no `personal` field of its answer carries -- says
// it showed them.
func TestWhoReadWhoseDataIsRecorded(t *testing.T) {
	w := newWorld(t)
	x := w.x
	park := w.person("박지민", "park@acme.example")
	kim := w.holder("kim", "member")
	asKim := w.signIn(kim)

	vs, err := app.NewPartyServiceClient(w.conn).List(asKim, app.PartyListRequest_builder{}.Build())
	x.NoError(err)
	x.NotEmpty(vs.GetItems())

	rs := w.accessed(t, app.PartyService_List_FullMethodName)
	x.Len(rs, 1)
	x.Equal("read", rs[0].Kind)
	x.Contains(rs[0].Subjects, park.GetId())
	tenant, err := pdid.From(w.tenant.GetId())
	x.NoError(err)
	actor, err := pdid.From(kim.GetId())
	x.NoError(err)
	x.Equal(tenant.Uuid(), rs[0].TenantId)
	x.Equal(actor.Uuid(), rs[0].ActorId)
	x.NotEmpty(detailOf(t, rs[0]).GetAddress())

	t.Run("and a sentence that names somebody", func(t *testing.T) {
		x := require.New(t)
		assets := app.NewAssetServiceClient(w.conn)

		laptop, err := assets.Add(w.owner, app.AssetAddRequest_builder{Name: "노트북"}.Build())
		x.NoError(err)
		_, err = assets.Assign(w.owner, app.AssetAssignRequest_builder{
			Ref:   app.AssetRef_builder{Id: laptop.GetId()}.Build(),
			Role:  "owner",
			Party: app.PartyRef_builder{Id: park.GetId()}.Build(),
		}.Build())
		x.NoError(err)

		_, err = assets.Timeline(asKim, app.AssetTimelineRequest_builder{Ref: app.AssetRef_builder{Id: laptop.GetId()}.Build()}.Build())
		x.NoError(err)

		rs := w.accessed(t, app.AssetService_Timeline_FullMethodName)
		x.Len(rs, 1)
		x.Contains(rs[0].Subjects, park.GetId(), "the history read her name out, and did not say so")
	})
}

// TestADownloadOfPeopleSaysWhatItIsFor: the register's export names its
// custodians, so it is refused without a reason, and its row -- with the
// reason, in whatever words -- is written before it answers. The form a bulk
// registration is written in names nobody, and needs none.
func TestADownloadOfPeopleSaysWhatItIsFor(t *testing.T) {
	w := newWorld(t)
	x := w.x
	park := w.person("박지민", "park@acme.example")
	assets := app.NewAssetServiceClient(w.conn)

	laptop, err := assets.Add(w.owner, app.AssetAddRequest_builder{Name: "노트북"}.Build())
	x.NoError(err)
	// Issued to her, which is what makes her its 사용자 in the register.
	_, err = app.NewCustodyServiceClient(w.conn).Add(w.owner, app.CustodyAddRequest_builder{
		Party: app.PartyRef_builder{Id: park.GetId()}.Build(),
		Lines: []*app.CustodyLineSpec{
			app.CustodyLineSpec_builder{Asset: app.AssetRef_builder{Id: laptop.GetId()}.Build()}.Build(),
		},
	}.Build())
	x.NoError(err)

	_, err = assets.Export(w.owner, &app.AssetExportRequest{})
	x.Equal(codes.InvalidArgument, status.Code(err))
	x.Contains(err.Error(), access.ReasonKey)

	why := metadata.AppendToOutgoingContext(w.owner, access.ReasonKey, "10월 자산 실사")
	v, err := assets.Export(why, &app.AssetExportRequest{})
	x.NoError(err)
	x.Contains(string(v.GetData()), "park@acme.example")

	// Written before it answered: nothing waits.
	x.Zero(w.s.Access.Pending())
	rs, err := w.s.Ent.Access.Query().Where(entaccess.KindEQ("download")).All(context.Background())
	x.NoError(err)
	x.Len(rs, 1)
	x.Equal(app.AssetService_Export_FullMethodName, rs[0].Action)
	x.Equal("10월 자산 실사", detailOf(t, rs[0]).GetReason())
	x.Contains(rs[0].Subjects, park.GetId(), "the file names her custodian's column, and the row does not")

	_, err = assets.Form(w.owner, &app.AssetFormRequest{})
	x.NoError(err, "the form names nobody")
}

// TestAWorkspacesExportSaysWhatItIsFor: everybody's data, out of the
// deployment, so it is refused without a reason and recorded with one, before
// the first byte goes.
func TestAWorkspacesExportSaysWhatItIsFor(t *testing.T) {
	w := newWorld(t)
	x := w.x
	park := w.person("박지민", "park@acme.example")
	path := "/account/workspaces/" + w.tenant.GetAlias() + "/export"

	x.Equal(http.StatusBadRequest, w.page(w.owner, http.MethodGet, path).Code)
	n, err := w.s.Ent.Access.Query().Count(context.Background())
	x.NoError(err)
	x.Zero(n, "a refused export was recorded")

	res := w.page(w.owner, http.MethodGet, path+"?reason="+url.QueryEscape("폐업 전 보관"))
	x.Equal(http.StatusOK, res.Code)

	rs, err := w.s.Ent.Access.Query().Where(entaccess.KindEQ("download")).All(context.Background())
	x.NoError(err)
	x.Len(rs, 1)
	x.Equal("GET /account/workspaces/{alias}/export", rs[0].Action)
	x.Equal("폐업 전 보관", detailOf(t, rs[0]).GetReason())
	x.Contains(rs[0].Subjects, park.GetId())
	x.GreaterOrEqual(rs[0].SubjectCount, uint32(2), "the person record and the owner's membership")

	// And the workspace takes its access record out with it -- this export's
	// own row among it, written before anything was read for the file.
	zr, err := zip.NewReader(bytes.NewReader(res.Body.Bytes()), int64(res.Body.Len()))
	x.NoError(err)
	f, err := zr.Open("trail/access.jsonl")
	x.NoError(err, "the export has no access record")
	b, err := io.ReadAll(f)
	x.NoError(err)
	x.Contains(string(b), "GET /account/workspaces/{alias}/export")
}
