/**
 * Where the page starts: whether anybody is signed in, and then the app.
 *
 * The session is an HttpOnly cookie the server set -- at the end of a sign-in
 * through roster's issuer (`/sso/callback`), or at `POST /session` for a
 * password -- so the page never holds a credential; it only asks who it is, at
 * `GET /account`, and which workspaces that is in. Which ways in to draw is
 * the server's to say (`GET /sso`). The page is about the workspace its
 * address names (`/@<alias>/...`), and one that names none opens the last one.
 *
 * @module
 */

import { StrictMode, useCallback, useEffect, useState, type ReactNode } from 'react'
import { createRoot } from 'react-dom/client'

import '../gen/domains.js'
import {
	lastWorkspace,
	openWorkspace,
	rememberWorkspace,
	signedIn,
	signInThrough,
	signOutThroughIssuer,
	waysIn,
	workspace,
	type Account,
	type Ways,
} from './api.js'
import { App } from './app.js'
import { Mark } from './marks.js'
import { Rpc } from './rpc.js'
import { Spinner, Toasts } from './ui.js'
import { Inbox } from './invitations.js'
import { Elsewhere } from './workspaces.js'
import './style.css'

/** What a sign-in through the issuer came back as (`/?signin=`), in words. */
const cameBack: Record<string, string> = {
	refused: '로그인을 취소했습니다.',
	failed: '로그인하지 못했습니다. 다시 해 보세요.',
	unavailable: '지금은 로그인을 확인할 수 없습니다. 잠시 뒤에 다시 해 보세요.',
}

/** What the last round trip came to, read once and taken out of the address. */
function signinOutcome(): string {
	const q = new URLSearchParams(location.search)
	const v = q.get('signin')
	if (v === null) return ''
	q.delete('signin')
	const rest = q.toString()
	history.replaceState(null, '', location.pathname + (rest === '' ? '' : '?' + rest) + location.hash)
	return cameBack[v] ?? cameBack['failed'] ?? ''
}

function Brand(): ReactNode {
	return (
		<div className="brand">
			<img src="/icon.svg" alt="" />
			<div>
				<h1>Rove</h1>
				<p>자산 · 공간 관리</p>
			</div>
		</div>
	)
}

function Login(props: { ways: Ways; onIn: () => void }): ReactNode {
	const [busy, setBusy] = useState(false)
	const [bad, setBad] = useState(signinOutcome)
	const { sso, providers, password } = props.ways
	return (
		<div className="login">
			<div className="card">
				<Brand />
				{sso && (
					<div className="providers">
						{providers.length === 0 ? (
							<button className="primary" onClick={() => signInThrough()}>
								로그인
							</button>
						) : (
							providers.map((p) => (
								<button key={p.connection} className="provider" onClick={() => signInThrough(p.connection)}>
									<Mark of={p.mark} />
									<span>{p.label} 계정으로 로그인</span>
								</button>
							))
						)}
					</div>
				)}
				{sso && password && <p className="or">또는 아이디로</p>}
				{password && <PasswordForm busy={busy} setBusy={setBusy} setBad={setBad} onIn={props.onIn} />}
				{bad !== '' && <p className="bad">{bad}</p>}
			</div>
		</div>
	)
}

function PasswordForm(props: {
	busy: boolean
	setBusy: (v: boolean) => void
	setBad: (v: string) => void
	onIn: () => void
}): ReactNode {
	const { busy, setBusy, setBad } = props
	return (
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
			<label>
				아이디 또는 이메일
				<input name="login" autoComplete="username" autoFocus required />
			</label>
			<label>
				비밀번호
				<input name="password" type="password" autoComplete="current-password" required />
			</label>
			<button className="primary" disabled={busy}>
				{busy ? '확인 중…' : '로그인'}
			</button>
		</form>
	)
}

type State =
	| { at: 'checking' }
	| { at: 'out'; ways: Ways }
	| { at: 'unknown'; why: string }
	| { at: 'elsewhere'; account: Account }
	| { at: 'inbox'; account: Account }
	| { at: 'in'; account: Account }

function Boot(): ReactNode {
	const [state, setState] = useState<State>({ at: 'checking' })
	const [ways, setWays] = useState<Ways>({ sso: false, providers: [], password: true })
	const [n, setN] = useState(0)

	const check = useCallback(async () => {
		setState({ at: 'checking' })
		try {
			const [a, w] = await Promise.all([signedIn(), waysIn()])
			setWays(w)
			if (a === 'out') {
				setState({ at: 'out', ways: w })
				return
			}
			// The invitations waiting for the account: about no workspace, and
			// opened by the link an invitation's mail carries.
			if (location.pathname === '/invitations') {
				setState({ at: 'inbox', account: a })
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

	const out = useCallback(() => setState({ at: 'out', ways }), [ways])
	const signOut = useCallback(async () => {
		if (ways.sso) {
			// To the issuer and back, which ends the page with it.
			signOutThroughIssuer()
			return
		}
		await fetch('/session', { method: 'DELETE' })
		setState({ at: 'out', ways })
	}, [ways])

	switch (state.at) {
		case 'checking':
			return (
				<div className="boot">
					<Spinner />
				</div>
			)
		case 'out':
			return <Login ways={state.ways} onIn={() => setN((v) => v + 1)} />
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
		case 'inbox':
			return <Inbox account={state.account} onSignOut={signOut} />
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
