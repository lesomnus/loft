import { useMemo, useState, type ReactNode } from 'react'

import type { Party } from '../../gen/rove/org_pb.js'
import { AssetService, HolderService, PartyService, idBytes, idStr, ref } from '../api.js'
import { useCatalog } from '../catalog.js'
import { AssetLink, PartySelect } from '../pickers.js'
import { useAct, useRpc } from '../rpc.js'
import { Badge, Card, Confirm, Empty, Field, FormModal, Kv, Load, Modal, PageHead, Search, Select, Tabs } from '../ui.js'
import { partyKindWord, roleWord, statusWord } from '../words.js'

export function People(): ReactNode {
	const c = useCatalog()
	const [tab, setTab] = useState('org')
	const [q, setQ] = useState('')
	const [adding, setAdding] = useState<string>()
	const [open, setOpen] = useState<Party>()
	const holders = useRpc(HolderService.method.list, c.can('admin') ? { size: 500 } : null)
	const roles = useMemo(() => new Map((holders.data?.items ?? []).map((h) => [idStr(h.id), h.role])), [holders.data])

	const teams = c.parties.filter((p) => p.kind === 'team' || p.kind === 'org')
	const people = c.parties.filter((p) => p.kind === 'person')
	const vendors = c.parties.filter((p) => p.kind === 'vendor')
	const match = (p: Party) => q === '' || p.name.includes(q) || p.email.includes(q) || p.code.includes(q)

	const Person = (p: { p: Party }): ReactNode => (
		<li className="link" onClick={() => setOpen(p.p)}>
			<span>
				<strong>{p.p.name}</strong>
				<span className="mute small">
					{p.p.email !== '' && ` · ${p.p.email}`}
					{p.p.code !== '' && ` · ${p.p.code}`}
				</span>
			</span>
			<span>
				{p.p.holder !== undefined ? (
					<Badge v={roles.get(idStr(p.p.holder.id)) === 'member' ? 'mute' : 'info'}>{roleWord[roles.get(idStr(p.p.holder.id)) ?? ''] ?? '로그인 있음'}</Badge>
				) : (
					<span className="mute small">로그인 없음</span>
				)}
			</span>
		</li>
	)

	return (
		<>
			<PageHead
				title="사람"
				sub={`${people.length}명 · 팀 ${teams.filter((t) => t.kind === 'team').length}개`}
				actions={
					c.can('admin') && (
						<>
							<button onClick={() => setAdding('team')}>+ 팀</button>
							<button onClick={() => setAdding('vendor')}>+ 거래처</button>
							<button className="primary" onClick={() => setAdding('person')}>
								+ 사람
							</button>
						</>
					)
				}
			/>
			<div className="filters">
				<Tabs
					tabs={[
						{ key: 'org', label: '조직도' },
						{ key: 'vendor', label: '거래처' },
					]}
					at={tab}
					onChange={setTab}
				/>
				<Search value={q} onChange={setQ} placeholder="이름, 이메일, 사번" />
			</div>
			{tab === 'org' && (
				<div className="team-grid">
					{teams
						.sort((a, b) => (a.kind === b.kind ? a.name.localeCompare(b.name, 'ko') : a.kind === 'org' ? -1 : 1))
						.map((t) => {
							const members = people.filter((p) => idStr(p.parentId) === idStr(t.id) && match(p))
							if (members.length === 0 && q !== '') return null
							return (
								<Card key={idStr(t.id)} title={`${t.name} (${members.length})`} actions={<span className="mute small">{partyKindWord[t.kind]}</span>}>
									{members.length === 0 ? <Empty>아무도 없습니다.</Empty> : <ul className="rows">{members.map((p) => <Person key={idStr(p.id)} p={p} />)}</ul>}
								</Card>
							)
						})}
					{(() => {
						const loose = people.filter((p) => !teams.some((t) => idStr(t.id) === idStr(p.parentId)) && match(p))
						return loose.length === 0 ? null : (
							<Card title={`소속 없음 (${loose.length})`}>
								<ul className="rows">
									{loose.map((p) => (
										<Person key={idStr(p.id)} p={p} />
									))}
								</ul>
							</Card>
						)
					})()}
				</div>
			)}
			{tab === 'vendor' && (
				<Card>
					{vendors.length === 0 ? (
						<Empty>거래처가 없습니다.</Empty>
					) : (
						<ul className="rows">
							{vendors.filter(match).map((v) => (
								<li key={idStr(v.id)} className="link" onClick={() => setOpen(v)}>
									<span>
										<strong>{v.name}</strong> <span className="mute small">{[v.email, v.phone].filter((x) => x).join(' · ')}</span>
									</span>
								</li>
							))}
						</ul>
					)}
				</Card>
			)}
			{adding !== undefined && <PartyForm kind={adding} onClose={() => setAdding(undefined)} />}
			{open !== undefined && <PartyDialog p={open} role={roles.get(idStr(open.holder?.id))} onClose={() => setOpen(undefined)} />}
		</>
	)
}

