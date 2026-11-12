/**
 * The services, and the small conversions every page needs.
 *
 * A page reads through `useRpc` and writes through `useAct` (`rpc.tsx`), over
 * the one transport here: every call says whose the page is (`callAs`). What
 * is not an RPC -- signing in, who the session is, a download, an upload -- is
 * a `fetch` of its own.
 *
 * @module
 */

import { create, type DescMessage, type MessageInitShape } from '@bufbuild/protobuf'
import { timestampDate, timestampFromDate, type Timestamp, TimestampSchema } from '@bufbuild/protobuf/wkt'
import { BinaryReader, WireType } from '@bufbuild/protobuf/wire'
import { Code, ConnectError, createClient, type Interceptor } from '@connectrpc/connect'
import { createConnectTransport } from '@connectrpc/connect-web'
import { from, newId, parse } from '@lesomnus/payday/pdid'
import { IDENTITY_HEADER } from '@lesomnus/payday/query'

import { EventDomain } from '../gen/domains.js'
import { events } from '../gen/events.js'
import { unreachable } from './offline.js'

import { AllocationService, BookableService, ReservationService } from '../gen/loft/booking_svc_pb.js'
import { AssetService, AttachmentService, LabelService } from '../gen/loft/asset_svc_pb.js'
import { AssetTypeService, ItemModelService } from '../gen/loft/catalog_svc_pb.js'
import { AccessService } from '../gen/loft/payday/access_svc_pb.js'
import { AuditService } from '../gen/loft/payday/audit_svc_pb.js'
import { CountFindingService, InventoryCountService } from '../gen/loft/count_svc_pb.js'
import { PartyService } from '../gen/loft/org_svc_pb.js'
import { CustodyLineService, CustodyService } from '../gen/loft/custody_svc_pb.js'
import { EventService } from '../gen/loft/payday/event_svc_pb.js'
import { HolderService } from '../gen/loft/payday/holder_svc_pb.js'
import { AccessReviewService, NotificationService } from '../gen/loft/ops_svc_pb.js'
import { PurchaseLineService, PurchaseService, WorkOrderService } from '../gen/loft/work_svc_pb.js'
import { StockMovementService, StockService } from '../gen/loft/stock_svc_pb.js'
import { TenantService } from '../gen/loft/payday/tenant_svc_pb.js'
import { PaymentService } from '../gen/loft/payment_pb.js'
import { PermissionService } from '../gen/loft/permission_pb.js'

export {
	AccessReviewService,
	AccessService,
	AllocationService,
	AssetService,
	AssetTypeService,
	AttachmentService,
	AuditService,
	BookableService,
	CountFindingService,
	CustodyLineService,
	CustodyService,
	EventService,
	HolderService,
	InventoryCountService,
	ItemModelService,
	LabelService,
	NotificationService,
	PartyService,
	PaymentService,
	PermissionService,
	PurchaseLineService,
	PurchaseService,
	ReservationService,
	StockMovementService,
	StockService,
	TenantService,
	WorkOrderService,
}

/**
 * Whose the page's calls are: the name of what it keeps for the member it was
 * drawn for, as `GET /session` said it (`Session.identity`), and empty while it
 * is drawn for nobody.
 *
 * Every call says it (`payday-identity`), and the server serves a call that
 * does only as a session of that name. The cookie is the browser's, whoever
 * signed in last in any tab, so a page left behind by somebody else's sign-in
 * is refused, rather than reading their answers into what it keeps for the
 * first person or sending what it kept for them as the second; it then asks
 * again who is signed in (`main.tsx`).
 */
let caller = ''

/** callAs says whose the page's calls are from now on; see {@link caller}. */
export function callAs(identity: string): void {
	caller = identity
}

const asCaller: Interceptor = (next) => async (req) => {
	if (caller !== '') req.header.set(IDENTITY_HEADER, caller)
	return next(req)
}

