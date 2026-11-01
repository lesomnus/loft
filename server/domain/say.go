package domain

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"
	"uuid"

	"github.com/lesomnus/payday/past"
	"github.com/lesomnus/payday/pdid"

	app "github.com/lesomnus/loft"
	"github.com/lesomnus/loft/internal/ent"
	"github.com/lesomnus/loft/internal/ent/event"
	"github.com/lesomnus/loft/server/pd"
)

// An event keeps no sentence (payday#151): it keeps its kind's message
// (`loft/past.proto`), and what it says is made of that when it is read -- in
// the workspace's zone, naming the people in it as they are called then, each
// of them on the access record of the call that reads it (design 8.2). What
// is here is what Loft says of each kind, in Korean, once: a sentence for
// each, a kind of a shared message reading what its kind uses.
//
// A thing is said as the message holds it -- an asset by the tag it had, a
// reservation by the name it had -- and a person by identifier, named when it
// is read ([who]). The words are the ones each event was written with before
// it kept a message, so that what a page shows reads the same across the two,
// but for two things. A value of Loft's own -- a role, a relation, a stock's
// movement, how a finding was settled, what was done to a reservation -- is
// said in the word the page says it in (`io.go`), where it was written as its
// code; and a reservation's time is said in the workspace's zone, where it was
// UTC. A row from before reads as it was written.

// Sentences is what Loft says of its events.
var Sentences = sentences()

// whileSay is what a reservation the deployment let go says of why nobody
// took it up, by what its workspace was then ([whileReadOnly],
// [whileClosed]): nothing, when it was neither.
var whileSay = map[string]string{
	whileReadOnly: " (워크스페이스가 읽기 전용이던 때)",
	whileClosed:   " (워크스페이스가 닫혀 있던 때)",
}

