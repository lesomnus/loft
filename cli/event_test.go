package cli_test

import (
	"bytes"
	"context"
	"encoding/base64"
	"slices"
	"testing"
	"time"

	"github.com/lesomnus/payday/frame"
	"github.com/lesomnus/payday/pdid"
	"github.com/lesomnus/payday/pdpb"
	"github.com/lesomnus/payday/pdtest"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"

	app "github.com/lesomnus/loft"
	"github.com/lesomnus/loft/cli"
	"github.com/lesomnus/loft/cmd"
	entaccess "github.com/lesomnus/loft/internal/ent/access"
	"github.com/lesomnus/loft/internal/ent/event"
	"github.com/lesomnus/loft/internal/ent/holder"
	"github.com/lesomnus/loft/internal/ent/notification"
	enttenant "github.com/lesomnus/loft/internal/ent/tenant"
	"github.com/lesomnus/loft/server/domain"
	"github.com/lesomnus/loft/server/mail"
	"github.com/lesomnus/loft/server/pd"
)

// TestAnOperatorTakesAnEventsReasonOut is `loft event redact`: what a
// workspace was asked by a person whose words an event's reason keeps, done by
// the deployment's operator on its word. It says what it would do, without
// what the reason says, until told `--yes`; then the reason is gone, the act is
// an event of the workspace's whose reason is the request's reference, the
// access record says what the act read and wrote -- by the operator, and with
// neither reason in it -- and every row is as the trail says it is.
func TestAnOperatorTakesAnEventsReasonOut(t *testing.T) {
	x := require.New(t)
	ctx := t.Context()

	c := cmd.Config{}
	c.Db.Driver, c.Db.Dsn = pdtest.DB(t)
	c.Watch.Broker = "memory"
	c.App.Files = t.TempDir()
	c.Auth.Roster.Db.Driver, c.Auth.Roster.Db.Dsn = pdtest.DB(t)
	c.Audit.Digest.Key = "base64:" + base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{7}, 32))
	run(t, cli.NewCmdInit(&c), "--tenant", "acme", "--login", "owner", "--password", "owner-pass-1234")

	const words = "박지민 씨 개인 사정으로 이동 (010-2345-6789)"
	var id pdid.Id
	on(t, c, func(s *cmd.Server) {
		tn, err := s.Ent.Tenant.Query().Where(enttenant.Alias("acme")).Only(ctx)
		x.NoError(err)
		h, err := s.Ent.Holder.Query().Where(holder.TenantId(tn.Id), holder.Alias("owner")).Only(ctx)
		x.NoError(err)
		owner := frame.Into(ctx, frame.New(pdid.Id(h.Id), pdid.Id(tn.Id), frame.Whole()).WithScope(frame.Only(pdid.Id(tn.Id))))
		room, err := s.Walled.Asset().Add(owner, app.AssetAddRequest_builder{Name: "회의실", Kind: "space"}.Build())
		x.NoError(err)
		a, err := s.Walled.Asset().Add(owner, app.AssetAddRequest_builder{Name: "노트북"}.Build())
		x.NoError(err)
		_, err = s.Walled.Asset().Move(owner, app.AssetMoveRequest_builder{Ref: app.AssetById(a.GetId()), To: app.AssetById(room.GetId()), Reason: words}.Build())
		x.NoError(err)
		aid, err := pdid.From(a.GetId())
		x.NoError(err)
		v, err := s.Ent.Event.Query().Where(event.Kind("asset.move"), event.SubjectId(aid.Uuid())).Only(ctx)
		x.NoError(err)
		id = pdid.Id(v.Id)
	})
	reason := func() string {
		var v string
		on(t, c, func(s *cmd.Server) {
			e, err := s.Ent.Event.Get(ctx, id.Uuid())
			x.NoError(err)
			v = e.Reason
		})
		return v
	}
	redact := func(args ...string) error {
		return cli.NewCmdEvent(&c).Run(ctx, append([]string{"redact", "--tenant", "acme"}, args...))
	}

	x.ErrorContains(redact("--event", id.String()), "--why")
	x.ErrorContains(redact("--event", "x", "--why", "삭제 요청 2026-20"), "--event")
	x.ErrorContains(redact("--event", pdid.New(pd.AssetDomain).String(), "--why", "삭제 요청 2026-20"), "no event's identifier")

	out := run(t, cli.NewCmdEvent(&c), "redact", "--tenant", "acme", "--event", id.String(), "--why", "삭제 요청 2026-20")
	x.Contains(out, "asset.move")
	x.Contains(out, "would take its reason out")
	x.NotContains(out, "박지민", "what it says is nobody's to print")
	x.Equal(words, reason(), "nothing was done")

	out = run(t, cli.NewCmdEvent(&c), "redact", "--tenant", "acme", "--event", id.String(), "--why", " 삭제 요청 2026-20 ", "--by", "ops@desk", "--yes")
	x.Contains(out, "took the reason out of event "+id.String())
	x.Empty(reason())

	on(t, c, func(s *cmd.Server) {
		act, err := s.Ent.Event.Query().Where(event.Kind("event.redact")).Only(ctx)
		x.NoError(err)
		x.Equal("삭제 요청 2026-20", act.Reason)
		x.Nil(act.ActorId, "the deployment's own work")
		x.Contains(out, pdid.Id(act.Id).String(), "it says which event the act is")

		// What the act read and wrote, by the operator, and nothing anybody
		// said.
		rs, err := s.Ent.Access.Query().Where(entaccess.Action("loft event redact")).All(ctx)
		x.NoError(err)
		kinds := map[string][]pdid.Id{}
		for _, r := range rs {
			x.Equal("ok", r.Result)
			x.Equal(act.TenantId, r.TenantId, "the workspace's record")
			d := &pdpb.AccessDetail{}
			x.NoError(proto.Unmarshal(r.Value, d))
			x.Equal("ops@desk", d.GetAddress())
			x.Empty(d.GetReason())
			x.Empty(d.GetRequest())
			for _, v := range r.Subjects {
				kinds[r.Kind] = append(kinds[r.Kind], pdid.Id(v))
			}
		}
		x.True(slices.Contains(kinds["read"], id), "what it read: %v", kinds)
		x.True(slices.Contains(kinds["write"], pdid.Id(act.Id)), "what it wrote: %v", kinds)
		all, err := s.Ent.Access.Query().All(ctx)
		x.NoError(err)
		for _, r := range all {
			x.NotContains(string(r.Value), "박지민", r.Action)
			x.NotContains(string(r.Value), "2026-20", r.Action)
		}
	})

	// Once is all there is to take out.
	out = run(t, cli.NewCmdEvent(&c), "redact", "--tenant", "acme", "--event", id.String(), "--why", "삭제 요청 2026-20", "--yes")
	x.Contains(out, "it has no reason")

	// And every row is as the trail says: the event's, made again of it.
	x.NoError(cli.NewCmdTrail(&c).Run(ctx, []string{"rows"}))
}

