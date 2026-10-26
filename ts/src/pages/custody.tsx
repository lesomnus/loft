import { useState, type ReactNode } from 'react'
import { Link, useNavigate, useParams, useSearchParams } from 'react-router-dom'

import type { Asset } from '../../gen/rove/asset_pb.js'
import type { Custody, CustodyLine } from '../../gen/rove/custody_pb.js'
import { AssetService, BookableService, CustodyLineService, CustodyService, StockService, dateOf, fmt, fromInput, idBytes, idStr, localInput, minute, op, ref, sameId, ts } from '../api.js'
import { useCatalog } from '../catalog.js'
import { AssetLink, AssetPicker, PartySelect, SpaceSelect } from '../pickers.js'
import { useAct, useRpc } from '../rpc.js'
import { Badge, Card, Empty, Field, FormModal, Kv, Load, PageHead, Select } from '../ui.js'
import { conditionWord, custodyKindWord, custodyWord } from '../words.js'

export function Custodies(): ReactNode {
	const c = useCatalog()
	const [sp, setSp] = useSearchParams()
	const status = sp.get('status') ?? 'open'
	const overdue = sp.get('overdue') === '1'
	const [adding, setAdding] = useState(false)
	const go = useNavigate()
	const list = useRpc(CustodyService.method.list, { filters: status === '' ? [] : [{ status }], size: 500 })
	const now = Date.now()

	const rows = (list.data?.items ?? [])
		.filter((x) => !overdue || (x.status === 'open' && (dateOf(x.dueAt)?.getTime() ?? Infinity) < now))
		.sort((a, b) => (dateOf(b.issuedAt)?.getTime() ?? 0) - (dateOf(a.issuedAt)?.getTime() ?? 0))

	return (
		<>
			<PageHead
				title="지급·대여"
				sub="자산을 사람에게 건네고 돌려받는 기록입니다. 지급은 기한 없이, 대여는 반납 기한이 있습니다."
				actions={c.can('manager') && <button className="primary" onClick={() => setAdding(true)}>+ 지급·대여</button>}
			/>
			<div className="filters">
				<Select
					value={status}
					onChange={(v) => setSp((p) => { const n = new URLSearchParams(p); n.set('status', v); return n })}
					options={[
						{ value: 'open', label: '진행 중' },
						{ value: 'returned', label: '반납 완료' },
						{ value: '', label: '전체' },
					]}
				/>
				<label className="check">
					<input
						type="checkbox"
						checked={overdue}
						onChange={(e) => setSp((p) => { const n = new URLSearchParams(p); if (e.target.checked) n.set('overdue', '1'); else n.delete('overdue'); return n })}
					/>
					반납 기한 지난 것만
				</label>
			</div>
			<Load q={list}>
				{() =>
					rows.length === 0 ? (
						<Empty>없습니다.</Empty>
					) : (
						<div className="table-wrap">
							<table className="table">
								<thead>
									<tr>
										<th>받은 사람</th>
										<th>구분</th>
										<th>건넨 날</th>
										<th>반납 기한</th>
										<th>인수 확인</th>
										<th>상태</th>
										<th>메모</th>
									</tr>
								</thead>
								<tbody>
									{rows.map((x) => {
										const late = x.status === 'open' && (dateOf(x.dueAt)?.getTime() ?? Infinity) < now
										return (
											<tr key={idStr(x.id)} className="link" onClick={() => go(`/custody/${idStr(x.id)}`)}>
												<td>{c.party(x.party?.id)?.name ?? '-'}</td>
												<td>{custodyKindWord[x.kind] ?? x.kind}</td>
												<td>{fmt(x.issuedAt, 'date')}</td>
												<td className={late ? 'bad' : ''}>{x.dueAt !== undefined ? fmt(x.dueAt) : '-'}</td>
												<td>{x.acknowledgedAt !== undefined ? fmt(x.acknowledgedAt, 'date') : <span className="warn">대기</span>}</td>
												<td>
													<Badge v={late ? 'missing' : x.status} words={late ? { missing: '연체' } : custodyWord} />
												</td>
												<td className="mute">{x.desc}</td>
											</tr>
										)
									})}
								</tbody>
							</table>
						</div>
					)
				}
			</Load>
			{adding && <NewCustody assets={[]} onClose={() => setAdding(false)} />}
		</>
	)
}

