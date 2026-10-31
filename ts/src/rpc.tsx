/**
 * Reads and writes, the plain way.
 *
 * A read is a call made when a component mounts and made again when its
 * request changes or **anything was written** -- a write bumps one counter
 * every mounted read listens to, so whatever is on screen is read again and
 * nothing else is. A screen that should follow other people's changes as they
 * happen watches the row that stands for what it draws (`<Live>`), and one
 * that cannot asks to be polled.
 *
 * A read the network does not reach answers what it answered last, kept for
 * the account and workspace it was read as (`offline.ts`), and a write is
 * refused until the network is back.
 *
 * payday's store would do better than this -- one copy of a row however many
 * places draw it -- and is what this should become. It is not used yet
 * because most of what loft reads is not a row: a timeline, a tree at a
 * moment, a calendar, a report.
 *
 * @module
 */

import {
	fromBinary,
	toBinary,
	type DescMessage,
	type DescMethodServerStreaming,
	type DescMethodUnary,
	type DescService,
	type MessageInitShape,
	type MessageShape,
} from '@bufbuild/protobuf'
import { Code, ConnectError, createClient } from '@connectrpc/connect'
import { createContext, useCallback, useContext, useEffect, useMemo, useRef, useState, useSyncExternalStore, type ReactNode } from 'react'

import { errorText, isConsentOwed, isUnauthenticated, transport } from './api.js'
import { recall, remember, setOffline, unreachable, useOffline } from './offline.js'
import { useToast } from './ui.js'

// eslint-disable-next-line @typescript-eslint/no-explicit-any
const clients = new Map<DescService, any>()

// eslint-disable-next-line @typescript-eslint/no-explicit-any
function clientOf(s: DescService): any {
	let c = clients.get(s)
	if (c === undefined) {
		c = createClient(s, transport)
		clients.set(s, c)
	}
	return c
}

/** call makes one unary call. */
export async function call<I extends DescMessage, O extends DescMessage>(
	method: DescMethodUnary<I, O>,
	input: MessageInitShape<I>,
	signal?: AbortSignal,
	timeoutMs?: number,
): Promise<MessageShape<O>> {
	if (signal === undefined && timeoutMs === undefined) return clientOf(method.parent)[method.localName](input)
	return clientOf(method.parent)[method.localName](input, { signal, timeoutMs })
}

/**
 * read is a call that answers what it answered last when the network does not
 * reach the server, and says the page is offline. What it answers is kept for
 * the account and workspace this is, and only for them.
 */
export async function read<I extends DescMessage, O extends DescMessage>(
	method: DescMethodUnary<I, O>,
	input: MessageInitShape<I>,
	signal?: AbortSignal,
): Promise<MessageShape<O>> {
	const key = keyOfCall(method, input)
	// The same question with its moments left out: a read "from now" is asked
	// with another now every time, and offline the last one is what was seen.
	const loose = keyOfCall(method, input, true)
	try {
		const v = await call(method, input, signal)
		setOffline(false)
		void remember(loose === key ? [key] : [key, loose], toBinary(method.output, v))
		return v
	} catch (err) {
		if (signal?.aborted || !unreachable(err)) throw err
		setOffline(true)
		const kept = (await recall(key)) ?? (loose === key ? undefined : await recall(loose))
		if (kept === undefined) throw err
		return fromBinary(method.output, kept)
	}
}

type Epoch = { epoch: number; bump: () => void; onRefused: () => void }
const EpochCtx = createContext<Epoch>({ epoch: 0, bump: () => {}, onRefused: () => {} })

/**
 * Rpc is where the reads and writes below it are made. `onRefused` is what a
 * call refused for its session (`Unauthenticated`) does: ask again who is
 * signed in (`main.tsx`), since that may be a session that ended or one that
 * is somebody else's now.
 */
export function Rpc(props: { children: ReactNode; onRefused: () => void }): ReactNode {
	const [epoch, setEpoch] = useState(0)
	const bump = useCallback(() => setEpoch((v) => v + 1), [])
	const v = useMemo(() => ({ epoch, bump, onRefused: props.onRefused }), [epoch, bump, props.onRefused])

	// Back online, whatever is on screen is what was last seen; it is read again.
	const offline = useOffline()
	const was = useRef(offline)
	useEffect(() => {
		if (was.current && !offline) bump()
		was.current = offline
	}, [offline, bump])

	return <EpochCtx.Provider value={v}>{props.children}</EpochCtx.Provider>
}

export function useEpoch(): Epoch {
	return useContext(EpochCtx)
}

/**
 * A request as a string, so that the same question is the same key. `loose`
 * leaves its moments out, which is the same question asked at another time.
 */
