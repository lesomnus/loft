/**
 * How a workspace is paid for (design 7.9, levyan#14): what levyan, the
 * billing service, says of it, relayed by `loft.PaymentService` and kept
 * nowhere here.
 *
 * - **A workspace's**, in its settings for the people who run it: who pays for
 *   it, the credit levyan's operator granted it, what it has used this period
 *   and what that would come to -- with the allowances and the pools the
 *   operator gave it, and what each line was given and took (`Lines`) -- its
 *   statements, as lines too, and its owner asking a member to pay.
 * - **The banner** a workspace shows while nobody pays for it, once it is
 *   restricted and once it is read-only, and -- to the people who run it --
 *   while the billing account it is paid for with cannot be collected from:
 *   what to do, and where.
 *
 * What a payer does with what it pays with -- its billing accounts, the
 * checkout, an offer answered, its invoices -- is not here: it is where a
 * payer pays, the portal's (#326), and nothing here links to it (D81).
 *
 * Its dates are on the workspace's wall, and never on the browser's. What
 * levyan's operator gives (levyan#43, design 7.11) is only shown here:
 * nothing grants any of it.
 *
 * @module
 */

import { useMemo, useState, type ReactNode } from 'react'
import { Link } from 'react-router-dom'
import type { Timestamp } from '@bufbuild/protobuf/wkt'

import {
	BillingCollectionState,
	BillingDeclineKind,
	BillingLineKind,
	type BillingAllowance,
	type BillingCollection,
	type BillingCredit,
	type BillingDunning,
	type BillingLine,
	type BillingPayer,
	type BillingPeriod,
	type BillingPool,
	type BillingStatement,
	type BillingUsage,
} from '../gen/loft/payment_pb.js'
import { HolderService, PaymentService, dateOf, fmt, idStr, money, type Session } from './api.js'
import { useCatalog } from './catalog.js'
import { useAct, useReadOnly, useRestricted, useRpc } from './rpc.js'
import { Badge, Card, Empty, Field, Kv, Load, Modal, Select } from './ui.js'
import { roleWord } from './words.js'

/** Whether what a billing account is billed can be collected, as a person says it, and the badge tone of a status like it (`tone`). */
const collectionWord: Record<number, [string, string]> = {
	[BillingCollectionState.COLLECTABLE]: ['결제 가능', 'ok'],
	[BillingCollectionState.NO_SUBSCRIPTION]: ['결제수단 없음', 'pending'],
	[BillingCollectionState.PAST_DUE]: ['결제 실패, 다시 시도 중', 'rejected'],
	[BillingCollectionState.PAUSED]: ['일시정지', 'pending'],
	[BillingCollectionState.CANCELED]: ['구독 끝남', 'rejected'],
	[BillingCollectionState.NEEDS_ACTION]: ['확인이 필요함', 'pending'],
}

/** What each not collectable state asks of the payer. */
const collectionDo: Record<number, string> = {
	[BillingCollectionState.NO_SUBSCRIPTION]: '결제수단을 등록하세요.',
	[BillingCollectionState.PAST_DUE]: '결제가 실패해 Paddle이 다시 시도하고 있습니다. Paddle이 보낸 메일에서 결제수단을 바꿀 수 있습니다.',
	[BillingCollectionState.PAUSED]: '구독이 일시정지되었습니다.',
	[BillingCollectionState.CANCELED]: '구독이 끝났습니다. 결제수단을 다시 등록하세요.',
	[BillingCollectionState.NEEDS_ACTION]: '결제가 확인을 기다립니다(카드사 앱의 승인 같은 것). Paddle이 보낸 안내를 따르세요.',
}

/**
 * What is done of an invoice whose charge failed, as the people who run a
 * workspace it bills are told it (levyan's `docs/apps.md`, 6): by the kind of
 * its latest decline, what to do and by when -- when it is charged again,
 * from when every workspace it bills is restricted and from when it is
 * read-only, or that it is -- and nothing levyan did not say.
 */
function dunningSaid(d: BillingDunning): string {
	const failed = fmt(d.dateFailed, 'date')
	const now = new Date()
	const from = dateOf(d.dateRestricted)
	const passed = from !== undefined && from <= now
	const readOnly = dateOf(d.dateReadonly)
	const stage2 = readOnly === undefined ? '' : readOnly <= now ? ` ${fmt(readOnly, 'date')}부터 읽기 전용입니다.` : ` ${fmt(readOnly, 'date')}부터는 읽기 전용이 됩니다.`
	const restricts =
		(from === undefined ? '' : passed ? `${fmt(from, 'date')}부터 제한되었습니다.` : `${fmt(from, 'date')}까지 결제되지 않으면 제한됩니다.`) +
		stage2 +
		(passed ? ' 결제되면 곧 풀립니다.' : '')
	switch (d.kind) {
		case BillingDeclineKind.FRAUD:
			return `${failed}에 결제가 거절되어 제한되었습니다. 다른 결제수단을 써 주세요.${stage2}`
		case BillingDeclineKind.REPLACE:
			return `${failed}에 결제가 실패했습니다. 결제수단을 바꿔 주세요. ${restricts}`
		default:
			return d.dateNextRetry !== undefined
				? `${failed}에 결제가 실패했습니다. ${fmt(d.dateNextRetry)}에 다시 시도합니다. ${restricts}`
				: `${failed}에 결제가 실패했고, 다시 시도할 일정이 남지 않았습니다. 결제수단을 바꾸면 바로 다시 청구합니다. ${restricts}`
	}
}

