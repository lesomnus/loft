package cli_test

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"fmt"
	"io"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/lesomnus/payday/pdid"
	"github.com/lesomnus/payday/pdtest"
	"github.com/lesomnus/xli"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/encoding/protojson"

	app "github.com/lesomnus/loft"
	"github.com/lesomnus/loft/cli"
	"github.com/lesomnus/loft/cmd"
	"github.com/lesomnus/loft/internal/ent/event"
	"github.com/lesomnus/loft/internal/ent/holder"
	"github.com/lesomnus/loft/internal/ent/tenant"
	"github.com/lesomnus/loft/internal/ent/tenantcontract"
	"github.com/lesomnus/loft/internal/loftest"
	"github.com/lesomnus/loft/server/admin"
	"github.com/lesomnus/loft/server/operator"
)

// ran runs a command and answers what it printed, read as it prints: a pipe
// holds only so much before the writer waits for a reader.
func ran(t *testing.T, k *xli.Command, args ...string) (string, error) {
	t.Helper()
	r, w, err := os.Pipe()
	require.NoError(t, err)
	read := make(chan []byte)
	go func() {
		b, _ := io.ReadAll(r)
		read <- b
	}()
	was := os.Stdout
	os.Stdout = w
	err = k.Run(t.Context(), args)
	os.Stdout = was
	require.NoError(t, w.Close())
	return string(<-read), err
}

type deployment struct {
	c   cmd.Config
	s   *cmd.Server
	url string
	// key is where alice's key is, as `--key` names it.
	key string
}

func deploy(t *testing.T) *deployment { return deployWith(t, nil) }

// deployWith is a deployment, its configuration as `with` says, with acme --
// its owner and kim, a member -- and its entry point, which alice holds a key
// to.
func deployWith(t *testing.T, with func(*cmd.Config)) *deployment {
	t.Helper()
	x := require.New(t)
	c := cmd.Config{}
	c.Db.Driver, c.Db.Dsn = pdtest.DB(t)
	c.Watch.Broker = "memory"
	c.App.Files = t.TempDir()
	c.Auth.Roster.Db.Driver, c.Auth.Roster.Db.Dsn = pdtest.DB(t)
	if with != nil {
		with(&c)
	}
	_, err := ran(t, cli.NewCmdInit(&c), "--tenant", "acme", "--login", "owner", "--password", "owner1234")
	x.NoError(err)

	s, err := cmd.Build(t.Context(), c)
	x.NoError(err)
	t.Cleanup(func() { s.Close() })
	loftest.Member(t, s, "acme", "kim", "member", "kim-password")
	ops, err := admin.Operators(map[string]string{"alice": operatorKey})
	x.NoError(err)
	h, err := admin.Routes{S: s, C: c, Operators: ops}.Handler(t.Context())
	x.NoError(err)
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return &deployment{c: c, s: s, url: srv.URL, key: keyRef(t)}
}

// inZone has the process's local zone be `l` for the rest of the test: what a
// command says of a day is UTC's, whatever zone the operator's machine is in.
func inZone(t *testing.T, l *time.Location) {
	t.Helper()
	was := time.Local
	time.Local = l
	t.Cleanup(func() { time.Local = was })
}

// saysDay answers whether `out` says `prefix` and then the day, in UTC, of a
// moment between `from` and `to`: either one's, for when UTC's midnight came
// between the two.
func saysDay(out, prefix string, from, to time.Time) bool {
	return strings.Contains(out, prefix+from.UTC().Format(time.DateOnly)) || strings.Contains(out, prefix+to.UTC().Format(time.DateOnly))
}

// awayFromUTC is a zone whose date is not UTC's at `now`, nor for hours after:
// fourteen hours ahead of it from ten in the morning UTC, and twelve behind
// before then.
func awayFromUTC(now time.Time) *time.Location {
	if now.UTC().Hour() >= 10 {
		return time.FixedZone("UTC+14", 14*60*60)
	}
	return time.FixedZone("UTC-12", -12*60*60)
}

