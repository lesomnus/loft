package domain_test

import (
	"testing"
	"time"

	"github.com/lesomnus/payday/pdid"
	"github.com/lesomnus/z"

	app "github.com/lesomnus/loft"
	"github.com/lesomnus/loft/server/domain"
	"github.com/lesomnus/loft/server/pd"
)

// TestAReasonTakenOutReadsBeforeAnythingWritesOne: a build that declares
// `event.redact` reads a row of it wherever an event is read -- the feed, the
// history of what it is about, the generated List -- in its own words, and by
// its message. Nothing in this build writes one: the act that takes an
// event's reason out ships a release after it, so that a deployment it has
// written in can go back to this build and read what it wrote (CLAUDE.md,
// "The domain layer").
func TestAReasonTakenOutReadsBeforeAnythingWritesOne(t *testing.T) {
	e := newEnv(t)
	x := e.x

	room := e.space("창고", nil)
	office := e.space("사무실", nil)
	nb := e.item("노트북", "NB-1", room, 24*time.Hour)
	_, err := e.app().Asset().Move(e.owner, app.AssetMoveRequest_builder{
		Ref:    assetRef(nb),
		To:     assetRef(office),
		Reason: "김철수 님 자리로",
	}.Build())
	x.NoError(err)
	moves, err := e.app().Event().Recent(e.owner, app.EventRecentRequest_builder{Size: 10, Kind: "asset.move"}.Build())
	x.NoError(err)
	x.Len(moves.GetItems(), 1)
	moved, err := pdid.From(moves.GetItems()[0].GetId())
	x.NoError(err)

	id, err := domain.RedactionAsTheActWill(e.owner, e.app(), moved, "삭제 요청 2026-17")
	x.NoError(err)

	// The feed.
	feed, err := e.app().Event().Recent(e.owner, app.EventRecentRequest_builder{Size: 10, Kind: "event."}.Build())
	x.NoError(err)
	x.Len(feed.GetItems(), 1)
	x.Equal(id.Bytes(), feed.GetItems()[0].GetId())
	x.Equal("event.redact", feed.GetItems()[0].GetKind())
	x.Equal("작업 기록의 사유 삭제", feed.GetItems()[0].GetDesc())

	// The history of what the event it names was about.
	found := false
	for _, v := range e.timeline(nb, false) {
		if v.GetKind() != "event" || v.GetEventKind() != "event.redact" {
			continue
		}
		found = true
		x.Equal("작업 기록의 사유 삭제", v.GetSummary())
		x.Equal("삭제 요청 2026-17", v.GetReason())
	}
	x.True(found, "the asset's history holds the event.redact")

	// The generated List, by its message.
	l, err := e.app().Event().List(e.owner, app.EventListRequest_builder{
		Filters: []*app.EventFilter{app.EventFilter_builder{Kind: z.Ptr("event.redact")}.Build()},
	}.Build())
	x.NoError(err)
	x.Len(l.GetItems(), 1)
	m := pd.EventKinds["event.redact"].New().Interface()
	x.NoError(l.GetItems()[0].GetValue().UnmarshalTo(m))
	v, ok := m.(*app.EventRedacted)
	x.True(ok)
	x.Equal(moved.Bytes(), v.GetEvent())
}