/** What each meter is called. */
const meterWord: Record<string, string> = {
	'loft.assets.active': '활성 자산',
	'loft.attachments.bytes': '첨부 저장량',
	'loft.members': '구성원',
	'loft.history.retention_days': '이력 보존 기간',
}

/**
 * What each meter reads in (`proto/loft/meter.proto`, and `retention.Meter`),
 * for what levyan says of one beside the lines, which say theirs: an
 * allowance, a pool.
 */
const meterUnit: Record<string, string> = {
	'loft.assets.active': '{asset}',
	'loft.attachments.bytes': 'By',
	'loft.members': '{member}',
	'loft.history.retention_days': 'd',
}

/** A period, as the days it covers: its end is the first moment after it. */
function periodText(p: BillingPeriod | undefined): string {
	const start = dateOf(p?.dateStart)
	const end = dateOf(p?.dateEnd)
	if (start === undefined || end === undefined) return ''
	return `${fmt(start, 'date')} – ${fmt(new Date(end.getTime() - 1), 'date')}`
}

/**
 * A decimal levyan writes -- "338.709677", exact to six digits after the
 * point and cut there -- as millionths of it, exactly; one it did not write,
 * as a levyan from before it said any leaves it, is nothing.
 */
function micros(v: string): bigint {
	const m = /^(-?)(\d+)(?:\.(\d{1,6}))?$/.exec(v.trim())
	if (m === null) return 0n
	const n = BigInt((m[2] ?? '0') + (m[3] ?? '').padEnd(6, '0'))
	return m[1] === '-' ? -n : n
}

/**
 * Millionths as a person reads them, with `unit` after: to the first digit
 * after the point, rounded half up, with their thousands -- "1,212.9개" --
 * and an amount that is not nothing but would round to it, as less than the
 * least this shows, "0.1개 미만", rather than as nothing.
 */
function tenths(n: bigint, unit: string): string {
	const neg = n < 0n
	const a = neg ? -n : n
	const t = (a + 50_000n) / 100_000n
	if (t === 0n) return a === 0n || neg ? `0${unit}` : `0.1${unit} 미만`
	return `${neg ? '-' : ''}${(Number(t) / 10).toLocaleString('ko-KR', { maximumFractionDigits: 1 })}${unit}`
}

/** A quantity of a meter's unit, in millionths, as a line says it (`tenths`): 개, 명, 일, or bytes. */
function quantity(n: bigint, unit: string): string {
	switch (unit) {
		case '{asset}':
			return tenths(n, '개')
		case '{member}':
			return tenths(n, '명')
		case 'd':
			return tenths(n, '일')
		case 'By':
			return bytes(Number(n) / 1e6)
		default:
			return tenths(n, ` ${unit}`)
	}
}

/**
 * A pool's quantity, in millionths: of its meter's unit for a period, which
 * is a month, as `quantity` says the unit -- every meter of Loft's is a
 * stock, so an asset-month is an asset held for a month, "8,800 자산·월".
 */
function poolQuantity(n: bigint, unit: string): string {
	switch (unit) {
		case '{asset}':
			return tenths(n, ' 자산·월')
		case '{member}':
			return tenths(n, ' 구성원·월')
		case 'By':
			return `${bytes(Number(n) / 1e6)}·월`
		default:
			return tenths(n, ` ${unit}·월`)
	}
}

/** What a line used, in its unit (`quantity`): a history kept for none of its days is kept forever. */
function usageText(l: BillingLine): string {
	const n = micros(l.usage)
	return l.unit === 'd' && n === 0n ? '영원히' : quantity(n, l.unit)
}

/** Bytes as a person reads them, by the largest of the units it is at least one of, to the first digit after the point. */
function bytes(n: number): string {
	const units = ['B', 'KB', 'MB', 'GB', 'TB']
	let v = Math.abs(n)
	let i = 0
	while (v >= 1024 && i < units.length - 1) {
		v /= 1024
		i++
	}
	const s = v.toLocaleString('ko-KR', { maximumFractionDigits: 1 })
	if (s === '0' && n !== 0) return '0.1 B 미만'
	return `${n < 0 ? '-' : ''}${s} ${units[i]}`
}

