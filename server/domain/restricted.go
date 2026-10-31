package domain

import (
	"fmt"
	"slices"

	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/lesomnus/payday/entitle"

	app "github.com/lesomnus/loft"
)

// A workspace levyan restricts (levyan#14, design 7.5, D60).
//
// levyan grants a workspace [Restricted] when it is to be restricted -- today
// one nobody has paid for since its grace ran out (levyan's `payers.grace`),
// and whatever its operator grants by hand -- and ends the grant when it is
// not. This deployment keeps a copy of the grants (payday's `entitle`, kept by
// the follower `cmd.Build` runs) and reads it here, in the transaction of the
// write it decides, on that write's driver ([Deps.Entitlements]).
//
// **The grant restricts, and its absence restricts nothing.** A workspace
// with no grant of the key in force -- one levyan never restricted, one whose
// grant ended, one this deployment has not heard of from levyan yet -- does
// what it always could: a gap in levyan never locks a workspace (levyan's
// design 13). On a deployment that bills nobody the copy is not read at all.
//
// What a restricted workspace may not do is grow what it is billed for
// ([Grows]); it still reads, takes its data out, hands things out and takes
// them back, books, moves, disposes of and deletes what it has, changes what
// it is set to that bills nothing, closes, and pays. That is Claude's, and it
// stands until the owner says what restricting is (levyan#1's open question
// 3). An operator's commands write as the deployment, through no RPC, and are
// not held to it.

// Restricted is the key levyan grants a workspace it restricts: on, while it
// is.
const Restricted = "loft.restricted"

// The cases of [Grows] a method alone cannot say, refused where the domain
// layer sees them grow, inside a method that does more than this.
const (
	// GrowsAssetBack is an asset back from being disposed of or lost, which
	// `loft.assets.active` counts again ([Unmetered]): through
	// SetAttributes, or a Correct that takes back the fact that it went.
	GrowsAssetBack = "asset.status"

	// GrowsKeptLonger is the history kept longer than the window in force
	// (`TenantService.Configure`'s `keep_days`), which
	// `loft.history.retention_days` bills; a window made shorter, or the
	// same, is not.
	GrowsKeptLonger = "workspace.keep_days"

	// GrowsReceivedAssets is a purchase received as assets, which registers
	// one for each unit; received into stock, it registers nothing.
	GrowsReceivedAssets = "purchase.receive"
)

// Grows is what a restricted workspace may not do, by what it is told instead:
// every way it adds to what it is billed for (`proto/loft/meter.proto`). It
// is the one place that is said, and the owner's to widen -- to every write,
// a workspace that only reads.
//
// A key is the method a caller asks for it by, for one whose every call grows
// a meter: refused as the operation begins ([Tx.begin]), whichever way the
// call came -- an RPC, an operation of a batch, the page, the terminal, the
// assistant's `apply` -- before anything it does is done. Or it is a case the
// domain layer sees grow inside a method that does more than that, refused
// where it sees it.
var Grows = map[string]string{
	// A new asset, of whatever kind, and many at once.
	app.AssetService_Add_FullMethodName:     "자산을 새로 등록할 수 없습니다",
	app.AssetService_AddMany_FullMethodName: "자산을 대량으로 등록할 수 없습니다",
	app.StockService_Convert_FullMethodName: "재고를 자산으로 전환할 수 없습니다",
	// A file kept, which `loft.attachments.bytes` sums: the page uploads it
	// through this, over HTTP as any of its calls.
	app.AttachmentService_Upload_FullMethodName: "첨부를 올릴 수 없습니다",
	// A new member, which `loft.members` counts: a login given to a person
	// record, an invitation sent, and one accepted -- sent before the
	// workspace was restricted, too.
	app.PartyService_Invite_FullMethodName:      "로그인을 새로 발급할 수 없습니다",
	app.InvitationService_Add_FullMethodName:    "구성원을 초대할 수 없습니다",
	app.InvitationService_Accept_FullMethodName: "초대를 받아들일 수 없습니다",

	GrowsAssetBack:      "분실·폐기한 자산을 다시 쓰는 상태로 바꿀 수 없습니다",
	GrowsKeptLonger:     "이력을 지금보다 오래 보관하도록 바꿀 수 없습니다",
	GrowsReceivedAssets: "구매를 자산으로 입고할 수 없습니다(재고로는 입고할 수 있습니다)",
}

// Unmetered is the statuses of an asset `loft.assets.active` does not count:
// gone from the workspace. The meter's declaration says the same
// (`proto/loft/meter.proto`), and a test holds the two to each other.
var Unmetered = []string{"disposed", "lost"}

// ErrorRestricted is the reason a refusal of [Grows] carries
// (`errdetails.ErrorInfo`, domain `loft`), for a page to tell it from any
// other refusal.
const ErrorRestricted = "RESTRICTED"

// growing refuses the method the caller asked for, when it is one of [Grows]
// and the workspace is restricted. A call nobody made over the wire -- an
// operator's command, a sweep -- asks no method, and is not refused.
func (t *Tx) growing() error {
	m, ok := grpc.Method(t.base)
	if !ok {
		return nil
	}
	return t.grows(m)
}

// grows refuses what `key` names in [Grows] while the workspace is restricted,
// and anything else never.
func (t *Tx) grows(key string) error {
	what, ok := Grows[key]
	if !ok {
		return nil
	}
	on, err := t.restricted()
	if err != nil || !on {
		return err
	}

	st := status.New(codes.FailedPrecondition, fmt.Sprintf("이 워크스페이스는 제한되어 %s. 결제를 정리하면 다시 할 수 있습니다", what))
	v, err := st.WithDetails(&errdetails.ErrorInfo{
		Reason:   ErrorRestricted,
		Domain:   "loft",
		Metadata: map[string]string{"entitlement": Restricted, "refused": key},
	})
	if err != nil {
		return st.Err()
	}
	return v.Err()
}

// restricted is whether levyan restricts the workspace now: whether the copy
// holds a grant of [Restricted] in force, read in this transaction and once
// for all of it. Never on a deployment that bills nobody.
func (t *Tx) restricted() (bool, error) {
	if t.deps == nil || t.deps.Entitlements == nil {
		return false, nil
	}
	if t.shared != nil && t.shared.restricted != nil {
		return *t.shared.restricted, nil
	}

	on, err := entitle.Has(t.ctx, t.deps.Entitlements(t.drv), Restricted)
	if err != nil {
		return false, err
	}
	if t.shared != nil {
		t.shared.restricted = &on
	}
	return on, nil
}

// metered is whether `loft.assets.active` counts an asset of this status.
func metered(status string) bool { return !slices.Contains(Unmetered, status) }
