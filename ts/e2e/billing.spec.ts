import type { APIRequestContext, Page } from '@playwright/test'

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

test('an invoice billing a workspace whose charge failed is said above its pages, and that it is restricted once its day passed', async ({ page, request }) => {
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
})
