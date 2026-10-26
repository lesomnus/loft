package cli_test

import (
	"bytes"
	"encoding/base64"
	"testing"
	"time"

	"github.com/lesomnus/payday/frame"
	"github.com/lesomnus/payday/pdid"
	"github.com/lesomnus/payday/pdtest"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/timestamppb"

	app "github.com/lesomnus/rove"
	"github.com/lesomnus/rove/cli"
	"github.com/lesomnus/rove/cmd"
	"github.com/lesomnus/rove/internal/ent/allocation"
	"github.com/lesomnus/rove/internal/ent/holder"
	"github.com/lesomnus/rove/internal/ent/notification"
	enttenant "github.com/lesomnus/rove/internal/ent/tenant"
)

// TestAMigrationHoldsWhatABuildBeforeLeftOut: what a build from before a
// hand-over held its things' time handed over holds none of it (D53). `rove
// migrate` holds it from when it was handed over -- through the servers, so
// that every row is as the trail says -- and what was booked over it says so
// and is told; asked again, it has nothing left to hold.
func TestAMigrationHoldsWhatABuildBeforeLeftOut(t *testing.T) {
	x := require.New(t)
	ctx := t.Context()

	c := cmd.Config{}
	c.Db.Driver, c.Db.Dsn = pdtest.DB(t)
	c.Watch.Broker = "memory"
	c.App.Files = t.TempDir()
	c.Auth.Roster.Db.Driver, c.Auth.Roster.Db.Dsn = pdtest.DB(t)
	c.Audit.Digest.Key = "base64:" + base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{7}, 32))
	run(t, cli.NewCmdInit(&c), "--tenant", "acme", "--login", "owner", "--password", "owner-pass-1234")

	var camera, booking []byte
	tomorrow := time.Now().Add(24 * time.Hour).Truncate(time.Hour)
	on(t, c, func(s *cmd.Server) {
		tn, err := s.Ent.Tenant.Query().Where(enttenant.Alias("acme")).Only(ctx)
		x.NoError(err)
		h, err := s.Ent.Holder.Query().Where(holder.TenantId(tn.Id), holder.Alias("owner")).Only(ctx)
		x.NoError(err)
		owner := frame.Into(ctx, frame.New(pdid.Id(h.Id), pdid.Id(tn.Id), frame.Whole()).WithScope(frame.Only(pdid.Id(tn.Id))))
		a, err := s.Walled.Asset().Add(owner, app.AssetAddRequest_builder{Name: "카메라", Tag: "CM-1"}.Build())
		x.NoError(err)
		camera = a.GetId()
		_, err = s.Walled.Bookable().Add(owner, app.BookableAddRequest_builder{Asset: app.AssetById(camera)}.Build())
		x.NoError(err)
		p, err := s.Walled.Party().Add(owner, app.PartyAddRequest_builder{Name: "김민준", Kind: "person"}.Build())
		x.NoError(err)

		// Lent as a build from before lent it: through the servers, and
		// holding nothing of its time.
		tenant := app.TenantRef_builder{Id: tn.Id[:]}.Build()
		k, err := s.Base.Custody().Add(owner, app.CustodyAddRequest_builder{
			Tenant:   tenant,
			Party:    app.PartyById(p.GetId()),
			Kind:     "loan",
			Status:   "open",
			IssuedAt: timestamppb.Now(),
			DueAt:    timestamppb.New(tomorrow.Add(2 * time.Hour)),
		}.Build())
		x.NoError(err)
		_, err = s.Base.CustodyLine().Add(owner, app.CustodyLineAddRequest_builder{
			Tenant:   tenant,
			Custody:  app.CustodyById(k.GetId()),
			Asset:    app.AssetById(camera),
			Quantity: 1,
			OutAt:    timestamppb.Now(),
		}.Build())
		x.NoError(err)

		// And booked over, which nothing refused.
		r, err := s.Walled.Reservation().Add(owner, app.ReservationAddRequest_builder{
			Name:     "촬영",
			BeginsAt: timestamppb.New(tomorrow),
			EndsAt:   timestamppb.New(tomorrow.Add(time.Hour)),
			Items:    []*app.ReservationItemSpec{app.ReservationItemSpec_builder{Resource: app.AssetById(camera)}.Build()},
		}.Build())
		x.NoError(err)
		booking = r.GetId()
	})

	out := run(t, cli.NewCmdMigrate(&c))
	x.Contains(out, "what is out holds its time: 1 things")
	again := run(t, cli.NewCmdMigrate(&c))
	x.NotContains(again, "holds its time", "nothing left to hold")
	x.NoError(cli.NewCmdTrail(&c).Run(ctx, []string{"rows"}), "every row as the trail says it is")

	on(t, c, func(s *cmd.Server) {
		hs, err := s.Ent.Allocation.Query().Where(allocation.Kind("custody"), allocation.ResourceId(pdid.Id(camera).Uuid())).All(ctx)
		x.NoError(err)
		x.Len(hs, 1)
		x.True(hs[0].Blocking)
		x.True(hs[0].EndsAt.Equal(tomorrow.Add(2*time.Hour)), "until it is due back")

		tn, err := s.Ent.Tenant.Query().Where(enttenant.Alias("acme")).Only(ctx)
		x.NoError(err)
		h, err := s.Ent.Holder.Query().Where(holder.TenantId(tn.Id), holder.Alias("owner")).Only(ctx)
		x.NoError(err)
		owner := frame.Into(ctx, frame.New(pdid.Id(h.Id), pdid.Id(tn.Id), frame.Whole()).WithScope(frame.Only(pdid.Id(tn.Id))))
		cal, err := s.Walled.Reservation().Calendar(owner, app.ReservationCalendarRequest_builder{
			From: timestamppb.New(tomorrow.Add(-time.Hour)),
			To:   timestamppb.New(tomorrow.Add(2 * time.Hour)),
		}.Build())
		x.NoError(err)
		x.Len(cal.GetEntries(), 1)
		x.Equal(booking, cal.GetEntries()[0].GetReservation().GetId())
		x.Len(cal.GetEntries()[0].GetOut(), 1, "what was booked over it says so")
		n, err := s.Ent.Notification.Query().Where(notification.Kind("reservation.out"), notification.HolderId(h.Id)).Count(ctx)
		x.NoError(err)
		x.Positive(n, "and is told, by the deployment")
		mailed, err := s.Ent.Notification.Query().Where(notification.Kind("reservation.out"), notification.Mail(true)).Count(ctx)
		x.NoError(err)
		x.Zero(mailed, "a migration sends nothing")
	})
}