/**
 * The last day of a stretch that ends at `end` -- the first moment it is
 * over -- as the page says a day: what is in force until 2027-05-01 00:00 is
 * "2027. 4. 30.까지".
 */
function lastDay(end: Timestamp | undefined): string {
	const d = dateOf(end)
	return d === undefined ? '' : fmt(new Date(d.getTime() - 1), 'date')
}

/** How long before a credit expires the page says it soon will (levyan's `docs/apps.md`, 7, has the app say). */
const expiresSoon = 30 * 24 * 3600_000

/** Whether `c` expires within `expiresSoon`. */
function soonGone(c: BillingCredit, now: Date): boolean {
	const d = dateOf(c.dateExpires)
	return d !== undefined && d.getTime() - now.getTime() <= expiresSoon
}

/** What is left of a credit, of what was granted, and until when: "70,000원 남음 (100,000원 중, 2027. 10. 31.까지)". */
function creditText(c: BillingCredit): string {
	const until = lastDay(c.dateExpires)
	return `${money(c.left, c.currency)} 남음 (${money(c.granted, c.currency)} 중${until === '' ? '' : `, ${until}까지`})`
}

/** A line's amount, which is a decimal of the currency's minor unit: whole units of it, rounded down as the total is. */
function lineAmount(a: string, currency: string): string {
	const n = Number(a)
	return Number.isFinite(n) ? money(Math.floor(n), currency) : a
}

/** A collection's state as a badge; nothing for a channel no subscription collects on. */
function CollectionBadge(props: { c: BillingCollection | undefined }): ReactNode {
	const w = props.c === undefined ? undefined : collectionWord[props.c.state]
	if (w === undefined) return null
	return <Badge v={w[1]}>{w[0]}</Badge>
}

// ---------------------------------------------------------------------------
// A workspace's, in its settings.

/** A member who signs in: their membership, what they are called, and told apart by (D54). */
type Member = { holder: Uint8Array; name: string; called: string; role: string; leaving: boolean }

/**
 * The workspace's members, by their identifier: the holder at roster each is
 * (D85), which levyan names a payer by.
 */
function useMembers(): Map<string, Member> {
	const c = useCatalog()
	const holders = useRpc(HolderService.method.list, { size: 500 })
	return useMemo(() => {
		const out = new Map<string, Member>()
		for (const h of holders.data?.items ?? []) {
			const id = idStr(h.id)
			const party = c.parties.find((p) => idStr(p.holder?.id) === id)
			const name = party?.name || h.name || h.alias
			out.set(id, {
				holder: h.id,
				name,
				called: (party !== undefined && c.called(party.id)) || name,
				role: h.role,
				leaving: h.dateLeaving !== undefined,
			})
		}
		return out
	}, [holders.data, c])
}

/**
 * Who pays for the workspace, the credit levyan's operator granted it, what
 * it used this period -- with what the operator gave it beyond what its
 * prices include -- and its statements; and, for its owner, asking a member
 * to pay. What a payer does with what it pays with is not here (D81).
 */
export function PaymentTab(props: { session: Session }): ReactNode {
	const c = useCatalog()
	const payer = useRpc(PaymentService.method.getPayer, {})
	const usage = useRpc(PaymentService.method.getUsage, {})
	const statements = useRpc(PaymentService.method.listStatements, {})
	const members = useMembers()
	const nameOf = (account: string | undefined): string => {
		if (account === undefined || account === '') return ''
		if (account === props.session.id) return '나'
		return members.get(account)?.name ?? '이 워크스페이스에 없는 사람'
	}
	const credits = payer.data?.payer?.credits ?? []
	return (
		<>
			<Card title="결제 담당">
				<Load q={payer}>{(v) => <PayerSaid payer={v.payer} nameOf={nameOf} />}</Load>
			</Card>
			{c.can('owner') && <Load q={payer}>{(v) => <OfferCard payer={v.payer} members={members} me={props.session.id} />}</Load>}
			<WorkspaceCredit credits={credits} />
			<Card title="이번 기간 사용량과 예상 금액">
				<Load q={usage}>{(v) => (v.usage === undefined ? <Empty /> : <UsageSaid usage={v.usage} credits={credits} />)}</Load>
			</Card>
			<Card title="지난 명세서">
				<Load q={statements}>{(v) => <Statements statements={v.statements} />}</Load>
			</Card>
		</>
	)
}

/**
 * The credit levyan's operator granted the workspace itself that is left
 * (`GetPayer`'s; levyan's `docs/apps.md`, 7): what its lines are paid with
 * first, whichever billing account's invoice they are on, the soonest to
 * expire first -- and whether one expires soon. What a billing account was
 * granted is its payer's to see, and not here. Nothing for a workspace with
 * none, and nothing here grants any.
 */
