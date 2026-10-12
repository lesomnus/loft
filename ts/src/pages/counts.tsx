import { useEffect, useRef, useState, type ReactNode } from 'react'
import { Link, useNavigate, useParams } from 'react-router-dom'

import type { CountFinding, InventoryCount } from '../../gen/rove/count_pb.js'
import { CountFindingService, InventoryCountService, fmt, idBytes, idStr, op, ref, ts } from '../api.js'
import { useCatalog } from '../catalog.js'
import { SpaceSelect, useAssets } from '../pickers.js'
import { Live, useAct, useRpc } from '../rpc.js'
import { Badge, Card, Confirm, Empty, Field, FormModal, Load, OrderSelect, PageHead, Select, Stat, useToast, type Order } from '../ui.js'
import { countWord, findingWord, resolutionWord } from '../words.js'
import { Camera } from './scan.js'

export function Counts(): ReactNode {
	const c = useCatalog()
	const go = useNavigate()
	const [order, setOrder] = useState<Order>('newest')
	const list = useRpc(InventoryCountService.method.list, { size: 200, reverse: order === 'oldest' })
	const [adding, setAdding] = useState(false)
	return (
		<>
			<PageHead
				title="실사"
				sub="공간을 정해 실제로 있는 것을 스캔하고, 있어야 할 것과 맞춰 봅니다. 자가 실사는 누구나 스캔할 수 있습니다."
				actions={c.can('manager') && <button className="primary" onClick={() => setAdding(true)}>+ 실사 시작</button>}
			/>
			<div className="filters">
				<OrderSelect value={order} onChange={setOrder} />
			</div>
			<Load q={list}>
				{(d) =>
					d.items.length === 0 ? (
						<Empty>진행한 실사가 없습니다.</Empty>
					) : (
						<ul className="rows cards">
							{d.items.map((x) => (
								<li key={idStr(x.id)} className="link" onClick={() => go(`/counts/${idStr(x.id)}`)}>
									<span>
										<strong>{x.name}</strong>
										<span className="mute"> · {c.path(x.scope?.id)} · {fmt(x.startedAt, 'date')}</span>
										{x.selfService && <span className="chip">자가 실사</span>}
									</span>
									<Badge v={x.status} words={countWord} />
								</li>
							))}
						</ul>
					)
				}
			</Load>
			{adding && <NewCount onClose={() => setAdding(false)} />}
		</>
	)
}

function NewCount(props: { onClose: () => void }): ReactNode {
	const act = useAct()
	const go = useNavigate()
	const [scope, setScope] = useState('')
	const [name, setName] = useState('')
	const [self, setSelf] = useState(false)
	return (
		<FormModal
			title="실사 시작"
			submit="시작"
			onClose={props.onClose}
			onSubmit={async () => {
				const v = await act(InventoryCountService.method.add, { scope: ref(idBytes(scope)), name, selfService: self }, { ok: '시작했습니다.' })
				if (v === undefined) return false
				go(`/counts/${idStr(v.id)}`)
			}}
		>
			<Field label="범위 (공간) *">
				<SpaceSelect value={scope} onChange={setScope} required />
			</Field>
			<Field label="이름" hint="비우면 공간과 날짜">
				<input value={name} onChange={(e) => setName(e.target.value)} />
			</Field>
			<label className="check wide">
				<input type="checkbox" checked={self} onChange={(e) => setSelf(e.target.checked)} /> 자가 실사 — 구성원 누구나 자기 자리에서 스캔
			</label>
		</FormModal>
	)
}

export function CountPage(): ReactNode {
	const { id = '' } = useParams()
	// Every scan, reconcile and resolution -- anybody's -- is written to the
	// count's row too, so the page follows the row and reads its findings again.
	return (
		<Live watch={InventoryCountService.method.watch} ids={[idBytes(id)]}>
			<CountRead id={id} />
		</Live>
	)
}

function CountRead(props: { id: string }): ReactNode {
	const v = useRpc(InventoryCountService.method.get, { ref: ref(idBytes(props.id)) })
	return <Load q={v}>{(x) => <CountView c={x} />}</Load>
}

/** Scans made while offline, sent when the connection is back. */
const QUEUE = 'rove.count.queue'

type Queued = { count: string; code: string; at: string; seenAt: string; op: string }

