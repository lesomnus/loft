import { Code, ConnectError } from '@connectrpc/connect'
import jsQR from 'jsqr'
import { useEffect, useRef, useState, type ReactNode } from 'react'
import { useNavigate, useParams } from 'react-router-dom'

import type { Label } from '../../gen/rove/asset_pb.js'
import type { Asset } from '../../gen/rove/asset_pb.js'
import { LabelService, errorText, idStr, labelWorkspace, openWorkspace, ref, workspace } from '../api.js'
import { useCatalog } from '../catalog.js'
import { AssetPicker } from '../pickers.js'
import { read, useAct } from '../rpc.js'
import { Card, Field, PageHead, Spinner } from '../ui.js'

/**
 * The camera, reading QR codes. The browser's own BarcodeDetector where there
 * is one, and a decoder in script where there is not.
 */
export function Camera(props: { onCode: (v: string) => void }): ReactNode {
	const video = useRef<HTMLVideoElement>(null)
	const [err, setErr] = useState('')
	const last = useRef({ v: '', at: 0 })
	// Kept in a ref so that a parent drawing again does not restart the camera.
	const onCode = useRef(props.onCode)
	onCode.current = props.onCode

	useEffect(() => {
		let stream: MediaStream | undefined
		let stop = false
		const canvas = document.createElement('canvas')
		const ctx = canvas.getContext('2d', { willReadFrequently: true })
		// eslint-disable-next-line @typescript-eslint/no-explicit-any
		const Detector = (window as any).BarcodeDetector
		// eslint-disable-next-line @typescript-eslint/no-explicit-any
		const detector: any = Detector !== undefined ? new Detector({ formats: ['qr_code', 'code_128', 'code_39', 'ean_13'] }) : undefined

		const seen = (v: string) => {
			const now = Date.now()
			if (v === last.current.v && now - last.current.at < 2500) return
			last.current = { v, at: now }
			navigator.vibrate?.(60)
			onCode.current(v)
		}

		const tick = async () => {
			if (stop) return
			const el = video.current
			if (el !== null && el.readyState >= 2) {
				try {
					if (detector !== undefined) {
						const found = await detector.detect(el)
						if (found.length > 0) seen(String(found[0].rawValue))
					} else if (ctx !== null) {
						canvas.width = el.videoWidth
						canvas.height = el.videoHeight
						ctx.drawImage(el, 0, 0)
						const img = ctx.getImageData(0, 0, canvas.width, canvas.height)
						const q = jsQR(img.data, img.width, img.height, { inversionAttempts: 'dontInvert' })
						if (q !== null) seen(q.data)
					}
				} catch {
					// A frame that could not be read; the next one may.
				}
			}
			setTimeout(() => void tick(), 200)
		}

		navigator.mediaDevices
			?.getUserMedia({ video: { facingMode: 'environment' }, audio: false })
			.then((s) => {
				stream = s
				if (video.current !== null) {
					video.current.srcObject = s
					void video.current.play()
				}
				void tick()
			})
			.catch((e: unknown) => setErr(`카메라를 열 수 없습니다: ${String(e)}. 휴대폰에서는 https 주소나 localhost에서만 카메라를 쓸 수 있습니다.`))

		if (navigator.mediaDevices === undefined) setErr('이 브라우저는 카메라를 쓸 수 없습니다. 코드를 직접 입력하세요.')

		return () => {
			stop = true
			stream?.getTracks().forEach((t) => t.stop())
		}
	}, [])

	return (
		<div className="camera">
			{err !== '' ? <p className="warn">{err}</p> : <video ref={video} playsInline muted />}
		</div>
	)
}

export function Scan(): ReactNode {
	const { code } = useParams()
	const go = useNavigate()
	const [text, setText] = useState('')
	const [busy, setBusy] = useState(false)
	const [err, setErr] = useState('')
	const [blank, setBlank] = useState<Label>()

	const resolve = async (v: string) => {
		const t = v.trim()
		if (t === '') return
		setBusy(true)
		setErr('')
		try {
			const r = await read(LabelService.method.resolve, { code: t })
			if (r.asset !== undefined) {
				go(`/assets/${idStr(r.asset.id)}`, { replace: code !== undefined })
				return
			}
			if (r.label !== undefined) {
				setBlank(r.label)
				return
			}
			setErr('아무것도 찾지 못했습니다.')
		} catch (e) {
			// Not a label here: perhaps one in another workspace of the
			// account's, which is opened at the same scan (D28).
			if (e instanceof ConnectError && e.code === Code.NotFound) {
				try {
					const w = await labelWorkspace(t)
					if (w !== undefined && w.alias !== workspace) {
						openWorkspace(w.alias, `/scan/${encodeURIComponent(t)}`, code !== undefined)
						return
					}
				} catch {
					// The answer here stands.
				}
			}
			setErr(errorText(e))
		} finally {
			setBusy(false)
		}
	}

	useEffect(() => {
		if (code !== undefined) void resolve(code)
		// eslint-disable-next-line react-hooks/exhaustive-deps
	}, [code])

	if (blank !== undefined) return <BindBlank label={blank} onDone={() => setBlank(undefined)} />

	return (
		<>
			<PageHead title="스캔" sub="QR 라벨이나 바코드를 비추면 그 자산이 열립니다. 태그를 입력해도 됩니다." />
			<Card>
				<form
					className="scanbar"
					onSubmit={(e) => {
						e.preventDefault()
						void resolve(text)
					}}
				>
					<input value={text} onChange={(e) => setText(e.target.value)} placeholder="태그 또는 라벨 주소" autoFocus />
					<button className="primary" disabled={busy}>
						찾기
					</button>
				</form>
				{busy && <Spinner />}
				{err !== '' && <p className="bad">{err}</p>}
				<Camera onCode={(v) => void resolve(v)} />
			</Card>
		</>
	)
}

/** A label printed ahead of time, not on anything yet: put it on something. */
function BindBlank(props: { label: Label; onDone: () => void }): ReactNode {
	const c = useCatalog()
	const act = useAct()
	const go = useNavigate()
	const [a, setA] = useState<Asset>()
	if (props.label.state === 'void') {
		return (
			<Card title="폐기된 라벨">
				<p>이 라벨은 폐기되었습니다. 새 라벨을 붙여 주세요.</p>
				<button onClick={props.onDone}>다시 스캔</button>
			</Card>
		)
	}
	return (
		<Card title="아직 아무 자산에도 붙지 않은 라벨">
			{c.can('manager') ? (
				<>
					<p>이 라벨을 붙일 자산을 고르세요.</p>
					<Field label="자산" wide group>
						<AssetPicker value={a} onChange={setA} />
					</Field>
					<div className="inline">
						<button onClick={props.onDone}>취소</button>
						<button
							className="primary"
							disabled={a === undefined}
							onClick={async () => {
								if (a === undefined) return
								const v = await act(LabelService.method.bind, { ref: ref(props.label.id), asset: ref(a.id) }, { ok: '라벨을 연결했습니다.' })
								if (v !== undefined) go(`/assets/${idStr(a.id)}`)
							}}
						>
							연결
						</button>
					</div>
				</>
			) : (
				<p>관리자에게 이 라벨을 자산에 연결해 달라고 요청하세요.</p>
			)}
		</Card>
	)
}
