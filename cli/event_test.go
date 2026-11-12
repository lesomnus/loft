package cli_test

import (
	"bytes"
	"context"
	"encoding/base64"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/lesomnus/payday/frame"
	"github.com/lesomnus/payday/pdid"
	"github.com/lesomnus/payday/pdpb"
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
	"github.com/lesomnus/loft/internal/ent/tenant"
	"github.com/lesomnus/loft/server/domain"
	"github.com/lesomnus/loft/server/mail"
	"github.com/lesomnus/loft/server/operator"
	"github.com/lesomnus/loft/server/pd"
)

// digested is a deployment whose live rows are checked against the trail's
// digests, so that `trail rows` says whether every row is as the trail says.
func digested(t *testing.T) *deployment {
	return deployWith(t, func(c *cmd.Config) {
		c.Audit.Digest.Key = "base64:" + base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{7}, 32))
	})
}

// owning is acme's owner, as a call from inside it is framed.
func owning(t *testing.T, d *deployment) context.Context {
	t.Helper()
	ctx := t.Context()
	tn, err := d.s.Ent.Tenant.Query().Where(tenant.Alias("acme")).Only(ctx)
	require.NoError(t, err)
	h, err := d.s.Ent.Holder.Query().Where(holder.TenantId(tn.Id), holder.Alias("owner")).Only(ctx)
	require.NoError(t, err)
	return frame.Into(ctx, frame.New(pdid.Id(h.Id), pdid.Id(tn.Id), frame.Whole()).WithScope(frame.Only(pdid.Id(tn.Id))))
}