func sentences() *past.Sentences {
	s := past.NewSentences("ko")
	s.TimeLayout("ko", "2006-01-02 15:04")

	// asset.go and register.go
	past.SayKind(s, "asset.add", "ko", func(r *past.Reading, ev past.EventRow, m *app.AssetAdded) string {
		return fmt.Sprintf("%s 등록", m.GetTag())
	})
	past.SayKind(s, "asset.add_many", "ko", func(r *past.Reading, ev past.EventRow, m *app.AssetsAdded) string {
		return fmt.Sprintf("대량 등록: %d개", m.GetCount())
	})
	past.SayKind(s, "asset.move", "ko", func(r *past.Reading, ev past.EventRow, m *app.AssetMoved) string {
		if len(m.GetTo()) == 0 {
			return fmt.Sprintf("%s 꺼냄", m.GetTag())
		}
		return fmt.Sprintf("%s → %s", m.GetTag(), m.GetToTag())
	})
	past.SayKind(s, "asset.set", "ko", func(r *past.Reading, ev past.EventRow, m *app.AssetAttributesSet) string {
		return fmt.Sprintf("%s 속성 변경", m.GetTag())
	})
	past.SayKind(s, "asset.assign", "ko", func(r *past.Reading, ev past.EventRow, m *app.AssetAssigned) string {
		whom := "-"
		if len(m.GetParty()) > 0 {
			whom = who(r, m.GetParty())
		}
		return fmt.Sprintf("%s %s: %s", m.GetTag(), or(stewardSay[m.GetRole()], m.GetRole()), whom)
	})
	past.SayKind(s, "asset.relate", "ko", func(r *past.Reading, ev past.EventRow, m *app.AssetRelated) string {
		verb := "연결"
		if m.GetEnd() {
			verb = "연결 끝"
		}
		return fmt.Sprintf("%s %s %s (%s)", m.GetTag(), verb, m.GetTargetTag(), or(relationSay[m.GetRelation()], m.GetRelation()))
	})
	// A fact by what the asset's field is called, and an attribute by the
	// label the asset's type gives it now ([attribute]).
	past.SayKind(s, "asset.correct", "ko", func(r *past.Reading, ev past.EventRow, m *app.AssetCorrected) string {
		what := factName(m.GetKey())
		switch idOf(m.GetRow()).Domain() {
		case pd.PlacementDomain:
			what = "배치"
		case pd.LinkDomain:
			what = "관계"
		case pd.StewardshipDomain:
			what = or(stewardSay[m.GetRole()], m.GetRole())
		default:
			if k, ok := strings.CutPrefix(m.GetKey(), attrPrefix); ok {
				what = attribute(r, m.GetAsset(), k)
			}
		}
		return fmt.Sprintf("%s %s 정정", m.GetTag(), what)
	})
	past.SayKind(s, "asset.void", "ko", func(r *past.Reading, ev past.EventRow, m *app.AssetVoided) string {
		return fmt.Sprintf("%s 등록 취소", m.GetTag())
	})

	// stock.go
	past.SayKind(s, "stock.add", "ko", func(r *past.Reading, ev past.EventRow, m *app.StockAdded) string {
		return fmt.Sprintf("재고 시작: %s @ %s", m.GetName(), m.GetSpaceName())
	})
	// A change by `delta`, in the word of the movement it records.
	for _, k := range []string{"stock.receive", "stock.consume", "stock.adjust"} {
		past.SayKind(s, k, "ko", func(r *past.Reading, ev past.EventRow, m *app.StockChanged) string {
			move := strings.TrimPrefix(k, "stock.")
			return fmt.Sprintf("재고 %s: %s %+d", or(movementSay[move], move), m.GetName(), m.GetDelta())
		})
	}
	past.SayKind(s, "stock.transfer", "ko", func(r *past.Reading, ev past.EventRow, m *app.StockTransferred) string {
		return fmt.Sprintf("재고 이동: %s %d → %s", m.GetName(), m.GetQuantity(), m.GetToSpaceName())
	})
	past.SayKind(s, "stock.convert", "ko", func(r *past.Reading, ev past.EventRow, m *app.StockConverted) string {
		return fmt.Sprintf("재고 → 자산: %s %d개", m.GetName(), m.GetQuantity())
	})

	// custody.go and spin.go
	past.SayKind(s, "custody.issue", "ko", func(r *past.Reading, ev past.EventRow, m *app.CustodyIssued) string {
		verb := "지급"
		if m.GetKind() == "loan" {
			verb = "대여"
		}
		return fmt.Sprintf("%s: %s", verb, who(r, m.GetParty()))
	})
	past.SayKind(s, "custody.acknowledge", "ko", func(r *past.Reading, ev past.EventRow, m *app.CustodyAcknowledged) string {
		return "인수 확인"
	})
	past.SayKind(s, "custody.extend", "ko", func(r *past.Reading, ev past.EventRow, m *app.CustodyExtended) string {
		return "반납 기한 연장: " + m.GetDue().AsTime().In(r.Zone()).Format(time.DateOnly)
	})
	// A loan the sweep found late, by the due it missed, to the minute: the
	// moment it was found so is the event's own. Said alike of the loan and
	// of each thing still out.
	past.SayKind(s, "custody.overdue", "ko", func(r *past.Reading, ev past.EventRow, m *app.CustodyOverdue) string {
		return "반납 기한 지남: " + r.Time(m.GetDue().AsTime())
	})
	past.SayKind(s, "custody.return", "ko", func(r *past.Reading, ev past.EventRow, m *app.CustodyReturned) string {
		return "반납"
	})

	// count.go
	past.SayKind(s, "count.start", "ko", func(r *past.Reading, ev past.EventRow, m *app.CountDelimited) string {
		return "실사 시작: " + m.GetName()
	})
	past.SayKind(s, "count.scan", "ko", func(r *past.Reading, ev past.EventRow, m *app.CountScanned) string {
		return "실사 스캔"
	})
	past.SayKind(s, "count.reconcile", "ko", func(r *past.Reading, ev past.EventRow, m *app.CountReconciled) string {
		return "실사 대조"
	})
	past.SayKind(s, "count.resolve", "ko", func(r *past.Reading, ev past.EventRow, m *app.CountResolved) string {
		return "실사 결과 처리: " + or(resolutionSay[m.GetResolution()], m.GetResolution())
	})
	past.SayKind(s, "count.close", "ko", func(r *past.Reading, ev past.EventRow, m *app.CountDelimited) string {
		return "실사 종료: " + m.GetName()
	})

	// booking.go and spin.go
	past.SayKind(s, "bookable.add", "ko", func(r *past.Reading, ev past.EventRow, m *app.BookableSaved) string {
		return m.GetTag() + " 예약 가능"
	})
	past.SayKind(s, "bookable.update", "ko", func(r *past.Reading, ev past.EventRow, m *app.BookableSaved) string {
		return "예약 정책 변경"
	})
	// A series by when it begins and how often it takes place: what each time
	// it takes place says of itself is when that is (D45).
	past.SayKind(s, "reservation.add", "ko", func(r *past.Reading, ev past.EventRow, m *app.ReservationAdded) string {
		when := m.GetBeginsAt().AsTime().In(r.Zone()).Format("01-02 15:04")
		if n := m.GetOccurrences(); n > 1 {
			return fmt.Sprintf("반복 예약: %s %s부터 %d회", m.GetAssetName(), when, n)
		}
		return fmt.Sprintf("예약: %s %s", m.GetAssetName(), when)
	})
	past.SayKind(s, "reservation.confirm", "ko", func(r *past.Reading, ev past.EventRow, m *app.ReservationConfirmed) string {
		return "예약 확정: " + m.GetName()
	})
	// A decision, by what was done, as the page's button for it names it --
	// an approval is not said as the state it moved the reservation to,
	// "확정", which is what a hold confirmed says.
	for k, did := range map[string]string{
		"reservation.approve": "승인",
		"reservation.reject":  "거절",
		"reservation.cancel":  "취소",
		"reservation.checkin": "체크인",
	} {
		past.SayKind(s, k, "ko", func(r *past.Reading, ev past.EventRow, m *app.ReservationDecided) string {
			return "예약 " + did + ": " + m.GetName()
		})
	}
	past.SayKind(s, "reservation.complete", "ko", func(r *past.Reading, ev past.EventRow, m *app.ReservationDecided) string {
		if m.GetEnded() {
			return "예약 종료: " + m.GetName()
		}
		return "예약 " + reservationSay[resCompleted] + ": " + m.GetName()
	})
	// By what it was, and -- when nobody could act on it in time, its
	// workspace read-only or closed -- by why, which was nobody's doing: a
	// confirmed one nobody could check in to or pick up is said so, and no
	// no-show (D68).
	past.SayKind(s, "reservation.expire", "ko", func(r *past.Reading, ev past.EventRow, m *app.ReservationReleased) string {
		what := "홀드 만료: "
		switch m.GetFrom() {
		case resRequested:
			what = "승인 대기 만료: "
		case resConfirmed:
			what = "예약 만료: "
		}
		return what + m.GetName() + whileSay[m.GetWorkspace()]
	})
	past.SayKind(s, "reservation.no_show", "ko", func(r *past.Reading, ev past.EventRow, m *app.ReservationReleased) string {
		return "노쇼: " + m.GetName()
	})
	// What a reservation holds of a thing it needs, as it follows what the
	// thing can give -- a member of its kit, or a pool's units -- taken,
	// waited for, taken once free, or let go of. Said alike in the
	// reservation's history and the thing's, so by both: the thing, by how
	// many of it where it is counted, and the reservation by its name and
	// when it begins, which tells one of a series from the next.
	for k, did := range map[string]string{
		"reservation.take":    "예약에 잡힘",
		"reservation.wait":    "예약 대기",
		"reservation.resolve": "예약 확보",
		"reservation.let_go":  "예약에서 빠짐",
	} {
		past.SayKind(s, k, "ko", func(r *past.Reading, ev past.EventRow, m *app.ReservationAllocationChanged) string {
			what := m.GetAssetName()
			if n := m.GetUnits(); n > 0 {
				what = fmt.Sprintf("%s %d개", what, n)
			}
			when := m.GetBeginsAt().AsTime().In(r.Zone()).Format("01-02 15:04")
			return fmt.Sprintf("%s: %s (%s)", did, what, strings.TrimSpace(m.GetName()+" "+when))
		})
	}

	// label.go and spin.go
	past.SayKind(s, "domain.add", "ko", func(r *past.Reading, ev past.EventRow, m *app.TenantDomainAdded) string {
		return domainWords(m.GetPurpose()) + " 등록: " + m.GetHost()
	})
	past.SayKind(s, "domain.verify", "ko", func(r *past.Reading, ev past.EventRow, m *app.TenantDomainVerified) string {
		if m.GetAgain() {
			return domainWords(m.GetPurpose()) + " 다시 확인: " + m.GetHost()
		}
		return domainWords(m.GetPurpose()) + " 확인: " + m.GetHost()
	})
	past.SayKind(s, "domain.activate", "ko", func(r *past.Reading, ev past.EventRow, m *app.TenantDomainActivated) string {
		return domainWords(m.GetPurpose()) + " 활성화: " + m.GetHost()
	})
	past.SayKind(s, "domain.retire", "ko", func(r *past.Reading, ev past.EventRow, m *app.TenantDomainRetired) string {
		return domainWords(m.GetPurpose()) + " 은퇴: " + m.GetHost()
	})
	// Why it lapsed is the deployment's to say, here, where it once wrote
	// it as the event's reason -- which is a person's words now.
	past.SayKind(s, "domain.lapse", "ko", func(r *past.Reading, ev past.EventRow, m *app.TenantDomainLapsed) string {
		return fmt.Sprintf("%s 사용 중지: %s (DNS가 %s부터 이 서비스를 가리키지 않거나 TXT 레코드가 없습니다)",
			domainWords(m.GetPurpose()), m.GetHost(), m.GetFailingSince().AsTime().In(r.Zone()).Format("1월 2일 15:04"))
	})
	past.SayKind(s, "label.print", "ko", func(r *past.Reading, ev past.EventRow, m *app.LabelsPrinted) string {
		return "라벨 인쇄: " + m.GetBatch()
	})
	past.SayKind(s, "label.bind", "ko", func(r *past.Reading, ev past.EventRow, m *app.LabelBound) string {
		return m.GetTag() + " 라벨 부착"
	})
	past.SayKind(s, "label.unbind", "ko", func(r *past.Reading, ev past.EventRow, m *app.LabelUnbound) string {
		return "라벨 떼어냄"
	})
	past.SayKind(s, "attachment.add", "ko", func(r *past.Reading, ev past.EventRow, m *app.AttachmentAdded) string {
		return "파일 첨부: " + m.GetName()
	})

	// org.go, depart.go and workspace.go
	past.SayKind(s, "party.add", "ko", func(r *past.Reading, ev past.EventRow, m *app.PartySaved) string {
		return "추가: " + who(r, m.GetParty())
	})
	past.SayKind(s, "party.update", "ko", func(r *past.Reading, ev past.EventRow, m *app.PartySaved) string {
		return "수정: " + who(r, m.GetParty())
	})
	past.SayKind(s, "party.invite", "ko", func(r *past.Reading, ev past.EventRow, m *app.PartyInvited) string {
		return fmt.Sprintf("로그인 발급: %s (%s)", who(r, m.GetParty()), alias(r, m.GetHolder()))
	})
	past.SayKind(s, "party.role", "ko", func(r *past.Reading, ev past.EventRow, m *app.PartyRoleSet) string {
		switch m.GetBy() {
		case "succession":
			return fmt.Sprintf("소유자 승계: %s (마지막 소유자의 계정 탈퇴)", who(r, m.GetHolder()))
		case "operator":
			return fmt.Sprintf("소유자 지정: %s (운영자)", who(r, m.GetHolder()))
		}
		return fmt.Sprintf("역할 변경: %s → %s", who(r, m.GetParty()), or(roleWords[m.GetRole()], m.GetRole()))
	})
	past.SayKind(s, "party.password", "ko", func(r *past.Reading, ev past.EventRow, m *app.PartyPasswordSet) string {
		return "비밀번호 변경"
	})
	past.SayKind(s, "party.deactivate", "ko", func(r *past.Reading, ev past.EventRow, m *app.PartyDeactivated) string {
		return "로그인 비활성화: " + who(r, m.GetParty())
	})
	past.SayKind(s, "party.pseudonymize", "ko", func(r *past.Reading, ev past.EventRow, m *app.PartyPseudonymized) string {
		return "개인정보 삭제"
	})
	past.SayKind(s, "type.add", "ko", func(r *past.Reading, ev past.EventRow, m *app.AssetTypeSaved) string {
		return "유형 추가: " + m.GetName()
	})
	past.SayKind(s, "type.update", "ko", func(r *past.Reading, ev past.EventRow, m *app.AssetTypeSaved) string {
		return "유형 수정: " + m.GetName()
	})
	past.SayKind(s, "model.add", "ko", func(r *past.Reading, ev past.EventRow, m *app.ItemModelSaved) string {
		return "모델 추가: " + strings.TrimSpace(m.GetMaker()+" "+m.GetName())
	})
	past.SayKind(s, "model.update", "ko", func(r *past.Reading, ev past.EventRow, m *app.ItemModelSaved) string {
		return "모델 수정: " + m.GetName()
	})
	past.SayKind(s, "account.depart", "ko", func(r *past.Reading, ev past.EventRow, m *app.AccountDeparted) string {
		return "계정 탈퇴: 개인정보 삭제"
	})
	past.SayKind(s, "workspace.configure", "ko", func(r *past.Reading, ev past.EventRow, m *app.WorkspaceConfigured) string {
		if !m.HasName() {
			return "워크스페이스 설정 변경"
		}
		// Its name, said alone as the page says it, or beside the rest. None
		// is called after its owner (D57), whose name is not said here.
		alone := !m.HasTimezone() && !m.HasCurrency() && !m.HasNoShowMinutes() &&
			!m.HasKeepDays() && !m.HasAccessKeepDays() && !m.HasRestoreDays()
		switch {
		case alone && m.GetName() == "":
			return "워크스페이스 이름 지움 (소유자의 이름으로 부름)"
		case alone:
			return "워크스페이스 이름 변경: " + m.GetName()
		case m.GetName() == "":
			return "워크스페이스 설정 변경 (이름 지움)"
		}
		return "워크스페이스 설정 변경 (이름: " + m.GetName() + ")"
	})
	past.SayKind(s, "workspace.close", "ko", func(r *past.Reading, ev past.EventRow, m *app.WorkspaceClosed) string {
		switch m.GetBy() {
		case ClosedByOperator:
			return "워크스페이스 닫기 (운영자)"
		case ClosedByDeployment:
			return "워크스페이스 닫기 (남은 구성원 없음)"
		}
		return "워크스페이스 닫기"
	})
	past.SayKind(s, "workspace.restore", "ko", func(r *past.Reading, ev past.EventRow, m *app.WorkspaceRestored) string {
		return "워크스페이스 복구"
	})
	past.SayKind(s, "workspace.import", "ko", func(r *past.Reading, ev past.EventRow, m *app.WorkspaceImported) string {
		return fmt.Sprintf("워크스페이스 복구 (운영자): %s 내보내기에서", m.GetExportedAt().AsTime().Format(time.DateOnly))
	})

	// invitation.go
	past.SayKind(s, "invitation.add", "ko", func(r *past.Reading, ev past.EventRow, m *app.InvitationAdded) string {
		return "초대 (" + roleWords[m.GetRole()] + ")"
	})
	past.SayKind(s, "invitation.claim", "ko", func(r *past.Reading, ev past.EventRow, m *app.InvitationChanged) string {
		return "초대 링크로 받음"
	})
	past.SayKind(s, "invitation.accept", "ko", func(r *past.Reading, ev past.EventRow, m *app.InvitationAccepted) string {
		return "초대 수락 (" + roleWords[m.GetRole()] + ")"
	})
	past.SayKind(s, "invitation.deny", "ko", func(r *past.Reading, ev past.EventRow, m *app.InvitationChanged) string {
		return "초대 거절"
	})
	past.SayKind(s, "invitation.revoke", "ko", func(r *past.Reading, ev past.EventRow, m *app.InvitationChanged) string {
		return "초대 취소"
	})

	// work.go, access.go and expire.go
	past.SayKind(s, "work.open", "ko", func(r *past.Reading, ev past.EventRow, m *app.WorkOrderSaved) string {
		return fmt.Sprintf("작업 열림: %s (%s)", m.GetName(), m.GetTag())
	})
	past.SayKind(s, "work.update", "ko", func(r *past.Reading, ev past.EventRow, m *app.WorkOrderSaved) string {
		return "작업 수정: " + m.GetName()
	})
	past.SayKind(s, "work.done", "ko", func(r *past.Reading, ev past.EventRow, m *app.WorkOrderFinished) string {
		return "작업 완료: " + m.GetName()
	})
	past.SayKind(s, "work.cancelled", "ko", func(r *past.Reading, ev past.EventRow, m *app.WorkOrderFinished) string {
		return "작업 취소: " + m.GetName()
	})
	past.SayKind(s, "purchase.add", "ko", func(r *past.Reading, ev past.EventRow, m *app.PurchaseAdded) string {
		return "구매 등록: " + m.GetName()
	})
	past.SayKind(s, "purchase.receive", "ko", func(r *past.Reading, ev past.EventRow, m *app.PurchaseReceived) string {
		return "입고: " + m.GetName()
	})
	past.SayKind(s, "access.review", "ko", func(r *past.Reading, ev past.EventRow, m *app.AccessReviewed) string {
		z := r.Zone()
		period := fmt.Sprintf("%s – %s",
			m.GetFrom().AsTime().In(z).Format(time.DateOnly),
			m.GetTo().AsTime().Add(-time.Nanosecond).In(z).Format(time.DateOnly))
		return fmt.Sprintf("접속기록 점검: %s, 호출 %d건 (내려받기 %d건, 거부 %d건)", period, m.GetCalls(), m.GetDownloads(), m.GetRefused())
	})
	past.SayKind(s, "retention.expired", "ko", func(r *past.Reading, ev past.EventRow, m *app.RetentionExpired) string {
		total := m.GetPlacements() + m.GetLinks() + m.GetStewardships() + m.GetFacts() + m.GetEvents() +
			m.GetReservations() + m.GetCustodies() + m.GetCounts() + m.GetWorkOrders() + m.GetStockMovements() +
			m.GetAttachments()
		return fmt.Sprintf("보존 기간이 지난 이력 %d건 삭제 (%s 이전)", total, m.GetBefore().AsTime().In(r.Zone()).Format("2006년 1월 2일"))
	})

	// An event's reason taken out (payday#151's Redact): what was taken out
	// is the redacted event's reason, and the request it answers this one's.
	past.SayKind(s, "event.redact", "ko", func(r *past.Reading, ev past.EventRow, m *app.EventRedacted) string {
		return "작업 기록의 사유 삭제"
	})

	// A row from before Loft's events were the past's: the sentence it was
	// written with, and the people it marks named as they are now.
	s.Legacy(func(r *past.Reading, ev past.EventRow) string {
		n := namerOf(r.Context())
		if n == nil {
			return ""
		}
		v, err := n.legacy(r.Context(), ev.Id)
		if err != nil {
			r.Fail(err)
			return ""
		}
		return n.say(v.desc, v.payload)
	})

	return s
}

