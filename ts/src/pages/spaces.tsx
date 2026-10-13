import { useMemo, useState, type ReactNode } from 'react'
import { Link } from 'react-router-dom'

import type { Asset } from '../../gen/rove/asset_pb.js'
import type { AssetState } from '../../gen/rove/asset_svc_pb.js'
import { AssetService, idBytes, idStr, localInput, ref, ts } from '../api.js'
import { HistoryDays, useCatalog } from '../catalog.js'
import { AssetLink } from '../pickers.js'
import { useRpc } from '../rpc.js'
import { Badge, Card, Empty, Load, PageHead, Select, Tabs } from '../ui.js'
import { conditionWord, kindWord, modeWord, statusWord } from '../words.js'

export function Spaces(): ReactNode {
	const c = useCatalog()
	const roots = c.spaces.filter((s) => s.parentId.length === 0 || c.space(s.parentId) === undefined)
	const [tab, setTab] = useState('now')
	const [picked, setRoot] = useState('')
	const root = picked !== '' ? picked : roots[0] === undefined ? '' : idStr(roots[0].id)

	return (
		<>
			<PageHead title="공간" sub="공간의 지금 모습, 과거 어느 시점의 모습, 그리고 두 시점 사이에 바뀐 것을 봅니다." />
			<Tabs
				tabs={[
					{ key: 'now', label: '지금' },
					{ key: 'then', label: '시점 이동' },
					{ key: 'diff', label: '변화 비교' },
				]}
				at={tab}
				onChange={setTab}
			/>
			{tab === 'now' && <Now />}
			{tab !== 'now' && (
				<div className="filters">
					<label className="inline">
						기준 공간
						<Select
							value={root}
							onChange={setRoot}
							options={c.spaces
								.map((s) => ({ value: idStr(s.id), label: c.path(s.id) }))
								.sort((a, b) => a.label.localeCompare(b.label, 'ko'))}
						/>
					</label>
				</div>
			)}
			{tab === 'then' && root !== '' && <Then root={root} />}
			{tab === 'diff' && root !== '' && <Diff root={root} />}
		</>
	)
}

function Now(): ReactNode {
	const c = useCatalog()
	const [open, setOpen] = useState<string>()
	const kids = useMemo(() => {
		const m = new Map<string, Asset[]>()
		for (const s of c.spaces) {
			const p = c.space(s.parentId) === undefined ? '' : idStr(s.parentId)
			m.set(p, [...(m.get(p) ?? []), s])
		}
		for (const v of m.values()) v.sort((a, b) => a.name.localeCompare(b.name, 'ko'))
		return m
	}, [c])

	const Node = (p: { s: Asset; depth: number }): ReactNode => {
		const id = idStr(p.s.id)
		const under = kids.get(id) ?? []
		return (
			<li>
				<button className={`node ${open === id ? 'on' : ''}`} onClick={() => setOpen(id)} style={{ paddingLeft: `${p.depth * 16 + 8}px` }}>
					⌂ {p.s.name} <span className="mute small">{c.type(p.s.type?.id)?.name ?? ''}</span>
				</button>
				{under.length > 0 && (
					<ul>
						{under.map((k) => (
							<Node key={idStr(k.id)} s={k} depth={p.depth + 1} />
						))}
					</ul>
				)}
			</li>
		)
	}

	return (
		<div className="split">
			<Card title="공간 구조">
				{c.spaces.length === 0 ? (
					<Empty>공간이 없습니다. 자산 등록에서 종류를 '공간'으로 만들어 보세요.</Empty>
				) : (
					<ul className="tree">
						{(kids.get('') ?? []).map((s) => (
							<Node key={idStr(s.id)} s={s} depth={0} />
						))}
					</ul>
				)}
			</Card>
			{open !== undefined ? <Contents id={open} /> : <Card><Empty>왼쪽에서 공간을 고르세요.</Empty></Card>}
		</div>
	)
}