// tablesOf is every table of the database `driver` and `dsn` name, opened
// afresh, and a digest of what each holds: what a call that says it changes
// nothing is held to, table by table.
func tablesOf(t *testing.T, driver, dsn string) map[string]string {
	t.Helper()
	x := require.New(t)
	db, err := sql.Open(driver, dsn)
	x.NoError(err)
	defer db.Close()

	q := "SELECT name FROM sqlite_master WHERE type = 'table' AND name NOT LIKE 'sqlite_%'"
	if driver == "pgx" {
		q = "SELECT table_name FROM information_schema.tables WHERE table_schema = current_schema() AND table_type = 'BASE TABLE'"
	}
	names := []string{}
	rs, err := db.QueryContext(t.Context(), q)
	x.NoError(err)
	for rs.Next() {
		v := ""
		x.NoError(rs.Scan(&v))
		names = append(names, v)
	}
	x.NoError(rs.Err())
	x.NoError(rs.Close())
	x.NotEmpty(names)

	out := map[string]string{}
	for _, name := range names {
		rs, err := db.QueryContext(t.Context(), `SELECT * FROM "`+name+`"`)
		x.NoError(err, name)
		cols, err := rs.Columns()
		x.NoError(err)
		lines := []string{}
		for rs.Next() {
			vs := make([]any, len(cols))
			ps := make([]any, len(cols))
			for i := range vs {
				ps[i] = &vs[i]
			}
			x.NoError(rs.Scan(ps...), name)
			lines = append(lines, fmt.Sprintf("%#v", vs))
		}
		x.NoError(rs.Err())
		x.NoError(rs.Close())
		slices.Sort(lines)
		sum := sha256.Sum256([]byte(strings.Join(lines, "\n")))
		out[name] = fmt.Sprintf("%d row(s), %x", len(lines), sum[:8])
	}
	return out
}

// unchanged holds what tablesOf said of a database after a call to what it
// said before, table by table, but for the tables `except` names.
func unchanged(t *testing.T, was, now map[string]string, except ...string) {
	t.Helper()
	for name := range was {
		_, ok := now[name]
		require.True(t, ok, "%s went", name)
	}
	for name, v := range now {
		if slices.Contains(except, name) {
			continue
		}
		require.Contains(t, was, name, "%s was made", name)
		require.Equal(t, was[name], v, "%s changed", name)
	}
}

// loft runs `loft` against the deployment's entry point, as alice: a
// connection for the command alone, as a script names one.
func (d *deployment) loft(t *testing.T, args ...string) (string, error) {
	t.Helper()
	return ran(t, root(), append([]string{"--url", d.url, "--key", d.key}, args...)...)
}

// keyRef is where alice's key is, in a file of the test's own.
func keyRef(t *testing.T) string {
	return "file:" + keyFile(t)
}

