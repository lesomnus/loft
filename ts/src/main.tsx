/**
 * Where the page starts: whether anybody is signed in, and then the app.
 *
 * The session is an HttpOnly cookie the server set -- at the end of a sign-in
 * through roster's issuer (`/sso/callback`), or at `POST /session` for a
 * password -- so the page never holds a credential; it only asks who it is,
 * and the workspace, at `GET /session`. Which ways in to draw is the server's
 * to say (`GET /sso`). The page is about the one workspace the deployment
 * serves (D85).
 *
 * With no network the page opens as who it was last signed in as, from what
 * it last saw (`offline.ts`); signing out, or being found signed out, drops
 * everything it kept.
 *
 * @module
 */

import { StrictMode, useCallback, useEffect, useRef, useState, type ReactNode } from 'react'
import { createRoot } from 'react-dom/client'

import '../gen/domains.js'
import { callAs, localize, signedIn, signInThrough, signOutThroughIssuer, waysIn, type Session, type Ways } from './api.js'
import { App } from './app.js'
import { Mark } from './marks.js'
import { forget, keepAs, keepOnly, keepTheApp, lastSeen, letGo, seen, setOffline, unreachable } from './offline.js'
import { Rpc } from './rpc.js'
import { Spinner, Toasts, useToast } from './ui.js'
import { Terminal } from './terminal.js'
import { Closed } from './closed.js'
import './style.css'

/** What a sign-in through the issuer came back as (`/?signin=`), in words. */
const cameBack: Record<string, string> = {
	refused: '로그인을 취소했습니다.',
	failed: '로그인하지 못했습니다. 다시 해 보세요.',
	unavailable: '지금은 로그인을 확인할 수 없습니다. 잠시 뒤에 다시 해 보세요.',
	ended: '이 워크스페이스에서 로그인이 중지된 사람입니다. 관리자에게 문의하세요.',
	// Somebody roster binds no role of Loft's (D89): what they may do is the
	// roles the tenant's administrators bind them at roster.
	norole: 'Loft에서 받은 역할이 없습니다. roster에서 이 워크스페이스의 관리자에게 역할을 받으세요.',
}

