import { useMemo, useState, type ReactNode } from 'react'
import { useSearchParams } from 'react-router-dom'

import type { BusySpan, CalendarEntry, ReservationConflict } from '../../gen/rove/booking_svc_pb.js'
import type { Reservation } from '../../gen/rove/booking_pb.js'
import { AssetService, BookableService, ReservationService, at, dateInput, dateOf, fmt, fmtWith, fromInput, idBytes, idStr, localInput, minute, op, ref, sameId, shift, ts, wall } from '../api.js'
import { useCatalog } from '../catalog.js'
import { PartySelect, useAssets } from '../pickers.js'
import { Live, useAct, useRpc } from '../rpc.js'
import { Badge, Card, Empty, Field, FormModal, Load, Modal, PageHead, Select, Tabs } from '../ui.js'
import { kindWord, reservationWord } from '../words.js'
import { NewCustody } from './custody.js'

const H0 = 7
const H1 = 22
const day = 86400_000

/** The Monday of `d`'s week, at midnight on the workspace's wall. */
function monday(d: Date): Date {
	const w = wall(d)
	return at(w.y, w.m, w.d - ((w.wd + 6) % 7))
}

export function Reservations(): ReactNode {
	const c = useCatalog()
	const [sp, setSp] = useSearchParams()
	const tab = sp.get('tab') ?? 'calendar'
	// Every reservation made, decided or ended writes the bookables it takes
	// first (`schedule_version`), so the page follows those: the one it is
	// showing, or every one there is to book.
	const one = sp.get('resource') ?? ''
	const shown = one !== '' ? [c.bookables.find((b) => idStr(b.asset?.id) === one)?.id] : c.bookables.filter((b) => b.enabled).map((b) => b.id)
	return (
		<Live watch={BookableService.method.watch} ids={shown} max={32}>
			<PageHead title="예약" sub="회의실, 장비, 키트를 시간 단위로 예약합니다. 겹치는 예약은 받지 않습니다." />
			<Tabs
				tabs={[
					{ key: 'calendar', label: '캘린더' },
					{ key: 'mine', label: '내 예약' },
					...(c.can('manager') ? [{ key: 'approve', label: '승인' }, { key: 'all', label: '전체 목록' }] : []),
				]}
				at={tab}
				onChange={(k) => setSp((p) => { const n = new URLSearchParams(p); n.set('tab', k); return n })}
			/>
			{tab === 'calendar' && <Calendar />}
			{tab === 'mine' && <List mine />}
			{tab === 'approve' && <Approvals />}
			{tab === 'all' && <List />}
		</Live>
	)
}