function Contents(props: { id: string }): ReactNode {
	const c = useCatalog()
	const s = c.space(idBytes(props.id))
	const kids = useRpc(AssetService.method.list, { filters: [{ parentId: idBytes(props.id) }], size: 500 })
	const all = useRpc(AssetService.method.search, { within: ref(idBytes(props.id)), size: 1 })
	return (
		<Card
			title={c.path(idBytes(props.id))}
			actions={
				<>
					<Link to={`/assets/${props.id}`}>공간 상세</Link> · <Link to={`/assets?within=${props.id}`}>하위 전체 {all.data?.total ?? ''}개</Link>
				</>
			}
		>
			{s !== undefined && Object.keys(s.attributes).length > 0 && (
				<p className="mute small">
					{Object.entries(s.attributes)
						.map(([k, v]) => `${c.type(s.type?.id)?.spec?.attributes.find((d) => d.key === k)?.label ?? k} ${v}`)
						.join(' · ')}
				</p>
			)}
			<Load q={kids}>
				{(d) =>
					d.items.length === 0 ? (
						<Empty>비어 있습니다.</Empty>
					) : (
						<ul className="rows">
							{d.items
								.slice()
								.sort((a, b) => (a.kind === b.kind ? a.tag.localeCompare(b.tag) : a.kind === 'space' ? -1 : 1))
								.map((a) => (
									<li key={idStr(a.id)}>
										<AssetLink a={a} />
										<span>
											<span className="mute small">
												{a.kind !== 'item' && `${kindWord[a.kind]} · `}
												{a.custodian !== undefined && `${c.party(a.custodian.id)?.name ?? ''} · `}
											</span>
											<Badge v={a.status} words={statusWord} />
										</span>
									</li>
								))}
						</ul>
					)
				}
			</Load>
		</Card>
	)
}

function Then(props: { root: string }): ReactNode {
	const c = useCatalog()
	const [at, setAt] = useState(localInput(c.inHistory(new Date(Date.now() - 365 * 86400_000))))
	const [known, setKnown] = useState('')
	const min = localInput(c.historyFrom())
	const q = useRpc(AssetService.method.queryAt, {
		root: ref(idBytes(props.root)),
		at: ts(new Date(at)),
		known: known === '' ? undefined : ts(new Date(known)),
		depth: 0,
	})
	return (
		<Card
			title="그때의 모습"
			actions={
				<div className="inline">
					<label className="inline">
						시점
						<input type="datetime-local" min={min} value={at} onChange={(e) => setAt(e.target.value)} />
					</label>
					<label className="inline" title="그 시점 이후에 늦게 기록된 일을 빼고 봅니다">
						기록 기준
						<input type="datetime-local" min={min} value={known} onChange={(e) => setKnown(e.target.value)} />
					</label>
				</div>
			}
		>
			<p className="mute small">
				'시점'에 무엇이 어디에 있었는지 보여 줍니다. '기록 기준'을 정하면 그때까지 기록된 것만으로 봅니다 — 그때 시스템이 무엇을 알고 있었는지입니다.
				<HistoryDays />
			</p>
			<Load q={q}>{(d) => <StateTree items={d.items} />}</Load>
		</Card>
	)
}

