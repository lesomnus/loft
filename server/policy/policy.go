// Package policy says what each role may call (design 9.3).
//
// It is a table, and what is not in it is refused. A method added to the schema
// is closed until somebody decides who may call it, which is the direction a
// mistake should fall in.
//
// It is also what seals the rows only the domain layer writes. The history
// rows, the events, the allocations and the rest have generated Add, Patch and
// Erase like everything else, and nothing here gives them to anybody: the
// domain layer writes them from behind the gate, where a policy is not asked,
// and a caller asking for them directly is refused before anything is read.
// The same goes for the generated Patch of everything the domain completes --
// an asset changes through Move and SetAttributes, so that the history is
// written with it, and never by a Patch that would leave the history behind.
//
// A policy is asked once per call, about the method the caller named, and a
// batch asks it once per operation. What the domain layer then calls on the
// caller's behalf is not asked about again; that is the point of the layer.
package policy

import (
	"context"
	"strings"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/lesomnus/payday/frame"
	"github.com/lesomnus/payday/gate"

	app "github.com/lesomnus/rove"
)

// Level is how much a role may do, in order.
type Level int

const (
	// Nobody is a method no role is given.
	Nobody Level = iota
	// Account is anybody signed in, as their account and not as a membership:
	// the calls an account makes before it is anybody's member, which
	// `cmd.AccountMethods` lists and the domain layer holds to its own rows.
	Account
	// Read is everybody signed in, auditors included.
	Read
	// Member is everybody who works here: reserve, report, take supplies.
	Member
	// Manager runs the register: assets, hand-overs, counts, stock, work.
	Manager
	// Admin runs the tenant: people, roles, types, domains.
	Admin
	// Owner is the tenant's.
	Owner
)

// Levels is what each role is.
var Levels = map[string]Level{
	"auditor": Read,
	"member":  Member,
	"manager": Manager,
	"admin":   Admin,
	"owner":   Owner,
}

type rule struct {
	min Level
	// auditor lets an auditor in below `min`: the reads an auditor exists for,
	// such as the trail, which are an admin's otherwise.
	auditor bool
}

// Table is every method somebody may call, by the name gRPC knows it by.
var Table = map[string]rule{}

func allow(min Level, service string, methods ...string) {
	for _, m := range methods {
		Table["/rove."+service+"/"+m] = rule{min: min}
	}
}

func oversee(service string, methods ...string) {
	for _, m := range methods {
		Table["/rove."+service+"/"+m] = rule{min: Admin, auditor: true}
	}
}

var reads = []string{"Get", "List", "Watch"}

