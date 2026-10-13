import { useState, type ReactNode } from 'react'

import type { Stock } from '../../gen/rove/stock_pb.js'
import { StockMovementService, StockService, fmt, idBytes, idStr, op, ref } from '../api.js'
import { modelName, useCatalog } from '../catalog.js'
import { ModelSelect, SpaceSelect } from '../pickers.js'
import { useAct, useRpc } from '../rpc.js'
import { Empty, Field, FormModal, Load, Modal, OrderSelect, PageHead, type Order } from '../ui.js'
import { movementWord } from '../words.js'

type Mode = 'receive' | 'consume' | 'adjust' | 'transfer' | 'convert' | 'history'

export function Stocks(): ReactNode {
	const c = useCatalog()
	const list = useRpc(StockService.method.list, { size: 500 })
	const [adding, setAdding] = useState(false)
	const [open, setOpen] = useState<{ s: Stock; mode: Mode }>()
	const mgr = c.can('manager')

	return (
		<>
			<PageHead
				title="재고"
				sub="하나하나 추적하지 않는 소모품과 비품을 수량으로 관리합니다. 기준 이하로 떨어지면 관리자에게 알립니다."
				actions={mgr && <button className="primary" onClick={() => setAdding(true)}>+ 재고 품목</button>}
			/>
			<Load q={list}>
				{(d) =>
					d.items.length === 0 ? (
						<Empty>재고 품목이 없습니다.</Empty>
					) : (
						<div className="table-wrap">
							<table className="table">
								<thead>
									<tr>
										<th>품목</th>
										<th>보관 장소</th>
										<th className="num">수량</th>
										<th className="num">기준</th>
										<th />
									</tr>
								</thead>
								<tbody>
									{d.items.map((s) => {
										const low = s.threshold > 0n && s.quantity <= s.threshold
										return (
											<tr key={idStr(s.id)}>
												<td>
													<strong>{s.name}</strong>
													<div className="mute small">{modelName(c.model(s.model?.id))}</div>
												</td>
												<td>{c.path(s.space?.id) || '-'}</td>
												<td className={`num ${low ? 'bad' : ''}`}>
													{String(s.quantity)} {s.unit}
												</td>
												<td className="num mute">{s.threshold > 0n ? String(s.threshold) : '-'}</td>
												<td className="row-actions">
													<button className="small" onClick={() => setOpen({ s, mode: 'consume' })}>
														사용
													</button>
													{mgr && (
														<>
															<button className="small" onClick={() => setOpen({ s, mode: 'receive' })}>
																입고
															</button>
															<button className="small" onClick={() => setOpen({ s, mode: 'adjust' })}>
																조정
															</button>
															<button className="small" onClick={() => setOpen({ s, mode: 'transfer' })}>
																이동
															</button>
															<button className="small" onClick={() => setOpen({ s, mode: 'convert' })}>
																자산으로
															</button>
														</>
													)}
													<button className="small link" onClick={() => setOpen({ s, mode: 'history' })}>
														내역
													</button>
												</td>
											</tr>
										)
									})}
								</tbody>
							</table>
						</div>
					)
				}
			</Load>
			{adding && <NewStock onClose={() => setAdding(false)} />}
			{open !== undefined && open.mode === 'history' && <History s={open.s} onClose={() => setOpen(undefined)} />}
			{open !== undefined && open.mode !== 'history' && <Change s={open.s} mode={open.mode} onClose={() => setOpen(undefined)} />}
		</>
	)
}

const titles: Record<Exclude<Mode, 'history'>, string> = {
	receive: '입고',
	consume: '사용',
	adjust: '수량 조정',
	transfer: '다른 곳으로 이동',
	convert: '자산으로 전환',
}

