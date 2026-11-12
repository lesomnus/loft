import type { APIRequestContext, Page } from '@playwright/test'

import { card, choose, expect, field, makeWorkspace, owner, people, refused, said, signIn, test, unique, workspace, workspacesOf } from './loft.js'

// Run against the server that bills (playwright.config.ts): to a levyan of
// the test's own (e2e/levyan.sh, internal/levyantest). Nothing reaches
// levyan's. What a payer does where a payer pays -- a billing account opened,
// an offer accepted -- is none of this deployment's (D81): the test has its
// levyan say it.

/** Has the test's levyan say something of a workspace (cmd/levyantest). */
async function levyan(request: APIRequestContext, what: string, q: Record<string, string>): Promise<void> {
	const at = test.info().project.metadata.levyan as string
	const res = await request.post(`${at}/e2e/${what}?${new URLSearchParams(q).toString()}`)
	expect(res.status(), await res.text()).toBe(204)
}

/** A workspace of its own, made by whoever `page` is signed in as. */
async function made(page: Page, name: string): Promise<{ id: string; alias: string }> {
	return await makeWorkspace(page, name)
}

/** The roster account whoever `page` is signed in as is, as `GET /account` says. */
async function accountOf(page: Page): Promise<string> {
	const res = await page.request.get('/account')
	expect(res.ok(), await res.text()).toBeTruthy()
	return ((await res.json()) as { id: string }).id
}

test('the owner sees who pays and what was used, and the member they ask pays from the next period once they accept', async ({ page, request }) => {
	await signIn(page, owner.login, owner.password)
	const acme = (await workspacesOf(page)).find((w) => `/@${w.alias}` === workspace)
	expect(acme).toBeDefined()
	await page.goto(`${workspace}/settings?tab=billing`)

	// levyan is told who made the workspace -- who pays for it from then --
	// by the loop that tells it what changed, every ten seconds.
	const payer = card(page, '결제 담당')
	await expect(async () => {
		await page.reload()
		await expect(payer).toContainText('지금 내는 사람', { timeout: 1000 })
	}).toPass({ timeout: 30_000 })
	await expect(payer).toContainText('나 ·')
	const usage = card(page, '이번 기간 사용량과 예상 금액')
	await expect(usage).toContainText('활성 자산')
	await expect(usage).toContainText('구성원')
	await expect(usage).toContainText('예상 합계')
	await expect(card(page, '지난 명세서')).toContainText('닫힌 기간이 아직 없습니다')

	const offer = card(page, '결제 담당 제안')
	await expect(offer).toContainText('제안받은 사람이 받아들이면 이번 기간이 끝난 뒤부터 그 사람이 냅니다.')
	await choose(field(offer, '제안할 구성원'), people.member.name)
	await offer.getByRole('button', { name: '제안', exact: true }).click()
	await said(page, `${people.member.name}님에게 결제 담당을 제안했습니다.`)
	await expect(payer).toContainText(`${people.member.name} ·`)
	// Where they answer it is where a payer pays, and no link of this page's
	// (D81).
	await expect(payer.getByRole('link')).toHaveCount(0)

	// They accept it there.
	await levyan(request, 'accept', { workspace: acme?.id ?? '' })
	await page.reload()
	await expect(card(page, '결제 담당')).toContainText(`다음 기간부터${people.member.name}`)
})

test('a workspace nobody pays for says so above every page, and that it is restricted once its day passed', async ({ page, request }) => {
	await signIn(page, people.workspaces.login)
	const ws = await made(page, unique('e2e 결제 담당 없음'))

	await levyan(request, 'payerless', { workspace: ws.id, from: new Date(Date.now() + 30 * 24 * 3600_000).toISOString() })
	await page.goto(`/@${ws.alias}/`)
	const banner = page.getByRole('alert')
	await expect(banner).toContainText('이 워크스페이스에는 결제 담당이 없습니다.')
	await expect(banner).toContainText('제한되어 자산 등록, 초대, 첨부 올리기처럼')
	await banner.getByRole('link', { name: '결제 담당 정하기 →' }).click()
	await expect(card(page, '결제 담당')).toContainText('결제 담당이 없습니다.')
	await expect(card(page, '결제 담당')).toContainText('제한됩니다.')

	await levyan(request, 'payerless', { workspace: ws.id, from: new Date(Date.now() - 3600_000).toISOString() })
	await page.reload()
	await expect(page.getByRole('alert')).toContainText('제한되었습니다.')
	await expect(card(page, '결제 담당')).toContainText('제한되었습니다.')
	// And from when it is read-only for it, sixty days after its payer left.
	await expect(page.getByRole('alert')).toContainText('부터는 읽기 전용이 되어')
	await expect(card(page, '결제 담당')).toContainText('부터는 읽기 전용이 됩니다.')
})

