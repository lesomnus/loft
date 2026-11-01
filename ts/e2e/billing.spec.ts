import type { APIRequestContext, Locator, Page } from '@playwright/test'

import { card, choose, expect, field, owner, people, refused, said, signIn, test, unique, workspace } from './loft.js'
import { paddle } from './paddle.js'

// Run against the server that bills (playwright.config.ts): to a levyan of
// the test's own (e2e/levyan.sh, internal/levyantest), with Paddle's default
// payment link served and Paddle.js the test's own (e2e/paddle.ts). Nothing
// reaches levyan's or Paddle's.

/** Has the test's levyan say something of a workspace (cmd/levyantest). */
async function levyan(request: APIRequestContext, what: string, q: Record<string, string>): Promise<void> {
	const at = test.info().project.metadata.levyan as string
	const res = await request.post(`${at}/e2e/${what}?${new URLSearchParams(q).toString()}`)
	expect(res.status(), await res.text()).toBe(204)
}

/** A workspace of its own, made by whoever `page` is signed in as. */
async function made(page: Page, name: string): Promise<{ id: string; alias: string }> {
	const res = await page.request.post('/account/workspaces', { data: { name } })
	expect(res.ok(), await res.text()).toBeTruthy()
	return (await res.json()) as { id: string; alias: string }
}

/** A billing account as levyan lists it, in JSON: no `name` where it has none. */
type Listed = { id: string; name?: string; created: string }

/** The billing accounts of whoever `page` is signed in as, in the order the page lists them. */
async function billingAccounts(page: Page): Promise<Listed[]> {
	const res = await page.request.post('/loft.PaymentService/ListBillingAccounts', { headers: { 'connect-protocol-version': '1' }, data: {} })
	expect(res.ok(), await res.text()).toBeTruthy()
	return ((await res.json()) as { billingAccounts?: Listed[] }).billingAccounts ?? []
}

/**
 * What the billing page calls each of `accounts`: its name, or when it was
 * made -- to the minute, in Korea, where the browser is (playwright.config.ts)
 * and the clocks are nine hours ahead of UTC all year -- and, after either,
 * the end of its identifier where two of them read the same.
 */
function calledAs(accounts: Listed[]): string[] {
	const p = (n: number) => String(n).padStart(2, '0')
	const own = accounts.map((b) => {
		if (b.name !== undefined && b.name !== '') return b.name
		const d = new Date(Date.parse(b.created) + 9 * 3600_000)
		return `결제 계정 (${d.getUTCMonth() + 1}월 ${d.getUTCDate()}일 ${p(d.getUTCHours())}:${p(d.getUTCMinutes())} 만듦)`
	})
	return own.map((v, i) => (own.filter((w) => w === v).length > 1 ? `${v} · #${accounts[i]?.id.slice(-4)}` : v))
}

test('a billing account is made by naming its country, and its checkout gives it a card', async ({ page }) => {
	const p = await paddle(page)
	const went: string[] = []
	page.on('framenavigated', (f) => {
		if (f === page.mainFrame()) went.push(f.url())
	})
	await signIn(page, people.assets.login)

	await page.goto('/billing')
	await expect(page.getByRole('heading', { name: '결제', level: 1 })).toBeVisible()
	await expect(card(page, '결제 계정')).toContainText('아직 결제 계정이 없습니다')
	await page.getByRole('button', { name: '새 결제 계정' }).click()
	const make = page.getByRole('dialog')
	await expect(field(make, '나라')).toHaveValue('KR')
	await make.getByRole('button', { name: '만들기' }).click()
	// The channel and the currency are levyan's to say of the country.
	await said(page, '결제 계정을 만들었습니다: MoR (Paddle), KRW')
	const account = card(page, '결제 계정').getByRole('listitem')
	await expect(account).toContainText('MoR (Paddle)')
	await expect(account).toContainText('KRW')
	await expect(account).toContainText('결제수단 없음')

	// Opened on the page Paddle's checkout opens on, by its transaction, and
	// back here once the payer finished; asked about until levyan has the
	// subscription.
	await account.getByRole('button', { name: '결제수단 등록' }).click()
	await expect(page.getByRole('status')).toHaveText('결제수단을 등록했습니다.')
	await expect(account).toContainText('결제 가능')
	await expect(account).toContainText('카드 visa •••• 4242')
	await expect(account).toContainText('활성')
	await expect(page).toHaveURL(/\/billing$/)

	const opened = went.map((u) => new URL(u)).find((u) => u.pathname === '/billing/checkout')
	expect(opened, went.join('\n')).toBeDefined()
	const transaction = opened?.searchParams.get('transaction') ?? ''
	const checkout = opened?.searchParams.get('checkout') ?? ''
	expect(transaction).toMatch(/^txn_[a-z0-9]+$/)
	expect(went.map((u) => new URL(u)).some((u) => u.pathname === '/billing' && u.searchParams.get('checkout') === checkout)).toBeTruthy()
	await expect
		.poll(() => p.told)
		.toEqual([
			{ environment: 'sandbox' },
			{ token: 'test_000000000000000000000000000' },
			{ open: transaction, successUrl: `${new URL(page.url()).origin}/billing?checkout=${checkout}` },
		])
	expect(p.asked).toEqual([])

	// An invoice of it whose charge failed for a method to replace: said at
	// once, with when it restricts.
	const list = await page.request.post('/loft.PaymentService/ListBillingAccounts', { headers: { 'connect-protocol-version': '1' }, data: {} })
	expect(list.ok(), await list.text()).toBeTruthy()
	const [ba] = ((await list.json()) as { billingAccounts: { id: string }[] }).billingAccounts
	await levyan(page.request, 'dunning', { billing_account: ba?.id ?? '', kind: 'replace', restricted: new Date(Date.now() + 7 * 24 * 3600_000).toISOString() })
	await page.reload()
	await expect(account.getByRole('status')).toContainText('결제가 실패했습니다. 결제수단을 바꿔 주세요.')
	await expect(account.getByRole('status')).toContainText('까지 결제되지 않으면 제한됩니다.')
})