function Calendar(): ReactNode {
	const c = useCatalog()
	const [sp, setSp] = useSearchParams()
	const resource = sp.get('resource') ?? ''
	const [start, setStart] = useState(() => monday(new Date()))
	const [draft, setDraft] = useState<{ from: Date; to: Date; resource: string }>()
	const [open, setOpen] = useState<CalendarEntry>()
	const end = shift(start, 7)
	const enabled = c.bookables.filter((b) => b.enabled)
	const resources = resource === '' ? enabled.map((b) => b.asset?.id).filter((v): v is Uint8Array => v !== undefined) : [idBytes(resource)]

	const cal = useRpc(ReservationService.method.calendar, { resources: resources.map(ref), from: ts(start), to: ts(end) }, { poll: 60_000 })
	const busy = useRpc(BookableService.method.availability, resource === '' ? null : { resources: [ref(idBytes(resource))], from: ts(start), to: ts(end) })
	const names = useResourceNames()

	const days = Array.from({ length: 7 }, (_, i) => shift(start, i))
	const hours = Array.from({ length: H1 - H0 }, (_, i) => H0 + i)
	const pos = (from: Date, to: Date, d: Date) => {
		const w = wall(d)
		const d0 = at(w.y, w.m, w.d, H0).getTime()
		const d1 = at(w.y, w.m, w.d, H1).getTime()
		const a = Math.max(from.getTime(), d0)
		const b = Math.min(to.getTime(), d1)
		if (b <= a) return undefined
		const span = d1 - d0
		return { top: `${(100 * (a - d0)) / span}%`, height: `${(100 * (b - a)) / span}%` }
	}

	return (
		<>
			<div className="filters">
				<Select
					value={resource}
					onChange={(v) => setSp((p) => { const n = new URLSearchParams(p); if (v === '') n.delete('resource'); else n.set('resource', v); return n })}
					empty="모든 예약 자원"
					options={enabled.map((b) => ({ value: idStr(b.asset?.id), label: names.get(idStr(b.asset?.id)) ?? '…' }))}
				/>
				<div className="inline">
					<button onClick={() => setStart(shift(start, -7))}>‹ 이전 주</button>
					<button onClick={() => setStart(monday(new Date()))}>이번 주</button>
					<button onClick={() => setStart(shift(start, 7))}>다음 주 ›</button>
				</div>
				<span className="mute">
					{fmt(start, 'date')} – {fmt(new Date(end.getTime() - 1), 'date')}
				</span>
				<button
					className="primary"
					onClick={() => {
						// The next hour on the workspace's clock.
						const w = wall(new Date())
						const f = at(w.y, w.m, w.d, w.h + 1)
						setDraft({ from: f, to: new Date(f.getTime() + 3600_000), resource })
					}}
				>
					+ 예약
				</button>
			</div>
			{enabled.length === 0 ? (
				<Empty>예약할 수 있는 자원이 없습니다. 자산 화면에서 '예약 가능하게'를 누르세요.</Empty>
			) : (
				<div className="week">
					<div className="hours">
						<div className="dayhead" />
						{hours.map((h) => (
							<div key={h} className="hour">
								{h}:00
							</div>
						))}
					</div>
					{days.map((d) => (
						<div key={d.toISOString()} className={`daycol ${dateInput(d) === dateInput(new Date()) ? 'today' : ''}`}>
							<div className="dayhead">{fmtWith({ weekday: 'short', month: 'numeric', day: 'numeric' }, d)}</div>
							<div
								className="slots"
								onClick={(e) => {
									const r = (e.currentTarget as HTMLElement).getBoundingClientRect()
									const frac = (e.clientY - r.top) / r.height
									const mins = Math.floor((frac * (H1 - H0) * 60) / 30) * 30
									const w = wall(d)
									const from = at(w.y, w.m, w.d, H0, mins)
									setDraft({ from, to: new Date(from.getTime() + 3600_000), resource })
								}}
							>
								{hours.map((h) => (
									<div key={h} className="hourline" />
								))}
								{(busy.data?.busy ?? [])
									.filter((b) => b.kind !== 'reservation')
									.map((b, i) => {
										const p = pos(dateOf(b.beginsAt) ?? d, dateOf(b.endsAt) ?? d, d)
										return p === undefined ? null : (
											<div key={`b${i}`} className={`busy ${b.kind}`} style={p} title={b.label}>
												{b.kind === 'maintenance' ? '정비' : b.kind === 'closed' ? '운영 시간 외' : b.kind === 'waiting' ? `구성품 대기: ${b.label}` : b.label}
											</div>
										)
									})}
								{(cal.data?.entries ?? []).map((e) => {
									const r = e.reservation
									if (r === undefined || ['cancelled', 'rejected', 'expired'].includes(r.status)) return null
									const p = pos(dateOf(r.beginsAt) ?? d, dateOf(r.endsAt) ?? d, d)
									if (p === undefined) return null
									const mine = sameId(r.party?.id, c.me.party?.id)
									return (
										<div
											key={`${idStr(r.id)}`}
											className={`event ${r.status} ${mine ? 'mine' : ''} ${e.conflicts.length > 0 ? 'conflict' : ''}`}
											style={p}
											onClick={(ev) => {
												ev.stopPropagation()
												setOpen(e)
											}}
										>
											<strong>{r.name}</strong>
											<span>
												{fmt(r.beginsAt, 'time')}–{fmt(r.endsAt, 'time')}
											</span>
											{resource === '' && <span>{e.resourceIds.map((x) => names.get(idStr(x)) ?? '').join(', ')}</span>}
											<span>{e.partyName}</span>
											{e.conflicts.length > 0 && <span className="warn">구성품 충돌</span>}
										</div>
									)
								})}
							</div>
						</div>
					))}
				</div>
			)}
			{draft !== undefined && <NewReservation draft={draft} onClose={() => setDraft(undefined)} />}
			{open !== undefined && open.reservation !== undefined && (
				<ReservationDialog r={open.reservation} resources={open.resourceIds} conflicts={open.conflicts} onClose={() => setOpen(undefined)} />
			)}
		</>
	)
}

/** The names of the reservable assets. */
function useResourceNames(): Map<string, string> {
	const c = useCatalog()
	const assets = useAssets(c.bookables.map((b) => b.asset?.id))
	return useMemo(() => {
		const m = new Map<string, string>()
		for (const a of assets.values()) m.set(idStr(a.id), `${a.name}${a.kind !== 'space' ? ` (${kindWord[a.kind]})` : ''}`)
		return m
	}, [assets])
}