function WorkspaceCredit(props: { credits: BillingCredit[] }): ReactNode {
	if (props.credits.length === 0) return null
	const now = new Date()
	return (
		<Card title="크레딧">
			<ul className="credits">
				{props.credits.map((c) => (
					<li key={c.id}>
						{creditText(c)}
						{soonGone(c, now) && (
							<>
								{' '}
								<Badge v="pending">곧 만료</Badge>
							</>
						)}
					</li>
				))}
			</ul>
			<p className="small">
				결제 서비스가 이 워크스페이스에 준 크레딧입니다. 청구서를 확정할 때, 어느 결제 계정의 청구서든 이 워크스페이스의 줄에서 먼저 빼고 만료가 빠른 것부터 씁니다.
			</p>
		</Card>
	)
}

/**
 * What the workspace used this period and what that would come to: what
 * levyan's operator gave it beyond what its prices include, in force this
 * period (`Given`), and on each line what that gave and took (`Lines`); and
 * the estimate, which is after both and before any credit -- an invoice
 * spends that as it is finalized, and the page says so where the workspace
 * has some (levyan's `docs/apps.md`, 7).
 */
function UsageSaid(props: { usage: BillingUsage; credits: BillingCredit[] }): ReactNode {
	const u = props.usage
	// Of the estimate's currency alone: two are never added together.
	const left = props.credits.filter((c) => c.currency === u.currency).reduce((n, c) => n + c.left, 0n)
	return (
		<>
			<p className="mute small">
				{periodText(u.period)} · {fmt(u.dateAsOf)} 기준. 기간이 끝나면 결제 서비스가 확정합니다.
			</p>
			<Given usage={u} />
			{u.lines.length === 0 ? <Empty>아직 잰 것이 없습니다.</Empty> : <Lines lines={u.lines} currency={u.currency} />}
			{u.currency === '' ? (
				<p className="note warn">아직 가격이 정해지지 않아 금액이 없습니다.</p>
			) : (
				<>
					<p>
						예상 합계 <strong>{money(u.total, u.currency)}</strong>
					</p>
					{left > 0n && (
						<p className="small">
							크레딧에서 먼저 냅니다. 청구서를 확정할 때 이 워크스페이스에 남은 크레딧 {money(left, u.currency)}에서 먼저 빼고, 예상 합계는 그 전의 금액입니다.
						</p>
					)}
				</>
			)}
		</>
	)
}

/** An allowance as the usage says it: "프로모션 무료량: 활성 자산 +500개 (2027. 4. 30.까지)", and from when, where that is past the period's start. */
function allowanceText(a: BillingAllowance, unit: string, start: Date | undefined): string {
	const from = dateOf(a.dateEffective)
	const when = [from !== undefined && start !== undefined && from > start ? `${fmt(from, 'date')}부터` : '', a.dateExpires !== undefined ? `${lastDay(a.dateExpires)}까지` : '']
		.filter((v) => v !== '')
		.join(' ')
	return `프로모션 무료량: ${meterWord[a.meter] ?? a.meter} +${quantity(a.quantity * 1_000_000n, unit)}${when === '' ? '' : ` (${when})`}`
}

/**
 * What levyan's operator gave the workspace beyond what its prices include,
 * in force at some moment of this period (levyan's `docs/apps.md`, 7): each
 * allowance, more of a meter each period; and each pool, what the periods
 * closed left of it, and what this period would take of it and leave
 * (`PoolSaid`). Nothing, for a workspace given neither.
 */
function Given(props: { usage: BillingUsage }): ReactNode {
	const u = props.usage
	if (u.allowances.length === 0 && u.pools.length === 0) return null
	const start = dateOf(u.period?.dateStart)
	const unitOf = (meter: string): string => u.lines.find((l) => l.meter === meter)?.unit ?? meterUnit[meter] ?? ''
	// What this period takes of each pool: of its own lines -- what a
	// correction's say is what the period it corrects takes now, which the
	// pool's `left` was told of when that period closed.
	const takes = new Map<string, bigint>()
	for (const l of u.lines) {
		if (l.kind === BillingLineKind.CORRECTION) continue
		for (const t of l.pools) takes.set(t.pool, (takes.get(t.pool) ?? 0n) + micros(t.taken))
	}
	return (
		<ul className="benefits">
			{u.allowances.map((a) => (
				<li key={a.id}>{allowanceText(a, unitOf(a.meter), start)}</li>
			))}
			{u.pools.map((p) => (
				<PoolSaid key={p.id} p={p} unit={unitOf(p.meter)} takes={takes.get(p.id) ?? 0n} start={start} />
			))}
		</ul>
	)
}

/**
 * A pool as the usage says it: what the periods closed left of what was
 * granted, until when, and what is left once this period takes what its
 * lines take -- or that this period would take what is left.
 */
