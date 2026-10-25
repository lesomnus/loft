package cli_test

import (
	"bytes"
	"encoding/base64"
	"slices"
	"testing"

	"github.com/lesomnus/payday/frame"
	"github.com/lesomnus/payday/pdid"
	"github.com/lesomnus/payday/pdpb"
	"github.com/lesomnus/payday/pdtest"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	app "github.com/lesomnus/rove"
	"github.com/lesomnus/rove/cli"
	"github.com/lesomnus/rove/cmd"
	entaccess "github.com/lesomnus/rove/internal/ent/access"
	"github.com/lesomnus/rove/internal/ent/event"
	"github.com/lesomnus/rove/internal/ent/holder"
	enttenant "github.com/lesomnus/rove/internal/ent/tenant"
	"github.com/lesomnus/rove/server/pd"
)

// TestAnOperatorTakesAnEventsReasonOut is `rove event redact`: what a
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
		rs, err := s.Ent.Access.Query().Where(entaccess.Action("rove event redact")).All(ctx)
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
