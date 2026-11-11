import type { Locator, Page } from '@playwright/test'

import { card, expect, people, refusal, signIn, test, workspace } from './loft.js'

// The workspace's records of what was done -- its operations, and the trail of
// every write -- read a page at a time, each page from the row the one before
// ended at (EventService.Recent, AuditService.Recent): back to the first thing
// the workspace holds, and back again.

/**
 * Reads `list` on to its last page, each page once it has been answered, and
 * says how many pages there were.
 */
async function toTheEnd(page: Page, list: Locator, method: string): Promise<number> {
	const on = list.getByRole('button', { name: '다음 페이지 ›' })
	const back = list.getByRole('button', { name: '‹ 앞 페이지' })
	let pages = 1
	while ((await on.count()) > 0) {
		const answered = page.waitForResponse((r) => r.url().endsWith(method))
		await on.click()
		await answered
		// Drawn once it may be pressed again.
		await expect(back).toBeEnabled()
		pages++
	}
	return pages
}

test("a workspace's operations and its trail are read back a page at a time, to the first thing it holds", async ({ page }) => {
	await signIn(page, people.reviewer.login)

	await page.goto(`${workspace}/settings?tab=events`)
	const events = card(page, '최근 작업 기록')
	await expect(events.getByRole('row').nth(1)).toBeVisible()
	await expect(events.getByRole('button', { name: '‹ 앞 페이지' })).toHaveCount(0)
	// The demo did more than a page of things, the first of which was to make
	// a type.
	expect(await toTheEnd(page, events, '/loft.EventService/Recent')).toBeGreaterThan(1)
	await expect(events.getByRole('row').last()).toContainText('type.add')
	await events.getByRole('button', { name: '‹ 앞 페이지' }).click()
	await expect(events.getByRole('button', { name: '다음 페이지 ›' })).toBeEnabled()

	await page.getByRole('tab', { name: '감사 기록' }).click()
	const trail = card(page, '감사 기록')
	await expect(trail.getByRole('row').nth(1)).toBeVisible()
	expect(await toTheEnd(page, trail, '/loft.AuditService/Recent')).toBeGreaterThan(1)
	// What the trail holds first is the workspace being made, by nobody's hand.
	const first = trail.getByRole('row').last()
	await expect(first).toContainText('TenantService/Add')
	await expect(first).toContainText('시스템')
})

// Who reads them (D79): the people who run the workspace and its auditors, as
// above. A member or a manager is shown none of the records -- no tab, and a
// link to the work log kept from before opens the first tab -- and the server
// refuses them the log; what happened to one asset is still on its page.
test("a member and a manager are not shown the workspace's records, and read an asset's history on its page", async ({ page, as }) => {
	await signIn(page, people.member.login)
	await page.goto(`${workspace}/settings`)
	await expect(page.getByRole('tab', { name: '유형' })).toBeVisible()
	for (const name of ['작업 기록', '감사 기록', '접속기록']) {
		await expect(page.getByRole('tab', { name, exact: true })).toHaveCount(0)
	}
	await page.goto(`${workspace}/settings?tab=events`)
	await expect(card(page, '자산 유형')).toBeVisible()
	await expect(card(page, '최근 작업 기록')).toHaveCount(0)
	expect(await refusal(page, 'EventService/Recent', {})).toContain('구성원 역할로는 할 수 없는 작업입니다')

	// Their own laptop, which the demo put at their desk.
	await page.goto(`${workspace}/assets`)
	await page.getByPlaceholder('이름, 태그, 시리얼, 모델, 사용자', { exact: true }).fill('NB-002')
	await page.getByRole('link', { name: `${people.member.name} 노트북`, exact: true }).click()
	await expect(card(page, '이력').getByRole('listitem').filter({ hasText: '자리 배치' }).first()).toBeVisible()

	const manager = await as(people.approver.login)
	await manager.goto(`${workspace}/settings`)
	await expect(manager.getByRole('tab', { name: '유형' })).toBeVisible()
	await expect(manager.getByRole('tab', { name: '작업 기록', exact: true })).toHaveCount(0)
	expect(await refusal(manager, 'EventService/List', {})).toContain('매니저 역할로는 할 수 없는 작업입니다')
})
