package cmd_test

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"slices"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/lesomnus/payday/access"
	"github.com/lesomnus/payday/gate"
	"github.com/lesomnus/payday/pdid"
	"github.com/lesomnus/payday/pdpb"
	"github.com/lesomnus/z"
	"github.com/protobuf-orm/ent/dialect/sql/schema"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"
	"google.golang.org/protobuf/types/known/anypb"

	app "github.com/lesomnus/rove"
	"github.com/lesomnus/rove/cli"
	"github.com/lesomnus/rove/cmd"
	"github.com/lesomnus/rove/internal/ent/holder"
	"github.com/lesomnus/rove/internal/ent/migrate"
	"github.com/lesomnus/rove/server/clilogin"
	"github.com/lesomnus/rove/server/policy"
)

// refusedByTable says whether a call was refused by `server/policy` itself: a
// refusal that names the method, in the table's own words. A refusal the
// domain layer gives a call the table let through -- somebody else's
// reservation, say -- names none.
func refusedByTable(err error, method string) bool {
	s, ok := status.FromError(err)
	return ok && s.Code() == codes.PermissionDenied && strings.Contains(s.Message(), "("+method+")")
}

// TestEveryMethodIsAsTheTableSaysForEveryRole is the role × method table in
// whole (plan M2): every method the served stack has, called by every role
// over the wire, is let through exactly when `server/policy` says so -- and
// what the table does not name is refused to everybody, owner included.
func TestEveryMethodIsAsTheTableSaysForEveryRole(t *testing.T) {
	w := newWorld(t)
	x := w.x
	ctx := context.Background()

	// A workspace with a name, so that a close confirmed by nothing is not a
	// close: the calls below are made with empty requests.
	_, err := w.s.Base.Tenant().Patch(ctx, app.TenantPatchRequest_builder{
		Ref:              app.TenantRef_builder{Id: w.tenant.GetId()}.Build(),
		Name:             z.Ptr("Acme"),
		DateUpdatedForce: z.Ptr(true),
	}.Build())
	x.NoError(err)

	roles := []string{"auditor", "member", "manager", "admin", "owner"}
	as := map[string]context.Context{"owner": w.owner}
	rows := map[string]*app.Holder{}
	for _, r := range roles {
		h := w.holder("as-"+r, r)
		rows[r] = h
		if r != "owner" {
			as[r] = w.signIn(h)
		}
	}

	g, err := w.s.Grpc(ctx, w.c)
	x.NoError(err)
	type call struct {
		full   string
		md     protoreflect.MethodDescriptor
		stream bool
	}
	calls := []call{}
	for svc, info := range g.GetServiceInfo() {
		if strings.HasPrefix(svc, "grpc.") {
			continue
		}
		d, err := protoregistry.GlobalFiles.FindDescriptorByName(protoreflect.FullName(svc))
		x.NoError(err, svc)
		for _, m := range info.Methods {
			md := d.(protoreflect.ServiceDescriptor).Methods().ByName(protoreflect.Name(m.Name))
			x.NotNil(md, "%s/%s", svc, m.Name)
			calls = append(calls, call{full: "/" + svc + "/" + m.Name, md: md, stream: m.IsServerStream})
		}
	}
	sort.Slice(calls, func(i, j int) bool { return calls[i].full < calls[j].full })
	x.Greater(len(calls), 200, "every method the stack serves")

	ask := func(ctx context.Context, c call) error {
		// A download says what it is for, or is refused before the table is
		// asked (rove#136): said here, so that what answers is the table.
		if slices.Contains(cmd.Downloads, c.full) {
			ctx = metadata.AppendToOutgoingContext(ctx, access.ReasonKey, "criteria")
		}
		in, err := protoregistry.GlobalTypes.FindMessageByName(c.md.Input().FullName())
		if err != nil {
			return err
		}
		out, err := protoregistry.GlobalTypes.FindMessageByName(c.md.Output().FullName())
		if err != nil {
			return err
		}
		if !c.stream {
			return w.conn.Invoke(ctx, c.full, in.New().Interface(), out.New().Interface())
		}
		sctx, cancel := context.WithTimeout(ctx, 400*time.Millisecond)
		defer cancel()
		s, err := w.conn.NewStream(sctx, &grpc.StreamDesc{ServerStreams: true}, c.full)
		if err == nil {
			err = s.SendMsg(in.New().Interface())
		}
		if err == nil {
			err = s.CloseSend()
		}
		if err == nil {
			err = s.RecvMsg(out.New().Interface())
		}
		if status.Code(err) == codes.DeadlineExceeded {
			// Opened, and nothing to say yet: let through.
			return nil
		}
		return err
	}

	var (
		mu       sync.Mutex
		problems []string
		opened   = map[string]int{}
	)
	for _, r := range roles {
		var wg sync.WaitGroup
		sem := make(chan struct{}, 16)
		for _, c := range calls {
			want := policy.Policy{}.May(ctx, gate.Call{Action: c.full, Row: rows[r]})
			wg.Add(1)
			sem <- struct{}{}
			go func() {
				defer wg.Done()
				defer func() { <-sem }()
				err := ask(as[r], c)
				refused := refusedByTable(err, c.full)
				mu.Lock()
				defer mu.Unlock()
				switch {
				case want == nil && refused:
					problems = append(problems, fmt.Sprintf("%s %s: the table lets it through, and the stack refused it: %v", r, c.full, err))
				case want == nil:
					opened[r]++
				case err == nil:
					problems = append(problems, fmt.Sprintf("%s %s: the table refuses it, and the stack answered", r, c.full))
				case !refused && status.Code(err) != codes.Unimplemented:
					// Unimplemented is a general write payday closes before
					// any table is asked; anything else got past the table.
					problems = append(problems, fmt.Sprintf("%s %s: the table refuses it, and the stack answered %v", r, c.full, err))
				}
			}()
		}
		wg.Wait()
	}
	sort.Strings(problems)
	x.Empty(problems)
	t.Logf("let through, by role: %v (of %d methods)", opened, len(calls))
	x.Less(opened["auditor"], opened["admin"])
	x.Less(opened["member"], opened["manager"])
	x.Less(opened["manager"], opened["admin"])
	x.Less(opened["admin"], opened["owner"])

	// Nobody signed in is nobody: every method, refused before any table.
	for _, c := range calls {
		err := ask(context.Background(), c)
		x.True(slices.Contains([]codes.Code{codes.Unauthenticated, codes.Unimplemented}, status.Code(err)), "%s answered nobody: %v", c.full, err)
	}
}

