/**
 * A terminal asking to be signed in as this account: `rove login` printed
 * this page's address with a code, and the person approves it here, signed in
 * (`server/clilogin`).
 *
 * A page about no workspace, like the inbox: what it signs in is the account.
 *
 * @module
 */

import { useEffect, useState, type ReactNode } from 'react'

import { fmt, type Account } from './api.js'

/** What the server shows of a terminal waiting. */
type Asking = {
	code: string
	client: string
	origin: string
	created: string
	expires: string
}

type State =
	| { at: 'loading' }
	| { at: 'asking'; v: Asking }
	| { at: 'gone' }
	| { at: 'approved' }
	| { at: 'denied' }
	| { at: 'failed'; why: string }

export function Terminal(props: { account: Account; onSignOut: () => void }): ReactNode {
	const code = new URLSearchParams(location.search).get('code') ?? ''
	const [state, setState] = useState<State>({ at: 'loading' })
	const [busy, setBusy] = useState(false)

	useEffect(() => {
		let gone = false
		void (async () => {
			const res = await fetch('/cli/logins?code=' + encodeURIComponent(code))
			if (gone) return
			if (res.status === 404) {
				setState({ at: 'gone' })
				return
			}
			if (!res.ok) {
				setState({ at: 'failed', why: (await res.text()) || res.statusText })
				return
			}
			setState({ at: 'asking', v: (await res.json()) as Asking })
		})()
		return () => {
			gone = true
		}
	}, [code])

	const answer = async (to: 'approve' | 'deny'): Promise<void> => {
		setBusy(true)
		try {
			const res = await fetch('/cli/logins/' + to, {
				method: 'POST',
				headers: { 'content-type': 'application/json' },
				body: JSON.stringify({ code }),
			})
			if (res.status === 404 || res.status === 409) {
				setState({ at: 'gone' })
				return
			}
			if (!res.ok) {
				setState({ at: 'failed', why: (await res.text()) || res.statusText })
				return
			}
			setState({ at: to === 'approve' ? 'approved' : 'denied' })
		} finally {
			setBusy(false)
		}
	}

	return (
		<div className="login">
			<div className="card terminal">
				<h2>터미널 로그인</h2>
				{state.at === 'loading' && <p className="mute">불러오는 중…</p>}
				{state.at === 'gone' && <p>이 요청은 없거나 시간이 지났습니다. 터미널에서 <code>rove login</code>을 다시 실행하세요.</p>}
				{state.at === 'failed' && <p className="bad">{state.why}</p>}
				{state.at === 'approved' && <p>승인했습니다. 터미널로 돌아가세요.</p>}
				{state.at === 'denied' && <p>거절했습니다. 터미널은 로그인되지 않습니다.</p>}
				{state.at === 'asking' && (
					<>
						<p>
							<strong>{props.account.login}</strong> 계정으로 터미널이 로그인하려고 합니다.
						</p>
						<dl className="kv">
							<dt>코드</dt>
							<dd>
								<code className="big">{state.v.code}</code>
							</dd>
							<dt>터미널</dt>
							<dd>{state.v.client}</dd>
							<dt>요청한 곳</dt>
							<dd>{state.v.origin}</dd>
							<dt>요청 시각</dt>
							<dd>{fmt(new Date(state.v.created))}</dd>
						</dl>
						<p className="bad">
							방금 내 컴퓨터의 터미널에서 <code>rove login</code>을 실행했고, 터미널에 같은 코드가 보일 때만 승인하세요. 누군가 이 주소를 보내며 승인해 달라고 했다면 거절하세요.
						</p>
						<div className="actions">
							<button disabled={busy} onClick={() => void answer('deny')}>
								거절
							</button>
							<button className="primary" disabled={busy} onClick={() => void answer('approve')}>
								승인
							</button>
						</div>
					</>
				)}
				<p className="mute small">
					<a href="/">Rove로 돌아가기</a> · <a onClick={props.onSignOut}>로그아웃</a>
				</p>
			</div>
		</div>
	)
}