function Change(props: { s: Stock; mode: Exclude<Mode, 'history'>; onClose: () => void }): ReactNode {
	const act = useAct()
	const [qty, setQty] = useState(props.mode === 'adjust' ? '' : '1')
	const [reason, setReason] = useState('')
	const [to, setTo] = useState('')
	const [prefix, setPrefix] = useState('')
	const [key] = useState(op)
	const s = props.s

	return (
		<FormModal
			title={`${s.name} — ${titles[props.mode]}`}
			submit={titles[props.mode]}
			onClose={props.onClose}
			onSubmit={async () => {
				const quantity = BigInt(qty || '0')
				const r = { ref: ref(s.id), quantity, reason, op: key }
				switch (props.mode) {
					case 'receive':
						return (await act(StockService.method.receive, r, { ok: '입고했습니다.' })) !== undefined
					case 'consume':
						return (await act(StockService.method.consume, r, { ok: '사용 처리했습니다.' })) !== undefined
					case 'adjust':
						return (await act(StockService.method.adjust, r, { ok: '조정했습니다.' })) !== undefined
					case 'transfer':
						return (await act(StockService.method.transfer, { ...r, to: ref(idBytes(to)) }, { ok: '옮겼습니다.' })) !== undefined
					case 'convert':
						return (
							(await act(StockService.method.convert, { ref: ref(s.id), quantity, tagPrefix: prefix, op: key }, { ok: '자산으로 등록했습니다.' })) !==
							undefined
						)
				}
			}}
		>
			<p className="wide mute">
				지금 {String(s.quantity)}
				{s.unit}
			</p>
			<Field label={props.mode === 'adjust' ? '늘리거나 줄일 수량 (예: -2)' : '수량'}>
				<input type="number" value={qty} min={props.mode === 'adjust' ? undefined : 1} onChange={(e) => setQty(e.target.value)} required autoFocus />
			</Field>
			{props.mode === 'transfer' && (
				<Field label="어디로">
					<SpaceSelect value={to} onChange={setTo} required />
				</Field>
			)}
			{props.mode === 'convert' && (
				<Field label="태그 앞부분" hint="예: MS → MS-001, MS-002 …  비우면 자동">
					<input value={prefix} onChange={(e) => setPrefix(e.target.value)} />
				</Field>
			)}
			{props.mode !== 'convert' && (
				<Field label="사유" wide>
					<input value={reason} onChange={(e) => setReason(e.target.value)} placeholder={props.mode === 'adjust' ? '실사 결과 등' : ''} />
				</Field>
			)}
		</FormModal>
	)
}

/**
 * What happened to a stock, newest first or in the order it happened. Either
 * way is the list read by the server, which walks the same index from either
 * end, so the oldest are the oldest there are and not the oldest of the newest
 * page.
 */
function History(props: { s: Stock; onClose: () => void }): ReactNode {
	const [order, setOrder] = useState<Order>('newest')
	const ms = useRpc(StockMovementService.method.list, { filters: [{ stock: ref(props.s.id) }], size: 200, reverse: order === 'oldest' })
	return (
		<Modal title={`${props.s.name} 내역`} onClose={props.onClose} wide>
			<div className="filters">
				<OrderSelect value={order} onChange={setOrder} />
			</div>
			<Load q={ms}>
				{(d) =>
					d.items.length === 0 ? (
						<Empty />
					) : (
						<table className="table">
							<thead>
								<tr>
									<th>언제</th>
									<th>무엇</th>
									<th className="num">변화</th>
									<th className="num">남은 수량</th>
								</tr>
							</thead>
							<tbody>
								{d.items.map((m) => (
									<tr key={idStr(m.id)}>
										<td>{fmt(m.occurredAt)}</td>
										<td>{movementWord[m.reason] ?? m.reason}</td>
										<td className={`num ${m.delta < 0n ? 'bad' : 'ok'}`}>{m.delta > 0n ? `+${m.delta}` : String(m.delta)}</td>
										<td className="num">{String(m.balance)}</td>
									</tr>
								))}
							</tbody>
						</table>
					)
				}
			</Load>
		</Modal>
	)
}

function NewStock(props: { onClose: () => void }): ReactNode {
	const act = useAct()
	const [model, setModel] = useState('')
	const [space, setSpace] = useState('')
	const [qty, setQty] = useState('0')
	const [threshold, setThreshold] = useState('0')
	const [unit, setUnit] = useState('개')
	const [name, setName] = useState('')
	return (
		<FormModal
			title="재고 품목"
			onClose={props.onClose}
			onSubmit={async () =>
				(await act(
					StockService.method.add,
					{
						model: ref(idBytes(model)),
						space: ref(idBytes(space)),
						quantity: BigInt(qty || '0'),
						threshold: BigInt(threshold || '0'),
						unit,
						name,
					},
					{ ok: '만들었습니다.' },
				)) !== undefined
			}
		>
			<Field label="모델 *" hint="없으면 설정 › 모델에서 먼저 만드세요">
				<ModelSelect value={model} onChange={setModel} empty="선택" />
			</Field>
			<Field label="보관 장소 *">
				<SpaceSelect value={space} onChange={setSpace} required />
			</Field>
			<Field label="이름" hint="비우면 모델 이름">
				<input value={name} onChange={(e) => setName(e.target.value)} />
			</Field>
			<Field label="지금 수량">
				<input type="number" min={0} value={qty} onChange={(e) => setQty(e.target.value)} />
			</Field>
			<Field label="부족 기준">
				<input type="number" min={0} value={threshold} onChange={(e) => setThreshold(e.target.value)} />
			</Field>
			<Field label="단위">
				<input value={unit} onChange={(e) => setUnit(e.target.value)} />
			</Field>
		</FormModal>
	)
}
