package domain_test

import (
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"google.golang.org/protobuf/types/known/timestamppb"
	"pgregory.net/rapid"

	app "github.com/lesomnus/rove"
)

// The time oracle (plan.md M1): random histories of moves and removals,
// recorded late and then corrected, replayed on a model kept here -- and QueryAt
// and the Timeline asked about every day of every one of them, by what is known
// now and by what was known after each step (AsOf).
//
// The model is the rule written plainly (design 3.3): a placement holds from when
// it is said to until the next change already known; a removal ends what holds
// there; a retraction takes a row out as if it had never been recorded, and the
// row before it, if it ended there, goes on over the gap; a correction to when
// something happened is that retraction and the placement said again. What the
// oracle checks is everything between that rule and an answer: the rows written
// and superseded, the moments they are stamped with, the tree read at a moment,
// and what could be seen at a moment of knowing.

// oracleDays is how many days of past a history plays over. Every moment
// written is a whole day, so a day's answer is the answer all day.
const oracleDays = 6

// seg is one placement as the model holds it: from day `from` until day `to`
// (or for good, when `to` is negative), in space `in`.
type seg struct{ from, to, in int }

func (s seg) holds(d int) bool { return d >= s.from && (s.to < 0 || d < s.to) }

// known is one item's placements as known at one moment, in day order.
type known []seg

func (k known) at(d int) (int, bool) {
	for _, s := range k {
		if s.holds(d) {
			return s.in, true
		}
	}
	return 0, false
}

// next is the first change after `d` already known: a placement starting, or
// one ending; negative is none.
func (k known) next(d int) int {
	n := -1
	for _, s := range k {
		for _, v := range []int{s.from, s.to} {
			if v > d && (n < 0 || v < n) {
				n = v
			}
		}
	}
	return n
}

// set is "in `in` from day `d`", or out of wherever it was when `in` is
// negative.
func (k known) set(d, in int) known {
	cur, had := k.at(d)
	if (in < 0 && !had) || (in >= 0 && had && cur == in) {
		return k
	}
	end := k.next(d)
	out := known{}
	for _, s := range k {
		switch {
		case s.to >= 0 && s.to <= d:
			out = append(out, s)
		case s.from == d:
		case s.from > d:
			out = append(out, s)
		default:
			out = append(out, seg{s.from, d, s.in})
		}
	}
	if in >= 0 {
		out = append(out, seg{d, end, in})
	}
	slices.SortFunc(out, func(a, b seg) int { return a.from - b.from })
	return out
}

// retract takes the placement that starts on `from` out.
func (k known) retract(from int) (known, seg) {
	i := slices.IndexFunc(k, func(s seg) bool { return s.from == from })
	gone := k[i]
	out := known{}
	for j, s := range k {
		switch {
		case j == i:
		case j == i-1 && s.to == gone.from:
			out = append(out, seg{s.from, gone.to, s.in})
		default:
			out = append(out, s)
		}
	}
	return out, gone
}

// oracle is one history being played: the assets it plays with, and the model
// as it stood after every step, with the moment the step was recorded.
type oracle struct {
	e      *env
	rt     *rapid.T
	day0   time.Time
	root   *app.Asset
	spaces []*app.Asset
	items  []*app.Asset

	now   []known
	steps []step
}

// step is what the model said once a write was recorded, and when that was.
type step struct {
	at   time.Time
	what string
	m    []known
}

func (o *oracle) day(d int) time.Time { return o.day0.Add(time.Duration(d) * day) }

func (o *oracle) dayOf(t time.Time) int { return int(t.Sub(o.day0) / day) }

// recorded closes a step: the write was recorded at the clock's moment, and the
// next one is recorded a minute later.
func (o *oracle) recorded(what string) {
	m := make([]known, len(o.now))
	for i, k := range o.now {
		m[i] = slices.Clone(k)
	}
	o.steps = append(o.steps, step{o.e.now, what, m})
	o.e.now = o.e.now.Add(time.Minute)
}

func (o *oracle) add(name, kind string, in *app.Asset, since time.Time) *app.Asset {
	req := app.AssetAddRequest_builder{Name: name, Kind: kind, Since: timestamppb.New(since)}.Build()
	if in != nil {
		req.SetTo(assetRef(in))
	}
	v, err := o.e.app().Asset().Add(o.e.owner, req)
	if err != nil {
		o.rt.Fatalf("add %s: %v", name, err)
	}
	return v
}

