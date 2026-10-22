import { useMemo, useState, type ReactNode } from 'react'
import { Link, useSearchParams } from 'react-router-dom'

import type { Bookable } from '../../gen/rove/booking_pb.js'
import type { AssetType, AttributeDef, ItemModel } from '../../gen/rove/catalog_pb.js'
import type { TenantDomain } from '../../gen/rove/asset_pb.js'
import type { Tenant } from '../../gen/rove/payday/tenant_pb.js'
import type { TenantDomainStatusResponse } from '../../gen/rove/asset_svc_pb.js'
import {
	AccessReviewService,
	AccessService,
	AssetTypeService,
	AuditService,
	BookableService,
	EventService,
	ItemModelService,
	TenantDomainService,
	TenantService,
	UsageSnapshotService,
	at,
	daysWord,
	fmt,
	fmtWith,
	idBytes,
	idStr,
	ref,
	ts,
	wall,
	workspace,
} from '../api.js'
import { modelName, useCatalog } from '../catalog.js'
import { TypeSelect, useAssets } from '../pickers.js'
import { useAct, useRpc } from '../rpc.js'
import { Badge, Card, Confirm, Empty, Field, FormModal, Load, OrderSelect, PageHead, Select, Tabs, type Order } from '../ui.js'
import { attrTypeWord, domainWord, kindWord } from '../words.js'

export function Settings(): ReactNode {
	const c = useCatalog()
	const [sp, setSp] = useSearchParams()
	const tab = sp.get('tab') ?? 'types'
	const tabs = [
		{ key: 'types', label: '유형' },
		{ key: 'models', label: '모델' },
		{ key: 'bookables', label: '예약 자원' },
		{ key: 'labels', label: '라벨' },
		{ key: 'events', label: '작업 기록' },
		...(c.can('admin') || c.role === 'auditor'
			? [
					{ key: 'audit', label: '감사 기록' },
					{ key: 'access', label: '접속기록' },
					{ key: 'usage', label: '사용량' },
				]
			: []),
		...(c.can('owner') ? [{ key: 'workspace', label: '워크스페이스' }] : []),
	]
	return (
		<>
			<PageHead title="설정" />
			<Tabs tabs={tabs} at={tab} onChange={(k) => setSp({ tab: k })} />
			{tab === 'types' && <Types />}
			{tab === 'models' && <Models />}
			{tab === 'bookables' && <Bookables />}
			{tab === 'labels' && <Labels />}
			{tab === 'events' && <Events />}
			{tab === 'audit' && <Audit />}
			{tab === 'access' && <Access />}
			{tab === 'usage' && <Usage />}
			{tab === 'workspace' && <WorkspaceTab />}
		</>
	)
}

function Types(): ReactNode {
	const c = useCatalog()
	const [open, setOpen] = useState<AssetType | 'new'>()
	return (
		<Card title="자산 유형" actions={c.can('admin') && <button className="primary small" onClick={() => setOpen('new')}>+ 유형</button>}>
			<p className="mute small">유형은 자산이 가지는 속성(예: 노트북의 CPU, 메모리)을 정합니다. 속성의 키는 바꾸지 않는 것이 좋습니다 — 이력이 키로 남습니다.</p>
			<ul className="rows">
				{c.types
					.slice()
					.sort((a, b) => (a.kind === b.kind ? a.name.localeCompare(b.name, 'ko') : a.kind.localeCompare(b.kind)))
					.map((t) => (
						<li key={idStr(t.id)} className={c.can('admin') ? 'link' : ''} onClick={() => c.can('admin') && setOpen(t)}>
							<span>
								<strong>{t.name}</strong> <span className="chip">{kindWord[t.kind]}</span>
								<span className="mute small"> {(t.spec?.attributes ?? []).map((a) => a.label || a.key).join(', ')}</span>
							</span>
							<span className="mute small">v{t.schemaVersion}</span>
						</li>
					))}
			</ul>
			{open !== undefined && <TypeForm t={open === 'new' ? undefined : open} onClose={() => setOpen(undefined)} />}
		</Card>
	)
}

type AttrDraft = { key: string; label: string; type: string; required: boolean; options: string; unit: string }

