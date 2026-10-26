import QRCode from 'qrcode'
import { useEffect, useState, type ReactNode } from 'react'
import { Link, useSearchParams } from 'react-router-dom'

import type { Label } from '../../gen/rove/asset_pb.js'
import { LabelService, TenantDomainService, errorText, idBytes, idStr, labelCodeText } from '../api.js'
import { useCatalog } from '../catalog.js'
import { call, useRpc } from '../rpc.js'
import { useAssets } from '../pickers.js'
import { Spinner } from '../ui.js'

/**
 * One print per opening of the page. A dev build mounts everything twice, and
 * a second print would be a second set of labels nobody asked for.
 */
const printing = new Map<string, ReturnType<typeof print>>()

function print(ids: string[], blank: number) {
	return call(LabelService.method.print, ids.length > 0 ? { assetIds: ids.map(idBytes) } : { count: Math.min(Math.max(blank, 1), 200) })
}

/** The host a label's address is on, for the sheet's heading. */
function hostOf(u: string | undefined): string {
	try {
		return u === undefined ? '' : new URL(u).host
	} catch {
		return ''
	}
}

/**
 * A sheet of QR labels to print: one for each asset named, bound already, or
 * a number of blank ones to stick on things later.
 *
 * It asks for the labels when it is opened, so opening it is printing them:
 * reloading the page makes new ones. That is deliberate -- a label is a row,
 * and one that was never stuck on anything is simply never bound.
 */
export function LabelSheet(): ReactNode {
	const [sp] = useSearchParams()
	const c = useCatalog()
	const ids = (sp.get('assets') ?? '').split(',').filter((v) => v !== '')
	const blank = Number(sp.get('count') ?? '0')
	const status = useRpc(TenantDomainService.method.status, {})
	const [got, setGot] = useState<{ labels: Label[]; urls: string[]; qr: string[] }>()
	const [err, setErr] = useState('')
	const [size, setSize] = useState(sp.get('size') ?? 'm')
	const assets = useAssets(ids.map((s) => idBytes(s)))

	useEffect(() => {
		if (status.data === undefined) return
		let live = true
		void (async () => {
			try {
				const k = sp.toString()
				let p = printing.get(k)
				if (p === undefined) {
					p = print(ids, blank)
					printing.set(k, p)
					setTimeout(() => printing.delete(k), 5000)
				}
				const v = await p
				const qr = await Promise.all(v.urls.map((u) => QRCode.toDataURL(u, { margin: 0, errorCorrectionLevel: 'M', width: 256 })))
				if (live) setGot({ labels: v.labels, urls: v.urls, qr })
			} catch (e) {
				if (live) setErr(errorText(e))
			}
		})()
		return () => {
			live = false
		}
		// eslint-disable-next-line react-hooks/exhaustive-deps
	}, [status.data === undefined])

	if (err !== '') return <div className="print-page bad">{err}</div>
	if (got === undefined) return <div className="boot"><Spinner /></div>

	return (
		<div className="print-page">
			<div className="no-print toolbar">
				<Link to="/assets">← 돌아가기</Link>
				<span>
					라벨 {got.labels.length}장 · {status.data?.active?.host ?? hostOf(got.urls[0])}
				</span>
				<select value={size} onChange={(e) => setSize(e.target.value)}>
					<option value="s">작게 (40×20mm)</option>
					<option value="m">보통 (50×30mm)</option>
					<option value="l">크게 (70×40mm)</option>
				</select>
				<button className="primary" onClick={() => window.print()}>
					인쇄
				</button>
			</div>
			<div className={`labels ${size}`}>
				{got.labels.map((l, i) => {
					const a = assets.get(idStr(l.subjectId))
					return (
						<div key={idStr(l.id)} className="qr-label">
							<img src={got.qr[i]} alt="" />
							<div className="text">
								<strong>{a?.tag ?? ''}</strong>
								<span>{a?.name ?? '미부착 라벨'}</span>
								<small>
									{labelCodeText(l.code)} {c.me.workspaceName}
								</small>
							</div>
						</div>
					)
				})}
			</div>
		</div>
	)
}
