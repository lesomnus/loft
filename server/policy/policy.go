// Package policy says what may be called (design 9.3, D89).
//
// Who may call what is roster's. A role there is a list of methods, bound to
// people by the tenant's administrators in roster's screens, and every call
// is asked of roster as it is made: may this person call this method --
// roster's `HolderService/Reaches`, read with payday's `frame.Covers` and kept
// for half a minute by roster's `reach` package (#327's decisions 7, 10, 27),
// about the person and never about a role, whose name Loft does not know. A
// role is the tenant's to rename, split or narrow without Loft noticing.
//
// What is here is what stays Loft's.
//
//   - **What may be given at all** ([Grantable]): every method a role Loft
//     ships names. One no such role names is refused before roster is asked,
//     whatever a role at roster says -- a pattern like `/loft.*/*`, which the
//     tenant's administrators hold so that they can bind Loft's roles
//     (roster's `app install --administer`), covers the generated writes of
//     the rows only the domain layer writes too: the history rows, the
//     events, the allocations and the rest have generated Add, Patch and Erase
//     like everything else, and the domain layer writes them from behind the
//     gate, where nothing is asked. They are sealed here, as the generated
//     Patch of everything the domain completes is -- an asset changes through
//     Move and SetAttributes, so that the history is written with it. A method
//     added to the schema is closed until a role Loft ships names it, which is
//     the direction a mistake should fall in; one kept from everybody on
//     purpose, until somebody decides, is [Closed], and said so where a review
//     reads the roles.
//   - **The roles Loft ships** ([Roles]): owner, administrator, manager,
//     member and auditor, and what each holds at first. `loft init` writes
//     them into the tenant at roster, and binds the first owner, as the holder
//     Loft is there (`identity.Store.WriteRoles`, #327's decisions 9, 31);
//     from then on they are the tenant's, to change in roster's screens.
//     Nothing reads them to decide a call. They are written here as the table
//     they were -- each method and the least role it is given -- because that
//     is how a review reads them; `testdata/roles.txt` is them written out.
//
// A policy is asked once per call, about the method the caller named. What the
// domain layer then calls on the caller's behalf is not asked about again;
// that is the point of the layer. A permission finer than one method -- the
// same act over somebody else's, `proto/loft/permission.proto` -- the domain
// layer asks itself (`domain.Tx.may`), of roster too.
package policy

import (
	"context"
	"errors"
	"slices"
	"strings"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/lesomnus/payday/frame"
	"github.com/lesomnus/payday/gate"
	"github.com/lesomnus/payday/pdid"
	"github.com/lesomnus/roster/reach"
)

// level is how much a role Loft ships may do, in order: what the table below
// is written in, and nothing else.
type level int

const (
	// nobody is a method no role is given ([Closed]).
	nobody level = iota
	// read is everybody signed in, auditors included.
	read
	// member is everybody who works here: reserve, report, take supplies.
	member
	// manager runs the register: assets, hand-overs, counts, stock, work.
	manager
	// admin runs the workspace: the person records, the types, the bookable
	// policies.
	admin
	// owner is the workspace's: its settings, and who pays for it.
	owner
)

type rule struct {
	min level
	// auditor lets an auditor in below `min`: the reads an auditor exists for,
	// such as the trail, which are an admin's otherwise.
	auditor bool
}

// first is every method somebody may call, by the name gRPC knows it by, and
// the least role Loft ships that is given it: what [Roles] is made of.
var first = map[string]rule{}

func allow(min level, service string, methods ...string) {
	for _, m := range methods {
		first["/loft."+service+"/"+m] = rule{min: min}
	}
}

func oversee(service string, methods ...string) {
	for _, m := range methods {
		first["/loft."+service+"/"+m] = rule{min: admin, auditor: true}
	}
}

// report is a method the people who run the register read, and an auditor
// (design 9.14): a manager and up, and an auditor below that.
func report(service string, methods ...string) {
	for _, m := range methods {
		first["/loft."+service+"/"+m] = rule{min: manager, auditor: true}
	}
}

