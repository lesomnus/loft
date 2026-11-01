// Package policy says what each role may call (design 9.3).
//
// It is a table, and what is not in it is refused. A method added to the schema
// is closed until somebody decides who may call it, which is the direction a
// mistake should fall in. One kept from every role on purpose, until that is
// decided, is in it as closed: refused as the rest are, and said so where a
// review reads the table.
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

	app "github.com/lesomnus/loft"
)

// Level is how much a role may do, in order.
type Level int

const (
	// Nobody is a method no role is given.
	Nobody Level = iota
	// Account is anybody signed in, as their account and not as a membership:
	// the calls an account makes before it is anybody's member, which the
	// schema marks as its own (`pd.AccountMethods`, payday#184) and the domain
	// layer holds to its own rows. A method of this level is an account's and
	// nobody else's: a membership is not given it, whatever its role.
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
		Table["/loft."+service+"/"+m] = rule{min: min}
	}
}

func oversee(service string, methods ...string) {
	for _, m := range methods {
		Table["/loft."+service+"/"+m] = rule{min: Admin, auditor: true}
	}
}

// report is a method the people who run the register read, and an auditor
// (design 9.14): a manager and up, and an auditor below that.
func report(service string, methods ...string) {
	for _, m := range methods {
		Table["/loft."+service+"/"+m] = rule{min: Manager, auditor: true}
	}
}

// closed is a method that is served and that no role is given yet: refused to
// everybody, the owner included, as a method the table does not name is --
// and said so in the table a review reads, so that giving it to a role is
// this line made an allow.
func closed(service string, methods ...string) {
	for _, m := range methods {
		Table["/loft."+service+"/"+m] = rule{min: Nobody}
	}
}

var reads = []string{"Get", "List", "Watch"}

func init() {
	// The register and its history are everybody's to read.
	//
	// The time rows -- Placement, Link, Stewardship, Fact -- are read through
	// the history reads, Timeline, QueryAt and Diff, and not one by one: the
	// history is told by the layer that writes it, as what each row was, with
	// the names it is about.
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
	// And who read or wrote whose data (loft#136): the record a tenant's
	// administrators review each month, and an auditor reads -- and that it
	// was reviewed, which either of them records. Read out by Review alone,
	// which says on the record whom it named: the rows themselves -- where
	// each call came from, what a read asked for -- are no RPC anybody is
	// served.
	oversee("AccessService", "Review")
	oversee("AccessReviewService", "Add", "Get", "List")
	allow(Read, "EventService", "Recent")
	// Taking an event's reason out, for a person whose request to erase what
	// it says of them points at it (payday#151's Redact): who may is the
	// owner's to decide, and until then nobody here may. The deployment's
	// operator does it, on the workspace's word, with `loft event redact`.
	closed("EventService", "Redact")
	oversee("HolderService", reads...)

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

	allow(Owner, "TenantService", "Patch", "Close", "Restore", "Configure", "Preview")

	allow(Read, "BookableService", "Availability")
	allow(Admin, "BookableService", "Add", "Update")

	allow(Read, "ReservationService", "Calendar")
	// A reservation's history (D45): a member's own, and anybody's to whoever
	// runs the register or audits it, which the domain layer checks.
	allow(Read, "ReservationService", "Timeline")
	// Their own; somebody else's is a manager's, which the domain layer checks.
	allow(Member, "ReservationService", "Add", "Confirm", "Cancel", "CheckIn", "Complete")
	allow(Manager, "ReservationService", "Approve", "Reject")
	// What a pickup is to hand out of a pool, read by whoever hands it out:
	// CustodyService.Add's.
	allow(Manager, "ReservationService", "Pickup")

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

	// The reports (design 9.14, D47) and their CSVs: what the people who run
	// the register read of it, and an auditor -- and no member, since one of
	// them is who has what of everybody's. The one that names people is a
	// download besides (`cmd.Downloads`), which says what it is for.
	report("AssetService", "Census", "ExportCensus")
	report("CustodyService", "Outstanding", "ExportOutstanding")
	report("BookableService", "Usage", "ExportUsage")
	report("InventoryCountService", "Results", "ExportResults")
	report("StockService", "Flow", "ExportFlow")

	// Inviting into the workspace is its administrators', up to their own
	// role, which the domain layer checks. The answer is the account's it is
	// addressed to, and so is reading it: served as the account, held by the
	// domain layer to the invitations addressed to it -- and a workspace's own
	// to somebody who runs that workspace.
	// A workspace's invitations are its administrators': sent, read and
	// taken back as their membership of it. The ones addressed to an account
	// are that account's own, answered as itself.
	allow(Admin, "InvitationService", "Add", "List", "Revoke")
	allow(Account, "InvitationService", "Inbox", "Claim", "Accept", "Deny")

	// How a workspace is paid for (design 7.9): a payer's own billing
	// accounts, checkouts, offers, the workspaces it pays for, its invoices
	// and the statements of the periods it paid for (levyan#47) are the
	// account's alone, relayed to levyan as it; who pays for a workspace,
	// what it has used and its statements are for the people who run it to
	// read; and asking a member to pay for it is its owner's.
	allow(Account, "PaymentService",
		"ListBillingAccounts", "CreateBillingAccount", "UpdateBillingAccount", "GetBillingAccount",
		"StartCheckout", "CompleteCheckout",
		"ListOffers", "Accept", "Decline",
		"ListLinks", "Use", "Stop",
		"ListInvoices", "GetInvoice", "ListCreditNotes",
		"ListPayerStatements", "GetPayerStatement",
	)
	allow(Admin, "PaymentService", "GetPayer", "GetUsage", "ListStatements")
	allow(Owner, "PaymentService", "Offer")

	// A tenant's contract and the legal holds on it are the operator's: `loft
	// contract` and `loft hold` write them, from a shell, and no role here
	// may. The contract is everybody's to read, since it says what becomes of
	// the workspace's trail when it is destroyed; a hold is for the people who
	// run the tenant. How long the workspace keeps its history is its own,
	// its owner's to say (`TenantService.Configure`, D36).
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
// than an account's for a call an account makes as itself (payday#184).
func levelOf(c gate.Call) Level {
	if c.AsAccount {
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
	if r.min == Nobody {
		// A closed method ([closed]): before any role is weighed, since every
		// role is at least nobody.
		return status.Errorf(codes.PermissionDenied, "아직 어느 역할에도 주지 않은 작업입니다 (%s)", c.Action)
	}
	role := Role(c)
	lv := levelOf(c)
	// The account's own methods are called as the account, and nothing else
	// is: payday refuses such a call every other method before this is asked,
	// and this refuses a membership the account's own.
	switch {
	case r.min == Account && lv == Account:
		return nil
	case r.min == Account:
		return status.Errorf(codes.PermissionDenied, "계정 자신으로 부르는 작업입니다 (%s)", c.Action)
	case lv == Account:
		return status.Errorf(codes.PermissionDenied, "워크스페이스의 구성원으로 부르는 작업입니다 (%s)", c.Action)
	}
	if lv >= r.min && lv > Nobody {
		return nil
	}
	if r.auditor && role == "auditor" {
		return nil
	}
	return status.Errorf(codes.PermissionDenied, "%s 역할로는 할 수 없는 작업입니다 (%s)", or(roleWord[role], "알 수 없는"), c.Action)
}

// Where is the caller's own tenant, and nothing else: loft has no operator
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
