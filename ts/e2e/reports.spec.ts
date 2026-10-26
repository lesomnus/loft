import { readFile } from 'node:fs/promises'

import { card, expect, field, momentAt, owner, people, rpc, signIn, test, unique, workspace, type Reply } from './rove.js'
import type { Locator, Page } from '@playwright/test'

// The reports (design 9.14): one screen, a table each and its CSV. What each
// test reads is what it made itself, through the server -- the forms that
// make them are the other tests' -- and found by its name in a table, under
// the column a person reads it in; the rest of the workspace is the demo's
// and every other test's, and is counted too.

/** The reports screen, at the report named `tab`, as the navigation opens it. */
async function report(page: Page, tab: string): Promise<void> {
	await page.goto(`${workspace}/`)
	await page.getByRole('navigation').getByRole('link', { name: /리포트/ }).click()
	await page.getByRole('tab', { name: tab }).click()
}

/** What the row of `table` that says `row` has under the column `column`. */
async function cell(table: Locator, row: string, column: string): Promise<Locator> {
	await expect(table.getByRole('row').filter({ hasText: row })).toHaveCount(1)
	const heads = (await table.getByRole('columnheader').allInnerTexts()).map((v) => v.trim())
	const i = heads.indexOf(column)
	expect(i, `"${column}" is none of ${heads.join(', ')}`).toBeGreaterThanOrEqual(0)
	return table.getByRole('row').filter({ hasText: row }).getByRole('cell').nth(i)
}

test('what there is: a type of its own counted, by status', async ({ page, as }) => {
	await signIn(page, owner.login, owner.password)
	const kind = unique('e2e 리포트 유형')
	const t = await rpc(page, 'AssetTypeService/Add', { name: kind, kind: 'item' })
	for (const n of [1, 2]) {
		await rpc(page, 'AssetService/Add', { name: `${kind} ${n}`, kind: 'item', type: { id: t.id } })
	}

	await report(page, '자산 현황')
	const table = page.getByRole('table', { name: '유형별 자산' })
	await expect(await cell(table, kind, '합계')).toHaveText('2')
	await expect(await cell(table, kind, '사용중')).toHaveText('2')
	await expect(await cell(table, kind, '분실')).toHaveText('0')

	// An auditor reads the reports; a member is not shown them.
	const auditor = await as(people.reviewer.login)
	await report(auditor, '자산 현황')
	await expect(await cell(auditor.getByRole('table', { name: '유형별 자산' }), kind, '합계')).toHaveText('2')
	const member = await as(people.member.login)
	await expect(member.getByRole('navigation').getByRole('link', { name: /자산/ }).first()).toBeVisible()
	await expect(member.getByRole('navigation').getByRole('link', { name: /리포트/ })).toHaveCount(0)
})

test("what is out: a loan of its own, who has it, and its CSV, which says what it is for", async ({ page }, info) => {
	await signIn(page, owner.login, owner.password)
	const who = unique('e2e 리포트 대여자')
	const what = unique('e2e 리포트 대여품')
	const person = await rpc(page, 'PartyService/Add', { name: who, kind: 'person' })
	const thing = await rpc(page, 'AssetService/Add', { name: what, kind: 'item' })
	await rpc(page, 'CustodyService/Add', { party: { id: person.id }, kind: 'loan', dueAt: momentAt(7, 18), lines: [{ asset: { id: thing.id } }] })

	await report(page, '대여 현황')
	await page.getByLabel('구분').selectOption({ label: '대여' })
	const table = page.getByRole('table', { name: '나가 있는 것' })
	await expect(await cell(table, who, '무엇')).toContainText(what)
	await expect(await cell(table, who, '수량')).toHaveText('1')
	await expect(await cell(table, who, '연체')).toHaveText('-')

	// It names people, so it says what for first, and the access record keeps
	// that (rove#136).
	await page.getByRole('button', { name: 'CSV 내려받기' }).click()
	await field(page, '내려받는 이유').fill('e2e: 대여 점검')
	const download = page.waitForEvent('download')
	await page.getByRole('button', { name: '내려받기', exact: true }).click()
	const got = await download
	expect(got.suggestedFilename()).toMatch(/^rove-loans-\d{8}\.csv$/)
	const file = info.outputPath('loans.csv')
	await got.saveAs(file)
	const lines = (await readFile(file)).toString('utf8').split('\n')
	expect(lines[0]).toBe('\ufeff받은 사람,구분,태그,이름,수량,단위,건넨 날,반납 기한,연체(일),인수 확인')
	expect(lines.some((l) => l.startsWith(`${who},대여,`) && l.includes(what))).toBe(true)
})