function PartyForm(props: { kind: string; p?: Party; onClose: () => void }): ReactNode {
	const act = useAct()
	const p = props.p
	const [name, setName] = useState(p?.name ?? '')
	const [parent, setParent] = useState(idStr(p?.parentId))
	const [email, setEmail] = useState(p?.email ?? '')
	const [phone, setPhone] = useState(p?.phone ?? '')
	const [code, setCode] = useState(p?.code ?? '')
	const [desc, setDesc] = useState(p?.desc ?? '')
	const kind = p?.kind ?? props.kind
	return (
		<FormModal
			title={p === undefined ? `${partyKindWord[kind]} 추가` : `${p.name} 수정`}
			onClose={props.onClose}
			onSubmit={async () => {
				if (p === undefined) {
					return (
						(await act(
							PartyService.method.add,
							{ name, kind, parentId: parent === '' ? undefined : idBytes(parent), email, phone, code, desc },
							{ ok: '추가했습니다.' },
						)) !== undefined
					)
				}
				return (
					(await act(
						PartyService.method.update,
						{ ref: ref(p.id), name, email, phone, code, desc, parentId: parent === '' ? new Uint8Array() : idBytes(parent), parentNull: parent === '' },
						{ ok: '수정했습니다.' },
					)) !== undefined
				)
			}}
		>
			<Field label="이름 *">
				<input value={name} onChange={(e) => setName(e.target.value)} required autoFocus />
			</Field>
			{kind !== 'vendor' && (
				<Field label={kind === 'person' ? '소속 팀' : '상위 조직'}>
					<PartySelect value={parent} onChange={setParent} kinds={['team', 'org']} empty="(없음)" />
				</Field>
			)}
			<Field label="이메일" hint={kind === 'person' ? '로그인할 때도 씁니다' : undefined}>
				<input type="email" value={email} onChange={(e) => setEmail(e.target.value)} />
			</Field>
			<Field label="전화">
				<input value={phone} onChange={(e) => setPhone(e.target.value)} />
			</Field>
			{kind === 'person' && (
				<Field label="사번">
					<input value={code} onChange={(e) => setCode(e.target.value)} />
				</Field>
			)}
			<Field label="메모" wide>
				<input value={desc} onChange={(e) => setDesc(e.target.value)} />
			</Field>
		</FormModal>
	)
}

function PartyDialog(props: { p: Party; role: string | undefined; onClose: () => void }): ReactNode {
	const c = useCatalog()
	const act = useAct()
	const p = props.p
	const [mode, setMode] = useState<'view' | 'edit' | 'invite' | 'role' | 'password'>('view')
	const [made, setMade] = useState<{ alias: string; password: string }>()
	const holds = useRpc(AssetService.method.search, p.kind === 'vendor' ? null : { custodian: ref(p.id), size: 100 })
	const admin = c.can('admin')
	const self = idStr(c.me.party?.id) === idStr(p.id)

	if (mode === 'edit') return <PartyForm kind={p.kind} p={p} onClose={props.onClose} />
	if (mode === 'invite') return <Invite p={p} onMade={(v) => { setMade(v); setMode('view') }} onClose={props.onClose} />
	if (mode === 'role') return <SetRole p={p} role={props.role ?? 'member'} onClose={props.onClose} />
	if (mode === 'password') return <ResetPassword p={p} onMade={(v) => { setMade(v); setMode('view') }} onClose={props.onClose} />

	return (
		<Modal title={p.name} onClose={props.onClose} wide>
			{made !== undefined && (
				<div className="note ok">
					로그인: <code>{made.alias}</code> / 비밀번호: <code>{made.password}</code>
					<br />이 비밀번호는 다시 볼 수 없습니다. 지금 전달하세요.
				</div>
			)}
			<Kv
				items={[
					['구분', partyKindWord[p.kind]],
					['소속', c.party(p.parentId)?.name ?? '-'],
					['이메일', p.email || '-'],
					['전화', p.phone || '-'],
					...(p.kind === 'person' ? ([['사번', p.code || '-'], ['역할', p.holder !== undefined ? roleWord[props.role ?? ''] ?? '로그인 있음' : '로그인 없음']] as [ReactNode, ReactNode][]) : []),
				]}
			/>
			{p.kind !== 'vendor' && (
				<Card title="가지고 있는 자산">
					<Load q={holds}>
						{(d) =>
							d.items.length === 0 ? (
								<Empty>없습니다.</Empty>
							) : (
								<ul className="rows">
									{d.items.map((a) => (
										<li key={idStr(a.id)}>
											<AssetLink a={a} />
											<Badge v={a.status} words={statusWord} />
										</li>
									))}
								</ul>
							)
						}
					</Load>
				</Card>
			)}
			{admin && (
				<footer className="modal-actions">
					<button onClick={() => setMode('edit')}>수정</button>
					{p.kind === 'person' && p.holder === undefined && c.me.accountsHere && <button className="primary" onClick={() => setMode('invite')}>로그인 발급</button>}
					{p.kind === 'person' && p.holder === undefined && !c.me.accountsHere && <span className="mute small">계정은 roster에서 만듭니다.</span>}
					{p.kind === 'person' && p.holder !== undefined && !self && (
						<>
							<button onClick={() => setMode('role')}>역할 변경</button>
							{c.me.accountsHere && <button onClick={() => setMode('password')}>비밀번호 재설정</button>}
							<Confirm label="로그인 중지" danger question={`${p.name}의 로그인을 중지합니다. 사람과 이력은 남습니다.`} onYes={() => act(PartyService.method.deactivate, { ref: ref(p.id) }, { ok: '중지했습니다.' })} />
						</>
					)}
					{p.kind === 'person' && !self && (
						<Confirm
							label="개인정보 삭제"
							danger
							question={`${p.name}의 이름, 연락처를 지우고 익명으로 바꿉니다. 이력의 기록은 '익명'으로 남습니다. 되돌릴 수 없습니다.`}
							onYes={async () => {
								const v = await act(PartyService.method.pseudonymize, { ref: ref(p.id) }, { ok: '익명으로 바꿨습니다.' })
								if (v !== undefined) props.onClose()
							}}
						/>
					)}
				</footer>
			)}
		</Modal>
	)
}

