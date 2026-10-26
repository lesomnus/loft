package domain_test

import (
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/lesomnus/payday/pdid"
	"github.com/lesomnus/z"
	"google.golang.org/protobuf/types/known/timestamppb"

	app "github.com/lesomnus/rove"
	"github.com/lesomnus/rove/internal/ent"
	"github.com/lesomnus/rove/server/domain"
)

// A loan the sweep finds past its due with some of it not back is an event
// (D55): the loan's, at the moment it was found so, saying the due it missed
// in the workspace's zone, and under it one of each asset still out -- once
// for each due the loan misses, and naming nobody. What finding it late does
// is part of it: a booking that waits for what it holds waits under it.

// sentence is what the history says of a loan found late that was due `due`,
// told in `zone`.
func sentence(due time.Time, zone *time.Location) string {
	return "반납 기한 지남: " + due.In(zone).Format("2006-01-02 15:04")
}

// TestALoanFoundLateIsAnEventOnce: found late, a loan is an event of its own
// and of each thing of it still out, at the moment it was found so -- in the
// loan's history, the feed, and each thing's -- and found again it is not;
// given a later due and late again, it is one more, of that due; and back,
// it is nothing more.
func TestALoanFoundLateIsAnEventOnce(t *testing.T) {
	e := newEnv(t)
	x := e.x
	// Told in New York, where the deployment tells Seoul.
	_, err := e.app().Tenant().Configure(e.owner, app.TenantConfigureRequest_builder{
		Ref:      app.TenantRef_builder{Id: e.tenant.Bytes()}.Build(),
		Timezone: z.Ptr("America/New_York"),
	}.Build())
	x.NoError(err)
	ny, err := time.LoadLocation("America/New_York")
	x.NoError(err)

	camera := e.item("카메라", "CM-1", nil, day)
	lens := e.item("렌즈", "LN-1", nil, day)
	_, lee := e.person("member")

	due := e.now.Add(time.Hour).Truncate(time.Minute)
	loan := e.lend(lee, due, camera, lens)

	// The lens is back before it is due, and the camera is not.
	lines, err := e.app().CustodyLine().List(e.owner, app.CustodyLineListRequest_builder{
		Filters: []*app.CustodyLineFilter{app.CustodyLineFilter_builder{Custody: ref[app.CustodyRef](loan.GetId())}.Build()},
	}.Build())
	x.NoError(err)
	i := slices.IndexFunc(lines.GetItems(), func(l *app.CustodyLine) bool { return sameId(l.GetAsset().GetId(), lens.GetId()) })
	x.GreaterOrEqual(i, 0)
	_, err = e.app().Custody().Return(e.owner, app.CustodyReturnRequest_builder{
		Ref:   ref[app.CustodyRef](loan.GetId()),
		Lines: []*app.CustodyReturnLine{app.CustodyReturnLine_builder{Line: ref[app.CustodyLineRef](lines.GetItems()[i].GetId())}.Build()},
	}.Build())
	x.NoError(err)

	e.sweep()
	x.Empty(e.eventsOf("custody.overdue"), "not due yet")

	e.now = e.now.Add(2 * time.Hour)
	found := e.now
	e.sweep()
	own, kids := e.split(e.eventsOf("custody.overdue"))
	x.Equal(idUUID(loan.GetId()), *own.SubjectId, "the loan's")
	x.Nil(own.ActorId, "the deployment's own work, nobody's")
	x.True(found.Equal(own.OccurredAt), "when it was found late: %s", own.OccurredAt)
	held := value[*app.CustodyOverdue](e, own)
	x.True(sameId(loan.GetId(), held.GetCustody()))
	x.True(due.Equal(held.GetDue().AsTime()), "the due it missed")
	x.Empty(held.GetAsset(), "the loan's own holds no thing")
	x.Equal(sentence(due, ny), e.say(own))

	x.Len(kids, 1, "of what is still out, and not of what is back")
	kid := kids[idUUID(camera.GetId())]
	x.NotNil(kid)
	x.True(found.Equal(kid.OccurredAt))
	x.Nil(kid.ActorId)
	x.True(sameId(camera.GetId(), value[*app.CustodyOverdue](e, kid).GetAsset()))

	// The camera's history says it, once, as the loan says it; the lens's
	// says nothing of it.
	es := e.happened(camera, "custody.overdue")
	x.Len(es, 1)
	x.Equal(sentence(due, ny), es[0].GetSummary())
	x.True(found.Equal(es[0].GetValidFrom().AsTime()))
	x.True(sameId(es[0].GetCauseId(), pdid.Id(own.Id).Bytes()), "under the loan's")
	x.Empty(es[0].GetCause(), "which says no more than it")
	x.Empty(e.happened(lens, "custody.overdue"))
	// Nor who has it, which the hand-over says.
	for _, v := range es[0].GetDetail() {
		x.NotContains(v, lee.GetName())
	}

	// Swept again, it is late still, and was found so once.
	e.now = e.now.Add(time.Hour)
	e.sweep()
	x.Len(e.eventsOf("custody.overdue"), 2, "the loan's and the camera's, and no more")

	// Given a later due, it is not late, and nothing says it was found so
	// again until it misses that one too.
	later := e.now.Add(time.Hour).Truncate(time.Minute)
	_, err = e.app().Custody().Extend(e.owner, app.CustodyExtendRequest_builder{Ref: ref[app.CustodyRef](loan.GetId()), DueAt: timestamppb.New(later)}.Build())
	x.NoError(err)
	e.sweep()
	x.Len(e.eventsOf("custody.overdue"), 2)
	e.now = e.now.Add(2 * time.Hour)
	again := e.now
	e.sweep()
	owns := loansOf(e.eventsOf("custody.overdue"))
	x.Len(owns, 2, "one for each due it missed")
	x.True(again.Equal(owns[1].OccurredAt))
	x.True(later.Equal(value[*app.CustodyOverdue](e, owns[1]).GetDue().AsTime()))
	x.Equal(sentence(later, ny), e.say(owns[1]))
	x.Equal([]string{sentence(later, ny), sentence(due, ny)}, summaries(e.happened(camera, "custody.overdue")), "newest first")

	// Back, it is late no more, which the return says.
	e.now = e.now.Add(time.Hour)
	e.giveBack(loan)
	e.now = e.now.Add(time.Hour)
	e.sweep()
	x.Len(loansOf(e.eventsOf("custody.overdue")), 2)
	x.Equal([]string{"반납", sentence(later, ny), sentence(due, ny)}, summaries(e.happened(camera, "custody.overdue", "custody.return")))
}

