package cmd_test

import (
	"bytes"
	"context"
	"testing"
	"time"

	"github.com/lesomnus/payday/audit"
	"github.com/lesomnus/payday/pdid"
	"github.com/lesomnus/payday/pdpb"
	"github.com/lesomnus/payday/trail"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/anypb"
	"google.golang.org/protobuf/types/known/timestamppb"

	app "github.com/lesomnus/rove"
	"github.com/lesomnus/rove/internal/ent"
	entaudit "github.com/lesomnus/rove/internal/ent/audit"
	"github.com/lesomnus/rove/internal/ent/holder"
	"github.com/lesomnus/rove/internal/ent/notification"
	"github.com/lesomnus/rove/server/domain"
	"github.com/lesomnus/rove/server/pd"
)

// TestAnOperationUndoneMailsNothing: a notification is queued in the
// operation's own transaction and mailed after it, so an operation that is
// undone -- here, the first half of a batch whose second half is refused --
// leaves nothing to mail, where the same operation on its own is mailed.
func TestAnOperationUndoneMailsNothing(t *testing.T) {
	w := newWorld(t)
	x := w.x
	ctx := context.Background()
	box := &mailbox{}
	w.s.Deps.Mail = box
	owner, err := w.s.Ent.Holder.Query().Where(holder.Alias("owner"), holder.DateErasedIsNil()).First(ctx)
	x.NoError(err)
	x.NoError(w.s.Identity.AddAddress(ctx, pdid.Id(*owner.Account), "owner@acme.example", true))

	assets := app.NewAssetServiceClient(w.conn)
	hall, err := assets.Add(w.owner, app.AssetAddRequest_builder{Name: "강당", Kind: "space"}.Build())
	x.NoError(err)
	_, err = app.NewBookableServiceClient(w.conn).Add(w.owner, app.BookableAddRequest_builder{
		Asset: app.AssetRef_builder{Id: hall.GetId()}.Build(), Approval: true,
	}.Build())
	x.NoError(err)
	kh := w.holder("kim", "member")
	_, err = w.s.Base.Party().Add(ctx, app.PartyAddRequest_builder{
		Tenant: app.TenantRef_builder{Id: w.tenant.GetId()}.Build(),
		Name:   "김민수", Kind: "person",
		Holder: app.HolderRef_builder{Id: kh.GetId()}.Build(),
	}.Build())
	x.NoError(err)
	kim := w.signIn(kh)

	reserve := func(at time.Time) *app.ReservationAddRequest {
		return app.ReservationAddRequest_builder{
			Name:     "회의",
			BeginsAt: timestamppb.New(at),
			EndsAt:   timestamppb.New(at.Add(time.Hour)),
			Items: []*app.ReservationItemSpec{app.ReservationItemSpec_builder{
				Resource: app.AssetRef_builder{Id: hall.GetId()}.Build(),
			}.Build()},
		}.Build()
	}
	mail := func() {
		x.NoError(domain.Mailer{Drv: w.s.Drv, Deps: w.s.Deps}.Pass(ctx))
	}
	tomorrow := time.Now().Add(24 * time.Hour).Truncate(time.Hour)

	add, err := anypb.New(reserve(tomorrow))
	x.NoError(err)
	// The same time again, which the first holds while it waits: allowed to
	// ask, and refused once asked, after the first was written.
	again, err := anypb.New(reserve(tomorrow))
	x.NoError(err)
	_, err = pdpb.NewBatchServiceClient(w.conn).Do(kim, pdpb.BatchRequest_builder{Ops: []*pdpb.Op{
		pdpb.Op_builder{Method: "/rove.ReservationService/Add", Request: add}.Build(),
		pdpb.Op_builder{Method: "/rove.ReservationService/Add", Request: again}.Build(),
	}}.Build())
	x.Equal(codes.Aborted, status.Code(err), "the second overlaps the first, so the batch is undone: %v", err)
	n, err := w.s.Ent.Notification.Query().Where(notification.Kind("reservation.requested")).Count(ctx)
	x.NoError(err)
	x.Zero(n, "the request was undone with it")
	mail()
	x.Empty(box.sent, "and nothing went by mail")

	// The mailer's own writes are recorded acts, with a digest, so that a
	// check of the rows finds the notification as the mailer left it
	// (payday#108).
	d, err := audit.NewDigest(bytes.Repeat([]byte{7}, 32))
	x.NoError(err)
	w.s.Deps.Audit = audit.Contents{Digest: d}

	_, err = app.NewReservationServiceClient(w.conn).Add(kim, reserve(tomorrow.Add(2*time.Hour)))
	x.NoError(err)
	mail()
	x.Len(box.sent, 1, "the same request on its own is mailed")
	x.Equal("owner@acme.example", box.sent[0].To)

	sent, err := w.s.Ent.Notification.Query().Where(notification.Kind("reservation.requested")).Only(ctx)
	x.NoError(err)
	rows, err := w.s.Ent.Audit.Query().Where(entaudit.ObjectId(sent.Id)).Order(ent.Asc(entaudit.FieldDateCreated), ent.Asc(entaudit.FieldId)).All(ctx)
	x.NoError(err)
	acts := []string{}
	for _, v := range rows[1:] {
		acts = append(acts, v.Action)
	}
	x.Equal([]string{domain.MailTried, domain.MailSent}, acts)

	got, err := pd.Digests(ctx, w.s.Ent, d, trail.Policy{})
	x.NoError(err)
	x.True(got.Ok(), "%+v", got)
	x.Positive(got.Matches, "the notification's newest row of the trail keeps a digest")
}
