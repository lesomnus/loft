/**
 * Invitations: the ones a workspace sent, and the ones waiting for an account
 * (design 9.11, D30).
 *
 * The inbox is a page about no workspace -- `/invitations`, with no `@alias` --
 * because what is in it is from workspaces the account is not in yet, and its
 * calls are served as the account rather than as a membership. The link an
 * invitation's mail carries opens it with `?code=`, which makes that
 * invitation the account's before the list is read: what reaches an account
 * roster holds no checked address of.
 *
 * @module
 */

import { useCallback, useEffect, useState, type ReactNode } from 'react'

import type { Invitation } from '../gen/loft/org_pb.js'
import { InvitationService, errorText, fmt, idBytes, idStr, openWorkspace, workspace, type Account } from './api.js'
import { useCatalog } from './catalog.js'
import { call, useAct, useRpc } from './rpc.js'
import { Badge, Card, Confirm, Empty, Field, FormModal, Load, Select } from './ui.js'
import { roleWord } from './words.js'

/** What a state is called. */
const stateWord: Record<string, string> = {
	pending: '기다리는 중',
	accepted: '수락함',
	denied: '거절함',
	revoked: '취소함',
	expired: '기한 지남',
}

/** The particle a word takes for "as": 으로 after a final consonant but ㄹ, 로 otherwise. */
function ro(word: string): string {
	const last = word.charCodeAt(word.length - 1)
	if (Number.isNaN(last) || last < 0xac00 || last > 0xd7a3) return '(으)로'
	const final = (last - 0xac00) % 28
	return final === 0 || final === 8 ? '로' : '으로'
}

/** The order roles are wide in: nobody invites somebody wider than themselves. */
const width: Record<string, number> = { auditor: 1, member: 2, manager: 3, admin: 4, owner: 5 }

/** The roles somebody of `mine` may invite as. */
function rolesUpTo(mine: string): { value: string; label: string }[] {
	return Object.entries(roleWord)
		.filter(([k]) => (width[k] ?? 0) <= (width[mine] ?? 0))
		.map(([value, label]) => ({ value, label }))
}

/** The invitations waiting for an account, from every workspace. */
async function waiting(account: Account): Promise<Invitation[]> {
	const v = await call(InvitationService.method.list, { filters: [{ account: idBytes(account.id) }] })
	return v.items
}

/** How many invitations wait for the account, for a badge; none when it cannot say. */
export function useWaiting(account: Account): number {
	const [n, setN] = useState(0)
	useEffect(() => {
		// On a workspace's own domain the invitations are the service's to ask.
		if (account.pinned !== undefined) return
		let gone = false
		waiting(account)
			.then((v) => !gone && setN(v.length))
			.catch(() => {})
		return () => {
			gone = true
		}
	}, [account])
	return n
}

/** The inbox: what is waiting for this account, and the answer to each. */
export function Inbox(props: { account: Account; onSignOut: () => void }): ReactNode {
	const [items, setItems] = useState<Invitation[]>()
	const [bad, setBad] = useState('')
	const [busy, setBusy] = useState('')

	const load = useCallback(async () => {
		try {
			setItems(await waiting(props.account))
		} catch (err) {
			setBad(errorText(err))
		}
	}, [props.account])

	useEffect(() => {
		const q = new URLSearchParams(location.search)
		const code = q.get('code')
		void (async () => {
			if (code !== null) {
				// Taken out of the address first: a code is spent once, and one
				// left in the address is one a reload spends again.
				q.delete('code')
				const rest = q.toString()
				history.replaceState(null, '', location.pathname + (rest === '' ? '' : '?' + rest))
				try {
					await call(InvitationService.method.claim, { code })
				} catch (err) {
					setBad(errorText(err))
				}
			}
			await load()
		})()
	}, [load])

	const answer = async (v: Invitation, yes: boolean) => {
		setBusy(idStr(v.id))
		setBad('')
		try {
			if (yes) {
				await call(InvitationService.method.accept, { ref: { key: { case: 'id', value: v.id } } })
				openWorkspace(v.tenant?.alias ?? '')
				return
			}
			await call(InvitationService.method.deny, { ref: { key: { case: 'id', value: v.id } } })
			await load()
		} catch (err) {
			setBad(errorText(err))
		} finally {
			setBusy('')
		}
	}

	const back = props.account.workspaces[0]
	return (
		<div className="login">
			<div className="inbox">
				<div className="brand">
					<img src="/icon.svg" alt="" />
					<div>
						<h1>받은 초대</h1>
						<p>{props.account.name || props.account.login}</p>
					</div>
				</div>
				{bad !== '' && <p className="bad">{bad}</p>}
				{items === undefined ? (
					<p className="mute">불러오는 중…</p>
				) : items.length === 0 ? (
					<p className="mute">기다리는 초대가 없습니다.</p>
				) : (
					<ul>
						{items.map((v) => (
							<li key={idStr(v.id)}>
								<div>
									<strong>{v.tenant?.name || v.tenant?.alias}</strong> <span className="mute small">@{v.tenant?.alias}</span>
									<p className="small">
										{v.inviter?.name}님이 {roleWord[v.role] ?? v.role}
										{ro(roleWord[v.role] ?? v.role)} 초대했습니다 · {fmt(v.dateExpires, 'date')}까지
									</p>
								</div>
								<div className="answer">
									<button className="primary" disabled={busy !== ''} onClick={() => void answer(v, true)}>
										수락
									</button>
									<button disabled={busy !== ''} onClick={() => void answer(v, false)}>
										거절
									</button>
								</div>
							</li>
						))}
					</ul>
				)}
				<div className="leave">
					{back !== undefined && (
						<button className="link" onClick={() => openWorkspace(back)}>
							워크스페이스로 돌아가기
						</button>
					)}
					<button className="link" onClick={props.onSignOut}>
						로그아웃
					</button>
				</div>
			</div>
		</div>
	)
}

