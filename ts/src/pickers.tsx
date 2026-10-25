/**
 * Choosing things: an asset by searching, a space, a person, a type.
 *
 * @module
 */

import { useEffect, useMemo, useState, type ReactNode } from 'react'
import { Link } from 'react-router-dom'

import type { Asset } from '../gen/rove/asset_pb.js'
import { AssetService, idBytes, idStr } from './api.js'
import { modelName, useCatalog } from './catalog.js'
import { read, useEpoch, useLive, useRpc } from './rpc.js'
import { useDebounced, type Labelled } from './ui.js'
import { kindWord, partyKindWord } from './words.js'

export function AssetLink(props: { a: { id: Uint8Array; tag: string; name: string } | undefined }): ReactNode {
	if (props.a === undefined) return <span>-</span>
	return (
		<Link to={`/assets/${idStr(props.a.id)}`} className="asset-link">
			<code>{props.a.tag}</code> {props.a.name}
		</Link>
	)
}

/**
 * An asset, found by typing any of what Search matches. It is more than one
 * control -- what is typed, the assets found, and 바꾸기 once one is picked --
 * so a field it is in is a `group` (ui.tsx, `Field`).
 */
export function AssetPicker(props: {
	value: Asset | undefined
	onChange: (a: Asset | undefined) => void
	kind?: string
	placeholder?: string
	exclude?: Uint8Array[]
}): ReactNode {
	const [q, setQ] = useState('')
	const d = useDebounced(q)
	const found = useRpc(AssetService.method.search, d.trim() === '' ? null : { q: d.trim(), kind: props.kind ?? '', size: 12 })
	const ex = new Set((props.exclude ?? []).map(idStr))

	if (props.value !== undefined) {
		return (
			<div className="picked">
				<span>
					<code>{props.value.tag}</code> {props.value.name}
				</span>
				<button type="button" className="link" onClick={() => props.onChange(undefined)}>
					바꾸기
				</button>
			</div>
		)
	}
	return (
		<div className="picker">
			<input
				type="search"
				value={q}
				onChange={(e) => setQ(e.target.value)}
				placeholder={props.placeholder ?? '태그, 이름, 시리얼로 찾기'}
			/>
			{d.trim() !== '' && (
				<ul className="options">
					{(found.data?.items ?? [])
						.filter((a) => !ex.has(idStr(a.id)))
						.map((a) => (
							<li key={idStr(a.id)}>
								<button
									type="button"
									onClick={() => {
										props.onChange(a)
										setQ('')
									}}
								>
									<code>{a.tag}</code> {a.name} <span className="mute">{kindWord[a.kind]}</span>
								</button>
							</li>
						))}
					{found.data !== undefined && found.data.items.length === 0 && <li className="mute">없습니다</li>}
				</ul>
			)}
		</div>
	)
}

/** Spaces in tree order, each with the path to it. */
export function useSpaceOptions(): { value: string; label: string }[] {
	const c = useCatalog()
	return useMemo(
		() =>
			c.spaces
				.map((s) => ({ value: idStr(s.id), label: c.path(s.id) || s.name }))
				.sort((a, b) => a.label.localeCompare(b.label, 'ko')),
		[c],
	)
}

export function SpaceSelect(props: { value: string; onChange: (v: string) => void; empty?: string; required?: boolean } & Labelled): ReactNode {
	const opts = useSpaceOptions()
	return (
		<select
			id={props.id}
			aria-describedby={props['aria-describedby']}
			value={props.value}
			onChange={(e) => props.onChange(e.target.value)}
			required={props.required}
		>
			<option value="">{props.empty ?? '공간 선택'}</option>
			{opts.map((o) => (
				<option key={o.value} value={o.value}>
					{o.label}
				</option>
			))}
		</select>
	)
}

export function PartySelect(
	props: {
		value: string
		onChange: (v: string) => void
		kinds?: string[]
		empty?: string
		required?: boolean
	} & Labelled,
): ReactNode {
	const c = useCatalog()
	const kinds = props.kinds ?? ['person']
	const vs = c.parties
		.filter((p) => kinds.includes(p.kind))
		.sort((a, b) => a.name.localeCompare(b.name, 'ko'))
	return (
		<select
			id={props.id}
			aria-describedby={props['aria-describedby']}
			value={props.value}
			onChange={(e) => props.onChange(e.target.value)}
			required={props.required}
		>
			<option value="">{props.empty ?? '선택'}</option>
			{vs.map((p) => {
				const team = c.party(p.parentId)
				return (
					<option key={idStr(p.id)} value={idStr(p.id)}>
						{p.name}
						{team !== undefined && p.kind === 'person' ? ` · ${team.name}` : ''}
						{kinds.length > 1 ? ` (${partyKindWord[p.kind]})` : ''}
					</option>
				)
			})}
		</select>
	)
}

