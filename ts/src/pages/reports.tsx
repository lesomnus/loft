/**
 * The reports (design 9.14): what the people who run the register read of
 * it, added up by the server -- what there is, what is out, how what may be
 * booked was used, what the counts found, and how the stock moved. A table
 * each, and its CSV.
 *
 * A period is days on the workspace's wall, the last one included, whatever
 * zone the browser is in: the server is asked from the first day's start and
 * before the day after the last.
 *
 * @module
 */

import { useState, type ReactNode } from 'react'
import { useNavigate, useSearchParams } from 'react-router-dom'

import type { AssetCensusRow } from '../../gen/rove/asset_svc_pb.js'
import type { Timestamp } from '@bufbuild/protobuf/wkt'
import { AssetService, BookableService, CustodyService, InventoryCountService, StockService, at, dateInput, download, errorText, fmt, fromInput, idStr, raw, shift, ts, wall } from '../api.js'
import { reasoned, ReasonModal } from '../reason.js'
import { useRpc } from '../rpc.js'
import { Card, Empty, Field, Load, PageHead, Pager, Select, Tabs, usePages, useToast } from '../ui.js'
import { countWord, custodyKindWord, findingWord, kindWord, resolutionWord, statusWord } from '../words.js'

/** The statuses a census counts by, in the order a person reads them. */
const statuses = ['ordered', 'active', 'in_repair', 'lost', 'retired', 'disposed']

export function Reports(): ReactNode {
	const [sp, setSp] = useSearchParams()
	const tab = sp.get('tab') ?? 'assets'
	const tabs = [
		{ key: 'assets', label: '자산 현황' },
		{ key: 'loans', label: '대여 현황' },
		{ key: 'bookings', label: '예약 이용률' },
		{ key: 'counts', label: '실사 결과' },
		{ key: 'stock', label: '재고 소모' },
	]
	return (
		<>
			<PageHead title="리포트" sub="서버가 데이터베이스에서 더한 숫자입니다. 표마다 CSV로 내려받을 수 있습니다." />
			<Tabs tabs={tabs} at={tab} onChange={(k) => setSp({ tab: k })} />
			{tab === 'assets' && <Census />}
			{tab === 'loans' && <Loans />}
			{tab === 'bookings' && <Bookings />}
			{tab === 'counts' && <Counts />}
			{tab === 'stock' && <Flow />}
		</>
	)
}

type Sheet = { data: Uint8Array; name: string; contentType: string }

/** Asks the server for a report's file and hands it to the browser. */
function useSave(): (get: () => Promise<Sheet>) => Promise<boolean> {
	const toast = useToast()
	return async (get) => {
		try {
			const v = await get()
			download(v.data, v.name, v.contentType)
			return true
		} catch (err) {
			toast(errorText(err), 'bad')
			return false
		}
	}
}

/** A period: its first day and its last, as a date input writes them. */
type Days = { from: string; to: string }

function today(): { y: number; m: number; d: number } {
	const w = wall(new Date())
	return { y: w.y, m: w.m, d: w.d }
}

/** The last `n` days, today among them. */
function lastDays(n: number): Days {
	const t = today()
	return { from: dateInput(at(t.y, t.m, t.d - n + 1)), to: dateInput(at(t.y, t.m, t.d)) }
}

function thisMonth(): Days {
	const t = today()
	return { from: dateInput(at(t.y, t.m, 1)), to: dateInput(at(t.y, t.m + 1, 0)) }
}

function lastMonth(): Days {
	const t = today()
	return { from: dateInput(at(t.y, t.m - 1, 1)), to: dateInput(at(t.y, t.m, 0)) }
}

/** The last year, today among them. */
function lastYear(): Days {
	const t = today()
	return { from: dateInput(at(t.y - 1, t.m, t.d + 1)), to: dateInput(at(t.y, t.m, t.d)) }
}

/** What a request says of a period: the first day's start, and the day after the last. */
function span(d: Days): { from?: Timestamp; to?: Timestamp } {
	const first = fromInput(d.from)
	const last = fromInput(d.to)
	if (Number.isNaN(first.getTime()) || Number.isNaN(last.getTime())) return {}
	return { from: ts(first), to: ts(shift(last, 1)) }
}

