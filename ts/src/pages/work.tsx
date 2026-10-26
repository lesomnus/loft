import { useState, type ReactNode } from 'react'
import { useSearchParams } from 'react-router-dom'

import type { Asset } from '../../gen/rove/asset_pb.js'
import type { WorkOrder } from '../../gen/rove/work_pb.js'
import { AssetService, WorkOrderService, amountIn, amountStep, amountText, dateOf, fmt, fromInput, idBytes, idStr, localInput, money, place, ref, ts, unitOf } from '../api.js'
import { useCatalog } from '../catalog.js'
import { AssetLink, AssetPicker, PartySelect } from '../pickers.js'
import { useAct, useRpc } from '../rpc.js'
import { Badge, Empty, Field, FormModal, Kv, Load, Modal, PageHead, Select } from '../ui.js'
import { conditionWord, workKindWord, workStatusWord } from '../words.js'

export function Work(): ReactNode {
	const c = useCatalog()
	const [sp, setSp] = useSearchParams()
	const status = sp.get('status') ?? 'active'
	const [adding, setAdding] = useState(false)
	const [open, setOpen] = useState<WorkOrder>()
	const list = useRpc(WorkOrderService.method.list, { filters: status === 'active' || status === '' ? [] : [{ status }], size: 500 })

	const rows = (list.data?.items ?? [])
		.filter((w) => status !== 'active' || ['open', 'scheduled', 'in_progress'].includes(w.status))
		.sort((a, b) => (dateOf(a.beginsAt ?? a.dateCreated)?.getTime() ?? 0) - (dateOf(b.beginsAt ?? b.dateCreated)?.getTime() ?? 0))

	return (
		<>
			<PageHead
				title="작업"
				sub="수리, 점검, 정비. 막아 두기를 켠 작업은 그 시간 동안 예약을 받지 않습니다."
				actions={<button className="primary" onClick={() => setAdding(true)}>{c.can('manager') ? '+ 작업 등록' : '+ 고장 신고'}</button>}
			/>
			<div className="filters">
				<Select
					value={status}
					onChange={(v) => setSp({ status: v })}
					options={[
						{ value: 'active', label: '진행 중인 것' },
						...Object.entries(workStatusWord).map(([value, label]) => ({ value, label })),
						{ value: '', label: '전체' },
					]}
				/>
			</div>
			<Load q={list}>
				{() =>
					rows.length === 0 ? (
						<Empty>작업이 없습니다.</Empty>
					) : (
						<div className="table-wrap">
							<table className="table">
								<thead>
									<tr>
										<th>구분</th>
										<th>작업</th>
										<th>자산</th>
										<th>일정</th>
										<th>비용</th>
										<th>상태</th>
									</tr>
								</thead>
								<tbody>
									{rows.map((w) => (
										<tr key={idStr(w.id)} className="link" onClick={() => setOpen(w)}>
											<td>
												<span className="chip">{workKindWord[w.kind] ?? w.kind}</span>
											</td>
											<td>
												{w.name}
												{w.everyDays > 0 && <span className="mute"> · {w.everyDays}일마다</span>}
												{w.blocking && <span className="mute"> · 예약 막음</span>}
											</td>
											<td onClick={(e) => e.stopPropagation()}>
												<WorkAsset id={w.asset?.id} />
											</td>
											<td>{w.beginsAt !== undefined ? fmt(w.beginsAt) : <span className="mute">접수 {fmt(w.dateCreated, 'date')}</span>}</td>
											<td>{w.cost > 0n ? money(w.cost, w.currency) : '-'}</td>
											<td>
												<Badge v={w.status} words={workStatusWord} />
											</td>
										</tr>
									))}
								</tbody>
							</table>
						</div>
					)
				}
			</Load>
			{adding && <NewWorkOrder onClose={() => setAdding(false)} />}
			{open !== undefined && <WorkDialog w={open} onClose={() => setOpen(undefined)} />}
		</>
	)
}

function WorkAsset(props: { id: Uint8Array | undefined }): ReactNode {
	const a = useRpc(AssetService.method.get, props.id === undefined ? null : { ref: ref(props.id) })
	return <AssetLink a={a.data} />
}