// TestAnOperatorTakesAnEventsReasonOut is `loft event redact`: what a
// workspace was asked by a person whose words an event's reason keeps, done
// by the operator on its word. It says what it would do, without what the
// reason says, and changes nothing until told `--yes`; then the reason is
// gone, the act is an event of the workspace's whose reason is the request's
// reference and whose actor is the operator, the access record says what the
// act read and wrote -- by the operator, from the entry point, and with
// neither reason in it -- and every row is as the trail says it is.
func TestAnOperatorTakesAnEventsReasonOut(t *testing.T) {
	x := require.New(t)
	ctx := t.Context()
	d := digested(t)
	alice := operator.Id("alice")

	const words = "박지민 씨 개인 사정으로 이동 (010-2345-6789)"
	owner := owning(t, d)
	room, err := d.s.Walled.Asset().Add(owner, app.AssetAddRequest_builder{Name: "회의실", Kind: "space"}.Build())
	x.NoError(err)
	a, err := d.s.Walled.Asset().Add(owner, app.AssetAddRequest_builder{Name: "노트북"}.Build())
	x.NoError(err)
	_, err = d.s.Walled.Asset().Move(owner, app.AssetMoveRequest_builder{Ref: app.AssetById(a.GetId()), To: app.AssetById(room.GetId()), Reason: words}.Build())
	x.NoError(err)
	aid, err := pdid.From(a.GetId())
	x.NoError(err)
	v, err := d.s.Ent.Event.Query().Where(event.Kind("asset.move"), event.SubjectId(aid.Uuid())).Only(ctx)
	x.NoError(err)
	id := pdid.Id(v.Id)
	reason := func() string {
		e, err := d.s.Ent.Event.Get(ctx, id.Uuid())
		x.NoError(err)
		return e.Reason
	}
	cli.Elsewhere(t, d.c, "beta", "lee", false)

	for want, args := range map[string][]string{
		"--why":                 {"--tenant", "acme", "--event", id.String()},
		"--event":               {"--tenant", "acme", "--event", "x", "--why", "삭제 요청 2026-20"},
		"no event's identifier": {"--tenant", "acme", "--event", pdid.New(pd.AssetDomain).String(), "--why", "삭제 요청 2026-20"},
		"--tenant":              {"--event", id.String(), "--why", "삭제 요청 2026-20"},
		"@beta has no event":    {"--tenant", "beta", "--event", id.String(), "--why", "삭제 요청 2026-20", "--yes"},
		"nowhere":               {"--tenant", "nowhere", "--event", id.String(), "--why", "삭제 요청 2026-20", "--yes"},
	} {
		_, err := d.loft(t, append([]string{"event", "redact"}, args...)...)
		x.ErrorContains(err, want, "%v", args)
	}
	x.Equal(words, reason(), "refused, and nothing was done")

	was := tablesOf(t, d.c.Db.Driver, d.c.Db.Dsn)
	out, err := d.loft(t, "event", "redact", "--tenant", "acme", "--event", id.String(), "--why", "삭제 요청 2026-20")
	x.NoError(err, out)
	x.Contains(out, "event "+id.String()+" of @acme: asset.move")
	x.Contains(out, "would take its reason out")
	x.NotContains(out, "박지민", "what it says is nobody's to print")
	x.Equal(words, reason(), "nothing was done")
	unchanged(t, was, tablesOf(t, d.c.Db.Driver, d.c.Db.Dsn), entaccess.Table)

	out, err = d.loft(t, "event", "redact", "--tenant", "acme", "--event", id.String(), "--why", " 삭제 요청 2026-20 ", "--yes")
	x.NoError(err, out)
	x.Contains(out, "took the reason out of event "+id.String())
	x.Empty(reason())

	act, err := d.s.Ent.Event.Query().Where(event.Kind("event.redact")).Only(ctx)
	x.NoError(err)
	x.Equal("삭제 요청 2026-20", act.Reason)
	x.NotNil(act.ActorId)
	x.Equal(alice.Uuid(), *act.ActorId, "the operator did it")
	x.Contains(out, "the act is event "+pdid.Id(act.Id).String(), "it says which event the act is")

	// What the act read and wrote, by the operator, and nothing anybody said.
	rs, err := d.s.Ent.Access.Query().Where(entaccess.Action(app.EventService_Redact_FullMethodName)).All(ctx)
	x.NoError(err)
	x.NotEmpty(rs)
	kinds := map[string][]pdid.Id{}
	for _, r := range rs {
		x.Equal("ok", r.Result)
		x.Equal(act.TenantId, r.TenantId, "the workspace's record")
		x.Equal(alice.Uuid(), r.ActorId)
		d := &pdpb.AccessDetail{}
		x.NoError(proto.Unmarshal(r.Value, d))
		x.True(strings.HasPrefix(d.GetAddress(), "admin:"), d.GetAddress())
		x.Empty(d.GetReason())
		x.Empty(d.GetRequest())
		for _, v := range r.Subjects {
			kinds[r.Kind] = append(kinds[r.Kind], pdid.Id(v))
		}
	}
	x.True(slices.Contains(kinds["read"], id), "what it read: %v", kinds)
	x.True(slices.Contains(kinds["write"], pdid.Id(act.Id)), "what it wrote: %v", kinds)
	all, err := d.s.Ent.Access.Query().All(ctx)
	x.NoError(err)
	for _, r := range all {
		x.NotContains(string(r.Value), "박지민", r.Action)
		x.NotContains(string(r.Value), "2026-20", r.Action)
	}

	// Once is all there is to take out, and what took it out is said.
	out, err = d.loft(t, "event", "redact", "--tenant", "acme", "--event", id.String(), "--why", "삭제 요청 2026-20", "--yes")
	x.NoError(err, out)
	x.Contains(out, "it has no reason")
	x.Contains(out, "event "+pdid.Id(act.Id).String()+" took it out")
	x.Contains(out, "by alice, an operator")

	// And every row is as the trail says: the event's, made again of it.
	out, err = d.loft(t, "trail", "rows")
	x.NoError(err, out)
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
	d := digested(t)

	const words = "박지민 씨 개인 사정으로 지급 (010-2345-6789)"
	owner := owning(t, d)
	var lines []*app.CustodyLineSpec
	for _, name := range []string{"노트북", "모니터"} {
		a, err := d.s.Walled.Asset().Add(owner, app.AssetAddRequest_builder{Name: name}.Build())
		x.NoError(err)
		lines = append(lines, app.CustodyLineSpec_builder{Asset: app.AssetById(a.GetId())}.Build())
	}
	p, err := d.s.Walled.Party().Add(owner, app.PartyAddRequest_builder{Name: "김철수", Kind: "person"}.Build())
	x.NoError(err)
	cu, err := d.s.Walled.Custody().Add(owner, app.CustodyAddRequest_builder{Party: app.PartyRef_builder{Id: p.GetId()}.Build(), Desc: words, Lines: lines}.Build())
	x.NoError(err)
	handed, err := pdid.From(cu.GetId())
	x.NoError(err)
	own, err := d.s.Ent.Event.Query().Where(event.Kind("custody.issue"), event.SubjectId(handed.Uuid())).Only(ctx)
	x.NoError(err)
	said := []pdid.Id{pdid.Id(own.Id)}
	parts, err := d.s.Ent.Event.Query().Where(event.CauseId(own.Id)).All(ctx)
	x.NoError(err)
	for _, v := range parts {
		said = append(said, pdid.Id(v.Id))
	}
	x.Len(said, 3)
	left := func() int {
		n := 0
		for _, id := range said {
			e, err := d.s.Ent.Event.Get(ctx, id.Uuid())
			x.NoError(err)
			if e.Reason != "" {
				n++
			}
		}
		return n
	}
	memo := func() string {
		cu, err := d.s.Ent.Custody.Get(ctx, handed.Uuid())
		x.NoError(err)
		return cu.Desc
	}

	out, err := d.loft(t, "event", "redact", "--tenant", "acme", "--event", said[0].String(), "--why", "삭제 요청 2026-26")
	x.NoError(err, out)
	x.Contains(out, "says the same in 2 other events")
	x.Contains(out, "custody.issue ×2")
	x.Contains(out, "1 row(s) keep the same as their own words")
	x.Contains(out, "Custody ×1")
	x.Contains(out, "would take its reason out")
	x.NotContains(out, "박지민")
	x.Equal(3, left(), "nothing was done")
	x.Equal(words, memo())

	out, err = d.loft(t, "event", "redact", "--tenant", "acme", "--event", said[0].String(), "--why", "삭제 요청 2026-26", "--yes")
	x.NoError(err, out)
	x.Contains(out, "took the reason out of event "+said[0].String())
	x.Contains(out, "and out of the 2 other events of its operation")
	x.Contains(out, "and out of the 1 row(s) that kept the same as their own words")
	x.Zero(left())
	x.Empty(memo(), "taken out of the hand-over's memo")
	rs, err := d.s.Ent.Access.Query().Where(entaccess.Action(app.EventService_Redact_FullMethodName), entaccess.KindEQ("write")).All(ctx)
	x.NoError(err)
	var wrote []pdid.Id
	for _, r := range rs {
		for _, v := range r.Subjects {
			wrote = append(wrote, pdid.Id(v))
		}
	}
	x.Contains(wrote, handed, "the hand-over, as the operator's write")

	// Asked of another of them, there is nothing left, and the part of the
	// act that took it out is said.
	out, err = d.loft(t, "event", "redact", "--tenant", "acme", "--event", said[2].String(), "--why", "삭제 요청 2026-26", "--yes")
	x.NoError(err, out)
	x.Contains(out, "it has no reason")
	x.Contains(out, "took it out")
	out, err = d.loft(t, "trail", "rows")
	x.NoError(err, out)
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
	d := digested(t)

	const words = "박지민 씨 개인 사정으로 반려 (010-2345-6789)"
	owner := owning(t, d)
	hall, err := d.s.Walled.Asset().Add(owner, app.AssetAddRequest_builder{Name: "강당", Kind: "space"}.Build())
	x.NoError(err)
	_, err = d.s.Walled.Bookable().Add(owner, app.BookableAddRequest_builder{Asset: app.AssetById(hall.GetId()), Approval: true}.Build())
	x.NoError(err)
	p, err := d.s.Walled.Party().Add(owner, app.PartyAddRequest_builder{Name: "박철수", Kind: "person", Email: "park@acme.example"}.Build())
	x.NoError(err)
	_, err = d.s.Walled.Party().Invite(owner, app.PartyInviteRequest_builder{Ref: app.PartyRef_builder{Id: p.GetId()}.Build(), Alias: "park", Role: "member", Password: "park-password-1234"}.Build())
	x.NoError(err)
	at := time.Now().Add(24 * time.Hour).Truncate(time.Hour)
	r, err := d.s.Walled.Reservation().Add(owner, app.ReservationAddRequest_builder{
		Name:      "인터뷰",
		Party:     app.PartyRef_builder{Id: p.GetId()}.Build(),
		DateStart: timestamppb.New(at),
		DateEnd:   timestamppb.New(at.Add(time.Hour)),
		Items:     []*app.ReservationItemSpec{app.ReservationItemSpec_builder{Resource: app.AssetById(hall.GetId())}.Build()},
	}.Build())
	x.NoError(err)
	x.Equal("requested", r.GetStatus())

	// Rejected, and mailed to whoever it was for.
	box := &posted{}
	d.s.Deps.Mail = box
	d.s.Deps.Links.App = "https://loft.example"
	_, err = d.s.Walled.Reservation().Reject(owner, app.ReservationDecideRequest_builder{Ref: app.ReservationRef_builder{Id: r.GetId()}.Build(), Reason: words}.Build())
	x.NoError(err)
	x.NoError(domain.Mailer{Drv: d.s.Drv, Deps: d.s.Deps}.Pass(ctx))
	x.Len(box.sent, 1)
	x.Contains(box.sent[0].Body, words)
	rid, err := pdid.From(r.GetId())
	x.NoError(err)
	v, err := d.s.Ent.Event.Query().Where(event.Kind("reservation.reject"), event.SubjectId(rid.Uuid())).Only(ctx)
	x.NoError(err)
	rejected := pdid.Id(v.Id)
	n, err := d.s.Ent.Notification.Query().Where(notification.Kind("reservation.decided"), notification.SubjectId(rid.Uuid())).Only(ctx)
	x.NoError(err)
	x.NotNil(n.DateMailed)
	told := pdid.Id(n.Id)
	says := func() string {
		n, err := d.s.Ent.Notification.Get(ctx, told.Uuid())
		x.NoError(err)
		return n.Desc
	}

	out, err := d.loft(t, "event", "redact", "--tenant", "acme", "--event", rejected.String(), "--why", "삭제 요청 2026-32")
	x.NoError(err, out)
	x.Contains(out, "1 notice(s) say the same as their words")
	x.Contains(out, "reservation.decided ×1")
	x.Contains(out, "1 of them went by mail before this, which is not taken back")
	x.Contains(out, "would take its reason out")
	x.NotContains(out, "박지민")
	x.Equal(words, says(), "nothing was done")

	out, err = d.loft(t, "event", "redact", "--tenant", "acme", "--event", rejected.String(), "--why", "삭제 요청 2026-32", "--yes")
	x.NoError(err, out)
	x.Contains(out, "took the reason out of event "+rejected.String())
	x.Contains(out, "and out of the 1 notice(s) that said the same as their words")
	x.Empty(says())

	rs, err := d.s.Ent.Access.Query().Where(entaccess.Action(app.EventService_Redact_FullMethodName), entaccess.KindEQ("write")).All(ctx)
	x.NoError(err)
	var wrote []pdid.Id
	for _, r := range rs {
		for _, v := range r.Subjects {
			wrote = append(wrote, pdid.Id(v))
		}
	}
	x.Contains(wrote, told, "the notice, as the operator's write")
	all, err := d.s.Ent.Access.Query().All(ctx)
	x.NoError(err)
	for _, r := range all {
		x.NotContains(string(r.Value), "박지민", r.Action)
	}
	out, err = d.loft(t, "trail", "rows")
	x.NoError(err, out)
}

