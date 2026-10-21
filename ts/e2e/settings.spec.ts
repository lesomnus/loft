import { card, expect, field, people, said, signIn, test, unique } from './rove.js'

// Where a workspace is (#144): its owner says the zone its dates are told in,
// the currency a new amount is priced in, and how long a room waits for
// whoever booked it -- and an amount is then typed and shown in that currency.

test("a workspace's owner says where it is, and its amounts are in its currency", async ({ page }) => {
	const name = unique('e2e 뉴욕 지사')
	const bought = unique('e2e 케이블 구매')
	await signIn(page, people.place.login)

	// A workspace of their own, which they own.
	const switcher = page.locator('summary')
	await switcher.click()
	await page.getByRole('button', { name: '+ 새 워크스페이스' }).click()
	await page.getByPlaceholder('워크스페이스 이름').fill(name)
	await page.getByRole('button', { name: '만들기', exact: true }).click()
	await expect(switcher).toHaveText(name)

	await page.getByRole('navigation').getByRole('link', { name: /설정/ }).click()
	await page.getByRole('tab', { name: '워크스페이스' }).click()
	const place = card(page, '지역과 예약')
	// Saved, the page is drawn again: its dates follow the workspace from the
	// first thing drawn.
	const save = async (): Promise<void> => {
		const drawn = page.waitForEvent('load')
		await place.getByRole('button', { name: '저장', exact: true }).click()
		await drawn
	}
	await expect(field(place, '시간대')).toHaveValue('')
	await expect(field(place, '시간대').locator('option').first()).toHaveText('기본값 (Asia/Seoul)')
	await field(place, '시간대').selectOption('America/New_York')
	await field(place, '통화').selectOption('USD')
	await field(place, '노쇼 해제 (분)').fill('5')
	await save()

	// Said, as the workspace says it now.
	await expect(field(place, '시간대')).toHaveValue('America/New_York')
	await expect(field(place, '통화')).toHaveValue('USD')
	await expect(field(place, '노쇼 해제 (분)')).toHaveValue('5')

	// A room nobody has to check in to.
	await place.getByRole('checkbox', { name: '체크인을 받지 않음' }).check()
	await expect(field(place, '노쇼 해제 (분)')).toBeDisabled()
	await save()
	await expect(place.getByRole('checkbox', { name: '체크인을 받지 않음' })).toBeChecked()

	// Bought in dollars and cents.
	await page.getByRole('navigation').getByRole('link', { name: /구매/ }).click()
	await page.getByRole('button', { name: '+ 구매 등록' }).click()
	const order = page.getByRole('dialog')
	await field(order, '이름').fill(bought)
	const line = order.getByRole('table').locator('tbody').getByRole('row').first()
	const headers = (await order.getByRole('table').locator('thead th').allInnerTexts()).map((h) => h.trim())
	expect(headers).toContain('단가 (USD)')
	await line.getByRole('cell').nth(headers.indexOf('설명')).getByRole('textbox').fill('HDMI 케이블')
	await line.getByRole('cell').nth(headers.indexOf('수량')).getByRole('spinbutton').fill('2')
	await line.getByRole('cell').nth(headers.indexOf('단가 (USD)')).getByRole('spinbutton').fill('12.5')
	await expect(order.getByText('합계 US$25.00')).toBeVisible()
	await order.getByRole('button', { name: '저장', exact: true }).click()
	await said(page, '등록했습니다.')
	await expect(page.getByRole('row').filter({ hasText: bought })).toContainText('US$25.00')

	// And a time is the one on its wall, whatever the browser's (Seoul's)
	// says: ten in the morning typed is ten in the morning in New York.
	const desk = unique('e2e 책상')
	const work = unique('e2e 다리 수리')
	await page.getByRole('navigation').getByRole('link', { name: /자산/ }).click()
	await page.getByRole('button', { name: '+ 자산 등록' }).click()
	const add = page.getByRole('dialog')
	await field(add, '이름').fill(desk)
	await add.getByRole('button', { name: '등록', exact: true }).click()
	await said(page, '등록했습니다.')

	const tomorrow = new Intl.DateTimeFormat('en-CA', { timeZone: 'America/New_York', year: 'numeric', month: '2-digit', day: '2-digit' }).format(
		new Date(Date.now() + 86400_000),
	)
	await page.getByRole('navigation').getByRole('link', { name: /작업/ }).click()
	await page.getByRole('button', { name: '+ 작업 등록' }).click()
	const open = page.getByRole('dialog')
	const asset = field(open, '자산')
	await asset.getByRole('searchbox').fill(desk)
	await asset.getByRole('button', { name: new RegExp(desk) }).click()
	await field(open, '작업').fill(work)
	await field(open, '시작').fill(`${tomorrow}T10:00`)
	await field(open, '예상 비용 (USD)').fill('40.25')
	await open.getByRole('button', { name: '등록', exact: true }).click()
	await said(page, '등록했습니다.')
	const row = page.getByRole('row').filter({ hasText: work })
	await expect(row).toContainText('오전 10:00')
	await expect(row).toContainText('US$40.25')
})
