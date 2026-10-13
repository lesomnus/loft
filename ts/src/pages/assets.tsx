import { useState, type ReactNode } from 'react'
import { Link, useNavigate, useSearchParams } from 'react-router-dom'

import type { AssetType } from '../../gen/rove/catalog_pb.js'
import { AssetService, download, errorText, idBytes, idStr, raw, readFile, ref, ts } from '../api.js'
import { modelName, useCatalog } from '../catalog.js'
import { ModelSelect, SpaceSelect, TypeSelect, Where } from '../pickers.js'
import { useAct, useRpc } from '../rpc.js'
import { Badge, Empty, Field, FormModal, Load, PageHead, Search, Select, useDebounced, useToast } from '../ui.js'
import { conditionWord, kindWord, statusWord } from '../words.js'

const PAGE = 50

export function Assets(): ReactNode {
	const c = useCatalog()
	const [sp, setSp] = useSearchParams()
	const [q, setQ] = useState(sp.get('q') ?? '')
	const dq = useDebounced(q)
	const kind = sp.get('kind') ?? ''
	const status = sp.get('status') ?? ''
	const type = sp.get('type') ?? ''
	const within = sp.get('within') ?? ''
	const page = Number(sp.get('page') ?? '0')
	const [picked, setPicked] = useState<Set<string>>(new Set())
	const [adding, setAdding] = useState(false)
	const [importing, setImporting] = useState(false)
	const go = useNavigate()
	const toast = useToast()

	const set = (k: string, v: string) => {
		const n = new URLSearchParams(sp)
		if (v === '') n.delete(k)
		else n.set(k, v)
		n.delete('page')
		setSp(n, { replace: true })
	}

	const found = useRpc(AssetService.method.search, {
		q: dq.trim(),
		kind,
		status,
		type: type === '' ? undefined : ref(idBytes(type)),
		within: within === '' ? undefined : ref(idBytes(within)),
		size: PAGE,
		offset: page * PAGE,
	})

	const exportAs = async (format: string) => {
		try {
			const v = await raw.asset.export({ format })
			download(v.data, v.name, v.contentType)
		} catch (err) {
			toast(errorText(err), 'bad')
		}
	}

	return (
		<>
			<PageHead
				title="자산"
				sub={found.data !== undefined ? `${found.data.total}개` : undefined}
				actions={
					<>
						{picked.size > 0 && (
							<button onClick={() => go(`/labels/print?assets=${[...picked].join(',')}`)}>라벨 인쇄 ({picked.size})</button>
						)}
						{c.can('manager') && (
							<>
								<button onClick={() => void exportAs('xlsx')}>엑셀로 내보내기</button>
								<button onClick={() => setImporting(true)}>가져오기</button>
								<button className="primary" onClick={() => setAdding(true)}>
									+ 자산 등록
								</button>
							</>
						)}
					</>
				}
			/>
			<div className="filters">
				<Search value={q} onChange={(v) => { setQ(v); set('q', v) }} placeholder="이름, 태그, 시리얼, 모델, 사용자" />
				<Select value={kind} onChange={(v) => set('kind', v)} empty="모든 종류" options={Object.entries(kindWord).map(([value, label]) => ({ value, label }))} />
				<Select value={status} onChange={(v) => set('status', v)} empty="모든 상태" options={Object.entries(statusWord).map(([value, label]) => ({ value, label }))} />
				<TypeSelect value={type} onChange={(v) => set('type', v)} empty="모든 유형" />
				<SpaceSelect value={within} onChange={(v) => set('within', v)} empty="모든 위치" />
			</div>
			<Load q={found}>
				{(d) =>
					d.items.length === 0 ? (
						<Empty>조건에 맞는 자산이 없습니다.</Empty>
					) : (
						<>
							<div className="table-wrap">
								<table className="table">
									<thead>
										<tr>
											<th className="check">
												<input
													type="checkbox"
													checked={d.items.every((a) => picked.has(idStr(a.id)))}
													onChange={(e) => {
														const n = new Set(picked)
														for (const a of d.items) {
															if (e.target.checked) n.add(idStr(a.id))
															else n.delete(idStr(a.id))
														}
														setPicked(n)
													}}
												/>
											</th>
											<th>태그</th>
											<th>이름</th>
											<th>유형 / 모델</th>
											<th>상태</th>
											<th>위치</th>
											<th>사용자</th>
										</tr>
									</thead>
									<tbody>
										{d.items.map((a) => {
											const id = idStr(a.id)
											return (
												<tr key={id} className="link" onClick={() => go(`/assets/${id}`)}>
													<td className="check" onClick={(e) => e.stopPropagation()}>
														<input
															type="checkbox"
															checked={picked.has(id)}
															onChange={(e) => {
																const n = new Set(picked)
																if (e.target.checked) n.add(id)
																else n.delete(id)
																setPicked(n)
															}}
														/>
													</td>
													<td>
														<code>{a.tag}</code>
													</td>
													<td>
														<Link to={`/assets/${id}`} onClick={(e) => e.stopPropagation()}>
															{a.name}
														</Link>
														{a.kind !== 'item' && <span className="chip">{kindWord[a.kind]}</span>}
													</td>
													<td className="mute">
														{c.type(a.type?.id)?.name ?? ''}
														{a.model !== undefined && ` · ${modelName(c.model(a.model.id))}`}
													</td>
													<td>
														<Badge v={a.status} words={statusWord} />
														{a.condition !== 'good' && <Badge v={a.condition} words={conditionWord} />}
													</td>
													<td onClick={(e) => e.stopPropagation()}>
														<Where a={a} />
													</td>
													<td>{c.party(a.custodian?.id)?.name ?? ''}</td>
												</tr>
											)
										})}
									</tbody>
								</table>
							</div>
							<div className="pager">
								<button disabled={page === 0} onClick={() => setSp((p) => { const n = new URLSearchParams(p); n.set('page', String(page - 1)); return n })}>
									이전
								</button>
								<span>
									{page + 1} / {Math.max(1, Math.ceil(d.total / PAGE))}
								</span>
								<button
									disabled={(page + 1) * PAGE >= d.total}
									onClick={() => setSp((p) => { const n = new URLSearchParams(p); n.set('page', String(page + 1)); return n })}
								>
									다음
								</button>
							</div>
						</>
					)
				}
			</Load>
			{adding && <NewAsset onClose={() => setAdding(false)} />}
			{importing && <Import onClose={() => setImporting(false)} />}
		</>
	)
}