test('the owner sees who pays and what was used, and a member they ask accepts as themselves', async ({ page, as }) => {
	await signIn(page, owner.login, owner.password)
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
	await choose(field(offer, '제안할 구성원'), people.member.name)
	await offer.getByRole('button', { name: '제안', exact: true }).click()
	await said(page, `${people.member.name}님에게 결제 담당을 제안했습니다.`)
	await expect(payer).toContainText(`${people.member.name} ·`)

	const member = await as(people.member.login)
	await member.goto('/billing')
	const offers = card(member, '받은 제안')
	await expect(offers.getByRole('listitem')).toContainText('결제 담당이 되어 달라는 제안을 받았습니다')
	await offers.getByRole('button', { name: '받아들이기' }).click()
	const answer = member.getByRole('dialog')
	await expect(field(answer, '낼 결제 계정')).toHaveValue('')
	await answer.getByRole('button', { name: '받아들이기' }).click()
	await said(member, '제안을 받아들였습니다. 이번 기간이 끝난 뒤부터 냅니다.')
	await expect(card(member, '받은 제안')).toContainText('제안이 없습니다')
	await expect(card(member, '내는 워크스페이스').getByRole('listitem')).toContainText('제안을 받아들여서')

	await page.reload()
	await expect(card(page, '결제 담당')).toContainText(`다음 기간부터${people.member.name}`)
})

