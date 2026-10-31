package domain

import (
	"context"
	"testing"
	"time"
	"uuid"

	"github.com/lesomnus/payday/past"
	"github.com/lesomnus/payday/pdid"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/anypb"
	"google.golang.org/protobuf/types/known/timestamppb"

	app "github.com/lesomnus/loft"
	"github.com/lesomnus/loft/server/pd"
)

// TestADetailSaysEachKindOfValue: what an event held, as its timeline entry's
// detail says it, of every kind of field a message holds -- a moment on the
// workspace's wall, a number in decimal, a bool that holds, a list joined, a
// map's entries in the order of their keys -- and a field the kind left empty
// not at all. A row the namer reads is called what it reads.
func TestADetailSaysEachKindOfValue(t *testing.T) {
	x := require.New(t)
	ctx := context.Background()
	seoul, err := time.LoadLocation("Asia/Seoul")
	x.NoError(err)

	tenant := pdid.New(pd.TenantDomain)
	laptop, kit := pdid.New(pd.AssetDomain), pdid.New(pd.AssetDomain)
	n := &namer{
		t: &Tx{ctx: ctx, tenant: tenant, own: &workspaceSettings{zone: seoul}},
		assets: map[uuid.UUID]called{
			laptop.Uuid(): {found: true, name: "NB-1 노트북"},
			kit.Uuid():    {found: true, name: "K-1 회의 키트"},
		},
	}
	d := &details{n: n}
	row := func(kind string, v proto.Message) past.EventRow {
		value, err := anypb.New(v)
		x.NoError(err)
		return past.EventRow{Id: pdid.New(pd.EventDomain), Tenant: tenant, Kind: kind, Value: value}
	}

	got, err := d.of(row("workspace.import", app.WorkspaceImported_builder{
		ExportedAt: timestamppb.New(time.Date(2026, 10, 20, 23, 0, 0, 0, time.UTC)),
		Rows:       120,
		Released:   []string{"acme", "acme-old"},
		Reopened:   true,
		Left:       map[string]uint32{"party": 1, "asset": 3},
	}.Build()))
	x.NoError(err)
	x.Equal(map[string]string{
		"exported_at": "2026-10-21T08:00:00+09:00",
		"rows":        "120",
		"released":    "acme, acme-old",
		"reopened":    "true",
		"left":        "asset=3, party=1",
	}, got)

	got, err = d.of(row("asset.relate", app.AssetRelated_builder{
		Asset:     laptop.Bytes(),
		Tag:       "NB-1",
		Target:    kit.Bytes(),
		TargetTag: "K-1",
		Relation:  "member_of",
		Required:  true,
	}.Build()))
	x.NoError(err)
	x.Equal(map[string]string{
		"asset":      "NB-1 노트북",
		"tag":        "NB-1",
		"target":     "K-1 회의 키트",
		"target_tag": "K-1",
		"relation":   "member_of",
		"required":   "true",
	}, got, "an end that did not happen is not said")

	// A row from before holds no message, and is the payload's to say.
	got, err = d.of(past.EventRow{Id: pdid.New(pd.EventDomain), Tenant: tenant, Kind: "asset.relate"})
	x.NoError(err)
	x.Empty(got)
}