// rows is an item's placements as the Timeline answers them, known now or at
// `k`.
func (o *oracle) rows(item int, k *time.Time) []*app.TimelineEntry {
	req := app.AssetTimelineRequest_builder{Ref: assetRef(o.items[item])}.Build()
	if k != nil {
		req.SetKnown(timestamppb.New(*k))
	}
	v, err := o.e.app().Asset().Timeline(o.e.owner, req)
	if err != nil {
		o.rt.Fatalf("timeline: %v", err)
	}
	out := []*app.TimelineEntry{}
	for _, en := range v.GetEntries() {
		if en.GetKind() == "placement" {
			out = append(out, en)
		}
	}
	slices.SortFunc(out, func(a, b *app.TimelineEntry) int { return a.GetValidFrom().AsTime().Compare(b.GetValidFrom().AsTime()) })
	return out
}

func (o *oracle) spaceOf(id []byte) int {
	return slices.IndexFunc(o.spaces, func(s *app.Asset) bool { return sameId(s.GetId(), id) })
}

// where is every item's space on day `d`, by QueryAt over the root -- known
// now, or as known at `k`.
func (o *oracle) where(d int, k *time.Time) map[int]int {
	req := app.AssetQueryAtRequest_builder{Root: assetRef(o.root), At: timestamppb.New(o.day(d))}.Build()
	if k != nil {
		req.SetKnown(timestamppb.New(*k))
	}
	v, err := o.e.app().Asset().QueryAt(o.e.owner, req)
	if err != nil {
		o.rt.Fatalf("query at day %d: %v", d, err)
	}
	out := map[int]int{}
	for _, s := range v.GetItems() {
		i := slices.IndexFunc(o.items, func(a *app.Asset) bool { return sameId(a.GetId(), s.GetId()) })
		if i < 0 {
			continue
		}
		if !s.GetExisted() {
			o.rt.Fatalf("item %d answered on day %d as not existing", i, d)
		}
		out[i] = o.spaceOf(s.GetParentId())
	}
	return out
}

func (o *oracle) expected(m []known, d int) map[int]int {
	out := map[int]int{}
	for i, k := range m {
		if in, ok := k.at(d); ok {
			out[i] = in
		}
	}
	return out
}

func (o *oracle) history() string {
	b := strings.Builder{}
	for i, s := range o.steps {
		fmt.Fprintf(&b, "\n  %d: %s", i, s.what)
	}
	return b.String()
}

// check asks about every day, by what was known after every step, and by what
// is known now.
func (o *oracle) check() {
	last := o.steps[len(o.steps)-1]
	for d := range oracleDays {
		if got, want := o.where(d, nil), o.expected(last.m, d); !mapsEqual(got, want) {
			o.rt.Fatalf("day %d, known now: QueryAt says %v, the rule says %v%s", d, got, want, o.history())
		}
	}
	for i, s := range o.steps {
		k := s.at
		for d := range oracleDays {
			if got, want := o.where(d, &k), o.expected(s.m, d); !mapsEqual(got, want) {
				o.rt.Fatalf("day %d, as known after step %d (%s): QueryAt says %v, the rule says %v%s", d, i, s.what, got, want, o.history())
			}
		}
		// And a moment before the next step was recorded is still this one.
		between, d := k.Add(30*time.Second), i%oracleDays
		if got, want := o.where(d, &between), o.expected(s.m, d); !mapsEqual(got, want) {
			o.rt.Fatalf("day %d, between step %d and the next: QueryAt says %v, the rule says %v%s", d, i, got, want, o.history())
		}
		for item := range o.items {
			got := []seg{}
			for _, en := range o.rows(item, &k) {
				to := -1
				if en.HasValidTo() {
					to = o.dayOf(en.GetValidTo().AsTime())
				}
				got = append(got, seg{o.dayOf(en.GetValidFrom().AsTime()), to, o.spaceOf(en.GetOtherId())})
			}
			if want := []seg(s.m[item]); !slices.Equal(got, want) {
				o.rt.Fatalf("item %d's rows as known after step %d (%s): the Timeline says %v, the rule says %v%s", item, i, s.what, got, want, o.history())
			}
		}
	}
}

func mapsEqual(a, b map[int]int) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if w, ok := b[k]; !ok || w != v {
			return false
		}
	}
	return true
}

