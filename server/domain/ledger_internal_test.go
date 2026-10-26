package domain

import (
	"testing"
	"time"
	"uuid"

	"github.com/stretchr/testify/require"

	"github.com/lesomnus/rove/internal/ent"
)

// TestPoolsGiveWhatTheyCanAtOnce: what pools that share units can give is
// what they can give together, each unit to one of them -- found by moving a
// shared one to whichever has no other then, and never twice.
func TestPoolsGiveWhatTheyCanAtOnce(t *testing.T) {
	a, m, b, c := uuid.NewV4(), uuid.NewV4(), uuid.NewV4(), uuid.NewV4()
	one, two, three, set := uuid.NewV4(), uuid.NewV4(), uuid.NewV4(), uuid.NewV4()
	l := &ledger{
		units: map[uuid.UUID][]uuid.UUID{one: {m, a}, two: {m, b}, three: {b, c}},
		set:   map[uuid.UUID]uint32{set: 3},
	}
	none := map[uuid.UUID]bool{}
	for _, v := range []struct {
		why   string
		asked map[uuid.UUID]int
		busy  map[uuid.UUID]bool
		want  bool
	}{
		{"nothing asked", nil, none, true},
		{"each its own", map[uuid.UUID]int{one: 2}, none, true},
		{"the shared one to the one that needs it", map[uuid.UUID]int{one: 2, two: 1}, none, true},
		{"a chain of them", map[uuid.UUID]int{one: 2, two: 1, three: 1}, none, true},
		{"more than there are", map[uuid.UUID]int{one: 2, two: 2}, none, false},
		{"one busy", map[uuid.UUID]int{one: 1, two: 1}, map[uuid.UUID]bool{b: true, a: true}, false},
		{"more than one has", map[uuid.UUID]int{one: 3}, none, false},
		{"a number set", map[uuid.UUID]int{set: 3, one: 2}, none, true},
		{"more than was set", map[uuid.UUID]int{set: 4}, none, false},
	} {
		busy := v.busy
		require.Equal(t, v.want, l.gives(v.asked, busy), v.why)
	}

	// What is to spare of one, the others given what they ask first.
	require.Equal(t, 2, l.room(one, map[uuid.UUID]int{two: 1, three: 1}, none), "B and C go to the others")
	require.Equal(t, 1, l.room(one, map[uuid.UUID]int{two: 1, three: 1}, map[uuid.UUID]bool{c: true}), "B to the third, M to the second")
	require.Equal(t, 0, l.room(one, map[uuid.UUID]int{one: 1, two: 1}, map[uuid.UUID]bool{b: true}), "A and M are given")
	require.Equal(t, 2, l.room(one, nil, none))
	require.Equal(t, 1, l.room(set, map[uuid.UUID]int{set: 2}, none))
}

// TestWhatPoolsGiveIsReadAtEachMoment: a stretch is short where any moment of
// it is, what ends at a moment leaving room for what begins then.
func TestWhatPoolsGiveIsReadAtEachMoment(t *testing.T) {
	m, b := uuid.NewV4(), uuid.NewV4()
	one, two := uuid.NewV4(), uuid.NewV4()
	l := &ledger{units: map[uuid.UUID][]uuid.UUID{one: {m}, two: {m, b}}, set: map[uuid.UUID]uint32{}}
	at := time.Date(2026, 10, 27, 9, 0, 0, 0, time.UTC)
	span := func(of uuid.UUID, from, to int, units uint32) *ent.Allocation {
		return &ent.Allocation{ResourceId: of, BeginsAt: at.Add(time.Duration(from) * time.Hour), EndsAt: at.Add(time.Duration(to) * time.Hour), Units: units}
	}

	own := []*ent.Allocation{span(one, 0, 3, 1), span(two, 2, 4, 1)}
	require.False(t, l.short(own, nil, at, at.Add(5*time.Hour)), "the second has B")
	held := []*ent.Allocation{span(b, 3, 5, 1)}
	require.False(t, l.short(own, held, at, at.Add(5*time.Hour)), "and M from three on, when the first is done")
	held = []*ent.Allocation{span(b, 2, 3, 1)}
	require.True(t, l.short(own, held, at, at.Add(5*time.Hour)), "from two to three, both need M")
	require.False(t, l.short(own, held, at.Add(3*time.Hour), at.Add(5*time.Hour)), "but not after")

	require.EqualValues(t, 0, l.spare(two, own[:1], held, at, at.Add(3*time.Hour)), "B is busy for an hour of it")
	require.EqualValues(t, 1, l.spare(two, own[:1], nil, at, at.Add(3*time.Hour)))
	require.EqualValues(t, 0, l.spare(two, own, nil, at, at), "nothing of no time")
}
