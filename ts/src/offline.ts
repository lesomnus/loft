/**
 * What the page keeps for when there is no network (decided 2026-10-12): the
 * app itself, which a service worker keeps (`ts/sw.js`), and the last answer
 * of every read a page made, which is kept here.
 *
 * An answer is somebody's, in some workspace, so it is kept **for one account
 * in one workspace** -- under the account's identifier and the workspace's, as
 * `GET /account` names them -- and never served to another: what this browser
 * kept for others is dropped the moment it is known whose turn it is, and
 * everything is dropped when the account signs out or is found signed out.
 * A worker could not keep it that way, since it sees neither.
 *
 * Offline the app is for reading. A read that cannot reach the server answers
 * what it answered last and the page says that is what it is (`<Offline>`); a
 * write is refused with the reason (`useAct`), except a count's scans, which
 * wait in a queue of their own (`pages/counts.tsx`).
 *
 * @module
 */

import { useSyncExternalStore } from 'react'

const DB = 'rove'
const READS = 'reads'
const META = 'meta'

/** How long an answer is worth keeping for a day with no network. */
const keepFor = 30 * 86400_000

let scope = ''

/**
 * keepAs says whose the reads are: an account in a workspace, by their
 * identifiers. Empty is nobody's, and nothing is kept or served.
 */
export function keepAs(account: string, workspace: string): void {
	scope = account === '' || workspace === '' ? '' : `${account}/${workspace}/`
}

let opening: Promise<IDBDatabase | undefined> | undefined

function db(): Promise<IDBDatabase | undefined> {
	opening ??= new Promise((resolve) => {
		try {
			const req = indexedDB.open(DB, 1)
			req.onupgradeneeded = () => {
				const d = req.result
				d.createObjectStore(READS).createIndex('at', 'at')
				d.createObjectStore(META)
			}
			req.onsuccess = () => resolve(req.result)
			// A browser that keeps nothing -- a private window, storage turned
			// off -- is a page that works online and opens nothing offline.
			req.onerror = () => resolve(undefined)
			req.onblocked = () => resolve(undefined)
		} catch {
			resolve(undefined)
		}
	})
	return opening
}

function done(tx: IDBTransaction): Promise<void> {
	return new Promise((resolve, reject) => {
		tx.oncomplete = () => resolve()
		tx.onabort = () => reject(tx.error)
		tx.onerror = () => reject(tx.error)
	})
}

function answer<T>(req: IDBRequest<T>): Promise<T> {
	return new Promise((resolve, reject) => {
		req.onsuccess = () => resolve(req.result)
		req.onerror = () => reject(req.error)
	})
}

type Kept = { at: number; v: Uint8Array }

/**
 * remember keeps what a read answered, under each of `keys`, for the account
 * and workspace this is.
 */
export async function remember(keys: string[], v: Uint8Array): Promise<void> {
	const s = scope
	if (s === '') return
	const d = await db()
	if (d === undefined) return
	try {
		const tx = d.transaction(READS, 'readwrite')
		const at = Date.now()
		for (const k of keys) tx.objectStore(READS).put({ at, v } satisfies Kept, s + k)
		await done(tx)
	} catch {
		// Full, or gone: this answer is not there offline, and nothing else changes.
	}
}

/** recall is what a read answered last, for the account and workspace this is. */
export async function recall(key: string): Promise<Uint8Array | undefined> {
	const s = scope
	if (s === '') return undefined
	const d = await db()
	if (d === undefined) return undefined
	try {
		const v = (await answer(d.transaction(READS).objectStore(READS).get(s + key))) as Kept | undefined
		return v?.v
	} catch {
		return undefined
	}
}

/**
 * keepOnly drops what was kept for anybody but this account, in any workspace
 * but these, and anything older than is worth keeping.
 */
export async function keepOnly(account: string, workspaces: string[]): Promise<void> {
	const d = await db()
	if (d === undefined) return
	const mine = workspaces.map((w) => `${account}/${w}/`)
	const old = Date.now() - keepFor
	try {
		const tx = d.transaction(READS, 'readwrite')
		const req = tx.objectStore(READS).openCursor()
		req.onsuccess = () => {
			const c = req.result
			if (c === null) return
			const k = String(c.key)
			if (!mine.some((p) => k.startsWith(p)) || (c.value as Kept).at < old) c.delete()
			c.continue()
		}
		await done(tx)
	} catch {
		// Dropped next time.
	}
}

/** forget drops everything this browser kept, for every account. */
export async function forget(): Promise<void> {
	scope = ''
	const d = await db()
	if (d === undefined) return
	try {
		const tx = d.transaction([READS, META], 'readwrite')
		tx.objectStore(READS).clear()
		tx.objectStore(META).clear()
		await done(tx)
	} catch {
		// Nothing more can be done from here.
	}
}

/**
 * lastSeen is who this browser was signed in as and how it signs in, as the
 * server last answered: what the page opens with when it cannot ask.
 */
export async function lastSeen<A, W>(): Promise<{ account: A; ways: W } | undefined> {
	const d = await db()
	if (d === undefined) return undefined
	try {
		return (await answer(d.transaction(META).objectStore(META).get('signed-in'))) as { account: A; ways: W } | undefined
	} catch {
		return undefined
	}
}

/** seen keeps who this browser is signed in as, for {@link lastSeen}. */
export async function seen<A, W>(account: A, ways: W): Promise<void> {
	const d = await db()
	if (d === undefined) return
	try {
		const tx = d.transaction(META, 'readwrite')
		tx.objectStore(META).put({ account, ways }, 'signed-in')
		await done(tx)
	} catch {
		// Not openable offline, then.
	}
}

let offline = false
const listeners = new Set<() => void>()

/** setOffline says whether the server can be reached, as the last call found. */
export function setOffline(v: boolean): void {
	if (offline === v) return
	offline = v
	for (const f of listeners) f()
}

export function isOffline(): boolean {
	return offline
}

/** useOffline is whether the server could not be reached, the last time anything asked. */
export function useOffline(): boolean {
	return useSyncExternalStore(
		(f) => {
			listeners.add(f)
			return () => listeners.delete(f)
		},
		() => offline,
	)
}

if (typeof window !== 'undefined') {
	window.addEventListener('offline', () => setOffline(true))
	// Only a sign the next call may get through, which is what says so.
	window.addEventListener('online', () => setOffline(false))
	if (!navigator.onLine) offline = true
}

/**
 * unreachable is whether a call failed for want of a network rather than
 * because the server said no: a fetch that never got an answer, which is a
 * TypeError however deep in the error it is.
 */
export function unreachable(err: unknown): boolean {
	if (typeof navigator !== 'undefined' && !navigator.onLine) return true
	let e: unknown = err
	for (let i = 0; i < 4 && e !== undefined && e !== null; i++) {
		if (e instanceof TypeError) return true
		e = (e as { cause?: unknown }).cause
	}
	return false
}

/** Has the service worker keep the app, so that it opens with no network. */
export function keepTheApp(): void {
	if (!import.meta.env.PROD || !('serviceWorker' in navigator)) return
	navigator.serviceWorker.register('/sw.js').catch(() => {
		// The app works as it did without one: online only.
	})
}
