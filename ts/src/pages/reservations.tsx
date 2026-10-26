import { useMemo, useState, type ReactNode } from 'react'
import { useSearchParams } from 'react-router-dom'

import type { Asset } from '../../gen/rove/asset_pb.js'
import type { TimelineEntry } from '../../gen/rove/asset_svc_pb.js'
import type { BusySpan, CalendarEntry, ReservationConflict, ReservationOutOfUse } from '../../gen/rove/booking_svc_pb.js'
import type { Allocation, Reservation } from '../../gen/rove/booking_pb.js'
import { AllocationService, BookableService, ReservationService, at, dateInput, dateOf, fmt, fmtWith, fromInput, idBytes, idStr, localInput, minute, op, ref, sameId, shift, ts, wall } from '../api.js'
import { useCatalog } from '../catalog.js'
import { PartySelect, useAssets, useAssetsRead } from '../pickers.js'
import { Live, useAct, useRpc } from '../rpc.js'
import { Badge, Card, Empty, Field, FormModal, Load, Modal, PageHead, Select, Tabs } from '../ui.js'
import { goneWord, kindWord, markWord, outOfUse, reservationWord, statusWord } from '../words.js'
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
	const kinds = useResourceKinds()

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
												{b.kind === 'maintenance' ? '정비' : b.kind === 'closed' ? '운영 시간 외' : b.kind === 'waiting' ? `${kinds.get(resource) === 'group' ? '수량 대기' : '구성품 대기'}: ${b.label}` : b.label}
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
											className={`event ${r.status} ${mine ? 'mine' : ''} ${markWord(e) !== '' ? 'conflict' : ''}`}
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
											{markWord(e) !== '' && <span className="warn">{markWord(e)}</span>}
										</div>
									)
								})}
							</div>
						</div>
					))}
				</div>
			)}
			{draft !== undefined && <NewReservation draft={draft} onClose={() => setDraft(undefined)} />}
			{open !== undefined && <EntryDialog e={open} onClose={() => setOpen(undefined)} />}
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