/** The attribute inputs a type declares. */
export function AttrFields(props: {
	type: AssetType | undefined
	value: Record<string, string>
	onChange: (v: Record<string, string>) => void
}): ReactNode {
	const defs = props.type?.spec?.attributes ?? []
	if (defs.length === 0) return null
	return (
		<>
			{defs.map((d) => {
				const v = props.value[d.key] ?? ''
				const set = (x: string) => props.onChange({ ...props.value, [d.key]: x })
				const label = `${d.label || d.key}${d.unit ? ` (${d.unit})` : ''}${d.required ? ' *' : ''}`
				return (
					<Field key={d.key} label={label}>
						{d.type === 'enum' ? (
							<Select value={v} onChange={set} empty="-" options={d.options.map((o) => ({ value: o, label: o }))} required={d.required} />
						) : d.type === 'bool' ? (
							<Select value={v} onChange={set} empty="-" options={[{ value: 'true', label: '예' }, { value: 'false', label: '아니오' }]} />
						) : (
							<input
								type={d.type === 'number' ? 'number' : d.type === 'date' ? 'date' : 'text'}
								step="any"
								value={v}
								onChange={(e) => set(e.target.value)}
								required={d.required}
							/>
						)}
					</Field>
				)
			})}
		</>
	)
}

function NewAsset(props: { onClose: () => void }): ReactNode {
	const c = useCatalog()
	const act = useAct()
	const go = useNavigate()
	const [kind, setKind] = useState('item')
	const [name, setName] = useState('')
	const [tag, setTag] = useState('')
	const [type, setType] = useState('')
	const [model, setModel] = useState('')
	const [serial, setSerial] = useState('')
	const [to, setTo] = useState('')
	const [acquired, setAcquired] = useState('')
	const [since, setSince] = useState('')
	const [desc, setDesc] = useState('')
	const [attrs, setAttrs] = useState<Record<string, string>>({})
	const ty = c.types.find((t) => idStr(t.id) === type)

	return (
		<FormModal
			title="자산 등록"
			submit="등록"
			wide
			onClose={props.onClose}
			onSubmit={async () => {
				const v = await act(
					AssetService.method.add,
					{
						name,
						kind,
						tag,
						serial,
						desc,
						type: type === '' ? undefined : ref(idBytes(type)),
						model: model === '' ? undefined : ref(idBytes(model)),
						to: to === '' ? undefined : ref(idBytes(to)),
						acquiredAt: acquired === '' ? undefined : ts(new Date(acquired)),
						since: since === '' ? undefined : ts(new Date(since)),
						attributes: Object.fromEntries(Object.entries(attrs).filter(([, x]) => x !== '')),
						reason: '등록',
					},
					{ ok: '등록했습니다.' },
				)
				if (v === undefined) return false
				go(`/assets/${idStr(v.id)}`)
			}}
		>
			<Field label="종류">
				<Select value={kind} onChange={setKind} options={Object.entries(kindWord).map(([value, label]) => ({ value, label }))} />
			</Field>
			<Field label="이름 *">
				<input value={name} onChange={(e) => setName(e.target.value)} required autoFocus />
			</Field>
			<Field label="태그" hint="비우면 자동으로 매깁니다">
				<input value={tag} onChange={(e) => setTag(e.target.value)} placeholder="예: NB-101" />
			</Field>
			<Field label="유형">
				<TypeSelect value={type} onChange={(v) => { setType(v); const t = c.types.find((x) => idStr(x.id) === v); if (t !== undefined) setKind(t.kind) }} />
			</Field>
			{kind === 'item' && (
				<Field label="모델">
					<ModelSelect value={model} onChange={setModel} type={type} />
				</Field>
			)}
			{kind === 'item' && (
				<Field label="시리얼">
					<input value={serial} onChange={(e) => setSerial(e.target.value)} />
				</Field>
			)}
			<Field label="위치">
				<SpaceSelect value={to} onChange={setTo} empty="(최상위 / 위치 없음)" />
			</Field>
			<Field label="취득일">
				<input type="date" value={acquired} onChange={(e) => setAcquired(e.target.value)} />
			</Field>
			<Field label="기준 시각" hint="이 자산이 이 상태였던 시점. 비우면 지금">
				<input type="datetime-local" value={since} onChange={(e) => setSince(e.target.value)} />
			</Field>
			<AttrFields type={ty} value={attrs} onChange={setAttrs} />
			<Field label="설명" wide>
				<textarea value={desc} onChange={(e) => setDesc(e.target.value)} rows={2} />
			</Field>
		</FormModal>
	)
}