// TestAMemberSlipsNothingPastTheTableInABatch: a batch is held to the table one
// operation at a time, and is one transaction -- so an operation a member may
// not call refuses the whole of it, and what came before it in the batch is
// not written either.
func TestAMemberSlipsNothingPastTheTableInABatch(t *testing.T) {
	w := newWorld(t)
	x := w.x
	ctx := context.Background()
	kim := w.holder("kim", "member")
	member := w.signIn(kim)
	batch := pdpb.NewBatchServiceClient(w.conn)
	op := func(method string, req interface {
		ProtoReflect() protoreflect.Message
	}) *pdpb.Op {
		v, err := anypb.New(req)
		x.NoError(err)
		return pdpb.Op_builder{Method: method, Request: v}.Build()
	}
	mailOff := func() bool {
		id, err := pdid.From(kim.GetId())
		x.NoError(err)
		h, err := w.s.Ent.Holder.Get(ctx, id.Uuid())
		x.NoError(err)
		return h.MailOff
	}
	kimRef := app.HolderRef_builder{Id: kim.GetId()}.Build()

	// What a member may do, alone in a batch, is done.
	_, err := batch.Do(member, pdpb.BatchRequest_builder{Ops: []*pdpb.Op{
		op("/rove.NotificationService/SetMail", app.NotificationSetMailRequest_builder{Off: true}.Build()),
	}}.Build())
	x.NoError(err, "a member's own operation, batched")
	x.True(mailOff())
	_, err = batch.Do(member, pdpb.BatchRequest_builder{Ops: []*pdpb.Op{
		op("/rove.NotificationService/SetMail", app.NotificationSetMailRequest_builder{Off: false}.Build()),
	}}.Build())
	x.NoError(err)
	x.False(mailOff())

	// The same beside an administrator's -- a type, a role of their own --
	// is nothing done.
	for _, slipped := range []*pdpb.Op{
		op("/rove.AssetTypeService/Add", app.AssetTypeAddRequest_builder{Name: "슬쩍", Kind: "item"}.Build()),
		op("/rove.PartyService/SetRole", app.PartySetRoleRequest_builder{Role: "owner"}.Build()),
		op("/rove.HolderService/Patch", app.HolderPatchRequest_builder{Ref: kimRef, Role: z.Ptr("owner")}.Build()),
	} {
		_, err := batch.Do(member, pdpb.BatchRequest_builder{Ops: []*pdpb.Op{
			op("/rove.NotificationService/SetMail", app.NotificationSetMailRequest_builder{Off: true}.Build()),
			slipped,
		}}.Build())
		// Refused by the table -- or, for a general write, closed before any
		// table is asked, which is refused all the same.
		x.Contains([]codes.Code{codes.PermissionDenied, codes.Unimplemented}, status.Code(err), "%s: %v", slipped.GetMethod(), err)
		x.False(mailOff(), "%s: the operation before it was written", slipped.GetMethod())
	}
	v, err := w.s.Ent.Holder.Query().Where(holder.Alias("kim")).Only(ctx)
	x.NoError(err)
	x.Equal("member", v.Role, "still a member")
	types, err := app.NewAssetTypeServiceClient(w.conn).List(w.owner, app.AssetTypeListRequest_builder{}.Build())
	x.NoError(err)
	for _, ty := range types.GetItems() {
		x.NotEqual("슬쩍", ty.GetName(), "a type a member slipped in")
	}
}