/** The kinds of the reservable assets: what waits for a group waits for some of a pool. */
function useResourceKinds(): Map<string, string> {
	const c = useCatalog()
	const assets = useAssets(c.bookables.map((b) => b.asset?.id))
	return useMemo(() => new Map([...assets.values()].map((a) => [idStr(a.id), a.kind])), [assets])
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
	// What is out of use is not booked anew (D43): said of it where it is
	// picked, and once it is, before the server refuses it.
	const assets = useAssets(c.bookables.map((x) => x.asset?.id))
	const gone = (id: string) => {
		const a = assets.get(id)
		return a !== undefined && outOfUse.includes(a.status) ? a.status : undefined
	}
	const chosen = gone(resource)

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
					options={c.bookables
						.filter((x) => x.enabled)
						.map((x) => {
							const id = idStr(x.asset?.id)
							const status = gone(id)
							return { value: id, label: `${names.get(id) ?? '…'}${status !== undefined ? ` · ${statusWord[status] ?? status}` : ''}` }
						})}
				/>
			</Field>
			{chosen !== undefined && (
				<p className="wide warn small" role="note" aria-label="쓸 수 없는 자산">
					{statusWord[chosen] ?? chosen} 상태라 새로 예약할 수 없습니다.
				</p>
			)}
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

/**
 * A reservation as the calendar answers it, opened: the one dialog it is
 * opened in wherever it is listed -- the calendar, my reservations, the whole
 * list, the requests waiting for a manager, the dashboard, and the bookings of
 * an asset (D45).
 */
export function EntryDialog(props: { e: CalendarEntry; onClose: () => void }): ReactNode {
	const r = props.e.reservation
	if (r === undefined) return null
	return <ReservationDialog r={r} resources={props.e.resourceIds} conflicts={props.e.conflicts} outOfUse={props.e.outOfUse} onClose={props.onClose} />
}

/**
 * A reservation, and what may be done with it: with what it waits for, and
 * what it takes that is out of use (D42), said before anybody comes for it.
 * What it booked itself that is out of use, it is neither said yes to nor
 * used through (D43), and is not offered to be.
 */
function ReservationDialog(props: { r: Reservation; resources: Uint8Array[]; conflicts: ReservationConflict[]; outOfUse: ReservationOutOfUse[]; onClose: () => void }): ReactNode {
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
	const gone = props.outOfUse.map(outOfUseItem)
	const unusable = booked(gone, props.resources)
	// An item out of use is left out of a pickup, which goes on without it;
	// a kit or a group out of use is not picked up.
	const pickable = !unusable.some((v) => v.kind !== 'item')

	if (pickup) {
		return <PickUp r={r} resources={props.resources} conflicts={props.conflicts} onClose={props.onClose} />
	}
	return (
		<Modal title={r.name} onClose={props.onClose}>
			<p>
				<Badge v={r.status} words={reservationWord} /> {fmt(r.beginsAt)} – {fmt(r.endsAt)}
			</p>
			<p>
				{props.resources.map((x) => names.get(idStr(x)) ?? '').join(', ')} · {c.called(r.party?.id)}
			</p>
			{r.rrule !== '' && <p className="mute small">반복: {r.rrule}</p>}
			{r.status === 'held' && r.expiresAt !== undefined && <p className="warn small">{fmt(r.expiresAt, 'time')}까지 확정하지 않으면 풀립니다.</p>}
			<Waiting conflicts={props.conflicts} />
			<OutOfUse items={gone} more="분실·불용·폐기되었거나 삭제되어 수령할 때 건네지 않습니다." />
			<Unusable status={r.status} items={unusable} />
			{(mine || mgr || c.role === 'auditor') && <History r={r} />}
			<footer className="modal-actions">
				{r.status === 'held' && (mine || mgr) && unusable.length === 0 && (
					<button className="primary" onClick={() => void decide(ReservationService.method.confirm, '확정했습니다.')}>
						확정
					</button>
				)}
				{r.status === 'requested' && mgr && (
					<>
						<button onClick={() => void decide(ReservationService.method.reject, '거절했습니다.')}>거절</button>
						{unusable.length === 0 && (
							<button className="primary" onClick={() => void decide(ReservationService.method.approve, '승인했습니다.')}>
								승인
							</button>
						)}
					</>
				)}
				{r.status === 'confirmed' && isSpace && (mine || mgr) && now >= begins - 30 * 60_000 && unusable.length === 0 && (
					<button className="primary" onClick={() => void decide(ReservationService.method.checkIn, '체크인했습니다.')}>
						체크인
					</button>
				)}
				{r.status === 'confirmed' && !isSpace && mgr && pickable && (
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
 * Equipment is picked up as a loan due back when the reservation ends: what
 * it holds is handed out, a kit's members with it, and what it waits for is
 * said there too -- a kit's member it does not hold does not go out with it,
 * and neither does one it takes that is out of use, held or waited for. Of a
 * pool, as many of its units as it holds, free until it ends, are put in, and
 * any of the others free then may be picked instead (D46).
 */
function PickUp(props: { r: Reservation; resources: Uint8Array[]; conflicts: ReservationConflict[]; onClose: () => void }): ReactNode {
	const held = useRpc(AllocationService.method.list, { filters: [{ refId: props.r.id }], size: 200 })
	const offer = useRpc(ReservationService.method.pickup, { ref: ref(props.r.id) })
	const ids = useMemo(() => (held.data === undefined ? undefined : holds(props.resources, held.data.items)), [held.data, props.resources])
	const waited = useMemo(() => (held.data === undefined ? [] : waitsFor(held.data.items)), [held.data])
	// What it is offered of its pools, or nothing when that could not be read.
	const pools = offer.state === 'error' ? [] : offer.data?.pools
	const units = useMemo(() => (pools ?? []).flatMap((p) => [p.poolId, ...p.offered, ...p.others]), [pools])
	const assets = useAssetsRead([...(ids ?? []), ...waited, ...units])
	if (ids === undefined || pools === undefined || assets === undefined) return null
	const of = (vs: Uint8Array[]) => vs.map((id) => assets.get(idStr(id))).filter((a): a is Asset => a !== undefined)
	const offered = pools.map((p) => ({ pool: assets.get(idStr(p.poolId)), units: p.units, offered: of(p.offered), others: of(p.others) }))
	return <NewCustodyForPickup r={props.r} assets={of(ids)} waited={of(waited)} pools={offered} conflicts={props.conflicts} onClose={props.onClose} />
}

/** What a pickup is offered of a pool it booked: as many as it holds, and the rest free until it ends. */
type PoolOffer = { pool: Asset | undefined; units: number; offered: Asset[]; others: Asset[] }

/**
 * What a reservation holds, in order: what it booked, and then the members
 * its kits took with it -- an allocation of each (design 4) -- but for what
 * it waits for, a member its kit was given that something else holds then,
 * which is not its own until it is free.
 */
function holds(booked: Uint8Array[], allocations: Allocation[]): Uint8Array[] {
	const out: Uint8Array[] = []
	const seen = new Set<string>()
	for (const id of [...booked, ...allocations.filter((a) => a.kind !== 'waiting').map((a) => a.resource?.id)]) {
		if (id === undefined || seen.has(idStr(id))) continue
		seen.add(idStr(id))
		out.push(id)
	}
	return out
}

/** What a reservation waits for, of the members its kits take with it. */
function waitsFor(allocations: Allocation[]): Uint8Array[] {
	return allocations.filter((a) => a.kind === 'waiting').flatMap((a) => (a.resource?.id === undefined ? [] : [a.resource.id]))
}

/** What the note of what is out of use names. */
type OutOfUseItem = { id: Uint8Array; tag: string; name: string; status: string; kind: string }

/** What the server says a reservation takes that is out of use, as the note names it. */
function outOfUseItem(v: ReservationOutOfUse): OutOfUseItem {
	return { id: v.resourceId, tag: v.resourceTag, name: v.resourceName, status: v.status, kind: v.resourceKind }
}

/** What of these a reservation booked itself, of the resources it booked. */
function booked(items: OutOfUseItem[], resources: Uint8Array[]): OutOfUseItem[] {
	return items.filter((v) => resources.some((x) => sameId(x, v.id)))
}

/**
 * What a reservation takes that is out of use -- lost, retired or disposed
 * (D42, D43), or deleted as registered by mistake, for good (D48). A space it
 * booked is not checked into. Anything else no hand-over takes: what was
 * booked itself, or a member kept in its kit as it went (design 4, D40),
 * which the kit's bookings take until it is taken off the kit. `more` is what
 * the place it is said in does about what is handed over.
 */
function OutOfUse(props: { items: OutOfUseItem[]; more: string }): ReactNode {
	const spaces = props.items.filter((a) => a.kind === 'space')
	const things = props.items.filter((a) => a.kind !== 'space')
	const deleted = (vs: OutOfUseItem[]) => vs.some((a) => a.status === 'voided')
	const list = (vs: OutOfUseItem[]) => (
		<ul>
			{vs.map((a) => (
				<li key={idStr(a.id)}>
					{a.tag} {a.name}: {goneWord[a.status] ?? a.status}
				</li>
			))}
		</ul>
	)
	return (
		<>
			{things.length > 0 && (
				<div className="note warn" role="note" aria-label="건넬 수 없는 자산">
					<strong>건넬 수 없는 자산</strong>
					{list(things)}
					<p className="small">{props.more} 키트의 구성품으로 남아 있으면 그 키트의 예약이 계속 잡습니다.</p>
					{deleted(things) && <p className="small">삭제한 것은 다시 쓸 수 없습니다. 예약을 취소하고 다른 것으로 다시 예약하세요.</p>}
				</div>
			)}
			{spaces.length > 0 && (
				<div className="note warn" role="note" aria-label="쓸 수 없는 공간">
					<strong>쓸 수 없는 공간</strong>
					{list(spaces)}
					{deleted(spaces) ? (
						<p className="small">삭제한 공간이라 체크인할 수 없습니다. 예약을 취소하고 다른 곳으로 다시 예약하세요.</p>
					) : (
						<p className="small">분실·불용·폐기 상태라 체크인할 수 없습니다. 다시 쓸 수 있게 되면 체크인할 수 있습니다.</p>
					)}
				</div>
			)}
		</>
	)
}

/**
 * What a reservation in `status` is not while what it booked is out of use
 * (D43): approved, or confirmed from a hold -- saying yes to it is booking it
 * -- nor a kit or a group of it picked up. That a space is not checked into
 * is its note's to say, and an item is left out of a pickup that goes on.
 */
function Unusable(props: { status: string; items: OutOfUseItem[] }): ReactNode {
	if (props.items.length === 0) return null
	let say = ''
	if (props.status === 'held') say = '예약한 것이 쓸 수 없는 상태라 확정할 수 없습니다. 취소하세요.'
	else if (props.status === 'requested') say = '예약한 것이 쓸 수 없는 상태라 승인할 수 없습니다. 거절하거나 취소하세요.'
	else if (props.status === 'confirmed' && props.items.some((v) => v.kind === 'kit' || v.kind === 'group')) say = '예약한 것이 쓸 수 없는 상태라 수령할 수 없습니다.'
	return say === '' ? null : <p className="warn small">{say}</p>
}

/**
 * A pickup's hand-over: to whom it is for, a loan due back when it ends, of
 * what it holds that may be handed over. What it takes that is out of use is
 * not, whether it holds it or waits for it, and it is said beside what the
 * reservation waits for. Of a pool, the units it is offered, with a box for
 * each unit it may be handed instead (D46).
 */
function NewCustodyForPickup(props: { r: Reservation; assets: Asset[]; waited: Asset[]; pools: PoolOffer[]; conflicts: ReservationConflict[]; onClose: () => void }): ReactNode {
	const items = props.assets.filter((a) => a.kind === 'item')
	const gone = [...items, ...props.waited.filter((a) => a.kind === 'item')].filter((a) => outOfUse.includes(a.status))
	const pools = props.pools.filter((p) => p.pool !== undefined && p.offered.length + p.others.length > 0)
	return (
		<NewCustody
			assets={[...items.filter((a) => !outOfUse.includes(a.status)), ...pools.flatMap((p) => p.offered)]}
			choices={pools.map((p) => ({
				label: `${p.pool?.name ?? ''}에서 ${p.units}개`,
				hint:
					p.offered.length < p.units
						? `지금 건넬 수 있는 것은 ${p.offered.length}개입니다. 나머지는 누가 가지고 있거나 그때 다른 예약·정비가 잡고 있습니다`
						: '다른 것으로 바꾸려면 상자를 고르세요. 고른 것은 이 예약이 끝날 때까지 이 예약의 것입니다',
				assets: [...p.offered, ...p.others],
			}))}
			party={idStr(props.r.party?.id)}
			reservation={props.r.id}
			due={dateOf(props.r.endsAt)}
			note={
				props.conflicts.length > 0 || gone.length > 0 ? (
					<>
						<Waiting conflicts={props.conflicts} more="이 예약이 잡지 못한 구성품은 건넬 자산에 넣지 않았습니다." short="이 예약이 잡지 못한 수량은 건넬 자산에 넣지 않았습니다." />
						<OutOfUse items={gone} more="분실·불용·폐기 상태라 건넬 자산에 넣지 않았습니다." />
					</>
				) : undefined
			}
			onClose={props.onClose}
		/>
	)
}

/**
 * What a reservation waits for (design 4): the members its kit was given
 * after it was made that something else holds then, and what it booked of a
 * pool that has fewer to give then than the reservations made before it
 * hold. It takes each once there is room, and a member it does not hold is not
 * handed out with the kit. `more` is what the place it is said in does about
 * a kit's member, and `short` about what it waits for of a pool.
 */
function Waiting(props: { conflicts: ReservationConflict[]; more?: string; short?: string }): ReactNode {
	const members = props.conflicts.filter((c) => c.units === 0)
	const pools = props.conflicts.filter((c) => c.units > 0)
	return (
		<>
			{members.length > 0 && (
				<div className="note warn" role="note" aria-label="구성품 충돌">
					<strong>구성품 충돌</strong>
					<ul className="waiting">
						{members.map((c) => (
							<li key={`${idStr(c.resourceId)}-${fmt(c.beginsAt)}`}>
								{c.resourceTag} {c.resourceName} ({fmt(c.beginsAt)} – {fmt(c.endsAt, 'time')}):{' '}
								{c.heldBy.length === 0 ? '지금은 비어 있어 곧 이 예약에 잡힙니다.' : `${heldByAll(c.heldBy)}이(가) 잡고 있습니다.`}
							</li>
						))}
					</ul>
					<p className="small">키트에 나중에 더해진 구성품입니다. 비면 이 예약이 잡습니다.</p>
					{props.more !== undefined && <p className="small">{props.more}</p>}
				</div>
			)}
			{pools.length > 0 && (
				<div className="note warn" role="note" aria-label="수량 부족">
					<strong>수량 부족</strong>
					<ul className="waiting">
						{pools.map((c) => (
							<li key={`${idStr(c.resourceId)}-${fmt(c.beginsAt)}`}>
								{c.resourceTag} {c.resourceName} ({fmt(c.beginsAt)} – {fmt(c.endsAt, 'time')}): {c.units}개 가운데{' '}
								{c.available === 0 ? '남은 것이 없습니다.' : `${c.available}개만 남아 있습니다.`}
								{c.heldBy.length > 0 && ` ${heldByAll(c.heldBy)}이(가) 잡고 있습니다.`}
							</li>
						))}
					</ul>
					<p className="small">풀에서 쓸 수 있는 것이 줄었습니다. 다시 생기면 먼저 기다린 예약부터 잡습니다.</p>
					{props.short !== undefined && <p className="small">{props.short}</p>}
				</div>
			)}
		</>
	)
}

/** What holds what a reservation waits for, as a person says it: the first few. */
function heldByAll(bs: BusySpan[]): string {
	const said = [...new Set(bs.map(heldBy))]
	return said.length > 3 ? `${said.slice(0, 3).join(', ')} 외 ${said.length - 3}건` : said.join(', ')
}

/** What holds a member a reservation waits for, or some of a pool, as a person says it. */
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
											<td>{e.partyName || c.called(e.reservation?.party?.id)}</td>
											<td>
												<Badge v={e.reservation?.status ?? ''} words={reservationWord} />
												{markWord(e) !== '' && <span className="badge warn">{markWord(e)}</span>}
											</td>
										</tr>
									))}
								</tbody>
							</table>
						</div>
					)
				}
			</Load>
			{open !== undefined && <EntryDialog e={open} onClose={() => setOpen(undefined)} />}
		</Card>
	)
}

