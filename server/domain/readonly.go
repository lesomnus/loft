package domain

import (
	"context"

	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/lesomnus/payday/entitle"

	app "github.com/lesomnus/loft"
)

// A workspace levyan makes read-only (levyan#11, design 7.10, D64).
//
// The second stage of restricting a workspace, the owner's (levyan#1,
// 2026-11-01): sixty days after an invoice billing it first failed to be
// charged, or after it last had a payer, levyan grants it [ReadOnly], on, and
// ends the grant with the first stage's once the invoice is paid or somebody
// pays for it (levyan's `docs/apps.md`, 3, "제한의 두 단계"). The first stage,
// [Restricted], holds it to what it is billed by ([Grows]); this one holds it
// to what levyan's chapter says an app takes of it: reading, taking its data
// out, paying, and its members leaving it -- and closing, which is the
// operator's (D83) and no method of a role's. What is the person's at roster
// rather than the workspace's -- signing in -- stays theirs, and what a
// person is told stays theirs to quiet. Every other write is refused.
//
// So the table is of what it still does, [ReadOnlyAllows], and not of what it
// may not: a write somebody adds later is refused until it is put there. A
// method is refused as its operation begins ([Tx.begin], after `past.Begin`,
// so that a copy of an operation that happened before answers as it did),
// and -- for a write that begins no operation -- as the transaction it runs in
// would commit ([Deps.run]): every write this layer serves a caller is one of
// the two, which `readonly_test.go` holds every method a role may be given
// to. The copy is read as [Restricted]'s is, in the write's transaction, on
// its driver.
//
// It fails open as the first stage does: no grant of the key in force is not
// read-only, and a deployment that bills nobody reads no copy. What no caller
// of a workspace asks -- an operator's command at the entry point, a sweep, a
// door that is no RPC -- is not refused: the doors that are no RPC read (a
// file's download) or are a person's (signing in, their sessions), and an
// operator writes as the deployment -- closing a workspace and restoring it
// among it (D83).

// ReadOnly is the key levyan grants a workspace it makes read-only: on, while
// it is.
const ReadOnly = "loft.readonly"

// ErrorReadOnly is the reason a refusal of a read-only workspace's write
// carries (`errdetails.ErrorInfo`, domain `loft`), for a page to tell it from
// any other refusal -- [ErrorRestricted]'s among them.
const ErrorReadOnly = "READONLY"

// Allowed is why a read-only workspace still does something: what levyan's
// `docs/apps.md` (3, "제한의 두 단계") says an app takes of one, what is the
// person's at roster and not the workspace's, and what is a person's own.
type Allowed string

const (
	// Reads is every read (읽기): what the workspace has and what became of
	// it, read, searched and reported on, what a change of its settings
	// would take, and the reads that leave a record of themselves where the
	// deployment keeps one (who saw whose data, payday's access record).
	Reads Allowed = "reads"

	// Exports is its data taken out and its files downloaded (내보내기와
	// 내려받기): the register's CSV and the reports', a file it keeps, and the
	// blank form a sheet is filled in on. All of it at once is the
	// operator's (`TenantService.Export` at the entry point, D83).
	Exports Allowed = "exports"

	// Pays is how the workspace is paid for (결제, design 7.9): every call of
	// `loft.PaymentService` -- who pays for it, what it used, its statements,
	// a member asked to pay for it -- which Loft relays to levyan and keeps
	// nothing of. Paying, which its payer does where a payer pays and not
	// here (D81), is how it ends.
	Pays Allowed = "pays"

	// Leaves is a person forgotten in it (떠나기, 개인정보 삭제) -- which ends
	// their membership, and is besides an erasure somebody is owed, which
	// waits for no bill as a workspace's destruction waits for none (D59).
	// A membership ended otherwise is roster's (D87).
	Leaves Allowed = "leaves"

	// Own is a person's own of what the deployment tells them here (D67): a
	// notice marked read, and whether one comes by mail as well -- their own
	// rows and a setting of their membership, which grow nothing: how a
	// person quiets what a read-only workspace is still told
	// ([ReadOnlyTells]), as at any other time.
	Own Allowed = "own"
)