// TestAnOperatorWorksFromOutsideTheBox: what an operator does to a workspace
// -- finds it, closes and restores it, names an owner, moves its address,
// writes its contract and a hold -- asked of the entry point on a connection
// of their key,
// from wherever they are: each says what it would do until it is told
// `--yes`.
func TestAnOperatorWorksFromOutsideTheBox(t *testing.T) {
	x := require.New(t)
	// In a zone whose date is not UTC's now, as Seoul's is from midnight to
	// nine: the days the commands say are UTC's, whatever the machine's.
	inZone(t, awayFromUTC(time.Now()))
	d := deploy(t)
	ctx := context.Background()
	month := func(from time.Time) (time.Time, time.Time) {
		return from.Add(30 * 24 * time.Hour), time.Now().Add(30 * 24 * time.Hour)
	}
	must := func(args ...string) string {
		t.Helper()
		out, err := d.loft(t, args...)
		x.NoError(err, "%s: %s", strings.Join(args, " "), out)
		return out
	}

	out := must("tenant", "ls")
	x.Contains(out, "@acme\t")
	x.Contains(out, "\topen")
	x.Contains(must("workspace", "ls", "--q", "ACM"), "@acme", "found by a piece of its address, as a workspace")
	x.NotContains(must("ws", "ls", "--q", "nothing-like-it"), "@acme")

	out = must("tenant", "show", "@acme")
	x.Contains(out, "2 people")
	x.Contains(out, "owners owner")
	x.Contains(out, "member\t@kim\tmember")

	began := time.Now()
	out = must("tenant", "close", "--reason", "asked", "acme")
	x.Contains(out, "would close @acme")
	x.Contains(out, "`--yes` does it")
	from, to := month(began)
	x.True(saysDay(out, "destroyed after ", from, to), "not UTC's day, a month from now: %s", out)
	x.NotContains(must("tenant", "ls", "--closed"), "@acme", "nothing was done")
	began = time.Now()
	out = must("tenant", "close", "--reason", "asked", "--yes", "acme")
	x.Contains(out, "closed")
	x.Contains(out, "by operator")
	from, to = month(began)
	x.True(saysDay(out, "destroyed ", from, to), "not UTC's day, a month from now: %s", out)
	x.Contains(must("tenant", "ls", "--closed"), "@acme")
	out = must("tenant", "close", "--yes", "acme")
	x.Contains(out, "closed already")
	x.True(saysDay(out, "since ", began, time.Now()), "not UTC's day: %s", out)
	x.True(saysDay(out, "destroyed after ", from, to), "not UTC's day, a month from closing: %s", out)
	x.Contains(must("tenant", "restore", "acme"), "would restore @acme")
	x.NotContains(must("tenant", "restore", "--yes", "acme"), "closed")
	x.NotContains(must("tenant", "ls", "--closed"), "@acme", "open again")

	x.Contains(must("tenant", "name-owner", "--member", "@kim", "acme"), "would make @kim")
	out = must("tenant", "name-owner", "--member", "@kim", "--yes", "acme")
	x.Contains(out, "owners kim,owner")
	_, err := d.loft(t, "tenant", "name-owner", "--member", "nobody", "--yes", "acme")
	x.ErrorContains(err, "no member")

	out = must("tenant", "move", "--to", "acme-two", "acme")
	x.Contains(out, "would move @acme to @acme-two")
	held := d.c.App.Workspaces.Hold()
	x.True(saysDay(out, "holding @acme until ", time.Now().Add(held-time.Minute), time.Now().Add(held)), "not UTC's day: %s", out)
	x.Contains(must("tenant", "ls"), "@acme\t", "nothing was done")
	out = must("tenant", "move", "--to", "acme-two", "--yes", "acme")
	x.Contains(out, "@acme-two\t")
	tn, err := d.s.Ent.Tenant.Query().Where(tenant.Alias("acme-two")).Only(ctx)
	x.NoError(err)
	_, err = d.loft(t, "tenant", "move", "--to", "acme", "--yes", "acme-two")
	x.ErrorContains(err, "held", "the old address is held, and not taken back by accident")

	x.Contains(must("tenant-contract", "add", "--name", "pilot", "acme-two"), "would write a contract")
	out = must("tenant-contract", "add", "--name", "pilot", "--yes", "acme-two")
	x.Contains(out, "contract\t")
	x.Contains(out, "pilot\tthen retire")

	x.Contains(must("legal-hold", "add", "--why", "a case", "acme-two"), "would keep everything")
	out = must("legal-hold", "add", "--why", "a case", "--yes", "acme-two")
	x.Contains(out, "a case\theld")
	i := strings.Index(out, "hold\t")
	x.Positive(i)
	id := strings.Fields(out[i:])[1]
	x.Contains(must("legal-hold", "patch", "--lift", id), "would lift hold")
	x.Contains(must("legal-hold", "patch", "--lift", "--yes", id), "a case\tlifted")

	// What the history says of the owner named, and who did it, is the
	// deployment's work at the operator's word: their identifier, and no
	// name of theirs or of the member's.
	owners, err := d.s.Ent.Holder.Query().Where(holder.TenantId(tn.Id), holder.Role("owner")).Count(ctx)
	x.NoError(err)
	x.Equal(2, owners)
	kim, err := d.s.Ent.Holder.Query().Where(holder.TenantId(tn.Id), holder.Alias("kim")).Only(ctx)
	x.NoError(err)
	ev, err := d.s.Ent.Event.Query().Where(event.TenantId(tn.Id), event.Kind("party.role")).Only(ctx)
	x.NoError(err)
	x.NotNil(ev.ActorId)
	x.Equal(operator.Id("alice").Uuid(), *ev.ActorId, "alice did it")
	x.NotNil(ev.Value)
	said := &app.PartyRoleSet{}
	x.NoError(ev.Value.UnmarshalTo(said))
	x.Equal("operator", said.GetBy())
	x.Equal("owner", said.GetRole())
	x.Equal(pdid.Id(kim.Id).Bytes(), said.GetHolder())
	x.NotContains(protojson.Format(ev.Value), kim.Name)
	x.NotContains(protojson.Format(ev.Value), "alice")
}