test('what is late: two of a name, told apart by their teams wherever it is listed, and late today', async ({ page }, info) => {
	await signIn(page, owner.login, owner.password)
	const name = unique('e2e 동명이인')
	const teams = [unique('e2e 대여팀'), unique('e2e 대여팀')]
	// Due in a moment, and waited out: nothing is lent till a time gone by.
	const due = new Date(Date.now() + 4_000)
	for (const team of teams) {
		const t = await rpc(page, 'PartyService/Add', { name: team, kind: 'team' })
		const p = await rpc(page, 'PartyService/Add', { name, kind: 'person', parentId: t.id })
		const a = await rpc(page, 'AssetService/Add', { name: unique('e2e 동명이인 노트북'), kind: 'item' })
		await rpc(page, 'CustodyService/Add', { party: { id: p.id }, kind: 'loan', dueAt: due.toISOString(), lines: [{ asset: { id: a.id } }] })
	}
	const told = teams.map((t) => `${name} · ${t}`)
	await expect.poll(() => Date.now(), { timeout: 10_000 }).toBeGreaterThan(due.getTime() + 500)
	// How late, in the workspace's days (D54): due earlier today is late and
	// no day late -- a day, had Seoul's midnight come since.
	const day = (d: Date): string => new Intl.DateTimeFormat('en-CA', { timeZone: 'Asia/Seoul' }).format(d)
	const today = day(due) === day(new Date())

	await report(page, '대여 현황')
	await page.getByLabel('기한').selectOption({ label: '반납 기한 지난 것만' })
	const table = page.getByRole('table', { name: '나가 있는 것' })
	for (const who of told) {
		await expect(await cell(table, who, '연체')).toHaveText(today ? '오늘' : '1일')
	}
	await page.getByRole('button', { name: 'CSV 내려받기' }).click()
	await field(page, '내려받는 이유').fill('e2e: 연체 점검')
	const download = page.waitForEvent('download')
	await page.getByRole('button', { name: '내려받기', exact: true }).click()
	const file = info.outputPath('late.csv')
	await (await download).saveAs(file)
	const lines = (await readFile(file)).toString('utf8').split('\n')
	for (const who of told) {
		expect(lines.filter((l) => l.startsWith(`${who},대여,`))).toHaveLength(1)
	}

	// The dashboard's card of what is late says it as the report does, and
	// so does the list it leads to.
	await page.goto(`${workspace}/`)
	const late = card(page, '연체된 대여')
	for (const who of told) {
		await expect(late.getByRole('listitem').filter({ hasText: who })).toContainText(today ? '오늘 지남' : '1일 지남')
	}
	await late.getByRole('link', { name: '전체' }).click()
	for (const who of told) {
		await expect(page.getByRole('row').filter({ hasText: who })).toContainText('연체')
	}
})

