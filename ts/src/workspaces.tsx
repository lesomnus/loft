/**
 * Which workspace a page is about, and going to another (design 9.11).
 *
 * A workspace is opened by loading the page at its address, `/@<alias>/...`,
 * rather than by routing inside the page: every read a page made is about the
 * workspace it was opened in, and a new page has none of them.
 *
 * @module
 */

import { useState, type ReactNode } from 'react'

import { makeWorkspace, openWorkspace, workspace, type Account } from './api.js'
import { useWaiting } from './invitations.js'

/** The form that makes a workspace and opens it. */
function Make(props: { onCancel?: () => void }): ReactNode {
	const [name, setName] = useState('')
	const [busy, setBusy] = useState(false)
	const [bad, setBad] = useState('')
	return (
		<form
			className="make-workspace"
			onSubmit={async (e) => {
				e.preventDefault()
				setBusy(true)
				setBad('')
				try {
					const w = await makeWorkspace(name.trim())
					openWorkspace(w.alias)
				} catch (err) {
					setBad(err instanceof Error ? err.message : String(err))
					setBusy(false)
				}
			}}
		>
			<input value={name} onChange={(e) => setName(e.target.value)} placeholder="워크스페이스 이름" maxLength={80} required autoFocus />
			<button className="primary" disabled={busy || name.trim() === ''}>
				{busy ? '만드는 중…' : '만들기'}
			</button>
			{props.onCancel !== undefined && (
				<button type="button" className="link" onClick={props.onCancel}>
					취소
				</button>
			)}
			{bad !== '' && <p className="bad small">{bad}</p>}
		</form>
	)
}

/** The workspaces an account is in, for going to another, and making one. */
export function Switcher(props: { account: Account; current: string }): ReactNode {
	const [making, setMaking] = useState(false)
	const waiting = useWaiting(props.account)
	const full = props.account.may !== undefined && props.account.owns >= props.account.may
	return (
		<details className="switcher">
			<summary>
				{props.current}
				{waiting > 0 && <span className="dot" title={`받은 초대 ${waiting}개`} />}
			</summary>
			<ul>
				{props.account.workspaces.map((w) => (
					<li key={w.id}>
						<button className="link" aria-current={w.alias === workspace ? 'page' : undefined} onClick={() => openWorkspace(w.alias)}>
							<span>{w.name || w.alias}</span>
							<span className="mute small">@{w.alias}</span>
						</button>
					</li>
				))}
			</ul>
			<a className="link invites" href="/invitations">
				받은 초대{waiting > 0 && <span className="badge info">{waiting}</span>}
			</a>
			{making ? (
				<Make onCancel={() => setMaking(false)} />
			) : (
				<button className="link" onClick={() => setMaking(true)} title={full ? '더 만들려면 결제가 필요합니다' : undefined}>
					+ 새 워크스페이스
				</button>
			)}
		</details>
	)
}

/** What a signed-in account sees when its address names no workspace it is in. */
export function Elsewhere(props: { account: Account; onSignOut: () => void }): ReactNode {
	const named = workspace !== '' && !props.account.workspaces.some((w) => w.alias === workspace)
	return (
		<div className="login">
			<div className="elsewhere">
				<div className="brand">
					<img src="/icon.svg" alt="" />
					<div>
						<h1>Rove</h1>
						<p>{props.account.name || props.account.login}</p>
					</div>
				</div>
				{named && <p className="bad">@{workspace}에 들어갈 수 없습니다.</p>}
				{props.account.workspaces.length === 0 ? (
					<p>아직 워크스페이스가 없습니다. 하나 만들어 시작하세요.</p>
				) : (
					<>
						<p>들어갈 워크스페이스를 고르세요.</p>
						<ul>
							{props.account.workspaces.map((w) => (
								<li key={w.id}>
									<button className="link" onClick={() => openWorkspace(w.alias)}>
										{w.name || w.alias} <span className="mute small">@{w.alias}</span>
									</button>
								</li>
							))}
						</ul>
					</>
				)}
				<Make />
				<a className="link" href="/invitations">
					받은 초대 보기
				</a>
				<button className="link" onClick={props.onSignOut}>
					로그아웃
				</button>
			</div>
		</div>
	)
}
