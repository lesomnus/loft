package cmd_test

import (
	"bytes"
	"context"
	"encoding/base64"
	"testing"
	"time"

	"github.com/lesomnus/payday/audit"
	"github.com/lesomnus/payday/frame"
	"github.com/lesomnus/payday/grpcx"
	"github.com/lesomnus/payday/pdid"
	"github.com/lesomnus/payday/trail"
	"github.com/protobuf-orm/ent/dialect"
	"github.com/protobuf-orm/protoc-gen-orm-ent/runtime/enttx"
	"google.golang.org/protobuf/types/known/timestamppb"

	app "github.com/lesomnus/loft"
	"github.com/lesomnus/loft/cmd"
	"github.com/lesomnus/loft/internal/ent"
	entaudit "github.com/lesomnus/loft/internal/ent/audit"
	"github.com/lesomnus/loft/internal/ent/holder"
	"github.com/lesomnus/loft/internal/ent/notification"
	"github.com/lesomnus/loft/server/domain"
	"github.com/lesomnus/loft/server/pd"
)

// TestAnOperationUndoneMailsNothing: a notification is queued in the
// operation's own transaction and mailed after it, so an operation that is
// undone -- here, one that joined a transaction rolled back after it, as an
// operation inside a call that writes more does when a later write is
// refused -- leaves nothing to mail, where the same operation on its own is
// mailed.
func TestAnOperationUndoneMailsNothing(t *testing.T) {
	// With a digest key from the first write, since a check of the rows finds
	// a line written without one (payday#115): what is asked of it below is
	// about the mailer's writes, not a deployment given a key late.
	key := bytes.Repeat([]byte{7}, 32)
	w := newWorldWith(t, func(c *cmd.Config) {
		c.Audit.Digest.Key = "base64:" + base64.StdEncoding.EncodeToString(key)
	})
	x := w.x
	ctx := context.Background()
	box := &mailbox{}
	w.s.Deps.Mail = box
	owner, err := w.s.Ent.Holder.Query().Where(holder.Alias("owner"), holder.DateErasedIsNil()).First(ctx)
	x.NoError(err)
	x.NoError(w.s.Identity.AddAddress(ctx, pdid.Id(owner.Id), "owner@acme.example", true))

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
			Name:      "회의",
			DateStart: timestamppb.New(at),
			DateEnd:   timestamppb.New(at.Add(time.Hour)),
			Items: []*app.ReservationItemSpec{app.ReservationItemSpec_builder{
				Resource: app.AssetRef_builder{Id: hall.GetId()}.Build(),
			}.Build()},
		}.Build()
	}
	mail := func() {
		x.NoError(domain.Mailer{Drv: w.s.Drv, Deps: w.s.Deps}.Pass(ctx))
	}
	tomorrow := time.Now().Add(24 * time.Hour).Truncate(time.Hour)

	// Asked as kim, served as the method kim would have called, on the stack
	// a caller reaches rebound onto a transaction: the domain layer joins it
	// rather than opening its own, and is undone with it.
	kid, err := pdid.From(kh.GetId())
	x.NoError(err)
	tid, err := pdid.From(w.tenant.GetId())
	x.NoError(err)
	asKim := frame.Into(ctx, frame.New(kid, tid, frame.Whole()).WithScope(frame.Only(tid)).WithRow(kh))
	tdrv, tx, err := dialect.BeginTx(ctx, w.s.Drv)
	x.NoError(err)
	defer tx.Rollback()
	walled, err := enttx.Rebind(w.s.Walled, tdrv)
	x.NoError(err)
	_, err = walled.Reservation().Add(grpcx.AsMethod(asKim, app.ReservationService_Add_FullMethodName), reserve(tomorrow))
	x.NoError(err)
	queued, err := ent.NewClient(ent.Driver(tdrv)).Notification.Query().Where(notification.Kind("reservation.requested")).Count(ctx)
	x.NoError(err)
	x.Equal(1, queued, "the request was queued in the transaction")
	x.NoError(tx.Rollback())
	n, err := w.s.Ent.Notification.Query().Where(notification.Kind("reservation.requested")).Count(ctx)
	x.NoError(err)
	x.Zero(n, "the request was undone with it")
	mail()
	x.Empty(box.sent, "and nothing went by mail")

	// The mailer's own writes are recorded acts, with a digest, so that a
	// check of the rows finds the notification as the mailer left it
	// (payday#108).
	d, err := audit.NewDigest(key)
	x.NoError(err)

	_, err = app.NewReservationServiceClient(w.conn).Add(kim, reserve(tomorrow.Add(2*time.Hour)))
	x.NoError(err)
	mail()
	x.Len(box.sent, 1, "the same request on its own is mailed")
	x.Equal("owner@acme.example", box.sent[0].To)
	// From the workspace by what it is called: the world's has no name of
	// its own, and its owner no person record (D57).
	x.Contains(box.sent[0].Body, "Loft의 owner의 워크스페이스에서 온 알림입니다.")

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