function TypeForm(props: { t: AssetType | undefined; onClose: () => void }): ReactNode {
	const act = useAct()
	const t = props.t
	const [name, setName] = useState(t?.name ?? '')
	const [kind, setKind] = useState(t?.kind ?? 'item')
	const [attrs, setAttrs] = useState<AttrDraft[]>(
		(t?.spec?.attributes ?? []).map((a: AttributeDef) => ({ key: a.key, label: a.label, type: a.type || 'text', required: a.required, options: a.options.join(', '), unit: a.unit })),
	)
	const spec = {
		attributes: attrs
			.filter((a) => a.key.trim() !== '')
			.map((a) => ({
				key: a.key.trim(),
				label: a.label.trim(),
				type: a.type,
				required: a.required,
				unit: a.unit.trim(),
				options: a.type === 'enum' ? a.options.split(',').map((o) => o.trim()).filter((o) => o !== '') : [],
			})),
		capabilities: t?.spec?.capabilities ?? [],
	}
	return (
		<FormModal
			title={t === undefined ? '유형 추가' : `${t.name} 수정`}
			wide
			onClose={props.onClose}
			onSubmit={async () => {
				if (t === undefined) {
					return (await act(AssetTypeService.method.add, { name, kind, spec }, { ok: '추가했습니다.' })) !== undefined
				}
				return (await act(AssetTypeService.method.update, { ref: ref(t.id), name, spec }, { ok: '바꿨습니다.' })) !== undefined
			}}
		>
			<Field label="이름 *">
				<input value={name} onChange={(e) => setName(e.target.value)} required />
			</Field>
			<Field label="종류">
				<Select value={kind} onChange={setKind} options={Object.entries(kindWord).map(([value, label]) => ({ value, label }))} />
			</Field>
			<div className="wide">
				<table className="table compact">
					<thead>
						<tr>
							<th>키</th>
							<th>표시 이름</th>
							<th>형식</th>
							<th>단위</th>
							<th>선택지 (쉼표)</th>
							<th>필수</th>
							<th />
						</tr>
					</thead>
					<tbody>
						{attrs.map((a, i) => {
							const set = (v: Partial<AttrDraft>) => setAttrs(attrs.map((x, j) => (j === i ? { ...x, ...v } : x)))
							return (
								<tr key={i}>
									<td>
										<input value={a.key} onChange={(e) => set({ key: e.target.value })} placeholder="ram" pattern="[^ .]+" />
									</td>
									<td>
										<input value={a.label} onChange={(e) => set({ label: e.target.value })} placeholder="메모리" />
									</td>
									<td>
										<Select value={a.type} onChange={(v) => set({ type: v })} options={Object.entries(attrTypeWord).map(([value, label]) => ({ value, label }))} />
									</td>
									<td>
										<input value={a.unit} onChange={(e) => set({ unit: e.target.value })} />
									</td>
									<td>
										<input value={a.options} onChange={(e) => set({ options: e.target.value })} disabled={a.type !== 'enum'} />
									</td>
									<td>
										<input type="checkbox" checked={a.required} onChange={(e) => set({ required: e.target.checked })} />
									</td>
									<td>
										<button type="button" className="link" onClick={() => setAttrs(attrs.filter((_, j) => j !== i))}>
											빼기
										</button>
									</td>
								</tr>
							)
						})}
					</tbody>
				</table>
				<button type="button" onClick={() => setAttrs([...attrs, { key: '', label: '', type: 'text', required: false, options: '', unit: '' }])}>
					+ 속성
				</button>
			</div>
		</FormModal>
	)
}

function Models(): ReactNode {
	const c = useCatalog()
	const [open, setOpen] = useState<ItemModel | 'new'>()
	return (
		<Card title="모델" actions={c.can('manager') && <button className="primary small" onClick={() => setOpen('new')}>+ 모델</button>}>
			<ul className="rows">
				{c.models
					.slice()
					.sort((a, b) => modelName(a).localeCompare(modelName(b), 'ko'))
					.map((m) => (
						<li key={idStr(m.id)} className={c.can('manager') ? 'link' : ''} onClick={() => c.can('manager') && setOpen(m)}>
							<span>
								<strong>{modelName(m)}</strong>
								{m.code !== '' && <span className="mute"> · {m.code}</span>}
							</span>
							<span className="mute small">{c.type(m.type?.id)?.name ?? ''}</span>
						</li>
					))}
			</ul>
			{open !== undefined && <ModelForm m={open === 'new' ? undefined : open} onClose={() => setOpen(undefined)} />}
		</Card>
	)
}

