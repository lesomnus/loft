package cli_test

import (
	"cmp"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
	"uuid"

	"github.com/lesomnus/payday/frame"
	"github.com/lesomnus/payday/past"
	"github.com/lesomnus/payday/pdid"
	"github.com/lesomnus/payday/pdtest"
	"github.com/lesomnus/z"
	entschema "github.com/protobuf-orm/ent/dialect/sql/schema"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/known/timestamppb"

	app "github.com/lesomnus/rove"
	"github.com/lesomnus/rove/cli"
	"github.com/lesomnus/rove/cmd"
	"github.com/lesomnus/rove/internal/ent/holder"
	entmigrate "github.com/lesomnus/rove/internal/ent/migrate"
	"github.com/lesomnus/rove/internal/ent/tenant"
	"github.com/lesomnus/rove/server/domain"
	"github.com/lesomnus/rove/server/pd"
)

// oldFacts is Rove's Fact as the builds before payday's past made it
// (25387c6), as each database was left with it: its own table, a row for each
// key, its value as text, and the asset an edge -- and, on PostgreSQL, the
// index of the values known now and the trigger of the asset's tenant pg.sql
// made on it.
var oldFacts = map[string][]string{
	"sqlite3": {
		"CREATE TABLE `fact` (`id` uuid NOT NULL, `key` text NOT NULL, `value` text NOT NULL, `cleared` bool NOT NULL, `date_updated` datetime NOT NULL, " +
			"`date_created` datetime NULL, `valid_from` datetime NULL, `event_id` uuid NOT NULL, `superseded_at` datetime NULL, `superseded_by` uuid NULL, " +
			"`tenant_id` uuid NOT NULL, `asset_id` uuid NOT NULL, PRIMARY KEY (`id`), " +
			"CONSTRAINT `fact_tenant_tenant` FOREIGN KEY (`tenant_id`) REFERENCES `tenant` (`id`) ON DELETE NO ACTION, " +
			"CONSTRAINT `fact_asset_asset` FOREIGN KEY (`asset_id`) REFERENCES `asset` (`id`) ON DELETE NO ACTION)",
		"CREATE INDEX `fact_page` ON `fact` (`tenant_id`, `date_created`, `id`)",
		"CREATE INDEX `fact_asset` ON `fact` (`asset_id`, `key`, `valid_from`)",
	},
	"pgx": {
		`CREATE TABLE fact (id uuid NOT NULL, key character varying NOT NULL, value character varying NOT NULL, cleared boolean NOT NULL,
			date_updated timestamp with time zone NOT NULL, date_created timestamp with time zone, valid_from timestamp with time zone,
			event_id uuid NOT NULL, superseded_at timestamp with time zone, superseded_by uuid, tenant_id uuid NOT NULL, asset_id uuid NOT NULL,
			CONSTRAINT fact_pkey PRIMARY KEY (id),
			CONSTRAINT fact_asset_asset FOREIGN KEY (asset_id) REFERENCES asset(id),
			CONSTRAINT fact_tenant_tenant FOREIGN KEY (tenant_id) REFERENCES tenant(id))`,
		"CREATE INDEX fact_asset ON fact USING btree (asset_id, key, valid_from)",
		"CREATE UNIQUE INDEX fact_once ON fact USING btree (asset_id, key, valid_from) WHERE (superseded_at IS NULL)",
		"CREATE INDEX fact_page ON fact USING btree (tenant_id, date_created, id)",
		"CREATE TRIGGER fact_asset_id_same_tenant BEFORE INSERT OR UPDATE OF asset_id, tenant_id ON fact FOR EACH ROW EXECUTE FUNCTION rove_same_tenant('asset_id', 'asset')",
	},
}

// oldFact is a row of Rove's own Fact.
type oldFact struct {
	id, asset, event uuid.UUID
	key, value       string
	cleared          bool
	from, recorded   time.Time
	superseded       *time.Time
	supersededBy     *uuid.UUID
}