// TestAnOperatorIsToldWhereElseAnOperationSaysItAndTakesItOutThereToo: what a
// hand-over's memo says, each thing it handed over copies (design D49), and
// the hand-over keeps as its own words (D56). Before `--yes` the operator is
// told how many events of the operation say the same, and how many rows keep
// it, and of which kinds, and nothing of what they say; with it, the words
// are taken out of all of them, the hand-over's memo on the access record as
// the operator's write.
func TestAnOperatorIsToldWhereElseAnOperationSaysItAndTakesItOutThereToo(t *testing.T) {
	x := require.New(t)
	ctx := t.Context()

	c := cmd.Config{}
	c.Db.Driver, c.Db.Dsn = pdtest.DB(t)
	c.Watch.Broker = "memory"
	c.App.Files = t.TempDir()
	c.Auth.Roster.Db.Driver, c.Auth.Roster.Db.Dsn = pdtest.DB(t)
	c.Audit.Digest.Key = "base64:" + base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{7}, 32))
	run(t, cli.NewCmdInit(&c), "--tenant", "acme", "--login", "owner", "--password", "owner-pass-1234")

	const words = "박지민 씨 개인 사정으로 지급 (010-2345-6789)"
	var said []pdid.Id
	var handed pdid.Id
	on(t, c, func(s *cmd.Server) {
		tn, err := s.Ent.Tenant.Query().Where(enttenant.Alias("acme")).Only(ctx)
		x.NoError(err)
		h, err := s.Ent.Holder.Query().Where(holder.TenantId(tn.Id), holder.Alias("owner")).Only(ctx)
		x.NoError(err)
		owner := frame.Into(ctx, frame.New(pdid.Id(h.Id), pdid.Id(tn.Id), frame.Whole()).WithScope(frame.Only(pdid.Id(tn.Id))))
		var lines []*app.CustodyLineSpec
		for _, name := range []string{"노트북", "모니터"} {
			a, err := s.Walled.Asset().Add(owner, app.AssetAddRequest_builder{Name: name}.Build())
			x.NoError(err)
			lines = append(lines, app.CustodyLineSpec_builder{Asset: app.AssetById(a.GetId())}.Build())
		}
		p, err := s.Walled.Party().Add(owner, app.PartyAddRequest_builder{Name: "김철수", Kind: "person"}.Build())
		x.NoError(err)
		cu, err := s.Walled.Custody().Add(owner, app.CustodyAddRequest_builder{Party: app.PartyRef_builder{Id: p.GetId()}.Build(), Desc: words, Lines: lines}.Build())
		x.NoError(err)
		cid, err := pdid.From(cu.GetId())
		x.NoError(err)
		handed = cid
		own, err := s.Ent.Event.Query().Where(event.Kind("custody.issue"), event.SubjectId(cid.Uuid())).Only(ctx)
		x.NoError(err)
		said = append(said, pdid.Id(own.Id))
		parts, err := s.Ent.Event.Query().Where(event.CauseId(own.Id)).All(ctx)
		x.NoError(err)
		for _, v := range parts {
			said = append(said, pdid.Id(v.Id))
		}
	})
	x.Len(said, 3)
	left := func() int {
		n := 0
		on(t, c, func(s *cmd.Server) {
			for _, id := range said {
				e, err := s.Ent.Event.Get(ctx, id.Uuid())
				x.NoError(err)
				if e.Reason != "" {
					n++
				}
			}
		})
		return n
	}
	memo := func() string {
		var v string
		on(t, c, func(s *cmd.Server) {
			cu, err := s.Ent.Custody.Get(ctx, handed.Uuid())
			x.NoError(err)
			v = cu.Desc
		})
		return v
	}

	out := run(t, cli.NewCmdEvent(&c), "redact", "--tenant", "acme", "--event", said[0].String(), "--why", "삭제 요청 2026-26")
	x.Contains(out, "says the same in 2 other events")
	x.Contains(out, "custody.issue ×2")
	x.Contains(out, "1 row(s) keep the same as their own words")
	x.Contains(out, "Custody ×1")
	x.Contains(out, "would take its reason out")
	x.NotContains(out, "박지민")
	x.Equal(3, left(), "nothing was done")
	x.Equal(words, memo())

	out = run(t, cli.NewCmdEvent(&c), "redact", "--tenant", "acme", "--event", said[0].String(), "--why", "삭제 요청 2026-26", "--by", "ops@desk", "--yes")
	x.Contains(out, "took the reason out of event "+said[0].String())
	x.Contains(out, "and out of the 2 other events of its operation")
	x.Contains(out, "and out of the 1 row(s) that kept the same as their own words")
	x.Zero(left())
	x.Empty(memo(), "taken out of the hand-over's memo")
	on(t, c, func(s *cmd.Server) {
		rs, err := s.Ent.Access.Query().Where(entaccess.Action("loft event redact"), entaccess.KindEQ("write")).All(ctx)
		x.NoError(err)
		var wrote []pdid.Id
		for _, r := range rs {
			for _, v := range r.Subjects {
				wrote = append(wrote, pdid.Id(v))
			}
		}
		x.Contains(wrote, handed, "the hand-over, as the operator's write")
	})

	// Asked of another of them, there is nothing left.
	out = run(t, cli.NewCmdEvent(&c), "redact", "--tenant", "acme", "--event", said[2].String(), "--why", "삭제 요청 2026-26", "--yes")
	x.Contains(out, "it has no reason")
	x.NoError(cli.NewCmdTrail(&c).Run(ctx, []string{"rows"}))
}