function ModelForm(props: { m: ItemModel | undefined; onClose: () => void }): ReactNode {
	const act = useAct()
	const m = props.m
	const [name, setName] = useState(m?.name ?? '')
	const [maker, setMaker] = useState(m?.maker ?? '')
	const [code, setCode] = useState(m?.code ?? '')
	const [type, setType] = useState(idStr(m?.type?.id))
	const [ru, setRu] = useState(String(m?.spec?.rackUnits ?? ''))
	return (
		<FormModal
			title={m === undefined ? '모델 추가' : `${modelName(m)} 수정`}
			onClose={props.onClose}
			onSubmit={async () => {
				const spec = ru === '' || ru === '0' ? undefined : { rackUnits: Number(ru), slots: m?.spec?.slots ?? [], heightUnits: 0 }
				if (m === undefined) {
					return (await act(ItemModelService.method.add, { name, maker, code, type: type === '' ? undefined : ref(idBytes(type)), spec }, { ok: '추가했습니다.' })) !== undefined
				}
				return (
					(await act(
						ItemModelService.method.update,
						{ ref: ref(m.id), name, maker, code, type: type === '' ? undefined : ref(idBytes(type)), typeNull: type === '', spec },
						{ ok: '바꿨습니다.' },
					)) !== undefined
				)
			}}
		>
			<Field label="제조사">
				<input value={maker} onChange={(e) => setMaker(e.target.value)} />
			</Field>
			<Field label="모델명 *">
				<input value={name} onChange={(e) => setName(e.target.value)} required />
			</Field>
			<Field label="모델 코드">
				<input value={code} onChange={(e) => setCode(e.target.value)} />
			</Field>
			<Field label="유형">
				<TypeSelect value={type} onChange={setType} />
			</Field>
			<Field label="랙 높이 (U)" hint="랙 장비만">
				<input type="number" min={0} value={ru} onChange={(e) => setRu(e.target.value)} />
			</Field>
		</FormModal>
	)
}

const weekdays = ['일', '월', '화', '수', '목', '금', '토']

function Bookables(): ReactNode {
	const c = useCatalog()
	const assets = useAssets(c.bookables.map((b) => b.asset?.id))
	const [open, setOpen] = useState<Bookable>()
	return (
		<Card title="예약 자원">
			<p className="mute small">자산 화면의 '예약 가능하게'로 추가합니다. 같은 배타 그룹에 묶인 자원은 함께 예약되지 않습니다 (예: 대회의실과 그 안의 분할 회의실).</p>
			{c.bookables.length === 0 ? (
				<Empty />
			) : (
				<ul className="rows">
					{c.bookables.map((b) => {
						const a = assets.get(idStr(b.asset?.id))
						return (
							<li key={idStr(b.id)} className={c.can('admin') ? 'link' : ''} onClick={() => c.can('admin') && setOpen(b)}>
								<span>
									<strong>{a?.name ?? '…'}</strong> <span className="mute small">{a?.tag}</span>
									{!b.enabled && <span className="chip">중지</span>}
								</span>
								<span className="mute small">
									{b.approval && '승인 필요 · '}
									{b.units > 1 && `${b.units}개 · `}
									{b.hours !== undefined ? `${b.hours.ranges.map((r) => weekdays[r.weekday]).join('')} ${Math.floor((b.hours.ranges[0]?.fromMinute ?? 0) / 60)}–${Math.floor((b.hours.ranges[0]?.toMinute ?? 0) / 60)}시` : '언제나'}
									{b.exclusiveGroup !== '' && ` · 그룹 ${b.exclusiveGroup}`}
								</span>
							</li>
						)
					})}
				</ul>
			)}
			{open !== undefined && <BookableForm b={open} onClose={() => setOpen(undefined)} />}
		</Card>
	)
}