function PoolSaid(props: { p: BillingPool; unit: string; takes: bigint; start: Date | undefined }): ReactNode {
	const { p, unit, takes } = props
	const left = micros(p.left)
	const after = left - takes
	const from = dateOf(p.dateEffective)
	const when = [from !== undefined && props.start !== undefined && from > props.start ? `${fmt(from, 'date')}부터` : '', p.dateExpires !== undefined ? `${lastDay(p.dateExpires)}까지` : '']
		.filter((v) => v !== '')
		.join(' ')
	return (
		<li>
			사용량 크레딧: {poolQuantity(left, unit)} 남음 ({poolQuantity(p.granted * 1_000_000n, unit)} 중{when === '' ? '' : `, ${when}`}).
			{takes > 0n &&
				(after > 0n ? (
					` 이번 기간에 ${poolQuantity(takes, unit)}을 쓰면 ${poolQuantity(after, unit)}이 남습니다.`
				) : (
					<strong> 이번 기간에 남은 것을 다 쓸 것 같습니다.</strong>
				))}
		</li>
	)
}

/**
 * The lines of a period (`LineRow`), and how their numbers are rounded
 * (`LinesNote`) unless `note` is false -- where several periods' lines are
 * shown together, which say it once after them.
 */
function Lines(props: { lines: BillingLine[]; currency: string; note?: boolean }): ReactNode {
	return (
		<>
			<div className="table-wrap">
				<table className="table lines">
					<thead>
						<tr>
							<th>항목</th>
							<th className="num">사용량</th>
							<th className="num">무료</th>
							<th className="num">사용량 크레딧</th>
							<th className="num">유료</th>
							<th className="num">금액</th>
						</tr>
					</thead>
					<tbody>
						{props.lines.map((l, i) => (
							<LineRow key={`${l.meter}-${i}`} l={l} currency={props.currency} />
						))}
					</tbody>
				</table>
			</div>
			{props.note !== false && <LinesNote />}
		</>
	)
}

/**
 * How the numbers of a period's lines are rounded: every quantity to the
 * first digit after the point (`tenths`), and every line's amount down to
 * the currency's minor unit, as the total is once of all of them together.
 */
function LinesNote(): ReactNode {
	return (
		<p className="mute small">
			무료는 가격의 무료량에 프로모션 무료량을 더한 것입니다. 무료를 넘은 양은 사용량 크레딧에서 먼저 빼고, 남은 유료에 단가를 곱합니다. 수량은 소수점 아래 첫째 자리까지
			반올림했고(0보다 크지만 0.1에 못 미치면 0.1 미만), 줄의 금액은 통화의 가장 작은 단위 아래를 버렸습니다. 합계는 결제 서비스가 정확히 더한 뒤 한 번 버린 것이라 줄의 금액을 더한 것과 조금
			다를 수 있습니다.
		</p>
	)
}

/**
 * One line: what its meter was used; what was included for nothing -- what
 * its prices include, and what an allowance gave on top (`allowance`, "100개
 * + 프로모션 338.7개"); what of what was billable past that a pool took
 * (`pooled`), in the pool's unit; what is left to price; and what it comes
 * to. A line priced by bands, or by nothing, says its use and its amount,
 * and nothing it is not priced by.
 */
function LineRow(props: { l: BillingLine; currency: string }): ReactNode {
	const { l } = props
	const included = micros(l.included)
	const allowance = micros(l.allowance)
	const billable = micros(l.billable)
	const pooled = micros(l.pooled)
	const byUnit = included !== 0n || billable !== 0n || allowance !== 0n || pooled !== 0n
	const priced = included - allowance
	const free = !byUnit
		? '-'
		: allowance === 0n
			? quantity(included, l.unit)
			: [priced !== 0n ? quantity(priced, l.unit) : '', `프로모션 ${quantity(allowance, l.unit)}`].filter((v) => v !== '').join(' + ')
	return (
		<tr>
			<td>
				{meterWord[l.meter] ?? l.meter}
				{l.corrects !== undefined && <span className="mute small"> (정정: {periodText(l.corrects)})</span>}
			</td>
			<td className="num">{usageText(l)}</td>
			<td className="num">{free}</td>
			<td className="num">{pooled === 0n ? '-' : poolQuantity(pooled, l.unit)}</td>
			<td className="num">{byUnit ? quantity(billable - pooled, l.unit) : '-'}</td>
			<td className="num">{props.currency === '' ? '-' : lineAmount(l.amount, props.currency)}</td>
		</tr>
	)
}