/**
 * The app answers on the page's own origin: served by it, or through Vite's
 * proxy.
 *
 * It reads with the messages payday's events hold (`gen/events.ts`): an event
 * keeps its value as an `Any`, which JSON names by its type and protobuf-es
 * reads only with that type registered, so without them a list of events with
 * a value in one fails whole. Empty while Loft declares no past of payday's.
 */
export const transport = createConnectTransport({
	baseUrl: location.origin,
	interceptors: [asCaller],
	jsonOptions: { registry: events },
})

/**
 * The workspace the deployment serves (D85), as `GET /session` tells the page
 * of it before it calls anything: enough to draw what a closed one shows,
 * since every call into it is refused (D83).
 */
export interface Workspace {
	id: string
	alias: string
	/** What it is called as it keeps it, or its address for one that keeps none. */
	name: string
	/** Where its dates are drawn: its own zone, or the deployment's. */
	timezone?: string
	/** When it was closed, and when it goes unless an operator restores it (D29). */
	closed?: string
	destroy?: string
}

/** Who this browser is signed in as, as `GET /session` answers it. */
export interface Session {
	/** The member, which is their holder at roster too (D85). */
	id: string
	login: string
	name: string
	/** The methods the session is narrowed to (`loft auth login --allow`, D33); absent is all. */
	allow?: string[]
	/**
	 * Whether this deployment's workspace is billed, by levyan: what the page
	 * offers its billing for (design 7.9).
	 */
	billing?: boolean
	/**
	 * What the session asking lets its caller see, as a name: what the page
	 * keeps its answers and its unsent writes for, and says on every call
	 * (`callAs`). Another sign-in of the same person is the same name.
	 */
	identity: string
	workspace: Workspace
}

/** Where somebody deletes their account on roster's account page: its `#leave` section. */
export function leavePage(manage: string): string {
	return manage.split('#')[0] + '#leave'
}

/** Who this browser is signed in as, or `out` for none: what `GET /session` says. */
export async function signedIn(): Promise<Session | 'out'> {
	const res = await fetch('/session')
	if (res.status === 401) return 'out'
	if (!res.ok) throw new Error(`로그인을 읽을 수 없습니다 (${res.status})`)
	return (await res.json()) as Session
}

/** One way in the sign-in page offers: a connection of the tenant at roster. */
export interface Provider {
	connection: string
	label: string
	mark?: string
}

/** How somebody signs in here, as `GET /sso` answers it. */
export interface Ways {
	/** Whether there is an issuer at all, and which of its connections to draw. */
	sso: boolean
	providers: Provider[]
	/** Whether the password form is offered beside them. */
	password: boolean
}

/** The ways in this deployment offers. A server with no `/sso` offers a password. */
export async function waysIn(): Promise<Ways> {
	const res = await fetch('/sso')
	if (res.status === 404) return { sso: false, providers: [], password: true }
	if (!res.ok) throw new Error(`로그인 방법을 읽을 수 없습니다 (${res.status})`)
	return (await res.json()) as Ways
}

/**
 * Sends the browser to sign in through the issuer: with the button's
 * connection, or none for roster's page to ask, and back to where it is now.
 */
export function signInThrough(connection?: string): void {
	const q = new URLSearchParams({ next: location.pathname + location.search })
	if (connection !== undefined) q.set('with', connection)
	location.assign('/sso/login?' + q.toString())
}

/**
 * Signs out here and at the issuer. A form, because the browser has to go
 * there -- a session ended here alone is one the issuer signs straight back in
 * -- and a POST, which another site cannot send with this one's cookie.
 */
export function signOutThroughIssuer(): void {
	const f = document.createElement('form')
	f.method = 'post'
	f.action = '/sso/logout'
	document.body.append(f)
	f.submit()
}

/** A label's code as it is printed under its QR: in two fours, to be read out. */
export function labelCodeText(code: string | undefined): string {
	if (code === undefined || code.length !== 8) return ''
	return `${code.slice(0, 4)}-${code.slice(4)}`
}