// ReadOnlyAllows is what a read-only workspace still does, by the method a
// caller asks for it by, and why. A method a role may be given is in it or is
// refused, whichever way the call came: an RPC, the page, the terminal, the
// assistant's `apply`.
//
// That it reads, takes its data out, pays and closes is the owner's
// (levyan#1, 2026-11-01); closing is the operator's since (D83), and so in
// no row here. That its members may leave it and that what is the person's
// at roster stays theirs is Claude's, as is that what a person is told stays
// theirs to quiet (D67). Which methods those are is said here, and held to
// what levyan's `docs/apps.md` says an app takes -- but for a password,
// which is the person's at roster, and what a person is told, which is theirs.
var ReadOnlyAllows = map[string]Allowed{
	// What it has, and what became of it: the register and its catalog, its
	// people, bookings, hand-overs, stock, counts, work and purchases, as they
	// are and as they were, searched and watched.
	app.AssetService_Get_FullMethodName:              Reads,
	app.AssetService_List_FullMethodName:             Reads,
	app.AssetService_Watch_FullMethodName:            Reads,
	app.AssetService_Search_FullMethodName:           Reads,
	app.AssetService_Timeline_FullMethodName:         Reads,
	app.AssetService_QueryAt_FullMethodName:          Reads,
	app.AssetService_Diff_FullMethodName:             Reads,
	app.AssetService_Report_FullMethodName:           Reads,
	app.AssetTypeService_Get_FullMethodName:          Reads,
	app.AssetTypeService_List_FullMethodName:         Reads,
	app.AssetTypeService_Watch_FullMethodName:        Reads,
	app.ItemModelService_Get_FullMethodName:          Reads,
	app.ItemModelService_List_FullMethodName:         Reads,
	app.ItemModelService_Watch_FullMethodName:        Reads,
	app.ItemModelService_Search_FullMethodName:       Reads,
	app.AttachmentService_Get_FullMethodName:         Reads,
	app.AttachmentService_List_FullMethodName:        Reads,
	app.AttachmentService_Watch_FullMethodName:       Reads,
	app.LabelService_Get_FullMethodName:              Reads,
	app.LabelService_List_FullMethodName:             Reads,
	app.LabelService_Watch_FullMethodName:            Reads,
	app.LabelService_Resolve_FullMethodName:          Reads,
	app.PartyService_Get_FullMethodName:              Reads,
	app.PartyService_List_FullMethodName:             Reads,
	app.PartyService_Watch_FullMethodName:            Reads,
	app.PartyService_Search_FullMethodName:           Reads,
	app.PartyService_Me_FullMethodName:               Reads,
	app.BookableService_Get_FullMethodName:           Reads,
	app.BookableService_List_FullMethodName:          Reads,
	app.BookableService_Watch_FullMethodName:         Reads,
	app.BookableService_Availability_FullMethodName:  Reads,
	app.ReservationService_Get_FullMethodName:        Reads,
	app.ReservationService_List_FullMethodName:       Reads,
	app.ReservationService_Watch_FullMethodName:      Reads,
	app.ReservationService_Calendar_FullMethodName:   Reads,
	app.ReservationService_Timeline_FullMethodName:   Reads,
	app.ReservationService_Pickup_FullMethodName:     Reads,
	app.ReservationItemService_Get_FullMethodName:    Reads,
	app.ReservationItemService_List_FullMethodName:   Reads,
	app.AllocationService_Get_FullMethodName:         Reads,
	app.AllocationService_List_FullMethodName:        Reads,
	app.CustodyService_Get_FullMethodName:            Reads,
	app.CustodyService_List_FullMethodName:           Reads,
	app.CustodyService_Watch_FullMethodName:          Reads,
	app.CustodyLineService_Get_FullMethodName:        Reads,
	app.CustodyLineService_List_FullMethodName:       Reads,
	app.StockService_Get_FullMethodName:              Reads,
	app.StockService_List_FullMethodName:             Reads,
	app.StockService_Watch_FullMethodName:            Reads,
	app.StockMovementService_Get_FullMethodName:      Reads,
	app.StockMovementService_List_FullMethodName:     Reads,
	app.InventoryCountService_Get_FullMethodName:     Reads,
	app.InventoryCountService_List_FullMethodName:    Reads,
	app.InventoryCountService_Watch_FullMethodName:   Reads,
	app.CountFindingService_Get_FullMethodName:       Reads,
	app.CountFindingService_List_FullMethodName:      Reads,
	app.CountFindingService_Watch_FullMethodName:     Reads,
	app.WorkOrderService_Get_FullMethodName:          Reads,
	app.WorkOrderService_List_FullMethodName:         Reads,
	app.WorkOrderService_Watch_FullMethodName:        Reads,
	app.PurchaseService_Get_FullMethodName:           Reads,
	app.PurchaseService_List_FullMethodName:          Reads,
	app.PurchaseService_Watch_FullMethodName:         Reads,
	app.PurchaseLineService_Get_FullMethodName:       Reads,
	app.PurchaseLineService_List_FullMethodName:      Reads,
	app.EventService_Get_FullMethodName:              Reads,
	app.EventService_List_FullMethodName:             Reads,
	app.EventService_Recent_FullMethodName:           Reads,
	app.AssetService_Census_FullMethodName:           Reads,
	app.CustodyService_Outstanding_FullMethodName:    Reads,
	app.BookableService_Usage_FullMethodName:         Reads,
	app.InventoryCountService_Results_FullMethodName: Reads,
	app.StockService_Flow_FullMethodName:             Reads,
	app.AssetService_ReadSheet_FullMethodName:        Reads,

	// The workspace itself: its settings and what a change of them would
	// take, its contract and its legal holds, its memberships, what it tells
	// people, its trail, and who saw whose data.
	app.TenantService_Get_FullMethodName:          Reads,
	app.TenantService_List_FullMethodName:         Reads,
	app.TenantService_Preview_FullMethodName:      Reads,
	app.TenantContractService_Get_FullMethodName:  Reads,
	app.TenantContractService_List_FullMethodName: Reads,
	app.LegalHoldService_Get_FullMethodName:       Reads,
	app.LegalHoldService_List_FullMethodName:      Reads,
	app.HolderService_Get_FullMethodName:          Reads,
	app.HolderService_List_FullMethodName:         Reads,
	app.HolderService_Watch_FullMethodName:        Reads,
	app.NotificationService_Inbox_FullMethodName:  Reads,
	app.AuditService_Get_FullMethodName:           Reads,
	app.AuditService_List_FullMethodName:          Reads,
	app.AuditService_Recent_FullMethodName:        Reads,
	app.AccessService_Review_FullMethodName:       Reads,
	app.AccessReviewService_Get_FullMethodName:    Reads,
	app.AccessReviewService_List_FullMethodName:   Reads,

	// Its data taken out, and its files.
	app.AssetService_Export_FullMethodName:                 Exports,
	app.AssetService_ExportCensus_FullMethodName:           Exports,
	app.CustodyService_ExportOutstanding_FullMethodName:    Exports,
	app.BookableService_ExportUsage_FullMethodName:         Exports,
	app.InventoryCountService_ExportResults_FullMethodName: Exports,
	app.StockService_ExportFlow_FullMethodName:             Exports,
	app.AttachmentService_Url_FullMethodName:               Exports,
	app.AssetService_Form_FullMethodName:                   Exports,

	// Who pays for it, and asking a member to: paying is how this ends.
	app.PaymentService_GetPayer_FullMethodName:       Pays,
	app.PaymentService_GetUsage_FullMethodName:       Pays,
	app.PaymentService_ListStatements_FullMethodName: Pays,
	app.PaymentService_Offer_FullMethodName:          Pays,

	// Leaving it.
	app.PartyService_Pseudonymize_FullMethodName: Leaves,

	// What a person is told here, which is their own: read, and by mail or
	// not.
	app.NotificationService_MarkRead_FullMethodName: Own,
	app.NotificationService_SetMail_FullMethodName:  Own,
}