test('a member who pays for a workspace is told above its pages that a charge failed, and is sent to their own page', async ({ page, as, request }) => {
	await signIn(page, owner.login, owner.password)
	const mine = await page.request.get('/account')
	expect(mine.ok(), await mine.text()).toBeTruthy()
	const acme = ((await mine.json()) as { workspaces: { id: string; alias: string }[] }).workspaces.find((w) => `/@${w.alias}` === workspace)
	expect(acme).toBeDefined()

	// Asked by the owner, and accepted with a billing account of their own.
	await page.goto(`${workspace}/settings?tab=billing`)
	const offer = card(page, '결제 담당 제안')
	await choose(field(offer, '제안할 구성원'), people.lent.name)
	await offer.getByRole('button', { name: '제안', exact: true }).click()
	await said(page, `${people.lent.name}님에게 결제 담당을 제안했습니다.`)

	const payer = await as(people.lent.login)
	await payer.goto('/billing')
	await payer.getByRole('button', { name: '새 결제 계정' }).click()
	await payer.getByRole('dialog').getByRole('button', { name: '만들기' }).click()
	await said(payer, '결제 계정을 만들었습니다: MoR (Paddle), KRW')
	await card(payer, '받은 제안').getByRole('button', { name: '받아들이기' }).click()
	const answer = payer.getByRole('dialog')
	await expect(field(answer, '낼 결제 계정')).not.toHaveValue('')
	await answer.getByRole('button', { name: '받아들이기' }).click()
	await said(payer, '제안을 받아들였습니다. 이번 기간이 끝난 뒤부터 냅니다.')

	// The period is over, and the first invoice of theirs is not paid: a
	// method to replace, restricting a week from now.
	await levyan(request, 'turn', { workspace: acme?.id ?? '' })
	await levyan(request, 'dunning', { workspace: acme?.id ?? '', kind: 'replace', restricted: new Date(Date.now() + 7 * 24 * 3600_000).toISOString() })

	// A member reads nothing of who pays for the workspace; the payer is told
	// by what is theirs, and goes to settle it.
	await payer.goto(`${workspace}/`)
	const banner = payer.getByRole('alert')
	await expect(banner).toContainText('이 워크스페이스를 내는 결제 계정의 청구서 결제가 실패했습니다.')
	await expect(banner).toContainText('까지 결제되지 않으면 제한되어')
	await banner.getByRole('link', { name: '결제 화면에서 정리하기 →' }).click()
	await expect(payer).toHaveURL(/\/billing$/)
	await expect(card(payer, '결제 계정').getByRole('listitem').getByRole('status')).toContainText('결제수단을 다시 등록하세요.')

	// Its owner is told so of the workspace, and goes to its settings.
	await page.goto(`${workspace}/`)
	await expect(page.getByRole('alert')).toContainText('이 워크스페이스를 청구한 청구서의 결제가 실패했습니다.')
	await expect(page.getByRole('alert').getByRole('link', { name: '결제 설정 보기 →' })).toBeVisible()
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
	// it is read, taken out, paid for, closed and left -- and where to go
	// about it. Not that nothing else is changed: what is a person's own is
	// (design 7.13, 7.14).
	await page.goto(`/@${ws.alias}/assets`)
	const banner = page.getByRole('alert')
	await expect(banner).toContainText('이 워크스페이스는 읽기 전용입니다.')
	await expect(banner).toContainText('워크스페이스의 데이터는 바꿀 수 없습니다. 보기, 내보내기, 결제, 닫기, 떠나기는 할 수 있습니다.')
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
	await page.goto(`/@${ws.alias}/settings?tab=billing`)
	await expect(card(page, '결제 담당')).toContainText('다시 시도합니다.')

	await levyan(request, 'dunning', { workspace: ws.id, kind: 'temporary', restricted: new Date(Date.now() - 3600_000).toISOString() })
	await page.reload()
	await expect(page.getByRole('alert')).toContainText('결제가 밀려 제한되었습니다.')
	// And from when it is read-only, the second stage: sixty days after its
	// charge failed, as levyan says it (`readonly_from`).
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

test('billing accounts with no name are told apart wherever they are offered, and one given a name is offered by it', async ({ page, as }) => {
	// Asked by the owner to pay for the workspace: what a payer chooses one of
	// their billing accounts for.
	await signIn(page, owner.login, owner.password)
	await page.goto(`${workspace}/settings?tab=billing`)
	const offer = card(page, '결제 담당 제안')
	await choose(field(offer, '제안할 구성원'), people.namesAccounts.name)
	await offer.getByRole('button', { name: '제안', exact: true }).click()
	await said(page, `${people.namesAccounts.name}님에게 결제 담당을 제안했습니다.`)

	const payer = await as(people.namesAccounts.login)
	await paddle(payer)
	await payer.goto('/billing')
	const mine = card(payer, '결제 계정')
	const make = async (name: string): Promise<void> => {
		await payer.getByRole('button', { name: '새 결제 계정' }).click()
		const dialog = payer.getByRole('dialog')
		await field(dialog, '이름').fill(name)
		await dialog.getByRole('button', { name: '만들기' }).click()
		await said(payer, '결제 계정을 만들었습니다: MoR (Paddle), KRW')
	}
	// The card called `called`, whose name is `was` -- what the form starts
	// from, and never what the page calls one with none -- renamed `to`.
	const rename = async (called: string, was: string, to: string, ok: string): Promise<void> => {
		await mine.getByRole('listitem', { name: called, exact: true }).getByRole('button', { name: '이름 바꾸기' }).click()
		const dialog = payer.getByRole('dialog')
		await expect(field(dialog, '이름')).toHaveValue(was)
		await field(dialog, '이름').fill(to)
		await dialog.getByRole('button', { name: '바꾸기' }).click()
		await said(payer, ok)
	}
	// What the offer is answered with: the choice of a billing account, as it
	// is offered.
	const accepting = async (): Promise<Locator> => {
		await card(payer, '받은 제안').getByRole('button', { name: '받아들이기' }).click()
		return field(payer.getByRole('dialog'), '낼 결제 계정')
	}

	// Two made with no name are each called by when they were made -- and two
	// made in one minute by the end of their identifiers too -- on their own
	// cards and where they are offered.
	await make('')
	await make('')
	await expect(mine.getByRole('listitem')).toHaveCount(2)
	const [a = '', b = ''] = calledAs(await billingAccounts(payer))
	expect(a).not.toEqual(b)
	for (const v of [a, b]) {
		expect(v).toMatch(/^결제 계정 \(\d+월 \d+일 \d\d:\d\d 만듦\)( · #[0-9a-f]{4})?$/)
		await expect(mine.getByRole('listitem', { name: v, exact: true })).toContainText(v)
	}
	await expect((await accepting()).locator('option')).toHaveText(['나중에 정하기', `${a} · 대한민국 · KRW`, `${b} · 대한민국 · KRW`])
	await payer.getByRole('dialog').getByRole('button', { name: '취소' }).click()

	// One named as it is made, and the second renamed: the first is the only
	// one left with no name, which its minute alone tells apart.
	await make('회사용')
	await rename(b, '', '개인 카드', '이름을 바꿨습니다.')
	const [alone = ''] = calledAs(await billingAccounts(payer))
	expect(alone).toMatch(/^결제 계정 \(\d+월 \d+일 \d\d:\d\d 만듦\)$/)
	await expect(mine.getByRole('listitem')).toHaveCount(3)
	for (const v of [alone, '개인 카드', '회사용']) await expect(mine.getByRole('listitem', { name: v, exact: true })).toContainText(v)

	// How one pays is said where the page knows it.
	const company = mine.getByRole('listitem', { name: '회사용', exact: true })
	await company.getByRole('button', { name: '결제수단 등록' }).click()
	await expect(payer.getByRole('status')).toHaveText('결제수단을 등록했습니다.')
	await expect(company).toContainText('카드 visa •••• 4242')

	const offered = [`${alone} · 대한민국 · KRW`, '개인 카드 · 대한민국 · KRW', '회사용 · 대한민국 · KRW · 카드 visa •••• 4242']
	const choice = await accepting()
	await expect(choice.locator('option')).toHaveText(['나중에 정하기', ...offered])
	await choose(choice, '회사용')
	await payer.getByRole('dialog').getByRole('button', { name: '받아들이기' }).click()
	await said(payer, '제안을 받아들였습니다. 이번 기간이 끝난 뒤부터 냅니다.')
	const paid = card(payer, '내는 워크스페이스').getByRole('listitem')
	await expect(paid).toContainText('회사용 · KRW')
	await paid.getByRole('button', { name: '결제 계정 바꾸기' }).click()
	await expect(field(payer.getByRole('dialog'), '결제 계정').locator('option')).toHaveText(offered)
	await payer.getByRole('dialog').getByRole('button', { name: '취소' }).click()

	// An empty name takes it away, and the account is called by when it was
	// made again.
	await rename('회사용', '회사용', '', '이름을 지웠습니다.')
	const [, , unnamed = ''] = calledAs(await billingAccounts(payer))
	expect(unnamed).toMatch(/^결제 계정 \(\d+월 \d+일 \d\d:\d\d 만듦\)( · #[0-9a-f]{4})?$/)
	await expect(mine.getByRole('listitem', { name: unnamed, exact: true })).toContainText('카드 visa •••• 4242')
	await expect(paid).toContainText(`${unnamed} · KRW`)
})

/** A billing account made on the billing page by whoever `page` is signed in as, called `name` or nothing. */
async function makeAccount(page: Page, name: string): Promise<void> {
	await page.getByRole('button', { name: '새 결제 계정' }).click()
	const dialog = page.getByRole('dialog')
	await field(dialog, '이름').fill(name)
	await dialog.getByRole('button', { name: '만들기' }).click()
	await said(page, '결제 계정을 만들었습니다: MoR (Paddle), KRW')
}

test.describe('in a browser far from the deployment', () => {
	// The billing server names no zone of its own (e2e/serve.sh), and is in
	// Asia/Seoul as a deployment that names none is; the browser is not.
	test.use({ timezoneId: 'America/Los_Angeles' })

	test("the billing page draws its dates on the deployment's wall, and never on the browser's", async ({ page }) => {
		await signIn(page, people.zone.login)
		expect(await page.evaluate(() => Intl.DateTimeFormat().resolvedOptions().timeZone)).toBe('America/Los_Angeles')
		const me = await page.request.get('/account')
		expect(me.ok(), await me.text()).toBeTruthy()
		expect(((await me.json()) as { timezone?: string }).timezone).toBe('Asia/Seoul')

		// One with no name is called by when it was made, to the minute, as
		// the clock in Seoul says it (`calledAs`): some sixteen hours from
		// what the browser's says.
		await page.goto('/billing')
		await makeAccount(page, '')
		const mine = card(page, '결제 계정')
		const [listed] = await billingAccounts(page)
		const [called = ''] = calledAs(listed === undefined ? [] : [listed])
		expect(called).toMatch(/^결제 계정 \(\d+월 \d+일 \d\d:\d\d 만듦\)$/)
		await expect(mine.getByRole('listitem', { name: called, exact: true })).toContainText(called)

		// And one with a name says the day it was made, Seoul's.
		await mine.getByRole('listitem', { name: called, exact: true }).getByRole('button', { name: '이름 바꾸기' }).click()
		await field(page.getByRole('dialog'), '이름').fill('개인 카드')
		await page.getByRole('dialog').getByRole('button', { name: '바꾸기' }).click()
		await said(page, '이름을 바꿨습니다.')
		const day = await page.evaluate((t) => new Intl.DateTimeFormat('ko-KR', { dateStyle: 'medium', timeZone: 'Asia/Seoul' }).format(new Date(t)), listed?.created ?? '')
		await expect(mine.getByRole('listitem', { name: '개인 카드', exact: true })).toContainText(`${day}에 만듦`)
	})
})

test('a billing account that cannot be collected from yet is said before anything fails: on its payer\'s page, and above the workspace it pays for to whoever runs it', async ({ page, request }) => {
	await paddle(page)
	await signIn(page, people.collects.login)

	// Made before the workspace it pays for, which is paid for with it from
	// when it was made.
	await page.goto('/billing')
	await makeAccount(page, '회사용')
	const stuck = card(page, '결제할 수 없는 결제 계정')
	const row = stuck.getByRole('listitem', { name: '회사용', exact: true })
	await expect(row).toContainText('결제수단 없음')
	await expect(row).toContainText('결제수단을 등록하세요.')
	await expect(stuck).toContainText('이 때문에 제한된 것은 없습니다.')
	const name = unique('e2e 결제수단 없음')
	const ws = await made(page, name)

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

	// The payer is sent to their own page, where the state's action is.
	await banner.getByRole('link', { name: '결제 화면에서 결제수단 등록하기 →' }).click()
	await expect(page).toHaveURL(/\/billing$/)
	await expect(row).toContainText(`이 결제 계정으로 내는 워크스페이스: ${name}.`)
	await row.getByRole('button', { name: '결제수단 등록' }).click()
	await expect(page.getByRole('status')).toHaveText('결제수단을 등록했습니다.')
	await expect(card(page, '결제 계정').getByRole('listitem', { name: '회사용', exact: true })).toContainText('결제 가능')
	await expect(stuck).toHaveCount(0)
	await page.goto(`/@${ws.alias}/settings?tab=billing`)
	await expect(card(page, '결제 담당')).toContainText('결제 가능')
	await expect(banner).toHaveCount(0)

	// A charge the merchant failed and tries again: no checkout to begin,
	// but where its card says what Paddle asks.
	const [ba] = await billingAccounts(page)
	await levyan(request, 'collection', { billing_account: ba?.id ?? '', state: 'past_due' })
	await page.goto('/billing')
	await expect(row).toContainText('결제 실패, 다시 시도 중')
	await expect(row).toContainText('Paddle이 보낸 메일에서 결제수단을 바꿀 수 있습니다.')
	await expect(row.getByRole('button', { name: '결제수단 등록' })).toHaveCount(0)
	await row.getByRole('link', { name: '결제 계정 보기' }).click()
	await expect(page).toHaveURL(new RegExp(`/billing#billing-account-${ba?.id ?? ''}$`))
	await page.goto(`/@${ws.alias}/`)
	await expect(banner).toContainText('결제 실패, 다시 시도 중.')
	await expect(banner.getByRole('link', { name: '결제 화면에서 보기 →' })).toBeVisible()
})

test('who a billing account bills is said as it is made and again after, and the business of one paid by transfer is its contract\'s', async ({ page, request }) => {
	await signIn(page, people.billedTo.login)
	await page.goto('/billing')

	// Made up, every word of it.
	await page.getByRole('button', { name: '새 결제 계정' }).click()
	const make = page.getByRole('dialog')
	await field(make, '이름').fill('세금계산서용')
	await field(make, '받는 사람 이름').fill(people.billedTo.name)
	await field(make, '받는 사람 이메일').fill('billing@example.com')
	await field(make, '받는 사람 주소').fill('서울시 중구 예시로 1')
	await expect(field(make, '청구 대상')).toHaveValue('person')
	await expect(field(make, '사업자등록번호')).toHaveCount(0)
	await choose(field(make, '청구 대상'), '사업자')
	await field(make, '사업자등록번호').fill('000-12-34567')
	await field(make, '상호').fill('(주)예시상사')
	await field(make, '대표자').fill(people.billedTo.name)
	await field(make, '세금계산서 이메일').fill('tax@example.com')
	await field(make, '사업장 주소').fill('서울시 중구 예시로 1')
	await field(make, '업태').fill('서비스')
	await field(make, '종목').fill('소프트웨어')
	await make.getByRole('button', { name: '만들기' }).click()
	await said(page, '결제 계정을 만들었습니다: MoR (Paddle), KRW')
	const mine = card(page, '결제 계정').getByRole('listitem', { name: '세금계산서용', exact: true })
	await expect(mine).toContainText(`${people.billedTo.name} · billing@example.com · 서울시 중구 예시로 1`)
	await expect(mine).toContainText('(주)예시상사 · 000-12-34567')

	// Said again, from what levyan says of it now.
	await mine.getByRole('button', { name: '청구 정보 바꾸기' }).click()
	const edit = page.getByRole('dialog')
	await expect(field(edit, '받는 사람 이메일')).toHaveValue('billing@example.com')
	await expect(field(edit, '청구 대상')).toHaveValue('business')
	await expect(field(edit, '사업자등록번호')).toHaveValue('000-12-34567')
	await expect(field(edit, '종목')).toHaveValue('소프트웨어')
	await field(edit, '받는 사람 이메일').fill('pay@example.com')
	await field(edit, '받는 사람 주소').fill('')
	await field(edit, '상호').fill('(주)예시')
	await edit.getByRole('button', { name: '저장' }).click()
	await said(page, '청구 정보를 저장했습니다.')
	await expect(mine).toContainText(`받는 사람${people.billedTo.name} · pay@example.com`)
	await expect(mine).not.toContainText('예시로 1')
	await expect(mine).toContainText('(주)예시 · 000-12-34567')

	// Billed as a person from now: what was said of the business goes.
	await mine.getByRole('button', { name: '청구 정보 바꾸기' }).click()
	await choose(field(page.getByRole('dialog'), '청구 대상'), '개인')
	await expect(field(page.getByRole('dialog'), '사업자등록번호')).toHaveCount(0)
	await page.getByRole('dialog').getByRole('button', { name: '저장' }).click()
	await said(page, '청구 정보를 저장했습니다.')
	await expect(mine).not.toContainText('000-12-34567')
	await mine.getByRole('button', { name: '청구 정보 바꾸기' }).click()
	await expect(field(page.getByRole('dialog'), '청구 대상')).toHaveValue('person')
	await page.getByRole('dialog').getByRole('button', { name: '취소' }).click()

	// One paid by transfer, opened by levyan's operator after a contract: its
	// business is the contract's, said and not offered to change.
	const me = await page.request.get('/account')
	expect(me.ok(), await me.text()).toBeTruthy()
	const { id } = (await me.json()) as { id: string }
	await levyan(request, 'bank', { account: id, name: '계약', business: '(주)계약상사', registration: '000-98-76543', email: 'contract@example.com' })
	await page.reload()
	const bank = card(page, '결제 계정').getByRole('listitem', { name: '계약', exact: true })
	await expect(bank).toContainText('계좌이체')
	await expect(bank).toContainText('(주)계약상사 · 000-98-76543')
	await expect(bank.getByRole('button', { name: '결제수단 등록' })).toHaveCount(0)
	await bank.getByRole('button', { name: '청구 정보 바꾸기' }).click()
	const fixed = page.getByRole('dialog')
	await expect(fixed).toContainText('계좌이체로 내는 결제 계정의 사업자 정보는 계약에 따른 것이라 운영자가 바꿉니다.')
	await expect(fixed).toContainText('000-98-76543')
	await expect(field(fixed, '청구 대상')).toHaveCount(0)
	await expect(field(fixed, '사업자등록번호')).toHaveCount(0)
	await field(fixed, '받는 사람 이름').fill(people.billedTo.name)
	await fixed.getByRole('button', { name: '저장' }).click()
	await said(page, '청구 정보를 저장했습니다.')
	await expect(bank).toContainText(`받는 사람${people.billedTo.name}`)
	await expect(bank).toContainText('(주)계약상사 · 000-98-76543')
})

test('an owner takes on paying for their workspace in one go, from the end of the period', async ({ page, request }) => {
	await signIn(page, people.takesOver.login)
	await page.goto('/billing')
	await makeAccount(page, '개인 카드')
	await makeAccount(page, '회사용')

	// Nobody pays for it, as once its payer left.
	const name = unique('e2e 내가 결제 담당')
	const ws = await made(page, name)
	await levyan(request, 'payerless', { workspace: ws.id, from: new Date(Date.now() + 30 * 24 * 3600_000).toISOString() })
	await page.goto(`/@${ws.alias}/settings?tab=billing`)
	const payer = card(page, '결제 담당')
	await expect(payer).toContainText('결제 담당이 없습니다.')
	await expect(page.getByRole('alert')).toContainText('이 워크스페이스에는 결제 담당이 없습니다.')
	await expect(card(page, '결제 담당 제안')).toContainText('내가 내려면 위의 "내가 결제 담당 맡기"로 바로 맡습니다.')

	await payer.getByRole('button', { name: '내가 결제 담당 맡기' }).click()
	const take = page.getByRole('dialog')
	await expect(take).toContainText('이번 기간이 끝난 뒤부터 내가 이 워크스페이스를 냅니다.')
	const choice = field(take, '낼 결제 계정')
	await expect(choice.locator('option')).toHaveText(['나중에 정하기', '개인 카드 · 대한민국 · KRW', '회사용 · 대한민국 · KRW'])
	await choose(choice, '회사용')
	await take.getByRole('button', { name: '맡기' }).click()
	await said(page, '결제 담당을 맡았습니다. 이번 기간이 끝난 뒤부터 냅니다.')
	await expect(payer).toContainText('다음 기간부터나')
	await expect(payer.getByRole('button', { name: '내가 결제 담당 맡기' })).toHaveCount(0)
	await expect(page.getByRole('alert')).toHaveCount(0)

	// An offer to themself, accepted: so their own page says.
	await page.goto('/billing')
	await expect(card(page, '받은 제안')).toContainText('제안이 없습니다')
	const paid = card(page, '내는 워크스페이스').getByRole('listitem').filter({ hasText: name })
	await expect(paid).toContainText('회사용 · KRW')
	await expect(paid).toContainText('제안을 받아들여서')
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

test("a payer's page says what each billing account may be paid with before its method, and what of an invoice the credit paid", async ({ page, request }) => {
	await signIn(page, people.credits.login)
	await page.goto('/billing')
	await makeAccount(page, '프로모션 계정')
	const name = unique('e2e 크레딧')
	const ws = await made(page, name)
	const [ba] = await billingAccounts(page)
	expect(ba).toBeDefined()

	// The workspace's credit, which expires within the month; the billing
	// account's own, later; and what it is owed beside them.
	const soon = new Date(Date.now() + 10 * 24 * 3600_000).toISOString()
	const later = monthStart(12)
	await levyan(request, 'credit', { workspace: ws.id, amount: '70000', expires: soon })
	await levyan(request, 'credit', { billing_account: ba?.id ?? '', amount: '30000', expires: later })
	await levyan(request, 'balance', { billing_account: ba?.id ?? '', amount: '5000' })

	// levyan is told who made the workspace -- who pays for it from then,
	// with the billing account they made -- by the loop that tells it what
	// changed, every ten seconds: the workspace's credit is then the billing
	// account's to be paid with.
	const row = card(page, '결제 계정').getByRole('listitem', { name: '프로모션 계정', exact: true })
	const theirs = row.getByRole('listitem').filter({ hasText: `${name}의 크레딧` })
	await expect(async () => {
		await page.reload()
		await expect(theirs).toBeVisible({ timeout: 1000 })
	}).toPass({ timeout: 30_000 })
	const [soonDay, laterDay] = await lastDays(page, soon, later)
	await expect(theirs).toContainText(`70,000원 남음 (70,000원 중, ${soonDay}까지)`)
	await expect(theirs).toContainText('곧 만료')
	const own = row.getByRole('listitem').filter({ hasText: '이 결제 계정의 크레딧' })
	await expect(own).toContainText(`30,000원 남음 (30,000원 중, ${laterDay}까지)`)
	await expect(own).not.toContainText('곧 만료')
	await expect(row).toContainText('잔액5,000원 · 만료 없음')
	await expect(row).toContainText(`크레딧 70,000원이 ${soonDay}까지라 곧 만료됩니다.`)

	// An invoice of the period before, billing the workspace 80,000원: the
	// credit that expires sooner paid what it could, the billing account's
	// the rest, and nothing was left due.
	await levyan(request, 'invoice', { billing_account: ba?.id ?? '', workspace: ws.id, label: name, total: '80000' })
	await page.reload()
	const invoice = card(page, '청구서').getByRole('row').filter({ hasText: '80,000원' })
	await expect(invoice).toContainText('결제됨')
	await expect(invoice).toContainText('크레딧 −80,000원')
	await invoice.getByRole('button', { name: '보기' }).click()
	const dialog = page.getByRole('dialog')
	await expect(dialog).toContainText(`크레딧−70,000원 · ${name}의 크레딧`)
	await expect(dialog).toContainText('크레딧−10,000원 · 결제 계정의 크레딧')
	await expect(dialog).toContainText('청구한 금액0원')
	await dialog.getByRole('button', { name: '닫기' }).click()

	// What is left of it: the workspace's spent, 20,000원 of the billing
	// account's, and the balance, which is spent after them.
	await expect(own).toContainText(`20,000원 남음 (30,000원 중, ${laterDay}까지)`)
	await expect(theirs).toHaveCount(0)
	await expect(row).toContainText('잔액5,000원 · 만료 없음')
})

test("each line of a payer's invoice says what its period came to, meter by meter, as levyan answers the payer", async ({ page, request }) => {
	await signIn(page, people.statements.login)
	await page.goto('/billing')
	await makeAccount(page, '명세서 계정')
	const name = unique('e2e 명세서')
	const ws = await made(page, name)
	const [ba] = await billingAccounts(page)
	expect(ba).toBeDefined()

	// levyan is told who made the workspace -- who pays for it from then,
	// with the billing account they made -- by the loop that tells it what
	// changed, every ten seconds.
	const links = card(page, '내는 워크스페이스')
	await expect(async () => {
		await page.reload()
		await expect(links).toContainText(name, { timeout: 1000 })
	}).toPass({ timeout: 30_000 })

	// What levyan was handed of it, and what its operator gave it from the
	// period before: 500 more assets each period, and a pool of 10,000
	// asset-months. The period before closed -- paid for with the billing
	// account, whose payer's its statement is (levyan#47) -- and was
	// invoiced: 1,000원, of the member.
	const from = monthStart(-1)
	await levyan(request, 'reading', { workspace: ws.id, meter: 'loft.assets.active', value: '1200' })
	await levyan(request, 'reading', { workspace: ws.id, meter: 'loft.members', value: '1' })
	await levyan(request, 'allowance', { workspace: ws.id, meter: 'loft.assets.active', quantity: '500', from })
	await levyan(request, 'pool', { workspace: ws.id, meter: 'loft.assets.active', quantity: '10000', from })
	await levyan(request, 'close', { workspace: ws.id })
	await levyan(request, 'invoice', { billing_account: ba?.id ?? '', workspace: ws.id, label: name })

	await page.reload()
	await card(page, '청구서').getByRole('row').filter({ hasText: '1,000원' }).getByRole('button', { name: '보기' }).click()
	const dialog = page.getByRole('dialog')
	// Under the workspace and its period, on the deployment's wall; each
	// line what was used, what was free -- the price's and the promotion's
	// -- what the pool took, what is left to price, and what it comes to.
	const [first, last] = await lastDays(page, new Date(Date.parse(from) + 1).toISOString(), monthStart(0))
	const statement = dialog.getByRole('region', { name: `${name} · ${first} – ${last} 명세서` })
	await expect(statement.getByRole('row').filter({ hasText: '활성 자산' }).getByRole('cell')).toHaveText(['활성 자산', '1,200개', '프로모션 500개', '700 자산·월', '0개', '0원'])
	await expect(statement.getByRole('row').filter({ hasText: '구성원' }).getByRole('cell')).toHaveText(['구성원', '1명', '0명', '-', '1명', '1,000원'])
	await expect(dialog).toContainText('수량은 소수점 아래 첫째 자리까지 반올림했고')

	// On a phone the dialog stays on the screen, and the lines scroll
	// sideways in it.
	await page.setViewportSize({ width: 390, height: 844 })
	await expect.poll(async () => (await dialog.boundingBox())?.width ?? Infinity).toBeLessThanOrEqual(390)
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

	test("a payer's page never scrolls sideways: their billing accounts and what can be done with each, the credit they hold, the workspaces they pay for, and their invoices with each line's statement", async ({ page, request }) => {
		await signIn(page, people.phonePayer.login)
		await page.goto('/billing')
		await expect(card(page, '결제 계정')).toContainText('아직 결제 계정이 없습니다')
		await onTheScreen(page)

		// One that pays with nothing yet: three things to do with it, and the
		// card of those that cannot be collected from above the rest.
		await makeAccount(page, '회사 법인카드')
		const row = card(page, '결제 계정').getByRole('listitem', { name: '회사 법인카드', exact: true })
		for (const name of ['결제수단 등록', '이름 바꾸기', '청구 정보 바꾸기']) await expect(row.getByRole('button', { name })).toBeVisible()
		await expect(card(page, '결제할 수 없는 결제 계정').getByRole('button', { name: '결제수단 등록' })).toBeVisible()
		await onTheScreen(page)

		// A workspace paid for with it -- levyan is told who made it by the
		// loop that tells it what changed, every ten seconds -- and what
		// levyan's operator gave, of the workspace's and the billing account's
		// own; its period before closed, and was invoiced and paid with that
		// credit; and a charge that failed.
		const name = unique('e2e 휴대폰')
		const ws = await made(page, name)
		const paid = card(page, '내는 워크스페이스').getByRole('listitem').filter({ hasText: name })
		await expect(async () => {
			await page.reload()
			await expect(paid.getByRole('button', { name: '결제 계정 바꾸기' })).toBeVisible({ timeout: 1000 })
		}).toPass({ timeout: 30_000 })
		const [ba] = await billingAccounts(page)
		const from = monthStart(-1)
		await levyan(request, 'credit', { workspace: ws.id, amount: '70000', expires: new Date(Date.now() + 10 * 24 * 3600_000).toISOString() })
		await levyan(request, 'credit', { billing_account: ba?.id ?? '', amount: '30000', expires: monthStart(12) })
		await levyan(request, 'balance', { billing_account: ba?.id ?? '', amount: '5000' })
		await levyan(request, 'reading', { workspace: ws.id, meter: 'loft.assets.active', value: '1200' })
		await levyan(request, 'reading', { workspace: ws.id, meter: 'loft.members', value: '1' })
		await levyan(request, 'allowance', { workspace: ws.id, meter: 'loft.assets.active', quantity: '500', from })
		await levyan(request, 'pool', { workspace: ws.id, meter: 'loft.assets.active', quantity: '10000', from })
		await levyan(request, 'close', { workspace: ws.id })
		await levyan(request, 'invoice', { billing_account: ba?.id ?? '', workspace: ws.id, label: name })
		await levyan(request, 'dunning', { billing_account: ba?.id ?? '', kind: 'temporary', restricted: new Date(Date.now() + 7 * 24 * 3600_000).toISOString() })
		await page.reload()
		await expect(row.getByRole('listitem').filter({ hasText: `${name}의 크레딧` })).toContainText('곧 만료')
		await expect(row.getByRole('status')).toContainText('결제가 실패했습니다.')
		await expect(paid.getByRole('button', { name: '그만 내기' })).toBeVisible()
		const invoice = card(page, '청구서').getByRole('row').filter({ hasText: '1,000원' })
		await expect(invoice).toContainText('결제됨')
		await onTheScreen(page)

		// The invoice, open: each line's statement slides sideways in the
		// dialog, which stays on the screen.
		await invoice.getByRole('button', { name: '보기' }).click()
		const dialog = page.getByRole('dialog')
		await expect(dialog.getByRole('region').getByRole('row').filter({ hasText: '활성 자산' })).toBeVisible()
		await onTheScreen(page)
	})

	test("a workspace's billing settings never scroll sideways, nor its owner's own page with the offer they made there", async ({ page, request }) => {
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

		// Its owner asks themself to pay for it, and answers on their own page.
		const offer = card(page, '결제 담당 제안')
		await choose(field(offer, '제안할 구성원'), '(나)')
		await offer.getByRole('button', { name: '제안', exact: true }).click()
		await said(page, `${people.phoneOwner.name}님에게 결제 담당을 제안했습니다.`)
		await onTheScreen(page)
		await page.goto('/billing')
		const offered = card(page, '받은 제안').getByRole('listitem').filter({ hasText: name })
		await expect(offered.getByRole('button', { name: '받아들이기' })).toBeVisible()
		await expect(offered.getByRole('button', { name: '거절' })).toBeVisible()
		await onTheScreen(page)
	})
})