function readQueue(): Queued[] {
	try {
		return JSON.parse(localStorage.getItem(QUEUE) ?? '[]') as Queued[]
	} catch {
		return []
	}
}

function CountView(props: { c: InventoryCount }): ReactNode {
	const x = props.c
	const c = useCatalog()
	const act = useAct()
	const toast = useToast()
	const findings = useRpc(CountFindingService.method.list, { filters: [{ count: ref(x.id) }], size: 500 })
	const assets = useAssets((findings.data?.items ?? []).map((f) => f.asset?.id))
	const [code, setCode] = useState('')
	const [at, setAt] = useState('')
	const [camera, setCamera] = useState(false)
	const [filter, setFilter] = useState('')
	const [queued, setQueued] = useState(readQueue().filter((q) => q.count === idStr(x.id)).length)
	const open = x.status === 'open'
	const input = useRef<HTMLInputElement>(null)

	const send = async (q: Queued): Promise<boolean> => {
		const v = await act(
			InventoryCountService.method.scan,
			{
				ref: ref(idBytes(q.count)),
				code: q.code,
				at: q.at === '' ? undefined : ref(idBytes(q.at)),
				seenAt: ts(new Date(q.seenAt)),
				op: idBytes(q.op),
			},
			{ quiet: true },
		)
		return v !== undefined
	}

	const flush = async () => {
		const all = readQueue()
		const left: Queued[] = []
		for (const q of all) {
			if (q.count !== idStr(x.id)) {
				left.push(q)
				continue
			}
			if (!(await send(q))) left.push(q)
		}
		localStorage.setItem(QUEUE, JSON.stringify(left))
		setQueued(left.filter((q) => q.count === idStr(x.id)).length)
	}

	useEffect(() => {
		const f = () => void flush()
		window.addEventListener('online', f)
		return () => window.removeEventListener('online', f)
	})

	const scan = async (v: string) => {
		const text = v.trim()
		if (text === '') return
		const q: Queued = { count: idStr(x.id), code: text, at, seenAt: new Date().toISOString(), op: idStr(op()) }
		if (!navigator.onLine) {
			localStorage.setItem(QUEUE, JSON.stringify([...readQueue(), q]))
			setQueued((n) => n + 1)
			toast('오프라인: 저장해 두었다가 연결되면 보냅니다.', 'info')
			setCode('')
			input.current?.focus()
			return
		}
		const ok = await send(q)
		if (ok) toast(`확인: ${text}`)
		else {
			localStorage.setItem(QUEUE, JSON.stringify([...readQueue(), q]))
			setQueued((n) => n + 1)
			toast('보내지 못해 저장해 두었습니다.', 'bad')
		}
		setCode('')
		input.current?.focus()
	}

	const items = findings.data?.items ?? []
	const count = (k: string) => items.filter((f) => f.kind === k).length
	const shown = items.filter((f) => filter === '' || f.kind === filter || (filter === 'open' && f.resolution === 'open'))

	return (
		<>
			<PageHead
				title={x.name}
				sub={
					<>
						<Badge v={x.status} words={countWord} /> {c.path(x.scope?.id)} · 시작 {fmt(x.startedAt)}
						{x.closedAt !== undefined && ` · 종료 ${fmt(x.closedAt)}`}
					</>
				}
				actions={
					open &&
					c.can('manager') && (
						<>
							<button onClick={() => void act(InventoryCountService.method.reconcile, { ref: ref(x.id) }, { ok: '대조했습니다. 못 찾은 자산이 표시됩니다.' })}>
								대조
							</button>
							<Confirm label="실사 종료" question="종료하면 더 스캔할 수 없습니다." onYes={() => act(InventoryCountService.method.close, { ref: ref(x.id) }, { ok: '종료했습니다.' })} />
						</>
					)
				}
			/>
			<div className="stats">
				<Stat label="확인" value={count('seen')} tone="ok" onClick={() => setFilter('seen')} />
				<Stat label="위치 다름" value={count('misplaced')} tone="warn" onClick={() => setFilter('misplaced')} />
				<Stat label="미발견" value={count('missing')} tone="bad" onClick={() => setFilter('missing')} />
				<Stat label="미등록" value={count('unknown')} tone="bad" onClick={() => setFilter('unknown')} />
				<Stat label="미처리" value={items.filter((f) => f.resolution === 'open').length} onClick={() => setFilter('open')} />
			</div>
			{open && (
				<Card title="스캔">
					<form
						className="scanbar"
						onSubmit={(e) => {
							e.preventDefault()
							void scan(code)
						}}
					>
						<input
							ref={input}
							value={code}
							onChange={(e) => setCode(e.target.value)}
							placeholder="QR을 스캔하거나 태그를 입력 후 Enter"
							autoFocus
						/>
						<button type="button" onClick={() => setCamera((v) => !v)}>
							{camera ? '카메라 끄기' : '카메라'}
						</button>
						<label className="inline">
							발견 위치
							<SpaceSelect value={at} onChange={setAt} empty="(범위 공간)" />
						</label>
					</form>
					{camera && <Camera onCode={(v) => void scan(v)} />}
					{queued > 0 && (
						<p className="warn small">
							보내지 못한 스캔 {queued}건 <button className="link" onClick={() => void flush()}>지금 보내기</button>
						</p>
					)}
				</Card>
			)}
			<Card title="결과" actions={filter !== '' && <button className="link" onClick={() => setFilter('')}>전체 보기</button>}>
				<Load q={findings}>
					{() =>
						shown.length === 0 ? (
							<Empty>아직 없습니다.</Empty>
						) : (
							<ul className="rows">
								{shown.map((f) => (
									<Finding key={idStr(f.id)} f={f} name={f.asset !== undefined ? assets.get(idStr(f.asset.id)) : undefined} open={open} />
								))}
							</ul>
						)
					}
				</Load>
			</Card>
		</>
	)
}