// readOnlyRefusal is what a read-only workspace says of a write it refuses:
// that it is read-only, what it still does, and how it ends. Not that it does
// nothing else: what is the person's at roster, or their own, it does (D68).
const readOnlyRefusal = "이 워크스페이스는 읽기 전용이라 바꿀 수 없습니다. 보기, 내보내기, 결제, 떠나기는 할 수 있고, 결제를 정리하면 다시 바꿀 수 있습니다"

// passing is a write this layer adds nothing to -- a generated one a role is
// given, served as it is -- but what levyan's grants hold the workspace back
// from ([Tx.held]): in a transaction of this layer's, so that the copy is read
// in the write's, as every other write reads it.
func passing[Out any](s Domain, ctx context.Context, f func(t *Tx) (Out, error)) (Out, error) {
	var out Out
	err := s.tx(ctx, func(t *Tx) error {
		if err := t.held(); err != nil {
			return err
		}
		v, err := f(t)
		out = v
		return err
	})
	return out, err
}

// held refuses what levyan's grants hold the workspace back from, as an
// operation begins: a write a read-only workspace does not do, and then what
// grows a restricted one.
func (t *Tx) held() error {
	if err := t.writing(); err != nil {
		return err
	}
	return t.growing()
}

// writing refuses the method the caller asked for while the workspace is
// read-only, unless [ReadOnlyAllows] names it. A call nobody made over the
// wire -- a sweep -- asks no method, and is not refused; nor is an
// operator's at the entry point, which writes as the deployment (D64, as
// D60) -- an unpaid workspace closed or restored by its operator among it
// (D83).
func (t *Tx) writing() error {
	m, ok := grpc.Method(t.base)
	if !ok || operated(t.base) {
		return nil
	}
	if _, ok := ReadOnlyAllows[m]; ok {
		return nil
	}
	on, err := t.readonly()
	if err != nil || !on {
		return err
	}

	st := status.New(codes.FailedPrecondition, readOnlyRefusal)
	v, err := st.WithDetails(&errdetails.ErrorInfo{
		Reason:   ErrorReadOnly,
		Domain:   "loft",
		Metadata: map[string]string{"entitlement": ReadOnly, "refused": m},
	})
	if err != nil {
		return st.Err()
	}
	return v.Err()
}

// readonly is whether levyan makes the workspace read-only now: whether the
// copy holds a grant of [ReadOnly] in force, read in this transaction and
// once for all of it. Never on a deployment that bills nobody, and never for
// a call that is in no workspace.
func (t *Tx) readonly() (bool, error) {
	if t.deps == nil || t.deps.Entitlements == nil || t.tenant.IsZero() {
		return false, nil
	}
	if t.shared != nil && t.shared.readonly != nil {
		return *t.shared.readonly, nil
	}

	on, err := entitle.Has(t.ctx, t.deps.Entitlements(t.drv), ReadOnly)
	if err != nil {
		return false, err
	}
	if t.shared != nil {
		t.shared.readonly = &on
	}
	return on, nil
}