const repeatOptions = [
	{ value: '', label: '반복 없음' },
	{ value: 'FREQ=DAILY;COUNT=5', label: '매일 5회' },
	{ value: 'FREQ=WEEKLY;COUNT=4', label: '매주 4회' },
	{ value: 'FREQ=WEEKLY;COUNT=12', label: '매주 12회' },
	{ value: 'FREQ=WEEKLY;INTERVAL=2;COUNT=6', label: '격주 6회' },
	{ value: 'FREQ=MONTHLY;COUNT=6', label: '매월 6회' },
]

function NewReservation(props: { draft: { from: Date; to: Date; resource: string }; onClose: () => void }): ReactNode {
	const c = useCatalog()
	const act = useAct()
	const names = useResourceNames()
	const [name, setName] = useState('')
	const [resource, setResource] = useState(props.draft.resource)
	const [from, setFrom] = useState(localInput(props.draft.from))
	const [to, setTo] = useState(localInput(props.draft.to))
	const [repeat, setRepeat] = useState('')
	const [party, setParty] = useState('')
	const [hold, setHold] = useState(false)
	const [override, setOverride] = useState(false)
	const [reason, setReason] = useState('')
	const [key] = useState(op)
	const b = c.bookable(resource === '' ? undefined : idBytes(resource))

	return (
		<FormModal
			title="예약"
			submit={hold ? '임시로 잡기' : '예약'}
			wide
			onClose={props.onClose}
			onSubmit={async () => {
				if (resource === '') return false
				const v = await act(
					ReservationService.method.add,
					{
						name: name || '예약',
						beginsAt: ts(fromInput(from)),
						endsAt: ts(fromInput(to)),
						items: [{ resource: ref(idBytes(resource)) }],
						repeat,
						party: party === '' ? undefined : ref(idBytes(party)),
						hold,
						override,
						reason,
						op: key,
					},
				)
				if (v === undefined) return false
				return true
			}}
		>
			<Field label="무엇을 *">
				<Select
					value={resource}
					onChange={setResource}
					empty="선택"
					required
					options={c.bookables.filter((x) => x.enabled).map((x) => ({ value: idStr(x.asset?.id), label: names.get(idStr(x.asset?.id)) ?? '…' }))}
				/>
			</Field>
			<Field label="제목">
				<input value={name} onChange={(e) => setName(e.target.value)} placeholder="예: 주간 회의" />
			</Field>
			<Field label="시작 *">
				<input type="datetime-local" step={1800} value={from} onChange={(e) => setFrom(e.target.value)} required />
			</Field>
			<Field label="끝 *">
				<input type="datetime-local" step={1800} value={to} onChange={(e) => setTo(e.target.value)} required />
			</Field>
			<Field label="반복">
				<Select value={repeat} onChange={setRepeat} options={repeatOptions} />
			</Field>
			{c.can('manager') && (
				<Field label="누구를 위해" hint="비우면 나">
					<PartySelect value={party} onChange={setParty} kinds={['person', 'team']} empty="(나)" />
				</Field>
			)}
			<label className="check wide">
				<input type="checkbox" checked={hold} onChange={(e) => setHold(e.target.checked)} /> 10분간 임시로 잡아 두고 나중에 확정
			</label>
			{c.can('manager') && (
				<label className="check wide">
					<input type="checkbox" checked={override} onChange={(e) => setOverride(e.target.checked)} /> 겹쳐도 예약 (관리자 강제, 사유 필요)
				</label>
			)}
			{override && (
				<Field label="사유 *" wide>
					<input value={reason} onChange={(e) => setReason(e.target.value)} required />
				</Field>
			)}
			{b !== undefined && (
				<p className="wide mute small">
					{b.approval ? '관리자 승인이 필요한 자원입니다. ' : ''}
					{b.maxMinutes > 0 ? `최대 ${Math.round(b.maxMinutes / 60)}시간. ` : ''}
					{b.bufferAfter > 0 ? `끝난 뒤 ${b.bufferAfter}분 정리 시간. ` : ''}
					{b.horizonDays > 0 ? `${b.horizonDays}일 앞까지 예약할 수 있습니다.` : ''}
				</p>
			)}
		</FormModal>
	)
}