// TestAnOperatorWritesEveryTermOfAContract: `tenant-contract add` at the
// entry point writes every term a contract has -- whether a destroyed
// workspace's trail goes whole (payday#115), and from when -- and `tenant
// show` says each, beside how long the workspace keeps its history by its own
// settings (D36), which an operator reads and does not write.
func TestAnOperatorWritesEveryTermOfAContract(t *testing.T) {
	x := require.New(t)
	d := deploy(t)
	ctx := context.Background()

	tn, err := d.s.Ent.Tenant.Query().Where(tenant.Alias("acme")).Only(ctx)
	x.NoError(err)
	_, err = d.s.Ent.Tenant.UpdateOneId(tn.Id).SetKeepDays(730).SetRestoreDays(90).Save(ctx)
	x.NoError(err)
	// An operator fourteen hours ahead of UTC writes it, and one twelve behind
	// reads it, for each of whom the first of January in UTC is another day:
	// each is said the day it was given as.
	was := time.Local
	t.Cleanup(func() { time.Local = was })
	time.Local = time.FixedZone("UTC+14", 14*60*60)

	out, err := d.loft(t, "tenant-contract", "add", "--name", "enterprise", "--desc", "agreement 2026-7", "--purge-floors",
		"--effective", "2026-01-01", "--yes", "acme")
	x.NoError(err, out)
	x.Contains(out, "enterprise\tthen purge\tagreement 2026-7")
	x.Contains(out, "settings\thistory 730d, access record the deployment's, restore 90d", "the workspace's own settings")
	x.Contains(out, "keeps\thistory 730d, access record forever, restore 90d once closed, then purge", "what they come to")

	v, err := d.s.Ent.TenantContract.Query().Where(tenantcontract.TenantIdEQ(tn.Id)).Only(ctx)
	x.NoError(err)
	x.Equal("enterprise", v.Name)
	x.Equal("agreement 2026-7", v.Desc)
	x.True(v.PurgeFloors)
	x.Equal("2026-01-01", v.DateEffective.UTC().Format(time.DateOnly), "the day it was given as")
	x.Contains(out, "contract\t2026-01-01\tenterprise", "said as the day it was given as, whatever the operator's zone")
	time.Local = time.FixedZone("UTC-12", -12*60*60)
	shown, err := d.loft(t, "tenant", "show", "acme")
	x.NoError(err, shown)
	x.Contains(shown, "contract\t2026-01-01\tenterprise", "read as another day, by an operator in another zone")

	// What its settings, contracts and holds come to.
	x.Contains(shown, "contract\t2026-01-01\tenterprise\tthen purge\tagreement 2026-7")
	x.Contains(shown, "keeps\thistory 730d, access record forever, restore 90d once closed, then purge")

	// One that says nothing more retires.
	out, err = d.loft(t, "tenant-contract", "add", "--name", "standard", "--yes", "acme")
	x.NoError(err, out)
	x.Contains(out, "standard\tthen retire")

	_, err = d.loft(t, "tenant-contract", "add", "--name", "pro", "--keep", "60", "--yes", "acme")
	x.Error(err, "a workspace's own window, written by an operator")
}

// TestAnOperatorsCommandWantsAnOperatorsConnection: the entry point refuses
// a key nobody holds; and an operator's command on a person's connection asks
// nobody, and says which connection it wants.
func TestAnOperatorsCommandWantsAnOperatorsConnection(t *testing.T) {
	x := require.New(t)
	s := serve(t)
	ep := s.entryPoint()

	nobodys := filepath.Join(t.TempDir(), "nobody.key")
	x.NoError(os.WriteFile(nobodys, []byte("somebody-elses-key-of-a-good-length-too"), 0o600))
	_, err := ran(t, root(), "--url", ep, "--key", "file:"+nobodys, "tenant", "ls")
	x.ErrorContains(err, "who is asking")

	s.signIn("owner", "owner1234")
	for _, args := range [][]string{
		{"tenant", "show", "acme"},
		{"tenant", "move", "--to", "elsewhere", "acme"},
		{"trail", "verify"},
		{"search", "status"},
		{"tenant-contract", "add", "--name", "x", "acme"},
		{"holder", "password", "--login", "kim"},
	} {
		_, err := ran(t, root(), args...)
		x.Equal(codes.PermissionDenied, status.Code(err), "%v", args)
		x.ErrorContains(err, "is the operator's, and this is a person's connection: use an operator's", "%v", args)
	}
}
