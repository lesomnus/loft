/**
 * The services, and the small conversions every page needs.
 *
 * A page reads through `useRpc` and writes through `useAct` (`rpc.tsx`), over
 * the one transport here: every call says the workspace it is about, and whose
 * the page is (`callAs`). What is not an RPC -- signing in, the account, a
 * download, an upload -- is a `fetch` of its own.
 *
 * @module
 */

import { create, type DescMessage, type MessageInitShape } from '@bufbuild/protobuf'
import { timestampDate, timestampFromDate, type Timestamp, TimestampSchema } from '@bufbuild/protobuf/wkt'
import { Code, ConnectError, createClient, type Interceptor } from '@connectrpc/connect'
import { createConnectTransport } from '@connectrpc/connect-web'
import { from, newId, parse } from '@lesomnus/payday/pdid'
import { IDENTITY_HEADER } from '@lesomnus/payday/query'

import { EventDomain } from '../gen/domains.js'
import { events } from '../gen/events.js'
import { unreachable } from './offline.js'

import { AllocationService, BookableService, ReservationService } from '../gen/rove/booking_svc_pb.js'
import { AssetService, AttachmentService, LabelService, TenantDomainService } from '../gen/rove/asset_svc_pb.js'
import { AssetTypeService, ItemModelService } from '../gen/rove/catalog_svc_pb.js'
import { AccessService } from '../gen/rove/payday/access_svc_pb.js'
import { AuditService } from '../gen/rove/payday/audit_svc_pb.js'
import { CountFindingService, InventoryCountService } from '../gen/rove/count_svc_pb.js'
import { InvitationService, PartyService } from '../gen/rove/org_svc_pb.js'
import { CustodyLineService, CustodyService } from '../gen/rove/custody_svc_pb.js'
import { EventService } from '../gen/rove/payday/event_svc_pb.js'
import { HolderService } from '../gen/rove/payday/holder_svc_pb.js'
import { AccessReviewService, NotificationService } from '../gen/rove/ops_svc_pb.js'
import { PurchaseLineService, PurchaseService, WorkOrderService } from '../gen/rove/work_svc_pb.js'
import { StockMovementService, StockService } from '../gen/rove/stock_svc_pb.js'
import { TenantService } from '../gen/rove/payday/tenant_svc_pb.js'

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
	InvitationService,
	ItemModelService,
	LabelService,
	NotificationService,
	PartyService,
	PurchaseLineService,
	PurchaseService,
	ReservationService,
	StockMovementService,
	StockService,
	TenantDomainService,
	TenantService,
	WorkOrderService,
}

/**
 * The workspace this page is about: the `@alias` its address begins with, and
 * empty when it begins with none (design 9.11). A page is about one workspace
 * for as long as it is open; going to another is loading the page again, so
 * nothing read in one is drawn in another.
 */
export const workspace: string = (() => {
	const m = /^\/@([^/]+)/.exec(location.pathname)
	return m === null ? '' : decodeURIComponent(m[1] ?? '')
})()

/** Every call names the workspace it is about, which is what it is served as a member of. */
const inWorkspace: Interceptor = (next) => async (req) => {
	if (workspace !== '') req.header.set('Rove-Workspace', workspace)
	return next(req)
}

/**
 * Whose the page's calls are: the name of what it keeps for the account it was
 * drawn for, as `GET /account` said it (`Account.identity`), and empty while it
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
 * a value in one fails whole. Empty while Rove declares no past of payday's.
 */
export const transport = createConnectTransport({
	baseUrl: location.origin,
	interceptors: [inWorkspace, asCaller],
	jsonOptions: { registry: events },
})

/** A workspace an account is in, as `GET /account` answers it. */
export interface Workspace {
	id: string
	alias: string
	name: string
	role: string
	/** When it was closed, and when it goes unless restored (D29). */
	closed?: string
	destroy?: string
	/** For a closed one: how many days its address is then held (D29). */
	alias_hold_days?: number
	/**
	 * Where its app is when that is a domain of its own (rove#155): the
	 * address a page of it begins with there, which opening it goes to.
	 */
	home?: string
}