// secretsIn is what of a schema could hold a password or something a password
// is checked against: a table or column by such a name, less what is known to
// be something else.
func secretsIn(tables []*schema.Table, known map[string]string) []string {
	looks := regexp.MustCompile(`(?i)pass|pwd|hash|secret|salt|digest|cred|verif|otp|recovery|token|claim|nonce|pin$`)
	out := []string{}
	for _, tb := range tables {
		if looks.MatchString(tb.Name) {
			out = append(out, tb.Name)
		}
		for _, c := range tb.Columns {
			name := tb.Name + "." + c.Name
			if looks.MatchString(c.Name) {
				if _, ok := known[name]; !ok {
					out = append(out, name)
				}
			}
		}
	}
	sort.Strings(out)
	return out
}

// TestNoPasswordIsKeptHere: people's passwords are roster's (design 9.10), and
// this deployment's database holds none of them, nor anything one could be
// checked against. What it does keep that looks like one is said here, each
// with what it is.
func TestNoPasswordIsKeptHere(t *testing.T) {
	x := require.New(t)
	known := map[string]string{
		"session.secret":           "the digest of a session key this deployment made for a browser, or a terminal made and sent the digest of",
		"session.held":             "the ID token an issuer handed over, given back to it at signing out",
		"clilogin.secret":          "the digest of the session key a terminal made up, which it sent as a digest",
		"invitation.claim":         "the digest of the code an invitation's mail carries, which proves the mailbox and signs nobody in",
		"tenantdomain.token":       "the value of a DNS record that proves a label domain",
		"tenantdomain.verified_at": "when that record was seen",
		"archived.digest":          "the checksum of a chunk of the trail's archive",
		"audit.digest":             "a keyed digest of the row a write left, which says whether a live row is still that row (payday#102)",
		"audit.digest_fields":      "the numbers of the fields that digest covers",
	}
	x.Empty(secretsIn(migrate.Tables, known), "a column that could hold a password, or what a password is checked against")

	// Each of those is still a column, so the list says what is there.
	cols := map[string]bool{}
	for _, tb := range migrate.Tables {
		for _, c := range tb.Columns {
			cols[tb.Name+"."+c.Name] = true
		}
	}
	for name := range known {
		x.True(cols[name], "%s is said to be here and is not", name)
	}

	// And the check finds one when there is one.
	x.Equal([]string{"credential", "credential.password_hash", "holder.recovery_codes"}, secretsIn([]*schema.Table{
		{Name: "credential", Columns: []*schema.Column{{Name: "password_hash"}}},
		{Name: "holder", Columns: []*schema.Column{{Name: "alias"}, {Name: "recovery_codes"}}},
	}, known))
}