function Invite(props: { p: Party; onMade: (v: { alias: string; password: string }) => void; onClose: () => void }): ReactNode {
	const act = useAct()
	const guess = (props.p.email.split('@')[0] ?? '').toLowerCase().replace(/[^a-z0-9-]/g, '-')
	const [alias, setAlias] = useState(guess)
	const [role, setRole] = useState('member')
	const [password, setPassword] = useState('')
	return (
		<FormModal
			title={`${props.p.name} 로그인 발급`}
			submit="발급"
			onClose={props.onClose}
			onSubmit={async () => {
				const v = await act(PartyService.method.invite, { ref: ref(props.p.id), alias, role, password }, { ok: '발급했습니다.' })
				if (v === undefined) return false
				props.onMade({ alias, password: v.password || '(입력한 비밀번호)' })
				return false
			}}
		>
			<Field label="아이디 *" hint="영문 소문자, 숫자, -">
				<input value={alias} onChange={(e) => setAlias(e.target.value)} required pattern="[a-z0-9][a-z0-9\-]*" />
			</Field>
			<Field label="역할">
				<Select value={role} onChange={setRole} options={Object.entries(roleWord).filter(([k]) => k !== 'owner').map(([value, label]) => ({ value, label }))} />
			</Field>
			<Field label="비밀번호" hint="비우면 만들어 드립니다 (8자 이상)">
				<input value={password} onChange={(e) => setPassword(e.target.value)} minLength={8} />
			</Field>
		</FormModal>
	)
}

function SetRole(props: { p: Party; role: string; onClose: () => void }): ReactNode {
	const c = useCatalog()
	const act = useAct()
	const [role, setRole] = useState(props.role)
	return (
		<FormModal
			title={`${props.p.name} 역할`}
			onClose={props.onClose}
			onSubmit={async () => (await act(PartyService.method.setRole, { ref: ref(props.p.id), role }, { ok: '바꿨습니다.' })) !== undefined}
		>
			<Field label="역할">
				<Select
					value={role}
					onChange={setRole}
					options={Object.entries(roleWord)
						.filter(([k]) => k !== 'owner' || c.can('owner'))
						.map(([value, label]) => ({ value, label }))}
				/>
			</Field>
			<ul className="wide small mute">
				<li>구성원: 조회, 예약, 고장 신고, 재고 사용, 자가 실사</li>
				<li>매니저: 자산 등록·이동, 지급·반납, 실사, 재고, 작업, 구매, 예약 승인</li>
				<li>관리자: 사람·역할, 유형, 예약 자원, 라벨 도메인</li>
				<li>감사자: 모든 조회와 감사 기록, 변경은 못 함</li>
			</ul>
		</FormModal>
	)
}

function ResetPassword(props: { p: Party; onMade: (v: { alias: string; password: string }) => void; onClose: () => void }): ReactNode {
	const act = useAct()
	const [password, setPassword] = useState(() => Math.random().toString(36).slice(2, 6) + '-' + Math.random().toString(36).slice(2, 6) + '-' + Math.random().toString(36).slice(2, 6))
	return (
		<FormModal
			title={`${props.p.name} 비밀번호 재설정`}
			submit="재설정"
			onClose={props.onClose}
			onSubmit={async () => {
				const v = await act(PartyService.method.setPassword, { ref: ref(props.p.id), password }, { ok: '재설정했습니다. 기존 로그인은 모두 끊깁니다.' })
				if (v === undefined) return false
				props.onMade({ alias: props.p.holder === undefined ? '' : '(기존 아이디)', password })
				return false
			}}
		>
			<Field label="새 비밀번호">
				<input value={password} onChange={(e) => setPassword(e.target.value)} minLength={8} required />
			</Field>
		</FormModal>
	)
}
