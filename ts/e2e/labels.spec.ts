import { expect, people, said, signIn, test, workspace } from './rove.js'

// D28: a label is found by the eight characters printed under its QR, as well
// as by the address in it -- in any case, with or without the dash.
const printed = /[0-9A-HJKMNP-TV-Z]{4}-[0-9A-HJKMNP-TV-Z]{4}/

test('a label printed blank is found by the code under its QR and put on an asset', async ({ page }) => {
	await signIn(page, people.labels.login)

	// Opening the sheet is printing: it asks for the labels.
	await page.goto(`${workspace}/labels/print?count=1`)
	await expect(page.getByText('라벨 1장')).toBeVisible()
	await expect(page.getByText('미부착 라벨')).toBeVisible()
	const code = (await page.getByText(printed).innerText()).match(printed)?.[0]
	expect(code).toBeDefined()

	await page.goto(`${workspace}/scan`)
	await page.getByPlaceholder('태그 또는 라벨 주소').fill(code!.replace('-', '').toLowerCase())
	await page.getByRole('button', { name: '찾기' }).click()
	await expect(page.getByRole('heading', { name: '아직 아무 자산에도 붙지 않은 라벨' })).toBeVisible()
	await page.getByPlaceholder('태그, 이름, 시리얼로 찾기').fill('NB-053')
	await page.getByRole('button', { name: /NB-053/ }).click()
	await page.getByRole('button', { name: '연결', exact: true }).click()
	await said(page, '라벨을 연결했습니다.')
	await expect(page.getByRole('heading', { level: 1 })).toContainText('NB-053')

	// Bound, the same code opens the asset.
	await page.goto(`${workspace}/scan/${code}`)
	await expect(page.getByRole('heading', { level: 1 })).toContainText('NB-053')
})