/** What `POST /session` answers beside 403 for somebody roster binds no role of Loft's (`session.NoRole`). */
const noRole = "no role of loft's at roster"

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
				<h1>Loft</h1>
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
					const why = res.status === 403 ? (await res.text()).trim() : ''
					setBad(
						res.status === 401
							? '아이디 또는 비밀번호가 맞지 않습니다.'
							: res.status === 403
								? (cameBack[why === noRole ? 'norole' : 'ended'] ?? '')
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
 * Where a sign-out in one tab is said to every other tab of this address: each
 * lets go of what it holds and asks again who is signed in. What this browser
 * kept was forgotten by the tab that signed out (`forget`), for all of them.
 */
const signedOut: BroadcastChannel | undefined = typeof BroadcastChannel === 'undefined' ? undefined : new BroadcastChannel('loft/signed-out')

type State =
	| { at: 'checking' }
	| { at: 'out'; ways: Ways }
	| { at: 'unknown'; why: string }
	| { at: 'terminal'; session: Session }
	| { at: 'closed'; session: Session }
	| { at: 'in'; session: Session }

function Boot(): ReactNode {
	const toast = useToast()
	const [state, setState] = useState<State>({ at: 'checking' })
	const [ways, setWays] = useState<Ways>({ sso: false, providers: [], password: true })
	const [n, setN] = useState(0)
	// Whose the page is drawn for: the name `GET /session` gave the session it
	// was drawn as, which every call says (`callAs`). Empty for nobody.
	const drawn = useRef('')

	const check = useCallback(async () => {
		setState({ at: 'checking' })
		try {
			let a: Session | 'out'
			let w: Ways
			let fresh = true
			try {
				;[a, w] = await Promise.all([signedIn(), waysIn()])
			} catch (err) {
				// No network: who this browser was signed in as when the server
				// last said, and the pages as they were last seen.
				if (!unreachable(err)) throw err
				const last = await lastSeen<Session, Ways>()
				if (last === undefined) throw new Error('서버에 연결할 수 없습니다. 이 브라우저에서 아직 열어 본 적이 없어 오프라인으로 보여 줄 것이 없습니다.')
				setOffline(true)
				a = last.session
				w = last.ways
				fresh = false
			}
			setWays(w)
			if (a === 'out') {
				// Signed out, or the session ended: the page is nobody's, and
				// keeps and serves nothing as anybody. What they made and could
				// not send waits for them, as a sign-out would not (`letGo`).
				drawn.current = ''
				callAs('')
				void letGo()
				setState({ at: 'out', ways: w })
				return
			}
			drawn.current = a.identity ?? ''
			callAs(drawn.current)
			// Where the dates are, before anything is drawn: the workspace's
			// zone, as the server tells it, and never the browser's (design
			// 7.9) -- for what a closed one says too.
			localize({ timezone: a.workspace.timezone })
			if (fresh) {
				// Kept for this member and the workspace, and for nobody and
				// nowhere else.
				void seen(a, w)
				void keepOnly(a.id, [a.workspace.id])
			}
			// A terminal asking to be signed in as the member (`loft auth login`).
			if (location.pathname === '/cli') {
				setState({ at: 'terminal', session: a })
				return
			}
			if (a.workspace.closed !== undefined) {
				setState({ at: 'closed', session: a })
				return
			}
			keepAs(a.id, a.workspace.id)
			setState({ at: 'in', session: a })
		} catch (err) {
			setState({ at: 'unknown', why: err instanceof Error ? err.message : String(err) })
		}
	}, [])

	useEffect(() => {
		void check()
	}, [check, n])

	// Starting again as whoever is signed in now, holding nothing for the
	// one the page was drawn for.
	const again = useCallback(() => {
		drawn.current = ''
		callAs('')
		keepAs('', '')
		setN((v) => v + 1)
	}, [])

	// A call refused for its session (`Unauthenticated`) asks again who is
	// signed in. Still this page's caller, and it was refused for something
	// else: starting the page again would only lose what is on it. Anybody
	// else, or nobody -- a session that ended, or somebody else's sign-in in
	// another tab -- and the page starts again as whoever that is: the sign-in,
	// with what the first person could not send waiting for them, or the other
	// person's page, which keeps nothing of the first's (`keepOnly`).
	const asking = useRef(false)
	const lost = useCallback(() => {
		if (asking.current || drawn.current === '') return
		asking.current = true
		const was = drawn.current
		signedIn()
			.then((a) => {
				if (a === 'out' || a.identity !== was) again()
			})
			.catch(() => {
				// Not answered: the next call that is refused asks again.
			})
			.finally(() => {
				asking.current = false
			})
	}, [again])

	// Another tab signed out.
	useEffect(() => {
		if (signedOut === undefined) return
		signedOut.addEventListener('message', again)
		return () => signedOut.removeEventListener('message', again)
	}, [again])

	const signOut = useCallback(async () => {
		if (!navigator.onLine) {
			toast('오프라인에서는 로그아웃할 수 없습니다. 연결되면 다시 해 보세요.', 'bad')
			return
		}
		// What this browser kept of theirs goes first, whatever comes next,
		// and every other tab lets go of what it holds (`signedOut`) once the
		// session is over -- or on the way to where it ends.
		const was = drawn.current
		drawn.current = ''
		callAs('')
		await forget()
		if (ways.sso) {
			// To the issuer and back, which ends the page with it.
			signedOut?.postMessage(was)
			signOutThroughIssuer()
			return
		}
		try {
			await fetch('/session', { method: 'DELETE' })
		} catch {
			drawn.current = was
			callAs(was)
			toast('서버에 연결할 수 없어 로그아웃하지 못했습니다. 연결되면 다시 해 보세요.', 'bad')
			return
		}
		signedOut?.postMessage(was)
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
		case 'terminal':
			return <Terminal session={state.session} onSignOut={signOut} />
		case 'closed':
			return <Closed session={state.session} onSignOut={signOut} />
		case 'in':
			return (
				<Rpc onRefused={lost}>
					<App session={state.session} onSignOut={signOut} />
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
