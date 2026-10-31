/**
 * How a workspace is paid for (design 7.9, levyan#14): what levyan, the
 * billing service, says of it, relayed by `loft.PaymentService` and kept
 * nowhere here.
 *
 * - **The account's own page**, `/billing`: about no workspace, as the
 *   invitations are, since what a payer pays with is its own and its calls are
 *   the account's (payday#184). Its billing accounts -- made by naming a
 *   country, and sold through the channel and billed in the currency levyan
 *   says of it; called by a name the payer gives, or by when each was made
 *   (`accountNames`) -- who each one's bills go to and the business they are
 *   billed to (`BilledTo`), which levyan keeps and this passes on; whether
 *   each can be collected, those that cannot yet said above the rest
 *   (`Uncollectable`); the subscription it is collected through and how it
 *   pays, which only ever says the kind, the brand and the last four digits;
 *   the checkout that starts one, opened on the page Paddle's checkout opens
 *   on (`/billing/checkout`) and asked about again as the payer comes back
 *   here; the offers to pay for a workspace, the workspaces it pays for, its
 *   invoices and credit notes.
 * - **A workspace's**, in its settings for the people who run it: who pays for
 *   it, what it has used this period and what that would come to, its
 *   statements, its owner asking a member to pay, and its owner taking it on
 *   themself in one go (`TakeOver`).
 * - **The banner** a workspace shows while nobody pays for it, once it is
 *   restricted, and -- to the people who run it -- while the billing account
 *   it is paid for with cannot be collected from: what to do, and where.
 *
 * Its dates are on the wall the server tells it -- the deployment's on the
 * account's page, which is about no workspace (`main.tsx`), and the
 * workspace's in a workspace -- and never on the browser's.
 *
 * @module
 */

import { useCallback, useEffect, useMemo, useState, type ChangeEvent, type ReactNode } from 'react'
import { Link } from 'react-router-dom'
import type { MessageInitShape } from '@bufbuild/protobuf'
import type { Timestamp } from '@bufbuild/protobuf/wkt'

import {
	BillingChannel,
	BillingCheckoutState,
	BillingCollectionState,
	BillingCreditNoteReason,
	BillingDeclineKind,
	BillingInvoiceState,
	BillingLinkBegan,
	BillingLinkEnded,
	BillingSubscriptionChange,
	BillingSubscriptionStatus,
	type BillingAccount,
	type BillingBusiness,
	type BillingCollection,
	type BillingCreditNote,
	type BillingDunning,
	type BillingInvoice,
	type BillingLine,
	type BillingLink,
	type BillingOffer,
	type BillingPayer,
	type BillingPeriod,
	type PaymentGetBillingAccountResponse,
	type PaymentGetInvoiceResponse,
	type PaymentUpdateBillingAccountRequestSchema,
} from '../gen/loft/payment_pb.js'
import { HolderService, PaymentService, dateOf, errorText, fmt, idStr, money, openWorkspace, wall, workspace, type Account } from './api.js'
import { useCatalog } from './catalog.js'
import { call, useAct, useEpoch, useRestricted, useRpc } from './rpc.js'
import { Badge, Card, Confirm, Empty, Field, FormModal, Kv, Load, Modal, Select, Spinner, useToast } from './ui.js'
import { roleWord } from './words.js'

/** What a channel is called. */
const channelWord: Record<number, string> = {
	[BillingChannel.MOR]: 'MoR (Paddle)',
	[BillingChannel.PG_KR]: '국내 PG',
	[BillingChannel.BANK_KR]: '계좌이체',
}

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

const statusWord: Record<number, string> = {
	[BillingSubscriptionStatus.ACTIVE]: '활성',
	[BillingSubscriptionStatus.TRIALING]: '체험',
	[BillingSubscriptionStatus.PAST_DUE]: '연체',
	[BillingSubscriptionStatus.PAUSED]: '일시정지',
	[BillingSubscriptionStatus.CANCELED]: '해지됨',
}

const changeWord: Record<number, string> = {
	[BillingSubscriptionChange.CANCEL]: '해지',
	[BillingSubscriptionChange.PAUSE]: '일시정지',
	[BillingSubscriptionChange.RESUME]: '재개',
}

const beganWord: Record<number, string> = {
	[BillingLinkBegan.ARRIVED]: '워크스페이스를 만들어서',
	[BillingLinkBegan.ACCEPTED]: '제안을 받아들여서',
	[BillingLinkBegan.USED]: '결제 계정을 바꿔서',
	[BillingLinkBegan.RESUMED]: '다시 이어서',
}

const endedWord: Record<number, string> = {
	[BillingLinkEnded.REPLACED]: '다른 결제로 바뀜',
	[BillingLinkEnded.STOPPED]: '그만 냄',
	[BillingLinkEnded.LEFT]: '워크스페이스를 떠남',
	[BillingLinkEnded.SUSPENDED]: '계정 정지',
	[BillingLinkEnded.ERASED]: '계정 삭제',
	[BillingLinkEnded.DESTROYED]: '워크스페이스 파기',
}

const invoiceWord: Record<number, [string, string]> = {
	[BillingInvoiceState.DRAFT]: ['초안', 'mute'],
	[BillingInvoiceState.OPEN]: ['미결', 'pending'],
	[BillingInvoiceState.PAID]: ['결제됨', 'ok'],
	[BillingInvoiceState.UNCOLLECTIBLE]: ['회수 불가', 'rejected'],
	[BillingInvoiceState.VOID]: ['무효', 'mute'],
	[BillingInvoiceState.WRITTEN_OFF]: ['털어 냄', 'mute'],
	[BillingInvoiceState.SHADOW]: ['시험 청구', 'mute'],
}

const reasonWord: Record<number, string> = {
	[BillingCreditNoteReason.CORRECTION]: '정정',
	[BillingCreditNoteReason.REFUND]: '환불',
	[BillingCreditNoteReason.CANCELLATION]: '취소',
}

/**
 * What is done of an invoice whose charge failed, as the payer is told it
 * (levyan's `docs/apps.md`, 6): by the kind of its latest decline, what to do
 * and by when -- when it is charged again, from when every workspace it bills
 * is restricted, or that it is -- and nothing levyan did not say.
 */
function dunningSaid(d: BillingDunning, collection: BillingCollection | undefined): string {
	const failed = fmt(d.failed, 'date')
	const from = dateOf(d.restrictedFrom)
	const passed = from !== undefined && from <= new Date()
	const restricts = from === undefined ? '' : passed ? `${fmt(from, 'date')}부터 제한되었습니다. 결제되면 곧 풀립니다.` : `${fmt(from, 'date')}까지 결제되지 않으면 제한됩니다.`
	switch (d.kind) {
		case BillingDeclineKind.FRAUD:
			return `${failed}에 결제가 거절되어 제한되었습니다. 다른 결제수단을 써 주세요.`
		case BillingDeclineKind.REPLACE: {
			const how =
				collection?.state === BillingCollectionState.NEEDS_ACTION
					? 'Paddle이 보낸 안내(카드사 앱의 승인 같은 것)를 따르세요.'
					: collection?.state === BillingCollectionState.NO_SUBSCRIPTION || collection?.state === BillingCollectionState.CANCELED
						? '결제수단을 다시 등록하세요.'
						: '결제수단을 바꿔 주세요.'
			return `${failed}에 결제가 실패했습니다. ${how} ${restricts}`
		}
		default:
			return d.nextRetry !== undefined
				? `${failed}에 결제가 실패했습니다. ${fmt(d.nextRetry)}에 다시 시도합니다. ${restricts}`
				: `${failed}에 결제가 실패했고, 다시 시도할 일정이 남지 않았습니다. 결제수단을 바꾸면 바로 다시 청구합니다. ${restricts}`
	}
}

/** How a payer pays, by the merchant's name of the kind. */
const methodWord: Record<string, string> = {
	card: '카드',
	south_korea_local_card: '국내 카드',
	kakao_pay: '카카오페이',
	naver_pay: '네이버페이',
	payco: 'PAYCO',
	samsung_pay: '삼성페이',
	apple_pay: 'Apple Pay',
	google_pay: 'Google Pay',
	paypal: 'PayPal',
}

/** What each meter is called. */
const meterWord: Record<string, string> = {
	'loft.assets.active': '활성 자산',
	'loft.attachments.bytes': '첨부 저장량',
	'loft.members': '구성원',
	'loft.history.retention_days': '이력 보존 기간',
}