test('a write a restricted workspace refused is said above every page, to whoever made it', async ({ page, request }) => {
	await signIn(page, people.stock.login)
	const ws = await made(page, unique('e2e 제한'))
	await levyan(request, 'restrict', { workspace: ws.id })

	// levyan's grant reaches this deployment's copy a moment later.
	await expect
		.poll(async () => {
			const res = await page.request.post('/loft.AssetService/Add', {
				headers: { 'connect-protocol-version': '1', 'loft-workspace': ws.alias },
				data: { name: unique('e2e 노트북') },
			})
			return res.status()
		})
		.toBe(400)

	// Every member's page says so as it is drawn, by the copy its writes are
	// held to, and the write is refused all the same.
	await page.goto(`/@${ws.alias}/assets`)
	const banner = page.getByRole('alert')
	await expect(banner).toContainText('이 워크스페이스는 제한되었습니다.')
	await expect(banner).toContainText('청구되는 것을 늘리는 일을 할 수 없습니다')
	await expect(banner.getByRole('link')).toBeVisible()
	await page.getByRole('button', { name: '+ 자산 등록' }).click()
	const add = page.getByRole('dialog')
	await field(add, '이름').fill(unique('e2e 노트북'))
	await add.getByRole('button', { name: '등록', exact: true }).click()
	await refused(page, '제한되어 자산을 새로 등록할 수 없습니다')
})

test('a workspace made read-only says so above every page, and refuses a write as the page makes it', async ({ page, request }) => {
	await signIn(page, people.readOnly.login)
	const ws = await made(page, unique('e2e 읽기 전용'))
	const add = (name: string) =>
		page.request.post('/loft.AssetService/Add', {
			headers: { 'connect-protocol-version': '1', 'loft-workspace': ws.alias },
			data: { name },
		})
	const laptop = unique('e2e 노트북')
	const res = await add(laptop)
	expect(res.ok(), await res.text()).toBeTruthy()
	await levyan(request, 'readonly', { workspace: ws.id })

	// levyan's grant reaches this deployment's copy a moment later.
	await expect.poll(async () => (await add(unique('e2e 의자'))).status()).toBe(400)

	// Every member's page says so as it is drawn, by the copy its writes are
	// held to: that what is the workspace's is changed in nothing, and that
	// it is read, taken out, paid for and left -- and where to go
	// about it. Not that nothing else is changed: what is a person's own is
	// (design 7.13, 7.14).
	await page.goto(`/@${ws.alias}/assets`)
	const banner = page.getByRole('alert')
	await expect(banner).toContainText('이 워크스페이스는 읽기 전용입니다.')
	await expect(banner).toContainText('워크스페이스의 데이터는 바꿀 수 없습니다. 보기, 내보내기, 결제, 떠나기는 할 수 있습니다.')
	await expect(banner.getByRole('link')).toBeVisible()
	// What it has is read as it was.
	await expect(page.getByText(laptop)).toBeVisible()
	await page.getByRole('button', { name: '+ 자산 등록' }).click()
	const dialog = page.getByRole('dialog')
	await field(dialog, '이름').fill(unique('e2e 모니터'))
	await dialog.getByRole('button', { name: '등록', exact: true }).click()
	await refused(page, '이 워크스페이스는 읽기 전용이라 바꿀 수 없습니다')

	// What a person is told stays theirs (design 7.13): marked read, and kept
	// off their mail.
	await page.goto(`/@${ws.alias}/notifications`)
	await page.getByRole('button', { name: '모두 읽음' }).click()
	await said(page, '모두 읽음으로 표시했습니다.')
	await page.goto(`/@${ws.alias}/me`)
	const mail = page.getByRole('checkbox', { name: '알림을 메일로도 받기' })
	await expect(mail).toBeChecked()
	// The box says what the server answered, and turns once it has: clicked,
	// and not unchecked, which asks it to have turned at once.
	await mail.click()
	await said(page, '메일 알림을 껐습니다. 앱에서는 계속 받습니다.')
	await expect(mail).not.toBeChecked()
})

