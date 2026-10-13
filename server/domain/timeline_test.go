package domain

import (
	"testing"
	"time"
	"uuid"

	"github.com/stretchr/testify/require"
)

func day(d int) time.Time { return time.Date(2026, 9, d, 0, 0, 0, 0, time.UTC) }

func ptr[T any](v T) *T { return &v }

// rows makes a stored timeline: each span gets an id, as rows read back do.
func rows(ss ...span[string]) timeline[string] {
	for i := range ss {
		ss[i].id = uuid.New()
	}
	return ss
}

// shape is a timeline without ids, to compare against.
func shape(tl timeline[string]) []string {
	out := []string{}
	for _, s := range tl {
		to := "inf"
		if s.to != nil {
			to = s.to.Format("02")
		}
		out = append(out, s.state+"["+s.from.Format("02")+","+to+")")
	}
	return out
}

func TestTimelineMoveNow(t *testing.T) {
	x := require.New(t)
	old := rows(span[string]{from: day(1), state: "A"})

	tl, changed := old.set(day(15), ptr("B"))
	x.True(changed)
	x.Equal([]string{"A[01,15)", "B[15,inf)"}, shape(tl))

	gone, added := tl.diff(old)
	x.Equal([]uuid.UUID{old[0].id}, gone, "the open row is superseded, not edited")
	x.Len(added, 2)
}

func TestTimelineBackdatedMoveLastsUntilTheNextKnownChange(t *testing.T) {
	x := require.New(t)
	old := rows(
		span[string]{from: day(1), to: ptr(day(15)), state: "A"},
		span[string]{from: day(15), state: "C"},
	)

	// Recorded later: it was in B from the 10th.
	tl, _ := old.set(day(10), ptr("B"))
	x.Equal([]string{"A[01,10)", "B[10,15)", "C[15,inf)"}, shape(tl))

	gone, added := tl.diff(old)
	x.Equal([]uuid.UUID{old[0].id}, gone, "the row after the edit is left alone")
	x.Len(added, 2)
}

func TestTimelineRemoveLeavesAGap(t *testing.T) {
	x := require.New(t)
	old := rows(span[string]{from: day(1), state: "A"})

	tl, _ := old.set(day(20), nil)
	x.Equal([]string{"A[01,20)"}, shape(tl))

	// And a move into the gap lasts until the gap's end, which is never.
	tl, _ = tl.set(day(25), ptr("B"))
	x.Equal([]string{"A[01,20)", "B[25,inf)"}, shape(tl))
}

func TestTimelineSetTheSameIsNothing(t *testing.T) {
	x := require.New(t)
	old := rows(span[string]{from: day(1), state: "A"})

	tl, changed := old.set(day(5), ptr("A"))
	x.False(changed)
	gone, added := tl.diff(old)
	x.Empty(gone)
	x.Empty(added)
}

func TestTimelineReplacesARowBeginningAtTheSameMoment(t *testing.T) {
	x := require.New(t)
	old := rows(
		span[string]{from: day(1), to: ptr(day(10)), state: "A"},
		span[string]{from: day(10), state: "B"},
	)

	tl, _ := old.set(day(10), ptr("C"))
	x.Equal([]string{"A[01,10)", "C[10,inf)"}, shape(tl))
}

func TestTimelineRetractHealsTheGap(t *testing.T) {
	x := require.New(t)
	old := rows(
		span[string]{from: day(1), to: ptr(day(10)), state: "A"},
		span[string]{from: day(10), to: ptr(day(20)), state: "B"},
		span[string]{from: day(20), state: "C"},
	)

	tl, gone, ok := old.retract(old[1].id)
	x.True(ok)
	x.Equal("B", gone.state)
	x.Equal([]string{"A[01,20)", "C[20,inf)"}, shape(tl), "the move to B never happened, so it stayed in A")
}

func TestTimelineRescheduleEarlierAndLater(t *testing.T) {
	x := require.New(t)
	old := rows(
		span[string]{from: day(1), to: ptr(day(15)), state: "A"},
		span[string]{from: day(15), state: "B"},
	)

	// The move to B was on the 12th, not the 15th.
	tl, gone, _ := old.retract(old[1].id)
	tl, _ = tl.set(day(12), &gone.state)
	x.Equal([]string{"A[01,12)", "B[12,inf)"}, shape(tl))

	// Or on the 18th.
	tl, gone, _ = old.retract(old[1].id)
	tl, _ = tl.set(day(18), &gone.state)
	x.Equal([]string{"A[01,18)", "B[18,inf)"}, shape(tl))
}
