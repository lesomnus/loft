import { writeFile } from 'node:fs/promises'

import { card, choose, expect, field, people, said, signIn, test, unique, workspace } from './rove.js'

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

test('a sheet is previewed before anything is imported, and imports what it showed (csv)', async ({ page }, info) => {
	const name = unique('e2e 가져온 책상')
	const tag = `E2E-${Math.random().toString(36).slice(2, 7).toUpperCase()}`
	const file = info.outputPath('assets.csv')
	await writeFile(file, `태그,이름,위치\n${tag},${name},본사/4층/창고\n`)
	await signIn(page, people.imports.login)
	await page.goto(`${workspace}/assets`)

	await page.getByRole('button', { name: '가져오기', exact: true }).click()
	const dialog = page.getByRole('dialog')
	await field(dialog, '파일').setInputFiles(file)
	await dialog.getByRole('button', { name: '미리 보기', exact: true }).click()
	await expect(dialog.getByText('미리 보기: 새로 등록 1 · 수정 0 · 변화 없음 0')).toBeVisible()
	await expect(dialog.getByText(/^오류/)).toHaveCount(0)

	// Nothing is written until it is asked for.
	await page.goto(`${workspace}/assets?q=${encodeURIComponent(tag)}`)
	await expect(page.getByText('조건에 맞는 자산이 없습니다.')).toBeVisible()

	await page.getByRole('button', { name: '가져오기', exact: true }).click()
	await field(dialog, '파일').setInputFiles(file)
	await dialog.getByRole('button', { name: '미리 보기', exact: true }).click()
	await dialog.getByRole('button', { name: '이대로 가져오기' }).click()
	await said(page, '가져왔습니다.')
	await expect(dialog.getByText('완료: 새로 등록 1 · 수정 0 · 변화 없음 0')).toBeVisible()
	await dialog.getByRole('button', { name: '닫기' }).click()

	await page.goto(`${workspace}/assets?q=${encodeURIComponent(tag)}`)
	await expect(page.getByRole('link', { name, exact: true })).toBeVisible()
})

test('an exported sheet previews back with no errors (xlsx)', async ({ page }, info) => {
	await signIn(page, people.imports.login)
	await page.goto(`${workspace}/assets`)

	const download = page.waitForEvent('download')
	await page.getByRole('button', { name: '엑셀로 내보내기' }).click()
	const file = info.outputPath('assets.xlsx')
	await (await download).saveAs(file)

	await page.getByRole('button', { name: '가져오기', exact: true }).click()
	const dialog = page.getByRole('dialog')
	await field(dialog, '파일').setInputFiles(file)
	await dialog.getByRole('button', { name: '미리 보기', exact: true }).click()
	await expect(dialog.getByText(/^미리 보기: /)).toBeVisible()
	await expect(dialog.getByText(/^오류/)).toHaveCount(0)
	await expect(dialog.getByRole('button', { name: '이대로 가져오기' })).toBeVisible()
})
