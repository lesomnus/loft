/**
 * Where the page starts: whether anybody is signed in, and then the app.
 *
 * The session is an HttpOnly cookie the server set at `POST /session`, so the
 * page never holds a credential; it only asks who it is, at `GET /account`,
 * and which workspaces that is in. The page is about the workspace its address
 * names (`/@<alias>/...`), and one that names none opens the last one.
 *
 * @module
 */

import { StrictMode, useCallback, useEffect, useState, type ReactNode } from 'react'
import { createRoot } from 'react-dom/client'

import '../gen/domains.js'
import { lastWorkspace, openWorkspace, rememberWorkspace, signedIn, workspace, type Account } from './api.js'
import { App } from './app.js'
import { Rpc } from './rpc.js'
import { Spinner, Toasts } from './ui.js'
import { Elsewhere } from './workspaces.js'
import './style.css'

function Login(props: { onIn: () => void }): ReactNode {
	const [busy, setBusy] = useState(false)
	const [bad, setBad] = useState('')
	return (
		<div className="login">
			<form
				onSubmit={async (e) => {
					e.preventDefault()
					const f = new FormData(e.currentTarget)
					setBusy(true)
					setBad('')
					try {
						const res = await fetch('/session', {
							method: 'POST',
							headers: { 'content-type': 'application/json' },
							body: JSON.stringify({
								login: String(f.get('login') ?? ''),
								password: String(f.get('password') ?? ''),
							}),
						})
						if (res.status === 204) {
							props.onIn()
							return
						}
						setBad(
							res.status === 401
								? '아이디 또는 비밀번호가 맞지 않습니다.'
								: res.status === 503
									? '지금은 로그인을 확인할 수 없습니다. 잠시 뒤에 다시 해 보세요.'
									: `로그인할 수 없습니다 (${res.status}).`,
						)
					} catch {
						setBad('서버에 연결할 수 없습니다.')
					} finally {
						setBusy(false)
					}
				}}
			>
				<div className="brand">
					<img src="/icon.svg" alt="" />
					<div>
						<h1>Rove</h1>
						<p>자산 · 공간 관리</p>
					</div>
				</div>
				<label>
					아이디 또는 이메일
					<input name="login" autoComplete="username" autoFocus required />
				</label>
				<label>
					비밀번호
					<input name="password" type="password" autoComplete="current-password" required />
				</label>
				{bad !== '' && <p className="bad">{bad}</p>}
				<button className="primary" disabled={busy}>
					{busy ? '확인 중…' : '로그인'}
				</button>
			</form>
		</div>
	)
}

type State =
	| { at: 'checking' }
	| { at: 'out' }
	| { at: 'unknown'; why: string }
	| { at: 'elsewhere'; account: Account }
	| { at: 'in'; account: Account }

function Boot(): ReactNode {
	const [state, setState] = useState<State>({ at: 'checking' })
	const [n, setN] = useState(0)

	const check = useCallback(async () => {
		setState({ at: 'checking' })
		try {
			const a = await signedIn()
			if (a === 'out') {
				setState({ at: 'out' })
				return
			}
			if (workspace === '') {
				// An address that names no workspace -- the root, or a label
				// scanned -- opens the one this browser was last in, and the
				// first one otherwise, with the rest of the address kept.
				const pick = a.workspaces.find((w) => w.alias === lastWorkspace()) ?? a.workspaces[0]
				if (pick !== undefined) {
					openWorkspace(pick.alias, location.pathname + location.search, true)
					return
				}
				setState({ at: 'elsewhere', account: a })
				return
			}
			if (!a.workspaces.some((w) => w.alias === workspace)) {
				setState({ at: 'elsewhere', account: a })
				return
			}
			rememberWorkspace(workspace)
			setState({ at: 'in', account: a })
		} catch (err) {
			setState({ at: 'unknown', why: err instanceof Error ? err.message : String(err) })
		}
	}, [])

	useEffect(() => {
		void check()
	}, [check, n])

	const out = useCallback(() => setState({ at: 'out' }), [])
	const signOut = useCallback(async () => {
		await fetch('/session', { method: 'DELETE' })
		setState({ at: 'out' })
	}, [])

	switch (state.at) {
		case 'checking':
			return (
				<div className="boot">
					<Spinner />
				</div>
			)
		case 'out':
			return <Login onIn={() => setN((v) => v + 1)} />
		case 'unknown':
			return (
				<div className="login">
					<div className="elsewhere">
						<p className="bad">{state.why}</p>
						<button className="primary" onClick={() => setN((v) => v + 1)}>
							다시 시도
						</button>
					</div>
				</div>
			)
		case 'elsewhere':
			return <Elsewhere account={state.account} onSignOut={signOut} />
		case 'in':
			return (
				<Rpc onSignedOut={out}>
					<App account={state.account} onSignOut={signOut} />
				</Rpc>
			)
	}
}

createRoot(document.getElementById('root') as HTMLElement).render(
	<StrictMode>
		<Toasts>
			<Boot />
		</Toasts>
	</StrictMode>,
)
