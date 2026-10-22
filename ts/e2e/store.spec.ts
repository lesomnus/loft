import type { Page } from '@playwright/test'

import { card, choose, escape, expect, field, owner, people, said, signIn, submitSignIn, test, unique, workspace } from './rove.js'

// What a page keeps is its caller's, as payday's store is (`offline.ts`): the
// answers it was given and the scans it could not send, kept for one person in
// one workspace and said on every call it makes (`payday-identity`). A
// sign-out forgets it, in every tab; a session that ends by itself does not,
// and what waited goes once the same person is back; and nobody else's page
// keeps any of it, or is answered as them.

/**
 * A count of the store room the demo's owner starts, one every member scans
 * in (자가 실사): where the scans below go.
 */
async function started(as: (login: string, password?: string) => Promise<Page>): Promise<string> {
	const o = await as(owner.login, owner.password)
	await o.goto(`${workspace}/counts`)
	await o.getByRole('button', { name: '+ 실사 시작' }).click()
	const start = o.getByRole('dialog')
	await choose(field(start, '범위 (공간)'), '창고')
	await field(start, '이름').fill(unique('e2e 창고 실사'))
	await start.getByRole('checkbox', { name: /자가 실사/ }).check()
	await start.getByRole('button', { name: '시작', exact: true }).click()
	await said(o, '시작했습니다.')
	return new URL(o.url()).pathname
}

/** The account the browser is signed in as, by its identifier. */
async function account(page: Page): Promise<string> {
	const res = await page.request.get('/account')
	expect(res.status()).toBe(200)
	return ((await res.json()) as { id: string }).id
}

/** What this browser keeps for account `id`: answers, and writes waiting to be sent. */
async function kept(page: Page, id: string): Promise<{ answers: number; waiting: number }> {
	return page.evaluate(async (id) => {
		const mine = (k: unknown): boolean => String(k).startsWith(`${id}/`)
		// Opened only when the page made it, so that asking does not make it.
		const there = (await indexedDB.databases()).some((d) => d.name === 'rove')
		const answers = !there
			? 0
			: await new Promise<number>((resolve, reject) => {
					const req = indexedDB.open('rove')
					req.onerror = () => reject(req.error)
					req.onsuccess = () => {
						const d = req.result
						const keys = d.transaction('reads').objectStore('reads').getAllKeys()
						keys.onsuccess = () => {
							d.close()
							resolve(keys.result.filter(mine).length)
						}
						keys.onerror = () => reject(keys.error)
					}
				})
		const waiting = (JSON.parse(localStorage.getItem('rove.waiting') ?? '[]') as { scope: string }[]).filter((w) => mine(w.scope)).length
		return { answers, waiting }
	}, id)
}

/** A scan the server cannot be reached for, which waits to be sent. */
async function unsent(page: Page, code: string): Promise<void> {
	await page.route('**/rove.InventoryCountService/Scan', (r) => r.abort())
	const scan = page.getByPlaceholder('QR을 스캔하거나 태그를 입력 후 Enter')
	await scan.fill(code)
	await scan.press('Enter')
	await expect(page.getByText('보내지 못한 스캔 1건')).toBeVisible()
	await page.unroute('**/rove.InventoryCountService/Scan')
}

/** The person the page is drawn for, as its side says. */
function who(page: Page, name: string) {
	return page.getByRole('complementary').getByRole('link', { name: new RegExp(`^${escape(name)}`) })
}

const nothing = { answers: 0, waiting: 0 }

test("a sign-out forgets what the page kept for the person, and the next person's page has none of it", async ({ page, as }) => {
	const count = await started(as)
	await signIn(page, people.keeps.login)
	const id = await account(page)
	await page.goto(count)
	await unsent(page, 'RK-001')
	const was = await kept(page, id)
	expect(was.answers).toBeGreaterThan(0)
	expect(was.waiting).toBe(1)
	// The same person in another tab, which a sign-out here is said to.
	const second = await page.context().newPage()
	await second.goto(count)
	await expect(who(second, people.keeps.name)).toBeVisible()

	await page.getByRole('button', { name: '로그아웃', exact: true }).click()
	await expect(page.getByLabel('아이디 또는 이메일')).toBeVisible()
	await expect(second.getByLabel('아이디 또는 이메일')).toBeVisible()
	await expect.poll(() => kept(page, id)).toEqual(nothing)

	// Somebody else signs in at the same browser, at the same count.
	await submitSignIn(page, people.keepsNext.login)
	await expect(who(page, people.keepsNext.name)).toBeVisible()
	await expect(page.getByPlaceholder('QR을 스캔하거나 태그를 입력 후 Enter')).toBeVisible()
	await expect(page.getByText('보내지 못한 스캔')).toHaveCount(0)
})

test('a session that ends by itself is not a sign-out: what could not be sent waits for the person, and goes once they are back', async ({ page, as }) => {
	const count = await started(as)
	await signIn(page, people.waits.login)
	await page.goto(count)
	await unsent(page, 'RK-001')

	// The browser no longer holds the session, and the next call is refused.
	await page.context().clearCookies()
	await page.getByRole('button', { name: '지금 보내기' }).click()
	await expect(page.getByLabel('아이디 또는 이메일')).toBeVisible()

	// The same person, back: the scan waited, and goes now.
	await submitSignIn(page, people.waits.login)
	await expect(page.getByText('보내지 못한 스캔 1건')).toBeVisible()
	await page.getByRole('button', { name: '지금 보내기' }).click()
	await expect(page.getByText('보내지 못한 스캔')).toHaveCount(0)
	await expect(card(page, '결과').getByRole('listitem').filter({ hasText: 'RK-001' })).toBeVisible()
})

test("somebody else's sign-in keeps nothing of the person before, and a page left behind by it is refused rather than answered as them", async ({ page, as }) => {
	const count = await started(as)
	await signIn(page, people.leftBehind.login)
	const first = await account(page)
	await page.goto(count)
	await unsent(page, 'RK-001')
	await expect(who(page, people.leftBehind.name)).toBeVisible()

	// In another tab of the same browser, the session goes and somebody else
	// signs in: nothing of the first person's is kept.
	await page.context().clearCookies()
	const other = await page.context().newPage()
	await signIn(other, people.signsInNext.login)
	await expect.poll(() => kept(other, first)).toEqual(nothing)

	// The page left behind asks for something. It says whose it is, so it is
	// refused rather than answered as the other person; it asks who is signed
	// in now, and starts again as them.
	await page.getByRole('navigation').getByRole('link', { name: /자산/ }).click()
	await expect(who(page, people.signsInNext.name)).toBeVisible()
	await expect.poll(() => kept(page, first)).toEqual(nothing)
})