// TestWhatWasTrueThenIsWhatTheRuleSays.
//
// Random histories: things moved between three rooms and taken out of them,
// recorded days late, then corrected to another day or found never to have
// happened. Every answer about every day -- by what is known now, and by what was
// known after every step and between steps -- is the one the rule gives, and so
// is every row the Timeline shows as known at each step.
func TestWhatWasTrueThenIsWhatTheRuleSays(t *testing.T) {
	e := newEnv(t)
	n := 0
	rapid.Check(t, func(rt *rapid.T) {
		n++
		o := &oracle{e: e, rt: rt}
		o.day0 = e.now.Add(-oracleDays * day)

		// A building of three rooms, and three things in the first from day 0,
		// all recorded at one moment.
		o.root = o.add(fmt.Sprintf("건물 %d", n), "space", nil, o.day(-1))
		for s := range 3 {
			o.spaces = append(o.spaces, o.add(fmt.Sprintf("방 %d-%d", n, s), "space", o.root, o.day(-1)))
		}
		for i := range 3 {
			o.items = append(o.items, o.add(fmt.Sprintf("물건 %d-%d", n, i), "item", o.spaces[0], o.day(0)))
			o.now = append(o.now, known{{0, -1, 0}})
		}
		o.recorded("three things in room 0 from day 0")

		ops := rapid.IntRange(1, 10).Draw(rt, "ops")
		for range ops {
			item := rapid.IntRange(0, len(o.items)-1).Draw(rt, "item")
			d := rapid.IntRange(0, oracleDays-1).Draw(rt, "day")
			ref := assetRef(o.items[item])
			switch rapid.SampledFrom([]string{"move", "move", "remove", "reschedule", "retract"}).Draw(rt, "op") {
			case "move":
				in := rapid.IntRange(0, len(o.spaces)-1).Draw(rt, "to")
				_, err := e.app().Asset().Move(e.owner, app.AssetMoveRequest_builder{Ref: ref, To: assetRef(o.spaces[in]), At: timestamppb.New(o.day(d)), Reason: "oracle"}.Build())
				if err != nil {
					rt.Fatalf("move item %d to room %d on day %d: %v%s", item, in, d, err, o.history())
				}
				o.now[item] = o.now[item].set(d, in)
				o.recorded(fmt.Sprintf("item %d to room %d from day %d", item, in, d))

			case "remove":
				_, err := e.app().Asset().Move(e.owner, app.AssetMoveRequest_builder{Ref: ref, At: timestamppb.New(o.day(d)), Reason: "oracle"}.Build())
				if err != nil {
					rt.Fatalf("take item %d out on day %d: %v%s", item, d, err, o.history())
				}
				o.now[item] = o.now[item].set(d, -1)
				o.recorded(fmt.Sprintf("item %d out from day %d", item, d))

			case "reschedule", "retract":
				rows := o.rows(item, nil)
				if len(rows) == 0 {
					continue
				}
				r := rows[rapid.IntRange(0, len(rows)-1).Draw(rt, "row")]
				from := o.dayOf(r.GetValidFrom().AsTime())
				req := app.AssetCorrectRequest_builder{Ref: ref, RowId: r.GetRowId(), Reason: "oracle"}.Build()
				what := fmt.Sprintf("item %d's placement from day %d never happened", item, from)
				if rapid.Bool().Draw(rt, "retract") {
					req.SetRetract(true)
					o.now[item], _ = o.now[item].retract(from)
				} else {
					req.SetValidFrom(timestamppb.New(o.day(d)))
					k, gone := o.now[item].retract(from)
					o.now[item] = k.set(d, gone.in)
					what = fmt.Sprintf("item %d's placement from day %d was from day %d", item, from, d)
				}
				if _, err := e.app().Asset().Correct(e.owner, req); err != nil {
					rt.Fatalf("%s: %v%s", what, err, o.history())
				}
				o.recorded(what)
			}
		}

		o.check()
	})
}

// TestAThingIsSomewhereAtEveryMomentOfKnowing.
//
// An operation is recorded at one moment: the rows it supersedes stop being
// known then, and the rows it writes start being known then. Were those two
// moments read off the clock apart -- the start of the operation for one, the
// write of each row for the other -- there would be an instant, as long as the
// operation took, at which neither was known and the thing was nowhere.
func TestAThingIsSomewhereAtEveryMomentOfKnowing(t *testing.T) {
	e := newEnv(t)
	x := e.x
	hq := e.space("본사", nil)
	a := e.space("A", hq)
	b := e.space("B", hq)
	laptop := e.item("노트북", "NB-1", a, day)

	// A clock that moves on every time it is read, as a real one does between
	// the start of an operation and the rows it writes.
	e.now = e.now.Add(time.Minute)
	start := e.now
	ticks := e.now
	e.s.Deps.Now = func() time.Time {
		ticks = ticks.Add(time.Millisecond)
		return ticks
	}
	_, err := e.app().Asset().Move(e.owner, app.AssetMoveRequest_builder{Ref: assetRef(laptop), To: assetRef(b)}.Build())
	x.NoError(err)
	end := ticks
	e.s.Deps.Now = func() time.Time { return e.now }
	x.True(end.After(start.Add(2*time.Millisecond)), "the clock was read more than once")

	after := end.Add(time.Hour)
	was := ""
	for k := start; !k.After(end); k = k.Add(time.Millisecond) {
		in := e.where(hq, laptop, after, &k)
		x.NotNil(in, "known at %s, the laptop was nowhere", k.Sub(start))
		now := map[bool]string{true: "A", false: "B"}[sameId(in, a.GetId())]
		x.True(was == "" || was == now || (was == "A" && now == "B"), "known at %s it went back", k.Sub(start))
		was = now
	}
	x.Equal("B", was)
}