test('an invoice billing a workspace whose charge failed is said above its pages, that it is restricted once its day passed, and read-only once that one did', async ({ page, request }) => {
	await signIn(page, people.counts.login)
	const ws = await made(page, unique('e2e 결제 실패'))

	await levyan(request, 'dunning', { workspace: ws.id, kind: 'temporary', restricted: new Date(Date.now() + 7 * 24 * 3600_000).toISOString() })
	await page.goto(`/@${ws.alias}/`)
	const banner = page.getByRole('alert')
	await expect(banner).toContainText('이 워크스페이스를 청구한 청구서의 결제가 실패했습니다.')
	await expect(banner).toContainText('까지 결제되지 않으면 제한되어')
	// Its owner is sent to its settings, and nowhere outside the workspace
	// (D81).
	await expect(banner.getByRole('link')).toHaveCount(1)
	await banner.getByRole('link', { name: '결제 설정 보기 →' }).click()
	await expect(page).toHaveURL(/\/settings\?tab=billing$/)
	await expect(card(page, '결제 담당')).toContainText('다시 시도합니다.')
	await expect(card(page, '결제 담당')).toContainText('결제 담당이 정리할 수 있습니다.')

	await levyan(request, 'dunning', { workspace: ws.id, kind: 'temporary', restricted: new Date(Date.now() - 3600_000).toISOString() })
	await page.reload()
	await expect(page.getByRole('alert')).toContainText('결제가 밀려 제한되었습니다.')
	// And from when it is read-only, the second stage: sixty days after its
	// charge failed, as levyan says it (`date_readonly`).
	await expect(page.getByRole('alert')).toContainText('부터는 읽기 전용이 되어 워크스페이스의 데이터를 바꿀 수 없게 됩니다.')
	await expect(card(page, '결제 담당')).toContainText('부터는 읽기 전용이 됩니다.')

	// Once that day passed it is said read-only, and since when and why,
	// before levyan's grant reaches the copy.
	await levyan(request, 'dunning', {
		workspace: ws.id,
		kind: 'temporary',
		restricted: new Date(Date.now() - 2 * 3600_000).toISOString(),
		readonly: new Date(Date.now() - 3600_000).toISOString(),
	})
	await page.reload()
	await expect(page.getByRole('alert')).toContainText('부터 읽기 전용입니다.')
	await expect(page.getByRole('alert')).toContainText('결제가 실패한 청구서가 아직 결제되지 않아')
	await expect(card(page, '결제 담당')).toContainText('부터 읽기 전용입니다.')
})

test('a billing account that cannot be collected from yet is said above the workspace it pays for, to whoever runs it, before anything fails', async ({ page, request }) => {
	await signIn(page, people.collects.login)

	// Opened where a payer pays, before the workspace it pays for, which is
	// paid for with it from when it was made.
	await levyan(request, 'account', { account: await accountOf(page), name: '회사용' })
	const ws = await made(page, unique('e2e 결제수단 없음'))

	// levyan is told who made it -- who pays for it, and with what -- by the
	// loop that tells it what changed, every ten seconds.
	await page.goto(`/@${ws.alias}/`)
	const banner = page.getByRole('status').filter({ hasText: '이 워크스페이스를 내는 결제 계정으로 지금 결제할 수 없습니다' })
	await expect(async () => {
		await page.reload()
		await expect(banner).toBeVisible({ timeout: 1000 })
	}).toPass({ timeout: 30_000 })
	await expect(banner).toContainText('결제수단 없음.')
	await expect(banner).toContainText('결제수단을 등록하세요. 이 때문에 제한된 것은 없습니다.')
	await expect(page.getByRole('alert')).toHaveCount(0)

	// The way is to the workspace's billing, and nowhere outside it (D81).
	await expect(banner.getByRole('link')).toHaveCount(1)
	await banner.getByRole('link', { name: '결제 설정 보기 →' }).click()
	await expect(page).toHaveURL(/\/settings\?tab=billing$/)
	const payer = card(page, '결제 담당')
	await expect(payer).toContainText('결제수단 없음')
	await expect(payer).toContainText('결제 담당의 결제 계정: 결제수단을 등록하세요.')

	// A charge the merchant failed and tries again.
	await levyan(request, 'collection', { workspace: ws.id, state: 'past_due' })
	await page.goto(`/@${ws.alias}/`)
	await expect(banner).toContainText('결제 실패, 다시 시도 중.')
	await expect(banner).toContainText('Paddle이 보낸 메일에서 결제수단을 바꿀 수 있습니다.')

	// And once it can be collected, nothing is said above the workspace.
	await levyan(request, 'collection', { workspace: ws.id, state: 'collectable' })
	await page.goto(`/@${ws.alias}/settings?tab=billing`)
	await expect(card(page, '결제 담당')).toContainText('결제 가능')
	await expect(banner).toHaveCount(0)
})