export function NewWorkOrder(props: { asset?: Asset; onClose: () => void }): ReactNode {
	const c = useCatalog()
	const act = useAct()
	const mgr = c.can('manager')
	const [asset, setAsset] = useState<Asset | undefined>(props.asset)
	const [kind, setKind] = useState('repair')
	const [name, setName] = useState('')
	const [desc, setDesc] = useState('')
	const [begins, setBegins] = useState('')
	const [ends, setEnds] = useState('')
	const [blocking, setBlocking] = useState(false)
	const [every, setEvery] = useState('')
	const [vendor, setVendor] = useState('')
	const [cost, setCost] = useState('')

	return (
		<FormModal
			title={mgr ? '작업 등록' : '고장 신고'}
			submit={mgr ? '등록' : '신고'}
			wide
			onClose={props.onClose}
			onSubmit={async () => {
				if (asset === undefined) return false
				return (
					(await act(
						WorkOrderService.method.add,
						{
							asset: ref(asset.id),
							kind,
							name,
							desc,
							beginsAt: begins === '' ? undefined : ts(fromInput(begins)),
							endsAt: ends === '' ? undefined : ts(fromInput(ends)),
							blocking,
							everyDays: Number(every || 0),
							vendor: vendor === '' ? undefined : ref(idBytes(vendor)),
							cost: amountIn(cost) ?? 0n,
							// What the cost was typed in, as the form said it.
							currency: place.currency,
						},
						{ ok: mgr ? '등록했습니다.' : '신고했습니다. 담당자에게 전달됩니다.' },
					)) !== undefined
				)
			}}
		>
			<Field label="자산 *" wide group>
				<AssetPicker value={asset} onChange={setAsset} />
			</Field>
			{mgr && (
				<Field label="구분">
					<Select value={kind} onChange={setKind} options={Object.entries(workKindWord).map(([value, label]) => ({ value, label }))} />
				</Field>
			)}
			<Field label={mgr ? '작업 *' : '증상 *'} wide>
				<input value={name} onChange={(e) => setName(e.target.value)} required placeholder={mgr ? '예: 배터리 교체' : '예: 전원이 켜지지 않음'} />
			</Field>
			<Field label="자세히" wide>
				<textarea value={desc} onChange={(e) => setDesc(e.target.value)} rows={3} />
			</Field>
			{mgr && (
				<>
					<Field label="시작">
						<input type="datetime-local" value={begins} onChange={(e) => setBegins(e.target.value)} />
					</Field>
					<Field label="끝">
						<input type="datetime-local" value={ends} onChange={(e) => setEnds(e.target.value)} />
					</Field>
					<label className="check">
						<input type="checkbox" checked={blocking} onChange={(e) => setBlocking(e.target.checked)} /> 이 시간 동안 예약 막기
					</label>
					<Field label="반복 (일)" hint="정기 점검: 끝내면 다음 회차가 열립니다">
						<input type="number" min={0} value={every} onChange={(e) => setEvery(e.target.value)} />
					</Field>
					<Field label="업체">
						<PartySelect value={vendor} onChange={setVendor} kinds={['vendor']} empty="(없음)" />
					</Field>
					<Field label={`예상 비용 (${unitOf()})`}>
						<input type="number" min={0} step={amountStep()} value={cost} onChange={(e) => setCost(e.target.value)} />
					</Field>
				</>
			)}
		</FormModal>
	)
}

