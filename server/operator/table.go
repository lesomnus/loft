package operator

import (
	"slices"
	"strings"

	"google.golang.org/protobuf/reflect/protoreflect"

	app "github.com/lesomnus/loft"
)

// What an operator may ask the entry point is here, beside who an operator
// is, and not in `server/admin`: the terminal reads it too (design 9.16), to
// say which connection a command wants, and `loft` must not link the entry
// point. It is the methods' names and what each is, and nothing that serves
// them.

// Kind is what a method of the entry point does with what it reads.
type Kind int

const (
	// Read changes nothing.
	Read Kind = iota + 1
	// Write writes, as the deployment's own work at the operator's word.
	Write
	// Download hands people's data out to be kept somewhere else: it says
	// what it is for (`payday-reason-bin`), and its row of the access record
	// is written before its answer leaves.
	Download
)

// Table is every method the entry point serves an operator, by the name gRPC
// knows it by, and what it is (D74). What is not in it is refused before
// anything is read, whatever is registered: a method added to a service the
// entry point serves is closed here until it is added. There are no roles
// among operators -- whoever holds a key holds the table.
//
// An entity's own RPC is here where it is what an operator does, served as the
// operator: the same request, and the deployment's work in the workspace it
// names. The rest are the operator's own services, which no other server has.
var Table = map[string]Kind{
	// Finding a workspace, of any; and closing and opening it, which is the
	// operator's alone (D83) -- at its owner's request, or for what it was
	// closed for -- and no role's in it.
	app.TenantService_Get_FullMethodName:     Read,
	app.TenantService_List_FullMethodName:    Read,
	app.TenantService_Close_FullMethodName:   Write,
	app.TenantService_Restore_FullMethodName: Write,
	// And everything it has, out: for its owner, or one that is leaving
	// (design 8.2).
	app.TenantService_Export_FullMethodName: Download,

	// What the deployment's agreement with a workspace says, and its legal
	// holds: the operator's, and nobody's in the workspace.
	app.TenantContractService_Add_FullMethodName:  Write,
	app.TenantContractService_Get_FullMethodName:  Read,
	app.TenantContractService_List_FullMethodName: Read,
	app.LegalHoldService_Add_FullMethodName:       Write,
	app.LegalHoldService_Patch_FullMethodName:     Write,
	app.LegalHoldService_Get_FullMethodName:       Read,
	app.LegalHoldService_List_FullMethodName:      Read,

	// Who is in a workspace.
	app.HolderService_Get_FullMethodName:  Read,
	app.HolderService_List_FullMethodName: Read,

	app.OperatorTenantService_Show_FullMethodName: Read,
	// A workspace's end by the operator's hand, and its return from an
	// export (design 8.2, D34).
	app.OperatorTenantService_Retired_FullMethodName: Read,
	app.OperatorTenantService_Retire_FullMethodName:  Write,
	app.OperatorTenantService_Purge_FullMethodName:   Write,
	app.OperatorTenantService_Import_FullMethodName:  Write,

	// What an export and the trail's bundle are held to, where they are.
	app.OperatorTrailService_Keys_FullMethodName: Read,
	// The trail's archive and the live rows checked against it, what an
	// operator who looked accepts, and what its erasures owe.
	app.OperatorTrailService_Verify_FullMethodName:     Read,
	app.OperatorTrailService_Accept_FullMethodName:     Write,
	app.OperatorTrailService_Rows_FullMethodName:       Read,
	app.OperatorTrailService_AcceptRows_FullMethodName: Write,
	app.OperatorTrailService_Owed_FullMethodName:       Read,
	// A workspace's trail, out, for a demand to preserve it (loft#153).
	app.OperatorTrailService_Export_FullMethodName: Download,

	// The search index, for a deployment that names a cluster (D31).
	app.OperatorSearchService_Status_FullMethodName:  Read,
	app.OperatorSearchService_Reindex_FullMethodName: Write,
	app.OperatorSearchService_Remove_FullMethodName:  Write,
	app.OperatorSearchService_Analyze_FullMethodName: Read,

	// What the keep windows take out of the history (design 8.2, 8.3).
	app.OperatorRetentionService_Plan_FullMethodName: Read,
	app.OperatorRetentionService_Run_FullMethodName:  Write,

	// Who the members are, which is roster's (design 9.10), and a member on
	// the roster in this process.
	app.OperatorIdentityService_Link_FullMethodName:      Write,
	app.OperatorIdentityService_Depart_FullMethodName:    Write,
	app.OperatorIdentityService_Reconcile_FullMethodName: Write,
	app.OperatorHolderService_Add_FullMethodName:         Write,
	app.OperatorHolderService_Password_FullMethodName:    Write,

	// An event's reason taken out, on the word of the workspace it is of,
	// for a person whose request to erase what it says of them points at it
	// (payday#151) -- which no role of a workspace is given yet -- and what
	// it would take, said first.
	app.EventService_Redact_FullMethodName:            Write,
	app.OperatorEventService_Redaction_FullMethodName: Read,

	// The access record of the whole deployment (loft#136), and the reviews
	// of it the workspaces' administrators recorded.
	app.OperatorAccessService_List_FullMethodName: Read,
	app.AccessReviewService_List_FullMethodName:   Read,

	// Who the key a call carries is, for the terminal (design 9.16).
	app.OperatorAuthService_Whoami_FullMethodName: Read,
}

// Downloads is the methods of [Table] that are downloads.
func Downloads() []string {
	out := []string{}
	for m, k := range Table {
		if k == Download {
			out = append(out, m)
		}
	}
	slices.Sort(out)
	return out
}

// Own is whether `method` -- "/loft.OperatorTrailService/Verify", as gRPC
// names it -- is of the operator's own services, which the entry point alone
// serves: every service `proto/loft/operator.proto` declares. The rest of the
// table is an entity's, which the public server serves too, to the people of
// a workspace.
func Own(method string) bool {
	service, _, ok := strings.Cut(strings.TrimPrefix(method, "/"), "/")
	if !ok {
		return false
	}
	f := app.File_loft_operator_proto
	name, ok := strings.CutPrefix(service, string(f.Package())+".")
	return ok && f.Services().ByName(protoreflect.Name(name)) != nil
}