/** The statements of the periods closed, each opened to its lines (`Lines`): what the allowances gave and the pools took then. */
function Statements(props: { statements: BillingStatement[] }): ReactNode {
	const [open, setOpen] = useState<BillingStatement>()
	if (props.statements.length === 0) return <Empty>닫힌 기간이 아직 없습니다.</Empty>
	return (
		<>
			<table className="table">
				<thead>
					<tr>
						<th>기간</th>
						<th className="num">합계</th>
						<th>닫은 날</th>
						<th />
					</tr>
				</thead>
				<tbody>
					{props.statements.map((s) => (
						<tr key={s.id}>
							<td>{periodText(s.period)}</td>
							<td className="num">{money(s.total, s.currency)}</td>
							<td>{fmt(s.dateClosed, 'date')}</td>
							<td>
								<button className="link" onClick={() => setOpen(s)}>
									보기
								</button>
							</td>
						</tr>
					))}
				</tbody>
			</table>
			{open !== undefined && (
				<Modal title={`명세서 · ${periodText(open.period)}`} onClose={() => setOpen(undefined)} wide>
					{open.lines.length === 0 ? <Empty>잰 것이 없었습니다.</Empty> : <Lines lines={open.lines} currency={open.currency} />}
					<p>
						합계 <strong>{money(open.total, open.currency)}</strong> · {fmt(open.dateClosed, 'date')}에 닫음
					</p>
				</Modal>
			)}
		</>
	)
}

/**
 * Who pays, as levyan says it. What the payer is to do is said, and where they
 * do it is not linked: that is where a payer pays, which is not here (D81).
 */
function PayerSaid(props: { payer: BillingPayer | undefined; nameOf: (account: string | undefined) => string }): ReactNode {
	const p = props.payer
	if (p === undefined || (p.current === undefined && p.next === undefined && p.offer === undefined && p.datePayerless === undefined && p.dunning.length === 0)) {
		return <p className="mute">결제 서비스가 아직 이 워크스페이스의 결제 담당을 모릅니다. 잠시 뒤에 다시 보세요.</p>
	}
	const restricted = dateOf(p.dateRestricted)
	const readOnly = dateOf(p.dateReadonly)
	const now = new Date()
	return (
		<>
			<Kv
				items={[
					...(p.current !== undefined
						? ([['지금 내는 사람', `${props.nameOf(p.current.account)} · ${fmt(p.current.dateStart, 'date')}부터${p.current.dateEnd !== undefined ? ` ${fmt(p.current.dateEnd, 'date')}까지` : ''}`]] as [ReactNode, ReactNode][])
						: []),
					...(p.next !== undefined ? ([['다음 기간부터', props.nameOf(p.next.account)]] as [ReactNode, ReactNode][]) : []),
					...(p.offer !== undefined ? ([['제안', `${props.nameOf(p.offer.account)} · ${fmt(p.offer.dateOffered, 'date')}에 제안함`]] as [ReactNode, ReactNode][]) : []),
					...(p.collection !== undefined ? ([['수금', <CollectionBadge key="c" c={p.collection} />]] as [ReactNode, ReactNode][]) : []),
				]}
			/>
			{p.datePayerless !== undefined && (
				<p className="note warn" role="status">
					{fmt(p.datePayerless, 'date')}부터 결제 담당이 없습니다.{' '}
					{restricted !== undefined && (restricted <= now ? `${fmt(restricted, 'date')}부터 제한되었습니다.` : `${fmt(restricted, 'date')}부터 제한됩니다.`)}{' '}
					{readOnly !== undefined && (readOnly <= now ? `${fmt(readOnly, 'date')}부터 읽기 전용입니다.` : `${fmt(readOnly, 'date')}부터는 읽기 전용이 됩니다.`)} 구성원에게 결제 담당을 제안하고, 그 사람이 받아들이면
					풀립니다.
				</p>
			)}
			{p.collection !== undefined && collectionDo[p.collection.state] !== undefined && (
				<p className="small">결제 담당의 결제 계정: {collectionDo[p.collection.state]}</p>
			)}
			{p.dunning.map((d) => (
				<p key={d.invoice} className="note warn" role="status">
					이 워크스페이스를 청구한 청구서의 {dunningSaid(d)} 결제 담당이 정리할 수 있습니다.
				</p>
			))}
		</>
	)
}

/**
 * The owner asking a member to pay for the workspace: one who is not leaving,
 * and does not pay already -- themself too. The member answers where a payer
 * pays, which is not here (D81).
 */