export function CustodyPage(): ReactNode {
	const { id = '' } = useParams()
	const v = useRpc(CustodyService.method.get, { ref: ref(idBytes(id)) })
	return <Load q={v}>{(x) => <CustodyView c={x} />}</Load>
}

function CustodyView(props: { c: Custody }): ReactNode {
	const x = props.c
	const c = useCatalog()
	const act = useAct()
	const lines = useRpc(CustodyLineService.method.list, { filters: [{ custody: ref(x.id) }], size: 500 })
	const [dialog, setDialog] = useState<'return' | 'extend'>()
	const mine = sameId(c.me.party?.id, x.party?.id)
	const late = x.status === 'open' && (dateOf(x.dueAt)?.getTime() ?? Infinity) < Date.now()

	return (
		<>
			<PageHead
				title={`${custodyKindWord[x.kind] ?? x.kind}: ${c.party(x.party?.id)?.name ?? ''}`}
				sub={<Badge v={late ? 'missing' : x.status} words={late ? { missing: '연체' } : custodyWord} />}
				actions={
					<>
						{x.status === 'open' && x.acknowledgedAt === undefined && (mine || c.can('manager')) && (
							<button className="primary" onClick={() => void act(CustodyService.method.acknowledge, { ref: ref(x.id) }, { ok: '인수를 확인했습니다.' })}>
								인수 확인
							</button>
						)}
						{x.status === 'open' && c.can('manager') && (
							<>
								<button onClick={() => setDialog('extend')}>기한 연장</button>
								<button className="primary" onClick={() => setDialog('return')}>
									반납 받기
								</button>
							</>
						)}
					</>
				}
			/>
			<div className="grid2">
				<Card title="내용">
					<Kv
						items={[
							['받은 사람', c.party(x.party?.id)?.name ?? '-'],
							['건넨 날', fmt(x.issuedAt)],
							['반납 기한', x.dueAt !== undefined ? fmt(x.dueAt) : '없음 (지급)'],
							['인수 확인', x.acknowledgedAt !== undefined ? fmt(x.acknowledgedAt) : '대기'],
							['반납', x.returnedAt !== undefined ? fmt(x.returnedAt) : '-'],
							['메모', x.desc || '-'],
						]}
					/>
				</Card>
				<Card title="품목">
					<Load q={lines}>
						{(d) => (
							<ul className="rows">
								{d.items.map((l) => (
									<Line key={idStr(l.id)} l={l} />
								))}
							</ul>
						)}
					</Load>
				</Card>
			</div>
			{dialog === 'return' && lines.data !== undefined && <Return c={x} lines={lines.data.items} onClose={() => setDialog(undefined)} />}
			{dialog === 'extend' && <Extend c={x} onClose={() => setDialog(undefined)} />}
		</>
	)
}

function Line(props: { l: CustodyLine }): ReactNode {
	const l = props.l
	const a = useRpc(AssetService.method.get, l.asset !== undefined ? { ref: ref(l.asset.id) } : null)
	const s = useRpc(StockService.method.get, l.stock !== undefined ? { ref: ref(l.stock.id) } : null)
	const left = l.quantity - l.returnedQuantity
	return (
		<li>
			<span>
				{l.asset !== undefined ? <AssetLink a={a.data} /> : `${s.data?.name ?? '재고'} × ${l.quantity}`}
				{l.conditionOut !== '' && <span className="mute"> · 건넬 때 {conditionWord[l.conditionOut]}</span>}
			</span>
			<span>{left > 0n ? <span className="warn">{l.asset !== undefined ? '사용 중' : `${left} 남음`}</span> : <span className="ok">반납 {fmt(l.returnedAt, 'date')}</span>}</span>
		</li>
	)
}

