import { useState, type ReactNode } from 'react'

import { agree, type Account } from './api.js'

/**
 * What an account agrees to before it uses this deployment (design 9.11): each
 * document at the version in force, read where it is linked. Nothing else is
 * served to it until it has -- every call is refused -- so this is the first
 * page it sees, and the one it sees again when a document changes.
 */
export function Consent(props: { account: Account; onDone: () => void; onSignOut: () => void }): ReactNode {
	const owes = props.account.owes ?? []
	const [checked, setChecked] = useState<Record<string, boolean>>({})
	const [busy, setBusy] = useState(false)
	const [bad, setBad] = useState('')
	const all = owes.every((d) => checked[d.name] === true)
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
				<p>Rove를 쓰려면 아래 문서에 동의해야 합니다.</p>
				<ul className="consent">
					{owes.map((d) => (
						<li key={d.name}>
							<label className="check">
								<input type="checkbox" checked={checked[d.name] === true} onChange={(ev) => setChecked((v) => ({ ...v, [d.name]: ev.target.checked }))} />{' '}
								{d.title}에 동의합니다 (필수)
							</label>{' '}
							<a href={d.url} target="_blank" rel="noreferrer">
								읽기
							</a>{' '}
							<span className="mute small">{d.version}</span>
						</li>
					))}
				</ul>
				{bad !== '' && <p className="bad">{bad}</p>}
				<button
					className="primary"
					disabled={!all || busy}
					onClick={async () => {
						setBusy(true)
						setBad('')
						try {
							await agree(owes)
							props.onDone()
						} catch (e) {
							setBad(e instanceof Error ? e.message : String(e))
						} finally {
							setBusy(false)
						}
					}}
				>
					동의하고 계속
				</button>
				<button className="link" onClick={props.onSignOut}>
					동의하지 않고 로그아웃
				</button>
			</div>
		</div>
	)
}