function BookableForm(props: { b: Bookable; onClose: () => void }): ReactNode {
	const act = useAct()
	const b = props.b
	const r0 = b.hours?.ranges[0]
	const [approval, setApproval] = useState(b.approval)
	const [enabled, setEnabled] = useState(b.enabled)
	const [days, setDays] = useState<number[]>(b.hours?.ranges.map((r) => r.weekday) ?? [])
	const [from, setFrom] = useState(String(Math.floor((r0?.fromMinute ?? 480) / 60)))
	const [to, setTo] = useState(String(Math.floor((r0?.toMinute ?? 1200) / 60)))
	const [maxH, setMaxH] = useState(String(Math.round(b.maxMinutes / 60)))
	const [minM, setMinM] = useState(String(b.minMinutes))
	const [before, setBefore] = useState(String(b.bufferBefore))
	const [after, setAfter] = useState(String(b.bufferAfter))
	const [horizon, setHorizon] = useState(String(b.horizonDays))
	const [group, setGroup] = useState(b.exclusiveGroup)
	const [units, setUnits] = useState(String(b.units))
	return (
		<FormModal
			title="예약 자원 설정"
			wide
			onClose={props.onClose}
			onSubmit={async () =>
				(await act(
					BookableService.method.update,
					{
						ref: ref(b.id),
						approval,
						enabled,
						hours: days.length > 0 ? { ranges: days.map((weekday) => ({ weekday, fromMinute: Number(from) * 60, toMinute: Number(to) * 60 })) } : undefined,
						hoursNull: days.length === 0,
						maxMinutes: Math.round(Number(maxH) * 60),
						minMinutes: Number(minM),
						bufferBefore: Number(before),
						bufferAfter: Number(after),
						horizonDays: Number(horizon),
						exclusiveGroup: group,
						units: Number(units),
					},
					{ ok: '바꿨습니다.' },
				)) !== undefined
			}
		>
			<label className="check">
				<input type="checkbox" checked={enabled} onChange={(e) => setEnabled(e.target.checked)} /> 예약 받기
			</label>
			<label className="check">
				<input type="checkbox" checked={approval} onChange={(e) => setApproval(e.target.checked)} /> 승인 필요
			</label>
			<Field label="운영 요일" wide group hint="아무것도 고르지 않으면 언제나">
				<div className="inline">
					{weekdays.map((w, i) => (
						<label key={i} className="check">
							<input type="checkbox" checked={days.includes(i)} onChange={(e) => setDays(e.target.checked ? [...days, i].sort() : days.filter((d) => d !== i))} />
							{w}
						</label>
					))}
				</div>
			</Field>
			<Field label="운영 시작 (시)">
				<input type="number" min={0} max={23} value={from} onChange={(e) => setFrom(e.target.value)} />
			</Field>
			<Field label="운영 끝 (시)">
				<input type="number" min={1} max={24} value={to} onChange={(e) => setTo(e.target.value)} />
			</Field>
			<Field label="최소 (분)">
				<input type="number" min={0} value={minM} onChange={(e) => setMinM(e.target.value)} />
			</Field>
			<Field label="최대 (시간)">
				<input type="number" min={0} value={maxH} onChange={(e) => setMaxH(e.target.value)} />
			</Field>
			<Field label="준비 시간 (분)">
				<input type="number" min={0} value={before} onChange={(e) => setBefore(e.target.value)} />
			</Field>
			<Field label="정리 시간 (분)">
				<input type="number" min={0} value={after} onChange={(e) => setAfter(e.target.value)} />
			</Field>
			<Field label="며칠 앞까지">
				<input type="number" min={0} value={horizon} onChange={(e) => setHorizon(e.target.value)} />
			</Field>
			<Field label="수량" hint="풀(그룹)일 때">
				<input type="number" min={1} value={units} onChange={(e) => setUnits(e.target.value)} />
			</Field>
			<Field label="배타 그룹">
				<input value={group} onChange={(e) => setGroup(e.target.value)} />
			</Field>
		</FormModal>
	)
}

/**
 * Where labels print: the deployment's own address, with a code that is one
 * in the whole deployment (D28) -- or, where tenants have domains of their
 * own (D23), the tenant's.
 */
function Labels(): ReactNode {
	const status = useRpc(TenantDomainService.method.status, {})
	return (
		<Load q={status}>
			{(s) =>
				s.tenantDomains ? (
					<Domains s={s} />
				) : (
					<Card title="라벨">
						<p>
							QR 라벨은 이 서비스의 주소 <code>{s.base}/l/코드</code>로 인쇄됩니다. 코드는 서비스 전체에서 하나뿐이라 어느 워크스페이스의 라벨인지는 코드가 정합니다.{' '}
							<Link to="/labels/print?count=24">빈 라벨 24장 인쇄</Link>
						</p>
						<p className="mute small">
							다른 워크스페이스의 라벨을 스캔하면, 그 워크스페이스의 구성원이라면 그곳에서 열립니다. 카메라가 읽지 못할 때는 라벨 아래의 코드(예: 7K3M-Q9TX)를 스캔 화면에 입력하세요.
						</p>
					</Card>
				)
			}
		</Load>
	)
}

function Domains(props: { s: TenantDomainStatusResponse }): ReactNode {
	const c = useCatalog()
	const act = useAct()
	const s = props.s
	const list = useRpc(TenantDomainService.method.list, { size: 50 })
	const [adding, setAdding] = useState(false)
	return (
		<Card title="라벨 도메인" actions={c.can('admin') && <button className="primary small" onClick={() => setAdding(true)}>+ 도메인</button>}>
			{s.labels ? (
				<p>
					새 라벨은 <code>{s.active?.host}</code> 주소로 인쇄됩니다.{' '}
					<Link to="/labels/print?count=24">빈 라벨 24장 인쇄</Link>
				</p>
			) : (
				<p className="warn">
					라벨 도메인이 없어 QR 라벨 기능이 꺼져 있습니다. 태그로 찾기는 그대로 됩니다. 도메인을 추가하면 켜집니다.
				</p>
			)}
			<Load q={list}>
				{(d) =>
					d.items.length === 0 ? (
						<Empty />
					) : (
						<ul className="rows">
							{d.items.map((x) => (
								<DomainRow key={idStr(x.id)} d={x} target={s.target} admin={c.can('admin')} act={act} />
							))}
						</ul>
					)
				}
			</Load>
			<p className="mute small">
				인쇄한 라벨은 그 주소를 계속 씁니다. 도메인을 바꾸면 이전 도메인은 '이전 주소'로 남아 기존 라벨도 계속 열립니다. 중지하면 그 주소로 인쇄된 라벨이 더 이상 열리지 않습니다.
			</p>
			{adding && <NewDomain suffix={s.defaultSuffix} onClose={() => setAdding(false)} />}
		</Card>
	)
}