function StateTree(props: { items: AssetState[] }): ReactNode {
	const shown = props.items.filter((s) => s.existed)
	if (shown.length === 0) return <Empty>그때는 아무것도 없었습니다.</Empty>

	// Depth first, each thing under what held it then, spaces before things.
	const ids = new Set(shown.map((s) => idStr(s.id)))
	const under = new Map<string, AssetState[]>()
	for (const s of shown) {
		const p = ids.has(idStr(s.parentId)) ? idStr(s.parentId) : ''
		under.set(p, [...(under.get(p) ?? []), s])
	}
	for (const v of under.values()) {
		v.sort((a, b) => (a.kind === b.kind ? a.tag.localeCompare(b.tag) : a.kind === 'space' ? -1 : b.kind === 'space' ? 1 : a.tag.localeCompare(b.tag)))
	}
	const rows: { s: AssetState; depth: number }[] = []
	const walk = (p: string, depth: number) => {
		for (const s of under.get(p) ?? []) {
			rows.push({ s, depth })
			walk(idStr(s.id), depth + 1)
		}
	}
	walk('', 0)

	return (
		<ul className="state-tree">
			{rows.map(({ s, depth }) => (
				<li key={idStr(s.id)} style={{ paddingLeft: `${depth * 18}px` }}>
					<span className={s.kind === 'space' ? 'space' : ''}>
						{s.kind === 'space' ? '⌂' : '•'} <code>{s.tag}</code>{' '}
						<Link to={`/assets/${idStr(s.id)}`}>{s.facts['name'] ?? ''}</Link>
					</span>
					<span className="mute small">
						{s.mode !== '' && s.mode !== 'located' && ` ${modeWord[s.mode]}`}
						{s.uFrom > 0 && ` U${s.uFrom}${s.uTo > s.uFrom ? `–${s.uTo}` : ''}`}
						{s.slot !== '' && ` · ${s.slot}`}
						{s.stewardNames['custodian'] !== undefined && ` · ${s.stewardNames['custodian']}`}
						{s.facts['status'] !== undefined && s.facts['status'] !== 'active' && ` · ${statusWord[s.facts['status']] ?? s.facts['status']}`}
						{s.facts['condition'] !== undefined && s.facts['condition'] !== 'good' && ` · ${conditionWord[s.facts['condition']] ?? ''}`}
					</span>
				</li>
			))}
		</ul>
	)
}

/** What a changed field is called, for a person. */
function fieldName(f: string): string {
	const names: Record<string, string> = {
		parent: '위치',
		name: '이름',
		desc: '설명',
		status: '상태',
		condition: '컨디션',
		serial: '시리얼',
		tag: '태그',
		type: '유형',
		model: '모델',
		'steward.custodian': '사용자',
		'steward.owner': '소유',
		'steward.manager': '관리 담당',
	}
	return names[f] ?? f.replace(/^attr\./, '속성 ')
}

/** A value as a person reads it: a status word rather than the code. */
function valueOf(f: string, v: string): string {
	if (f === 'status') return statusWord[v] ?? v
	if (f === 'condition') return conditionWord[v] ?? v
	return v
}

const whatWord: Record<string, string> = { entered: '들어옴', left: '나감', moved: '자리 바뀜', changed: '바뀜' }

function Diff(props: { root: string }): ReactNode {
	const c = useCatalog()
	const [from, setFrom] = useState(localInput(c.inHistory(new Date(Date.now() - 90 * 86400_000))))
	const [to, setTo] = useState(localInput(new Date()))
	const min = localInput(c.historyFrom())
	const q = useRpc(AssetService.method.diff, { root: ref(idBytes(props.root)), from: ts(new Date(from)), to: ts(new Date(to)) })
	return (
		<Card
			title="두 시점 비교"
			actions={
				<div className="inline">
					<input type="datetime-local" min={min} value={from} onChange={(e) => setFrom(e.target.value)} />→
					<input type="datetime-local" min={min} value={to} onChange={(e) => setTo(e.target.value)} />
				</div>
			}
		>
			<Load q={q}>
				{(d) =>
					d.changes.length === 0 ? (
						<Empty>바뀐 것이 없습니다.</Empty>
					) : (
						<div className="table-wrap">
							<table className="table">
								<thead>
									<tr>
										<th>자산</th>
										<th>변화</th>
										<th>항목</th>
										<th>전</th>
										<th>후</th>
									</tr>
								</thead>
								<tbody>
									{d.changes.map((x, i) => (
										<tr key={i}>
											<td>
												<Link to={`/assets/${idStr(x.id)}`}>
													<code>{x.tag}</code> {x.name}
												</Link>
											</td>
											<td>
												<Badge v={x.what === 'left' ? 'missing' : x.what === 'entered' ? 'ok' : 'pending'}>{whatWord[x.what] ?? x.what}</Badge>
											</td>
											<td>{fieldName(x.field)}</td>
											<td className="mute">{valueOf(x.field, x.before)}</td>
											<td>{valueOf(x.field, x.after)}</td>
										</tr>
									))}
								</tbody>
							</table>
						</div>
					)
				}
			</Load>
		</Card>
	)
}