// TestAnAssetsFactsMoveIntoThePast: the facts Rove wrote in its own table, as
// the build before left them, are payday's past's once migrated (payday#151):
// each row the same row -- its identifier, its asset, its moments and the
// events it names as they were -- written as the asset's field by its number
// and its value's canonical JSON, whatever form the request said it in; and
// each put on the trail as the migration's. What an asset was at a moment
// reads as it did, and the rows are what the trail says of them. Asked again,
// the migration has nothing to do; and what Rove's history reads, and what its
// expiry takes, are the moved rows as they are any other.
func TestAnAssetsFactsMoveIntoThePast(t *testing.T) {
	x := require.New(t)
	ctx := t.Context()

	key := filepath.Join(t.TempDir(), "digest.key")
	b := make([]byte, 32)
	_, err := rand.Read(b)
	x.NoError(err)
	x.NoError(os.WriteFile(key, []byte(base64.StdEncoding.EncodeToString(b)+"\n"), 0o600))
	c := cmd.Config{}
	c.Db.Driver, c.Db.Dsn = pdtest.DB(t)
	c.Watch.Broker = "memory"
	c.App.Files = t.TempDir()
	c.Auth.Roster.Db.Driver, c.Auth.Roster.Db.Dsn = pdtest.DB(t)
	c.Audit.Digest.Key = key
	run(t, cli.NewCmdInit(&c), "--tenant", "acme", "--login", "owner", "--password", "owner1234")

	s, err := cmd.Build(ctx, c)
	x.NoError(err)
	defer s.Close()
	tn, err := s.Ent.Tenant.Query().Where(tenant.Alias("acme")).Only(ctx)
	x.NoError(err)
	h, err := s.Ent.Holder.Query().Where(holder.TenantId(tn.Id), holder.Alias("owner")).Only(ctx)
	x.NoError(err)
	owner := frame.Into(ctx, frame.New(pdid.Id(h.Id), pdid.Id(tn.Id), frame.Whole()).WithScope(frame.Only(pdid.Id(tn.Id))))

	// Two assets and what they are of, through the stack.
	ty, err := s.Walled.AssetType().Add(owner, app.AssetTypeAddRequest_builder{Name: "노트북"}.Build())
	x.NoError(err)
	mo, err := s.Walled.ItemModel().Add(owner, app.ItemModelAddRequest_builder{Name: "XPS 13", Maker: "Dell"}.Build())
	x.NoError(err)
	day := 24 * time.Hour
	now := time.Now().UTC().Truncate(time.Microsecond)
	laptop, err := s.Walled.Asset().Add(owner, app.AssetAddRequest_builder{Name: "노트북", Tag: "NB-1", Since: timestamppb.New(now.Add(-300 * day))}.Build())
	x.NoError(err)
	room, err := s.Walled.Asset().Add(owner, app.AssetAddRequest_builder{Name: "회의실", Tag: "S-1", Kind: "space", Since: timestamppb.New(now.Add(-300 * day))}.Build())
	x.NoError(err)
	a, r := uuid.UUID(laptop.GetId()), uuid.UUID(room.GetId())

	// What the build before kept of them: the facts each was made with, as
	// it wrote them, under the identifiers they have -- which the trail
	// knows -- and the rest of what it wrote of them since, in every form a
	// request said a value in.
	var rows []oldFact
	made, err := s.Ent.Fact.Query().All(ctx)
	x.NoError(err)
	for _, f := range made {
		k, ok := domain.KeyOf(past.Slot{Field: protoreflect.FieldNumber(f.FieldNumber), Key: f.Key})
		x.True(ok)
		var text string
		x.NoError(json.Unmarshal([]byte(f.Value), &text), f.Value)
		rows = append(rows, oldFact{id: f.Id, asset: f.SubjectId, event: f.EventId, key: k, value: text, from: f.ValidFrom, recorded: f.DateCreated})
	}
	ev := func() uuid.UUID { return pdid.New(pd.EventDomain).Uuid() }
	at := func(d time.Duration) time.Time {
		return now.Add(-d).Add(123456 * time.Nanosecond).Truncate(time.Microsecond)
	}
	fact := func(asset uuid.UUID, k, v string, from time.Duration) oldFact {
		return oldFact{id: pdid.New(pd.FactDomain).Uuid(), asset: asset, event: ev(), key: k, value: v, from: at(from), recorded: at(from - time.Hour)}
	}
	cleared := func(asset uuid.UUID, k string, from time.Duration) oldFact {
		v := fact(asset, k, "", from)
		v.cleared = true
		return v
	}
	superseded := func(v oldFact, by time.Duration) oldFact {
		when, e := at(by), ev()
		v.superseded, v.supersededBy = &when, &e
		return v
	}
	rows = append(rows,
		fact(a, "desc", `C:\temp "XPS"`+"\n줄\t바꿈\x01", 250*day),
		fact(a, "type", strings.ToUpper(pdid.Id(ty.GetId()).String()), 250*day),
		fact(a, "model", "{"+pdid.Id(mo.GetId()).String()+"}", 250*day),
		fact(a, "serial", "SN 0001", 250*day),
		fact(a, "status", "in_repair", 200*day),
		fact(a, "status", "active", 150*day),
		fact(a, "attr.보증 만료", "2027-01-01", 250*day),
		fact(a, "attr.", "빈 키", 250*day),
		fact(a, "attr.cpu", "i7", 250*day),
		cleared(a, "attr.cpu", 100*day),
		cleared(a, "serial", 100*day),
		superseded(fact(a, "name", "옛 노트북", 280*day), 279*day),
		fact(r, "condition", "damaged", 20*day),
		fact(r, "tag", "회의실-3층", 20*day),
	)

	exec := func(q string, args ...any) {
		t.Helper()
		_, err := s.Db.ExecContext(ctx, q, args...)
		x.NoError(err, q)
	}
	exec("DROP TABLE fact")
	for _, q := range oldFacts[c.Db.Driver] {
		exec(q)
	}
	put := func(v oldFact) {
		q := "INSERT INTO fact (id, key, value, cleared, date_updated, date_created, valid_from, event_id, superseded_at, superseded_by, tenant_id, asset_id) " +
			"VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)"
		if s.Dialect == "postgres" {
			q = "INSERT INTO fact (id, key, value, cleared, date_updated, date_created, valid_from, event_id, superseded_at, superseded_by, tenant_id, asset_id) " +
				"VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)"
		}
		var by any
		if v.supersededBy != nil {
			by = v.supersededBy.String()
		}
		var sup any
		if v.superseded != nil {
			sup = *v.superseded
		}
		updated := v.recorded
		if v.superseded != nil {
			updated = *v.superseded
		}
		exec(q, v.id.String(), v.key, v.value, v.cleared, updated, v.recorded, v.from, v.event.String(), sup, by, tn.Id.String(), v.asset.String())
	}
	for _, v := range rows {
		put(v)
	}

	// What the history said of the laptop before, by the reading the build
	// before made of its rows: at each of these moments, as known now.
	moments := []time.Time{now.Add(-260 * day), now.Add(-175 * day), now.Add(-120 * day), now}
	oracle := map[time.Time]map[string]string{}
	for _, m := range moments {
		oracle[m] = factsAt(rows, a, m)
	}

	out := run(t, cli.NewCmdMigrate(&c))
	x.Contains(out, fmt.Sprintf("the assets' facts moved into payday's past: %d rewritten, %d put on the trail", len(rows), len(rows)))

	// Each row, the same row in payday's shape.
	for _, v := range rows {
		f, err := s.Ent.Fact.Get(ctx, v.id)
		x.NoError(err, "%s %s", v.key, v.value)
		field, entry, value, err := domain.LegacyFact(v.key, v.value, v.cleared)
		x.NoError(err)
		x.Equal(v.asset, f.SubjectId)
		x.EqualValues(pd.AssetDomain, f.Domain)
		x.EqualValues(field, f.FieldNumber, v.key)
		x.Equal(entry, f.Key, v.key)
		x.Equal(value, f.Value, v.key)
		x.Equal(v.event, f.EventId)
		x.True(v.from.Equal(f.ValidFrom), "%s from %s, and %s", v.key, v.from, f.ValidFrom)
		x.True(v.recorded.Equal(f.DateCreated), "%s known from %s, and %s", v.key, v.recorded, f.DateCreated)
		if v.superseded == nil {
			x.Nil(f.SupersededAt)
			x.Nil(f.SupersededBy)
		} else {
			x.True(v.superseded.Equal(*f.SupersededAt))
			x.Equal(*v.supersededBy, *f.SupersededBy)
		}
	}
	x.Equal(`"`+strings.ToLower(pdid.Id(ty.GetId()).String())+`"`, value(t, s, a, 9), "a type in one form")
	x.Equal(`"`+pdid.Id(mo.GetId()).String()+`"`, value(t, s, a, 10), "a model in one form")

	// Nothing of the table before is left.
	x.NoError(entschema.Check(ctx, s.Db, s.Dialect, entmigrate.Tables))
	for _, col := range []string{"asset_id", "cleared"} {
		x.False(hasColumnOf(t, s, "fact", col), col)
	}
	for _, tb := range []string{"rove_fact_aside", "rove_fact_moved"} {
		x.False(hasTableOf(t, s, tb), tb)
	}

	// What it was at a moment, as it read before -- a type and a model by
	// the one form an identifier is written in.
	for _, m := range moments {
		v, err := s.Walled.Asset().QueryAt(owner, app.AssetQueryAtRequest_builder{Root: app.AssetById(a[:]), At: timestamppb.New(m), Depth: 1}.Build())
		x.NoError(err)
		want := oracle[m]
		for _, k := range []string{"type", "model"} {
			if id, ok := want[k]; ok {
				want[k] = pdid.MustParse(strings.Trim(id, "{}")).String()
			}
		}
		x.Equal(want, v.GetItems()[0].GetFacts(), "at %s", m)
	}

	// And the rows are what the trail says of them.
	got, err := printed(t, cli.NewCmdTrail(&c), "rows")
	x.NoError(err, got)
	x.Contains(got, " 0 differ, 0 forged,")
	x.Contains(got, " 0 not on the trail,")

	x.NotContains(run(t, cli.NewCmdMigrate(&c)), "moved into payday's past", "a second time")

	// Its history reads them, and an expiry takes them as any: a window of
	// a hundred and thirty days takes what had stopped being true by then --
	// the name corrected long ago, and the status it was twice before it was
	// active -- and what was then is what the window begins with.
	tl, err := s.Walled.Asset().Timeline(owner, app.AssetTimelineRequest_builder{Ref: app.AssetById(a[:]), Superseded: true}.Build())
	x.NoError(err)
	summaries := []string{}
	for _, e := range tl.GetEntries() {
		if e.GetKind() == "fact" {
			summaries = append(summaries, e.GetSummary())
		}
	}
	x.Contains(summaries, "유형 = 노트북")
	x.Contains(summaries, "모델 = Dell XPS 13")
	x.Contains(summaries, "상태 = 수리중")
	x.Contains(summaries, "cpu = (지움)")
	x.Contains(summaries, " = 빈 키")

	_, err = s.Base.Tenant().Patch(owner, app.TenantPatchRequest_builder{Ref: app.TenantRef_builder{Id: tn.Id[:]}.Build(), KeepDays: z.Ptr(uint32(130)), DateUpdatedForce: z.Ptr(true)}.Build())
	x.NoError(err)
	before := must(s.Ent.Fact.Query().Count(ctx))
	gone, err := s.Deps.Expire(ctx, s.Base, s.Drv, pdid.Id(tn.Id), false)
	x.NoError(err)
	x.Equal(3, gone.Facts, "the old name, and what the status was before it was active")
	x.Equal(before-gone.Facts, must(s.Ent.Fact.Query().Count(ctx)))
	for _, m := range moments[2:] {
		v, err := s.Walled.Asset().QueryAt(owner, app.AssetQueryAtRequest_builder{Root: app.AssetById(a[:]), At: timestamppb.New(m), Depth: 1}.Build())
		x.NoError(err)
		x.Equal(oracle[m], v.GetItems()[0].GetFacts(), "inside the window, at %s", m)
	}
	got, err = printed(t, cli.NewCmdTrail(&c), "rows")
	x.NoError(err, got)
	x.Contains(got, " 0 missing;", "what went is on the trail as gone")
}