// closed is a method that is served and that no role is given yet: refused to
// everybody, as a method no role names is -- and said so where a review reads
// the roles, so that giving it to one is this line made an allow.
func closed(service string, methods ...string) {
	for _, m := range methods {
		first["/loft."+service+"/"+m] = rule{min: nobody}
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
		"Asset", "AssetType", "ItemModel", "Attachment", "Label",
		"Party",
		"Bookable", "Reservation",
		"Custody",
		"Stock",
		"InventoryCount", "CountFinding",
		"WorkOrder", "Purchase",
	} {
		allow(read, s+"Service", reads...)
	}
	// And what nobody watches -- the tenant's own row, and the rows that are a
	// part of another -- by Get and List.
	for _, s := range []string{
		"Tenant",
		"ReservationItem", "Allocation",
		"CustodyLine",
		"StockMovement",
		"PurchaseLine",
	} {
		allow(read, s+"Service", "Get", "List")
	}

	// What only the people who run the tenant, or audit it, read.
	oversee("AuditService", "Get", "List", "Recent")
	// The work log (설정 › 작업 기록): every operation in the workspace,
	// newest first, with the reasons people wrote, which are theirs. The
	// owner decided it is read as the trail is (2026-11-12, D79). Get and List
	// are the same rows without the sentence, so they go with it. What
	// happened to one thing is not the log: an asset's history
	// (`AssetService.Timeline` and the rest, below) is everybody's still, and
	// a reservation's is as D45 has it.
	oversee("EventService", "Get", "List", "Recent")
	// And who read or wrote whose data (loft#136): the record a tenant's
	// administrators review each month, and an auditor reads -- and that it
	// was reviewed, which either of them records. Read out by Review alone,
	// which says on the record whom it named: the rows themselves -- where
	// each call came from, what a read asked for -- are no RPC anybody is
	// served.
	oversee("AccessService", "Review")
	oversee("AccessReviewService", "Add", "Get", "List")
	// Taking an event's reason out, for a person whose request to erase what
	// it says of them points at it (payday#151's Redact): who may is the
	// owner's to decide, and until then nobody here may. The deployment's
	// operator does it, on the workspace's word, with `loft event redact`.
	closed("EventService", "Redact")
	oversee("HolderService", reads...)

	allow(read, "AssetService", "Timeline", "QueryAt", "Diff", "Search", "Report")
	allow(manager, "AssetService", "Add", "Erase", "Move", "SetAttributes", "Assign", "Relate", "Correct", "Export")
	// Bulk registration, and reading its form: who may Add may add many
	// (design 9.13). The types it makes are an admin's still -- the domain
	// asks, as AssetTypeService.Add is asked here.
	allow(manager, "AssetService", "AddMany", "ReadSheet", "Form")

	allow(admin, "AssetTypeService", "Add", "Update", "Erase")
	allow(manager, "ItemModelService", "Add", "Update")
	allow(admin, "ItemModelService", "Erase")
	allow(read, "ItemModelService", "Search")

	allow(read, "LabelService", "Resolve")
	allow(manager, "LabelService", "Print", "Bind", "Unbind")

	allow(read, "AttachmentService", "Url")
	allow(member, "AttachmentService", "Upload")
	allow(manager, "AttachmentService", "Erase")

	allow(read, "PartyService", "Me", "Search")
	// Who is a member, and how they sign in, is roster's (D87), and so is
	// what they may call (D89): what is here is the person records.
	allow(admin, "PartyService", "Add", "Update", "Pseudonymize")

	// A workspace's settings are its owner's. Its end is not anybody's in it
	// (D83): closing it, restoring it and taking all of it out are the
	// operator's, at the entry point (`server/operator`'s table), and no role
	// here is given them.
	allow(owner, "TenantService", "Patch", "Configure", "Preview")

	allow(read, "BookableService", "Availability")
	allow(admin, "BookableService", "Add", "Update")

	allow(read, "ReservationService", "Calendar")
	// A reservation's history (D45): a member's own, and anybody's to whoever
	// may read every one (`PermissionService.ReadEveryReservation`, below),
	// which the domain layer asks.
	allow(read, "ReservationService", "Timeline")
	// Their own; somebody else's, and over what is booked, is a permission
	// of its own (below), which the domain layer asks.
	allow(member, "ReservationService", "Add", "Confirm", "Cancel", "CheckIn", "Complete")
	allow(manager, "ReservationService", "Approve", "Reject")
	// What a pickup is to hand out of a pool, read by whoever hands it out:
	// CustodyService.Add's.
	allow(manager, "ReservationService", "Pickup")

	allow(manager, "CustodyService", "Add", "Return", "Extend")
	// The receiver's; for somebody else is a permission of its own (below),
	// which the domain layer asks.
	allow(member, "CustodyService", "Acknowledge")

	allow(member, "StockService", "Consume")
	allow(manager, "StockService", "Add", "Receive", "Adjust", "Transfer", "Convert")

	allow(manager, "InventoryCountService", "Add", "Reconcile", "Resolve", "Close")
	// A self-audit is everybody's; any other count is a permission of its own
	// (below), which the domain layer asks.
	allow(member, "InventoryCountService", "Scan")

	// Reporting something broken is everybody's; deciding work is a
	// permission of its own (below), which the domain layer asks.
	allow(member, "WorkOrderService", "Add")
	allow(manager, "WorkOrderService", "Update", "Complete", "Cancel")

	allow(manager, "PurchaseService", "Add", "Receive")

	allow(read, "NotificationService", "Inbox", "MarkRead", "SetMail")

	// The reports (design 9.14, D47) and their CSVs: what the people who run
	// the register read of it, and an auditor -- and no member, since one of
	// them is who has what of everybody's. The one that names people is a
	// download besides (`cmd.Downloads`), which says what it is for.
	report("AssetService", "Census", "ExportCensus")
	report("CustodyService", "Outstanding", "ExportOutstanding")
	report("BookableService", "Usage", "ExportUsage")
	report("InventoryCountService", "Results", "ExportResults")
	report("StockService", "Flow", "ExportFlow")

	// How a workspace is paid for (design 7.9): who pays for it, what it has
	// used and its statements are for the people who run it to read; and
	// asking a member to pay for it is its owner's. What a payer does with
	// what it pays with is the portal's, and no method here (D81).
	allow(admin, "PaymentService", "GetPayer", "GetUsage", "ListStatements")
	allow(owner, "PaymentService", "Offer")

	// A tenant's contract and the legal holds on it are the operator's: the
	// entry point writes them (`loft tenant-contract add`, `loft legal-hold add`), and no role here
	// may. The contract is everybody's to read, since it says what becomes of
	// the workspace's trail when it is destroyed; a hold is for the people who
	// run the tenant. How long the workspace keeps its history is its own,
	// its owner's to say (`TenantService.Configure`, D36).
	allow(read, "TenantContractService", "Get", "List")
	oversee("LegalHoldService", "Get", "List")

	// The permissions finer than one RPC (#327's decision 8, D89,
	// `proto/loft/permission.proto`): the same act over more than the
	// caller's own, which the domain layer asks roster about as it would
	// about an RPC. Each is what the manager's role held as a role.
	allow(manager, "PermissionService",
		"BookForOthers", "BookOverConflicts", "ActOnOthersBookings",
		"AcknowledgeForOthers", "ScanAnyCount", "RunWork")
	// And reading every reservation's history, which an auditor exists for
	// (D45).
	report("PermissionService", "ReadEveryReservation")
}