function Finding(props: { f: CountFinding; name: { id: Uint8Array; tag: string; name: string } | undefined; open: boolean }): ReactNode {
	const f = props.f
	const c = useCatalog()
	const [resolve, setResolve] = useState(false)
	return (
		<li>
			<span>
				<Badge v={f.kind} words={findingWord} />{' '}
				{props.name !== undefined ? (
					<Link to={`/assets/${idStr(props.name.id)}`}>
						<code>{props.name.tag}</code> {props.name.name}
					</Link>
				) : (
					<span className="mute">등록되지 않은 라벨</span>
				)}
				{f.kind === 'misplaced' && (
					<span className="mute small">
						{' '}
						— 장부 {c.path(f.expectedParentId) || '?'} / 발견 {c.path(f.observedParentId) || '?'}
					</span>
				)}
				{f.kind === 'missing' && <span className="mute small"> — 장부상 {c.path(f.expectedParentId) || '?'}</span>}
				<span className="mute small"> · {fmt(f.seenAt)}</span>
			</span>
			<span className="inline">
				<Badge v={f.resolution} words={resolutionWord} />
				{props.open && f.resolution === 'open' && c.can('manager') && (
					<button className="small" onClick={() => setResolve(true)}>
						처리
					</button>
				)}
			</span>
			{resolve && <Resolve f={f} onClose={() => setResolve(false)} />}
		</li>
	)
}

function Resolve(props: { f: CountFinding; onClose: () => void }): ReactNode {
	const act = useAct()
	const f = props.f
	const [res, setRes] = useState(f.kind === 'misplaced' ? 'moved' : f.kind === 'missing' ? 'lost' : 'ignored')
	const [reason, setReason] = useState('')
	return (
		<FormModal
			title="실사 결과 처리"
			submit="처리"
			onClose={props.onClose}
			onSubmit={async () => (await act(InventoryCountService.method.resolve, { ref: ref(f.id), resolution: res, reason }, { ok: '처리했습니다.' })) !== undefined}
		>
			<Field label="처리">
				<Select
					value={res}
					onChange={setRes}
					options={[
						...(f.kind === 'misplaced' ? [{ value: 'moved', label: '발견된 위치로 옮김 (발견 시각 기준)' }] : []),
						...(f.asset !== undefined ? [{ value: 'lost', label: '분실 처리' }] : []),
						{ value: 'ignored', label: '무시' },
					]}
				/>
			</Field>
			<Field label="사유" wide>
				<input value={reason} onChange={(e) => setReason(e.target.value)} />
			</Field>
		</FormModal>
	)
}