/** The invitations this workspace sent, for the people who run it. */
export function SentInvitations(): ReactNode {
	const act = useAct()
	const sent = useRpc(InvitationService.method.list, { filters: [{ tenant: { key: { case: 'alias', value: workspace } } }] })
	return (
		<Card title="보낸 초대">
			<Load q={sent}>
				{(v) =>
					v.items.length === 0 ? (
						<Empty>보낸 초대가 없습니다.</Empty>
					) : (
						<ul className="rows">
							{v.items.map((i) => (
								<li key={idStr(i.id)}>
									<span>
										<strong>{i.email}</strong>{' '}
										<span className="mute small">
											{roleWord[i.role] ?? i.role} · {i.inviter?.name} · {fmt(i.dateCreated, 'date')}
										</span>
									</span>
									<span className="answer">
										<Badge v={i.state}>{stateWord[i.state] ?? i.state}</Badge>
										{i.state === 'pending' && (
											<Confirm
												label="취소"
												className="link"
												question={`${i.email}에게 보낸 초대를 취소할까요? 메일의 링크로도 들어올 수 없게 됩니다.`}
												onYes={() => act(InvitationService.method.revoke, { ref: { key: { case: 'id', value: i.id } } }, { ok: '취소했습니다.' })}
											/>
										)}
									</span>
								</li>
							))}
						</ul>
					)
				}
			</Load>
		</Card>
	)
}

/**
 * The form that invites: an address, or the address of a person record the
 * workspace keeps, and a role no wider than the inviter's own.
 */
export function InviteForm(props: { party?: { id: Uint8Array; name: string; email: string }; onClose: () => void }): ReactNode {
	const c = useCatalog()
	const act = useAct()
	const [email, setEmail] = useState(props.party?.email ?? '')
	const [role, setRole] = useState('member')
	return (
		<FormModal
			title={props.party === undefined ? '초대' : `${props.party.name} 초대`}
			submit="초대 보내기"
			onClose={props.onClose}
			onSubmit={async () =>
				(await act(
					InvitationService.method.add,
					{
						email: email.trim(),
						role,
						...(props.party === undefined ? {} : { party: { key: { case: 'id', value: props.party.id } } }),
					},
					{ ok: `${email.trim()}에 초대를 보냈습니다.` },
				)) !== undefined
			}
		>
			<Field label="이메일 *" hint="이 주소로 초대 메일이 갑니다. Loft 계정이 없는 사람은 메일의 링크에서 가입합니다">
				<input type="email" value={email} onChange={(e) => setEmail(e.target.value)} required autoFocus />
			</Field>
			<Field label="역할" hint="자기 역할보다 넓은 역할로는 초대할 수 없습니다">
				<Select value={role} onChange={setRole} options={rolesUpTo(c.role)} />
			</Field>
		</FormModal>
	)
}
