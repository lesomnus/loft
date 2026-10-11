/**
 * The words a person reads for the values the server keeps.
 *
 * @module
 */

export const kindWord: Record<string, string> = { item: '물품', space: '공간', kit: '키트', group: '그룹' }

export const statusWord: Record<string, string> = {
	ordered: '주문',
	active: '사용중',
	in_repair: '수리중',
	lost: '분실',
	retired: '불용',
	disposed: '폐기',
}

export const conditionWord: Record<string, string> = { good: '양호', damaged: '손상', broken: '고장' }

export const modeWord: Record<string, string> = { located: '놓임', installed: '장착', part: '구성품' }

export const roleWord: Record<string, string> = {
	owner: '소유자',
	admin: '관리자',
	manager: '매니저',
	member: '구성원',
	auditor: '감사자',
}

export const partyKindWord: Record<string, string> = { person: '사람', team: '팀', org: '조직', vendor: '거래처' }

export const stewardWord: Record<string, string> = { owner: '소유', manager: '관리 담당', custodian: '보유' }

export const reservationWord: Record<string, string> = {
	held: '임시 홀드',
	requested: '승인 대기',
	confirmed: '확정',
	in_use: '이용 중',
	completed: '완료',
	cancelled: '취소',
	rejected: '거절',
	expired: '만료',
	no_show: '노쇼',
}

export const custodyWord: Record<string, string> = { open: '진행 중', returned: '반납 완료' }

export const custodyKindWord: Record<string, string> = { issue: '지급', loan: '대여' }

export const workKindWord: Record<string, string> = {
	repair: '수리',
	inspection: '점검',
	maintenance: '정비',
	rma: 'RMA',
}

export const workStatusWord: Record<string, string> = {
	open: '접수',
	scheduled: '예정',
	in_progress: '진행 중',
	done: '완료',
	cancelled: '취소',
}

export const purchaseWord: Record<string, string> = { ordered: '주문', received: '입고 완료', cancelled: '취소' }

export const countWord: Record<string, string> = { open: '진행 중', closed: '종료' }

export const findingWord: Record<string, string> = {
	seen: '확인',
	misplaced: '위치 다름',
	unknown: '미등록',
	missing: '미발견',
}

export const resolutionWord: Record<string, string> = {
	open: '미처리',
	ok: '정상',
	moved: '위치 반영',
	lost: '분실 처리',
	ignored: '무시',
}

export const movementWord: Record<string, string> = {
	receive: '입고',
	consume: '사용',
	adjust: '조정',
	issue: '지급',
	return: '반납',
	transfer_in: '이동 입고',
	transfer_out: '이동 출고',
	convert: '자산 전환',
}

export const labelWord: Record<string, string> = { new: '미부착', bound: '부착', void: '폐기' }

export const domainWord: Record<string, string> = {
	pending: '확인 대기',
	ready: '확인됨',
	active: '사용 중',
	legacy: '이전 주소',
	retired: '중지',
}

export const attrTypeWord: Record<string, string> = {
	text: '글',
	number: '숫자',
	bool: '예/아니오',
	date: '날짜',
	enum: '선택',
}

export function word(map: Record<string, string>, v: string | undefined): string {
	if (v === undefined || v === '') return '-'
	return map[v] ?? v
}

/** Which tone a status is drawn in. */
export function tone(v: string): 'ok' | 'warn' | 'bad' | 'mute' | 'info' {
	switch (v) {
		case 'active':
		case 'good':
		case 'confirmed':
		case 'done':
		case 'received':
		case 'returned':
		case 'ok':
		case 'seen':
		case 'bound':
		case 'accepted':
			return 'ok'
		case 'in_repair':
		case 'damaged':
		case 'requested':
		case 'held':
		case 'scheduled':
		case 'in_progress':
		case 'misplaced':
		case 'pending':
		case 'open':
			return 'warn'
		case 'lost':
		case 'broken':
		case 'rejected':
		case 'no_show':
		case 'missing':
		case 'unknown':
		case 'denied':
			return 'bad'
		case 'in_use':
		case 'ordered':
		case 'ready':
			return 'info'
		default:
			return 'mute'
	}
}