/**
 * Every request a manager has to decide, and what each waits for. The sweep
 * lets go of a request once it has begun, so they are all from now on -- as
 * far ahead as anything may be booked. Each opens as its dialog does
 * elsewhere, with its history (D45).
 */
function Approvals(): ReactNode {
	const c = useCatalog()
	const act = useAct()
	const [open, setOpen] = useState<CalendarEntry>()
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
								const gone = e.outOfUse.map(outOfUseItem)
								// What it booked that is out of use, which it is not approved with (D43).
								const unusable = booked(gone, e.resourceIds)
								return (
									<li key={idStr(r.id)}>
										<span>
											{fmt(r.beginsAt)} – {fmt(r.endsAt, 'time')} ·{' '}
											<button className="link" onClick={() => setOpen(e)}>
												<strong>{r.name}</strong>
											</button>{' '}
											· {e.partyName || c.called(r.party?.id)}
										</span>
										<span className="inline">
											<button onClick={() => void act(ReservationService.method.reject, { ref: ref(r.id) }, { ok: '거절했습니다.' })}>거절</button>
											{unusable.length === 0 && (
												<button className="primary" onClick={() => void act(ReservationService.method.approve, { ref: ref(r.id) }, { ok: '승인했습니다.' })}>
													승인
												</button>
											)}
										</span>
										<Waiting conflicts={e.conflicts} />
										<OutOfUse items={gone} more="분실·불용·폐기되었거나 삭제되어 수령할 때 건네지 않습니다." />
										<Unusable status={r.status} items={unusable} />
									</li>
								)
							})}
						</ul>
					)
				}
			</Load>
			{open !== undefined && <EntryDialog e={open} onClose={() => setOpen(undefined)} />}
		</Card>
	)
}