export const raw = {
	party: createClient(PartyService, transport),
	asset: createClient(AssetService, transport),
	attachment: createClient(AttachmentService, transport),
	label: createClient(LabelService, transport),
	tenant: createClient(TenantService, transport),
	// What the reports' files are asked of (pages/reports.tsx).
	custody: createClient(CustodyService, transport),
	bookable: createClient(BookableService, transport),
	count: createClient(InventoryCountService, transport),
	stock: createClient(StockService, transport),
}

/** An identifier as a page writes it in a URL. */
export function idStr(b: Uint8Array | undefined): string {
	if (b === undefined || b.length !== 16) return ''
	try {
		return from(b).toString()
	} catch {
		return ''
	}
}

/** The bytes of an identifier a URL carried. */
export function idBytes(s: string): Uint8Array {
	return parse(s).bytes
}

export function sameId(a: Uint8Array | undefined, b: Uint8Array | undefined): boolean {
	if (a === undefined || b === undefined || a.length !== b.length || a.length === 0) return false
	for (let i = 0; i < a.length; i++) if (a[i] !== b[i]) return false
	return true
}

/** A reference by identifier, which every `*Ref` message has the shape of. */
export function ref(id: Uint8Array): { key: { case: 'id'; value: Uint8Array } } {
	return { key: { case: 'id', value: id } }
}

export function ts(d: Date): Timestamp {
	return timestampFromDate(d)
}

/**
 * Now, to the minute: what a read asks "from now" as. A moment to the
 * millisecond is a new question every time the page is drawn, and a read
 * asks again whenever its question changes -- so a page drawn because a read
 * answered would ask again because it was drawn, as fast as the server
 * answers, for as long as it is open.
 */
export function minute(): Date {
	const d = new Date()
	d.setSeconds(0, 0)
	return d
}

export function tsOf(v: MessageInitShape<typeof TimestampSchema> | Timestamp | undefined): Date | undefined {
	if (v === undefined) return undefined
	const t = create(TimestampSchema, v as MessageInitShape<typeof TimestampSchema>)
	if (t.seconds === 0n && t.nanos === 0) return undefined
	return timestampDate(t)
}

export function dateOf(t: Timestamp | undefined): Date | undefined {
	if (t === undefined) return undefined
	if (t.seconds === 0n && t.nanos === 0) return undefined
	return timestampDate(t)
}

/**
 * Where the workspace is (#144): the zone its dates are shown in, and the
 * currency an amount that names none is in. The page's until the workspace
 * says, and then the workspace's -- what its sentences on the server are told
 * in too.
 */
export const place = { timezone: undefined as string | undefined, currency: 'KRW' }

let dtf = new Intl.DateTimeFormat('ko-KR', { dateStyle: 'medium', timeStyle: 'short' })
let df = new Intl.DateTimeFormat('ko-KR', { dateStyle: 'medium' })
let tf = new Intl.DateTimeFormat('ko-KR', { timeStyle: 'short' })
let pf = partsIn(undefined)

function partsIn(timeZone: string | undefined): Intl.DateTimeFormat {
	return new Intl.DateTimeFormat('en-US', {
		timeZone,
		hourCycle: 'h23',
		weekday: 'short',
		year: 'numeric',
		month: 'numeric',
		day: 'numeric',
		hour: 'numeric',
		minute: 'numeric',
		second: 'numeric',
	})
}

/** Tells dates and amounts where the workspace is from now on. */
export function localize(v: { timezone?: string; currency?: string }): void {
	const timeZone = v.timezone || undefined
	try {
		dtf = new Intl.DateTimeFormat('ko-KR', { dateStyle: 'medium', timeStyle: 'short', timeZone })
		df = new Intl.DateTimeFormat('ko-KR', { dateStyle: 'medium', timeZone })
		tf = new Intl.DateTimeFormat('ko-KR', { timeStyle: 'short', timeZone })
		pf = partsIn(timeZone)
		place.timezone = timeZone
	} catch {
		// A zone this browser does not know: the page's own, as before.
	}
	if (v.currency) place.currency = v.currency
}

