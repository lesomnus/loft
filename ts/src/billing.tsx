/**
 * How a workspace is paid for (design 7.9, levyan#14): what levyan, the
 * billing service, says of it, relayed by `loft.PaymentService` and kept
 * nowhere here.
 *
 * - **The account's own page**, `/billing`: about no workspace, as the
 *   invitations are, since what a payer pays with is its own and its calls are
 *   the account's (payday#184). Its billing accounts -- made by naming a
 *   country, and sold through the channel and billed in the currency levyan
 *   says of it -- whether each can be collected, the subscription it is
 *   collected through and how it pays, which only ever says the kind, the
 *   brand and the last four digits; the checkout that starts one, opened on
 *   the page Paddle's checkout opens on (`/billing/checkout`) and asked about
 *   again as the payer comes back here; the offers to pay for a workspace, the
 *   workspaces it pays for, its invoices and credit notes.
 * - **A workspace's**, in its settings for the people who run it: who pays for
 *   it, what it has used this period and what that would come to, its
 *   statements, and its owner asking a member to pay.
 * - **The banner** a workspace shows while nobody pays for it, or once it is
 *   restricted: what to do, and where.
 *
 * @module
 */

import { useCallback, useEffect, useMemo, useState, type ReactNode } from 'react'
import { Link } from 'react-router-dom'
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
} from '../gen/loft/payment_pb.js'
import { HolderService, PaymentService, dateOf, errorText, fmt, idStr, money, openWorkspace, workspace, type Account } from './api.js'
import { useCatalog } from './catalog.js'
import { call, useAct, useRestricted, useRpc } from './rpc.js'
import { Badge, Card, Confirm, Empty, Field, FormModal, Kv, Load, Modal, Select, useToast } from './ui.js'
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
										<AccountRow key={b.id} b={b} n={n} busy={busy} onStart={(b) => void start(b)} />
									))}
								</ul>
							)}
						</Card>
						<Offers offers={v.offers} accounts={v.accounts} named={named} busy={busy} write={write} />
						<Links links={v.links} accounts={v.accounts} named={named} busy={busy} write={write} />
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

/** A billing account made by naming its country: the channel and the currency are levyan's to say of it. */
function NewAccount(props: { onClose: () => void; onMade: (b: BillingAccount) => void }): ReactNode {
	const [country, setCountry] = useState('KR')
	const toast = useToast()
	return (
		<FormModal
			title="새 결제 계정"
			submit="만들기"
			onClose={props.onClose}
			onSubmit={async () => {
				try {
					const r = await call(PaymentService.method.createBillingAccount, { country })
					if (r.billingAccount !== undefined) props.onMade(r.billingAccount)
					return true
				} catch (err) {
					toast(errorText(err), 'bad')
					return false
				}
			}}
		>
			<Field label="나라" hint="어느 채널로, 어느 통화로 청구할지는 결제 서비스가 나라에 따라 정합니다. 만든 뒤에는 바뀌지 않습니다.">
				<Select value={country} onChange={setCountry} options={countries.map(([value, name]) => ({ value, label: `${name} (${value})` }))} />
			</Field>
		</FormModal>
	)
}

/** One billing account: what it is billed through and in, whether it can be collected, and how it pays. */
function AccountRow(props: { b: BillingAccount; n: number; busy: boolean; onStart: (b: BillingAccount) => void }): ReactNode {
	const { b } = props
	const [got, setGot] = useState<PaymentGetBillingAccountResponse>()
	const [bad, setBad] = useState('')
	useEffect(() => {
		let gone = false
		call(PaymentService.method.getBillingAccount, { id: b.id }).then(
			(v) => !gone && setGot(v),
			(err) => !gone && setBad(errorText(err)),
		)
		return () => {
			gone = true
		}
	}, [b.id, props.n])

	const s = got?.subscription
	const state = got?.collection?.state ?? BillingCollectionState.UNSPECIFIED
	// A checkout begins a subscription where there is none to collect
	// through, which is also what a method to replace asks.
	const opens = b.channel === BillingChannel.MOR && (state === BillingCollectionState.NO_SUBSCRIPTION || state === BillingCollectionState.CANCELED)
	const items: [ReactNode, ReactNode][] = [
		['나라', `${countryName(b.country)} (${b.country})`],
		['채널', channelWord[b.channel] ?? '-'],
		['통화', b.currency],
	]
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
		<li aria-label={b.name || `결제 계정 ${b.country} ${b.currency}`}>
			<div>
				<strong>{b.name || '결제 계정'}</strong> <span className="mute small">{fmt(b.created, 'date')}에 만듦</span>
				<Kv items={items} />
				{got !== undefined && collectionDo[state] !== undefined && <p className="small">{collectionDo[state]}</p>}
				{(got?.dunning ?? []).map((d) => (
					<p key={d.invoice} className="note warn" role="status">
						{dunningSaid(d, got?.collection)}
					</p>
				))}
				{bad !== '' && <p className="bad small">{bad}</p>}
			</div>
			{opens && (
				<div className="answer">
					<button className="primary" disabled={props.busy} onClick={() => props.onStart(b)}>
						결제수단 등록
					</button>
				</div>
			)}
		</li>
	)
}

type Write = (f: () => Promise<unknown>, ok: string) => Promise<boolean>

/** What a list of billing accounts offers a choice of. */
function accountOptions(accounts: BillingAccount[]): { value: string; label: string }[] {
	return accounts.map((b) => ({ value: b.id, label: `${b.name || '결제 계정'} · ${countryName(b.country)} · ${b.currency}` }))
}

/** The offers made to the account to pay for a workspace. */
function Offers(props: { offers: BillingOffer[]; accounts: BillingAccount[]; named: (id: string) => string; busy: boolean; write: Write }): ReactNode {
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
						<Select value={with_} onChange={setWith} empty="나중에 정하기" options={accountOptions(props.accounts)} />
					</Field>
				</FormModal>
			)}
		</Card>
	)
}

/** The workspaces the account pays for, or will from the end of the period. */
function Links(props: { links: BillingLink[]; accounts: BillingAccount[]; named: (id: string) => string; busy: boolean; write: Write }): ReactNode {
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
										{b === undefined ? '결제 계정을 아직 정하지 않음' : `${b.name || '결제 계정'} · ${b.currency}`} · {fmt(l.start, 'date')}부터
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
						<Select value={with_} onChange={setWith} options={accountOptions(props.accounts)} required />
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
	return (
		<>
			<Card title="결제 담당">
				<Load q={payer}>{(v) => <PayerSaid payer={v.payer} nameOf={nameOf} me={props.account.id} page={billingPage(props.account)} />}</Load>
			</Card>
			{c.can('owner') && (
				<Load q={payer}>{(v) => <OfferCard payer={v.payer} members={members} me={props.account.id} />}</Load>
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

/** The owner asking a member to pay for the workspace: who signs in, is not leaving, and does not pay already. */
function OfferCard(props: { payer: BillingPayer | undefined; members: Map<string, Member>; me: string }): ReactNode {
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
				제안받은 사람이 자기 결제 화면에서 받아들이면 이번 기간이 끝난 뒤부터 그 사람이 냅니다. 새 제안은 전의 제안을 거둡니다. 내가 내려면 나에게 제안하고 결제 화면에서 받아들이세요.
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
 * settle it.
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
	if (!restricted && payerlessFrom === undefined && dunning === undefined) return null

	const payerIsMe = admin ? p?.current !== undefined && p.current.account === props.account.id : mine.pays !== undefined
	const offeredToMe = admin ? p?.offer !== undefined && p.offer.account === props.account.id : mine.offered !== undefined
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