func init() {
	// The register and its history are everybody's to read.
	//
	// The time rows -- Placement, Link, Stewardship, Fact -- are read through
	// the history reads, Timeline, QueryAt and Diff, and not one by one: those
	// answer within the tenant's view window (design 8.1), and the rows cannot
	// be narrowed to it where they are kept without hiding them from the
	// domain layer that supersedes them. The events can, and are; see
	// `domain.View`.
	for _, s := range []string{
		"Asset", "AssetType", "ItemModel", "Attachment", "Label", "TenantDomain",
		"Party",
		"Bookable", "Reservation",
		"Custody",
		"Stock",
		"InventoryCount", "CountFinding",
		"WorkOrder", "Purchase",
	} {
		allow(Read, s+"Service", reads...)
	}
	// And what nobody watches -- the events, the tenant's own row, and the
	// rows that are a part of another -- by Get and List.
	for _, s := range []string{
		"Event",
		"Tenant",
		"ReservationItem", "Allocation",
		"CustodyLine",
		"StockMovement",
		"PurchaseLine",
	} {
		allow(Read, s+"Service", "Get", "List")
	}

	// What only the people who run the tenant, or audit it, read.
	oversee("AuditService", "Get", "List", "Recent")
	// And who read or wrote whose data (rove#136): the record a tenant's
	// administrators review each month, and an auditor reads.
	oversee("AccessService", "Get", "List")
	allow(Read, "EventService", "Recent")
	oversee("HolderService", reads...)
	oversee("UsageSnapshotService", "Get", "List")

	allow(Read, "AssetService", "Timeline", "QueryAt", "Diff", "Search", "Report")
	allow(Manager, "AssetService", "Add", "Erase", "Move", "SetAttributes", "Assign", "Relate", "Correct", "Export")
	// Bulk registration, and reading its form: who may Add may add many
	// (design 9.13). The types it makes are an admin's still -- the domain
	// asks, as AssetTypeService.Add is asked here.
	allow(Manager, "AssetService", "AddMany", "ReadSheet", "Form")

	allow(Admin, "AssetTypeService", "Add", "Update", "Erase")
	allow(Manager, "ItemModelService", "Add", "Update")
	allow(Admin, "ItemModelService", "Erase")
	allow(Read, "ItemModelService", "Search")

	allow(Read, "LabelService", "Resolve")
	allow(Manager, "LabelService", "Print", "Bind", "Unbind")

	allow(Read, "TenantDomainService", "Status")
	allow(Admin, "TenantDomainService", "Add", "Verify", "Activate", "Retire")

	allow(Read, "AttachmentService", "Url")
	allow(Member, "AttachmentService", "Upload")
	allow(Manager, "AttachmentService", "Erase")

	allow(Read, "PartyService", "Me", "Search")
	// Their own password; somebody else's is an admin's, which the domain
	// layer checks against the row.
	allow(Read, "PartyService", "SetPassword")
	allow(Admin, "PartyService", "Add", "Update", "Invite", "SetRole", "Deactivate", "Pseudonymize")

	allow(Owner, "TenantService", "Patch", "Close", "Restore", "Configure")

	allow(Read, "BookableService", "Availability")
	allow(Admin, "BookableService", "Add", "Update")

	allow(Read, "ReservationService", "Calendar")
	// Their own; somebody else's is a manager's, which the domain layer checks.
	allow(Member, "ReservationService", "Add", "Confirm", "Cancel", "CheckIn", "Complete")
	allow(Manager, "ReservationService", "Approve", "Reject")

	allow(Manager, "CustodyService", "Add", "Return", "Extend")
	// The receiver's, which the domain layer checks.
	allow(Member, "CustodyService", "Acknowledge")

	allow(Member, "StockService", "Consume")
	allow(Manager, "StockService", "Add", "Receive", "Adjust", "Transfer", "Convert")

	allow(Manager, "InventoryCountService", "Add", "Reconcile", "Resolve", "Close")
	// A self-audit is everybody's; any other count is a manager's, which the
	// domain layer checks.
	allow(Member, "InventoryCountService", "Scan")

	// Reporting something broken is everybody's.
	allow(Member, "WorkOrderService", "Add")
	allow(Manager, "WorkOrderService", "Update", "Complete", "Cancel")

	allow(Manager, "PurchaseService", "Add", "Receive")

	allow(Read, "NotificationService", "Inbox", "MarkRead", "SetMail")

	// Inviting into the workspace is its administrators', up to their own
	// role, which the domain layer checks. The answer is the account's it is
	// addressed to, and so is reading it: served as the account, held by the
	// domain layer to the invitations addressed to it -- and a workspace's own
	// to somebody who runs that workspace.
	allow(Admin, "InvitationService", "Add", "Revoke")
	allow(Account, "InvitationService", "List", "Claim", "Accept", "Deny")

	// A tenant's contract and the legal holds on it are the operator's: `rove
	// contract` and `rove hold` write them, from a shell, and no role here
	// may. The contract is everybody's to read, since it is what decides how
	// far back their history goes; a hold is for the people who run the tenant.
	allow(Read, "TenantContractService", "Get", "List")
	oversee("LegalHoldService", "Get", "List")

	// A batch is checked per operation, by the same table.
	Table["/payday.BatchService/Do"] = rule{min: Read}
}

var roleWord = map[string]string{"owner": "소유자", "admin": "관리자", "manager": "매니저", "member": "구성원", "auditor": "감사자"}

// Policy is the table, asked.
type Policy struct{}

var _ gate.Policy = Policy{}

// Role is the caller's role, from the holder row the resolver read.
func Role(c gate.Call) string {
	if h, ok := c.Row.(*app.Holder); ok {
		return h.GetRole()
	}
	return ""
}

// levelOf is how much the caller may do: their membership's role, or no more
// than an account's for a call served as the account.
func levelOf(c gate.Call) Level {
	if _, ok := c.Row.(*app.Account); ok {
		return Account
	}
	return Levels[Role(c)]
}

func (Policy) May(ctx context.Context, c gate.Call) error {
	r, ok := Table[c.Action]
	if !ok {
		if strings.HasPrefix(c.Action, "/grpc.") {
			// Health and reflection, which a frame never reaches anyway.
			return nil
		}
		return status.Errorf(codes.PermissionDenied, "직접 호출할 수 없는 작업입니다 (%s)", c.Action)
	}
	role := Role(c)
	lv := levelOf(c)
	if lv >= r.min && lv > Nobody {
		return nil
	}
	if r.auditor && role == "auditor" {
		return nil
	}
	if r.min == Nobody {
		return status.Errorf(codes.PermissionDenied, "직접 호출할 수 없는 작업입니다 (%s)", c.Action)
	}
	return status.Errorf(codes.PermissionDenied, "%s 역할로는 할 수 없는 작업입니다 (%s)", or(roleWord[role], "알 수 없는"), c.Action)
}

// Where is the caller's own tenant, and nothing else: rove has no operator
// who sees across tenants from inside one.
func (Policy) Where(ctx context.Context, c gate.Call) (frame.Tenants, error) {
	return frame.Only(c.Tenant), nil
}

func or(v, otherwise string) string {
	if v == "" {
		return otherwise
	}
	return v
}
