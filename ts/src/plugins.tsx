/**
 * View plugins: modules built into the page that add to an asset's screen
 * where they apply (design 5). The page asks each one whether it matches an
 * asset, from the asset, its type and its model alone, and draws the ones
 * that do in their slot.
 *
 * A plugin adds to the asset page; none replaces it. Two plugins for one
 * asset are two panels, in the order they are listed here.
 *
 * @module
 */

import { useState, type ReactNode } from 'react'
import { Link } from 'react-router-dom'

import type { Asset } from '../gen/rove/asset_pb.js'
import type { AssetState } from '../gen/rove/asset_svc_pb.js'
import type { AssetType, ItemModel } from '../gen/rove/catalog_pb.js'
import { AssetService, idStr, ref, ts } from './api.js'
import { AssetPicker } from './pickers.js'
import { useAct, useRpc } from './rpc.js'
import { Card, Field, FormModal } from './ui.js'

export interface PluginProps {
	a: Asset
	type: AssetType | undefined
	model: ItemModel | undefined
	manage: boolean
}

export interface Plugin {
	id: string
	title: string
	/** `panel` is under the asset's details; `tab` is a tab of its own. */
	slot: 'panel' | 'tab'
	matches: (p: PluginProps) => boolean
	View: (p: PluginProps) => ReactNode
}

/** How many units a rack has: its model says, or its `units` attribute. */
function rackUnits(p: PluginProps): number {
	return p.model?.spec?.rackUnits || Number(p.a.attributes['units'] ?? 0) || 0
}

const rack: Plugin = {
	id: 'rack',
	title: '랙 배치도',
	slot: 'tab',
	matches: (p) => rackUnits(p) > 0 || (p.type?.spec?.capabilities ?? []).includes('rack'),
	View: Rack,
}

function Rack(p: PluginProps): ReactNode {
	const n = rackUnits(p) || 42
	// The U range is the placement's, so the rack is read as it is now.
	const [now] = useState(() => ts(new Date()))
	const q = useRpc(AssetService.method.queryAt, { root: ref(p.a.id), at: now, depth: 1 })
	const [install, setInstall] = useState<number>()
	const at = new Map<number, AssetState>()
	const loose: AssetState[] = []
	for (const k of q.data?.items ?? []) {
		if (k.depth !== 1 || !k.existed) continue
		if (k.mode === 'installed' && k.uFrom > 0) {
			for (let u = k.uFrom; u <= Math.max(k.uFrom, k.uTo); u++) at.set(u, k)
		} else {
			loose.push(k)
		}
	}
	const rows: ReactNode[] = []
	for (let u = n; u >= 1; u--) {
		const k = at.get(u)
		if (k !== undefined) {
			const top = Math.max(k.uFrom, k.uTo)
			if (u !== top) continue
			const h = top - k.uFrom + 1
			rows.push(
				<div key={u} className="u taken" style={{ height: `${h * 22}px` }}>
					<span className="num">{h === 1 ? `U${k.uFrom}` : `U${k.uFrom}–${top}`}</span>
					<Link to={`/assets/${idStr(k.id)}`}>
						<code>{k.tag}</code> {k.facts['name'] ?? ''}
					</Link>
				</div>,
			)
			continue
		}
		rows.push(
			<div key={u} className="u free">
				<span className="num">U{u}</span>
				{p.manage && (
					<button className="link small" onClick={() => setInstall(u)}>
						설치
					</button>
				)}
			</div>,
		)
	}
	const used = new Set([...at.values()].map((k) => idStr(k.id))).size
	return (
		<Card title={`랙 배치도 · ${n}U`} actions={<span className="mute small">{at.size}U 사용 · 장비 {used}대</span>}>
			<div className="rack">{rows}</div>
			{loose.length > 0 && <p className="mute small">U 위치 없이 들어 있는 것: {loose.map((k) => k.tag).join(', ')}</p>}
			{install !== undefined && <Install rack={p.a} u={install} onClose={() => setInstall(undefined)} />}
		</Card>
	)
}

function Install(props: { rack: Asset; u: number; onClose: () => void }): ReactNode {
	const act = useAct()
	const [a, setA] = useState<Asset>()
	const [from, setFrom] = useState(String(props.u))
	const [to, setTo] = useState(String(props.u))
	return (
		<FormModal
			title={`U${props.u}에 설치`}
			submit="설치"
			onClose={props.onClose}
			onSubmit={async () => {
				if (a === undefined) return false
				return (
					(await act(
						AssetService.method.move,
						{ ref: ref(a.id), to: ref(props.rack.id), mode: 'installed', uFrom: Number(from), uTo: Number(to), reason: '랙 설치' },
						{ ok: '설치했습니다.' },
					)) !== undefined
				)
			}}
		>
			<Field label="장비" wide group>
				<AssetPicker value={a} onChange={setA} kind="item" exclude={[props.rack.id]} />
			</Field>
			<Field label="아래 U">
				<input type="number" min={1} value={from} onChange={(e) => setFrom(e.target.value)} />
			</Field>
			<Field label="위 U">
				<input type="number" min={1} value={to} onChange={(e) => setTo(e.target.value)} />
			</Field>
		</FormModal>
	)
}

const warranty: Plugin = {
	id: 'warranty',
	title: '보증',
	slot: 'panel',
	matches: (p) => (p.a.attributes['warranty_until'] ?? '') !== '',
	View: Warranty,
}

function Warranty(p: PluginProps): ReactNode {
	const until = new Date(`${p.a.attributes['warranty_until']}T23:59:59`)
	if (Number.isNaN(until.getTime())) return null
	const days = Math.ceil((until.getTime() - Date.now()) / 86400_000)
	const tone = days < 0 ? 'bad' : days < 90 ? 'warn' : 'ok'
	return (
		<Card title="보증">
			<p className={tone}>
				{days < 0 ? `보증이 ${-days}일 전에 끝났습니다.` : `보증 만료까지 ${days}일 (${p.a.attributes['warranty_until']})`}
			</p>
		</Card>
	)
}

export const plugins: Plugin[] = [rack, warranty]
