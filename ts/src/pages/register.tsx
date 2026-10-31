import { useState, type ReactNode } from 'react'
import { useNavigate } from 'react-router-dom'

import type { AssetAddManyResponse, AssetAddRequest, AssetReadSheetResponse } from '../../gen/loft/asset_svc_pb.js'
import { AssetService, download, errorText, op, raw } from '../api.js'
import { useCatalog } from '../catalog.js'
import { useAct } from '../rpc.js'
import { Card, Empty, Field, PageHead, useToast } from '../ui.js'
import { kindWord } from '../words.js'

/** The form's column for what an AddMany calls a field, as `loft register` says it. */
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

/** What each column of the form takes: the form itself is only its header. */
const formColumns: [string, string][] = [
	['이름', '필수'],
	['종류', '물품, 공간, 키트, 그룹. 비우면 유형의 것, 그것도 없으면 물품'],
	['유형', '이름으로. 없는 이름이면 새로 만듭니다(관리자만)'],
	['모델', '이름으로. 없는 이름이면 새로 만듭니다'],
	['제조사', '모델을 찾고 만들 때 함께 봅니다'],
	['시리얼', ''],
	['태그', '비우면 정해 줍니다. 이미 있는 태그는 등록하지 않습니다'],
	['위치태그', '이미 있는 공간의 태그. 공간을 새로 만들지 않습니다'],
	['취득일', '2024-03-01처럼'],
	['기준일', '입고일. 이력이 이날부터입니다. 비우면 오늘'],
	['설명', ''],
	['상태', '사용중, 주문, 수리중, 분실, 불용, 폐기. 비우면 사용중'],
	['컨디션', '양호, 손상, 고장. 비우면 양호'],
	['속성.키', "유형의 속성. 열 이름을 '속성.' 다음에 속성 키로 적습니다 (예: 속성.색상)"],
]

type Read = { file: string; sheet: AssetReadSheetResponse; dry?: AssetAddManyResponse }

/**
 * Bulk registration (design 9.13): a CSV in loft's form, read by the
 * deployment, shown as what it would register -- every issue by its line --
 * and registered as one intake when nothing stops it. A workbook the
 * deployment refuses unopened, so the page says how to save one as a CSV.
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
			const v = await raw.asset.form({})
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
			const sheet = await raw.asset.readSheet({ data })
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
				sub="loft 양식의 시트로 새 자산을 한 번에 등록합니다. 이미 있는 자산은 고치지 않습니다."
				actions={<button onClick={() => void form()}>양식 내려받기</button>}
			/>
			<Card>
				<div className="form">
					<div className="fields">
						<Field label="시트" hint="양식에 맞춰 적어 CSV로 저장한 파일">
							<input type="file" accept=".csv,text/csv" disabled={busy} onChange={(e) => void pick(e.target.files?.[0])} />
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
			<FormGuide />
		</>
	)
}

function FormGuide(): ReactNode {
	return (
		<Card title="양식">
			<p className="mute small">
				한 줄에 자산 하나씩 적습니다. 새 자산만 등록하고, 이미 있는 자산은 고치지 않습니다. 엑셀에서 적었으면 저장할 때 파일 형식을 'CSV UTF-8(쉼표로 분리)'로 고릅니다. 엑셀 파일(.xlsx)은 받지 않습니다.
			</p>
			<p className="mute small">
				시리얼·태그·속성이 숫자뿐이면, 적기 전에 그 열의 셀 서식을 텍스트로 바꿉니다. 그러지 않으면 엑셀이 앞의 0을 지우고, 긴 숫자를 3.57951E+14처럼 줄여 저장합니다.
			</p>
			<div className="table-wrap">
				<table className="table">
					<thead>
						<tr>
							<th>열</th>
							<th>적는 것</th>
						</tr>
					</thead>
					<tbody>
						{formColumns.map(([column, what]) => (
							<tr key={column}>
								<td>{column}</td>
								<td>{what}</td>
							</tr>
						))}
					</tbody>
				</table>
			</div>
		</Card>
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