/** The account signed in, and the workspaces it is in. */
export interface Account {
	id: string
	login: string
	name: string
	workspaces: Workspace[]
	owns: number
	may?: number
	/** roster's page where the account itself is managed, and deleted where its directory offers that. */
	manage?: string
	/** What it has to agree to before it uses anything here (design 9.11). */
	owes?: ConsentDocument[]
	/**
	 * The workspace this address serves alone, when it is a workspace's own
	 * domain (rove#155): nothing of the account's beside it is offered here.
	 */
	pinned?: string
	/** The service's address, said where `pinned` is: where the account's other workspaces are. */
	service?: string
	/**
	 * What the session asking lets its caller see, as a name: what the page
	 * keeps its answers and its unsent writes for, and says on every call
	 * (`callAs`). Another sign-in of the same person is the same name.
	 */
	identity: string
}

/** One document an account agrees to, at the version in force. */
export interface ConsentDocument {
	name: string
	title: string
	version: string
	url: string
}

/** One agreement an account made. */
export interface Agreement {
	document: string
	version: string
	at: string
	client?: string
}

/** Agrees to every document named, at the version read. */
export async function agree(docs: ConsentDocument[]): Promise<void> {
	const res = await fetch('/account/consents', {
		method: 'POST',
		headers: { 'content-type': 'application/json' },
		body: JSON.stringify({ agree: Object.fromEntries(docs.map((d) => [d.name, d.version])) }),
	})
	if (!res.ok) throw new Error((await res.text()).trim() || `동의를 기록하지 못했습니다 (${res.status})`)
}

/** What the account agreed to, newest first. */
export async function agreements(): Promise<{ owed: ConsentDocument[]; agreed: Agreement[] }> {
	const res = await fetch('/account/consents')
	if (!res.ok) throw new Error(`동의 기록을 읽을 수 없습니다 (${res.status})`)
	return (await res.json()) as { owed: ConsentDocument[]; agreed: Agreement[] }
}

/** Where somebody deletes their account on roster's account page: its `#leave` section. */
export function leavePage(manage: string): string {
	return manage.split('#')[0] + '#leave'
}

/** The account this browser is signed in as, or `out` for none. */
export async function signedIn(): Promise<Account | 'out'> {
	const res = await fetch('/account')
	if (res.status === 401) return 'out'
	if (!res.ok) throw new Error(`계정을 읽을 수 없습니다 (${res.status})`)
	return (await res.json()) as Account
}

/** One way in the sign-in page offers: a connection of the directory at roster. */
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
	/**
	 * Whether this is a workspace's own domain, where nobody signs in: the
	 * page sends the browser to the service's address to be signed in, and it
	 * comes back with a session of this host's (rove#155).
	 */
	handoff?: boolean
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

/** How many times a page sends the browser off to be signed in, in a while, before it stops and asks. */
const handoffs = { max: 3, within: 60_000, key: 'rove-handoffs' }

/**
 * Sends the browser from a workspace's own domain to the service's address to
 * be signed in, and back to where it is now. Unless somebody `asked` -- a
 * button -- it answers false, sending nothing, when it has gone round more
 * than a few times in a minute: something on the way does not take, and a
 * button is better than a loop.
 */
export function handOff(asked = false): boolean {
	const now = Date.now()
	let recent: number[] = []
	try {
		recent = (JSON.parse(sessionStorage.getItem(handoffs.key) ?? '[]') as number[]).filter((t) => now - t < handoffs.within)
	} catch {
		// Nothing kept, which is the same as nothing recent.
	}
	if (!asked && recent.length >= handoffs.max) return false
	try {
		sessionStorage.setItem(handoffs.key, JSON.stringify([...recent, now]))
	} catch {
		// A browser that keeps nothing goes round as often as it is sent.
	}
	location.replace('/handoff/start?' + new URLSearchParams({ next: location.pathname + location.search }).toString())
	return true
}

