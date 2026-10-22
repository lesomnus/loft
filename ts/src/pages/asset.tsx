import { useState, type ReactNode } from 'react'
import { Link, useNavigate, useParams } from 'react-router-dom'

import type { Asset } from '../../gen/rove/asset_pb.js'
import type { TimelineEntry } from '../../gen/rove/asset_svc_pb.js'
import {
	AssetService,
	AttachmentService,
	BookableService,
	CustodyLineService,
	CustodyService,
	LabelService,
	ReservationService,
	WorkOrderService,
	dateOf,
	fmt,
	fromInput,
	idBytes,
	idStr,
	localInput,
	minute,
	raw,
	readFile,
	ref,
	sameId,
	ts,
} from '../api.js'
import { modelName, useCatalog } from '../catalog.js'
import { AssetLink, AssetPicker, PartySelect, SpaceSelect, Where } from '../pickers.js'
import { Live, useAct, useRpc } from '../rpc.js'
import { Badge, Card, Confirm, Empty, Field, FormModal, Kv, Load, PageHead, Select, Spinner, Tabs, useToast } from '../ui.js'
import {
	conditionWord,
	kindWord,
	labelWord,
	modeWord,
	reservationWord,
	statusWord,
	workKindWord,
	workStatusWord,
} from '../words.js'
import { plugins } from '../plugins.js'
import { AttrFields } from './assets.js'
import { NewCustody } from './custody.js'
import { NewWorkOrder } from './work.js'

export function AssetPage(): ReactNode {
	const { id = '' } = useParams()
	let bytes: Uint8Array | undefined
	try {
		bytes = idBytes(id)
	} catch {
		bytes = undefined
	}
	if (bytes === undefined) return <Empty>잘못된 주소입니다.</Empty>
	// Whatever somebody else does to it -- a move, a hand-over, a status, what
	// is put in it -- is written to its row, so the page follows the row.
	return (
		<Live watch={AssetService.method.watch} ids={[bytes]}>
			<AssetRead id={bytes} />
		</Live>
	)
}

function AssetRead(props: { id: Uint8Array }): ReactNode {
	const a = useRpc(AssetService.method.get, { ref: ref(props.id) })
	return <Load q={a}>{(v) => <AssetView a={v} />}</Load>
}

type Dialog = 'move' | 'set' | 'assign' | 'relate' | 'custody' | 'work' | 'bookable' | 'bind' | 'upload' | undefined