function ReservationDialog(props: { r: Reservation; resources: Uint8Array[]; conflicts: ReservationConflict[]; onClose: () => void }): ReactNode {
	const c = useCatalog()
	const act = useAct()
	const names = useResourceNames()
	const r = props.r
	const mine = sameId(r.party?.id, c.me.party?.id) || sameId(r.requestedBy, c.me.holder?.id)
	const mgr = c.can('manager')
	const [pickup, setPickup] = useState(false)
	const decide = async (m: typeof ReservationService.method.cancel, ok: string, series = false) => {
		const v = await act(m, { ref: ref(r.id), series }, { ok })
		if (v !== undefined) props.onClose()
	}
	const now = Date.now()
	const begins = dateOf(r.beginsAt)?.getTime() ?? 0
	const isSpace = props.resources.every((x) => c.space(x) !== undefined)

	if (pickup) {
		return <PickUp r={r} resources={props.resources} conflicts={props.conflicts} onClose={props.onClose} />
	}
	return (
		<Modal title={r.name} onClose={props.onClose}>
			<p>
				<Badge v={r.status} words={reservationWord} /> {fmt(r.beginsAt)} – {fmt(r.endsAt)}
			</p>
			<p>
				{props.resources.map((x) => names.get(idStr(x)) ?? '').join(', ')} · {c.party(r.party?.id)?.name ?? ''}
			</p>
			{r.rrule !== '' && <p className="mute small">반복: {r.rrule}</p>}
			{r.status === 'held' && r.expiresAt !== undefined && <p className="warn small">{fmt(r.expiresAt, 'time')}까지 확정하지 않으면 풀립니다.</p>}
			<Waiting conflicts={props.conflicts} />
			<footer className="modal-actions">
				{r.status === 'held' && (mine || mgr) && <button className="primary" onClick={() => void decide(ReservationService.method.confirm, '확정했습니다.')}>확정</button>}
				{r.status === 'requested' && mgr && (
					<>
						<button onClick={() => void decide(ReservationService.method.reject, '거절했습니다.')}>거절</button>
						<button className="primary" onClick={() => void decide(ReservationService.method.approve, '승인했습니다.')}>
							승인
						</button>
					</>
				)}
				{r.status === 'confirmed' && isSpace && (mine || mgr) && now >= begins - 30 * 60_000 && (
					<button className="primary" onClick={() => void decide(ReservationService.method.checkIn, '체크인했습니다.')}>
						체크인
					</button>
				)}
				{r.status === 'confirmed' && !isSpace && mgr && (
					<button className="primary" onClick={() => setPickup(true)}>
						수령 처리 (대여)
					</button>
				)}
				{['confirmed', 'in_use'].includes(r.status) && isSpace && (mine || mgr) && (
					<button onClick={() => void decide(ReservationService.method.complete, '종료했습니다.')}>일찍 끝내기</button>
				)}
				{['held', 'requested', 'confirmed'].includes(r.status) && (mine || mgr) && (
					<>
						<button className="danger" onClick={() => void decide(ReservationService.method.cancel, '취소했습니다.')}>
							취소
						</button>
						{r.seriesId.length > 0 && (
							<button className="danger" onClick={() => void decide(ReservationService.method.cancel, '이후 반복을 모두 취소했습니다.', true)}>
								이후 반복 모두 취소
							</button>
						)}
					</>
				)}
			</footer>
		</Modal>
	)
}

/**
 * Equipment is picked up as a loan due back when the reservation ends, and
 * what it waits for is said there too: a kit's member it does not hold does
 * not go out with it.
 */
function PickUp(props: { r: Reservation; resources: Uint8Array[]; conflicts: ReservationConflict[]; onClose: () => void }): ReactNode {
	const assets = useRpc(AssetService.method.list, { filters: props.resources.map((id) => ({ ref: ref(id) })), size: 50 })
	if (assets.data === undefined) return null
	return (
		<NewCustodyForPickup r={props.r} assets={assets.data.items} conflicts={props.conflicts} onClose={props.onClose} />
	)
}

function NewCustodyForPickup(props: { r: Reservation; assets: import('../../gen/rove/asset_pb.js').Asset[]; conflicts: ReservationConflict[]; onClose: () => void }): ReactNode {
	return (
		<NewCustody
			assets={props.assets.filter((a) => a.kind === 'item')}
			party={idStr(props.r.party?.id)}
			reservation={props.r.id}
			note={props.conflicts.length > 0 ? <Waiting conflicts={props.conflicts} /> : undefined}
			onClose={props.onClose}
		/>
	)
}

/**
 * What a reservation waits for: the members its kit was given after it was
 * made that something else holds then (design 4). It takes each once it is
 * free, and a member it does not hold is not handed out with the kit.
 */
