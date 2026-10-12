import { writeFile } from 'node:fs/promises'

import type { Page } from '@playwright/test'
import jsQR from 'jsqr'
import QRCode from 'qrcode'

import { expect } from './rove.js'

/**
 * What the label sheet a page is on prints: the code under each QR, and the
 * address in the QR itself, read off the image the sheet draws as a scanner
 * reads it.
 */
export async function printed(page: Page): Promise<{ code: string; url: string }[]> {
	const labels = page.locator('.qr-label')
	await expect(labels.first()).toBeVisible()
	const out: { code: string; url: string }[] = []
	for (const label of await labels.all()) {
		const code = (await label.locator('small').innerText()).match(/[0-9A-HJKMNP-TV-Z]{4}-[0-9A-HJKMNP-TV-Z]{4}/)?.[0]
		if (code === undefined) throw new Error(`a label with no code under its QR: ${await label.innerText()}`)
		const image = await label.locator('img').evaluate(async (el: HTMLImageElement) => {
			await el.decode()
			const c = document.createElement('canvas')
			c.width = el.naturalWidth
			c.height = el.naturalHeight
			const g = c.getContext('2d')
			if (g === null) throw new Error('no 2d context')
			g.drawImage(el, 0, 0)
			return { width: c.width, height: c.height, data: Array.from(g.getImageData(0, 0, c.width, c.height).data) }
		})
		const url = read(image.width, image.height, Uint8ClampedArray.from(image.data))
		out.push({ code, url })
	}
	return out
}

/**
 * The text in a QR drawn as RGBA. The sheet prints it with no margin, which a
 * label's own edge gives it; here it is given one, as the label's white would.
 */
function read(width: number, height: number, rgba: Uint8ClampedArray): string {
	const pad = 32
	const w = width + 2 * pad
	const h = height + 2 * pad
	const out = new Uint8ClampedArray(w * h * 4).fill(255)
	for (let y = 0; y < height; y++) {
		out.set(rgba.subarray(y * width * 4, (y + 1) * width * 4), ((y + pad) * w + pad) * 4)
	}
	const v = jsQR(out, w, h)
	if (v === null) throw new Error('the label sheet printed a QR nothing reads')
	return v.data
}

/**
 * Writes a video of a QR of `text` that a camera would see held up to it --
 * dark on white, with the margin a label leaves -- for Chromium to play as its
 * camera (`--use-file-for-fake-video-capture`). It is Y4M, which Chromium
 * reads with nothing else installed: raw 4:2:0 frames after a line that says
 * their size, so a QR of a few dozen modules is written here a pixel at a time
 * rather than kept beside the tests.
 */
export async function video(text: string, file: string): Promise<void> {
	const width = 640
	const height = 480
	const qr = QRCode.create(text, { errorCorrectionLevel: 'M' })
	const n = qr.modules.size
	// As large as fits with four modules of margin all round.
	const scale = Math.floor(Math.min(width, height) / (n + 8))
	const left = Math.floor((width - n * scale) / 2)
	const top = Math.floor((height - n * scale) / 2)

	// Video white and video black: a camera's picture is in the range 16..235.
	const luma = Buffer.alloc(width * height, 235)
	for (let row = 0; row < n; row++) {
		for (let col = 0; col < n; col++) {
			if (!qr.modules.get(row, col)) continue
			for (let y = 0; y < scale; y++) {
				luma.fill(16, (top + row * scale + y) * width + left + col * scale, (top + row * scale + y) * width + left + (col + 1) * scale)
			}
		}
	}
	// No colour: both chroma planes at their middle.
	const chroma = Buffer.alloc((width / 2) * (height / 2) * 2, 128)
	const frame = Buffer.concat([Buffer.from('FRAME\n'), luma, chroma])

	// Two frames of the same, which Chromium plays over and over: a camera
	// held still.
	await writeFile(file, Buffer.concat([Buffer.from(`YUV4MPEG2 W${width} H${height} F2:1 Ip A1:1 C420jpeg\n`), frame, frame]))
}