function keyOf(v: unknown, loose = false): string {
	return JSON.stringify(v, (_, x) => {
		if (typeof x === 'bigint') return `${x}n`
		if (x instanceof Uint8Array) return `b:${hex(x)}`
		if (loose && typeof x === 'object' && x !== null && (x as { $typeName?: string }).$typeName === 'google.protobuf.Timestamp') return '*'
		return x
	})
}

function keyOfCall(method: DescMethodUnary, input: unknown, loose = false): string {
	return `${method.parent.typeName}/${method.name}:${keyOf(input, loose)}`
}

function hex(b: Uint8Array): string {
	return Array.from(b, (v) => v.toString(16).padStart(2, '0')).join('')
}

export interface Read<T> {
	state: 'pending' | 'ok' | 'error'
	data: T | undefined
	error: unknown
	reload: () => void
}

/**
 * useRpc reads one call, again whenever its request changes or anything was
 * written. `null` for a request is not asking yet.
 *
 * What it answered last stays drawn while it is asked again, so a page does
 * not blink to a spinner every time somebody saves something.
 */
export function useRpc<I extends DescMessage, O extends DescMessage>(
	method: DescMethodUnary<I, O>,
	input: MessageInitShape<I> | null,
	opts: { poll?: number } = {},
): Read<MessageShape<O>> {
	const { epoch, onRefused } = useEpoch()
	const live = useContext(LiveCtx)
	const key = input === null ? null : keyOfCall(method, input)
	const [nonce, setNonce] = useState(0)
	const [at, setAt] = useState<{ key: string | null; state: Read<unknown>['state']; data: MessageShape<O> | undefined; error: unknown }>({
		key: null,
		state: 'pending',
		data: undefined,
		error: undefined,
	})
	const inputRef = useRef(input)
	inputRef.current = input

	useEffect(() => {
		if (key === null || inputRef.current === null) return
		const ctl = new AbortController()
		read(method, inputRef.current, ctl.signal).then(
			(data) => {
				if (!ctl.signal.aborted) setAt({ key, state: 'ok', data, error: undefined })
			},
			(error) => {
				if (ctl.signal.aborted) return
				if (isUnauthenticated(error)) onRefused()
				if (isConsentOwed(error)) location.reload()
				setAt((v) => ({ key, state: 'error', data: v.key === key ? v.data : undefined, error }))
			},
		)
		return () => ctl.abort()
	}, [key, epoch, nonce, live, method, onRefused])

	useEffect(() => {
		if (opts.poll === undefined || key === null) return
		const t = setInterval(() => setNonce((v) => v + 1), opts.poll)
		return () => clearInterval(t)
	}, [opts.poll, key])

	const reload = useCallback(() => setNonce((v) => v + 1), [])

	// What was read for another request is not this one's answer; while the
	// new one is on its way the old one is still drawn, as pending.
	const same = at.key === key
	return {
		state: same ? at.state : 'pending',
		data: at.data,
		error: same ? at.error : undefined,
		reload,
	}
}

export type Act = <I extends DescMessage, O extends DescMessage>(
	method: DescMethodUnary<I, O>,
	input: MessageInitShape<I>,
	/**
	 * `timeoutMs` is how long the call may take, said to the server, which
	 * otherwise gives a call that names none thirty seconds.
	 */
	opts?: { ok?: string; quiet?: boolean; timeoutMs?: number },
) => Promise<MessageShape<O> | undefined>

/** What a write offline is told. */
export const offlineWrite = '오프라인이라 저장하지 않았습니다. 연결되면 다시 해 보세요.'

/**
 * useAct is a write: it says what happened, and has every read on screen
 * asked again. It answers undefined when the write failed, having said why.
 *
 * Offline it is refused, since what is on screen is only what was last seen:
 * at once when the browser knows it has no network, and when the call finds
 * none otherwise. Nothing is kept to be sent later; the one write that is, a
 * count's scan, waits as its caller's (`offline.ts`'s `wait`).
 */
export function useAct(): Act {
	const toast = useToast()
	const { bump, onRefused } = useEpoch()
	return useCallback(
		async (method, input, opts = {}) => {
			if (!navigator.onLine) {
				setOffline(true)
				if (!opts.quiet) toast(offlineWrite, 'bad')
				return undefined
			}
			try {
				const v = await call(method, input, undefined, opts.timeoutMs)
				setOffline(false)
				if (opts.ok !== undefined) toast(opts.ok)
				bump()
				return v
			} catch (err) {
				if (unreachable(err)) {
					setOffline(true)
					if (!opts.quiet) toast(offlineWrite, 'bad')
					return undefined
				}
				if (isUnauthenticated(err)) onRefused()
				if (isConsentOwed(err)) location.reload()
				if (!opts.quiet) toast(errorText(err), 'bad')
				return undefined
			}
		},
		[toast, bump, onRefused],
	) as Act
}