/** A moment as the clock on the workspace's wall says it. */
export type Wall = { y: number; m: number; d: number; h: number; mi: number; s: number; wd: number }

const weekdays = ['Sun', 'Mon', 'Tue', 'Wed', 'Thu', 'Fri', 'Sat']

/**
 * What the clock on the workspace's wall says at `t`: the year, the month
 * from 0 and the day, the hour, the minute and the second, and the weekday
 * from Sunday's 0 -- as `Date`'s own getters say them of the browser's.
 */
export function wall(t: Date): Wall {
	const v: Record<string, string> = {}
	for (const p of pf.formatToParts(t)) v[p.type] = p.value
	return {
		y: Number(v.year),
		m: Number(v.month) - 1,
		d: Number(v.day),
		h: Number(v.hour) % 24,
		mi: Number(v.minute),
		s: Number(v.second),
		wd: weekdays.indexOf(v.weekday ?? ''),
	}
}

/**
 * The moment the clock on the workspace's wall says this: `new Date(y, m, d,
 * h, mi)` in its zone rather than the browser's. A day or an hour past the
 * end of its month or day goes on into the next, as there. A time a change of
 * the clocks skipped is the one an hour on.
 */
export function at(y: number, m: number, d: number, h = 0, mi = 0, s = 0): Date {
	const said = Date.UTC(y, m, d, h, mi, s)
	// The zone's wall at a moment, read as though it were UTC.
	const read = (t: number): number => {
		const w = wall(new Date(t))
		return Date.UTC(w.y, w.m, w.d, w.h, w.mi, w.s)
	}
	const first = said - (read(said) - said)
	// Once more, where the clocks changed between the two.
	const second = said - (read(first) - first)
	if (read(second) === said) return new Date(second)
	if (read(first) === said) return new Date(first)
	return new Date(Math.max(first, second))
}

/** The moment the workspace's wall says the same time `days` later. */
export function shift(t: Date, days: number): Date {
	const w = wall(t)
	return at(w.y, w.m, w.d + days, w.h, w.mi, w.s)
}

/** A date the way `opts` says it, on the workspace's wall. */
export function fmtWith(opts: Intl.DateTimeFormatOptions, d: Date): string {
	return new Intl.DateTimeFormat('ko-KR', { ...opts, timeZone: place.timezone }).format(d)
}

/** A `<input type="datetime-local">` value for a moment, on the workspace's wall. */
export function localInput(d: Date | undefined): string {
	if (d === undefined) return ''
	const p = (n: number) => String(n).padStart(2, '0')
	const w = wall(d)
	return `${w.y}-${p(w.m + 1)}-${p(w.d)}T${p(w.h)}:${p(w.mi)}`
}

export function dateInput(d: Date | undefined): string {
	return localInput(d).slice(0, 10)
}

/**
 * The moment a `datetime-local` or `date` input says, on the workspace's
 * wall: what `new Date(v)` is in the browser's zone. Invalid is what that
 * would be too.
 */
export function fromInput(v: string): Date {
	const m = /^(\d{4})-(\d{2})-(\d{2})(?:T(\d{2}):(\d{2})(?::(\d{2}))?)?$/.exec(v.trim())
	if (m === null) return new Date(Number.NaN)
	return at(Number(m[1]), Number(m[2]) - 1, Number(m[3]), Number(m[4] ?? 0), Number(m[5] ?? 0), Number(m[6] ?? 0))
}

export function fmt(t: Timestamp | Date | undefined, only?: 'date' | 'time'): string {
	const d = t instanceof Date ? t : dateOf(t)
	if (d === undefined) return '-'
	if (only === 'date') return df.format(d)
	if (only === 'time') return tf.format(d)
	return dtf.format(d)
}

const rtf = new Intl.RelativeTimeFormat('ko-KR', { numeric: 'auto' })

