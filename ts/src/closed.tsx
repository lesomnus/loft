/**
 * A closed workspace (D29, D83): nobody uses it, its owner neither, and what
 * becomes of it -- restored, taken out, destroyed -- is the deployment's
 * operator's, whom everybody in it is told to ask. Every call into it is
 * refused, so what it says is what `GET /session` tells the page of the
 * workspace (D85): when it was closed and what goes when.
 *
 * @module
 */

import type { ReactNode } from 'react'

import { daysWord, fmt, type Session } from './api.js'

export function Closed(props: { session: Session; onSignOut: () => void }): ReactNode {
	const ws = props.session.workspace
	const on = (v?: string): string => (v === undefined ? '' : fmt(new Date(v), 'date'))
	return (
		<div className="login">
			<div className="elsewhere">
				<h2>{ws.name || ws.alias} — 닫힌 워크스페이스</h2>
				<p>{on(ws.closed)}에 닫혔습니다. 다시 열거나 데이터를 받으려면 운영자에게 문의하세요.</p>
				<p className="mute">
					복구하지 않으면 {on(ws.destroy)} 이후 이 워크스페이스의 모든 데이터가 지워지고, 주소(@{ws.alias})는 {daysWord(ws.alias_hold_days ?? 365)} 동안 아무도 쓰지 못하게 보류됩니다.
				</p>
				<p className="mute small">
					<a onClick={props.onSignOut}>로그아웃</a>
				</p>
			</div>
		</div>
	)
}