/** Offline is what a page says while what it draws is what was last seen. */
export function Offline(): ReactNode {
	if (!useOffline()) return null
	return (
		<div className="offline-banner" role="status">
			<strong>오프라인 — 마지막으로 본 내용</strong>
			<span>연결되면 다시 불러옵니다. 그동안 저장은 할 수 없고, 실사 스캔은 모아 두었다가 보냅니다.</span>
		</div>
	)
}

const LiveCtx = createContext(0)

/**
 * Live has whatever is read under it read again when one of the rows it names
 * is written -- by anybody, as it happens: a `Watch` of those rows (decided
 * 2026-10-12). A watch follows only rows it names, so a page names the row
 * that stands for what it draws, which is written whenever that changes
 * (design 9.6): an asset's own row, which a space's or a kit's
 * `content_version` is in, a bookable's `schedule_version`, a count's
 * `findings_version`. At most `max` of `ids` are followed, which is what one
 * watch of that entity carries (8 assets, 32 of most others).
 *
 * It is a nicety and never a dependency. A watch that cannot be had -- no
 * broker in this deployment, no Watch in the role -- is let go quietly, and
 * the page is what it was without one. One that drops is asked again, a
 * second later and then longer up to half a minute, and what changed while it
 * was down is read again once it is back. A tab out of sight holds no stream.
 */
export function Live(props: {
	// eslint-disable-next-line @typescript-eslint/no-explicit-any
	watch: DescMethodServerStreaming<any, any>
	ids: (Uint8Array | undefined)[]
	max?: number
	children: ReactNode
}): ReactNode {
	const outer = useContext(LiveCtx)
	const n = useWatch(props.watch, props.ids, props.max ?? 8)
	return <LiveCtx.Provider value={outer + n}>{props.children}</LiveCtx.Provider>
}

/** The codes a watch is not asked again after: no, rather than not now. */
const final = [Code.Unimplemented, Code.PermissionDenied, Code.InvalidArgument, Code.NotFound, Code.Unauthenticated, Code.FailedPrecondition]

// eslint-disable-next-line @typescript-eslint/no-explicit-any
function useWatch(method: DescMethodServerStreaming<any, any>, ids: (Uint8Array | undefined)[], max: number): number {
	const [n, setN] = useState(0)
	const offline = useOffline()
	const hidden = useHidden()
	const key = [...new Set(ids.filter((v): v is Uint8Array => v !== undefined && v.length > 0).map(hex))].sort().slice(0, max).join(',')
	const before = useRef('')

	useEffect(() => {
		if (key === '' || offline || hidden) return
		const ctl = new AbortController()
		const filters = key.split(',').map((h) => ({ ref: { key: { case: 'id', value: unhex(h) } } }))
		// Opened again for the same rows -- after a drop, a tab out of sight, a
		// time offline -- is a stream that missed whatever happened meanwhile.
		let again = before.current === key
		before.current = key
		let wait = 1000
		let timer: ReturnType<typeof setTimeout> | undefined
		let soon: ReturnType<typeof setTimeout> | undefined
		// A burst of writes is one read: the first is read a moment after it,
		// with whatever came after it.
		const changed = () => {
			if (soon !== undefined) return
			soon = setTimeout(() => {
				soon = undefined
				setN((v) => v + 1)
			}, 300)
		}
		const open = async () => {
			try {
				// Not skipping what is there now: that the first message has
				// come is how it is known the stream is open, and so that
				// reading again then misses nothing.
				let first = true
				for await (const _ of clientOf(method.parent)[method.localName]({ filters }, { signal: ctl.signal })) {
					if (first) {
						first = false
						wait = 1000
						if (again) changed()
						again = true
						continue
					}
					changed()
				}
			} catch (err) {
				if (ctl.signal.aborted) return
				if (err instanceof ConnectError && final.includes(err.code)) return
			}
			if (ctl.signal.aborted) return
			again = true
			timer = setTimeout(() => void open(), wait * (0.75 + Math.random() / 2))
			wait = Math.min(wait * 2, 30_000)
		}
		void open()
		return () => {
			ctl.abort()
			clearTimeout(timer)
			clearTimeout(soon)
		}
	}, [key, method, offline, hidden])

	return n
}

function unhex(h: string): Uint8Array {
	const b = new Uint8Array(h.length / 2)
	for (let i = 0; i < b.length; i++) b[i] = parseInt(h.slice(2 * i, 2 * i + 2), 16)
	return b
}

function useHidden(): boolean {
	return useSyncExternalStore(
		(f) => {
			document.addEventListener('visibilitychange', f)
			return () => document.removeEventListener('visibilitychange', f)
		},
		() => document.visibilityState === 'hidden',
	)
}

/** useLive is how many times what is under a `<Live>` changed, for a read that is not a `useRpc`. */
export function useLive(): number {
	return useContext(LiveCtx)
}