export function TypeSelect(props: { value: string; onChange: (v: string) => void; kind?: string; empty?: string } & Labelled): ReactNode {
	const c = useCatalog()
	const vs = c.types.filter((t) => props.kind === undefined || t.kind === props.kind).sort((a, b) => a.name.localeCompare(b.name, 'ko'))
	return (
		<select id={props.id} aria-describedby={props['aria-describedby']} value={props.value} onChange={(e) => props.onChange(e.target.value)}>
			<option value="">{props.empty ?? '유형 없음'}</option>
			{vs.map((t) => (
				<option key={idStr(t.id)} value={idStr(t.id)}>
					{t.name}
				</option>
			))}
		</select>
	)
}

export function ModelSelect(props: { value: string; onChange: (v: string) => void; type?: string; empty?: string } & Labelled): ReactNode {
	const c = useCatalog()
	const vs = c.models
		.filter((m) => props.type === undefined || props.type === '' || idStr(m.type?.id) === props.type)
		.sort((a, b) => modelName(a).localeCompare(modelName(b), 'ko'))
	return (
		<select id={props.id} aria-describedby={props['aria-describedby']} value={props.value} onChange={(e) => props.onChange(e.target.value)}>
			<option value="">{props.empty ?? '모델 없음'}</option>
			{vs.map((m) => (
				<option key={idStr(m.id)} value={idStr(m.id)}>
					{modelName(m)}
				</option>
			))}
		</select>
	)
}

/** Where an asset is, as a path a person can read. */
export function Where(props: { a: Asset }): ReactNode {
	const c = useCatalog()
	if (props.a.parentId.length === 0) return <span className="mute">-</span>
	const p = c.space(props.a.parentId)
	if (p !== undefined) {
		return <Link to={`/assets/${idStr(p.id)}`}>{c.path(p.id)}</Link>
	}
	return <ParentLink id={props.a.parentId} />
}

function ParentLink(props: { id: Uint8Array }): ReactNode {
	const v = useRpc(AssetService.method.get, { ref: { key: { case: 'id', value: props.id } } })
	if (v.data === undefined) return <span className="mute">…</span>
	return <AssetLink a={v.data} />
}

/**
 * Assets by identifier, read in groups of eight -- the most filters one list
 * takes, and they are or-ed. While another set is read, what was read of the
 * last is drawn.
 */
export function useAssets(ids: (Uint8Array | undefined)[]): Map<string, Asset> {
	return useAssetsOf(ids).last
}

/**
 * The assets {@link useAssets} reads, and undefined until those of these
 * identifiers are: for what is made once with all of them, as a form is
 * filled in with them.
 */
export function useAssetsRead(ids: (Uint8Array | undefined)[]): Map<string, Asset> | undefined {
	return useAssetsOf(ids).read
}

function useAssetsOf(ids: (Uint8Array | undefined)[]): { last: Map<string, Asset>; read: Map<string, Asset> | undefined } {
	const { epoch } = useEpoch()
	const live = useLive()
	const key = [...new Set(ids.map(idStr).filter((v) => v !== ''))].sort().join(',')
	const [m, setM] = useState({ key: undefined as string | undefined, m: new Map<string, Asset>() })
	useEffect(() => {
		let live = true
		const vs = key === '' ? [] : key.split(',')
		const chunks: string[][] = []
		for (let i = 0; i < vs.length; i += 8) chunks.push(vs.slice(i, i + 8))
		Promise.all(
			chunks.map((c) => read(AssetService.method.list, { filters: c.map((s) => ({ ref: { key: { case: 'id' as const, value: idBytes(s) } } })), size: 8 })),
		).then(
			(rs) => {
				if (!live) return
				const n = new Map<string, Asset>()
				for (const r of rs) for (const a of r.items) n.set(idStr(a.id), a)
				setM({ key, m: n })
			},
			() => {},
		)
		return () => {
			live = false
		}
	}, [key, epoch, live])
	return { last: m.m, read: m.key === key ? m.m : undefined }
}
