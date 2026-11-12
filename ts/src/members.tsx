import { useState, type ReactNode } from 'react'

import type { Party } from '../gen/loft/org_pb.js'
import { HolderService, PartyService, idBytes, idStr, ref } from './api.js'
import { useCatalog } from './catalog.js'
import { useAct, useRpc } from './rpc.js'
import { Badge, Card, Field, FormModal, Select } from './ui.js'
import { roleWord } from './words.js'

/** Whether a person record is one forgotten here: called what forgetting it called it. */
export function forgotten(p: Party): boolean {
	return p.name === `익명 ${idStr(p.id).slice(0, 8)}`
}

/**
 * The members here that no person record is (D88): each taken off its record
 * by hand, until one is linked to it -- which signing in does not do. Shown
 * to the people who run the workspace, who link them.
 */
export function Recordless(): ReactNode {
	const c = useCatalog()
	const members = useRpc(HolderService.method.list, c.can('admin') ? { size: 500 } : null)
	const none = (members.data?.items ?? []).filter((h) => h.dateErased === undefined && !c.parties.some((p) => idStr(p.holder?.id) === idStr(h.id)))
	if (none.length === 0) return null
	return (
		<Card title={`사람 기록이 없는 구성원 (${none.length})`}>
			<ul className="rows">
				{none.map((h) => (
					<li key={idStr(h.id)}>
						<span>
							<strong>{h.name}</strong> <span className="mute small">@{h.alias}</span>
						</span>
						<Badge v={h.role === 'member' ? 'mute' : 'info'}>{roleWord[h.role] ?? h.role}</Badge>
					</li>
				))}
			</ul>
			<p className="mute small">사람 기록을 열어 구성원과 잇기로 잇습니다.</p>
		</Card>
	)
}

/**
 * A member made by hand the person record `p` is (D88): one whose first
 * sign-in found none of the records it could be, or the wrong one. A member
 * that is another record's leaves it.
 */
export function LinkMember(props: { p: Party; onClose: () => void }): ReactNode {
	const c = useCatalog()
	const act = useAct()
	const members = useRpc(HolderService.method.list, { size: 500 })
	const [member, setMember] = useState('')
	const recordOf = new Map(c.parties.filter((x) => x.holder !== undefined).map((x) => [idStr(x.holder?.id), x]))
	const was = recordOf.get(member)
	return (
		<FormModal
			title={`${props.p.name} — 구성원과 잇기`}
			submit="잇기"
			onClose={props.onClose}
			onSubmit={async () => member !== '' && (await act(PartyService.method.linkMember, { ref: ref(props.p.id), holder: ref(idBytes(member)) }, { ok: '이었습니다.' })) !== undefined}
		>
			<Field label="구성원 *">
				<Select
					value={member}
					onChange={setMember}
					required
					empty="(고르세요)"
					options={(members.data?.items ?? [])
						.filter((h) => h.dateErased === undefined)
						.map((h) => {
							const at = recordOf.get(idStr(h.id))
							return { value: idStr(h.id), label: `${h.name} (@${h.alias})${at === undefined ? ' — 사람 기록 없음' : ` — ${at.name}`}` }
						})}
				/>
			</Field>
			{was !== undefined && (
				<p className="note wide">
					지금은 {was.name}의 기록과 이어져 있습니다. 이 기록으로 옮기고, {was.name}의 기록은 로그인 없는 기록으로 남습니다. 필요 없는 기록이면 옮긴 뒤 개인정보 삭제로 지우세요.
				</p>
			)}
		</FormModal>
	)
}
