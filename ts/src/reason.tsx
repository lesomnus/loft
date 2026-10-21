/**
 * What a download is for (rove#136). A download hands people's data out to be
 * kept somewhere else, so it says why, and the access record keeps it beside
 * who took what: the server refuses one that does not.
 */
import { encodeBinaryHeader } from '@connectrpc/connect'
import { useState, type ReactNode } from 'react'

import { Field, FormModal } from './ui.js'

/**
 * The header a call says it in: payday's `access.ReasonKey`. A `-bin` key, so
 * the reason is any text, base64 on the wire -- a header is ASCII, and a
 * browser refuses to send one that is not.
 */
export const reasonHeader = 'payday-reason-bin'

/** A reason as a call's options carry it. */
export function reasoned(why: string): { headers: Record<string, string> } {
	return { headers: { [reasonHeader]: encodeBinaryHeader(why) } }
}

/** Asks what a download is for, and hands the answer to `onReason`. */
export function ReasonModal(props: {
	title: ReactNode
	onClose: () => void
	onReason: (why: string) => Promise<boolean | void>
}): ReactNode {
	const [why, setWhy] = useState('')
	return (
		<FormModal title={props.title} submit="내려받기" onClose={props.onClose} onSubmit={() => props.onReason(why.trim())}>
			<p className="mute small">개인정보가 담긴 파일입니다. 내려받는 이유는 누가 언제 내려받았는지와 함께 접속기록에 남습니다.</p>
			<Field label="내려받는 이유" hint="예: 10월 자산 실사">
				<input required value={why} onChange={(e) => setWhy(e.target.value)} autoFocus />
			</Field>
		</FormModal>
	)
}