// who is the person or membership `b` names, as a sentence says one: as they
// are called when it is read -- a membership by the person record it is, as
// whoever did something is named -- each on the access record of the call
// that reads it; and "?" where nothing names it, as Loft has always said one.
func who(r *past.Reading, b []byte) string {
	id := idOf(b)
	if id.IsZero() {
		return "?"
	}
	if v := r.Name(id); v != id.String() {
		return v
	}
	return "?"
}

// alias is a membership by the name it signs in with, `@` and it, or the
// pseudonym of somebody forgotten: what the namer the reading is for says,
// which puts whose it is on the access record as [who] does.
func alias(r *past.Reading, b []byte) string {
	n := namerOf(r.Context())
	id := idOf(b)
	if n == nil || id.IsZero() {
		return "?"
	}
	return n.member(id.Uuid())
}

// attribute is an asset's attribute `key` as a sentence says it: by the label
// the asset's type gives it now, which the namer the reading is for reads
// ([namer.attribute]), and by the key where the type gives it none -- the
// workspace's own word for it, never Loft's code.
func attribute(r *past.Reading, asset []byte, key string) string {
	n := namerOf(r.Context())
	id := idOf(asset)
	if n == nil || id.IsZero() {
		return key
	}
	v, err := n.attribute(r.Context(), id.Uuid(), key)
	if err != nil {
		r.Fail(err)
		return key
	}
	return v
}

