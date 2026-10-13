package domain

import (
	"slices"
	"sort"
	"time"
	"uuid"
)

// span is one time row as the editor sees it: an interval of valid time and
// what was true in it. `id` is the row it came from, and zero for one the
// editor made.
type span[S comparable] struct {
	id    uuid.UUID
	from  time.Time
	to    *time.Time
	state S
}

func (s span[S]) contains(at time.Time) bool {
	return !at.Before(s.from) && (s.to == nil || at.Before(*s.to))
}

// timeline is one subject's current-knowledge rows in valid-time order, never
// overlapping: where a child was, who held an asset in a role, whether two
// assets were linked.
//
// Editing it never touches a row. An edit answers a new timeline, and
// [timeline.diff] says which rows that supersedes and which it adds -- which
// is the whole of the rule that a time row is written once (design 3.3).
type timeline[S comparable] []span[S]

func (tl timeline[S]) sorted() timeline[S] {
	out := slices.Clone(tl)
	sort.SliceStable(out, func(i, j int) bool { return out[i].from.Before(out[j].from) })
	return out
}

// at answers the state at a moment, and whether there was one.
func (tl timeline[S]) at(t time.Time) (S, bool) {
	for _, s := range tl {
		if s.contains(t) {
			return s.state, true
		}
	}
	var zero S
	return zero, false
}

// next answers the first change strictly after `at`: a row beginning, or a row
// ending that nothing begins at. Nil is "never".
func (tl timeline[S]) next(at time.Time) *time.Time {
	var end *time.Time
	consider := func(v time.Time) {
		if v.After(at) && (end == nil || v.Before(*end)) {
			e := v
			end = &e
		}
	}
	for _, s := range tl {
		consider(s.from)
		if s.to != nil {
			consider(*s.to)
		}
	}
	return end
}

// set makes the state `v` from `at` until the next change already known, or
// clears it there when `v` is nil.
//
// "Until the next change" is what makes a backdated edit safe: recording on
// Friday that something moved on Monday changes Monday to whatever was known
// to happen next, and nothing after.
func (tl timeline[S]) set(at time.Time, v *S) (timeline[S], bool) {
	cur, had := tl.at(at)
	if v == nil && !had {
		return tl, false
	}
	if v != nil && had && cur == *v {
		return tl, false
	}

	end := tl.next(at)

	out := timeline[S]{}
	for _, s := range tl {
		switch {
		case s.to != nil && !s.to.After(at):
			// Over before the edit begins.
			out = append(out, s)
		case !s.from.Before(at):
			if s.from.Equal(at) {
				// Begins exactly here, so the edit replaces it.
				continue
			}
			out = append(out, s)
		default:
			// Contains the moment: it ends there now.
			t := at
			out = append(out, span[S]{from: s.from, to: &t, state: s.state})
		}
	}
	if v != nil {
		out = append(out, span[S]{from: at, to: end, state: *v})
	}

	return out.sorted(), true
}

// retract removes a row as if it had never been recorded. The row before it,
// if it ended where this one began, goes on over the gap: a move that never
// happened leaves the thing where it was.
func (tl timeline[S]) retract(id uuid.UUID) (timeline[S], span[S], bool) {
	i := slices.IndexFunc(tl, func(s span[S]) bool { return s.id == id })
	if i < 0 {
		return tl, span[S]{}, false
	}

	gone := tl[i]
	out := timeline[S]{}
	for j, s := range tl {
		if j == i {
			continue
		}
		if s.to != nil && s.to.Equal(gone.from) && j == i-1 {
			out = append(out, span[S]{from: s.from, to: gone.to, state: s.state})
			continue
		}
		out = append(out, s)
	}

	return out.sorted(), gone, true
}

// diff answers what replacing `old` with `tl` writes: the rows superseded, and
// the rows added. A row both have, by id, is left alone.
func (tl timeline[S]) diff(old timeline[S]) (gone []uuid.UUID, added []span[S]) {
	kept := map[uuid.UUID]bool{}
	for _, s := range tl {
		if s.id != (uuid.UUID{}) {
			kept[s.id] = true
			continue
		}
		added = append(added, s)
	}
	for _, s := range old {
		if !kept[s.id] {
			gone = append(gone, s.id)
		}
	}
	return gone, added
}

func sortedKeys[V any](m map[string]V) []string {
	ks := make([]string, 0, len(m))
	for k := range m {
		ks = append(ks, k)
	}
	sort.Strings(ks)
	return ks
}
