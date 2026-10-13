import { readFile } from 'node:fs/promises'

import { expect, field, people, said, signIn, test, unique } from './rove.js'
import { unzip } from './zip.js'

// A workspace's end (D29): its owner closes it, which destroys nothing; while
// it is closed they take its data out, and restore it.

test('a workspace is closed by its owner, its data taken out, and restored', async ({ page }) => {
	const name = unique('e2e 닫을 곳')
	const desk = unique('e2e 책상')
	await signIn(page, people.closer.login)

	// A workspace of their own, with something in it.
	const switcher = page.locator('summary')
	await switcher.click()
	await page.getByRole('button', { name: '+ 새 워크스페이스' }).click()
	await page.getByPlaceholder('워크스페이스 이름').fill(name)
	await page.getByRole('button', { name: '만들기', exact: true }).click()
	await expect(switcher).toHaveText(name)
	const alias = new URL(page.url()).pathname.split('/')[1].slice(1)
	await page.getByRole('navigation').getByRole('link', { name: /자산/ }).click()
	await page.getByRole('button', { name: '+ 자산 등록' }).click()
	const add = page.getByRole('dialog')
	await field(add, '이름').fill(desk)
	await add.getByRole('button', { name: '등록', exact: true }).click()
	await said(page, '등록했습니다.')

	// Closed by typing its name.
	await page.getByRole('navigation').getByRole('link', { name: /설정/ }).click()
	await page.getByRole('tab', { name: '워크스페이스' }).click()
	const close = page.getByRole('button', { name: '워크스페이스 닫기' })
	await expect(close).toBeDisabled()
	await field(page, `확인을 위해 워크스페이스 이름(${name})을 그대로 입력하세요`).fill(name)
	await field(page, '닫는 이유 (선택)').fill('e2e: 끝난 실험')
	await close.click()
	await expect(page.getByRole('heading', { name: `${name} — 닫힌 워크스페이스` })).toBeVisible()
	await expect(page.getByRole('navigation')).toHaveCount(0)

	// What it has, taken out while it is closed.
	const taken = page.waitForEvent('download')
	await page.getByRole('link', { name: '데이터 내려받기 (zip)' }).click()
	const download = await taken
	expect(download.suggestedFilename()).toMatch(new RegExp(`^${alias}-\\d{8}\\.zip$`))
	const zip = unzip(await readFile(await download.path()))
	expect(JSON.parse(zip.get('manifest.json')?.toString() ?? '{}')).toMatchObject({ tenant: { name, alias } })
	expect(zip.get('rows/asset.jsonl')?.toString()).toContain(desk)

	// And restored, with what it had.
	await page.getByRole('button', { name: '복구', exact: true }).click()
	await expect(page.getByRole('button', { name: '로그아웃', exact: true })).toBeVisible()
	await expect(switcher).toHaveText(name)
	await page.getByRole('navigation').getByRole('link', { name: /자산/ }).click()
	await expect(page.getByRole('link', { name: desk, exact: true })).toBeVisible()
})