/**
 * The first moment of the month `n` months from this one, in Korea -- where
 * the test's levyan has its periods (internal/levyantest) -- as RFC 3339.
 */
function monthStart(n: number): string {
	const seoul = new Date(Date.now() + 9 * 3600_000)
	return new Date(Date.UTC(seoul.getUTCFullYear(), seoul.getUTCMonth() + n, 1) - 9 * 3600_000).toISOString()
}

/** The last day of each stretch that ends at one of `ends` -- the first moment after it -- as the page says a day, in Korea. */
async function lastDays(page: Page, ...ends: string[]): Promise<string[]> {
	return page.evaluate((ends) => ends.map((t) => new Intl.DateTimeFormat('ko-KR', { dateStyle: 'medium', timeZone: 'Asia/Seoul' }).format(new Date(Date.parse(t) - 1))), ends)
}

test("what levyan's operator gave a workspace is said to whoever runs it, and each line says what it gave and took", async ({ page, request }) => {
	await signIn(page, people.benefits.login)
	const ws = await made(page, unique('e2e 혜택'))

	// What levyan was handed of it -- the deployment measures a workspace it
	// made at midnight -- and what its operator gave it from the period
	// before: 500 more assets each period, a pool of 10,000 asset-months,
	// and 70,000원 of credit.
	const from = monthStart(-1)
	const until = monthStart(6)
	const expires = monthStart(12)
	await levyan(request, 'reading', { workspace: ws.id, meter: 'loft.assets.active', value: '1200' })
	await levyan(request, 'reading', { workspace: ws.id, meter: 'loft.members', value: '1' })
	await levyan(request, 'allowance', { workspace: ws.id, meter: 'loft.assets.active', quantity: '500', from, until })
	await levyan(request, 'pool', { workspace: ws.id, meter: 'loft.assets.active', quantity: '10000', from, expires })
	await levyan(request, 'credit', { workspace: ws.id, amount: '70000', expires })
	// The period before closed, and took of the pool what it was billable
	// for past the 500 included: 700.
	await levyan(request, 'close', { workspace: ws.id })

	await page.goto(`/@${ws.alias}/settings?tab=billing`)
	const [untilDay, expiresDay] = await lastDays(page, until, expires)
	const usage = card(page, '이번 기간 사용량과 예상 금액')
	await expect(usage.getByRole('listitem').filter({ hasText: '프로모션 무료량' })).toHaveText(`프로모션 무료량: 활성 자산 +500개 (${untilDay}까지)`)
	await expect(usage.getByRole('listitem').filter({ hasText: '사용량 크레딧' })).toHaveText(
		`사용량 크레딧: 9,300 자산·월 남음 (10,000 자산·월 중, ${expiresDay}까지). 이번 기간에 700 자산·월을 쓰면 8,600 자산·월이 남습니다.`,
	)
	// Each line: what was used, what was included -- the allowance's -- what
	// the pool took, what is left to price, and what it comes to.
	const line = ['활성 자산', '1,200개', '프로모션 500개', '700 자산·월', '0개', '0원']
	await expect(usage.getByRole('row').filter({ hasText: '활성 자산' }).getByRole('cell')).toHaveText(line)
	await expect(usage.getByRole('row').filter({ hasText: '구성원' }).getByRole('cell')).toHaveText(['구성원', '1명', '0명', '-', '1명', '1,000원'])
	await expect(usage).toContainText('예상 합계 1,000원')
	await expect(usage).toContainText('수량은 소수점 아래 첫째 자리까지 반올림했고')
	// The credit is spent as an invoice is finalized, and not in the estimate.
	await expect(usage).toContainText('크레딧에서 먼저 냅니다. 청구서를 확정할 때 이 워크스페이스에 남은 크레딧 70,000원에서 먼저 빼고, 예상 합계는 그 전의 금액입니다.')
	await expect(card(page, '크레딧').getByRole('listitem')).toHaveText(`70,000원 남음 (70,000원 중, ${expiresDay}까지)`)

	// The period before, as it closed: what the allowance gave and the pool
	// took then.
	await card(page, '지난 명세서').getByRole('button', { name: '보기' }).click()
	const statement = page.getByRole('dialog')
	await expect(statement.getByRole('row').filter({ hasText: '활성 자산' }).getByRole('cell')).toHaveText(line)
	await expect(statement).toContainText('합계 1,000원')
})