function DomainRow(props: { d: TenantDomain; target: string; admin: boolean; act: ReturnType<typeof useAct> }): ReactNode {
	const d = props.d
	return (
		<li>
			<span>
				<code>{d.host}</code> <Badge v={d.state} words={domainWord} />
				<span className="mute small"> {d.source === 'default' ? '기본 주소' : '자체 도메인'}</span>
				{d.state === 'pending' && (
					<div className="small">
						DNS에 TXT 레코드 <code>_rove-challenge.{d.host}</code> = <code>{d.token}</code>
						{props.target !== '' && (
							<>
								{' '}
								와 CNAME <code>{d.host}</code> → <code>{props.target}</code>
							</>
						)}{' '}
						를 추가하세요. 몇 분마다 자동으로 확인합니다.
					</div>
				)}
			</span>
			{props.admin && (
				<span className="inline">
					{d.state === 'pending' && <button className="small" onClick={() => void props.act(TenantDomainService.method.verify, { ref: ref(d.id) }, { ok: '확인되었습니다.' })}>지금 확인</button>}
					{['ready', 'legacy'].includes(d.state) && (
						<button className="small primary" onClick={() => void props.act(TenantDomainService.method.activate, { ref: ref(d.id) }, { ok: '이제 이 주소로 인쇄합니다.' })}>
							사용
						</button>
					)}
					{d.state !== 'retired' && (
						<Confirm
							className="small danger"
							label="중지"
							danger
							question={`${d.host}를 중지합니다. 이 주소로 인쇄된 라벨은 더 이상 열리지 않습니다.`}
							onYes={() => props.act(TenantDomainService.method.retire, { ref: ref(d.id), force: true }, { ok: '중지했습니다.' })}
						/>
					)}
				</span>
			)}
		</li>
	)
}

function NewDomain(props: { suffix: string; onClose: () => void }): ReactNode {
	const act = useAct()
	const [how, setHow] = useState(props.suffix !== '' ? 'sub' : 'custom')
	const [v, setV] = useState('')
	return (
		<FormModal
			title="라벨 도메인 추가"
			onClose={props.onClose}
			onSubmit={async () => (await act(TenantDomainService.method.add, how === 'sub' ? { sub: v } : { host: v }, { ok: '추가했습니다.' })) !== undefined}
		>
			<Field label="종류">
				<Select
					value={how}
					onChange={setHow}
					options={[
						...(props.suffix !== '' ? [{ value: 'sub', label: `기본 주소 (*.${props.suffix})` }] : []),
						{ value: 'custom', label: '자체 도메인 (DNS 확인 필요)' },
					]}
				/>
			</Field>
			<Field label={how === 'sub' ? '이름' : '호스트'} hint={how === 'sub' ? `→ 이름.${props.suffix}` : '예: assets.example.com'}>
				<input value={v} onChange={(e) => setV(e.target.value)} required />
			</Field>
		</FormModal>
	)
}

function Events(): ReactNode {
	const list = useRpc(EventService.method.recent, { size: 200 })
	return (
		<Card title="최근 작업 기록" actions={<span className="mute small">무엇을 왜 했는지 — 자산 이력의 원천</span>}>
			<Load q={list}>
				{(d) =>
					d.items.length === 0 ? (
						<Empty />
					) : (
						<table className="table compact">
							<thead>
								<tr>
									<th>언제</th>
									<th>무엇</th>
									<th>내용</th>
									<th>사유</th>
								</tr>
							</thead>
							<tbody>
								{d.items.map((e) => (
										<tr key={idStr(e.id)}>
											<td>{fmt(e.occurredAt)}</td>
											<td>
												<code>{e.kind}</code>
											</td>
											<td>{e.desc}</td>
											<td className="mute">{e.reason}</td>
										</tr>
									))}
							</tbody>
						</table>
					)
				}
			</Load>
		</Card>
	)
}

function Audit(): ReactNode {
	const c = useCatalog()
	const list = useRpc(AuditService.method.recent, { size: 200 })
	return (
		<Card title="감사 기록" actions={<span className="mute small">모든 변경이 누가, 무엇을 바꿨는지 자동으로 남습니다</span>}>
			<Load q={list}>
				{(d) =>
					d.items.length === 0 ? (
						<Empty />
					) : (
						<table className="table compact">
							<thead>
								<tr>
									<th>언제</th>
									<th>누가</th>
									<th>동작</th>
								</tr>
							</thead>
							<tbody>
								{d.items.map((a) => (
										<tr key={idStr(a.id)}>
											<td>{fmt(a.dateCreated)}</td>
											<td>{c.parties.find((p) => idStr(p.holder?.id) === idStr(a.actorId))?.name ?? (a.actorId.every((b) => b === 0) ? '시스템' : idStr(a.actorId).slice(0, 8))}</td>
											<td>
												<code>{a.action.replace('/rove.', '')}</code>
											</td>
										</tr>
									))}
							</tbody>
						</table>
					)
				}
			</Load>
		</Card>
	)
}