function OfferCard(props: { payer: BillingPayer | undefined; members: Map<string, Member>; me: string }): ReactNode {
	const act = useAct()
	const pays = new Set([props.payer?.current?.account, props.payer?.next?.account].filter((v): v is string => v !== undefined && v !== ''))
	const options = [...props.members.entries()]
		.filter(([id, m]) => !m.leaving && !pays.has(id))
		.map(([id, m]) => ({ value: idStr(m.holder), label: `${id === props.me ? `${m.called} (나)` : m.called} · ${roleWord[m.role] ?? m.role}` }))
	const [holder, setHolder] = useState('')
	const chosen = options.some((o) => o.value === holder) ? holder : ''
	return (
		<Card title="결제 담당 제안">
			<p className="small">
				제안받은 사람이 받아들이면 이번 기간이 끝난 뒤부터 그 사람이 냅니다. 새 제안은 전의 제안을 거둡니다.{pays.has(props.me) ? '' : ' 내가 내려면 나에게 제안하세요.'}
			</p>
			{options.length === 0 ? (
				<Empty>제안할 수 있는 구성원이 없습니다.</Empty>
			) : (
				<form
					className="inline"
					onSubmit={async (e) => {
						e.preventDefault()
						const m = [...props.members.values()].find((v) => idStr(v.holder) === chosen)
						if (m === undefined) return
						await act(PaymentService.method.offer, { holder: m.holder }, { ok: `${m.name}님에게 결제 담당을 제안했습니다.` })
					}}
				>
					<Field label="제안할 구성원">
						<Select value={chosen} onChange={setHolder} empty="고르세요" options={options} required />
					</Field>
					<button className="primary" disabled={chosen === ''}>
						제안
					</button>
				</form>
			)}
		</Card>
	)
}

// ---------------------------------------------------------------------------
// The banner.

/** The soonest of `dates`, or undefined for none. */
function soonest(dates: (Date | undefined)[]): Date | undefined {
	let out: Date | undefined
	for (const d of dates) if (d !== undefined && (out === undefined || d < out)) out = d
	return out
}

/**
 * What a workspace says above every page while nobody pays for it, while an
 * invoice that bills it failed to be charged, once it is restricted, and once
 * it is read-only -- what to do, and where (levyan's `docs/apps.md`, 3, 4 and
 * 6). Every member is told it is restricted or read-only, by the copy of
 * levyan's grants its writes are held to (`PartyService.Me`) or by a write
 * refused for it; read-only, the second stage, is said over the first, and
 * before it comes, from when it will (`date_readonly`), to whoever reads it.
 * Why, and what comes before, the people who run it are told from who pays
 * for it (`GetPayer`), which they alone read, and are sent to the
 * workspace's billing; everybody else, to them. Where a payer settles it is
 * where a payer pays, which is not here, and is not linked (D81). And while
 * nothing failed and nothing is restricted, the people who run it are told
 * the billing account it is paid for with cannot be collected from yet
 * (`GetPayer`'s collection, `CollectionBanner`), which restricts nothing.
 */