// TestAnOperatorReadsTheAccessRecordAndItsReviews: `access list` says each
// call by identifier, an operator's by name too, narrowed as it is asked; and
// `access-review list` what every workspace's administrators recorded, or one
// workspace's.
func TestAnOperatorReadsTheAccessRecordAndItsReviews(t *testing.T) {
	x := require.New(t)
	d := deploy(t)

	_, err := d.loft(t, "tenant", "show", "acme")
	x.NoError(err)
	out, err := d.loft(t, "access", "list", "--tenant", "acme")
	x.NoError(err, out)
	x.Contains(out, "/loft.OperatorTenantService/Show")
	x.Contains(out, "actor="+operator.Id("alice").String()+" (alice, an operator)")
	x.Contains(out, "from=admin:")
	out, err = d.loft(t, "access", "list", "--kind", "download")
	x.NoError(err, out)
	x.Contains(out, "nothing in that stretch")
	out, err = d.loft(t, "access", "list", "--from", time.Now().Add(time.Hour).UTC().Format(time.RFC3339))
	x.Error(err, "a stretch that ends before it begins: %s", out)
	_, err = d.loft(t, "access", "list", "--kind", "glance")
	x.ErrorContains(err, "kind")
	_, err = d.loft(t, "access", "list", "--actor", "x")
	x.ErrorContains(err, "--actor")

	out, err = d.loft(t, "access-review", "list")
	x.NoError(err, out)
	x.Contains(out, "no review recorded")
	now := time.Now()
	_, err = d.s.Walled.AccessReview().Add(owning(t, d), app.AccessReviewAddRequest_builder{
		DateStart: timestamppb.New(now.Add(-24 * time.Hour)),
		DateEnd:   timestamppb.New(now),
		Desc:      "이상 없음",
	}.Build())
	x.NoError(err)
	out, err = d.loft(t, "access-review", "list", "--tenant", "acme")
	x.NoError(err, out)
	x.Contains(out, "  acme  ")
	x.Contains(out, `"이상 없음"`)
	cli.Elsewhere(t, d.c, "beta", "lee", false)
	out, err = d.loft(t, "access-review", "list", "--tenant", "beta")
	x.NoError(err, out)
	x.Contains(out, "no review recorded")
}
