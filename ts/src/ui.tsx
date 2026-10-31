/**
 * The handful of pieces every page is built from.
 *
 * @module
 */

import {
	cloneElement,
	createContext,
	useCallback,
	useContext,
	useEffect,
	useId,
	useLayoutEffect,
	useRef,
	useState,
	type ReactElement,
	type ReactNode,
} from 'react'

import { errorText } from './api.js'
import { tone, word } from './words.js'

export function Badge(props: { v: string; words?: Record<string, string>; children?: ReactNode }): ReactNode {
	return <span className={`badge ${tone(props.v)}`}>{props.children ?? (props.words ? word(props.words, props.v) : props.v)}</span>
}

export function Spinner(): ReactNode {
	return <div className="spinner" aria-label="불러오는 중" />
}

export function Empty(props: { children?: ReactNode }): ReactNode {
	return <div className="empty">{props.children ?? '아직 아무것도 없습니다.'}</div>
}

export function Failed(props: { error: unknown }): ReactNode {
	return <div className="failed">{errorText(props.error)}</div>
}

/** Loading, failure, or what arrived. */
export function Load<T>(props: {
	q: { state: string; data: T | undefined; error: unknown }
	children: (v: T) => ReactNode
}): ReactNode {
	if (props.q.data !== undefined) return props.children(props.q.data)
	if (props.q.state === 'error') return <Failed error={props.q.error} />
	return <Spinner />
}

export function Card(props: { title?: ReactNode; actions?: ReactNode; children?: ReactNode; className?: string }): ReactNode {
	return (
		<section className={`card ${props.className ?? ''}`}>
			{(props.title !== undefined || props.actions !== undefined) && (
				<header>
					<h3>{props.title}</h3>
					<div className="actions">{props.actions}</div>
				</header>
			)}
			{props.children}
		</section>
	)
}

export function PageHead(props: { title: ReactNode; sub?: ReactNode; actions?: ReactNode }): ReactNode {
	return (
		<div className="page-head">
			<div>
				<h1>{props.title}</h1>
				{props.sub !== undefined && <p className="sub">{props.sub}</p>}
			</div>
			<div className="actions">{props.actions}</div>
		</div>
	)
}

export function Stat(props: { label: string; value: ReactNode; tone?: string; onClick?: () => void }): ReactNode {
	return (
		<div className={`stat ${props.tone ?? ''} ${props.onClick ? 'link' : ''}`} onClick={props.onClick}>
			<div className="value">{props.value}</div>
			<div className="label">{props.label}</div>
		</div>
	)
}

export function Tabs(props: { tabs: { key: string; label: ReactNode }[]; at: string; onChange: (k: string) => void }): ReactNode {
	return (
		<div className="tabs" role="tablist">
			{props.tabs.map((t) => (
				<button key={t.key} role="tab" className={t.key === props.at ? 'on' : ''} onClick={() => props.onChange(t.key)}>
					{t.label}
				</button>
			))}
		</div>
	)
}

/**
 * What a control in a form is told by the field it is in: its id, which the
 * field's <label> names it by, and the hint that describes it. A component
 * that is a field's control takes these and puts them on its own element.
 */
export type Labelled = { id?: string; 'aria-describedby'?: string }

/**
 * A form's field: its name, the one control it names, and a hint.
 *
 * The <label> is beside the control and names it by its id, which the field
 * hands the control it is given. It used to wrap the control instead, and a
 * label that holds more than one control does two things nobody asked it to.
 * It names every control in it by the whole of what it says, so a picker's
 * 바꾸기 button was called by the field's name and the asset picked. And a
 * click on one of them that the page redraws before the click is over is the
 * label's own by the time the browser looks: it hands it on to its first
 * control, which in an asset picker was the 바꾸기 just drawn, and that
 * unpicked what had been picked (loft#53).
 *
 * A field of more than one control -- a picker, a row of boxes -- is a
 * `group`: named by what it says, and naming nothing in it. Its controls say
 * what they are themselves. `beside` is something that acts on the control,
 * a button, set beside it rather than named by the field.
 */
