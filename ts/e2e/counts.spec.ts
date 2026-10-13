import { card, choose, expect, field, people, said, signIn, test, unique, workspace } from './rove.js'

test('an inventory count of a room finds what is scanned in it', async ({ page }) => {
	const name = unique('e2e 서버실 실사')
	await signIn(page, people.counts.login)

	await page.goto(`${workspace}/counts`)
	await page.getByRole('button', { name: '+ 실사 시작' }).click()
	const start = page.getByRole('dialog')
	await choose(field(start, '범위 (공간)'), '서버실')
	await field(start, '이름').fill(name)
	await start.getByRole('button', { name: '시작', exact: true }).click()
	await said(page, '시작했습니다.')
	await expect(page.getByRole('heading', { level: 1 })).toHaveText(name)

	// A scanner types what it read and presses Enter.
	const scan = page.getByPlaceholder('QR을 스캔하거나 태그를 입력 후 Enter')
	await scan.fill('RK-001')
	await scan.press('Enter')
	await said(page, '확인: RK-001')
	await expect(scan).toHaveValue('')

	const found = card(page, '결과').getByRole('listitem').filter({ hasText: 'RK-001' })
	await expect(found).toContainText('서버 랙 A')
	await expect(found.getByText('확인', { exact: true })).toBeVisible()
})
