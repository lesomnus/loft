package policy

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"testing"

	"github.com/lesomnus/payday/gate"
	"github.com/lesomnus/payday/pdid"
	"github.com/lesomnus/payday/pdpb"
	"github.com/lesomnus/roster/reach"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"

	app "github.com/lesomnus/loft"
)

var update = flag.Bool("update", false, "write testdata/roles.txt from the roles as they are")

// TestTheRolesAreWhatTheyWereReviewedAs is the roles Loft ships written out
// (`testdata/roles.txt`, #327's decision 9): every method somebody may be
// given, and which of the five roles hold it at first -- or `closed`, kept
// from everybody. A change to what a role starts with is a change to that
// file, which a review reads line by line; `go test ./server/policy -update`
// writes it again. That a role bound at roster lets through exactly what it
// holds, over the wire, is `cmd`'s TestEveryMethodIsAsTheRolesSayForEveryRole.
func TestTheRolesAreWhatTheyWereReviewedAs(t *testing.T) {
	lines := []string{}
	for m, r := range first {
		holders := []string{}
		for _, role := range Roles {
			if slices.Contains(role.Methods, m) {
				holders = append(holders, strings.TrimPrefix(role.Alias, "loft-"))
			}
		}
		if r.min == nobody {
			holders = []string{"closed"}
		}
		lines = append(lines, fmt.Sprintf("%s\t%s", m, strings.Join(holders, " ")))
	}
	sort.Strings(lines)
	got := strings.Join(lines, "\n") + "\n"

	path := filepath.Join("testdata", "roles.txt")
	if *update {
		require.NoError(t, os.WriteFile(path, []byte(got), 0o644))
		return
	}
	want, err := os.ReadFile(path)
	require.NoError(t, err, "go test ./server/policy -update")
	require.Equal(t, string(want), got, "the roles moved; read the diff, then -update")
}

// TestEachRoleHoldsWhatItIsFor: the five in order, each holding all a narrower
// one does -- but the auditor, who reads what an administrator reads and
// writes nothing but that they reviewed who read what -- and the owner every
// method that may be given at all.
func TestEachRoleHoldsWhatItIsFor(t *testing.T) {
	x := require.New(t)
	aliases := []string{}
	for _, r := range Roles {
		aliases = append(aliases, r.Alias)
		x.NotEmpty(r.Name, r.Alias)
		x.True(slices.IsSorted(r.Methods), r.Alias)
	}
	x.Equal([]string{"loft-owner", "loft-admin", "loft-manager", "loft-member", "loft-auditor"}, aliases)

	owner, _ := RoleOf("owner")
	x.Equal(Methods(), owner.Methods, "the owner holds everything that may be given")
	for i := 1; i < 4; i++ {
		wide, narrow := Roles[i-1], Roles[i]
		for _, m := range narrow.Methods {
			x.Contains(wide.Methods, m, "%s holds what %s does", wide.Alias, narrow.Alias)
		}
		x.Greater(len(wide.Methods), len(narrow.Methods), wide.Alias)
	}

	auditor, _ := RoleOf("loft-auditor")
	member, _ := RoleOf("member")
	admin, _ := RoleOf("admin")
	for _, m := range auditor.Methods {
		x.Contains(admin.Methods, m, "an auditor reads nothing an administrator does not")
		if m == app.AccessReviewService_Add_FullMethodName {
			// That they reviewed the access record, which is what they are
			// for (loft#136): the one row an auditor writes.
			continue
		}
		x.False(strings.Contains(m, "/Add") || strings.Contains(m, "/Erase") || strings.Contains(m, "/Update"), "an auditor changes nothing: %s", m)
	}
	x.Contains(auditor.Methods, app.PermissionService_ReadEveryReservation_FullMethodName, "D45")
	x.NotContains(member.Methods, app.PermissionService_BookForOthers_FullMethodName)

	_, ok := RoleOf("custodian")
	x.False(ok)
}

// TestEveryPermissionIsGivenToSomebody: a method of `PermissionService` no
// role ships with would be a permission nobody can be given but by a pattern
// -- and every one the schema declares is in the roles, as an RPC is.
func TestEveryPermissionIsGivenToSomebody(t *testing.T) {
	x := require.New(t)
	sd := app.File_loft_permission_proto.Services().ByName("PermissionService")
	x.NotNil(sd)
	for i := range sd.Methods().Len() {
		m := "/loft.PermissionService/" + string(sd.Methods().Get(i).Name())
		x.True(Grantable(m), m)
	}
	x.GreaterOrEqual(sd.Methods().Len(), 7)
}

