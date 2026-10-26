import { useState, type ReactNode } from 'react'
import { useNavigate } from 'react-router-dom'

import type { Purchase } from '../../gen/rove/work_pb.js'
import { PurchaseLineService, PurchaseService, amountIn, amountStep, fmt, idBytes, idStr, money, place, ref, unitOf } from '../api.js'
import { modelName, useCatalog } from '../catalog.js'
import { ModelSelect, PartySelect, SpaceSelect } from '../pickers.js'
import { useAct, useRpc } from '../rpc.js'
import { Badge, Empty, Field, FormModal, Kv, Load, Modal, OrderSelect, PageHead, Select, type Order } from '../ui.js'
import { purchaseWord } from '../words.js'

export function Purchases(): ReactNode {
	const c = useCatalog()
	const [order, setOrder] = useState<Order>('newest')
	const list = useRpc(PurchaseService.method.list, { size: 500, reverse: order === 'oldest' })
	const [adding, setAdding] = useState(false)
	const [open, setOpen] = useState<Purchase>()
	return (
		<>
			<PageHead
				title="구매"
				sub="주문한 것을 적어 두고, 도착하면 입고 처리해 자산이나 재고로 등록합니다."
				actions={<button className="primary" onClick={() => setAdding(true)}>+ 구매 등록</button>}
			/>
			<div className="filters">
				<OrderSelect value={order} onChange={setOrder} />
			</div>
			<Load q={list}>
				{(d) =>
					d.items.length === 0 ? (
						<Empty>구매 기록이 없습니다.</Empty>
					) : (
						<div className="table-wrap">
							<table className="table">
								<thead>
									<tr>
										<th>구매</th>
										<th>업체</th>
										<th>주문일</th>
										<th className="num">금액</th>
										<th>상태</th>
									</tr>
								</thead>
								<tbody>
									{d.items.map((p) => (
										<tr key={idStr(p.id)} className="link" onClick={() => setOpen(p)}>
											<td>
												{p.name}
												{p.reference !== '' && <span className="mute"> · {p.reference}</span>}
											</td>
											<td>{c.called(p.vendor?.id) || '-'}</td>
											<td>{fmt(p.orderedAt, 'date')}</td>
											<td className="num">{money(p.total, p.currency)}</td>
											<td>
												<Badge v={p.status} words={purchaseWord} />
											</td>
										</tr>
									))}
								</tbody>
							</table>
						</div>
					)
				}
			</Load>
			{adding && <NewPurchase onClose={() => setAdding(false)} />}
			{open !== undefined && <PurchaseDialog p={open} onClose={() => setOpen(undefined)} />}
		</>
	)
}

type LineDraft = { model: string; desc: string; quantity: string; unitCost: string; receiveAs: string }

function NewPurchase(props: { onClose: () => void }): ReactNode {
	const act = useAct()
	const [name, setName] = useState('')
	const [reference, setReference] = useState('')
	const [vendor, setVendor] = useState('')
	const [lines, setLines] = useState<LineDraft[]>([{ model: '', desc: '', quantity: '1', unitCost: '0', receiveAs: 'asset' }])
	// A new purchase is priced in the workspace's currency, in its minor unit.
	const currency = place.currency
	const total = lines.reduce((s, l) => s + Number(l.quantity || 0) * Number(amountIn(l.unitCost, currency) ?? 0n), 0)
	return (
		<FormModal
			title="구매 등록"
			wide
			onClose={props.onClose}
			onSubmit={async () =>
				(await act(
					PurchaseService.method.add,
					{
						name,
						reference,
						vendor: vendor === '' ? undefined : ref(idBytes(vendor)),
						// The currency the amounts were typed in, which the
						// workspace may have changed since the page was drawn.
						currency,
						lines: lines.map((l) => ({
							model: l.model === '' ? undefined : ref(idBytes(l.model)),
							desc: l.desc,
							quantity: BigInt(l.quantity || '0'),
							unitCost: amountIn(l.unitCost, currency) ?? 0n,
							receiveAs: l.receiveAs,
						})),
					},
					{ ok: '등록했습니다.' },
				)) !== undefined
			}
		>
			<Field label="이름">
				<input value={name} onChange={(e) => setName(e.target.value)} placeholder="비우면 날짜" />
			</Field>
			<Field label="주문 번호">
				<input value={reference} onChange={(e) => setReference(e.target.value)} />
			</Field>
			<Field label="업체">
				<PartySelect value={vendor} onChange={setVendor} kinds={['vendor']} empty="(없음)" />
			</Field>
			<div className="wide">
				<table className="table compact">
					<thead>
						<tr>
							<th>모델</th>
							<th>설명</th>
							<th>수량</th>
							<th>단가 ({unitOf(currency)})</th>
							<th>입고 형태</th>
							<th />
						</tr>
					</thead>
					<tbody>
						{lines.map((l, i) => {
							const set = (v: Partial<LineDraft>) => setLines(lines.map((x, j) => (j === i ? { ...x, ...v } : x)))
							return (
								<tr key={i}>
									<td>
										<ModelSelect value={l.model} onChange={(v) => set({ model: v })} empty="(없음)" />
									</td>
									<td>
										<input value={l.desc} onChange={(e) => set({ desc: e.target.value })} />
									</td>
									<td>
										<input type="number" min={1} value={l.quantity} onChange={(e) => set({ quantity: e.target.value })} />
									</td>
									<td>
										<input type="number" min={0} step={amountStep(currency)} value={l.unitCost} onChange={(e) => set({ unitCost: e.target.value })} />
									</td>
									<td>
										<Select value={l.receiveAs} onChange={(v) => set({ receiveAs: v })} options={[{ value: 'asset', label: '자산 (하나씩)' }, { value: 'stock', label: '재고 (수량)' }]} />
									</td>
									<td>
										<button type="button" className="link" onClick={() => setLines(lines.filter((_, j) => j !== i))}>
											빼기
										</button>
									</td>
								</tr>
							)
						})}
					</tbody>
				</table>
				<div className="inline">
					<button type="button" onClick={() => setLines([...lines, { model: '', desc: '', quantity: '1', unitCost: '0', receiveAs: 'asset' }])}>
						+ 줄 추가
					</button>
					<span className="mute">합계 {money(total, currency)}</span>
				</div>
			</div>
		</FormModal>
	)
}