function WorkDialog(props: { w: WorkOrder; onClose: () => void }): ReactNode {
	const c = useCatalog()
	const act = useAct()
	const w = props.w
	const [mode, setMode] = useState<'view' | 'edit' | 'done' | 'cancel'>('view')
	const [name, setName] = useState(w.name)
	const [desc, setDesc] = useState(w.desc)
	const [status, setStatus] = useState(w.status)
	const [begins, setBegins] = useState(localInput(dateOf(w.beginsAt)))
	const [ends, setEnds] = useState(localInput(dateOf(w.endsAt)))
	const [blocking, setBlocking] = useState(w.blocking)
	const [cost, setCost] = useState(amountText(w.cost, w.currency))
	const [condition, setCondition] = useState('')
	const [reason, setReason] = useState('')
	const live = ['open', 'scheduled', 'in_progress'].includes(w.status)

	if (mode === 'view') {
		return (
			<Modal title={w.name} onClose={props.onClose}>
				<Kv
					items={[
						['구분', workKindWord[w.kind] ?? w.kind],
						['상태', <Badge key="s" v={w.status} words={workStatusWord} />],
						['자산', <WorkAsset key="a" id={w.asset?.id} />],
						['일정', w.beginsAt !== undefined ? `${fmt(w.beginsAt)}${w.endsAt !== undefined ? ` – ${fmt(w.endsAt)}` : ''}` : '-'],
						['예약 막음', w.blocking ? '예' : '아니오'],
						['반복', w.everyDays > 0 ? `${w.everyDays}일마다` : '-'],
						['업체', c.called(w.vendor?.id) || '-'],
						['비용', w.cost > 0n ? money(w.cost, w.currency) : '-'],
						['완료', fmt(w.completedAt)],
						['접수', fmt(w.dateCreated)],
					]}
				/>
				{w.desc !== '' && <p className="desc">{w.desc}</p>}
				{live && c.can('manager') && (
					<footer className="modal-actions">
						<button onClick={() => setMode('cancel')}>취소</button>
						<button onClick={() => setMode('edit')}>수정</button>
						<button className="primary" onClick={() => setMode('done')}>
							완료
						</button>
					</footer>
				)}
			</Modal>
		)
	}
	if (mode === 'edit') {
		return (
			<FormModal
				title="작업 수정"
				wide
				onClose={props.onClose}
				onSubmit={async () =>
					(await act(
						WorkOrderService.method.update,
						{
							ref: ref(w.id),
							name,
							desc,
							status: status === w.status ? '' : status,
							beginsAt: begins === '' ? undefined : ts(fromInput(begins)),
							endsAt: ends === '' ? undefined : ts(fromInput(ends)),
							blocking,
							cost: amountIn(cost, w.currency) ?? 0n,
						},
						{ ok: '수정했습니다.' },
					)) !== undefined
				}
			>
				<Field label="작업" wide>
					<input value={name} onChange={(e) => setName(e.target.value)} />
				</Field>
				<Field label="상태">
					<Select
						value={status}
						onChange={setStatus}
						options={['open', 'scheduled', 'in_progress'].map((value) => ({ value, label: workStatusWord[value] ?? value }))}
					/>
				</Field>
				<Field label="시작">
					<input type="datetime-local" value={begins} onChange={(e) => setBegins(e.target.value)} />
				</Field>
				<Field label="끝">
					<input type="datetime-local" value={ends} onChange={(e) => setEnds(e.target.value)} />
				</Field>
				<label className="check">
					<input type="checkbox" checked={blocking} onChange={(e) => setBlocking(e.target.checked)} /> 예약 막기
				</label>
				<Field label={`비용 (${unitOf(w.currency)})`}>
					<input type="number" min={0} step={amountStep(w.currency)} value={cost} onChange={(e) => setCost(e.target.value)} />
				</Field>
				<Field label="자세히" wide>
					<textarea value={desc} onChange={(e) => setDesc(e.target.value)} rows={3} />
				</Field>
				<p className="wide mute small">수리를 '진행 중'으로 바꾸면 자산 상태가 수리중이 됩니다.</p>
			</FormModal>
		)
	}
	return (
		<FormModal
			title={mode === 'done' ? '작업 완료' : '작업 취소'}
			submit={mode === 'done' ? '완료' : '작업 취소'}
			danger={mode === 'cancel'}
			onClose={props.onClose}
			onSubmit={async () =>
				(await act(
					mode === 'done' ? WorkOrderService.method.complete : WorkOrderService.method.cancel,
					{ ref: ref(w.id), reason, cost: amountIn(cost, w.currency) ?? 0n, condition },
					{ ok: mode === 'done' ? '완료했습니다.' : '취소했습니다.' },
				)) !== undefined
			}
		>
			{mode === 'done' && (
				<>
					<Field label={`최종 비용 (${unitOf(w.currency)})`}>
						<input type="number" min={0} step={amountStep(w.currency)} value={cost} onChange={(e) => setCost(e.target.value)} />
					</Field>
					<Field label="작업 후 컨디션">
						<Select value={condition} onChange={setCondition} empty="바꾸지 않음" options={Object.entries(conditionWord).map(([value, label]) => ({ value, label }))} />
					</Field>
				</>
			)}
			<Field label="메모" wide>
				<input value={reason} onChange={(e) => setReason(e.target.value)} />
			</Field>
		</FormModal>
	)
}