function Return(props: { c: Custody; lines: CustodyLine[]; onClose: () => void }): ReactNode {
	const act = useAct()
	const open = props.lines.filter((l) => l.quantity > l.returnedQuantity)
	const [pick, setPick] = useState<Record<string, { on: boolean; qty: string; cond: string }>>(
		Object.fromEntries(open.map((l) => [idStr(l.id), { on: true, qty: String(l.quantity - l.returnedQuantity), cond: '' }])),
	)
	const [to, setTo] = useState('')
	const [at, setAt] = useState('')
	const [key] = useState(op)

	return (
		<FormModal
			title="반납 받기"
			submit="반납"
			wide
			onClose={props.onClose}
			onSubmit={async () => {
				const lines = open
					.filter((l) => pick[idStr(l.id)]?.on)
					.map((l) => {
						const p = pick[idStr(l.id)]
						return { line: ref(l.id), quantity: BigInt(p?.qty || '0'), condition: p?.cond ?? '' }
					})
				if (lines.length === 0) return false
				return (
					(await act(
						CustodyService.method.return,
						{
							ref: ref(props.c.id),
							lines,
							to: to === '' ? undefined : ref(idBytes(to)),
							at: at === '' ? undefined : ts(fromInput(at)),
							op: key,
						},
						{ ok: '반납 처리했습니다.' },
					)) !== undefined
				)
			}}
		>
			<div className="wide">
				{open.map((l) => {
					const p = pick[idStr(l.id)] ?? { on: false, qty: '0', cond: '' }
					const set = (v: Partial<typeof p>) => setPick({ ...pick, [idStr(l.id)]: { ...p, ...v } })
					return (
						<div key={idStr(l.id)} className="return-line">
							<label className="check">
								<input type="checkbox" checked={p.on} onChange={(e) => set({ on: e.target.checked })} />
								<LineName l={l} />
							</label>
							{l.stock !== undefined && (
								<input type="number" min={1} max={Number(l.quantity - l.returnedQuantity)} value={p.qty} onChange={(e) => set({ qty: e.target.value })} />
							)}
							{l.asset !== undefined && (
								<Select value={p.cond} onChange={(v) => set({ cond: v })} empty="상태 그대로" options={Object.entries(conditionWord).map(([value, label]) => ({ value, label }))} />
							)}
						</div>
					)
				})}
			</div>
			<Field label="둘 곳" hint="비우면 원래 자리">
				<SpaceSelect value={to} onChange={setTo} empty="(그대로)" />
			</Field>
			<Field label="반납 시각" hint="비우면 지금">
				<input type="datetime-local" value={at} onChange={(e) => setAt(e.target.value)} />
			</Field>
		</FormModal>
	)
}

function LineName(props: { l: CustodyLine }): ReactNode {
	const a = useRpc(AssetService.method.get, props.l.asset !== undefined ? { ref: ref(props.l.asset.id) } : null)
	const s = useRpc(StockService.method.get, props.l.stock !== undefined ? { ref: ref(props.l.stock.id) } : null)
	if (props.l.asset !== undefined) return <span>{a.data !== undefined ? `${a.data.tag} ${a.data.name}` : '…'}</span>
	return <span>{s.data?.name ?? '재고'}</span>
}

function Extend(props: { c: Custody; onClose: () => void }): ReactNode {
	const act = useAct()
	const [due, setDue] = useState('')
	return (
		<FormModal
			title="반납 기한 연장"
			onClose={props.onClose}
			onSubmit={async () => (await act(CustodyService.method.extend, { ref: ref(props.c.id), dueAt: ts(fromInput(due)) }, { ok: '연장했습니다.' })) !== undefined}
		>
			<Field label="새 기한">
				<input type="datetime-local" value={due} onChange={(e) => setDue(e.target.value)} required />
			</Field>
		</FormModal>
	)
}

/**
 * What a hand-over may pick among: of a pickup, a pool's units free for the
 * reservation, as many as it holds put in to begin with (D46).
 */
export type Choice = { label: string; hint?: string; assets: Asset[] }