// asked is roster, answering from a list of what each holder may call, and
// counting what it was asked.
type asked struct {
	may   map[pdid.Id][]string
	err   error
	calls int
}

func (a *asked) May(_ context.Context, holder pdid.Id, method string) error {
	a.calls++
	if a.err != nil {
		return a.err
	}
	if slices.Contains(a.may[holder], method) || slices.Contains(a.may[holder], "/loft.*/*") {
		return nil
	}
	return fmt.Errorf("%w: %s", reach.ErrDenied, method)
}

// TestTheGateAsksRosterAboutThePerson: what roster says of the caller is what
// is served -- and what no role Loft ships names is refused before roster is
// asked, whatever a role there holds: `/loft.*/*`, which the tenant's
// administrators hold, opens no row only the domain layer writes.
func TestTheGateAsksRosterAboutThePerson(t *testing.T) {
	x := require.New(t)
	ctx := context.Background()
	kim, lee, admin := pdid.New(1), pdid.New(1), pdid.New(1)
	roster := &asked{may: map[pdid.Id][]string{
		kim:   {"/loft.AssetService/Get"},
		admin: {"/loft.*/*"},
	}}
	p := Policy{Reach: roster}
	call := func(who pdid.Id, method string) error {
		return p.May(ctx, gate.Call{Actor: who, Action: method})
	}

	x.NoError(call(kim, "/loft.AssetService/Get"))
	err := call(lee, "/loft.AssetService/Get")
	x.Equal(codes.PermissionDenied, status.Code(err))
	x.Contains(status.Convert(err).Message(), "(/loft.AssetService/Get)", "the refusal names the method")
	x.NoError(call(admin, "/loft.AssetService/Add"))

	for _, sealed := range []string{
		"/loft.PlacementService/Add",           // a time row only the domain layer writes
		"/loft.EventService/Add",               // the past's
		"/loft.AssetService/Patch",             // a write the domain completes otherwise
		app.EventService_Redact_FullMethodName, // closed: nobody, until somebody decides
		"/loft.NoSuchService/Get",
	} {
		before := roster.calls
		err := call(admin, sealed)
		x.Equal(codes.PermissionDenied, status.Code(err), sealed)
		x.Equal(before, roster.calls, "%s was refused before roster was asked", sealed)
	}
	x.True(Closed(app.EventService_Redact_FullMethodName))
	x.False(Grantable(app.EventService_Redact_FullMethodName))

	x.NoError(call(kim, "/grpc.health.v1.Health/Check"), "health and reflection")

	roster.err = fmt.Errorf("%w: down", reach.ErrUnreachable)
	err = call(kim, "/loft.AssetService/Get")
	x.Equal(codes.Unavailable, status.Code(err), "a roster that cannot be asked is a refusal, and no doing of the caller's")
	roster.err = context.Canceled
	x.Equal(codes.Canceled, status.Code(call(kim, "/loft.AssetService/Get")))
	x.True(errors.Is(Said(nil, "m"), nil))
}

// TestNothingBeyondTheWallMayBeGiven: an entity that says `global: {}` has no
// wall in front of it -- its table is every tenant's rows at once -- so a
// method of its service given to anybody would hand all of them to whoever
// holds it. None may be: what those entities are for, the deployment does
// through the ungated server (payday's permissions guide, §13: "grep for it
// and read the list"). Read off the schema rather than a list here, so that a
// global entity added later is held to it too.
func TestNothingBeyondTheWallMayBeGiven(t *testing.T) {
	global := []string{}
	protoregistry.GlobalFiles.RangeFiles(func(fd protoreflect.FileDescriptor) bool {
		if fd.Package() != "loft" {
			return true
		}
		for i := 0; i < fd.Messages().Len(); i++ {
			md := fd.Messages().Get(i)
			if e, _ := proto.GetExtension(md.Options(), pdpb.E_Entity).(*pdpb.Entity); e.GetGlobal() != nil {
				global = append(global, "/"+string(md.FullName())+"Service/")
			}
		}
		return true
	})
	sort.Strings(global)
	// Sessions, terminal sign-ins, sign-in failures, the search index's
	// queue, what levyan is to be told, and payday's outbox and archive
	// manifest: if this
	// is shorter, the registry was read before the schema was linked, and the
	// test says nothing.
	require.GreaterOrEqual(t, len(global), 7, "the global entities: %v", global)

	for m := range first {
		for _, g := range global {
			require.False(t, strings.HasPrefix(m, g), "%s is a global entity's, which the wall does not narrow", m)
		}
	}
}