const accessWord: Record<string, string> = { read: '조회', write: '수정', download: '내려받기' }

/**
 * The workspace's access record, a month at a time, as its administrators
 * review it (rove#136): who read, wrote or took out whose data, from where,
 * and what a download was for -- and that they looked it over. PIPA's safety
 * measures ask for it every month.
 */
function Access(): ReactNode {
	const c = useCatalog()
	const act = useAct()
	// Last month, on the workspace's wall: the one a review is usually of.
	const [month, setMonth] = useState(() => {
		const w = wall(new Date())
		return { y: w.y, m: w.m - 1 }
	})
	const from = at(month.y, month.m, 1)
	const to = at(month.y, month.m + 1, 1)
	const [actor, setActor] = useState('')
	const [subject, setSubject] = useState('')
	const [kind, setKind] = useState('')
	// Where each page begins, the first at the start.
	const [pages, setPages] = useState<string[]>([''])
	const after = pages[pages.length - 1] ?? ''
	const q = useRpc(AccessService.method.review, {
		from: ts(from),
		to: ts(to),
		actor: actor === '' ? undefined : idBytes(actor),
		subject: subject === '' ? undefined : idBytes(subject),
		kind,
		size: 50,
		after,
	})
	const reviews = useRpc(AccessReviewService.method.list, { size: 24 })
	const [note, setNote] = useState('')
	const name = (holder: Uint8Array | undefined): string => c.parties.find((p) => idStr(p.holder?.id) === idStr(holder))?.name ?? '?'
	const label = fmtWith({ year: 'numeric', month: 'long' }, from)
	const go = (by: number): void => {
		const w = wall(at(month.y, month.m + by, 1))
		setMonth({ y: w.y, m: w.m })
		setPages([''])
	}
	const narrow = (set: (v: string) => void) => (v: string) => {
		set(v)
		setPages([''])
	}
	return (
		<>
			<Card
				title={`접속기록 — ${label}`}
				actions={
					<div className="inline">
						<button onClick={() => go(-1)}>‹ 이전 달</button>
						<button onClick={() => go(1)}>다음 달 ›</button>
						<button onClick={q.reload}>새로고침</button>
					</div>
				}
			>
				<p className="mute small">
					개인정보를 읽거나 쓰거나 내려받은 호출마다 한 줄씩 남습니다. 매달 점검하고, 내려받기는 그 사유를 확인하세요.
				</p>
				<div className="filters">
					<Select
						aria-label="누가"
						value={actor}
						onChange={narrow(setActor)}
						empty="모든 구성원"
						options={c.parties.filter((p) => p.holder !== undefined).map((p) => ({ value: idStr(p.holder?.id), label: p.name }))}
					/>
					<Select
						aria-label="누구의 정보"
						value={subject}
						onChange={narrow(setSubject)}
						empty="모든 사람"
						options={c.parties.filter((p) => p.kind === 'person').map((p) => ({ value: idStr(p.id), label: p.name }))}
					/>
					<Select
						aria-label="종류"
						value={kind}
						onChange={narrow(setKind)}
						empty="모든 종류"
						options={Object.entries(accessWord).map(([value, label]) => ({ value, label }))}
					/>
				</div>
				<Load q={q}>
					{(d) => (
						<>
							<p>
								이 달 {d.calls}건 · 내려받기 {d.downloads}건 · 거부 {d.refused}건
							</p>
							{d.items.length === 0 ? (
								<Empty>이 조건에 맞는 기록이 없습니다.</Empty>
							) : (
								<div className="table-wrap">
									<table className="table compact">
										<thead>
											<tr>
												<th>언제</th>
												<th>누가</th>
												<th>무엇을</th>
												<th>누구의 정보</th>
												<th>어디서</th>
												<th>결과</th>
												<th>내려받은 이유</th>
											</tr>
										</thead>
										<tbody>
											{d.items.map((e) => {
												const named = e.subjects.map((s) => s.name || s.kind).join(', ')
												const rest = e.subjectCount - e.subjects.length
												return (
													<tr key={idStr(e.id)}>
														<td>{fmt(e.at)}</td>
														<td>{e.actorName}</td>
														<td>
															{accessWord[e.kind] ?? e.kind} <code>{e.action.replace('/rove.', '')}</code>
														</td>
														<td>
															{named}
															{rest > 0 ? ` 외 ${rest}명` : ''}
														</td>
														<td>{e.address || '-'}</td>
														<td>{e.result === 'ok' ? '성공' : `거부 (${e.result})`}</td>
														<td>{e.reason || (e.kind === 'download' ? '없음' : '')}</td>
													</tr>
												)
											})}
										</tbody>
									</table>
								</div>
							)}
							<div className="inline">
								{pages.length > 1 && <button onClick={() => setPages(pages.slice(0, -1))}>‹ 앞 페이지</button>}
								{d.next !== '' && <button onClick={() => setPages([...pages, d.next])}>다음 페이지 ›</button>}
							</div>
						</>
					)}
				</Load>
			</Card>
			<Card title="점검 기록">
				<Field label="점검 결과" hint={`${label}의 접속기록을 점검했다는 기록으로 남습니다: 누가, 언제, 그때 기록이 몇 건이었는지와 함께.`}>
					<textarea value={note} onChange={(e) => setNote(e.target.value)} rows={2} placeholder="예: 이상 없음. 내려받기 2건 모두 사유 확인" />
				</Field>
				<div className="actions">
					<button
						className="primary"
						onClick={() =>
							void act(AccessReviewService.method.add, { periodFrom: ts(from), periodTo: ts(to), desc: note }, { ok: '점검을 기록했습니다.' }).then((r) => {
								if (r !== undefined) setNote('')
							})
						}
					>
						{label} 점검 완료로 기록
					</button>
				</div>
				<Load q={reviews}>
					{(d) =>
						d.items.length === 0 ? (
							<Empty>아직 점검 기록이 없습니다.</Empty>
						) : (
							<table className="table compact">
								<thead>
									<tr>
										<th>점검한 기간</th>
										<th>점검한 사람</th>
										<th>기록한 때</th>
										<th className="num">호출</th>
										<th className="num">내려받기</th>
										<th className="num">거부</th>
										<th>점검 결과</th>
									</tr>
								</thead>
								<tbody>
									{d.items.map((r) => (
										<tr key={idStr(r.id)}>
											<td>
												{fmt(r.periodFrom, 'date')} – {fmt(r.periodTo === undefined ? undefined : new Date(Number(r.periodTo.seconds) * 1000 - 1), 'date')}
											</td>
											<td>{name(r.reviewer?.id)}</td>
											<td>{fmt(r.dateCreated)}</td>
											<td className="num">{r.calls}</td>
											<td className="num">{r.downloads}</td>
											<td className="num">{r.refused}</td>
											<td>{r.desc}</td>
										</tr>
									))}
								</tbody>
							</table>
						)
					}
				</Load>
			</Card>
		</>
	)
}

