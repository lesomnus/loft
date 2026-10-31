import type { Locator } from '@playwright/test'

import { choose, expect, field, people, said, signIn, test, unique, workspace } from './loft.js'

// 구매 and 재고: what is ordered is received as assets one by one and as stock
// by the count, and stock is used, falls below what it should keep, and is
// received again.

test('a purchase is ordered, and received as assets one by one and as stock by the count', async ({ page }) => {
	const name = unique('e2e 모니터와 건전지')
	// The tags it is received under: a prefix nothing else in the run has.
	const prefix = `PO${Math.random().toString(36).slice(2, 5).toUpperCase()}`
	await signIn(page, people.assets.login)

	await page.goto(`${workspace}/purchases`)
	await page.getByRole('button', { name: '+ 구매 등록' }).click()
	const order = page.getByRole('dialog')
	await field(order, '이름').fill(name)
	await field(order, '주문 번호').fill(unique('PO-E2E'))
	const lines = order.getByRole('table')
	await line(lines, 0, { model: 'LG 27UP850', quantity: '2', cost: '450000', as: '자산 (하나씩)' })
	await order.getByRole('button', { name: '+ 줄 추가' }).click()
	await line(lines, 1, { model: 'AA 건전지', quantity: '30', cost: '500', as: '재고 (수량)' })
	await expect(order.getByText('합계 915,000원')).toBeVisible()
	await order.getByRole('button', { name: '저장', exact: true }).click()
	await said(page, '등록했습니다.')

	const ordered = page.getByRole('row').filter({ hasText: name })
	await expect(ordered).toContainText('주문')
	await expect(ordered).toContainText('915,000원')

	// Newest first, and then in the order they were made, the demo's first --
	// each read by the server, and not a page turned over.
	const purchases = page.getByRole('table').locator('tbody').getByRole('row')
	await expect(purchases.first()).toContainText(name)
	await page.getByLabel('순서').selectOption({ label: '오래된순' })
	await expect(purchases.first()).toContainText('모니터 추가 구매')
	await expect(purchases.last()).toContainText(name)

	await ordered.click()
	const opened = page.getByRole('dialog')
	await expect(opened.getByRole('row').filter({ hasText: 'LG 27UP850' })).toContainText('자산')
	await expect(opened.getByRole('row').filter({ hasText: 'AA 건전지' })).toContainText('재고')
	await opened.getByRole('button', { name: '입고 처리' }).click()
	const receive = page.getByRole('dialog')
	await choose(field(receive, '받은 곳'), '경영지원팀 사무실')
	await field(receive, '태그 앞부분').fill(prefix)
	await receive.getByRole('button', { name: '입고', exact: true }).click()
	await said(page, '입고했습니다.')

	// The monitors are assets of their own, under the prefix, where they came.
	await expect(page).toHaveURL(new RegExp(`/assets\\?q=${prefix}$`))
	for (const tag of [`${prefix}-001`, `${prefix}-002`]) {
		await expect(page.getByRole('row').filter({ hasText: tag })).toContainText('경영지원팀 사무실')
	}

	// The batteries are a count, kept where they came.
	await page.goto(`${workspace}/stock`)
	const kept = page
		.getByRole('row')
		.filter({ hasText: 'AA 건전지' })
		.filter({ hasText: '경영지원팀 사무실' })
	await expect(kept).toContainText('30 개')

	await page.goto(`${workspace}/purchases`)
	await expect(page.getByRole('row').filter({ hasText: name })).toContainText('입고 완료')
})