/** A span of days the way a person says it: 365 is 1년, and 30 is 30일. */
export function daysWord(n: number): string {
	return n > 0 && n % 365 === 0 ? `${n / 365}년` : `${n}일`
}

/** How long ago or from now, the way a person says it. */
export function ago(t: Timestamp | Date | undefined): string {
	const d = t instanceof Date ? t : dateOf(t)
	if (d === undefined) return '-'
	const s = (d.getTime() - Date.now()) / 1000
	const a = Math.abs(s)
	if (a < 60) return rtf.format(Math.round(s), 'second')
	if (a < 3600) return rtf.format(Math.round(s / 60), 'minute')
	if (a < 86400) return rtf.format(Math.round(s / 3600), 'hour')
	if (a < 86400 * 30) return rtf.format(Math.round(s / 86400), 'day')
	if (a < 86400 * 365) return rtf.format(Math.round(s / 86400 / 30), 'month')
	return rtf.format(Math.round(s / 86400 / 365), 'year')
}

/** What went wrong, for a person. */
export function errorText(err: unknown): string {
	if (unreachable(err)) return '서버에 연결할 수 없습니다. 오프라인에서는 전에 본 화면만 볼 수 있습니다.'
	if (err instanceof ConnectError) {
		const m = err.rawMessage
		// The server says most things in Korean already; what it says in
		// English is from a layer below loft, and is given a Korean head.
		if (/[가-힣]/.test(m) && err.code !== Code.Unauthenticated) return m
		switch (err.code) {
			case Code.PermissionDenied:
				return `권한이 없습니다. (${m})`
			case Code.NotFound:
				return `찾을 수 없습니다. (${m})`
			case Code.Unauthenticated:
				return '로그인이 필요합니다.'
			case Code.AlreadyExists:
				return `이미 처리되었습니다. (${m})`
			case Code.Unavailable:
				return `서버에 연결할 수 없습니다. (${m})`
			default:
				return m
		}
	}
	if (err instanceof Error) return err.message
	return String(err)
}

/**
 * Whether a call was refused for its workspace being restricted (design 7.5):
 * `FailedPrecondition` with an `ErrorInfo` of reason `RESTRICTED`, which is
 * how the server tells it apart from every other refusal.
 */
export function isRestricted(err: unknown): boolean {
	return refusedAs(err, 'RESTRICTED')
}

/**
 * Whether a call was refused for its workspace being read-only (design 7.10):
 * `FailedPrecondition` with an `ErrorInfo` of reason `READONLY`.
 */
export function isReadOnly(err: unknown): boolean {
	return refusedAs(err, 'READONLY')
}

/** Whether a call was refused `FailedPrecondition`, with an `ErrorInfo` of `reason`. */
function refusedAs(err: unknown, reason: string): boolean {
	if (!(err instanceof ConnectError) || err.code !== Code.FailedPrecondition) return false
	return err.details.some((d) => 'type' in d && d.type === 'google.rpc.ErrorInfo' && reasonOf(d.value) === reason)
}

/** The reason an `ErrorInfo` says -- its first field -- read as protobuf wrote it. */
function reasonOf(b: Uint8Array): string | undefined {
	try {
		const r = new BinaryReader(b)
		while (r.pos < r.len) {
			const [no, wire] = r.tag()
			if (no === 1 && wire === WireType.LengthDelimited) return r.string()
			r.skip(wire)
		}
	} catch {
		// Not one.
	}
	return undefined
}

export function isUnauthenticated(err: unknown): boolean {
	return err instanceof ConnectError && err.code === Code.Unauthenticated
}

/**
 * Whether a call was refused for its workspace being closed (D29, D83):
 * `FailedPrecondition` with an `ErrorInfo` of reason `CLOSED`, whoever made
 * it. Loading the page again is what draws the notice a closed workspace
 * shows (`Closed`).
 */
export function isClosed(err: unknown): boolean {
	return refusedAs(err, 'CLOSED')
}