function Usage(): ReactNode {
	const [order, setOrder] = useState<Order>('newest')
	const list = useRpc(UsageSnapshotService.method.list, { size: 60, reverse: order === 'oldest' })
	return (
		<Card
			title="일별 사용량"
			actions={
				<>
					<span className="mute small">하루 한 번 기록 — 요금제가 생기면 쓰일 근거</span>
					<OrderSelect value={order} onChange={setOrder} />
				</>
			}
		>
			<Load q={list}>
				{(d) => {
					const rows = d.items
					const keys = [...new Set(rows.flatMap((r) => Object.keys(r.metrics)))].sort()
					return rows.length === 0 ? (
						<Empty>아직 기록이 없습니다.</Empty>
					) : (
						<div className="table-wrap">
							<table className="table compact">
								<thead>
									<tr>
										<th>날짜</th>
										{keys.map((k) => (
											<th key={k} className="num">
												{k}
											</th>
										))}
									</tr>
								</thead>
								<tbody>
									{rows.map((r) => (
										<tr key={idStr(r.id)}>
											<td>{r.day}</td>
											{keys.map((k) => (
												<td key={k} className="num">
													{r.metrics[k] ?? ''}
												</td>
											))}
										</tr>
									))}
								</tbody>
							</table>
						</div>
					)
				}}
			</Load>
		</Card>
	)
}

/**
 * Where the workspace is, and how long a room waits for whoever booked it: the
 * workspace's own settings, its owner's to say (#144). Empty is the
 * deployment's.
 */
/**
 * What a select of a workspace's zone or currency offers: what the browser
 * knows, and what the workspace says though the browser knows nothing of it --
 * or saving the card would say something else.
 */
function offered(known: string[], has: string): string[] {
	return has === '' || known.includes(has) ? known : [has, ...known]
}

