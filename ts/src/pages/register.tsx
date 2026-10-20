import { useState, type ReactNode } from 'react'
import { useNavigate } from 'react-router-dom'

import type { AssetAddManyResponse, AssetAddRequest, AssetReadSheetResponse } from '../../gen/rove/asset_svc_pb.js'
import { AssetService, download, errorText, op, raw } from '../api.js'
import { useCatalog } from '../catalog.js'
import { useAct } from '../rpc.js'
import { Card, Empty, Field, PageHead, useToast } from '../ui.js'
import { kindWord } from '../words.js'

/** The form's column for what an AddMany calls a field, as `rove register` says it. */
const columnOf: Record<string, string> = {
	name: '이름',
	kind: '종류',
	type: '유형',
	type_name: '유형',
	model: '모델',
	model_name: '모델',
	maker: '제조사',
	serial: '시리얼',
	tag: '태그',
	to: '위치태그',
	acquired_at: '취득일',
	since: '기준일',
	desc: '설명',
	status: '상태',
	condition: '컨디션',
	attributes: '속성',
	custodian: '사용자',
	mode: '배치',
	slot: '자리',
}

type Read = { file: string; sheet: AssetReadSheetResponse; dry?: AssetAddManyResponse }

/**
 * Bulk registration (design 9.13): a sheet in rove's form, read by the
 * deployment, shown as what it would register -- every issue by its line --
 * and registered as one intake when nothing stops it.
 */
export function Register(): ReactNode {
	const c = useCatalog()
	const go = useNavigate()
	const toast = useToast()
	const act = useAct()
	const [read, setRead] = useState<Read>()
	const [reason, setReason] = useState('')
	const [busy, setBusy] = useState(false)
	const [failed, setFailed] = useState('')

	if (!c.can('manager')) return <Empty>대량 등록은 매니저 이상이 합니다.</Empty>

	const form = async () => {
		try {
			const v = await raw.asset.export({ format: 'xlsx', template: true })
			download(v.data, v.name, v.contentType)
		} catch (err) {
			toast(errorText(err), 'bad')
		}
	}

	const pick = async (f: File | undefined) => {
		setRead(undefined)
		setFailed('')
		if (f === undefined) return
		setBusy(true)
		try {
			const data = new Uint8Array(await f.arrayBuffer())
			const sheet = await raw.asset.readSheet({ data, format: f.name.toLowerCase().endsWith('.xlsx') ? 'xlsx' : '' })
			const dry = sheet.items.length === 0 ? undefined : await raw.asset.addMany({ items: sheet.items, dryRun: true })
			setRead({ file: f.name, sheet, dry })
		} catch (err) {
			setFailed(errorText(err))
		} finally {
			setBusy(false)
		}
	}

	// A write, so through `act`: what is on screen is read again after it --
	// the catalog too, which the types and models it made are new to.
	const register = async () => {
		if (read === undefined) return
		setBusy(true)
		const v = await act(AssetService.method.addMany, { items: read.sheet.items, op: op(), reason: reason.trim() }, { timeoutMs: 60_000 })
		setBusy(false)
		if (v === undefined) return
		toast(`${v.items.length}개를 등록했습니다`)
		go('/assets')
	}

	return (
		<>
			<PageHead
				title="대량 등록"
				sub="rove 양식의 시트로 새 자산을 한 번에 등록합니다. 이미 있는 자산은 고치지 않습니다."
				actions={<button onClick={() => void form()}>양식 내려받기</button>}
			/>
			<Card>
				<div className="form">
					<div className="fields">
						<Field label="시트" hint="양식에 맞춰 적은 .xlsx 또는 .csv">
							<input
								type="file"
								accept=".xlsx,.csv,text/csv,application/vnd.openxmlformats-officedocument.spreadsheetml.sheet"
								disabled={busy}
								onChange={(e) => void pick(e.target.files?.[0])}
							/>
						</Field>
					</div>
				</div>
			</Card>
			{failed !== '' && (
				<p className="bad" role="alert">
					{failed}
				</p>
			)}
			{read !== undefined && <Preview read={read} reason={reason} setReason={setReason} busy={busy} onRegister={() => void register()} />}
		</>
	)
}

function Preview(props: { read: Read; reason: string; setReason: (v: string) => void; busy: boolean; onRegister: () => void }): ReactNode {
	const { sheet, dry, file } = props.read
	const line = (i: number) => sheet.lines[i] ?? 0
	const issues = [
		...sheet.issues.map((v) => ({ where: v.line === 0 ? '시트' : `${v.line}행`, column: v.column, message: v.message })),
		...(dry?.issues ?? []).map((v) => ({ where: `${line(v.item)}행`, column: columnOf[v.field] ?? v.field, message: v.message })),
	]
	const bad = new Set((dry?.issues ?? []).map((v) => v.item))
	const n = sheet.items.length
	const made = [...(dry?.newTypes ?? []).map((v) => `유형 ${v}`), ...(dry?.newModels ?? []).map((v) => `모델 ${v}`)]

	return (
		<Card title="미리 보기" actions={<span>{file}</span>}>
			<p>
				{n === 0 ? '등록할 행이 없습니다.' : `${n}개를 등록합니다.`}
				{made.length > 0 && ` 새로 만듦: ${made.join(', ')}`}
			</p>
			{issues.length > 0 && (
				<div role="alert">
					<p className="bad">문제가 {issues.length}건 있습니다. 시트를 고쳐 다시 올리세요.</p>
					<ul>
						{issues.map((v, i) => (
							<li key={i}>
								{v.where}
								{v.column !== '' && ` ${v.column}`}: {v.message}
							</li>
						))}
					</ul>
				</div>
			)}
			{n > 0 && (
				<div className="table-wrap">
					<table className="table">
						<thead>
							<tr>
								<th>행</th>
								<th>태그</th>
								<th>이름</th>
								<th>종류</th>
								<th>유형</th>
								<th>모델</th>
								<th>위치</th>
							</tr>
						</thead>
						<tbody>
							{sheet.items.map((it, i) => (
								<tr key={i} className={bad.has(i) ? 'bad' : undefined}>
									<td>{line(i)}</td>
									<td>{dry?.tags[i] ?? it.tag}</td>
									<td>{it.name}</td>
									<td>{kindWord[it.kind] ?? it.kind}</td>
									<td>{it.typeName}</td>
									<td>{[it.maker, it.modelName].filter((v) => v !== undefined && v !== '').join(' ')}</td>
									<td>{placeOf(it)}</td>
								</tr>
							))}
						</tbody>
					</table>
				</div>
			)}
			<div className="form">
				<div className="fields">
					<Field label="사유" hint="이력에 남습니다. 예: 10월 입고">
						<input value={props.reason} onChange={(e) => props.setReason(e.target.value)} />
					</Field>
				</div>
				<footer>
					<button className="primary" disabled={props.busy || n === 0 || issues.length > 0} onClick={props.onRegister}>
						{n}개 등록
					</button>
				</footer>
			</div>
		</Card>
	)
}

/** Where a row says its asset goes: a space's tag. */
function placeOf(it: AssetAddRequest): string {
	return it.to?.key.case === 'tag' ? (it.to.key.value.tag ?? '') : ''
}