/** The countries a billing account is offered in first, as people call them. */
const countries: [string, string][] = [
	['KR', '대한민국'],
	['US', '미국'],
	['JP', '일본'],
	['CN', '중국'],
	['TW', '대만'],
	['HK', '홍콩'],
	['SG', '싱가포르'],
	['VN', '베트남'],
	['TH', '태국'],
	['ID', '인도네시아'],
	['PH', '필리핀'],
	['MY', '말레이시아'],
	['IN', '인도'],
	['AU', '호주'],
	['NZ', '뉴질랜드'],
	['CA', '캐나다'],
	['GB', '영국'],
	['DE', '독일'],
	['FR', '프랑스'],
	['NL', '네덜란드'],
	['ES', '스페인'],
	['IT', '이탈리아'],
]

function countryName(code: string): string {
	return countries.find(([c]) => c === code)?.[1] ?? code
}

/** A period, as the days it covers: its end is the first moment after it. */
function periodText(p: BillingPeriod | undefined): string {
	const start = dateOf(p?.start)
	const end = dateOf(p?.end)
	if (start === undefined || end === undefined) return ''
	return `${fmt(start, 'date')} – ${fmt(new Date(end.getTime() - 1), 'date')}`
}

/** From `start` until the moment before `end`, as days. */
function spanText(start: Timestamp | undefined, end: Timestamp | undefined): string {
	return periodText({ start, end } as BillingPeriod)
}

/** How a payer pays: the kind, the brand and the last four digits, and nothing more of a card. */
function methodText(m: { type: string; brand: string; last4: string } | undefined): string {
	if (m === undefined || m.type === '') return '아직 없음'
	return [methodWord[m.type] ?? m.type, m.brand, m.last4 === '' ? '' : `•••• ${m.last4}`].filter((v) => v !== '').join(' ')
}

/**
 * A moment to the minute, as the clock on the wall the page is told says it
 * (`localize`) -- the deployment's on the account's page -- and never the
 * browser's: 10월 31일 14:05.
 */
function minuteText(t: Timestamp | undefined): string {
	const d = dateOf(t)
	if (d === undefined) return ''
	const w = wall(d)
	const p = (n: number) => String(n).padStart(2, '0')
	return `${w.m + 1}월 ${w.d}일 ${p(w.h)}:${p(w.mi)}`
}

/**
 * What each of a payer's billing accounts is called wherever the page lists
 * or offers one: the name the payer gave it, or else when it was made -- and,
 * where two would still read the same (one name twice, or two made in one
 * minute), the end of its identifier after it.
 */
function accountNames(accounts: BillingAccount[]): (b: BillingAccount) => string {
	const own = (b: BillingAccount): string => {
		const name = b.name.trim()
		if (name !== '') return name
		const made = minuteText(b.created)
		return made === '' ? '결제 계정' : `결제 계정 (${made} 만듦)`
	}
	const seen = new Map<string, number>()
	for (const b of accounts) {
		const v = own(b)
		seen.set(v, (seen.get(v) ?? 0) + 1)
	}
	return (b) => {
		const v = own(b)
		return (seen.get(v) ?? 0) > 1 ? `${v} · #${b.id.slice(-4)}` : v
	}
}

/** What a line used, in its unit. */
function usageText(l: BillingLine): string {
	const n = Number(l.usage)
	switch (l.unit) {
		case '{asset}':
			return `${n.toLocaleString('ko-KR')}개`
		case '{member}':
			return `${n.toLocaleString('ko-KR')}명`
		case 'd':
			return n === 0 ? '영원히' : `${n.toLocaleString('ko-KR')}일`
		case 'By':
			return bytes(n)
		default:
			return `${l.usage} ${l.unit}`
	}
}