// TestAFactThePastCannotWriteStopsTheMigration: a fact Rove never wrote -- a
// key no field of an asset is under -- is said, with how many, and nothing is
// moved; what was set aside is there for a person to say what it was, and
// once they have, the migration goes on from where it stopped.
func TestAFactThePastCannotWriteStopsTheMigration(t *testing.T) {
	x := require.New(t)
	ctx := t.Context()

	c := cmd.Config{}
	c.Db.Driver, c.Db.Dsn = pdtest.DB(t)
	c.Watch.Broker = "memory"
	c.App.Files = t.TempDir()
	c.Auth.Roster.Db.Driver, c.Auth.Roster.Db.Dsn = pdtest.DB(t)
	run(t, cli.NewCmdInit(&c), "--tenant", "acme", "--login", "owner", "--password", "owner1234")
	s, err := cmd.Build(ctx, c)
	x.NoError(err)
	defer s.Close()
	tn, err := s.Ent.Tenant.Query().Where(tenant.Alias("acme")).Only(ctx)
	x.NoError(err)
	h, err := s.Ent.Holder.Query().Where(holder.TenantId(tn.Id), holder.Alias("owner")).Only(ctx)
	x.NoError(err)
	owner := frame.Into(ctx, frame.New(pdid.Id(h.Id), pdid.Id(tn.Id), frame.Whole()).WithScope(frame.Only(pdid.Id(tn.Id))))
	laptop, err := s.Walled.Asset().Add(owner, app.AssetAddRequest_builder{Name: "노트북", Tag: "NB-1"}.Build())
	x.NoError(err)

	_, err = s.Db.ExecContext(ctx, "DROP TABLE fact")
	x.NoError(err)
	for _, q := range oldFacts[c.Db.Driver] {
		_, err := s.Db.ExecContext(ctx, q)
		x.NoError(err, q)
	}
	q := "INSERT INTO fact (id, key, value, cleared, date_updated, date_created, valid_from, event_id, tenant_id, asset_id) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)"
	if s.Dialect == "postgres" {
		q = "INSERT INTO fact (id, key, value, cleared, date_updated, date_created, valid_from, event_id, tenant_id, asset_id) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)"
	}
	now := time.Now().UTC()
	bad := []string{"location", "location", "Name"}
	for i, k := range append([]string{"name"}, bad...) {
		_, err := s.Db.ExecContext(ctx, q, pdid.New(pd.FactDomain).String(), k, fmt.Sprint("x", i), false, now, now, now.Add(time.Duration(i)*time.Second),
			pdid.New(pd.EventDomain).String(), tn.Id.String(), uuid.UUID(laptop.GetId()).String())
		x.NoError(err)
	}

	err = cli.Migrate(ctx, s)
	x.ErrorContains(err, "3 payday's past cannot write")
	x.ErrorContains(err, `"location": 2`)
	x.ErrorContains(err, `"Name": 1`)
	x.True(hasTableOf(t, s, "rove_fact_aside"), "what was set aside")
	x.Zero(must(s.Ent.Fact.Query().Count(ctx)), "a fact moved")

	// Said what they were, they move.
	_, err = s.Db.ExecContext(ctx, "UPDATE rove_fact_aside SET key = 'name' WHERE key = 'Name'")
	x.NoError(err)
	_, err = s.Db.ExecContext(ctx, "DELETE FROM rove_fact_aside WHERE key = 'location'")
	x.NoError(err)
	x.NoError(cli.Migrate(ctx, s))
	x.Equal(2, must(s.Ent.Fact.Query().Count(ctx)))
	x.False(hasTableOf(t, s, "rove_fact_aside"))
}