function Waiting(props: { conflicts: ReservationConflict[] }): ReactNode {
	if (props.conflicts.length === 0) return null
	return (
		<div className="note warn" role="note" aria-label="구성품 충돌">
			<strong>구성품 충돌</strong>
			<ul className="waiting">
				{props.conflicts.map((c) => (
					<li key={`${idStr(c.resourceId)}-${fmt(c.beginsAt)}`}>
						{c.resourceTag} {c.resourceName} ({fmt(c.beginsAt)} – {fmt(c.endsAt, 'time')}):{' '}
						{c.heldBy.length === 0 ? '지금은 비어 있어 곧 이 예약에 잡힙니다.' : `${c.heldBy.map(heldBy).join(', ')}이(가) 잡고 있습니다.`}
					</li>
				))}
			</ul>
			<p className="small">키트에 나중에 더해진 구성품입니다. 비면 이 예약이 잡습니다.</p>
		</div>
	)
}

/** What holds a member a reservation waits for, as a person says it. */
function heldBy(b: BusySpan): string {
	return b.kind === 'maintenance' ? '정비' : `'${b.label}' 예약`
}

function List(props: { mine?: boolean }): ReactNode {
	const c = useCatalog()
	const [open, setOpen] = useState<CalendarEntry>()
	const from = new Date(minute().getTime() - 30 * day)
	const to = new Date(minute().getTime() + 90 * day)
	const cal = useRpc(ReservationService.method.calendar, { from: ts(from), to: ts(to), mine: props.mine === true })
	const names = useResourceNames()
	return (
		<Card title={props.mine ? '내 예약 (지난 30일 ~ 앞으로 90일)' : '전체 예약'}>
			<Load q={cal}>
				{(d) =>
					d.entries.length === 0 ? (
						<Empty>예약이 없습니다.</Empty>
					) : (
						<div className="table-wrap">
							<table className="table">
								<thead>
									<tr>
										<th>언제</th>
										<th>무엇</th>
										<th>제목</th>
										<th>누구</th>
										<th>상태</th>
									</tr>
								</thead>
								<tbody>
									{d.entries.map((e) => (
										<tr key={idStr(e.reservation?.id)} className="link" onClick={() => setOpen(e)}>
											<td>
												{fmt(e.reservation?.beginsAt)} – {fmt(e.reservation?.endsAt, 'time')}
											</td>
											<td>{e.resourceIds.map((x) => names.get(idStr(x)) ?? '').join(', ')}</td>
											<td>{e.reservation?.name}</td>
											<td>{e.partyName || c.party(e.reservation?.party?.id)?.name}</td>
											<td>
												<Badge v={e.reservation?.status ?? ''} words={reservationWord} />
												{e.conflicts.length > 0 && <span className="badge warn">구성품 충돌</span>}
											</td>
										</tr>
									))}
								</tbody>
							</table>
						</div>
					)
				}
			</Load>
			{open !== undefined && open.reservation !== undefined && (
				<ReservationDialog r={open.reservation} resources={open.resourceIds} conflicts={open.conflicts} onClose={() => setOpen(undefined)} />
			)}
		</Card>
	)
}

/**
 * Every request a manager has to decide, and what each waits for. The sweep
 * lets go of a request once it has begun, so they are all from now on -- as
 * far ahead as anything may be booked.
 */
function Approvals(): ReactNode {
	const c = useCatalog()
	const act = useAct()
	const from = new Date(minute().getTime() - day)
	const to = new Date(minute().getTime() + 3650 * day)
	const list = useRpc(ReservationService.method.calendar, { statuses: ['requested'], from: ts(from), to: ts(to) })
	return (
		<Card title="승인 대기">
			<Load q={list}>
				{(d) =>
					d.entries.length === 0 ? (
						<Empty>대기 중인 요청이 없습니다.</Empty>
					) : (
						<ul className="rows">
							{d.entries.map((e) => {
								const r = e.reservation
								if (r === undefined) return null
								return (
									<li key={idStr(r.id)}>
										<span>
											{fmt(r.beginsAt)} – {fmt(r.endsAt, 'time')} · <strong>{r.name}</strong> · {e.partyName || c.party(r.party?.id)?.name}
										</span>
										<span className="inline">
											<button onClick={() => void act(ReservationService.method.reject, { ref: ref(r.id) }, { ok: '거절했습니다.' })}>거절</button>
											<button className="primary" onClick={() => void act(ReservationService.method.approve, { ref: ref(r.id) }, { ok: '승인했습니다.' })}>
												승인
											</button>
										</span>
										<Waiting conflicts={e.conflicts} />
									</li>
								)
							})}
						</ul>
					)
				}
			</Load>
		</Card>
	)
}
