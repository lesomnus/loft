import { useState, type ReactNode } from 'react'
import { Link, useNavigate } from 'react-router-dom'

import type { CalendarEntry } from '../../gen/loft/booking_svc_pb.js'
import { AssetService, CustodyService, NotificationService, ReservationService, ago, fmt, idStr, minute, ref, ts } from '../api.js'
import { useCatalog } from '../catalog.js'
import { AssetLink } from '../pickers.js'
import { useRpc } from '../rpc.js'
import { Badge, Card, Empty, Load, PageHead, Stat } from '../ui.js'
import { kindWord, markWord, reservationWord, statusWord } from '../words.js'
import { EntryDialog } from './reservations.js'

export function Dashboard(): ReactNode {
	const c = useCatalog()
	const go = useNavigate()
	const summary = useRpc(AssetService.method.report, { kind: 'summary' }, { poll: 120_000 })
	const low = useRpc(AssetService.method.report, { kind: 'stock' })
	const mine = useRpc(
		AssetService.method.search,
		c.me.party === undefined ? null : { custodian: ref(c.me.party.id), size: 50 },
	)
	const now = minute()
	const week = new Date(now.getTime() + 7 * 86400_000)
	const cal = useRpc(ReservationService.method.calendar, { from: ts(now), to: ts(week), mine: true })
	// The requests a manager decides, as the approvals tab reads them: the
	// sweep lets go of one once it has begun, so all of them are from now on.
	const pending = useRpc(
		ReservationService.method.calendar,
		c.can('manager') ? { statuses: ['requested'], from: ts(new Date(now.getTime() - 86400_000)), to: ts(new Date(now.getTime() + 3650 * 86400_000)) } : null,
	)
	// A reservation listed here opens as it does wherever it is listed (D45).
	const [open, setOpen] = useState<CalendarEntry>()
	const inbox = useRpc(NotificationService.method.inbox, { size: 6 }, { poll: 60_000 })
	// What is late, by who has it as a list tells people apart and by how
	// many of the workspace's days, as the report of what is out says both
	// (D54): due earlier today is late, and no day late yet.
	const overdue = useRpc(AssetService.method.report, c.can('manager') ? { kind: 'custody' } : null)
	const openCustody = useRpc(CustodyService.method.list, c.me.party === undefined ? null : { filters: [{ party: ref(c.me.party.id) }], size: 50 })

	const v = (group: string, key: string) =>
		summary.data?.rows.find((r) => r.group === group && r.key === key)?.value ?? 0

	return (
		<>
			<PageHead title={`안녕하세요, ${c.me.party?.name ?? c.me.holder?.alias ?? ''}님`} sub={c.me.workspaceName} />
			<div className="stats">
				<Stat label="물품" value={v('kind', 'item')} onClick={() => go('/assets?kind=item')} />
				<Stat label="공간" value={v('kind', 'space')} onClick={() => go('/spaces')} />
				<Stat label="수리 중" value={v('status', 'in_repair')} tone="warn" onClick={() => go('/assets?status=in_repair')} />
				<Stat label="분실" value={v('status', 'lost')} tone="bad" onClick={() => go('/assets?status=lost')} />
				<Stat label="지급·대여 중" value={v('custody', 'open')} onClick={() => go('/custody')} />
				<Stat label="반납 기한 지남" value={v('custody', 'overdue')} tone={v('custody', 'overdue') > 0 ? 'bad' : ''} onClick={() => go('/custody?overdue=1')} />
				<Stat label="진행 중 작업" value={v('work', 'open')} onClick={() => go('/work')} />
				<Stat label="재고 부족" value={low.data?.rows.length ?? 0} tone={(low.data?.rows.length ?? 0) > 0 ? 'warn' : ''} onClick={() => go('/stock')} />
			</div>

			<div className="grid2">
				<Card title="내가 가진 자산" actions={<Link to="/me">전체</Link>}>
					<Load q={mine}>
						{(d) =>
							d.items.length === 0 ? (
								<Empty>지급받은 자산이 없습니다.</Empty>
							) : (
								<ul className="rows">
									{d.items.slice(0, 8).map((a) => (
										<li key={idStr(a.id)}>
											<AssetLink a={a} />
											<Badge v={a.status} words={statusWord} />
										</li>
									))}
								</ul>
							)
						}
					</Load>
					{(openCustody.data?.items ?? []).some((x) => x.status === 'open' && x.acknowledgedAt === undefined) && (
						<p className="note warn">
							인수 확인을 기다리는 지급이 있습니다. <Link to="/me">확인하기</Link>
						</p>
					)}
				</Card>

				<Card title="이번 주 내 예약" actions={<Link to="/reservations">예약하기</Link>}>
					<Load q={cal}>
						{(d) =>
							d.entries.length === 0 ? (
								<Empty>예정된 예약이 없습니다.</Empty>
							) : (
								<ul className="rows">
									{d.entries.slice(0, 8).map((e) => (
										<li key={idStr(e.reservation?.id)}>
											<span>
												{fmt(e.reservation?.beginsAt)} ·{' '}
												<button className="link" onClick={() => setOpen(e)}>
													{e.reservation?.name}
												</button>
												{markWord(e) !== '' && <span className="warn"> · {markWord(e)}</span>}
											</span>
											<Badge v={e.reservation?.status ?? ''} words={reservationWord} />
										</li>
									))}
								</ul>
							)
						}
					</Load>
				</Card>

				{c.can('manager') && (
					<Card title="승인 대기 예약" actions={<Link to="/reservations?tab=approve">처리</Link>}>
						<Load q={pending}>
							{(d) =>
								d.entries.length === 0 ? (
									<Empty>대기 중인 요청이 없습니다.</Empty>
								) : (
									<ul className="rows">
										{d.entries.map((e) => (
											<li key={idStr(e.reservation?.id)}>
												<span>
													{fmt(e.reservation?.beginsAt)} ·{' '}
													<button className="link" onClick={() => setOpen(e)}>
														{e.reservation?.name}
													</button>
												</span>
												<span className="mute">{e.partyName || c.called(e.reservation?.party?.id)}</span>
											</li>
										))}
									</ul>
								)
							}
						</Load>
					</Card>
				)}

				{c.can('manager') && (
					<Card title="연체된 대여" actions={<Link to="/custody?overdue=1">전체</Link>}>
						<Load q={overdue}>
							{(d) => {
								const rows = d.rows.filter((r) => r.group === 'overdue')
								return rows.length === 0 ? (
									<Empty>연체가 없습니다.</Empty>
								) : (
									<ul className="rows">
										{rows.map((r) => (
											<li key={r.key}>
												<Link to={`/custody/${r.key}`}>{r.label}</Link>
												<span className="bad">{r.value > 0 ? `${r.value}일 지남` : '오늘 지남'}</span>
											</li>
										))}
									</ul>
								)
							}}
						</Load>
					</Card>
				)}

				<Card title="재고 부족" actions={<Link to="/stock">재고</Link>}>
					<Load q={low}>
						{(d) =>
							d.rows.length === 0 ? (
								<Empty>모두 기준 이상입니다.</Empty>
							) : (
								<ul className="rows">
									{d.rows.map((r) => (
										<li key={r.key}>
											<span>{r.label}</span>
											<span className="warn">
												{r.value} / 기준 {r.detail['threshold']}
											</span>
										</li>
									))}
								</ul>
							)
						}
					</Load>
				</Card>

				<Card title="최근 알림" actions={<Link to="/notifications">전체</Link>}>
					<Load q={inbox}>
						{(d) =>
							d.items.length === 0 ? (
								<Empty>알림이 없습니다.</Empty>
							) : (
								<ul className="rows">
									{d.items.map((n) => (
										<li key={idStr(n.id)} className={n.readAt === undefined ? 'unread' : ''}>
											<span>
												<strong>{n.name}</strong> {n.desc}
											</span>
											<span className="mute">{ago(n.dateCreated)}</span>
										</li>
									))}
								</ul>
							)
						}
					</Load>
				</Card>

				<Card title="유형별 자산">
					<Load q={summary}>
						{(d) => (
							<ul className="bars">
								{d.rows
									.filter((r) => r.group === 'type')
									.sort((a, b) => b.value - a.value)
									.slice(0, 10)
									.map((r) => {
										const max = Math.max(...d.rows.filter((x) => x.group === 'type').map((x) => x.value), 1)
										return (
											<li key={r.key}>
												<span className="name">{r.label}</span>
												<span className="bar">
													<span style={{ width: `${(100 * r.value) / max}%` }} />
												</span>
												<span className="num">{r.value}</span>
											</li>
										)
									})}
							</ul>
						)}
					</Load>
					<p className="mute small">
						{Object.entries(kindWord)
							.map(([k, w]) => `${w} ${v('kind', k)}`)
							.join(' · ')}
					</p>
				</Card>
			</div>
			{open !== undefined && <EntryDialog e={open} onClose={() => setOpen(undefined)} />}
		</>
	)
}