function AssetView(props: { a: Asset }): ReactNode {
	const a = props.a
	const c = useCatalog()
	const act = useAct()
	const go = useNavigate()
	const [tab, setTab] = useState('timeline')
	const [dialog, setDialog] = useState<Dialog>()
	const ty = c.type(a.type?.id)
	const mgr = c.can('manager')
	const bookable = c.bookable(a.id)
	const holds = a.kind === 'space' || a.kind === 'kit' || a.kind === 'group'

	const pp = { a, type: ty, model: c.model(a.model?.id), manage: mgr }
	const extra = plugins.filter((p) => p.matches(pp))
	const tabs = [
		{ key: 'timeline', label: '이력' },
		...extra.filter((p) => p.slot === 'tab').map((p) => ({ key: `plugin:${p.id}`, label: p.title })),
		...(holds ? [{ key: 'inside', label: a.kind === 'space' ? '안에 있는 것' : '구성' }] : []),
		...(bookable !== undefined ? [{ key: 'bookings', label: '예약' }] : []),
		{ key: 'work', label: '작업' },
		{ key: 'files', label: '첨부' },
		{ key: 'labels', label: '라벨' },
	]

	return (
		<>
			<PageHead
				title={
					<>
						<code className="tag">{a.tag}</code> {a.name}
					</>
				}
				sub={
					<>
						<span className="chip">{kindWord[a.kind]}</span>
						<Badge v={a.status} words={statusWord} /> <Badge v={a.condition} words={conditionWord} />
						{ty !== undefined && <span className="mute"> · {ty.name}</span>}
						{a.model !== undefined && <span className="mute"> · {modelName(c.model(a.model.id))}</span>}
					</>
				}
				actions={
					<>
						<Acknowledge a={a} />
						{mgr && <button onClick={() => setDialog('move')}>이동</button>}
						{mgr && <button onClick={() => setDialog('set')}>정보·상태 변경</button>}
						{mgr && a.kind === 'item' && a.custodian === undefined && (
							<button onClick={() => setDialog('custody')}>지급·대여</button>
						)}
						<button onClick={() => setDialog('work')}>{mgr ? '작업 등록' : '고장 신고'}</button>
						{bookable !== undefined && (
							<button className="primary" onClick={() => go(`/reservations?resource=${idStr(a.id)}`)}>
								예약
							</button>
						)}
					</>
				}
			/>

			<div className="detail">
				<Card title="정보">
					<Kv
						items={[
							['위치', <Where key="w" a={a} />],
							...(a.placementMode !== '' && a.placementMode !== 'located'
								? ([['배치', `${modeWord[a.placementMode] ?? a.placementMode}${a.slot ? ` · ${a.slot}` : ''}`]] as [ReactNode, ReactNode][])
								: []),
							['사용자', a.custodian !== undefined ? c.party(a.custodian.id)?.name ?? '-' : '-'],
							...(a.serial !== '' ? ([['시리얼', <code key="s">{a.serial}</code>]] as [ReactNode, ReactNode][]) : []),
							['취득일', fmt(a.acquiredAt, 'date')],
							['등록', fmt(a.dateCreated, 'date')],
							...(ty?.spec?.attributes ?? []).map(
								(d): [ReactNode, ReactNode] => [d.label || d.key, a.attributes[d.key] !== undefined ? `${a.attributes[d.key]}${d.unit ? ` ${d.unit}` : ''}` : '-'],
							),
							...Object.entries(a.attributes)
								.filter(([k]) => !(ty?.spec?.attributes ?? []).some((d) => d.key === k))
								.map(([k, v]): [ReactNode, ReactNode] => [k, v]),
						]}
					/>
					{a.desc !== '' && <p className="desc">{a.desc}</p>}
					<Stewards a={a} />
					{extra
						.filter((p) => p.slot === 'panel')
						.map((p) => (
							<p.View key={p.id} {...pp} />
						))}
					{mgr && (
						<div className="row-actions">
							<button className="small" onClick={() => setDialog('assign')}>
								소유·관리 담당
							</button>
							<button className="small" onClick={() => setDialog('relate')}>
								키트·그룹 연결
							</button>
							{c.can('admin') && bookable === undefined && a.kind !== 'group' && (
								<button className="small" onClick={() => setDialog('bookable')}>
									예약 가능하게
								</button>
							)}
							<Link className="button small" to={`/labels/print?assets=${idStr(a.id)}`}>
								라벨 인쇄
							</Link>
							<button className="small" onClick={() => setDialog('bind')}>
								라벨 연결
							</button>
							<Confirm
								className="small danger"
								label="삭제"
								danger
								question={`${a.tag} ${a.name}을(를) 삭제합니다. 이력은 남지만 목록에서 사라집니다. 안에 든 것이 있으면 삭제할 수 없습니다.`}
								onYes={async () => {
									const v = await act(AssetService.method.erase, ref(a.id), { ok: '삭제했습니다.' })
									if (v !== undefined) go('/assets')
								}}
							/>
						</div>
					)}
				</Card>

				<div className="detail-main">
					<Tabs tabs={tabs} at={tab} onChange={setTab} />
					{tab === 'timeline' && <Timeline a={a} />}
					{tab === 'inside' && <Inside a={a} />}
					{tab === 'bookings' && bookable !== undefined && (
						<Live watch={BookableService.method.watch} ids={[bookable.id]}>
							<Bookings a={a} />
						</Live>
					)}
					{tab === 'work' && <WorkTab a={a} />}
					{tab === 'files' && <Files a={a} onUpload={() => setDialog('upload')} />}
					{tab === 'labels' && <Labels a={a} />}
					{extra
						.filter((p) => p.slot === 'tab' && tab === `plugin:${p.id}`)
						.map((p) => (
							<p.View key={p.id} {...pp} />
						))}
				</div>
			</div>

			{dialog === 'move' && <Move a={a} onClose={() => setDialog(undefined)} />}
			{dialog === 'set' && <SetAttrs a={a} onClose={() => setDialog(undefined)} />}
			{dialog === 'assign' && <Assign a={a} onClose={() => setDialog(undefined)} />}
			{dialog === 'relate' && <Relate a={a} onClose={() => setDialog(undefined)} />}
			{dialog === 'custody' && <NewCustody assets={[a]} onClose={() => setDialog(undefined)} />}
			{dialog === 'work' && <NewWorkOrder asset={a} onClose={() => setDialog(undefined)} />}
			{dialog === 'bookable' && <MakeBookable a={a} onClose={() => setDialog(undefined)} />}
			{dialog === 'bind' && <Bind a={a} onClose={() => setDialog(undefined)} />}
			{dialog === 'upload' && <Upload a={a} onClose={() => setDialog(undefined)} />}
		</>
	)
}