/**
 * Holds `page` to the screen it is shown on: nothing on it is past the
 * screen's edge for a person to scroll the page sideways to -- a table wider
 * than the screen slides in a box of its own -- and no dialog open over it is
 * wider than the screen. What sticks out is named when something does.
 */
async function onTheScreen(page: Page): Promise<void> {
	const { screen, wide, past } = await page.evaluate(() => {
		const screen = document.documentElement.clientWidth
		const out = (e: Element) => e.getBoundingClientRect().right > screen
		// What a box that scrolls holds is past nothing while the box is on the screen.
		const held = (e: Element): boolean => {
			for (let a = e.parentElement; a !== null; a = a.parentElement) if (getComputedStyle(a).overflowX !== 'visible') return !out(a)
			return false
		}
		const past = [...document.body.querySelectorAll('*')]
			.filter((e) => out(e) && !held(e) && ![...e.children].some((c) => out(c) && !held(c)))
			.map((e) => `<${e.tagName.toLowerCase()}> ${(e.textContent ?? '').trim().slice(0, 30)}`)
		return { screen, wide: document.documentElement.scrollWidth, past }
	})
	expect(wide, `the page scrolls sideways, past ${past.join(', ')}`).toBeLessThanOrEqual(screen)
	for (const dialog of await page.getByRole('dialog').all()) {
		const box = await dialog.boundingBox()
		expect((box?.x ?? 0) + (box?.width ?? Infinity), `a dialog is wider than the screen, past ${past.join(', ')}`).toBeLessThanOrEqual(screen)
	}
}

test.describe('on a phone', () => {
	// As wide as a phone held upright.
	test.use({ viewport: { width: 390, height: 844 } })

	test("a workspace's billing settings never scroll sideways, nor the offer its owner makes there", async ({ page, request }) => {
		await signIn(page, people.phoneOwner.login)
		const name = unique('e2e 휴대폰 설정')
		const ws = await made(page, name)
		// Nobody pays for it; it was used and given more, and credited, and its
		// period before closed.
		await levyan(request, 'payerless', { workspace: ws.id, from: new Date(Date.now() + 30 * 24 * 3600_000).toISOString() })
		const from = monthStart(-1)
		await levyan(request, 'reading', { workspace: ws.id, meter: 'loft.assets.active', value: '1200' })
		await levyan(request, 'reading', { workspace: ws.id, meter: 'loft.members', value: '1' })
		await levyan(request, 'allowance', { workspace: ws.id, meter: 'loft.assets.active', quantity: '500', from })
		await levyan(request, 'pool', { workspace: ws.id, meter: 'loft.assets.active', quantity: '10000', from })
		await levyan(request, 'credit', { workspace: ws.id, amount: '70000', expires: monthStart(12) })
		await levyan(request, 'close', { workspace: ws.id })

		await page.goto(`/@${ws.alias}/settings?tab=billing`)
		await expect(page.getByRole('alert')).toContainText('이 워크스페이스에는 결제 담당이 없습니다.')
		await expect(card(page, '결제 담당')).toContainText('결제 담당이 없습니다.')
		await expect(card(page, '이번 기간 사용량과 예상 금액').getByRole('row').filter({ hasText: '활성 자산' })).toBeVisible()
		await expect(card(page, '크레딧').getByRole('listitem')).toContainText('70,000원 남음')
		await onTheScreen(page)

		await card(page, '지난 명세서').getByRole('button', { name: '보기' }).click()
		const statement = page.getByRole('dialog')
		await expect(statement.getByRole('row').filter({ hasText: '활성 자산' })).toBeVisible()
		await onTheScreen(page)
		await statement.getByRole('button', { name: '닫기' }).click()

		// Its owner asks themself to pay for it.
		const offer = card(page, '결제 담당 제안')
		await choose(field(offer, '제안할 구성원'), '(나)')
		await offer.getByRole('button', { name: '제안', exact: true }).click()
		await said(page, `${people.phoneOwner.name}님에게 결제 담당을 제안했습니다.`)
		await expect(card(page, '결제 담당')).toContainText('나 ·')
		await onTheScreen(page)
	})
})