function Period(props: { value: Days; onChange: (v: Days) => void; year?: boolean }): ReactNode {
	const set = (k: keyof Days) => (e: { target: { value: string } }) => props.onChange({ ...props.value, [k]: e.target.value })
	return (
		<div className="filters period">
			<Field label="시작일">
				<input type="date" value={props.value.from} onChange={set('from')} />
			</Field>
			<Field label="종료일">
				<input type="date" value={props.value.to} onChange={set('to')} />
			</Field>
			<button onClick={() => props.onChange(lastDays(30))}>지난 30일</button>
			<button onClick={() => props.onChange(thisMonth())}>이번 달</button>
			<button onClick={() => props.onChange(lastMonth())}>지난달</button>
			{props.year && <button onClick={() => props.onChange(lastYear())}>지난 1년</button>}
		</div>
	)
}

/** What a period was read as, for a person. */
function said(from: Timestamp | undefined, to: Timestamp | undefined): string {
	if (from === undefined || to === undefined) return ''
	const last = new Date(Number(to.seconds) * 1000 - 1)
	return `${fmt(from, 'date')} ~ ${fmt(last, 'date')}`
}

function n(v: bigint | number): string {
	return Number(v).toLocaleString('ko-KR')
}

// --- 자산 현황 ---

function Census(): ReactNode {
	const [by, setBy] = useState('type')
	const [kind, setKind] = useState('item')
	const q = useRpc(AssetService.method.census, { by, kind })
	const save = useSave()
	return (
		<Card title="자산 현황" actions={<button onClick={() => void save(() => raw.asset.exportCensus({ by, kind }))}>CSV 내려받기</button>}>
			<p className="mute small">지금 등록된 자산을 상태별로 셉니다. 위치별은 무엇 안에 있든 그것이 있는 가장 가까운 공간에 셉니다(컴퓨터에 장착한 부품은 그 컴퓨터가 있는 방).</p>
			<div className="filters">
				<Select
					aria-label="기준"
					value={by}
					onChange={setBy}
					options={[
						{ value: 'type', label: '유형별' },
						{ value: 'location', label: '위치별' },
					]}
				/>
				<Select aria-label="종류" value={kind} onChange={setKind} options={Object.entries(kindWord).map(([value, label]) => ({ value, label }))} />
			</div>
			<Load q={q}>
				{(d) =>
					d.rows.length === 0 ? (
						<Empty>셀 자산이 없습니다.</Empty>
					) : (
						<CensusTable rows={d.rows} total={d.total} places={d.by === 'location'} at={d.at} />
					)
				}
			</Load>
		</Card>
	)
}

function CensusTable(props: { rows: AssetCensusRow[]; total: AssetCensusRow | undefined; places: boolean; at: Timestamp | undefined }): ReactNode {
	const { page, at: p, pages, go } = usePages(props.rows, 100)
	const counts = (r: AssetCensusRow): ReactNode =>
		statuses.map((s) => (
			<td key={s} className="num">
				{n(r.statuses[s] ?? 0)}
			</td>
		))
	return (
		<>
			<p>
				{fmt(props.at)} 기준 {n(props.total?.total ?? 0)}개
			</p>
			<div className="table-wrap">
				<table className="table compact" aria-label={props.places ? '위치별 자산' : '유형별 자산'}>
					<thead>
						<tr>
							<th>{props.places ? '위치' : '유형'}</th>
							{props.places && <th>태그</th>}
							<th className="num">합계</th>
							{props.places && <th className="num">하위 포함</th>}
							{statuses.map((s) => (
								<th key={s} className="num">
									{statusWord[s]}
								</th>
							))}
						</tr>
					</thead>
					<tbody>
						{page.map((r) => (
							<tr key={idStr(r.id) || r.name}>
								<td>
									{r.path.length > 0 && <span className="mute">{r.path.join(' › ')} › </span>}
									{r.name}
								</td>
								{props.places && <td className="mute">{r.tag}</td>}
								<td className="num">
									<strong>{n(r.total)}</strong>
								</td>
								{props.places && <td className="num">{n(r.within)}</td>}
								{counts(r)}
							</tr>
						))}
						{props.total !== undefined && (
							<tr>
								<td>
									<strong>합계</strong>
								</td>
								{props.places && <td />}
								<td className="num">
									<strong>{n(props.total.total)}</strong>
								</td>
								{props.places && <td className="num">{n(props.total.within)}</td>}
								{counts(props.total)}
							</tr>
						)}
					</tbody>
				</table>
			</div>
			<Pager at={p} pages={pages} go={go} />
		</>
	)
}

