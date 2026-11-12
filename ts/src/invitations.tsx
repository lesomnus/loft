/**
 * Invitations a workspace sent (design 9.11), for the people who run it.
 * Answering one is nobody's here any more (D85): a member is who roster has in
 * the tenant the deployment serves, made a member the first time they sign
 * in. What takes the place of an invitation is #332's.
 *
 * @module
 */

import { useState, type ReactNode } from 'react'

import { InvitationService, fmt, idStr } from './api.js'
import { useCatalog } from './catalog.js'
import { useAct, useRpc } from './rpc.js'
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

/** The order roles are wide in: nobody invites somebody wider than themselves. */
const width: Record<string, number> = { auditor: 1, member: 2, manager: 3, admin: 4, owner: 5 }

/** The roles somebody of `mine` may invite as. */
function rolesUpTo(mine: string): { value: string; label: string }[] {
	return Object.entries(roleWord)
		.filter(([k]) => (width[k] ?? 0) <= (width[mine] ?? 0))
		.map(([value, label]) => ({ value, label }))
}

/** The invitations this workspace sent, for the people who run it. */
export function SentInvitations(): ReactNode {
	const act = useAct()
	const c = useCatalog()
	const tenant = c.me.tenant?.id
	const sent = useRpc(InvitationService.method.list, tenant === undefined ? null : { filters: [{ tenant: { key: { case: 'id', value: tenant } } }] })
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
												label="초대 취소"
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
			<Field label="이메일 *" hint="이 주소로 초대 메일이 갑니다. 받는 사람은 이 워크스페이스의 계정으로 로그인해 들어옵니다">
				<input type="email" value={email} onChange={(e) => setEmail(e.target.value)} required autoFocus />
			</Field>
			<Field label="역할" hint="자기 역할보다 넓은 역할로는 초대할 수 없습니다">
				<Select value={role} onChange={setRole} options={rolesUpTo(c.role)} />
			</Field>
		</FormModal>
	)
}