/** A fresh op id for a write a retry must not repeat. */
export function op(): Uint8Array {
	// The Event domain; see `server/pd`. A retry sends the same bytes.
	return newEventId()
}

function newEventId(): Uint8Array {
	return newId(EventDomain).bytes
}

export type Init<T extends DescMessage> = MessageInitShape<T>

/** Bytes as base64, for what goes into an `<img>` or a download: one array, or the parts of one. */
export function download(data: Uint8Array | Uint8Array[], name: string, type: string): void {
	const blob = new Blob(Array.isArray(data) ? (data as BlobPart[]) : [data as BlobPart], { type })
	const url = URL.createObjectURL(blob)
	const a = document.createElement('a')
	a.href = url
	a.download = name
	document.body.appendChild(a)
	a.click()
	a.remove()
	setTimeout(() => URL.revokeObjectURL(url), 10_000)
}

export async function readFile(f: File): Promise<Uint8Array> {
	return new Uint8Array(await f.arrayBuffer())
}

/**
 * The minor unit of every current ISO 4217 currency that has other than two
 * digits after the point (`internal/money`). An amount is a whole number of
 * its currency's minor unit (#144): won for KRW, cents for USD.
 */
const minorDigits: Record<string, number> = {
	BIF: 0, CLP: 0, DJF: 0, GNF: 0, ISK: 0, JPY: 0, KMF: 0, KRW: 0, PYG: 0,
	RWF: 0, UGX: 0, UYI: 0, VND: 0, VUV: 0, XAF: 0, XOF: 0, XPF: 0,
	BHD: 3, IQD: 3, JOD: 3, KWD: 3, LYD: 3, OMR: 3, TND: 3,
	CLF: 4, UYW: 4,
}

/** How many of an amount's digits in `currency` are after the point. */
export function digitsOf(currency?: string): number {
	return minorDigits[currency || place.currency] ?? 2
}

/** An amount as a number of the currency itself: 1250 cents is 12.5. */
function major(n: bigint | number, currency?: string): number {
	return Number(n) / 10 ** digitsOf(currency)
}

/** An amount in its currency, or the workspace's when it names none. */
export function money(n: bigint | number, currency?: string): string {
	const c = currency || place.currency
	const d = digitsOf(c)
	if (c === 'KRW') return `${major(n, c).toLocaleString('ko-KR')}원`
	try {
		return new Intl.NumberFormat('ko-KR', { style: 'currency', currency: c, minimumFractionDigits: d, maximumFractionDigits: d }).format(major(n, c))
	} catch {
		return `${major(n, c).toLocaleString('ko-KR', { minimumFractionDigits: d, maximumFractionDigits: d })} ${c}`
	}
}

/** An amount as an input shows it: 1250 cents is "12.50", 0 is "". */
export function amountText(n: bigint | number, currency?: string): string {
	return Number(n) === 0 ? '' : major(n, currency).toFixed(digitsOf(currency))
}

/**
 * What an input says, as an amount: "12.5" dollars is 1250n cents. Empty is
 * zero; a figure the currency has no unit for -- a tenth of a won -- is
 * undefined, and the input's `step` (`amountStep`) says so first.
 */
export function amountIn(text: string, currency?: string): bigint | undefined {
	const v = text.trim()
	if (v === '') return 0n
	const d = digitsOf(currency)
	const m = /^(\d*)(?:\.(\d*))?$/.exec(v)
	if (m === null || v === '.' || (m[2] ?? '').length > d) return undefined
	return BigInt((m[1] || '0') + (m[2] ?? '').padEnd(d, '0'))
}

/** What a column of amounts in `currency` is headed with: 원, or its code. */
export function unitOf(currency?: string): string {
	const c = currency || place.currency
	return c === 'KRW' ? '원' : c
}

/** The `step` of an input an amount in `currency` is typed into: "0.01" for USD. */
export function amountStep(currency?: string): string {
	const d = digitsOf(currency)
	return d === 0 ? '1' : `0.${'0'.repeat(d - 1)}1`
}
