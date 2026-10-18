import { readFile } from 'node:fs/promises'

import { card, choose, expect, field, people, said, signIn, test, unique, workspace } from './rove.js'
import { unzip } from './zip.js'

test('an asset is registered, found, opened and moved, and its timeline says so', async ({ page }) => {
	const name = unique('e2e 의자')
	await signIn(page, people.assets.login)

	await page.getByRole('navigation').getByRole('link', { name: /자산/ }).click()
	await page.getByRole('button', { name: '+ 자산 등록' }).click()
	const add = page.getByRole('dialog')
	await field(add, '이름').fill(name)
	await choose(field(add, '위치'), '창고')
	await add.getByRole('button', { name: '등록', exact: true }).click()
	await said(page, '등록했습니다.')
	const head = page.getByRole('heading', { level: 1 })
	await expect(head).toContainText(name)
	// Left blank, the tag is made up for it.
	const tag = (await head.locator('code').innerText()).trim()
	expect(tag).not.toBe('')

	await page.getByRole('navigation').getByRole('link', { name: /자산/ }).click()
	await page.getByPlaceholder('이름, 태그, 시리얼, 모델, 사용자', { exact: true }).fill(name)
	await page.getByRole('link', { name, exact: true }).click()
	await expect(head).toContainText(name)

	await page.getByRole('button', { name: '이동', exact: true }).click()
	const move = page.getByRole('dialog')
	await expect(move.getByRole('heading', { name: `${tag} 이동` })).toBeVisible()
	await choose(field(move, '공간'), '디자인팀 사무실')
	await field(move, '사유').fill('e2e: 자리를 옮김')
	await move.getByRole('button', { name: '이동', exact: true }).click()
	await said(page, '옮겼습니다.')

	// Where it is now, and on the timeline the placement that began, with why.
	await expect(card(page, '정보')).toContainText('디자인팀 사무실')
	await expect(page.getByRole('listitem').filter({ hasText: '디자인팀 사무실 안' })).toContainText('e2e: 자리를 옮김')
})

test('the register is exported as a workbook to read', async ({ page }, info) => {
	await signIn(page, people.exports.login)
	await page.goto(`${workspace}/assets`)

	const download = page.waitForEvent('download')
	await page.getByRole('button', { name: '엑셀로 내보내기' }).click()
	const got = await download
	expect(got.suggestedFilename()).toMatch(/^rove-assets-\d{8}\.xlsx$/)

	// A workbook: a zip whose first sheet is the register.
	const file = info.outputPath('assets.xlsx')
	await got.saveAs(file)
	const parts = unzip(await readFile(file))
	expect(parts.has('xl/workbook.xml')).toBe(true)
	expect([...parts.values()].map((v) => v.toString()).join('')).toContain('위치태그')

	// And nothing reads one back: the sheet import is gone (design D34).
	await expect(page.getByRole('button', { name: '가져오기', exact: true })).toHaveCount(0)
})