function bytes(n: number): string {
	const units = ['B', 'KB', 'MB', 'GB', 'TB']
	let v = n
	let i = 0
	while (v >= 1024 && i < units.length - 1) {
		v /= 1024
		i++
	}
	return `${i === 0 ? v : v.toFixed(1)} ${units[i]}`
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

/** The workspaces an account is in, by the identifier levyan names them by. */
function workspaceNames(account: Account): (id: string) => string {
	const by = new Map(account.workspaces.map((w) => [w.id, w.name || `@${w.alias}`]))
	return (id) => by.get(id) ?? '이 계정이 들어 있지 않은 워크스페이스'
}

/**
 * Where the account's own page is: here, and at the service's address on a
 * workspace's own domain, which serves nothing of the account's beside the
 * workspace (loft#155) -- as the invitations are.
 */
function billingPage(account: Account): string {
	return account.pinned !== undefined && account.service !== undefined ? `${account.service}/billing` : '/billing'
}

// ---------------------------------------------------------------------------
// The account's own page.

/** What the page is drawn from, asked of levyan together. */
type Paying = {
	accounts: BillingAccount[]
	offers: BillingOffer[]
	links: BillingLink[]
	invoices: BillingInvoice[]
	notes: BillingCreditNote[]
}

async function paying(): Promise<Paying> {
	const [a, o, l, i, n] = await Promise.all([
		call(PaymentService.method.listBillingAccounts, {}),
		call(PaymentService.method.listOffers, {}),
		call(PaymentService.method.listLinks, {}),
		call(PaymentService.method.listInvoices, {}),
		call(PaymentService.method.listCreditNotes, {}),
	])
	return { accounts: a.billingAccounts, offers: o.offers, links: l.links, invoices: i.invoices, notes: n.creditNotes }
}

/** What levyan says of one billing account beyond the list, or why it could not be asked. */
type Detail = { got?: PaymentGetBillingAccountResponse; bad?: string }

/**
 * What levyan says of each billing account beyond the list -- whether it can
 * be collected, its subscription and how it pays, its failed charges --
 * asked of each as the list arrives: for its row, and for the choices of a
 * billing account, which say how each pays where this knows it
 * (`accountOptions`). What was said before stays until the answer after it.
 */
function useDetails(accounts: BillingAccount[] | undefined): Record<string, Detail> {
	const [details, setDetails] = useState<Record<string, Detail>>({})
	useEffect(() => {
		if (accounts === undefined) return
		let gone = false
		for (const b of accounts) {
			call(PaymentService.method.getBillingAccount, { id: b.id }).then(
				(got) => !gone && setDetails((v) => ({ ...v, [b.id]: { got } })),
				(err) => !gone && setDetails((v) => ({ ...v, [b.id]: { got: v[b.id]?.got, bad: errorText(err) } })),
			)
		}
		return () => {
			gone = true
		}
	}, [accounts])
	return details
}

/**
 * A checkout the payer comes back from (`/billing?checkout=`): asked about
 * until it is done, which is once Paddle made the subscription -- a moment or
 * so after the payer finished, and done by Paddle's notice to levyan all the
 * same -- and taken out of the address once it is.
 */
function useCheckoutBack(onDone: () => void): string {
	const [said, setSaid] = useState('')
	useEffect(() => {
		const q = new URLSearchParams(location.search)
		const id = q.get('checkout')
		if (id === null || id === '') return
		let gone = false
		const forget = () => {
			q.delete('checkout')
			const rest = q.toString()
			history.replaceState(null, '', location.pathname + (rest === '' ? '' : '?' + rest))
		}
		setSaid('결제수단을 확인하는 중입니다…')
		void (async () => {
			for (let i = 0; i < 20 && !gone; i++) {
				try {
					const r = await call(PaymentService.method.completeCheckout, { checkout: id })
					const state = r.checkout?.state
					if (state === BillingCheckoutState.COMPLETED) {
						forget()
						setSaid('결제수단을 등록했습니다.')
						onDone()
						return
					}
					if (state === BillingCheckoutState.CANCELED) {
						forget()
						setSaid('결제창이 취소되었습니다. 결제수단 등록을 다시 시작하세요.')
						onDone()
						return
					}
				} catch (err) {
					setSaid(errorText(err))
					return
				}
				await new Promise((r) => setTimeout(r, 1500))
			}
			if (!gone) setSaid('Paddle이 아직 구독을 만드는 중입니다. 잠시 뒤에 이 페이지를 다시 여세요.')
		})()
		return () => {
			gone = true
		}
	}, [onDone])
	return said
}

/** The account's own page of what it pays with and for. */
export function Billing(props: { account: Account; onSignOut: () => void }): ReactNode {
	const toast = useToast()
	const [v, setV] = useState<Paying>()
	const [bad, setBad] = useState('')
	const [n, setN] = useState(0)
	const [making, setMaking] = useState(false)
	const [busy, setBusy] = useState(false)
	const again = useCallback(() => setN((v) => v + 1), [])
	const back = useCheckoutBack(again)
	const named = workspaceNames(props.account)
	const details = useDetails(v?.accounts)
	const called = accountNames(v?.accounts ?? [])

	useEffect(() => {
		let gone = false
		paying().then(
			(v) => {
				if (gone) return
				setV(v)
				setBad('')
			},
			(err) => !gone && setBad(errorText(err)),
		)
		return () => {
			gone = true
		}
	}, [n])

	// A write, said, and everything asked again.
	const write = async (f: () => Promise<unknown>, ok: string): Promise<boolean> => {
		setBusy(true)
		try {
			await f()
			toast(ok)
			again()
			return true
		} catch (err) {
			toast(errorText(err), 'bad')
			return false
		} finally {
			setBusy(false)
		}
	}

	// The checkout is opened on the page Paddle's checkout opens on, which
	// sends the payer back here.
	const start = async (b: BillingAccount): Promise<void> => {
		setBusy(true)
		try {
			const r = await call(PaymentService.method.startCheckout, { billingAccount: b.id, successUrl: `${location.origin}/billing` })
			const co = r.checkout
			if (co?.window === undefined) throw new Error('결제창을 열 값을 받지 못했습니다.')
			location.assign(`/billing/checkout?transaction=${encodeURIComponent(co.window.transaction)}&checkout=${encodeURIComponent(co.id)}`)
		} catch (err) {
			toast(errorText(err), 'bad')
			setBusy(false)
		}
	}

	const home = props.account.workspaces[0]
	return (
		<div className="login">
			<div className="inbox billing">
				<div className="brand">
					<img src="/icon.svg" alt="" />
					<div>
						<h1>결제</h1>
						<p>{props.account.name || props.account.login}</p>
					</div>
				</div>
				{back !== '' && (
					<p className="note ok" role="status">
						{back}
					</p>
				)}
				{bad !== '' ? (
					<p className="bad">결제 정보를 불러오지 못했습니다. {bad}</p>
				) : v === undefined ? (
					<p className="mute">불러오는 중…</p>
				) : (
					<>
						<Uncollectable accounts={v.accounts} links={v.links} called={called} details={details} named={named} busy={busy} onStart={(b) => void start(b)} />
						<Card
							title="결제 계정"
							actions={
								<button className="primary" onClick={() => setMaking(true)}>
									새 결제 계정
								</button>
							}
						>
							{v.accounts.length === 0 ? (
								<Empty>아직 결제 계정이 없습니다. 워크스페이스를 내려면 결제 계정을 만들고 결제수단을 등록하세요.</Empty>
							) : (
								<ul className="billing-accounts">
									{v.accounts.map((b) => (
										<AccountRow key={b.id} b={b} called={called(b)} detail={details[b.id]} busy={busy} write={write} onStart={(b) => void start(b)} />
									))}
								</ul>
							)}
						</Card>
						<Offers offers={v.offers} accounts={v.accounts} called={called} details={details} named={named} busy={busy} write={write} />
						<Links links={v.links} accounts={v.accounts} called={called} details={details} named={named} busy={busy} write={write} />
						<Invoices invoices={v.invoices} notes={v.notes} accounts={v.accounts} named={named} />
					</>
				)}
				<div className="leave">
					{home !== undefined && (
						<button className="link" onClick={() => openWorkspace(home)}>
							워크스페이스로 돌아가기
						</button>
					)}
					<button className="link" onClick={props.onSignOut}>
						로그아웃
					</button>
				</div>
			</div>
			{making && (
				<NewAccount
					onClose={() => setMaking(false)}
					onMade={(b) => {
						toast(`결제 계정을 만들었습니다: ${channelWord[b.channel] ?? '채널 미정'}, ${b.currency}`)
						again()
					}}
				/>
			)}
		</div>
	)
}

/**
 * A billing account made by naming its country -- the channel and the
 * currency are levyan's to say of it -- and, if the payer likes, a name of
 * its own to tell it from the others by, and who its bills go to
 * (`BilledToFields`): none of which is kept here.
 */
function NewAccount(props: { onClose: () => void; onMade: (b: BillingAccount) => void }): ReactNode {
	const [name, setName] = useState('')
	const [country, setCountry] = useState('KR')
	const [to, setTo] = useState(() => billedToOf(undefined))
	const toast = useToast()
	return (
		<FormModal
			title="새 결제 계정"
			submit="만들기"
			wide
			onClose={props.onClose}
			onSubmit={async () => {
				try {
					const r = await call(PaymentService.method.createBillingAccount, {
						name: name.trim(),
						country,
						recipient: trimmed(to.recipient),
						...(to.asBusiness ? { business: trimmed(to.business) } : {}),
					})
					if (r.billingAccount !== undefined) props.onMade(r.billingAccount)
					return true
				} catch (err) {
					toast(errorText(err), 'bad')
					return false
				}
			}}
		>
			<Field label="이름" hint="여러 결제 계정을 구분하는 이름입니다. 예: 개인 카드, 회사용">
				<input value={name} onChange={(e) => setName(e.target.value)} autoFocus />
			</Field>
			<Field label="나라" hint="어느 채널로, 어느 통화로 청구할지는 결제 서비스가 나라에 따라 정합니다. 만든 뒤에는 바뀌지 않습니다.">
				<Select value={country} onChange={setCountry} options={countries.map(([value, name]) => ({ value, label: `${name} (${value})` }))} />
			</Field>
			<BilledToFields v={to} set={setTo} optional />
		</FormModal>
	)
}

/**
 * Who a billing account's bills go to (levyan's `Recipient`), and whether --
 * and to which business -- they are billed to one (its `Business`, what a
 * Korean tax invoice names it by), as a form holds them: every field text, as
 * levyan takes it.
 */
type BilledTo = {
	recipient: { name: string; email: string; address: string }
	asBusiness: boolean
	business: { registration: string; name: string; representative: string; address: string; kind: string; item: string; email: string }
}

/** What `b` says of who its bills go to, as the form starts from: nothing, for one not made yet. */
function billedToOf(b: BillingAccount | undefined): BilledTo {
	const r = b?.recipient
	const v = b?.business
	return {
		recipient: { name: r?.name ?? '', email: r?.email ?? '', address: r?.address ?? '' },
		asBusiness: v !== undefined,
		business: {
			registration: v?.registration ?? '',
			name: v?.name ?? '',
			representative: v?.representative ?? '',
			address: v?.address ?? '',
			kind: v?.kind ?? '',
			item: v?.item ?? '',
			email: v?.email ?? '',
		},
	}
}

/** Each of `v`'s words without the spaces around them, as levyan keeps them. */
function trimmed<T extends Record<string, string>>(v: T): T {
	return Object.fromEntries(Object.entries(v).map(([k, s]) => [k, s.trim()])) as T
}

/**
 * What saying again who `b`'s bills go to is asked of levyan: who receives
 * them, whole -- an empty field clears it -- and the business, whole, or that
 * there is none any longer (`clear_business`); and, for one paid by transfer,
 * no business at all: its contract's, which levyan refuses the account
 * (`BilledToFields`' `fixed`). Its name is left as it is.
 */
function updateOf(b: BillingAccount, v: BilledTo): MessageInitShape<typeof PaymentUpdateBillingAccountRequestSchema> {
	const out = { id: b.id, recipient: trimmed(v.recipient) }
	if (b.channel === BillingChannel.BANK_KR) return out
	if (v.asBusiness) return { ...out, business: trimmed(v.business) }
	if (b.business !== undefined) return { ...out, clearBusiness: true }
	return out
}

/** A business, as a billing account's card and its form say it. */
function businessItems(v: BillingBusiness): [ReactNode, ReactNode][] {
	const items: [ReactNode, ReactNode][] = [
		['사업자등록번호', v.registration],
		['상호', v.name],
		['대표자', v.representative],
		['사업장 주소', v.address],
		['업태', v.kind],
		['종목', v.item],
		['세금계산서 이메일', v.email],
	]
	return items.filter(([, w]) => w !== '')
}

/**
 * The fields of who a billing account's bills go to: who receives them, each
 * a field of one control (ui.tsx, `Field`), and whether they are billed to a
 * business -- a choice -- with the business's own fields while they are.
 * Somebody's, all of it: passed on to levyan and kept nowhere here.
 *
 * `fixed` is the business of one paid by transfer, which is its contract's
 * and an operator's to change (levyan refuses the account): said, and not
 * offered to change. `optional` says, as a billing account is made, that
 * none of it need be said yet.
 */
function BilledToFields(props: { v: BilledTo; set: (v: BilledTo) => void; fixed?: BillingBusiness | null; optional?: boolean }): ReactNode {
	const { v, set } = props
	const recipient = (k: keyof BilledTo['recipient']) => (e: ChangeEvent<HTMLInputElement>) => set({ ...v, recipient: { ...v.recipient, [k]: e.target.value } })
	const business = (k: keyof BilledTo['business']) => (e: ChangeEvent<HTMLInputElement>) => set({ ...v, business: { ...v.business, [k]: e.target.value } })
	return (
		<>
			<div className="subhead">
				<strong>받는 사람</strong>{' '}
				<span className="mute small">{props.optional ? '청구서를 받는 사람입니다. 나중에 적어도 됩니다.' : '청구서를 받는 사람입니다. 비우면 지웁니다.'}</span>
			</div>
			<Field label="받는 사람 이름">
				<input value={v.recipient.name} onChange={recipient('name')} autoComplete="name" />
			</Field>
			<Field label="받는 사람 이메일">
				<input type="email" value={v.recipient.email} onChange={recipient('email')} autoComplete="email" />
			</Field>
			<Field label="받는 사람 주소" wide>
				<input value={v.recipient.address} onChange={recipient('address')} autoComplete="street-address" />
			</Field>
			<div className="subhead">
				<strong>사업자</strong>
			</div>
			{props.fixed !== undefined ? (
				<div className="fixed">
					{props.fixed === null ? <div className="mute">사업자 정보가 없습니다.</div> : <Kv items={businessItems(props.fixed)} />}
					<div className="said small">계좌이체로 내는 결제 계정의 사업자 정보는 계약에 따른 것이라 운영자가 바꿉니다. 바꾸려면 운영자에게 알려 주세요.</div>
				</div>
			) : (
				<>
					<Field label="청구 대상" hint="사업자로 청구하면 세금계산서에 적을 사업자 정보를 함께 적습니다." wide>
						<Select
							value={v.asBusiness ? 'business' : 'person'}
							onChange={(w) => set({ ...v, asBusiness: w === 'business' })}
							options={[
								{ value: 'person', label: '개인' },
								{ value: 'business', label: '사업자' },
							]}
						/>
					</Field>
					{v.asBusiness && (
						<>
							<Field label="사업자등록번호" hint="숫자 열 자리, 예: 123-45-67890">
								<input value={v.business.registration} onChange={business('registration')} required />
							</Field>
							<Field label="상호">
								<input value={v.business.name} onChange={business('name')} autoComplete="organization" required />
							</Field>
							<Field label="대표자">
								<input value={v.business.representative} onChange={business('representative')} />
							</Field>
							<Field label="세금계산서 이메일" hint="세금계산서를 받을 주소입니다.">
								<input type="email" value={v.business.email} onChange={business('email')} />
							</Field>
							<Field label="사업장 주소" wide>
								<input value={v.business.address} onChange={business('address')} />
							</Field>
							<Field label="업태">
								<input value={v.business.kind} onChange={business('kind')} />
							</Field>
							<Field label="종목">
								<input value={v.business.item} onChange={business('item')} />
							</Field>
						</>
					)}
				</>
			)}
		</>
	)
}

/**
 * Who `b`'s bills go to, said again (`UpdateBillingAccount`, `updateOf`):
 * from what levyan says of it now.
 */
function BilledToForm(props: { b: BillingAccount; called: string; write: Write; onClose: () => void }): ReactNode {
	const [v, setV] = useState(() => billedToOf(props.b))
	const bank = props.b.channel === BillingChannel.BANK_KR
	return (
		<FormModal
			title={`${props.called} 청구 정보`}
			submit="저장"
			wide
			onClose={props.onClose}
			onSubmit={() => props.write(() => call(PaymentService.method.updateBillingAccount, updateOf(props.b, v)), '청구 정보를 저장했습니다.')}
		>
			<BilledToFields v={v} set={setV} fixed={bank ? (props.b.business ?? null) : undefined} />
		</FormModal>
	)
}

/**
 * Whether a checkout is what `b` lacks: one begins a subscription where there
 * is none to collect through, or none any longer -- which is also what a
 * method to replace asks -- on the merchant of record's channel alone.
 */
function checksOut(b: BillingAccount, state: BillingCollectionState): boolean {
	return b.channel === BillingChannel.MOR && (state === BillingCollectionState.NO_SUBSCRIPTION || state === BillingCollectionState.CANCELED)
}

/** Where a billing account's card is on the account's page, for a link to it. */
function cardOf(b: BillingAccount): string {
	return `billing-account-${b.id}`
}

/** Who a billing account's bills go to, in a line: what is said of it, and nothing that is not. */
function recipientText(b: BillingAccount): string {
	const r = b.recipient
	return [r?.name ?? '', r?.email ?? '', r?.address ?? ''].filter((v) => v !== '').join(' · ')
}

/**
 * One billing account, by what it is called (`accountNames`): what it is
 * billed through and in, who its bills go to, whether it can be collected,
 * and how it pays.
 */
function AccountRow(props: { b: BillingAccount; called: string; detail: Detail | undefined; busy: boolean; write: Write; onStart: (b: BillingAccount) => void }): ReactNode {
	const { b } = props
	const got = props.detail?.got
	const bad = props.detail?.bad ?? ''
	const [renaming, setRenaming] = useState(false)
	const [billing, setBilling] = useState(false)

	const s = got?.subscription
	const state = got?.collection?.state ?? BillingCollectionState.UNSPECIFIED
	const opens = checksOut(b, state)
	const items: [ReactNode, ReactNode][] = [
		['나라', `${countryName(b.country)} (${b.country})`],
		['채널', channelWord[b.channel] ?? '-'],
		['통화', b.currency],
	]
	const to = recipientText(b)
	if (to !== '') items.push(['받는 사람', to])
	if (b.business !== undefined) items.push(['사업자', [b.business.name, b.business.registration].filter((v) => v !== '').join(' · ') || '-'])
	if (got !== undefined && b.channel === BillingChannel.MOR) {
		items.push(['수금', <CollectionBadge key="c" c={got.collection} />])
		if (s !== undefined) {
			items.push(['구독', statusWord[s.status] ?? '-'])
			if (s.nextBilled !== undefined) items.push(['다음 청구', fmt(s.nextBilled, 'date')])
			if (s.change !== BillingSubscriptionChange.UNSPECIFIED) items.push(['예정된 변경', `${changeWord[s.change] ?? ''} · ${fmt(s.changeAt, 'date')}`])
			items.push(['결제수단', methodText(s.method)])
		}
	}
	return (
		<li aria-label={props.called} id={cardOf(b)}>
			<div>
				<strong>{props.called}</strong>
				{/* One with no name is called by when it was made, which says the day already. */}
				{b.name.trim() !== '' && <span className="mute small"> {fmt(b.created, 'date')}에 만듦</span>}
				<Kv items={items} />
				{got !== undefined && collectionDo[state] !== undefined && <p className="small">{collectionDo[state]}</p>}
				{(got?.dunning ?? []).map((d) => (
					<p key={d.invoice} className="note warn" role="status">
						{dunningSaid(d, got?.collection)}
					</p>
				))}
				{bad !== '' && <p className="bad small">{bad}</p>}
			</div>
			<div className="answer">
				{opens && (
					<button className="primary" disabled={props.busy} onClick={() => props.onStart(b)}>
						결제수단 등록
					</button>
				)}
				<button disabled={props.busy} onClick={() => setRenaming(true)}>
					이름 바꾸기
				</button>
				<button disabled={props.busy} onClick={() => setBilling(true)}>
					청구 정보 바꾸기
				</button>
			</div>
			{renaming && <Rename b={b} called={props.called} write={props.write} onClose={() => setRenaming(false)} />}
			{billing && <BilledToForm b={b} called={props.called} write={props.write} onClose={() => setBilling(false)} />}
		</li>
	)
}

/**
 * The billing accounts that cannot be collected from yet -- before anything
 * of theirs failed (levyan's `docs/apps.md`, 4) -- above everything else on
 * the account's page: each by what it is called, how levyan says it stands
 * and what that asks of the payer, the workspaces it pays for, and its
 * action -- the checkout, where a registered method is what it lacks, and
 * its own card otherwise, which says what Paddle asks. Nothing is restricted
 * for it, and it says so. Of nothing levyan did not answer (`useDetails`).
 */
function Uncollectable(props: {
	accounts: BillingAccount[]
	links: BillingLink[]
	called: (b: BillingAccount) => string
	details: Record<string, Detail>
	named: (id: string) => string
	busy: boolean
	onStart: (b: BillingAccount) => void
}): ReactNode {
	const now = new Date()
	const stuck = props.accounts.flatMap((b) => {
		const state = props.details[b.id]?.got?.collection?.state
		return state !== undefined && collectionDo[state] !== undefined ? [{ b, state }] : []
	})
	if (stuck.length === 0) return null
	return (
		<Card title="결제할 수 없는 결제 계정" className="uncollectable">
			<p className="small">아래 결제 계정으로는 지금 결제할 수 없습니다. 이 때문에 제한된 것은 없습니다.</p>
			<ul>
				{stuck.map(({ b, state }) => {
					const pays = props.links
						.filter((l) => {
							const end = dateOf(l.end)
							return l.billingAccount === b.id && (end === undefined || end > now)
						})
						.map((l) => props.named(l.workspace))
					return (
						<li key={b.id} aria-label={props.called(b)}>
							<div>
								<strong>{props.called(b)}</strong> <CollectionBadge c={props.details[b.id]?.got?.collection} />
								<p className="small">
									{collectionDo[state]}
									{pays.length > 0 && ` 이 결제 계정으로 내는 워크스페이스: ${pays.join(', ')}.`}
								</p>
							</div>
							<div className="answer">
								{checksOut(b, state) ? (
									<button className="primary" disabled={props.busy} onClick={() => props.onStart(b)}>
										결제수단 등록
									</button>
								) : (
									<a href={`#${cardOf(b)}`}>결제 계정 보기</a>
								)}
							</div>
						</li>
					)
				})}
			</ul>
		</Card>
	)
}

/**
 * A billing account's name said again: what it alone is asked
 * (`UpdateBillingAccount` with `name` and nothing else), so an empty one
 * takes the name away, and the account is called by when it was made again.
 */
function Rename(props: { b: BillingAccount; called: string; write: Write; onClose: () => void }): ReactNode {
	const [name, setName] = useState(props.b.name)
	return (
		<FormModal
			title={`${props.called} 이름 바꾸기`}
			submit="바꾸기"
			onClose={props.onClose}
			onSubmit={() => {
				const said = name.trim()
				return props.write(
					() => call(PaymentService.method.updateBillingAccount, { id: props.b.id, name: said }),
					said === '' ? '이름을 지웠습니다.' : '이름을 바꿨습니다.',
				)
			}}
		>
			<Field label="이름" hint="여러 결제 계정을 구분하는 이름입니다. 비우면 이름을 지우고, 만든 때로 부릅니다." wide>
				<input value={name} onChange={(e) => setName(e.target.value)} autoFocus />
			</Field>
		</FormModal>
	)
}

type Write = (f: () => Promise<unknown>, ok: string) => Promise<boolean>

/**
 * What a list of billing accounts offers a choice of: each by what it is
 * called (`accountNames`), where it is billed and in what, and how it pays
 * where the page was told (`useDetails`) -- asked of nobody for this.
 */
function accountOptions(accounts: BillingAccount[], called: (b: BillingAccount) => string, details: Record<string, Detail>): { value: string; label: string }[] {
	return accounts.map((b) => {
		const m = details[b.id]?.got?.subscription?.method
		const pays = m === undefined || m.type === '' ? [] : [methodText(m)]
		return { value: b.id, label: [called(b), countryName(b.country), b.currency, ...pays].join(' · ') }
	})
}

/** The offers made to the account to pay for a workspace. */
function Offers(props: {
	offers: BillingOffer[]
	accounts: BillingAccount[]
	called: (b: BillingAccount) => string
	details: Record<string, Detail>
	named: (id: string) => string
	busy: boolean
	write: Write
}): ReactNode {
	const [accepting, setAccepting] = useState<BillingOffer>()
	const [with_, setWith] = useState('')
	return (
		<Card title="받은 제안">
			{props.offers.length === 0 ? (
				<Empty>결제 담당이 되어 달라는 제안이 없습니다.</Empty>
			) : (
				<ul>
					{props.offers.map((o) => (
						<li key={o.id}>
							<div>
								<strong>{props.named(o.workspace)}</strong>
								<p className="small">{fmt(o.offered, 'date')}에 결제 담당이 되어 달라는 제안을 받았습니다. 받아들이면 이번 기간이 끝난 뒤부터 냅니다.</p>
							</div>
							<div className="answer">
								<button
									className="primary"
									disabled={props.busy}
									onClick={() => {
										setWith(props.accounts[0]?.id ?? '')
										setAccepting(o)
									}}
								>
									받아들이기
								</button>
								<Confirm
									label="거절"
									question={`${props.named(o.workspace)}의 결제 담당 제안을 거절할까요?`}
									disabled={props.busy}
									onYes={() => props.write(() => call(PaymentService.method.decline, { offer: o.id }), '제안을 거절했습니다.')}
								/>
							</div>
						</li>
					))}
				</ul>
			)}
			{accepting !== undefined && (
				<FormModal
					title={`${props.named(accepting.workspace)}의 결제 담당`}
					submit="받아들이기"
					onClose={() => setAccepting(undefined)}
					onSubmit={() =>
						props.write(
							() => call(PaymentService.method.accept, { offer: accepting.id, billingAccount: with_ }),
							'제안을 받아들였습니다. 이번 기간이 끝난 뒤부터 냅니다.',
						)
					}
				>
					<Field label="낼 결제 계정" hint="나중에 정해도 됩니다. 정하기 전까지는 기본 조건으로 매깁니다.">
						<Select value={with_} onChange={setWith} empty="나중에 정하기" options={accountOptions(props.accounts, props.called, props.details)} />
					</Field>
				</FormModal>
			)}
		</Card>
	)
}

/** The workspaces the account pays for, or will from the end of the period. */
function Links(props: {
	links: BillingLink[]
	accounts: BillingAccount[]
	called: (b: BillingAccount) => string
	details: Record<string, Detail>
	named: (id: string) => string
	busy: boolean
	write: Write
}): ReactNode {
	const [using, setUsing] = useState<BillingLink>()
	const [with_, setWith] = useState('')
	const account = (id: string) => props.accounts.find((b) => b.id === id)
	return (
		<Card title="내는 워크스페이스">
			{props.links.length === 0 ? (
				<Empty>내고 있는 워크스페이스가 없습니다.</Empty>
			) : (
				<ul>
					{props.links.map((l) => {
						const b = account(l.billingAccount)
						const ended = l.end !== undefined
						return (
							<li key={l.id}>
								<div>
									<strong>{props.named(l.workspace)}</strong>
									<p className="small">
										{b === undefined ? '결제 계정을 아직 정하지 않음' : `${props.called(b)} · ${b.currency}`} · {fmt(l.start, 'date')}부터
										{ended ? ` ${fmt(l.end, 'date')}까지 (${endedWord[l.ended] ?? '끝남'})` : ''} · {beganWord[l.began] ?? ''}
									</p>
								</div>
								{!ended && (
									<div className="answer">
										<button
											disabled={props.busy || props.accounts.length === 0}
											onClick={() => {
												setWith(props.accounts.find((b) => b.id !== l.billingAccount)?.id ?? props.accounts[0]?.id ?? '')
												setUsing(l)
											}}
										>
											결제 계정 바꾸기
										</button>
										<Confirm
											label="그만 내기"
											danger
											disabled={props.busy}
											question={`이번 기간이 끝나면 ${props.named(l.workspace)}를 더 내지 않습니다. 그 뒤로 아무도 내지 않으면 30일 뒤에 제한됩니다.`}
											onYes={() => props.write(() => call(PaymentService.method.stop, { workspace: l.workspace }), '이번 기간이 끝나면 더 내지 않습니다.')}
										/>
									</div>
								)}
							</li>
						)
					})}
				</ul>
			)}
			{using !== undefined && (
				<FormModal
					title={`${props.named(using.workspace)}를 낼 결제 계정`}
					submit="바꾸기"
					onClose={() => setUsing(undefined)}
					onSubmit={() =>
						props.write(
							() => call(PaymentService.method.use, { workspace: using.workspace, billingAccount: with_ }),
							'이번 기간이 끝난 뒤부터 이 결제 계정으로 냅니다.',
						)
					}
				>
					<Field label="결제 계정">
						<Select value={with_} onChange={setWith} options={accountOptions(props.accounts, props.called, props.details)} required />
					</Field>
				</FormModal>
			)}
		</Card>
	)
}

/** The invoices of the account's billing accounts, and what credit notes took back of them. */
function Invoices(props: { invoices: BillingInvoice[]; notes: BillingCreditNote[]; accounts: BillingAccount[]; named: (id: string) => string }): ReactNode {
	const [open, setOpen] = useState<BillingInvoice>()
	const of = (id: string) => props.invoices.find((v) => v.id === id)
	return (
		<>
			<Card title="청구서">
				{props.invoices.length === 0 ? (
					<Empty>아직 청구서가 없습니다.</Empty>
				) : (
					<div className="table-wrap">
						<table className="table">
							<thead>
								<tr>
									<th>기간</th>
									<th>상태</th>
									<th className="num">합계</th>
									<th className="num">남은 금액</th>
									<th>확정</th>
									<th />
								</tr>
							</thead>
							<tbody>
								{props.invoices.map((v) => (
									<tr key={v.id}>
										<td>{spanText(v.start, v.end)}</td>
										<td>
											<Badge v={invoiceWord[v.state]?.[1] ?? 'mute'}>{invoiceWord[v.state]?.[0] ?? '-'}</Badge>
										</td>
										<td className="num">{money(v.total, v.currency)}</td>
										<td className="num">{money(v.remaining, v.currency)}</td>
										<td>{fmt(v.finalized, 'date')}</td>
										<td>
											<button className="link" onClick={() => setOpen(v)}>
												보기
											</button>
										</td>
									</tr>
								))}
							</tbody>
						</table>
					</div>
				)}
			</Card>
			<Card title="정정 청구">
				{props.notes.length === 0 ? (
					<Empty>정정 청구가 없습니다.</Empty>
				) : (
					<div className="table-wrap">
						<table className="table">
							<thead>
								<tr>
									<th>청구서</th>
									<th>사유</th>
									<th className="num">금액</th>
									<th>만든 날</th>
								</tr>
							</thead>
							<tbody>
								{props.notes.map((n) => {
									const inv = of(n.invoice)
									return (
										<tr key={n.id}>
											<td>{inv === undefined ? '-' : spanText(inv.start, inv.end)}</td>
											<td>{reasonWord[n.reason] ?? '-'}</td>
											<td className="num">{money(n.total, inv?.currency)}</td>
											<td>{fmt(n.created, 'date')}</td>
										</tr>
									)
								})}
							</tbody>
						</table>
					</div>
				)}
			</Card>
			{open !== undefined && <InvoiceModal invoice={open} named={props.named} onClose={() => setOpen(undefined)} />}
		</>
	)
}

/** An invoice, with its lines and its credit notes. */
function InvoiceModal(props: { invoice: BillingInvoice; named: (id: string) => string; onClose: () => void }): ReactNode {
	const [v, setV] = useState<PaymentGetInvoiceResponse>()
	const [bad, setBad] = useState('')
	useEffect(() => {
		call(PaymentService.method.getInvoice, { id: props.invoice.id }).then(setV, (err) => setBad(errorText(err)))
	}, [props.invoice.id])
	const inv = v?.invoice ?? props.invoice
	return (
		<Modal title={`청구서 · ${spanText(inv.start, inv.end)}`} onClose={props.onClose} wide>
			{bad !== '' && <p className="bad">{bad}</p>}
			{v?.dunning !== undefined && (
				<p className="note warn" role="status">
					{dunningSaid(v.dunning, undefined)}
				</p>
			)}
			<Kv
				items={[
					['상태', invoiceWord[inv.state]?.[0] ?? '-'],
					['채널', channelWord[inv.channel] ?? '-'],
					['소계', money(inv.subtotal, inv.currency)],
					...(inv.tax > 0n ? ([['세금', money(inv.tax, inv.currency)]] as [ReactNode, ReactNode][]) : []),
					['합계', money(inv.total, inv.currency)],
					['낸 금액', money(inv.paid, inv.currency)],
					['남은 금액', money(inv.remaining, inv.currency)],
					...(inv.channelNumber !== '' ? ([['Paddle의 번호', inv.channelNumber]] as [ReactNode, ReactNode][]) : []),
				]}
			/>
			{v !== undefined && (
				<table className="table">
					<thead>
						<tr>
							<th>워크스페이스</th>
							<th>기간</th>
							<th className="num">금액</th>
						</tr>
					</thead>
					<tbody>
						{inv.lines.map((l) => (
							<tr key={l.statement}>
								<td>{l.label || props.named(l.workspace)}</td>
								<td>{periodText(l.period)}</td>
								<td className="num">{money(l.total, inv.currency)}</td>
							</tr>
						))}
					</tbody>
				</table>
			)}
			{v !== undefined && v.creditNotes.length > 0 && (
				<ul>
					{v.creditNotes.map((n) => (
						<li key={n.id}>
							{reasonWord[n.reason] ?? '정정'} · {money(n.total, inv.currency)} · {fmt(n.created, 'date')}
						</li>
					))}
				</ul>
			)}
		</Modal>
	)
}

// ---------------------------------------------------------------------------
// A workspace's, in its settings.

/** A member who signs in: their membership, what they are called, and told apart by (D54). */
type Member = { holder: Uint8Array; name: string; called: string; role: string; leaving: boolean }

/** The workspace's members who sign in, by the account each is a membership of. */
function useMembers(): Map<string, Member> {
	const c = useCatalog()
	const holders = useRpc(HolderService.method.list, { size: 500 })
	return useMemo(() => {
		const out = new Map<string, Member>()
		for (const h of holders.data?.items ?? []) {
			const account = idStr(h.account)
			if (account === '') continue
			const party = c.parties.find((p) => idStr(p.holder?.id) === idStr(h.id))
			const name = party?.name || h.name || h.alias
			out.set(account, {
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

/** Who pays for the workspace, what it used this period and its statements; and, for its owner, asking a member to pay. */
export function PaymentTab(props: { account: Account }): ReactNode {
	const c = useCatalog()
	const payer = useRpc(PaymentService.method.getPayer, {})
	const usage = useRpc(PaymentService.method.getUsage, {})
	const statements = useRpc(PaymentService.method.listStatements, {})
	const members = useMembers()
	const nameOf = (account: string | undefined): string => {
		if (account === undefined || account === '') return ''
		if (account === props.account.id) return '나'
		return members.get(account)?.name ?? '이 워크스페이스에 없는 계정'
	}
	// The owner taking it on themself, in one go, where the account's own
	// calls are made: never on the workspace's own domain (loft#155).
	const mine = members.get(props.account.id)
	const takes = c.can('owner') && props.account.pinned === undefined && mine !== undefined && !mine.leaving
	return (
		<>
			<Card title="결제 담당" actions={takes && payer.data !== undefined && <TakeOver payer={payer.data.payer} me={props.account.id} holder={mine?.holder} />}>
				<Load q={payer}>{(v) => <PayerSaid payer={v.payer} nameOf={nameOf} me={props.account.id} page={billingPage(props.account)} />}</Load>
			</Card>
			{c.can('owner') && (
				<Load q={payer}>{(v) => <OfferCard payer={v.payer} members={members} me={props.account.id} takes={takes} />}</Load>
			)}
			<Card title="이번 기간 사용량과 예상 금액">
				<Load q={usage}>
					{(v) =>
						v.usage === undefined ? (
							<Empty />
						) : (
							<>
								<p className="mute small">
									{periodText(v.usage.period)} · {fmt(v.usage.asOf)} 기준. 기간이 끝나면 결제 서비스가 확정합니다.
								</p>
								{v.usage.lines.length === 0 ? (
									<Empty>아직 잰 것이 없습니다.</Empty>
								) : (
									<table className="table">
										<thead>
											<tr>
												<th>항목</th>
												<th className="num">사용량</th>
												<th className="num">금액</th>
											</tr>
										</thead>
										<tbody>
											{v.usage.lines.map((l, i) => (
												<tr key={`${l.meter}-${i}`}>
													<td>
														{meterWord[l.meter] ?? l.meter}
														{l.corrects !== undefined && <span className="mute small"> (정정: {periodText(l.corrects)})</span>}
													</td>
													<td className="num">{usageText(l)}</td>
													<td className="num">{v.usage?.currency === '' ? '-' : lineAmount(l.amount, v.usage?.currency ?? '')}</td>
												</tr>
											))}
										</tbody>
									</table>
								)}
								{v.usage.currency === '' ? (
									<p className="note warn">아직 가격이 정해지지 않아 금액이 없습니다.</p>
								) : (
									<p>
										예상 합계 <strong>{money(v.usage.total, v.usage.currency)}</strong>
									</p>
								)}
							</>
						)
					}
				</Load>
			</Card>
			<Card title="지난 명세서">
				<Load q={statements}>
					{(v) =>
						v.statements.length === 0 ? (
							<Empty>닫힌 기간이 아직 없습니다.</Empty>
						) : (
							<table className="table">
								<thead>
									<tr>
										<th>기간</th>
										<th className="num">합계</th>
										<th>닫은 날</th>
									</tr>
								</thead>
								<tbody>
									{v.statements.map((s) => (
										<tr key={s.id}>
											<td>{periodText(s.period)}</td>
											<td className="num">{money(s.total, s.currency)}</td>
											<td>{fmt(s.closed, 'date')}</td>
										</tr>
									))}
								</tbody>
							</table>
						)
					}
				</Load>
			</Card>
		</>
	)
}

/** Who pays, as levyan says it. */
function PayerSaid(props: { payer: BillingPayer | undefined; nameOf: (account: string | undefined) => string; me: string; page: string }): ReactNode {
	const p = props.payer
	if (p === undefined || (p.current === undefined && p.next === undefined && p.offer === undefined && p.payerlessSince === undefined && p.dunning.length === 0)) {
		return <p className="mute">결제 서비스가 아직 이 워크스페이스의 결제 담당을 모릅니다. 잠시 뒤에 다시 보세요.</p>
	}
	const restricted = dateOf(p.restrictedFrom)
	const now = new Date()
	const offeredToMe = p.offer !== undefined && p.offer.account === props.me
	const paysMe = p.current?.account === props.me || p.next?.account === props.me
	return (
		<>
			<Kv
				items={[
					...(p.current !== undefined
						? ([['지금 내는 사람', `${props.nameOf(p.current.account)} · ${fmt(p.current.start, 'date')}부터${p.current.end !== undefined ? ` ${fmt(p.current.end, 'date')}까지` : ''}`]] as [ReactNode, ReactNode][])
						: []),
					...(p.next !== undefined ? ([['다음 기간부터', props.nameOf(p.next.account)]] as [ReactNode, ReactNode][]) : []),
					...(p.offer !== undefined ? ([['제안', `${props.nameOf(p.offer.account)} · ${fmt(p.offer.offered, 'date')}에 제안함`]] as [ReactNode, ReactNode][]) : []),
					...(p.collection !== undefined ? ([['수금', <CollectionBadge key="c" c={p.collection} />]] as [ReactNode, ReactNode][]) : []),
				]}
			/>
			{p.payerlessSince !== undefined && (
				<p className="note warn" role="status">
					{fmt(p.payerlessSince, 'date')}부터 결제 담당이 없습니다.{' '}
					{restricted !== undefined && (restricted <= now ? `${fmt(restricted, 'date')}부터 제한되었습니다.` : `${fmt(restricted, 'date')}부터 제한됩니다.`)} 구성원에게 결제 담당을 제안하고, 그 사람이 결제 화면에서 받아들이면 풀립니다.
				</p>
			)}
			{p.collection !== undefined && collectionDo[p.collection.state] !== undefined && (
				<p className="small">결제 담당의 결제 계정: {collectionDo[p.collection.state]}</p>
			)}
			{p.dunning.map((d) => (
				<p key={d.invoice} className="note warn" role="status">
					이 워크스페이스를 청구한 청구서의 {dunningSaid(d, undefined)} 결제 담당이 결제 화면에서 정리할 수 있습니다.
				</p>
			))}
			{(offeredToMe || paysMe) && (
				<p>
					<a href={props.page}>{offeredToMe ? '결제 화면에서 제안 받아들이기' : '결제 화면으로'}</a>
				</p>
			)}
		</>
	)
}

/**
 * The owner asking a member to pay for the workspace: who signs in, is not
 * leaving, and does not pay already -- themself too, which `takes` (the
 * owner may take it on in one go, `TakeOver`) does at once.
 */
function OfferCard(props: { payer: BillingPayer | undefined; members: Map<string, Member>; me: string; takes: boolean }): ReactNode {
	const act = useAct()
	const pays = new Set([props.payer?.current?.account, props.payer?.next?.account].filter((v): v is string => v !== undefined && v !== ''))
	const options = [...props.members.entries()]
		.filter(([account, m]) => !m.leaving && !pays.has(account))
		.map(([account, m]) => ({ value: idStr(m.holder), label: `${account === props.me ? `${m.called} (나)` : m.called} · ${roleWord[m.role] ?? m.role}` }))
	const [holder, setHolder] = useState('')
	const chosen = options.some((o) => o.value === holder) ? holder : ''
	return (
		<Card title="결제 담당 제안">
			<p className="small">
				제안받은 사람이 자기 결제 화면에서 받아들이면 이번 기간이 끝난 뒤부터 그 사람이 냅니다. 새 제안은 전의 제안을 거둡니다.{' '}
				{pays.has(props.me) ? '' : props.takes ? '내가 내려면 위의 "내가 결제 담당 맡기"로 바로 맡습니다.' : '내가 내려면 나에게 제안하고 결제 화면에서 받아들이세요.'}
			</p>
			{options.length === 0 ? (
				<Empty>제안할 수 있는 구성원이 없습니다. 로그인하는 구성원에게만 제안할 수 있습니다.</Empty>
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

/**
 * The owner paying for the workspace themself, in one go: what was an offer
 * to themself here and its answer on their own page is both at once -- loft's
 * own `Offer` to their membership, then `Accept` as the account, with the
 * billing account they pick -- and no call of levyan's beside those two. As
 * levyan has every change of payer, from the end of the period now open:
 * whoever pays now pays until then. Not offered to an owner who pays already,
 * now or from then.
 */
function TakeOver(props: { payer: BillingPayer | undefined; me: string; holder: Uint8Array | undefined }): ReactNode {
	const [open, setOpen] = useState(false)
	const pays = props.payer?.current?.account === props.me || props.payer?.next?.account === props.me
	if (pays || props.holder === undefined) return null
	const now = props.payer?.current?.account
	return (
		<>
			<button className="primary" onClick={() => setOpen(true)}>
				내가 결제 담당 맡기
			</button>
			{open && <TakeOverForm holder={props.holder} someone={now !== undefined && now !== ''} onClose={() => setOpen(false)} />}
		</>
	)
}

/**
 * Which of their billing accounts the owner pays with, asked of the account's
 * own calls as the form opens (`call`, kept for nobody offline, as on the
 * account's page), and the two calls that make them the payer. An offer made
 * that could not be accepted stays the owner's to answer on their own page,
 * and the form says so.
 */
function TakeOverForm(props: { holder: Uint8Array; someone: boolean; onClose: () => void }): ReactNode {
	const toast = useToast()
	const { bump } = useEpoch()
	const [accounts, setAccounts] = useState<BillingAccount[]>()
	const [bad, setBad] = useState('')
	const [with_, setWith] = useState('')
	useEffect(() => {
		let gone = false
		call(PaymentService.method.listBillingAccounts, {}).then(
			(v) => {
				if (gone) return
				setAccounts(v.billingAccounts)
				setWith(v.billingAccounts[0]?.id ?? '')
			},
			(err) => !gone && setBad(errorText(err)),
		)
		return () => {
			gone = true
		}
	}, [])
	const details = useDetails(accounts)
	const called = accountNames(accounts ?? [])
	return (
		<FormModal
			title="내가 결제 담당 맡기"
			submit="맡기"
			onClose={props.onClose}
			onSubmit={async () => {
				let offer = ''
				try {
					offer = (await call(PaymentService.method.offer, { holder: props.holder })).offer?.id ?? ''
				} catch (err) {
					toast(errorText(err), 'bad')
					return false
				}
				try {
					await call(PaymentService.method.accept, { offer, billingAccount: with_ })
				} catch (err) {
					toast(`나에게 결제 담당을 제안했지만 받아들이지 못했습니다. 결제 화면의 받은 제안에서 받아들일 수 있습니다. ${errorText(err)}`, 'bad')
					bump()
					return false
				}
				toast('결제 담당을 맡았습니다. 이번 기간이 끝난 뒤부터 냅니다.')
				bump()
				return true
			}}
		>
			<p>
				이번 기간이 끝난 뒤부터 내가 이 워크스페이스를 냅니다.{props.someone ? ' 그때까지는 지금 내는 사람이 냅니다.' : ''} 결제 계정은 결제 화면에서 만들고 바꿀 수
				있습니다.
			</p>
			{bad !== '' ? (
				<p className="bad">결제 계정을 불러오지 못했습니다. {bad}</p>
			) : accounts === undefined ? (
				<Spinner />
			) : (
				<Field
					label="낼 결제 계정"
					hint={accounts.length === 0 ? '아직 결제 계정이 없습니다. 정하기 전까지는 기본 조건으로 매깁니다.' : '나중에 정해도 됩니다. 정하기 전까지는 기본 조건으로 매깁니다.'}
					wide
				>
					<Select value={with_} onChange={setWith} empty="나중에 정하기" options={accountOptions(accounts, called, details)} />
				</Field>
			)}
		</FormModal>
	)
}

// ---------------------------------------------------------------------------
// The banner.

/** What the account itself has to do with the workspace it is in, by its own calls (`usePaysHere`). */
type Here = {
	/** What it pays for the workspace with now, when it does. */
	pays?: BillingLink
	/** What it was asked to pay for it, and has not answered. */
	offered?: BillingOffer
	/** The failed charges of the billing account it pays for it with, the one that restricts soonest first. */
	dunning: BillingDunning[]
}

/**
 * Whether the account pays for the workspace `ws` now, or was asked to, and
 * what the billing account it pays with says of its failed charges: what
 * the banner asks of the account's own calls (`ListLinks`, `ListOffers`,
 * `GetBillingAccount`) for a member who is not one of the workspace's
 * administrators -- who reads nothing of who pays for it (`GetPayer`), and
 * may yet be the one to settle it. Asked with `call` rather than kept for
 * offline, as the account's page is. What could not be asked says nothing:
 * the banner then says what it can without it.
 */
function usePaysHere(ws: string, on: boolean): Here {
	const [here, setHere] = useState<Here>({ dunning: [] })
	useEffect(() => {
		if (!on || ws === '') return
		let gone = false
		const ask = async (): Promise<void> => {
			try {
				const [l, o] = await Promise.all([call(PaymentService.method.listLinks, {}), call(PaymentService.method.listOffers, {})])
				const now = new Date()
				const pays = l.links.find((v) => {
					const start = dateOf(v.start)
					const end = dateOf(v.end)
					return v.workspace === ws && start !== undefined && start <= now && (end === undefined || end > now)
				})
				const offered = o.offers.find((v) => v.workspace === ws)
				const with_ = pays?.billingAccount ?? ''
				const dunning = with_ === '' ? [] : (await call(PaymentService.method.getBillingAccount, { id: with_ })).dunning
				if (!gone) setHere({ pays, offered, dunning })
			} catch {
				// Not the banner's to say: the account's page says why.
			}
		}
		void ask()
		const every = setInterval(() => void ask(), 10 * 60_000)
		return () => {
			gone = true
			clearInterval(every)
		}
	}, [ws, on])
	return on ? here : { dunning: [] }
}

/**
 * What a workspace says above every page while nobody pays for it, while an
 * invoice that bills it failed to be charged, and once it is restricted --
 * what to do, and where (levyan's `docs/apps.md`, 4 and 6). Every member is
 * told it is restricted, by the copy of levyan's grants its writes are held
 * to (`PartyService.Me`) or by a write refused for it. Why, and what comes
 * before, the people who run it are told from who pays for it (`GetPayer`),
 * which they alone read; a member who is not one of them, and pays for it or
 * was asked to, by the account's own calls (`usePaysHere`) -- the payer, of
 * their billing account's failed charges before it restricts, as levyan's
 * table has the payer told. Either is sent to the account's own page to
 * settle it. And while nothing failed and nothing is restricted, the people
 * who run it are told the billing account it is paid for with cannot be
 * collected from yet (`GetPayer`'s collection, `CollectionBanner`), which
 * restricts nothing.
 */
export function BillingBanner(props: { account: Account }): ReactNode {
	const c = useCatalog()
	const refused = useRestricted()
	const billed = props.account.billing === true
	const admin = c.can('admin')
	const payer = useRpc(PaymentService.method.getPayer, billed && admin ? {} : null, { poll: 10 * 60_000 })
	const p = payer.data?.payer
	// The account's own calls are its own, outside every workspace, and a
	// workspace's own domain serves none of them.
	const ws = props.account.workspaces.find((w) => w.alias === workspace)?.id ?? ''
	const mine = usePaysHere(ws, billed && !admin && props.account.pinned === undefined)
	const now = new Date()

	// What restricts it, or will: having no payer, and the invoice that
	// restricts soonest of those whose charge failed -- of those that bill
	// the workspace, or, for a payer who reads nothing of the workspace's,
	// of the billing account it pays with.
	const payerlessFrom = p?.payerlessSince !== undefined ? dateOf(p.restrictedFrom) : undefined
	const dunning = admin ? p?.dunning[0] : mine.dunning[0]
	const dunningFrom = dateOf(dunning?.restrictedFrom)
	const passed = (v: Date | undefined): v is Date => v !== undefined && v <= now
	const restricted = c.me.restricted || refused || passed(payerlessFrom) || passed(dunningFrom)

	const payerIsMe = admin ? p?.current !== undefined && p.current.account === props.account.id : mine.pays !== undefined
	const offeredToMe = admin ? p?.offer !== undefined && p.offer.account === props.account.id : mine.offered !== undefined
	if (!restricted && payerlessFrom === undefined && dunning === undefined) {
		// Nothing failed, and nothing is restricted: whether the billing
		// account it is paid for with can be collected, to the people who
		// run it, who alone read who pays for it.
		const state = p?.collection?.state
		if (!admin || state === undefined || collectionDo[state] === undefined) return null
		return <CollectionBanner state={state} payerIsMe={payerIsMe} owner={c.can('owner')} page={billingPage(props.account)} />
	}
	const grows = '자산 등록, 초대, 첨부 올리기처럼 청구되는 것을 늘리는 일'

	let said: ReactNode
	if (restricted) {
		const why = passed(payerlessFrom) ? '결제 담당이 없어 ' : passed(dunningFrom) ? '결제가 밀려 ' : ''
		const since = passed(payerlessFrom) ? payerlessFrom : passed(dunningFrom) ? dunningFrom : undefined
		said = (
			<>
				<strong>
					이 워크스페이스는 {since !== undefined ? `${fmt(since, 'date')}부터 ` : ''}
					{why}제한되었습니다.
				</strong>
				<span>{grows}을 할 수 없습니다. 보기, 내보내기, 지급·반납은 그대로입니다. 결제를 정리하면 다시 할 수 있습니다.</span>
			</>
		)
	} else if (dunning !== undefined) {
		said = (
			<>
				<strong>{admin ? '이 워크스페이스를 청구한 청구서의 결제가 실패했습니다.' : '이 워크스페이스를 내는 결제 계정의 청구서 결제가 실패했습니다.'}</strong>
				<span>
					{dunningFrom !== undefined ? `${fmt(dunningFrom, 'date')}까지 결제되지 않으면 ` : ''}제한되어 {grows}을 할 수 없게 됩니다.
				</span>
			</>
		)
	} else {
		said = (
			<>
				<strong>이 워크스페이스에는 결제 담당이 없습니다.</strong>
				<span>
					{payerlessFrom !== undefined ? `${fmt(payerlessFrom, 'date')}부터 ` : ''}제한되어 {grows}을 할 수 없게 됩니다.
				</span>
			</>
		)
	}

	// Where to go: the payer's own page for what is theirs to settle, the
	// workspace's settings for its owner and administrators.
	const payerless = payerlessFrom !== undefined
	const page = billingPage(props.account)
	let go: ReactNode
	if (offeredToMe) {
		go = <a href={page}>결제 담당 제안을 받았습니다. 결제 화면에서 받아들이기 →</a>
	} else if (payerIsMe && !payerless) {
		go = <a href={page}>결제 화면에서 정리하기 →</a>
	} else if (c.can('owner')) {
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
 * the words the payer is told it in on their own page (`collectionDo`), and
 * the way to it. It restricts nothing, and says so: what fails later is
 * dunning's to say (`BillingBanner`). The payer is sent to their own page --
 * to register a method where that is what it lacks -- and the others to the
 * workspace's billing, where its owner may take it on themself (`TakeOver`).
 */
function CollectionBanner(props: { state: BillingCollectionState; payerIsMe: boolean; owner: boolean; page: string }): ReactNode {
	const word = collectionWord[props.state]?.[0] ?? ''
	const asked = collectionDo[props.state] ?? ''
	const registers = props.state === BillingCollectionState.NO_SUBSCRIPTION || props.state === BillingCollectionState.CANCELED
	let go: ReactNode
	if (props.payerIsMe) {
		go = <a href={props.page}>{registers ? '결제 화면에서 결제수단 등록하기 →' : '결제 화면에서 보기 →'}</a>
	} else if (props.owner) {
		go = <Link to="/settings?tab=billing">결제 설정 보기 →</Link>
	} else {
		go = <Link to="/settings?tab=billing">결제 설정 보기 → (결제 담당은 소유자가 정합니다)</Link>
	}
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