function PlaceCard(props: { t: Tenant }): ReactNode {
	const act = useAct()
	const c = useCatalog()
	const zones = useMemo(() => offered(Intl.supportedValuesOf('timeZone'), props.t.timezone), [props.t.timezone])
	const currencies = useMemo(() => offered(Intl.supportedValuesOf('currency'), props.t.currency), [props.t.currency])
	// What the deployment says, as far as the page can know it: what the
	// workspace is told in while it says nothing itself.
	const deployment = (own: string, told: string): string => (own === '' ? `기본값 (${told})` : '기본값')
	const [timezone, setTimezone] = useState(props.t.timezone)
	const [currency, setCurrency] = useState(props.t.currency)
	const [noShow, setNoShow] = useState(props.t.noShowMinutes > 0 ? String(props.t.noShowMinutes) : '')
	const [unchecked, setUnchecked] = useState(props.t.noShowMinutes < 0)
	const save = async (): Promise<void> => {
		const noShowMinutes = unchecked ? -1 : noShow.trim() === '' ? 0 : Number(noShow)
		const r = await act(TenantService.method.configure, { ref: ref(props.t.id), timezone, currency, noShowMinutes }, { ok: '저장했습니다.' })
		// The page's dates follow the workspace from its first draw.
		if (r !== undefined) location.reload()
	}
	return (
		<Card title="지역과 예약">
			<Field label="시간대" hint="파일의 날짜, 알림의 시각, 하루 단위 집계가 이 시간대를 따릅니다. 비우면 배포의 기본값입니다.">
				<select value={timezone} onChange={(e) => setTimezone(e.target.value)}>
					<option value="">{deployment(props.t.timezone, c.me.timezone)}</option>
					{zones.map((z) => (
						<option key={z} value={z}>
							{z}
						</option>
					))}
				</select>
			</Field>
			<Field label="통화" hint="새 구매와 작업의 금액이 이 통화로 기록됩니다. 이미 기록된 금액은 그대로입니다.">
				<select value={currency} onChange={(e) => setCurrency(e.target.value)}>
					<option value="">{deployment(props.t.currency, c.me.currency)}</option>
					{currencies.map((c) => (
						<option key={c} value={c}>
							{c}
						</option>
					))}
				</select>
			</Field>
			<Field label="노쇼 해제 (분)" hint="회의실 예약이 시작되고 이만큼 지나도록 체크인하지 않으면 예약을 풉니다. 비우면 배포의 기본값입니다.">
				<input type="number" min={1} max={1440} value={noShow} disabled={unchecked} onChange={(e) => setNoShow(e.target.value)} />
			</Field>
			<label className="check">
				<input type="checkbox" checked={unchecked} onChange={(e) => setUnchecked(e.target.checked)} /> 체크인을 받지 않음
			</label>
			<div className="actions">
				<button className="primary" onClick={() => void save()}>
					저장
				</button>
			</div>
		</Card>
	)
}

/** The workspace itself, for its owner: its address, and closing it (D29). */
function WorkspaceTab(): ReactNode {
	const act = useAct()
	const c = useCatalog()
	const t = useRpc(TenantService.method.get, { ref: { key: { case: 'alias', value: workspace } } })
	const [confirm, setConfirm] = useState('')
	const [reason, setReason] = useState('')
	return (
		<Load q={t}>
			{(v) => (
				<>
					<Card title="워크스페이스">
						<dl className="kv">
							<dt>이름</dt>
							<dd>{v.name}</dd>
							<dt>주소</dt>
							<dd>
								@{v.alias} <span className="mute small">— 바꿔야 한다면 운영자에게 요청하세요</span>
							</dd>
						</dl>
					</Card>
					<PlaceCard t={v} />
					<Card title="워크스페이스 닫기">
						<p>
							닫으면 소유자 말고는 아무도 들어오지 못합니다. 소유자는 {c.me.restoreDays}일 안에 복구하거나 모든 데이터를 내려받을 수 있고, 그 뒤에는 이 워크스페이스의 모든 데이터가 지워집니다. 다만 법이 보관 기간을 정한 기록(접속기록 등)은 계약이 달리 정하지 않았다면 그 기간이 지난 뒤에 지워집니다. 법적 보존이 걸려 있으면 풀릴 때까지 지우지 않습니다. 주소 @{v.alias}는 {daysWord(c.me.aliasHoldDays)} 동안 아무도 쓰지 못하게 보류됩니다.
						</p>
						<Field label={`확인을 위해 워크스페이스 이름(${v.name})을 그대로 입력하세요`}>
							<input value={confirm} onChange={(e) => setConfirm(e.target.value)} />
						</Field>
						<Field label="닫는 이유 (선택)">
							<input value={reason} onChange={(e) => setReason(e.target.value)} />
						</Field>
						<div className="actions">
							<button
								className="danger"
								disabled={confirm.trim() !== v.name.trim()}
								onClick={() =>
									void act(TenantService.method.close, { ref: ref(v.id), confirm, reason }, { ok: '닫았습니다.' }).then((r) => {
										if (r !== undefined) location.reload()
									})
								}
							>
								워크스페이스 닫기
							</button>
						</div>
					</Card>
				</>
			)}
		</Load>
	)
}