// TestWhatALateLoanMakesWaitWaitsUnderIt: a pool's booking made last waits
// once a unit lent before it is late and held until it is back, and the wait
// is part of the loan's being found late -- of each loan's, of two found late
// in one pass -- which is what its history says it waits for, naming nobody.
func TestWhatALateLoanMakesWaitWaitsUnderIt(t *testing.T) {
	e := newEnv(t)
	x := e.x
	camera := e.item("카메라 1", "CM-1", nil, day)
	tripod := e.item("삼각대 1", "TP-1", nil, day)
	cameras := e.pool("카메라", camera, e.item("카메라 2", "CM-2", nil, day))
	tripods := e.pool("삼각대", tripod, e.item("삼각대 2", "TP-2", nil, day))
	kim, _ := e.person("member")
	lee, _ := e.person("member")
	_, park := e.person("member")

	type booked struct {
		pool, unit    *app.Asset
		first, latest *app.Reservation
		loan          *app.Custody
	}
	due := e.now.Add(time.Hour).Truncate(time.Minute)
	bs := []*booked{}
	for _, b := range []*booked{{pool: cameras, unit: camera}, {pool: tripods, unit: tripod}} {
		var err error
		b.first, err = e.take(kim, b.pool, 1, e.tomorrow(9, 0), e.tomorrow(12, 0))
		x.NoError(err)
		b.latest, err = e.take(lee, b.pool, 1, e.tomorrow(10, 0), e.tomorrow(11, 0))
		x.NoError(err)
		// Due back before either begins.
		b.loan = e.lend(park, due, b.unit)
		x.True(e.holds(b.latest, b.pool), "back before it is wanted")
		bs = append(bs, b)
	}

	e.now = e.now.Add(2 * time.Hour)
	e.sweep()
	late := e.about("custody.overdue")
	waits := e.about("reservation.wait")
	for _, b := range bs {
		x.True(e.holds(b.first, b.pool), "made first, kept")
		x.False(e.holds(b.latest, b.pool), "made last, waits")

		own := late[idUUID(b.loan.GetId())]
		x.NotNil(own)
		for _, of := range []*ent.Event{waits[idUUID(b.latest.GetId())], waits[idUUID(b.pool.GetId())]} {
			x.NotNil(of)
			x.NotNil(of.CauseId)
			x.Equal(own.Id, *of.CauseId, "under its own loan's being found late")
		}

		es := e.reservationTimeline(lee, b.latest)
		i := slices.IndexFunc(es, func(en *app.TimelineEntry) bool { return en.GetEventKind() == "reservation.wait" })
		x.GreaterOrEqual(i, 0)
		x.Equal(sentence(due, domain.DefaultZone), es[i].GetCause(), "what it waits for")
		for _, en := range es {
			x.False(strings.Contains(en.GetSummary()+" "+en.GetCause(), park.GetName()), "nobody is named who has it: %s", en.GetSummary())
		}
	}
}

// TestAPickupFoundLateIsOnItsReservationsHistory: what became of the loan a
// pickup made is its reservation's history (D45), its being late too.
func TestAPickupFoundLateIsOnItsReservationsHistory(t *testing.T) {
	e := newEnv(t)
	x := e.x
	camera := e.item("카메라", "CM-1", nil, day)
	e.bookable(camera, nil)
	kim, kimParty := e.person("member")

	r, err := e.reserve(kim, camera, e.tomorrow(9, 0), e.tomorrow(12, 0), nil)
	x.NoError(err)
	ends := r.GetEndsAt().AsTime()
	e.now = r.GetBeginsAt().AsTime()
	_, err = e.pickup(r, kimParty, camera)
	x.NoError(err)

	// Due back when it ends, and an hour after, not back.
	e.now = ends.Add(time.Hour)
	e.sweep()
	e.sweep()
	h := e.history(r)
	x.Equal(1, count(h, sentence(ends, domain.DefaultZone)), "once: %v", h)
	x.Len(e.happened(camera, "custody.overdue"), 1, "and of what it handed out")
}

// loansOf is the events among these that are an operation's own, oldest
// first.
func loansOf(vs []*ent.Event) []*ent.Event {
	out := slices.DeleteFunc(slices.Clone(vs), func(v *ent.Event) bool { return v.CauseId != nil })
	slices.SortFunc(out, func(a, b *ent.Event) int { return a.OccurredAt.Compare(b.OccurredAt) })
	return out
}

func count(vs []string, v string) int {
	n := 0
	for _, s := range vs {
		if s == v {
			n++
		}
	}
	return n
}