export function Field(
	props: { label: ReactNode; hint?: ReactNode; wide?: boolean } & (
		| { group: true; children: ReactNode; beside?: undefined }
		| { group?: false; children: ReactElement<Labelled>; beside?: ReactNode }
	),
): ReactNode {
	const own = useId()
	const hint = props.hint === undefined ? undefined : `${own}-hint`
	const cls = `field ${props.wide ? 'wide' : ''}`
	const said = props.hint !== undefined && (
		<span className="hint" id={hint}>
			{props.hint}
		</span>
	)

	if (props.group === true) {
		return (
			<div className={cls} role="group" aria-labelledby={`${own}-label`} aria-describedby={hint}>
				<span className="label" id={`${own}-label`}>
					{props.label}
				</span>
				{props.children}
				{said}
			</div>
		)
	}

	return (
		<FieldControl label={props.label} beside={props.beside} id={props.children.props.id ?? own} hint={hint} cls={cls} said={said}>
			{props.children}
		</FieldControl>
	)
}

function FieldControl(props: {
	label: ReactNode
	children: ReactElement<Labelled>
	beside?: ReactNode
	id: string
	hint?: string
	cls: string
	said: ReactNode
}): ReactNode {
	const control = cloneElement(props.children, {
		id: props.id,
		'aria-describedby': [props.children.props['aria-describedby'], props.hint].filter((v) => v !== undefined).join(' ') || undefined,
	})

	// Said where it is wrong, since nothing else would say it: a control that
	// does not take the id the field hands it is a field whose label names
	// nothing, and that looks exactly like one that works. The browser tests
	// fail on it (e2e/loft.ts).
	useLayoutEffect(() => {
		const el = document.getElementById(props.id)
		if (el === null || !('labels' in el)) {
			console.error(`Field: "${labelText(props.label)}" names no control; its control takes no id, or is a group`)
		}
	})

	return (
		<div className={props.cls}>
			<label className="label" htmlFor={props.id}>
				{props.label}
			</label>
			{props.beside === undefined ? (
				control
			) : (
				<div className="inline">
					{control}
					{props.beside}
				</div>
			)}
			{props.said}
		</div>
	)
}

function labelText(v: ReactNode): string {
	return typeof v === 'string' || typeof v === 'number' ? String(v) : '(a label that is not text)'
}

export function Select(
	props: {
		value: string
		onChange: (v: string) => void
		options: { value: string; label: ReactNode }[]
		empty?: string
		required?: boolean
		// What it is called when no field names it, as one in a row of
		// filters is.
		'aria-label'?: string
	} & Labelled,
): ReactNode {
	return (
		<select
			id={props.id}
			aria-label={props['aria-label']}
			aria-describedby={props['aria-describedby']}
			value={props.value}
			onChange={(e) => props.onChange(e.target.value)}
			required={props.required}
		>
			{props.empty !== undefined && <option value="">{props.empty}</option>}
			{props.options.map((o) => (
				<option key={o.value} value={o.value}>
					{o.label}
				</option>
			))}
		</select>
	)
}

/** Which way a list is read: newest first, or in the order things happened. */
export type Order = 'newest' | 'oldest'

/**
 * The choice of which way a list is read. Either way is the server's reading
 * (`reverse`), which walks the same index from either end, so the oldest are
 * the oldest there are and not the oldest of the newest page.
 */
export function OrderSelect(props: { value: Order; onChange: (v: Order) => void }): ReactNode {
	return (
		<Select
			aria-label="순서"
			value={props.value}
			onChange={(v) => props.onChange(v === 'oldest' ? 'oldest' : 'newest')}
			options={[
				{ value: 'newest', label: '최신순' },
				{ value: 'oldest', label: '오래된순' },
			]}
		/>
	)
}

export function Kv(props: { items: [ReactNode, ReactNode][] }): ReactNode {
	return (
		<dl className="kv">
			{props.items.map(([k, v], i) => (
				<div key={i}>
					<dt>{k}</dt>
					<dd>{v}</dd>
				</div>
			))}
		</dl>
	)
}

/** A dialog over the page; Escape and the backdrop close it. */
export function Modal(props: { title: ReactNode; onClose: () => void; children: ReactNode; wide?: boolean }): ReactNode {
	useEffect(() => {
		const f = (e: KeyboardEvent) => {
			if (e.key === 'Escape') props.onClose()
		}
		window.addEventListener('keydown', f)
		return () => window.removeEventListener('keydown', f)
	}, [props.onClose])

	return (
		<div className="modal-back" onMouseDown={(e) => e.target === e.currentTarget && props.onClose()}>
			<div className={`modal ${props.wide ? 'wide' : ''}`} role="dialog" aria-modal="true">
				<header>
					<h2>{props.title}</h2>
					<button className="icon" onClick={props.onClose} aria-label="닫기">
						✕
					</button>
				</header>
				<div className="body">{props.children}</div>
			</div>
		</div>
	)
}

/**
 * A form in a dialog: the fields, and a submit that stays pressed while the
 * write is in flight and closes the dialog when it worked.
 */