// TestNoOtherDoorTakesAPassword is the rest of `auth.sso_only` (D28): beside
// `POST /session`, which TestAPasswordIsNoWayInWhereTheIssuerIsTheOnlyOne
// refuses, nothing here signs anybody in on a password -- not a header on a
// call, not a terminal, which is a session only once somebody signed in at the
// page approves it -- and the one request that carries a password sets one,
// for a session that is there already.
func TestNoOtherDoorTakesAPassword(t *testing.T) {
	w := newSsoWorld(t, func(c *cmd.Config) { c.Auth.SsoOnly = true })
	x := w.x
	ctx := context.Background()
	park, err := w.s.Identity.AddPerson(ctx, "park", "박", pdid.Nil)
	x.NoError(err)
	x.NoError(w.s.Identity.SetPassword(ctx, park.Id, "correct horse"))
	w.here(park, "member")

	for _, h := range []string{
		"Basic " + base64.StdEncoding.EncodeToString([]byte("park:correct horse")),
		"Bearer correct horse",
	} {
		md := metadata.AppendToOutgoingContext(ctx, "authorization", h)
		_, err := app.NewPartyServiceClient(w.conn).Me(md, &app.PartyMeRequest{})
		x.Equal(codes.Unauthenticated, status.Code(err), "a password in a header: %s", h)
	}

	// A terminal asks with a key it made up -- a password beside it is
	// nothing -- and its key is no session until the page approves it.
	raw := make([]byte, 32)
	_, err = rand.Read(raw)
	x.NoError(err)
	key := base64.RawURLEncoding.EncodeToString(raw)
	sum := sha256.Sum256([]byte(key))
	secret := base64.RawURLEncoding.EncodeToString(sum[:])
	post := func(path, body string) (int, string) {
		res, err := w.browser().Post(w.app.URL+path, "application/json", strings.NewReader(body))
		x.NoError(err)
		defer res.Body.Close()
		b, _ := io.ReadAll(res.Body)
		return res.StatusCode, string(b)
	}
	n, body := post("/cli/logins", `{"secret":"`+secret+`","client":"rove-cli","login":"park","password":"correct horse"}`)
	x.Equal(http.StatusCreated, n, body)
	var started clilogin.Started
	x.NoError(json.Unmarshal([]byte(body), &started))
	n, _ = post("/cli/logins/approve", `{"code":"`+started.Code+`","login":"park","password":"correct horse"}`)
	x.Equal(http.StatusUnauthorized, n, "approved by a password rather than a page's session")
	n, body = post("/cli/logins/"+started.Id+"/poll", `{"secret":"`+secret+`"}`)
	x.Equal(http.StatusOK, n)
	x.JSONEq(`{"state":"pending"}`, body)
	as := metadata.AppendToOutgoingContext(ctx, "cookie", started.Cookie+"="+key)
	_, err = app.NewPartyServiceClient(w.conn).Me(as, &app.PartyMeRequest{})
	x.Equal(codes.Unauthenticated, status.Code(err), "a terminal nobody approved")
	for _, f := range cli.NewCmdLogin().Flags {
		x.NotContains(strings.ToLower(f.Info().Name), "pass", "`rove login` takes a password")
	}

	// What carries a password, of every call this app serves: setting one's
	// own or, for an administrator, somebody's, and the login an
	// administrator issues on the roster in this process -- each setting a
	// password, none signing anybody in, and each a call made with a session
	// there is already.
	carries := map[string]bool{}
	protoregistry.GlobalFiles.RangeFiles(func(fd protoreflect.FileDescriptor) bool {
		if fd.Package() != "rove" {
			return true
		}
		for i := range fd.Services().Len() {
			ms := fd.Services().Get(i).Methods()
			for j := range ms.Len() {
				fs := ms.Get(j).Input().Fields()
				for k := range fs.Len() {
					if strings.Contains(strings.ToLower(string(fs.Get(k).Name())), "pass") {
						carries[string(ms.Get(j).FullName())] = true
					}
				}
			}
		}
		return true
	})
	x.Equal(map[string]bool{"rove.PartyService.SetPassword": true, "rove.PartyService.Invite": true}, carries)
	_, err = app.NewPartyServiceClient(w.conn).SetPassword(ctx, app.PartySetPasswordRequest_builder{Password: "correct horse"}.Build())
	x.Equal(codes.Unauthenticated, status.Code(err), "a password set by nobody")
}