// namerKey is the namer of the answer being said, on the context its reading
// is for: what a sentence that says more than [past.Reading.Name] does --
// a membership's alias, a row from before -- reads with.
type namerKey struct{}

func withNamer(ctx context.Context, n *namer) context.Context {
	return context.WithValue(ctx, namerKey{}, n)
}

func namerOf(ctx context.Context) *namer {
	n, _ := ctx.Value(namerKey{}).(*namer)
	return n
}

// reading is how an answer of this layer says its events: in Korean, in the
// workspace's zone, its people named by `n` as they are called now.
func (t *Tx) reading(n *namer) *past.Reading {
	return Sentences.Read(withNamer(t.ctx, n), n, "ko", t.zone())
}

// said is a row of Loft's Event as payday's past reads one.
func said(v *ent.Event) past.EventRow {
	r := past.EventRow{
		Id:       pdid.Id(v.Id),
		Tenant:   pdid.Id(v.TenantId),
		Kind:     v.Kind,
		At:       v.OccurredAt,
		Reason:   v.Reason,
		Trace:    v.TraceId,
		Value:    v.Value,
		Recorded: v.DateCreated,
	}
	if v.ActorId != nil {
		r.Actor = pdid.Id(*v.ActorId)
	}
	if v.SubjectId != nil {
		r.Subject = pdid.Id(*v.SubjectId)
	}
	if v.CauseId != nil {
		r.Cause = pdid.Id(*v.CauseId)
	}
	return r
}