// --- 대여 현황 ---

function Loans(): ReactNode {
	const go = useNavigate()
	const [kind, setKind] = useState('')
	const [late, setLate] = useState('')
	const [page, setPage] = useState(0)
	const [asking, setAsking] = useState(false)
	const save = useSave()
	const size = 50
	const ask = { kind, overdue: late === 'overdue' }
	const q = useRpc(CustodyService.method.outstanding, { ...ask, size, offset: page * size })
	const narrow = (set: (v: string) => void) => (v: string) => {
		set(v)
		setPage(0)
	}
	return (
		<Card title="대여 현황" actions={<button onClick={() => setAsking(true)}>CSV 내려받기</button>}>
			<p className="mute small">지금 나가 있는 것 하나하나와 가진 사람, 반납 기한이 지났는지입니다. 연체는 워크스페이스의 날짜로 셉니다.</p>
			<div className="filters">
				<Select
					aria-label="구분"
					value={kind}
					onChange={narrow(setKind)}
					empty="지급·대여 모두"
					options={Object.entries(custodyKindWord).map(([value, label]) => ({ value, label }))}
				/>
				<Select aria-label="기한" value={late} onChange={narrow(setLate)} empty="기한 상관없이" options={[{ value: 'overdue', label: '반납 기한 지난 것만' }]} />
			</div>
			<Load q={q}>
				{(d) => (
					<>
						<p>
							나가 있는 지급·대여 {n(d.custodies)}건 (기한 지남 {n(d.overdue)}건) · 가진 사람 {n(d.people)}명 · 자산 {n(d.assets)}개 · 재고 {n(d.stocks)}줄
						</p>
						{d.rows.length === 0 ? (
							<Empty>나가 있는 것이 없습니다.</Empty>
						) : (
							<div className="table-wrap">
								<table className="table compact" aria-label="나가 있는 것">
									<thead>
										<tr>
											<th>받은 사람</th>
											<th>구분</th>
											<th>무엇</th>
											<th className="num">수량</th>
											<th>건넨 날</th>
											<th>반납 기한</th>
											<th className="num">연체</th>
											<th>인수 확인</th>
										</tr>
									</thead>
									<tbody>
										{d.rows.map((r) => (
											<tr key={idStr(r.lineId)} className="link" onClick={() => go(`/custody/${idStr(r.custodyId)}`)}>
												<td>{r.partyName}</td>
												<td>{custodyKindWord[r.kind] ?? r.kind}</td>
												<td>
													{r.assetTag !== '' && <code>{r.assetTag}</code>} {r.assetName || r.stockName}
												</td>
												<td className="num">
													{n(r.quantity)}
													{r.unit !== '' && ` ${r.unit}`}
												</td>
												<td>{fmt(r.issuedAt, 'date')}</td>
												<td className={r.overdue ? 'bad' : ''}>{r.dueAt !== undefined ? fmt(r.dueAt) : '-'}</td>
												<td className={`num ${r.overdue ? 'bad' : ''}`}>{r.overdue ? (r.overdueDays > 0 ? `${n(r.overdueDays)}일` : '오늘') : '-'}</td>
												<td>{r.acknowledged ? '확인' : <span className="warn">대기</span>}</td>
											</tr>
										))}
									</tbody>
								</table>
							</div>
						)}
						<Pager at={page} pages={Math.max(1, Math.ceil(d.total / size))} go={setPage} />
					</>
				)}
			</Load>
			{asking && (
				<ReasonModal
					title="대여 현황 내려받기"
					onClose={() => setAsking(false)}
					onReason={(why) => save(() => raw.custody.exportOutstanding(ask, reasoned(why)))}
				/>
			)}
		</Card>
	)
}

// --- 예약 이용률 ---

