import type { Locator, Page } from '@playwright/test'

import { card, expect, people, signIn, test, workspace } from './loft.js'

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
