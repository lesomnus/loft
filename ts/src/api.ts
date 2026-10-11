/**
 * The services, and the small conversions every page needs.
 *
 * A page reads through `useQuery` and writes through `useAct` (see `act.tsx`),
 * both of which go through payday's queries so a row drawn in six places is
 * one row. `raw` is for what wants the answer and none of that: signing in,
 * a download, an upload.
 *
 * @module
 */

import { create, type DescMessage, type MessageInitShape } from '@bufbuild/protobuf'
import { timestampDate, timestampFromDate, type Timestamp, TimestampSchema } from '@bufbuild/protobuf/wkt'
import { Code, ConnectError, createClient, type Interceptor } from '@connectrpc/connect'
import { createConnectTransport } from '@connectrpc/connect-web'
import { from, newId, parse } from '@lesomnus/payday/pdid'

import { EventDomain } from '../gen/domains.js'

import { AllocationService, BookableService, ReservationService } from '../gen/rove/booking_svc_pb.js'
import { AssetService, AttachmentService, LabelService, TenantDomainService } from '../gen/rove/asset_svc_pb.js'
import { AssetTypeService, ItemModelService } from '../gen/rove/catalog_svc_pb.js'
import { AuditService } from '../gen/rove/payday/audit_svc_pb.js'
import { CountFindingService, InventoryCountService } from '../gen/rove/count_svc_pb.js'
import { InvitationService, PartyService } from '../gen/rove/org_svc_pb.js'
import { CustodyLineService, CustodyService } from '../gen/rove/custody_svc_pb.js'
import { EventService } from '../gen/rove/history_svc_pb.js'
import { HolderService } from '../gen/rove/payday/holder_svc_pb.js'
import { NotificationService, UsageSnapshotService } from '../gen/rove/ops_svc_pb.js'
import { PurchaseLineService, PurchaseService, WorkOrderService } from '../gen/rove/work_svc_pb.js'
import { StockMovementService, StockService } from '../gen/rove/stock_svc_pb.js'
import { TenantService } from '../gen/rove/payday/tenant_svc_pb.js'

export {
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
	UsageSnapshotService,
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

/** The app answers on the page's own origin: served by it, or through Vite's proxy. */
export const transport = createConnectTransport({ baseUrl: location.origin, interceptors: [inWorkspace] })

/** A workspace an account is in, as `GET /account` answers it. */
export interface Workspace {
	id: string
	alias: string
	name: string
	role: string
	/** When it was closed, and when it goes unless restored (D29). */
	closed?: string
	destroy?: string
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
 * Opens a workspace: the page again, at its address. `instead` replaces this
 * page in the history rather than following it, for a page that was only on
 * its way somewhere.
 */
export function openWorkspace(alias: string, rest = '/', instead = false): void {
	rememberWorkspace(alias)
	const to = `/@${encodeURIComponent(alias)}${rest}`
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

const dtf = new Intl.DateTimeFormat('ko-KR', { dateStyle: 'medium', timeStyle: 'short' })
const df = new Intl.DateTimeFormat('ko-KR', { dateStyle: 'medium' })
const tf = new Intl.DateTimeFormat('ko-KR', { timeStyle: 'short' })

export function fmt(t: Timestamp | Date | undefined, only?: 'date' | 'time'): string {
	const d = t instanceof Date ? t : dateOf(t)
	if (d === undefined) return '-'
	if (only === 'date') return df.format(d)
	if (only === 'time') return tf.format(d)
	return dtf.format(d)
}

const rtf = new Intl.RelativeTimeFormat('ko-KR', { numeric: 'auto' })

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

/** A `<input type="datetime-local">` value for a moment, in local time. */
export function localInput(d: Date | undefined): string {
	if (d === undefined) return ''
	const p = (n: number) => String(n).padStart(2, '0')
	return `${d.getFullYear()}-${p(d.getMonth() + 1)}-${p(d.getDate())}T${p(d.getHours())}:${p(d.getMinutes())}`
}

export function dateInput(d: Date | undefined): string {
	return localInput(d).slice(0, 10)
}

/** What went wrong, for a person. */
export function errorText(err: unknown): string {
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

export function won(n: bigint | number): string {
	return `${Number(n).toLocaleString('ko-KR')}원`
}