export function FormModal(props: {
	title: ReactNode
	submit?: ReactNode
	onClose: () => void
	onSubmit: () => Promise<boolean | void>
	children: ReactNode
	wide?: boolean
	danger?: boolean
}): ReactNode {
	const [busy, setBusy] = useState(false)
	return (
		<Modal title={props.title} onClose={props.onClose} wide={props.wide}>
			<form
				className="form"
				onSubmit={async (e) => {
					e.preventDefault()
					if (busy) return
					setBusy(true)
					try {
						const ok = await props.onSubmit()
						if (ok !== false) props.onClose()
					} finally {
						setBusy(false)
					}
				}}
			>
				<div className="fields">{props.children}</div>
				<footer>
					<button type="button" onClick={props.onClose}>
						취소
					</button>
					<button type="submit" className={props.danger ? 'danger' : 'primary'} disabled={busy}>
						{busy ? '처리 중…' : (props.submit ?? '저장')}
					</button>
				</footer>
			</form>
		</Modal>
	)
}

type Toast = { id: number; text: string; tone: 'ok' | 'bad' | 'info' }
const ToastCtx = createContext<(text: string, tone?: Toast['tone']) => void>(() => {})

export function Toasts(props: { children: ReactNode }): ReactNode {
	const [items, setItems] = useState<Toast[]>([])
	const n = useRef(0)
	const say = useCallback((text: string, tone: Toast['tone'] = 'ok') => {
		const id = ++n.current
		setItems((v) => [...v, { id, text, tone }])
		setTimeout(() => setItems((v) => v.filter((t) => t.id !== id)), tone === 'bad' ? 7000 : 3500)
	}, [])
	return (
		<ToastCtx.Provider value={say}>
			{props.children}
			<div className="toasts">
				{items.map((t) => (
					<div key={t.id} className={`toast ${t.tone}`} onClick={() => setItems((v) => v.filter((x) => x.id !== t.id))}>
						{t.text}
					</div>
				))}
			</div>
		</ToastCtx.Provider>
	)
}

export function useToast(): (text: string, tone?: Toast['tone']) => void {
	return useContext(ToastCtx)
}

/** A button that asks before it does something that cannot be taken back. */
export function Confirm(props: {
	label: ReactNode
	question: ReactNode
	onYes: () => Promise<unknown> | void
	danger?: boolean
	className?: string
	disabled?: boolean
}): ReactNode {
	const [open, setOpen] = useState(false)
	return (
		<>
			<button className={props.className ?? (props.danger ? 'danger' : '')} onClick={() => setOpen(true)} disabled={props.disabled}>
				{props.label}
			</button>
			{open && (
				<FormModal
					title="확인"
					submit={props.label}
					danger={props.danger}
					onClose={() => setOpen(false)}
					onSubmit={async () => {
						await props.onYes()
					}}
				>
					<p>{props.question}</p>
				</FormModal>
			)}
		</>
	)
}

/** A list cut into pages on the client. */
export function usePages<T>(items: T[], size = 50): { page: T[]; at: number; pages: number; go: (n: number) => void } {
	const [at, setAt] = useState(0)
	const pages = Math.max(1, Math.ceil(items.length / size))
	const n = Math.min(at, pages - 1)
	return { page: items.slice(n * size, n * size + size), at: n, pages, go: setAt }
}

export function Pager(props: { at: number; pages: number; go: (n: number) => void }): ReactNode {
	if (props.pages <= 1) return null
	return (
		<div className="pager">
			<button disabled={props.at === 0} onClick={() => props.go(props.at - 1)}>
				이전
			</button>
			<span>
				{props.at + 1} / {props.pages}
			</span>
			<button disabled={props.at >= props.pages - 1} onClick={() => props.go(props.at + 1)}>
				다음
			</button>
		</div>
	)
}

export function Search(props: { value: string; onChange: (v: string) => void; placeholder?: string; autoFocus?: boolean }): ReactNode {
	return (
		<input
			type="search"
			className="search"
			value={props.value}
			placeholder={props.placeholder ?? '검색'}
			onChange={(e) => props.onChange(e.target.value)}
			autoFocus={props.autoFocus}
		/>
	)
}

/** A value that settles a moment after it stops changing, for a search box. */
export function useDebounced<T>(v: T, ms = 250): T {
	const [d, setD] = useState(v)
	useEffect(() => {
		const t = setTimeout(() => setD(v), ms)
		return () => clearTimeout(t)
	}, [v, ms])
	return d
}
