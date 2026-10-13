import type { ReactNode } from 'react'
import { useNavigate } from 'react-router-dom'

import { NotificationService, ago, fmt, idStr } from '../api.js'
import { useAct, useRpc } from '../rpc.js'
import { Card, Empty, Load, PageHead } from '../ui.js'

export function Notifications(): ReactNode {
	const act = useAct()
	const go = useNavigate()
	const inbox = useRpc(NotificationService.method.inbox, { size: 200 }, { poll: 60_000 })
	return (
		<>
			<PageHead
				title="알림"
				sub={inbox.data !== undefined ? `읽지 않음 ${inbox.data.unread}` : undefined}
				actions={<button onClick={() => void act(NotificationService.method.markRead, {}, { ok: '모두 읽음으로 표시했습니다.' })}>모두 읽음</button>}
			/>
			<Card>
				<Load q={inbox}>
					{(d) =>
						d.items.length === 0 ? (
							<Empty>알림이 없습니다.</Empty>
						) : (
							<ul className="rows">
								{d.items.map((n) => (
									<li
										key={idStr(n.id)}
										className={`link ${n.readAt === undefined ? 'unread' : ''}`}
										onClick={() => {
											if (n.readAt === undefined) void act(NotificationService.method.markRead, { ids: [n.id] }, { quiet: true })
											if (n.link !== '') go(n.link)
										}}
									>
										<span>
											<strong>{n.name}</strong>
											<span className="mute"> {n.desc}</span>
										</span>
										<span className="mute small" title={fmt(n.dateCreated)}>
											{ago(n.dateCreated)}
										</span>
									</li>
								))}
							</ul>
						)
					}
				</Load>
			</Card>
		</>
	)
}