/**
 * Signs out on a workspace's own domain: this host's session, and the sign-in
 * at the service's address it was handed from. A form, as signing out at the
 * issuer is, because the browser goes on to where that one ends.
 */
export function signOutThroughHandoff(): void {
	const f = document.createElement('form')
	f.method = 'post'
	f.action = '/handoff/logout'
	document.body.append(f)
	f.submit()
}

/**
 * Where a sign-in at the service's address goes on to: the handoff that sent
 * the browser here to be signed in (`?next=/handoff?...`), and nowhere else.
 */
export function handingOff(): string {
	const next = new URLSearchParams(location.search).get('next') ?? ''
	return next.startsWith('/handoff?') ? next : ''
}

/** A new workspace the account owns, with an alias nobody chose. */
export async function makeWorkspace(name: string): Promise<Workspace> {
	const res = await fetch('/account/workspaces', {
		method: 'POST',
		headers: { 'content-type': 'application/json' },
		body: JSON.stringify({ name }),
	})
	if (res.status === 402) throw new Error('워크스페이스를 더 만들려면 결제가 필요합니다.')
	if (!res.ok) throw new Error((await res.text()).trim() || `만들 수 없습니다 (${res.status})`)
	return (await res.json()) as Workspace
}

/**
 * Which of the account's workspaces a scanned code is a label in, for a scan
 * made in another; undefined for none of them. A label in a workspace the
 * account is not in reads as none.
 */
export async function labelWorkspace(code: string): Promise<Workspace | undefined> {
	const res = await fetch('/account/labels/' + encodeURIComponent(code))
	if (res.status === 404) return undefined
	if (!res.ok) throw new Error(`라벨을 찾을 수 없습니다 (${res.status})`)
	return (await res.json()) as Workspace
}

/** A label's code as it is printed under its QR: in two fours, to be read out. */
export function labelCodeText(code: string | undefined): string {
	if (code === undefined || code.length !== 8) return ''
	return `${code.slice(0, 4)}-${code.slice(4)}`
}

const lastKey = 'rove.workspace'

/** The workspace this browser was last in, which a page with none in its address opens. */
export function lastWorkspace(): string {
	try {
		return localStorage.getItem(lastKey) ?? ''
	} catch {
		return ''
	}
}

/** Remembers the workspace this browser is in, for a page with none in its address. */
export function rememberWorkspace(alias: string): void {
	try {
		localStorage.setItem(lastKey, alias)
	} catch {
		// A browser that keeps nothing opens the first workspace next time.
	}
}

/**
 * Opens a workspace: the page again, at its address -- on the domain of its
 * own its app is on, when it has one (rove#155), where the service would send
 * the page anyway. `instead` replaces this page in the history rather than
 * following it, for a page that was only on its way somewhere.
 */
export function openWorkspace(w: string | Workspace, rest = '/', instead = false): void {
	const alias = typeof w === 'string' ? w : w.alias
	const home = typeof w === 'string' ? '' : (w.home ?? '')
	rememberWorkspace(alias)
	const to = `${home}/@${encodeURIComponent(alias)}${rest}`
	if (instead) location.replace(to)
	else location.assign(to)
}

export const raw = {
	party: createClient(PartyService, transport),
	asset: createClient(AssetService, transport),
	attachment: createClient(AttachmentService, transport),
	label: createClient(LabelService, transport),
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
		// English is from a layer below rove, and is given a Korean head.
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

export function isUnauthenticated(err: unknown): boolean {
	return err instanceof ConnectError && err.code === Code.Unauthenticated
}

/**
 * Whether a call was refused because the account has not agreed to a document
 * in force -- one that changed while the page was open (`cmd.ConsentOwed`).
 * Loading the page again is what asks for the agreement.
 */
export function isConsentOwed(err: unknown): boolean {
	return err instanceof ConnectError && err.code === Code.FailedPrecondition && err.rawMessage.includes('rove consent')
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

/** Bytes as base64, for what goes into an `<img>` or a download. */
export function download(data: Uint8Array, name: string, type: string): void {
	const blob = new Blob([data as BlobPart], { type })
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
