package domain_test

import (
	"context"
	"fmt"
	"math/rand/v2"
	"os"
	"slices"
	"testing"
	"time"
	"uuid"

	"github.com/lesomnus/payday/pdid"
	"google.golang.org/protobuf/types/known/timestamppb"

	app "github.com/lesomnus/rove"
	"github.com/lesomnus/rove/internal/ent"
	"github.com/lesomnus/rove/server/domain"
	"github.com/lesomnus/rove/server/pd"
)

// TestAThousandThingsAtAMomentAnswerInASecond: plan.md M1, the size design
// 1.2 is written for -- a hundred thousand assets and a year of history -- and
// a building of a thousand things in it asked about at a moment of that year.
// Ninety-five of every hundred answers come back within a second.
//
// It writes about seven hundred thousand rows, so it runs when asked:
// `ROVE_PERF=1` with `PDTEST_POSTGRES`. The rows are written straight to the
// tables, as a history the domain layer would have written.
func TestAThousandThingsAtAMomentAnswerInASecond(t *testing.T) {
	if os.Getenv("ROVE_PERF") == "" {
		t.Skip("set ROVE_PERF=1, with PDTEST_POSTGRES")
	}
	e := newEnv(t)
	e.postgresOnly()
	x := e.x
	ctx := context.Background()

	const (
		buildings = 100
		rooms     = 9   // per building
		things    = 110 // per room: 1 + 9 + 990 = 1,000 a building
	)
	year := e.now.Add(-365 * day)
	ev := pdid.New(pd.EventDomain).Uuid()
	tenant := e.tenant.Uuid()

	assets := []*ent.AssetCreate{}
	places := []*ent.PlacementCreate{}
	facts := []*ent.FactCreate{}
	// Written in the order the foreign keys want: the assets, then the rows
	// about them.
	flush := func(force bool) {
		if !force && len(assets) < 1000 && len(places) < 1000 && len(facts) < 1000 {
			return
		}
		if len(assets) > 0 {
			x.NoError(e.s.Ent.Asset.CreateBulk(assets...).Exec(ctx))
			assets = assets[:0]
		}
		for len(places) > 0 {
			k := min(len(places), 1000)
			x.NoError(e.s.Ent.Placement.CreateBulk(places[:k]...).Exec(ctx))
			places = places[k:]
		}
		for len(facts) > 0 {
			k := min(len(facts), 1000)
			x.NoError(e.s.Ent.Fact.CreateBulk(facts[:k]...).Exec(ctx))
			facts = facts[k:]
		}
	}
	n := 0
	asset := func(kind string, parent *uuid.UUID, since time.Time) uuid.UUID {
		n++
		id := pdid.New(pd.AssetDomain).Uuid()
		tag := fmt.Sprintf("P-%06d", n)
		c := e.s.Ent.Asset.Create().SetId(id).SetTenantId(tenant).SetName(tag).SetDesc("").SetKind(kind).
			SetTag(tag).SetSerial("").SetStatus("active").SetCondition("good").SetSlot("").SetPlacementMode("").SetContentVersion(0).SetDateUpdated(since).SetDateCreated(since)
		if parent != nil {
			c.SetParentId(*parent).SetPlacementMode("located")
		}
		assets = append(assets, c)
		// Each as payday's past keeps it: the asset's field, and its
		// value's canonical JSON (payday#151).
		for k, v := range map[string]string{"name": tag, "status": "active", "condition": "good", "tag": tag} {
			field, key, value, err := domain.LegacyFact(k, v, false)
			x.NoError(err)
			facts = append(facts, e.s.Ent.Fact.Create().SetId(pdid.New(pd.FactDomain).Uuid()).SetTenantId(tenant).SetSubjectId(id).
				SetDomain(uint32(pd.AssetDomain)).SetFieldNumber(uint32(field)).SetKey(key).SetValue(value).
				SetValidFrom(since).SetEventId(ev).SetDateUpdated(since).SetDateCreated(since))
		}
		return id
	}
	place := func(child, parent uuid.UUID, from time.Time, to *time.Time) {
		c := e.s.Ent.Placement.Create().SetId(pdid.New(pd.PlacementDomain).Uuid()).SetTenantId(tenant).
			SetChildId(child).SetParentId(parent).SetMode("located").SetSlot("").SetUFrom(0).SetUTo(0).
			SetValidFrom(from).SetEventId(ev).SetDateUpdated(from).SetDateCreated(from)
		if to != nil {
			c.SetValidTo(*to)
		}
		places = append(places, c)
	}

	began := time.Now()
	roots := []uuid.UUID{}
	for range buildings {
		b := asset("space", nil, year.Add(-30*day))
		roots = append(roots, b)
		inside := []uuid.UUID{}
		for range rooms {
			r := asset("space", &b, year.Add(-30*day))
			place(r, b, year.Add(-30*day), nil)
			inside = append(inside, r)
		}
		flush(true)
		for i := range rooms * things {
			// Three rooms over the year, all in one building: a building is
			// a thousand things at every moment of it.
			a, bb, c := inside[i%rooms], inside[(i+1)%rooms], inside[(i+2)%rooms]
			id := asset("item", &c, year)
			first, second := year.Add(time.Duration(i%120)*day), year.Add(time.Duration(200+i%100)*day)
			place(id, a, year, &first)
			place(id, bb, first, &second)
			place(id, c, second, nil)
			flush(false)
		}
		flush(true)
	}
	t.Logf("wrote %d assets and their year in %s", n, time.Since(began).Round(time.Second))
	_, err := e.s.Db.ExecContext(ctx, "ANALYZE")
	x.NoError(err)

	took := []time.Duration{}
	r := rand.New(rand.NewPCG(1, 2))
	for range 60 {
		root := roots[r.IntN(len(roots))]
		at := year.Add(time.Duration(r.Int64N(int64(365 * day))))
		start := time.Now()
		v, err := e.app().Asset().QueryAt(e.owner, app.AssetQueryAtRequest_builder{
			Root: app.AssetRef_builder{Id: pdid.Id(root).Bytes()}.Build(), At: timestamppb.New(at),
		}.Build())
		took = append(took, time.Since(start))
		x.NoError(err)
		x.Len(v.GetItems(), 1+rooms+rooms*things, "a building and everything in it")
	}
	slices.Sort(took)
	p50, p95 := took[len(took)/2], took[len(took)*95/100]
	t.Logf("QueryAt over 1,000 of %d: p50 %s, p95 %s, max %s", n, p50.Round(time.Millisecond), p95.Round(time.Millisecond), took[len(took)-1].Round(time.Millisecond))
	x.Less(p95, time.Second)
}
