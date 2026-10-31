import { printed, video } from './qr.js'
import { expect, heed, people, signIn, test, workspace } from './loft.js'

// The scan page's camera, with a label held up to it: Chromium plays a video
// of the label's QR as its camera, and the page reads it as it reads a real
// one (scan.tsx) -- nothing typed.

test('a label held up to the camera opens what it is on', async ({ page, playwright, baseURL, breaches }, info) => {
	await signIn(page, people.labels.login)
	await page.goto(`${workspace}/assets`)
	await page.getByPlaceholder('이름, 태그, 시리얼, 모델, 사용자', { exact: true }).fill('NB-051')
	await expect(page.getByRole('row').filter({ hasText: 'NB-051' })).toHaveCount(1)
	await page.getByRole('link', { name: '예비 노트북', exact: true }).click()
	await expect(page.getByRole('heading', { level: 1 })).toContainText('NB-051')
	await page.getByRole('link', { name: '라벨 인쇄' }).click()
	const [label] = await printed(page)

	// A browser whose camera sees that label, for somebody with a phone.
	const file = info.outputPath('label.y4m')
	await video(label.url, file)
	const phone = await playwright.chromium.launch({
		args: ['--use-fake-ui-for-media-stream', '--use-fake-device-for-media-stream', `--use-file-for-fake-video-capture=${file}`],
	})
	try {
		const context = await phone.newContext({
			baseURL,
			locale: 'ko-KR',
			timezoneId: 'Asia/Seoul',
			serviceWorkers: 'block',
			permissions: ['camera'],
		})
		const scan = await context.newPage()
		// The camera page is held to its policy too.
		heed(scan, breaches)
		await signIn(scan, people.camera.login)
		await scan.getByRole('link', { name: /스캔/ }).click()
		await expect(scan.getByRole('heading', { level: 1 })).toContainText('NB-051')
	} finally {
		await phone.close()
	}
})