/**
 * The person something was handed to says it arrived, here: where scanning it
 * brings them (plan.md M4, 스캔 → 인수 확인). The hand-over's own page and 내
 * 정보 offer the same; this is the one a phone's camera opens.
 *
 * Only to that person, and only while it waits: what it was handed over in is
 * the line of a hand-over still out with this asset on it.
 */
function Acknowledge(props: { a: Asset }): ReactNode {
	const c = useCatalog()
	const act = useAct()
	const mine = props.a.custodian !== undefined && sameId(props.a.custodian.id, c.me.party?.id)
	const lines = useRpc(CustodyLineService.method.list, mine ? { filters: [{ asset: ref(props.a.id) }], size: 50 } : null)
	const out = mine ? lines.data?.items.find((l) => l.returnedAt === undefined && l.custody !== undefined) : undefined
	const held = useRpc(CustodyService.method.get, out?.custody !== undefined ? { ref: ref(out.custody.id) } : null)
	const x = held.data
	if (out === undefined || x === undefined || !sameId(x.id, out.custody?.id) || x.status !== 'open' || x.acknowledgedAt !== undefined) return null
	return (
		<button className="primary" onClick={() => void act(CustodyService.method.acknowledge, { ref: ref(x.id) }, { ok: '인수를 확인했습니다.' })}>
			인수 확인
		</button>
	)
}

/** Owner and manager are stewardships, read off the timeline's open rows. */
function Stewards(props: { a: Asset }): ReactNode {
	const t = useRpc(AssetService.method.timeline, { ref: ref(props.a.id) })
	const now = Date.now()
	const open = (t.data?.entries ?? []).filter(
		(e) =>
			e.kind === 'stewardship' &&
			e.supersededAt === undefined &&
			(dateOf(e.validFrom)?.getTime() ?? 0) <= now &&
			(e.validTo === undefined || (dateOf(e.validTo)?.getTime() ?? 0) > now) &&
			e.detail['role'] !== 'custodian',
	)
	if (open.length === 0) return null
	return (
		<Kv
			items={open.map((e): [ReactNode, ReactNode] => [e.detail['role'] === 'owner' ? '소유' : '관리 담당', e.otherName])}
		/>
	)
}

const kindIcon: Record<string, string> = { placement: '⌂', fact: '✎', stewardship: '☺', link: '⛓', event: '•' }

function Timeline(props: { a: Asset }): ReactNode {
	const [superseded, setSuperseded] = useState(false)
	const [known, setKnown] = useState('')
	const [correct, setCorrect] = useState<TimelineEntry>()
	const c = useCatalog()
	const t = useRpc(AssetService.method.timeline, {
		ref: ref(props.a.id),
		superseded,
		known: known === '' ? undefined : ts(fromInput(known)),
	})

	return (
		<Card
			title="이력"
			actions={
				<div className="inline">
					<label className="check">
						<input type="checkbox" checked={superseded} onChange={(e) => setSuperseded(e.target.checked)} /> 정정된 기록도
					</label>
					<label className="inline" title="그때 시스템이 알던 대로 봅니다">
						기록 시점
						<input type="datetime-local" value={known} onChange={(e) => setKnown(e.target.value)} />
					</label>
				</div>
			}
		>
			<p className="mute small">
				왼쪽은 실제로 그랬던 시점, 오른쪽 아래는 기록한 시점입니다. 늦게 알게 된 일도 그 시점으로 기록하고, 잘못된 기록은 지우지 않고 정정합니다.
			</p>
			<Load q={t}>
				{(d) =>
					d.entries.length === 0 ? (
						<Empty />
					) : (
						<ol className="timeline">
							{d.entries.map((e, i) => {
								const gone = e.supersededAt !== undefined
								return (
									<li key={`${idStr(e.rowId)}${i}`} className={`${e.kind} ${gone ? 'gone' : ''}`}>
										<span className="when">
											{fmt(e.validFrom)}
											{e.validTo !== undefined && <span className="mute"> → {fmt(e.validTo)}</span>}
										</span>
										<span className="what">
											<span className="icon">{kindIcon[e.kind]}</span>
											{e.summary}
											{e.reason !== '' && <span className="reason"> — {e.reason}</span>}
											{e.cause !== '' && <span className="mute small"> ({e.cause})</span>}
										</span>
										<span className="who mute small">
											{e.actor !== '' && `${e.actor} · `}기록 {fmt(e.recordedAt)}
											{gone && ` · 정정됨 ${fmt(e.supersededAt)}`}
										</span>
										{c.can('manager') && !gone && e.kind !== 'event' && (
											<button className="link small" onClick={() => setCorrect(e)}>
												정정
											</button>
										)}
									</li>
								)
							})}
						</ol>
					)
				}
			</Load>
			{correct !== undefined && <Correct a={props.a} e={correct} onClose={() => setCorrect(undefined)} />}
		</Card>
	)
}

