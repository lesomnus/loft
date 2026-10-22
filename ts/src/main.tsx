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
 * On a workspace's own domain nobody signs in (rove#155): the page sends the
 * browser to the service's address (`/handoff/start`), which signs it in --
 * and asks for what the account has not agreed to yet -- and hands back a
 * session of that host's, and signing out there ends both.
 *
 * With no network the page opens as who it was last signed in as, from what
 * it last saw (`offline.ts`); signing out, or being found signed out, drops
 * everything it kept.
 *
 * @module
 */

import { StrictMode, useCallback, useEffect, useState, type ReactNode } from 'react'
import { createRoot } from 'react-dom/client'

import '../gen/domains.js'
import {
	handingOff,
	handOff,
	labelWorkspace,
	lastWorkspace,
	openWorkspace,
	rememberWorkspace,
	signedIn,
	signInThrough,
	signOutThroughHandoff,
	signOutThroughIssuer,
	waysIn,
	workspace,
	type Account,
	type Ways,
	type Workspace,
} from './api.js'
import { App } from './app.js'
import { Mark } from './marks.js'
import { forget, keepAs, keepOnly, keepTheApp, lastSeen, seen, setOffline, unreachable } from './offline.js'
import { Rpc } from './rpc.js'
import { Spinner, Toasts, useToast } from './ui.js'
import { Inbox } from './invitations.js'
import { Terminal } from './terminal.js'
import { Closed, Elsewhere } from './workspaces.js'
import { Consent } from './consent.js'
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
	const { sso, providers, password, handoff } = props.ways
	return (
		<div className="login">
			<div className="card">
				<Brand />
				{handoff === true && (
					<div className="providers">
						<button className="primary" onClick={() => handOff(true)}>
							로그인
						</button>
					</div>
				)}
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

/**
 * The workspace of the account's that a label scanned at an address naming
 * none is in (`/scan/<code>`, where `/l/<code>` lands): opened there, rather
 * than in the one last opened, which may be on a domain of its own -- that
 * domain serves its own labels alone, and could not send it on (rove#155).
 * Nothing when the address is no label's, or the label is none of the
 * account's.
 */
async function scannedIn(): Promise<Workspace | undefined> {
	const m = /^\/scan\/([^/]+)$/.exec(location.pathname)
	if (m === null) return undefined
	try {
		return await labelWorkspace(decodeURIComponent(m[1]))
	} catch {
		return undefined
	}
}

type State =
	| { at: 'checking' }
	| { at: 'out'; ways: Ways }
	| { at: 'unknown'; why: string }
	| { at: 'consent'; account: Account }
	| { at: 'elsewhere'; account: Account }
	| { at: 'inbox'; account: Account }
	| { at: 'terminal'; account: Account }
	| { at: 'closed'; account: Account; ws: Workspace }
	| { at: 'in'; account: Account }

function Boot(): ReactNode {
	const toast = useToast()
	const [state, setState] = useState<State>({ at: 'checking' })
	const [ways, setWays] = useState<Ways>({ sso: false, providers: [], password: true })
	const [n, setN] = useState(0)

	const check = useCallback(async () => {
		setState({ at: 'checking' })
		try {
			let a: Account | 'out'
			let w: Ways
			let fresh = true
			try {
				;[a, w] = await Promise.all([signedIn(), waysIn()])
			} catch (err) {
				// No network: who this browser was signed in as when the server
				// last said, and the pages as they were last seen.
				if (!unreachable(err)) throw err
				const last = await lastSeen<Account, Ways>()
				if (last === undefined) throw new Error('서버에 연결할 수 없습니다. 이 브라우저에서 아직 열어 본 적이 없어 오프라인으로 보여 줄 것이 없습니다.')
				setOffline(true)
				a = last.account
				w = last.ways
				fresh = false
			}
			setWays(w)
			if (a === 'out') {
				// Signed out, here or anywhere else: nothing of theirs stays.
				void forget()
				// On a workspace's own domain, off to be signed in at the
				// service's -- unless the last round trip came back refused,
				// which a button tries again.
				if (w.handoff === true && !new URLSearchParams(location.search).has('signin') && handOff()) return
				setState({ at: 'out', ways: w })
				return
			}
			if (fresh) {
				// Kept for this account and the workspaces it is in now, and
				// for nobody and nowhere else.
				void seen(a, w)
				void keepOnly(a.id, a.workspaces.map((v) => v.id))
			}
			// What the deployment asks of an account comes before anything
			// it has: every call is refused until it agrees (design 9.11).
			if ((a.owes ?? []).length > 0) {
				// On a workspace's own domain it is agreed to at the
				// service's, on the way back here.
				if (w.handoff === true && handOff()) return
				setState({ at: 'consent', account: a })
				return
			}
			// Signed in at the service's address for a workspace's own
			// domain, which the browser goes back to.
			const next = handingOff()
			if (next !== '') {
				location.replace(next)
				return
			}
			// The invitations waiting for the account: about no workspace, and
			// opened by the link an invitation's mail carries.
			if (location.pathname === '/invitations') {
				setState({ at: 'inbox', account: a })
				return
			}
			// A terminal asking to be signed in as the account (`rove login`).
			if (location.pathname === '/cli') {
				setState({ at: 'terminal', account: a })
				return
			}
			if (workspace === '') {
				// An address that names no workspace -- the root, or a label
				// scanned -- opens the one the label is in, or else the one
				// this browser was last in, and the first one otherwise, with
				// the rest of the address kept; but `?workspaces`, which a
				// workspace's own domain sends somebody to for the account's
				// others, asks which.
				const pick = new URLSearchParams(location.search).has('workspaces')
					? undefined
					: ((await scannedIn()) ?? a.workspaces.find((w) => w.alias === lastWorkspace()) ?? a.workspaces[0])
				if (pick !== undefined) {
					openWorkspace(pick, location.pathname + location.search, true)
					return
				}
				setState({ at: 'elsewhere', account: a })
				return
			}
			const here = a.workspaces.find((w) => w.alias === workspace)
			if (here === undefined) {
				setState({ at: 'elsewhere', account: a })
				return
			}
			if (here.closed !== undefined) {
				setState({ at: 'closed', account: a, ws: here })
				return
			}
			rememberWorkspace(workspace)
			keepAs(a.id, here.id)
			setState({ at: 'in', account: a })
		} catch (err) {
			setState({ at: 'unknown', why: err instanceof Error ? err.message : String(err) })
		}
	}, [])

	useEffect(() => {
		void check()
	}, [check, n])

	const out = useCallback(() => {
		void forget()
		if (ways.handoff === true && handOff()) return
		setState({ at: 'out', ways })
	}, [ways])
	const signOut = useCallback(async () => {
		if (!navigator.onLine) {
			toast('오프라인에서는 로그아웃할 수 없습니다. 연결되면 다시 해 보세요.', 'bad')
			return
		}
		// What this browser kept of theirs goes first, whatever comes next.
		await forget()
		if (ways.handoff === true) {
			// This host's session and the sign-in it was handed from, and on
			// to wherever that one ends.
			signOutThroughHandoff()
			return
		}
		if (ways.sso) {
			// To the issuer and back, which ends the page with it.
			signOutThroughIssuer()
			return
		}
		try {
			await fetch('/session', { method: 'DELETE' })
		} catch {
			toast('서버에 연결할 수 없어 로그아웃하지 못했습니다. 연결되면 다시 해 보세요.', 'bad')
			return
		}
		setState({ at: 'out', ways })
	}, [ways, toast])

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
		case 'consent':
			return <Consent account={state.account} onDone={() => setN((v) => v + 1)} onSignOut={signOut} />
		case 'elsewhere':
			return <Elsewhere account={state.account} onSignOut={signOut} />
		case 'inbox':
			return <Inbox account={state.account} onSignOut={signOut} />
		case 'terminal':
			return <Terminal account={state.account} onSignOut={signOut} />
		case 'closed':
			return <Closed account={state.account} ws={state.ws} onSignOut={signOut} />
		case 'in':
			return (
				<Rpc onSignedOut={out}>
					<App account={state.account} onSignOut={signOut} />
				</Rpc>
			)
	}
}

keepTheApp()

createRoot(document.getElementById('root') as HTMLElement).render(
	<StrictMode>
		<Toasts>
			<Boot />
		</Toasts>
	</StrictMode>,
)