// posted is a mail server that keeps what it was sent.
type posted struct{ sent []mail.Message }

func (p *posted) Send(_ context.Context, v mail.Message) error {
	p.sent = append(p.sent, v)
	return nil
}

// TestAnOperatorIsToldOfTheNoticesThatSayItAndTakesItOutOfThem: a rejection's
// reason is the words of the notice whoever asked for the reservation was
// told in, and was mailed (design D51). Before `--yes` the operator is told
// how many notices say it and of which kinds, and how many went by mail --
// which is not taken back -- and nothing of what they say; with it, the
// words are taken out of the notice, which is on the access record as the
// operator's write, and every row is as the trail says.
func TestAnOperatorIsToldOfTheNoticesThatSayItAndTakesItOutOfThem(t *testing.T) {
	x := require.New(t)
	ctx := t.Context()

	c := cmd.Config{}
	c.Db.Driver, c.Db.Dsn = pdtest.DB(t)
	c.Watch.Broker = "memory"
	c.App.Files = t.TempDir()
	c.Auth.Roster.Db.Driver, c.Auth.Roster.Db.Dsn = pdtest.DB(t)
	c.Audit.Digest.Key = "base64:" + base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{7}, 32))
	run(t, cli.NewCmdInit(&c), "--tenant", "acme", "--login", "owner", "--password", "owner-pass-1234")

	const words = "박지민 씨 개인 사정으로 반려 (010-2345-6789)"
	var rejected, told pdid.Id
	on(t, c, func(s *cmd.Server) {
		tn, err := s.Ent.Tenant.Query().Where(enttenant.Alias("acme")).Only(ctx)
		x.NoError(err)
		h, err := s.Ent.Holder.Query().Where(holder.TenantId(tn.Id), holder.Alias("owner")).Only(ctx)
		x.NoError(err)
		owner := frame.Into(ctx, frame.New(pdid.Id(h.Id), pdid.Id(tn.Id), frame.Whole()).WithScope(frame.Only(pdid.Id(tn.Id))))
		hall, err := s.Walled.Asset().Add(owner, app.AssetAddRequest_builder{Name: "강당", Kind: "space"}.Build())
		x.NoError(err)
		_, err = s.Walled.Bookable().Add(owner, app.BookableAddRequest_builder{Asset: app.AssetById(hall.GetId()), Approval: true}.Build())
		x.NoError(err)
		p, err := s.Walled.Party().Add(owner, app.PartyAddRequest_builder{Name: "김철수", Kind: "person", Email: "kim@acme.example"}.Build())
		x.NoError(err)
		_, err = s.Walled.Party().Invite(owner, app.PartyInviteRequest_builder{Ref: app.PartyRef_builder{Id: p.GetId()}.Build(), Alias: "kim", Role: "member", Password: "kim-password-1234"}.Build())
		x.NoError(err)
		at := time.Now().Add(24 * time.Hour).Truncate(time.Hour)
		r, err := s.Walled.Reservation().Add(owner, app.ReservationAddRequest_builder{
			Name:     "인터뷰",
			Party:    app.PartyRef_builder{Id: p.GetId()}.Build(),
			BeginsAt: timestamppb.New(at),
			EndsAt:   timestamppb.New(at.Add(time.Hour)),
			Items:    []*app.ReservationItemSpec{app.ReservationItemSpec_builder{Resource: app.AssetById(hall.GetId())}.Build()},
		}.Build())
		x.NoError(err)
		x.Equal("requested", r.GetStatus())

		// Rejected, and mailed to whoever it was for.
		box := &posted{}
		s.Deps.Mail = box
		s.Deps.Links.App = "https://loft.example"
		_, err = s.Walled.Reservation().Reject(owner, app.ReservationDecideRequest_builder{Ref: app.ReservationRef_builder{Id: r.GetId()}.Build(), Reason: words}.Build())
		x.NoError(err)
		x.NoError(domain.Mailer{Drv: s.Drv, Deps: s.Deps}.Pass(ctx))
		x.Len(box.sent, 1)
		x.Contains(box.sent[0].Body, words)

		rid, err := pdid.From(r.GetId())
		x.NoError(err)
		v, err := s.Ent.Event.Query().Where(event.Kind("reservation.reject"), event.SubjectId(rid.Uuid())).Only(ctx)
		x.NoError(err)
		rejected = pdid.Id(v.Id)
		n, err := s.Ent.Notification.Query().Where(notification.Kind("reservation.decided"), notification.SubjectId(rid.Uuid())).Only(ctx)
		x.NoError(err)
		x.NotNil(n.DateMailed)
		told = pdid.Id(n.Id)
	})
	says := func() string {
		var v string
		on(t, c, func(s *cmd.Server) {
			n, err := s.Ent.Notification.Get(ctx, told.Uuid())
			x.NoError(err)
			v = n.Desc
		})
		return v
	}

	out := run(t, cli.NewCmdEvent(&c), "redact", "--tenant", "acme", "--event", rejected.String(), "--why", "삭제 요청 2026-32")
	x.Contains(out, "1 notice(s) say the same as their words")
	x.Contains(out, "reservation.decided ×1")
	x.Contains(out, "1 of them went by mail before this, which is not taken back")
	x.Contains(out, "would take its reason out")
	x.NotContains(out, "박지민")
	x.Equal(words, says(), "nothing was done")

	out = run(t, cli.NewCmdEvent(&c), "redact", "--tenant", "acme", "--event", rejected.String(), "--why", "삭제 요청 2026-32", "--by", "ops@desk", "--yes")
	x.Contains(out, "took the reason out of event "+rejected.String())
	x.Contains(out, "and out of the 1 notice(s) that said the same as their words")
	x.Empty(says())

	on(t, c, func(s *cmd.Server) {
		rs, err := s.Ent.Access.Query().Where(entaccess.Action("loft event redact"), entaccess.KindEQ("write")).All(ctx)
		x.NoError(err)
		var wrote []pdid.Id
		for _, r := range rs {
			for _, v := range r.Subjects {
				wrote = append(wrote, pdid.Id(v))
			}
		}
		x.Contains(wrote, told, "the notice, as the operator's write")
		all, err := s.Ent.Access.Query().All(ctx)
		x.NoError(err)
		for _, r := range all {
			x.NotContains(string(r.Value), "박지민", r.Action)
		}
	})
	x.NoError(cli.NewCmdTrail(&c).Run(ctx, []string{"rows"}))
}