function Correct(props: { a: Asset; e: TimelineEntry; onClose: () => void }): ReactNode {
	const act = useAct()
	const [how, setHow] = useState('from')
	const [from, setFrom] = useState(localInput(dateOf(props.e.validFrom)))
	const [reason, setReason] = useState('')
	return (
		<FormModal
			title="기록 정정"
			submit="정정"
			onClose={props.onClose}
			onSubmit={async () =>
				(await act(
					AssetService.method.correct,
					{
						ref: ref(props.a.id),
						rowId: props.e.rowId,
						retract: how === 'retract',
						validFrom: how === 'from' ? ts(fromInput(from)) : undefined,
						reason,
					},
					{ ok: '정정했습니다.' },
				)) !== undefined
			}
		>
			<p className="wide">
				<strong>{props.e.summary}</strong> ({fmt(props.e.validFrom)}부터)
			</p>
			<Field label="어떻게">
				<Select
					value={how}
					onChange={setHow}
					options={[
						{ value: 'from', label: '실제 시점이 달랐다' },
						{ value: 'retract', label: '없었던 일이다 (취소)' },
					]}
				/>
			</Field>
			{how === 'from' && (
				<Field label="실제 시점">
					<input type="datetime-local" value={from} onChange={(e) => setFrom(e.target.value)} required />
				</Field>
			)}
			<Field label="사유 *" wide>
				<input value={reason} onChange={(e) => setReason(e.target.value)} required />
			</Field>
		</FormModal>
	)
}