function Bookings(): ReactNode {
	const go = useNavigate()
	const [days, setDays] = useState(() => lastDays(30))
	const q = useRpc(BookableService.method.usage, span(days))
	const save = useSave()
	const hours = (m: bigint): string => (Number(m) / 60).toFixed(1)
	const share = (used: bigint, booked: bigint): string => (booked > 0n ? `${Math.round((100 * Number(used)) / Number(booked))}%` : '-')
	return (
		<Card title="예약 이용률" actions={<button onClick={() => void save(() => raw.bookable.exportUsage(span(days)))}>CSV 내려받기</button>}>
			<p className="mute small">
				기간 안에 시작한 예약 가운데 시간을 약속받은 것(확정·이용 중·완료·노쇼)을 셉니다. 이용 시간은 체크인이나 수령부터 반납이나 끝낸 때까지입니다. 풀은 수량만큼 곱합니다.
			</p>
			<Period value={days} onChange={setDays} />
			<Load q={q}>
				{(d) => {
					const sum = d.rows.reduce(
						(a, r) => ({ reservations: a.reservations + r.reservations, used: a.used + r.used, noShows: a.noShows + r.noShows, booked: a.booked + r.bookedMinutes, usedFor: a.usedFor + r.usedMinutes }),
						{ reservations: 0, used: 0, noShows: 0, booked: 0n, usedFor: 0n },
					)
					return d.rows.length === 0 ? (
						<Empty>예약할 수 있는 자원이 없습니다.</Empty>
					) : (
						<>
							<p>{said(d.from, d.to)}</p>
							<div className="table-wrap">
								<table className="table compact" aria-label="자원별 이용률">
									<thead>
										<tr>
											<th>자원</th>
											<th>종류</th>
											<th className="num">예약</th>
											<th className="num">이용</th>
											<th className="num">노쇼</th>
											<th className="num">예약 시간</th>
											<th className="num">이용 시간</th>
											<th className="num">이용률</th>
										</tr>
									</thead>
									<tbody>
										{d.rows.map((r) => (
											<tr key={idStr(r.resourceId)} className="link" onClick={() => go(`/assets/${idStr(r.resourceId)}`)}>
												<td>
													<code>{r.tag}</code> {r.name}
													{r.units > 1 && <span className="mute small"> ×{r.units}</span>}
												</td>
												<td>{kindWord[r.kind] ?? r.kind}</td>
												<td className="num">{n(r.reservations)}</td>
												<td className="num">{n(r.used)}</td>
												<td className={`num ${r.noShows > 0 ? 'warn' : ''}`}>{n(r.noShows)}</td>
												<td className="num">{hours(r.bookedMinutes)}</td>
												<td className="num">{hours(r.usedMinutes)}</td>
												<td className="num">{share(r.usedMinutes, r.bookedMinutes)}</td>
											</tr>
										))}
										<tr>
											<td>
												<strong>합계</strong>
											</td>
											<td />
											<td className="num">{n(sum.reservations)}</td>
											<td className="num">{n(sum.used)}</td>
											<td className="num">{n(sum.noShows)}</td>
											<td className="num">{hours(sum.booked)}</td>
											<td className="num">{hours(sum.usedFor)}</td>
											<td className="num">{share(sum.usedFor, sum.booked)}</td>
										</tr>
									</tbody>
								</table>
							</div>
						</>
					)
				}}
			</Load>
		</Card>
	)
}

// --- 실사 결과 ---