/**
 * Handing things over: assets one by one, and quantities out of stock. `note`
 * is what the one handing them over is to know first -- of a pickup, what the
 * reservation does not hold, or holds and may not hand over. `due` makes it a
 * loan due back then, as a pickup is: when its reservation ends. `choices` is
 * what may be picked instead of what it begins with, by ticking.
 */
export function NewCustody(props: {
	assets: Asset[]
	party?: string
	reservation?: Uint8Array
	due?: Date
	note?: ReactNode
	choices?: Choice[]
	onClose: () => void
}): ReactNode {
	const act = useAct()
	const go = useNavigate()
	const [party, setParty] = useState(props.party ?? '')
	const [kind, setKind] = useState(props.due === undefined ? 'issue' : 'loan')
	const [due, setDue] = useState(localInput(props.due))
	const [assets, setAssets] = useState<Asset[]>(props.assets)
	const [next, setNext] = useState<Asset>()
	const [stock, setStock] = useState('')
	const [qty, setQty] = useState('1')
	const [stocks, setStocks] = useState<{ id: string; qty: string; name: string }[]>([])
	const [desc, setDesc] = useState('')
	const [key] = useState(op)
	const all = useRpc(StockService.method.list, { size: 500 })

	return (
		<FormModal
			title="지급·대여"
			submit="건네기"
			wide
			onClose={props.onClose}
			onSubmit={async () => {
				if (party === '' || (assets.length === 0 && stocks.length === 0)) return false
				const v = await act(
					CustodyService.method.add,
					{
						party: ref(idBytes(party)),
						kind,
						dueAt: kind === 'loan' && due !== '' ? ts(fromInput(due)) : undefined,
						desc,
						reservationId: props.reservation,
						op: key,
						lines: [
							...assets.map((a) => ({ asset: ref(a.id) })),
							...stocks.map((s) => ({ stock: ref(idBytes(s.id)), quantity: BigInt(s.qty) })),
						],
					},
					{ ok: '건넸습니다. 받은 사람에게 인수 확인 알림이 갑니다.' },
				)
				if (v === undefined) return false
				go(`/custody/${idStr(v.id)}`)
			}}
		>
			{props.note !== undefined && <div className="wide">{props.note}</div>}
			{(props.choices ?? []).map((c) => (
				<Field key={c.label} label={c.label} hint={c.hint} wide group>
					<div className="boxes">
						{c.assets.map((a) => (
							<label key={idStr(a.id)} className="check">
								<input
									type="checkbox"
									checked={assets.some((x) => sameId(x.id, a.id))}
									onChange={(e) => setAssets(e.target.checked ? [...assets, a] : assets.filter((x) => !sameId(x.id, a.id)))}
								/>{' '}
								<code>{a.tag}</code> {a.name}
							</label>
						))}
					</div>
				</Field>
			))}
			<Field label="받는 사람 *">
				<PartySelect value={party} onChange={setParty} kinds={['person', 'team']} required />
			</Field>
			<Field label="구분">
				<Select value={kind} onChange={setKind} options={[{ value: 'issue', label: '지급 (기한 없음)' }, { value: 'loan', label: '대여 (반납 기한)' }]} />
			</Field>
			{kind === 'loan' && (
				<Field label="반납 기한 *">
					<input type="datetime-local" value={due} onChange={(e) => setDue(e.target.value)} required />
				</Field>
			)}
			<Field label="자산 추가" wide group>
				<AssetPicker
					value={next}
					onChange={(a) => {
						if (a !== undefined && !assets.some((x) => sameId(x.id, a.id))) setAssets([...assets, a])
						setNext(undefined)
					}}
					kind="item"
					exclude={assets.map((a) => a.id)}
				/>
			</Field>
			{assets.length > 0 && (
				<ul className="rows wide" aria-label="건넬 자산">
					{assets.map((a) => (
						<li key={idStr(a.id)}>
							<span>
								<code>{a.tag}</code> {a.name}
								{a.custodian !== undefined && <span className="bad"> · 이미 누군가 가지고 있음</span>}
							</span>
							<button type="button" className="link" onClick={() => setAssets(assets.filter((x) => x !== a))}>
								빼기
							</button>
						</li>
					))}
				</ul>
			)}
			<Booked assets={assets} due={kind === 'loan' && due !== '' ? fromInput(due) : undefined} reservation={props.reservation} />
			<Field label="재고에서">
				<Select
					value={stock}
					onChange={setStock}
					empty="재고 선택"
					options={(all.data?.items ?? []).map((s) => ({ value: idStr(s.id), label: `${s.name} (${s.quantity}${s.unit})` }))}
				/>
			</Field>
			<Field
				label="수량"
				beside={
					<button
						type="button"
						disabled={stock === ''}
						onClick={() => {
							const s = all.data?.items.find((x) => idStr(x.id) === stock)
							setStocks([...stocks, { id: stock, qty, name: s?.name ?? '' }])
							setStock('')
							setQty('1')
						}}
					>
						추가
					</button>
				}
			>
				<input type="number" min={1} value={qty} onChange={(e) => setQty(e.target.value)} />
			</Field>
			{stocks.length > 0 && (
				<ul className="rows wide">
					{stocks.map((s, i) => (
						<li key={i}>
							<span>
								{s.name} × {s.qty}
							</span>
							<button type="button" className="link" onClick={() => setStocks(stocks.filter((_, j) => j !== i))}>
								빼기
							</button>
						</li>
					))}
				</ul>
			)}
			<Field label="메모" wide>
				<input value={desc} onChange={(e) => setDesc(e.target.value)} placeholder="예: 입사 지급, 출장용" />
			</Field>
			<p className="wide mute small">
				<Link to="/scan">스캔</Link>으로도 자산을 찾을 수 있습니다.
			</p>
		</FormModal>
	)
}