// legacyRow is what a row from before kept of what it said: the sentence,
// and what it named.
type legacyRow struct{ desc, payload string }

// legacy is what the row `id` said, read once: off what the answer read of
// it already, or else off the row.
func (n *namer) legacy(ctx context.Context, id pdid.Id) (legacyRow, error) {
	if v, ok := n.legacies[id.Uuid()]; ok {
		return v, nil
	}
	v, err := n.t.db.Event.Query().
		Where(event.Id(id.Uuid()), event.TenantId(n.t.tenant.Uuid())).
		Select(event.FieldDesc, event.FieldPayload).
		Only(ctx)
	if err != nil {
		return legacyRow{}, err
	}
	n.knows(v.Id, v.Desc, v.Payload)
	return n.legacies[v.Id], nil
}

// before reads what the rows from before among `rows` said, the ones it does
// not know yet, in one read: a row that holds no message is one of them.
func (n *namer) before(rows ...map[uuid.UUID]past.EventRow) error {
	var ids []uuid.UUID
	for _, m := range rows {
		for k, v := range m {
			if _, ok := n.legacies[k]; !ok && v.Value == nil {
				ids = append(ids, k)
			}
		}
	}
	for page := range slices.Chunk(ids, 500) {
		vs, err := n.t.db.Event.Query().
			Where(event.IdIn(page...), event.TenantId(n.t.tenant.Uuid())).
			Select(event.FieldId, event.FieldDesc, event.FieldPayload).
			All(n.t.ctx)
		if err != nil {
			return err
		}
		for _, v := range vs {
			n.knows(v.Id, v.Desc, v.Payload)
		}
	}
	return nil
}

// knows is what a row from before said, read with it.
func (n *namer) knows(id uuid.UUID, desc, payload string) {
	if n.legacies == nil {
		n.legacies = map[uuid.UUID]legacyRow{}
	}
	n.legacies[id] = legacyRow{desc, payload}
}