// Role is a role Loft ships (#327's decision 9): what it is called at roster,
// and what it holds at first.
type Role struct {
	// Alias is the role's at roster, in the tenant: `loft-owner`, ... -- named
	// for Loft, since a tenant's roles are every app's it uses.
	Alias string
	// Name is what roster's screens call it.
	Name string
	// Desc says what it is for, in roster's screens.
	Desc string
	// Methods is what it holds at first, by full name, in order.
	Methods []string
}

// Roles is what Loft ships, owner first: what `loft init` writes into the
// tenant at roster, each under its alias, and leaves alone where the tenant
// has one already.
var Roles []Role

// RoleOf is one of [Roles] by its alias, or by the word it is made of
// (`owner` for `loft-owner`) -- what `loft holder add --role` and the demo
// name one by.
func RoleOf(name string) (Role, bool) {
	for _, r := range Roles {
		if r.Alias == name || r.Alias == "loft-"+name {
			return r, true
		}
	}
	return Role{}, false
}

// Grantable says whether `method` may be given to anybody: a method a role
// Loft ships names. What no such role names -- and what is [Closed] -- the
// gate refuses whatever roster says.
//
// What no role is given, the operator's entry point may still serve
// (`server/operator`'s table), and the terminal says a command of it is the
// operator's (design 9.16).
func Grantable(method string) bool {
	r, ok := first[method]
	return ok && r.min > nobody
}