function Import(props: { onClose: () => void }): ReactNode {
	const act = useAct()
	const [file, setFile] = useState<File | undefined>()
	const [result, setResult] = useState<{ dry: boolean; created: number; updated: number; skipped: number; errors: string[]; warnings: string[] }>()

	const run = async (dry: boolean) => {
		if (file === undefined) return false
		const data = await readFile(file)
		const format = file.name.toLowerCase().endsWith('.xlsx') ? 'xlsx' : 'csv'
		const v = await act(AssetService.method.import, { format, data, dryRun: dry }, { ok: dry ? undefined : '가져왔습니다.' })
		if (v === undefined) return false
		setResult({ dry, created: v.created, updated: v.updated, skipped: v.skipped, errors: v.errors, warnings: v.warnings })
		return false
	}

	return (
		<FormModal title="자산 가져오기" submit="미리 보기" wide onClose={props.onClose} onSubmit={() => run(true)}>
			<p className="wide">
				CSV 또는 엑셀(xlsx) 첫 시트를 읽습니다. 열 이름은 <code>태그, 이름, 종류, 유형, 모델, 제조사, 시리얼, 상태, 컨디션, 위치, 위치태그, 사용자, 취득일, 기준일, 설명</code> 과
				<code>속성.키</code> 를 씁니다. 태그가 이미 있으면 그 자산을 고칩니다. 위치는 <code>본사/3층/회의실 A</code> 처럼 적으면 없는 공간을 만듭니다.
				먼저 미리 보기로 확인한 뒤 가져오세요. 내보낸 파일을 고쳐서 다시 가져올 수 있습니다.
			</p>
			<Field label="파일" wide>
				<input type="file" accept=".csv,.xlsx" onChange={(e) => { setFile(e.target.files?.[0]); setResult(undefined) }} required />
			</Field>
			{result !== undefined && (
				<div className="wide import-result">
					<p>
						<strong>{result.dry ? '미리 보기' : '완료'}</strong>: 새로 등록 {result.created} · 수정 {result.updated} · 변화 없음 {result.skipped}
					</p>
					{result.errors.length > 0 && (
						<>
							<p className="bad">오류 {result.errors.length}건 — 고친 뒤 다시 시도하세요. 아무것도 바뀌지 않았습니다.</p>
							<ul className="bad small">
								{result.errors.map((e, i) => (
									<li key={i}>{e}</li>
								))}
							</ul>
						</>
					)}
					{result.warnings.length > 0 && (
						<ul className="warn small">
							{result.warnings.map((e, i) => (
								<li key={i}>{e}</li>
							))}
						</ul>
					)}
					{result.dry && result.errors.length === 0 && (
						<button type="button" className="primary" onClick={() => void run(false)}>
							이대로 가져오기
						</button>
					)}
				</div>
			)}
		</FormModal>
	)
}