/** The longest a calendar of what is taken is read over at once. */
const quarter = 91 * 86400_000

/**
 * What is booked of what is about to be handed over over the time it would be
 * out (D53): until a loan is due back, and the buffer after it; of an issue,
 * which is out until it is returned, the next three months. It is handed out
 * all the same, and each of those reservations keeps its time, says it is
 * out, and is told -- whoever booked it, and whoever runs the reservations.
 * Of a pickup, its own reservation is none of it.
 */
function Booked(props: { assets: Asset[]; due: Date | undefined; reservation?: Uint8Array }): ReactNode {
	const c = useCatalog()
	const from = minute()
	const buffer = Math.max(0, ...props.assets.map((a) => c.bookable(a.id)?.bufferAfter ?? 0))
	const to = props.due === undefined ? new Date(from.getTime() + quarter) : new Date(Math.min(props.due.getTime() + buffer * 60_000, from.getTime() + quarter))
	const ids = props.assets.map((a) => a.id)
	const busy = useRpc(BookableService.method.availability, ids.length === 0 || to <= from ? null : { resources: ids.map(ref), from: ts(from), to: ts(to) })
	const booked = (busy.data?.busy ?? []).filter((b) => (b.kind === 'reservation' || b.kind === 'override') && (props.reservation === undefined || !sameId(b.refId, props.reservation)))
	if (booked.length === 0) return null
	const named = (id: Uint8Array) => {
		const a = props.assets.find((x) => sameId(x.id, id))
		return a === undefined ? '' : `${a.tag} ${a.name}`
	}
	return (
		<div className="note warn wide" role="note" aria-label="겹치는 예약">
			<strong>겹치는 예약</strong>
			<ul>
				{booked.map((b) => (
					<li key={`${idStr(b.resourceId)}-${idStr(b.refId)}`}>
						{`${named(b.resourceId)}: '${b.label}' ${fmt(b.beginsAt)} – ${fmt(b.endsAt, 'time')}`}
					</li>
				))}
			</ul>
			<p className="small">건네도 그 예약은 시간을 그대로 잡고 나가 있는 자산으로 보이며, 예약한 사람과 관리자에게 알립니다. 돌아오기 전에는 그 예약의 수령 때 건넬 수 없습니다.</p>
		</div>
	)
}