function Counts(): ReactNode {
	const go = useNavigate()
	const [days, setDays] = useState(() => lastYear())
	const q = useRpc(InventoryCountService.method.results, span(days))
	const save = useSave()
	return (
		<Card title="실사 결과" actions={<button onClick={() => void save(() => raw.count.exportResults(span(days)))}>CSV 내려받기</button>}>
			<p className="mute small">기간 안에 시작한 실사마다 무엇을 찾았는지와, 차이(위치 다름·미발견·미등록)를 어떻게 처리했는지입니다. 분실 처리가 손실입니다.</p>
			<Period value={days} onChange={setDays} year />
			<Load q={q}>
				{(d) =>
					d.rows.length === 0 ? (
						<Empty>이 기간에 시작한 실사가 없습니다.</Empty>
					) : (
						<>
							<p>{said(d.from, d.to)}</p>
							<div className="table-wrap">
								<table className="table compact" aria-label="실사별 결과">
									<thead>
										<tr>
											<th>실사</th>
											<th>범위</th>
											<th>상태</th>
											<th>시작</th>
											<th className="num">{findingWord.seen}</th>
											<th className="num">{findingWord.misplaced}</th>
											<th className="num">{findingWord.missing}</th>
											<th className="num">{findingWord.unknown}</th>
											<th className="num">{resolutionWord.moved}</th>
											<th className="num">{resolutionWord.lost}</th>
											<th className="num">{resolutionWord.ignored}</th>
											<th className="num">{resolutionWord.open}</th>
										</tr>
									</thead>
									<tbody>
										{d.rows.map((r) => (
											<tr key={idStr(r.countId)} className="link" onClick={() => go(`/counts/${idStr(r.countId)}`)}>
												<td>{r.name}</td>
												<td>{r.scopeName}</td>
												<td>{countWord[r.status] ?? r.status}</td>
												<td>{fmt(r.startedAt, 'date')}</td>
												<td className="num">{n(r.seen)}</td>
												<td className="num">{n(r.misplaced)}</td>
												<td className="num">{n(r.missing)}</td>
												<td className="num">{n(r.unknown)}</td>
												<td className="num">{n(r.moved)}</td>
												<td className={`num ${r.lost > 0 ? 'bad' : ''}`}>{n(r.lost)}</td>
												<td className="num">{n(r.ignored)}</td>
												<td className={`num ${r.unsettled > 0 ? 'warn' : ''}`}>{n(r.unsettled)}</td>
											</tr>
										))}
									</tbody>
								</table>
							</div>
						</>
					)
				}
			</Load>
		</Card>
	)
}

// --- 재고 소모 ---

function Flow(): ReactNode {
	const [days, setDays] = useState(() => lastDays(30))
	const q = useRpc(StockService.method.flow, span(days))
	const save = useSave()
	const adjusted = (up: bigint, down: bigint): string => {
		const v = up - down
		return v > 0n ? `+${n(v)}` : n(v)
	}
	return (
		<Card title="재고 소모" actions={<button onClick={() => void save(() => raw.stock.exportFlow(span(days)))}>CSV 내려받기</button>}>
			<p className="mute small">재고마다 기간 처음과 끝의 수량, 그 사이에 들어오고 나간 것입니다. 단위가 재고마다 다르므로 재고끼리 더하지 않습니다.</p>
			<Period value={days} onChange={setDays} />
			<Load q={q}>
				{(d) =>
					d.rows.length === 0 ? (
						<Empty>재고 품목이 없습니다.</Empty>
					) : (
						<>
							<p>{said(d.from, d.to)}</p>
							<div className="table-wrap">
								<table className="table compact" aria-label="재고별 입출고">
									<thead>
										<tr>
											<th>재고</th>
											<th>위치</th>
											<th className="num">기초</th>
											<th className="num">입고</th>
											<th className="num">반납</th>
											<th className="num">이동 입고</th>
											<th className="num">사용</th>
											<th className="num">지급</th>
											<th className="num">이동 출고</th>
											<th className="num">자산 전환</th>
											<th className="num">조정</th>
											<th className="num">기말</th>
										</tr>
									</thead>
									<tbody>
										{d.rows.map((r) => (
											<tr key={idStr(r.stockId)}>
												<td>
													{r.name} <span className="mute small">({r.unit})</span>
												</td>
												<td>{r.spaceName}</td>
												<td className="num">{n(r.opening)}</td>
												<td className="num">{n(r.received)}</td>
												<td className="num">{n(r.returned)}</td>
												<td className="num">{n(r.transferredIn)}</td>
												<td className="num">{n(r.consumed)}</td>
												<td className="num">{n(r.issued)}</td>
												<td className="num">{n(r.transferredOut)}</td>
												<td className="num">{n(r.converted)}</td>
												<td className="num">{adjusted(r.adjustedUp, r.adjustedDown)}</td>
												<td className="num">
													<strong>{n(r.closing)}</strong>
												</td>
											</tr>
										))}
									</tbody>
								</table>
							</div>
						</>
					)
				}
			</Load>
		</Card>
	)
}