// Closed says whether `method` is served and kept from everybody on purpose,
// until somebody decides who may.
func Closed(method string) bool {
	r, ok := first[method]
	return ok && r.min == nobody
}

// Named is every method this package names, [Grantable] and [Closed], in
// order: what a test of every method somebody could be given goes over.
func Named() []string {
	out := []string{}
	for m := range first {
		out = append(out, m)
	}
	slices.Sort(out)
	return out
}

// Methods is every [Grantable] method, in order: what the page is told
// somebody may call, read against what roster says of them.
func Methods() []string {
	out := []string{}
	for m, r := range first {
		if r.min > nobody {
			out = append(out, m)
		}
	}
	slices.Sort(out)
	return out
}

func init() {
	for _, r := range []struct {
		alias, name, desc string
		lv                level
		auditor           bool
	}{
		{"loft-owner", "Loft 소유자", "워크스페이스의 설정과 결제까지 모두 합니다.", owner, false},
		{"loft-admin", "Loft 관리자", "사람 기록, 자산 유형, 예약 정책을 관리하고 기록을 봅니다.", admin, false},
		{"loft-manager", "Loft 매니저", "자산, 지급·대여, 실사, 재고, 작업을 운영하고 남의 예약을 다룹니다.", manager, false},
		{"loft-member", "Loft 구성원", "자산을 보고, 예약하고, 고장을 신고하고, 받은 것을 확인합니다.", member, false},
		{"loft-auditor", "Loft 감사자", "모두 읽고 아무것도 바꾸지 않습니다. 기록과 리포트를 봅니다.", read, true},
	} {
		ms := []string{}
		for m, rl := range first {
			if rl.min == nobody {
				continue
			}
			if rl.min <= r.lv || r.auditor && rl.auditor {
				ms = append(ms, m)
			}
		}
		slices.Sort(ms)
		Roles = append(Roles, Role{Alias: r.alias, Name: r.name, Desc: r.desc, Methods: ms})
	}
}

// Reach answers whether a holder may call a method, as roster says it:
// roster's `reach.Holders.May`, which [Policy] and the domain layer are both
// handed.
type Reach interface {
	May(ctx context.Context, holder pdid.Id, method string) error
}

// Policy asks roster, for what may be given at all.
type Policy struct {
	Reach Reach
}

var _ gate.Policy = Policy{}

func (p Policy) May(ctx context.Context, c gate.Call) error {
	r, ok := first[c.Action]
	if !ok {
		if strings.HasPrefix(c.Action, "/grpc.") {
			// Health and reflection, which a frame never reaches anyway.
			return nil
		}
		return status.Errorf(codes.PermissionDenied, "직접 호출할 수 없는 작업입니다 (%s)", c.Action)
	}
	if r.min == nobody {
		// A closed method ([Closed]): before roster is asked, since whatever
		// a role there holds, nobody is given it here.
		return status.Errorf(codes.PermissionDenied, "아직 어느 역할에도 주지 않은 작업입니다 (%s)", c.Action)
	}
	return Said(p.Reach.May(ctx, c.Actor, c.Action), c.Action)
}

// Said is what roster said of a call, as its caller is told it: nil for a
// method roster says they may call, PermissionDenied naming the method for
// one it does not -- what the gate says, and the domain layer for a
// permission -- and Unavailable for a roster that could not be asked, which
// is no doing of the caller's (#327's decision 27).
func Said(err error, method string) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, reach.ErrDenied):
		return status.Errorf(codes.PermissionDenied, "roster에서 받은 역할로는 할 수 없는 작업입니다 (%s)", method)
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return status.FromContextError(err).Err()
	default:
		return status.Errorf(codes.Unavailable, "roster에 물을 수 없어 할 수 있는 일인지 알 수 없습니다. 잠시 뒤에 다시 하세요 (%s)", method)
	}
}

// Where is the caller's own tenant, and nothing else: loft has no operator
// who sees across tenants from inside one.
func (Policy) Where(ctx context.Context, c gate.Call) (frame.Tenants, error) {
	return frame.Only(c.Tenant), nil
}
