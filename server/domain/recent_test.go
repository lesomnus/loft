package domain_test

import (
	"cmp"
	"context"
	"fmt"
	"slices"
	"testing"
	"time"
	"uuid"

	"google.golang.org/grpc/codes"

	app "github.com/lesomnus/loft"
	"github.com/lesomnus/loft/internal/ent/audit"
)

// The workspace's feed and its trail, newest first and a page at a time: each
// page goes on from the row the one before ended at, so that a page that ends
// inside an instant goes on with the rest of it. An operation records all of
// its events at one -- a bulk registration one for every asset -- and the
// trail's lines are stamped to the microsecond, each as it is written.

// row is a row of a list newest first, as its order reads it.
type row struct {
	at time.Time
	id uuid.UUID
}

// pagesOf reads a list a page at a time from its start: `read` answers the
// page after the cursor it is given, and the cursor after that page.
func (e *env) pagesOf(read func(after string) ([]row, string)) [][]row {
	e.t.Helper()
	pages := [][]row{}
	after := ""
	for range 100 {
		rs, next := read(after)
		pages = append(pages, rs)
		if next == "" {
			return pages
		}
		after = next
	}
	e.x.FailNow("a list read a page at a time does not end")
	return nil
}

// readOnce holds a list read a page at a time to the list read at once: each
// row once, newest first and then by key, and with a page that ended inside
// an instant among them, which is what the list is read across.
func (e *env) readOnce(what string, whole []row, pages [][]row) {
	e.t.Helper()
	x := e.x
	x.True(slices.IsSortedFunc(whole, func(a, b row) int { return cmp.Or(b.at.Compare(a.at), b.id.Compare(a.id)) }), "%s: newest first, then by key", what)

	got := slices.Concat(pages...)
	x.Equal(whole, got, "%s: read a page at a time, it is what one read is", what)
	ids := []uuid.UUID{}
	for _, r := range got {
		ids = append(ids, r.id)
	}
	slices.SortFunc(ids, uuid.UUID.Compare)
	x.Len(slices.Compact(ids), len(got), "%s: a row twice", what)

	inside := false
	for i := 1; i < len(pages); i++ {
		if len(pages[i-1]) > 0 && len(pages[i]) > 0 && pages[i-1][len(pages[i-1])-1].at.Equal(pages[i][0].at) {
			inside = true
		}
	}
	x.True(inside, "%s: no page ended inside an instant", what)
}

// instants is a workspace's history in three instants: seven things
// registered one at a time, an intake of twelve -- its own event, and a child
// for each asset -- a second later, and two more on their own a second after
// that.
func (e *env) instants() {
	for i := range 7 {
		e.item(fmt.Sprintf("모니터 %d", i), fmt.Sprintf("MO-%d", i), nil, day)
	}
	e.now = e.now.Add(time.Second)
	items := []*app.AssetAddRequest{}
	for i := range 12 {
		items = append(items, app.AssetAddRequest_builder{Name: fmt.Sprintf("의자 %d", i)}.Build())
	}
	_, err := e.app().Asset().AddMany(e.owner, app.AssetAddManyRequest_builder{Items: items}.Build())
	e.x.NoError(err)
	e.now = e.now.Add(time.Second)
	e.item("노트북", "NB-1", nil, day)
	e.item("노트북", "NB-2", nil, day)
}

// TestTheFeedIsReadAcrossAnInstantOnce: the workspace's feed and the feed
// with the children of its operations, three at a time, across the instants
// they were recorded at.
func TestTheFeedIsReadAcrossAnInstantOnce(t *testing.T) {
	e := newEnv(t)
	x := e.x
	e.instants()

	feed := func(children bool, size uint32) func(string) ([]row, string) {
		return func(after string) ([]row, string) {
			v, err := e.app().Event().Recent(e.owner, app.EventRecentRequest_builder{Size: size, Children: children, After: after}.Build())
			x.NoError(err)
			rs := []row{}
			for _, ev := range v.GetItems() {
				rs = append(rs, row{ev.GetDateCreated().AsTime(), uuid.UUID(ev.GetId())})
			}
			return rs, v.GetNext()
		}
	}
	for _, children := range []bool{false, true} {
		what := fmt.Sprintf("the feed, children %v", children)
		whole, next := feed(children, 500)("")
		x.Empty(next, "%s: all of it in one page", what)
		x.GreaterOrEqual(len(whole), 10, what)
		e.readOnce(what, whole, e.pagesOf(feed(children, 3)))
	}

	_, err := e.app().Event().Recent(e.owner, app.EventRecentRequest_builder{After: "어디"}.Build())
	x.Equal(codes.InvalidArgument, codeOf(err), "a page that is no page")
}

// TestTheTrailIsReadAcrossAnInstantOnce: the trail of the workspace's
// writes, seven lines at a time, across the instants they were written at.
func TestTheTrailIsReadAcrossAnInstantOnce(t *testing.T) {
	e := newEnv(t)
	x := e.x
	ctx := context.Background()
	e.instants()

	// The lines of what was written, each at its own microsecond -- and, after
	// them, ten at one and two a second later, as lines written together may
	// be: the trail's pass writes one for each row it carries forward, in a
	// loop. Beneath the servers, which stamp a line with the time it is
	// written; keyed in no order of their making, so that the order is the
	// keys'.
	v, err := e.s.Ent.Audit.Query().Where(audit.TenantId(e.tenant.Uuid())).First(ctx)
	x.NoError(err)
	at := time.Now().UTC().Add(time.Hour).Truncate(time.Second)
	for i := range 12 {
		when := at
		if i >= 10 {
			when = at.Add(time.Second)
		}
		x.NoError(e.s.Ent.Audit.Create().
			SetId(uuid.NewV4()).
			SetTenantId(v.TenantId).
			SetActorId(v.ActorId).
			SetActorTenantId(v.ActorTenantId).
			SetTraceId(v.TraceId).
			SetAction(v.Action).
			SetObjectId(v.ObjectId).
			SetPatch(v.Patch).
			SetValue(v.Value).
			SetFields(v.Fields).
			SetDomain(v.Domain).
			SetDateCreated(when).
			Exec(ctx))
	}

	trail := func(size uint32) func(string) ([]row, string) {
		return func(after string) ([]row, string) {
			v, err := e.app().Audit().Recent(e.owner, app.AuditRecentRequest_builder{Size: size, After: after}.Build())
			x.NoError(err)
			rs := []row{}
			for _, a := range v.GetItems() {
				rs = append(rs, row{a.GetDateCreated().AsTime(), uuid.UUID(a.GetId())})
			}
			return rs, v.GetNext()
		}
	}
	whole, next := trail(500)("")
	x.Empty(next, "all of it in one page")
	e.readOnce("the trail", whole, e.pagesOf(trail(7)))

	_, err = e.app().Audit().Recent(e.owner, app.AuditRecentRequest_builder{After: "어디"}.Build())
	x.Equal(codes.InvalidArgument, codeOf(err), "a page that is no page")
}