// factsAt is what the build before read an asset's facts as at `at`, as known
// now: each key's value of the latest that held by then, a key cleared
// holding none.
func factsAt(rows []oldFact, asset uuid.UUID, at time.Time) map[string]string {
	vs := slices.Clone(rows)
	slices.SortStableFunc(vs, func(a, b oldFact) int {
		return cmp.Or(a.from.Compare(b.from), a.recorded.Compare(b.recorded))
	})
	out := map[string]string{}
	for _, v := range vs {
		if v.asset != asset || v.superseded != nil || v.from.After(at) {
			continue
		}
		if v.cleared {
			delete(out, v.key)
			continue
		}
		out[v.key] = v.value
	}
	return out
}

// value is the value of the standing fact of field `n` of `asset`.
func value(t *testing.T, s *cmd.Server, asset uuid.UUID, n uint32) string {
	t.Helper()
	var v string
	q := "SELECT value FROM fact WHERE subject_id = ? AND field_number = ? AND superseded_at IS NULL"
	if s.Dialect == "postgres" {
		q = "SELECT value FROM fact WHERE subject_id = $1 AND field_number = $2 AND superseded_at IS NULL"
	}
	require.NoError(t, s.Db.QueryRowContext(t.Context(), q, asset.String(), n).Scan(&v))
	return v
}

func hasColumnOf(t *testing.T, s *cmd.Server, table, column string) bool {
	t.Helper()
	q := "SELECT count(*) FROM pragma_table_info(?) WHERE name = ?"
	if s.Dialect == "postgres" {
		q = "SELECT count(*) FROM information_schema.columns WHERE table_schema = current_schema() AND table_name = $1 AND column_name = $2"
	}
	n := 0
	require.NoError(t, s.Db.QueryRowContext(t.Context(), q, table, column).Scan(&n))
	return n > 0
}

func hasTableOf(t *testing.T, s *cmd.Server, table string) bool {
	t.Helper()
	q := "SELECT count(*) FROM sqlite_master WHERE type = 'table' AND name = ?"
	if s.Dialect == "postgres" {
		q = "SELECT count(*) FROM information_schema.tables WHERE table_schema = current_schema() AND table_name = $1"
	}
	n := 0
	require.NoError(t, s.Db.QueryRowContext(t.Context(), q, table).Scan(&n))
	return n > 0
}

func must[T any](v T, err error) T {
	if err != nil {
		panic(err)
	}
	return v
}