test('stock is used, falls to what it should keep, and is received again; its history says so', async ({ page, as }) => {
	const name = unique('e2e 복사용지')
	await signIn(page, people.assets.login)

	await page.goto(`${workspace}/stock`)
	await page.getByRole('button', { name: '+ 재고 품목' }).click()
	const make = page.getByRole('dialog')
	await choose(field(make, '모델'), 'A4 복사용지')
	await choose(field(make, '보관 장소'), '영업팀 사무실')
	await field(make, '이름').fill(name)
	await field(make, '지금 수량').fill('10')
	await field(make, '부족 기준').fill('4')
	await field(make, '단위').fill('박스')
	await make.getByRole('button', { name: '저장', exact: true }).click()
	await said(page, '만들었습니다.')
	const row = page.getByRole('row').filter({ hasText: name })
	await expect(row).toContainText('10 박스')

	// A member takes what they need: using stock is everybody's.
	const theirs = await as(people.stock.login)
	await theirs.goto(`${workspace}/stock`)
	const there = theirs.getByRole('row').filter({ hasText: name })
	await expect(there.getByRole('button', { name: '입고' })).toHaveCount(0)
	await there.getByRole('button', { name: '사용', exact: true }).click()
	const use = theirs.getByRole('dialog')
	await field(use, '수량').fill('7')
	await field(use, '사유').fill('e2e 회의 자료')
	await use.getByRole('button', { name: '사용', exact: true }).click()
	await said(theirs, '사용 처리했습니다.')
	// Three left, below the four it should keep, and drawn as short.
	const left = await under(theirs.getByRole('table'), there, '수량')
	await expect(left).toHaveText('3 박스')
	await expect(left).toHaveClass(/\bbad\b/)

	await page.reload()
	await row.getByRole('button', { name: '입고', exact: true }).click()
	const receive = page.getByRole('dialog')
	await expect(receive.getByText('지금 3박스')).toBeVisible()
	await field(receive, '수량').fill('5')
	await receive.getByRole('button', { name: '입고', exact: true }).click()
	await said(page, '입고했습니다.')
	const now = await under(page.getByRole('table'), row, '수량')
	await expect(now).toHaveText('8 박스')
	await expect(now).not.toHaveClass(/\bbad\b/)

	await row.getByRole('button', { name: '내역' }).click()
	const history = page.getByRole('dialog')
	await expect(history.getByRole('heading', { name: `${name} 내역` })).toBeVisible()
	// Newest first, and then in the order it happened, what it was made with
	// first -- each read by the server, and not a page turned over.
	const moves = history.getByRole('table')
	const rows = moves.locator('tbody').getByRole('row')
	await expect(rows).toHaveCount(3)
	await expect(await under(moves, rows.nth(0), '무엇')).toHaveText('입고')
	await expect(await under(moves, rows.nth(0), '변화')).toHaveText('+5')
	await expect(await under(moves, rows.nth(0), '남은 수량')).toHaveText('8')
	await history.getByLabel('순서').selectOption({ label: '오래된순' })
	await expect(await under(moves, rows.nth(0), '변화')).toHaveText('+10')
	for (const [i, [what, change, balance]] of [
		['입고', '+10', '10'],
		['사용', '-7', '3'],
		['입고', '+5', '8'],
	].entries()) {
		await expect(await under(moves, rows.nth(i), '무엇')).toHaveText(what)
		await expect(await under(moves, rows.nth(i), '변화')).toHaveText(change)
		await expect(await under(moves, rows.nth(i), '남은 수량')).toHaveText(balance)
	}
})

/** Fills in the `i`th line of a purchase, column by column. */
async function line(lines: Locator, i: number, v: { model: string; quantity: string; cost: string; as: string }): Promise<void> {
	const row = lines.locator('tbody').getByRole('row').nth(i)
	await choose((await under(lines, row, '모델')).getByRole('combobox'), v.model)
	await (await under(lines, row, '수량')).getByRole('spinbutton').fill(v.quantity)
	await (await under(lines, row, '단가 (원)')).getByRole('spinbutton').fill(v.cost)
	await choose((await under(lines, row, '입고 형태')).getByRole('combobox'), v.as)
}

/** The cell of `row` under the column headed `header`, as a person reads a table. */
async function under(table: Locator, row: Locator, header: string): Promise<Locator> {
	// The headings by what they are: Playwright gives a <th> with no scope no
	// role of its own.
	const headers = (await table.locator('thead th').allInnerTexts()).map((h) => h.trim())
	const i = headers.indexOf(header)
	if (i < 0) throw new Error(`no column headed "${header}": ${headers.join(', ')}`)
	return row.getByRole('cell').nth(i)
}
