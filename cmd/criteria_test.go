package cmd_test

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
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
	"github.com/lesomnus/payday/pdid"
	"github.com/protobuf-orm/ent/dialect/sql/schema"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"
	"google.golang.org/protobuf/types/known/emptypb"

	app "github.com/lesomnus/loft"
	"github.com/lesomnus/loft/cli"
	"github.com/lesomnus/loft/cmd"
	"github.com/lesomnus/loft/internal/ent/migrate"
	"github.com/lesomnus/loft/server/clilogin"
	"github.com/lesomnus/loft/server/policy"
)

// refusedByTable says whether a call was refused by `server/policy` itself: a
// refusal that names the method, in its own words. A refusal the domain layer
// gives a call the gate let through -- somebody else's reservation, say --
// names none.
func refusedByTable(err error, method string) bool {
	s, ok := status.FromError(err)
	return ok && s.Code() == codes.PermissionDenied && strings.Contains(s.Message(), "("+method+")")
}

// TestEveryMethodIsAsTheRolesSayForEveryRole is the role × method table in
// whole (plan M2), with the roles where they are now (D89): every method the
// served stack has, called over the wire by somebody bound at roster to each
// role Loft ships, is let through exactly when that role holds it
// (`policy.Roles`, `server/policy/testdata/roles.txt`) -- and what no role
// holds is refused to everybody, owner included, before roster is asked.
func TestEveryMethodIsAsTheRolesSayForEveryRole(t *testing.T) {
	w := newWorld(t)
	x := w.x
	ctx := context.Background()

	roles := []string{"auditor", "member", "manager", "admin", "owner"}
	as := map[string]context.Context{"owner": w.owner}
	for _, r := range roles {
		if r != "owner" {
			as[r] = w.signIn(w.holder("as-"+r, r))
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
		// asked (loft#136): said here, so that what answers is the table.
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
			// One the server ended at once says io.EOF here, and why to the
			// receive below (grpc.ClientStream).
			if err = s.SendMsg(in.New().Interface()); errors.Is(err, io.EOF) {
				err = nil
			}
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
		role, ok := policy.RoleOf(r)
		x.True(ok, r)
		var wg sync.WaitGroup
		sem := make(chan struct{}, 16)
		for _, c := range calls {
			var want error
			if !slices.Contains(role.Methods, c.full) {
				want = errors.New("no role holds it")
			}
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
					problems = append(problems, fmt.Sprintf("%s %s: the role holds it, and the stack refused it: %v", r, c.full, err))
				case want == nil:
					opened[r]++
				case err == nil:
					problems = append(problems, fmt.Sprintf("%s %s: the role does not hold it, and the stack answered", r, c.full))
				case !refused && status.Code(err) != codes.Unimplemented:
					// Unimplemented is a general write payday closes before
					// the gate is asked; anything else got past the gate.
					problems = append(problems, fmt.Sprintf("%s %s: the role does not hold it, and the stack answered %v", r, c.full, err))
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
		"session.secret":      "an HMAC, under a key the database does not hold, of the digest of a session key this deployment made for a browser, or a terminal made and sent the digest of",
		"session.held":        "the ID token an issuer handed over, given back to it at signing out",
		"clilogin.secret":     "the session key a terminal made up and sent the digest of, kept as a session's is",
		"archived.digest":     "the checksum of a chunk of the trail's archive",
		"audit.digest":        "a keyed digest of the row a write left, which says whether a live row is still that row (payday#102)",
		"audit.digest_fields": "the numbers of the fields that digest covers",
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
// page approves it -- and the requests that carry a password set one: for a
// session that is there already, or at the operator's entry point, which
// this server does not serve, for an operator's key.
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
	n, body := post("/cli/logins", `{"secret":"`+secret+`","client":"loft-cli","login":"park","password":"correct horse"}`)
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
	login := cli.NewCmdAuth().Commands.Get("login")
	x.NotNil(login)
	for _, f := range login.Flags {
		x.NotContains(strings.ToLower(f.Info().Name), "pass", "`loft auth login` takes a password")
	}

	// What carries a password, of every call this app serves: none -- who
	// signs in and how is roster's (D87). The operator's, at the entry point
	// alone (`server/admin`, design 9.15): somebody made, or given a
	// password, on the roster in this process, and the members a restore
	// brought back made there -- each setting a password, for an operator's
	// key, and none of them served here.
	carries := map[string]bool{}
	protoregistry.GlobalFiles.RangeFiles(func(fd protoreflect.FileDescriptor) bool {
		if fd.Package() != "loft" {
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
	x.Equal(map[string]bool{
		"loft.OperatorHolderService.Add":      true,
		"loft.OperatorHolderService.Password": true,
		"loft.OperatorIdentityService.Link":   true,
	}, carries)
	for m := range carries {
		if !strings.HasPrefix(m, "loft.Operator") {
			continue
		}
		i := strings.LastIndex(m, ".")
		err := w.conn.Invoke(ctx, "/"+m[:i]+"/"+m[i+1:], &emptypb.Empty{}, &emptypb.Empty{})
		x.Equal(codes.Unimplemented, status.Code(err), "%s is served here: %v", m, err)
	}
}