function PurchaseDialog(props: { p: Purchase; onClose: () => void }): ReactNode {
	const c = useCatalog()
	const act = useAct()
	const go = useNavigate()
	const p = props.p
	const lines = useRpc(PurchaseLineService.method.list, { filters: [{ purchase: ref(p.id) }], size: 200 })
	const [receive, setReceive] = useState(false)
	const [into, setInto] = useState('')
	const [prefix, setPrefix] = useState('')

	if (receive) {
		return (
			<FormModal
				title="입고"
				submit="입고"
				onClose={props.onClose}
				onSubmit={async () => {
					const v = await act(PurchaseService.method.receive, { ref: ref(p.id), into: ref(idBytes(into)), tagPrefix: prefix }, { ok: '입고했습니다.' })
					if (v === undefined) return false
					if (v.assets.length > 0) go(`/assets?q=${encodeURIComponent(prefix)}`)
				}}
			>
				<Field label="받은 곳 *">
					<SpaceSelect value={into} onChange={setInto} required />
				</Field>
				<Field label="태그 앞부분" hint="예: MN → MN-001 … 비우면 자동">
					<input value={prefix} onChange={(e) => setPrefix(e.target.value)} />
				</Field>
			</FormModal>
		)
	}
	return (
		<Modal title={p.name} onClose={props.onClose} wide>
			<Kv
				items={[
					['상태', <Badge key="s" v={p.status} words={purchaseWord} />],
					['업체', c.called(p.vendor?.id) || '-'],
					['주문 번호', p.reference || '-'],
					['주문일', fmt(p.orderedAt, 'date')],
					['입고일', fmt(p.receivedAt, 'date')],
					['합계', money(p.total, p.currency)],
				]}
			/>
			<Load q={lines}>
				{(d) => (
					<table className="table compact">
						<thead>
							<tr>
								<th>품목</th>
								<th className="num">수량</th>
								<th className="num">단가</th>
								<th>형태</th>
								<th className="num">입고</th>
							</tr>
						</thead>
						<tbody>
							{d.items.map((l) => (
								<tr key={idStr(l.id)}>
									<td>{modelName(c.model(l.model?.id)) || l.desc}</td>
									<td className="num">{String(l.quantity)}</td>
									<td className="num">{money(l.unitCost, p.currency)}</td>
									<td>{l.receiveAs === 'stock' ? '재고' : '자산'}</td>
									<td className="num">{String(l.receivedQuantity)}</td>
								</tr>
							))}
						</tbody>
					</table>
				)}
			</Load>
			{p.status === 'ordered' && c.can('manager') && (
				<footer className="modal-actions">
					<button className="primary" onClick={() => setReceive(true)}>
						입고 처리
					</button>
				</footer>
			)}
		</Modal>
	)
}