export function BillingBanner(props: { session: Session }): ReactNode {
	const c = useCatalog()
	const refused = useRestricted()
	const refusedReadOnly = useReadOnly()
	const billed = props.session.billing === true
	const admin = c.can('admin')
	const payer = useRpc(PaymentService.method.getPayer, billed && admin ? {} : null, { poll: 10 * 60_000 })
	const p = payer.data?.payer
	const now = new Date()

	// What restricts it, or will: having no payer, and the invoice that
	// restricts soonest of those whose charge failed that bill the
	// workspace. And from when it is read-only, the second stage (design
	// 7.10): the soonest either says.
	const nobodyPays = p?.datePayerless !== undefined
	const payerlessFrom = nobodyPays ? dateOf(p.dateRestricted) : undefined
	const dunnings = p?.dunning ?? []
	const dunning = dunnings[0]
	const dunningFrom = dateOf(dunning?.dateRestricted)
	const readOnlyFrom = soonest([nobodyPays ? dateOf(p.dateReadonly) : undefined, ...dunnings.map((d) => dateOf(d.dateReadonly))])
	const passed = (v: Date | undefined): v is Date => v !== undefined && v <= now
	// Read-only holds it to more than the first stage: by its own grant, as
	// its writes are held, or -- to whoever knows its day -- once that passed,
	// as levyan has a page say it before the grant comes (`docs/apps.md`, 3).
	const readOnly = c.me.readonly || refusedReadOnly || passed(readOnlyFrom)
	const restricted = readOnly || c.me.restricted || refused || passed(payerlessFrom) || passed(dunningFrom)
	// What the banner says of the second stage before it comes.
	const becomesReadOnly =
		!readOnly && readOnlyFrom !== undefined
			? ` ${fmt(readOnlyFrom, 'date')}부터는 읽기 전용이 되어 워크스페이스의 데이터를 바꿀 수 없게 됩니다. 보기, 내보내기, 결제, 떠나기는 그때도 할 수 있습니다.`
			: ''

	const payerIsMe = p?.current !== undefined && p.current.account === props.session.id
	const payerless = payerlessFrom !== undefined
	if (!restricted && payerlessFrom === undefined && dunning === undefined && readOnlyFrom === undefined) {
		// Nothing failed, and nothing is restricted: whether the billing
		// account it is paid for with can be collected, to the people who
		// run it, who alone read who pays for it.
		const state = p?.collection?.state
		if (!admin || state === undefined || collectionDo[state] === undefined) return null
		return <CollectionBanner state={state} payerIsMe={payerIsMe} owner={c.can('owner')} />
	}
	const grows = '자산 등록, 초대, 첨부 올리기처럼 청구되는 것을 늘리는 일'

	let said: ReactNode
	if (readOnly) {
		// Since when, why, as far as the banner knows it -- nobody paying for
		// it, or the invoice whose charge failed first -- and what ends it, to
		// the people who run it: who alone know why.
		const why = nobodyPays
			? '결제 담당이 없는 채로 오래 지나'
			: dunning !== undefined
				? `${fmt(dunning.dateFailed, 'date')}에 결제가 실패한 청구서가 아직 결제되지 않아`
				: '결제가 오래 정리되지 않아'
		const ends = !admin ? '결제가 정리되면' : nobodyPays ? '결제 담당이 정해지면' : payerIsMe ? '내가 밀린 청구서를 결제하면' : '결제 담당이 밀린 청구서를 결제하면'
		said = (
			<>
				<strong>이 워크스페이스는 {passed(readOnlyFrom) ? `${fmt(readOnlyFrom, 'date')}부터 ` : ''}읽기 전용입니다.</strong>
				<span>
					{why} 워크스페이스의 데이터는 바꿀 수 없습니다. 보기, 내보내기, 결제, 떠나기는 할 수 있습니다. {ends} 곧 다시 바꿀 수 있습니다.
				</span>
			</>
		)
	} else if (restricted) {
		const why = passed(payerlessFrom) ? '결제 담당이 없어 ' : passed(dunningFrom) ? '결제가 밀려 ' : ''
		const since = passed(payerlessFrom) ? payerlessFrom : passed(dunningFrom) ? dunningFrom : undefined
		said = (
			<>
				<strong>
					이 워크스페이스는 {since !== undefined ? `${fmt(since, 'date')}부터 ` : ''}
					{why}제한되었습니다.
				</strong>
				<span>
					{grows}을 할 수 없습니다. 보기, 내보내기, 지급·반납은 그대로입니다. 결제를 정리하면 다시 할 수 있습니다.{becomesReadOnly}
				</span>
			</>
		)
	} else if (dunning !== undefined) {
		said = (
			<>
				<strong>이 워크스페이스를 청구한 청구서의 결제가 실패했습니다.</strong>
				<span>
					{dunningFrom !== undefined ? `${fmt(dunningFrom, 'date')}까지 결제되지 않으면 ` : ''}제한되어 {grows}을 할 수 없게 됩니다.{becomesReadOnly}
				</span>
			</>
		)
	} else {
		said = (
			<>
				<strong>이 워크스페이스에는 결제 담당이 없습니다.</strong>
				<span>
					{payerlessFrom !== undefined ? `${fmt(payerlessFrom, 'date')}부터 ` : ''}제한되어 {grows}을 할 수 없게 됩니다.{becomesReadOnly}
				</span>
			</>
		)
	}

	// Where to go: the workspace's settings for its owner and administrators,
	// and nowhere outside the workspace -- where a payer pays is not here
	// (D81).
	let go: ReactNode
	if (c.can('owner')) {
		go = <Link to="/settings?tab=billing">{payerless ? '결제 담당 정하기 →' : '결제 설정 보기 →'}</Link>
	} else if (admin) {
		go = <Link to="/settings?tab=billing">결제 설정 보기 → (결제 담당은 소유자가 정합니다)</Link>
	} else {
		go = <span>워크스페이스의 소유자나 관리자에게 알려 주세요.</span>
	}

	return (
		<div className="billing-banner" role="alert">
			{said}
			{go}
		</div>
	)
}

/**
 * What a workspace says above its pages, to its owners and administrators,
 * while the billing account it is paid for with cannot be collected from --
 * before any charge of it failed (levyan's `docs/apps.md`, 4): its state, in
 * the words of what it asks (`collectionDo`), said to the payer as theirs to
 * do, and the way to the workspace's billing. It restricts nothing, and says
 * so: what fails later is dunning's to say (`BillingBanner`).
 */
function CollectionBanner(props: { state: BillingCollectionState; payerIsMe: boolean; owner: boolean }): ReactNode {
	const word = collectionWord[props.state]?.[0] ?? ''
	const asked = collectionDo[props.state] ?? ''
	const go = props.owner ? (
		<Link to="/settings?tab=billing">결제 설정 보기 →</Link>
	) : (
		<Link to="/settings?tab=billing">결제 설정 보기 → (결제 담당은 소유자가 정합니다)</Link>
	)
	return (
		<div className="billing-banner warn" role="status">
			<strong>이 워크스페이스를 내는 결제 계정으로 지금 결제할 수 없습니다: {word}.</strong>
			<span>
				{props.payerIsMe ? asked : `결제 담당이 할 일: ${asked}`} 이 때문에 제한된 것은 없습니다.
			</span>
			{go}
		</div>
	)
}
