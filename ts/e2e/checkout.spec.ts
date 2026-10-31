import { expect, test } from './loft.js'
import { paddle } from './paddle.js'

// Run against the server with a checkout (`app.billing.checkout`,
// playwright.config.ts): Paddle's default payment link, `/billing/checkout`,
// opened by nobody signed in, with Paddle.js the test's own (e2e/paddle.ts).

const token = 'test_000000000000000000000000000'

test('the default payment link opens the checkout its address names, in the sandbox, with its token', async ({ page }) => {
	const p = await paddle(page)
	await page.goto('/billing/checkout?_ptxn=txn_01h2b0qpjc0xt8k5aw6nsdec4p')

	await expect(page.getByRole('heading', { name: '결제' })).toBeVisible()
	await expect(page.getByRole('status')).toHaveText('결제를 마쳤습니다.')
	await expect.poll(() => p.told).toEqual([{ environment: 'sandbox' }, { token }, { open: 'txn_01h2b0qpjc0xt8k5aw6nsdec4p', successUrl: '' }])
	expect(p.asked).toEqual([])
})

test('with nothing to pay it says so, opens nothing, and links home', async ({ page }) => {
	const p = await paddle(page)
	await page.goto('/billing/checkout')

	await expect(page.getByRole('status')).toHaveText('여기서 결제할 것이 없습니다. 결제할 것이 있으면 받은 결제 링크로 여세요.')
	await expect.poll(() => p.told).toEqual([{ environment: 'sandbox' }, { token }])
	expect(p.asked).toEqual([])

	await page.getByRole('link', { name: '처음으로' }).click()
	await expect(page.getByLabel('아이디 또는 이메일')).toBeVisible()
})

test('a page that could not load Paddle.js says so', async ({ page }) => {
	await page.route('https://cdn.paddle.com/**', (route) => route.abort('connectionrefused'))
	await page.goto('/billing/checkout?_ptxn=txn_01h2b0qpjc0xt8k5aw6nsdec4p')
	await expect(page.getByRole('status')).toHaveText('결제창을 불러오지 못했습니다. 잠시 뒤에 이 페이지를 다시 여세요.')
})