test('how what may be booked was used: a booking of its own, picked up and returned', async ({ page }) => {
	await signIn(page, owner.login, owner.password)
	const name = unique('e2e 리포트 카메라')
	const camera = await rpc(page, 'AssetService/Add', { name, kind: 'item' })
	await rpc(page, 'BookableService/Add', { asset: { id: camera.id } })
	const me = (await rpc(page, 'PartyService/Me', {})) as Reply & { party: Reply }
	// Begun a few minutes ago, which is in the last thirty days whenever it is.
	const now = Date.now()
	const r = await rpc(page, 'ReservationService/Add', {
		name: unique('e2e 리포트 촬영'),
		items: [{ resource: { id: camera.id } }],
		beginsAt: new Date(now - 5 * 60_000).toISOString(),
		endsAt: new Date(now + 55 * 60_000).toISOString(),
	})
	const c = await rpc(page, 'CustodyService/Add', {
		party: { id: me.party.id },
		reservationId: r.id,
		dueAt: new Date(now + 55 * 60_000).toISOString(),
		lines: [{ asset: { id: camera.id } }],
	})
	await rpc(page, 'CustodyService/Return', { ref: { id: c.id } })

	await report(page, '예약 이용률')
	const table = page.getByRole('table', { name: '자원별 이용률' })
	await expect(await cell(table, name, '예약')).toHaveText('1')
	await expect(await cell(table, name, '이용')).toHaveText('1')
	await expect(await cell(table, name, '노쇼')).toHaveText('0')
	await expect(await cell(table, name, '예약 시간')).toHaveText('1.0')
})

test('what a count found: one of its own, a thing missing and reported lost', async ({ page }) => {
	await signIn(page, owner.login, owner.password)
	const name = unique('e2e 리포트 실사')
	const room = await rpc(page, 'AssetService/Add', { name: unique('e2e 리포트 창고'), kind: 'space' })
	const seen = await rpc(page, 'AssetService/Add', { name: unique('e2e 실사 물품'), kind: 'item', to: { id: room.id } })
	await rpc(page, 'AssetService/Add', { name: unique('e2e 실사 물품'), kind: 'item', to: { id: room.id } })
	const count = await rpc(page, 'InventoryCountService/Add', { scope: { id: room.id }, name })
	await rpc(page, 'InventoryCountService/Scan', { ref: { id: count.id }, asset: { id: seen.id } })
	await rpc(page, 'InventoryCountService/Reconcile', { ref: { id: count.id } })
	const found = (await rpc(page, 'CountFindingService/List', { filters: [{ count: { id: count.id } }] })) as Reply & { items: (Reply & { kind: string })[] }
	const missing = found.items.find((f) => f.kind === 'missing')
	expect(missing, 'what was never seen').toBeDefined()
	await rpc(page, 'InventoryCountService/Resolve', { ref: { id: missing?.id }, resolution: 'lost' })

	await report(page, '실사 결과')
	const table = page.getByRole('table', { name: '실사별 결과' })
	await expect(await cell(table, name, '확인')).toHaveText('1')
	await expect(await cell(table, name, '미발견')).toHaveText('1')
	await expect(await cell(table, name, '분실 처리')).toHaveText('1')
	await expect(await cell(table, name, '미처리')).toHaveText('0')
})

test("how stock moved: a stock of its own received and used, and its CSV, which names nobody", async ({ page }, info) => {
	await signIn(page, owner.login, owner.password)
	const name = unique('e2e 리포트 소모품')
	const room = await rpc(page, 'AssetService/Add', { name: unique('e2e 리포트 선반'), kind: 'space' })
	const model = await rpc(page, 'ItemModelService/Add', { name })
	const stock = await rpc(page, 'StockService/Add', { model: { id: model.id }, space: { id: room.id }, quantity: '10' })
	await rpc(page, 'StockService/Consume', { ref: { id: stock.id }, quantity: '3' })

	await report(page, '재고 소모')
	const table = page.getByRole('table', { name: '재고별 입출고' })
	await expect(await cell(table, name, '기초')).toHaveText('0')
	await expect(await cell(table, name, '입고')).toHaveText('10')
	await expect(await cell(table, name, '사용')).toHaveText('3')
	await expect(await cell(table, name, '기말')).toHaveText('7')

	// Nobody's data, so it asks for no reason.
	const download = page.waitForEvent('download')
	await page.getByRole('button', { name: 'CSV 내려받기' }).click()
	const got = await download
	expect(got.suggestedFilename()).toMatch(/^rove-stock-\d{8}-\d{8}\.csv$/)
	const file = info.outputPath('stock.csv')
	await got.saveAs(file)
	const text = (await readFile(file)).toString('utf8')
	expect(text.split('\n')[0]).toMatch(/^\ufeff재고,위치,단위,기초,입고,/)
	expect(text).toContain(`${name},`)
})