/**
 * What happened to a reservation, newest first (D45): who booked it, decided
 * on it, checked into it, picked it up and brought it back, and why -- each
 * line as the server says it, its people as they are called now -- and what
 * a change to a kit or a pool it booked did to it, under that change. Its own
 * to a member, and anybody's to who runs or audits the register; nobody else
 * is offered it, and the server says no to them.
 */
function History(props: { r: Reservation }): ReactNode {
	const h = useRpc(ReservationService.method.timeline, { ref: ref(props.r.id) })
	return (
		<section className="history">
			<h3>이력</h3>
			<Load q={h}>
				{(d) =>
					d.entries.length === 0 ? (
						<Empty>남은 기록이 없습니다.</Empty>
					) : (
						<ol className="timeline compact" aria-label="이력">
							{underCauses(d.entries).map((g) => {
								const first = g.entries[0]
								return (
									<li key={idStr(first.rowId)}>
										<span className="when">{fmt(first.validFrom)}</span>
										<span className="what">
											{g.cause === '' ? <Said e={first} /> : g.cause}
											{g.cause !== '' && (
												<ul className="parts">
													{g.entries.map((e) => (
														<li key={idStr(e.rowId)}>
															<Said e={e} />
														</li>
													))}
												</ul>
											)}
										</span>
										<span className="who mute small">{first.actor}</span>
									</li>
								)
							})}
						</ol>
					)
				}
			</Load>
		</section>
	)
}

/** What an entry of a history says, and why, in the words of whoever asked. */
function Said(props: { e: TimelineEntry }): ReactNode {
	return (
		<>
			{props.e.summary}
			{props.e.reason !== '' && <span className="reason"> — {props.e.reason}</span>}
		</>
	)
}

/**
 * A history's entries, each by itself but for what one operation over many
 * things did, one after another, which goes under the operation: a change to
 * a kit, by what it says. An entry the operation says no more than -- a series
 * cancelled from one of it on, of each -- is by itself.
 */
function underCauses(es: TimelineEntry[]): { cause: string; entries: TimelineEntry[] }[] {
	const out: { cause: string; id: string; entries: TimelineEntry[] }[] = []
	for (const e of es) {
		const id = e.cause === '' ? '' : idStr(e.causeId)
		const last = out[out.length - 1]
		if (id !== '' && last !== undefined && last.id === id) last.entries.push(e)
		else out.push({ cause: e.cause, id, entries: [e] })
	}
	return out
}