function Inside(props: { a: Asset }): ReactNode {
	const kids = useRpc(AssetService.method.list, { filters: [{ parentId: props.a.id }], size: 500 })
	const all = useRpc(AssetService.method.search, { within: ref(props.a.id), size: 1 })
	const c = useCatalog()
	return (
		<Card title={`바로 안에 있는 것`} actions={<Link to={`/assets?within=${idStr(props.a.id)}`}>하위 전체 {all.data?.total ?? ''}개 보기</Link>}>
			<Load q={kids}>
				{(d) =>
					d.items.length === 0 ? (
						<Empty>비어 있습니다.</Empty>
					) : (
						<ul className="rows">
							{d.items.map((x) => (
								<li key={idStr(x.id)}>
									<AssetLink a={x} />
									<span className="mute">
										{x.kind !== 'item' && `${kindWord[x.kind]} · `}
										{modeWord[x.placementMode] ?? ''}
										{x.slot !== '' && ` · ${x.slot}`}
										{x.custodian !== undefined && ` · ${c.party(x.custodian.id)?.name ?? ''}`}
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

function Bookings(props: { a: Asset }): ReactNode {
	const now = minute()
	const cal = useRpc(ReservationService.method.calendar, {
		resources: [ref(props.a.id)],
		from: ts(now),
		to: ts(new Date(now.getTime() + 60 * 86400_000)),
	})
	return (
		<Card title="다가오는 예약 (60일)" actions={<Link to={`/reservations?resource=${idStr(props.a.id)}`}>예약 화면</Link>}>
			<Load q={cal}>
				{(d) =>
					d.entries.length === 0 ? (
						<Empty>예약이 없습니다.</Empty>
					) : (
						<ul className="rows">
							{d.entries.map((e) => (
								<li key={idStr(e.reservation?.id)}>
									<span>
										{fmt(e.reservation?.beginsAt)} – {fmt(e.reservation?.endsAt, 'time')} · {e.reservation?.name}
									</span>
									<span>
										<span className="mute">{e.partyName} </span>
										<Badge v={e.reservation?.status ?? ''} words={reservationWord} />
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

function WorkTab(props: { a: Asset }): ReactNode {
	const ws = useRpc(WorkOrderService.method.list, { filters: [{ asset: ref(props.a.id) }], size: 100 })
	return (
		<Card title="작업" actions={<Link to="/work">작업 목록</Link>}>
			<Load q={ws}>
				{(d) =>
					d.items.length === 0 ? (
						<Empty>작업 기록이 없습니다.</Empty>
					) : (
						<ul className="rows">
							{d.items.map((w) => (
								<li key={idStr(w.id)}>
									<span>
										<span className="chip">{workKindWord[w.kind]}</span> {w.name}
										<span className="mute"> · {fmt(w.beginsAt ?? w.dateCreated, 'date')}</span>
									</span>
									<Badge v={w.status} words={workStatusWord} />
								</li>
							))}
						</ul>
					)
				}
			</Load>
		</Card>
	)
}

function Files(props: { a: Asset; onUpload: () => void }): ReactNode {
	const fs = useRpc(AttachmentService.method.list, { filters: [{ subjectId: props.a.id }], size: 100 })
	const toast = useToast()
	const open = async (id: Uint8Array) => {
		try {
			const v = await raw.attachment.url({ ref: ref(id) })
			window.open(v.url, '_blank', 'noopener')
		} catch (err) {
			toast(String(err), 'bad')
		}
	}
	return (
		<Card title="첨부" actions={<button className="small" onClick={props.onUpload}>+ 올리기</button>}>
			<Load q={fs}>
				{(d) =>
					d.items.length === 0 ? (
						<Empty>사진, 영수증, 보증서를 올려 두세요.</Empty>
					) : (
						<ul className="rows">
							{d.items.map((f) => (
								<li key={idStr(f.id)}>
									<button className="link" onClick={() => void open(f.id)}>
										{f.name}
									</button>
									<span className="mute">
										{(Number(f.sizeBytes) / 1024).toFixed(0)} KB · {fmt(f.dateCreated, 'date')}
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

function Labels(props: { a: Asset }): ReactNode {
	const ls = useRpc(LabelService.method.list, { filters: [{ subjectId: props.a.id }], size: 50 })
	const act = useAct()
	return (
		<Card title="라벨" actions={<Link to={`/labels/print?assets=${idStr(props.a.id)}`}>인쇄</Link>}>
			<p className="mute small">
				라벨이 없어도 태그 <code>{props.a.tag}</code> 로 찾을 수 있습니다. QR 라벨을 붙이면 휴대폰으로 스캔해 바로 열립니다.
			</p>
			<Load q={ls}>
				{(d) =>
					d.items.length === 0 ? (
						<Empty>연결된 라벨이 없습니다.</Empty>
					) : (
						<ul className="rows">
							{d.items.map((l) => (
								<li key={idStr(l.id)}>
									<span>
										<code>{idStr(l.id).slice(0, 13)}…</code> <Badge v={l.state} words={labelWord} />
										<span className="mute"> {fmt(l.boundAt ?? l.dateCreated, 'date')}</span>
									</span>
									{l.state === 'bound' && (
										<span>
											<button
												className="link small"
												onClick={() => void act(LabelService.method.unbind, { ref: ref(l.id) }, { ok: '라벨을 떼었습니다.' })}
											>
												떼기
											</button>{' '}
											<button
												className="link small bad"
												onClick={() => void act(LabelService.method.unbind, { ref: ref(l.id), void: true }, { ok: '라벨을 폐기했습니다.' })}
											>
												폐기
											</button>
										</span>
									)}
								</li>
							))}
						</ul>
					)
				}
			</Load>
		</Card>
	)
}

function Move(props: { a: Asset; onClose: () => void }): ReactNode {
	const act = useAct()
	const [mode, setMode] = useState('space')
	const [space, setSpace] = useState('')
	const [into, setInto] = useState<Asset>()
	const [how, setHow] = useState('located')
	const [slot, setSlot] = useState('')
	const [uFrom, setUFrom] = useState('')
	const [uTo, setUTo] = useState('')
	const [at, setAt] = useState('')
	const [reason, setReason] = useState('')
	return (
		<FormModal
			title={`${props.a.tag} 이동`}
			submit="이동"
			onClose={props.onClose}
			onSubmit={async () => {
				const to = mode === 'space' ? (space === '' ? undefined : ref(idBytes(space))) : mode === 'asset' ? (into === undefined ? undefined : ref(into.id)) : undefined
				if (mode !== 'out' && to === undefined) return false
				return (
					(await act(
						AssetService.method.move,
						{
							ref: ref(props.a.id),
							to,
							mode: how,
							slot,
							uFrom: Number(uFrom || 0),
							uTo: Number(uTo || 0),
							at: at === '' ? undefined : ts(fromInput(at)),
							reason,
						},
						{ ok: '옮겼습니다.' },
					)) !== undefined
				)
			}}
		>
			<Field label="어디로">
				<Select
					value={mode}
					onChange={setMode}
					options={[
						{ value: 'space', label: '공간으로' },
						{ value: 'asset', label: '다른 자산 안으로 (랙, 키트, 장비)' },
						{ value: 'out', label: '어디에도 없음 (꺼냄)' },
					]}
				/>
			</Field>
			{mode === 'space' && (
				<Field label="공간">
					<SpaceSelect value={space} onChange={setSpace} required />
				</Field>
			)}
			{mode === 'asset' && (
				<Field label="자산" wide group>
					<AssetPicker value={into} onChange={setInto} exclude={[props.a.id]} />
				</Field>
			)}
			{mode !== 'out' && (
				<>
					<Field label="배치">
						<Select value={how} onChange={setHow} options={Object.entries(modeWord).map(([value, label]) => ({ value, label }))} />
					</Field>
					<Field label="자리" hint="선반, 슬롯, 좌석 번호 등">
						<input value={slot} onChange={(e) => setSlot(e.target.value)} />
					</Field>
					{how === 'installed' && (
						<>
							<Field label="랙 U 시작">
								<input type="number" min={1} value={uFrom} onChange={(e) => setUFrom(e.target.value)} />
							</Field>
							<Field label="랙 U 끝">
								<input type="number" min={1} value={uTo} onChange={(e) => setUTo(e.target.value)} />
							</Field>
						</>
					)}
				</>
			)}
			<Field label="언제" hint="비우면 지금. 지난 일을 늦게 기록할 때 그 시점을 적습니다">
				<input type="datetime-local" value={at} onChange={(e) => setAt(e.target.value)} />
			</Field>
			<Field label="사유">
				<input value={reason} onChange={(e) => setReason(e.target.value)} />
			</Field>
		</FormModal>
	)
}

function SetAttrs(props: { a: Asset; onClose: () => void }): ReactNode {
	const c = useCatalog()
	const act = useAct()
	const a = props.a
	const [name, setName] = useState(a.name)
	const [tag, setTag] = useState(a.tag)
	const [status, setStatus] = useState(a.status)
	const [condition, setCondition] = useState(a.condition)
	const [serial, setSerial] = useState(a.serial)
	const [desc, setDesc] = useState(a.desc)
	const [attrs, setAttrs] = useState<Record<string, string>>({ ...a.attributes })
	const [at, setAt] = useState('')
	const [reason, setReason] = useState('')
	return (
		<FormModal
			title="정보·상태 변경"
			wide
			onClose={props.onClose}
			onSubmit={async () => {
				const set: Record<string, string> = {}
				const clear: string[] = []
				if (name !== a.name) set['name'] = name
				if (tag !== a.tag) set['tag'] = tag
				if (status !== a.status) set['status'] = status
				if (condition !== a.condition) set['condition'] = condition
				if (serial !== a.serial) set['serial'] = serial
				if (desc !== a.desc) set['desc'] = desc
				for (const [k, v] of Object.entries(attrs)) {
					if ((a.attributes[k] ?? '') === v) continue
					if (v === '') clear.push(`attr.${k}`)
					else set[`attr.${k}`] = v
				}
				if (Object.keys(set).length === 0 && clear.length === 0) return
				return (
					(await act(
						AssetService.method.setAttributes,
						{ ref: ref(a.id), set, clear, at: at === '' ? undefined : ts(fromInput(at)), reason },
						{ ok: '바꿨습니다.' },
					)) !== undefined
				)
			}}
		>
			<Field label="이름">
				<input value={name} onChange={(e) => setName(e.target.value)} required />
			</Field>
			<Field label="태그">
				<input value={tag} onChange={(e) => setTag(e.target.value)} required />
			</Field>
			<Field label="상태">
				<Select value={status} onChange={setStatus} options={Object.entries(statusWord).map(([value, label]) => ({ value, label }))} />
			</Field>
			<Field label="컨디션">
				<Select value={condition} onChange={setCondition} options={Object.entries(conditionWord).map(([value, label]) => ({ value, label }))} />
			</Field>
			<Field label="시리얼">
				<input value={serial} onChange={(e) => setSerial(e.target.value)} />
			</Field>
			<AttrFields type={c.type(a.type?.id)} value={attrs} onChange={setAttrs} />
			<Field label="설명" wide>
				<textarea value={desc} onChange={(e) => setDesc(e.target.value)} rows={2} />
			</Field>
			<Field label="언제부터" hint="비우면 지금">
				<input type="datetime-local" value={at} onChange={(e) => setAt(e.target.value)} />
			</Field>
			<Field label="사유">
				<input value={reason} onChange={(e) => setReason(e.target.value)} />
			</Field>
		</FormModal>
	)
}

function Assign(props: { a: Asset; onClose: () => void }): ReactNode {
	const act = useAct()
	const [role, setRole] = useState('owner')
	const [party, setParty] = useState('')
	const [at, setAt] = useState('')
	return (
		<FormModal
			title="소유·관리 담당"
			onClose={props.onClose}
			onSubmit={async () =>
				(await act(
					AssetService.method.assign,
					{
						ref: ref(props.a.id),
						role,
						party: party === '' ? undefined : ref(idBytes(party)),
						at: at === '' ? undefined : ts(fromInput(at)),
					},
					{ ok: party === '' ? '해제했습니다.' : '지정했습니다.' },
				)) !== undefined
			}
		>
			<p className="wide mute small">사용자(보유자)는 지급·대여로 정해집니다. 여기서는 소유 부서와 관리 담당자를 정합니다.</p>
			<Field label="역할">
				<Select value={role} onChange={setRole} options={[{ value: 'owner', label: '소유 (부서·팀)' }, { value: 'manager', label: '관리 담당' }]} />
			</Field>
			<Field label="누구" hint="비우면 해제">
				<PartySelect value={party} onChange={setParty} kinds={['person', 'team', 'org']} empty="(해제)" />
			</Field>
			<Field label="언제부터" hint="비우면 지금">
				<input type="datetime-local" value={at} onChange={(e) => setAt(e.target.value)} />
			</Field>
		</FormModal>
	)
}

function Relate(props: { a: Asset; onClose: () => void }): ReactNode {
	const act = useAct()
	const [target, setTarget] = useState<Asset>()
	const [kind, setKind] = useState('member_of')
	const [required, setRequired] = useState(true)
	const [end, setEnd] = useState(false)
	return (
		<FormModal
			title="키트·그룹 연결"
			onClose={props.onClose}
			onSubmit={async () => {
				if (target === undefined) return false
				return (
					(await act(
						AssetService.method.relate,
						{ ref: ref(props.a.id), target: ref(target.id), kind, required, end },
						{ ok: end ? '연결을 끊었습니다.' : '연결했습니다.' },
					)) !== undefined
				)
			}}
		>
			<Field label="대상" wide group>
				<AssetPicker value={target} onChange={setTarget} exclude={[props.a.id]} placeholder="키트나 그룹을 찾으세요" />
			</Field>
			<Field label="관계">
				<Select
					value={kind}
					onChange={setKind}
					options={[
						{ value: 'member_of', label: '구성품 / 소속 (키트, 풀)' },
						{ value: 'connected_to', label: '연결됨 (케이블, 도킹)' },
					]}
				/>
			</Field>
			{kind === 'member_of' && (
				<label className="check">
					<input type="checkbox" checked={required} onChange={(e) => setRequired(e.target.checked)} /> 필수 구성품 (키트를 예약하면 함께 잡힘)
				</label>
			)}
			<label className="check">
				<input type="checkbox" checked={end} onChange={(e) => setEnd(e.target.checked)} /> 연결 끊기
			</label>
		</FormModal>
	)
}

function MakeBookable(props: { a: Asset; onClose: () => void }): ReactNode {
	const act = useAct()
	const [approval, setApproval] = useState(false)
	const [hours, setHours] = useState(props.a.kind === 'space')
	const [maxH, setMaxH] = useState(props.a.kind === 'space' ? '8' : '168')
	const [after, setAfter] = useState(props.a.kind === 'space' ? '0' : '10')
	return (
		<FormModal
			title="예약 가능하게"
			onClose={props.onClose}
			onSubmit={async () =>
				(await act(
					BookableService.method.add,
					{
						asset: ref(props.a.id),
						approval,
						maxMinutes: Math.round(Number(maxH) * 60),
						bufferAfter: Number(after),
						horizonDays: 90,
						hours: hours
							? { ranges: [1, 2, 3, 4, 5].map((weekday) => ({ weekday, fromMinute: 8 * 60, toMinute: 20 * 60 })) }
							: undefined,
					},
					{ ok: '이제 예약할 수 있습니다.' },
				)) !== undefined
			}
		>
			<label className="check wide">
				<input type="checkbox" checked={approval} onChange={(e) => setApproval(e.target.checked)} /> 관리자 승인 필요
			</label>
			<label className="check wide">
				<input type="checkbox" checked={hours} onChange={(e) => setHours(e.target.checked)} /> 평일 08–20시에만
			</label>
			<Field label="최대 이용 시간 (시간)">
				<input type="number" min={1} value={maxH} onChange={(e) => setMaxH(e.target.value)} />
			</Field>
			<Field label="정리 시간 (분)">
				<input type="number" min={0} value={after} onChange={(e) => setAfter(e.target.value)} />
			</Field>
			<p className="wide mute small">세부 운영 시간과 배타 그룹은 설정 › 예약 자원에서 바꿀 수 있습니다.</p>
		</FormModal>
	)
}

function Bind(props: { a: Asset; onClose: () => void }): ReactNode {
	const act = useAct()
	const [code, setCode] = useState('')
	return (
		<FormModal
			title="라벨 연결"
			submit="연결"
			onClose={props.onClose}
			onSubmit={async () => {
				const r = await act(LabelService.method.resolve, { code }, { quiet: false })
				if (r?.label === undefined) return false
				return (await act(LabelService.method.bind, { ref: ref(r.label.id), asset: ref(props.a.id) }, { ok: '라벨을 연결했습니다.' })) !== undefined
			}}
		>
			<p className="wide mute small">미리 인쇄해 둔 빈 라벨을 이 자산에 붙일 때 씁니다. 라벨의 QR을 스캔하거나 주소를 붙여 넣으세요.</p>
			<Field label="라벨 주소 또는 코드" wide>
				<input value={code} onChange={(e) => setCode(e.target.value)} required autoFocus />
			</Field>
		</FormModal>
	)
}

function Upload(props: { a: Asset; onClose: () => void }): ReactNode {
	const act = useAct()
	const [file, setFile] = useState<File>()
	const [busy, setBusy] = useState(false)
	return (
		<FormModal
			title="첨부 올리기"
			submit={busy ? <Spinner /> : '올리기'}
			onClose={props.onClose}
			onSubmit={async () => {
				if (file === undefined) return false
				setBusy(true)
				try {
					const data = await readFile(file)
					return (
						(await act(
							AttachmentService.method.upload,
							{ subjectId: props.a.id, name: file.name, contentType: file.type || 'application/octet-stream', data },
							{ ok: '올렸습니다.' },
						)) !== undefined
					)
				} finally {
					setBusy(false)
				}
			}}
		>
			<Field label="파일 (20MB까지)" wide>
				<input type="file" accept="image/*,application/pdf,.xlsx,.docx,.txt,.zip" capture="environment" onChange={(e) => setFile(e.target.files?.[0])} required />
			</Field>
		</FormModal>
	)
}
